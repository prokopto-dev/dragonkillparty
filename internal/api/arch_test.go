package api

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/authz"
)

// Architectural tests over the Huma registry and over the source that populates it.
//
// These enforce law 1 of AGENTS.md — routes are declared only in internal/api — and the per-operation
// rules of canonical conventions §7. They exist at route #1 on purpose: a gate installed at route
// #40 has thirty-nine exceptions to grandfather, and the whole reason PR 4 is the fourth PR rather
// than the fortieth is that this file is cheap to write when the registry has one entry.
//
// Two mechanisms, because neither alone is sufficient, and the split is not arbitrary:
//
//   - The REGISTRY (huma.API.OpenAPI().Paths) is authoritative for what a served operation declares:
//     its operationId, its security, its extensions. This is the same object the committed spec is
//     marshalled from, so an assertion here and the published document cannot disagree.
//   - The SOURCE (a go/parser scan for huma.Register calls) is authoritative for two things the
//     registry structurally cannot answer. Law 1 needs to know about routes in packages this test
//     binary never imports — which is every package that could violate it. And `Hidden: true` is
//     invisible in the registry: huma.Register runs `else if !op.Hidden { oapi.AddOperation(&op) }`
//     (huma/v2@v2.39.1 huma.go:816), so a hidden operation is never added to Paths at all and no
//     amount of reading the registry can tell a correctly-hidden route from one that was never
//     written. The declaration is in the source; that is where it is read.
//
// Every negative case below is exercised against a fixture, never by breaking the real tree. A gate
// nobody has seen go red is a gate nobody knows works.

// registeredOp is one operation as the live registry holds it.
type registeredOp struct {
	Path   string
	Method string
	Op     *huma.Operation
}

// String makes require failure messages name the route rather than a struct dump.
func (o registeredOp) String() string { return o.Method + " " + o.Path }

// registeredOperations enumerates the operations this binary serves.
//
// It builds the API through NewHumaAPI, the same constructor `dkp openapi` and New use, rather than
// registering a private copy. A second assembly site is how these tests would start passing against
// a registry the server does not actually serve.
func registeredOperations(t *testing.T) []registeredOp {
	t.Helper()

	doc := NewHumaAPI(Config{}).OpenAPI()
	require.NotNil(t, doc, "the Huma API has no OpenAPI document")

	var ops []registeredOp

	for path, item := range doc.Paths {
		for method, op := range map[string]*huma.Operation{
			http.MethodGet:    item.Get,
			http.MethodPut:    item.Put,
			http.MethodPost:   item.Post,
			http.MethodDelete: item.Delete,
			http.MethodPatch:  item.Patch,
			http.MethodHead:   item.Head,
			// OPTIONS and TRACE are deliberately absent: neither is a shape this API declares, and
			// listing them would suggest they are.
		} {
			if op != nil {
				ops = append(ops, registeredOp{Path: path, Method: method, Op: op})
			}
		}
	}

	require.NotEmpty(t, ops,
		"the registry is empty, so every assertion in this file would pass without checking anything")

	// Sorted so a failure message is stable across runs; Paths is a map.
	slices.SortFunc(ops, func(a, b registeredOp) int { return strings.Compare(a.String(), b.String()) })

	return ops
}

// TestArch_OperationIDs_AreExplicitUniqueAndLowerCamelCase is the operationId contract.
//
// An operationId is PUBLIC API: both generated SDKs derive a method name from it, so a missing one
// produces a method named after whatever Huma inferred, and a duplicate produces two methods with
// one name. Canonical §7 requires it to be explicit and lowerCamelCase for exactly that reason.
//
// Delete this and an operation can ship with no id; the first person to notice is a bot author whose
// SDK method disappeared in a minor release.
func TestArch_OperationIDs_AreExplicitUniqueAndLowerCamelCase(t *testing.T) {
	t.Parallel()

	require.Empty(t, operationIDViolations(registeredOperations(t)))
}

// operationIDViolations returns one message per operationId defect in ops.
//
// Extracted from the test above rather than inlined, so that
// TestArch_MissingOperationID_FailsBuild can drive THIS function against a deliberately broken
// fixture. That is the difference between a fixture that proves the gate fires and one that merely
// re-states a library's behaviour: an audit of the first version of this file neutered the real
// assertion and the fixture still passed, because the two shared no code. They do now — the same
// arrangement scanRegisterCalls already had for the law-1 and Hidden gates.
func operationIDViolations(ops []registeredOp) []string {
	var problems []string

	seen := make(map[string]registeredOp)

	for _, op := range ops {
		switch {
		case op.Op.OperationID == "":
			problems = append(problems, fmt.Sprintf(
				"%s declares no OperationID. It must be explicit — never auto-derived, never renamed.", op))

			continue
		case !lowerCamelCase(op.Op.OperationID):
			problems = append(problems, fmt.Sprintf(
				"%s has OperationID %q, which is not lowerCamelCase (canonical §16: verb + resource, "+
					"e.g. createRaidTick)", op, op.Op.OperationID))
		}

		if prior, dup := seen[op.Op.OperationID]; dup {
			problems = append(problems, fmt.Sprintf(
				"%q is declared by both %s and %s; the SDK generators would emit two methods with "+
					"one name", op.Op.OperationID, prior, op))
		}

		seen[op.Op.OperationID] = op
	}

	return problems
}

