# ADR-0029 — `internal/net/safehttp` is the only `*http.Client`, and it validates the connected IP

**Status:** accepted · **Date:** 2026-08-23 · **Deciders:** owner

## Context and problem statement

The product's outbound surface is much larger than "an app that does not talk to the internet"
suggests: guild-configured webhooks, the Discord notifier, avatar-by-URL import, the item icon base,
OIDC discovery and JWKS, the optional HIBP range check, the S3 backup endpoint, the update check and
the optional P99 seed importer. Every one of those takes a URL from somebody, and the host running
it is typically a home NAS or a raid PC sharing a LAN with a router admin page, a NAS UI and a Home
Assistant with no authentication at all (`docs/design/03-security.md` §1, adversary 7). An outbound
fetch that can be aimed at `192.168.1.1` or `169.254.169.254` is a pivot into that LAN carrying the
product's own network position — scored 12, the joint-highest residual risk in that document's
register.

Nothing in the repository made an outbound request when this landed. That is the reason to build the
containment now rather than later: `03-security.md` §8.1 puts `safehttp` in Phase 2 because JWKS
fetching for OIDC is the first outbound request, and "deferring the gate until after clients exist
elsewhere is exactly the grandfathering this design forbids".

Two questions had to be answered rather than assumed. **Where does validation happen** — and **what
happens to the code that already builds a client**: `dkp healthcheck`, whose loopback
`GET /healthz` is precisely the request the outbound policy denies.

## Considered options

### Where the address is validated

| Option | For | Against |
|---|---|---|
| A — Validate the URL's hostname, then fetch | Trivial; no custom transport | Rebindable by construction. The transport resolves the name AGAIN after the check, so a record answering a public address for the check and `127.0.0.1` for the connection walks straight through. This is the defect in most hand-rolled SSRF filters |
| B — Resolve, validate the answers, then dial the ADDRESS | Closes the rebinding window | Re-implements happy-eyeballs, IPv4/IPv6 selection and multi-answer fallback inside the product, and each of those is a place to get it subtly wrong. Redirects and connection reuse need the whole thing repeated |
| **C — Validate inside `net.Dialer.Control`** | The check runs on the address the socket is about to connect to. Every candidate address, every redirect hop and every new connection is separately validated, without anybody arranging for it. The dialer keeps doing the dialing | Requires a bespoke transport, and the policy is a value that has to be threaded to every caller — which is what makes the grep gate necessary rather than optional |

### What happens to `dkp healthcheck`

| Option | For | Against |
|---|---|---|
| D — Allowlist `cmd/dkp/healthcheck.go` in the gate | One line | The first "just this one file" exception, in the rule whose entire value is that it has none. The next one is an argument, not a decision |
| E — Give the outbound policy a loopback switch | No new constructor | Spells "probe this process" and "reach the LAN" as the same knob. A policy value with `AllowLoopback: true` is one struct-literal edit away from being the general client |
| **F — A separate `NewLoopback` constructor** | The exception is a named function in the package it belongs to, and what it permits is the COMPLEMENT of the outbound policy rather than a relaxation of it: a client from it can reach loopback and nothing else — not the LAN, not the internet. It cannot be quietly reused as a general client | `--url` on `dkp healthcheck` stops accepting a remote address, which is a (documented) behaviour change |

## Decision outcome

**Chosen: C and F.** `internal/net/safehttp` is the only place an `*http.Client` may be constructed;
`NET001`/`NET002` in `internal/repogate` fail `make lint-repo` on a client, a transport or a
`http.Get`/`http.DefaultClient` anywhere under `internal/` or `cmd/` outside it, with an advisory
type-aware twin in `internal/repogate/typedlaw` that also sees a dot-imported `net/http`.
`cmd/dkp/healthcheck.go` moves to `safehttp.NewLoopback` in the same change, so the gate lands with
no allowlisted file at all.

Four things about the shape are decisions rather than details:

1. **The gate bans CONSTRUCTION, not the type.** `safehttp.New` returns an `*http.Client`, so
   holding one, passing one and keeping one in a struct field is how every caller uses the package.
   A rule that fired on `*http.Client` would make the package it protects unusable, and an unusable
   rule is a waived rule.
2. **`_test.go` is exempt, and the boundary is drawn at code that ships.** Every client in a test
   here points at an `httptest` server on `127.0.0.1` — the address the policy denies, and denies
   correctly. Gating tests would mean either permitting loopback (deleting the control) or growing a
   per-file allowlist (deleting the rule).
3. **Zero redirects, for every caller.** `03-security.md` §8.1 specifies "max 3, each hop
   re-validated" for general clients and zero for webhooks. The first consumer is a JWKS fetch
   against an endpoint pinned from the discovery document, which has no business moving, so the
   redirect-following variant — with its cross-host credential stripping — is not written yet. It
   arrives with the first surface that needs it (issue #296). Every hop would be validated by the
   same dialer regardless; the hop that is never made cannot be got wrong.
4. **The denied set is a superset of the table in `03-security.md` §8.1.** Teredo (`2001::/32`),
   6to4 (`2002::/16`), both NAT64 forms and IPv4-compatible IPv6 each carry an IPv4 destination
   inside an IPv6 address, so on a host configured for any of them they are a route to `127.0.0.1`
   that does not look like one. `DKP_OUTBOUND_ALLOW_PRIVATE` relaxes the LAN classes and
   deliberately does NOT relax link-local: `169.254.169.254` is the cloud metadata service, and
   "allow my LAN" is never a request to reach it.

### Consequences

- Good, because the highest-scoring residual risk in the register now has a mechanical control
  rather than a convention, and the control is proved by a DNS stub that rebinds — the fixture
  `03-security.md` §8.1 asks for, which goes red if the `Control` hook is removed.
- Good, because the gate ships before the first outbound client exists, so there is nothing to
  grandfather and no "we will tighten it later" to schedule.
- Good, because the healthcheck exception is a named constructor with its own tests rather than a
  path in an allowlist, which means the next request for an exception has to argue for a new
  constructor and say what it may reach.
- **Bad, because `dkp healthcheck --url` no longer accepts a remote address.** It is the container
  `HEALTHCHECK`, it probes the process it runs beside, and a remote probe is `curl`'s job — but an
  operator who scripted it against another host has to change that script.
- **Bad, because a caller can still take the returned client apart.** `*http.Client` is a struct
  with a public `Transport` field. The gate stops a second client being built, not the first being
  disarmed; that one is a review rule, stated in the package doc.
- **Bad, because the policy is not yet wired to its environment variables anywhere.**
  `DKP_OUTBOUND_ALLOW_PRIVATE` and `DKP_OUTBOUND_ALLOW_CIDRS` are parsed by `safehttp.ParsePolicy`
  and documented, but nothing in `cmd/dkp` reads them until a surface needs a general client
  (issue #295). A documented variable that is read nowhere is a mild lie for exactly as long as
  that takes.
- Neutral: one more package in `internal/`, in a new `internal/net/` subtree. The name is the one
  `03-security.md` uses throughout, and the subtree is where a second outbound protocol's policy
  would go.

### Reversal cost

Low for the shape, high for the property. Deleting the gate is one entry in two catalogues; deleting
the dialer check is deleting the SSRF defence, and every alternative to it — hostname checks,
resolve-then-dial — is one of the options rejected above. Moving the healthcheck back to its own
client costs three lines and the first allowlisted exception.
