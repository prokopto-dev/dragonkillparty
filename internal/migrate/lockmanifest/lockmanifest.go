// Package lockmanifest maintains and checks db/migrations-sqlite/SHIPPED.lock, the append-only
// manifest of migrations that have already shipped.
//
// db/migrations-sqlite/SHIPPED.lock holds one `filename sha256` row per migration that has appeared
// in a tagged release. A migration in that list has already run on somebody's database, so editing
// it makes an existing install and a fresh install end up with different schemas — and "works on a
// fresh install, breaks on upgrade" is the most damaging bug class for this audience: a volunteer
// officer with ten years of guild DKP and no backup discipline. See AGENTS.md and
// .claude/rules/migrations.md > "Shipped migrations are frozen".
//
// # Three callers, three different questions
//
//	verify              Every listed file still exists and still hashes to its recorded value.
//	                    Runs on EVERY PR as MIG003 in scripts/repo-gates.sh. It deliberately does
//	                    NOT require completeness: a migration added on a feature branch has not
//	                    shipped yet and must not be listed. Requiring it here would make the gate
//	                    fire on the one change it is supposed to permit.
//
//	verify --complete   The above, PLUS every migration in the tree is listed. That is only true at
//	                    a tag, where by definition everything present ships — so this is the release
//	                    path (`make release-shipped-lock`, release.yml's `prepare` job). It runs
//	                    before any image, binary or moving tag exists, which is the last point at
//	                    which a missing row is free to fix.
//
//	seal                Append a row for every migration not yet listed. Run when preparing a
//	                    release (`make shipped-lock-seal`, the cut-release skill), so the manifest
//	                    is sealed IN the Release PR and reviewed by the human who merges it. CI
//	                    never pushes to main, so the release job verifies rather than writes.
//	                    Existing rows are never rewritten and never removed — that is what
//	                    "append-only" means here, and it is the entire point of the file.
//
//	init                Create the manifest with its header and no rows. Refuses if it already
//	                    exists, so it can never destroy a row. This is how the file is bootstrapped;
//	                    after that, `seal` is the only writer.
//
// # Why this is not atlas.sum
//
// atlas.sum protects the CURRENT migration set against tampering, and `make verify-generated` fails
// when it drifts. That is a real control and a different promise: atlas.sum tracks the tree as it
// is, so regenerating it after an edit makes the edit legitimate again. SHIPPED.lock records what a
// USER'S DATABASE has already executed, which nothing in this repository is allowed to change after
// the fact. An edit plus a re-hash walks past atlas.sum and is exactly what MIG003 stops.
//
// # Scope is db/migrations-sqlite
//
// db/migrations-postgres is generated and compiled in CI only — it is never applied to a user's
// database in 1.x — so freezing it would be a promise about nobody's data. Rows are basenames, which
// is the form .claude/hooks/guard-protected-paths.sh matches against for BOTH dialect directories.
//
// # Why this is Go and not a shell script
//
// It was scripts/shipped-lock.sh until issue #129. The row parsing and the merge-base history
// reasoning are the highest-data-loss-blast-radius logic in the repository's gates, and in bash they
// could only be tested through a subprocess: every assertion had to be made about stdout. Here
// parseManifest and historyCheck are ordinary functions with ordinary table-driven tests, so
// "a malformed row is a failure, never a skip" and "the merge base is a PREFIX, not a set" are
// asserted directly rather than inferred from a printed line. The negative fixtures in
// test/repo/migration_gates_test.go carried over unchanged and still drive the real binary.
//
// # Why this is a library and not only a command (issue #173)
//
// It was `package main`, so internal/repogate — the engine that runs MIG003 — could not import it
// and ran it with `go run` instead. That nested one Go build inside another on every `make
// lint-repo` and inside each of the thirty subprocesses test/repo spawns, and it flattened the
// answer: a manifest that DISAGREES and a check that COULD NOT RUN both arrived as a non-zero exit
// and captured stdout. [Verify] returns [ErrDisagrees] for the first and an ordinary wrapped error
// for the second, which is a distinction the gate can report and a reader can act on.
//
// TWO CALLERS, ONE IMPLEMENTATION, and that is not negotiable: the release path runs the same code
// with one more assertion (`verify --complete`). A second copy is what this file exists to prevent
// happening to the manifest itself, and it would be no better here.
//
// It lives beside internal/migrate rather than in cmd/dkp because it is dev tooling: cmd/dkp is the
// product binary and an officer never runs a repository gate. internal/ledger/enumgen sets that
// precedent and gives the same reason.
package lockmanifest

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// migrationDir and lockFile are repo-root-relative, and they are also exactly what every message
// prints. A failure names db/migrations-sqlite/SHIPPED.lock, never the absolute path of the tree
// under inspection: the negative fixtures run against a t.TempDir() and assert that its path never
// appears in the output, because a gate that reports a temp path has told the reader nothing about
// which file in their checkout to look at.
const (
	migrationDir = "db/migrations-sqlite"
	lockFile     = migrationDir + "/SHIPPED.lock"

	// defaultBaseRef is the ref the append-only history check compares against. See historyCheck
	// for why it is a merge base against this ref and not the ref itself.
	defaultBaseRef = "origin/main"
)

