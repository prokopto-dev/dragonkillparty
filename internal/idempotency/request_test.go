package idempotency_test

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/idempotency"
)

// TestValidateKey_AcceptsWhatTheDocumentationTellsBotAuthorsToSend. docs/api says "16–255 opaque
// characters; use a UUIDv4 or a ULID", so both of those and the shapes around them must pass — a
// validator that refused what the published guide recommends is a validator nobody can satisfy.
func TestValidateKey_AcceptsWhatTheDocumentationTellsBotAuthorsToSend(t *testing.T) {
	t.Parallel()

	for _, key := range []string{
		"01JZ0IDEMPOTENCYKEY0000001",                    // a ULID
		"3f8c1d2e-4b5a-6c7d-8e9f-0a1b2c3d4e5f",          // a UUIDv4
		strings.Repeat("a", idempotency.MinKeyLength),   // exactly the floor
		strings.Repeat("a", idempotency.MaxKeyLength),   // exactly the ceiling
		"tick:raid-01JZ0RAID000000000000001:2026-08-23", // a bot's own scheme
	} {
		got, err := idempotency.ValidateKey(key)
		require.NoError(t, err, "%q is a key the published guidance produces", key)
		require.Equal(t, key, got)
	}
}

// TestValidateKey_TrimsTransportWhitespaceAndNothingElse.
//
// The outer whitespace is transport noise; the INSIDE is not normalised, and that is deliberate. Two
// keys differing in case are two keys, because deciding otherwise would make `A1B2…` and `a1b2…` the
// same logical operation for a client that never said they were.
func TestValidateKey_TrimsTransportWhitespaceAndNothingElse(t *testing.T) {
	t.Parallel()

	got, err := idempotency.ValidateKey("  01JZ0IDEMPOTENCYKEY0000001\t")
	require.NoError(t, err)
	require.Equal(t, "01JZ0IDEMPOTENCYKEY0000001", got)

	upper, err := idempotency.ValidateKey("ABCDEFGHIJKLMNOP")
	require.NoError(t, err)

	lower, err := idempotency.ValidateKey("abcdefghijklmnop")
	require.NoError(t, err)

	require.NotEqual(t, upper, lower, "case is not normalised: the key is opaque to this server")
}

// TestValidateKey_RefusesWhatIsNotAKey. Each row is a different client bug and each gets the same
// answer, because the fix is the same sentence: generate one per logical operation.
func TestValidateKey_RefusesWhatIsNotAKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		key  string
	}{
		{name: "absent", key: ""},
		{name: "whitespace only", key: "   "},
		{name: "a counter", key: "1"},
		{name: "one under the floor", key: strings.Repeat("a", idempotency.MinKeyLength-1)},
		{name: "one over the ceiling", key: strings.Repeat("a", idempotency.MaxKeyLength+1)},
		{name: "a control character", key: "01JZ0IDEMPOTENCY\x00KEY0001"},
		{name: "a terminal escape", key: "01JZ0IDEMPOT\x1b[31mENCYKEY01"},
		{name: "a newline", key: "01JZ0IDEMPOTENCY\nKEY0001"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := idempotency.ValidateKey(tt.key)
			require.ErrorIs(t, err, idempotency.ErrKeyRequired)
		})
	}
}

// TestRequestHash_IsDeterministicAndCoversEveryComponent. A component missing from the hash is a
// difference a replay cannot see, and a replay that cannot see a difference answers the wrong
// request with the right status.
func TestRequestHash_IsDeterministicAndCoversEveryComponent(t *testing.T) {
	t.Parallel()

	base := idempotency.Request{
		Method:       http.MethodPost,
		PathTemplate: "/api/v1/raids/{raid_id}/ticks",
		Path:         "/api/v1/raids/01JZ0RAID000000000000001/ticks",
		RawQuery:     "",
		Body:         []byte(`{"ticks":[]}`),
	}

	require.Equal(t, base.RequestHash(), base.RequestHash(), "the hash must be deterministic")

	tests := []struct {
		name   string
		mutate func(idempotency.Request) idempotency.Request
	}{
		{
			name: "method",
			mutate: func(r idempotency.Request) idempotency.Request {
				r.Method = http.MethodPut

				return r
			},
		},
		{
			name: "path template",
			mutate: func(r idempotency.Request) idempotency.Request {
				r.PathTemplate = "/api/v1/raids/{raid_id}/finalize"

				return r
			},
		},
		{
			name: "resolved path",
			mutate: func(r idempotency.Request) idempotency.Request {
				r.Path = "/api/v1/raids/01JZ0RAID000000000000002/ticks"

				return r
			},
		},
		{
			name: "query",
			mutate: func(r idempotency.Request) idempotency.Request {
				r.RawQuery = "dry_run=true"

				return r
			},
		},
		{
			name: "body",
			mutate: func(r idempotency.Request) idempotency.Request {
				r.Body = []byte(`{"ticks":[{"value_centipoints":100}]}`)

				return r
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.NotEqual(t, base.RequestHash(), tt.mutate(base).RequestHash(),
				"a change to the %s did not change the hash", tt.name)
		})
	}
}

