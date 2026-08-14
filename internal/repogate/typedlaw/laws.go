package typedlaw

import (
	"go/types"
	"strings"
)

// law is one type-aware analyzer: an id shared with internal/repogate, a description, and a pure
// function over one type-checked package.
//
// The shape is go/analysis's — {Name, Doc, Run over a package, report a position} — for the reason
// internal/repogate's astRule carries the same shape: the framework's VALUE is the shape, and the
// dependency buys a driver this package does not need. What is not borrowed is facts; no law here
// asks a question about a package other than the one it is reading.
type law struct {
	id   string
	desc string

	// run reports every violation in one package. A law that does not apply to the package —
	// PURE001 outside internal/strategy — returns nothing rather than being filtered by the caller,
	// so the scoping and the reason for it stay in one place.
	run func(p *pkg) []string
}

// laws is the catalogue, in the order internal/repogate reports its own: law 1, law 2, law 3, then
// money. Law 4 is not here and cannot be — see the package doc.
func laws() []law {
	return []law{
		routeLaw(),
		sqlOpenLaw(),
		sqlCallLaw(),
		sqlForTestLaw(),
		sqlHandleLaw(),
		strategyStoreLaw(),
		strategyRandLaw(),
		clockNowLaw(),
		clockSystemLaw(),
		moneyFloatLaw(),
	}
}

// --- Law 1: routes are declared only in internal/api --------------------------------------------

// routeLaw is ROUTE001, resolved to the OBJECT rather than to the two identifiers around a dot.
//
// What that buys over the syntax twin: `huma.Register` assigned to a variable and called through
// it, a dot-imported huma, and a Register reached through any alias — none of which produce a
// `huma.Register` selector for a parser to match. What it costs: a route declared in a file that
// does not compile is invisible here, which is why ROUTE001 in internal/repogate is the gate.
func routeLaw() law {
	return law{
		id:   "ROUTE001",
		desc: "huma.Register reached outside internal/api (routes are declared only there)",
		run: func(p *pkg) []string {
			if p.under("internal/api") {
				return nil
			}

			return p.usesOf(func(obj types.Object) bool {
				return isFuncIn(obj, "Register", func(path string) bool {
					return strings.Contains(path, "danielgtaylor/huma")
				})
			})
		},
	}
}

// --- Law 2: *sql.DB is held only by internal/store ----------------------------------------------

// sqlOpenLaw is SQL001: the two constructors, by object identity.
func sqlOpenLaw() law {
	return law{
		id:   "SQL001",
		desc: "database/sql.Open / OpenDB reached outside internal/store",
		run: func(p *pkg) []string {
			if p.under("internal/store") {
				return nil
			}

			return p.usesOf(func(obj types.Object) bool {
				return isFuncIn(obj, "Open", isDatabaseSQL) || isFuncIn(obj, "OpenDB", isDatabaseSQL)
			})
		},
	}
}

// queryMethods are the database/sql methods that run SQL. Named rather than derived because the set
// is closed and small, and because naming it here is what makes the rule readable next to SQL002 in
// internal/repogate, which watches the same six names without being able to tell whose they are.
var queryMethods = map[string]bool{
	"Query": true, "QueryRow": true, "Exec": true,
	"QueryContext": true, "QueryRowContext": true, "ExecContext": true,
}

// sqlCallLaw is SQL002, decided by the method's DECLARING PACKAGE rather than by its name.
//
// This is the rule that gains most from types, in both directions at once.
//
// It stops over-reporting: `r.URL.Query()` is net/url's accessor and appears throughout
// internal/api. The syntax rule has to exclude it by shape — a zero-argument call — because a
// line-scoped allowlist can only drop the whole line, and the line most likely to hold
// `r.URL.Query()` is also the one most likely to hold the `conn.QueryContext(ctx, …)` next to it.
// Here the exclusion is not needed at all: net/url is not database/sql.
//
// And it stops under-reporting: a handle reached through an embedded field, a named type or a type
// alias never spells `sql` in the calling file, and the promoted method still resolves to
// database/sql. types.Selection follows the embedding for free.
func sqlCallLaw() law {
	return law{
		id:   "SQL002",
		desc: "a database/sql method called outside internal/store",
		run: func(p *pkg) []string {
			if p.under("internal/store") {
				return nil
			}

			var hits []string

			for sel, selection := range p.info.Selections {
				fn, ok := selection.Obj().(*types.Func)
				if !ok || !queryMethods[fn.Name()] || !isDatabaseSQL(pkgPath(fn)) {
					continue
				}

				hits = append(hits, p.hit(sel.Sel))
			}

			return hits
		},
	}
}