// TestArch_Operations_DeclareSecurityAndPermission is the "no exceptions" half of AGENTS.md's API
// invariant.
//
// Both fields are required on every operation. The Security check is on nil rather than on
// emptiness, and that distinction is the entire subtlety: `security: []` is the EXPLICIT declaration
// that an operation needs no credential (docs/design/02-api-design.md:144), while an omitted
// security key means "inherit the document-level requirement". They are opposite meanings and one of
// them is falsy, so a check written as `require.NotEmpty` would reject every public endpoint and
// push the next author into omitting the field instead — which is the failure this test exists to
// prevent, arrived at by way of the test itself.
func TestArch_Operations_DeclareSecurityAndPermission(t *testing.T) {
	t.Parallel()

	for _, op := range registeredOperations(t) {
		require.NotNil(t, op.Op.Security,
			"%s does not declare Security. A public operation declares an explicitly empty "+
				"[]map[string][]string{}, which is not the same as omitting the field.", op)

		permission, ok := op.Op.Extensions[ExtensionPermission].(string)
		require.True(t, ok,
			"%s does not declare %s in its Extensions map. Note Metadata is NOT a substitute: it is "+
				"tagged `yaml:\"-\"` in Huma and never reaches the OpenAPI document, so an operation "+
				"declaring its permission there passes nothing and fails `make verify-spec`.",
			op, ExtensionPermission)

		require.NotEmpty(t, permission, "%s declares an empty %s", op, ExtensionPermission)

		if slices.Contains(SentinelPermissions(), permission) {
			continue
		}

		// A non-sentinel permission must resolve in the catalogue. PR 4 could only assert that
		// internal/authz/catalogue.go EXISTED, because it did not; PR 5a created it and Phase 2
		// Wave 0b (#261) gave it a database to project into, so the assertion is now the real one:
		// the key is in Catalogue(). That is the same set authz.Reconcile writes into the permission
		// table at boot and the same set the FK on role_permission resolves against, so a key that
		// fails here would fail the boot rather than return a 403.
		require.Containsf(t, catalogueKeys(), permission,
			"%s names permission %q, which is neither a sentinel nor a key in "+
				"internal/authz/catalogue.go. Adding a permission key is a schema change — see "+
				".claude/rules/api-endpoints.md's \"Stop and ask if\".", op, permission)
	}
}

// catalogueKeys returns every permission key in the authz catalogue.
func catalogueKeys() []string {
	keys := make([]string, 0, len(authz.Catalogue()))
	for _, p := range authz.Catalogue() {
		keys = append(keys, p.Key)
	}

	return keys
}

// TestArch_DeclaredPermissions_AreCatalogueKeysWithoutSentinels pins what the boot path is handed.
//
// DeclaredPermissions() is the required set authz.Reconcile verifies against this officer's database
// before the listener opens (#261), so two properties have to hold and both are easy to break in a
// way nothing else notices:
//
//   - every element resolves in the catalogue — otherwise a correctly-declared route makes the binary
//     refuse to boot;
//   - NO element is a sentinel — `public` and `self` are not catalogue keys and must never become
//     permission rows, so a public route must not be able to fail a boot.
//
// The second is the one worth a test of its own: the sentinel filter is three lines in a loop, and
// deleting it produces a binary that boots fine today (getMeta is the only public operation and the
// registry is small) and refuses to boot the first time somebody adds a `self` route.
func TestArch_DeclaredPermissions_AreCatalogueKeysWithoutSentinels(t *testing.T) {
	t.Parallel()

	declared := DeclaredPermissions()

	require.NotEmpty(t, declared,
		"no operation declares a catalogue permission, so this test would pass vacuously. "+
			"/api/v1/guild declares roster.read and admin.settings.")

	for _, key := range declared {
		require.NotContains(t, SentinelPermissions(), key,
			"DeclaredPermissions() returned the sentinel %q. Sentinels are not catalogue keys and "+
				"must never reach authz.Reconcile — a public route would then be a boot failure.", key)
		require.Contains(t, catalogueKeys(), key,
			"DeclaredPermissions() returned %q, which is not in internal/authz/catalogue.go", key)
	}

	require.Equal(t, slices.Sorted(slices.Values(declared)), declared,
		"DeclaredPermissions() must be sorted: the boot log and the missing-key report are read by an "+
			"operator, and a set whose order changes on every boot is a diff nobody can use")
}

// TestArch_MutatingPost_RequiresIdempotencyKey is a tripwire installed ahead of the code it gates.
//
// READ THIS ASSERTION HONESTLY: it is vacuously true today. The registry holds one GET, so the loop
// body never runs and passing it proves nothing about today's behaviour. Its value is entirely in
// the future — canonical §7 requires Idempotency-Key on every POST that creates domain state, PR 9
// and PR 10 add the first ones, and "bots retry; duplicated ticks and double-charged bids are the
// top support burden" (AGENTS.md) is a lesson this project would otherwise learn from a guild's
// ledger rather than from CI.
//
// It is paired with TestArch_MutatingPostWithoutIdempotencyKey_FailsBuild, which drives the same
// function against a fixture POST. Without that pairing this test would pass forever even if the
// header check were wrong — not only until PR 9, but after it, because a check matching the wrong
// parameter location would keep passing with real operations registered.
func TestArch_MutatingPost_RequiresIdempotencyKey(t *testing.T) {
	t.Parallel()

	require.Empty(t, idempotencyViolations(registeredOperations(t)))
}

