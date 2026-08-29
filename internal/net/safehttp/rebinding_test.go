package safehttp

import (
	"context"
	"encoding/binary"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// dnsStub is a UDP name server that answers A queries from a fixed sequence of addresses, one per
// query, repeating the last answer once the sequence runs out.
//
// It exists for one fixture — the DNS-rebinding case in 03-security.md §8.1, "a DNS stub that
// returns a public IP first and 127.0.0.1 second". Nothing in the standard library resolves a name
// to an answer a test chooses; net.Resolver's only seam is the connection it makes to a server, so
// the way to control an answer is to be the server. It speaks exactly as much DNS as Go's resolver
// asks of it: echo the header id and the question, answer A from the sequence, answer everything
// else with an empty NOERROR.
type dnsStub struct {
	conn *net.UDPConn

	mu      sync.Mutex
	answers []netip.Addr
}

// The wire constants this stub needs. DNS is a large protocol and this is the corner of it a
// resolver uses for one A lookup.
const (
	dnsHeaderLen  = 12
	dnsTypeA      = 1
	dnsClassIN    = 1
	dnsNamePtr    = 0xc0 // the two-byte compression pointer, here always to the question at offset 12
	dnsResponseHi = 0x81 // QR=1, RD=1
	dnsResponseLo = 0x80 // RA=1, RCODE=0 (NOERROR)
)

// newDNSStub starts a stub on loopback and stops it when the test ends.
func newDNSStub(t *testing.T, answers ...netip.Addr) *dnsStub {
	t.Helper()

	require.NotEmpty(t, answers, "a stub with no answers would fail every lookup for the wrong reason")

	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	require.NoError(t, err, "bind the stub name server")

	stub := &dnsStub{conn: conn, answers: answers}

	t.Cleanup(func() { _ = conn.Close() })

	go stub.serve()

	return stub
}

// serve answers queries until the connection is closed.
func (s *dnsStub) serve() {
	buf := make([]byte, 512)

	for {
		n, peer, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			return // The test ended and the connection was closed.
		}

		reply, ok := s.reply(buf[:n])
		if !ok {
			continue
		}

		_, _ = s.conn.WriteToUDP(reply, peer)
	}
}

// next pops the next answer, repeating the last one for every query after the sequence ends.
func (s *dnsStub) next() netip.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()

	addr := s.answers[0]
	if len(s.answers) > 1 {
		s.answers = s.answers[1:]
	}

	return addr
}

// reply builds the response to one query, or reports that the query was not one this stub answers.
func (s *dnsStub) reply(query []byte) ([]byte, bool) {
	if len(query) < dnsHeaderLen || binary.BigEndian.Uint16(query[4:6]) != 1 {
		return nil, false
	}

	// Walk the QNAME to find the end of the question section: length-prefixed labels, terminated by
	// a zero length, then QTYPE and QCLASS.
	end := dnsHeaderLen

	for end < len(query) {
		length := int(query[end])
		if length == 0 {
			end++

			break
		}

		if length >= dnsNamePtr {
			return nil, false // A compression pointer in a question is not a shape a resolver sends.
		}

		end += length + 1
	}

	if end+4 > len(query) {
		return nil, false
	}

	qtype := binary.BigEndian.Uint16(query[end : end+2])
	end += 4
	question := query[dnsHeaderLen:end]

	var rdata []byte

	if qtype == dnsTypeA {
		if addr := s.next(); addr.Is4() {
			rdata = addr.AsSlice()
		}
	}

	answers := uint16(0)
	if len(rdata) == 4 {
		answers = 1
	}

	reply := make([]byte, 0, dnsHeaderLen+len(question)+16)
	reply = append(reply, query[0], query[1], dnsResponseHi, dnsResponseLo)
	reply = binary.BigEndian.AppendUint16(reply, 1)       // QDCOUNT
	reply = binary.BigEndian.AppendUint16(reply, answers) // ANCOUNT
	reply = binary.BigEndian.AppendUint16(reply, 0)       // NSCOUNT
	reply = binary.BigEndian.AppendUint16(reply, 0)       // ARCOUNT
	reply = append(reply, question...)

	if answers == 1 {
		reply = append(reply, dnsNamePtr, dnsHeaderLen)
		reply = binary.BigEndian.AppendUint16(reply, dnsTypeA)
		reply = binary.BigEndian.AppendUint16(reply, dnsClassIN)
		reply = binary.BigEndian.AppendUint32(reply, 0) // TTL 0 — nothing may cache this
		reply = binary.BigEndian.AppendUint16(reply, 4) // RDLENGTH
		reply = append(reply, rdata...)
	}

	return reply, true
}

// resolver returns a net.Resolver that asks this stub and nothing else.
func (s *dnsStub) resolver() *net.Resolver {
	server := s.conn.LocalAddr().String()

	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer

			return dialer.DialContext(ctx, "udp", server)
		},
	}
}

// TestNew_RebindingHostname_IsRefusedAtDial is the reason the check lives in net.Dialer.Control and
// not next to the URL.
//
// The name answers 203.0.113.9 — a public address, which every hostname-shaped defence accepts —
// and then answers 127.0.0.1 for the connection that follows. That is DNS rebinding: it needs no
// privileged network position, only a record with a short TTL, and it defeats any design that
// resolves, checks and then hands the NAME back to http.Transport, because the transport resolves
// again.
//
// Here there is no window to rebind into. The address the socket is about to connect to is the
// address that gets checked, so the second answer is refused on its own terms — and the server on
// 127.0.0.1 never sees a request.
func TestNew_RebindingHostname_IsRefusedAtDial(t *testing.T) {
	t.Parallel()

	// A fully qualified name, root dot and all: it cannot pick up a search domain from the host's
	// resolv.conf, and .test is reserved by RFC 6761 so it can never resolve for real.
	const host = "rebind.test."

	srv, served := loopbackServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"keys":[]}`))
	})

	stub := newDNSStub(t, netip.MustParseAddr("203.0.113.9"), netip.MustParseAddr("127.0.0.1"))
	resolver := stub.resolver()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	// Resolution one: the answer a checked-then-fetched design would have approved.
	first, err := resolver.LookupHost(ctx, host)
	require.NoError(t, err, "the stub must answer the first lookup")
	require.Equal(t, []string{"203.0.113.9"}, first,
		"the fixture is only a rebinding fixture if the first answer is public")

	port := serverPort(t, srv.URL)
	target := "http://" + host + ":" + strconv.Itoa(int(port)) + "/.well-known/jwks.json"

	// Scheme and port are allowed on purpose, so that the address check is the only control left
	// that can refuse this request.
	policy := Policy{AllowHTTPHosts: []string{host}, AllowPorts: []uint16{port}}

	resp, err := get(t, newClient(policy, resolver), target)
	if resp != nil {
		_ = resp.Body.Close()
	}

	require.ErrorIs(t, err, ErrBlocked, "the rebound address must be refused")
	require.Equal(t, classLoopback, blockClass(t, err))
	require.Zero(t, served.Load(), "the rebound request reached the server")

	// Not vacuous: the same name, resolved by the same stub to the same 127.0.0.1, reaches the
	// server through a client whose policy permits loopback. The refusal above is the policy, not a
	// resolver that stopped working after the first answer.
	reachable, err := get(t, newClient(Policy{loopbackOnly: true, Timeout: 2 * time.Second}, resolver), target)
	require.NoError(t, err, "the name still resolves and the server still answers")

	defer func() { _ = reachable.Body.Close() }()

	require.Equal(t, int64(1), served.Load())
}
