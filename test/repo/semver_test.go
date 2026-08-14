// A minimal npm-flavoured semver, because web_overrides_test.go's range check needs one and this
// repository has no semver library in Go.
//
// WHY IT IS HERE RATHER THAN IN go.mod. Adding a dependency is a human decision (AGENTS.md), and
// "a test needs it" is the weakest reason there is to take one — the module would be in the runtime
// graph of nothing, and `internal/licence` would have to carry its licence forever. Issue #186 named
// this as the cost of the check and the answer is ~200 lines of arithmetic over three integers.
//
// SCOPE is the syntax the ranges under web/node_modules actually use — `^0.27.0 || ^0.28.0`, `4.3.0`,
// `^4.3.0` — plus the neighbours the next override will land on: `~`, the four inequalities, `=`,
// partial versions, `*`/`x`, and the hyphen range. Build metadata is parsed and ignored, which is
// what semver §10 says it is for.
//
// AN UNPARSEABLE RANGE IS A FAILURE, NEVER A PASS. Every entry point returns an error rather than a
// permissive default, and the caller turns that into a red test naming the parent and the range. A
// range checker that silently answers "satisfied" to syntax it does not understand is a gate that
// reports green on the case it exists to catch — which is the defect class this whole directory is
// about.
package repo_test

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// semver is a parsed version. Build metadata is discarded: semver §10 says it takes no part in
// precedence, so keeping it would only invite a comparison that uses it.
type semver struct {
	major, minor, patch int
	// pre holds the dot-separated prerelease identifiers, and is nil for a release version. A
	// prerelease sorts BEFORE its release (§11) and is deliberately hard to satisfy a range with.
	pre []string
}

func (v semver) String() string {
	s := fmt.Sprintf("%d.%d.%d", v.major, v.minor, v.patch)
	if len(v.pre) > 0 {
		s += "-" + strings.Join(v.pre, ".")
	}

	return s
}

// parseSemver parses a full `major.minor.patch[-prerelease][+build]`.
func parseSemver(s string) (semver, error) {
	v, err := parsePartialSemver(s)
	if err != nil {
		return semver{}, err
	}
	if v.wildcardMajor || v.wildcardMinor || v.wildcardPatch {
		return semver{}, fmt.Errorf("parse version %q: a wildcard is a range, not a version", s)
	}

	return v.semver, nil
}

// partialSemver is a version with any of its three components left off or written as a wildcard —
// `1.2`, `1.x`, `*`. Only a range may hold one; the wildcard flags are what tell the comparator
// builder how far to widen.
type partialSemver struct {
	semver
	wildcardMajor, wildcardMinor, wildcardPatch bool
}

// numericIdentRe would be a regexp; strconv is enough and says the same thing more directly.
func isNumericIdent(s string) bool {
	if s == "" {
		return false
	}

	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}

	return true
}

func parsePartialSemver(s string) (partialSemver, error) {
	orig := strings.TrimSpace(s)

	s = strings.TrimPrefix(orig, "v")

	if s == "" || s == "*" || s == "x" || s == "X" {
		return partialSemver{wildcardMajor: true, wildcardMinor: true, wildcardPatch: true}, nil
	}

	// Build metadata first: it may contain a `-`, so stripping it before the prerelease split is the
	// only order that parses `1.2.3-rc.1+build-7`.
	if plus := strings.IndexByte(s, '+'); plus >= 0 {
		s = s[:plus]
	}

	var pre []string

	if dash := strings.IndexByte(s, '-'); dash >= 0 {
		preStr := s[dash+1:]
		s = s[:dash]

		if preStr == "" {
			return partialSemver{}, fmt.Errorf("parse version %q: empty prerelease", orig)
		}

		pre = strings.Split(preStr, ".")
		for _, ident := range pre {
			if ident == "" {
				return partialSemver{}, fmt.Errorf("parse version %q: empty prerelease identifier", orig)
			}
		}
	}

	parts := strings.Split(s, ".")
	if len(parts) > 3 {
		return partialSemver{}, fmt.Errorf("parse version %q: %d components", orig, len(parts))
	}

	out := partialSemver{semver: semver{pre: pre}}
	fields := []*int{&out.major, &out.minor, &out.patch}
	wildcards := []*bool{&out.wildcardMajor, &out.wildcardMinor, &out.wildcardPatch}

	for i := range fields {
		if i >= len(parts) {
			*wildcards[i] = true

			continue
		}

		part := parts[i]
		if part == "*" || part == "x" || part == "X" {
			*wildcards[i] = true

			continue
		}

		if !isNumericIdent(part) {
			return partialSemver{}, fmt.Errorf("parse version %q: component %q is not a number", orig, part)
		}

		n, err := strconv.Atoi(part)
		if err != nil {
			return partialSemver{}, fmt.Errorf("parse version %q: %w", orig, err)
		}

		*fields[i] = n
	}

	// `1.x.3` is not a version anyone writes and not one npm accepts; refusing it keeps the widening
	// rules below unambiguous.
	if (out.wildcardMajor && !out.wildcardMinor) || (out.wildcardMinor && !out.wildcardPatch) {
		return partialSemver{}, fmt.Errorf("parse version %q: a wildcard component must be the last one", orig)
	}

	return out, nil
}

