package api

import (
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"github.com/prokopto-dev/dragonkillparty/internal/api/middleware"
	"github.com/prokopto-dev/dragonkillparty/internal/auth"
	"github.com/prokopto-dev/dragonkillparty/internal/clock"
	"github.com/prokopto-dev/dragonkillparty/internal/guild"
	"github.com/prokopto-dev/dragonkillparty/internal/store"
)

const (
	// BasePath is the versioned API prefix. Canonical §7: within v1 the surface is additive only,
	// and a breaking change mints /api/v2 rather than editing this.
	BasePath = "/api/v1"

	// SpecVersion is the `info.version` of the OpenAPI document.
	//
	// It is the version of the API CONTRACT, not of the binary, and the distinction is load-bearing.
	// openapi/openapi.json is committed and diff-gated: if this were the ldflags-stamped binary
	// version, every release build would regenerate a different document and `make verify-generated`
	// would report drift on a tree nobody edited. The binary's version, commit and build date are
	// runtime facts and belong in the GET /api/v1/meta response, which is where they are.
	SpecVersion = "1.0.0"

	// specTitle is `info.title`. It reaches users as the name of the generated SDK packages and the
	// heading of the reference page, so it is prose rather than an identifier.
	specTitle = "Dragon Kill Party"
)

// Config is everything the handler tree needs from the process around it.
//
// Every field is a RUNTIME value. None of them may influence the OpenAPI document, because
// `dkp openapi` has to emit the same bytes on a laptop and in a release build or the drift gate
// fires on an untouched tree. TestOpenAPI_ConfigVariation_ProducesIdenticalSpec holds that line.
type Config struct {
	// Version, Commit and BuildDate are the ldflags stamps from cmd/dkp. They are reported by
	// GET /api/v1/meta so a bot can tell which build it is talking to.
	Version   string
	Commit    string
	BuildDate string

	// Clock is injected because time.Now is grep-banned outside internal/clock (gate CLOCK001).
	// A nil Clock falls back to clock.System — New is called from tests that do not care about
	// time, and making every one of them construct a clock would be ceremony without a reader.
	Clock clock.Clock

	// Readiness backs GET /readyz. A nil Readiness means the route is not registered at all,
	// preserving the split PR 3 introduced: /healthz must never touch the database, /readyz must,
	// and the two are wired from different places so that dependency is visible in the call site.
	//
	// A non-nil interface holding a NIL implementation is a different case and is not silently
	// tolerated: the route registers and answers 503 with `no readiness checker configured` rather
	// than calling Ready() on a nil receiver (#75, checkerUnwired). A wiring bug should sound like a
	// wiring bug, not like a panic.
	Readiness ReadyChecker

	// ReadyDetail is who may see a /readyz `detail`, read from DKP_READYZ_DETAIL in cmd/dkp.
	//
	// The zero value is ReadyDetailNever, so a Config that does not mention this field — every test
	// that predates #74, and any future embedder — discloses nothing. That default is the fix: the
	// peer address cannot be a trust signal behind the reverse proxy this project recommends, so
	// disclosure waits for the operator to say so. See ReadyDetailPolicy.
	ReadyDetail ReadyDetailPolicy

	// APIBase is the value GET /config.json reports as API_BASE, read from DKP_API_BASE in cmd/dkp.
	// Empty (the default) means same-origin, which is what a co-hosted binary serves. It is a
	// RUNTIME value like the build stamps: it must not influence the OpenAPI document, and it does
	// not — /config.json is not an operation, so `dkp openapi` never sees it.
	APIBase string

	// WebUI, when non-nil, is the embedded SPA handler mounted as the catch-all for every non-/api,
	// non-infrastructure path. A nil WebUI means the binary serves the API and the docs but no SPA —
	// the shape every test that predates PR 6 constructs, which is why this is a field rather than an
	// unconditional mount. cmd/dkp passes internal/ui.Handler(); a handler-level test passes nil.
	WebUI http.Handler

	// Auth resolves the credential on every request into the single Principal a handler sees
	// (docs/design/03-security.md §5). cmd/dkp builds one from the store, the clock and the keyring
	// it loaded from <data-dir>/secrets.json.
	//
	// A NIL Auth DOES NOT MEAN "NO AUTHENTICATION". It means no operation that declares `Security`
	// can be served at all: those answer 503, and only operations that declare an explicitly empty
	// `Security` still work. The alternative — skipping the middleware — is a wiring bug that turns
	// every gate in the product off silently and passes every test, which is exactly the failure this
	// field exists to prevent. `dkp openapi` and the architectural tests pass nil deliberately: they
	// build the registry without serving a request.
	Auth *auth.Service

	// Authorization is what the boot path learned about this instance's ability to authorize a
	// request: did cmd/dkp's reconcileOnBoot project the permission catalogue into the database
	// before the listener opened?
	//
	// THE ZERO VALUE REFUSES every operation that requires a permission (issue #272). A Config that
	// does not mention this field describes a process that never reconciled anything, so the
	// operations that need a permission answer 503 while /healthz, /readyz, /config.json, the docs
	// and the public operations stay up. Serving them anyway would be an authorization choke point
	// whose unprepared state is "allow", which is the one way authorization is not allowed to fail.
	// See AuthorizationState.
	//
	// IT IS A DIFFERENT QUESTION FROM Auth, and the two are deliberately separate fields. Auth
	// answers "who is asking"; this answers "is this instance in a state to decide what they may
	// do". A process can have a perfectly good credential resolver and an unreconciled catalogue —
	// that is precisely the boot #272 fails closed on — and collapsing them into one flag would make
	// a database that came up late look like a missing keyring in the logs.
	Authorization AuthorizationState

	// Store backs the query-backed operations — PR 5's GET and PATCH /api/v1/guild are the first.
	// It is nil when New is called only to build the spec (NewHumaAPI): the operations still register
	// so they appear in the document, and their handlers are never invoked in that path. A nil Store
	// reaching a handler at runtime is a wiring bug, not an input to guard, so the service is built
	// from a nil Store here and the arch tests exercise the spec with it absent.
	Store *store.Store
}

