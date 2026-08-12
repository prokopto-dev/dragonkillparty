// Package repogate is the repository's architectural gate engine: the rules `make lint-repo` runs,
// and the fences AGENTS.md's architectural laws and the AGPL firewall rest on.
//
// It is repo tooling, not product surface — stdlib only, and deliberately NOT a `dkp` subcommand,
// because nothing an operator runs should carry the gates and cmd/dkp stays the only shipped
// binary. TestRepoGates_IsNotLinkedIntoTheBinary asserts the binary's package graph never reaches
// here, the same way TestLicence_IsNotLinkedIntoTheBinary does for internal/licence.
//
// # The rules
//
// Each rule has an id, and a failure prints file:line plus the rule that fired. They are cheap,
// they run on every PR, and they are deliberately dumb: a rule an agent cannot argue with.
//
//	ROUTE001  an HTTP route declared with huma.Register outside internal/api      (law 1)
//	SQL001    sql.Open / sql.OpenDB outside internal/store                        (law 2)
//	SQL002    .Query / .QueryRow / .Exec outside internal/store                   (law 2)
//	SQL003    a ForTest raw-SQL helper called outside a _test.go file
//	PURE001   internal/strategy imports internal/store                            (law 3)
//	PURE002   math/rand in internal/strategy                                      (law 3)
//	CLOCK001  time.Now outside internal/clock
//	CLOCK002  clock.System constructed in internal/strategy
//	MONEY001  a float type in internal/ledger or internal/strategy
//	MONEY002  SQLite's total() — it returns a float where sum() returns an integer
//	MONEY003  a REAL / NUMERIC / DECIMAL column type
//	WEB001    raw fetch / XMLHttpRequest outside web/src/api                      (law 4)
//	WEB002    dangerouslySetInnerHTML
//	WEB003    an off-origin URL anywhere in the SPA
//	DS001     a raw hex colour outside the token layer
//	DS002     a raw px value outside the token layer
//	MIG001    DDL inside a goose Down block
//	MIG002    a backtick-quoted identifier in a migration
//	MIG003    a migration frozen by db/migrations-sqlite/SHIPPED.lock was modified
//	ENUM001   a hand-written string-enum CHECK in db/schema.hcl
//	ADR001    a change that needs a decision record carries one
//	GOLD001   '-update' in a command a CI job runs — the golden-file rewrite fence
//	PIN001    an action not pinned to a 40-character commit SHA
//	QEMU001   QEMU in a workflow — multi-arch is cross-compiled, never emulated
//	AGPL001   an EQdkp Plus identifier outside the allowlisted files
//	AGPL002   an EQdkp Plus config key used as a DKP schema name
//	GATE000   the engine itself could not run
//
// # Rules whose target tree does not exist yet pass vacuously
//
// That is correct, not a hole: the rule is installed before the code it gates, which is the whole
// point (ROADMAP sequencing doctrine #1). [hasTree] is the guard, and it requires a directory that
// holds at least one file — an existing-but-empty tree skips too, because that is what a
// half-created directory looks like.
//
// The one thing a vacuous skip must never be is silent. Every skip prints its rule id, so a CI log
// distinguishes "the rule ran and found nothing" from "the rule was not there". `make lint-repo`
// strips DKP_REPO_ROOT with `env -u` for the same reason, and
// TestLintRepo_HostileRepoRootEnv_StillScansTheRealTree is what keeps that strip in place.
//
// # Two engines, and the split is by what the rule is about
//
// [textRules] are the config-shaped rules: a tree, a file glob, a pattern, an allowlist. Their
// subject is text — a YAML `uses:` line, a CSS colour, a SQL column type, a TypeScript import — and
// there is no parser for "every file the SPA ships" that would be more honest than reading the
// lines.
//
// [astRules] are the Go-syntax laws. A route declaration, a database handle, an import, a float, a
// construction of the real clock: all of them are AST properties that a regex can only approximate.
// The approximation is what produced the defects this package was written from — a grep that fires
// on the prose documenting it, an exclusion that has to live in the pattern rather than the
// allowlist because the allowlist is line-scoped (see SQL002 in [astRules]), a helper that silently
// stripped nothing for months. Reading the syntax removes the whole class: a comment is not a call
// and a string is not an import, without anybody having to arrange for that.
//
// # Why not golang.org/x/tools/go/analysis
//
// Issue #123 asked for `go/analysis` analyzers, and this package implements their SHAPE — a table
// of {id, description, tree, run} values, each a pure function over one parsed file — while
// deliberately not taking the dependency. Two reasons, and the second is the one that decides it.
//
//  1. It is a new direct dependency, and AGENTS.md makes that a human decision.
//
//  2. Every `go/analysis` driver — singlechecker, multichecker, `go vet` — loads TYPE-CHECKED
//     packages through go/packages, which needs a module that builds. The negative fixtures these
//     rules exist for are the opposite of that: deliberately tainted trees in t.TempDir() with no
//     go.mod, no imports resolved and no intention of compiling. So is a repository mid-sequence,
//     where a rule is installed before the code it gates. A driver that cannot run on either would
//     have to be pointed at the real tree only, which is precisely the state where a gate is
//     trusted rather than tested.
//
// go/parser answers every question these rules ask — they are all syntactic — and answers it on a
// tree that does not build. See docs/adr/0018-repo-gates-as-a-go-engine.md.
//
// # Comment stripping is per-rule data, and the AGPL firewall opts out
//
// Most rules drop whole-line comments before matching, because a gate that fires on the prose
// documenting it is a gate people route around: db/schema.hcl's header describes the enum shape,
// web/src/styles/fonts.css explains the third-party @import it is NOT writing, and the committed
// workflows say "No QEMU" in as many words.
//
// AGPL001 strips nothing, and that is the one place where the difference matters. Everywhere else a
// banned token inside a comment is prose about the rule; there it is the thing itself. Transcribing
// EQdkp Plus's AGPL-3.0 PHP into a Go comment is exactly as much of a licence violation as
// transcribing it into code, and "I only pasted it as a reference" is precisely how it happens. A
// well-meaning refactor that gave the firewall the same stripping as its neighbours — for
// consistency — would open it, and TestRepoGates_EQdkpIdentifierOutsideAllowlist_FailsGate is the
// fixture that would say so.
package repogate
