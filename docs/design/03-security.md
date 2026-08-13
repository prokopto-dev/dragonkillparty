# Security design and threat model

**Status:** design. **Audience:** contributor, agent, operator.
**Normative tie-breaker:** [`00-canonical-conventions.md`](00-canonical-conventions.md). Where this
file and that one disagree, that one wins and this file has a bug.

**Thesis.** Most guild-DKP compromise is not RCE. It is an insider quietly moving points. So the
budget goes to an append-only ledger, a single authorization choke point, total transparency to
members, and blast-radius limits on bot tokens — and the *minimum* to ceremony that volunteer
officers will disable.

**The rule that shapes every section below:** a control must be expressed as something testable in CI
or observable in `dkp doctor`. A control an operator has to remember is a control that does not
exist. Every row of every table names its mechanism; where a claim is an empirical assumption rather
than a decision, it is marked **(assumption)**.

---

## 1. Where the budget goes

| Spend | Control | Mechanism |
|---|---|---|
| **High** | Append-only ledger and audit log | DB trigger + an integration test asserting the trigger fires |
| **High** | One authorization choke point | Middleware before the handler; the authz matrix golden file |
| **High** | Transparency to 60 self-interested observers | Public per-account statement, member-downloadable artifacts, Discord broadcast on every mutation |
| **High** | Token blast radius | Capability floor, scoped PATs, per-token activity log, one-command revoke-and-reverse |
| **Medium** | Rich-text sanitisation | `internal/richtext` as the single choke point + an XSS corpus |
| **Medium** | SSRF containment | `internal/net/safehttp` as the only `*http.Client` constructor + a grep gate |
| **Low** | Ceremony (lockouts, CAPTCHAs, mandatory rotation, approval workflows) | Deliberately thin. See §5.5 for the one that was cut. |

---

## 2. Threat model

### 2.1 Assets, ranked by what a guild actually loses

| # | Asset | Why it matters | Loss modes |
|---|---|---|---|
| 1 | **The ledger** (batches, entries, bids, holds) | This *is* the product. Ten years of raid history cannot be re-derived from the game. | Silent mutation, deletion, unauthorized append, replay |
| 2 | **Audit log and artifacts** | The only evidence answering "why did my points change?" Without it every dispute is a popularity contest. | Deletion, selective editing, artifact tampering |
| 3 | **Officer and owner credentials** | Compromise yields assets 1 and 2. Officers reuse passwords from 2005-era gaming forums. | Credential stuffing, phishing, session theft, OAuth link takeover |
| 4 | **Bot and API tokens** | Live in hobbyist Discord-bot repos, `.env` files, pinned Discord messages. Assume eventual public leak. | Public leak, over-scoping, no expiry, no revocation path |
| 5 | **Server secrets** (root key and its subkeys) | Forgeable tokens and webhooks, decryptable TOTP seeds. | Baked into an image, committed, world-readable on the volume |
| 6 | **Member PII** (emails, Discord ids, IPs, `/tell` text inside uploaded logs) | A raid log contains private conversations of people who never used this product. | Bulk export by a low-privilege actor, backup exposure |
| 7 | **The host** | Often a home NAS or a raid PC on the same LAN as a router admin panel. | RCE, SSRF pivot into the LAN, container escape |
| 8 | **Availability during a raid** | A 60-person raid stalls if the bid board is down. Low security impact, high social impact. | Login flood, SSE exhaustion, zip bomb, import monopolising the single writer |

### 2.2 Actors and assumed capability

| Actor | Authenticates as | Assumed intent | Assumed capability |
|---|---|---|---|
| Anonymous internet | none | opportunistic | Mass scanning, combolists, exploit kits aimed at PHP/CMS paths |
| Opportunistic scanner | none | automated | Finds the instance within hours of exposure regardless of obscurity; hits `/wp-login.php`, `/api.php`, `/.env` |
| **Member** | session or feed token | self-interested, occasionally hostile | Probes for IDOR; tries to claim a character that is not theirs; forges an artifact if given `logs:ingest` |
| **Hostile or departing member** | member or officer | malicious *and domain-literate* | Understands ticks, bids, pools; may exfiltrate the roster to a rival guild; may still hold a valid session after being removed from Discord |
| **Officer** | session, MFA where required | mostly honest, incentive-conflicted | Creates raids, ticks, awards, adjustments — *and is a legitimate recipient of DKP*. The single highest-value threat actor in this model. |
| **Owner/admin** | session + step-up | trusted with the app, not necessarily with the host | Roles, tokens, settings, imports, backups |
| **Service account / PAT** | bearer token | dumb, retry-happy, eventually leaked | Whatever its scopes allow, from anywhere, at machine speed, forever |
| **Host operator** | shell on the box | **out of scope as an adversary** | Owns everything. Controls here are *detective* (off-box anchors and backups), never preventive. The docs say this plainly. |
| **Upstream dependency / build pipeline** | n/a | compromised | npm lifecycle scripts, a typosquatted Go module, a mutated Action tag |

### 2.3 Trust boundaries

```
 ┌ internet ───────────────────────────────────────────────────────────┐
 │  browsers · Discord bots · log parsers · bid bots · scanners         │
 └──────────────┬──────────────────────────────────────────────────────┘
      [B1] TLS + reverse proxy.  X-Forwarded-For trust is decided HERE.
 ┌──────────────▼──────────────────────────────────────────────────────┐
 │  dkp process (uid 65532, FROM scratch, read-only rootfs, no shell)   │
 │   [B2] middleware: ratelimit → principal → authz → idempotency       │
 │   [B3] handler → service → store.Tx   (only holder of *sql.DB)       │
 │   [B4] strategy sandbox: pure — no DB, no clock, no RNG, no network  │
 │   [B5] safehttp: only constructor of an *http.Client (SSRF gate)     │
 │   [B6] richtext: only producer of HTML from user input (XSS gate)    │
 └──────────────┬──────────────────────────┬───────────────────────────┘
      [B7] /data volume                [B8] outbound: Discord, webhooks,
      (db, secrets 0600, artifacts,          OIDC/JWKS, S3 backup,
       backups, WAL)                         icon fetch, update check
```

Each boundary is grep-enforced in CI, not merely documented:

| Boundary | Gate |
|---|---|
| B2 | Every operation declares `Security` + `x-dkp-permission`; handlers never call an ACL function |
| B3 | `sql.Open`, `.Query(`, `.Exec(`, `.QueryRow(` outside `internal/store` fails CI |
| B4 | `internal/store` import, `time.Now`, `math/rand` inside `internal/strategy` fails CI |
| B5 | `http.Client{`, `http.Get`, `http.Post` outside `internal/net/safehttp` fails CI |
| B6 | HTML produced outside `internal/richtext`; `dangerouslySetInnerHTML` outside one component |

**There is no plugin boundary.** EQdkp's "drop third-party PHP into a directory" model is an explicit
non-goal. The extension point is the HTTP API. Nothing a guild installs executes in this process.

### 2.4 Top attack scenarios, ranked by likelihood × impact

L and I on 1–5. "Primary control" names the structural defence; details follow in later sections.

| Scenario | L | I | Score | Primary control |
|---|---|---|---|---|
| **Officer self-awards DKP** — an adjustment or ghost tick crediting their own account, or their own loot priced at 0 | 5 | 4 | **20** | Append-only ledger · `actor_is_beneficiary` auto-flag · mandatory `reason` · real-time Discord broadcast · member-visible per-account statement (§9) |
| **Bot token leaked in a public repo** and used to write awards or adjustments | 4 | 4 | **16** | Scoped PATs, HMAC-hashed at rest, display-once, default expiry · the **capability floor** (§6.4) · per-token activity log · one-command revoke-and-reverse |
| **Editing or deleting a finalized raid or tick** to erase a rival's attendance | 4 | 4 | **16** | Post-finalization changes are correction batches, never mutations · DB trigger makes a silent `UPDATE` impossible · separate permission · dispute window · webhook event |
| **Credential stuffing against an officer account** | 4 | 4 | **16** | argon2id · per-account progressive delay · bundled breached-password list · MFA required for `admin.*` roles · new-device email |
| **Stored XSS via rich text — a raid note, an article, a comment, the shoutbox, an item or character name** → session theft → officer takeover | 3 | 5 | **15** | `internal/richtext` as the only HTML producer · no raw HTML accepted anywhere · CSP `script-src 'self'` with no `unsafe-inline` · bidi stripping in names (§7.3) |
| **Privilege creep via role edits** — an officer grants themselves `admin.*`, or Discord role sync silently promotes | 3 | 5 | **15** | Cannot grant a permission you do not hold · cannot edit your own roles · step-up MFA · at-least-one-owner invariant · Discord roles are advisory and can never map to `admin.*` |
| **The instance is never patched**; a published advisory sits unapplied for a year | 5 | 3 | **15** | The highest *residual* risk in self-hosted software. `:1` rolling tag as the documented default · advisory feed carrying `min_safe_version` driving a non-dismissible banner · `dkp doctor` staleness check |
| **Forged artifact upload** — a doctored `RaidRoster` or fabricated `/who` paste claiming attendance | 4 | 3 | **12** | `logs:ingest` is officer/service-account only · uploader and content hash recorded · corroboration badge when ≥2 independent uploaders produce the same tick window · every member can download the raw artifact |
| **Bid replay or double-spend** — a bot retries `POST /bids` or `settle`, or one balance funds two sessions | 4 | 3 | **12** | `Idempotency-Key` required, keyed `(principal, key)` · unique `source_ref` · balance **holds** taken in the validating transaction · `If-Match` on transitions · settle-time revalidation |
| **Officer covers tracks by deleting audit rows** | 3 | 4 | **12** | No endpoint deletes audit rows at any permission level · DB trigger · hash chain · off-box daily anchors (§9.3, with its honest limits) |
| **SSRF via a guild-controlled webhook, avatar URL or item icon** → pivot to the LAN | 3 | 4 | **12** | `safehttp` validating the *connected* IP in `net.Dialer.Control` (defeats DNS rebinding) · private ranges denied by default · webhooks follow zero redirects |
| **Backup file exposure** — `/data` mapped into another container's webroot, or the zip mailed around | 3 | 4 | **12** | Backups `0600` · optional encryption · download is session + step-up + audited and PAT-forbidden · docs state a backup is a full credential and PII dump |
| **Character claim theft** — a member claims someone else's character and inherits the balance | 3 | 4 | **12** | Officer approval is the default and always available · claim tokens single-use, hashed, TTL-bound to one character · claims audited and broadcast |
| **Opportunistic scanner lands a generic web exploit** (the EQdkp historical pattern: RFI, SQLi, reflected XSS, admin-auth-by-`Referer`) | 5 | 2\* | **10** | Memory-safe Go · no dynamic includes, no `eval`, no plugin loader · parameterized SQL by construction · `html/template` on server-rendered surfaces · scratch image with no shell to pivot into |
| **Supply-chain compromise** (npm lifecycle script, typosquatted module, mutated Action tag) | 2 | 5 | **10** | `ignore-scripts` · lockfiles + `go.sum` + checksum DB · Renovate release-age delay · Actions pinned to SHAs · SLSA L3 provenance |
| **Session survives removal from the guild** — a kicked member keeps writing | 3 | 3 | **9** | `session_epoch` bumped on role change and deactivation · token revocation when a service-account owner departs · nightly roster reconciliation |
| **Login-flood DoS** — argon2id memory exhaustion on a 1 GB box during a raid | 3 | 3 | **9** | Concurrency semaphore around every argon2 call · throttling *before* hashing · measured parameters · `ReadHeaderTimeout` |
| **Public recruitment form abused** (Phase 7) — spam flood, XSS in an application, avatar-URL SSRF | 3 | 2 | **6** | No account required but rate-limited per IP and globally · all text through `richtext` · no URL fetching from an unauthenticated form |

\* Impact 2 not because the class is mild, but because the architecture removes the usual escalation
paths: no shell, no interpreter, no writable code path, no plugin loader in the image.

**Note on tenancy.** There is exactly one guild per instance and no `guild_id` column
(canonical §9). The cross-guild data-leak scenario that appears in multi-tenant designs is not
mitigated here — it is *removed*, because a missing `WHERE guild_id = ?` is a silent leak that no
test catches by accident. A hoster running several guilds runs several containers with separate
volumes; 1.0 makes no isolation claim between mutually hostile tenants in one process, because that
configuration does not exist.

