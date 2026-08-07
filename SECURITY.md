# Security policy

> **Pre-1.0.** There is no released software yet, so there is nothing deployed to attack. This
> policy is published now because the design, the schema, and the threat model are public and worth
> reviewing early — and because a policy written after the first report is written badly.

## Reporting a vulnerability

**Do not open a public issue, a discussion, or a pull request.** Do not post it in Discord.

| Route | How |
|---|---|
| **Preferred** | GitHub Private Vulnerability Reporting — the repository's **Security → Advisories → Report a vulnerability** button. It creates a private fork we can patch in. |
| Email | `security@dragonkillparty.example` *(placeholder; replace before the repository is published)*. PGP key published at `/.well-known/security.txt` on the project site. |

There is **no bug bounty.** This is a volunteer project run by people with day jobs, and pretending
otherwise wastes your time. Credit in the advisory is given by default; anonymity on request.

**A useful report contains:** the version or commit, how the instance is deployed (container, bare
binary, behind which reverse proxy), the affected endpoint or code path, what an attacker gains, and
a reproduction — a `curl` sequence beats a paragraph. If you have a patch, say so; we will usually
take it under the same [DCO](CONTRIBUTING.md#developer-certificate-of-origin) as any other change.

## Response commitments

Realistic numbers for a small volunteer team. If we miss one, chase us — open a public issue saying
only "awaiting a response on a private report, sent <date>", with no details.

| Stage | Commitment |
|---|---|
| Acknowledgement | 3 business days |
| Triage, severity, and a plan | 7 days — CVSS v4 **plus** a plain-English "what an attacker can actually do to your guild" |
| Fix, Critical / High | 14 days |
| Fix, Medium | 30 days |
| Fix, Low | Next scheduled release |
| Public disclosure | Coordinated, 90 days by default, negotiable in either direction |

## Supported versions

Self-hosted software gets patched when the operator notices, so the support window is short on
purpose and the notification is loud.

| Version | Status |
|---|---|
| `main`, pre-1.0 | The only thing that exists today. Fixes land on `main`; there are no backports before 1.0. |
| Latest minor of the current major | Supported |
| Previous minor | Security fixes only, for **90 days** after its successor ships |
| Anything older | Unsupported. Upgrade. |
| `:edge` container tag | Built from `main`, unsupported, not for guilds |

Run the **`:1` rolling tag** — `ghcr.io/dragonkillparty/dkp:1`. It tracks the latest 1.x and is the
documented default precisely so that `docker pull && docker restart` is the whole upgrade procedure.
Every 1.x upgrades to any later 1.y; the nightly upgrade ladder tests that from the oldest supported
refdb to `HEAD`.

**You will be told.** The daily update check returns `{latest, min_safe_version, security,
advisory_url}`. When the running version is below `min_safe_version` the admin UI shows a
persistent, non-dismissible banner, `dkp doctor` fails, and `/ops` flags it. There is **no remote
kill switch and no forced auto-update** — both are backdoors. Notification is the whole mechanism,
and it is the single control that most affects real-world risk in self-hosted software.

## Scope

### In scope

Anything in this repository, in the published container images, and in the released binaries.

### The classes we care most about

Ranked by what a guild actually loses. A finding in any of these gets triaged at High or above
before we look at the CVSS score.

| Class | What counts |
|---|---|
| **Authorization bypass** | Reaching an operation without its `x-dkp-permission`; reading or mutating another person's record by ID; a route that skips the single authorization choke point; the SPA reaching a capability the public API does not expose. |
| **Ledger tampering** | Any path that `UPDATE`s or `DELETE`s a `ledger_batch` or `ledger_entry` row, in Go, in SQL, or in a migration. Any path that makes a balance disagree with the entries behind it. Any way to alter or delete an audit row, or to break the audit hash chain without detection. Corrections are reversal batches; anything else is a vulnerability, not a feature request. |
| **Sealed-bid leakage** | Learning a sealed bid — its existence, its amount, or its owner — before reveal. Via the API, an SSE frame, a webhook payload, an `ETag`, a collection count, an error message, an audit row a bidder can read, or a timing difference. This one is unusually easy to leak by accident and unusually damaging: a leaked sealed bid is an invisible loss of the auction's whole premise. |
| **Token scope escalation** | Any path by which a personal access token exceeds `role permissions ∩ token scopes`. There is no `admin:*` scope and no all-powerful token. Operations that alter authentication, authorization, or bulk-export state — minting or revoking tokens, editing roles, downloading a backup, bulk PII read, committing an import — are session + step-up only and have **no scope at all**. A PAT reaching any of them is a vulnerability by definition. |
| **SSRF via webhook and other outbound URLs** | Guild-configured webhooks, the Discord notifier, avatar-by-URL, the item icon base URL, OIDC discovery and JWKS, the backup endpoint. A great many of these instances run on a home LAN next to a router admin page and a Proxmox host. Validation happens on the IP the socket is about to connect to, so a bypass of the dialer check — DNS rebinding, a redirect hop, an IPv4-mapped or NAT64 address, an unexpected scheme — is in scope even without a demonstrated pivot. |

Also very much in scope, without being their own row: authentication and session handling, stored or
reflected XSS in any rich-text or name field, CSRF, SQL injection, idempotency or replay flaws that
double-charge a bid or double-post a tick, PII disclosure, secret leakage in logs or error bodies,
and supply-chain integrity of the published artifacts.

### Out of scope

| Not a vulnerability in this project | Why |
|---|---|
| Your reverse proxy, TLS termination, or firewall configuration | We ship safe defaults and document the proxy setup; we cannot fix your nginx. `X-Forwarded-For` is trusted only from configured proxy CIDRs — misconfiguring that is an operator issue. |
| Exposing the instance to the internet with no proxy, or mounting `/data` somewhere world-readable | A backup file is a full credential and PII dump; the docs say so plainly. |
| Enabling `/metrics` without `DKP_METRICS_TOKEN`, or relaxing `DKP_OUTBOUND_ALLOW_PRIVATE` | Both are disabled or restrictive by default, both are audited when changed. |
| An officer legitimately adjusting points, or an owner reading data they are entitled to read | The design's answer to officer abuse is attribution, reversibility, and visibility — not prevention. See the non-goals in the security design. |
| Defence against the host operator | Whoever holds the SQLite file holds the data. Controls there are detective (audit anchors, off-box backups), not preventive. |
| Missing hardening headers with no exploitable consequence, weak TLS on *your* proxy, version disclosure, self-XSS, clickjacking on unauthenticated pages | Report them as ordinary issues; they are welcome, just not embargoed. |
| Volumetric DoS, or "I ran a scanner and it says" | Automated scanner output with no analysis will be closed. |
| Vulnerabilities in EQdkp Plus itself | Report those to that project. This one only reads its database. |

**Safe harbour.** Test against your own instance. We will not pursue good-faith research that stays
within this policy, does not access other people's data, does not degrade a live guild's service,
and gives us a reasonable window before disclosure. Testing against somebody else's guild instance
without their written permission is not good faith and is not covered.

## What happens after triage

1. **Fix on a private fork.** Every security fix ships with a regression test; the fix and the test
   land together.
2. **Advisory.** A GHSA is published for every security fix, with a CVE requested through GitHub's
   CNA. It states the affected versions, the fixed versions, the impact in plain language, and —
   where one exists — a **config-level workaround** (`DKP_COMPAT_ENABLED=false`,
   `DKP_OUTBOUND_ALLOW_PRIVATE=false`, revoke-all-tokens) so an operator who cannot upgrade today
   can still act today.
3. **Release.** Security releases are **patch-only and isolated from feature work**, so the upgrade
   decision is never bundled with a behaviour change. A `SECURITY:` commit trailer drives the
   generated release notes.
4. **Backport.** The fix goes to the latest minor and, within its 90-day window, to the previous
   minor, via the `backport/1.x` label. A `release-1.x` maintenance branch is created lazily, only
   when one is actually needed.
5. **Notify.** `min_safe_version` in the advisory feed is raised, which turns on the in-product
   banner and fails `dkp doctor` on every unpatched instance.
6. **Post-mortem where it is useful.** If the bug class could recur, the fix includes the mechanism
   that stops it recurring — a lint rule, a CI gate, a database trigger — and the ADR that explains
   it. A rule without a mechanism is a wish.

## What we do continuously

A control listed as **planned** is not running. This table states what is wired up today, because a
security document that describes intentions in the present tense is worse than one that says
nothing — it stops anyone from noticing the gap.

| Control | Cadence | Status |
|---|---|---|
| `govulncheck` — reachable Go vulnerabilities only, so the signal stays high enough to act on | Every PR | **live** — `security / govulncheck`, required, not `continue-on-error` |
| Dependency licence gate — copyleft (GPL/AGPL/LGPL/EPL/CDDL/CC BY-SA), non-commercial (CC BY-NC/ND) and source-available (BUSL/SSPL/Elastic/FSL/PolyForm/Commons Clause) are all denied in the runtime graph | Every PR | **live** — `security / licences`, required. Allowlist-based, so an unrecognised licence stops the build rather than passing |
| The AGPL firewall — EQdkp Plus identifiers outside the two allowlisted paths | Every PR | **live** — AGPL001 in `lint / repo` |
| GitHub Actions pinned to a full commit SHA | Every PR | **live** — PIN001 in `lint / repo` |
| gitleaks secret scanning | Every PR, full history on a schedule | **planned** — a local pre-commit hook runs today; there is no CI job yet |
| `osv-scanner`, `pnpm audit` | Every PR | **planned** |
| CodeQL (Go + JS), `security-extended` | Every PR, plus weekly | **planned** |
| Trivy image CVEs | Nightly against `:latest`, filing an issue on new High/Critical | **planned** — needs the release image (Phase 0 PR 7) |
| Nightly `govulncheck` on `main` | Nightly | **planned** — needs `nightly-verify.yml` filled in |
| The authorization matrix — every principal × every operation, asserted | Every PR | **planned** — needs `internal/authz` (Phase 2) |
| SBOM, cosign signatures, SLSA build provenance on every release artifact | Every release | **planned** — Phase 0 PR 7 |

To run the two live dependency controls locally:

```bash
make licence-gate && make govulncheck
```

`make check` runs the licence gate as part of `make lint`. `govulncheck` is not in `make check` —
not because it is slow (it takes about four seconds) but because it fetches the vulnerability
database from `vuln.go.dev`, and `make check` is expected to work without connectivity. CI runs it
as its own required job.

The runtime image is `FROM scratch`: no shell, no package manager, no interpreter, and therefore no
base-image CVE feed. A `:1-debug` tag exists for people who need to exec in, and its larger attack
surface is documented where it is offered.
