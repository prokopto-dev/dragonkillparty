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
// BOTH ROT MODES ARE NOW CHECKED (issue #186). The four offline tests read package.json, the
// lockfile and the register and can see neither mode directly, because the lockfile records RESOLVED
// versions and not the ranges each parent declares. Those ranges live in each parent's own
// package.json under node_modules, so the last three tests in this file read the installed tree:
//
//   - mode 1, a pin outside its parent's declared range, is a range check per (override, parent)
//     pair, waivable only through a reviewed row in web/OVERRIDES.md that also records the range it
//     is breaking — so a parent bump that moves the range invalidates the waiver rather than being
//     covered by it;
//   - mode 2, an override that outlived its reason, is the observation that an override forcing a
//     version no parent's range would have gone below is forcing nothing. That is exactly the
//     removal condition the esbuild row states, made mechanical.
//
// They need `pnpm install` to have run, which is what makes them the only tests here that are not
// pure file reading: a skip on a laptop, a FAILURE under CI (issue #177), where `test / integration`
// and `suite / shuffled` both install web dependencies.
package repo_test

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The two `## ` headings in web/OVERRIDES.md that carry a machine-read table.
const (
	currentOverridesHeading = "Current overrides"
	rangeExceptionsHeading  = "Sanctioned range exceptions"
)

// pnpmStoreRel is pnpm's virtual store. Every installed package has exactly one real directory
// under it — `.pnpm/<name>@<version>[_<peers>]/node_modules/<name>` — and everything else in a
// package's node_modules is a symlink into another of those, which is what lets the walk below read
// each manifest once without following a link.
const pnpmStoreRel = "web/node_modules/.pnpm"

// dependencyFields are the manifest fields whose ranges pnpm resolves the INSTALLED tree from, and
// therefore the ones an override overrides.
//
// devDependencies are deliberately absent, and not as a simplification: a published package's dev
// dependencies are never installed by a consumer, so they say nothing about what the resolution
// contains. axe-core@4.12.1 declares `esbuild: ^0.10.x` as a devDependency — reading that field
// would report the esbuild pin as five majors outside a "parent's" range that nothing in this tree
// resolves from.
var dependencyFields = []string{"dependencies", "optionalDependencies", "peerDependencies"}

// declaredDependency is one parent's declaration of the range it accepts for a package.
type declaredDependency struct {
	parent string // `name@version`, or `web/package.json` for the workspace itself
	field  string // which of dependencyFields it came from
	spec   string // the range as the parent wrote it
}

// rangeExceptionRow is one row of web/OVERRIDES.md's "Sanctioned range exceptions" table.
type rangeExceptionRow struct {
	pkg      string
	parent   string
	declared string
	why      string
}

// overridesRowRe matches one row of the register's table: `| `pkg` | `version` | why | parent | when |`.
var overridesRowRe = regexp.MustCompile(`^\|\s*` + "`" + `([^` + "`" + `]+)` + "`" + `\s*\|\s*` + "`" + `([^` + "`" + `]+)` + "`" + `\s*\|(.+)\|(.+)\|(.+)\|\s*$`)

// issueRefRe matches a GitHub issue reference, which every row must carry.
var issueRefRe = regexp.MustCompile(`#\d+`)

// rangeExceptionRowRe matches one row of the waiver table: three backticked cells, then the reason.
var rangeExceptionRowRe = regexp.MustCompile(
	`^\|\s*` + "`" + `([^` + "`" + `]+)` + "`" +
		`\s*\|\s*` + "`" + `([^` + "`" + `]+)` + "`" +
		`\s*\|\s*` + "`" + `([^` + "`" + `]+)` + "`" +
		`\s*\|(.+)\|\s*$`)

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

// markdownSection returns the lines under a `## ` heading, up to the next one.
//
// Both tables in web/OVERRIDES.md are parsed by regexp, and a regexp loose enough to read one is
// loose enough to read a row of the other by accident. Scoping each parser to its own section means
// adding a column to either table cannot silently change what the other one sees.
func markdownSection(doc, heading string) string {
	var (
		out    []string
		inside bool
	)

	for _, line := range strings.Split(doc, "\n") {
		if strings.HasPrefix(line, "## ") {
			inside = strings.TrimSpace(strings.TrimPrefix(line, "## ")) == heading

			continue
		}

		if inside {
			out = append(out, line)
		}
	}

	return strings.Join(out, "\n")
}