// New builds the complete HTTP handler tree: routes, Huma mount, and middleware.
//
// It replaces PR 1's NewMux and PR 3's NewMuxWithReadiness, which internal/api/health.go:12-17
// explicitly handed to this PR to revise. They were a no-argument constructor and a
// one-argument constructor over the same router; adding the build stamps, the clock and (in PR 5)
// a store would have made a third and a fourth, each differing from its neighbour by one field.
// One Config-taking constructor is the shape that stops growing. cmd/dkp/serve.go and
// internal/api/health_test.go are the two call sites.
//
// The return type is http.Handler rather than *http.ServeMux, and that is the substantive part of
// the change. The request-id and problem middleware wrap the router, so what a caller mounts is no
// longer the router itself — and a concrete *http.ServeMux return type would either leak the
// unwrapped router (letting a caller serve it and silently lose every middleware) or force the
// middleware to be applied in cmd/, which is where routes are forbidden to be known about.
//
// Order matters, outermost first:
//
//  1. RequestID — so the id exists before anything can log or fail, including /healthz and /readyz,
//     which are raw handlers Huma never sees.
//  2. Problem — so an unrouted path, a method mismatch or a panic still answers problem+json.
//  3. the router.
func New(cfg Config) http.Handler {
	clk := cfg.Clock
	if clk == nil {
		clk = clock.System{}
	}

	mux := http.NewServeMux()

	// Infrastructure routes, deliberately NOT Huma operations and deliberately outside /api/v1.
	// See handleHealthz for the four reasons; the short version is that /readyz answers 503 with a
	// body that is not problem+json by design, and registering it with Huma would force a choice
	// between breaking that wire contract and breaking "every error is RFC 9457".
	mux.HandleFunc("GET /healthz", handleHealthz)

	// `!= nil` and not checkerUnwired: an untyped-nil Readiness means "this binary does not do
	// readiness" and gets no route, while an interface holding a typed nil is a wiring bug that must
	// be audible — it registers, and handleReadyz reports it as unwired (#75).
	if cfg.Readiness != nil {
		mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
			handleReadyz(w, r, cfg.Readiness, cfg.ReadyDetail)
		})
	}

	// The SPA's runtime configuration. Mounted before the Huma tree, at the root and outside
	// /api/v1, for the reasons config_json.go gives: it is not API surface and must not appear in the
	// spec. Always registered — a bot benefits from /config.json as much as the SPA does, and it
	// costs nothing when no SPA is mounted.
	registerConfigJSON(mux, cfg.APIBase)

	humaAPI := humago.New(mux, humaConfig())

	// THE TWO GATES, in the order they run, installed BEFORE any operation is registered because Huma
	// captures the middleware chain at huma.Register time (huma.go:881) — one added afterwards is
	// silently never called. NewHumaAPI installs NEITHER: that path builds the registry for
	// `dkp openapi` and the arch tests, where no request is ever served and a middleware would only
	// be a second assembly site's worth of difference between the emitted document and this one.
	//
	// CAN THIS INSTANCE DECIDE, THEN WHO IS ASKING, and that order is deliberate. The first gate is
	// about the PROCESS: an instance whose permission catalogue never reached the database cannot
	// authorize anything, so it answers 503 without reading a credential it could not act on anyway
	// (#272). The second is about the REQUEST: it resolves the cookie or the bearer into one
	// Principal and refuses an operation that declares `Security` when no live credential is present
	// (03-security.md §4.1, §5).
	//
	// What is still missing between them is the permission check itself — `authz.Check`, the scope
	// intersection and the step-up window — which is Wave 0e (#276). Until it lands, a reconciled
	// instance lets every authenticated principal through every operation.
	humaAPI.UseMiddleware(authorizationGate(humaAPI, cfg.Authorization))
	humaAPI.UseMiddleware(principalMiddleware(humaAPI, cfg.Auth))

	registerOperations(humaAPI, cfg)
	registerDocs(mux)

	// The SPA catch-all, mounted LAST and only when a WebUI is supplied.
	//
	// It handles "/", which in net/http's ServeMux is the lowest-precedence pattern: every more
	// specific route above — /healthz, /readyz, /config.json, /api/v1/... , /api/v1/docs — wins over
	// it, so the SPA receives only the paths nothing else claimed. That is exactly the set the
	// client-side router should own.
	//
	// The "/api/" guard is mounted alongside and is NOT optional: "/api/" is a longer, more specific
	// pattern than "/", so ServeMux routes every unmatched /api path to this 404 rather than to the
	// SPA. Without it, a mistyped endpoint like /api/v1/tikcs would fall through to the SPA and
	// return 200 with an HTML page — the exact "200 with an error page" failure every bot author
	// suffered from EQdkp. The SPA handler in internal/ui 404s /api too, as defence in depth, but the
	// routing guarantee belongs here where the mount order is visible. TestServer_WebUI_* pins both.
	//
	// The guard renders problem+json rather than net/http's text/plain `404 page not found`: a bot
	// hitting a mistyped endpoint must get the same RFC 9457 body it gets everywhere else, not a
	// content type its error parser cannot read. This is a routed pattern, so the Problem middleware
	// hands it through untouched — the handler owns its own error shape (handleAPINotFound).
	if cfg.WebUI != nil {
		mux.Handle("/", cfg.WebUI)
		mux.HandleFunc("/api/", handleAPINotFound)
	}

	// Both arguments are the same mux: Problem asks it which pattern would match, then delegates to
	// it. Two parameters rather than one because the Matcher it needs is an interface and the
	// handler it wraps is not — a single parameter would force every caller to satisfy both.
	handler := middleware.Problem(mux, problemRenderer{}, mux)

	return middleware.RequestID(clk, handler)
}

