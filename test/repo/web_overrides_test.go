// The pnpm.overrides register (issue #168).
//
// An override forces a version onto a package's whole subtree — including subtrees whose parent
// never declared support for it — and pnpm accepts that silently. The two entries in
// web/package.json were both correct and both necessary when they landed, and NOTHING verified them
// afterwards, which is the state this file changes. Two ways an override rots:
//
//  1. It sits outside its parent's declared range, and a later unrelated bump moves the parent while
//     the pin keeps forcing the old version.
//  2. It outlives its reason, and then holds a fix back: an override pinned to an exact version
//     PREVENTS the next security patch, so the entry added to close a CVE becomes the reason the
//     following one cannot be closed — with `security / osv` reporting the advisory and nothing
//     saying that a line in package.json is why.
//
// The model is osv-scanner.toml, whose waivers each carry a filed issue and an expiry precisely
// because a deliberate exception with nothing forcing a revisit becomes permanent. web/OVERRIDES.md
// is the register; the tests below are what make it impossible to skip.
//
// WHAT IS NOT HERE, and deliberately: whether a pin sits inside the range its parent DECLARES. The
// lockfile records resolved versions, not the parents' declared ranges — those live in each parent's
// own package.json under node_modules — so that check needs a dependency install behind it. It is
// filed rather than half-built, and web/OVERRIDES.md says so in the same words.
package repo_test

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// overridesRowRe matches one row of the register's table: `| `pkg` | `version` | why | parent | when |`.
var overridesRowRe = regexp.MustCompile(`^\|\s*` + "`" + `([^` + "`" + `]+)` + "`" + `\s*\|\s*` + "`" + `([^` + "`" + `]+)` + "`" + `\s*\|(.+)\|(.+)\|(.+)\|\s*$`)

// issueRefRe matches a GitHub issue reference, which every row must carry.
var issueRefRe = regexp.MustCompile(`#\d+`)

// overrideRow is one entry of web/OVERRIDES.md.
type overrideRow struct {
	pkg     string
	pinned  string
	why     string
	parent  string
	removed string
}

// packageJSONOverrides reads web/package.json's pnpm.overrides block.
func packageJSONOverrides(t *testing.T) map[string]string {
	t.Helper()

	var manifest struct {
		Pnpm struct {
			Overrides map[string]string `json:"overrides"`
		} `json:"pnpm"`
	}

	require.NoError(t, json.Unmarshal([]byte(readRepoFile(t, "web/package.json")), &manifest),
		"parse web/package.json")

	return manifest.Pnpm.Overrides
}

// lockfileOverrides reads the top-level `overrides:` block of web/pnpm-lock.yaml.
//
// Parsed by indentation rather than with a YAML library, for ci_required_test.go's reason:
// gopkg.in/yaml.v3 is an indirect dependency today and promoting it to a direct one to read a block
// this regular would mean adding a dependency for a test, which AGENTS.md requires a human to
// approve. The block is `overrides:` followed by two-space `name: version` lines.
func lockfileOverrides(t *testing.T) map[string]string {
	t.Helper()

	out := map[string]string{}
	inBlock := false

	for _, line := range strings.Split(readRepoFile(t, "web/pnpm-lock.yaml"), "\n") {
		if line == "overrides:" {
			inBlock = true

			continue
		}

		if !inBlock {
			continue
		}

		if !strings.HasPrefix(line, "  ") || strings.TrimSpace(line) == "" {
			break // a line at column 0 ends the block
		}

		name, version, ok := strings.Cut(strings.TrimSpace(line), ": ")
		if !ok {
			continue
		}

		out[strings.Trim(name, `'"`)] = strings.Trim(version, `'"`)
	}

	return out
}

// registerRows reads the table in web/OVERRIDES.md.
func registerRows(t *testing.T) map[string]overrideRow {
	t.Helper()

	rows := map[string]overrideRow{}

	for _, line := range strings.Split(readRepoFile(t, "web/OVERRIDES.md"), "\n") {
		m := overridesRowRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}

		rows[m[1]] = overrideRow{
			pkg:     m[1],
			pinned:  m[2],
			why:     strings.TrimSpace(m[3]),
			parent:  strings.TrimSpace(m[4]),
			removed: strings.TrimSpace(m[5]),
		}
	}

	return rows
}

