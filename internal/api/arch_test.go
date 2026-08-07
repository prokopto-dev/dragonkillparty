package api

import (
	"context"
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

	seen := make(map[string]registeredOp)

	for _, op := range registeredOperations(t) {
		require.NotEmpty(t, op.Op.OperationID,
			"%s declares no OperationID. It must be explicit — never auto-derived, never renamed.", op)

		require.True(t, lowerCamelCase(op.Op.OperationID),
			"%s has OperationID %q, which is not lowerCamelCase (canonical §16: verb + resource, "+
				"e.g. createRaidTick)", op, op.Op.OperationID)

		if prior, dup := seen[op.Op.OperationID]; dup {
			require.Failf(t, "duplicate OperationID",
				"%q is declared by both %s and %s; the SDK generators would emit two methods with "+
					"one name", op.Op.OperationID, prior, op)
		}

		seen[op.Op.OperationID] = op
	}
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
				"declaring its permission there passes nothing and fails scripts/verify-spec.py.",
			op, ExtensionPermission)

		require.NotEmpty(t, permission, "%s declares an empty %s", op, ExtensionPermission)

		if slices.Contains(SentinelPermissions(), permission) {
			continue
		}

		// A non-sentinel permission must resolve in the generated catalogue. That file does not
		// exist yet and PR 4 does not create one, because `role_permission` is FK-constrained to
		// `permission(key)` and inventing a key is a boot failure, not a 403. The first operation
		// that needs a real permission — PR 5's /api/v1/guild — brings the catalogue with it.
		require.FileExistsf(t, filepath.Join(repoRoot(t), "internal", "authz", "catalogue.go"),
			"%s names permission %q, which is not a sentinel, but internal/authz/catalogue.go does "+
				"not exist. Adding a permission key is a schema change — see "+
				".claude/rules/api-endpoints.md's \"Stop and ask if\".", op, permission)
	}
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
// The prefix list is canonical §7's, cross-checked against .claude/rules/api-endpoints.md:140. Note
// that internal/api/EXAMPLE_ENDPOINT.md:231 writes `/bid-sessions` where the rule file writes
// `/bids`; both are listed here rather than picking one, because a fence that is too wide costs an
// author one explicit header and a fence that is too narrow costs a guild its points.
func TestArch_MutatingPost_RequiresIdempotencyKey(t *testing.T) {
	t.Parallel()

	fenced := []string{
		"/api/v1/raids", "/api/v1/awards", "/api/v1/adjustments",
		"/api/v1/bids", "/api/v1/bid-sessions", "/api/v1/raid-submissions", "/api/v1/ledger",
	}

	for _, op := range registeredOperations(t) {
		if op.Method != http.MethodPost {
			continue
		}

		if !slices.ContainsFunc(fenced, func(p string) bool { return strings.HasPrefix(op.Path, p) }) {
			continue
		}

		var found bool

		for _, param := range op.Op.Parameters {
			if param != nil && strings.EqualFold(param.Name, "Idempotency-Key") && param.In == "header" {
				found = param.Required
			}
		}

		require.Truef(t, found,
			"%s creates domain state under a fenced prefix but does not require an Idempotency-Key "+
				"header. Add an IdempotencyKey field to its input struct, tagged as a required "+
				"Idempotency-Key header.", op)
	}
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

// TestArch_MissingOperationID_FailsBuild proves the operationId assertion fires.
//
// Named by docs/development/first-ten-prs.md PR 4 as "deleting OperationID from getMeta fails CI".
// It is done against a throwaway registry rather than by mutating getMeta, for the reason every
// negative fixture in test/repo is: making the real tree fail to prove a gate works leaves the repo
// broken for everyone, and a test that passes only because somebody remembered to undo the damage is
// not a test.
func TestArch_MissingOperationID_FailsBuild(t *testing.T) {
	t.Parallel()

	api := humago.New(http.NewServeMux(), humaConfig())

	huma.Register(api, huma.Operation{
		// OperationID deliberately absent — this is the fixture.
		Method:     http.MethodGet,
		Path:       "/api/v1/nameless",
		Summary:    "An operation whose author forgot the one field that is public API",
		Security:   []map[string][]string{},
		Extensions: map[string]any{ExtensionPermission: PermissionPublic},
	}, func(_ context.Context, _ *struct{}) (*MetaOutput, error) { return &MetaOutput{}, nil })

	op := api.OpenAPI().Paths["/api/v1/nameless"]
	require.NotNil(t, op, "the fixture operation did not register")
	require.NotNil(t, op.Get)

	require.Empty(t, op.Get.OperationID,
		"Huma auto-derived an OperationID for an operation that declared none. If that is now the "+
			"library's behaviour, the assertion in "+
			"TestArch_OperationIDs_AreExplicitUniqueAndLowerCamelCase can no longer catch a missing "+
			"id and must be replaced with a source-level check.")
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