// handleAPINotFound answers an unmatched /api path with an RFC 9457 problem+json 404.
//
// It is the handler behind the "/api/" catch-all mounted alongside the SPA. Because "/api/" is a
// routed pattern, the Problem middleware delegates to it untouched (see middleware.Problem), so this
// handler — not the middleware's unrouted-path branch — owns the response. Reusing problemRenderer
// keeps the body byte-identical to every other unrouted-path 404 the server produces; the bare
// http.NotFound it replaces returned text/plain, which a bot's error parser cannot read.
func handleAPINotFound(w http.ResponseWriter, r *http.Request) {
	problemRenderer{}.RenderProblem(w, r, http.StatusNotFound, string(CodeNotFound),
		"No operation is registered for this path.")
}

// NewHumaAPI builds the Huma API alone, with no server around it.
//
// It exists for `dkp openapi` and for arch_test.go, both of which need the operation registry
// without a listener. Keeping it as one function rather than letting each caller assemble its own
// config is what makes the emitted document and the served document the same document — a second
// assembly site is how the committed spec starts describing a server nobody runs.
func NewHumaAPI(cfg Config) huma.API {
	humaAPI := humago.New(http.NewServeMux(), humaConfig())
	registerOperations(humaAPI, cfg)

	return humaAPI
}

