package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

// Build stamps, injected at link time by the Makefile:
//
//	-ldflags "-X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)"
//
// They are package-level vars because -X can only write to one; they are written once by the
// linker and never assigned at runtime, so they are not the mutable global state go-idioms bans.
//
// The defaults are what an unstamped `go build ./cmd/dkp` reports. None of them may be empty: a
// binary that cannot say what it is cannot be supported, and an empty -X target ships exactly that.
//
// date is injected, never observed. time.Now is banned outside internal/clock (gate CLOCK001 in
// scripts/repo-gates.sh), and a build date read at runtime would report the run date instead —
// a different fact wearing the same label.
var version, commit, date = "dev", "none", "unknown"

// newVersionCmd builds `dkp version`.
//
// The output is one `key: value` per line so a support request ("paste the output of dkp version")
// and a test can both read it without a parser.
func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version, commit and build date",
		Long: "Print the build stamps compiled into this binary. All three are injected by the\n" +
			"build; an unstamped local build reports version \"dev\".",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// cmd.OutOrStdout, never fmt.Print*: forbidigo bans the latter, and a command that
			// writes to the process stdout directly cannot be tested or piped.
			if _, err := fmt.Fprintf(cmd.OutOrStdout(),
				"version: %s\ncommit: %s\ndate: %s\n", version, commit, date); err != nil {
				return fmt.Errorf("write version: %w", err)
			}

			return nil
		},
	}
}
