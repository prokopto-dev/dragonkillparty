package authz

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"

	"github.com/prokopto-dev/dragonkillparty/internal/clock"
	"github.com/prokopto-dev/dragonkillparty/internal/store"
	"github.com/prokopto-dev/dragonkillparty/internal/store/sqlitegen"
)

// Sentinel errors, in the owning package (.claude/rules/go-idioms.md). Callers compare with
// errors.Is, so a wrapped error from inside store still matches.
var (
	// ErrNoStore is returned when the reconciler has no store. cmd/dkp enters that degraded state
	// deliberately when the database cannot be opened — /healthz must keep answering 200 whatever the
	// database is doing (canonical §13) — and this is what tells the boot path that reconciliation did
	// not happen, as distinct from happening and finding nothing.
	ErrNoStore = errors.New("authz reconciler has no database")

	// ErrMissingPermission is returned when a permission key the running binary REQUIRES does not
	// resolve to a live row after reconciliation. It is a boot failure: canonical §6 makes a divergent
	// permission list a boot failure rather than a style issue, and this is the code path that keeps
	// that sentence true.
	ErrMissingPermission = errors.New("required permission key is not in the catalogue")
)

// Reconciler projects the Go catalogue into the permission table.
//
// It holds a store and a clock and nothing else (.claude/rules/go-idioms.md): no ctx field, no
// mutable state. The clock is injected because time.Now is grep-banned outside internal/clock, and
// the timestamp it produces is the one written into orphaned_at — a value an officer reads when
// asking why a role lost a capability after a downgrade.
type Reconciler struct {
	store *store.Store
	clock clock.Clock
}

// NewReconciler builds a Reconciler.
//
// A nil clock is a programming error the caller fixes, not a default this package invents. A nil
// store is NOT: it is the degraded boot state cmd/dkp enters when DKP_DB_PATH is unusable, and
// Reconcile answers it with ErrNoStore rather than a nil dereference.
func NewReconciler(st *store.Store, clk clock.Clock) *Reconciler {
	return &Reconciler{store: st, clock: clk}
}

// Report is what one reconciliation did, for the boot log.
//
// Counts rather than rows: on a steady-state boot every number is zero and Live is the catalogue
// size, which is exactly the line an operator wants to see. The two slices are named because the keys
// matter — an orphaned key is capability a role may still hold and no code implements, and a restored
// one is that state ending.
type Report struct {
	// Live is the number of keys the running binary ships: len(Catalogue()).
	Live int

	// Inserted is the number of catalogue keys that had no row before this reconciliation. On a fresh
	// install it is the whole catalogue; afterwards it is whatever the upgrade added.
	Inserted int

	// Updated is the number of existing rows whose category, label, description, policy flags or sort
	// order changed. Wording changes land here.
	Updated int

	// Orphaned names the keys that have a row and are no longer shipped — a downgrade, or a key
	// deliberately retired. Their rows are stamped, never deleted.
	Orphaned []string

	// Restored names the keys whose orphaned_at stamp this reconciliation cleared: they are shipped
	// again, so the row is live again.
	Restored []string
}

