# Authentication and scopes

**Audience:** bot author, officer, security reviewer.

Two credential types, one authorization model, and a hard floor on what any token can ever do.

> A **third** credential type — OAuth 2.0 device authorization grant tokens, for desktop overlay
> clients that can hold no secret — is proposed in
> [`../design/08-nparse-plus-integration.md`](../design/08-nparse-plus-integration.md) but **not yet
> accepted**. It would need to land in Phase 2 alongside the rest of the auth surface. Until that
> decision is made, this page describes the whole credential surface.

## Credentials

| | Personal access token (PAT) | Browser session |
|---|---|---|
| Format | `dkp_pat_<8-char prefix>_<43 chars>` | `__Host-dkp_session` cookie |
| Transport | `Authorization: Bearer dkp_pat_…` | `HttpOnly; Secure; SameSite=Lax; Path=/`, no `Domain` |
| Belongs to | A **service account** | A person |
| Lifetime | 365 days default, 730 max | Idle 14 days, absolute 30 |
| Revocation | Immediate — one row update on the path every request reads | Immediate; rotated on login, MFA, password change and step-up |
| Carries | Scopes | Everything the person's roles grant, plus step-up when re-authenticated |

**Cookies are ignored entirely when `Authorization` is present.** The precedence is fixed, so
"send both and get the union" cannot work. A test sends a low-privilege bearer plus a
high-privilege cookie and asserts the bearer's rights apply.

**Query-string tokens are rejected** with `401 token_in_query_string`. The single exception is the
EQdkp compat shim, which accepts `?atoken=` because that is what fifteen years of existing P99 bots
send. Legacy tokens are a distinct class, rate-limited harder, logged with a deprecation counter
naming the prefix, and the access logger redacts `atoken` from every logged URL.

**Feed tokens** (`dkp_feed_…`) are path-embedded — `/feeds/{feed_token}/raids.ics` — because calendar
and RSS readers cannot set headers. They are read-only, single-purpose, independently revocable, and
scoped to exactly one feed. A test proves an `articles_rss` feed token can read nothing else.

## Why tokens belong to service accounts

A service account is a bot identity with a recorded human owner.

```
service_account(id, name, description, owner_user_id, created_at, disabled_at)
api_token(id, prefix, hash, name, service_account_id, user_id, scopes,
          expires_at, last_used_at, last_used_ip, rate_limit_rpm,
          created_by_user_id, revoked_at, revoked_reason, superseded_by_token_id)
```

- **The owner is for audit and notification, not authority.** Deactivating the owning user flags the
  service account `orphaned` and raises an owner-reassignment task. It does not revoke the token. The
  bot survives the officer leaving the guild, which is the most predictable failure in guild tooling
  and the entire reason for the indirection.
- **A token's rights are `owner_role_permissions ∩ token_scopes`.** Reducing the owner's role
  immediately reduces every token they minted.
- **Scope subsetting is enforced on mint.** You cannot mint a token with a scope you do not hold. An
  architectural test asserts it.

A person may still mint a token bound to themselves rather than a service account — for the rare
power user running a personal parser — but it is an escape hatch, not the pattern. A Discord bot that
acted as N people would have to hold and rotate N secrets.

## The permission and scope catalogue

There is exactly **one** source: `internal/authz/catalogue.go`. It generates the `permission` table
seed, the OpenAPI `x-dkp-permission` metadata, the PAT scope enum, the authorization matrix and
`docs/reference/permissions.md`. `role_permission` is foreign-keyed to `permission(key)`, so a
hand-written list that drifts is a **boot failure**, not a style problem.

**Permissions** are `<resource>.<action>` and narrow a *role*:

```
roster.read roster.write person.merge character.claim.approve
raid.read raid.create raid.update raid.finalize raid.tick.create raid.tick.delete
item.read item.award item.alias.manage
dkp.read dkp.adjust dkp.decay.run ledger.reverse
bid.read bid.manage bid.reveal_early
calendar.read calendar.write signup.manage
cms.read cms.write cms.moderate
import.run import.commit
webhook.manage token.mint token.revoke
admin.settings admin.security.manage admin.roles.manage admin.backup admin.owner
person.pii.read audit.read ops.read
```

**Scopes** are `<family>:<verb>` and narrow a *token*. They are deliberately coarser:

| Scope | Grants |
|---|---|
| `roster:read` | Persons, characters, ranks, roles, raid groups, claims, search |
| `roster:write` | Person and character CRUD, claim approval, merges, renames, application decisions |
| `raids:read` | Raids, ticks, attendance, artifact metadata and content, event types, reconciliation |
| `raids:write` | Raid and tick CRUD, finalize, connected raids, kill credits, reconciliation resolve, raid submissions |
| `dkp:read` | Balances, standings, ledger, attendance statistics, pools, strategies |
| `dkp:adjust` | Adjustments and their reversals, decay and cap runs |
| `loot:read` | Items, aliases, priority lists, awards, item history, guild bank |
| `loot:award` | Create and reverse awards, create items, aliases and priority lists, issue from the bank |
| `bids:read` | Bid sessions, boards, non-sealed bid lists, spendable balances |
| `bids:manage` | Open, close, resolve, override and cancel sessions; place and retract bids |
| `logs:ingest` | Artifact upload, parse preview, the parser catalogue |
| `calendar:read` · `calendar:write` | Calendar events and signups |
| `cms:read` · `cms:write` | Articles, comments, media, shoutbox, portal blocks, menu, theme, team |
| `events:subscribe` | The SSE stream and the replay endpoint. Per-topic authorization still applies |
| `webhooks:manage` | Webhook CRUD, deliveries, redelivery, secret rotation |

