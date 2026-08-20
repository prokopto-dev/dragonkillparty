package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"time"
)

// SessionCookieName is the session cookie, and this exact string is canonical §7's. internal/api's
// published `securitySchemes` block names the same constant rather than a second literal.
//
// THE `__Host-` PREFIX IS THE CONTROL, not decoration (§3.6). A browser refuses a `__Host-` cookie
// that carries a `Domain`, or a `Path` other than `/`, or arrives without `Secure` — so the name
// itself pins the cookie to the exact origin and blocks subdomain injection. That matters here
// because self-hosters park several apps under one domain, and a cookie scoped to `.guild.org` is a
// cookie any of them can set.
const SessionCookieName = "__Host-dkp_session"

// SessionSecretBytes is the cookie's payload: 32 bytes from crypto/rand, 43 characters unpadded
// base64url (§3.6).
const (
	SessionSecretBytes = 32
	SessionSecretLen   = 43
)

// The session lifetime §3.6 specifies. IDLE SLIDES ON USE, ABSOLUTE DOES NOT — a session kept warm
// by a polling tab still ends, which is the property that bounds a stolen cookie.
//
// "REMEMBER ME" (30 days idle, 90 absolute) IS NOT IMPLEMENTED HERE, deliberately: it is a checkbox
// on a login form, there is no login form yet, and honouring it needs the per-session idle window to
// be a column rather than this constant — otherwise a remembered session would slide by 14 days
// while claiming 30. The endpoint that offers the checkbox adds the column in the same change.
const (
	SessionIdleWindow     = 14 * 24 * time.Hour
	SessionAbsoluteWindow = 30 * 24 * time.Hour
)

// mintedSession is what newSessionSecret produces: the cookie value and the row material.
//
// UNEXPORTED, where a token's MintedToken is not, because Service.CreateSession is the ONLY way to
// mint a session: a session that is not written inside the transaction that read the user's epoch is
// born under a stale one. A token has no such coupling, so its mint is a pure exported function.
type mintedSession struct {
	// Secret is the cookie value. Like a token's plaintext, it exists exactly once and is never
	// stored: the database holds Hash.
	Secret string

	// Hash is the SHA-256 of the 32 secret bytes — session.token_hash.
	Hash []byte
}

// newSessionSecret mints a session cookie value.
//
// UNKEYED SHA-256 HERE, HMAC UNDER A PEPPER FOR TOKENS, and the asymmetry is deliberate rather than
// an inconsistency. A pepper defends the stored hash against an attacker who has the database and
// not the filesystem — worth having for a PAT, which is pasted into bot configs and inherited by the
// next officer, and which the design therefore treats as long-lived and widely copied. A session
// cookie lives in one browser for at most 30 days and is not written down anywhere; adding a keyed
// hash would make session verification depend on the secrets file, so a missing or unreadable
// secrets.json would log everybody out rather than only refusing bots.
//
// Either way the secret is 32 bytes from crypto/rand, so the hash is not brute-forceable: there is
// no password here to be guessed.
func newSessionSecret() (mintedSession, error) {
	raw := make([]byte, SessionSecretBytes)
	if _, err := rand.Read(raw); err != nil {
		return mintedSession{}, fmt.Errorf("generate session secret: %w", err)
	}

	sum := sha256.Sum256(raw)

	return mintedSession{
		Secret: base64.RawURLEncoding.EncodeToString(raw),
		Hash:   sum[:],
	}, nil
}

// hashSessionSecret turns a presented cookie value into the lookup key, or reports
// ErrMalformedCredential.
//
// THE DECODED BYTES ARE WHAT IS HASHED, not the base64 text, and the choice is written down because
// both readings of §3.6 are available and the two are not interchangeable: hashing the text would
// make two encodings of one secret two different rows. Tokens are hashed the same way, so the two
// credential classes agree about what "the secret" is.
//
// It never touches the database — a cookie of the wrong shape is refused before the lookup.
func hashSessionSecret(cookie string) ([]byte, error) {
	if len(cookie) != SessionSecretLen {
		return nil, fmt.Errorf("session cookie is %d characters, want %d: %w",
			len(cookie), SessionSecretLen, ErrMalformedCredential)
	}

	raw, err := base64.RawURLEncoding.DecodeString(cookie)
	if err != nil {
		return nil, fmt.Errorf("decode session cookie: %w: %w", ErrMalformedCredential, err)
	}

	if len(raw) != SessionSecretBytes {
		return nil, fmt.Errorf("session cookie is %d bytes, want %d: %w",
			len(raw), SessionSecretBytes, ErrMalformedCredential)
	}

	sum := sha256.Sum256(raw)

	return sum[:], nil
}

// sessionCookie reads the session cookie off a request.
//
// READING THIS COOKIE IS THIS PACKAGE'S JOB AND NOBODY ELSE'S (§5: "a lint rule bans reading the
// session cookie outside internal/auth"). One reader is what makes the precedence rule of §6.3
// enforceable at all — a handler that read the cookie itself would be a second authentication path,
// and the first one to disagree with this one is the divergence between the API and the UI that the
// whole design is arranged to prevent.
func sessionCookie(r *http.Request) (string, bool) {
	c, err := r.Cookie(SessionCookieName)
	if err != nil || c.Value == "" {
		return "", false
	}

	return c.Value, true
}