// registerOperations declares every Huma operation the binary serves, in one place.
//
// New and NewHumaAPI both call it so the served document and the emitted document are the same
// document — the second-assembly-site failure server.go's header warns about. The guild service is
// built from cfg.Store, which is nil in the spec-only path (NewHumaAPI): the operations register
// regardless, because a route absent from the registry is a route absent from the spec, and the
// handlers are never invoked without a listener.
func registerOperations(humaAPI huma.API, cfg Config) {
	clk := cfg.Clock
	if clk == nil {
		clk = clock.System{}
	}

	registerMeta(humaAPI, cfg)
	registerGuild(humaAPI, guild.NewService(cfg.Store, clk))
}

// humaConfig assembles the OpenAPI document's fixed parts.
//
// Built explicitly rather than from huma.DefaultConfig, for one reason worth stating: DefaultConfig
// installs a SchemaLinkTransformer, which adds a `$schema` key to every response body and a `Link`
// header alongside it. That is a wire-format addition no design document specifies, it would be
// baked into the committed spec and then protected by the drift gate forever, and it would have to
// be explained to every SDK consumer. Opting in later is a one-line change; opting out after the
// SDKs ship is a breaking one.
//
// DocsPath is empty for the same class of reason: Huma's built-in renderer is a <script> tag
// pointing at unpkg.com, and acceptance criterion 7 requires the reference to be served from the
// binary with no network fetch. registerDocs serves a vendored Scalar instead.
func humaConfig() huma.Config {
	registry := huma.NewMapRegistry("#/components/schemas/", huma.DefaultSchemaNamer)

	return huma.Config{
		OpenAPI: &huma.OpenAPI{
			OpenAPI: "3.1.0",
			Info: &huma.Info{
				Title:       specTitle,
				Version:     SpecVersion,
				Description: "DKP and guild management for Project 1999 EverQuest raiding guilds.",
				License: &huma.License{
					Name:       "Apache-2.0",
					Identifier: "Apache-2.0",
				},
			},
			Components: &huma.Components{
				Schemas: registry,
				// The two schemes every operation's Security requirement names. Without this block
				// they were undefined names in a published document (see internal/api/security.go).
				SecuritySchemes: securitySchemes(),
			},
			Webhooks: webhookPlaceholder(),
		},
		OpenAPIPath:   BasePath + "/openapi",
		DocsPath:      "",
		SchemasPath:   "",
		Formats:       huma.DefaultFormats,
		DefaultFormat: "application/json",
	}
}