// idempotencyFencedPrefixes are the paths under which a POST creates domain state.
//
// Canonical §7's list, cross-checked against .claude/rules/api-endpoints.md:140. Note that
// internal/api/EXAMPLE_ENDPOINT.md:231 writes `/bid-sessions` where the rule file writes `/bids`;
// both are listed rather than picking one, because a fence that is too wide costs an author one
// explicit header and a fence that is too narrow costs a guild its points.
func idempotencyFencedPrefixes() []string {
	return []string{
		"/api/v1/raids", "/api/v1/awards", "/api/v1/adjustments",
		"/api/v1/bids", "/api/v1/bid-sessions", "/api/v1/raid-submissions", "/api/v1/ledger",
	}
}

// idempotencyViolations returns one message per fenced POST that does not require an
// Idempotency-Key header.
//
// Extracted so the vacuous real-registry gate and the fixture test share it — see
// operationIDViolations for why that sharing is the point rather than tidiness.
func idempotencyViolations(ops []registeredOp) []string {
	var problems []string

	for _, op := range ops {
		if op.Method != http.MethodPost {
			continue
		}

		fenced := slices.ContainsFunc(idempotencyFencedPrefixes(),
			func(p string) bool { return strings.HasPrefix(op.Path, p) })
		if !fenced {
			continue
		}

		var found bool

		for _, param := range op.Op.Parameters {
			if param != nil && strings.EqualFold(param.Name, "Idempotency-Key") && param.In == "header" {
				found = param.Required
			}
		}

		if !found {
			problems = append(problems, fmt.Sprintf(
				"%s creates domain state under a fenced prefix but does not require an "+
					"Idempotency-Key header. Add an IdempotencyKey field to its input struct, tagged "+
					"as a required Idempotency-Key header.", op))
		}
	}

	return problems
}

// routeDecl is one huma.Register call as it is written in the source.
type routeDecl struct {
	File   string
	Line   int
	Path   string
	Hidden bool
}

// scanRegisterCalls finds every huma.Register call under root.
//
// go/parser rather than go/packages or go/types: this must be able to read a package that does not
// compile and one that nothing imports, it runs inside `make test-unit` against a < 5 s budget, and
// the question — "does this file contain a call spelled huma.Register" — is syntactic. Type
// resolution would buy the ability to see through an alias import and would cost seconds.
//
// _test.go files are excluded. A test that registers an operation into a throwaway API is not a
// route the server serves, and this very file does exactly that a few functions down; scanning them
// would make the law-1 assertion fail on its own test suite.
func scanRegisterCalls(t *testing.T, root string) []routeDecl {
	t.Helper()

	var found []routeDecl

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if d.IsDir() {
			// testdata is Go's documented "not part of the build" directory, and the negative
			// fixtures below live in t.TempDir() anyway.
			if name := d.Name(); name == "testdata" || name == ".git" || name == "node_modules" {
				return fs.SkipDir
			}

			return nil
		}

		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		fset := token.NewFileSet()

		file, parseErr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if parseErr != nil {
			// A file that does not parse cannot be declaring a route, and failing here would make
			// this test the messenger for every syntax error in the repository.
			return nil //nolint:nilerr // deliberate: unparseable files are not route declarations
		}

		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}

			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Register" {
				return true
			}

			pkg, ok := sel.X.(*ast.Ident)
			if !ok || pkg.Name != "huma" {
				return true
			}

			decl := routeDecl{File: path, Line: fset.Position(call.Pos()).Line}
			if len(call.Args) >= 2 {
				decl.Path, decl.Hidden = operationLiteralFields(call.Args[1])
			}

			found = append(found, decl)

			return true
		})

		return nil
	})
	require.NoError(t, err, "walk %s", root)

	return found
}

// operationLiteralFields pulls Path and Hidden out of a huma.Operation composite literal.
//
// Only literal values are read. An operation whose Path is built from a variable returns "" here,
// and the Hidden assertion treats that as unallowlisted — deliberately, because a hidden route with
// a computed path is precisely the shape that would slip a route past a reviewer, and canonical §7's
// allowlist is a list of five literal paths.
func operationLiteralFields(arg ast.Expr) (path string, hidden bool) {
	lit, ok := arg.(*ast.CompositeLit)
	if !ok {
		return "", false
	}

	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}

		key, ok := kv.Key.(*ast.Ident)
		if !ok {
			continue
		}

		switch key.Name {
		case "Path":
			if s, ok := kv.Value.(*ast.BasicLit); ok && s.Kind == token.STRING {
				if unquoted, err := strconv.Unquote(s.Value); err == nil {
					path = unquoted
				}
			}
		case "Hidden":
			if b, ok := kv.Value.(*ast.Ident); ok && b.Name == "true" {
				hidden = true
			}
		}
	}

	return path, hidden
}

// TestArch_Routes_AreDeclaredOnlyInAPIPackage is law 1, machine-checked.
//
// AGENTS.md: internal/api is the only tree where an HTTP route may be declared. Before PR 4 nothing
// enforced this — scripts/repo-gates.sh has no route gate despite .github/workflows/ci.yml
// advertising one — so a server-rendered back door that bypassed the spec would have been caught by
// review or not at all. The whole API-first argument rests on there being no such door, and three
// CI gates exist to prove it (docs/design/02-api-design.md:606).
//
// Delete this and the SPA can be served from an operation absent from the published spec, which is
// exactly how "the UI needs it but a bot would not" endpoints appear.
func TestArch_Routes_AreDeclaredOnlyInAPIPackage(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	apiDir := filepath.Join(root, "internal", "api")

	for _, tree := range []string{"internal", "cmd"} {
		for _, decl := range scanRegisterCalls(t, filepath.Join(root, tree)) {
			require.Truef(t, strings.HasPrefix(decl.File, apiDir+string(filepath.Separator)),
				"%s:%d declares an HTTP route with huma.Register outside internal/api. Law 1: "+
					"routes are declared only in internal/api (AGENTS.md). Move the operation there "+
					"and call it from this package.", decl.File, decl.Line)
		}
	}
}