// Exit codes. 2 is usage, distinct from 1, so a caller that mistypes a mode is not reported as a
// tampered manifest.
const (
	exitOK    = 0
	exitFail  = 1
	exitUsage = 2
)

// manifestHeader is the prose a freshly created manifest carries. It is for a human who opens the
// file, and it lives here rather than in the file so `init` and any future writer cannot disagree
// about it. It is byte-identical to the header the shell implementation wrote, which is the header
// the committed db/migrations-sqlite/SHIPPED.lock already carries.
const manifestHeader = `# SHIPPED.lock — migrations that have shipped in a tagged release. APPEND-ONLY.
#
# One "<filename> <sha256>" row per migration, in the order they shipped. A row here means the file
# has already run on somebody's database: it is frozen, and correcting it means writing a NEW
# migration, never editing this one. Rows are appended by "make shipped-lock-seal" when a release
# is prepared, and are never rewritten or removed.
#
# Enforced by MIG003 in scripts/repo-gates.sh on every PR (every listed hash must still match) and
# by "make release-shipped-lock" at tag time (every migration present must also be listed).
# Blank lines and lines starting with # are ignored.
`

// tree is the checkout under inspection.
//
// DKP_REPO_ROOT lets a test point this command at a tree other than this checkout, exactly as
// repo-gates.sh and install-atlas.sh do, so the gate can be tested rather than trusted: a test
// writes a lock file and a deliberately altered migration into t.TempDir() and requires a non-zero
// exit. Such a fixture cannot live in the repo, because the real `make lint-repo` would find it.
type tree struct {
	root    string // the tree to inspect; every path below is resolved against it
	baseRef string // the ref the append-only history check takes a merge base with
}

func (t tree) dirPath() string  { return filepath.Join(t.root, filepath.FromSlash(migrationDir)) }
func (t tree) lockPath() string { return filepath.Join(t.root, filepath.FromSlash(lockFile)) }

func (t tree) migrationPath(name string) string {
	return filepath.Join(t.root, filepath.FromSlash(migrationDir), name)
}

// output is where an invocation writes.
//
// Both streams live behind one type so that a test can read exactly what a mode printed without a
// subprocess, and so that the one place a write error is discarded is here rather than at forty call
// sites. Discarding it is the right call and the reason is narrow: if stdout or stderr has gone away
// there is nothing this command could do about it, and the verdict is carried by the exit code —
// which every caller, MIG003 included, is what actually reads.
//
// say goes to stdout: findings, notes and the count. fail goes to stderr: the explanation that
// follows a non-zero exit. The split matches what the shell implementation did, and MIG003 captures
// both merged.
type output struct {
	stdout io.Writer
	stderr io.Writer
}

func (o output) say(format string, a ...any)  { _, _ = fmt.Fprintf(o.stdout, format, a...) }
func (o output) fail(format string, a ...any) { _, _ = fmt.Fprintf(o.stderr, format, a...) }

