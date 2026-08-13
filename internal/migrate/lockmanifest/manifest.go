package lockmanifest

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
)

// row is one `<filename> <sha256>` line of the manifest.
type row struct {
	line int    // 1-based, so a failure points at a line rather than at a file
	name string // migration basename, as it appears in db/migrations-sqlite
	hash string // 64 lowercase hex characters
}

// parseManifest parses SHIPPED.lock and returns its rows plus one problem per row it rejected.
//
// A malformed row is a FAILURE, not a skipped line. A truncated or half-written manifest that parsed
// to zero rows would otherwise report "0 shipped migrations unchanged" and pass, which is precisely
// the vacuous green this file exists to prevent. Every rejection below therefore produces a problem
// AND withholds the row, so a rejected line can never be mistaken for a checked one.
func parseManifest(data []byte) (rows []row, problems []string) {
	seen := make(map[string]bool)

	for i, line := range strings.Split(string(data), "\n") {
		lineno := i + 1
		line = strings.TrimSuffix(line, "\r")

		// Blank and comment lines are ignored, as the header promises. Anything else is a row, and
		// rows start in column 1 — a leading space is a sign of a hand-edit, not of a format, so an
		// indented line is parsed as a row and fails as one.
		if line == "" || strings.HasPrefix(line, "#") || strings.TrimSpace(line) == "" {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) != 2 {
			problems = append(problems,
				fmt.Sprintf("%s:%d: a row is exactly '<filename> <sha256>': %s", lockFile, lineno, line))

			continue
		}

		name, hash := fields[0], fields[1]

		switch {
		case !isPlainBasename(name):
			problems = append(problems,
				fmt.Sprintf("%s:%d: the filename is a plain migration basename: %s", lockFile, lineno, name))

			continue

		case !strings.HasSuffix(name, ".sql"):
			problems = append(problems,
				fmt.Sprintf("%s:%d: the filename must be a .sql migration: %s", lockFile, lineno, name))

			continue
		}

		if !isLowerHex64(hash) {
			problems = append(problems,
				fmt.Sprintf("%s:%d: the hash must be 64 lowercase hex characters: %s", lockFile, lineno, hash))

			continue
		}

		if seen[name] {
			problems = append(problems,
				fmt.Sprintf("%s:%d: %s is listed twice; the manifest is append-only, not editable", lockFile, lineno, name))

			continue
		}

		seen[name] = true
		rows = append(rows, row{line: lineno, name: name, hash: hash})
	}

	return rows, problems
}

// isPlainBasename reports whether name is a bare migration filename.
//
// A path is rejected rather than resolved. Rows are basenames — that is the form
// .claude/hooks/guard-protected-paths.sh matches against for both dialect directories — and a row
// that could name `../../somewhere/else.sql` would let the manifest be pointed away from the
// migrations it is supposed to be freezing.
func isPlainBasename(name string) bool {
	if name == "" {
		return false
	}

	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '.', r == '-':
		default:
			return false
		}
	}

	return true
}

// isLowerHex64 reports whether hash is exactly 64 lowercase hex characters.
//
// Uppercase is rejected rather than folded: the hash a row carries has to be comparable to the one
// this command computes with a plain string comparison, and a manifest holding two spellings of the
// same digest is a manifest somebody has been editing by hand.
func isLowerHex64(hash string) bool {
	if len(hash) != 64 {
		return false
	}

	for _, r := range hash {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f':
		default:
			return false
		}
	}

	return true
}

// checkRows verifies every listed migration still exists and still hashes to its recorded value.
//
// An error — as opposed to a problem — is returned when a file is there but cannot be hashed. That
// is a hard failure, never a skip: a hash gate that cannot hash must not report green.
func checkRows(t tree, rows []row) (problems []string, err error) {
	for _, r := range rows {
		path := t.migrationPath(r.name)

		info, statErr := os.Stat(path)
		if statErr != nil || !info.Mode().IsRegular() {
			if statErr != nil && !errors.Is(statErr, fs.ErrNotExist) {
				return nil, fmt.Errorf("stat %s/%s: %w", migrationDir, r.name, statErr)
			}

			problems = append(problems,
				fmt.Sprintf("%s — DELETED. It is listed in %s, so it has already run on a user's database.",
					r.name, lockFile))

			continue
		}

		got, err := sha256File(path)
		if err != nil {
			return nil, err
		}

		if got != r.hash {
			problems = append(problems,
				fmt.Sprintf("%s — MODIFIED. expected %s, found %s", r.name, r.hash, got))
		}
	}

	return problems, nil
}

// checkComplete asserts every migration in the tree is listed. This is the release-path assertion
// only (`verify --complete`).
//
// Every migration present at a tag ships with that tag, so every one of them must already be listed.
// This is the check that turns "somebody forgot to seal the manifest" into a failed release instead
// of a silent hole in the record. It is deliberately NOT part of the per-PR gate: a migration added
// on a feature branch has not shipped yet and must not be listed, so requiring it there would fire
// on the one change the rule exists to permit.
func checkComplete(t tree, rows []row) (problems []string, err error) {
	files, err := migrationFiles(t)
	if err != nil {
		return nil, err
	}

	listed := make(map[string]bool, len(rows))
	for _, r := range rows {
		listed[r.name] = true
	}

	for _, base := range files {
		if listed[base] {
			continue
		}

		problems = append(problems,
			fmt.Sprintf("%s — present in %s but NOT listed in %s; seal it with: make shipped-lock-seal",
				base, migrationDir, lockFile))
	}

	return problems, nil
}

// migrationFiles returns the basenames of every .sql migration in the tree, in filename order.
//
// A missing directory is not an error: repo-gates.sh drives this command against fabricated trees,
// and the shell implementation's glob produced nothing there rather than failing. Dot-files are
// skipped for the same reason — a glob never matched them, and an editor swap file is not a
// migration.
func migrationFiles(t tree) (names []string, err error) {
	entries, err := os.ReadDir(t.dirPath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}

	if err != nil {
		return nil, fmt.Errorf("read %s: %w", migrationDir, err)
	}

	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".sql") {
			continue
		}

		names = append(names, name)
	}

	return names, nil
}

// sha256File returns a file's digest as lowercase hex — the form SHIPPED.lock records.
func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("hash %s: %w", path, err)
	}

	// A read-only handle: there is nothing to do about a failure to close one, and the digest below
	// is already complete.
	defer func() { _ = f.Close() }()

	sum := sha256.New()
	if _, err := io.Copy(sum, f); err != nil {
		return "", fmt.Errorf("hash %s: %w", path, err)
	}

	return hex.EncodeToString(sum.Sum(nil)), nil
}
