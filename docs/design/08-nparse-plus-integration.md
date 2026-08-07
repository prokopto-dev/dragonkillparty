# nParse+ integration

**Status:** design input.
`bids:place` is **accepted** and is in the canonical scope catalogue.
The **device authorization grant** and **`GET /me/summary`** are **proposed** and need an owner
decision before Phase 2 closes — see *Sequencing*.
**Normative tie-breaker:** [`00-canonical-conventions.md`](00-canonical-conventions.md).

## What this is

[nParse+](https://github.com/prokopto-dev/nparse-plus) is the P99 companion overlay — spell and buff
timers, a trigger engine, DPS meter, live maps, respawn timers — maintained by this project's owner
as a fork of [nomns/nparse](https://github.com/nomns/nparse). It is Python 3.12+ with a Qt UI, and it
ships an optional add-on system (Settings → Advanced, disabled by default) with a published SDK,
`nparseplus-sdk`, versioned independently of the app and supporting custom **windows, parsers and
pollers**.

**Both sides of this integration are ours.** That removes the usual integration risks — no upstream
negotiation, no waiting on a third party to expose a hook, and the plugin contract can be shaped to
fit rather than worked around. It also means the API and the plugin can be co-designed, which is
exactly the dogfooding relationship the public API needs.

The intent is to ship a Dragon Kill Party plugin for it, giving:

| Audience | Surface |
|---|---|
| Every raider | A small overlay insert: my current DKP, my attendance, my active bids, the next scheduled raid |
| Officers / leaders | A full control interface: submit raid attendance, open and confirm item bids, award loot, run ticks |

This is the most important integration input the project has received, because it replaces the
deferred first-party Discord bot as **the canonical API consumer** — the client that proves the
public API is complete. If the plugin needs a capability the API lacks, that is an API bug.

## Why it validates the architecture rather than straining it

The plugin needs exactly what the design already commits to, which is a good sign:

- It reads the player's own `eqlog_*.txt`, so **attendance submission is already the ingest path** —
  `POST /raid-submissions` with `logs:ingest`. No new mechanism.
- The overlay is a read-mostly poller, which the ETag-on-collections design already makes nearly
  free.
- Live bid boards are SSE, which already works through whatever the raider's network does.
- Officer control is *the whole API*. Nothing new — that is the point.

## What it does change

Five things need a decision they would not otherwise have needed.

### 1. Desktop clients are public clients — PATs are the wrong shape

A pasted PAT works for a server-side Discord bot. It does not work for sixty raiders each installing
an overlay: minting, distributing and rotating sixty tokens by hand is an administrative failure
waiting to happen, and a long-lived secret sitting in a desktop config file has a much worse leak
profile than one in a bot's environment.

**Proposed (needs an owner decision):** add the **OAuth 2.0 device authorization grant** (RFC 8628)
for desktop clients. The raider clicks "connect", the overlay shows a short code, they approve it
once in the browser where they are already signed in, and the overlay receives a short-lived access
token plus a refresh token bound to that device. Revocation is per-device and visible to the member.

This is a **third credential type** alongside session cookies and PATs, which
[`../api/auth-and-scopes.md`](../api/auth-and-scopes.md) currently says there are two of. Accepting
it means editing that page and the auth surface in Phase 2; declining it means the overlay
distributes a PAT per raider and accepts that operational cost. Decide before Phase 2 closes.

PATs remain the mechanism for server-side bots. This is an addition, not a replacement.

> This partially reverses the adversarial critic's cut of the OAuth2 client-credentials endpoint.
> That cut was correct — client credentials solved nothing PATs did not. The **device** grant solves
> something PATs genuinely cannot: interactive authorization for a client that can hold no secret.

### 2. A new scope: placing your own bid is not running the auction

The current catalogue has `bids:read` and `bids:manage`. The overlay needs a third, strictly weaker
capability: bid **as yourself**, up to your own balance, in an already-open session.

```
bids:place    place and retract your OWN bids; cannot open, close, resolve or settle
bids:manage   officer: open, extend, close, resolve, settle, override  (unchanged)
```

`bids:place` is one of three *self-scoped* scopes in the catalogue (with `bank:request` and
`draft:vote`, canonical §6) — it authorizes an action only
against the authenticated member's own accounts. That asymmetry needs an explicit test in the authz
matrix, because it is the one place a scope check is not sufficient on its own.

### 3. Character claiming becomes genuinely self-service

The design ranked claiming methods as: officer approval (always available) → roster-dump
verification → in-game nonce challenge (an enhancement, never a gate).

nParse+ inverts that. Because the plugin reads the local log file, the nonce challenge becomes
**native and reliable**: the server issues a nonce, the raider types `dkp-verify a7f3x` in guild
chat, the plugin reads its own log and posts the observation back. That is real possession evidence
for the character, not a self-assertion.

This is worth promoting from "enhancement" to the **preferred** path when the plugin is present,
because it removes the officer from the loop for the single highest-volume onboarding task.

### 4. A compact "me" endpoint

An overlay refreshing every 30 seconds across 60 raiders must not be 60 × N requests. Add one
aggregate read:

```
GET /api/v1/me/summary   ->  balance per pool, attendance % per window, my open bids,
                             my next scheduled raid, current raid state
```

One request, weak `ETag`, `304` on unchanged — so the steady-state cost of a full raid's overlays is
close to zero. This is the only aggregate endpoint in the API and it needs justifying in review
precisely because it is a composite; the justification is that it exists to make the *client* cheap,
not to make the server clever.

### 5. Rate limiting assumes the wrong shape

Current limits assume few tokens with high volume (bots). The plugin inverts it: **many tokens with
low volume**, all bursting simultaneously when a raid starts and when a bid opens. Per-token buckets
are right, but the defaults need to be set from this profile, and the SSE connection ceiling has to
accommodate one stream per raider rather than one per guild.

## Licensing — verified, and it is fine

| | |
|---|---|
| `prokopto-dev/nparse-plus` | **GPL-3.0**, inherited from `nomns/nparse`, which it forks |
| `nparseplus-sdk` (the plugin SDK) | **GPL-3.0-or-later**, declared in `sdk/pyproject.toml` |
| Dragon Kill Party | Apache-2.0 |

Owning the fork does **not** allow relicensing it: GPL-3.0 came from upstream and is sticky. A
plugin that imports `nparseplus-sdk` and is distributed must therefore be **GPL-3.0-or-later**.

That is not a problem, because the plugin is a *client*:

- The plugin lives in **its own repository**, licensed GPL-3.0-or-later — never in this one.
- It talks to Dragon Kill Party over the **public HTTP API only**. HTTP is an arm's-length boundary,
  not linkage, so nothing propagates to core.
- Nothing from the plugin repo is ever vendored back into core, and core takes no build- or
  test-time dependency on it — enforced by the same CI grep that guards `dkp-p99-seed`.

This mirrors the `dkp-p99-seed` posture: a separate repo on a different legal footing that core
must never depend on.

**One decision worth making deliberately:** if `sdk/` is entirely original work containing no code
derived from `nomns/nparse`, its licence is yours to choose, and relicensing it **LGPL-3.0** would
let third parties write permissively-licensed nParse+ plugins. That is a decision about the nParse+
ecosystem, not about Dragon Kill Party — and note it would not fully settle the question, because
whether an in-process plugin inside a GPL host is a derivative work is genuinely unsettled
regardless of the SDK's own licence. For the DKP plugin specifically it changes nothing: shipping it
GPL-3.0-or-later costs us nothing and sidesteps the ambiguity entirely.

Record the outcome in an ADR in the *plugin* repo, not this one.

## Sequencing

Nothing here blocks the roadmap, and none of it should be built early.

| Item | Phase | Why then |
|---|---|---|
| `bids:place` scope + the self-scoped authz matrix case | 2 | The catalogue is generated and FK-constrained; adding a scope later is a schema change |
| Device authorization grant | 2 | Same reason — it is an auth-surface change, and Phase 2 is where auth is settled |
| `GET /me/summary` | 3 | Needs balances and attendance to exist |
| Nonce claiming promoted to preferred | 4 | Needs the log parsers |
| Rate-limit defaults retuned | 6 | Needs the SSE and bid load profile to be real |
| **The plugin itself** | **post-1.0, separate GPL-3.0-or-later repo** | It is a client. It cannot be useful before the API it consumes is complete, and building it earlier would freeze the API around a single consumer |

Because both sides are ours, there is a real temptation to build the plugin early against a
half-finished API. Resist it: a single consumer co-evolving with the API is how an API ends up
shaped like its first client instead of like its problem domain. The Python SDK generated in Phase 2
is the right early deliverable — the plugin can then be written against a published, versioned
contract rather than against internals.

## What to do now

1. **Nothing in code.** The one genuinely time-sensitive item is already handled: the licence is
   verified (GPL-3.0-or-later, separate repo, HTTP-only).
2. **Add `bids:place` and the device grant to the Phase 2 scope catalogue**, because the catalogue is
   generated and FK-constrained — adding a scope after Phase 2 is a schema change, adding it during
   is a line in a Go `const` block.
3. **Feed the SDK's shape back into the API design.** nParse+ plugins are windows, parsers and
   pollers; the API surfaces that map to those are `GET /me/summary` (window), `POST
   /raid-submissions` (parser), and SSE (poller). If any of the three is awkward from Python, that
   is API feedback worth having before Phase 3, and it is available for the cost of a spike.

## Open questions

1. Should the overlay's officer surface be a genuine control interface, or a *launcher* that opens
   the relevant web page? The former duplicates a large UI surface in a second codebase and a second
   language; the latter keeps one implementation of every officer workflow. **Recommendation:
   launcher for anything complex — raid setup, reconciliation, decay, importer — and native only for
   the three things that must happen mid-combat without alt-tabbing: confirm a bid, award an item,
   start a tick.**
2. Does the plugin need offline queueing when a raider's connection drops mid-raid? The idempotency
   design already makes replay safe, but the client needs a documented retry contract — and an
   officer's queued attendance tick replaying twenty minutes late has domain consequences
   (`effective_at` versus `recorded_at`) that need a stated answer.
3. Does the overlay show *other* members' balances, or only the raider's own? Showing everyone's
   turns every raider's overlay into a standings client and changes both the caching profile and the
   privacy posture. **Recommendation: own balance by default; full standings behind an explicit
   guild setting.**
4. `nparseplus-sdk` versions independently and plugins call `check_compat()` against
   `nparseplus_sdk.__version__`. The DKP plugin therefore has *two* compatibility axes — the SDK
   version and the DKP API version. Decide early which one the plugin's version number tracks.
