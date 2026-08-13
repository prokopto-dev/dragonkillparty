package main

import (
	"github.com/spf13/cobra"
)

// newRootCmd builds the `dkp` command tree.
//
// Wiring only: it declares no flags with behaviour of their own and runs nothing. Bare `dkp`
// prints help, which is what cobra does for a command with no Run.
func newRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dkp",
		Short: "DKP and guild management for Project 1999 EverQuest raiding guilds",
		Long: "Dragon Kill Party is a single binary that runs a guild's DKP ledger, raid attendance,\n" +
			"loot auctions and guild site. It is API-first: the bundled web UI is an ordinary client\n" +
			"of the same public API that bots use.\n\n" +
			"Run `dkp serve` to start the server.",

		// A runtime failure is not a usage error. Without this, a failed `dkp serve --addr :80`
		// dumps the whole help text after the error and buries the one line that mattered.
		SilenceUsage: true,
	}

	cmd.AddCommand(
		newVersionCmd(),
		// nil: no readiness hook in production. Tests pass one to learn an ephemeral port.
		newServeCmd(nil),
		newMigrateCmd(),
		// `dkp seed` — the synthetic-dataset generator `make seed` runs (issue #190).
		newSeedCmd(),
		newOpenAPICmd(),
		// The container HEALTHCHECK. A loopback GET /healthz that touches no database — canonical
		// §13 — so it stays green through a migration while /readyz reports not-ready.
		newHealthcheckCmd(),
	)

	return cmd
}
