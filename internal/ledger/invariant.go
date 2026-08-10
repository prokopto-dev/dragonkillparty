package ledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"

	accountkinds "github.com/prokopto-dev/dragonkillparty/internal/account/kinds"
	"github.com/prokopto-dev/dragonkillparty/internal/core"
	"github.com/prokopto-dev/dragonkillparty/internal/store"
	"github.com/prokopto-dev/dragonkillparty/internal/store/sqlitegen"
	"github.com/prokopto-dev/dragonkillparty/internal/strategy"
)

// The commit-time invariant engine. Phase 0 PR 10a.
//
// A strategy DECLARES which rules constrain it (strategy.Invariant, a descriptor); this file
// EXECUTES them, inside the write transaction, with the Queries in hand — which is why the engine
// lives here and not there. Checking "does this batch push an account below its floor?" requires
// reading a balance, and a package that can read a balance is a package that imports internal/store,
// which law 3 forbids for strategies.
//
// The engine checks the declared set PLUS a universal set no strategy may waive. That asymmetry is
// the point: a strategy can add constraints on itself but cannot remove the ones that keep the
// ledger a ledger.
//
// FAIL CLOSED. A declared invariant the engine does not implement is a COMMIT FAILURE, never a
// silent pass. The alternative — ignoring an unrecognised kind — means a strategy that declares
// Permutation gets no permutation checking and no warning, which is worse than not offering the
// vocabulary at all, because it reads as protection in every review of that strategy.
//
// ON THE UNIVERSAL SET AND THE NAME `NoFloat`. .claude/rules/ledger-and-strategy.md lists the
// universal set as NoFloat, BatchNonEmpty, EntriesReferenceLiveAccounts and SeqMonotonic. Three of
// those are implemented below under their own names. `NoFloat` is not, and the omission is argued
// rather than accidental: by the time a proposal reaches this engine its amounts are
// core.Centipoints, which is an int64, so there is no float left to find — a runtime check named
// NoFloat could only assert that an int64 is an int64, and a tautology carrying a load-bearing name
// is worse than an honest gap. The ban is real and it is enforced three layers earlier: the Go type
// (core.Centipoints), the forbidigo `float32|float64` ban scoped to this package and
// internal/strategy in .golangci.yml, and the STRICT INTEGER column that rejects a REAL at write
// time. What DOES have a runtime residue is the arithmetic those layers cannot see — a zero amount
// and an overflowing sum — and both are checked below under names that say what they check.

// ErrInvariantViolated is the sentinel every invariant failure wraps, so a caller can branch on
// "the proposal was rejected" without enumerating the rules.
var ErrInvariantViolated = errors.New("invariant violated")

// InvariantError is a named, quotable failure: the rule that fired and why.
//
// Named because "the batch was rejected" is not an answer an officer can act on, and because the
// rule name is what a support conversation quotes. The Detail is written for a human reading a
// problem+json body, not for a stack trace.
type InvariantError struct {
	// Invariant is the rule that failed, in the vocabulary of
	// .claude/rules/ledger-and-strategy.md's table.
	Invariant string
	// Detail says what was wrong, with the numbers in it.
	Detail string
}

func (e *InvariantError) Error() string {
	return fmt.Sprintf("invariant %s: %s", e.Invariant, e.Detail)
}

// Is makes errors.Is(err, ErrInvariantViolated) true for every invariant failure.
func (e *InvariantError) Is(target error) bool { return target == ErrInvariantViolated }

// violation is the constructor, kept short because it appears once per rule below.
func violation(name, format string, args ...any) error {
	return &InvariantError{Invariant: name, Detail: fmt.Sprintf(format, args...)}
}

// invariantCtx is everything the engine may look at: the transaction's Queries, which pool and which
// seq this batch is being committed at, and the pool's head seq before it.
//
// It is a struct rather than five parameters because every rule below takes the same set, and
// because adding a sixth thing the engine can see should be a visible edit to one type — widening
// what an invariant may consult is a design decision, exactly as widening the strategy Ctx facade is.
type invariantCtx struct {
	q       store.Queries
	poolID  core.ULID
	seq     int64
	headSeq int64
}