// compareSemver returns -1, 0 or 1, per semver §11: numeric comparison of the three components,
// then a release beats its own prereleases, then identifier-by-identifier.
func compareSemver(a, b semver) int {
	for _, pair := range [][2]int{{a.major, b.major}, {a.minor, b.minor}, {a.patch, b.patch}} {
		if pair[0] != pair[1] {
			if pair[0] < pair[1] {
				return -1
			}

			return 1
		}
	}

	switch {
	case len(a.pre) == 0 && len(b.pre) == 0:
		return 0
	case len(a.pre) == 0:
		return 1
	case len(b.pre) == 0:
		return -1
	}

	for i := 0; i < len(a.pre) && i < len(b.pre); i++ {
		if c := comparePrereleaseIdent(a.pre[i], b.pre[i]); c != 0 {
			return c
		}
	}

	switch {
	case len(a.pre) < len(b.pre):
		return -1
	case len(a.pre) > len(b.pre):
		return 1
	}

	return 0
}

// comparePrereleaseIdent implements §11's identifier rule: numeric identifiers compare numerically
// and always sort below alphanumeric ones.
func comparePrereleaseIdent(a, b string) int {
	aNum, bNum := isNumericIdent(a), isNumericIdent(b)

	switch {
	case aNum && bNum:
		x, _ := strconv.Atoi(a)
		y, _ := strconv.Atoi(b)

		switch {
		case x < y:
			return -1
		case x > y:
			return 1
		}

		return 0
	case aNum:
		return -1
	case bNum:
		return 1
	}

	return strings.Compare(a, b)
}

// semverBound is one half-line: an operator and the version it is drawn at.
type semverBound struct {
	op  string // ">=", ">", "<=", "<"
	ver semver
}

func (b semverBound) holds(v semver) bool {
	c := compareSemver(v, b.ver)

	switch b.op {
	case ">=":
		return c >= 0
	case ">":
		return c > 0
	case "<=":
		return c <= 0
	case "<":
		return c < 0
	}

	return false
}

// semverRange is a union of conjunctions — npm's `A || B`, each side a space-separated set of
// comparators that must all hold.
type semverRange [][]semverBound

// comparatorOps are matched longest-first, so `>=` is never read as `>`.
var comparatorOps = []string{">=", "<=", ">", "<", "=", "^", "~"}

// parseSemverRange parses an npm range expression. It returns an error for anything it does not
// understand, which the caller must surface rather than swallow.
func parseSemverRange(s string) (semverRange, error) {
	var out semverRange

	for _, alternative := range strings.Split(s, "||") {
		bounds, err := parseSemverConjunction(alternative)
		if err != nil {
			return nil, err
		}

		out = append(out, bounds)
	}

	return out, nil
}

// parseSemverConjunction parses one `||`-free half: a hyphen range, or a set of comparators.
func parseSemverConjunction(s string) ([]semverBound, error) {
	fields := strings.Fields(s)

	// A hyphen range: `1.2.3 - 2.3.4`, inclusive at both ends.
	if len(fields) == 3 && fields[1] == "-" {
		lo, err := parsePartialSemver(fields[0])
		if err != nil {
			return nil, err
		}

		hi, err := parsePartialSemver(fields[2])
		if err != nil {
			return nil, err
		}

		bounds := []semverBound{{op: ">=", ver: lo.semver}}
		if upper, bounded := partialUpperBound(hi); bounded {
			bounds = append(bounds, semverBound{op: "<", ver: upper})
		} else {
			bounds = append(bounds, semverBound{op: "<=", ver: hi.semver})
		}

		return bounds, nil
	}

	if len(fields) == 0 {
		// The empty range: `""` means "any version", exactly as `*` does.
		return nil, nil
	}

	var bounds []semverBound

	for _, field := range fields {
		got, err := parseComparator(field)
		if err != nil {
			return nil, err
		}

		bounds = append(bounds, got...)
	}

	return bounds, nil
}