// registerRows reads the "Current overrides" table in web/OVERRIDES.md.
func registerRows(t *testing.T) map[string]overrideRow {
	t.Helper()

	rows := map[string]overrideRow{}

	section := markdownSection(readRepoFile(t, "web/OVERRIDES.md"), currentOverridesHeading)

	for _, line := range strings.Split(section, "\n") {
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

// ------------------------------------------------------------------------------------------------
// The half that needs an install (issue #186): a pin against the ranges its parents DECLARE.
// ------------------------------------------------------------------------------------------------

// rangeExceptionRows reads the "Sanctioned range exceptions" table in web/OVERRIDES.md.
//
// Keyed by package and parent NAME rather than by `name@version`: parent versions churn on every
// unrelated bump, and a waiver keyed to one would have to be re-approved by whoever happened to run
// `pnpm update`. The declared range in the third column is what makes the row self-invalidating —
// the exception covers a break with THAT range, and a parent that moves to a different one is a
// different decision.
func rangeExceptionRows(t *testing.T) map[string]rangeExceptionRow {
	t.Helper()

	rows := map[string]rangeExceptionRow{}

	section := markdownSection(readRepoFile(t, "web/OVERRIDES.md"), rangeExceptionsHeading)

	for _, line := range strings.Split(section, "\n") {
		m := rangeExceptionRowRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}

		row := rangeExceptionRow{pkg: m[1], parent: m[2], declared: m[3], why: strings.TrimSpace(m[4])}
		rows[row.pkg+" over "+row.parent] = row
	}

	return rows
}

// requireInstalledWebTree gates the three tests below on a pnpm install having happened.
//
// A skip on a laptop, a FAILURE under CI, which is toolskip_test.go's rule and the reason these
// belong in `make test` at all: `make check` runs `vet` (and therefore `web-deps`) before `test`, so
// the local run that matters has the tree; `test / integration` and `suite / shuffled` install it
// explicitly. A gate that skipped quietly here would be issue #177 again, one directory over.
func requireInstalledWebTree(t *testing.T) string {
	t.Helper()

	store := filepath.Join(repoRoot(t), filepath.FromSlash(pnpmStoreRel))
	requireToolAt(t, store, pnpmStoreRel,
		"ci.yml's `test / integration` and nightly-verify.yml's `suite / shuffled` must run "+
			"`pnpm install --frozen-lockfile --ignore-scripts` in web/ (issue #177); locally, `make check` "+
			"gets there through web-deps")

	return store
}

// isPackageRootManifest reports whether a package.json is a package's OWN manifest rather than one
// of the marker files a package ships inside itself (`dist/package.json` holding `{"type":"module"}`
// is the common one). The test is positional and exact: a package root is `<...>/node_modules/<name>`
// or `<...>/node_modules/@scope/<name>`, and nothing else is.
func isPackageRootManifest(path string) bool {
	dir := filepath.Dir(path)

	if filepath.Base(filepath.Dir(dir)) == "node_modules" {
		return true
	}

	scope := filepath.Base(filepath.Dir(dir))

	return strings.HasPrefix(scope, "@") && filepath.Base(filepath.Dir(filepath.Dir(dir))) == "node_modules"
}

// declaredRanges reads every installed package's own manifest and returns, per dependency NAME,
// every parent that declares a range for it.
//
// The walk never follows a symlink — filepath.WalkDir lstats — which is exactly what is wanted here:
// a package's node_modules is symlinks into the store, so each real manifest is visited once through
// its own store directory and the same manifest is not re-read through each of its dependents.
func declaredRanges(t *testing.T, store string) map[string][]declaredDependency {
	t.Helper()

	out := map[string][]declaredDependency{}
	seen := map[string]bool{}

	add := func(path, label string) {
		data, err := os.ReadFile(path)
		require.NoErrorf(t, err, "read %s", path)

		var manifest struct {
			Name     string            `json:"name"`
			Version  string            `json:"version"`
			Deps     map[string]string `json:"dependencies"`
			Optional map[string]string `json:"optionalDependencies"`
			Peer     map[string]string `json:"peerDependencies"`
			PeerMeta map[string]struct {
				Optional bool `json:"optional"`
			} `json:"peerDependenciesMeta"`
		}

		// A manifest this walk cannot parse is skipped rather than fatal: node_modules is a third
		// party's tree, and one package shipping a package.json with a comment in it must not take
		// the gate down. Nothing here can be missed silently — the callers assert they found parents.
		if json.Unmarshal(data, &manifest) != nil || manifest.Name == "" {
			return
		}

		if label == "" {
			label = manifest.Name + "@" + manifest.Version
		}

		if seen[label] {
			return
		}

		seen[label] = true

		for _, field := range dependencyFields {
			var declared map[string]string

			switch field {
			case "dependencies":
				declared = manifest.Deps
			case "optionalDependencies":
				declared = manifest.Optional
			case "peerDependencies":
				declared = manifest.Peer
			}

			for name, spec := range declared {
				// An optional peer is a capability the parent can do without, so its range is not a
				// constraint on the resolution the way the other two are.
				if field == "peerDependencies" && manifest.PeerMeta[name].Optional {
					continue
				}

				out[name] = append(out[name], declaredDependency{parent: label, field: field, spec: spec})
			}
		}
	}

	// The workspace's own manifest is a parent too — an override for something web/package.json also
	// depends on directly is the case web/OVERRIDES.md's "What does not belong here" describes.
	add(filepath.Join(repoRoot(t), "web", "package.json"), "web/package.json")

	require.NoError(t, filepath.WalkDir(store, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || entry.Name() != "package.json" || !isPackageRootManifest(path) {
			return nil
		}

		add(path, "")

		return nil
	}), "walk %s", pnpmStoreRel)

	for name := range out {
		sort.Slice(out[name], func(i, j int) bool { return out[name][i].parent < out[name][j].parent })
	}

	return out
}

