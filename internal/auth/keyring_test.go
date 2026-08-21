package auth_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/auth"
)

// TestKeyring_ShortRootKey_IsAnError. HKDF accepts any length of input keying material happily, so a
// truncated or empty root key would produce a perfectly well-formed pepper with a fraction of the
// entropy and nothing would say so — which is exactly what a configuration mistake produces.
func TestKeyring_ShortRootKey_IsAnError(t *testing.T) {
	t.Parallel()

	for _, size := range []int{0, 1, 16, 31, 33} {
		ring, err := auth.NewKeyring(bytes.Repeat([]byte("k"), size))
		require.Nil(t, ring)
		require.ErrorIsf(t, err, auth.ErrRootKeyLength, "a %d-byte root key was accepted", size)
	}
}

// TestKeyring_TokenHash_IsDeterministicAndKeyed is the property the whole bearer path rests on: the
// same secret hashes to the same 32 bytes under one root key, and to something else under another.
//
// THE SECOND HALF IS WHAT MAKES A DATABASE LEAK USELESS. An attacker with every api_token row and no
// secrets.json cannot produce a secret that hashes to a stored value, because the pepper is not in
// the database.
func TestKeyring_TokenHash_IsDeterministicAndKeyed(t *testing.T) {
	t.Parallel()

	first := auth.NewTestKeyring(t)

	second, err := auth.NewKeyring(bytes.Repeat([]byte("z"), auth.RootKeyLen))
	require.NoError(t, err)

	secret := []byte("the same thirty-two byte secret!")

	a, err := first.TokenHash(auth.PepperKIDv1, secret)
	require.NoError(t, err)
	require.Len(t, a, 32)

	again, err := first.TokenHash(auth.PepperKIDv1, secret)
	require.NoError(t, err)
	require.Equal(t, a, again, "the same pepper and secret must hash the same, or no token resolves twice")

	other, err := second.TokenHash(auth.PepperKIDv1, secret)
	require.NoError(t, err)
	require.NotEqual(t, a, other, "a different root key must produce a different hash")
}

// TestKeyring_UnknownPepperKID_FailsClosed. A row stamped with a kid this binary cannot derive is a
// downgrade past a pepper rotation. Hashing it under whatever pepper is to hand would report
// "unknown credential" for every token in the guild and send an officer hunting a revocation bug;
// the named error is the difference between that and one log line.
func TestKeyring_UnknownPepperKID_FailsClosed(t *testing.T) {
	t.Parallel()

	hash, err := auth.NewTestKeyring(t).TokenHash("v2", []byte("secret"))
	require.Nil(t, hash)
	require.ErrorIs(t, err, auth.ErrUnknownPepperKID)
}

// TestKeyring_NilKeyring_HasNoPepper: the spec-only construction path (a nil keyring) must refuse to
// hash rather than panic or, worse, hash under a zero key.
func TestKeyring_NilKeyring_HasNoPepper(t *testing.T) {
	t.Parallel()

	var ring *auth.Keyring

	hash, err := ring.TokenHash(auth.PepperKIDv1, []byte("secret"))
	require.Nil(t, hash)
	require.ErrorIs(t, err, auth.ErrNoPepper)
}

// TestNewToken_SecretHalf_ContainsUnderscores is the regression test for the parser hazard, and it
// exists because the first draft of this file fell into it.
//
// base64url's alphabet contains `_`, so roughly HALF of all minted tokens carry one inside the
// secret. A parser that split on the last (or the first) underscore would therefore mis-parse about
// half of every guild's tokens — and the failure is a lookup that misses, which looks exactly like a
// wrong token rather than like a bug. internal/auth parses by FIXED OFFSET for that reason, and this
// test proves the hazard is real rather than theoretical.
//
// Fifty mints: the chance that none of them contains an underscore is (63/64)^(43*50), which is
// about 1e-15. This is not a flaky test; it is a certainty with a decimal point.
func TestNewToken_SecretHalf_ContainsUnderscores(t *testing.T) {
	t.Parallel()

	keys := auth.NewTestKeyring(t)
	offset := len(auth.TokenScheme) + auth.TokenPrefixLen + 1

	var withUnderscore, misparsed int

	for range 50 {
		minted, err := auth.NewToken(keys)
		require.NoError(t, err)

		secret := minted.Plaintext[offset:]
		require.Len(t, secret, auth.TokenSecretLen,
			"the fixed-offset split must always yield the whole secret")

		if strings.Contains(secret, "_") {
			withUnderscore++
		}

		// What a delimiter-splitting parser would have produced.
		if len(minted.Plaintext[strings.LastIndex(minted.Plaintext, "_")+1:]) != auth.TokenSecretLen {
			misparsed++
		}
	}

	require.Positive(t, withUnderscore,
		"no minted secret contained an underscore in fifty tries — the encoding changed")
	require.Equal(t, withUnderscore, misparsed,
		"every token whose secret holds an underscore is exactly one a delimiter split gets wrong")
}