// parseComparator turns one comparator into the bounds it stands for.
func parseComparator(field string) ([]semverBound, error) {
	op := ""

	for _, candidate := range comparatorOps {
		if strings.HasPrefix(field, candidate) {
			op = candidate

			break
		}
	}

	rest := strings.TrimSpace(strings.TrimPrefix(field, op))

	v, err := parsePartialSemver(rest)
	if err != nil {
		return nil, fmt.Errorf("comparator %q: %w", field, err)
	}

	switch op {
	case ">=":
		return []semverBound{{op: ">=", ver: v.semver}}, nil
	case "<=":
		// `<=1.2` admits every 1.2.x, so the inclusive upper end widens the same way a bare partial's
		// exclusive one does.
		if upper, bounded := partialUpperBound(v); bounded {
			return []semverBound{{op: "<", ver: upper}}, nil
		}

		return []semverBound{{op: "<=", ver: v.semver}}, nil
	case "<":
		return []semverBound{{op: "<", ver: v.semver}}, nil
	case ">":
		// `>1.2` excludes all of 1.2.x, which is `>=1.3.0` — npm's rule, and the one place a partial
		// moves the bound rather than widening it.
		if upper, bounded := partialUpperBound(v); bounded {
			return []semverBound{{op: ">=", ver: upper}}, nil
		}

		return []semverBound{{op: ">", ver: v.semver}}, nil
	case "^":
		return caretBounds(v), nil
	case "~":
		return tildeBounds(v), nil
	default: // "=" and the bare version, which mean the same thing
		return exactBounds(v), nil
	}
}

// partialUpperBound returns the first version ABOVE everything a partial admits — 1.2 -> 1.3.0,
// 1 -> 2.0.0 — and reports false when the partial is complete or is the bare wildcard.
func partialUpperBound(v partialSemver) (semver, bool) {
	switch {
	case v.wildcardMajor:
		return semver{}, false
	case v.wildcardMinor:
		return semver{major: v.major + 1}, true
	case v.wildcardPatch:
		return semver{major: v.major, minor: v.minor + 1}, true
	}

	return semver{}, false
}

// exactBounds pins a complete version and widens a partial one: `=1.2` is every 1.2.x.
func exactBounds(v partialSemver) []semverBound {
	if upper, bounded := partialUpperBound(v); bounded {
		return []semverBound{{op: ">=", ver: v.semver}, {op: "<", ver: upper}}
	}

	if v.wildcardMajor {
		return nil
	}

	return []semverBound{{op: ">=", ver: v.semver}, {op: "<=", ver: v.semver}}
}

// caretBounds implements `^`: compatible with, where "compatible" means the leftmost NON-ZERO
// component may not change. ^0.28.0 therefore admits 0.28.x and nothing else, which is why
// esbuild's pin has to be checked against `^0.27.0 || ^0.28.0` and not against a major.
func caretBounds(v partialSemver) []semverBound {
	if v.wildcardMajor {
		return nil
	}

	lower := semverBound{op: ">=", ver: v.semver}

	var upper semver

	switch {
	case v.major != 0:
		upper = semver{major: v.major + 1}
	case v.wildcardMinor:
		// ^0.x -> >=0.0.0 <1.0.0: nothing below the major is known, so the major is the bound.
		upper = semver{major: 1}
	case v.minor != 0 || v.wildcardPatch:
		upper = semver{major: 0, minor: v.minor + 1}
	default:
		upper = semver{major: 0, minor: 0, patch: v.patch + 1}
	}

	return []semverBound{lower, {op: "<", ver: upper}}
}

// tildeBounds implements `~`: patch-level changes if a minor is given, minor-level if it is not.
func tildeBounds(v partialSemver) []semverBound {
	if v.wildcardMajor {
		return nil
	}

	lower := semverBound{op: ">=", ver: v.semver}

	if v.wildcardMinor {
		return []semverBound{lower, {op: "<", ver: semver{major: v.major + 1}}}
	}

	return []semverBound{lower, {op: "<", ver: semver{major: v.major, minor: v.minor + 1}}}
}

// satisfies reports whether v is admitted by the range.
func (r semverRange) satisfies(v semver) bool {
	for _, conjunction := range r {
		if conjunctionAdmits(conjunction, v) {
			return true
		}
	}

	return false
}