// ErrDisagrees is returned when the manifest disagrees with the migrations, or with its own
// history. It is the finding, as opposed to a failure to look.
//
// The distinction is the reason this is a library: the gate engine reports "a migration listed in
// SHIPPED.lock was modified or deleted" for this error and "the manifest could not be checked" for
// any other, and a caller that could only see a non-zero exit code had to say the first about both.
var ErrDisagrees = errors.New("the manifest disagrees with the migrations, or with its own history")

// DefaultBaseRef is the ref the append-only history check compares against. See historyCheck for why
// it is a merge base against this ref and not the ref itself.
const DefaultBaseRef = defaultBaseRef

// Verify checks the manifest at root and writes what it found to out.
//
// complete adds the release-path assertion: every migration in the tree is listed. It is deliberately
// NOT part of the per-PR gate — a migration added on a feature branch has not shipped yet and must
// not be listed, so requiring it there would fire on the one change the rule exists to permit.
//
// nil means clean. [ErrDisagrees] means the manifest and the tree, or the manifest and its own
// history, do not agree. ANY OTHER ERROR MEANS THE CHECK COULD NOT RUN, which callers must treat as
// a failure and never as a pass: a hash gate that cannot hash must not report green.
func Verify(root, baseRef string, complete bool, out io.Writer) error {
	return verify(tree{root: root, baseRef: orDefault(baseRef)}, complete,
		output{stdout: out, stderr: out})
}

// Run dispatches one command-line invocation and returns its exit code. It is the whole of the
// shippedlock command, kept here so that every mode is exercised by this package's tests rather than
// only through a subprocess.
func Run(args []string, root, baseRef string, stdout, stderr io.Writer) int {
	return run(args, tree{root: root, baseRef: orDefault(baseRef)}, output{stdout: stdout, stderr: stderr})
}

func orDefault(baseRef string) string {
	if baseRef == "" {
		return defaultBaseRef
	}

	return baseRef
}

// run dispatches one invocation and returns its exit code.
//
// Everything below is driven through here rather than through main so a test can run a whole mode
// against a fabricated tree and read both streams, which is what makes the seal and init semantics
// assertable without a subprocess.
func run(args []string, t tree, o output) int {
	mode := "verify"
	if len(args) > 0 {
		mode = args[0]
	}

	switch mode {
	case "verify":
		complete := false

		switch {
		case len(args) <= 1:
		case len(args) == 2 && args[1] == "--complete":
			complete = true
		default:
			return usage(o)
		}

		return exitOf(verify(t, complete, o), o)

	case "seal":
		if len(args) > 1 {
			return usage(o)
		}

		return seal(t, o)

	case "init":
		if len(args) > 1 {
			return usage(o)
		}

		return initManifest(t, o)

	default:
		return usage(o)
	}
}

func usage(o output) int {
	o.fail("usage: shippedlock [verify [--complete] | seal | init]\n")

	return exitUsage
}

// verify is the per-PR gate, and with complete set the release gate.
//
// It returns nil when clean, ErrDisagrees when the manifest and the tree disagree, and any other
// error when the check could not run. The three used to be two — an exit code of 0 or 1 — which is
// what made a gate engine calling this say "a migration was modified" about a tree it had merely
// failed to read (issue #173).
func verify(t tree, complete bool, o output) error {
	data, err := os.ReadFile(t.lockPath())

	switch {
	case errors.Is(err, fs.ErrNotExist) && complete:
		// A release cannot assert what shipped against a manifest that is not there, so the
		// release path fails where the per-PR path is content.
		return fmt.Errorf("%s does not exist. A release cannot record which migrations\n"+
			"shipped without it. Create it with: go run ./internal/migrate/shippedlock init", lockFile)

	case errors.Is(err, fs.ErrNotExist):
		o.say("  no %s — nothing has shipped yet\n", lockFile)

		return nil

	case err != nil:
		// A manifest that cannot be read is a hard failure, never a pass: the whole file exists to
		// be checked, and "could not check it" must not print green.
		return fmt.Errorf("read %s: %w", lockFile, err)
	}

	rows, problems, err := inspect(t, data, complete, o)
	if err != nil {
		return err
	}

	if len(problems) > 0 {
		o.fail("\nshipped-lock: %v.\n", ErrDisagrees)
		o.fail("A migration that has shipped is frozen: it has already run on a user's database, and\n")
		o.fail("editing it makes their schema and a fresh install silently diverge. To change what a\n")
		o.fail("shipped migration created, write a NEW migration: make migration NAME=<snake_case>\n")

		return ErrDisagrees
	}

	reportClean(o, len(rows))

	return nil
}

