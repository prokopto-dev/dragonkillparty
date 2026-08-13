package repogate

import (
	"regexp"
	"strings"

	"github.com/prokopto-dev/dragonkillparty/internal/migrate/sqlscan"
)

// MIG001 and MIG002 — the two rules about the TEXT of a migration.
//
// Both were flat `grep -rnE` patterns over the whole file, and both were wrong in the same way: the
// question each asks is about a REGION of the file, and a pattern cannot see one.
//
// # MIG001 — DDL inside a goose Down block (issue #137)
//
// The rule is "migrations are forward-only", and its subject is the DOWN block. As a pattern it
// matched `^\s*(DROP|ALTER)` anywhere, including the Up block — where SQLite's 12-step table rebuild
// puts exactly those two statements:
//
//	DROP TABLE "ledger_entry";
//	ALTER TABLE "new_ledger_entry" RENAME TO "ledger_entry";
//
// SQLite cannot drop or retype a column any other way, and .claude/rules/migrations.md says in as
// many words to let Atlas generate that rebuild. So the first migration to take the documented,
// recommended path would have failed `lint / repo` with a message saying its DDL was in a Down block
// when it was in the Up block — and the two readings available to its author are "the gate is
// broken" and "I must hand-write the rebuild". The second is how a rebuild silently loses the
// ledger's append-only triggers.
//
// It never fired because no migration in the tree rebuilds a table yet. The one that does —
// test/fixtures/migrations/rebuild/000002_ledger_batch_rebuild.sql, applied to a populated ledger on
// every CI run — lives under test/fixtures, which this rule does not scan. The fixture that proves
// the pattern works is also the reason nobody had watched the gate misfire on it.
//
// # MIG002 — a backtick-quoted identifier (issue #138)
//
// Atlas emits `dkp_meta`: MySQL-style backtick quoting, which SQLite accepts as a compatibility
// extension. sqlc's SQLite parser does not, and it fails SILENTLY — it parses no table out of the
// schema, generates an empty package, and reports `relation "x" does not exist` against the query
// file, which is the one file that was correct. scripts/new-migration.sh rewrites them at generation
// time; this rule is the backstop for a migration that arrived some other way.
//
// As a pattern it failed on ANY backtick, and that made the generator's own refusal message untrue.
// `make migration` refuses when a backtick is inside a STRING LITERAL — where it is data, and where
// rewriting it would change the value the schema asked for — and offers two ways out: change
// db/schema.hcl, or rewrite the identifiers by hand. The second one produced a file that is valid
// SQL, applies in goose, hashes cleanly, and could never pass `lint / repo`, because the literal's
// backtick is still there and must be. Half the advice led into a dead end, discovered after the
// work.
//
// So the rule now asks internal/migrate/sqlscan the same question the generator asks it: is this
// backtick data, or is it an identifier quote? One scanner, two callers — a gate and a generator
// that disagree about what a backtick is are not two implementations anybody can keep in step.
const migrationTree = "db/migrations-sqlite"

// migrationDDL is the pattern MIG001 has always used. It is unchanged; what changed is the lines it
// is applied to.
var migrationDDL = regexp.MustCompile(`^\s*(DROP|ALTER)\b`)

// runMigrationRules evaluates MIG001 and MIG002.
//
// One walk of the tree for both, because they read the same files and the scanner caches by path.
// The skips are printed per RULE rather than per walk: a rule that vanishes silently is
// indistinguishable in a CI log from a rule that ran, and these two used to be two catalogue entries
// printing two skips.
func runMigrationRules(s *scanner, rep *report) {
	if !s.hasTree(migrationTree) {
		rep.skip("MIG001", migrationTree)
		rep.skip("MIG002", migrationTree)

		return
	}

	var ddl, ticks []string

	for _, path := range s.paths(migrationTree, []string{"*.sql"}) {
		lines := s.lines(path)

		ddl = append(ddl, downBlockDDL(path, lines)...)

		// The scanner reads the file as one string because a string literal may span lines, which is
		// the entire reason it can answer a question a per-line pattern cannot.
		for _, tick := range sqlscan.BackticksOutsideStringLiterals(strings.Join(lines, "\n")) {
			ticks = append(ticks, hit(path, tick.Line, tick.Text))
		}
	}

	if len(ddl) > 0 {
		rep.violation("MIG001", "DDL inside a goose Down block (migrations are forward-only)", ddl)
	}

	if len(ticks) > 0 {
		rep.violation("MIG002", "backtick-quoted identifier in a migration (sqlc parses no table "+
			"and says so about the query)", ticks)
	}
}

// downBlockDDL returns every DDL statement AFTER the goose Down marker.
//
// A migration with no marker has no Down block, so it contributes nothing — the honest reading, and
// the one that keeps the rule's description true. A file whose entire content is an Up block is not
// a file with an empty Down block; it is a file the rule has nothing to say about.
//
// Comment lines are stripped in the two spellings the catalogue's general rules strip, for parity
// with what this rule did as a text rule. SQL's own `--` needs no stripping: a line that opens with
// it cannot match a pattern anchored on DROP or ALTER.
func downBlockDDL(path string, lines []string) []string {
	start := sqlscan.DownBlockStart(lines)
	if start == 0 {
		return nil
	}

	var hits []string

	for i := start; i < len(lines); i++ {
		line := lines[i]

		if isComment(line, hashAndSlash) || !migrationDDL.MatchString(line) {
			continue
		}

		hits = append(hits, hit(path, i+1, line))
	}

	return hits
}