// TestRequestHash_FieldBoundaries_AreEncoded is the length-prefix property, and the reason the hash
// is not a concatenation: "PO" + "ST/x" and "POST" + "/x" are the same byte stream, and two distinct
// requests sharing a fingerprint is a replay of the wrong response.
func TestRequestHash_FieldBoundaries_AreEncoded(t *testing.T) {
	t.Parallel()

	left := idempotency.Request{Method: "PO", PathTemplate: "ST/x"}
	right := idempotency.Request{Method: "POST", PathTemplate: "/x"}

	require.NotEqual(t, left.RequestHash(), right.RequestHash())
}

// TestRequestHash_IgnoresThePrincipalAndTheKey. Those two are the record's KEY, not its fingerprint:
// folding them into the hash would make every record's hash unique and the reused-key check
// vacuous, because no two requests could ever collide.
func TestRequestHash_IgnoresThePrincipalAndTheKey(t *testing.T) {
	t.Parallel()

	a := idempotency.Request{PrincipalRef: "user:a", Key: "k1", Method: http.MethodPost}
	b := idempotency.Request{PrincipalRef: "user:b", Key: "k2", Method: http.MethodPost}

	require.Equal(t, a.RequestHash(), b.RequestHash())
}

// TestReadBody_MirrorsHumasOwnLimit. The middleware reads the body to hash it and the handler reads
// it again; if the two limits differed, one of them would refuse a body the other accepted and the
// resulting 413 would be explicable from neither.
//
// Huma treats a read of EXACTLY the limit as too large (huma.go:2164), because it cannot tell a body
// of exactly n bytes from a longer one truncated at n. This does the same.
func TestReadBody_MirrorsHumasOwnLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		body         string
		limit        int64
		wantTooLarge bool
		want         string
	}{
		{name: "under the limit", body: "abc", limit: 8, want: "abc"},
		{name: "one under the limit", body: "abcdefg", limit: 8, want: "abcdefg"},
		{name: "exactly the limit", body: "abcdefgh", limit: 8, wantTooLarge: true},
		{name: "over the limit", body: "abcdefghij", limit: 8, wantTooLarge: true},
		{name: "empty", body: "", limit: 8, want: ""},
		{name: "no limit", body: "abcdefghij", limit: 0, want: "abcdefghij"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			body, tooLarge, err := idempotency.ReadBody(strings.NewReader(tt.body), tt.limit)
			require.NoError(t, err)
			require.Equal(t, tt.wantTooLarge, tooLarge)

			if !tt.wantTooLarge {
				require.Equal(t, tt.want, string(body))
			}
		})
	}
}

// TestReadBody_NilReader_IsAnEmptyBody. A GET-shaped request that somehow reaches a fenced operation
// has no body, and that is not a fault: net/http gives a nil Body on some request shapes and the
// hash of an empty body is a perfectly good fingerprint.
func TestReadBody_NilReader_IsAnEmptyBody(t *testing.T) {
	t.Parallel()

	body, tooLarge, err := idempotency.ReadBody(nil, 1024)
	require.NoError(t, err)
	require.False(t, tooLarge)
	require.Empty(t, body)
}

// TestReadBody_ReadFailure_IsReported. A connection that goes away mid-request is not a body of zero
// bytes: hashing an empty body there would claim a key for a request nobody made.
func TestReadBody_ReadFailure_IsReported(t *testing.T) {
	t.Parallel()

	want := errors.New("connection reset")

	_, _, err := idempotency.ReadBody(failingReader{err: want}, 1024)
	require.ErrorIs(t, err, want)
}

// failingReader fails on the first read.
type failingReader struct{ err error }

func (f failingReader) Read([]byte) (int, error) { return 0, f.err }

var _ io.Reader = failingReader{}
