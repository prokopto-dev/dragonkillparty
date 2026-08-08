package core

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Cursor is an opaque, tamper-evident pagination position (canonical §4, api-endpoints.md).
//
// A collection response carries `next_cursor`, and the client sends it back verbatim on the next
// page. It encodes four things:
//
//   - v: the codec version, so the shape can change without misreading an old client's cursor;
//   - k: the sort key of the last item returned — a ULID, which is what gives keyset pagination its
//     stable "everything after this point" semantics even as rows are inserted;
//   - id: the tie-breaker id when the sort key is not unique (two rows with the same key);
//   - f: a fingerprint of the request's filter set, so a cursor minted for one query cannot be
//     replayed against a different one and silently return the wrong page.
//
// The token is HMAC-signed with the instance key. A tampered token is rejected (ErrCursorInvalid),
// and a token whose filter fingerprint does not match the current request is rejected distinctly
// (ErrCursorFilterMismatch) — the two map to the `cursor_invalid` and `cursor_filter_mismatch`
// codes at the API edge. internal/core does not import internal/api (the dependency runs the other
// way), so it returns sentinels and the handler translates.
type Cursor struct {
	// Version of the codec. Bumped when the wire shape below changes.
	Version int
	// Key is the sort key of the last row on the previous page: a ULID, so it is orderable.
	Key ULID
	// ID is the tie-breaker when Key is not unique across rows.
	ID ULID
	// Filter is a fingerprint of the request's filter set. Opaque here; the caller decides how to
	// derive it (FilterFingerprint is the helper) and only that its equality is meaningful.
	Filter string
}

// Sentinel errors. Callers compare with errors.Is; the API layer maps them to problem codes.
var (
	// ErrCursorInvalid is returned when a token is malformed, truncated, encoded for a different
	// instance key, or otherwise fails signature verification. It maps to `cursor_invalid` (400).
	ErrCursorInvalid = errors.New("invalid cursor")

	// ErrCursorFilterMismatch is returned when a well-signed token's embedded filter fingerprint
	// differs from the one the current request produces — a real cursor, but for a different query.
	// It maps to `cursor_filter_mismatch` (400). It is a DISTINCT error from ErrCursorInvalid because
	// the two mean different things to a bot: "your token is corrupt" versus "you changed the filter
	// mid-scroll", and the second is recoverable by restarting the scan without the old cursor.
	ErrCursorFilterMismatch = errors.New("cursor filter set does not match the request")

	// ErrEmptyHMACKey guards construction. A codec with a zero-length key would "sign" every cursor
	// identically and verify anything, which is no signature at all — so it is a construction-time
	// error, not a silent weak mode.
	ErrEmptyHMACKey = errors.New("cursor hmac key must not be empty")
)

// cursorVersion is the current codec version.
const cursorVersion = 1

// cursorSep separates the three fields of the wire token. It is `.`, which is not in the base64url
// alphabet, so it can never appear inside a field and split is unambiguous.
const cursorSep = "."

// CursorCodec encodes and decodes cursors, signing with a per-instance HMAC key.
//
// One codec is constructed at boot from the instance key and shared; it holds no mutable state, so
// it is safe for concurrent use. The key never leaves the process — a cursor exposes the last-seen
// ULID (which a client already saw) but nothing else, and the signature is what stops a client
// forging a key or a filter the server never issued.
type CursorCodec struct {
	key []byte
}

// NewCursorCodec builds a codec from the instance HMAC key.
func NewCursorCodec(hmacKey []byte) (*CursorCodec, error) {
	if len(hmacKey) == 0 {
		return nil, ErrEmptyHMACKey
	}

	// Copy so a caller mutating its slice later cannot change how existing cursors verify.
	k := make([]byte, len(hmacKey))
	copy(k, hmacKey)

	return &CursorCodec{key: k}, nil
}

// b64 is the unpadded URL-safe base64 encoding. URL-safe because cursors travel in query strings;
// unpadded because `=` is legal in a query but ugly and needless here.
var b64 = base64.RawURLEncoding

// payload is the signed inner document. Field names are single letters to keep the token short —
// this is a machine format, never read by a human, and its shape is pinned by the version tag.
type payload struct {
	V int    `json:"v"`
	K string `json:"k"`
	I string `json:"id"`
	F string `json:"f"`
}

