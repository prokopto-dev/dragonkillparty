package repo_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The rebuild fixtures under test/fixtures/migrations/rebuild/ are MATCHED PAIRS: a correct
// migration and a deliberately broken twin that differs from it in exactly one declared way.
//
// Each pair is a controlled experiment, and its value comes entirely from the control being the same
// migration. The positive test asserts "the triggers survive a rebuild"; the negative control proves
// that assertion can fail by applying the same rebuild without the trigger re-creation. If somebody
// edits one file and not the other — a new column, a changed index, a different CHECK — the two stop
// being the same rebuild. The failure is silent in the direction that matters: the broken twin keeps
// failing the way it always did, because DROP TABLE keeps doing what DROP TABLE does, so nothing goes
// red while the positive test quietly stops being controlled for.
//
// Until now the only thing holding the pairs together was a sentence in each file's header, and a
// two-file invariant stated in prose is exactly what a reviewer reads past. This makes it executable.
//
// The comparison is over STATEMENTS, not bytes. Byte equality is what the headers used to claim and
// it was never true — the two files carry different explanations of themselves, and they should:
// prose is where a fixture says what it is for. So comments are normalised away and goose's
// annotations are kept, since an annotation is a directive and one of the pairs differs by exactly
// one of them.

// deliberatelyBrokenMarker is the machine-readable "this file is broken on purpose" header.
//
// A marker rather than prose because prose is what an agent trying to make CI green reads past, and
// "never repair this file" is the one instruction in these files that a well-meaning fix would
// violate. Greppable, so `git grep` finds every deliberately broken fixture in the repository, and
// asserted in both directions below so it cannot drift onto a correct fixture either.
const deliberatelyBrokenMarker = "-- dkp:fixture deliberately-broken"

// rebuildPairs is the register of matched pairs and the ONE difference each is allowed.
//
// permitted holds statements (or goose annotations) that must appear in the reference and must not
// appear in the twin. Everything else has to match exactly, in order. Adding a pair here is how a
// future fixture joins the scheme; widening permitted for an existing one is how the scheme is
// defeated, so a change to these three lines is the review signal.
var rebuildPairs = []struct {
	name string
	// reference is the CORRECT migration, repo-root-relative.
	reference string
	// broken is the deliberately broken twin.
	broken string
	// permitted is what the reference has and the twin does not. Nothing else may differ.
	permitted []string
	// why explains the pair to whoever reads the failure.
	why string
}{
	{
		name:      "ledger_entry rebuild: with and without the trigger re-creation",
		reference: "test/fixtures/migrations/rebuild/000002_ledger_entry_rebuild.sql",
		broken:    "test/fixtures/migrations/rebuild/000002_ledger_entry_rebuild_no_triggers.sql",
		permitted: []string{
			"CREATE TRIGGER trg_ledger_entry_no_update BEFORE UPDATE ON ledger_entry " +
				"BEGIN SELECT RAISE(ABORT, 'ledger_entry is append-only'); END",
			"CREATE TRIGGER trg_ledger_entry_no_delete BEFORE DELETE ON ledger_entry " +
				"BEGIN SELECT RAISE(ABORT, 'ledger_entry is append-only'); END",
		},
		why: "the twin is the negative control for TestMigrate_FullStack_LedgerDataSurvivesUpgrade: " +
			"the same rebuild with the append-only triggers not re-created, which is the line a real " +
			"migration omits because Atlas cannot express a trigger",
	},
	{
		name:      "ledger_batch rebuild: with and without goose's transaction",
		reference: "test/fixtures/migrations/rebuild/000002_ledger_batch_rebuild.sql",
		broken:    "test/fixtures/migrations/rebuild/000002_ledger_batch_rebuild_in_transaction.sql",
		permitted: []string{"+goose NO TRANSACTION"},
		why: "the twin is Atlas's generated form of the same parent-table rebuild, which fails on " +
			"every populated database because SQLite ignores PRAGMA foreign_keys inside the " +
			"transaction goose wraps a migration in",
	},
}

