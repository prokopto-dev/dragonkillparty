package api

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// This file is the anti-rot gate for the two highest-leverage documents in the project:
// internal/api/EXAMPLE_ENDPOINT.md and db/RECIPES.md. Every later endpoint PR becomes "copy the
// example, change the nouns", so a worked example that has drifted from the code is worse than none —
// it teaches the wrong thing with authority. TestDocs_ExampleEndpointSnippets_Compile extracts every
// fenced code block from both files and proves it still builds against THIS tree:
//
//   - ```go   → a self-contained, package-declaring file that must `go build`
//   - ```sql  → a query that must pass `sqlc` against the committed migration set
//   - ```hcl  → a schema fragment that must pass `atlas schema inspect`
//
// A ```text (or ```json, ```bash) fence is documentation prose, deliberately NOT run: db/RECIPES.md
// marks its forward-looking recipes ```text precisely because they query tables no migration has
// created yet, and a query cannot be type-checked against a table that does not exist. The moment such
// a recipe's table ships, the fence flips to ```sql and this gate starts holding it to the code.
//
// The gate is slow (it shells out to go, sqlc and atlas), so it skips under -short — `make test-unit`
// does not pay it; `make test`, which CI runs as test-integration, does.

// docExamplePaths are the documents whose fences this gate compiles, relative to the repo root.
var docExamplePaths = []string{
	filepath.Join("internal", "api", "EXAMPLE_ENDPOINT.md"),
	filepath.Join("db", "RECIPES.md"),
}

// fenceOpen matches an opening code fence and captures its info string (the language). A closing
// fence has an empty info string, so the same pattern toggles the block on and off.
var fenceOpen = regexp.MustCompile("^```([a-zA-Z0-9]*)\\s*$")

// sqlNameDirective matches sqlc's `-- name: Foo :one` annotation and captures the query name, so the
// gate can suffix it uniquely across snippets without touching the SQL body.
var sqlNameDirective = regexp.MustCompile(`(?m)^(--\s*name:\s*)([A-Za-z0-9_]+)(\s+:\w+)`)

// snippet is one extracted fenced block: its language, its body, and where it came from so a failure
// names the document and the line the fence opened on.
type snippet struct {
	lang  string
	code  string
	doc   string
	line  int // 1-based line of the opening fence
	index int // ordinal within (doc, lang), for a stable package/dir name
}

// extractSnippets walks a markdown file and returns its fenced blocks. It is a line-oriented scanner
// rather than a full markdown parser on purpose: the only structure that matters here is the fence
// toggle, and a real parser would be a dependency this repo has not agreed to add.
func extractSnippets(t *testing.T, root, rel string) []snippet {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join(root, rel))
	require.NoErrorf(t, err, "read %s", rel)

	var (
		out     []snippet
		lines   = strings.Split(string(raw), "\n")
		inFence bool
		lang    string
		body    []string
		start   int
		counts  = map[string]int{}
	)

	for i, line := range lines {
		m := fenceOpen.FindStringSubmatch(line)
		if m == nil {
			if inFence {
				body = append(body, line)
			}

			continue
		}

		if !inFence {
			// Opening fence.
			inFence = true
			lang = strings.ToLower(m[1])
			body = body[:0]
			start = i + 1

			continue
		}

		// Closing fence.
		inFence = false
		out = append(out, snippet{
			lang:  lang,
			code:  strings.Join(body, "\n"),
			doc:   rel,
			line:  start,
			index: counts[lang],
		})
		counts[lang]++
	}

	require.Falsef(t, inFence, "%s: an unterminated code fence opened at line %d", rel, start)

	return out
}