// TestArch_RouteOutsideAPIPackage_FailsBuild proves the law-1 scan actually fires.
//
// Named by docs/development/first-ten-prs.md PR 4. Without it, TestArch_Routes_AreDeclaredOnlyInAPI
// would pass just as happily if scanRegisterCalls returned nothing at all — which is what it would
// do after any refactor that broke the selector match, and nobody would notice until a route landed
// in internal/cms.
func TestArch_RouteOutsideAPIPackage_FailsBuild(t *testing.T) {
	t.Parallel()

	tree := t.TempDir()
	offender := filepath.Join(tree, "sneaky.go")

	require.NoError(t, os.WriteFile(offender, []byte(`package cms

import "github.com/danielgtaylor/huma/v2"

func registerSomething(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "getSneaky",
		Method:      "GET",
		Path:        "/api/v1/sneaky",
	}, nil)
}
`), 0o600), "write the offending fixture")

	found := scanRegisterCalls(t, tree)

	require.Len(t, found, 1, "the scan missed a plain huma.Register call")
	require.Equal(t, offender, found[0].File)
	require.Equal(t, "/api/v1/sneaky", found[0].Path,
		"the scan found the call but could not read its Path, so the Hidden allowlist check "+
			"downstream would treat every route as unallowlisted")
}

// TestArch_HiddenOperations_AreAllowlisted enforces canonical §7's five-path allowlist.
//
// Read from the SOURCE, not the registry, and that is forced rather than chosen: huma.Register never
// adds a hidden operation to the OpenAPI object (huma.go:816), so a hidden route is absent from
// Paths and indistinguishable from one that does not exist. .github/workflows/ci.yml listed this
// assertion under `make verify-spec` until PR 4, where it is even less implementable — the committed
// JSON has strictly less information than the registry. It is here, and ci.yml now says so.
//
// Nothing sets Hidden today: /healthz and /readyz are raw net/http handlers, not operations. This
// test therefore passes over an empty set, and that is stated rather than implied. It is installed
// now because the allowlist is a rule about every operation that will ever exist.
func TestArch_HiddenOperations_AreAllowlisted(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	allowed := HiddenOperationAllowlist()

	for _, tree := range []string{"internal", "cmd"} {
		for _, decl := range scanRegisterCalls(t, filepath.Join(root, tree)) {
			if !decl.Hidden {
				continue
			}

			require.Truef(t, slices.Contains(allowed, decl.Path),
				"%s:%d declares Hidden: true on path %q, which is not in "+
					"HiddenOperationAllowlist (canonical §7 permits /healthz, /readyz, /metrics, "+
					"the OAuth callback and the compat shim). A hidden operation is absent from the "+
					"published spec, so this is how the SPA gets an endpoint bots cannot see.",
				decl.File, decl.Line, decl.Path)
		}
	}
}

// TestArch_HiddenOperationOutsideAllowlist_IsRejected proves the allowlist check fires.
//
// The positive test above runs over an empty set, so on its own it would pass forever even if the
// Hidden field were never read. This is the only evidence that it is.
func TestArch_HiddenOperationOutsideAllowlist_IsRejected(t *testing.T) {
	t.Parallel()

	tree := t.TempDir()

	require.NoError(t, os.WriteFile(filepath.Join(tree, "hidden.go"), []byte(`package api

import "github.com/danielgtaylor/huma/v2"

func registerHidden(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "getSpaShell",
		Method:      "GET",
		Path:        "/api/v1/spa-shell",
		Hidden:      true,
	}, nil)
}
`), 0o600), "write the hidden fixture")

	found := scanRegisterCalls(t, tree)
	require.Len(t, found, 1)
	require.True(t, found[0].Hidden, "the scan did not read Hidden: true out of the literal")
	require.NotContains(t, HiddenOperationAllowlist(), found[0].Path,
		"the fixture path must not be allowlisted or this test proves nothing")
}

// fixtureRegistry registers ops into a throwaway Huma API and returns them in the shape the
// assertion helpers consume.
//
// A throwaway registry rather than mutating the real one, for the reason every negative fixture in
// test/repo exists: making the real tree fail to prove a gate works leaves the repo broken for
// everyone, and a test that passes only because somebody remembered to undo the damage is not a test.
func fixtureRegistry(t *testing.T, ops ...huma.Operation) []registeredOp {
	t.Helper()

	api := humago.New(http.NewServeMux(), humaConfig())

	for _, op := range ops {
		huma.Register(api, op,
			func(_ context.Context, _ *struct{}) (*MetaOutput, error) { return &MetaOutput{}, nil })
	}

	var out []registeredOp

	for path, item := range api.OpenAPI().Paths {
		for method, op := range map[string]*huma.Operation{
			http.MethodGet: item.Get, http.MethodPut: item.Put, http.MethodPost: item.Post,
			http.MethodDelete: item.Delete, http.MethodPatch: item.Patch, http.MethodHead: item.Head,
		} {
			if op != nil {
				out = append(out, registeredOp{Path: path, Method: method, Op: op})
			}
		}
	}

	require.NotEmpty(t, out, "the fixture operations did not register")

	return out
}

