package api

import (
	"github.com/danielgtaylor/huma/v2"

	"github.com/prokopto-dev/dragonkillparty/internal/authz"
)

// The two security-scheme names. They are the keys in `components.securitySchemes` AND the keys every
// operation's Security requirement uses, which is the whole reason they are constants: a requirement
// naming a scheme the document does not define is a spec that tells a bot author to send a credential
// it never describes, and OpenAPI has no way to complain about it.
//
// TestArch_SecurityRequirements_NameADefinedScheme is the machine half. It found this: PR 5a shipped
// operations declaring `{"pat": ["roster:read"]}` and `{"session": {}}` against an EMPTY
// securitySchemes block, so `pat` and `session` were undefined names for four operations and two
// releases of the spec.
const (
	// SchemePAT is the bearer-token scheme. Canonical §7: `Authorization: Bearer dkp_pat_…` only,
	// query-string tokens rejected.
	SchemePAT = "pat"

	// SchemeSession is the browser session cookie.
	SchemeSession = "session"
)

// SessionCookieName is the session cookie, and this exact string is canonical §7's:
//
//	| Session cookie | `__Host-dkp_session`. This exact name appears in the `securitySchemes` block. |
//
// The `__Host-` prefix is load-bearing rather than decorative (03-security.md §3.6): it pins the
// cookie to the exact origin and blocks subdomain injection, which matters because self-hosters park
// several apps under one domain. A browser refuses a `__Host-` cookie that carries a Domain or a Path
// other than `/`, so the name is itself the control.
const SessionCookieName = "__Host-dkp_session"

// PATBearerFormat describes the token shape a bot author sends, from 03-security.md §6.1:
//
//	dkp_pat_<8-char public prefix>_<43 chars base64url of 32 random bytes>
//
// The prefix is non-secret and indexed; it is what appears in logs, UIs and the token list, and it is
// the greppable marker a secret scanner matches. The secret half never appears in either.
const PATBearerFormat = "dkp_pat_<8-char prefix>_<43-char secret>"

// THERE IS NO `PatScope` NAMED SCHEMA, AND IT IS NOT FOR WANT OF TRYING.
//
// Publishing the vocabulary as a `components.schemas` entry is the shape an SDK generator turns into
// a real type, and it was written and then removed: huma v2 PRUNES every schema nothing `$ref`s, on
// every marshal (openapi.go's usedSchemas — it exists so a Hidden operation's body schemas do not
// leak into the document). An unreferenced `PatScope` therefore vanishes from the committed file AND
// from the served /api/v1/openapi, identically, which is the one property internal/api/spec.go exists
// to preserve.
//
// The two ways past it are both worse than waiting. Post-processing the JSON after marshalling would
// desynchronise the served document from the committed one — the second-assembly-site failure
// server.go's header warns about. Inventing a request or response field that references the schema
// would add wire surface to satisfy a generator.
//
// So the vocabulary lives in ONE place, the `x-dkp-scopes` extension on the `pat` scheme below, and
// the named schema lands with the first operation that legitimately references it — token mint and
// rotate, in Wave 0d. Issue #268 carries it, so the SDK type arrives when it has a caller.

// securitySchemes returns `components.securitySchemes`, with the PAT scope list GENERATED from
// internal/authz.
//
// Canonical §6: "There is exactly one source: internal/authz/catalogue.go. It generates the permission
// table seed, the OpenAPI x-dkp-permission metadata, the PAT scope enum, the authorization-matrix
// header, and docs/reference/permissions.md." This is that sentence's third clause.
//
// WHY THE SCOPES ARE AN EXTENSION AND NOT A `scopes` MAP. OpenAPI gives a scopes map only to `oauth2`
// and `openIdConnect` schemes; `pat` is `http`/`bearer`, which has nowhere in the standard to put a
// vocabulary. The list therefore rides as `x-dkp-scopes` on the scheme — the same extension every
// PAT-callable operation already carries, at document level. No document specified the location, so
// the choice is the extension the existing gate already understands; the named schema an SDK would
// turn into a type is blocked by huma's pruning, which the block above explains.
//
// A scope array on a NON-oauth2 requirement, which every PAT-callable operation carries, is legal in
// OpenAPI 3.1: for other scheme types the array "MAY contain a list of role names which are required
// for the execution, but are not otherwise defined or exchanged in-band". That retires the open
// question 02-api-design.md raised and phase-0-pr5-decisions.md §U4 answered by adopting x-dkp-scopes.
func securitySchemes() map[string]*huma.SecurityScheme {
	scopes := make([]any, 0, len(authz.Scopes()))
	for _, s := range authz.Scopes() {
		scopes = append(scopes, s.Key)
	}

	return map[string]*huma.SecurityScheme{
		SchemePAT: {
			Type:         "http",
			Scheme:       "bearer",
			BearerFormat: PATBearerFormat,
			// PRESENT TENSE, AND IT IS NOW TRUE. Until Wave 0e this description opened with
			// authz.AuthorizationGapNotice, because everything below it described a control the server
			// did not run — and a well-described control reads as evidence the control exists. #276
			// landed authz.Check at the choke point, so the notice, its two consumers and the two tests
			// that asserted it was published were deleted in the same change, exactly as Wave 0d deleted
			// Phase 0's pair (ADR-0028).
			Description: "A personal access token belonging to a service account, sent as " +
				"`Authorization: Bearer dkp_pat_…`. Query-string tokens are rejected (canonical §7); " +
				"the compat shim's `?atoken=` is the single documented exception and is not part of " +
				"this API. Effective capability is the service account's role permissions INTERSECTED " +
				"with the token's scopes, so a token can only ever narrow what its account already " +
				"has. There is no `admin:*` scope and no all-powerful token (ADR-0011): the operations " +
				"that alter authentication, authorization or bulk-export state are session-and-" +
				"step-up only and carry no scope at all.",
			Extensions: map[string]any{
				ExtensionScopes: scopes,
			},
		},
		SchemeSession: {
			Type: "apiKey",
			In:   "cookie",
			Name: SessionCookieName,
			Description: "The browser session cookie: opaque, server-side, `HttpOnly; Secure; " +
				"SameSite=Lax; Path=/`, no `Domain`. The `__Host-` prefix pins it to the exact origin " +
				"(03-security.md §3.6). Cookies are ignored entirely when `Authorization` is present. " +
				"Operations in canonical §6's capability floor accept this scheme ONLY, and require a " +
				"recent step-up.",
		},
	}
}