// checkInvariants runs the universal set and then the proposal's declared set. It returns on the
// first failure: a batch is rejected or it is not, and enumerating every way in which a broken
// proposal is broken helps nobody.
//
// The universal pass hands the declared pass the account kinds it looked up, so that the ~40
// primary-key reads a raid-night batch costs are paid once rather than once per declared invariant.
func checkInvariants(ctx context.Context, ic invariantCtx, p strategy.BatchProposal) error {
	kinds, err := checkUniversal(ctx, ic, p)
	if err != nil {
		return err
	}

	for _, inv := range p.Invariants {
		if err := checkDeclared(ctx, ic, p, inv, kinds); err != nil {
			return err
		}
	}

	return nil
}

// checkUniversal runs the rules no strategy may waive, and returns each referenced account's kind
// ('person' or 'system') for the declared pass to reuse.
func checkUniversal(
	ctx context.Context, ic invariantCtx, p strategy.BatchProposal,
) (map[core.ULID]string, error) {
	// BatchNonEmpty. entry_count carries CHECK (entry_count > 0); catching it here names the
	// planner instead of surfacing a constraint violation from three layers down.
	if len(p.Entries) == 0 {
		return nil, violation("BatchNonEmpty",
			"the proposal has no entries; a batch that moves nothing must not be written")
	}

	// AmountsNonZero. ledger_entry carries CHECK (amount_cp <> 0). A zero entry is noise that breaks
	// entry_count reasoning, and it is nearly always a planner that computed a share of nothing and
	// wrote it anyway rather than dropping it — which is why ledger.Allocate filters zeros.
	for i, e := range p.Entries {
		if e.AmountCp == 0 {
			return nil, violation("AmountsNonZero",
				"entry %d for account %s moves 0 centipoints; drop the entry instead of writing a zero",
				i, e.AccountID)
		}
	}

	// NoAmountOverflow. The residue of the NoFloat ban that survives into runtime, argued in this
	// file's header. A wrapped sum satisfies a zero-sum check by arithmetic accident, which is the
	// one way conservation can be defeated without any individual amount looking wrong.
	if _, ok := p.NetAmountCp(); !ok {
		return nil, violation("NoAmountOverflow",
			"the %d entry amounts sum past int64; the batch's net effect is not representable", len(p.Entries))
	}

	// SeqMonotonic. The allocated seq must be exactly one past the pool's head. It cannot fail while
	// the single-writer property holds — NextPoolSeq computed both numbers from the same query —
	// so this is the tripwire for the day that property is lost, which is the day ux_batch_seq
	// starts rejecting inserts and somebody needs to know why.
	if ic.seq != ic.headSeq+1 {
		return nil, violation("SeqMonotonic",
			"batch allocated seq %d in pool %s whose head is %d; seq must be exactly head+1",
			ic.seq, ic.poolID, ic.headSeq)
	}

	// ReversalLinkage. Checked before the account lookups because it is one indexed read and they
	// are up to forty.
	if err := checkReversalLinkage(ctx, ic, p); err != nil {
		return nil, err
	}

	// EntriesReferenceLiveAccounts. The foreign key would also reject an unknown account, but as a
	// constraint violation naming a column rather than a rule naming an account — and the FK cannot
	// speak at all about an account that exists but should not receive entries, which is where this
	// rule grows when soft-delete lands in Phase 4. One lookup per DISTINCT account, so a 40-raider
	// batch costs 40 primary-key reads inside a transaction that is already the only writer.
	kinds := make(map[core.ULID]string, len(p.Entries))

	for _, id := range distinctAccounts(p.Entries) {
		row, err := ic.q.GetAccount(ctx, id.String())
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, violation("EntriesReferenceLiveAccounts",
					"entry references account %s, which does not exist", id)
			}

			return nil, fmt.Errorf("check account %s exists: %w", id, err)
		}

		kinds[id] = row.Kind
	}

	return kinds, nil
}