// forTestHelpers are internal/store's exported raw-SQL test affordances.
var forTestHelpers = map[string]bool{
	"ExecForTest": true, "ExecErrForTest": true, "QueryForTest": true,
	"QueryRowForTest": true, "TxForTest": true, "TxHandleForTest": true,
}

// sqlForTestLaw is SQL003, and the typed form is the cleaner statement of the same rule.
//
// internal/repogate has to allow `_test.go:` and `store/testing.go:` by path regex, because a
// syntax pass cannot tell the declaration from a call. This pass reads only the NON-TEST build (see
// the package doc), so every remaining reference is by definition production code — and the object
// identity means a helper reached through a wrapper in another package is still the same object.
func sqlForTestLaw() law {
	return law{
		id:   "SQL003",
		desc: "a store ForTest raw-SQL helper reached from the non-test build",
		run: func(p *pkg) []string {
			if p.under("internal/store") {
				return nil // the declaration site, and the package's own wiring around it
			}

			return p.usesOf(func(obj types.Object) bool {
				return forTestHelpers[obj.Name()] && underPackage(pkgPath(obj), "internal/store")
			})
		},
	}
}

// handleTypes are the database/sql types that ARE a connection to the database. sql.Rows and
// sql.Result are deliberately absent: they are the RESULT of a query, they are what a store method
// legitimately returns across the boundary, and banning them would ban the boundary.
var handleTypes = map[string]bool{"DB": true, "Tx": true, "Conn": true, "Stmt": true}

// sqlHandleLaw is SQL004 — law 2 as AGENTS.md states it, rather than an approximation of it.
//
// "*sql.DB is held only by internal/store" is a statement about a TYPE, and internal/repogate has
// no rule for it because a syntax pass cannot make one: matching the literal text `*sql.DB` is the
// rule an alias, a named type or an embedded field walks straight past, and the repository's own
// escape hatch — a wrapper type in another package that embeds the handle — would be invisible.
// SQL001 and SQL002 approximate it from the two ends a parser can see, the constructor and the
// call. This is the middle.
//
// Reported per DECLARATION — a var, a field, a parameter, a result — because that is where the
// handle is held, and because reporting every expression of that type would print the same escape
// dozens of times.
func sqlHandleLaw() law {
	return law{
		id:   "SQL004",
		desc: "a database/sql handle type (DB, Tx, Conn, Stmt) held outside internal/store",
		run: func(p *pkg) []string {
			if p.under("internal/store") {
				return nil
			}

			var hits []string

			for ident, obj := range p.info.Defs {
				if obj == nil || !holdsSQLHandle(obj) {
					continue
				}

				switch obj.(type) {
				case *types.Var, *types.TypeName:
					hits = append(hits, p.hit(ident))
				}
			}

			return hits
		},
	}
}

// holdsSQLHandle reports whether an object's type reaches a database/sql handle — directly, through
// a pointer, a slice, a map value or a named type's underlying form.
func holdsSQLHandle(obj types.Object) bool {
	return mentionsSQLHandle(obj.Type(), map[types.Type]bool{})
}

// mentionsSQLHandle walks a type looking for a database/sql handle, with a seen set because a
// struct may refer to itself.
//
// Unalias FIRST, and it is not housekeeping: `type handle = sql.DB` is the single cheapest way to
// hold the database under a name that never says so, and an alias is a distinct types.Type that
// answers no question about the thing it names.
func mentionsSQLHandle(t types.Type, seen map[types.Type]bool) bool {
	if t == nil || seen[t] {
		return false
	}

	seen[t] = true
	t = types.Unalias(t)

	switch typ := t.(type) {
	case *types.Named:
		obj := typ.Obj()
		if obj != nil && handleTypes[obj.Name()] && isDatabaseSQL(pkgPath(obj)) {
			return true
		}

		// An alias or a defined type whose underlying form is the handle — `type handle = sql.DB`,
		// or a struct that embeds one. This is the case the syntax rule cannot have.
		return mentionsSQLHandle(typ.Underlying(), seen)
	case *types.Pointer:
		return mentionsSQLHandle(typ.Elem(), seen)
	case *types.Slice:
		return mentionsSQLHandle(typ.Elem(), seen)
	case *types.Array:
		return mentionsSQLHandle(typ.Elem(), seen)
	case *types.Map:
		return mentionsSQLHandle(typ.Elem(), seen)
	case *types.Chan:
		return mentionsSQLHandle(typ.Elem(), seen)
	case *types.Struct:
		for i := range typ.NumFields() {
			if typ.Field(i).Embedded() && mentionsSQLHandle(typ.Field(i).Type(), seen) {
				return true
			}
		}

		return false
	default:
		return false
	}
}

