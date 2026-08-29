package safehttp

import (
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// New returns the only *http.Client this repository is allowed to have.
//
// Every control in 03-security.md §8.1 is wired here, and the one that carries the weight is
// [Policy.control] on the dialer: validation happens on the address the socket is about to connect
// to, not on the hostname the caller passed. The rest — scheme, port, redirect, body cap, deadlines
// — are bounds around it.
//
// The returned client is an ordinary *http.Client so that it can be handed to any library that
// takes one. That also means a caller CAN reach in and replace its Transport, which would remove
// every control above; NET001 stops a second client being constructed, not a first one being taken
// apart. Do not do that, and do not accept a review that does.
func New(p Policy) *http.Client { return newClient(p, nil) }

// NewLoopback returns a client that can reach THIS PROCESS and nothing else: loopback addresses
// only, any port, plain http permitted.
//
// It exists for the container HEALTHCHECK (`dkp healthcheck`), which performs a loopback
// GET /healthz — a request the outbound policy denies by design, and correctly, since 127.0.0.1 is
// exactly where an SSRF wants to land. Rather than exempt that file from the gate, the exception is
// a named constructor: it is one place, it is reviewable, and what it permits is not a relaxation
// of the outbound policy but the complement of it. A client from here cannot reach the LAN or the
// internet at all, so it can never be quietly reused as a general-purpose client.
//
// 03-security.md §8.1's other deliberate exception — `dkp doctor` fetching the operator's own
// configured public URL — is a different shape (a public name that resolves to a private address)
// and wants a narrow AllowCIDRs entry, not this.
func NewLoopback(timeout time.Duration) *http.Client {
	return newClient(Policy{loopbackOnly: true, Timeout: timeout}, nil)
}

// newClient builds the client, optionally against a caller-supplied resolver.
//
// The resolver parameter is unexported and is nil everywhere but one test: the DNS-rebinding
// fixture needs a name whose answer it controls, and a stub resolver is the only seam that reaches
// Go's own resolution path. It cannot weaken anything — the Control hook below is installed either
// way, and it is the Control hook that decides.
func newClient(p Policy, resolver *net.Resolver) *http.Client {
	p = p.normalised()

	dialer := &net.Dialer{
		Timeout:   p.ConnectTimeout,
		KeepAlive: 30 * time.Second,
		Resolver:  resolver,
		Control:   p.control,
	}

	transport := &http.Transport{
		// No proxy, and no HTTP_PROXY inheritance. http.DefaultTransport reads the environment;
		// a proxy set there would receive every outbound request AND move the dial to the proxy's
		// address, which would make the Control hook validate the proxy instead of the target.
		// A deployment that genuinely needs an egress proxy gets an explicit policy field, not an
		// environment variable this package silently obeys.
		Proxy:                 nil,
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          8,
		MaxIdleConnsPerHost:   2,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: p.Timeout,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
	}

	return &http.Client{
		Transport:     &guard{policy: p, next: transport},
		Timeout:       p.Timeout,
		CheckRedirect: refuseRedirect,
	}
}

// refuseRedirect is the zero-redirect rule.
//
// Zero, for every caller, not three. Webhooks are specified as zero-redirect outright; the first
// consumer of this package is a JWKS fetch, whose endpoint is pinned from the discovery document
// and has no business moving. A redirect-following variant — the design's "max 3, each hop
// re-validated, credentials stripped on cross-host" — arrives with the first surface that needs it,
// and until then the code that would implement it does not exist to be got wrong.
//
// The refusal names no URL. The Location header is attacker-controlled text, and an error that
// carries it is an error that carries it into whatever logs or renders the failure.
func refuseRedirect(_ *http.Request, via []*http.Request) error {
	return fmt.Errorf("%w after %d hop(s)", ErrRedirect, len(via))
}

// guard is the RoundTripper half of the policy: the URL check, and the body cap on the way back.
type guard struct {
	policy Policy
	next   http.RoundTripper
}

func (g *guard) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := g.policy.checkURL(req.URL); err != nil {
		return nil, err
	}

	resp, err := g.next.RoundTrip(req)
	if err != nil {
		// Returned as-is, deliberately: http.Client wraps every RoundTrip error in a *url.Error
		// that already names the method and the URL, and a second layer of context here would say
		// the same two things twice. What must NOT happen is this error being echoed to a caller
		// verbatim — see BlockedError.
		return nil, err
	}

	resp.Body = &cappedBody{body: resp.Body, remaining: g.policy.MaxBodyBytes}

	return resp, nil
}

// cappedBody fails the read that runs past the cap instead of buffering it.
//
// A cap enforced by the CALLER — "read at most n bytes and stop" — is not a cap: whether the
// response is bounded then depends on every call site remembering. Here the body a caller receives
// cannot exceed the limit however it is read.
type cappedBody struct {
	body      io.ReadCloser
	remaining int64
}

func (b *cappedBody) Read(p []byte) (int, error) {
	if b.remaining < 0 {
		return 0, ErrBodyTooLarge
	}

	// One byte past the limit, so that a body of exactly MaxBodyBytes succeeds and the first byte
	// beyond it is what fails.
	if int64(len(p)) > b.remaining+1 {
		p = p[:b.remaining+1]
	}

	n, err := b.body.Read(p)
	b.remaining -= int64(n)

	if b.remaining < 0 {
		return n, ErrBodyTooLarge
	}

	return n, err
}

func (b *cappedBody) Close() error { return b.body.Close() }