// Encode produces the wire token for c.
//
// The token is three dot-separated parts: the ULID sort key in the clear, then the base64url of the
// signed JSON payload, then the base64url of the HMAC.
//
// The plaintext leading key is deliberate and is what makes cursors ORDER-PRESERVING: because ULIDs
// are fixed-width Crockford base32 that sort lexically, and the key is the token's leading field,
// comparing two tokens as strings compares their keys first — so encode(a) < encode(b) whenever
// a.Key < b.Key. A cursor that sorts is a cursor a caller can use in a URL and reason about, and it
// costs nothing: the key is not secret (the client just saw that row) and the signature still covers
// it, so exposing it plaintext weakens nothing.
func (cc *CursorCodec) Encode(c Cursor) (string, error) {
	if c.Version == 0 {
		c.Version = cursorVersion
	}

	p := payload{V: c.Version, K: string(c.Key), I: string(c.ID), F: c.Filter}

	body, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("marshal cursor payload: %w", err)
	}

	bodyEnc := b64.EncodeToString(body)

	// The signature covers the leading key AND the encoded body, so tampering with either — flipping
	// the visible ULID, or swapping the body for a re-signed one from another key — breaks it.
	mac := cc.sign(string(c.Key), bodyEnc)

	return string(c.Key) + cursorSep + bodyEnc + cursorSep + b64.EncodeToString(mac), nil
}

// Decode verifies and parses a token, checking it against the request's filter fingerprint.
//
// wantFilter is the fingerprint the CURRENT request produces (from FilterFingerprint). Passing it
// here rather than letting the caller compare afterwards means the filter check cannot be forgotten:
// a decoded cursor is always one whose filter matched, or an error.
func (cc *CursorCodec) Decode(token, wantFilter string) (Cursor, error) {
	parts := strings.Split(token, cursorSep)
	if len(parts) != 3 {
		return Cursor{}, fmt.Errorf("%w: expected 3 parts, got %d", ErrCursorInvalid, len(parts))
	}

	keyPart, bodyEnc, macEnc := parts[0], parts[1], parts[2]

	// Verify the signature BEFORE trusting any field. hmac.Equal is constant-time, which matters
	// because a timing oracle on cursor verification would let an attacker forge one byte at a time.
	gotMAC, err := b64.DecodeString(macEnc)
	if err != nil {
		return Cursor{}, fmt.Errorf("%w: signature is not base64url: %v", ErrCursorInvalid, err)
	}

	wantMAC := cc.sign(keyPart, bodyEnc)
	if !hmac.Equal(gotMAC, wantMAC) {
		return Cursor{}, fmt.Errorf("%w: signature mismatch", ErrCursorInvalid)
	}

	body, err := b64.DecodeString(bodyEnc)
	if err != nil {
		return Cursor{}, fmt.Errorf("%w: body is not base64url: %v", ErrCursorInvalid, err)
	}

	var p payload
	if err := json.Unmarshal(body, &p); err != nil {
		return Cursor{}, fmt.Errorf("%w: body is not valid JSON: %v", ErrCursorInvalid, err)
	}

	// The clear-text key must equal the signed key. They cannot differ without breaking the HMAC
	// (which covers keyPart), so a mismatch here means a signing bug rather than an attack — but
	// checking it keeps the plaintext prefix honest and the property tests meaningful.
	if p.K != keyPart {
		return Cursor{}, fmt.Errorf("%w: clear key does not match signed key", ErrCursorInvalid)
	}

	if p.V != cursorVersion {
		return Cursor{}, fmt.Errorf("%w: unknown codec version %d", ErrCursorInvalid, p.V)
	}

	// Filter check is LAST and its own error. A signed cursor whose filter no longer matches is not
	// corrupt — it was issued by this server — so it earns the distinct, recoverable code rather
	// than being lumped in with tampering.
	if p.F != wantFilter {
		return Cursor{}, ErrCursorFilterMismatch
	}

	return Cursor{
		Version: p.V,
		Key:     ULID(p.K),
		ID:      ULID(p.I),
		Filter:  p.F,
	}, nil
}

// sign computes HMAC-SHA256 over the domain-separated key and body.
//
// The two fields are joined with a NUL, which cannot appear in either (one is Crockford base32, the
// other base64url), so there is no way to move a byte from one field to the other and keep the same
// MAC — the classic length-extension-adjacent concatenation ambiguity.
func (cc *CursorCodec) sign(keyPart, bodyEnc string) []byte {
	h := hmac.New(sha256.New, cc.key)
	h.Write([]byte(keyPart))
	h.Write([]byte{0})
	h.Write([]byte(bodyEnc))

	return h.Sum(nil)
}

// FilterFingerprint derives a short, stable fingerprint of a request's filter set.
//
// The input is the canonical string form of whatever filters, sort and query the endpoint applied;
// the caller assembles it (sorted key=value pairs, say) and this hashes it. Callers on both the
// mint and the read side must build the string the same way — the fingerprint only means "same
// query", and it is only as good as the canonicalisation the caller feeds it.
//
// It is a plain SHA-256, not the HMAC: the fingerprint is compared for equality, not trusted as a
// secret, and it is already inside the signed payload, so an attacker cannot change it without
// breaking the signature anyway.
func FilterFingerprint(canonicalFilters string) string {
	sum := sha256.Sum256([]byte(canonicalFilters))

	return b64.EncodeToString(sum[:])
}
