// Package typedlaw is the TYPE-AWARE second opinion on the architectural laws — the analyzers
// `make lint-laws` runs, and the pass `internal/repogate` deliberately cannot be.
//
// It is repo tooling, not product surface, on exactly the terms internal/repogate and
// internal/licence are: not a `dkp` subcommand, and never in the shipped binary's package graph.
// TestTypedLaw_IsNotLinkedIntoTheBinary asserts that the way its two neighbours do.
//
// # What this is for
//
// internal/repogate holds the LAWS and blocks the merge. It reads one file at a time with
// go/parser, it needs no build, and that is the property that makes it the gate: it runs against a
// deliberately tainted tree in t.TempDir() with no go.mod, and against a repository mid-sequence
// where a rule is installed before the code it gates (ROADMAP sequencing doctrine #1).
//
// Everything it cannot see, it cannot see because it has no types. A syntax pass reads `sql` and
// `time` and `huma` as identifiers somebody happened to type, so three whole classes of violation
// walk past it:
//
//   - a dot-imported `. "time"` makes `Now()` a bare call with no selector to match;
//   - a type alias, a named type or an embedded field reaches *sql.DB without the file ever
//     naming database/sql;
//   - `rate := 0.15` is a float64 in internal/strategy with the word `float64` nowhere in the file.
//
// And in the other direction, a syntax pass over-reports: `r.URL.Query()` is net/url's accessor and
// SQL002 has to exclude it by shape (a zero-argument call), because the only other answer a
// line-scoped allowlist can give is to drop the line — including the line where a genuine
// `conn.QueryContext(ctx, …)` sits next to it.
//
// Type information answers all four questions directly. A method's DECLARING PACKAGE is what makes
// it a database call, not its name; an object's identity is what makes `Now` the wall clock, not
// the letters before the dot.
//
// # Advisory, and additive
//
// This pass never replaces a rule in internal/repogate and never weakens one.
// TestTypedLaws_AreAdditive asserts that in code rather than in this comment: every rule id here
// still exists there, and `make lint-repo` remains what a merge is blocked on.
//
// It is advisory BY CONSTRUCTION rather than by `continue-on-error` — scripts/typed-laws.sh runs it
// in MODE=advise, which prints every finding, emits a `::warning::` annotation and exits 0.
// MODE=enforce exits non-zero on a finding instead, works today, and is what test/repo drives so
// that the negative fixtures assert on the analyzer rather than on a printed line.
//
// Advisory does NOT mean it cannot fail. A pass that exits 0 because it never ran is worse than no
// pass at all, so a BROKEN INVOCATION — `go list` failing, no export data, a package that will not
// type-check — is a hard failure in both modes. That is the same line scripts/migrate-lint.sh draws
// whatever its own MODE says, and the same one `make govulncheck` draws when its binary is missing.
//
// The mode split itself is that script's precedent too: it landed advisory under issue #131, while
// its analyzers were unproven against SQLite's 12-step rebuild, and became a gate under #136 once
// they had been observed against this tree's real migrations. Same path, same reason, and issue
// #241 is where the equivalent evidence for these laws is gathered.
//
// # No new dependency: go/types over `go list -export`
//
// Issue #172 proposed a `go/analysis` multichecker, and named its own prerequisite: a dependency
// proposal for golang.org/x/tools, which AGENTS.md makes a human decision and which nothing in this
// change is entitled to make. It is not needed. Every driver in that module exists to hand a
// type-checked package to an analyzer, and the standard library already ships both halves:
//
//	go list -json -deps -export ./...   builds the tree and reports each package's export data
//	go/importer.ForCompiler(…, "gc", lookup)  reads that export data — the documented module-aware form
//	go/types.Config.Check                     type-checks one package against it
//
// ~40 packages in this module, 1.3 s from a warm build cache, no module-graph change and no
// attribution owed. What is given up is the analysis framework's fact plumbing (an analyzer here
// cannot publish a fact for a downstream package to consume), and no law needs it: every rule below
// is a property of one package read against its own type information.
//
// See docs/adr/0027-type-aware-second-opinion-on-the-laws.md.
//
// # What it reads, and what it does not
//
// `go list -export` builds the NON-TEST package, so this pass reads GoFiles and never _test.go.
// That is a deliberate boundary rather than a gap: internal/repogate scans every .go file including
// tests and stays the gate over them, SQL003 exists precisely because a test affordance must not
// reach production code, and a pass that had to load test variants would need the `pkg [pkg.test]`
// import-path rewriting that is the one genuinely awkward part of go/packages.
//
// Law 4 — no raw fetch outside web/src/api — has no Go in it, so nothing here can be a second
// opinion about it. WEB001 in internal/repogate remains the gate, and the type-aware pass over that
// tree is `tsc`, which `make vet` already runs.
//
// # The rules
//
//	ROUTE001  huma.Register reached outside internal/api, by OBJECT               (law 1)
//	SQL001    database/sql.Open / OpenDB reached outside internal/store, by OBJECT (law 2)
//	SQL002    a database/sql method called outside internal/store, by DECLARING PACKAGE (law 2)
//	SQL003    a store ForTest raw-SQL helper reached from the non-test build
//	SQL004    a database/sql handle TYPE held outside internal/store              (law 2, stated)
//	PURE001   internal/strategy reaches internal/store TRANSITIVELY               (law 3)
//	PURE002   internal/strategy reaches math/rand transitively                    (law 3)
//	CLOCK001  time.Now reached outside internal/clock, by OBJECT
//	CLOCK002  clock.System reached inside internal/strategy, by OBJECT
//	MONEY001  an expression whose TYPE is a float in internal/ledger or internal/strategy
//
// SQL004 is the only id that is not already in internal/repogate, and it is the law as AGENTS.md
// states it — "*sql.DB is held only by internal/store" — rather than an approximation of it. It
// cannot have a fixture there for the reason this package exists: deciding that a field's type IS
// database/sql.DB requires resolving the type, and a syntax pass that matched the literal text
// `*sql.DB` is exactly the rule an alias walks past. Issue #172's acceptance asks for that reason to
// be written down, and this paragraph is it.
package typedlaw
