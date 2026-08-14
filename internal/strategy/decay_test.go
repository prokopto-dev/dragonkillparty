package strategy_test

import (
	"fmt"
	"testing"

	"github.com/prokopto-dev/dragonkillparty/internal/core"
	"github.com/prokopto-dev/dragonkillparty/internal/strategy"
)

// What the decay-family suites share. Phase 1, #194.
//
// `earn_test.go` holds the assertions three earn-family strategies make identically; this file holds
// the one thing `decay_percent` and `decay_window` need that a map of balances cannot give them — a
// façade that answers POSITIONALLY, because positional reads are the whole of their idempotency
// argument.
//
// THE FAKE IN fixed_price_test.go ANSWERS EVERY SEQ WITH THE SAME NUMBER, which is exactly right for
// a test about one plan and useless for a test about two. A decay run is idempotent because it reads
// the balances the period's snapshot seq froze, so a re-run after the first batch committed proposes
// that same batch again rather than a second haircut — and a fake that ignored the seq would report
// that property held while the planner did nothing of the kind. ledgerCtx keeps an append-only entry
// log and sums it, so committing a batch is what a commit actually is: rows at a higher seq.

// loggedEntry is one committed ledger entry: the account, what moved, and the seq of the batch it
// belonged to. It is the minimum that makes both façade reads answerable — a balance is the sum of
// the entries at or below a seq, and an earning is the sum of the POSITIVE ones inside a slice.
type loggedEntry struct {
	seq     int64
	account core.ULID
	amount  core.Centipoints
}

// ledgerCtx is a strategy.Ctx backed by an entry log rather than by a map of balances.
//
// It embeds *fakeCtx for everything that is not a positional read — the roster, the system accounts,
// the injected clock and seeded Rng, the config document — so the two fakes cannot disagree about
// what a pool looks like. Balance and EarnedBetween are the two it answers itself.
type ledgerCtx struct {
	*fakeCtx

	entries []loggedEntry
}

// newLedgerCtx builds a log-backed façade over n person accounts and an empty ledger.
func newLedgerCtx(tb testing.TB, n int, configJSON string) *ledgerCtx {
	tb.Helper()

	return &ledgerCtx{fakeCtx: newCtx(tb, n, 0, configJSON)}
}

// credit appends one entry at a seq, which is how a test says "this account earned this, then".
//
// It moves the pool head to that seq, because a batch that has been written IS the head: a planner
// asking for the current balance after this must see it.
func (c *ledgerCtx) credit(seq int64, account core.ULID, amount core.Centipoints) {
	c.entries = append(c.entries, loggedEntry{seq: seq, account: account, amount: amount})

	if seq > c.headSeq {
		c.headSeq = seq
	}
}

// commit writes a planned proposal into the log at a seq, exactly as the ledger would: every entry,
// in the planner's own order, all at the batch's seq.
func (c *ledgerCtx) commit(seq int64, p strategy.BatchProposal) {
	for _, e := range p.Entries {
		c.credit(seq, e.AccountID, e.AmountCp)
	}
}

// Balance is the definitional sum: every entry for the account in a batch at or below asOfSeq.
func (c *ledgerCtx) Balance(
	account core.ULID, balanceKind string, asOfSeq int64,
) (core.Centipoints, error) {
	if balanceKind != strategy.BalanceKindDKP {
		return 0, errUnknownBalanceKind(balanceKind)
	}

	var sum core.Centipoints

	for _, e := range c.entries {
		if e.account == account && e.seq <= asOfSeq {
			sum += e.amount
		}
	}

	return sum, nil
}

// EarnedBetween is the sum of the account's POSITIVE entries in the half-open slice (fromSeq, toSeq].
//
// Positive only, which is the property `decay_window` rests on rather than a convenience: an expiry
// batch is a debit, so it can never appear in a later slice and no earning can expire twice.
func (c *ledgerCtx) EarnedBetween(
	account core.ULID, balanceKind string, fromSeq, toSeq int64,
) (core.Centipoints, error) {
	if balanceKind != strategy.BalanceKindDKP {
		return 0, errUnknownBalanceKind(balanceKind)
	}

	var sum core.Centipoints

	for _, e := range c.entries {
		if e.account == account && e.amount > 0 && e.seq > fromSeq && e.seq <= toSeq {
			sum += e.amount
		}
	}

	return sum, nil
}

// errUnknownBalanceKind is what both reads return for a kind this pool does not hold. A fake that
// answered 0 for 'ep' would let a multi-kind planner look correct against a pool that has no such
// balance at all.
func errUnknownBalanceKind(balanceKind string) error {
	return fmt.Errorf("ledger ctx: unknown balance kind %q", balanceKind)
}

// canonicalOf renders a proposal as the canonical bytes the determinism and idempotency properties
// compare. A helper rather than two lines inline, because every caller must compare the WHOLE
// proposal — asserting on a few fields is how a changed entry order stays invisible.
func canonicalOf(tb testing.TB, p strategy.BatchProposal) string {
	tb.Helper()

	got, err := p.Canonical()
	if err != nil {
		tb.Fatalf("canonicalise the %s proposal: %v", p.Kind, err)
	}

	return string(got)
}
