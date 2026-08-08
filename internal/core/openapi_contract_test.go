package core_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestOpenAPI_CentipointsFields_AreIntegerNeverString is the JSON-Schema contract test from the
// acceptance criteria (canonical §1): every `*_centipoints` field in the committed openapi.json must
// have type "integer" and never "string".
//
// It is written to be correct whether or not such a field exists yet. Today there is none — the
// money-bearing resources arrive with 5a's guild fields and PR 9's ledger — so the test walks the
// whole document, collects every property whose name ends in `_centipoints`, and asserts the type of
// each. Zero fields is a pass, not a skip: the test becomes load-bearing the instant the first money
// field lands, and a "skip when none found" version would silently stop guarding at exactly that
// moment. TestOpenAPI_ContractTest_DetectsAStringCentipoints below proves the walker actually fails
// on a violation, so the vacuous pass is not hiding a broken test.
func TestOpenAPI_CentipointsFields_AreIntegerNeverString(t *testing.T) {
	t.Parallel()

	doc := readSpec(t)

	fields := collectCentipointsFields(doc, "")

	for path, schema := range fields {
		requireIntegerType(t, path, schema)
	}
}

// TestOpenAPI_ContractTest_DetectsAStringCentipoints is the negative control: a synthetic schema
// with a string-typed `_centipoints` field must be caught by the same walker. Without it, the test
// above is indistinguishable from one that never checks anything — the classic vacuous-pass trap for
// a contract that has no subjects yet.
func TestOpenAPI_ContractTest_DetectsAStringCentipoints(t *testing.T) {
	t.Parallel()

	// A hand-built document shaped like openapi.json, with one offending field.
	var doc map[string]any
	require.NoError(t, json.Unmarshal([]byte(`{
	  "components": {
	    "schemas": {
	      "Bad": {
	        "type": "object",
	        "properties": {
	          "value_centipoints": { "type": "string" },
	          "good_centipoints":  { "type": "integer" }
	        }
	      }
	    }
	  }
	}`), &doc))

	fields := collectCentipointsFields(doc, "")
	require.Len(t, fields, 2, "the walker must find both centipoints fields")

	// Assert the PREDICATE the real test enforces, on both fields — so this proves the enforcement
	// itself would catch a string, not merely that the walker classifies types. The good field is
	// accepted; the bad field is rejected AND the reason is that it is a string, not some unrelated
	// failure.
	goodOK, goodType := centipointsTypeOK(fields["components.schemas.Bad.properties.good_centipoints"])
	require.True(t, goodOK, "an integer-typed centipoints field must pass, got %q", goodType)

	badOK, badType := centipointsTypeOK(fields["components.schemas.Bad.properties.value_centipoints"])
	require.False(t, badOK, "a string-typed centipoints field must be rejected by the predicate")
	require.Equal(t, "string", badType,
		"the negative control must actually be a string, or it proves nothing")
}

// readSpec loads the committed openapi/openapi.json from the repository root.
func readSpec(t *testing.T) map[string]any {
	t.Helper()

	root := repoRootDir(t)
	path := filepath.Join(root, "openapi", "openapi.json")

	raw, err := os.ReadFile(path)
	require.NoError(t, err, "read committed spec at %s", path)

	var doc map[string]any
	require.NoError(t, json.Unmarshal(raw, &doc), "parse openapi.json")

	return doc
}

// repoRootDir returns the git working-tree root, so the test finds openapi.json regardless of which
// package directory `go test` runs from.
func repoRootDir(t *testing.T) string {
	t.Helper()

	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	require.NoError(t, err, "locate repo root")

	return strings.TrimSpace(string(out))
}

// collectCentipointsFields walks any decoded-JSON value and returns every property whose KEY ends in
// `_centipoints`, mapped from a dotted path to its schema object.
//
// It recurses through both maps and arrays, so it finds fields wherever they appear — a top-level
// component, a nested object, an array's `items` schema, a `$defs` block — because the contract is
// "every _centipoints field, everywhere", not "every field in components.schemas".
func collectCentipointsFields(v any, path string) map[string]map[string]any {
	found := make(map[string]map[string]any)

	switch node := v.(type) {
	case map[string]any:
		// If this node is a `properties` object, its keys are field names.
		if props, ok := node["properties"].(map[string]any); ok {
			for name, sch := range props {
				if schemaObj, ok := sch.(map[string]any); ok && strings.HasSuffix(name, "_centipoints") {
					found[joinPath(path, "properties."+name)] = schemaObj
				}
			}
		}

		for k, child := range node {
			for p, s := range collectCentipointsFields(child, joinPath(path, k)) {
				found[p] = s
			}
		}
	case []any:
		for i, child := range node {
			for p, s := range collectCentipointsFields(child, joinPath(path, indexKey(i))) {
				found[p] = s
			}
		}
	}

	return found
}

func joinPath(a, b string) string {
	if a == "" {
		return b
	}

	return a + "." + b
}

func indexKey(i int) string {
	return "[" + itoa(i) + "]"
}

// itoa avoids importing strconv for a single call in a path label.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}

	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}

	return string(b)
}

// schemaType returns the schema's declared type as a string. In OpenAPI 3.1 `type` may be a single
// string or an array of strings (`["integer","null"]` for a nullable field); this returns the first
// non-"null" member so a nullable money field is judged by its real type.
func schemaType(schema map[string]any) string {
	switch tv := schema["type"].(type) {
	case string:
		return tv
	case []any:
		for _, m := range tv {
			if s, ok := m.(string); ok && s != "null" {
				return s
			}
		}
	}

	return ""
}

// centipointsTypeOK is the enforcement predicate: a centipoints field is legal iff its declared type
// is "integer" (canonical §1). It returns the observed type too, so a caller can report and — in the
// negative-control test — assert that a rejection is specifically because the type is "string", the
// exact mistake the security design once shipped (`"value_centipoints": "35000"`).
//
// The real test (requireIntegerType) and the negative control both go through this one function, so
// the control genuinely exercises the enforcement rather than a parallel re-implementation of it.
func centipointsTypeOK(schema map[string]any) (ok bool, got string) {
	got = schemaType(schema)

	return got == "integer", got
}

// requireIntegerType asserts a centipoints field's type is integer (canonical §1), naming string
// explicitly when that is the failure so the message points at the classic mistake.
func requireIntegerType(t *testing.T, path string, schema map[string]any) {
	t.Helper()

	ok, got := centipointsTypeOK(schema)
	require.NotEqual(t, "string", got,
		"%s is typed string; money on the wire is an unquoted integer, never a string (canonical §1)", path)
	require.True(t, ok,
		"%s must be type integer (canonical §1), got %q", path, got)
}