### 2.5 Non-goals and accepted risks

- **Cryptographic proof that a raid happened.** The game emits nothing signed. Attendance provenance
  is *social* — who uploaded it, corroborated by whom — and the product surfaces that honestly.
- **Defending against the host operator.** Detective controls only. Stated plainly in the docs.
- **Preventing a disclosed, reasoned officer adjustment.** Officers are supposed to adjust points.
  The goal is that every adjustment is attributed, reasoned, reversible-not-erasable, and visible to
  the whole guild within seconds.
- **A remote kill switch or forced auto-update.** Both are backdoors. Notification only.
- **Anti-abuse for a public internet audience.** This is a 60-person hobby guild tool.

---

## 3. Authentication

### 3.1 Password storage

**argon2id** via `golang.org/x/crypto/argon2`, PHC-encoded so parameters travel with the hash:

```
$argon2id$v=19$m=19456,t=2,p=1$<22-char b64 salt>$<43-char b64 tag>
```

| Parameter | Value | Rationale |
|---|---|---|
| memory `m` | 19456 KiB | OWASP Password Storage baseline |
| iterations `t` | 2 | same |
| parallelism `p` | 1 | keeps peak memory predictable under concurrency |
| salt | 16 bytes from `crypto/rand`, per hash | — |
| tag | 32 bytes | — |
| max input | 128 bytes | request sanity bound, not a crypto need |

**Constrained-hardware ladder.** `DKP_ARGON2_PROFILE` selects a point on OWASP's equivalent-cost
ladder: `m=47104,t=1` / `m=19456,t=2` *(default)* / `m=12288,t=3` / `m=9216,t=4` / `m=7168,t=5`, all
`p=1`. `dkp doctor` measures actual hash wall time and warns outside 50–500 ms, printing the fix
("set `DKP_ARGON2_PROFILE=low` — your hardware takes 1.4 s per login"). **(assumption:** the exact
band is a starting point to re-measure on a Raspberry Pi during Phase 2.**)**

**Memory exhaustion is the operational risk, not cracking.** 19 MiB × unbounded concurrent logins
OOMs a Pi. Every argon2 call — verify *and* derive — passes a package-level weighted semaphore sized
`max(2, NumCPU)`; requests beyond it queue behind the rate limiter and are shed with `429` rather
than allocating. *Mechanism:* an integration test fires 200 concurrent logins and asserts RSS stays
inside a budget.

**No pepper on passwords, deliberately.** A pepper helps only against a DB-only leak, which for a
single-file SQLite deployment is nearly the same event as a filesystem leak, and it makes rotation
require every user to re-authenticate. Tokens *are* peppered (§6.1) because they need a fast keyed
hash, not a slow one. This is written down so it is not "fixed" later by someone who read a blog post.

**Rehash on login** when stored parameters differ from the current profile, inside the successful-login
transaction.

**Legacy EQdkp hashes are never imported** (`AGENTS.md`, non-negotiable). The source population mixes
`$2y$`, `$2a$…:salt`, `$argon2i(d)$`, phpass `$S$`, `ext_des`, salted sha512 and bare MD5. The
importer sets an impossible hash and mints one-time claim tokens.

### 3.2 Password policy

NIST SP 800-63B posture, not 2005's:

- **12–128 characters.** No composition rules. No forced periodic rotation.
- **Bundled offline breached-password blocklist** (~100k entries), checked at set-time. Offline
  because a self-hosted box may have no outbound internet and because an outbound call at
  registration is both a privacy leak and an availability dependency.
- Optional HIBP k-anonymity range check (`DKP_HIBP_CHECK`, off by default), through `safehttp`,
  fail-open, sending only the first five SHA-1 hex characters.
- Reject the username, the email local-part and the guild name as substrings, case-insensitively.
- The UI strength meter is advisory; the server decides.

### 3.3 Credential stuffing and brute force

| Control | Value |
|---|---|
| Per-account login attempts | 5/min, then progressive delay 1 s, 2 s, 4 s … capped at 30 s, decaying over 15 min |
| Per-IP login attempts | 20/min — **only** when the client IP is trustworthy per §8.3 |
| Global unauthenticated auth budget | 200/min instance-wide, shed with `429` before argon2 |
| Hard lockout | **Off by default.** A hard lockout is a DoS an attacker aims at every officer 30 minutes before a raid. After 20 failures in 30 min the account requires a second factor (if enrolled) or an emailed challenge, and the owner is notified. |
| Last-owner protection | The final account holding `admin.owner` can never be locked, disabled or auto-suspended |
| Response uniformity | One error, `invalid_credentials`, for unknown user / wrong password / unverified email / disabled account. A dummy verify against a fixed hash runs when the user does not exist. 250 ms response floor. |
| New-device notification | Email on a successful login from a fingerprint (IP /24 + UA family) unseen in 90 days, with a revoke-all link |
| CAPTCHA | **None.** It needs a third party, leaks users to Google, and breaks on a LAN-only install. Rate limits plus MFA carry the load. |

### 3.4 MFA / TOTP — ships in Phase 2

MFA enrolment is **not** post-1.0. Without it, the capability floor, the backup gate, the token-mint
gate and the role-edit gate are all advertised and unenforced, which is worse than not claiming them.

- **RFC 6238 TOTP**, SHA-1 for authenticator compatibility, 6 digits, 30 s period, ±1 step, replay
  prevented by storing the last accepted step counter per credential.
- Secret: 20 bytes from `crypto/rand`, **encrypted at rest** with AES-256-GCM under an HKDF subkey
  (`info = "dkp/totp-enc/v1"`), AAD = user ULID. It is not a password hash; it must be decryptable.
- **Recovery codes:** 10 × 10 characters, shown once, stored as `HMAC-SHA256(pepper_subkey, code)`,
  single-use, regenerable (regeneration invalidates all previous codes and is audited). Consuming one
  emails the user.
- Verification limited to 5/min/account; 10 consecutive failures require password re-authentication.
- **Enrolment and disable require re-authentication** within 5 minutes. Both audited and emailed.
- **Enforcement policy:** a guild can require MFA for any role; the default template requires it for
  any role holding `admin.*`. Checked at the authz choke point, not at login, so a session opened
  before enrolment cannot slip past.
- **Step-up (re-auth within 5 minutes) is required for, and only for:** minting/rotating/revoking a
  PAT, editing roles or role assignments, changing another user's credentials or email, disabling
  MFA, downloading a backup, committing an EQdkp import, reversing a ledger batch older than 30 days,
  changing OAuth/OIDC settings, changing the outbound network policy, and exporting the audit log.
  Short lists get followed; long ones get disabled.
- WebAuthn/passkeys: post-1.0. The credential table is polymorphic from day one so it is not a
  retrofit, but self-hosted origins change and passkey recovery for volunteer operators is a support
  burden.

*Mechanism:* an architectural test iterates the Huma registry and asserts the set of operations
carrying `x-dkp-stepup: true` equals exactly the list above.

### 3.5 Discord OAuth2 and generic OIDC

The most dangerous area in the design, because this is where account *takeover* — as opposed to
guessing — lives.

**Flow correctness**

| Item | Requirement |
|---|---|
| Grant | Authorization Code only. Never implicit, never password grant. |
| **PKCE** | `S256` **always**, including for the confidential server-side client. Verifier: 32 random bytes → base64url. Sending it unconditionally costs nothing and closes code interception. **(assumption:** whether Discord *requires* `code_verifier` for this client configuration is unverified; send it regardless.**)** |
| **`state`** | 32 random bytes in a signed `__Host-dkp_oauth` cookie (`HttpOnly; Secure; SameSite=Lax`, 10-minute TTL) bound to the PKCE verifier, the intended action (`login` \| `link`), and the return path. Single-use, deleted on callback. Missing or mismatched → hard fail, audited. |
| `nonce` (OIDC) | Required, verified against the ID-token claim. |
| `redirect_uri` | Exactly one, derived from `DKP_BASE_URL`, registered exactly at the provider. **Never** built from the `Host` header. No `redirect` query parameter is ever honoured. |
| Return path | A path only, validated against `^/[A-Za-z0-9/_\-]*$`. This is the open-redirect gate. |
| Scopes | `identify` always; `email` only if a local-account email is wanted; `guilds.members.read` only when role sync is enabled, requested at link time behind an explicit consent screen. |
| Token handling | Exchange server-side only. Provider access/refresh tokens are **not stored** unless role sync is enabled; when stored, AES-256-GCM under an HKDF subkey. Never returned to the browser, never logged. |
| ID-token validation | `iss`, `aud`, `exp`, `iat` skew ≤ 120 s, `nonce`; algorithm allowlist `RS256`/`ES256`; `alg: none` rejected; JWKS fetched through `safehttp` with caching and a `kid` match; the discovery document is fetched at config time and the resulting endpoints pinned. |

**Account-linking and takeover hazards — the part that actually bites**

1. **Identity is the provider `id`, never `username` or `global_name`.** Discord handles became
   changeable and *reusable* after the 2023 pomelo migration; keying on a handle means a user who
   releases theirs hands over the account. The unique index is `(provider, provider_subject_id)`.
2. **Never auto-link by matching email.** This is the classic OAuth takeover: the attacker registers
   a Discord account with `officer@guild.org`, signs in, and is merged into the officer's account.
   Linking a Discord identity to an existing local account requires an authenticated session for
   that account, re-authentication, and an explicit confirm. A Discord-first login that finds an
   unclaimed matching email creates a **new, unprivileged, unlinked** account and shows "an account
   with this email exists — sign in and link it from Settings".
3. **Verify the provider's `email_verified` / `verified` flag** before trusting an email for
   anything, and never for authorization.
4. **Unlinking is blocked** if it would leave the account with no usable credential. Link and unlink
   are audited and emailed.
5. **Discord roles are advisory.** Mapping a Discord role to a DKP role is opt-in per role, and roles
   carrying `admin.*` or `admin.owner` **cannot be mapped at all**. A compromised Discord server
   admin must not become a DKP admin, and a Discord outage must not remove the officers' ability to
   close out DKP. Sync runs on login and nightly; every change writes an audit row with
   `source = discord_sync`.
6. **Local accounts remain mandatory.** First-owner bootstrap is local-only. There is never a state
   where the only path to `admin.owner` runs through a third party.
7. A changed provider handle is surfaced in the UI, never acted upon.

### 3.6 Sessions

- **Opaque and server-side.** 32 bytes from `crypto/rand`, base64url. **The SHA-256 of the token is
  what is stored**; the plaintext exists only in the cookie, so a read-only database leak yields no
  live sessions.
- Cookie: `__Host-dkp_session`, `HttpOnly; Secure; SameSite=Lax; Path=/`, no `Domain`. The exact name
  appears in the OpenAPI `securitySchemes` block (canonical §7). The `__Host-` prefix pins it to the
  exact origin and blocks subdomain injection, which matters because self-hosters park several apps
  under one domain.
- `Secure` is set whenever the effective scheme is HTTPS (direct TLS, or `X-Forwarded-Proto` from a
  *trusted* proxy per §8.3). On plain HTTP it is dropped and a loud warning appears in the logs, at
  `/ops`, and in `dkp doctor`.
- Lifetime: idle 14 days, absolute 30 days. "Remember me" extends idle to 30, absolute to 90.
- **Rotation** (new id, old invalidated) on login, MFA completion, password change, email change,
  role or permission change, and step-up.
- **Sign out everywhere:** each user row carries `session_epoch`; bumping it invalidates every
  session in one write. Bumped automatically on password change, MFA disable, OAuth unlink, role
  change and deactivation.
- Session list UI shows created-at, last-seen, IP (per §11.1 retention), UA family, current-session
  marker and individual revoke. This is the user-visible detection control for a stolen session.
- **Not JWT.** A single-process app gains nothing from stateless sessions and loses instant
  revocation. The only signed bearer in the system is the 30-second, single-use, audience-bound SSE
  handshake ticket.

### 3.7 Password reset, email change, and the Host-header trap

- Reset token: 32 random bytes, **SHA-256-hashed at rest**, single-use, 30-minute TTL, bound to the
  user and to the email address at request time. Using it invalidates all sessions and all other
  reset tokens for that user.
