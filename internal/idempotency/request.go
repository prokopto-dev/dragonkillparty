package idempotency

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"

	auditkinds "github.com/prokopto-dev/dragonkillparty/internal/audit/kinds"
	"github.com/prokopto-dev/dragonkillparty/internal/core"
)

// Key length bounds, from docs/api/idempotency-and-concurrency.md: "Keys are 16–255 opaque
// characters; use a UUIDv4 or a ULID."
//
// THE FLOOR IS THE POINT OF THE PAIR. A caller sending `1` as an idempotency key has not generated a
// key, and 16 characters is short enough to admit every generator a bot author would reach for
// (a UUIDv4 is 36, a ULID 26) while refusing a counter. The ceiling is a bound on what this table
// stores and what a unique index has to compare.
const (
	MinKeyLength = 16
	MaxKeyLength = 255
)

// Sentinel errors, in the owning package (.claude/rules/go-idioms.md). internal/api maps each to a
// published code: ErrKeyRequired to 400 idempotency_key_required, ErrKeyReused to 422
// idempotency_key_reused, ErrInFlight to 409 idempotency_key_in_flight.
var (
	// ErrKeyRequired reports an absent or unusable Idempotency-Key on an operation that requires one.
	// It covers both "not sent" and "sent but not a key", because the caller's fix is the same
	// sentence: generate one per logical operation.
	ErrKeyRequired = errors.New("idempotency key required")

	// ErrNoStore is the degraded state cmd/dkp enters when the database could not be opened at boot
	// but the process stays up so /healthz keeps answering (canonical §13). A fenced request cannot be
	// served in that state — executing it without a record is exactly the double-write this package
	// exists to prevent — so the middleware maps it to 503.
	ErrNoStore = errors.New("idempotency service has no database")

	// ErrUnknownPrincipalKind reports a Principal whose Kind is neither of internal/audit/kinds'
	// actors. It is a wiring bug rather than a caller error: the scope of a key is the principal, and
	// a principal this package cannot name has no scope to be keyed by.
	ErrUnknownPrincipalKind = errors.New("unknown principal kind")
)

// PrincipalRef renders a principal as the `principal_ref` this table is keyed by: `user:<ulid>` or
// `service_account:<ulid>`.
//
// NEVER `token:<ulid>`, and canonical §7 says so in terms. A token rotated in the middle of a retry
// sequence must still replay rather than execute a second time; a token-scoped key would silently
// break the rotation guarantee docs/api/auth-and-scopes.md publishes, and it would break it in the
// direction that writes twice.
//
// The two prefixes are internal/audit/kinds' actor vocabulary, which is also the Principal's Kind and
// role_assignment's subject_kind — the same two strings all the way down, so a ref built here names
// the same subject an audit row and a role grant do.
func PrincipalRef(kind string, id core.ULID) (string, error) {
	switch kind {
	case auditkinds.ActorUser, auditkinds.ActorServiceAccount:
	default:
		return "", fmt.Errorf("principal ref for kind %q: %w", kind, ErrUnknownPrincipalKind)
	}

	if id == "" {
		return "", fmt.Errorf("principal ref for kind %q: %w", kind, ErrUnknownPrincipalKind)
	}

	return kind + ":" + id.String(), nil
}

// ValidateKey reports whether a caller's Idempotency-Key is usable, returning it trimmed of the
// surrounding whitespace an HTTP header may carry.
//
// IT DOES NOT NORMALISE THE INSIDE. The key is opaque to this server: two keys differing in case are
// two keys, because deciding otherwise would make `A1B2…` and `a1b2…` the same logical operation for
// a client that never said they were. Only the outer whitespace goes, because that is transport
// noise rather than a decision the client made.
func ValidateKey(raw string) (string, error) {
	key := strings.TrimSpace(raw)

	switch {
	case key == "":
		return "", fmt.Errorf("idempotency key is empty: %w", ErrKeyRequired)

	case len(key) < MinKeyLength:
		return "", fmt.Errorf("idempotency key is %d characters, the minimum is %d: %w",
			len(key), MinKeyLength, ErrKeyRequired)

	case len(key) > MaxKeyLength:
		return "", fmt.Errorf("idempotency key is %d characters, the maximum is %d: %w",
			len(key), MaxKeyLength, ErrKeyRequired)
	}

	// Printable ASCII only. A key reaches a log line, an error body and an operator's terminal; a
	// control character in one of those is at best unreadable and at worst a terminal escape in
	// somebody's `docker logs`. RFC 9110 field values permit far more than this, and refusing the
	// remainder costs a bot author nothing — every generator the documentation names emits hex,
	// base32 or base64.
	for i := 0; i < len(key); i++ {
		if key[i] < 0x21 || key[i] > 0x7e {
			return "", fmt.Errorf("idempotency key holds a character that is not printable ASCII at "+
				"offset %d: %w", i, ErrKeyRequired)
		}
	}

	return key, nil
}

