// Package specgate asserts the properties of openapi/openapi.json that regenerating it cannot
// establish.
//
// `make verify-generated` proves the committed spec matches the code. It cannot prove the spec is
// *correct*, because a wrong spec regenerates to the same wrong bytes every time. This package
// covers the properties named in .github/workflows/ci.yml's spec-drift job, as rules SPEC001-SPEC008.
// The command is internal/specgate/verifyspec, behind `make verify-spec`.
//
// GO RATHER THAN PYTHON, AND THAT IS THE POINT (issue #127). This was scripts/verify-spec.py, and
// issue #83 is what the Python cost: the script annotated a return as `dict | None`, which PEP 604
// only made legal in 3.10 and which is evaluated when the function is DEFINED, so on macOS's stock
// 3.9.6 the module raised TypeError at import — before it had read a byte of the spec. What the
// contributor saw was `make check` reporting the SPEC GATE failing, on a tree whose spec was fine,
// pointing at openapi/openapi.json, the one file nobody is allowed to hand-edit. An environment
// fault wearing a content fault's clothes is the most expensive shape a gate failure has, and the
// only durable fix is to stop having an interpreter: `go` is pinned by the go directive in go.mod,
// GOTOOLCHAIN=local means CI never silently downloads another one, and a gate written in the
// language the repository already compiles cannot fail for a reason that has nothing to do with its
// subject. Epic #125's two-bucket principle: a script that parses or computes moves to Go, thin glue
// around a real CLI stays bash.
//
// A LIBRARY WITH A THIN COMMAND, rather than one main package like internal/ledger/enumgen, for two
// reasons the Python had no way to get. The rule engine is unit-tested directly
// (internal/specgate/specgate_test.go) instead of only black-box-tested through a subprocess, which
// is the improvement epic #125 names. And internal/api's own test imports IsOperationID, so the
// "arch_test.go and this gate must agree on lowerCamelCase" comment on internal/api.lowerCamelCase is
// now a mechanism rather than a hope — one checks the Huma registry, the other checks the committed
// JSON, and an operationId that passes one and fails the other is a merge blocked for a reason
// nobody can reproduce.
//
// IT IMPORTS NOTHING FROM THIS REPOSITORY, deliberately, and must stay that way. A gate that needs
// internal/api to compile cannot run on the tree where it is most needed, and internal/api's test
// imports this package — so a repo import here is also an import cycle waiting to happen. The
// constants that mirror internal/api are asserted equal by tests on both sides instead.
//
// WHAT THIS GATE DELIBERATELY DOES NOT CHECK: the `Hidden: true` allowlist of canonical conventions
// §7. It is not implementable here. Huma never adds a hidden operation to `paths`
// (huma/v2 openapi.go:1570), so a hidden operation is simply ABSENT from this document and no amount
// of reading the JSON can tell a correctly-hidden operation from one that was never written. That
// assertion lives in internal/api/arch_test.go, which sees the in-process registry — which is also
// where docs/development/first-ten-prs.md's acceptance criteria put it.
package specgate

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Where the gate reads its two inputs from, relative to the repository root.
const (
	// SpecFile is the generated document under test.
	SpecFile = "openapi/openapi.json"

	// CatalogueFile is the authz catalogue SPEC005 resolves permission keys against.
	CatalogueFile = "internal/authz/catalogue.go"
)

// EnvRepoRoot names the tree to inspect, so the negative fixtures in test/repo can run against a
// tree in t.TempDir(). Same contract as the gate scripts; `make verify-spec` strips it with `env -u`.
const EnvRepoRoot = "DKP_REPO_ROOT"

// EnvBaseRef and DefaultBaseRef select the revision SPEC003 compares operationIds against.
//
// An EMPTY value disables the rename check. That exists for ONE caller — the fixtures in test/repo,
// which build a spec in t.TempDir() where there is no history to compare against and would otherwise
// all fail on a spurious SPEC003. It is a way to weaken the gate, so it is fenced the same way
// EnvRepoRoot is: the Makefile recipe strips it with `env -u`, and
// TestMakefile_VerifySpec_StripsBaseRefEnv asserts that it does. Do not add a caller.
const (
	EnvBaseRef     = "DKP_SPEC_BASE_REF"
	DefaultBaseRef = "origin/main"
)