- `POST /auth/reset` **always returns 202**, regardless of whether the address exists.
- Rate limits: 3/h/account, 10/h/IP, 50/h instance-wide.
- **Every link in every email is built from `DKP_BASE_URL`, never from the `Host` header.** A
  middleware rejects requests whose `Host` matches neither `DKP_BASE_URL` nor an explicit
  `DKP_EXTRA_HOSTS` entry, with `421 Misdirected Request`. This closes host-header poisoning of reset
  links — a bug class that has hit essentially every framework.
- Email change: re-auth, confirmation to the **new** address, notification to the **old**, and the
  change lands only when the new address confirms. The old address can revoke within 72 hours.
- SMTP is optional. If unconfigured, `dkp doctor` says so and `dkp admin reset-password <user>`
  prints a one-time link. A guild without mail is not locked out.

### 3.8 Character claiming is an authentication event

Claiming a character *is* claiming its DKP balance.

| Method | Trust | Notes |
|---|---|---|
| Officer approval | baseline, always available | Ships first. Pending claims are visible to all officers, audited and broadcast. |
| Roster-dump verification | strong | An officer or service-account upload auto-verifies name, class and level. The officer's client is the trust anchor. |
| In-game nonce (`dkp-verify a7f3x` in guild chat, ingested by the parser) | strongest per claim | 6 characters, 15-minute TTL, single-use, hashed, bound to one character. An enhancement, never a gate. |
| Claim tokens from import | credential-grade | 32 bytes, hashed, single-use, 14-day TTL, bound to one character. **The docs must say: distribute by DM, never in a public channel** — the printed list is a credential dump. |
| Screenshots, "trust me", Discord nickname | never | Nickname is prefill only. |

Claims are revocable. Revocation does not delete history; it re-points future attribution and writes
an audit row.

---

## 4. Authorization — the choke point

### 4.1 One place, structurally enforced

Every operation declares its permission in its Huma registration. The middleware enforces it *before*
the handler is entered, so a handler cannot forget to check.

```go
huma.Register(api, huma.Operation{
    OperationID: "createAdjustment",
    Method: http.MethodPost, Path: "/api/v1/adjustments",
    Security: []map[string][]string{{"pat": {"dkp:adjust"}}, {"session": {}}},
    Metadata: map[string]any{
        "x-dkp-permission": "dkp.adjust",
        "x-dkp-stepup":     false,
        "x-dkp-audit":      "always",
    },
}, handlers.CreateAdjustment)
```

The chain is fixed and total:

```
recover → requestID → accessLog(redacting) → hostCheck → securityHeaders
  → bodyLimit → rateLimit(anonymous) → principal(cookie|bearer) → csrfOrigin
  → rateLimit(principal) → authz(x-dkp-permission, x-dkp-stepup)
  → idempotency → handler
```

Three architectural tests make this non-optional:

1. **Every registered operation declares `Security`, `x-dkp-permission` and an explicit
   `operationId`.** Allowlist: `/healthz`, `/readyz`, `/metrics`, `/openapi.json`, the OAuth
   callback, the setup wizard while unbootstrapped, and routes flagged `x-dkp-public: true`. Adding
   an unauthenticated route is a test failure.
2. **Every mutating operation rejects a zero-scope PAT with `403`.** Enumerated from the registry,
   never hand-written.
3. **`x-dkp-public: true` is checked against a golden file** under CODEOWNERS. Making something
   public requires a reviewed diff.

### 4.2 The permission and scope catalogue

There is exactly one source: `internal/authz/catalogue.go` (canonical §6). It generates the
`permission` table seed, the OpenAPI `x-dkp-permission` metadata, the PAT scope enum, the
authorization-matrix header, and `docs/reference/permissions.md`. `role_permission` is FK-constrained
to `permission(key)`, so a divergent hand-written list is a **boot failure**, not a style issue.

Use the canonical keys. Do not invent parallel vocabularies (`dkp.adjustment.create`,
`admin.token.mint` and friends are stale spellings from an earlier draft).

**Effective capability = role permissions ∩ token scopes.** A token can only ever narrow what its
service account's role already grants.

**There is no `admin:*` scope and no all-powerful token.** This is the single biggest deliberate fix
over EQdkp Plus, whose `api_key` impersonates the first superadmin.

### 4.3 Escalation prevention

| Rule | Mechanism |
|---|---|
| You cannot grant a permission you do not currently hold | Integration test per permission key |
| You cannot edit your own role assignments — someone else must | Integration test |
| The last account holding `admin.owner` cannot be demoted, disabled or deleted | DB-level check plus integration test |
| There is **no hardcoded superadmin branch** | `admin.owner` is a role with every permission granted *as data*, evaluated by the same code path as any other role. A test asserts `authz.Check` contains no early return. EQdkp's "group id 2 short-circuits the ACL" is a named anti-pattern. |
| Role edits require `admin.roles.manage` + step-up + a `reason` | Middleware + audit row + `role.changed` webhook event |

### 4.4 `403` versus `404`

Encoded once, not left to per-handler taste, because across ~140 operations that is the only way it
stays consistent:

- **`404` when the principal may not know the resource exists** — another person's unclaimed
  character, a sealed bid before reveal, a hidden-rank member, an artifact behind a raid they cannot
  read.
- **`403` when the resource is known and the action is not permitted.**

IDs are ULIDs: sortable and non-enumerable, but treated as *identifiers, not secrets*. Nothing is
authorized by knowing an ID. Cursors are HMAC-signed over `{sort_key, tiebreak_id, filter_hash,
principal_class}` so a member cannot hand-craft a cursor that walks past a filter boundary.

### 4.5 The two-person approval rule: considered and cut

An earlier draft specified a **default-on two-person rule** on self-beneficial adjustments and on
adjustments above a threshold, staging the batch in `pending_approval` until a second officer
approved.

**It is cut.** It was specified as default-on while having no table, no endpoints, no permission key,
no event and no roadmap item — and it cannot be built against an append-only ledger without a
staging table that does not exist. A default-on control that does not exist is worse than no control,
because it gets written into the README.

What replaces it, and what is actually built:

| Replacement | Where |
|---|---|
| `actor_is_beneficiary` computed by the ledger writer and stored on both the batch and the audit row | Phase 1 |
| A visible badge on the batch, and different Discord message wording when the flag is set | Phase 1 / Phase 6 |
| Mandatory `reason` on every adjustment | Phase 1 |
| Member-visible per-account statement with batch drill-down to the source artifact | Phase 3 |
| Real-time broadcast of every ledger mutation to the guild channel | Phase 6 |

Transparency to 60 motivated, self-interested observers is the control here. If a guild later wants
enforcement rather than exposure, it needs a `pending_batch` table plus approve/reject endpoints and
a permission key — a real feature, spec'd and scheduled, not a sentence in a security document.

### 4.6 Why the authz matrix is the highest-value test suite in the product

Detail lives in [`04-testing.md`](04-testing.md); the security argument is:

- **The blast radius is the entire value proposition.** One missing check on `POST /adjustments` and
  any member mints themselves points — and because the ledger is append-only, that damage is
  permanent and public.
- **Authorization is the only cross-cutting concern that fails silently-permissive.** Omit
  idempotency and a test fails. Omit the pagination envelope and a test fails. Omit `Security` on a
  Huma operation and everything works beautifully, for everyone — and it looks fine in review,
  because the missing thing is a line that is not there.
- **Cost is O(1) per endpoint; coverage is O(endpoints × principals).** Adding a route costs one row.
- **It is the machine-checked form of the product's founding claim.** "There is no all-powerful
  token" is in the README; the `pat_zero_scope`, `pat_revoked` and `pat_expired` rows are what make
  it true.

---

## 5. The API and the UI cannot diverge

Divergence happens when the browser has a privileged channel. Four properties close it:

1. **One `Principal`.** Cookie and bearer resolve to the identical struct before any handler runs.
   No handler contains `if session { … } else { … }`; a lint rule bans reading the session cookie
   outside `internal/auth`.
2. **Permissions are the authority; scopes are a filter on top.** A session request is evaluated as
   `permissions(principal)`. A PAT request is `permissions(principal) ∩ scopeToPermissions(scopes)`.
   A token can never exceed its owner's rights, and a browser can never do something a fully scoped
   token cannot. A test asserts the scope→permission map is total over the declared permission
   universe minus the capability floor.
3. **No hidden operations.** `Hidden: true` appears only on the canonical §7 allowlist, enforced by a
   test, and the SPA contains no `fetch` outside `web/src/api` (ESLint).
4. **The PAT-parity suite.** Recorded SPA request sequences are replayed with a scoped PAT and
   asserted byte-identical. If the browser can do something a bot cannot, CI goes red.

---

## 6. Bot and API token security

### 6.1 Format and storage

```
dkp_pat_<8-char public prefix>_<43 chars base64url of 32 random bytes>
dkp_legacy_<8>_<43>     # compat-shim class only
dkp_feed_<8>_<43>       # single-purpose read-only feeds (iCal / RSS)
```

- Stored as `HMAC-SHA256(hkdf(root_key, "dkp/pat-pepper/v1"), secret)` — a **keyed hash, not a
  password hash**, because verification is on the hot path of every API request and must be one
  indexed lookup plus one `subtle.ConstantTimeCompare`. The pepper lives in `/data/secrets.json`, so
  a database-only leak yields nothing usable.
- `prefix` is indexed and non-secret. It identifies the row without revealing the secret and is what
  appears in logs, UIs and the token list.
- A distinctive, greppable prefix is itself a security feature: submit `dkp_pat_` to the GitHub
  secret-scanning partner programme, or at minimum publish the pattern so scanners can match it.
  **(assumption:** partner-programme acceptance for a small project is unverified.**)**

### 6.2 Ownership, scoping, lifecycle

- **Tokens belong to service accounts, not people.** A service account has a human owner for audit
  and notification. Deactivating the human does not kill the bot; the account is flagged `orphaned`
  and an owner-reassignment task appears. The failure mode this prevents — the bot dies mid-raid
  because an officer quit — is the most predictable one in guild tooling.
- **Display-once.** The plaintext is returned exactly once, in the `201` body, with
  `Cache-Control: no-store`. Never retrievable again, never emailed, never logged. Minting requires
  step-up and is audited by prefix.
- **Default expiry 365 days**, maximum 3 years; `never` requires an explicit confirmation flag and
  carries a permanent warning badge. Warnings at 30/7/1 days by email and Discord.
- **Rotation with overlap.** A rotate mints a new secret against the same id and scopes and keeps the
  old one valid for a grace window (default 24 h, max 7 days) so a bot can be redeployed without
  downtime. The window is visible and can be ended early.
- **Revocation is instantaneous** — opaque tokens, one `UPDATE` on a row the auth path already reads.
  This is the concrete reason PATs are not JWTs.
- **Per-token audit.** `actor_token_id` on every ledger batch, audit row and webhook delivery.
  `GET /tokens/{id}/activity` answers "did the leaked token do anything?" in seconds.
- **Optional CIDR pinning.** Most guild bots run from one VPS; pinning turns a leaked token into a
  mostly useless string.

### 6.3 Transport

- `Authorization: Bearer dkp_pat_…` only. Query-string tokens are rejected with `401` and an
  explanatory `problem+json`.
- **Sole exception:** the compat shim accepts `?atoken=` because that is what fifteen years of
  existing P99 bots send (canonical §7). Mitigations: legacy tokens are a distinct class that cannot
  hold write scopes beyond what the shim needs, are rate-limited harder, are counted by a deprecation
  metric naming the prefix, the whole shim is disableable with `DKP_COMPAT_ENABLED=false` and shows
  an admin banner counting days since first use, and **the access logger redacts `atoken` from every
  logged URL** — asserted by a test.
- **Cookies are ignored entirely when `Authorization` is present.** Precedence is fixed and explicit,
  so "send both, get the union" confusion attacks cannot occur. A test sends a low-privilege bearer
  plus a high-privilege cookie and asserts the bearer's rights apply.
- Feed tokens are path-embedded (`/feeds/{feed_token}/raids.ics`) because calendar clients cannot set
  headers. They are read-only, single-purpose, scoped to one feed kind, independently revocable,
  served with `Cache-Control: private, no-store`, and contain no email addresses.

### 6.4 The capability floor

**A PAT — any PAT — can never:**