// TestArch_MissingOperationID_FailsBuild proves the operationId assertion fires.
//
// Named by docs/development/first-ten-prs.md PR 4 as "deleting OperationID from getMeta fails CI".
//
// It drives operationIDViolations — the SAME function
// TestArch_OperationIDs_AreExplicitUniqueAndLowerCamelCase requires to be empty. The first version of
// this test did not: it registered a nameless operation and asserted Huma had not auto-filled the
// id, which proves a fact about the library and nothing about this repository's gate. An audit
// demonstrated the gap by neutering the real assertion, after which this test still passed. Sharing
// the function is what makes it a proof.
func TestArch_MissingOperationID_FailsBuild(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		op   huma.Operation
		want string
	}{
		{
			name: "no OperationID at all",
			op: huma.Operation{
				// Deliberately absent — this is the fixture.
				Method: http.MethodGet, Path: "/api/v1/nameless",
				Summary:  "An operation whose author forgot the one field that is public API",
				Security: []map[string][]string{},
			},
			want: "declares no OperationID",
		},
		{
			name: "PascalCase OperationID",
			op: huma.Operation{
				OperationID: "GetNameless",
				Method:      http.MethodGet, Path: "/api/v1/pascal",
				Summary: "An operation whose id would generate the wrong SDK method name",
			},
			want: "not lowerCamelCase",
		},
		{
			name: "snake_case OperationID",
			op: huma.Operation{
				OperationID: "get_nameless",
				Method:      http.MethodGet, Path: "/api/v1/snake",
				Summary: "Another shape canonical §16 rejects",
			},
			want: "not lowerCamelCase",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			problems := operationIDViolations(fixtureRegistry(t, tc.op))

			require.NotEmpty(t, problems,
				"operationIDViolations accepted %q, so the gate over the real registry would too",
				tc.name)
			require.Contains(t, strings.Join(problems, "\n"), tc.want)
		})
	}
}

// TestArch_DuplicateOperationID_PanicsAtRegistration pins a library guarantee this repo leans on.
//
// Writing a fixture for the duplicate branch of operationIDViolations turned up something better
// than the fixture: a duplicate operationId cannot reach the registry AT ALL, because
// huma.OpenAPI.AddOperation panics on one (huma/v2@v2.39.1 openapi.go:1525). The branch is therefore
// unreachable from the registry today, and is kept only as defence in depth.
//
// That makes this test the thing actually worth asserting. A panic at registration is a boot
// failure, which is the strongest possible enforcement — better than a red test — and it is a
// property of a dependency rather than of this repository, so nothing here would notice it going
// away. If Huma ever downgrades it to last-write-wins, this test goes red and points at the two
// places that then have to carry the weight: operationIDViolations' duplicate branch, and
// internal/specgate's SPEC002.
//
// SPEC002 is not redundant with this. It reads the committed JSON, where a path operation and a
// webhook CAN share an id — Huma registers webhooks through a different route that AddOperation
// never sees — and both generators derive names from the same namespace.
func TestArch_DuplicateOperationID_PanicsAtRegistration(t *testing.T) {
	t.Parallel()

	require.Panics(t, func() {
		_ = fixtureRegistry(t,
			huma.Operation{
				OperationID: "getThing", Method: http.MethodGet, Path: "/api/v1/things",
				Summary: "One of two operations sharing an id",
			},
			huma.Operation{
				OperationID: "getThing", Method: http.MethodGet, Path: "/api/v1/others",
				Summary: "The other of two operations sharing an id",
			},
		)
	}, "Huma accepted two operations with the same OperationID. The SDK generators would emit two "+
		"methods with one name, and nothing in this repository would catch it before they did.")
}

// TestArch_MutatingPostWithoutIdempotencyKey_FailsBuild proves the idempotency tripwire fires.
//
// TestArch_MutatingPost_RequiresIdempotencyKey runs over an empty set today — the registry holds one
// GET — so on its own it would pass forever even if the header check were wrong. That is not only a
// gap until PR 9 lands the first mutating POST; it is a gap AFTER it lands, because a subtly wrong
// check (matching the wrong parameter location, or a fenced prefix that never matches) would keep
// passing with real operations registered.
//
// The positive case is here too, and is the half that stops the rule being "fixed" into something
// that always fires.
func TestArch_MutatingPostWithoutIdempotencyKey_FailsBuild(t *testing.T) {
	t.Parallel()

	t.Run("a fenced POST without the header is rejected", func(t *testing.T) {
		t.Parallel()

		problems := idempotencyViolations(fixtureRegistry(t, huma.Operation{
			OperationID: "createRaidTick", Method: http.MethodPost, Path: "/api/v1/raids/ticks",
			Summary: "A mutating POST under a fenced prefix, with no Idempotency-Key",
		}))

		require.NotEmpty(t, problems,
			"a POST under /api/v1/raids requires no Idempotency-Key and the gate accepted it")
	})

	t.Run("an unfenced POST is not required to carry one", func(t *testing.T) {
		t.Parallel()

		require.Empty(t, idempotencyViolations(fixtureRegistry(t, huma.Operation{
			OperationID: "createSomethingElse", Method: http.MethodPost, Path: "/api/v1/elsewhere",
			Summary: "A POST outside every fenced prefix",
		})), "the fence matched a prefix it should not have")
	})

	t.Run("a GET under a fenced prefix is not required to carry one", func(t *testing.T) {
		t.Parallel()

		require.Empty(t, idempotencyViolations(fixtureRegistry(t, huma.Operation{
			OperationID: "listRaids", Method: http.MethodGet, Path: "/api/v1/raids",
			Summary: "A read under a fenced prefix",
		})), "the fence applied to a GET, which creates no domain state")
	})
}