// Reconcile writes the catalogue into the permission table and verifies that every required key
// resolves. It is the boot path canonical §6 and docs/design/01-domain-model.md §5 both describe.
//
// ONE TRANSACTION, and it covers the read as well as the writes. A half-reconciled table is a
// database whose FK to permission(key) means something different from what the binary implements,
// which is precisely the state the FK exists to make impossible; and reading outside the transaction
// would leave a window in which two instances booting against one file each decide to insert the same
// row.
//
// NOTHING IS EVER DELETED. A key with a row and no catalogue entry is stamped with orphaned_at and
// left in place. The reason is the FK: role_permission references permission(key), so a delete either
// fails against the grants that reference it or — with a cascade — silently strips capability from
// every role that held it. On a DOWNGRADE, which is when this happens, that would be permanent and
// invisible: the officer rolls forward again and the grants do not come back. Stamping keeps the
// grant, keeps referential integrity, and reverses itself on the next boot of a binary that ships the
// key again.
//
// THE REQUIRED SET IS THE ROUTE TABLE'S. Every registered operation declares an x-dkp-permission, and
// a route whose key does not resolve is an operation the middleware cannot authorize — so it is a
// boot failure rather than a 500 the first time somebody calls it. The keys arrive as an argument
// rather than being read from internal/api because this package must not import it: internal/api's
// own tests import this one, and the cycle would be immediate. cmd/dkp is the wiring point and passes
// api.DeclaredPermissions(), which has already dropped the `public` and `self` sentinels — they are
// not catalogue keys and must never become permission rows.
//
// An empty required set is legitimate and is not a skip: an instance whose routes are all public has
// nothing to require, and the catalogue is still reconciled.
func (r *Reconciler) Reconcile(ctx context.Context, required []string) (Report, error) {
	if r.store == nil {
		return Report{}, fmt.Errorf("reconcile permissions: %w", ErrNoStore)
	}

	catalogue := Catalogue()
	report := Report{Live: len(catalogue)}
	now := r.clock.Now().UnixMicro()

	err := r.store.Tx(ctx, func(ctx context.Context, q store.Queries) error {
		existing, err := existingPermissions(ctx, q)
		if err != nil {
			return err
		}

		if err := upsertCatalogue(ctx, q, existing, catalogue, &report); err != nil {
			return err
		}

		if err := orphanRetired(ctx, q, existing, catalogue, now, &report); err != nil {
			return err
		}

		return requireKeys(ctx, q, required)
	})
	if err != nil {
		return Report{}, fmt.Errorf("reconcile permissions: %w", err)
	}

	slog.InfoContext(ctx, "reconciled the permission catalogue",
		"live", report.Live, "inserted", report.Inserted, "updated", report.Updated,
		"orphaned", len(report.Orphaned), "restored", len(report.Restored), "required", len(required))

	// Loud, once per boot, and only when there is something to say. An orphaned key is a role holding
	// a capability no code implements — harmless in itself, and the first thing to look at when an
	// officer reports that a permission "disappeared" after a rollback.
	if len(report.Orphaned) > 0 {
		slog.WarnContext(ctx, "permission keys are in the database and not in this binary",
			"keys", report.Orphaned,
			"detail", "this is normal after a downgrade; the rows are marked, never deleted, and are "+
				"restored by booting a binary that ships them again")
	}

	return report, nil
}

// existingPermissions reads the whole permission table into a map keyed by permission key.
//
// The whole table, because it is the catalogue's size — fifty-eight rows — and the diff against the
// catalogue is set arithmetic that belongs in Go where it can be unit-tested, rather than in a
// sqlc.slice() over every key.
func existingPermissions(ctx context.Context, q store.Queries) (map[string]sqlitegen.Permission, error) {
	rows, err := q.ListPermissions(ctx)
	if err != nil {
		return nil, fmt.Errorf("list permissions: %w", err)
	}

	existing := make(map[string]sqlitegen.Permission, len(rows))
	for _, row := range rows {
		existing[row.Key] = row
	}

	return existing, nil
}

// upsertCatalogue writes every catalogue key whose row is absent, stale or orphaned, and counts what
// it did.
//
// It writes only what has CHANGED, so a steady-state boot issues no statement at all. That is not
// micro-optimisation: this runs inside the single write transaction every other boot step queues
// behind, and rewriting fifty-eight identical rows on every restart is fifty-eight WAL frames that
// make the boot log's "updated" count meaningless.
func upsertCatalogue(
	ctx context.Context,
	q store.Queries,
	existing map[string]sqlitegen.Permission,
	catalogue []Permission,
	report *Report,
) error {
	for _, p := range catalogue {
		row, found := existing[p.Key]

		switch {
		case !found:
			report.Inserted++
		case row.OrphanedAt != nil:
			// Shipped again. Counted as a restore rather than an update, because the two mean
			// different things to whoever reads the boot log: an update is a wording change, a restore
			// is a capability coming back.
			report.Restored = append(report.Restored, p.Key)
		case sameRow(row, p):
			continue
		default:
			report.Updated++
		}

		if err := q.UpsertPermission(ctx, p.toParams()); err != nil {
			return fmt.Errorf("upsert permission %s: %w", p.Key, err)
		}
	}

	return nil
}