// migrationElements reduces a migration to the sequence that has to match: its goose annotations and
// its SQL statements, whitespace-normalised, comments removed.
//
// Annotations are KEPT because they are instructions to goose and one pair's whole difference is an
// annotation. Comments are dropped because they are the part a fixture is supposed to differ in.
// BEGIN … END blocks are held together, since a trigger body contains semicolons and splitting one
// in half would produce two nonsense elements that happened to compare equal.
func migrationElements(t *testing.T, rel string) []string {
	t.Helper()

	body, err := os.ReadFile(filepath.Join(repoRoot(t), filepath.FromSlash(rel)))
	require.NoErrorf(t, err, "%s must exist", rel)

	var (
		elements []string
		current  strings.Builder
	)

	flush := func() {
		if current.Len() == 0 {
			return
		}

		elements = append(elements, strings.TrimSuffix(current.String(), ";"))
		current.Reset()
	}

	for _, line := range strings.Split(string(body), "\n") {
		trimmed := strings.Join(strings.Fields(line), " ")

		switch {
		case trimmed == "":
			continue
		case strings.HasPrefix(trimmed, "-- +goose"):
			// An annotation always terminates whatever came before it: goose reads it as a boundary,
			// so a file that let one land mid-statement would not mean what it looks like.
			flush()
			elements = append(elements, strings.TrimSpace(strings.TrimPrefix(trimmed, "--")))

			continue
		case strings.HasPrefix(trimmed, "--"):
			continue
		}

		if current.Len() > 0 {
			current.WriteString(" ")
		}

		current.WriteString(trimmed)

		if !strings.HasSuffix(trimmed, ";") {
			continue
		}

		if stmt := current.String(); strings.Contains(strings.ToUpper(stmt), " BEGIN ") &&
			!strings.HasSuffix(strings.ToUpper(stmt), "END;") {
			continue
		}

		flush()
	}

	require.Zero(t, current.Len(), "%s ends mid-statement: %q", rel, current.String())
	require.NotEmpty(t, elements, "%s parsed to no statements at all — is it empty?", rel)

	return elements
}

// pairDrift is every way a pair can stop being a pair. Empty means the two files differ in exactly
// the declared way and in no other.
type pairDrift struct {
	// notDeclared are permitted differences the reference does not actually contain — the
	// declaration describes a migration that no longer exists.
	notDeclared []string
	// repaired are permitted differences that have appeared in the BROKEN twin. Somebody fixed the
	// file whose brokenness is the experiment.
	repaired []string
	// remainder is the reference with the permitted differences removed. It must equal the twin.
	remainder []string
}

// comparePair is the gate's arithmetic, kept as a pure function so it can be driven with synthetic
// input.
//
// That is not tidiness. A gate whose failure branch has never executed is a gate nobody has any
// reason to believe in, and this one guards fixtures that are themselves the negative controls for
// something else — the whole chain rests on each link having been watched to fail.
func comparePair(reference, broken, permitted []string) pairDrift {
	drift := pairDrift{remainder: append([]string{}, reference...)}

	for _, want := range permitted {
		if slices.Contains(broken, want) {
			drift.repaired = append(drift.repaired, want)
		}

		i := slices.Index(drift.remainder, want)
		if i < 0 {
			drift.notDeclared = append(drift.notDeclared, want)

			continue
		}

		drift.remainder = append(drift.remainder[:i], drift.remainder[i+1:]...)
	}

	return drift
}

// matches reports whether the pair is intact: no repaired twin, no stale declaration, and the
// remainder identical to the twin.
func (d pairDrift) matches(broken []string) bool {
	return len(d.notDeclared) == 0 && len(d.repaired) == 0 && slices.Equal(d.remainder, broken)
}

// TestFixtures_RebuildPairs_DifferOnlyByTheDeclaredStatements is the gate.
//
// Strip the declared difference from the correct fixture and what is left must be the broken twin,
// statement for statement, in order. Anything else — a column added to one, an index changed in one,
// a CHECK tightened in one — is the pair coming apart, and it fails here rather than three months
// later when somebody wonders why the negative control never catches anything.
func TestFixtures_RebuildPairs_DifferOnlyByTheDeclaredStatements(t *testing.T) {
	t.Parallel()

	for _, pair := range rebuildPairs {
		t.Run(pair.name, func(t *testing.T) {
			t.Parallel()

			require.NotEmpty(t, pair.permitted,
				"a pair with no declared difference is not a pair; it is two copies of one file")

			reference := migrationElements(t, pair.reference)
			broken := migrationElements(t, pair.broken)
			drift := comparePair(reference, broken, pair.permitted)

			require.Empty(t, drift.repaired,
				"%s contains %v, which the pairing declares as the difference between it and %s. The "+
					"twin is deliberately broken (%s) and repairing it disarms the control that makes "+
					"the positive test mean anything.",
				pair.broken, drift.repaired, pair.reference, pair.why)

			require.Empty(t, drift.notDeclared,
				"%s does not contain %v, which rebuildPairs declares as the permitted difference. "+
					"Either the fixture was edited and the declaration was not, or the declaration "+
					"describes a migration that no longer exists.",
				pair.reference, drift.notDeclared)

			require.Equal(t, broken, drift.remainder,
				"%s and %s have drifted apart. They are a MATCHED PAIR: the only permitted difference "+
					"is %v, and %s. A change to one is a change to both, in the same commit — otherwise "+
					"the negative control stops controlling for the same migration and the positive "+
					"test's assertions are no longer about anything.",
				pair.reference, pair.broken, pair.permitted, pair.why)

			require.True(t, drift.matches(broken), "sanity: the drift report and the assertions above disagree")
		})
	}
}