// TestWebOverrides_EveryOverride_HasARegisterRow is the anti-rot property: the manifest and the
// register describe the same set, with the same pins.
//
// BOTH DIRECTIONS. A missing row is an override nobody has to justify; a stale row is a removal
// condition pointing at nothing, which is worse than no row because it reads as coverage.
func TestWebOverrides_EveryOverride_HasARegisterRow(t *testing.T) {
	t.Parallel()

	overrides := packageJSONOverrides(t)
	rows := registerRows(t)

	for pkg, version := range overrides {
		row, ok := rows[pkg]
		require.Truef(t, ok,
			"web/package.json overrides %q and web/OVERRIDES.md has no row for it. An override is a "+
				"resolution nothing else verifies (issue #168) — add a row naming why it exists, the "+
				"parent that forces it, and what removes it.", pkg)
		require.Equalf(t, version, row.pinned,
			"web/OVERRIDES.md pins %s at %s and web/package.json at %s. The register is only worth "+
				"reading if it is true.", pkg, row.pinned, version)
	}

	for pkg := range rows {
		require.Containsf(t, overrides, pkg,
			"web/OVERRIDES.md still has a row for %q, which web/package.json no longer overrides. "+
				"Clearing an override means deleting its row in the same change — a row for an "+
				"override that does not exist reads as coverage and is not.", pkg)
	}
}

// TestWebOverrides_LockfileAgreesWithTheManifest catches an override added, changed or removed
// without re-locking. pnpm records the override set in the lockfile, so the two can be compared
// offline — and an override the lockfile does not carry is inert while looking exactly like one that
// works.
func TestWebOverrides_LockfileAgreesWithTheManifest(t *testing.T) {
	t.Parallel()

	manifest := packageJSONOverrides(t)
	lock := lockfileOverrides(t)

	require.Equalf(t, manifest, lock,
		"web/package.json's pnpm.overrides and web/pnpm-lock.yaml's overrides: block disagree. An "+
			"override edited without `pnpm install` is inert — the resolution the lockfile records is "+
			"what pnpm actually applies, and it still says what it said before the edit.")
}

// TestWebOverrides_EveryRow_NamesAParentAnIssueAndAnExit is what stops the register decaying into a
// list of package names. Each of the three fields answers a question somebody will have in six
// months and cannot reconstruct from the diff.
func TestWebOverrides_EveryRow_NamesAParentAnIssueAndAnExit(t *testing.T) {
	t.Parallel()

	rows := registerRows(t)
	require.NotEmpty(t, rows,
		"no rows parsed out of web/OVERRIDES.md — has the table's shape changed? A register nothing "+
			"can read is a register nothing enforces.")

	for pkg, row := range rows {
		t.Run(pkg, func(t *testing.T) {
			t.Parallel()

			require.NotEmptyf(t, row.why, "%s: the row must say WHY the override exists", pkg)
			require.Regexpf(t, issueRefRe, row.why+row.removed,
				"%s: the row must cite a filed issue. This is osv-scanner.toml's rule and it is here "+
					"for the same reason: an exception nobody tracks is a decision nobody revisits.", pkg)
			require.NotEmptyf(t, row.parent,
				"%s: the row must name the parent that forces the pin — it is what a later bump has "+
					"to be checked against", pkg)
			require.NotEmptyf(t, row.removed,
				"%s: the row must name the condition that REMOVES the override. An override pinned to "+
					"an exact version prevents the next patch of that package, so the entry that fixed "+
					"one advisory becomes the reason the next one cannot be fixed (issue #168).", pkg)
		})
	}
}

// TestWebOverrides_TookEffect_InEveryResolution asserts the pin is what the tree actually resolved.
//
// An override that only partly applied leaves a second version of the package in the lockfile — a
// pin that has quietly stopped covering the subtree it was added for, which is failure mode 1 in
// web/OVERRIDES.md observed from the only angle the lockfile can see.
func TestWebOverrides_TookEffect_InEveryResolution(t *testing.T) {
	t.Parallel()

	lockfile := readRepoFile(t, "web/pnpm-lock.yaml")

	for pkg, version := range packageJSONOverrides(t) {
		t.Run(pkg, func(t *testing.T) {
			t.Parallel()

			// Lockfile v9 keys every package as `  name@version:` under `packages:` and `snapshots:`,
			// with a scoped name single-quoted. The version runs to the first `:` or `(`, the latter
			// being a peer-dependency suffix.
			re := regexp.MustCompile(`(?m)^\s{2}'?` + regexp.QuoteMeta(pkg) + `@([^:'(]+)`)

			var seen []string

			for _, m := range re.FindAllStringSubmatch(lockfile, -1) {
				seen = append(seen, m[1])
			}

			require.NotEmptyf(t, seen,
				"%s is overridden to %s and does not appear in web/pnpm-lock.yaml at all. An override "+
					"for a package nothing depends on is dead weight that will one day hold a real "+
					"resolution back.", pkg, version)

			for _, got := range seen {
				require.Equalf(t, version, got,
					"web/pnpm-lock.yaml resolves %s@%s while the override pins %s. The override did not "+
						"take effect everywhere, so the subtree it was added for is running a version "+
						"nobody chose.", pkg, got, version)
			}
		})
	}
}
