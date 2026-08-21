package auth

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strings"
)

// The personal-access-token wire format (docs/design/03-security.md §6.1, ADR-0011):
//
//	dkp_pat_<8-char public prefix>_<43 chars base64url of 32 random bytes>
//
// A DISTINCTIVE, GREPPABLE PREFIX IS ITSELF A CONTROL. It is what a secret scanner matches, what
// `dkp token revoke <prefix>` names, and what appears in logs and in the token list — while the
// secret half appears in none of them.
const (
	// TokenScheme opens every PAT. The two sibling classes §6.1 defines — `dkp_legacy_` for the
	// compat shim and `dkp_feed_` for path-embedded feeds — are DELIBERATELY NOT accepted by this
	// parser: a feed token in an Authorization header is a credential being used outside the single
	// purpose it was minted for, and the shim is a separate surface with its own resolution
	// (ADR-0013). Refusing them here is what keeps "single-purpose" true.
	TokenScheme = "dkp_pat_"

	// TokenPrefixBytes is the entropy behind the public prefix: 6 random bytes, which base64url
	// encodes to exactly the 8 characters §6.1 specifies. 2^48 prefixes is not a secret and does not
	// need to be — it is a row identifier — but it is far too sparse to enumerate, which is what
	// keeps the lookup from being an oracle.
	TokenPrefixBytes = 6
	TokenPrefixLen   = 8

	// TokenSecretBytes is the secret half: 32 bytes from crypto/rand, 43 characters unpadded
	// base64url. This is the only part that is ever secret and the only part that is never stored.
	TokenSecretBytes = 32
	TokenSecretLen   = 43

	// TokenLen is the whole thing: "dkp_pat_" + 8 + "_" + 43 = 60 characters.
	TokenLen = len(TokenScheme) + TokenPrefixLen + 1 + TokenSecretLen
)

// MintedToken is what NewToken produces: the one-time plaintext and the row material.
//
// DISPLAY-ONCE IS A PROPERTY OF THIS STRUCT'S LIFETIME (§6.2). Plaintext is returned in a 201 body
// with `Cache-Control: no-store` and then discarded — never stored, never emailed, never logged, and
// never recoverable, because Hash is a one-way function of it under a pepper this process holds.
type MintedToken struct {
	// Plaintext is the full `dkp_pat_…` string. THE ONLY FIELD THAT MUST NOT BE PERSISTED OR
	// LOGGED. There is no redacting wrapper type on it because the one caller that legitimately has
	// it must put it on the wire verbatim, and a type that made that awkward would be worked around.
	Plaintext string

	// Prefix is the public 8-character identifier: indexed, loggable, and what the token list shows.
	Prefix string

	// Hash is HMAC-SHA256(pepper, secret) — api_token.token_hash.
	Hash []byte

	// PepperKID is the pepper the hash was computed under — api_token.pepper_kid (§9.1).
	PepperKID string
}

// NewToken mints a personal access token.
//
// The randomness is crypto/rand and there is no seeded alternative, not even for tests: a test that
// could ask for a predictable token secret is a test that documents how to ask for one, and the
// value of this function is entirely in what it refuses to make predictable. Tests assert the SHAPE
// of what comes out and that it resolves.
func NewToken(k *Keyring) (MintedToken, error) {
	prefixRaw := make([]byte, TokenPrefixBytes)
	if _, err := rand.Read(prefixRaw); err != nil {
		return MintedToken{}, fmt.Errorf("generate token prefix: %w", err)
	}

	secret := make([]byte, TokenSecretBytes)
	if _, err := rand.Read(secret); err != nil {
		return MintedToken{}, fmt.Errorf("generate token secret: %w", err)
	}

	prefix := base64.RawURLEncoding.EncodeToString(prefixRaw)

	hash, err := k.TokenHash(k.CurrentKID(), secret)
	if err != nil {
		return MintedToken{}, fmt.Errorf("hash minted token: %w", err)
	}

	return MintedToken{
		Plaintext: TokenScheme + prefix + "_" + base64.RawURLEncoding.EncodeToString(secret),
		Prefix:    prefix,
		Hash:      hash,
		PepperKID: k.CurrentKID(),
	}, nil
}

// parsedToken is a well-formed PAT taken apart: the public half and the secret bytes.
type parsedToken struct {
	prefix string
	secret []byte
}

// parseToken splits a presented token, or reports ErrMalformedCredential.
//
// FIXED-WIDTH, NEVER strings.Split ON "_", and this is the bug that shape invites: base64url's
// alphabet CONTAINS the underscore, so a secret is free to hold several and splitting on the
// separator would carve a valid token into the wrong pieces roughly half the time. Positions are
// exact and every one of them is checked before anything is decoded.
//
// IT NEVER TOUCHES THE DATABASE. Everything here is a shape check, so a scanner firing garbage at
// the API cannot use the auth path as a query generator.
func parseToken(presented string) (parsedToken, error) {
	if len(presented) != TokenLen || !strings.HasPrefix(presented, TokenScheme) {
		return parsedToken{}, fmt.Errorf("token is not %d characters of %s…: %w",
			TokenLen, TokenScheme, ErrMalformedCredential)
	}

	rest := presented[len(TokenScheme):]

	prefix := rest[:TokenPrefixLen]
	if rest[TokenPrefixLen] != '_' {
		return parsedToken{}, fmt.Errorf("token prefix is not %d characters: %w",
			TokenPrefixLen, ErrMalformedCredential)
	}

	secret, err := base64.RawURLEncoding.DecodeString(rest[TokenPrefixLen+1:])
	if err != nil {
		return parsedToken{}, fmt.Errorf("decode token secret: %w: %w", ErrMalformedCredential, err)
	}

	if len(secret) != TokenSecretBytes {
		return parsedToken{}, fmt.Errorf("token secret is %d bytes, want %d: %w",
			len(secret), TokenSecretBytes, ErrMalformedCredential)
	}

	// The prefix must be the encoding of TokenPrefixBytes and nothing else — an 8-character run of
	// legal base64url characters that decodes to some other length is not a prefix this product
	// minted, and letting it reach the indexed lookup would widen the query's input alphabet for no
	// reason.
	if raw, decErr := base64.RawURLEncoding.DecodeString(prefix); decErr != nil || len(raw) != TokenPrefixBytes {
		return parsedToken{}, fmt.Errorf("token prefix is not %d base64url-encoded bytes: %w",
			TokenPrefixBytes, ErrMalformedCredential)
	}

	return parsedToken{prefix: prefix, secret: secret}, nil
}

// bearerToken extracts the credential from an Authorization header value.
//
// The scheme match is CASE-INSENSITIVE because RFC 7235 says the scheme token is, and a bot sending
// `authorization: bearer …` is sending a valid request that a case-sensitive comparison would refuse
// with a 401 nobody can debug from the outside.
func bearerToken(header string) (string, error) {
	const scheme = "bearer"

	name, value, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(strings.TrimSpace(name), scheme) {
		return "", fmt.Errorf("authorization scheme is not Bearer: %w", ErrMalformedCredential)
	}

	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("empty bearer credential: %w", ErrMalformedCredential)
	}

	return value, nil
}
