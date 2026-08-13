package seed

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	accountkinds "github.com/prokopto-dev/dragonkillparty/internal/account/kinds"
	auditkinds "github.com/prokopto-dev/dragonkillparty/internal/audit/kinds"
	"github.com/prokopto-dev/dragonkillparty/internal/clock"
	"github.com/prokopto-dev/dragonkillparty/internal/core"
	"github.com/prokopto-dev/dragonkillparty/internal/ledger"
	"github.com/prokopto-dev/dragonkillparty/internal/ledger/kinds"
	"github.com/prokopto-dev/dragonkillparty/internal/store"
	"github.com/prokopto-dev/dragonkillparty/internal/store/sqlitegen"
)

// ErrPoolNotEmpty is returned when the target pool already has batches.
//
// A REFUSAL rather than a top-up, and the reason is that a seed is a dataset with a known shape.
// Appending 520k entries to a pool that already has some produces a database whose row counts match
// no profile, against which every measurement is unattributable — and the entries cannot be taken
// back out, because the ledger is append-only. The recovery is to delete the database file, which is
// a thing a developer can do to a seeded database and can never do to a guild's.
var ErrPoolNotEmpty = errors.New("pool already has batches")

// actorLabel is what the audit row records as the actor for a seeded batch. Every batch the
// generator writes carries an audit row (Commit writes one; there is no way to ask it not to), so
// this string is how an operator looking at audit_log tells generated history from real history.
const actorLabel = "seed"

// Report is what Generate wrote.
type Report struct {
	// Profile is the profile that produced this, carried so a caller can print what it asked for
	// alongside what it got.
	Profile Profile

	// Accounts, Batches and Entries are the row counts written. They equal Profile.Counts() by
	// construction — both come from the same walk — and the perf suite additionally asserts they
	// equal what the database actually contains, which is the part that is not tautological.
	Accounts int
	Batches  int
	Entries  int

	// HeadSeq is the pool's sequence head afterwards: the seq a balance read should be taken as of.
	HeadSeq int64
}

// Progress is called as a generation runs, with the batches committed so far and the total planned.
//
// A CALLBACK rather than a log line, because the two audiences want different things: `dkp seed`
// wants a line on the terminal that a developer watching a two-minute command can see, and a test
// wants silence. Routing it through slog would make the visible one depend on a log level, and the
// level a seed run needs is exactly the one that also emits twenty thousand "committed ledger batch"
// records from the layer below.
//
// nil means no reporting. It is called from the commit loop, so it must be cheap and must not block.
type Progress func(done, total int)

// Generate writes a profile's dataset through the real ledger commit path.
//
// EVERY BATCH GOES THROUGH ledger.Service.Commit, one transaction each, exactly as a raid night
// does. That is the whole design (see the package comment): the invariant engine runs, the hash
// chain links, the seq allocator allocates, and balance_snapshot is maintained by the production
// upsert rather than by anything this package wrote. It is why generating the Perf profile takes
// tens of seconds instead of a second, and why the resulting database is worth measuring.
//
// It is NOT resumable and it is NOT idempotent. A failure part way through leaves the batches that
// committed, because they committed; the pool is then non-empty and a second attempt is refused.
// Delete the database and start again. Making it resumable would mean idempotency keys on twenty
// thousand batches — a lookup per batch on the hot path of the one operation that is already the
// slow one — to serve a case whose recovery is `rm`.
func Generate(
	ctx context.Context, st *store.Store, clk clock.Clock, p Profile, progress Progress,
) (Report, error) {
	if err := p.Validate(); err != nil {
		return Report{}, err
	}

	if err := requireEmptyPool(ctx, st, p.PoolID); err != nil {
		return Report{}, err
	}

	planned, err := p.Counts()
	if err != nil {
		return Report{}, err
	}

	slog.InfoContext(ctx, "seeding",
		"profile", p.Name, "pool_id", p.PoolID, "accounts", planned.Accounts,
		"batches", planned.Batches, "entries", planned.Entries)

	if err := insertAccounts(ctx, st, clk, p); err != nil {
		return Report{}, err
	}

	svc := ledger.NewService(st, clk)
	report := Report{Profile: p, Accounts: p.Accounts}

	// Every fiftieth of the run, so a `make seed` that takes two minutes is visibly working rather
	// than apparently hung. Integer arithmetic and a floor of 1, because a profile with fewer than
	// fifty batches would otherwise divide by zero.
	step := max(planned.Batches/50, 1)

	err = p.walk(func(b batchPlan) error {
		sourceRef := b.SourceRef

		receipt, err := svc.Commit(ctx, ledger.CommitRequest{
			PoolID:    p.PoolID,
			Proposal:  b.Proposal,
			Source:    kinds.SourceSystem,
			SourceRef: &sourceRef,
			Actor:     ledger.Actor{Kind: auditkinds.ActorSystem, Label: actorLabel},
		})
		if err != nil {
			return fmt.Errorf("commit seeded batch %s: %w", sourceRef, err)
		}

		report.Batches++
		report.Entries += len(b.Proposal.Entries)
		report.HeadSeq = receipt.Seq

		if progress != nil && report.Batches%step == 0 {
			progress(report.Batches, planned.Batches)
		}

		return nil
	})
	if err != nil {
		return Report{}, err
	}

	slog.InfoContext(ctx, "seeded",
		"profile", p.Name, "accounts", report.Accounts, "batches", report.Batches,
		"entries", report.Entries, "head_seq", report.HeadSeq)

	return report, nil
}

// requireEmptyPool refuses to seed on top of existing history.
func requireEmptyPool(ctx context.Context, st *store.Store, poolID core.ULID) error {
	head, err := ledger.MaxPoolSeq(ctx, st.Q(), poolID)
	if err != nil {
		return fmt.Errorf("read the head seq of pool %s: %w", poolID, err)
	}

	if head != 0 {
		return fmt.Errorf("pool %s is at seq %d: %w", poolID, head, ErrPoolNotEmpty)
	}

	return nil
}

// insertAccounts creates the roster in ONE transaction.
//
// One transaction for 280 rows rather than one each: they are a single logical fact ("this guild has
// these members"), the write pool is a single connection, and 280 separate transactions would be 280
// WAL commits for no atomicity anybody wants. It is also the shape a real roster import takes.
//
// person_id is a deterministic synthetic id and carries no foreign key yet — `person` is a later
// table (db/schema.hcl records the deferral) — but the account_person_shape CHECK still requires a
// person account to have one, so it is supplied rather than left null.
func insertAccounts(ctx context.Context, st *store.Store, clk clock.Clock, p Profile) error {
	now := int64(core.FromTime(clk.Now()))

	return st.Tx(ctx, func(ctx context.Context, q store.Queries) error {
		for i := range p.Accounts {
			personID := p.PersonID(i).String()

			if err := q.InsertAccount(ctx, sqlitegen.InsertAccountParams{
				ID: p.AccountID(i).String(),
				// The catalogue's constant, never the literal "person": the CHECK on this column is
				// generated from internal/account/kinds, so a literal here would be a second copy of
				// a generated vocabulary and a typo would only be caught by SQLite, from inside the
				// transaction, after 279 good rows.
				Kind:      accountkinds.KindPerson,
				PersonID:  &personID,
				SystemKey: nil,
				Label:     fmt.Sprintf("Raider %03d", i),
				CreatedAt: now,
				UpdatedAt: now,
			}); err != nil {
				return fmt.Errorf("insert seeded account %d of %d: %w", i, p.Accounts, err)
			}
		}

		return nil
	})
}
