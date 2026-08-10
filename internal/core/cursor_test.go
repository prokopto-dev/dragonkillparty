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

// principalClasses are the classes the cursor tests mint under. The codec treats a class as an
// opaque string and the closed set belongs to the auth layer, so these are representative values,
// not a definition: what the tests need is that two of them are different.
var principalClasses = []core.PrincipalClass{"anonymous", "member", "officer"}

// someClass is the class used wherever a test needs a valid principal class but the specific value
// is not what is under test. There is no "unbound" cursor, so every Encode needs one.
const someClass core.PrincipalClass = "member"

// TestCursor_EncodeDecode_RoundTrip is the first property from the acceptance criteria: a cursor
// encoded and decoded is unchanged, over 200 random cursors.
func TestCursor_EncodeDecode_RoundTrip(t *testing.T) {
	t.Parallel()

	cc := newCodec(t)

	prop := func(key, id, filter, class string) bool {
		in := core.Cursor{
			Version:        1,
			Key:            core.ULID(key),
			ID:             core.ULID(id),
			Filter:         filter,
			PrincipalClass: core.PrincipalClass(class),
		}

		token, err := cc.Encode(in)
		if err != nil {
			return false
		}

		out, err := cc.Decode(token, in.PrincipalClass, filter)
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
			vs[3] = reflect.ValueOf(string(principalClasses[rng.Intn(len(principalClasses))]))
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

		// Same filter, tie-breaker and principal class on both, so the ONLY difference is the key:
		// the property is about the key's contribution to the token order, not the payload's.
		ca := core.Cursor{
			Version: 1, Key: core.ULID(keyA), ID: core.ULID(keyA),
			Filter: noFilter, PrincipalClass: someClass,
		}
		cb := core.Cursor{
			Version: 1, Key: core.ULID(keyB), ID: core.ULID(keyB),
			Filter: noFilter, PrincipalClass: someClass,
		}

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
	orig := core.Cursor{
		Version: 1, Key: core.ULID(key), ID: core.ULID(key),
		Filter: noFilter, PrincipalClass: someClass,
	}

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

			_, err := cc.Decode(bad, someClass, noFilter)
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
		Version:        1,
		Key:            core.ULID(key),
		ID:             core.ULID(key),
		Filter:         mintedUnder,
		PrincipalClass: someClass,
	})
	require.NoError(t, err)

	// Same token, same principal, different current filter.
	_, err = cc.Decode(token, someClass, requestedNow)
	require.ErrorIs(t, err, core.ErrCursorFilterMismatch)
	require.NotErrorIs(t, err, core.ErrCursorInvalid,
		"a filter mismatch is a valid cursor for a different query, not a corrupt one")

	// The same token DOES decode when the filter matches — the positive control, without which the
	// test above passes against a codec that rejects everything.
	got, err := cc.Decode(token, someClass, mintedUnder)
	require.NoError(t, err)
	require.Equal(t, mintedUnder, got.Filter)
}

// TestCursor_PrincipalClassMismatch_IsRejected is the structural boundary property: a cursor minted
// for one principal class does not decode under another, and the rejection is ErrCursorInvalid —
// which the API maps to `cursor_invalid` — never the softer, recoverable filter-mismatch code.
//
// This is the test the property hangs on. Before the class became a signed field, "a member cannot
// hand-craft a cursor that walks past a principal boundary" depended on every future handler author
// remembering to fold a principal component into the opaque filter fingerprint. Now it is a
// property of the token, and this is what proves it.
func TestCursor_PrincipalClassMismatch_IsRejected(t *testing.T) {
	t.Parallel()

	cc := newCodec(t)

	key := randomULIDStringDeterministic(5)
	filter := core.FilterFingerprint("sort=-created_at")

	const (
		mintedFor = core.PrincipalClass("officer")
		presentBy = core.PrincipalClass("member")
	)

	token, err := cc.Encode(core.Cursor{
		Version:        1,
		Key:            core.ULID(key),
		ID:             core.ULID(key),
		Filter:         filter,
		PrincipalClass: mintedFor,
	})
	require.NoError(t, err)

	// The whole point: an officer's cursor replayed by a member, with the filter left identical so
	// the ONLY thing that changed is who is asking.
	_, err = cc.Decode(token, presentBy, filter)
	require.ErrorIs(t, err, core.ErrCursorInvalid,
		"a cursor minted for another principal class must be rejected as invalid")
	require.NotErrorIs(t, err, core.ErrCursorFilterMismatch,
		"crossing a principal boundary is not a recoverable filter change — it must not leak that "+
			"the token was genuine")

	// Checked BEFORE the filter, so changing the filter too cannot downgrade the answer to the more
	// informative filter-mismatch code.
	_, err = cc.Decode(token, presentBy, core.FilterFingerprint("sort=created_at"))
	require.ErrorIs(t, err, core.ErrCursorInvalid)
	require.NotErrorIs(t, err, core.ErrCursorFilterMismatch)

	// Positive control: the same token under the class it was minted for still decodes, and carries
	// the class back out. Without this the test above passes against a codec that rejects anything.
	got, err := cc.Decode(token, mintedFor, filter)
	require.NoError(t, err)
	require.Equal(t, mintedFor, got.PrincipalClass)
}

// TestCursor_EmptyPrincipalClass_CannotBeMintedOrVerified proves there is no unbound cursor. An
// empty class must fail at BOTH ends: Encode refuses to issue one, and Decode refuses to verify
// against one, so a handler that forgets to bind its listing cannot ship an endpoint that works.
func TestCursor_EmptyPrincipalClass_CannotBeMintedOrVerified(t *testing.T) {
	t.Parallel()

	cc := newCodec(t)

	key := randomULIDStringDeterministic(6)

	_, err := cc.Encode(core.Cursor{Version: 1, Key: core.ULID(key), ID: core.ULID(key)})
	require.ErrorIs(t, err, core.ErrEmptyPrincipalClass,
		"a cursor bound to no principal class must not be mintable")

	token, err := cc.Encode(core.Cursor{
		Version: 1, Key: core.ULID(key), ID: core.ULID(key), PrincipalClass: someClass,
	})
	require.NoError(t, err)

	_, err = cc.Decode(token, "", noFilter)
	require.ErrorIs(t, err, core.ErrEmptyPrincipalClass,
		"decoding without a principal class to check against must fail loudly, not fall through")
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
	token, err := theirs.Encode(core.Cursor{
		Version: 1, Key: core.ULID(key), ID: core.ULID(key), PrincipalClass: someClass,
	})
	require.NoError(t, err)

	_, err = mine.Decode(token, someClass, noFilter)
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
	token, err := cc.Encode(core.Cursor{
		Version: 1, Key: core.ULID(ulidKey), ID: core.ULID(ulidKey), PrincipalClass: someClass,
	})
	require.NoError(t, err)

	// Mutate the caller's slice. If the codec held it by reference, verification would now fail.
	for i := range key {
		key[i] ^= 0xFF
	}

	_, err = cc.Decode(token, someClass, noFilter)
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
