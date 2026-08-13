package repo_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSqlcGen_NoEmptyInterfaceFields closes the trap sqlc.yaml's override list documents in prose.
//
// sqlc's SQLite engine (v1.31.1) types a nullable column read through a bare `SELECT col` as
// `interface{}` rather than honouring emit_pointers_for_null_types — it loses the affinity of a NULL
// column that is not projected through a join or an aggregate. The repair is one `overrides:` entry
// per nullable column, pinning it to the pointer type the setting would have produced, and sqlc.yaml
// carries eighteen of them for exactly that reason.
//
// NOTHING FAILED WHEN ONE WAS MISSING. `make gen` writes the field, the tree compiles, every test
// passes, and `any` lands in the middle of the typed boundaries — which .claude/rules/go-idioms.md
// bans and which the two `var _ Queries = …` assertions cannot see, because an interface method
// taking `interface{}` satisfies a contract that also says `interface{}`. The only thing standing
// between that and a shipped store contract was a reviewer noticing the word in a generated diff.
// decay_run and pool_config_change (#191/#192) added five nullable columns and hit it five times.
//
// STRUCT FIELDS ONLY, deliberately. sqlc's own DBTX interface in db.go takes `...interface{}`
// because that is database/sql's signature, and it is not this rule's business: the defect is a
// COLUMN whose Go type lost its affinity, and a column is a struct field.
func TestSqlcGen_NoEmptyInterfaceFields(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(repoRoot(t), "internal", "store", "sqlitegen")

	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	require.NoError(t, err, "glob %s", dir)
	require.NotEmpty(t, files, "no generated files in %s — this test would pass vacuously", dir)

	var offenders []string

	for _, path := range files {
		fset := token.NewFileSet()

		file, parseErr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		require.NoError(t, parseErr, "parse %s", path)

		ast.Inspect(file, func(n ast.Node) bool {
			structType, ok := n.(*ast.StructType)
			if !ok || structType.Fields == nil {
				return true
			}

			for _, field := range structType.Fields.List {
				if !isEmptyInterface(field.Type) {
					continue
				}

				offenders = append(offenders,
					filepath.Base(path)+":"+fieldNames(field)+" is interface{}")
			}

			return true
		})
	}

	require.Empty(t, offenders,
		"generated struct fields typed interface{}: %s\n"+
			"sqlc lost the affinity of a nullable column. Add an `overrides:` entry per column to "+
			"sqlc.yaml pinning it to the pointer type emit_pointers_for_null_types would have produced "+
			"(*string, *int64, []byte for a BLOB), then run `make gen`. `any` in the store contract is "+
			"what .claude/rules/go-idioms.md bans, and neither compiler assertion in "+
			"internal/store/store.go can see it.", strings.Join(offenders, ", "))
}

// isEmptyInterface reports whether the expression is a bare `interface{}` — no methods, no embedded
// constraints. `any` parses as an *ast.Ident and is not matched here because sqlc does not emit it;
// a generator that started to would show up as a new offender rather than a silent pass, which is
// the safe direction.
func isEmptyInterface(expr ast.Expr) bool {
	iface, ok := expr.(*ast.InterfaceType)

	return ok && (iface.Methods == nil || len(iface.Methods.List) == 0)
}

// fieldNames renders a field's names for the failure message. An anonymous field has none, and
// "(embedded)" is more useful there than an empty string.
func fieldNames(field *ast.Field) string {
	if len(field.Names) == 0 {
		return "(embedded)"
	}

	names := make([]string, 0, len(field.Names))
	for _, name := range field.Names {
		names = append(names, name.Name)
	}

	return strings.Join(names, "/")
}