// TestArch_StateChangingOperation_RequiresIfMatch is the precondition tripwire.
//
// canonical §7 and .claude/rules/api-endpoints.md require If-Match on every PATCH and every state
// transition, so two officers editing the same resource race deterministically instead of both
// winning. .claude/rules/api-endpoints.md:227-237 and EXAMPLE_ENDPOINT.md have both claimed this test
// exists since PR 4; PR 5a ships the first PATCH and the test with it.
//
// The check is that the operation declares an If-Match HEADER parameter — NOT that it is required.
// The If-Match must be optional (see etag.go: a required tag yields 422, not the 428 canonical §7
// wants), so requiring it here would contradict the handler design. Declaring it is what matters: an
// operation with no If-Match parameter at all cannot enforce a precondition however its handler is
// written.
//
// Paired with TestArch_StateChangingWithoutIfMatch_FailsBuild, which drives the same function against
// a fixture PATCH, because the real registry has exactly one PATCH today and a check matching the
// wrong parameter location would keep passing as more land.
func TestArch_StateChangingOperation_RequiresIfMatch(t *testing.T) {
	t.Parallel()

	require.Empty(t, ifMatchViolations(registeredOperations(t)))
}

// ifMatchViolations returns one message per state-changing operation that declares no If-Match header
// parameter.
//
// A state-changing operation is any PATCH, PUT or DELETE — the methods that mutate an existing
// resource. POST is excluded: a POST creates domain state and is fenced by the Idempotency-Key rule
// instead (a create has no prior representation to precondition on). Extracted so the vacuous
// real-registry gate and the fixture test share it, the same arrangement operationIDViolations uses.
func ifMatchViolations(ops []registeredOp) []string {
	var problems []string

	for _, op := range ops {
		switch op.Method {
		case http.MethodPatch, http.MethodPut, http.MethodDelete:
		default:
			continue
		}

		var found bool

		for _, param := range op.Op.Parameters {
			if param != nil && strings.EqualFold(param.Name, "If-Match") && param.In == "header" {
				found = true
			}
		}

		if !found {
			problems = append(problems, fmt.Sprintf(
				"%s changes state but declares no If-Match header parameter. Add an IfMatch field to "+
					"its input struct, tagged `header:\"If-Match\"` (optional — the 428 for an absent "+
					"precondition is an explicit handler check, see etag.go).", op))
		}
	}

	return problems
}

// TestArch_StateChangingWithoutIfMatch_FailsBuild proves the If-Match tripwire fires.
//
// The positive test above runs over one PATCH today; without this, a broken check (matching the wrong
// parameter location, or excluding PATCH) would keep passing as more transitions land.
func TestArch_StateChangingWithoutIfMatch_FailsBuild(t *testing.T) {
	t.Parallel()

	t.Run("a PATCH without an If-Match parameter is rejected", func(t *testing.T) {
		t.Parallel()

		problems := ifMatchViolations(fixtureRegistry(t, huma.Operation{
			OperationID: "updateThing", Method: http.MethodPatch, Path: "/api/v1/things",
			Summary:  "A PATCH with no If-Match parameter",
			Security: []map[string][]string{{"session": {}}},
			Extensions: map[string]any{
				ExtensionPermission: "roster.write",
			},
		}))

		require.NotEmpty(t, problems,
			"a PATCH with no If-Match parameter was accepted, so the gate over the real registry would too")
	})

	t.Run("a GET is not required to carry an If-Match", func(t *testing.T) {
		t.Parallel()

		require.Empty(t, ifMatchViolations(fixtureRegistry(t, huma.Operation{
			OperationID: "getThing", Method: http.MethodGet, Path: "/api/v1/things",
			Summary:    "A read, which changes no state",
			Security:   []map[string][]string{{"session": {}}},
			Extensions: map[string]any{ExtensionPermission: "roster.read"},
		})), "the If-Match rule fired on a GET, which changes no state")
	})
}

// TestArch_SecurityRequirements_NameADefinedScheme closes the gap that shipped an empty
// securitySchemes block for two releases.
//
// A Security requirement is a MAP KEYED BY SCHEME NAME, and OpenAPI has no way to complain when that
// name is defined nowhere: the document simply tells a bot author to authenticate with `pat` and
// never says what `pat` is — not the transport, not the header, not the token format. Every
// operation registered before Wave 0c declared `{"pat": …}` or `{"session": {}}` against
// `components.securitySchemes: {}`, and every gate in the repository passed, because each one
// checked that Security was PRESENT rather than that it RESOLVED.
//
// The `security: []` case is the deliberate hole in the rule and not an oversight: an explicitly
// empty requirement list is how canonical §7 and 02-api-design.md §4.1 declare a public operation,
// so it names no scheme and there is nothing to resolve.
func TestArch_SecurityRequirements_NameADefinedScheme(t *testing.T) {
	t.Parallel()

	doc := NewHumaAPI(Config{}).OpenAPI()
	require.NotNil(t, doc.Components, "the document has no components block")
	require.NotEmpty(t, doc.Components.SecuritySchemes,
		"components.securitySchemes is empty. Every operation's Security names a scheme by key, so an "+
			"empty block means the published document requires credentials it never describes.")

	for _, op := range registeredOperations(t) {
		for _, requirement := range op.Op.Security {
			for scheme := range requirement {
				require.Containsf(t, doc.Components.SecuritySchemes, scheme,
					"%s requires security scheme %q, which components.securitySchemes does not define. "+
						"Add it in internal/api/security.go — a requirement naming an undefined scheme "+
						"is a spec that asks for a credential it never explains.", op, scheme)
			}
		}
	}
}