// TestFixtures_RebuildPairs_DriftIsDetected is this gate's own negative control.
//
// Every failure branch above, driven with synthetic input, because a gate that has only ever been
// seen to pass is indistinguishable from a gate that cannot fail — and the three cases differ:
// a fixture edited on one side only, a broken twin somebody "fixed", and a declaration left behind
// by an edit to the fixture it describes.
func TestFixtures_RebuildPairs_DriftIsDetected(t *testing.T) {
	t.Parallel()

	const (
		up      = "+goose Up"
		create  = "CREATE TABLE new_thing (id text NOT NULL PRIMARY KEY) STRICT"
		trigger = "CREATE TRIGGER trg_thing BEGIN SELECT RAISE(ABORT, 'no'); END"
		index   = "CREATE INDEX ix_thing ON thing (id)"
	)

	tests := []struct {
		name      string
		reference []string
		broken    []string
		permitted []string
	}{
		{
			name:      "a statement added to the reference and not to the twin",
			reference: []string{up, create, index, trigger},
			broken:    []string{up, create},
			permitted: []string{trigger},
		},
		{
			name:      "a statement changed on one side only",
			reference: []string{up, create, trigger},
			broken:    []string{up, create + " WITHOUT ROWID"},
			permitted: []string{trigger},
		},
		{
			name:      "the broken twin was repaired",
			reference: []string{up, create, trigger},
			broken:    []string{up, create, trigger},
			permitted: []string{trigger},
		},
		{
			name:      "the declared difference is no longer in the reference",
			reference: []string{up, create},
			broken:    []string{up, create},
			permitted: []string{trigger},
		},
		{
			name:      "statements reordered, which SQL cares about even when the set matches",
			reference: []string{up, create, index, trigger},
			broken:    []string{up, index, create},
			permitted: []string{trigger},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			require.False(t, comparePair(tc.reference, tc.broken, tc.permitted).matches(tc.broken),
				"the pairing gate accepted a drifted pair, so it is not holding the real fixtures to "+
					"anything either")
		})
	}

	// The control for the controls: an intact pair must still be accepted, or every case above passes
	// for the trivial reason that nothing is ever accepted.
	intactReference := []string{up, create, index, trigger}
	intactBroken := []string{up, create, index}
	require.True(t, comparePair(intactReference, intactBroken, []string{trigger}).matches(intactBroken),
		"the gate rejected a correctly matched pair")
}

// TestFixtures_DeliberatelyBrokenMigrations_CarryTheMarker makes "do not repair this file" a thing
// a machine can read.
//
// Both directions are asserted. A broken twin without the marker is a file that looks like a bug;
// a correct fixture WITH the marker is worse, because it tells the next reader not to fix something
// that genuinely is broken.
func TestFixtures_DeliberatelyBrokenMigrations_CarryTheMarker(t *testing.T) {
	t.Parallel()

	for _, pair := range rebuildPairs {
		t.Run(pair.name, func(t *testing.T) {
			t.Parallel()

			require.Contains(t, readRepoFile(t, pair.broken), deliberatelyBrokenMarker,
				"%s is a deliberately broken fixture and must say so in a form a grep can find. "+
					"Add `%s` to its header: prose saying the same thing is what an agent trying to "+
					"make CI green reads past.",
				pair.broken, deliberatelyBrokenMarker)

			require.NotContains(t, readRepoFile(t, pair.reference), deliberatelyBrokenMarker,
				"%s is the CORRECT half of the pair and carries the deliberately-broken marker. That "+
					"tells the next reader not to fix a file that is supposed to work.",
				pair.reference)
		})
	}
}

// TestMigrations_ShippedSet_CarriesNoDeliberatelyBrokenFixture is the one that matters if the marker
// scheme ever earns its keep.
//
// Every broken fixture's header says "test fixture only; it must never be moved into
// db/migrations-sqlite/". This is that sentence with teeth. Copying a fixture into the real migration
// set is a plausible mistake — the files look exactly like migrations, because they are — and the
// consequence is a migration that either fails on every populated database or silently removes the
// append-only guarantee from every one of them.
func TestMigrations_ShippedSet_CarriesNoDeliberatelyBrokenFixture(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(repoRoot(t), "db", "migrations-sqlite")

	entries, err := os.ReadDir(dir)
	require.NoError(t, err, "read db/migrations-sqlite")
	require.NotEmpty(t, entries, "db/migrations-sqlite is empty")

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}

		body, readErr := os.ReadFile(filepath.Join(dir, entry.Name()))
		require.NoError(t, readErr, "read %s", entry.Name())

		require.NotContains(t, string(body), deliberatelyBrokenMarker,
			"db/migrations-sqlite/%s is marked as a deliberately broken TEST FIXTURE. It has been "+
				"copied out of test/fixtures/migrations/ into the migration set every officer's "+
				"database will apply.", entry.Name())
	}
}
