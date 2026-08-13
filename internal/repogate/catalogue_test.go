package repogate

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRuleCatalogue_Decodes_EveryRuleTheEngineDocuments is the inventory, and it is what makes
// "losing a rule is visible in a diff" true rather than intended.
//
// The catalogue is one block per (rule, TREE), so the counts are load-bearing: MONEY002 covers db,
// internal and cmd, and the AGPL firewall covers four trees because a transcription can land in any
// of them. Dropping one tree from a rule is the quiet version of deleting the rule — the id still
// appears in the report, the file that would have failed is simply never read — and a test that only
// checked the ids would pass through it.
func TestRuleCatalogue_Decodes_EveryRuleTheEngineDocuments(t *testing.T) {
	t.Parallel()

	rules, err := textRules()
	require.NoError(t, err, "the embedded rule catalogue must decode")

	trees := make(map[string][]string, len(rules))
	for _, r := range rules {
		trees[r.id] = append(trees[r.id], r.tree)
	}

	for id, want := range map[string][]string{
		"MONEY002": {"db", "internal", "cmd"},
		"MONEY003": {"db"},
		"WEB001":   {"web/src"},
		"WEB002":   {"web/src"},
		"WEB003":   {"web/src"},
		"DS001":    {"web/src"},
		"DS002":    {"web/src"},
		"GOLD001":  {".github/workflows"},
		"PIN001":   {".github"},
		"QEMU001":  {".github/workflows"},
		"AGPL001":  {"internal", "web", "cmd", "db"},
		"AGPL002":  {"db"},
	} {
		require.Equalf(t, want, trees[id],
			"%s no longer covers the trees it is documented to cover. A rule that lost a tree still "+
				"appears in every report and simply never reads the file that would have failed.", id)
	}

	require.Len(t, trees, 12,
		"the catalogue gained or lost a rule id without this inventory being updated: %v", trees)
}

// TestDecodeRules_Malformed_IsAnErrorNeverAnEmptyCatalogue is the fail-closed fixture, and it is the
// property the whole package rests on.
//
// Every config-shaped rule in this repository lives in one file now: the money rules, the design
// tokens, law 4's text half, the supply-chain pins and the AGPL firewall. A decoder that answered a
// file it could not read with an empty slice would disable all of them AND print `repo gates
// passed`, which is precisely the vacuous green the whole test/repo package exists to prevent. So
// every malformed shape below has to be an ERROR, and none of them may be "no rules, no findings".
func TestDecodeRules_Malformed_IsAnErrorNeverAnEmptyCatalogue(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		body string
		want string
		why  string
	}{
		{
			name: "not HCL at all",
			body: "rule \"WEB001\" {\n  tree = \n}\n",
			want: "parse",
			why:  "a half-written catalogue — the state a bad merge leaves — must not read as zero rules",
		},
		{
			name: "an attribute the schema does not declare",
			body: "rule \"WEB001\" {\n  description = \"x\"\n  tree = \"web/src\"\n  patern = \"fetch\"\n}\n",
			want: "decode",
			why: "a typo'd attribute name would otherwise leave the rule with no pattern, which is the " +
				"same as no rule",
		},
		{
			name: "a rule with no pattern",
			body: "rule \"WEB001\" {\n  description = \"x\"\n  tree = \"web/src\"\n}\n",
			want: "decode",
			why:  "pattern is required; a rule that matches nothing is not a rule",
		},
		{
			name: "a pattern that is not a regexp",
			body: "rule \"WEB001\" {\n  description = \"x\"\n  tree = \"web/src\"\n  pattern = \"([\"\n}\n",
			want: "compile",
			why:  "an uncompilable pattern is a rule that cannot run, and a rule that cannot run is a failure",
		},
		{
			name: "a pattern that is only whitespace",
			body: "rule \"WEB001\" {\n  description = \"x\"\n  tree = \"web/src\"\n  pattern = \"  \"\n}\n",
			want: "empty pattern",
			why:  "an empty regexp matches every line, so it is a rule that fires on everything or nothing",
		},
		{
			name: "an empty catalogue",
			body: "// nothing here\n",
			want: "declares no rules",
			why: "an empty file is far more likely to be a mistake than a decision, and the mistake " +
				"disables every text rule at once",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rules, err := decodeRules([]byte(tc.body))

			require.Error(t, err, "%s", tc.why)
			require.Empty(t, rules, "a failed decode must yield no rules at all, never a partial catalogue")
			require.Contains(t, strings.ToLower(err.Error()), tc.want, "%v", err)
		})
	}
}

// TestReportTextRules_CatalogueFailure_IsAGateFailure is the other half: a catalogue that failed to
// load has to reach the REPORT as a red rule id, not as silence.
//
// This branch is unreachable from a fixture tree — the catalogue is embedded precisely so that a
// tainted tree cannot disarm the engine — so it is asserted here instead, where the error can be
// handed in directly.
func TestReportTextRules_CatalogueFailure_IsAGateFailure(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer

	rep := &report{w: &buf}
	reportTextRules(nil, errors.New("rules.hcl:12,3-8: Unsupported argument"), newScanner(t.TempDir()), rep)

	require.True(t, rep.finish(), "a catalogue that could not be read must fail the run\n%s", buf.String())
	require.Contains(t, buf.String(), "GATE000",
		"the failure must carry a rule id, in the same shape as a rule that fired\n%s", buf.String())
	require.Contains(t, buf.String(), "Unsupported argument",
		"the failure must quote what the decoder said, or the reader has nothing to fix\n%s", buf.String())
}
