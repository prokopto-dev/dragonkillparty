package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/prokopto-dev/dragonkillparty/internal/ledger"
	"github.com/prokopto-dev/dragonkillparty/internal/store"
)

// `dkp verify-ledger` — replay the ledger from genesis and check it against itself. Phase 1, issue
// #198.
//
// Wiring only, per the repo map: the replay is internal/ledger.Verify, and this file resolves flags,
// opens the store, renders the report and picks the exit code.
//
// THE EXIT CODE IS THE PRODUCT. An operator runs this by hand once; the nightly job runs it every
// night and reads nothing but the status. So a clean replay exits 0, drift exits non-zero, and — the
// one that is easy to get wrong — a replay that could not READ exits non-zero too, rather than
// reporting a clean ledger it never looked at. `make verify-ledger` and nightly-verify.yml's
// `replay / seed.Perf` job depend on exactly that distinction.
//
// It is READ-ONLY and safe against a live instance. The ledger is append-only and this command
// writes nothing at all, not even the cache: `--rebuild`, which the operations docs describe as the
// repair for drift, discards and recomputes balance_snapshot and is a separate job. A verifier that
// could repair what it found would be a verifier that could hide it.
//
// It reads through store.ReadTx — a READ transaction on the read pool — so the whole replay observes
// one consistent snapshot. Not store.Tx: that is the write pool, capped at one connection, and
// holding it for a multi-minute replay would queue every raid-night write behind a report
// (.claude/rules/store-and-sql.md: long jobs must not sit on the writer). Not store.Q() either —
// see the comment at the call site, which is the bug that distinction exists to prevent.

// newVerifyLedgerCmd builds `dkp verify-ledger`.
func newVerifyLedgerCmd() *cobra.Command {
	var (
		maxFindings int
		quiet       bool
	)

	cmd := &cobra.Command{
		Use:   "verify-ledger",
		Short: "Replay the ledger and verify its hash chains and cached balances",
		Long: "Replay the ledger from genesis and verify it against itself.\n\n" +
			"Two things are checked. Every batch hash and every audit hash is RECOMPUTED from the\n" +
			"stored rows and compared to what is stored, including the prev_hash links and the chain\n" +
			"heads in dkp_meta. And every ledger entry is folded back into per-account balances,\n" +
			"which must reproduce balance_snapshot exactly — amount, entry count and as-of seq — with\n" +
			"no cached row the log does not support and no account the cache has missed.\n\n" +
			"It is read-only and safe to run against a running instance. It exits 0 when the ledger\n" +
			"is clean, and non-zero when it is not or when it could not be read.\n\n" +
			"DKP_DB_PATH selects the database.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dbPath := os.Getenv(dbPathEnv)
			if dbPath == "" {
				return fmt.Errorf("%s is not set", dbPathEnv)
			}

			s, err := store.Open(cmd.Context(), dbPath)
			if err != nil {
				return fmt.Errorf("open %s: %w", dbPath, err)
			}
			defer func() {
				if closeErr := s.Close(); closeErr != nil {
					// Deliberately reported rather than returned: the verdict has already been
					// printed and decided, and failing the command on a close would turn a clean
					// ledger into a red nightly for a reason that is not about the ledger. The
					// write itself is waived — there is nowhere left to report a failed report.
					_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "close %s: %v\n", dbPath, closeErr)
				}
			}()

			out := cmd.OutOrStdout()

			var report ledger.Report

			// ONE SNAPSHOT FOR THE WHOLE REPLAY, through store.ReadTx rather than store.Q().
			//
			// The replay reads a pool's batches, then its chain head, then its cached balances, and
			// then compares them against each other. Through Q() each of those statements gets its
			// own connection off the read pool and its own view of whatever had committed by then,
			// so a batch committing mid-replay would advance the head past the walk's last hash and
			// this command would report ledger_head_mismatch on a healthy ledger. On raid night —
			// when commits happen, and when a false corruption alarm is worth the least.
			//
			// A WAL read transaction pins one snapshot and blocks no writer, so a concurrent award
			// still commits; the replay simply verifies the database as it was when it started,
			// which is the only thing "verify" can honestly mean while writes continue.
			err = s.ReadTx(cmd.Context(), func(ctx context.Context, q store.Queries) error {
				var verifyErr error

				report, verifyErr = ledger.Verify(ctx, q, ledger.VerifyOptions{
					MaxFindings: maxFindings,
					Progress:    verifyProgress(out, quiet),
				})

				return verifyErr
			})
			if err != nil {
				return fmt.Errorf("verify %s: %w", dbPath, err)
			}

			if err := writeVerifyReport(out, report); err != nil {
				return err
			}

			if !report.Clean() {
				return errVerifyFailed
			}

			return nil
		},
	}

	cmd.Flags().IntVar(&maxFindings, "max-findings", ledger.DefaultVerifyMaxFindings,
		"how many problems to print before summarising the rest (negative prints all of them)")
	cmd.Flags().BoolVar(&quiet, "quiet", false,
		"suppress the progress lines; the report and the exit code are unaffected")

	return cmd
}