// checkReversalLinkage requires that reverses_batch_id is set if and only if this is a reversal, and
// that the batch it names lives in the same pool.
//
// NEITHER PROPERTY IS EXPRESSIBLE IN THE SCHEMA, which is why they are here. The self-FK proves the
// target exists; ux_batch_reverses proves a batch is reversed at most once. Nothing in the database
// can say "only a batch of kind 'reversal' may carry this pointer", and nothing can say "and the
// target must be in this pool" — so without this check the database happily accepts both mistakes.
//
// The damage from each is PERMANENT, which is what makes this worth a read inside every commit:
//
//   - An ordinary batch carrying the pointer marks its target reversed, because "is this batch
//     reversed?" is a query rather than a column (§9.2). The target renders struck through, and —
//     worse — the unique index slot is now consumed, so the REAL reversal can never be written. In
//     an append-only table there is no way to take that back.
//   - A cross-pool reversal undoes nothing, because a balance is a sum within one pool, while still
//     marking the other pool's batch reversed.
//
// A reversal of a reversal is legal and stays legal: `.claude/rules/ledger-and-strategy.md` says it
// is just another reversal, so the target's own kind is not constrained.
func checkReversalLinkage(ctx context.Context, ic invariantCtx, p strategy.BatchProposal) error {
	isReversal := p.Kind == strategy.KindReversal

	switch {
	case isReversal && p.ReversesBatchID == nil:
		return violation("ReversalLinkage",
			"batch kind is %q but it names no batch as reversed; a reversal that points at nothing "+
				"is an ordinary batch wearing the word", strategy.KindReversal)

	case !isReversal && p.ReversesBatchID != nil:
		return violation("ReversalLinkage",
			"a batch of kind %q names batch %s as reversed; only a reversal may carry that pointer, "+
				"and setting it here would both strike through %s and consume the unique slot its "+
				"real reversal needs", p.Kind, *p.ReversesBatchID, *p.ReversesBatchID)

	case !isReversal:
		return nil
	}

	target, err := ic.q.GetLedgerBatch(ctx, p.ReversesBatchID.String())
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return violation("ReversalLinkage",
				"batch %s does not exist, so there is nothing to reverse", *p.ReversesBatchID)
		}

		return fmt.Errorf("load batch %s to reverse: %w", *p.ReversesBatchID, err)
	}

	if core.ULID(target.PoolID) != ic.poolID {
		return violation("ReversalLinkage",
			"batch %s belongs to pool %s, not %s; a balance is a sum within one pool, so a "+
				"cross-pool reversal would undo nothing while still marking its target reversed",
			*p.ReversesBatchID, target.PoolID, ic.poolID)
	}

	return nil
}

// touchesBalanceKind reports whether any entry in the proposal moves the given balance kind.
func touchesBalanceKind(p strategy.BatchProposal, kind string) bool {
	for _, e := range p.Entries {
		if e.BalanceKind == kind {
			return true
		}
	}

	return false
}

// checkDeclared runs one declared invariant, or refuses because the engine cannot.
func checkDeclared(
	ctx context.Context, ic invariantCtx, p strategy.BatchProposal,
	inv strategy.Invariant, kinds map[core.ULID]string,
) error {
	// A SCOPE THAT MATCHES NOTHING IS A FAILURE, NOT A PASS, and this guard is why every rule below
	// can assume its aggregate is non-empty.
	//
	// Each scoped rule filters the batch's entries down to one balance kind. When that filter
	// matched nothing the aggregate was empty, the loop over it ran zero times, and the rule
	// returned success — so a planner declaring `dkp` while emitting `dpk` satisfied SumZero without
	// a single entry being summed, and committed a non-zero-sum batch. Every review of that strategy
	// would read the declaration and believe it was constrained. Found in review of PR 10a.
	//
	// The trade-off, stated so the next person does not have to rediscover it: a strategy whose
	// batches only SOMETIMES touch a kind must now declare its invariants conditionally rather than
	// returning one fixed set. That is a real cost and it falls on multi-kind strategies (EPGP's
	// EP/GP pair is the case). It is the right side to err on — a declared rule that quietly checks
	// nothing is worse than a rejected batch, because the rejection is visible and the silence is
	// not — but a strategy that hits it should change its declaration, not this guard.
	if inv.BalanceKind != "" && !touchesBalanceKind(p, inv.BalanceKind) {
		return violation(string(inv.Kind),
			"the invariant is scoped to balance kind %q, which no entry in this batch touches, so it "+
				"would check nothing. Declare the kind the batch actually moves, or declare the "+
				"invariant only for the batches that move it.", inv.BalanceKind)
	}

	switch inv.Kind {
	case strategy.InvariantSumZero:
		return checkSumsToZero(p, inv, "SumZero",
			"the entries for balance kind %q sum to %d, not 0; a zero-sum batch must give away exactly what it takes")

	case strategy.InvariantLargestRemainderSumsToDebit:
		// The same arithmetic as SumZero, and deliberately a separate rule. It names a different
		// MISTAKE — rounding each credit independently instead of allocating by largest remainder —
		// and an invariant failure is read by somebody trying to work out what they did wrong. The
		// two exist separately in .claude/rules/ledger-and-strategy.md's table for the same reason.
		return checkSumsToZero(p, inv, "LargestRemainderSumsToDebit",
			"the credits for balance kind %q miss the debit by %d centipoints; "+
				"round with ledger.Allocate rather than dividing each credit independently")

	case strategy.InvariantNonNegative:
		return checkNonNegative(ctx, ic, p, inv, kinds)

	case strategy.InvariantConserved,
		strategy.InvariantMonotoneNonDecreasing,
		strategy.InvariantPermutation,
		strategy.InvariantRatioPreserved:
		// Declared, vocabulary-legal, and not implemented at this phase. Conserved needs a pool-wide
		// aggregate no shipped query provides; the other three exist for epgp and suicide_kings,
		// which .claude/rules/ledger-and-strategy.md marks as conditional rather than scheduled —
		// built when a named guild asks in an issue. Refusing the commit is the honest behaviour: a
		// strategy that declares a rule nothing checks is a strategy every reviewer will believe is
		// constrained.
		return violation(string(inv.Kind),
			"the %s invariant is declared but not implemented at this phase; "+
				"the strategy that needs it must land with its checker", inv.Kind)

	default:
		return violation(string(inv.Kind), "unknown invariant kind %q", inv.Kind)
	}
}

