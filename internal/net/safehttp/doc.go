// Package safehttp is the ONLY place in this repository where an *http.Client may be constructed.
//
// It is the SSRF containment boundary of docs/design/03-security.md §8.1, and the gate that keeps
// it the only place is NET001 in internal/repogate: an http.Client or http.Transport built, or
// http.Get / http.DefaultClient reached, anywhere under internal/ or cmd/ outside this package
// fails `make lint-repo`. The rule ships WITH the package rather than after it, because a gate
// installed once clients already exist somewhere else is a gate with a grandfathered exception —
// which is the thing 03-security.md forbids by name.
//
// # The threat
//
// The outbound surface is larger than "an app that does not talk to the internet" suggests:
// guild-configured webhooks, the Discord notifier, avatar-by-URL import, the item icon base, OIDC
// discovery and JWKS, the optional HIBP range check, the S3 backup endpoint and the update check.
// Every one of those takes a URL from somebody — an officer, an identity provider, a config file —
// and the host running it is very often a home NAS or a raid PC on the same LAN as a router admin
// page, a NAS UI and a Home Assistant instance with no authentication at all. An outbound fetch
// that can be pointed at 192.168.1.1 or at 169.254.169.254 is a pivot into that LAN with the
// product's own network position.
//
// # Why the dialer, and not the URL
//
// The check that matters runs in net.Dialer.Control, on the address the socket is about to connect
// to. Validating the hostname instead — resolve it, check the answer, then hand the NAME back to
// http.Transport — leaves a gap between the check and the connect that a second resolution fills:
// the attacker's DNS record answers with a public address for the check and 127.0.0.1 for the
// connect. That is DNS rebinding, it needs no privileged position, and it defeats every
// hostname-shaped defence that has ever been written. TestNew_RebindingHostname_IsRefusedAtDial
// drives it with a stub resolver that does exactly that.
//
// The same property makes the check total rather than best-effort: happy-eyeballs candidates, an
// A record with several answers, and any future redirect hop are all separate dials, so each is
// separately validated without anybody arranging for it.
//
// # What a client from here will not do
//
//   - reach loopback, RFC 1918, unique-local, CGNAT, link-local (169.254.169.254 included),
//     multicast, unspecified or reserved space — see [Policy.checkAddr] for the order and for
//     which of those DKP_OUTBOUND_ALLOW_PRIVATE relaxes (not link-local);
//   - speak anything but https, unless the host is on an explicit plain-http allowlist;
//   - connect to a port other than 80 or 443 without one being declared;
//   - follow a redirect — zero, for every caller (see [refuseRedirect]);
//   - carry credentials in the URL;
//   - read a response body past 1 MiB, or spend more than 10 s on a request;
//   - inherit HTTP_PROXY, HTTPS_PROXY or NO_PROXY from the environment.
//
// # What it does not cover
//
// HTTP. A raw net.Dial, a database driver dialling a DSN, or an SMTP client would each reach the
// LAN without passing through here, and none of them is gated — because none of them exists yet.
// The importer's MySQL connection is the one adjacent case and it has its own switch
// (DKP_IMPORT_ALLOW_PRIVATE, docs/operations/configuration.md). When a second outbound protocol
// appears, the policy in this package is what it should be built on, and NET001 is where the rule
// for it belongs.
//
// # Errors
//
// A refusal is a [BlockedError] wrapping [ErrBlocked], and its message is the failure CLASS only —
// "private", "link-local", "port". The address is a struct field. A raw dial error distinguishes
// "connection refused" from "no route to host", and an endpoint that echoes one back to its caller
// is a port scanner for whoever supplied the URL; 03-security.md §8.1 requires the API to answer
// 502 with the class, and this shape is what lets it.
//
// See docs/adr/0029-safehttp-is-the-only-http-client.md.
package safehttp
