package specgate

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/api"
)

// Unit tests for the rule engine.
//
// THESE ARE NOT THE NEGATIVE FIXTURES. Every rule's "a wrong spec is rejected, and the right one is
// still accepted" pair lives in test/repo/spec_gate_test.go, which drives the real command as a
// subprocess exactly as CI does; those fixtures predate this package and were carried over unchanged
// when the gate moved from Python (#127). What is here is the half a subprocess cannot see: the typed
// errors, the exit code Render returns, the ordering of the output, and the constants this package
// mirrors from internal/api — which was a comment saying "the two definitions must agree" for as long
// as one of the two was Python.

// writeSpec renders doc into tree/openapi/openapi.json and returns tree.
func writeSpec(t *testing.T, doc map[string]any) string {
	t.Helper()

	tree := t.TempDir()

	require.NoError(t, os.MkdirAll(filepath.Join(tree, "openapi"), 0o750))

	raw, err := json.Marshal(doc)
	require.NoError(t, err, "marshal the fixture spec")

	require.NoError(t, os.WriteFile(filepath.Join(tree, SpecFile), raw, 0o600))

	return tree
}

// conformingSpec is a document that passes every rule.
func conformingSpec() map[string]any {
	return map[string]any{
		"openapi": "3.1.0",
		"info":    map[string]any{"title": "Fixture", "version": "1.0.0"},
		"paths": map[string]any{
			"/api/v1/things": map[string]any{"get": map[string]any{
				"operationId":   "getThing",
				"security":      []any{},
				PermissionKey:   "public",
				"responses":     map[string]any{"200": map[string]any{"description": "OK"}},
				"x-dkp-fixture": "the walker must not choke on an unknown extension",
			}},
		},
	}
}

// rules returns the distinct rule ids in res, in the order they were reported.
func rules(res Result) []string {
	var out []string

	for _, v := range res.Violations {
		if !slices.Contains(out, v.Rule) {
			out = append(out, v.Rule)
		}
	}

	return out
}

// TestSpecGate_SentinelPermissions_MatchAPI turns a comment into a mechanism.
//
// SPEC005 resolves every permission that is NOT a sentinel against internal/authz/catalogue.go. If
// this list gains a value internal/api does not treat as a sentinel, that permission is required to
// exist in the catalogue by one half of the repository and required not to by the other. While this
// gate was Python the agreement could only be asserted in prose; both definitions are Go now.
func TestSpecGate_SentinelPermissions_MatchAPI(t *testing.T) {
	t.Parallel()

	require.Equal(t, api.SentinelPermissions(), SentinelPermissions(),
		"the gate's sentinel permissions have diverged from internal/api.SentinelPermissions(). A "+
			"value here that is not a sentinel there (or the reverse) makes SPEC005 demand a "+
			"catalogue entry for a key that must not have one, or skip one that must.")
}

// TestSpecGate_PermissionKey_MatchesAPI is the same argument for the extension key itself.
//
// A divergence here is the worst shape available: internal/api would emit the extension under one name
// and SPEC004 would look for another, so every operation would fail a rule that nothing in the spec
// could satisfy.
func TestSpecGate_PermissionKey_MatchesAPI(t *testing.T) {
	t.Parallel()

	require.Equal(t, api.ExtensionPermission, PermissionKey,
		"the gate looks for a different extension key than internal/api emits")
}

// TestCheck_MissingSpec_ReturnsErrSpecMissing pins the typed error rather than its text.
func TestCheck_MissingSpec_ReturnsErrSpecMissing(t *testing.T) {
	t.Parallel()

	_, err := Check(t.TempDir(), "")

	require.ErrorIs(t, err, ErrSpecMissing)
	require.Contains(t, err.Error(), "make gen",
		"the failure must name the command that fixes it")
}

// TestCheck_NoOperations_ReturnsErrVacuous closes the vacuous-pass hole.
//
// A document with no operations satisfies every per-operation rule by having nothing to check, so this
// is the one state in which the gate could report success without having looked at anything.
func TestCheck_NoOperations_ReturnsErrVacuous(t *testing.T) {
	t.Parallel()

	tree := writeSpec(t, map[string]any{
		"openapi": "3.1.0",
		"info":    map[string]any{"title": "Fixture", "version": "1.0.0"},
		"paths":   map[string]any{},
	})

	_, err := Check(tree, "")

	require.ErrorIs(t, err, ErrVacuous)
}

