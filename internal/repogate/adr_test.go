package repogate

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestAdrWaived_MarkerWithoutReason_IsNotAWaiver pins the half of ADR001 that costs nothing to get
// wrong and everything to leave wrong.
//
// Both documents specify `adr: n/a — <reason>`, and harvesting that reason is the entire value of
// the escape hatch: the reasons are what a later reader searches when the question is re-litigated.
// A marker that costs one token gets pasted onto the next PR too, and the gate becomes a formality
// within a month.
//
// test/repo drives three of these through the real script against a git fixture; the rest are here,
// because each case is one string and the subprocess was the only thing making them expensive.
func TestAdrWaived_MarkerWithoutReason_IsNotAWaiver(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		body string
		want bool
	}{
		{body: "adr: n/a — no new port, volume or process", want: true},
		{body: "ADR: N/A - the Dockerfile change re-orders two layers", want: true},
		{body: "## What and why\n\nCloses #123.\n\n  adr: n/a: the schema column is generated\n", want: true},
		{body: "adr: n/a", want: false},
		{body: "adr: n/a —", want: false},
		{body: "adr: n/a - ", want: false},
		{body: "adr: n/a — single", want: false},
		{body: "", want: false},
		{body: "## What and why\n\nCloses #123.\n", want: false},
		// Not a waiver: the line has to BE the marker, not mention it. Otherwise a PR body that
		// quotes the rule — which is exactly what a PR adding the rule does — waives it.
		{body: "the gate wants an `adr: n/a — <reason>` line in the body", want: false},
	} {
		t.Run(strings.ReplaceAll(tc.body, "\n", "⏎"), func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tc.want, adrWaived(tc.body))
		})
	}
}

// TestDirectRequires_IndirectAndVersionBumps_AreNotNewDependencies is the operationally important
// half of the go.mod trigger.
//
// Renovate bumps versions and adds indirects continuously, and a rule that fired on those would
// demand a waiver line on every dependency PR — which is how a gate stops being read and starts
// being pasted past. Both spellings of the directive are covered because a one-line `require` is
// what a `go get` leaves behind in a module with no block.
func TestDirectRequires_IndirectAndVersionBumps_AreNotNewDependencies(t *testing.T) {
	t.Parallel()

	const base = `module github.com/prokopto-dev/dragonkillparty

go 1.26

require (
	github.com/danielgtaylor/huma/v2 v2.39.1
	github.com/stretchr/testify v1.11.1
	golang.org/x/sys v0.47.0 // indirect
)

require golang.org/x/net v0.57.0
`

	direct := directRequires(strings.Split(base, "\n"))

	require.Equal(t, []string{
		"github.com/danielgtaylor/huma/v2",
		"github.com/stretchr/testify",
		"golang.org/x/net",
	}, direct,
		"an `// indirect` requirement is not a direct dependency, and the single-line spelling is "+
			"still one")

	bumped := directRequires(strings.Split(strings.Replace(base, "testify v1.11.1", "testify v1.12.0", 1), "\n"))
	require.Equal(t, direct, bumped, "a version bump adds no dependency")

	added := directRequires(strings.Split(strings.Replace(base,
		"\tgithub.com/stretchr/testify v1.11.1\n",
		"\tgithub.com/stretchr/testify v1.11.1\n\tgithub.com/redis/go-redis/v9 v9.5.1\n", 1), "\n"))
	require.Contains(t, added, "github.com/redis/go-redis/v9", "a new direct requirement must be seen")
}
