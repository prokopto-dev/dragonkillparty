package main

import (
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/spf13/cobra"

	"github.com/prokopto-dev/dragonkillparty/internal/net/safehttp"
)

const (
	// healthcheckDefaultURL is where `dkp healthcheck` looks by default. It is loopback and it is the
	// port `dkp serve` binds by default (defaultAddr, ":8080"). The container HEALTHCHECK runs inside
	// the same network namespace as the server, so 127.0.0.1 is the server, and a healthcheck that
	// reached out over the network would be testing DNS and the bridge rather than the process.
	healthcheckDefaultURL = "http://127.0.0.1:8080/healthz"

	// healthcheckTimeout bounds the single request. A HEALTHCHECK that can hang is a HEALTHCHECK that
	// never reports unhealthy: Docker would wait for it instead of restarting the container. Short,
	// because /healthz is a constant-time handler that touches nothing (canonical §13) — anything
	// slower than this is the process being wedged, which is exactly what we want to catch.
	healthcheckTimeout = 3 * time.Second
)

// newHealthcheckCmd builds `dkp healthcheck`.
//
// This is the command the Dockerfile HEALTHCHECK invokes (canonical §13, "The Dockerfile HEALTHCHECK
// calls /healthz, not dkp doctor"). It performs a plain loopback GET /healthz and exits 0 on 200,
// non-zero otherwise. It is NOT `dkp doctor`: doctor inspects the database, and a database-touching
// healthcheck lets Docker kill the container mid-migration, which is how a guild loses its ledger.
//
// It opens no database, reads no DKP_DB_PATH, and imports nothing that does. The whole point is that
// it stays green while /readyz is red — a container mid-migration is HEALTHY (do not kill it) but not
// READY (do not route to it). TestHealthcheck_UnreadableDatabase_StillSucceeds pins that.
func newHealthcheckCmd() *cobra.Command {
	var url string

	cmd := &cobra.Command{
		Use:   "healthcheck",
		Short: "Probe the local /healthz endpoint (the container HEALTHCHECK)",
		Long: "Perform a loopback GET /healthz and exit 0 when it answers 200.\n\n" +
			"This is what the container HEALTHCHECK runs. It touches no database, by design: a\n" +
			"database-touching healthcheck lets Docker kill the container mid-migration. Use\n" +
			"`dkp doctor` or GET /readyz for a database-aware check.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runHealthcheck(cmd, url)
		},
	}

	cmd.Flags().StringVar(&url, "url", healthcheckDefaultURL,
		"the /healthz URL to probe (loopback only — this command probes this process)")

	return cmd
}

// runHealthcheck issues one GET and maps a 200 to a nil error and anything else to a failure.
//
// The client comes from safehttp, which is the only place in the repository allowed to build one
// (NET001). It is the LOOPBACK client specifically: this command probes the process it is running
// beside, and 127.0.0.1 is an address the outbound policy denies — correctly, since that is exactly
// where an SSRF wants to land. safehttp.NewLoopback is the complement of that policy rather than a
// hole in it (it can reach nothing BUT loopback), so `--url` pointed at a LAN or internet address is
// refused. A remote probe is `curl`'s job, not the container HEALTHCHECK's.
//
// The explicit timeout is the other half, and it predates safehttp: http.DefaultClient has none, and
// a hung server must make the probe fail rather than make the probe hang.
func runHealthcheck(cmd *cobra.Command, url string) error {
	client := safehttp.NewLoopback(healthcheckTimeout)

	req, err := http.NewRequestWithContext(cmd.Context(), http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build healthcheck request for %s: %w", url, err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("probe %s: %w", url, err)
	}

	// Drain and close so the connection can be reused and no goroutine is left waiting on the body.
	// The body is drained even though it is unused: an unread body pins the connection.
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthcheck %s returned %d, want 200", url, resp.StatusCode)
	}

	return nil
}
