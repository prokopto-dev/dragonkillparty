// Command dkp is the single Dragon Kill Party binary: HTTP server, importer and operator tooling.
//
// This package is cobra wiring only. Anything that is more than "parse a flag and call a thing"
// belongs in internal/ — see the repo map in AGENTS.md.
package main

import (
	"context"
	"os"
)

func main() {
	// context.Background belongs in main, TestMain and job-worker roots, and nowhere else.
	if err := newRootCmd().ExecuteContext(context.Background()); err != nil {
		// Cobra already printed the error to stderr. Printing it again would double every failure.
		os.Exit(1)
	}
}
