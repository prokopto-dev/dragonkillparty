package auth

import (
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"fmt"
)

// RootKeyLen is the size of the one root key everything else is derived from: 32 bytes from
// crypto/rand (docs/design/03-security.md §9.1).
const RootKeyLen = 32

// The HKDF info strings of §9.1's derivation tree. ONE ROOT KEY, PER-PURPOSE SUBKEYS, NO KEY REUSE
// ACROSS CONTEXTS: one thing for an officer to back up, and a compromise of one purpose that does not
// hand over the others.
//
//	root_key ──HKDF-SHA256──┬─ info="dkp/pat-pepper/v1"      → PAT / feed-token HMAC key
//	                        ├─ info="dkp/webhook-sign/v1"    → outbound webhook HMAC key
//	                        ├─ info="dkp/totp-enc/v1"        → AES-256-GCM for TOTP seeds
//	                        ├─ info="dkp/oauth-token-enc/v1" → AES-256-GCM for stored provider tokens
//	                        ├─ info="dkp/state-sign/v1"      → OAuth state, CSRF, cursor signing
//	                        ├─ info="dkp/sse-ticket/v1"      → SSE handshake ticket signing
//	                        └─ info="dkp/backup-enc/v1"      → optional backup encryption
//
// ONLY THE FIRST IS DERIVED HERE. Deriving the other six now would be six unused keys with six
// places to get an info string subtly wrong, and each one arrives with the subsystem that uses it —
// the tree above is the map, not a to-do list. `crypto/hkdf` is standard library as of Go 1.24, so
// none of this needs a dependency.
const patPepperInfo = "dkp/pat-pepper/v1"

// PepperKIDv1 is the pepper_kid stamped on every api_token and feed_token row this binary writes.
//
// WHY A ROW RECORDS WHICH PEPPER HASHED IT (§9.1). The PAT pepper cannot be rotated in place: the
// plaintext secret is not stored, so old hashes cannot be re-peppered. The honest mechanism is that
// rotation derives a new subkey under a new kid, uses it for NEW tokens, marks the existing rows
// stale and surfaces a "rotate these N tokens" task — and that mechanism needs this column to exist
// BEFORE the rotation, not during the incident that prompted it.
const PepperKIDv1 = "v1"

// ErrUnknownPepperKID reports a token row stamped with a pepper this binary cannot derive.
//
// It FAILS CLOSED, which is the whole reason the kid is checked rather than assumed: a downgrade to
// a binary that predates a rotation would otherwise hash the presented secret with the wrong pepper,
// get a mismatch, and report "unknown credential" for every token in the guild — sending an officer
// hunting a revocation bug instead of reading one line that names the kid.
var ErrUnknownPepperKID = errors.New("unknown pepper kid")

// ErrRootKeyLength reports a root key that is not RootKeyLen bytes.
var ErrRootKeyLength = errors.New("root key must be 32 bytes")

// Keyring holds the subkeys derived from the instance root key.
//
// It holds the DERIVED pepper and not the root key itself: nothing in this package needs the root,
// and a struct that does not carry it cannot leak it into a heap dump, a panic, or a future
// String() somebody adds without thinking.
type Keyring struct {
	patPepper []byte
}

// NewKeyring derives the subkeys from a 32-byte root key.
//
// A SHORT KEY IS AN ERROR, NOT A PADDED ONE. HKDF accepts any length of input keying material
// happily, so a truncated or empty root key would produce a perfectly well-formed pepper with a
// fraction of the entropy and nothing would say so — which is precisely the failure a
// configuration mistake produces.
func NewKeyring(rootKey []byte) (*Keyring, error) {
	if len(rootKey) != RootKeyLen {
		return nil, fmt.Errorf("derive keyring from %d bytes: %w", len(rootKey), ErrRootKeyLength)
	}

	pepper, err := hkdf.Key(sha256.New, rootKey, nil, patPepperInfo, sha256.Size)
	if err != nil {
		return nil, fmt.Errorf("derive pat pepper: %w", err)
	}

	return &Keyring{patPepper: pepper}, nil
}

// TokenHash returns HMAC-SHA256(pepper(kid), secret) — what api_token.token_hash and
// feed_token.token_hash store.
//
// A KEYED HASH, NOT A PASSWORD HASH, and the difference is deliberate (ADR-0011): verification is on
// the hot path of every bot request and must be one indexed lookup plus one constant-time compare,
// where argon2id would put 19 MiB and ~100 ms in front of every call a raid bot makes. The threat it
// answers is a database leak, and the pepper — which lives outside the database — answers exactly
// that. Brute force is not in the threat model here because the secret is 32 bytes from crypto/rand,
// not a password somebody chose.
func (k *Keyring) TokenHash(kid string, secret []byte) ([]byte, error) {
	if k == nil || len(k.patPepper) == 0 {
		return nil, ErrNoPepper
	}

	if kid != PepperKIDv1 {
		return nil, fmt.Errorf("hash token under kid %q: %w", kid, ErrUnknownPepperKID)
	}

	mac := hmac.New(sha256.New, k.patPepper)
	mac.Write(secret)

	return mac.Sum(nil), nil
}

// CurrentKID is the pepper kid new credentials are minted under.
func (k *Keyring) CurrentKID() string { return PepperKIDv1 }
