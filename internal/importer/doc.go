// Package importer is the EQdkp Plus ETL, which runs in two phases: stage the source data
// verbatim, then transform it. Nothing here is transcribed from EQdkp Plus itself; the
// importer reads a user's database at runtime and never ports its PHP.
//
// Lands in: the pure transforms are a Phase 0 parallel lane; the importer proper is Phase 5.
package importer