func conjunctionAdmits(bounds []semverBound, v semver) bool {
	for _, bound := range bounds {
		if !bound.holds(v) {
			return false
		}
	}

	// npm's prerelease rule, and it is not a detail: 1.0.0-rc.1 does NOT satisfy `>=0.9.0`, because
	// a prerelease is only ever offered to somebody who asked for that exact release's prereleases.
	// Without this a pin of `4.3.1-canary` would read as satisfying every range in the tree.
	if len(v.pre) == 0 {
		return true
	}

	for _, bound := range bounds {
		if len(bound.ver.pre) > 0 &&
			bound.ver.major == v.major && bound.ver.minor == v.minor && bound.ver.patch == v.patch {
			return true
		}
	}

	return false
}

// minimum returns the lowest version the range admits, which is what tells web_overrides_test.go
// whether an override is still forcing anything.
//
// CONSERVATIVE BY CONSTRUCTION. `>1.2.3` is reported as 1.2.3 rather than as "the next version
// after it", and a conjunction with no lower bound at all reports 0.0.0. Both err towards a LOWER
// answer, and a lower answer means "the override is still doing work" — so the redundancy check
// under-fires rather than failing a build over arithmetic nobody asked it to do.
func (r semverRange) minimum() semver {
	var (
		out   semver
		found bool
	)

	for _, conjunction := range r {
		var lower semver // the zero value is 0.0.0, the floor of an unbounded conjunction

		for _, bound := range conjunction {
			if bound.op != ">=" && bound.op != ">" {
				continue
			}

			if compareSemver(bound.ver, lower) > 0 {
				lower = bound.ver
			}
		}

		if !found || compareSemver(lower, out) < 0 {
			out = lower
			found = true
		}
	}

	return out
}

// ---------------------------------------------------------------------------------------------
// The tests for the arithmetic above. AGENTS.md: add a test when you add a gate — and the gate that
// reads these answers is only as good as they are.
// ---------------------------------------------------------------------------------------------

func TestSemverRange_Satisfies_MatchesNpmSemantics(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		rangeS  string
		version string
		want    bool
	}{
		// The three ranges the installed tree actually declares against an overridden package.
		{"vite's esbuild range admits the pin", "^0.27.0 || ^0.28.0", "0.28.2", true},
		{"vite's esbuild range excludes the next minor", "^0.27.0 || ^0.28.0", "0.29.0", false},
		{"redocly's exact js-yaml pin rejects a patch", "4.3.0", "4.3.1", false},
		{"redocly's exact js-yaml pin accepts itself", "4.3.0", "4.3.0", true},
		{"eslintrc's caret js-yaml range accepts the patch", "^4.3.0", "4.3.1", true},

		// Caret, where the leftmost non-zero component is the one that may not move.
		{"caret on a major allows a minor bump", "^1.2.3", "1.9.0", true},
		{"caret on a major rejects the next major", "^1.2.3", "2.0.0", false},
		{"caret on 0.x pins the minor", "^0.2.3", "0.3.0", false},
		{"caret on 0.x allows a patch", "^0.2.3", "0.2.9", true},
		{"caret on 0.0.x pins the patch", "^0.0.3", "0.0.4", false},

		// Tilde.
		{"tilde allows a patch", "~1.2.3", "1.2.9", true},
		{"tilde rejects a minor", "~1.2.3", "1.3.0", false},
		{"tilde on a partial allows the whole minor", "~1.2", "1.2.7", true},

		// Inequalities, conjunctions and partials.
		{"a conjunction is an intersection", ">=1.0.0 <2.0.0", "1.5.0", true},
		{"a conjunction excludes above its top", ">=1.0.0 <2.0.0", "2.0.0", false},
		{"a partial upper bound widens", "<=1.2", "1.2.9", true},
		{"a partial lower bound moves past its minor", ">1.2", "1.2.9", false},
		{"a partial lower bound admits the next minor", ">1.2", "1.3.0", true},
		{"a bare partial is the whole minor", "1.2", "1.2.9", true},
		{"a bare partial excludes the next minor", "1.2", "1.3.0", false},
		{"a hyphen range is inclusive", "1.2.3 - 2.3.4", "2.3.4", true},
		{"a hyphen range excludes above its top", "1.2.3 - 2.3.4", "2.3.5", false},
		{"a wildcard admits anything", "*", "9.9.9", true},
		{"an empty range admits anything", "", "9.9.9", true},

		// The prerelease rule, which is the one an ad-hoc implementation always gets wrong.
		{"a prerelease does not satisfy an unrelated range", ">=0.9.0", "1.0.0-rc.1", false},
		{"a prerelease satisfies a range naming its own release", ">=1.0.0-rc.0", "1.0.0-rc.1", true},
		{"a release still satisfies a prerelease-anchored range", ">=1.0.0-rc.0", "1.0.0", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r, err := parseSemverRange(tc.rangeS)
			require.NoErrorf(t, err, "parse range %q", tc.rangeS)

			v, err := parseSemver(tc.version)
			require.NoErrorf(t, err, "parse version %q", tc.version)

			require.Equalf(t, tc.want, r.satisfies(v), "%q satisfies %q", tc.version, tc.rangeS)
		})
	}
}

