package guild

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"

	"github.com/prokopto-dev/dragonkillparty/internal/clock"
	"github.com/prokopto-dev/dragonkillparty/internal/store"
	"github.com/prokopto-dev/dragonkillparty/internal/store/sqlitegen"
)

// Sentinel errors, in the owning package (.claude/rules/go-idioms.md). Callers compare with
// errors.Is, so a wrapped error from inside store still matches, and internal/api maps each to a
// status: ErrNotFound to 404, ErrPreconditionFailed to 412.
var (
	// ErrNotFound is returned when the guild row does not exist. On a migrated database it always
	// exists, so this surfaces only against a database that was never seeded — which is a 404 the
	// handler renders as "guild not initialised", not a 500.
	ErrNotFound = errors.New("guild not found")

	// ErrPreconditionFailed is returned by Update when the caller's If-Match does not match the
	// current ETag. It carries the current representation so the handler can put it in meta.current
	// and the caller merges in one round trip (canonical §7).
	ErrPreconditionFailed = errors.New("guild precondition failed")

	// ErrNoStore is returned when the service has no store — the degraded state cmd/dkp enters when
	// the database could not be opened at boot but the process stays up so /healthz keeps answering
	// (canonical §13). The handler maps it to 503, so a guild request fails cleanly rather than
	// dereferencing a nil store.
	ErrNoStore = errors.New("guild service has no database")
)

// Guild is the domain representation of the single guild row.
//
// It is the ONE exported shape of "the guild" inside the domain (.claude/rules/go-idioms.md: one
// exported type per concept). internal/api maps it to the wire GuildDTO; the two are different
// concepts — storage-and-logic versus wire — and the boundary between them is where snake_case field
// names and the *_centipoints/_at suffixes are applied.
//
// InactiveAfterDays is a *int64 because NULL is meaningful: it is the "off" state of the inactivity
// sweep, distinct from 0 ("flag after zero days"). The pointer carries that distinction end to end.
type Guild struct {
	ID                int64
	Name              string
	Tag               string
	Timezone          string
	WeekStart         int64
	PointsLabel       string
	PointsPrecision   int64
	InactiveAfterDays *int64
	AutoSetInactive   bool
	HideInactive      bool
	CreatedAt         int64 // Micros (int64 Unix microseconds); PR 8 retypes this through core.Micros.
	UpdatedAt         int64 // Micros.
}

// UpdateInput is the set of fields a PATCH may change, each optional.
//
// A nil pointer means "absent, leave unchanged"; a non-nil pointer means "set to this value",
// including a pointer to a zero value. That is the distinction .claude/rules/api-endpoints.md
// requires of a PATCH body, carried from the wire through to here so the service can tell "clear the
// tag" (a pointer to "") from "do not touch the tag" (nil). IfMatch is the caller's ETag; Update
// compares it against the current ETag under the transaction.
type UpdateInput struct {
	IfMatch string

	Name            *string
	Tag             *string
	Timezone        *string
	WeekStart       *int64
	PointsLabel     *string
	PointsPrecision *int64
	// InactiveAfterDays is a pointer-to-pointer so the three states are all expressible: nil (absent,
	// leave unchanged), a pointer to nil (set to NULL, turn the sweep off), and a pointer to a value
	// (set to that many days). A plain *int64 could not distinguish "absent" from "set to NULL".
	InactiveAfterDays **int64
	AutoSetInactive   *bool
	HideInactive      *bool
}

// Service reads and updates the guild singleton.
//
// It holds a store and a clock and nothing else (.claude/rules/go-idioms.md): no ctx field, no
// mutable state. The clock is injected because time.Now is grep-banned outside internal/clock.
type Service struct {
	store *store.Store
	clock clock.Clock
}

