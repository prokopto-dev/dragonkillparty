package repo_test

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
)

// Negative fixtures for scripts/verify-spec.py.
//
// Every test here builds an openapi/openapi.json that should fail one rule and requires that the
// gate says so, naming the rule. The positive direction — this repository's real spec passes — is
// covered by `make check` on every run and by TestSpecGate_RealSpec_Passes below.
//
// The reason this file exists at all: verify-spec.py is a merge-blocking gate whose failure modes
// are all "the spec was wrong in a way regenerating cannot fix". Nobody exercises those by accident,
// so without fixtures the gate would be a script everybody trusts and nobody has seen work.

// specRuleLine matches the rule ids verify-spec.py prints, e.g. "  [SPEC001] ...".
var specRuleLine = regexp.MustCompile(`\[(SPEC\d{3})\]`)

// firedRules returns the distinct rule ids named in the gate's output.
func firedRules(output string) []string {
	var out []string

	seen := make(map[string]bool)

	for _, m := range specRuleLine.FindAllStringSubmatch(output, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true

			out = append(out, m[1])
		}
	}

	return out
}

// runSpecGate runs verify-spec.py against tree.
//
// A local runner rather than gates_test.go's runGateScript, because that one invokes `bash` and this
// gate is Python — python3 being the parsing tool this repository already depends on
// (scripts/check-links.py), so the gate needs no new pin and runs on both Ubuntu CI and a macOS
// laptop, which `jq` would not.
//
// DKP_SPEC_BASE_REF is set empty on purpose: the fixture trees have no git history, so the
// operationId-rename check has nothing to compare against and would fail every fixture for a reason
// none of them is testing. TestSpecGate_RenamedOperationID_IsRejected covers that rule against a
// real git repository, and TestMakefile_VerifySpec_StripsBaseRefEnv asserts production strips this.
// Skipped under -short, and only under -short, for the same reason licence_gate_test.go skips its
// fixtures: each case pays a python3 interpreter start of roughly 80 ms, and the ~20 cases here put
// `make test-unit` at the < 5 s budget in AGENTS.md. Raising that budget to fit these would be
// moving the goalpost; -short is the split the repo already has for exactly this.
//
// They are NOT excluded from anything that gates a merge. `make check` runs `make test`, not
// `make test-unit`; CI's test / integration job does the same; and `gen / spec-drift` runs the gate
// itself against the real tree on every PR.
func runSpecGate(t *testing.T, tree string) (output string, exitCode int) {
	t.Helper()

	if testing.Short() {
		t.Skip("spec-gate fixtures shell out to python3; run `make test` or `make check`")
	}

	require.NotEmpty(t, tree, "DKP_REPO_ROOT must not be empty — the script falls back to the real repo")
	require.True(t, filepath.IsAbs(tree), "DKP_REPO_ROOT must be absolute, got %q", tree)

	cmd := exec.Command("python3", scriptPath(t, "verify-spec.py"))
	cmd.Env = append(os.Environ(), "DKP_REPO_ROOT="+tree, "DKP_SPEC_BASE_REF=")

	out, err := cmd.CombinedOutput()
	if err == nil {
		return string(out), 0
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return string(out), exitErr.ExitCode()
	}

	t.Fatalf("run verify-spec.py: %v\n%s", err, out)

	return "", 0
}

// writeSpec renders doc into tree/openapi/openapi.json.
func writeSpec(t *testing.T, tree string, doc map[string]any) {
	t.Helper()

	dir := filepath.Join(tree, "openapi")
	require.NoError(t, os.MkdirAll(dir, 0o750), "create the fixture openapi directory")

	raw, err := json.MarshalIndent(doc, "", "  ")
	require.NoError(t, err, "marshal the fixture spec")

	require.NoError(t, os.WriteFile(filepath.Join(dir, "openapi.json"), raw, 0o600),
		"write the fixture spec")
}

// conformingOperation is a minimal operation that passes every rule. Fixtures below start from a
// copy and break exactly one thing, so a failure names the thing that was broken rather than the
// six things the fixture never got right.
func conformingOperation() map[string]any {
	return map[string]any{
		"operationId":      "getThing",
		"summary":          "Get a thing",
		"security":         []any{},
		"x-dkp-permission": "public",
		"responses": map[string]any{
			"200": map[string]any{"description": "OK"},
		},
	}
}

// conformingSpec is a whole document that passes every rule.
func conformingSpec() map[string]any {
	return map[string]any{
		"openapi": "3.1.0",
		"info":    map[string]any{"title": "Fixture", "version": "1.0.0"},
		"paths": map[string]any{
			"/api/v1/things": map[string]any{"get": conformingOperation()},
		},
	}
}