func TestSemverRange_Minimum_IsTheLowestVersionTheRangeAdmits(t *testing.T) {
	t.Parallel()

	tests := []struct {
		rangeS string
		want   string
	}{
		// The union takes the lower of the two branches, which is the whole point for esbuild: the
		// pin sits INSIDE this range and the range still admits 0.27.0, so the override is what is
		// keeping 0.27.x out.
		{"^0.27.0 || ^0.28.0", "0.27.0"},
		{"^4.3.0", "4.3.0"},
		{"4.3.0", "4.3.0"},
		{">=1.2.3 <2.0.0", "1.2.3"},
		{">1.2.3", "1.2.3"}, // conservative: the exclusive bound reports its own version
		{"<2.0.0", "0.0.0"}, // no lower bound at all
		{"*", "0.0.0"},
		{"1.2.3 - 2.3.4", "1.2.3"},
	}

	for _, tc := range tests {
		t.Run(tc.rangeS, func(t *testing.T) {
			t.Parallel()

			r, err := parseSemverRange(tc.rangeS)
			require.NoErrorf(t, err, "parse range %q", tc.rangeS)

			require.Equal(t, tc.want, r.minimum().String())
		})
	}
}

// TestParseSemverRange_Unparseable_IsAnError is the must-not-pass-vacuously half. Every case here
// is syntax this parser does not implement, and the gate that calls it turns the error into a red
// test naming the parent — rather than into a silent "satisfied".
func TestParseSemverRange_Unparseable_IsAnError(t *testing.T) {
	t.Parallel()

	for _, rangeS := range []string{
		"github:user/repo#semver:^1.0.0",
		"workspace:^",
		"file:../local",
		"https://example.invalid/pkg.tgz",
		"latest",
		"^1.2.3.4",
		"1.x.3",
		">=1.0.0-",
	} {
		t.Run(rangeS, func(t *testing.T) {
			t.Parallel()

			_, err := parseSemverRange(rangeS)
			require.Errorf(t, err, "%q must not parse: an unrecognised range that reads as satisfied is "+
				"a gate reporting green on the case it exists to catch", rangeS)
		})
	}
}

func TestCompareSemver_Prerelease_SortsBelowItsRelease(t *testing.T) {
	t.Parallel()

	ordered := []string{
		"1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta", "1.0.0-beta.2",
		"1.0.0-beta.11", "1.0.0-rc.1", "1.0.0", "1.0.1", "1.1.0", "2.0.0",
	}

	for i := 1; i < len(ordered); i++ {
		lo, err := parseSemver(ordered[i-1])
		require.NoError(t, err)

		hi, err := parseSemver(ordered[i])
		require.NoError(t, err)

		require.Equalf(t, -1, compareSemver(lo, hi), "%s must sort below %s (semver §11)", lo, hi)
		require.Equalf(t, 1, compareSemver(hi, lo), "%s must sort above %s (semver §11)", hi, lo)
		require.Equalf(t, 0, compareSemver(lo, lo), "%s must equal itself", lo)
	}
}

// TestParseSemver_BuildMetadata_IsIgnored covers semver §10: build metadata takes no part in
// precedence, so a version carrying it must compare equal to the same version without it.
func TestParseSemver_BuildMetadata_IsIgnored(t *testing.T) {
	t.Parallel()

	withBuild, err := parseSemver("1.2.3-rc.1+build.7")
	require.NoError(t, err)

	without, err := parseSemver("1.2.3-rc.1")
	require.NoError(t, err)

	require.Equal(t, 0, compareSemver(withBuild, without),
		"build metadata must not affect precedence (semver §10)")
	require.Equal(t, "1.2.3-rc.1", withBuild.String())
}
