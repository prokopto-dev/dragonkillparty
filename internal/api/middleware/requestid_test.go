package middleware

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fixedClock is a Clock that always returns the same instant.
//
// Declared here rather than imported: the middleware package needs only Now(), and a test fake is
// the one place where writing the two-line implementation beats reaching for a shared one.
type fixedClock struct{ at time.Time }

func (c fixedClock) Now() time.Time { return c.at }

// ulidPattern is Crockford base32: 26 characters, no I, L, O or U.
var ulidPattern = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`)

// capture runs one request through RequestID and reports the header and the context value the
// handler saw.
func capture(t *testing.T, inbound string) (header, fromContext string) {
	t.Helper()

	var seen string

	handler := RequestID(fixedClock{at: time.Unix(1_754_524_800, 0).UTC()},
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seen = IDFromContext(r.Context())
			w.WriteHeader(http.StatusNoContent)
		}))

	req := httptest.NewRequest(http.MethodGet, "/anything", nil)
	if inbound != "" {
		req.Header.Set(HeaderRequestID, inbound)
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	res := rec.Result()
	t.Cleanup(func() { _ = res.Body.Close() })

	return res.Header.Get(HeaderRequestID), seen
}

// TestRequestID_Absent_GeneratesAULID is half of acceptance criterion 6.
//
// "generated (ULID) when not [supplied]". A ULID rather than a UUID because it sorts by time, so a
// support request carrying one narrows the log window before anything is grepped.
func TestRequestID_Absent_GeneratesAULID(t *testing.T) {
	t.Parallel()

	header, ctxValue := capture(t, "")

	require.Regexp(t, ulidPattern, header, "the generated id is not a Crockford base32 ULID")
	require.Equal(t, header, ctxValue,
		"the handler saw a different id than the client did, so a problem body and the response "+
			"header would disagree")
}

// TestRequestID_Supplied_IsEchoed is the other half.
//
// A caller behind a proxy chain, or a bot that generates its own correlation id, keeps it — which is
// what makes the id usable to correlate across two systems rather than only within this one.
func TestRequestID_Supplied_IsEchoed(t *testing.T) {
	t.Parallel()

	header, ctxValue := capture(t, "01JZ8QKB4N7Y3F0S6M2W9D5H1T")

	require.Equal(t, "01JZ8QKB4N7Y3F0S6M2W9D5H1T", header)
	require.Equal(t, header, ctxValue)
}

// TestRequestID_GeneratesDistinctIDsUnderAFixedClock guards the entropy half.
//
// A ULID is 48 bits of timestamp and 80 bits of randomness. Under a fixed clock the timestamp half
// is constant, so if the entropy source were ever dropped — replaced by a counter, or by the
// timestamp alone — every request in the same millisecond would share an id and the support workflow
// would silently start pointing at the wrong request. Two ids from the same instant must differ.
func TestRequestID_GeneratesDistinctIDsUnderAFixedClock(t *testing.T) {
	t.Parallel()

	seen := make(map[string]bool, 64)

	for range 64 {
		id, _ := capture(t, "")
		require.False(t, seen[id], "generated a duplicate request id %q under a fixed clock", id)
		seen[id] = true
	}
}

// TestRequestID_HostileInboundValue_IsReplaced is a security boundary, not tidiness.
//
// The echoed value is written into a response header and into every log line for the request. A
// value containing CR or LF is a header-injection and log-injection primitive; an unbounded one is a
// log-flooding primitive. Each case below must be REPLACED by a generated id rather than sanitised
// in place, because a partially-cleaned attacker string is still attacker-controlled.
//
// Delete this test and sanitiseInboundID's next simplification to "trim and pass through" ships an
// injection with no failing test to stop it.
func TestRequestID_HostileInboundValue_IsReplaced(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value string
	}{
		{name: "crlf header injection", value: "abc\r\nSet-Cookie: admin=1"},
		{name: "bare newline", value: "abc\ndef"},
		{name: "carriage return", value: "abc\rdef"},
		{name: "null byte", value: "abc\x00def"},
		{name: "tab", value: "abc\tdef"},
		{name: "too long", value: string(make([]byte, maxInboundIDLen+1))},
		{name: "non ascii", value: "ідентифікатор"},
		{name: "del", value: "abc\x7fdef"},
		{name: "whitespace only", value: "   "},
		{name: "empty", value: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			header, ctxValue := capture(t, tc.value)

			require.Regexp(t, ulidPattern, header,
				"a hostile inbound id was echoed instead of replaced: %q", tc.value)
			require.Equal(t, header, ctxValue)
		})
	}
}

// TestRequestID_PlausibleForeignID_IsEchoed is the counterweight.
//
// Without it, the honest way to pass the hostile-input test above is to reject everything that is
// not a ULID — which would throw away every upstream correlation id and defeat the reason echoing
// exists. These are the shapes real proxies and tracing systems emit.
func TestRequestID_PlausibleForeignID_IsEchoed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value string
	}{
		{name: "uuid", value: "3f0d5e2a-4b8c-4d1e-9a7f-2c6b8e0d1a34"},
		{name: "w3c traceparent style", value: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"},
		{name: "cloudflare ray id", value: "8b2f1c9d4e0a7f31"},
		{name: "at the length limit", value: string(makeASCII(maxInboundIDLen))},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			header, _ := capture(t, tc.value)

			require.Equal(t, tc.value, header,
				"a legitimate upstream correlation id was replaced, which breaks cross-system tracing")
		})
	}
}

// makeASCII returns n printable ASCII bytes.
func makeASCII(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'a'
	}

	return b
}

// TestRequestID_HeaderIsSetBeforeTheHandlerWrites is a subtle ordering guarantee.
//
// Response headers are flushed when the handler first writes. If RequestID set the header after
// next.ServeHTTP, the Set would be silently dropped for every handler that wrote anything — which is
// every handler — and the id would reach the logs but never the caller. The failing request is
// exactly the one whose id someone needs.
func TestRequestID_HeaderIsSetBeforeTheHandlerWrites(t *testing.T) {
	t.Parallel()

	handler := RequestID(fixedClock{at: time.Unix(0, 0).UTC()},
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusTeapot)
			_, _ = w.Write([]byte("already written"))
		}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	res := rec.Result()
	t.Cleanup(func() { _ = res.Body.Close() })

	require.Regexp(t, ulidPattern, res.Header.Get(HeaderRequestID),
		"the request id is missing from a response the handler wrote itself")
}

// TestLogger_OutsideARequest_FallsBackToDefault keeps the logger helper safe to call anywhere.
//
// Logger is called from error paths, which is where a nil dereference is least affordable. A service
// or a test that never mounted the middleware must get a usable logger, not a panic.
func TestLogger_OutsideARequest_FallsBackToDefault(t *testing.T) {
	t.Parallel()

	require.NotNil(t, Logger(t.Context()))
}