// TestCheck_InvalidJSON_IsAFailureNotAPass covers the third load failure.
func TestCheck_InvalidJSON_IsAFailureNotAPass(t *testing.T) {
	t.Parallel()

	tree := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tree, "openapi"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(tree, SpecFile), []byte("{not json"), 0o600))

	_, err := Check(tree, "")

	require.Error(t, err)
	require.Contains(t, err.Error(), "not valid JSON")
}

// TestCheck_EmptyBaseRef_SaysTheRenameCheckIsOff pins the wording, not just the behaviour.
//
// TestMakefile_VerifySpec_StripsBaseRefEnv in test/repo asserts the string "rename check is disabled"
// is ABSENT from the real `make verify-spec` output — that is how it proves a hostile
// DKP_SPEC_BASE_REF="" did not reach the gate. A reword here would make that fence silently
// unfalsifiable: it would pass because the phrase no longer exists anywhere, not because the strip
// worked. So the phrase is asserted where it is produced.
func TestCheck_EmptyBaseRef_SaysTheRenameCheckIsOff(t *testing.T) {
	t.Parallel()

	res, err := Check(writeSpec(t, conformingSpec()), "")
	require.NoError(t, err)
	require.Empty(t, res.Violations)
	require.Len(t, res.Notes, 1)
	require.Contains(t, string(res.Notes[0]), "rename check is disabled",
		"test/repo/spec_gate_test.go greps for this phrase to prove the fixture switch never reaches "+
			"production; renaming it without changing that test disarms the fence")
}

// TestCheck_UnresolvableBaseRef_IsAViolation is the other half of the same rule.
//
// A base revision that is not present must FAIL rather than skip. A shallow clone is the normal CI
// checkout, and silently passing on one would make SPEC003 vacuous in exactly the configuration most
// likely to have it.
func TestCheck_UnresolvableBaseRef_IsAViolation(t *testing.T) {
	t.Parallel()

	res, err := Check(writeSpec(t, conformingSpec()), "refs/dkp/no-such-revision")
	require.NoError(t, err)

	require.Contains(t, rules(res), "SPEC003")
	require.Contains(t, res.Violations[0].Message, "fetch-depth: 0",
		"the failure must name what fixes it in CI")
}

// TestCheck_ViolationOrder_IsStable is a Go-specific regression this gate's Python could not have.
//
// A JSON object decoded into a Python dict preserves document order; a Go map has none. Ranging a map
// anywhere in the rules would make two runs over one document report the same violations in a
// different sequence — so a contributor comparing two CI logs, or a reviewer diffing before and after,
// would see churn that means nothing. Every map iteration in this package is sorted; this is what
// notices when one stops being.
func TestCheck_ViolationOrder_IsStable(t *testing.T) {
	t.Parallel()

	// Several rules at once, over several paths and several fields, so the assertion covers the
	// orderings of operations(), the walker and the property maps together.
	doc := conformingSpec()
	doc["paths"] = map[string]any{
		"/api/v1/alpha": map[string]any{"get": map[string]any{"operationId": "Alpha"}},
		"/api/v1/bravo": map[string]any{"get": map[string]any{"operationId": "bravo"}},
		"/healthz":      map[string]any{"get": map[string]any{"operationId": "charlie"}},
		"/api/v1/delta": map[string]any{"post": map[string]any{"operationId": "bravo"}},
	}
	doc["components"] = map[string]any{"schemas": map[string]any{"Thing": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"alpha_centipoints": map[string]any{"type": "string"},
			"bravo_cp":          map[string]any{"type": "integer"},
			"charlie_ratio":     map[string]any{"type": "number"},
			"show_twinks":       map[string]any{"type": "boolean"},
		},
	}}}

	tree := writeSpec(t, doc)

	first, err := Check(tree, "")
	require.NoError(t, err)
	require.NotEmpty(t, first.Violations, "the fixture must actually fail, or this proves nothing")

	for range 20 {
		again, err := Check(tree, "")
		require.NoError(t, err)
		require.Equal(t, first.Violations, again.Violations,
			"two runs over one document reported violations in a different order — a map is being "+
				"ranged unsorted somewhere in the rules")
	}
}