// TestArch_SecuritySchemes_MatchTheCanonicalContract pins the three values other documents quote.
//
// Each is load-bearing somewhere outside this file, which is why they are asserted here rather than
// left to review: canonical §7 requires the session cookie's exact name to appear in this block;
// 03-security.md §3.6 relies on the `__Host-` prefix for origin pinning; and the PAT scope list is
// canonical §6's "one catalogue generates the PAT scope enum", so a hand-typed copy here would be the
// second list that section exists to forbid.
func TestArch_SecuritySchemes_MatchTheCanonicalContract(t *testing.T) {
	t.Parallel()

	schemes := NewHumaAPI(Config{}).OpenAPI().Components.SecuritySchemes

	session := schemes[SchemeSession]
	require.NotNil(t, session, "the session scheme is not defined")
	require.Equal(t, "apiKey", session.Type)
	require.Equal(t, "cookie", session.In)
	require.Equal(t, "__Host-dkp_session", session.Name,
		"canonical §7: the session cookie's exact name appears in the securitySchemes block, and the "+
			"__Host- prefix is what pins it to the origin (03-security.md §3.6)")

	pat := schemes[SchemePAT]
	require.NotNil(t, pat, "the pat scheme is not defined")
	require.Equal(t, "http", pat.Type)
	require.Equal(t, "bearer", pat.Scheme,
		"canonical §7: `Authorization: Bearer dkp_pat_…` only — query-string tokens are rejected")

	declared, ok := stringSlice(pat.Extensions[ExtensionScopes])
	require.True(t, ok, "the pat scheme carries no %s extension", ExtensionScopes)

	want := make([]string, 0, len(authz.Scopes()))
	for _, s := range authz.Scopes() {
		want = append(want, s.Key)
	}

	require.Equal(t, want, declared,
		"the scheme's scope list must be authz.Scopes(), in order. Canonical §6: one catalogue "+
			"generates the PAT scope enum, so a second list here is exactly what that forbids.")
}

// TestArch_ScopeCoverage_MatchesSecurity enforces the three-case x-dkp-scopes rule, in both
// directions, and is the gate the previous four documents lacked when they each described an
// x-dkp-scopes convention that no code emitted (decision record §U4).
//
// The three cases, from canonical §6 and the decision record:
//
//   - PAT-callable: Security offers a `pat` alternative -> x-dkp-scopes is non-empty, every member
//     resolves in authz.Scopes(), and x-dkp-pat-forbidden is absent.
//   - Capability floor: the permission is in authz.CapabilityFloor() -> Security is session-only,
//     x-dkp-pat-forbidden is true, and there are NO scopes.
//   - Session-only by omission: session-only, permission not in the floor -> NEITHER scopes nor
//     pat-forbidden. Marking such an operation pat-forbidden is a false positive (admin.settings).
func TestArch_ScopeCoverage_MatchesSecurity(t *testing.T) {
	t.Parallel()

	require.Empty(t, scopeCoverageViolations(registeredOperations(t)))
}

// operationOffersPAT reports whether op's Security offers a `pat` alternative.
func operationOffersPAT(op registeredOp) bool {
	for _, requirement := range op.Op.Security {
		if _, ok := requirement["pat"]; ok {
			return true
		}
	}

	return false
}

// scopeCoverageViolations returns one message per operation that breaks the three-case scope rule.
//
// It reads the permission from Extensions to decide which case an operation is in, the scopes and the
// pat-forbidden flag to check the case's requirements, and authz.Scopes()/authz.CapabilityFloor() as
// the authorities — never a list local to this file, so the rule and the catalogue cannot drift
// apart. Extracted so the real-registry gate and the per-case fixtures share it.
func scopeCoverageViolations(ops []registeredOp) []string {
	valid := make(map[string]struct{}, len(authz.Scopes()))
	for _, s := range authz.Scopes() {
		valid[s.Key] = struct{}{}
	}

	floor := make(map[string]struct{}, len(authz.CapabilityFloor()))
	for _, k := range authz.CapabilityFloor() {
		floor[k] = struct{}{}
	}

	var problems []string

	for _, op := range ops {
		permission, _ := op.Op.Extensions[ExtensionPermission].(string)
		scopes, hasScopes := stringSlice(op.Op.Extensions[ExtensionScopes])
		patForbidden, _ := op.Op.Extensions[ExtensionPATForbidden].(bool)

		_, inFloor := floor[permission]

		switch {
		case operationOffersPAT(op):
			// PAT-callable.
			if !hasScopes || len(scopes) == 0 {
				problems = append(problems, fmt.Sprintf(
					"%s offers a pat Security alternative but declares no x-dkp-scopes. A PAT-callable "+
						"operation must name the scopes a token needs.", op))

				continue
			}

			for _, sc := range scopes {
				if _, ok := valid[sc]; !ok {
					problems = append(problems, fmt.Sprintf(
						"%s declares x-dkp-scope %q, which is not in authz.Scopes().", op, sc))
				}
			}

			if patForbidden {
				problems = append(problems, fmt.Sprintf(
					"%s is PAT-callable (its Security offers pat) but also declares "+
						"x-dkp-pat-forbidden: true. Those contradict.", op))
			}
		case inFloor:
			// Capability floor: session-only, pat-forbidden, no scopes.
			if !patForbidden {
				problems = append(problems, fmt.Sprintf(
					"%s names capability-floor permission %q but does not declare "+
						"x-dkp-pat-forbidden: true. The floor is session-and-step-up only.", op, permission))
			}

			if hasScopes && len(scopes) > 0 {
				problems = append(problems, fmt.Sprintf(
					"%s is in the capability floor and must carry no scopes, but declares %v.", op, scopes))
			}
		default:
			// Session-only by omission: neither scopes nor pat-forbidden.
			if hasScopes && len(scopes) > 0 {
				problems = append(problems, fmt.Sprintf(
					"%s is session-only (its Security offers no pat) and its permission %q is not in the "+
						"capability floor, so it must declare no x-dkp-scopes.", op, permission))
			}

			if patForbidden {
				problems = append(problems, fmt.Sprintf(
					"%s declares x-dkp-pat-forbidden: true but its permission %q is not in the capability "+
						"floor. Marking a session-only-by-omission operation pat-forbidden is a false "+
						"positive (decision record §U6: admin.settings is not in the floor).", op, permission))
			}
		}
	}

	return problems
}

