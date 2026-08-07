// Package api is the only tree in the repository where an HTTP route may be
// declared (law 1, AGENTS.md). No other package may register a path, mount a
// handler, or otherwise add API surface.
//
// The law is machine-checked, and has been since PR 4 installed the harness at
// route #1: arch_test.go AST-scans every package for huma.Register calls and
// compares the result against the Huma registry, so a route declared elsewhere
// fails CI rather than review.
//
// Routes are grouped one file per resource. There is deliberately no shared
// registry file — it would conflict on every parallel feature branch.
package api