// ANSI colours, unconditional. Every gate in this repository colours its own output and none of them
// probes for a TTY; `make check`'s output is read in a terminal or in a CI log that renders them.
const (
	colourRed    = "\033[31m"
	colourGreen  = "\033[32m"
	colourYellow = "\033[33m"
	colourReset  = "\033[0m"
)

// The two fatal states in which no rule can run. Both are failures rather than skips.
var (
	// ErrSpecMissing is returned when root holds no spec at all.
	ErrSpecMissing = errors.New(SpecFile + " does not exist — run `make gen`")

	// ErrVacuous is returned when the document declares no operations.
	//
	// A document with no operations satisfies every per-operation rule by having nothing to check.
	// That is the one state in which this gate could report success without having looked at
	// anything, and it is reachable: `dkp openapi` against a build where registration was
	// accidentally removed emits exactly this.
	ErrVacuous = errors.New(SpecFile + " declares no operations — the gate would pass vacuously")
)

// Violation is one rule failure, carrying the rule id separately so a caller can assert on which
// rule fired rather than on an exit code. A gate that fires for the wrong reason must be
// distinguishable from one that fires for the right one.
type Violation struct {
	// Rule is the id in the shape scripts/repo-gates.sh uses, e.g. "SPEC001".
	Rule string

	// Message says what is wrong and what to do about it.
	Message string
}

// Note is something the run needs to say that is not a failure — a check that legitimately could
// not run, and why.
type Note string

// Result is the outcome of one run over one document.
type Result struct {
	// Operations is how many operations were inspected, over paths and webhooks.
	Operations int

	Violations []Violation
	Notes      []Note
}

// report accumulates a Result while the rules run.
//
// A value passed to each rule rather than package-level state, for the reason
// .claude/rules/go-idioms.md gives: `-shuffle=on` plus `t.Parallel()` turn a package-level
// accumulator into an intermittent failure, and the unit tests here run the rules concurrently.
type report struct {
	violations []Violation
	notes      []Note
}

// violation records a rule failure. The signature is a printf wrapper so `go vet` checks the format
// strings at every call site.
func (r *report) violation(rule, format string, args ...any) {
	r.violations = append(r.violations, Violation{Rule: rule, Message: fmt.Sprintf(format, args...)})
}

// note records something the run could not do, with the reason.
func (r *report) note(format string, args ...any) {
	r.notes = append(r.notes, Note(fmt.Sprintf(format, args...)))
}

// Check reads the spec under root, applies every rule and returns what it found.
//
// baseRef is the revision SPEC003 compares operationIds against; empty disables that one rule and
// says so in a Note. The returned error is reserved for the states in which no rule could run at all
// — ErrSpecMissing, ErrVacuous, or unparseable JSON — because those are not "the spec is wrong in
// way N", they are "there is nothing here to check", and conflating the two is how a gate ends up
// passing vacuously.
//
// root is used to build paths and as the working directory for git; nothing here calls os.Chdir,
// which is process-global and would make the tests unable to run in parallel.
func Check(root, baseRef string) (Result, error) {
	raw, err := os.ReadFile(filepath.Join(root, SpecFile))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Result{}, ErrSpecMissing
		}

		return Result{}, fmt.Errorf("read %s: %w", SpecFile, err)
	}

	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return Result{}, fmt.Errorf("%s is not valid JSON: %w", SpecFile, err)
	}

	ops := operations(doc)
	if len(ops) == 0 {
		return Result{}, ErrVacuous
	}

	rep := &report{}

	ids := checkOperationIDs(rep, ops)
	checkNoRenames(rep, root, baseRef, ids)
	checkPermissionsResolve(rep, root, checkSecurityAndPermission(rep, ops))
	checkMoneyAndFloats(rep, doc)
	checkPathsAreVersioned(rep, ops)
	checkNoEQdkpConfigKeys(rep, doc)

	return Result{Operations: len(ops), Violations: rep.violations, Notes: rep.notes}, nil
}

