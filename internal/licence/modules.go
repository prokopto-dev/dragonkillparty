package licence

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
)

// Platform is one GOOS/GOARCH pair the module graph is resolved for.
type Platform struct {
	GOOS   string
	GOARCH string
}

// String renders a platform the way the Go toolchain and .goreleaser.yaml write it.
func (p Platform) String() string { return p.GOOS + "/" + p.GOARCH }

// releasePlatforms is the matrix the release actually ships: {linux,darwin,windows} x
// {amd64,arm64}, plus linux/arm. It must stay in step with .goreleaser.yaml's build matrix.
//
// This is the ONLY enumeration of the release platforms in the tooling. It used to be written twice
// — once in scripts/licence-gate.sh and once in scripts/third-party-notices.sh, in different shell
// dialects, under a comment asking the next reader to "keep in step with" the other. A platform in
// one and not the other is a hole in either the licence firewall or the attribution file, and
// nothing could see the divergence (issue #130).
var releasePlatforms = []Platform{
	{GOOS: "linux", GOARCH: "amd64"},
	{GOOS: "linux", GOARCH: "arm64"},
	{GOOS: "linux", GOARCH: "arm"},
	{GOOS: "darwin", GOARCH: "amd64"},
	{GOOS: "darwin", GOARCH: "arm64"},
	{GOOS: "windows", GOARCH: "amd64"},
	{GOOS: "windows", GOARCH: "arm64"},
}

// gatePlatforms is the subset the licence gate queries, and it is a subset on measured grounds.
//
// `go list` resolves build constraints for one GOOS/GOARCH at a time. On this repo a linux-only
// query returns 11 modules and the union returns 14: mousetrap, go-isatty and go-strftime are
// absent on linux and present in the darwin and windows binaries. A gate that ran only on the
// ubuntu CI runner would never examine them, and a GPL dependency behind `//go:build windows`
// would ship unnoticed. All three operating systems therefore appear here.
//
// One GOARCH per GOOS is enough: build constraints that add a MODULE are overwhelmingly GOOS-gated
// — syscall shims, terminal handling, filesystem notification — while GOARCH differences live
// inside a module, in its assembly and per-arch files. Measured on this repo, linux/amd64,
// linux/arm64 and linux/arm resolve identical module sets, as do darwin/amd64 and darwin/arm64, and
// these three reproduce the full release union exactly. If a dependency is ever gated on GOARCH
// alone, add that platform here — TestLicenceGate_PlatformSubset_CoversTheFullMatrix is what says
// so, by resolving the whole matrix and requiring these three to cover it.
//
// The notices generator uses the FULL list instead: reproducing one licence text too many costs a
// paragraph, while classifying one module too few costs the firewall.
var gatePlatforms = []Platform{
	{GOOS: "linux", GOARCH: "amd64"},
	{GOOS: "darwin", GOARCH: "arm64"},
	{GOOS: "windows", GOARCH: "amd64"},
}

// ReleasePlatforms returns the platforms the release ships, and the ones the notices generator
// unions over.
func ReleasePlatforms() []Platform { return slices.Clone(releasePlatforms) }

// GatePlatforms returns the platforms the licence gate resolves the module graph for — a subset of
// [ReleasePlatforms], see gatePlatforms for why that is safe and what proves it.
func GatePlatforms() []Platform { return slices.Clone(gatePlatforms) }

// Module is one entry of the resolved graph, as `go list` reports it.
type Module struct {
	// Main is true for the module being inspected. It is reported rather than filtered out: the
	// main module's presence is what makes an empty result mean "go list resolved nothing" rather
	// than "this module has no runtime dependencies", which is a legitimate state and was this
	// repository's own until sqlite landed.
	Main bool
	// Path is the module path, Version its selected version (empty for the main module and for a
	// filesystem `replace`), Dir its on-disk root.
	Path    string
	Version string
	// Dir comes from {{.Module.Dir}} directly. Reconstructing $GOMODCACHE paths by hand would break
	// on `replace` directives, and it is exactly the `replace` case that lets the licence gate's
	// negative fixtures run offline against a fabricated GPL module. It is empty under -mod=vendor.
	Dir string
}

// listFormat asks `go list` for the four fields Module carries. Packages from the standard library
// have no module and emit an empty line, which [RuntimeModules] drops.
const listFormat = `{{if .Module}}{{.Module.Main}}|{{.Module.Path}}|{{.Module.Version}}|{{.Module.Dir}}{{end}}`

