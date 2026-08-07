package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/prokopto-dev/dragonkillparty/internal/api"
)

// newOpenAPICmd builds `dkp openapi`.
//
// It writes the OpenAPI 3.1 document to stdout and nothing else — no flags, no output path, no
// "--format yaml". `make gen` redirects it into openapi/openapi.json (scripts/gen-openapi.sh), and
// the drift gate compares the result byte for byte, so every option added here is another way for
// the committed file and the emitted document to disagree.
//
// The spec is derived from the Go handler types by Huma; code is never generated from it
// (docs/design/02-api-design.md §9). That is why this command can exist at all: there is exactly one
// source of truth, and this prints a projection of it.
func newOpenAPICmd() *cobra.Command {
	return &cobra.Command{
		Use:   "openapi",
		Short: "Print the OpenAPI 3.1 document",
		Long: "Print the OpenAPI 3.1 document describing this binary's HTTP API.\n\n" +
			"The output is byte-for-byte identical to the committed openapi/openapi.json — `make gen`\n" +
			"writes that file with this command, and CI fails if regenerating changes anything.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			doc, err := api.SpecJSON()
			if err != nil {
				return fmt.Errorf("build openapi document: %w", err)
			}

			// cmd.OutOrStdout, never fmt.Print*: forbidigo bans the latter, and a command that
			// writes to the process stdout directly cannot be tested or piped.
			if _, err := cmd.OutOrStdout().Write(doc); err != nil {
				return fmt.Errorf("write openapi document: %w", err)
			}

			return nil
		},
	}
}
