// Package store is the only package that may hold a *sql.DB or call sql.Open, and every
// mutation of persistent state goes through store.Tx (law 2). Query shapes belong in
// db/RECIPES.md rather than being invented at the call site.
//
// It owns two pools against one SQLite file — a single-connection writer with
// _txlock=immediate, and a reader sized to max(4, NumCPU) — plus the statement counter every
// later statement budget reads from, and the template-database harness integration tests clone.
package store
