// Command sitegen builds the publishable UI-mockup site from docs/design/mockups/ into _site/.
//
// It is what `make mockup-site` runs and what .github/workflows/pages.yml deploys. All the work is
// in internal/mockup, which is where the tests are; this file is argument handling and exit codes.
//
// It replaces scripts/dc-publish.py and scripts/build-mockup-site.sh (issue #126). Those parsed HTML
// with regular expressions — a hand-written tag scanner, quote-state tracking and a nesting counter
// whose own comments documented two near-misses — to do a job that turns entirely on getting HTML
// parsing right. internal/mockup does it with golang.org/x/net/html instead, and gains a gate the
// shell version could not express: the finished page is handed to a real HTML5 tree builder and
// refused if it drops a table row.
//
// It lives beside the package it drives rather than in cmd/dkp for the reason
// internal/ledger/enumgen gives: cmd/dkp is the product binary, and an officer never publishes a
// design reference.
//
// Usage: sitegen [output-dir]        (default: _site)
package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/prokopto-dev/dragonkillparty/internal/mockup"
)

// mockupsDir is where the vendored surfaces live, relative to the repo root — which is where
// `make mockup-site` runs.
const mockupsDir = "docs/design/mockups"

// defaultOut is the directory .github/workflows/pages.yml uploads.
const defaultOut = "_site"

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "\033[31mmockup-site: %v\033[0m\n", err)
		os.Exit(1)
	}
}

func run(args []string, w io.Writer) error {
	if len(args) > 1 {
		return fmt.Errorf("expected at most one argument (the output directory), got %d", len(args))
	}

	// DKP_REPO_ROOT is the same contract the gate scripts use, so a test can drive this against a
	// fixture tree instead of the repository it is running inside.
	root := os.Getenv("DKP_REPO_ROOT")
	if root == "" {
		root = "."
	}

	out := filepath.Join(root, defaultOut)
	if len(args) == 1 && args[0] != "" {
		out = args[0]
	}

	if !filepath.IsAbs(out) {
		abs, err := filepath.Abs(out)
		if err != nil {
			return fmt.Errorf("resolve output directory %s: %w", out, err)
		}

		out = abs
	}

	if _, err := fmt.Fprintf(w, "mockup site → %s\n", out); err != nil {
		return fmt.Errorf("write to stdout: %w", err)
	}

	return mockup.Build(filepath.Join(root, mockupsDir), out, w)
}
