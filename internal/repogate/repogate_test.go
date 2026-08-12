// Tests for the gate engine's own logic.
//
// The negative fixtures in test/repo/ are the ones that matter most — they drive the real
// `scripts/repo-gates.sh` against a deliberately tainted tree and require the rule id to appear —
// and they carry over from the shell implementation unchanged. What they cannot do is exercise the
// logic DIRECTLY: every one of them costs a subprocess, so the shell rules were only ever tested
// through their output, and whole branches (an aliased import, a heredoc list, a waiver with no
// reason) were reachable only by constructing a whole tree for each.
//
// This file is the half that became possible when the rules moved to Go. It tests the scanners as
// functions, at no cost per case, which is the argument the licence gate made in issue #130 and the
// mockup builder in #150.
package repogate

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// write puts a file into a fixture tree, creating its parents.
func write(t *testing.T, tree, rel, body string) {
	t.Helper()

	path := filepath.Join(tree, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
}

// run evaluates every rule against a fixture tree and returns the report.
func run(t *testing.T, tree string) (output string, failed bool) {
	t.Helper()

	var buf bytes.Buffer

	err := Run(tree, &buf)

	return buf.String(), err != nil
}

// TestRun_AliasedImports_StillFireTheGoSyntaxLaws is the property the text rules could not have.
//
// Every violation below is written the way somebody writes it AFTER a grep gate has complained
// once: the package is imported under another name, so the banned token never appears in the file
// at all. `import c ".../internal/clock"`, `import r "math/rand/v2"`, `import h ".../huma/v2"` —
// each of them walked straight past the pattern that replaced it, and each is caught here because
// the rule resolves the local name out of the import block instead of assuming the conventional
// one.
//
// This is the whole reason the Go-syntax laws are syntax and not text. A check an alias defeats is
// a check that teaches people to alias.
func TestRun_AliasedImports_StillFireTheGoSyntaxLaws(t *testing.T) {
	t.Parallel()

	tree := t.TempDir()

	write(t, tree, "internal/cms/routes.go", `package cms

import h "github.com/danielgtaylor/huma/v2"

func register(api h.API) {
	h.Register(api, h.Operation{OperationID: "getSneaky"}, nil)
}
`)
	write(t, tree, "internal/strategy/decay.go", `package strategy

import (
	c "github.com/prokopto-dev/dragonkillparty/internal/clock"
	r "math/rand/v2"
)

func now() int64 { return c.System{}.Now().UnixMicro() }

func pick(n int) int { return r.IntN(n) }
`)

	// internal/api is where a route belongs, and internal/clock is where the real clock is built.
	// Without these the assertions below would pass just as happily against a rule that fired on
	// everything, and the first person to hit that reaches for --no-verify rather than the rule id.
	write(t, tree, "internal/api/roster.go", `package api

import h "github.com/danielgtaylor/huma/v2"

func register(api h.API) {
	h.Register(api, h.Operation{OperationID: "listRoster"}, nil)
}
`)
	write(t, tree, "internal/clock/system.go", `package clock

import "time"

type System struct{}

func (System) Now() time.Time { return time.Now() }
`)

	out, failed := run(t, tree)

	require.True(t, failed, "a tainted tree must fail\n%s", out)

	for _, want := range []string{
		"ROUTE001", "internal/cms/routes.go:",
		"CLOCK002", "PURE002", "internal/strategy/decay.go:",
	} {
		require.Contains(t, out, want, "%s", out)
	}

	require.NotContains(t, out, "internal/api/roster.go",
		"internal/api is the one tree where a route may be declared\n%s", out)
	require.NotContains(t, out, "internal/clock/system.go",
		"internal/clock is where time.Now is SUPPOSED to be called — the allowlist is not working\n%s", out)
}

// TestRun_ZeroArgumentQuery_IsNotADatabaseCall pins the exclusion that forced the shell rule into
// its most contorted shape.
//
// `r.URL.Query()` is net/url's accessor and appears all over internal/api. In a line-scoped text
// rule the exemption could not go in the allowlist — that drops the whole line, including a real
// violation that merely MENTIONS the accessor — so it had to be encoded in the pattern as
// `\(([^)]|$)`, whose `$` arm existed only to keep a call whose arguments wrap to the next line
// matched. Reading the call site answers both questions at once, and this fixture is the pair.
func TestRun_ZeroArgumentQuery_IsNotADatabaseCall(t *testing.T) {
	t.Parallel()

	tree := t.TempDir()

	write(t, tree, "internal/api/params.go", `package api

func params(r *http.Request) url.Values { return r.URL.Query() }
`)
	write(t, tree, "internal/api/search.go", `package api

func search(ctx context.Context, r *http.Request, conn *sql.DB) error {
	rows, err := conn.QueryContext(
		ctx,
		"SELECT 1 WHERE x = "+r.URL.Query().Get("q"),
	)
	_ = rows

	return err
}
`)

	out, failed := run(t, tree)

	require.True(t, failed, "%s", out)
	require.Contains(t, out, "SQL002", "%s", out)
	require.Contains(t, out, "internal/api/search.go:",
		"a call whose arguments wrap to the next line is still a call\n%s", out)
	require.NotContains(t, out, "internal/api/params.go",
		"a zero-argument .Query() cannot be a database call: every real one takes the SQL string\n%s", out)
}

// TestRun_UnparseableGoFile_IsSkippedNotReported records the one direction the Go-syntax laws are
// blind in, so that nobody discovers it by accident.
//
// A file that does not parse is skipped, exactly as internal/api/arch_test.go's route scan skips
// one. The compiler is the mechanism for "this file is Go", and a gate that also reported syntax
// errors would become the messenger for every typo in the repository while saying it worse.
func TestRun_UnparseableGoFile_IsSkippedNotReported(t *testing.T) {
	t.Parallel()

	tree := t.TempDir()

	write(t, tree, "internal/api/broken.go", "package api\n\nfunc oops( {\n")

	out, failed := run(t, tree)

	require.False(t, failed, "an unparseable file is the compiler's business, not a gate's\n%s", out)
	require.NotContains(t, out, "broken.go", "%s", out)
}

// TestRun_EmptyTree_SkipsEveryRuleLoudly is the vacuous-pass doctrine, asserted.
//
// A rule whose tree does not exist yet passes — the rule is installed before the code it gates —
// but it must SAY so. A skip that printed nothing would be indistinguishable in a CI log from a
// rule that ran, which is how a gate suite stops meaning anything without anybody noticing.
func TestRun_EmptyTree_SkipsEveryRuleLoudly(t *testing.T) {
	t.Parallel()

	out, failed := run(t, t.TempDir())

	require.False(t, failed, "an empty tree has nothing to violate\n%s", out)
	require.Contains(t, out, "repo gates passed", "%s", out)

	for _, id := range []string{"SQL001", "PURE001", "MONEY001", "MIG001", "ENUM001", "MIG003"} {
		require.Contains(t, out, "["+id+"]",
			"%s skipped without saying so — a rule that vanishes silently reads exactly like a rule "+
				"that ran\n%s", id, out)
	}
}

// TestRun_AGPLFirewall_DoesNotStripComments is the one rule whose comment handling is inverted, and
// the fixture that would say so if a refactor "unified" it with its neighbours.
//
// Everywhere else a banned token inside a comment is prose about the rule. Here it is the thing
// itself: transcribing AGPL-3.0 source into a Go comment infringes exactly as much as transcribing
// it into code, and "I only pasted it as a reference" is precisely how it happens.
//
// The identifier is assembled rather than written, and the same trick appears in test/repo and in
// internal/strategy's arch test for the same reason: this file lives under internal/, which is a
// tree AGPL001 scans, so spelling one here would make the firewall fire on its own test.
func TestRun_AGPLFirewall_DoesNotStripComments(t *testing.T) {
	t.Parallel()

	banned := "gen" + "_class"
	tree := t.TempDir()

	write(t, tree, "internal/importer/notes.go",
		"package importer\n\n// "+banned+" is EQdkp's class table; the column list was copied from their schema.\n")

	// Allowlisted: reading a user's database at runtime requires naming their tables somewhere.
	write(t, tree, "internal/importer/legacy_names.go",
		"package importer\n\nvar legacyTables = []string{\""+banned+"\"}\n")

	out, failed := run(t, tree)

	require.True(t, failed, "a transcription in a comment is still a transcription\n%s", out)
	require.Contains(t, out, "AGPL001", "%s", out)
	require.Contains(t, out, "internal/importer/notes.go:", "%s", out)
	require.NotContains(t, out, "legacy_names.go",
		"legacy_names.go is where EQdkp's table names are ALLOWED to be written down\n%s", out)
}

// TestReport_TruncatedHits_SaysHowManyItDropped covers the one behaviour that is new rather than
// carried over.
//
// The shell capped the rules that went through its `gate` helper at twenty lines and left the
// hand-written blocks — the AGPL firewall among them — unbounded. Unifying the cap would have
// quietly shortened the firewall's report, so the count is printed: nothing is hidden, and the
// number is what tells a reader this is a tree to clean rather than a line to fix.
func TestReport_TruncatedHits_SaysHowManyItDropped(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer

	rep := &report{w: &buf}

	hits := make([]string, 0, maxHits+3)
	for range maxHits + 3 {
		hits = append(hits, "file.go:1:hit")
	}

	rep.violation("TEST001", "a rule with more hits than it prints", hits)

	require.Contains(t, buf.String(), "… and 3 more", "%s", buf.String())
	require.Equal(t, maxHits, strings.Count(buf.String(), "file.go:1:hit"), "%s", buf.String())
}