// NewService builds a Service. A nil clock is a programming error the caller fixes, not a default
// this package invents — an Update that stamped updated_at from a silently-substituted clock would
// be the kind of wrong that only shows up as timestamps nobody can explain.
func NewService(st *store.Store, clk clock.Clock) *Service {
	return &Service{store: st, clock: clk}
}

// Get returns the guild singleton.
//
// It reads through store.Q() on the read pool — one statement, no transaction — because a GET is not
// a mutation. A missing row (sql.ErrNoRows) becomes ErrNotFound: on a migrated database the row
// always exists, so this is the never-seeded case, which is a 404 rather than a 500.
func (s *Service) Get(ctx context.Context) (Guild, error) {
	if s.store == nil {
		return Guild{}, fmt.Errorf("get guild: %w", ErrNoStore)
	}

	row, err := s.store.Q().GetGuild(ctx)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Guild{}, fmt.Errorf("get guild: %w", ErrNotFound)
		}

		return Guild{}, fmt.Errorf("get guild: %w", err)
	}

	return fromRow(row), nil
}

// Update applies a patch to the guild under an optimistic-concurrency check, and returns the new
// representation.
//
// The whole read-modify-write runs in one store.Tx on the single-writer pool, so no concurrent PATCH
// can interleave between the If-Match check and the write. If the caller's If-Match does not match
// the current ETag, it returns ErrPreconditionFailed carrying the CURRENT representation — the
// handler puts that in meta.current and meta.current_etag so a bot merges in one round trip. The
// handler is responsible for the 428 when If-Match is absent; by the time Update runs, an If-Match is
// present (empty string is treated as a mismatch here as a defence in depth).
func (s *Service) Update(ctx context.Context, in UpdateInput) (Guild, error) {
	if s.store == nil {
		return Guild{}, fmt.Errorf("update guild: %w", ErrNoStore)
	}

	var out Guild

	err := s.store.Tx(ctx, func(ctx context.Context, q store.Queries) error {
		row, err := q.GetGuild(ctx)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("load guild: %w", ErrNotFound)
			}

			return fmt.Errorf("load guild: %w", err)
		}

		cur := fromRow(row)

		if in.IfMatch != ETagOf(cur) {
			// The mismatch carries the current representation up to the handler.
			out = cur

			return fmt.Errorf("if-match %q does not match current: %w", in.IfMatch, ErrPreconditionFailed)
		}

		updated, err := q.UpdateGuild(ctx, in.toParams(cur, s.clock.Now().UnixMicro()))
		if err != nil {
			return fmt.Errorf("update guild: %w", err)
		}

		out = fromRow(updated)

		return nil
	})
	if err != nil {
		return out, err
	}

	return out, nil
}

// toParams merges the patch onto the current row and produces the full UpdateGuildParams the query
// takes. Every settable column is written; a field the patch leaves absent keeps the current value.
// updatedAt is the caller-supplied stamp (the injected clock's Now in Micros).
func (in UpdateInput) toParams(cur Guild, updatedAt int64) sqlitegen.UpdateGuildParams {
	p := sqlitegen.UpdateGuildParams{
		Name:              cur.Name,
		Tag:               cur.Tag,
		Timezone:          cur.Timezone,
		WeekStart:         cur.WeekStart,
		PointsLabel:       cur.PointsLabel,
		PointsPrecision:   cur.PointsPrecision,
		InactiveAfterDays: cur.InactiveAfterDays,
		AutoSetInactive:   boolToInt(cur.AutoSetInactive),
		HideInactive:      boolToInt(cur.HideInactive),
		UpdatedAt:         updatedAt,
	}

	if in.Name != nil {
		p.Name = *in.Name
	}

	if in.Tag != nil {
		p.Tag = *in.Tag
	}

	if in.Timezone != nil {
		p.Timezone = *in.Timezone
	}

	if in.WeekStart != nil {
		p.WeekStart = *in.WeekStart
	}

	if in.PointsLabel != nil {
		p.PointsLabel = *in.PointsLabel
	}

	if in.PointsPrecision != nil {
		p.PointsPrecision = *in.PointsPrecision
	}

	if in.InactiveAfterDays != nil {
		// A pointer to a (possibly nil) *int64: setting it to NULL turns the sweep off.
		p.InactiveAfterDays = *in.InactiveAfterDays
	}

	if in.AutoSetInactive != nil {
		p.AutoSetInactive = boolToInt(*in.AutoSetInactive)
	}

	if in.HideInactive != nil {
		p.HideInactive = boolToInt(*in.HideInactive)
	}

	return p
}