// TestDocs_ExampleEndpointSnippets_Compile is the gate. It extracts every fence from both documents
// and dispatches each by language. Go, SQL and HCL fences must compile; everything else is prose.
func TestDocs_ExampleEndpointSnippets_Compile(t *testing.T) {
	if testing.Short() {
		t.Skip("snippet compile shells out to go/sqlc/atlas; skipped under -short (make test-unit)")
	}

	root := repoRoot(t)

	var all []snippet
	for _, rel := range docExamplePaths {
		all = append(all, extractSnippets(t, root, rel)...)
	}

	// A load-bearing floor: if the extractor silently matched nothing (a fence-syntax change, a moved
	// file) the whole gate would pass vacuously, which is the failure mode an anti-rot test exists to
	// prevent. Both documents carry several Go and SQL fences, so a real run is well above this.
	var goCount, sqlCount, hclCount int
	for _, s := range all {
		switch s.lang {
		case "go":
			goCount++
		case "sql":
			sqlCount++
		case "hcl":
			hclCount++
		}
	}

	require.GreaterOrEqualf(t, goCount, 4, "expected several ```go fences; the extractor found %d — "+
		"has the fence syntax or a document path changed?", goCount)
	require.GreaterOrEqualf(t, sqlCount, 2, "expected several ```sql fences; found %d", sqlCount)
	require.GreaterOrEqualf(t, hclCount, 1, "expected at least one ```hcl fence; found %d", hclCount)

	compileGoSnippets(t, root, all)
	compileSQLSnippets(t, root, all)
	compileHCLSnippets(t, root, all)
}

// compileGoSnippets writes each ```go fence into its own package directory INSIDE the module tree
// (so `internal/...` imports resolve) and runs one `go build ./...` over the lot. Inside the module,
// not t.TempDir(), because a package outside the module root cannot import the module's internal
// packages — which every real fence does.
func compileGoSnippets(t *testing.T, root string, snippets []snippet) {
	t.Helper()

	dir, err := os.MkdirTemp(root, ".snippet-go-")
	require.NoError(t, err, "make go snippet dir")
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	var wrote int
	for _, s := range snippets {
		if s.lang != "go" {
			continue
		}

		require.Containsf(t, s.code, "package ",
			"%s:%d: a ```go fence must be a self-contained file with a package clause, so the compile "+
				"gate can build it standalone", s.doc, s.line)

		pkgDir := filepath.Join(dir, sanitiseName(s.doc, s.index))
		require.NoError(t, os.MkdirAll(pkgDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(pkgDir, "snippet.go"), []byte(s.code+"\n"), 0o644))
		wrote++
	}

	require.Positive(t, wrote, "no Go snippets were written")

	// go vet is folded in via build; a bare `go build` is enough to catch a symbol that does not
	// exist, a wrong signature, or an import that no longer resolves — which is the whole point.
	cmd := exec.Command("go", "build", "./...") //nolint:gosec // fixed args
	cmd.Dir = dir

	out, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "a ```go fence failed to build — a doc snippet has drifted from the code:\n%s", out)
}

// compileSQLSnippets gathers every runnable ```sql fence into a temp queries directory, points a
// generated sqlc.yaml at the COMMITTED migration set, and runs `sqlc compile`. Compiling against the
// migrations (not schema.hcl, which sqlc cannot read) is the same choice sqlc.yaml makes: the types
// are checked against the DDL that actually runs on an officer's database.
func compileSQLSnippets(t *testing.T, root string, snippets []snippet) {
	t.Helper()

	sqlc, err := exec.LookPath("sqlc")
	if err != nil {
		t.Skip("sqlc not on PATH; run make setup (CI installs it via setup-toolchain)")
	}

	work, err := os.MkdirTemp("", "dkp-snippet-sql-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(work) })

	// sqlc reads the migration DDL from a schema directory of *.sql files. Copy the committed set in.
	schemaDir := filepath.Join(work, "schema")
	queriesDir := filepath.Join(work, "queries")
	require.NoError(t, os.MkdirAll(schemaDir, 0o755))
	require.NoError(t, os.MkdirAll(queriesDir, 0o755))

	migrations, err := filepath.Glob(filepath.Join(root, "db", "migrations-sqlite", "*.sql"))
	require.NoError(t, err)
	require.NotEmpty(t, migrations, "no committed migrations to type-check the recipes against")

	for _, m := range migrations {
		data, readErr := os.ReadFile(m)
		require.NoError(t, readErr)
		require.NoError(t, os.WriteFile(filepath.Join(schemaDir, filepath.Base(m)), data, 0o644))
	}

	var wrote int
	for _, s := range snippets {
		if s.lang != "sql" {
			continue
		}

		// The same query name (GetGuild, UpdateGuild) appears in both EXAMPLE_ENDPOINT.md and
		// RECIPES.md, and sqlc rejects a duplicate name across the query set. Suffix each -- name:
		// directive with the snippet's ordinal so every extracted query is uniquely named while its
		// SQL body — the thing under test — is type-checked unchanged.
		code := uniqueQueryNames(s.code, sanitiseName(s.doc, s.index))

		name := sanitiseName(s.doc, s.index) + ".sql"
		require.NoError(t, os.WriteFile(filepath.Join(queriesDir, name), []byte(code+"\n"), 0o644))
		wrote++
	}

	require.Positive(t, wrote, "no SQL snippets were written")

	const cfg = `version: "2"
sql:
  - engine: "sqlite"
    schema: "schema"
    queries: "queries"
    gen:
      go:
        package: "snippetgen"
        out: "gen"
`
	require.NoError(t, os.WriteFile(filepath.Join(work, "sqlc.yaml"), []byte(cfg), 0o644))

	cmd := exec.Command(sqlc, "compile") //nolint:gosec // resolved path, fixed args
	cmd.Dir = work

	out, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "a ```sql recipe failed sqlc against the migration set — a recipe names a "+
		"column or table the schema does not have:\n%s", out)
}

