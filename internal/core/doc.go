// Package core defines the boundary value types every other package shares: Centipoints
// (int64), Micros (int64 Unix microseconds), ULID keys and the cursor codec. Point
// arithmetic is Centipoints only — never a float64, in Go, in SQL or on the wire.
//
// Lands in: Phase 0 PR 8.
package core
