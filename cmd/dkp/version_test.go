package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestVersion_UnstampedBuild_ReportsDev asserts the link-time build stamps have usable defaults.
//
// The interesting half is not "version == dev" — it is that none of the three is empty. An empty
// -X target produces a binary that cannot say what it is, and the failure shows up in a support
// thread six months later, not in CI.
func TestVersion_UnstampedBuild_ReportsDev(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	cmd := newRootCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"version"})

	require.NoError(t, cmd.Execute(), "dkp version must not fail")

	fields := parseKeyValueLines(t, out.String())

	tests := []struct {
		name string
		key  string
		want string // empty means: any value, as long as it is not empty
	}{
		{name: "version is the unstamped default", key: "version", want: "dev"},
		{name: "commit is never empty", key: "commit"},
		{name: "date is never empty", key: "date"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, ok := fields[tt.key]
			require.Truef(t, ok, "dkp version output has no %q line; got:\n%s", tt.key, out.String())
			require.NotEmptyf(t, got, "%s must never be empty", tt.key)

			if tt.want != "" {
				require.Equalf(t, tt.want, got, "%s", tt.key)
			}
		})
	}
}

// parseKeyValueLines splits "key: value" output into a map, failing on any line that is not in
// that shape — a silently dropped line would make a missing stamp look like a passing test.
func parseKeyValueLines(tb testing.TB, out string) map[string]string {
	tb.Helper()

	trimmed := strings.TrimSpace(out)
	require.NotEmpty(tb, trimmed, "dkp version printed nothing")

	fields := make(map[string]string)

	for line := range strings.SplitSeq(trimmed, "\n") {
		key, value, ok := strings.Cut(line, ":")
		require.Truef(tb, ok, "line %q is not in key: value form", line)

		fields[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}

	return fields
}