// TestWebOverrides_EveryPin_SatisfiesItsParentsDeclaredRange is rot mode 1, checked rather than
// described (issue #186).
//
// An override forces a version onto a subtree whose parents never agreed to it, and pnpm applies it
// without a word. That is the standard, correct use of the feature when somebody decided it — and
// the identical shape arrives by accident when an unrelated bump moves a parent to a major that
// wants a different version, at which point the pin is silently holding the tree at something no
// package in it declares support for. The two are indistinguishable from the lockfile; the
// difference is whether there is a reviewed row saying so.
func TestWebOverrides_EveryPin_SatisfiesItsParentsDeclaredRange(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("reads the installed web tree; run `make test`")
	}

	store := requireInstalledWebTree(t)
	ranges := declaredRanges(t, store)
	waivers := rangeExceptionRows(t)

	for pkg, pin := range packageJSONOverrides(t) {
		t.Run(pkg, func(t *testing.T) {
			t.Parallel()

			pinned, err := parseSemver(pin)
			require.NoErrorf(t, err, "web/package.json pins %s at %q, which is not a version", pkg, pin)

			parents := ranges[pkg]
			require.NotEmptyf(t, parents,
				"nothing under %s declares a dependency on %s, so this check just passed over an override "+
					"it never looked at. Either the override is dead weight (web/OVERRIDES.md's second rot "+
					"mode) or the walk stopped seeing manifests — both are failures, and a green here would "+
					"report neither.", pnpmStoreRel, pkg)

			for _, parent := range parents {
				accepted, err := parseSemverRange(parent.spec)
				require.NoErrorf(t, err,
					"%s declares %s: %q, which this range parser does not understand. It is refusing to "+
						"read that as satisfied — teach test/repo/semver_test.go the syntax, or say in "+
						"web/OVERRIDES.md why the dependency is not a semver range at all.",
					parent.parent, pkg, parent.spec)

				if accepted.satisfies(pinned) {
					continue
				}

				waiver, ok := waivers[pkg+" over "+parentName(parent.parent)]
				require.Truef(t, ok,
					"web/package.json pins %s at %s, and %s declares %s: %q in its %s — the pin is OUTSIDE "+
						"the range its parent asked for. That is web/OVERRIDES.md's first rot mode, and it "+
						"is either a deliberate exception or an accident of an unrelated bump. Add a row to "+
						"the \"%s\" table naming the parent, the range and the reason, or move the pin back "+
						"inside the range.",
					pkg, pin, parent.parent, pkg, parent.spec, parent.field, rangeExceptionsHeading)

				require.Equalf(t, parent.spec, waiver.declared,
					"web/OVERRIDES.md waives the %s pin against %s's declared range %q, but %s now declares "+
						"%q. The waiver records the range it breaks precisely so a parent bump does not "+
						"inherit an exception nobody re-read: check whether the pin is still needed against "+
						"the new range, then update the row or delete it.",
					pkg, waiver.parent, waiver.declared, parent.parent, parent.spec)
			}
		})
	}
}