Two deliberate couplings, both because they move points:

- **`settle` on a bid session requires `bids:manage` *and* `loot:award`.** Running an auction and
  moving money are different powers.
- **`dkp:adjust` is separate from `loot:award`.** A bid bot must not be able to hand out points.

## The capability floor

**Effective capability = role permissions ∩ token scopes. There is no `admin:*` scope and no
all-powerful token.** This is the single biggest deliberate fix over EQdkp Plus, whose `api_key`
impersonates the first superadmin.

A PAT — *any* PAT, regardless of scopes — can never do the following. These operations have **no
scope at all** and require a browser session with step-up (re-authentication within 5 minutes):

| Forbidden to every token | Why |
|---|---|
| Create, modify or delete users, or change any credential | Prevents persistence |
| Mint, rotate or revoke tokens | Prevents self-propagation |
| Edit roles, permissions or role assignments | Prevents escalation |
| Change auth settings — OAuth/OIDC config, MFA policy, session settings | Prevents downgrade attacks |
| Download or restore a backup | Prevents wholesale exfiltration |
| Export or delete the audit log | Prevents cover-up |
| Read another member's email address or IP history in bulk | Data minimisation |
| Run or commit an EQdkp import | The import writes the whole ledger |
| Change outbound-request policy or the webhook allowlist | Prevents relaxing SSRF policy and then pivoting |

**Enforced by** `x-dkp-pat-forbidden: true` at the single authorization choke point, plus an
architectural test that iterates the operation registry and asserts every operation whose permission
is in canonical §6's capability-floor enumeration is PAT-forbidden:

```
token.mint  token.revoke  admin.security.manage  admin.roles.manage
admin.backup  admin.owner  person.pii.read  audit.read  import.commit
```

The rule cannot rot because the test enumerates the registry rather than a list, and derives the
flagged set from canonical §6 rather than from a copy of it kept here.

> **Corrected in Phase 0 PR 5.** This page, `docs/design/03-security.md` and
> `.claude/agents/api-contract-guardian.md` each carried a different set. This one omitted
> `admin.owner` and `token.revoke` — so a test written from it would have let a PAT revoke tokens
> and would have exempted the owner role. `admin.settings` is deliberately absent: the
> security-affecting half of it is now `admin.security.manage`, and the rest (renaming the guild,
> adding a server, recomputing a pool) is session-only *without* step-up.

There is no "a PAT may not self-deal" rule. `dkp:adjust` exists precisely to create adjustments.
Self-dealing is controlled by the `actor_is_beneficiary` audit flag, which is visible to members —
not by blocking the scope that makes the bot useful.

## Rotation and revocation

**Rotation is a first-class operation**, because "rotate the bot's token" is otherwise a guaranteed
outage.

```bash
curl -s "$DKP_URL/api/v1/tokens/01JZ0TKNBDBT00000000000001/rotate" -X POST \
     -b "$COOKIEJAR" -H "Content-Type: application/json" \
     -d '{"overlap_seconds": 86400}'
```

Both tokens work for the overlap window (default 24 h, max 7 days) and are linked by
`superseded_by_token_id` in `GET /tokens`. Watch `last_used_at` on the old one go stale before the
window ends, then end it early if you like. Idempotency history survives rotation, because the
idempotency scope key uses the **service account**, not the token — a retry mid-rotation still
replays instead of double-writing.

**Revocation is instantaneous.** Opaque tokens mean one row update on the path every request reads.
This is the concrete reason PATs are not JWTs: in a single-process self-hosted app, stateless
verification buys nothing and costs instant revocation.

- Automatic revocation at `expires_at`. Warnings at 30, 7 and 1 days.
- Automatic *warning*, never revocation, after 90 days unused — "the bot only runs on raid nights" is
  normal.
- `POST /tokens/{id}/panic` revokes and returns a dry-run reversal preview of everything that token
  wrote, in one call, reachable from the admin UI in two clicks.

## What a leaked bot token can and cannot do

Suppose `dkp_pat_a91f3c2b_…` with `raids:write loot:award dkp:adjust` ends up in a public gist.

**It can** create raids, ticks, awards and adjustments — that is, move points around. That is real
damage, and it is exactly the damage the ledger is built to survive:

- every action is attributed to `actor_token_id` on the batch and the audit row;
- every action is broadcast in real time to whatever the guild watches;
- every action is reversible by a compensating batch;
- **nothing it did can be erased**, because the ledger is append-only and enforced by a database
  trigger.

**It cannot** mint another token, escalate itself, change who can log in, read anyone's email, pull a
backup, or delete the record of what it did. Recovery is one command:

```bash
dkp token revoke a91f3c2b --reverse-batches --since 24h --dry-run
```

Read the dry-run diff, then run it without `--dry-run`.

**Reducing the blast radius further.** Pin the token to a CIDR if the bot runs from one host — most
guild bots do, and a pinned token is a mostly useless string off that host. Give it the narrowest
scopes that work: a `!dkp` command needs `dkp:read` and nothing else. Log the **prefix** at boot so
your own logs identify which token is misbehaving without containing a credential.

**Leak detection.** The `dkp_pat_` prefix is distinctive and greppable on purpose. `last_used_ip` is
recorded and a first-use-from-a-new-network notification fires. `GET /tokens/{id}/activity` gives a
per-token request log — operation, status, timestamp, IP — which is what makes "did the leaked token
do anything?" answerable in seconds rather than never.