// Request is everything a claim is made of: who is asking, under which key, and a fingerprint of
// what they asked for.
//
// PathTemplate is the OPERATION's path (`/api/v1/raids/{raid_id}/ticks`) and Path is the request's
// (`/api/v1/raids/01JZ…/ticks`). Both are here, and RequestHash covers both, for a reason worth
// stating: canonical §7 says the stored hash covers "method + path template + body", which alone
// makes two ticks on two different raids indistinguishable when they carry the same body — so the
// same key would replay the first and the second raid would silently receive nothing. A missed tick
// is the same defect as a duplicated one with the sign flipped, and it is quieter.
type Request struct {
	PrincipalRef string
	Key          string
	Method       string
	PathTemplate string
	Path         string
	RawQuery     string
	Body         []byte
}

// RequestHash is the fingerprint a replay is checked against: sha256 over a length-prefixed encoding
// of the method, the path template, the resolved path, the query and the body.
//
// LENGTH-PREFIXED, not concatenated, for the reason guild's ETag is (internal/core/etag.go): without
// the prefixes a method of "PO" and a path of "ST/x" would hash the same as "POST" and "/x", and two
// distinct requests sharing a hash is a replay of the wrong response.
//
// THE BODY IS HASHED AS BYTES, NOT AS CANONICAL JSON. docs/api/idempotency-and-concurrency.md said
// "canonical-JSON body"; this hashes what was sent. A retrying client sends the same bytes — that is
// what a retry is — so canonicalisation would buy nothing for the case this exists to serve, while
// costing a JSON parse on every fenced request and a decision about what canonical means for a body
// that is not JSON. A client that re-serialises with different key ordering between attempts gets
// 422 idempotency_key_reused, which is loud and true rather than silent and probably fine.
func (r Request) RequestHash() []byte {
	h := sha256.New()

	writeField := func(b []byte) {
		var lenBuf [8]byte

		binary.BigEndian.PutUint64(lenBuf[:], uint64(len(b)))

		// A hash.Hash never returns an error (the interface documents it), so there is nothing to
		// handle and nothing to return it to.
		_, _ = h.Write(lenBuf[:])
		_, _ = h.Write(b)
	}

	writeField([]byte(r.Method))
	writeField([]byte(r.PathTemplate))
	writeField([]byte(r.Path))
	writeField([]byte(r.RawQuery))
	writeField(r.Body)

	return h.Sum(nil)
}

// ReadBody drains src into memory so the request can be hashed, and reports whether it exceeded max.
//
// IT MIRRORS HUMA'S OWN RULE EXACTLY (huma.go:2164, readBody): read through an io.LimitReader of max
// bytes and treat a read of exactly max as too large. Matching it rather than inventing a limit is
// the point — a middleware with its own cap would either refuse a body the handler would have
// accepted or accept one the handler then refuses, and both are a 413 nobody can explain from the
// two limits they can see. The caller passes the operation's MaxBodyBytes, which Huma defaults to
// 1 MiB and an operation may raise.
//
// The returned bytes are handed back to the handler as the request body, so nothing is consumed:
// this reads it once for both readers.
func ReadBody(src io.Reader, max int64) ([]byte, bool, error) {
	if src == nil {
		return nil, false, nil
	}

	if max <= 0 {
		body, err := io.ReadAll(src)
		if err != nil {
			return nil, false, fmt.Errorf("read request body: %w", err)
		}

		return body, false, nil
	}

	body, err := io.ReadAll(io.LimitReader(src, max))
	if err != nil {
		return nil, false, fmt.Errorf("read request body: %w", err)
	}

	return body, int64(len(body)) == max, nil
}