// TestWebOverrides_EveryOverride_StillForcesAResolution is rot mode 2, in the form the installed
// tree can prove offline (issue #186).
//
// An override is doing work only while some parent's declared range would otherwise admit a version
// BELOW the pin. When every range already floors at or above it, the pin is forcing a resolution
// that would happen anyway — and it is not inert while it sits there: an exact pin blocks the next
// patch of the package, so the entry added to close one advisory becomes the reason the next cannot
// be closed, with `security / osv` naming the advisory and nothing naming the line in package.json.
//
// This is the esbuild row's own removal condition — "Vite's declared range starts at or above
// 0.28.2" — evaluated instead of remembered. It is a SUFFICIENT test for redundancy and not a
// necessary one: whether the registry would in fact resolve higher needs a network re-resolution,
// which the issue describes and this deliberately is not.
func TestWebOverrides_EveryOverride_StillForcesAResolution(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("reads the installed web tree; run `make test`")
	}

	store := requireInstalledWebTree(t)
	ranges := declaredRanges(t, store)

	for pkg, pin := range packageJSONOverrides(t) {
		t.Run(pkg, func(t *testing.T) {
			t.Parallel()

			pinned, err := parseSemver(pin)
			require.NoErrorf(t, err, "web/package.json pins %s at %q, which is not a version", pkg, pin)

			parents := ranges[pkg]
			require.NotEmptyf(t, parents, "nothing under %s declares a dependency on %s", pnpmStoreRel, pkg)

			var floors []string

			for _, parent := range parents {
				accepted, err := parseSemverRange(parent.spec)
				require.NoErrorf(t, err, "%s declares %s: %q, which is not a range this parser reads",
					parent.parent, pkg, parent.spec)

				floor := accepted.minimum()
				if compareSemver(floor, pinned) < 0 {
					return // this parent would have gone lower; the override is what stops it
				}

				floors = append(floors, parent.parent+" "+parent.spec+" (floor "+floor.String()+")")
			}

			t.Fatalf("the %s override pins %s, and every parent's declared range already floors at or "+
				"above it:\n  %s\nThe override is forcing a resolution that would happen without it, and "+
				"an exact pin PREVENTS the next patch — so it is not harmless while it waits. This is the "+
				"removal condition web/OVERRIDES.md states for this row: delete the override, re-lock, and "+
				"delete the row.", pkg, pin, strings.Join(floors, "\n  "))
		})
	}
}

// TestWebOverrides_EveryRangeException_IsStillBroken is the shrink-only half, and it is what makes
// the waiver table something other than a place entries go to live forever.
//
// The same rule as web/e2e/axe-allowlist.json and osv-scanner.toml: an exception that no longer
// matches anything must be DELETED in the change that stops it matching. A row for a break that has
// healed reads as coverage, and the next reader takes "js-yaml is waived" to mean more than it does.
func TestWebOverrides_EveryRangeException_IsStillBroken(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("reads the installed web tree; run `make test`")
	}

	store := requireInstalledWebTree(t)
	ranges := declaredRanges(t, store)
	overrides := packageJSONOverrides(t)

	for _, key := range sortedKeys(rangeExceptionRows(t)) {
		row := rangeExceptionRows(t)[key]

		t.Run(key, func(t *testing.T) {
			t.Parallel()

			require.Regexpf(t, issueRefRe, row.why,
				"%s: the row must cite a filed issue. Same rule as every other exception register here: "+
					"an exception nobody tracks is a decision nobody revisits.", key)

			pin, overridden := overrides[row.pkg]
			require.Truef(t, overridden,
				"web/OVERRIDES.md waives a range break for %s, which web/package.json no longer overrides. "+
					"Clearing an override means deleting its waiver in the same change.", row.pkg)

			pinned, err := parseSemver(pin)
			require.NoErrorf(t, err, "web/package.json pins %s at %q, which is not a version", row.pkg, pin)

			broken := false

			for _, parent := range ranges[row.pkg] {
				if parentName(parent.parent) != row.parent {
					continue
				}

				accepted, err := parseSemverRange(parent.spec)
				require.NoErrorf(t, err, "%s declares %s: %q, which is not a range this parser reads",
					parent.parent, row.pkg, parent.spec)

				if !accepted.satisfies(pinned) && parent.spec == row.declared {
					broken = true
				}
			}

			require.Truef(t, broken,
				"web/OVERRIDES.md waives the %s pin against %s's %q, and the installed tree no longer has "+
					"that break — either %s moved its range or the pin came back inside it. Delete the row; "+
					"and check whether the override itself is now redundant, because the reason it was "+
					"outside the range in the first place is usually the reason it existed.",
				row.pkg, row.parent, row.declared, row.parent)
		})
	}
}