// --- Law 3: internal/strategy is pure -----------------------------------------------------------

// strategyStoreLaw is PURE001, TRANSITIVELY through this module's packages.
//
// The syntax twin reads one file's import block, so a strategy that reached the store through an
// intermediate package satisfies it. `go list` already reported the graph to build the tree, so
// asking the stronger question costs nothing. internal/strategy/arch_test.go asks it too, by
// walking the import graph; two independent readers of one property is the posture AGENTS.md takes
// toward every law, not a redundancy to collapse.
func strategyStoreLaw() law {
	return law{
		id:   "PURE001",
		desc: "internal/strategy reaches internal/store transitively (a plan may not read the database)",
		run: func(p *pkg) []string {
			return p.reachesFrom("internal/strategy", func(path string) bool {
				return underPackage(path, "internal/store")
			})
		},
	}
}

// strategyRandLaw is PURE002, transitively through this module's packages, both spellings.
//
// math/rand/v2 counts: the seed persisted onto ledger_batch.rng_seed is what makes a batch replay
// byte-identically, and a strategy that reached any unseeded source instead would make that column
// a decoration. Transitive for the same reason as PURE001 — a helper package under internal/ that
// rolls its own dice rolls them inside the plan.
//
// Terminal at the module boundary, and the tree type records the case that decided it: walked over
// the full closure this fires on the tree as it stands today, through ulid's own math/rand.
func strategyRandLaw() law {
	return law{
		id:   "PURE002",
		desc: "internal/strategy reaches math/rand transitively (use the injected seeded Rng)",
		run: func(p *pkg) []string {
			return p.reachesFrom("internal/strategy", func(path string) bool {
				return path == "math/rand" || strings.HasPrefix(path, "math/rand/")
			})
		},
	}
}

// clockNowLaw is CLOCK001, and the dot-import is the whole reason it is here.
//
// `import . "time"` makes `Now()` a bare call with no selector at all, so the syntax rule has
// nothing to match — issue #172 names this case first. Resolving the identifier to its object
// answers the question the rule is actually asking: is this the function time.Now.
func clockNowLaw() law {
	return law{
		id:   "CLOCK001",
		desc: "time.Now reached outside internal/clock (use the injected Clock)",
		run: func(p *pkg) []string {
			if p.under("internal/clock") {
				return nil
			}

			return p.usesOf(func(obj types.Object) bool {
				return isFuncIn(obj, "Now", func(path string) bool { return path == "time" })
			})
		},
	}
}

// clockSystemLaw is CLOCK002: the real clock, constructed inside a strategy.
//
// clock.System is the only real-clock path out of internal/clock — Clock is an interface, Fake is a
// test double, and System.Now is the one call site in the repository allowed to read the wall
// clock. A plan that depends on when it ran cannot be replayed.
func clockSystemLaw() law {
	return law{
		id:   "CLOCK002",
		desc: "clock.System reached in internal/strategy (the clock is injected through Ctx.Clock)",
		run: func(p *pkg) []string {
			if !p.under("internal/strategy") {
				return nil
			}

			return p.usesOf(func(obj types.Object) bool {
				_, isType := obj.(*types.TypeName)

				return isType && obj.Name() == "System" && underPackage(pkgPath(obj), "internal/clock")
			})
		},
	}
}

// --- Money: the point path is an integer --------------------------------------------------------