// RuntimeModules returns the union, across platforms, of the modules `go list -deps pattern`
// resolves in root. This is the one enumeration both the licence gate and the third-party notices
// generator read the graph through.
//
// pattern is `./...` for the gate and `./cmd/dkp` for the notices generator, and the difference is
// deliberate on both sides. WITHOUT -test is the whole point of the query:
//
//   - With -test, or via `go list -m all`, the set includes modules reachable only from a
//     dependency's own test binary. github.com/hashicorp/golang-lru/v2 (MPL-2.0) is in this repo's
//     `go list -m all` today purely because modernc.org/libc's tests import it — it is not linked
//     into dkp, and failing on it would be failing on code we do not ship.
//   - Scoping the GATE to ./cmd/dkp would be worse: cmd/dkp does not import internal/store yet, so
//     the binary's graph is cobra + pflag and the entire modernc.org/sqlite tree — the dependency
//     that prompted the gate — would go unexamined. The notices generator scopes to the binary
//     because attribution is owed for what the binary links.
//
// The platform queries are independent, so they run concurrently. Serially this was the slowest
// part of `make check`, and the gate's negative fixtures pay it once per fixture.
//
// The environment of the query is fixed here rather than by the caller — GOWORK and GOFLAGS both
// change which modules resolve, and both callers must see the same graph. See the comment on
// cmd.Env below.
//
// It is an error, never an empty result, when: `go list` fails for any platform; `go list` matches
// no packages (it EXITS ZERO in that case, warning only on stderr, which is a vacuous-pass path);
// or the union is empty, since the main module always appears in its own dependency list.
func RuntimeModules(root string, platforms []Platform, pattern string) ([]Module, error) {
	if len(platforms) == 0 {
		return nil, fmt.Errorf("resolve the runtime module graph in %s: no platforms to query", root)
	}

	type result struct {
		stdout string
		stderr string
		err    error
	}

	var (
		wg      sync.WaitGroup
		results = make([]result, len(platforms))
	)

	for i, p := range platforms {
		wg.Add(1)

		go func() {
			defer wg.Done()

			var stdout, stderr bytes.Buffer

			cmd := exec.Command("go", "list", "-deps", "-f", listFormat, pattern)
			cmd.Dir = root
			// GOWORK=off is load-bearing, not tidiness. Under a go.work file every workspace member
			// reports an EMPTY Module.Version — the same signal the main module gives — so a
			// third-party module brought in with `use ./thing` would be skipped as if it were
			// first-party, and the gate would print its success banner having examined nothing.
			// Module.Main is the correct main-module test and is what callers filter on.
			//
			// GOFLAGS is cleared for the reason `make licence-gate` strips it from its recipe: it can
			// carry -mod=vendor or -tags, either of which changes which modules `go list` resolves,
			// and a developer's environment must not decide which dependency graph is inspected. It
			// is cleared HERE, in the one enumeration both callers share, rather than on one recipe —
			// the gate stripped it and `make third-party-notices` did not, so the same function could
			// classify one graph while the attribution file shipped with a release described another
			// (issue #141). An empty value is how the go command spells "no flags"; a later entry
			// wins over anything os.Environ() carried.
			cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=", "GOOS="+p.GOOS, "GOARCH="+p.GOARCH)
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr

			err := cmd.Run()
			results[i] = result{stdout: stdout.String(), stderr: stderr.String(), err: err}
		}()
	}

	wg.Wait()

	var (
		modules []Module
		seen    = make(map[Module]bool)
	)

	for i, p := range platforms {
		r := results[i]
		if r.err != nil {
			return nil, fmt.Errorf("go list failed for %s in %s: %w\n%s", p, root, r.err, indent(r.stderr))
		}

		// `go list ./...` EXITS ZERO when nothing matches, warning only on stderr. Treating that as
		// a successful empty result would make the gate report success having examined nothing.
		if strings.Contains(r.stderr, "matched no packages") {
			return nil, fmt.Errorf("go list matched no packages in %s for %s — the gate examined nothing", root, p)
		}

		for line := range strings.SplitSeq(r.stdout, "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}

			fields := strings.SplitN(line, "|", 4)
			if len(fields) != 4 {
				return nil, fmt.Errorf("parse go list output for %s in %s: malformed line %q", p, root, line)
			}

			m := Module{
				Main:    fields[0] == "true",
				Path:    fields[1],
				Version: fields[2],
				Dir:     fields[3],
			}
			if !seen[m] {
				seen[m] = true

				modules = append(modules, m)
			}
		}
	}

	// The main module always appears in its own dependency list, so an empty result means `go list`
	// resolved nothing rather than "this module has no dependencies".
	if len(modules) == 0 {
		return nil, fmt.Errorf("go list resolved no modules at all in %s — the graph could not be read", root)
	}

	slices.SortFunc(modules, func(a, b Module) int {
		if c := strings.Compare(a.Path, b.Path); c != 0 {
			return c
		}

		return strings.Compare(a.Version, b.Version)
	})

	return modules, nil
}

// Dependencies drops the main module from a resolved graph, leaving what the project depends on.
func Dependencies(modules []Module) []Module {
	deps := make([]Module, 0, len(modules))

	for _, m := range modules {
		if !m.Main {
			deps = append(deps, m)
		}
	}

	return deps
}

// indent prefixes every line of s with two spaces, so a subprocess's stderr reads as quoted output
// inside an error message rather than as more of the message.
func indent(s string) string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return ""
	}

	return "  " + strings.ReplaceAll(s, "\n", "\n  ")
}