// parentName strips the `@version` off a `name@version` label, leaving a scoped name intact.
func parentName(label string) string {
	at := strings.LastIndex(label, "@")
	if at <= 0 {
		return label
	}

	return label[:at]
}

// TestWebOverrides_DeclaredRanges_SeeTheInstalledTree is the floor under the three checks above.
//
// Each of them is a loop, and a loop over nothing passes. `require.NotEmpty` on one package's parents
// is not enough on its own: a walk that found only web/package.json would satisfy it while never
// having opened the store. So this asserts the shape of what was read — the store is populated, the
// two parents the register NAMES are the parents the walk found, and the devDependencies exclusion is
// real rather than incidental.
//
// The last of those is the one worth having. axe-core declares `esbuild: ^0.10.x` as a devDependency
// — five majors below the pin — and a consumer never installs it, so reading that field would report
// a violation of a range nothing in this tree resolves from, against a "parent" that is not one. If
// axe-core ever moves esbuild into a real dependency this assertion fails, and that is information
// rather than noise: it would then genuinely be a parent.
func TestWebOverrides_DeclaredRanges_SeeTheInstalledTree(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("reads the installed web tree; run `make test`")
	}

	store := requireInstalledWebTree(t)
	ranges := declaredRanges(t, store)

	parentsOf := func(pkg string) []string {
		var out []string
		for _, parent := range ranges[pkg] {
			out = append(out, parentName(parent.parent))
		}

		return out
	}

	require.Greaterf(t, len(ranges), 100,
		"the walk over %s found declarations for only %d packages. A frozen install of this workspace "+
			"resolves ~220; a number this low means the walk stopped seeing manifests and every check "+
			"built on it is passing over a tree it never read.", pnpmStoreRel, len(ranges))

	require.Contains(t, parentsOf("esbuild"), "vite",
		"vite is the parent web/OVERRIDES.md names for the esbuild pin, and the walk did not find it "+
			"declaring esbuild — so the range check ran against a parent set that is missing the one the "+
			"register is about.")
	require.Contains(t, parentsOf("js-yaml"), "@redocly/openapi-core",
		"@redocly/openapi-core is the parent web/OVERRIDES.md names for the js-yaml pin, and the walk "+
			"did not find it. A scoped name must survive the store's `@scope+name@version` directory "+
			"spelling — that is what isPackageRootManifest is for.")

	// The negative fixture, read from the tree rather than remembered — otherwise the assertion below
	// would hold just as well against a package that has no esbuild devDependency at all, which is the
	// shape of a check that has quietly stopped testing anything.
	axeManifests, err := filepath.Glob(filepath.Join(store, "axe-core@*", "node_modules", "axe-core", "package.json"))
	require.NoError(t, err)
	require.Lenf(t, axeManifests, 1,
		"expected exactly one installed axe-core under %s, found %d. It is this check's negative "+
			"fixture: if it has left the tree, point the two assertions below at another package that "+
			"declares an overridden name in devDependencies, or drop them together.",
		pnpmStoreRel, len(axeManifests))

	data, err := os.ReadFile(axeManifests[0])
	require.NoError(t, err, "read axe-core's manifest")

	var axe struct {
		Dev map[string]string `json:"devDependencies"`
	}

	require.NoError(t, json.Unmarshal(data, &axe), "parse axe-core's manifest")
	require.Containsf(t, axe.Dev, "esbuild",
		"axe-core no longer declares esbuild in devDependencies, so the assertion below proves nothing: "+
			"it would pass against any package. Re-point it or delete it.")

	require.NotContains(t, parentsOf("esbuild"), "axe-core",
		"axe-core declares esbuild only in devDependencies, which a consumer never installs. Reading "+
			"that field would report the pin as five majors outside a range nothing in this tree resolves "+
			"from — a red gate over a parent that is not one.")
}