| Forbidden | Why |
|---|---|
| Create, modify or delete users, or change any credential | Prevents persistence |
| Mint, rotate or revoke tokens | Prevents self-propagation |
| Edit roles, role assignments, or role→Discord mappings | Prevents escalation |
| Change auth settings (OAuth/OIDC config, MFA policy, session settings) | Prevents downgrade attacks |
| Download or restore backups | Prevents wholesale exfiltration |
| Export the audit log | Prevents cover-up |
| Read another member's email or IP history in bulk | Data minimisation |
| Change the outbound network policy or the webhook allowlist | Prevents relaxing the SSRF gate, then pivoting |
| Commit an EQdkp import | Prevents a bot rewriting the guild's history |

All of these are **session + step-up only and carry no scope at all** (canonical §6). *Mechanism:*
`x-dkp-pat-forbidden: true` on those operations, plus an architectural test asserting the flagged set
equals exactly the set canonical §6 "The capability floor" enumerates:

```
token.mint  token.revoke  admin.security.manage  admin.roles.manage
admin.backup  admin.owner  person.pii.read  audit.read  import.commit
```

> **Corrected in Phase 0 PR 5.** This paragraph, `docs/api/auth-and-scopes.md` and
> `.claude/agents/api-contract-guardian.md` each carried a *different* set, and none matched
> canonical §6's prose. The differences were not cosmetic: this one included `admin.settings` and
> `admin.owner`, the other two omitted both. Canonical §6 now enumerates the set as permission keys
> rather than leaving it to be inferred from prose, all three copies point at that enumeration, and
> the test derives from it rather than from a hand-maintained list — so the three cannot drift apart
> again.
>
> `admin.settings` is **not** in the set. The half of it that belongs here — identity-provider
> credentials, MFA and session policy, and the outbound-request allowlist — became
> `admin.security.manage` in the same change, which is what the SSRF paragraph below actually needs.
> Leaving the whole key in would have put a re-authentication prompt in front of an officer renaming
> the guild.

**What a leaked `raids:write dkp:adjust loot:award` token *can* do:** create raids, ticks, awards and
adjustments — i.e. move points around. That is real damage, and it is exactly the damage the ledger is
built to survive: attributed to the token, broadcast to Discord in real time, reversible by a
compensating batch, and never able to erase what it did. Recovery is one command:

```
dkp token revoke <prefix> --reverse-batches --since 24h --dry-run
```

There is no "a PAT may not self-deal" rule. `dkp:adjust` exists precisely to create adjustments;
self-dealing is controlled by `actor_is_beneficiary` flagging and member visibility, not by blocking
the scope (canonical §6).

---

## 7. Input handling

### 7.1 Validation at the boundary

- **JSON Schema derived from Go types (Huma)** validates every request before the handler runs:
  types, ranges, enums, lengths, formats (`ulid`, `email`, `uri`), `minItems`/`maxItems` on arrays.
- **Unknown properties are rejected on requests** (`422 unknown_field`, naming the field). A typo'd
  field in bot code otherwise fails silently and produces wrong data. Responses stay additive;
  clients must ignore unknown fields, and a test injects a synthetic future enum to prove they do.
- **Money is `Centipoints` (int64) and travels as an unquoted JSON integer**, field suffix
  `_centipoints` (canonical §1). Values outside a sane band (`|v| > 10^12`) are rejected. A lint rule
  bans float types in `internal/ledger` and `internal/strategy`.
  *An earlier draft of this document specified quoted decimal strings. That was wrong and is deleted:
  realistic maxima are four orders of magnitude below `MAX_SAFE_INTEGER`, and strings force every SDK
  to parse and invite locale bugs.*
- **Times are integer microseconds** with a plausibility window (not before 1999-03-16, not more than
  24 h ahead) — this catches an officer's PC clock being years off, which happens.
- Every array is bounded: attendees ≤ 200, bids per session ≤ 2000, batch entries ≤ 5000, adjustment
  members ≤ 500.
- Global: `http.MaxBytesReader` at 1 MiB for JSON bodies (per-route overrides for uploads),
  `MaxHeaderBytes` 32 KiB, URL length 8 KiB, ≤ 50 query parameters.

### 7.2 SQL injection, by construction

- **sqlc-generated parameterized queries only.** The generated code cannot concatenate.
- CI repository gates (`internal/repogate`, ADR-0018): `SQL001` and `SQL002` fail `sql.Open`,
  `.Query(`, `.QueryRow(` and `.Exec(` outside `internal/store`. They read the parsed Go rather than
  the text, so an aliased import does not defeat them and a zero-argument `r.URL.Query()` is not a
  false positive; `fmt.Sprintf` near a SQL string literal and a bare `SELECT `/`INSERT `/`UPDATE `/
  `DELETE ` outside `db/queries/`, `internal/store` and `internal/importer` remain review rules.
- **Dynamic sort and filter are allowlist maps**, never interpolation. Unknown sort key → `422`.
  Direction is a two-valued enum. A fuzz test throws hostile sort, filter and cursor values at every
  list endpoint.
- **FTS5 `MATCH` is its own injection surface** and is routinely missed: user text containing `"`,
  `*`, `NEAR`, `OR` or a column filter changes query semantics or errors. All user search input is
  emitted as a single quoted FTS5 string with `"` doubled, or tokenised and re-quoted. Golden tests
  cover `a" OR b`, `NEAR(x y)`, `col:value` and a 10 KB term.
- `LIKE` patterns escape `%`, `_` and the escape character.
- SQLite hardening: `PRAGMA` values never come from input; `ATTACH` is unreachable; extension loading
  stays disabled; `PRAGMA trusted_schema=OFF`; `PRAGMA foreign_keys=ON`; per-connection caps on SQL
  length, expression depth and compound-select terms.
- **The importer connects to a user-supplied MySQL DSN.** It is admin-only and audited;
  `allowAllFiles=false` and `LOCAL INFILE` disabled — a malicious or compromised MySQL server can
  otherwise read files off the DKP host through the client-side `LOCAL INFILE` attack, a real and
  under-appreciated vector. TLS preferred. A read-only wrapper rejects anything but
  `SELECT`/`SHOW`/`DESCRIBE`/`EXPLAIN`. The connection is routed through the SSRF policy unless
  `DKP_IMPORT_ALLOW_PRIVATE=true`, which is the common case for a LAN MySQL and is therefore a
  documented, deliberate opt-in rather than a silent default.

### 7.3 Output encoding and XSS — the EQdkp weak spot, closed

EQdkp stored raw BBCode and HTML and rendered it at display time. That is the direct lineage of its
reflected-XSS advisory and most of its CVEs. **The owner-mandated portal and CMS scope materially
enlarges this surface**: articles, comments, the shoutbox, portal blocks, the team page, guild-bank
notes, recruitment applications and item-priority notes are all user-authored rich text, and the
recruitment form accepts it from an *unauthenticated* submitter. Everything below is therefore
specified concretely rather than as "sanitise input".

**Rule: no user-supplied HTML enters the system, ever.**

| Field class | Storage | Rendering |
|---|---|---|
| Names (character, item, rank, pool, event, token, role, article title) | plain text | React text nodes; length-capped; `name_norm` copy for matching, raw kept for display |
| Short notes (raid note, adjustment reason, bid note, dispute, shoutbox, comment) | markdown source, ≤ 4 KiB | server-rendered to HTML at write time |
| Long text (articles, guild rules, privacy page, MOTD, applications) | markdown source, ≤ 64 KiB | server-rendered at write time |
| Imported EQdkp BBCode | converted to markdown at import | same pipeline |

**The pipeline — `internal/richtext`, the only place HTML is produced from user input.** It ships in
**Phase 4** (raid notes are the first user-authored text), four phases before its heaviest consumer,
with its grep gate. Its `dangerouslySetInnerHTML` lint ban ships with it.

1. `goldmark` with **`html.WithUnsafe` NOT set**, plus `WithXHTML`. Raw HTML in the source is
   escaped, not passed through. No `WithHardWraps` surprises.
2. Output through **`bluemonday` with a hand-written policy, not `UGCPolicy` verbatim**:

   | Category | Allowed |
   |---|---|
   | Block | `p br hr h2 h3 h4 blockquote pre ul ol li table thead tbody tr th td` |
   | Inline | `strong em del code a img sup sub` |
   | Attributes | `a[href]`, `img[src alt width height]`, `td/th[colspan rowspan]` — and nothing else |
   | Never | `style`, `class`, `id`, `title`, `data-*`, any `on*`, `srcset`, `<h1>` (the page owns it) |
   | URL schemes | `http`, `https`, `mailto`; `RequireParseableURLs`; `rel="nofollow ugc noopener noreferrer"`; target blank on fully-qualified links |
   | `img[src]` | Must match `^/api/v1/(media\|artifacts)/` or the configured icon base. **No images from arbitrary origins** — simultaneously an XSS control, a privacy control (no tracking pixels in raid notes or articles) and an SSRF-adjacent control. |

3. **Store both** `body_md` (source, for editing) and `body_html` (rendered, for display). Only
   server-produced HTML is ever rendered. A sanitiser-policy change triggers a re-render migration
   job, so a tightening applies retroactively to existing content.
4. **Bidi and control characters are stripped** from all names and short text: U+202A–U+202E,
   U+2066–U+2069, U+200E/F, and C0/C1 controls except `\n` and `\t`. A right-to-left override in a
   character name lets a hostile member render as another member in the standings table — a genuine
   spoofing vector in a leaderboard product. Zero-width characters (U+200B–U+200D, U+FEFF) are
   stripped from names and the row is flagged in the reconciliation queue.