// checkSumsToZero is the shared arithmetic behind SumZero and LargestRemainderSumsToDebit.
//
// The scope is the invariant's BalanceKind, or every kind in the batch when it is empty. Checking
// per kind rather than over the whole batch matters for a multi-kind strategy: EPGP moves EP and GP
// in one batch, and a batch whose EP and GP errors cancel would pass a whole-batch sum while both
// balances are wrong.
func checkSumsToZero(p strategy.BatchProposal, inv strategy.Invariant, name, format string) error {
	sums, err := sumByKind(p, inv.BalanceKind)
	if err != nil {
		return violation(name, "%s", err.Error())
	}

	// Sorted, so the failure message names the same kind on every run. A map range would report
	// whichever kind Go happened to visit first, and a test that asserted the message would be
	// intermittently red.
	kinds := make([]string, 0, len(sums))
	for k := range sums {
		kinds = append(kinds, k)
	}

	sort.Strings(kinds)

	for _, k := range kinds {
		if sums[k] != 0 {
			return violation(name, format, k, sums[k])
		}
	}

	return nil
}

// checkNonNegative reads each affected account's balance at the pool head and adds this batch's
// delta, requiring the result to stay at or above the declared floor.
//
// It reads at headSeq, not at this batch's seq: the batch has not been written yet, so a read at
// seq would return the same number and the delta would be double-counted the moment the ordering of
// the commit changed. Reading the head and adding the delta in Go is arithmetic whose inputs are
// both visible in the failure message, which is what an officer asking "why was my award rejected?"
// needs to see.
//
// SYSTEM ACCOUNTS ARE EXEMPT, and this is a design decision rather than a convenience. The four
// system accounts exist precisely to absorb imbalance: guild_bank is the counterparty that funds
// opening balances and pays for a solo kill, write_off swallows a rotted item's debit, residue takes
// what a split could not allocate, and import_opening is the other side of every migrated balance.
// Every one of them is negative by design and permanently so. Applying a floor of zero to them would
// make the FIRST zero-sum award against a fresh guild impossible to commit — the bank has nothing
// yet — which would then be "fixed" by nobody declaring NonNegative at all, and the guard would be
// lost for the accounts that actually need it. `Conserved(kind, total)` is the invariant that
// constrains the system accounts, and it is checked across all accounts including them.
func checkNonNegative(
	ctx context.Context, ic invariantCtx, p strategy.BatchProposal,
	inv strategy.Invariant, kinds map[core.ULID]string,
) error {
	if inv.FloorCp == nil {
		return violation("NonNegative",
			"the invariant declares no floor; a floor of zero must be stated rather than defaulted, "+
				"because 'nobody may go below zero' and 'somebody forgot' are different declarations")
	}

	floor := *inv.FloorCp

	deltas, err := sumByAccountAndKind(p, inv.BalanceKind)
	if err != nil {
		return violation("NonNegative", "%s", err.Error())
	}

	for _, k := range sortedKeys(deltas) {
		if kinds[k.accountID] == accountkinds.KindSystem {
			continue
		}

		current, err := ic.q.BalanceAsOfSeq(ctx, sqlitegen.BalanceAsOfSeqParams{
			PoolID:      ic.poolID.String(),
			AccountID:   k.accountID.String(),
			BalanceKind: k.balanceKind,
			Seq:         ic.headSeq,
		})
		if err != nil {
			return fmt.Errorf("read balance for account %s kind %q: %w", k.accountID, k.balanceKind, err)
		}

		after, ok := addCentipoints(core.Centipoints(current), deltas[k])
		if !ok {
			return violation("NonNegative",
				"account %s kind %q: balance %d plus delta %d overflows int64",
				k.accountID, k.balanceKind, current, deltas[k])
		}

		if after < floor {
			return violation("NonNegative",
				"account %s kind %q would go to %d, below the floor of %d "+
					"(balance %d, this batch %+d)",
				k.accountID, k.balanceKind, after, floor, current, deltas[k])
		}
	}

	return nil
}