// fromRow maps a generated row to the domain Guild, translating the 0/1 integers SQLite stores
// booleans as back into Go bools at the package boundary.
func fromRow(row sqlitegen.Guild) Guild {
	return Guild{
		ID:                row.ID,
		Name:              row.Name,
		Tag:               row.Tag,
		Timezone:          row.Timezone,
		WeekStart:         row.WeekStart,
		PointsLabel:       row.PointsLabel,
		PointsPrecision:   row.PointsPrecision,
		InactiveAfterDays: row.InactiveAfterDays,
		AutoSetInactive:   row.AutoSetInactive == 1,
		HideInactive:      row.HideInactive == 1,
		CreatedAt:         row.CreatedAt,
		UpdatedAt:         row.UpdatedAt,
	}
}

// boolToInt maps a Go bool to the 0/1 an INTEGER boolean column stores under STRICT.
func boolToInt(b bool) int64 {
	if b {
		return 1
	}

	return 0
}

// ETagOf computes the strong ETag for a guild representation.
//
// A STRONG validator (canonical §7 and .claude/rules/api-endpoints.md), so it is quoted with no W/
// prefix, and it changes whenever ANY field of the representation changes — which includes updated_at,
// so a successful PATCH always yields a different ETag than the one the caller sent. That property is
// what makes the positive-control test meaningful: without updated_at in the hash, a no-op PATCH
// would return the same ETag and "the PATCH succeeded and the ETag changed" could not be asserted.
//
// SHA-256 over a deterministic, length-prefixed encoding of every field, rather than fmt.Sprintf of
// the struct: length-prefixing means a Name of "ab"+tag "c" cannot collide with Name "a"+tag "bc",
// which a naive concatenation would allow and which would let two distinct representations share an
// ETag. The first 16 bytes of the digest are rendered as hex — 128 bits is far more than enough to
// make an accidental collision impossible, and a shorter tag keeps the header small.
func ETagOf(g Guild) string {
	h := sha256.New()

	writeField := func(b []byte) {
		var lenBuf [8]byte
		binary.BigEndian.PutUint64(lenBuf[:], uint64(len(b)))
		_, _ = h.Write(lenBuf[:])
		_, _ = h.Write(b)
	}

	writeInt := func(n int64) {
		var buf [8]byte
		binary.BigEndian.PutUint64(buf[:], uint64(n))
		writeField(buf[:])
	}

	writeField([]byte(g.Name))
	writeField([]byte(g.Tag))
	writeField([]byte(g.Timezone))
	writeInt(g.WeekStart)
	writeField([]byte(g.PointsLabel))
	writeInt(g.PointsPrecision)

	// The nullable field: a distinct sentinel for NULL, so "unset" and any value hash differently.
	if g.InactiveAfterDays == nil {
		writeField([]byte("null"))
	} else {
		writeField([]byte(strconv.FormatInt(*g.InactiveAfterDays, 10)))
	}

	writeInt(boolToInt(g.AutoSetInactive))
	writeInt(boolToInt(g.HideInactive))
	writeInt(g.CreatedAt)
	writeInt(g.UpdatedAt)

	sum := h.Sum(nil)

	// A strong validator is a quoted string; the hex digest contains only [0-9a-f], so it needs no
	// escaping and the quotes are literal.
	return fmt.Sprintf("%q", hex.EncodeToString(sum[:16]))
}