5. **Confusable-name warning.** `name_norm` (NFKC + casefold + strip `'` `` ` `` `-`, computed **in
   Go**, a plain indexed column — canonical §8) powers a warning when a new character normalises onto
   an existing one.

**Front-end controls (ESLint, blocking):** `dangerouslySetInnerHTML` banned outside a single
`<RichText>` component that renders only server-produced `body_html`; `eval`, `new Function` and
`document.write` banned; `href={userValue}` banned without the shared `safeHref()` helper. React 19
auto-escaping does the rest.

**Server-rendered surfaces** — the setup wizard, `/ops`, the public standings page — use
`html/template`, which is contextually auto-escaping. A test renders every template with a payload
set (`"><script>`, `javascript:`, a literal U+202E, `{{`, `</script>`) and asserts no unescaped
occurrence.

**CSV and spreadsheet export injection.** Officers export standings and open them in Excel. Any cell
whose value begins with `=`, `+`, `-`, `@`, tab or CR is prefixed with a single quote and the field is
quoted. Applies to every export path. Golden test with a character named `=cmd|'/c calc'!A1`.

*Mechanism for the whole section:* an XSS corpus (stored, reflected, mutation-XSS, bidi controls, SVG
upload, polyglot image) runs against every write path that accepts text, and a test asserts
`internal/richtext` is the only producer of HTML. It is a 1.0 exit criterion.

### 7.4 Untrusted parser input

Raid logs, `/who` pastes, RaidRoster dumps and channel dumps are **attacker-influenceable text**: a
hostile member controls their own character name, their own chat lines, and — if they can upload —
the whole file.

- Parsers are stdlib-only, allocate bounded, and never `panic` on malformed input. A panic in a
  parser is a DoS; there is a `recover` at the job boundary plus a `goleak`-checked worker.
- **Go's `regexp` is RE2** — linear time, no backtracking — so parser regexes cannot ReDoS.
  Hand-written scanners can still be quadratic, so every parser is a `go test -fuzz` target with a
  committed seed corpus.
- Line length cap (4 KiB), line count cap per artifact, NUL and control stripping, invalid UTF-8
  replaced rather than rejected (real logs contain mojibake), and a per-artifact cap on distinct
  unknown names entering the reconciliation queue, so a hostile upload cannot create 50,000
  quarantine rows.
- **Parsed names are proposals, not entities.** Unknown names land in the reconciliation queue;
  `on_unresolved` defaults to `quarantine` and `create` is an explicit officer choice
  (canonical §12). The award is quarantined, never dropped, never silently attributed.

### 7.5 File uploads

| Type | Max size | Accepted | Handling |
|---|---|---|---|
| Log slice | 25 MiB | text | UTF-8-ish validation, control stripping, content-addressed |
| RaidRoster / `/who` paste | 1 MiB | text | same |
| Avatar / media-library image | 2 MiB | PNG, JPEG, WebP, GIF | re-encode, see below |
| EQdkp ACP backup zip | 2 GiB (configurable) | zip | see below |
| EQdkp `mysqldump` | 2 GiB | text or gzip | CLI-only, see below |

**Universal rules**

- `http.MaxBytesReader` applied **before any read**, per route. The limit is enforced by the server,
  never by trusting `Content-Length`.
- **Client filenames are never used for storage.** The path is
  `/data/artifacts/<sha256[0:2]>/<sha256>` — content-addressed, so path traversal is not merely
  blocked, it is *unrepresentable*. The original filename is display metadata, re-emitted on download
  as `Content-Disposition: attachment; filename*=UTF-8''<percent-encoded>`.
- **`Content-Type` and file extension are never trusted.** Type comes from magic-byte sniffing plus a
  format-specific parse.
- Artifacts and media are served by an authenticated handler with
  `Content-Type: application/octet-stream` (or `text/plain; charset=utf-8` for logs), `nosniff`,
  `Content-Disposition: attachment`, `Content-Security-Policy: default-src 'none'; sandbox`, and
  `Cross-Origin-Resource-Policy: same-origin`. There is no webroot and no static file server pointed
  at user content; the only static server is `http.FileServerFS` over the **embedded** SPA.
- Per-instance storage quota with a soft warning at 80% and a hard stop, surfaced in `dkp doctor`.

**Images — the highest-risk upload, because members and the media library both write them**

1. `image.DecodeConfig` first (cheap header read) → reject dimensions > 4096 in either axis or
   `w*h > 16_000_000` pixels. This is the decompression-bomb gate and it runs *before* full decode.
2. Full decode with the stdlib decoders into a bounded buffer.
3. **Re-encode server-side to WebP (PNG fallback) at ≤ 512×512.** Re-encoding *is* the sanitiser: it
   destroys polyglot files, appended payloads, and ICC/EXIF metadata including GPS coordinates — a
   real privacy leak from phone photos.
4. **SVG is rejected outright.** SVG is an XSS vector, not an image format, and no sanitiser for it
   is worth maintaining.
5. Animated GIF: first frame only, or reject. Configurable, default reject.

**Zip handling (EQdkp ACP backup) — Zip Slip and zip bombs**

- Reject entries whose name is absolute, contains `..`, a backslash or a NUL, or is not valid UTF-8.
- Reject any entry that is not a regular file: no symlinks, no devices.
- Caps: ≤ 5,000 entries; per-entry uncompressed ≤ 512 MiB; total uncompressed ≤ 4 GiB; per-entry and
  total compression ratio ≤ 100:1 — measured against bytes **actually written**, never the declared
  header size, which is attacker-controlled.
- Extraction is confined with **`os.Root` / `os.OpenRoot`**, which makes escaping impossible at the
  syscall level rather than by string checks. This is strictly stronger than `filepath.Clean`
  gymnastics.
- Target is a per-import temp directory under `/data/tmp/<ulid>/`, removed on completion or failure,
  with a startup sweep for orphans.
- Fuzz target over the zip reader, corpus including a 42 KB → 4.5 PB bomb, nested zips and Zip Slip
  samples.

**`--from-dump` is CLI-only and needs Docker.** Loading a `mysqldump` uses an ephemeral MariaDB
container, which needs a Docker socket. **Mounting the Docker socket into the DKP container is
equivalent to giving that container root on the host**, so it is never a documented deployment
option; the HTTP import endpoint refuses that mode with `501` and an explanation. The docs state this
constraint at the same volume as the feature.

---

## 8. Network surface

### 8.1 SSRF and `internal/net/safehttp`

Outbound surfaces: guild-configured **webhooks**, the **Discord notifier**, **avatar-by-URL** import,
the **item icon base**, **OIDC discovery and JWKS**, the optional **HIBP** range API, the **S3 backup
endpoint**, the **update check**, and the optional P99 seed importer. That is a large surface for an
app that "does not talk to the internet".

**`internal/net/safehttp` is the only place an `*http.Client` may be constructed** — grep-gated, and
it ships in **Phase 2**, because JWKS fetching for OIDC is the product's first outbound request.
Deferring the gate until after clients exist elsewhere is exactly the grandfathering this design
forbids.

| Control | Implementation |
|---|---|
| DNS-rebinding / TOCTOU proof | Validation happens in `net.Dialer.Control`, on the **actual IP the socket is about to connect to**. Any design that resolves, checks, then hands the hostname back to `http.Transport` is rebindable; this one is not. |
| Denied networks (default) | `127.0.0.0/8`, `::1`, `0.0.0.0/8`, `10/8`, `172.16/12`, `192.168/16`, `169.254/16` (including `169.254.169.254`), `100.64/10` (CGNAT/Tailscale), `224/4`, `240/4`, `::/128`, `fc00::/7`, `fe80::/10`, `::ffff:0:0/96` IPv4-mapped, `64:ff9b::/96` NAT64 |
| Schemes | `https` preferred; `http` only for hosts on an explicit allowlist |
| Ports | 80 and 443 only, plus explicit allowlist entries |
| Redirects | Max 3, **each hop re-validated by the same dialer**; `Authorization` and cookies stripped on cross-host redirect. **Webhooks follow zero redirects.** |
| Response | Size cap 1 MiB, total timeout 10 s, connect timeout 3 s, no `HTTP_PROXY` inheritance unless explicitly configured |
| Errors | Never echoed verbatim — a raw dial error is an SSRF oracle. Mapped to `502 upstream_unreachable` with the failure *class* only. |

`DKP_OUTBOUND_ALLOW_PRIVATE=false` by default. A deployment that genuinely needs an internal endpoint
sets a narrow CIDR allowlist (`DKP_OUTBOUND_ALLOW_CIDRS=10.0.5.0/24`), surfaced at `/ops`. Changing
it requires `admin.security.manage` + step-up, is audited, and is PAT-forbidden — so a leaked token
cannot relax the SSRF policy and then pivot. That key exists **because of this paragraph**: the
guarantee cannot hold under `admin.settings`, which also gates renaming the guild (canonical §6).

**Webhooks specifically:** HTTPS required unless the host is allowlisted; HMAC-SHA256 signature
(`t=<unix>,v1=<hex>` over `t.body`) with a 5-minute replay window so the *receiver* can authenticate
us; **payloads carry IDs only, never documents** — so an SSRF that does land somewhere internal
delivers a useless `{"topic":"bid_session.settled","event_seq":8821352}` rather than guild data.
Delivery bodies and responses are retained 30 days with headers redacted, and the delivery log is the
self-service debugging surface. A failing endpoint is dead-lettered after 5 attempts with a visible
admin queue, so a broken guild endpoint cannot become an outbound amplifier.

**One deliberate exception.** `dkp doctor`'s end-to-end SSE check fetches the operator's own
configured public URL, which on a home LAN resolves to a private address that `safehttp` denies.
Doctor therefore carries an **explicit, narrow allowlist entry for `DKP_BASE_URL` only**. Without it,
doctor reports a false failure on exactly the deployments it exists to help.

*Mechanism:* unit tests bind a server to `127.0.0.1` and use a DNS stub that returns a public IP
first and `127.0.0.1` second (the rebinding case); both are refused at dial time. Redirect-chain
re-validation and webhook zero-redirect each have their own test.

### 8.2 CSRF, CORS, framing, headers, CSP

**CSRF.** The API is JSON-only, cookie-authenticated for browsers and bearer-authenticated for bots.
Layered defence, no token ceremony:

1. `SameSite=Lax` + the `__Host-` prefix. Necessary, not sufficient.
2. **No GET mutates state.** An architectural test enumerates the registry and fails on any
   `GET`/`HEAD` whose permission implies a write. This is the assumption `SameSite=Lax` depends on
   and the most common way Lax-based defences fail.
3. **`Sec-Fetch-Site` / `Origin` check on every unsafe method for cookie-authenticated requests.**
   Accept `Sec-Fetch-Site: same-origin`; otherwise `Origin` must be present and exactly equal to the
   configured origin or an allowlisted CORS origin. **A missing `Origin` is rejected**, not allowed.
   This single check carries the most weight.
4. **`Content-Type: application/json` required** on mutating requests. `x-www-form-urlencoded`,
   `multipart/form-data` outside upload routes, and `text/plain` are rejected — these are the "simple
   request" forms that escape preflight.
5. Bearer requests are structurally CSRF-immune (browsers do not attach `Authorization` cross-site),
   and cookies are ignored when `Authorization` is present, so there is no confusion path.
6. Multipart upload routes additionally carry a double-submit token from a `__Host-dkp_csrf` cookie,
   because multipart is a simple request type and the `Origin` check would otherwise stand alone.

*Mechanism:* a suite issues cross-origin `POST`s with cookies attached across every combination of
present/absent `Origin`, `Sec-Fetch-Site` and content type, asserting `403` throughout.

**CORS.** Default: **no CORS headers at all**, same-origin only. The admin-configurable allowlist
holds **exact origin strings** — no wildcards, no suffix matching, no regex, because suffix matching
is how `evil-myguild.org` gets in. `Vary: Origin` always. `Access-Control-Allow-Credentials: true`
only for allowlisted origins. A test asserts `Access-Control-Allow-Origin: *` and
`Access-Control-Allow-Credentials: true` never appear on the same response.

**Framing.** `Content-Security-Policy: frame-ancestors 'none'` plus legacy `X-Frame-Options: DENY`,
everywhere, with no exception at 1.0. The `/embed/standings.js` widget and the parent-origin
allowlist it would need are deferred to 1.1; the public standings *page* ships in Phase 3 and is not
framable.

**Security headers**, applied by a single middleware and asserted by a golden-file test over three
representative responses (authenticated JSON, public HTML, artifact download), because header
regressions are otherwise invisible:

```
Strict-Transport-Security: max-age=31536000; includeSubDomains   [https only]
X-Content-Type-Options: nosniff
Referrer-Policy: strict-origin-when-cross-origin
Cross-Origin-Opener-Policy: same-origin
Cross-Origin-Resource-Policy: same-origin
Permissions-Policy: accelerometer=(), camera=(), display-capture=(), geolocation=(),
                    gyroscope=(), microphone=(), midi=(), payment=(), usb=(),
                    serial=(), bluetooth=(), interest-cohort=()
X-Frame-Options: DENY
X-Robots-Tag: noindex, nofollow                                  [/api/* and authenticated pages]
Cache-Control: no-store                                          [authenticated JSON and HTML]
```

HSTS `preload` is **not** added by default and is documented as opt-in only, with a warning that it
commits every subdomain of the operator's domain to HTTPS — a self-hoster running `nas.example.org`
on plain HTTP will otherwise brick their own network.

**Content-Security-Policy.** The SPA is built with hashed filenames and **no inline scripts**;
runtime configuration comes from `GET /config.json`, not an inline `<script>` blob. That is a
deliberate build constraint, made precisely so the policy can be strict:

```
default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self';
font-src 'self'; connect-src 'self'; manifest-src 'self'; worker-src 'self';
form-action 'self'; frame-ancestors 'none'; base-uri 'none'; object-src 'none';
upgrade-insecure-requests;                       [https only]
report-uri /api/v1/csp-report; report-to csp
```

No `unsafe-inline`, no `unsafe-eval`, no `data:` or `blob:` in `script-src`/`style-src`, no wildcards.
Because the icon origin and CORS origins are runtime-configurable, the policy is **assembled at
runtime from config**, so a property test asserts the assembled application policy never contains
`*`, `unsafe-inline`, `unsafe-eval`, `data:` or `blob:` for script or style, for any configuration
input.

- Artifact and media downloads: `default-src 'none'; sandbox; frame-ancestors 'none'`.
- `/docs` (embedded Scalar): if the vendored bundle needs inline styles, the relaxation is
  `style-src 'self' 'unsafe-inline'` **scoped to `/docs` only**, and a test asserts application
  routes never inherit it. Nonce the style tag at build time if feasible.
- Rollout: ship `Content-Security-Policy-Report-Only` for one minor release alongside the enforcing
  header. `/api/v1/csp-report` is rate-limited (10/min/IP), size-capped (8 KiB) and stored in a ring
  buffer visible at `/ops` — never in an unbounded table, which is a classic self-inflicted DoS.
- Post-1.0: `require-trusted-types-for 'script'`. React 19 tolerates it and it removes the DOM-XSS
  sink class entirely. Not a 1.0 blocker.
- **Honest caveat:** `connect-src 'self'` means the "SPA runs detached against a remote instance"
  argument holds in *development*, or when the operator allowlists the target origin. Say that
  rather than overselling it.

### 8.3 Reverse proxy, TLS and `X-Forwarded-For`

Three supported topologies, each with a copy-pasteable config: behind a reverse proxy terminating TLS
(the documented default), behind Cloudflare Tunnel (popular with home users, documented including its
XFF implications), and direct TLS. In-process ACME is documented as an option rather than shipped —
it has four common failure modes and generates support tickets exclusively from the small share who
enable it.

**XFF trust: the default is zero trust.**

- `DKP_TRUSTED_PROXIES` is **empty by default**. With it empty, `X-Forwarded-For`,
  `X-Forwarded-Proto`, `X-Real-IP` and `Forwarded` are **ignored entirely**; the client IP is
  `RemoteAddr`.
- A request arriving with `X-Forwarded-For` while the list is empty logs a **one-time** warning naming
  the fix, surfaces it at `/ops` and in `dkp doctor`, and continues ignoring the header. Not
  per-request — that is its own DoS.
- When configured, the algorithm is: **walk `X-Forwarded-For` right to left, popping entries whose IP
  is in a trusted CIDR; the first untrusted entry is the client IP.** Never the leftmost entry. If
  `RemoteAddr` itself is not in a trusted CIDR, ignore the header entirely regardless of config.
- `X-Forwarded-Proto: https` from a trusted proxy sets the effective scheme, which turns on `Secure`
  cookies and HSTS. From an untrusted peer it is ignored.

**Why this default matters concretely:** a spoofed `X-Forwarded-For` (a) defeats per-IP rate limiting
on the login endpoint by giving the attacker unlimited distinct "IPs", and (b) **poisons the audit
log**, letting an attacker attribute their actions to an officer's home IP. Both are silent. A
permissive default here is the kind of thing that ships in v1 and is never revisited.

*Mechanism:* an integration test sends `X-Forwarded-For: 1.2.3.4` from an untrusted peer and asserts
both the audit row and the rate-limit key use the real `RemoteAddr`. `dkp doctor` runs an end-to-end
proxy check: `X-Forwarded-Proto` sanity, SSE through the actual proxy (naming nginx `proxy_buffering`
explicitly when it fails), TLS expiry, and Discord redirect-URI match against `DKP_BASE_URL`.

---

## 9. Secrets, bootstrap and audit

### 9.1 One root key, HKDF subkeys

The `FROM scratch` image contains a binary, CA certs and tzdata. No bundled config, no default key,
no seeded admin.

On first boot, if `/data/secrets.json` does not exist, generate **one 32-byte root key** from
`crypto/rand`, written `0600` in a `0700` directory owned by 65532. Everything else is derived:

```
root_key ──HKDF-SHA256──┬─ info="dkp/pat-pepper/v1"      → PAT / feed-token HMAC key
                        ├─ info="dkp/webhook-sign/v1"    → outbound webhook HMAC key
                        ├─ info="dkp/totp-enc/v1"        → AES-256-GCM for TOTP seeds
                        ├─ info="dkp/oauth-token-enc/v1" → AES-256-GCM for stored provider tokens
                        ├─ info="dkp/state-sign/v1"      → OAuth state, CSRF, cursor signing
                        ├─ info="dkp/sse-ticket/v1"      → SSE handshake ticket signing
                        └─ info="dkp/backup-enc/v1"      → optional backup encryption
```

One thing to back up, per-purpose separation, no key reuse across contexts. `crypto/hkdf` is in the
standard library, so this needs no dependency.

Rotation, stated honestly:

- **Signing and encryption subkeys rotate cleanly:** keep the previous key for verify/decrypt during
  a drift window, sign and encrypt with the new one, re-encrypt at-rest ciphertexts in a background
  job.
- **The PAT pepper cannot be rotated in place** — the plaintext secret is not stored, so old hashes
  cannot be re-peppered. Each token row carries `pepper_kid`; rotation mints a new kid used for *new*
  tokens, marks existing tokens `pepper_stale`, and surfaces a "rotate these N tokens" task. Say this
  rather than implying a magic rotation.

If `/data/secrets.json` exists but is unreadable or malformed, **refuse to start**, naming the file
and the recovery path. Never silently regenerate: that invalidates every token and session and looks
like a mass logout bug. If the file mode is not `0600`, refuse to start.

**Configuration.** `DKP_`-prefixed env vars plus an optional `/data/dkp.env` (`0600`).
`DKP_<NAME>_FILE` is supported for every secret-valued setting so Docker and Kubernetes secrets work
without putting values in the environment, where they leak into `docker inspect`, crash dumps and
child processes. **Secrets are a type, not a convention:** `type Secret string` with redacting
`String()`, `MarshalJSON` and `LogValue`. A test marshals the whole `Config` and asserts no known
secret appears; a second does the same for `/ops` and `dkp doctor --json`.

**Access-log redaction.** Never log `Authorization`, `Cookie`, `Set-Cookie`, `X-DKP-Signature`, or the
`atoken`/`key`/`token` query parameters; never log request bodies for `/auth/*`, `/tokens` or
`/setup`. *Mechanism:* a test posts a known password through the login endpoint against a capturing
`slog` handler and asserts the password appears nowhere in the captured records. This test catches the
most common real-world credential leak. `dkp support-bundle` has its own canary test: seed canary
strings, generate the bundle, grep every file, any hit fails the build.

### 9.2 First-run bootstrap — the default-admin-password anti-pattern, killed

There is **no default password, ever**:

1. On boot with zero users, generate a 32-byte bootstrap token, print it to stdout (so
   `docker logs dkp` shows it) *and* write it to `/data/first-run-token.txt` (`0600`). Store only its
   SHA-256.
2. **While unbootstrapped, every route except `/setup`, `/healthz`, `/readyz` and static assets
   returns `503 setup_required`.** There is no half-open state where an unconfigured instance exposes
   an API.
3. `/setup` is a server-rendered form, not the SPA — it must work before any bundle, session or token
   exists. The operator **pastes the token into a POST form**; it is never accepted as a query
   parameter, because query strings land in proxy logs, browser history and `Referer` headers.
4. Constant-time comparison. Five attempts total, then the token self-destructs and the operator must
   run `dkp admin bootstrap --new-token` on the host — which proves host access and is the right bar.
5. TTL 60 minutes from boot, renewable by restart.
6. On success: create the first `admin.owner` with a policy-compliant password (and optional
   immediate TOTP enrolment), delete the token and the file, write an audit row, and make `/setup`
   return `410 Gone` permanently unless explicitly re-armed from the CLI.
7. `dkp doctor` **fails** if a bootstrap token file still exists after bootstrap completed.

The desktop path (`dkp serve` on a Windows raid PC) has identical properties and additionally opens
the browser to `/setup` with the token shown in the console.

### 9.3 Audit and non-repudiation, with its honest limits

**What is immutable**

| Table | Mutability |
|---|---|
| `ledger_batch`, `ledger_entry` | **Append-only**, `BEFORE UPDATE OR DELETE … RAISE(ABORT)` |
| `bid` | Append-only. Retraction is a new row. |
| `audit_log` | Append-only, same trigger |
| `artifact` bytes | Content-addressed, immutable by construction. Purge sets `purged_at` and removes bytes, leaving the hash and provenance. |
| `event_outbox` | Append-only; pruned by a job that writes an audit row |
| `balance_snapshot` | The **only** mutable derived data. Rebuildable from the log and verified nightly — but **load-bearing, not droppable** ([ADR-0023](../adr/0023-balance-snapshot-is-load-bearing.md)), so the nightly replay is a control rather than hygiene |

*Mechanism:* an integration test executes `UPDATE ledger_entry SET amount_cp = 1` and asserts the
statement raises; likewise `DELETE FROM audit_log`. Writing the trigger is not enough — the test is
what stops a future migration from silently dropping it.

**Audit content.** Every mutation, plus a specific set of reads, writes one row carrying: gapless
`seq`, `at` micros, actor kind (`user`/`token`/`system`/`boot`/`import`) and ids, client IP and its
truncation marker, UA family, request id, `operation_id`, **which permission authorized this**,
resource type and id, action, result (`allow`/`deny`/`error`), a `reason` (mandatory for destructive
and self-beneficial actions), before/after JSON for config and role diffs, and `prev_hash`/`hash`.
There is no `guild_id` column (canonical §9).

**Reads that must be audited:** any officer read of a sealed bid before reveal; backup download;
audit export; PII export; another member's email or IP history; token mint; import-report download.

**Self-dealing flag.** The ledger writer computes `actor_is_beneficiary` and sets it on both the batch
and the audit row. It drives the UI badge and the Discord message wording. It is what replaces the
cut two-person rule (§4.5).

**Hash chain and off-box anchors.** `hash = SHA-256(prev_hash || canonical_json(row_without_hash))`,
maintained independently for `ledger_batch` and `audit_log`, each with its own chain head in
`dkp_meta`.

The limitation must be stated, not glossed: **an actor with filesystem access can rewrite rows and
recompute the chain.** A local-only hash chain proves nothing against a local adversary. The fix is
publication:

1. A daily job emits an anchor `{chain, seq_range, head_hash, row_count, at}`.
2. Anchors are appended to `/data/audit-anchors.log`, **posted to the guild's Discord channel via
   webhook**, and optionally emailed to the officer list.
3. `dkp verify-ledger` recomputes both chains and compares them against the last N published anchors,
   reporting the exact `seq` at which divergence begins.
4. The nightly `verify` job runs this and raises a visible admin alert on mismatch.

**Where the honesty is load-bearing:** on an instance with **no Discord webhook and no SMTP
transport configured, daily anchoring silently does not exist.** The append-only file is on the same
volume as the database and inside the same actor's control, so it adds nothing against the only
adversary anchoring is for. Therefore:

- `dkp doctor` **fails** — not warns — with `audit_anchor_unconfigured` when no off-box anchor channel
  is configured, and prints the two ways to fix it.
- `/ops` shows anchor status and the timestamp of the last successful publication.
- The docs say **"tamper-evident when an off-box anchor channel is configured"** and never claim
  tamper-evidence unconditionally. Any marketing copy that drops the conditional is a bug.

**The officer-covers-tracks scenario, walked through**

| Attempt | Outcome |
|---|---|
| `DELETE FROM audit_log` via the API | No such endpoint exists at any permission level. EQdkp's `a_logs_del` is a named anti-pattern. |
| `UPDATE ledger_entry` via the API | No such endpoint; the trigger raises regardless. |
| Reverse their own suspicious batch | Allowed and normal — but the reversal is itself a batch, the original stays visible struck through, and both appear in Discord. Cover-up by reversal is *louder* than the original. |
| Delete an artifact so the tick cannot be checked | Purge keeps the hash, writes an audit row, and is broadcast. Members see "the evidence for tick 6 was deleted by X on date Y". |
| Prune the audit log via CLI | `dkp audit prune --before` writes a **gap marker** recording the deleted range, row count and boundary hashes, and requires interactive confirmation. Deleting audit rows leaves a scar. |
| Edit the SQLite file directly | Detected by chain verification against published anchors and by comparison with off-box backups — **only if anchoring is configured.** Not preventable. Documented as such. |

**The strongest control in this section is not cryptographic.** Every member can read every ledger
row, every attendance roster and every retained artifact, and every mutation is broadcast within
seconds. Transparency to 60 motivated, self-interested observers is a better auditor than any hash
chain.

### 9.4 Backups are crown jewels

A backup contains password hashes, PAT HMACs, encrypted TOTP seeds, every email, every IP in the
audit log and every uploaded log.

- `dkp backup` writes `0600` into `/data/backups/`, optionally encrypted — with the loud caveat that a
  backup encrypted with the on-volume key is useless if the volume is lost, so the docs push
  operators toward a recipient key they control.
- **The root key is included only when the backup is encrypted to an external recipient**; otherwise
  it is excluded and `dkp restore` prompts for it. Otherwise a leaked plaintext backup hands over the
  PAT pepper too.
- Download requires session + step-up + `admin.backup`, is PAT-forbidden, is audited, and streams.
  No long-lived signed URLs.
- `dkp restore --dry-run` reports what would be replaced and refuses a backup whose schema version
  exceeds the binary's.

---

## 10. Rate limiting and abuse controls

In-process token buckets, no Redis, keyed by principal where available and by client IP only where
the IP is trustworthy (§8.3). Defaults matter because nobody changes them.

| Surface | Default | Notes |
|---|---|---|
| Login (per account) | 5/min, progressive delay to 30 s | plus a 250 ms response floor |
| Login (per IP) | 20/min | only when the IP is trustworthy |
| TOTP verify | 5/min/account | 10 consecutive failures → password re-auth |
| Password reset request | 3/h/account, 10/h/IP, 50/h instance | always `202` |
| Bootstrap token | 5 attempts total | then self-destruct |
| Registration / claim | 3/h/IP; self-registration off by default | invite and claim-link model |
| PAT reads | 600/min | per-token override; `RateLimit-*` and `Retry-After` on `429` |
| PAT writes | 120/min | separate bucket |
| Compat shim | 60/min | deliberately harsher than v1 |
| Bid placement | 30/min/account, burst 5/s | bots hammer during anti-snipe |
| Artifact upload | 20/h/principal, 200/h instance | plus the storage quota |
| Recruitment application (unauthenticated) | 3/h/IP, 30/h instance | Phase 7 |
| Shoutbox / comments | 20/h/person | Phase 8 |
| Import job | 1 concurrent | per-job lock; SQLite has one writer anyway |
| SSE connections | 5/session, 50/instance | plus the send buffer and `resync` eviction |
| CSP reports | 10/min/IP, 8 KiB | ring buffer, never a table |
| Unauthenticated total | 200/min instance-wide | shed before argon2 |
| argon2 concurrency | `max(2, NumCPU)` semaphore | memory-exhaustion gate |

**HTTP server timeouts** — Go's zero values are all "no timeout", which is a Slowloris invitation:

```
ReadHeaderTimeout: 10s   ReadTimeout: 60s (300s on upload routes)
WriteTimeout: 60s (0 for SSE, handled by a per-connection context deadline)
IdleTimeout: 120s        MaxHeaderBytes: 32KiB
```

Other abuse controls: dispute creation 5/day/person; free-text length caps everywhere; a
reconciliation-queue insertion cap per artifact; the per-instance artifact storage quota; and webhook
dead-lettering after 5 failures.

`/metrics` is **disabled by default** (canonical §14). When enabled it binds a separate listener and
requires `DKP_METRICS_TOKEN`. It is never public and never gated by a PAT scope.

---

## 11. Privacy and retention, kept proportionate

### 11.1 Inventory

| Data | Where | Sensitivity | Default retention |
|---|---|---|---|
| Email address | `app_user` | medium | life of account |
| Password hash | `user_identity` | high (credential) | life of account |
| TOTP seed (encrypted) | MFA credential | high | life of enrolment |
| Discord id, handle snapshot, avatar URL | `user_identity` | medium | life of link |
| Discord access/refresh token (encrypted) | `user_identity` | high | only if role sync is enabled |
| **Client IP** | `session`, `audit_log`, token `last_used_ip` | medium | **truncated to /24 (v4) or /48 (v6) after 90 days** |
| User agent | `session`, `audit_log` | low | family string only, never the full UA |
| Character names, levels, classes | roster | pseudonymous but linkable | life of guild |
| Officer notes on members | `person` | **high in practice** — officers write candid things | life of account; included in the export |
| **Uploaded raid logs** | `artifact` | **highest surprise value** — they contain other players' names and `/tell` content, including people who never used this product | **180 days** |
| Avatar / media images | blob store | low (EXIF destroyed on re-encode) | life of account |

### 11.2 Raw artifacts: retained by default, with redaction

A log slice is simultaneously the best provenance evidence in the system and the biggest third-party
privacy exposure. The resolution (canonical §11):

- **Raw artifacts are retained by default, 180 days**, with **`/tell`-line redaction at ingest** and a
  guild opt-out. Private tells are the lines least likely to be needed for DKP and most likely to be
  embarrassing.
- Members can download the artifacts attached to any raid they can read. "Any member can download the
  dump behind this tick" is the strongest anti-drama mechanism in the design, and it dies under
  parse-and-discard.
- `parse_line` is pruned at 90 days, so `item_instance.parse_line_id` is nullable with
  `ON DELETE SET NULL` — a hard FK to a pruned table fails under `foreign_keys=ON`.

*An earlier draft of this document specified parse-and-discard by default. That is superseded: it
contradicted the transparency control this same document relies on in §9.3.*

### 11.3 Export and erasure

- `GET /api/v1/persons/{id}/export` — self-service, or admin and audited — produces a zip of every
  row referencing that person: account, identities, characters, ledger entries, attendance, awards,
  adjustments, bids, disputes, notes, sessions, audit rows where they were the actor, and artifacts
  they uploaded. JSON plus a human-readable summary.
- **Erasure is pseudonymisation, not deletion**, and the reasoning belongs in the docs so the operator
  can defend it: the ledger is append-only and, under zero-sum pools, *mathematically
  interdependent* — deleting one member's entries corrupts every other member's balance.
  `dkp person forget <id>` replaces name, email, Discord id, avatar, notes, IPs and user agents with
  `deleted-person-<ulid>`; drops sessions, tokens, MFA credentials and identities; keeps ledger rows
  keyed to the now-anonymous account. Audited, irreversible, and preceded by a dry-run report showing
  exactly what will be scrubbed and what will remain.
- Character names are in-game public identifiers and are not scrubbed by default; scrubbing them
  breaks the guild's raid history entirely. An explicit `--scrub-character-names` flag exists for the
  rare hard case, with that warning attached.

### 11.4 The self-hoster's position, stated once and plainly

`docs/operations/privacy.md`, about a page:

- **The guild is the data controller. Dragon Kill Party is software, not a service.** There is no
  vendor to share responsibility with.
- **What leaves the box by default: nothing except the daily update check.** Everything else is
  whatever the guild configures — Discord webhooks, SMTP, S3 backups, OIDC. The update check is a
  plain `GET` of a static JSON with no query parameters and no body; the remote host necessarily
  learns the requesting IP and the version in the User-Agent. Opt out with
  `DKP_UPDATE_CHECK=false`, and that fact appears in the settings UI next to the toggle.
- A ready-to-edit privacy-notice template, a `/privacy` page rendered from guild-editable markdown,
  the inventory table above, and the retention knobs.
- **Deliberately not built:** a consent-management platform, cookie banners (there are no third-party
  cookies and no analytics), DSAR tooling, telemetry of any kind. The export command, the forget
  command, the retention settings and the inventory are the proportionate set for a 60-person hobby
  guild.

---

## 12. Supply chain

| Ecosystem | Control |
|---|---|
| Go | `go.mod` + committed `go.sum`; `GOFLAGS=-mod=readonly`; checksum database on; `go mod verify` in CI; toolchain pinned by the `toolchain` directive |
| npm / pnpm | `pnpm-lock.yaml` committed; `--frozen-lockfile` everywhere including the Dockerfile; **`ignore-scripts=true`** in `.npmrc` (lifecycle scripts are the primary npm attack vector) with a reviewed allowlist if one is genuinely needed; a release-age delay of ~3 days *(not yet configured)*. **Verified at adoption** on pnpm 9.15.9 (issue #87): the setting name is `ignore-scripts`, it suppresses dependency lifecycle hooks but **not** `pnpm run <script>`, and pnpm reads the project `.npmrc` from the directory holding `package.json` **without walking upward** — so `web/.npmrc` is the file that binds and a repository-root one alone would be a decoration. Both are committed; the reviewed allowlist, if ever needed, is a `pnpm.onlyBuiltDependencies` entry naming the one package, never `ignore-scripts=false`. |
| GitHub Actions | **Pinned to full commit SHAs, never tags.** A tag is mutable; this is the cheapest high-value CI control there is. Renovate keeps SHAs current with a comment naming the version. |
| Docker base images | Pinned by digest |
| Runtime image | `FROM scratch` — no base image, therefore no base-image CVE feed |

**CI workflow hardening.** Workflow-level `permissions: contents: read`, elevated per job only where
needed. **No `pull_request_target` with a checkout of the PR head.** Fork PRs run with read-only
tokens and no secrets. Registry auth via OIDC, no long-lived token. `zizmor` runs over
`.github/workflows/` to catch template-injection sinks and unpinned uses. The release workflow is a
reusable workflow, which provides the build isolation SLSA Build L3 requires.

**SBOM, signing, provenance.** CycloneDX for the Go module graph and Syft for the image, both attached
to the release; cosign keyless over image and binaries; `docker buildx --provenance=mode=max` plus
`actions/attest-build-provenance`. **Verification is documented, not merely produced** — a provenance
chain nobody knows how to check is decoration, so `docs/operations/verify.md` gives the exact
`gh attestation verify` and `cosign verify` commands. Reproducibility via `-trimpath`,
`CGO_ENABLED=0`, `SOURCE_DATE_EPOCH`, with a weekly job that rebuilds the last release from its tag
and diffs the digest.

**Continuous monitoring.** Renovate (grouped weekly, security updates ungrouped and immediate);
`govulncheck` on every PR, whose call-graph analysis reports only *reachable* vulnerabilities and
therefore keeps the signal high enough that people act on it; `osv-scanner` across both lockfiles;
Trivy on the image (near-empty for scratch, but it reads Go build info out of the binary, so the
embedded module list is still scanned). A **licence gate** fails the build on any copyleft, non-commercial
or source-available licence in the runtime dependency graph, and the **AGPL firewall grep** fails on EQdkp
identifiers outside `internal/importer/legacy_names.go` and `internal/api/compat/` (canonical §15) —
an agent asked to "match EQdkp behaviour" will otherwise paste AGPL code helpfully and disastrously.

Of these, `govulncheck`, `osv-scanner`, the licence gate and the AGPL firewall run today; the nightly
`govulncheck` leg and Trivy do not. SECURITY.md carries the authoritative live/planned split.

The two vulnerability scanners are deliberately kept as two. `govulncheck` is reachability-aware and
Go-only; `osv-scanner` is reachability-blind and reads both `go.mod` and `web/pnpm-lock.yaml`. On the
Go graph that makes `osv-scanner` the noisier of the pair, which is precisely why it does not replace
`govulncheck` — and on the npm graph, where nothing looked at all before it, blindness beats absence.
Its first run found three advisories in transitive devDependencies (#133, #134, #135); each is waived
in `osv-scanner.toml` with a filed issue and an expiry date rather than by relaxing the gate.

**Vulnerability response** (`SECURITY.md`, short and actually followed): GitHub Private Vulnerability
Reporting or `security@`; acknowledgement 3 business days; triage 7 days with CVSS **and** a
plain-English "what an attacker can do to your guild"; fix targets Critical/High 14 days, Medium 30,
Low next release; coordinated disclosure at 90 days; GHSA for every security fix; supported versions
are the latest minor plus the previous minor for 90 days; no bug bounty, stated honestly.

**The single most valuable control for self-hosted software** is the in-product notification: the
daily update check returns `{latest, min_safe_version, security, advisory_url}`, and when the running
version is below `min_safe_version` the admin UI shows a **persistent, non-dismissible** banner,
`dkp doctor` fails, and `/ops` flags it. Every advisory ships a config-level workaround where one
exists (`DKP_COMPAT_ENABLED=false`, `DKP_OUTBOUND_ALLOW_PRIVATE=false`, revoke-all-tokens) so an
operator who cannot upgrade today can still act today. **No remote kill switch and no forced
auto-update** — both are backdoors.

---

## 13. When each control ships

Security controls are not a phase. Anything that constrains how code is written must exist before the
code it constrains, or it becomes a grandfathering exercise.

| Phase | Controls landing |
|---|---|
| **0** | Grep and lint gates (B3, B4); Actions pinned to SHAs; `ignore-scripts`; licence gate; AGPL firewall grep; `govulncheck`; secret scanning; `FROM scratch` image; migrate-on-boot snapshot and auto-restore. *Landed: the grep gates, SHA pinning, the licence gate, the AGPL firewall, `govulncheck`, `osv-scanner` over both dependency graphs, the advisory `atlas migrate lint`, and `ignore-scripts` (as a committed `.npmrc` default, not only as a flag at each call site). Outstanding: CI secret scanning, the image and migrate-on-boot.* |
| **1** | Append-only triggers **and the tests that assert they fire**; `actor_is_beneficiary`; mandatory `reason`; strategy purity gate |
| **2** | argon2id; sessions; **MFA/TOTP enrolment and step-up**; the permission catalogue; the capability floor; the authz matrix; first-run bootstrap; Discord OAuth and OIDC; **`internal/net/safehttp` and its grep gate**; audit hash chain; rate limiting; idempotency |
| **3** | Security headers and CSP on the server-rendered surfaces; the class-colour contrast validator; the i18n lint rule (no bare strings, so nothing user-facing bypasses escaping later) |
| **4** | **`internal/richtext` and the XSS corpus**; upload safety (bomb gates, `os.Root` extraction, re-encode, SVG rejection); artifact retention with `/tell` redaction; parser fuzz targets |
| **5** | MySQL read-only wrapper; `LOCAL INFILE` disabled; import commit as session + step-up; claim-token distribution warnings; compat-shim token class and `atoken` log redaction |
| **6** | Webhook HMAC signing and zero-redirect delivery; SSE ticket; dead-lettering |
| **7** | Every CMS write path through `richtext`; media re-encode; SVG rejected; unauthenticated application-form rate limits; comment moderation |
| **8** | `dkp doctor` security checks; support-bundle canary test; backup gating; the advisory-feed banner; an external review of `internal/auth` |

---

## 14. Pre-1.0 security checklist

Each box is a CI assertion or a `dkp doctor` check, not a manual review item.

**Authentication**
- [ ] argon2id `m=19456,t=2,p=1`, PHC-encoded, rehash-on-login; profile ladder; doctor timing check
- [ ] argon2 concurrency semaphore; the 200-concurrent-login memory test passes
- [ ] Password policy: 12–128, offline breach blocklist, no composition rules, no forced rotation
- [ ] Uniform login errors, dummy verify, 250 ms floor; the enumeration test passes
- [ ] TOTP: encrypted seeds, replay-prevented, recovery codes hashed and single-use, re-auth to enrol
- [ ] Step-up enforced on exactly the §3.4 list, asserted by an architectural test
- [ ] Discord OAuth: PKCE `S256`, single-use `state`, exact `redirect_uri`, keyed on provider `id`,
      **no email auto-linking**, `verified` checked, roles can never grant `admin.*`
- [ ] OIDC: `iss`/`aud`/`exp`/`nonce` verified, alg allowlist, `none` rejected, JWKS via `safehttp`
- [ ] Sessions: `__Host-` cookie, hash stored not plaintext, rotation on all six events, epoch bump
- [ ] Reset tokens hashed, single-use, 30 min, always `202`; every link from `DKP_BASE_URL`;
      `Host` mismatch → `421`

**Authorization**
- [ ] Every operation declares `Security` + `x-dkp-permission` + an explicit `operationId`
- [ ] `authz.Check` has no superadmin early return; a test asserts it
- [ ] Cannot grant a permission you lack; cannot edit your own roles; last-owner invariant
- [ ] The authz matrix is committed, CODEOWNERS-protected, `-update` refused in CI, covers every
      registered operation with no dead rows
- [ ] PAT-parity suite green
- [ ] `403` vs `404` policy encoded in the matrix, not in handler taste

**Tokens**
- [ ] HMAC-pepper at rest, `pepper_kid` per row, prefix indexed, constant-time compare
- [ ] Display-once; mint requires step-up; audited by prefix
- [ ] 365-day default expiry with 30/7/1-day warnings; rotation with grace; instant revoke
- [ ] **Capability floor** asserted by an architectural test over the registry
- [ ] Query-string tokens rejected except the compat shim; `atoken` redacted in logs (test)
- [ ] `dkp token revoke --reverse-batches` implemented and documented

**Input and output**
- [ ] Unknown request fields rejected; every array bounded; money as unquoted int64 centipoints;
      time plausibility window
- [ ] `SQL001`/`SQL002`/`SQL003` green; sort/filter allowlists; FTS5 escaping goldens; signed cursors
- [ ] Importer MySQL: `LOCAL INFILE` disabled, `allowAllFiles=false`, read-only statement wrapper
- [ ] `internal/richtext` is the only HTML producer (test); `body_md` + `body_html` both stored;
      re-render migration path exists
- [ ] Bidi, zero-width and control stripping on names; confusable-name warning
- [ ] `dangerouslySetInnerHTML` lint ban; `safeHref` helper; template XSS payload test
- [ ] CSV formula-injection prefixing (golden test with `=cmd|…`)
- [ ] Uploads: `MaxBytesReader` first, magic sniffing, content-addressed paths, `os.Root` extraction,
      ratio and entry caps, `DecodeConfig` bomb gate, re-encode, SVG rejected
- [ ] Parser fuzz targets with committed corpora

**Network**
- [ ] `safehttp` grep gate; dialer-level IP validation; rebinding test; webhooks zero-redirect
- [ ] Doctor's SSE check uses an explicit `DKP_BASE_URL` allowlist entry
- [ ] No GET mutates (architectural test)
- [ ] `Origin`/`Sec-Fetch-Site` enforced on unsafe cookie-auth requests; missing `Origin` rejected
- [ ] JSON content type required; form encodings rejected outside upload routes
- [ ] Cookies ignored when `Authorization` is present (test)
- [ ] CORS exact-match allowlist; `*`-with-credentials impossible (test)
- [ ] `frame-ancestors 'none'` everywhere at 1.0
- [ ] Security-header golden test over three response classes
- [ ] Runtime-assembled CSP contains no `*`, `unsafe-*`, `data:` or `blob:` for script or style
- [ ] `DKP_TRUSTED_PROXIES` empty by default; XFF ignored; right-to-left parsing when configured;
      the spoofing test passes

**Secrets and ops**
- [ ] Root key generated on first boot, `0600`, HKDF subkeys; refuse to start on malformed or loose mode
- [ ] `Secret` type redacts in `String`/`MarshalJSON`/`LogValue`; config-dump and `/ops` tests
- [ ] Log-redaction test: a posted password appears nowhere in captured logs
- [ ] Support-bundle canary test clean
- [ ] Bootstrap: `503` until bootstrapped, POST-only token, 5 attempts, 60-minute TTL, `410` after
- [ ] Container: non-root, read-only rootfs, `cap-drop=ALL`, `no-new-privileges`, no Docker socket
- [ ] Backups `0600`, gated + audited, root key excluded unless externally encrypted

**Audit**
- [ ] Append-only triggers **and the tests that assert they fire**
- [ ] Audited reads implemented (sealed bids, backups, audit export, PII, token mint)
- [ ] Hash chains plus daily anchors; `dkp verify-ledger` compares against published anchors
- [ ] **`dkp doctor` fails when no off-box anchor channel is configured**, and the docs never claim
      tamper-evidence unconditionally
- [ ] No endpoint deletes audit rows; CLI prune writes a gap marker
- [ ] `actor_is_beneficiary` on batches and audit rows

**Privacy and supply chain**
- [ ] Inventory published; retention job implemented; IP truncation at 90 days
- [ ] Artifacts retained 180 days with `/tell` redaction and a guild opt-out
- [ ] Export endpoint; `dkp person forget` with a dry run; `/privacy` page; update-check disclosure
- [ ] Lockfiles committed; `ignore-scripts`; release-age delay; Actions pinned to SHAs; digest-pinned bases
- [ ] SBOM + cosign + provenance + documented verification commands; reproducible-build diff job
- [ ] `SECURITY.md` published; the advisory feed drives the `min_safe_version` banner

---

## 15. Security gates in CI

The pipeline itself is specified in the CI/CD design; this is the security-owned subset, all
blocking on every PR unless noted. This table is the target state — SECURITY.md's "What we do
continuously" carries the authoritative list of which rows run today.

| Gate | Form |
|---|---|
| Reachable dependency vulnerabilities | `govulncheck ./...` — live, required, not `continue-on-error`. The nightly leg is not wired up yet |
| All-ecosystem vulnerabilities | `osv-scanner` over `go.mod` **and** `web/pnpm-lock.yaml` — live, required, not `continue-on-error`. Waivers are `[[IgnoredVulns]]` entries in `osv-scanner.toml`, each carrying a filed issue and an `ignoreUntil` expiry. `pnpm audit` is not planned alongside it: a second npm scanner is a second set of waivers to keep in step |
| Migration safety | `atlas migrate lint` — destructive, data-dependent and backward-incompatible change analysis over `db/migrations-sqlite/`. **Advisory** today (issue #131), advisory by construction rather than by `continue-on-error`; #136 tracks promotion. Additive to MIG001–003, the fresh-install fingerprint and the populated-upgrade gate, none of which Atlas can express |
| Go SAST | `gosec`, with every `#nosec` requiring a justification comment (asserted by a test) |
| Custom repo rules | `semgrep`: SQL string building, `http.Client` outside `safehttp`, `sql.Open` outside `store`, `time.Now` outside `clock`, `dangerouslySetInnerHTML`, missing `Security`, floats in ledger or strategy |
| Deep SAST | CodeQL (Go + JS), advisory pre-1.0, blocking after |
| Secret scanning | `gitleaks` on the diff; `trufflehog` over full history weekly; push protection on |
| Container scanning | Trivy on image and Go build info; Grype cross-check nightly |
| CI-config audit | `zizmor` over `.github/workflows` |
| **Authorization matrix** | Golden file, every principal × every operation |
| **PAT parity** | Recorded SPA sequences replayed with a scoped PAT |
| **Architectural gates** | `Security` + permission + `operationId` on every route; idempotency on mutating POSTs; zero-scope PAT → `403`; no hidden operations; no GET mutates; PAT-forbidden set equals the capability floor; step-up set equals §3.4 |
| **Trigger assertions** | `UPDATE ledger_entry` raises; `DELETE FROM audit_log` raises |
| Header and cookie goldens | Exact header set on three response classes; cookie flags on login |
| CSP assembly property test | No `*`, `unsafe-*`, `data:` or `blob:` for script or style, for any config |
| CSRF suite | Cross-origin POST × {`Origin` present/absent} × content types → `403` |
| SSRF suite | Dialer refuses loopback, private, link-local, CGNAT and mapped-v4; rebinding stub; redirect re-validation; webhook zero-redirect |
| Log-redaction test | Password, token and cookie never appear in captured `slog` output |
| Upload safety suite | Zip bomb, Zip Slip, symlink entry, absolute path, oversized image, polyglot PNG/JS, SVG, EXIF-bearing JPEG |
| XSS corpus | Stored, reflected, mutation-XSS, bidi controls, against every write path accepting text |
| Fuzzing | Log parsers, PHP-serialize reader, zip reader, cursor decoder, markdown sanitiser — smoke on PRs, 10 min each nightly |
| Concurrency and idempotency | Two simultaneous full-balance bids → exactly one success; 100× replay → one effect |
| Enumeration and timing | Login and reset responses identical for existing and non-existing accounts |
| Licence gate + AGPL firewall | `internal/licence` (`make licence-gate`), over **both** graphs; the EQdkp identifier grep (AGPL001) in `scripts/repo-gates.sh`. All live. Allowlist-based and fail-closed (LIC001 denied, LIC002 unrecognised or not on the allowlist, LIC003 embedded third-party copyleft). **Go:** the runtime module graph, `go list -deps ./...` without `-test`, unioned across the release platforms — a module reachable only from a dependency's own test binary is not linked into `dkp`. **JS:** the whole `web/` graph via `pnpm licenses list --json`, not just `--prod`, against the same closed allowlist plus two reviewed permissive extras (Python-2.0, CC-BY-4.0); an SPDX expression passes only when every token in it does, which over-denies rather than admitting a copyleft branch. The JS half needs pnpm, so it prints a note and is skipped on the Go-only test runners; the required `security / licences` job installs Node and pnpm and runs unconditionally, which is where a bad JS licence fails the build |
| Goroutine leaks | `goleak` in `TestMain` for `events`, `webhook`, `bids`, `jobs`, `server` |

**Two meta-rules protect the tests themselves**, because in an agent-heavy codebase the fastest path
to green is to weaken the oracle:

1. `test/golden/` and `test/fixtures/` are CODEOWNERS-protected, `-update` is refused when `CI=true`,
   and a test asserts the fixture count is non-decreasing.
2. Adding a route, a permission, a fixture or a `#nosec` without the corresponding matrix or
   justification entry fails the build.

Security regressions should be **impossible to merge**, not merely *likely to be noticed*. The full
treatment is in [`04-testing.md`](04-testing.md).