// TestRender_Violations_NameTheirRuleAndExitOne pins the output contract test/repo greps.
func TestRender_Violations_NameTheirRuleAndExitOne(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer

	code := Render(&stdout, &stderr, Result{
		Operations: 1,
		Violations: []Violation{{Rule: "SPEC001", Message: "an example failure"}},
		Notes:      []Note{"an example note"},
	})

	require.Equal(t, 1, code)
	require.Contains(t, stderr.String(), "[SPEC001]",
		"the rule id must be bracketed: test/repo/spec_gate_test.go's firedRules regex is what makes "+
			"a gate that fires for the wrong reason distinguishable from one that fires for the right one")
	require.Contains(t, stderr.String(), "an example failure")
	require.Contains(t, stdout.String(), "an example note", "a note is not a failure and goes to stdout")
}

// TestRender_Conforming_ReportsTheCountAndExitsZero covers the success wording.
//
// The count is not decoration: "all conforming" over zero operations would be the vacuous pass, and
// TestMakefile_VerifySpec_StripsBaseRefEnv matches this sentence to prove the gate ran at all.
func TestRender_Conforming_ReportsTheCountAndExitsZero(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer

	code := Render(&stdout, &stderr, Result{Operations: 7})

	require.Zero(t, code)
	require.Contains(t, stdout.String(), "7 operation(s), all conforming")
	require.Empty(t, stderr.String())
}

// TestRun_ConformingSpec_ExitsZero is the wiring check between Check and Render.
func TestRun_ConformingSpec_ExitsZero(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer

	code := Run(writeSpec(t, conformingSpec()), "", &stdout, &stderr)

	require.Zero(t, code, "stdout: %s\nstderr: %s", stdout.String(), stderr.String())
	require.Contains(t, stdout.String(), "1 operation(s), all conforming")
}

// TestRun_MissingSpec_ExitsOne proves the load failures reach the exit code.
//
// Without this, Check could return ErrSpecMissing and Run could drop it, and the gate would pass on a
// tree with no spec at all — the failure mode that would make every fixture in test/repo pass while
// the gate checked nothing.
func TestRun_MissingSpec_ExitsOne(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer

	code := Run(t.TempDir(), "", &stdout, &stderr)

	require.Equal(t, 1, code)
	require.Contains(t, stderr.String(), "make gen")
}

// TestSchemaTypes_UnionForm_IsFlattened covers the OpenAPI 3.1 shape SPEC006 must understand.
//
// An optional integer is `"type": ["integer", "null"]`, not `"type": "integer"`. A rule that read only
// the scalar form would report every nullable money field as "money whose type is not an integer" —
// and the natural way to silence that is to loosen the rule.
func TestSchemaTypes_UnionForm_IsFlattened(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		schema map[string]any
		want   []string
	}{
		{name: "scalar", schema: map[string]any{"type": "integer"}, want: []string{"integer"}},
		{
			name:   "nullable union",
			schema: map[string]any{"type": []any{"integer", "null"}},
			want:   []string{"integer", "null"},
		},
		{name: "absent", schema: map[string]any{}, want: nil},
		{name: "not a string", schema: map[string]any{"type": 7}, want: nil},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tc.want, schemaTypes(tc.schema))
		})
	}
}

// TestDescribeType_Absent_ReadsAsAbsent keeps a failure message honest.
//
// A message reading "its type is <nil>" sends the reader looking for a null they never wrote.
func TestDescribeType_Absent_ReadsAsAbsent(t *testing.T) {
	t.Parallel()

	require.Equal(t, "absent", describeType(map[string]any{}))
	require.Equal(t, `"string"`, describeType(map[string]any{"type": "string"}))
	require.Equal(t, `["integer", "null"]`,
		describeType(map[string]any{"type": []any{"integer", "null"}}))
}