// TestSpecGate_ConformingSpec_Passes is the control.
//
// Without it, every negative test below would pass just as happily against a gate that failed
// unconditionally — which is the most likely way for this file to become worthless.
func TestSpecGate_ConformingSpec_Passes(t *testing.T) {
	t.Parallel()

	tree := t.TempDir()
	writeSpec(t, tree, conformingSpec())

	out, code := runSpecGate(t, tree)

	require.Zero(t, code, "a conforming spec must pass\n%s", out)
	require.Empty(t, firedRules(out), "%s", out)
}

// TestSpecGate_EmptySpec_IsRejected closes the vacuous-pass hole.
//
// A document with no operations satisfies every per-operation rule by having nothing to check. That
// is the one state in which this gate could report success without having looked at anything, and
// it is reachable: `dkp openapi` against a build where registration was accidentally removed emits
// exactly this.
func TestSpecGate_EmptySpec_IsRejected(t *testing.T) {
	t.Parallel()

	tree := t.TempDir()
	writeSpec(t, tree, map[string]any{
		"openapi": "3.1.0",
		"info":    map[string]any{"title": "Fixture", "version": "1.0.0"},
		"paths":   map[string]any{},
	})

	out, code := runSpecGate(t, tree)

	require.NotZero(t, code, "a spec with no operations passed, so the gate can pass vacuously\n%s", out)
	require.Contains(t, out, "vacuously", "%s", out)
}

// TestSpecGate_MissingSpec_IsRejected covers the gate being pointed at a tree with no spec.
func TestSpecGate_MissingSpec_IsRejected(t *testing.T) {
	t.Parallel()

	out, code := runSpecGate(t, t.TempDir())

	require.NotZero(t, code, "a missing spec passed\n%s", out)
	require.Contains(t, out, "make gen", "the failure must name the command that fixes it\n%s", out)
}

// TestSpecGate_OperationIDViolations_AreRejected covers SPEC001 and SPEC002.
//
// An operationId is public API — both SDK generators derive a method name from it. A missing one
// produces a method named after whatever was inferred, a non-lowerCamelCase one produces a method
// name that does not match the house convention in the generated SDK, and a duplicate produces two
// methods with one name.
func TestSpecGate_OperationIDViolations_AreRejected(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		want string
		spec func() map[string]any
	}{
		{
			name: "missing operationId",
			want: "SPEC001",
			spec: func() map[string]any {
				doc := conformingSpec()
				op := conformingOperation()
				delete(op, "operationId")
				doc["paths"] = map[string]any{"/api/v1/things": map[string]any{"get": op}}

				return doc
			},
		},
		{
			name: "PascalCase operationId",
			want: "SPEC001",
			spec: func() map[string]any {
				doc := conformingSpec()
				op := conformingOperation()
				op["operationId"] = "GetThing"
				doc["paths"] = map[string]any{"/api/v1/things": map[string]any{"get": op}}

				return doc
			},
		},
		{
			name: "snake_case operationId",
			want: "SPEC001",
			spec: func() map[string]any {
				doc := conformingSpec()
				op := conformingOperation()
				op["operationId"] = "get_thing"
				doc["paths"] = map[string]any{"/api/v1/things": map[string]any{"get": op}}

				return doc
			},
		},
		{
			name: "duplicate operationId across two paths",
			want: "SPEC002",
			spec: func() map[string]any {
				doc := conformingSpec()
				doc["paths"] = map[string]any{
					"/api/v1/things": map[string]any{"get": conformingOperation()},
					"/api/v1/others": map[string]any{"get": conformingOperation()},
				}

				return doc
			},
		},
		{
			name: "duplicate operationId between a path and a webhook",
			want: "SPEC002",
			spec: func() map[string]any {
				doc := conformingSpec()
				doc["webhooks"] = map[string]any{
					"thing.happened": map[string]any{"post": conformingOperation()},
				}

				return doc
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tree := t.TempDir()
			writeSpec(t, tree, tc.spec())

			out, code := runSpecGate(t, tree)

			require.NotZero(t, code, "the gate accepted the fixture\n%s", out)
			require.Contains(t, firedRules(out), tc.want,
				"expected %s to fire; the gate reported %v\n%s", tc.want, firedRules(out), out)
		})
	}
}