// accountKind keys a per-(account, balance kind) aggregate. A struct key rather than a joined string
// so that an account id containing the separator cannot collide with another pair — ULIDs cannot,
// today, and a key whose safety depends on the alphabet of its parts is a key that breaks quietly
// the day the parts change.
type accountKind struct {
	accountID   core.ULID
	balanceKind string
}

// sumByKind totals the batch's entries per balance kind, restricted to `only` when it is non-empty.
// It reports an overflow as an error rather than wrapping, for the reason NoAmountOverflow exists.
func sumByKind(p strategy.BatchProposal, only string) (map[string]core.Centipoints, error) {
	sums := make(map[string]core.Centipoints)

	for _, e := range p.Entries {
		if only != "" && e.BalanceKind != only {
			continue
		}

		sum, ok := addCentipoints(sums[e.BalanceKind], e.AmountCp)
		if !ok {
			return nil, fmt.Errorf("the entries for balance kind %q sum past int64", e.BalanceKind)
		}

		sums[e.BalanceKind] = sum
	}

	return sums, nil
}

// sumByAccountAndKind totals the batch's entries per (account, balance kind), restricted to `only`
// when it is non-empty.
func sumByAccountAndKind(p strategy.BatchProposal, only string) (map[accountKind]core.Centipoints, error) {
	sums := make(map[accountKind]core.Centipoints)

	for _, e := range p.Entries {
		if only != "" && e.BalanceKind != only {
			continue
		}

		key := accountKind{accountID: e.AccountID, balanceKind: e.BalanceKind}

		sum, ok := addCentipoints(sums[key], e.AmountCp)
		if !ok {
			return nil, fmt.Errorf("the entries for account %s kind %q sum past int64",
				e.AccountID, e.BalanceKind)
		}

		sums[key] = sum
	}

	return sums, nil
}

// sortedKeys returns the map's keys in a stable order, so that a failure names the same account on
// every run and a statement sequence is reproducible.
func sortedKeys(m map[accountKind]core.Centipoints) []accountKind {
	keys := make([]accountKind, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}

	sort.Slice(keys, func(i, j int) bool {
		if keys[i].accountID != keys[j].accountID {
			return keys[i].accountID < keys[j].accountID
		}

		return keys[i].balanceKind < keys[j].balanceKind
	})

	return keys
}

// distinctAccounts returns the entries' account ids, deduplicated and sorted. Sorted for the same
// reason as sortedKeys: the lookups it drives are statements, and a reproducible statement order is
// what makes a statement-count budget and an EXPLAIN golden mean anything.
func distinctAccounts(entries []strategy.EntryProposal) []core.ULID {
	seen := make(map[core.ULID]struct{}, len(entries))
	ids := make([]core.ULID, 0, len(entries))

	for _, e := range entries {
		if _, ok := seen[e.AccountID]; ok {
			continue
		}

		seen[e.AccountID] = struct{}{}

		ids = append(ids, e.AccountID)
	}

	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	return ids
}

// addCentipoints adds two centipoint values, reporting overflow rather than wrapping. The one place
// in the product that adds money outside the database, alongside
// strategy.BatchProposal.NetAmountCp, which does the same for the same reason.
func addCentipoints(a, b core.Centipoints) (sum core.Centipoints, ok bool) {
	sum = a + b
	if (b > 0 && sum < a) || (b < 0 && sum > a) {
		return 0, false
	}

	return sum, true
}