// Render writes res in the shape the test fixtures and CI logs read, and returns the process exit
// code: 0 when the document conforms, 1 when it does not.
//
// Notes go to stdout because they are not failures. Violations go to stderr, prefixed with the count,
// because `make check`'s failure output is what a contributor reads first.
func Render(stdout, stderr io.Writer, res Result) int {
	var notes string

	for _, n := range res.Notes {
		notes += fmt.Sprintf("  %snote%s  %s\n", colourYellow, colourReset, n)
	}

	writeAll(stdout, notes)

	if len(res.Violations) > 0 {
		failures := fmt.Sprintf("\n%s  %s failed %d check(s)%s\n\n",
			colourRed, SpecFile, len(res.Violations), colourReset)

		for _, v := range res.Violations {
			failures += fmt.Sprintf("  %s[%s]%s %s\n", colourRed, v.Rule, colourReset, v.Message)
		}

		writeAll(stderr, failures+"\n")

		return 1
	}

	writeAll(stdout,
		fmt.Sprintf("  %s%d operation(s), all conforming%s\n", colourGreen, res.Operations, colourReset))

	return 0
}

// writeAll writes s to w, discarding the write error.
//
// The deliberate, reviewable waiver AGENTS.md §"Error handling" describes, in the case it names: a
// failed write to the gate's own report stream has nothing to do about it, because the only channel
// available for reporting the failure is the one that just failed. The exit code is what the caller
// acts on and it is returned regardless.
func writeAll(w io.Writer, s string) {
	if s == "" {
		return
	}

	_, _ = io.WriteString(w, s)
}

// Run is the whole gate: check root and report, returning the process exit code.
//
// The load failures print with no rule id, deliberately. "The spec is wrong in way N" and "there is
// no spec" are different problems and only the first has a rule to name.
func Run(root, baseRef string, stdout, stderr io.Writer) int {
	res, err := Check(root, baseRef)
	if err != nil {
		writeAll(stderr, fmt.Sprintf("  %s%v%s\n", colourRed, err, colourReset))

		return 1
	}

	return Render(stdout, stderr, res)
}

// operation is one (section, path-or-event, method, operation) tuple from the document.
type operation struct {
	// section is "paths" or "webhooks". Webhooks are in scope for the operationId rules and out of
	// scope for the security and permission rules: a webhook describes a request this server SENDS
	// to a subscriber's endpoint, so it has no permission of ours to declare and its security is the
	// subscriber's business. Its operationId still has to be unique, because both SDK generators
	// derive a type name from it in the same namespace as the REST methods.
	section string

	// name is the path template or the webhook event name.
	name string

	method string
	op     map[string]any
}

// where names the operation the way a failure message should, e.g. "GET /api/v1/things".
func (o operation) where() string {
	return strings.ToUpper(o.method) + " " + o.name
}

// key identifies an operation across two revisions of the document, for SPEC003.
type key struct {
	name   string
	method string
}

// methods are the HTTP methods an OpenAPI path item may carry. Anything else in a path item ($ref,
// parameters, summary, servers) is not an operation and must not be treated as one.
//
// A function returning a fresh literal rather than a package-level slice, for the reason
// .claude/rules/go-idioms.md gives about package-level mutable state.
func methods() []string {
	return []string{"get", "put", "post", "delete", "options", "head", "patch", "trace"}
}

// operations returns every operation in paths and webhooks.
//
// Sorted by name, which the Python this replaces got for free: a JSON object decoded into a Python
// dict preserves document order, and a Go map has no order at all. Two runs of a gate that report the
// same violations in a different sequence make the diff between two CI logs unreadable, and SPEC002's
// message names both sides of a duplicate ("used by both X and Y") so the order is visible output.
func operations(doc map[string]any) []operation {
	var found []operation

	for _, section := range []string{"paths", "webhooks"} {
		items, _ := doc[section].(map[string]any)

		for _, name := range slices.Sorted(maps.Keys(items)) {
			item, ok := items[name].(map[string]any)
			if !ok {
				continue
			}

			for _, method := range methods() {
				op, ok := item[method].(map[string]any)
				if !ok {
					continue
				}

				found = append(found, operation{section: section, name: name, method: method, op: op})
			}
		}
	}

	return found
}