// TestSpecGate_MissingSecurityOrPermission_IsRejected covers SPEC004.
//
// The `security` case is the subtle one and the reason this test names it explicitly: an OMITTED
// security key means "inherit the document-level requirement", while an explicitly EMPTY array means
// "this operation needs no credential". They are opposite meanings, one of them is falsy, and a gate
// written with a truthiness check would reject every public endpoint — pushing the next author into
// omitting the field, which is the failure the gate exists to prevent.
func TestSpecGate_MissingSecurityOrPermission_IsRejected(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(op map[string]any)
	}{
		{name: "no security key", mutate: func(op map[string]any) { delete(op, "security") }},
		{name: "no x-dkp-permission", mutate: func(op map[string]any) { delete(op, "x-dkp-permission") }},
		{name: "empty x-dkp-permission", mutate: func(op map[string]any) { op["x-dkp-permission"] = "" }},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			doc := conformingSpec()
			op := conformingOperation()
			tc.mutate(op)
			doc["paths"] = map[string]any{"/api/v1/things": map[string]any{"get": op}}

			tree := t.TempDir()
			writeSpec(t, tree, doc)

			out, code := runSpecGate(t, tree)

			require.NotZero(t, code, "the gate accepted the fixture\n%s", out)
			require.Contains(t, firedRules(out), "SPEC004", "%s", out)
		})
	}
}

// TestSpecGate_ExplicitEmptySecurity_IsAccepted is the counterweight to the test above.
//
// It is the assertion that stops SPEC004 from being "fixed" into a truthiness check. Delete it and
// the next person who sees `if not op.get("security")` will simplify it, every public operation will
// start failing, and the workaround they reach for will be to omit the field.
func TestSpecGate_ExplicitEmptySecurity_IsAccepted(t *testing.T) {
	t.Parallel()

	doc := conformingSpec()
	op := conformingOperation()
	op["security"] = []any{}
	doc["paths"] = map[string]any{"/api/v1/things": map[string]any{"get": op}}

	tree := t.TempDir()
	writeSpec(t, tree, doc)

	out, code := runSpecGate(t, tree)

	require.Zero(t, code,
		"an explicitly empty `security: []` is how a public operation declares itself "+
			"(docs/design/02-api-design.md:144) and must be accepted\n%s", out)
}

// TestSpecGate_UnknownPermission_IsRejected covers SPEC005.
//
// `role_permission` is FK-constrained to `permission(key)`, so a permission key with no catalogue
// entry is a BOOT FAILURE rather than a 403 — the instance refuses to start. Catching it in CI is
// the difference between a red build and a guild's site being down after an upgrade.
func TestSpecGate_UnknownPermission_IsRejected(t *testing.T) {
	t.Parallel()

	doc := conformingSpec()
	op := conformingOperation()
	op["x-dkp-permission"] = "raid.tick.invented"
	doc["paths"] = map[string]any{"/api/v1/things": map[string]any{"get": op}}

	tree := t.TempDir()
	writeSpec(t, tree, doc)

	out, code := runSpecGate(t, tree)

	require.NotZero(t, code, "the gate accepted a permission with no catalogue\n%s", out)
	require.Contains(t, firedRules(out), "SPEC005", "%s", out)
	require.Contains(t, out, "internal/authz/catalogue.go",
		"the failure must name the file that would have to contain the key\n%s", out)
}

// TestSpecGate_PermissionInCatalogue_IsAccepted proves SPEC005 resolves rather than always failing.
//
// Without it the rule could be implemented as "any non-sentinel permission fails" and every test
// above would still pass — which would block PR 5 on a gate that cannot be satisfied.
func TestSpecGate_PermissionInCatalogue_IsAccepted(t *testing.T) {
	t.Parallel()

	tree := t.TempDir()

	doc := conformingSpec()
	op := conformingOperation()
	op["x-dkp-permission"] = "roster.read"
	doc["paths"] = map[string]any{"/api/v1/things": map[string]any{"get": op}}
	writeSpec(t, tree, doc)

	authz := filepath.Join(tree, "internal", "authz")
	require.NoError(t, os.MkdirAll(authz, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(authz, "catalogue.go"),
		[]byte("package authz\n\nvar Keys = []string{\"roster.read\"}\n"), 0o600))

	out, code := runSpecGate(t, tree)

	require.Zero(t, code, "a permission present in the catalogue must be accepted\n%s", out)
}