// stringSlice coerces an Extensions value to a []string, accepting the []string the operations write
// and the []any a round-trip through the OpenAPI document would produce.
func stringSlice(v any) ([]string, bool) {
	switch s := v.(type) {
	case []string:
		return s, true
	case []any:
		out := make([]string, 0, len(s))
		for _, e := range s {
			str, ok := e.(string)
			if !ok {
				return nil, false
			}

			out = append(out, str)
		}

		return out, true
	default:
		return nil, false
	}
}

// TestArch_ScopeCoverageViolations_FiresPerCase proves the scope gate rejects each of the three
// cases when it is malformed, and accepts each when it is well-formed. Without a fixture per case, the
// gate could silently stop checking one of them and every real operation would still pass.
func TestArch_ScopeCoverageViolations_FiresPerCase(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		op         huma.Operation
		wantReject bool
		contains   string
	}{
		{
			name: "PAT-callable with no scopes is rejected",
			op: huma.Operation{
				OperationID: "listA", Method: http.MethodGet, Path: "/api/v1/a",
				Security:   []map[string][]string{{"pat": {"roster:read"}}, {"session": {}}},
				Extensions: map[string]any{ExtensionPermission: "roster.read"},
			},
			wantReject: true, contains: "declares no x-dkp-scopes",
		},
		{
			name: "PAT-callable with an unknown scope is rejected",
			op: huma.Operation{
				OperationID: "listB", Method: http.MethodGet, Path: "/api/v1/b",
				Security:   []map[string][]string{{"pat": {"roster:read"}}},
				Extensions: map[string]any{ExtensionPermission: "roster.read", ExtensionScopes: []string{"not:a:scope"}},
			},
			wantReject: true, contains: "not in authz.Scopes()",
		},
		{
			name: "floor operation without pat-forbidden is rejected",
			op: huma.Operation{
				OperationID: "mintC", Method: http.MethodPost, Path: "/api/v1/c",
				Security:   []map[string][]string{{"session": {}}},
				Extensions: map[string]any{ExtensionPermission: "token.mint"},
			},
			wantReject: true, contains: "does not declare x-dkp-pat-forbidden",
		},
		{
			name: "session-only-by-omission marked pat-forbidden is rejected",
			op: huma.Operation{
				OperationID: "updateD", Method: http.MethodPatch, Path: "/api/v1/d",
				Security: []map[string][]string{{"session": {}}},
				Extensions: map[string]any{
					ExtensionPermission: "admin.settings", ExtensionPATForbidden: true,
				},
			},
			wantReject: true, contains: "false positive",
		},
		{
			name: "PAT-callable with a valid scope is accepted",
			op: huma.Operation{
				OperationID: "listE", Method: http.MethodGet, Path: "/api/v1/e",
				Security:   []map[string][]string{{"pat": {"roster:read"}}, {"session": {}}},
				Extensions: map[string]any{ExtensionPermission: "roster.read", ExtensionScopes: []string{"roster:read"}},
			},
			wantReject: false,
		},
		{
			name: "floor operation with pat-forbidden and no scopes is accepted",
			op: huma.Operation{
				OperationID: "mintF", Method: http.MethodPost, Path: "/api/v1/f",
				Security:   []map[string][]string{{"session": {}}},
				Extensions: map[string]any{ExtensionPermission: "token.mint", ExtensionPATForbidden: true},
			},
			wantReject: false,
		},
		{
			name: "session-only-by-omission with neither is accepted",
			op: huma.Operation{
				OperationID: "updateG", Method: http.MethodPatch, Path: "/api/v1/g",
				Security:   []map[string][]string{{"session": {}}},
				Extensions: map[string]any{ExtensionPermission: "admin.settings"},
			},
			wantReject: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			problems := scopeCoverageViolations(fixtureRegistry(t, tc.op))

			if tc.wantReject {
				require.NotEmpty(t, problems, "the gate accepted a malformed operation")
				require.Contains(t, strings.Join(problems, "\n"), tc.contains)
			} else {
				require.Empty(t, problems, "the gate rejected a well-formed operation: %v", problems)
			}
		})
	}
}

// repoRoot returns the repository root, located by walking up to the directory holding go.mod.
//
// Not filepath.Abs("../.."), which silently produces the wrong answer the day this file moves.
func repoRoot(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	require.NoError(t, err, "getwd")

	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}

		parent := filepath.Dir(dir)
		require.NotEqual(t, parent, dir, "walked to the filesystem root without finding go.mod")

		dir = parent
	}
}
