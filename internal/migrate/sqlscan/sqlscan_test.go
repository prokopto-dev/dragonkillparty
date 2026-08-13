package sqlscan_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/migrate/sqlscan"
)

// TestDownBlockStart_MatchesWholeLinesOnly pins the definition MIG001 scopes itself with.
//
// The marker is a whole line, exactly as goose reads it and exactly as migrationfmt truncates at it.
// A line that merely CONTAINS the text — a comment about the marker, a string holding it — is not
// one, and a rule that thought otherwise would move the boundary of the Down block to wherever
// somebody mentioned it.
func TestDownBlockStart_MatchesWholeLinesOnly(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		body string
		want int
	}{
		{
			name: "the marker",
			body: "-- +goose Up\nCREATE TABLE \"t\" (\"id\" text);\n\n-- +goose Down\nDROP TABLE \"t\";\n",
			want: 4,
		},
		{
			name: "no marker at all",
			body: "-- +goose Up\nCREATE TABLE \"t\" (\"id\" text);\n",
			want: 0,
		},
		{
			name: "trailing text is not the marker",
			body: "-- +goose Up\n-- +goose Down is where the Down block would start\nDROP TABLE \"t\";\n",
			want: 0,
		},
		{
			name: "leading whitespace is not the marker",
			body: "-- +goose Up\n  -- +goose Down\nDROP TABLE \"t\";\n",
			want: 0,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tc.want,
				sqlscan.DownBlockStart(strings.Split(strings.TrimSuffix(tc.body, "\n"), "\n")))
		})
	}
}

// TestBacktickeOutsideStringLiterals_SeparatesDataFromIdentifierQuotes is issue #138's whole
// argument, as a table.
//
// `make migration` refuses when a backtick sits inside a string literal, because rewriting it would
// change the value db/schema.hcl asked for, and it tells the author they may instead rewrite the
// identifiers by hand. MIG002 fired on ANY backtick, so the file that advice produces — a correct
// migration whose only remaining backtick is that literal's — could never be landed. The gate has to
// be able to tell the two apart, and this is the function that does it.
func TestBackticksOutsideStringLiterals_SeparatesDataFromIdentifierQuotes(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		body  string
		lines []int
		why   string
	}{
		{
			name:  "an identifier quote",
			body:  "-- +goose Up\nCREATE TABLE `dkp_meta` (\"key\" text);\n",
			lines: []int{2},
			why:   "the MySQL-style quoting Atlas emits and sqlc cannot parse — the rule's whole purpose",
		},
		{
			name:  "a backtick inside a string literal",
			body:  "-- +goose Up\nCREATE TABLE \"t\" (\"k\" text DEFAULT 'the `value` column');\n",
			lines: nil,
			why: "data, not an identifier quote. This is the file `make migration` refuses on, and " +
				"the hand-fixed form of it must be landable (issue #138)",
		},
		{
			name:  "both in one file",
			body:  "-- +goose Up\nCREATE TABLE `t` (\"k\" text DEFAULT 'a `b` c');\n",
			lines: []int{2},
			why:   "a refused literal does not exempt the identifier quote beside it",
		},
		{
			name:  "a literal spanning physical lines",
			body:  "-- +goose Up\nCREATE TABLE \"t\" (\"k\" text DEFAULT 'opened here\nand a `backtick` on the next line');\n",
			lines: nil,
			why: "a SQLite literal may span lines, so the line a backtick sits on says nothing about " +
				"what it belongs to — the case a per-line pattern cannot have",
		},
		{
			name:  "two pairs on one line",
			body:  "-- +goose Up\nCREATE TABLE `t` (`k` text);\n",
			lines: []int{2},
			why:   "one mistake, reported once: the finding is the line, as it was when this was a grep",
		},
		{
			name:  "inside a SQL comment",
			body:  "-- +goose Up\n-- rename `old_col` to new_col\nALTER TABLE \"t\" ADD COLUMN \"c\" text;\n",
			lines: []int{2},
			why: "MIG002 fails on a backtick anywhere in the file and always has: a comment cannot " +
				"change meaning, so leaving it costs nothing and removing the rule's blindness costs a bypass",
		},
		{
			name:  "inside a double-quoted identifier",
			body:  "-- +goose Up\nCREATE TABLE \"a`b`c\" (\"k\" text);\n",
			lines: []int{2},
			why: "migrationfmt deliberately leaves this one alone so that a human looks at it, and " +
				"this rule is how a human comes to look at it",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var got []int
			for _, hit := range sqlscan.BackticksOutsideStringLiterals(tc.body) {
				got = append(got, hit.Line)
			}

			require.Equal(t, tc.lines, got, "%s", tc.why)
		})
	}
}

// TestBackticksOutsideStringLiterals_QuotesTheWholeLine keeps the finding readable: a gate that
// names a line number and not the line makes the reader go and find it, and a migration is a file
// people open rarely and edit never.
func TestBackticksOutsideStringLiterals_QuotesTheWholeLine(t *testing.T) {
	t.Parallel()

	hits := sqlscan.BackticksOutsideStringLiterals("-- +goose Up\nCREATE TABLE `dkp_meta` (\"key\" text);\n")

	require.Len(t, hits, 1)
	require.Equal(t, "CREATE TABLE `dkp_meta` (\"key\" text);", hits[0].Text)
}

// TestRewriteBackticks_StillRefusesALiteral is the property the gate must NOT have and the generator
// must: the same scanner answers both, and the difference is one argument.
//
// Without this the two callers could drift apart silently — the gate would go on passing files the
// generator refuses, which is fine, and the generator could quietly stop refusing, which is not.
func TestRewriteBackticks_StillRefusesALiteral(t *testing.T) {
	t.Parallel()

	_, err := sqlscan.RewriteBackticks("CREATE TABLE \"t\" (\"k\" text DEFAULT 'the `value` column');\n")

	require.ErrorIs(t, err, sqlscan.ErrBacktickInStringLiteral)

	out, err := sqlscan.RewriteBackticks("CREATE TABLE `t` (`k` text);\n")
	require.NoError(t, err)
	require.Equal(t, "CREATE TABLE \"t\" (\"k\" text);\n", out)
}