// compileHCLSnippets wraps each ```hcl fence in a minimal `schema "main" {}` and runs
// `atlas schema inspect` over it. The fences are table blocks that reference schema.main, so the
// wrapper is exactly what a real db/schema.hcl provides around them.
func compileHCLSnippets(t *testing.T, root string, snippets []snippet) {
	t.Helper()

	atlas, err := exec.LookPath("atlas")
	if err != nil {
		t.Skip("atlas not on PATH; run make setup (CI installs it via setup-toolchain)")
	}

	_ = root

	work, err := os.MkdirTemp("", "dkp-snippet-hcl-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(work) })

	var checked int
	for _, s := range snippets {
		if s.lang != "hcl" {
			continue
		}

		file := filepath.Join(work, sanitiseName(s.doc, s.index)+".hcl")
		wrapped := "schema \"main\" {\n}\n\n" + s.code + "\n"
		require.NoError(t, os.WriteFile(file, []byte(wrapped), 0o644))

		cmd := exec.Command(atlas, "schema", "inspect", //nolint:gosec // resolved path, fixed args
			"--url", "file://"+file,
			"--dev-url", atlasDevURL(t),
		)

		out, inspectErr := cmd.CombinedOutput()
		require.NoErrorf(t, inspectErr,
			"%s:%d: a ```hcl fence failed `atlas schema inspect` — the schema fragment does not parse "+
				"or is not valid SQLite:\n%s", s.doc, s.line, out)
		checked++
	}

	require.Positive(t, checked, "no HCL snippets were checked")
}

// atlasDevURL returns an in-memory Atlas dev-url whose database name no other invocation will use.
//
// Atlas derives its advisory lock name from the dev-url and takes that lock machine-wide, so two
// Atlas processes sharing a dev-url can fail with `acquiring database lock: ... already taken`
// rather than queueing — the flake in issue #36, which fired on `migrate diff` between packages
// that `go test ./...` was running at the same time. `schema inspect` is not observed taking that
// lock today, so this call site is not the one that failed; it gets its own name anyway, because
// the fixed string is what a future call site copies and atlas.hcl no longer contains one to copy.
func atlasDevURL(t *testing.T) string {
	t.Helper()

	var id [8]byte
	_, err := rand.Read(id[:])
	require.NoError(t, err, "read random bytes for the Atlas dev database name")

	return "sqlite://dkp_dev_" + hex.EncodeToString(id[:]) + "?mode=memory"
}

// uniqueQueryNames suffixes every `-- name: Foo :one` in a snippet with a per-snippet tag, so the
// same recipe name appearing in two documents does not collide in sqlc's single query set. The SQL
// body is untouched; only the sqlc annotation changes, which is what the gate must not let drift.
func uniqueQueryNames(code, tag string) string {
	return sqlNameDirective.ReplaceAllString(code, "${1}${2}_"+tag+"${3}")
}

// sanitiseName turns a (document, ordinal) pair into a Go-and-filesystem-safe identifier for a
// per-snippet package directory, so a build failure's directory name points back at the fence.
func sanitiseName(doc string, index int) string {
	base := strings.TrimSuffix(filepath.Base(doc), filepath.Ext(doc))
	base = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		default:
			return '_'
		}
	}, base)

	return strings.ToLower(base) + "_" + itoa(index)
}

// itoa is a tiny strconv.Itoa to keep the import list minimal in a file that is otherwise all os/exec.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}

	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}

	return string(b)
}