// exitOf is the command's translation of verify's outcome. ErrDisagrees has already explained itself
// through o; anything else is a failure to look, and says so with the "shipped-lock:" prefix every
// other hard failure in this file carries.
func exitOf(err error, o output) int {
	switch {
	case err == nil:
		return exitOK
	case errors.Is(err, ErrDisagrees):
		return exitFail
	default:
		o.fail("shipped-lock: %v\n", err)

		return exitFail
	}
}

// inspect runs every check against an already-read manifest, prints what each one found, and
// returns the parsed rows with the problems. A non-nil error means a check COULD NOT RUN at all — an
// unreadable migration, a merge-base blob git refuses to hand over — which is a hard failure and
// never a silent pass.
//
// Both verify and seal go through here, and that is deliberate: sealing on top of a tree where a
// listed migration was already altered — or where the manifest itself was rewritten — would launder
// the alteration into the record, which is the one thing this file must never do. A second, laxer
// set of checks on the seal path is exactly how that would happen.
func inspect(t tree, data []byte, complete bool, o output) (rows []row, problems []string, err error) {
	rows, problems = parseManifest(data)

	rowProblems, err := checkRows(t, rows)
	if err != nil {
		return nil, nil, err
	}

	problems = append(problems, rowProblems...)

	notes, historyProblems, err := historyCheck(t, data)
	if err != nil {
		return nil, nil, err
	}

	printLines(o, problems)
	printLines(o, notes)
	printLines(o, historyProblems)

	problems = append(problems, historyProblems...)

	if complete {
		completeProblems, err := checkComplete(t, rows)
		if err != nil {
			return nil, nil, err
		}

		printLines(o, completeProblems)

		problems = append(problems, completeProblems...)
	}

	return rows, problems, nil
}

// reportClean prints the count a passing run ends with.
//
// The count is load-bearing, not decoration: a truncated manifest that parsed to zero rows would
// otherwise report a cheerful green line having checked nothing. test/repo's
// TestRepoGates_UnchangedShippedMigration_PassesGate asserts the number is printed, for that reason.
func reportClean(o output, n int) {
	if n == 0 {
		o.say("  %s lists no migrations yet — nothing has shipped\n", lockFile)

		return
	}

	o.say("  %d shipped migration(s) unchanged\n", n)
}

// seal appends a row for every migration not yet listed, and refuses to do anything at all on a
// tree that does not already verify.
func seal(t tree, o output) int {
	if !isDir(t.dirPath()) {
		o.fail("shipped-lock: %s does not exist\n", migrationDir)

		return exitFail
	}

	if _, err := os.Stat(t.lockPath()); errors.Is(err, fs.ErrNotExist) {
		if err := os.WriteFile(t.lockPath(), []byte(manifestHeader), 0o644); err != nil {
			o.fail("shipped-lock: create %s: %v\n", lockFile, err)

			return exitFail
		}
	} else if err != nil {
		o.fail("shipped-lock: stat %s: %v\n", lockFile, err)

		return exitFail
	}

	data, err := os.ReadFile(t.lockPath())
	if err != nil {
		o.fail("shipped-lock: read %s: %v\n", lockFile, err)

		return exitFail
	}

	// Verify BEFORE appending, with the same checks the gate makes. See inspect.
	rows, ok := parsedIfClean(t, data, o)
	if !ok {
		return exitFail
	}

	added, err := appendUnlisted(t, rows, o)
	if err != nil {
		o.fail("shipped-lock: %v\n", err)

		return exitFail
	}

	if added == 0 {
		o.say("  nothing to seal — every migration in %s is already listed\n", migrationDir)
	} else {
		o.say("  %d migration(s) appended to %s — commit this in the Release PR\n", added, lockFile)
	}

	return exitOK
}

