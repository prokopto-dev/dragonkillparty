// Package store is the only package that may hold a *sql.DB or call sql.Open, and every
// mutation of persistent state goes through store.Tx (law 2). Query shapes belong in
// db/RECIPES.md rather than being invented at the call site.
//
// Lands in: Phase 0 PR 2.
package store