// moneyFloatLaw is MONEY001, decided by the TYPE of an expression rather than by the two words a
// syntax pass can look for.
//
// The hole it closes is not exotic. `rate := 0.15` in internal/strategy is a float64 with the word
// float64 nowhere in the file, and so is `total / 2.0`, and so is a variable whose type came from a
// named float in another package. A float in the point path does not fail, it DRIFTS: a balance
// wrong by a fraction of a point for a year is found by a guild member disputing a bid, never by
// CI.
//
// Declarations and composite-literal keys are read through the same expression map, so one
// offending line is reported once — the position is the expression, and dedupe collapses the rest.
func moneyFloatLaw() law {
	return law{
		id:   "MONEY001",
		desc: "an expression whose type is a float in internal/ledger or internal/strategy",
		run: func(p *pkg) []string {
			if !p.under("internal/ledger") && !p.under("internal/strategy") {
				return nil
			}

			var hits []string

			for expr, tv := range p.info.Types {
				if tv.Type == nil || !isFloat(tv.Type) {
					continue
				}

				hits = append(hits, p.hit(expr))
			}

			return hits
		},
	}
}

// isFloat reports whether a type is, or resolves to, a floating-point kind.
func isFloat(t types.Type) bool {
	basic, ok := t.Underlying().(*types.Basic)

	return ok && basic.Info()&types.IsFloat != 0
}

// --- Shared predicates --------------------------------------------------------------------------

// isDatabaseSQL reports whether an import path is the standard library's database/sql.
func isDatabaseSQL(path string) bool { return path == "database/sql" }

// underPackage reports whether an import path passes through a repo-relative package path.
//
// The module path is not assumed, for the reason internal/repogate gives: the fixtures are
// fabricated modules, and a rule that hardcoded this repository's module path would pass vacuously
// in exactly the tree a test points it at.
func underPackage(path, dir string) bool {
	return strings.Contains("/"+path+"/", "/"+dir+"/")
}

// pkgPath returns the import path an object was declared in, or "" for a universe-scope object.
func pkgPath(obj types.Object) string {
	if obj == nil || obj.Pkg() == nil {
		return ""
	}

	return obj.Pkg().Path()
}

// isFuncIn reports whether an object is a package-level function with the given name, declared in a
// package whose path satisfies match.
func isFuncIn(obj types.Object, name string, match func(path string) bool) bool {
	fn, ok := obj.(*types.Func)
	if !ok || fn.Name() != name {
		return false
	}

	// A METHOD with the same name is not the function: `clock.System{}.Now()` resolves to a method
	// on System, which is CLOCK002's business, and reporting it here would give one escape two ids.
	if sig, ok := fn.Type().(*types.Signature); ok && sig.Recv() != nil {
		return false
	}

	return match(pkgPath(fn))
}

// usesOf returns a hit for every identifier in the package that resolves to an object satisfying
// match.
//
// Uses rather than calls: `now := time.Now` reads the wall clock as surely as `time.Now()` does,
// and a reference handed to another package is the shape an escape actually takes.
func (p *pkg) usesOf(match func(obj types.Object) bool) []string {
	var hits []string

	for ident, obj := range p.info.Uses {
		if obj != nil && match(obj) {
			hits = append(hits, p.hit(ident))
		}
	}

	return hits
}

// reachesFrom reports a package-level finding when p sits under dir and reaches, through this
// module's own packages, a dependency satisfying match.
//
// The position is the package clause of its first file rather than an import line, because the
// finding is about the CLOSURE and the offending import may be three packages away. The chain is
// printed with it: a transitive finding whose path the reader has to rediscover with `go list
// -deps` is a finding they will not act on.
func (p *pkg) reachesFrom(dir string, match func(path string) bool) []string {
	if !p.under(dir) || len(p.files) == 0 {
		return nil
	}

	chain, ok := p.reaches(match)
	if !ok {
		return nil
	}

	return []string{p.hit(p.files[0].Name) + "  → " + strings.Join(shorten(chain, p.t.modulePath), " → ")}
}

// shorten drops the module prefix from this module's packages in a chain, so the printed path reads
// `internal/strategy → internal/dice → math/rand` rather than three copies of the module path.
func shorten(chain []string, module string) []string {
	out := make([]string, 0, len(chain))

	for _, path := range chain {
		out = append(out, strings.TrimPrefix(strings.TrimPrefix(path, module), "/"))
	}

	return out
}
