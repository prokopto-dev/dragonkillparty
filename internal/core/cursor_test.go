package core_test

import (
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"testing/quick"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/core"
)

// testHMACKey is the instance key the cursor tests sign with. A cursor minted with it verifies only
// against it — TestCursor_ForeignKey_IsRejected proves a different key is refused.
var testHMACKey = []byte("test-instance-hmac-key-0123456789")

// newCodec builds a codec or fails the test. Construction can only fail on an empty key, which the
// tests never pass, so a failure here is a real bug.
func newCodec(t *testing.T) *core.CursorCodec {
	t.Helper()

	cc, err := core.NewCursorCodec(testHMACKey)
	require.NoError(t, err)

	return cc
}

const noFilter = "" // the fingerprint of a request with no filters

// TestCursor_EncodeDecode_RoundTrip is the first property from the acceptance criteria: a cursor
// encoded and decoded is unchanged, over 200 random cursors.
func TestCursor_EncodeDecode_RoundTrip(t *testing.T) {
	t.Parallel()

	cc := newCodec(t)

	prop := func(key, id, filter string) bool {
		in := core.Cursor{
			Version: 1,
			Key:     core.ULID(key),
			ID:      core.ULID(id),
			Filter:  filter,
		}

		token, err := cc.Encode(in)
		if err != nil {
			return false
		}

		out, err := cc.Decode(token, filter)
		if err != nil {
			return false
		}

		return out == in
	}

	cfg := &quick.Config{
		MaxCount: propertyChecks,
		Values: func(vs []reflect.Value, rng *rand.Rand) {
			vs[0] = reflect.ValueOf(randomULIDString(rng))
			vs[1] = reflect.ValueOf(randomULIDString(rng))
			// A filter fingerprint is an opaque string; exercise both empty and non-empty.
			vs[2] = reflect.ValueOf(core.FilterFingerprint(randomULIDString(rng)))
		},
	}

	require.NoError(t, quick.Check(prop, cfg))
}

// TestCursor_OrderPreservation_ForULIDKeys is the load-bearing property named in the acceptance
// criteria: encode(a) < encode(b) iff a < b, for ULID keys, so cursors sort in a URL. Run over 200
// random pairs.
//
// This is why the codec puts the ULID key in the clear as the token's leading, fixed-width field:
// ULIDs sort lexically, so a string compare of two tokens compares their keys first. A signed opaque
// blob would randomise the order and this property would be impossible.
func TestCursor_OrderPreservation_ForULIDKeys(t *testing.T) {
	t.Parallel()

	cc := newCodec(t)

	prop := func(keyA, keyB string) bool {
		if keyA == keyB {
			return true // equal keys carry no ordering obligation
		}

		// Same filter and tie-breaker on both, so the ONLY difference is the key: the property is
		// about the key's contribution to the token order, not the payload's.
		ca := core.Cursor{Version: 1, Key: core.ULID(keyA), ID: core.ULID(keyA), Filter: noFilter}
		cb := core.Cursor{Version: 1, Key: core.ULID(keyB), ID: core.ULID(keyB), Filter: noFilter}

		ta, err := cc.Encode(ca)
		if err != nil {
			return false
		}

		tb, err := cc.Encode(cb)
		if err != nil {
			return false
		}

		keyLess := keyA < keyB
		tokenLess := ta < tb

		return keyLess == tokenLess
	}

	cfg := &quick.Config{
		MaxCount: propertyChecks,
		Values: func(vs []reflect.Value, rng *rand.Rand) {
			vs[0] = reflect.ValueOf(randomULIDString(rng))
			vs[1] = reflect.ValueOf(randomULIDString(rng))
		},
	}

	require.NoError(t, quick.Check(prop, cfg))
}

// TestCursor_TamperedToken_IsRejected proves the acceptance criterion: a tampered cursor is
// rejected with the invalid-cursor sentinel (which the API maps to `cursor_invalid`). Every field
// of the token is mutated in turn, because a signature that only covers part of the token is a
// signature over nothing where it does not reach.
func TestCursor_TamperedToken_IsRejected(t *testing.T) {
	t.Parallel()

	cc := newCodec(t)

	key := randomULIDStringDeterministic(1)
	orig := core.Cursor{Version: 1, Key: core.ULID(key), ID: core.ULID(key), Filter: noFilter}

	token, err := cc.Encode(orig)
	require.NoError(t, err)

	parts := strings.Split(token, ".")
	require.Len(t, parts, 3, "the wire token is three dot-separated parts")

	tampers := map[string]string{
		"flip a byte in the clear key": flipLastRune(parts[0]) + "." + parts[1] + "." + parts[2],
		"flip a byte in the body":      parts[0] + "." + flipLastRune(parts[1]) + "." + parts[2],
		"flip a byte in the signature": parts[0] + "." + parts[1] + "." + flipLastRune(parts[2]),
		"truncate the signature":       parts[0] + "." + parts[1] + "." + parts[2][:len(parts[2])-2],
		"drop a part":                  parts[0] + "." + parts[1],
		"extra part":                   token + ".extra",
		"empty":                        "",
		"not base64":                   parts[0] + ".!!!!.@@@@",
	}

	for name, bad := range tampers {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := cc.Decode(bad, noFilter)
			require.ErrorIs(t, err, core.ErrCursorInvalid, "tampered token must be ErrCursorInvalid")
			require.NotErrorIs(t, err, core.ErrCursorFilterMismatch,
				"tampering is not a filter mismatch — the codes must not be conflated")
		})
	}
}