// TestSpecGate_MoneyViolations_AreRejected covers SPEC006.
//
// Canonical conventions §1: point arithmetic is Centipoints (int64) only — not in Go, not in SQL,
// and not on the wire. A float on the wire is the specific failure that makes a ledger disagree with
// itself, because JSON numbers round-trip through IEEE-754 doubles in every client language, and it
// is invisible until a guild's balances stop adding up.
func TestSpecGate_MoneyViolations_AreRejected(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		field string
		typ   string
	}{
		{name: "money as string", field: "value_centipoints", typ: "string"},
		{name: "money as float", field: "value_centipoints", typ: "number"},
		{name: "a float that is not money", field: "attendance_ratio", typ: "number"},
		{name: "SQL suffix on the wire", field: "value_cp", typ: "integer"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			doc := conformingSpec()
			doc["components"] = map[string]any{
				"schemas": map[string]any{
					"Thing": map[string]any{
						"type": "object",
						"properties": map[string]any{
							tc.field: map[string]any{"type": tc.typ},
						},
					},
				},
			}

			tree := t.TempDir()
			writeSpec(t, tree, doc)

			out, code := runSpecGate(t, tree)

			require.NotZero(t, code, "the gate accepted %s: %s\n%s", tc.field, tc.typ, out)
			require.Contains(t, firedRules(out), "SPEC006", "%s", out)
		})
	}
}

// TestSpecGate_MoneyViolationInAParameter_IsRejected covers the parameter half of SPEC006.
//
// A query, header or path parameter is a different JSON shape from a body field —
// {"name": ..., "in": ..., "schema": {...}} rather than an entry under a `properties` map — and the
// first version of this gate walked only `properties`, so it never saw one. A review caught it
// before any parameter existed to slip through; this fixture is what stops it regressing.
//
// The case is not hypothetical: the first collection endpoint with a `?min_value_centipoints=`
// filter is exactly the shape canonical §1 exists to protect, and a float there corrupts a ledger
// the same way a float in a body does.
func TestSpecGate_MoneyViolationInAParameter_IsRejected(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		param string
		typ   string
	}{
		{name: "money parameter as float", param: "min_value_centipoints", typ: "number"},
		{name: "money parameter as string", param: "min_value_centipoints", typ: "string"},
		{name: "SQL suffix on a parameter", param: "min_value_cp", typ: "integer"},
		{name: "float parameter that is not money", param: "weight", typ: "number"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			doc := conformingSpec()
			op := conformingOperation()
			op["parameters"] = []any{
				map[string]any{
					"name":   tc.param,
					"in":     "query",
					"schema": map[string]any{"type": tc.typ},
				},
			}
			doc["paths"] = map[string]any{"/api/v1/things": map[string]any{"get": op}}

			tree := t.TempDir()
			writeSpec(t, tree, doc)

			out, code := runSpecGate(t, tree)

			require.NotZero(t, code,
				"the gate accepted parameter %s: %s\n%s", tc.param, tc.typ, out)
			require.Contains(t, firedRules(out), "SPEC006", "%s", out)
			require.Contains(t, out, tc.param,
				"the failure must name the offending parameter\n%s", out)
		})
	}
}

// TestSpecGate_LegitimateParameter_IsAccepted is the counterweight.
//
// Without it, the parameter check could be implemented as "any parameter fails" and every case above
// would still pass — blocking the first endpoint that takes a query parameter at all.
func TestSpecGate_LegitimateParameter_IsAccepted(t *testing.T) {
	t.Parallel()

	doc := conformingSpec()
	op := conformingOperation()
	op["parameters"] = []any{
		map[string]any{"name": "limit", "in": "query", "schema": map[string]any{"type": "integer"}},
		map[string]any{"name": "cursor", "in": "query", "schema": map[string]any{"type": "string"}},
		map[string]any{
			"name": "min_value_centipoints", "in": "query",
			"schema": map[string]any{"type": "integer"},
		},
	}
	doc["paths"] = map[string]any{"/api/v1/things": map[string]any{"get": op}}

	tree := t.TempDir()
	writeSpec(t, tree, doc)

	out, code := runSpecGate(t, tree)

	require.Zero(t, code,
		"an integer money parameter and the shared pagination parameters must all be accepted\n%s", out)
}

// TestSpecGate_UnversionedPath_IsRejected covers SPEC007.
//
// Canonical §7 puts every operation under /api/v1 and puts /healthz, /readyz, /metrics, the OAuth
// callback and the compat shim outside it AND out of the document, as Hidden operations. So an
// unversioned path appearing in the document means one of those five was registered without Hidden
// and is now published API — the no-hidden-operations rule seen from the other side.
func TestSpecGate_UnversionedPath_IsRejected(t *testing.T) {
	t.Parallel()

	doc := conformingSpec()
	doc["paths"] = map[string]any{"/healthz": map[string]any{"get": conformingOperation()}}

	tree := t.TempDir()
	writeSpec(t, tree, doc)

	out, code := runSpecGate(t, tree)

	require.NotZero(t, code, "the gate accepted an unversioned documented path\n%s", out)
	require.Contains(t, firedRules(out), "SPEC007", "%s", out)
}