// errVerifyFailed is what a drifted ledger returns, so the command exits non-zero without cobra
// printing a second copy of the findings that writeVerifyReport already listed.
//
// A sentinel with a deliberately short message: the report above it is the diagnosis, and this line
// is only the verdict.
var errVerifyFailed = errors.New("ledger verification FAILED")

// verifyProgress builds the callback that keeps a multi-minute replay from looking hung.
//
// It returns nil when quiet, which is what disables it inside Verify — and nil is also what a test
// harness wants, since a progress line per page over a seeded ledger is noise in a failure message.
func verifyProgress(out io.Writer, quiet bool) func(ledger.Progress) {
	if quiet {
		return nil
	}

	return func(p ledger.Progress) {
		// Deliberately discarded, as in `dkp seed`: a failed progress write is not a reason to
		// abandon a replay, and there is nowhere to report it more likely to work than the stream
		// that just failed.
		_, _ = fmt.Fprintf(out, "  pool %s: %d batches, %d entries\n", p.PoolID, p.Batches, p.Entries)
	}
}

// writeVerifyReport renders the report: what was read, then what was wrong with it.
//
// THE COUNTS COME FIRST, and they are not decoration. "Clean" over an empty database and "clean"
// over half a million entries are the same word for very different facts, and an operator reading
// this at 1 a.m. after a restore needs to see which one they have before they trust the verdict.
func writeVerifyReport(out io.Writer, r ledger.Report) error {
	if _, err := fmt.Fprintf(out,
		"replayed %d pool(s): %d batches, %d entries, %d cached balances, %d audit rows\n",
		len(r.Pools), r.Batches(), r.Entries(), r.Snapshots(), r.Audit.Rows,
	); err != nil {
		return fmt.Errorf("write summary: %w", err)
	}

	for _, p := range r.Pools {
		if _, err := fmt.Fprintf(out, "  pool %s  head seq %d  %s\n",
			p.PoolID, p.HeadSeq, orNoChain(p.Head, "batches")); err != nil {
			return fmt.Errorf("write pool summary: %w", err)
		}
	}

	if _, err := fmt.Fprintf(out, "  audit chain  head seq %d  %s\n",
		r.Audit.HeadSeq, orNoChain(r.Audit.Head, "rows")); err != nil {
		return fmt.Errorf("write audit summary: %w", err)
	}

	if r.Clean() {
		if _, err := fmt.Fprintln(out, "ledger verified clean"); err != nil {
			return fmt.Errorf("write verdict: %w", err)
		}

		return nil
	}

	if _, err := fmt.Fprintf(out, "%d finding(s):\n", r.FindingCount); err != nil {
		return fmt.Errorf("write finding count: %w", err)
	}

	for _, f := range r.Findings {
		if _, err := fmt.Fprintf(out, "  %s\n", f); err != nil {
			return fmt.Errorf("write finding: %w", err)
		}
	}

	if r.Truncated() {
		// Never a silent cap: a report that printed a hundred lines and stopped, without saying it
		// had stopped, reads as "there were a hundred problems".
		if _, err := fmt.Fprintf(out,
			"  ... and %d more; re-run with --max-findings=-1 to print all of them\n",
			r.FindingCount-int64(len(r.Findings))); err != nil {
			return fmt.Errorf("write truncation notice: %w", err)
		}
	}

	return nil
}

// orNoChain renders a chain head, or says there is no chain rather than printing an empty column.
// noun names what the chain would have held, so an empty pool and an empty audit log do not both
// report "no batches".
func orNoChain(head, noun string) string {
	if head == "" {
		return "(no " + noun + ")"
	}

	return "head " + head
}