// TestCursor_FilterMismatch_IsRejectedDistinctly proves the other acceptance criterion: a
// well-signed cursor whose embedded filter differs from the request's is rejected with the DISTINCT
// filter-mismatch sentinel, not silently honoured and not lumped in with tampering.
func TestCursor_FilterMismatch_IsRejectedDistinctly(t *testing.T) {
	t.Parallel()

	cc := newCodec(t)

	key := randomULIDStringDeterministic(2)
	mintedUnder := core.FilterFingerprint("state=open&sort=-created_at")
	requestedNow := core.FilterFingerprint("state=closed&sort=-created_at")

	token, err := cc.Encode(core.Cursor{
		Version: 1,
		Key:     core.ULID(key),
		ID:      core.ULID(key),
		Filter:  mintedUnder,
	})
	require.NoError(t, err)

	// Same token, different current filter.
	_, err = cc.Decode(token, requestedNow)
	require.ErrorIs(t, err, core.ErrCursorFilterMismatch)
	require.NotErrorIs(t, err, core.ErrCursorInvalid,
		"a filter mismatch is a valid cursor for a different query, not a corrupt one")

	// The same token DOES decode when the filter matches — the positive control, without which the
	// test above passes against a codec that rejects everything.
	got, err := cc.Decode(token, mintedUnder)
	require.NoError(t, err)
	require.Equal(t, mintedUnder, got.Filter)
}

// TestCursor_ForeignKey_IsRejected proves a cursor minted by one instance's key does not verify
// against another's — the whole point of signing. Without it, a cursor from any DKP instance would
// be honoured by any other.
func TestCursor_ForeignKey_IsRejected(t *testing.T) {
	t.Parallel()

	mine := newCodec(t)

	theirs, err := core.NewCursorCodec([]byte("a-completely-different-instance-key"))
	require.NoError(t, err)

	key := randomULIDStringDeterministic(3)
	token, err := theirs.Encode(core.Cursor{Version: 1, Key: core.ULID(key), ID: core.ULID(key)})
	require.NoError(t, err)

	_, err = mine.Decode(token, noFilter)
	require.ErrorIs(t, err, core.ErrCursorInvalid, "a foreign-signed cursor must not verify")
}

// TestNewCursorCodec_EmptyKey_IsAnError proves the weak-mode guard: a zero-length key cannot build a
// codec, because it would "sign" everything identically and verify anything.
func TestNewCursorCodec_EmptyKey_IsAnError(t *testing.T) {
	t.Parallel()

	_, err := core.NewCursorCodec(nil)
	require.ErrorIs(t, err, core.ErrEmptyHMACKey)

	_, err = core.NewCursorCodec([]byte{})
	require.ErrorIs(t, err, core.ErrEmptyHMACKey)
}

// TestCursor_KeyCopy_IsDefensive proves the codec copies the key it is given, so a caller mutating
// its slice afterwards cannot change how already-issued cursors verify.
func TestCursor_KeyCopy_IsDefensive(t *testing.T) {
	t.Parallel()

	key := make([]byte, len(testHMACKey))
	copy(key, testHMACKey)

	cc, err := core.NewCursorCodec(key)
	require.NoError(t, err)

	ulidKey := randomULIDStringDeterministic(4)
	token, err := cc.Encode(core.Cursor{Version: 1, Key: core.ULID(ulidKey), ID: core.ULID(ulidKey)})
	require.NoError(t, err)

	// Mutate the caller's slice. If the codec held it by reference, verification would now fail.
	for i := range key {
		key[i] ^= 0xFF
	}

	_, err = cc.Decode(token, noFilter)
	require.NoError(t, err, "mutating the caller's key slice must not affect the codec")
}

// flipLastRune returns s with its final byte incremented, staying inside the base64url alphabet by
// swapping to a definitely-different valid character. It is how the tamper cases produce a
// well-formed but altered token.
func flipLastRune(s string) string {
	if s == "" {
		return "A"
	}

	last := s[len(s)-1]

	repl := byte('A')
	if last == 'A' {
		repl = 'B'
	}

	return s[:len(s)-1] + string(repl)
}

// randomULIDStringDeterministic returns a fixed ULID for a given seed, so the non-property tests are
// reproducible without depending on the quick RNG.
func randomULIDStringDeterministic(seed int64) string {
	return randomULIDString(rand.New(rand.NewSource(seed)))
}