// parsedIfClean runs the verification half of a seal and returns the parsed rows only when the tree
// is clean. A seal on top of a tampered tree would recompute the hash of the tampered file and make
// the tampering permanent, so this refusal is the property that makes the whole manifest evidence
// rather than an echo of whatever is on disk.
//
// Completeness is NOT asserted here: seal is the thing that makes the manifest complete, so
// requiring completeness first would mean it could never run.
func parsedIfClean(t tree, data []byte, o output) (rows []row, ok bool) {
	rows, problems, err := inspect(t, data, false, o)
	if err != nil {
		o.fail("shipped-lock: %v\n", err)

		return nil, false
	}

	if len(problems) > 0 {
		o.fail("\nshipped-lock: refusing to seal — an already-shipped migration does not match its\n")
		o.fail("recorded hash. Fix that first; a seal on top of it would make the tampering permanent.\n")

		return nil, false
	}

	return rows, true
}

// appendUnlisted appends one row per migration that is not already listed, in filename order.
//
// O_APPEND, so a bug here cannot rewrite a byte that is already recorded: the file is opened with
// no way to seek back over an existing row.
func appendUnlisted(t tree, rows []row, o output) (added int, err error) {
	files, err := migrationFiles(t)
	if err != nil {
		return 0, err
	}

	listed := make(map[string]bool, len(rows))
	for _, r := range rows {
		listed[r.name] = true
	}

	f, err := os.OpenFile(t.lockPath(), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, fmt.Errorf("open %s for append: %w", lockFile, err)
	}

	added, err = writeRows(t, f, files, listed, o)

	// The Close error matters here in a way it does not on a read: it is where a short write on a
	// full disk surfaces, and a half-written row is a manifest nobody can verify.
	if closeErr := f.Close(); err == nil && closeErr != nil {
		err = fmt.Errorf("close %s: %w", lockFile, closeErr)
	}

	return added, err
}

// writeRows hashes and writes each unlisted migration, naming every one it seals. A failure part way
// through leaves the rows already written in place: they are correct, and rewriting the file to undo
// them is the one operation this command does not have.
func writeRows(t tree, w io.Writer, files []string, listed map[string]bool, o output) (added int, err error) {
	for _, base := range files {
		if listed[base] {
			continue
		}

		sum, err := sha256File(t.migrationPath(base))
		if err != nil {
			return added, err
		}

		if _, err := fmt.Fprintf(w, "%s %s\n", base, sum); err != nil {
			return added, fmt.Errorf("append %s to %s: %w", base, lockFile, err)
		}

		o.say("  sealed %s\n", base)

		added++
	}

	return added, nil
}

// initManifest bootstraps the manifest, and refuses if one is already there.
//
// The existence check comes first, before the directory check, so the "never overwrites" promise
// holds even in a tree where something else is wrong.
func initManifest(t tree, o output) int {
	switch _, err := os.Stat(t.lockPath()); {
	case err == nil:
		o.fail("shipped-lock: %s already exists; init never overwrites a manifest.\n", lockFile)

		return exitFail

	case !errors.Is(err, fs.ErrNotExist):
		o.fail("shipped-lock: stat %s: %v\n", lockFile, err)

		return exitFail
	}

	if !isDir(t.dirPath()) {
		o.fail("shipped-lock: %s does not exist\n", migrationDir)

		return exitFail
	}

	if err := os.WriteFile(t.lockPath(), []byte(manifestHeader), 0o644); err != nil {
		o.fail("shipped-lock: write %s: %v\n", lockFile, err)

		return exitFail
	}

	o.say("  wrote %s with no rows — nothing has shipped yet\n", lockFile)

	return exitOK
}

// printLines writes each line with the two-space indent every message from this command carries;
// MIG003 nests the whole block under its own heading in scripts/repo-gates.sh.
func printLines(o output, lines []string) {
	for _, l := range lines {
		o.say("  %s\n", l)
	}
}

func isDir(path string) bool {
	info, err := os.Stat(path)

	return err == nil && info.IsDir()
}