// orphanRetired stamps every row the running binary no longer ships.
//
// Rows already carrying a stamp are skipped here AND guarded in the SQL, so the first timestamp
// survives: what is worth knowing is when the key stopped being shipped, not when the instance was
// last restarted.
func orphanRetired(
	ctx context.Context,
	q store.Queries,
	existing map[string]sqlitegen.Permission,
	catalogue []Permission,
	now int64,
	report *Report,
) error {
	shipped := make(map[string]struct{}, len(catalogue))
	for _, p := range catalogue {
		shipped[p.Key] = struct{}{}
	}

	// Sorted, so the boot log and the Report are stable across runs: ranging a map is randomised, and
	// a warning whose key order changes on every restart is one an operator cannot diff.
	for _, key := range slices.Sorted(maps.Keys(existing)) {
		if _, live := shipped[key]; live {
			continue
		}

		if existing[key].OrphanedAt != nil {
			// Already stamped by an earlier boot. Still reported, because the condition is still true
			// and an operator restarting the process needs the same warning they got last time.
			report.Orphaned = append(report.Orphaned, key)

			continue
		}

		stamp := now
		if err := q.OrphanPermission(ctx, sqlitegen.OrphanPermissionParams{OrphanedAt: &stamp, Key: key}); err != nil {
			return fmt.Errorf("orphan permission %s: %w", key, err)
		}

		report.Orphaned = append(report.Orphaned, key)
	}

	return nil
}

// requireKeys verifies that every key the running binary's routes declare resolves to a LIVE row.
//
// Read back from the database rather than checked against Catalogue() in memory, and the difference
// is the whole value of the check: an in-memory comparison proves the catalogue agrees with itself,
// while this proves the row the foreign key will resolve against actually exists in this officer's
// database, after the writes above, in the same transaction. A patched build, a partial upgrade or a
// migration that never ran all fail here rather than at the first request.
//
// Every missing key is collected before returning, because an operator fixing a build wants the whole
// list, not the alphabetically first one.
func requireKeys(ctx context.Context, q store.Queries, required []string) error {
	var missing []string

	for _, key := range required {
		row, err := q.GetPermission(ctx, key)

		switch {
		case errors.Is(err, sql.ErrNoRows):
			missing = append(missing, key)
		case err != nil:
			return fmt.Errorf("read permission %s: %w", key, err)
		case row.OrphanedAt != nil:
			// A row exists but the running binary does not ship the key, while a route declares it.
			// That is the same defect as a missing row and is reported as one.
			missing = append(missing, key)
		}
	}

	if len(missing) > 0 {
		return fmt.Errorf("%w: %v — a route declares each of these and internal/authz/catalogue.go "+
			"does not ship it; canonical §6 makes a divergent permission list a boot failure",
			ErrMissingPermission, missing)
	}

	return nil
}

// sameRow reports whether the stored row already says what the catalogue says.
//
// Every column except orphaned_at, which the caller has already decided about, and key, which is how
// the two were paired. Comparing all of them rather than a subset is what makes a description fix
// land without a migration and without anybody remembering to widen this function.
func sameRow(row sqlitegen.Permission, p Permission) bool {
	return row.Category == p.Category &&
		row.Label == p.Label &&
		row.Description == p.Description &&
		row.IsDangerous == boolToInt(p.IsDangerous) &&
		row.RequiresStepUp == boolToInt(p.RequiresStepUp) &&
		row.SortOrder == p.SortOrder
}

// toParams renders one catalogue entry as the upsert's arguments.
//
// orphaned_at is not among them: the statement writes NULL unconditionally, because reaching it means
// the running binary ships the key.
func (p Permission) toParams() sqlitegen.UpsertPermissionParams {
	return sqlitegen.UpsertPermissionParams{
		Key:            p.Key,
		Category:       p.Category,
		Label:          p.Label,
		Description:    p.Description,
		IsDangerous:    boolToInt(p.IsDangerous),
		RequiresStepUp: boolToInt(p.RequiresStepUp),
		SortOrder:      p.SortOrder,
	}
}

// boolToInt renders a Go bool as the INTEGER 0 or 1 canonical §8 requires of a boolean column.
// SQLite has no boolean type and STRICT forbids inventing one.
func boolToInt(b bool) int64 {
	if b {
		return 1
	}

	return 0
}