// TestSpecGate_RenamedOperationID_IsRejected covers SPEC003 against a real git repository.
//
// A rename is the nastiest change this gate catches: it breaks the generated method name in every
// SDK while leaving the HTTP surface byte-identical, so neither the drift gate nor a reviewer
// reading the HTTP diff would notice. It is also the only rule here that needs history, which is why
// this fixture builds an actual repository rather than a directory.
func TestSpecGate_RenamedOperationID_IsRejected(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		// The only test in this file that shells out to git for four commands. `make test-unit` is
		// close to its budget; this still runs under `make test`, `make check` and CI's
		// test / integration job.
		t.Skip("builds a real git repository; run `make test` or `make check`")
	}

	tree := t.TempDir()

	git := func(args ...string) {
		t.Helper()

		cmd := exec.Command("git", args...)
		cmd.Dir = tree
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")

		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}

	git("init", "--quiet", "--initial-branch=main")

	writeSpec(t, tree, conformingSpec())
	git("add", ".")
	git("commit", "--quiet", "-m", "the spec before the rename")

	// origin/main without a remote: a local ref in the remote-tracking namespace is exactly what the
	// gate resolves, and it avoids a network fetch or a second clone.
	git("update-ref", "refs/remotes/origin/main", "HEAD")

	renamed := conformingSpec()
	renamed["paths"].(map[string]any)["/api/v1/things"].(map[string]any)["get"].(map[string]any)["operationId"] = "fetchThing"
	writeSpec(t, tree, renamed)

	cmd := exec.Command("python3", scriptPath(t, "verify-spec.py"))
	cmd.Dir = tree
	// DKP_SPEC_BASE_REF is left at its default so the real origin/main path is exercised.
	cmd.Env = append(os.Environ(), "DKP_REPO_ROOT="+tree)

	out, err := cmd.CombinedOutput()

	require.Error(t, err, "the gate accepted a renamed operationId\n%s", out)
	require.Contains(t, firedRules(string(out)), "SPEC003", "%s", out)
	require.Contains(t, string(out), "getThing", "the failure must name the old id\n%s", out)
	require.Contains(t, string(out), "fetchThing", "the failure must name the new id\n%s", out)
}

// TestSpecGate_RealSpec_Passes is the whole-repository control.
//
// Every other test here runs against a fixture. This one runs the gate the way CI runs it, so a rule
// that is correct against synthetic documents and wrong against the real one cannot hide.
func TestSpecGate_RealSpec_Passes(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("shells out to python3 and git; run `make test` or `make check`")
	}

	root := repoRoot(t)

	cmd := exec.Command("python3", scriptPath(t, "verify-spec.py"))
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "DKP_REPO_ROOT="+root)

	out, err := cmd.CombinedOutput()

	require.NoError(t, err, "this repository's own spec must pass the gate\n%s", out)
	require.Empty(t, firedRules(string(out)), "%s", out)
}

// TestMakefile_VerifySpec_StripsBaseRefEnv fences the one switch that can weaken this gate.
//
// DKP_SPEC_BASE_REF="" disables the operationId-rename check. It exists so the fixtures above can
// run against trees with no git history, and it must never reach the real invocation — a value
// leaking in from a developer's shell or a CI environment would turn a merge-blocking rule green
// while every other rule kept passing, which is the least visible way for a gate to stop working.
//
// The same shape as TestLintRepo_HostileRepoRootEnv_StillScansTheRealTree and
// TestMakefile_LicenceGate_StripsRepoRootEnv: set the hostile value, run the real target, and assert
// on the discriminating output rather than on the exit code.
func TestMakefile_VerifySpec_StripsBaseRefEnv(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("invokes make and python3; run `make test` or `make check`")
	}

	cmd := exec.Command("make", "verify-spec")
	cmd.Dir = repoRoot(t)
	cmd.Env = append(os.Environ(), "DKP_SPEC_BASE_REF=")

	out, err := cmd.CombinedOutput()

	require.NoError(t, err, "make verify-spec must pass on the real tree:\n%s", out)
	require.NotContains(t, string(out), "rename check is disabled",
		"the rename check was disabled by an environment variable — is `env -u DKP_SPEC_BASE_REF` "+
			"still on the verify-spec recipe in the Makefile?\n%s", out)
}
