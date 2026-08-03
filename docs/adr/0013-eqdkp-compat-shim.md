# ADR-0013 — Ship an EQdkp compatibility shim, deprecated at birth

**Status:** accepted · **Date:** 2026-08-03 · **Deciders:** owner

## Context and problem statement

Every P99 guild that would migrate already runs bots against EQdkp Plus's Plus Exchange API
(`/api.php`) — Castle Steward, bidbot2, jDKP, froakbot and a long tail of home-grown scripts, most
written by someone who has since left the guild. Cutover day is the moment a guild is most likely to
abandon the migration, and "all our bots are dead until someone rewrites them" is the most predictable
reason for that. The bots are the incumbent's real integration surface, whatever the documentation
says.

## Considered options

| Option | For | Against |
|---|---|---|
| A — No shim; bots migrate to `/api/v1` | One clean API; no legacy surface to maintain; forces everyone onto scopes and idempotency immediately | Cutover requires every guild to find or rewrite bots whose authors are gone. This is the single largest adoption risk in the plan |
| B — A permanent, fully-featured EQdkp-compatible API | Maximum compatibility; nobody ever has to change anything | A second first-class API forever, with its own auth model, its own bugs and its own drift. This is how the incumbent's API rotted in the first place |
| C — A bounded shim, deprecated the day it ships | Bots work on day one; the surface is small, capped and openly temporary | A legacy surface with no natural owner; it reproduces EQdkp's worst API decisions on purpose |

## Decision outcome

**Chosen: C.** `/api/compat/eqdkp/api.php` implements `points`, `raids`, `events`, `search`, `data`,
`user_chars`, `add_raid`, `add_item`, `add_adjustment` and `add_event` with EQdkp's exact
query-string dispatch, its `{status:1|0}` envelope, its XML-shaped JSON list forms
(`{"raid_attendees":{"member":[1,2,3]}}`) and its HTTP 200-on-error behaviour. **~700 lines.** It maps
legacy `member_id`s through the importer's persisted `import_id_map`, so a bot with hardcoded ids
keeps working.

Four boundaries make it safe to have:

1. **It translates onto v1 services.** It is not a second path into the domain and has no capability
   the v1 API lacks — so it cannot become the place where features land.
2. **It accepts `?atoken=`** — a query-string token that [ADR-0011](0011-opaque-pats-no-superadmin-token.md)
   rejects with `401` everywhere else — **because that is what existing bots send**. The exception is
   confined to this path, and each use logs a deprecation warning naming the token so the officer can
   migrate it.
3. **Deprecated from the day it ships**, rate-limited harder than v1, `Hidden: true` in the spec, and
   carrying a `/metrics` counter per function so an admin can see whether anything still calls it.
   Removal is a `/api/v2` event, announced with the same 18-month notice v1 gets.
4. **It ships in Phase 5, next to the cutover checklist** — the document that tells guilds to point
   their bots at it. Shipping the shim without the checklist means guilds cut over with dead bots
   anyway, which was a real sequencing bug in an earlier plan.

### Consequences

- Good, because cutover risk drops close to zero. Roughly 700 lines buy "your bots keep working"
  on the day that claim decides whether a guild migrates. It is the highest-leverage code in the
  project per line.
- Good, because it converts the biggest weakness of the stack choice — the thinnest contributor pool
  ([ADR-0001](0001-go-single-binary-and-sqlite.md)) — into a non-issue at cutover: nobody has to
  rewrite a Python bot in Go to migrate.
- Good, because calling v1 services means the shim can never expose something the public API does not,
  so it cannot rot into a parallel product.
- **Bad, because it deliberately reproduces bad decisions.** HTTP 200 on errors, RPC-over-query-string,
  and tokens in URLs that land in access logs, proxy logs and browser history. Every one of those is
  something this project otherwise treats as a defect.
- **Bad, because `?atoken=` is an absolute rule with an exception, and exceptions invite more.** The
  confinement is a code-review and grep discipline (`Hidden: true` is only legal on a fixed list), not
  a type-level guarantee.
- **Bad, because deprecated-from-birth code has no natural owner** and, without a removal date
  attached to a version, will still be here at 2.0 — quietly failing to keep up with the domain and
  quietly consuming review attention.
- **Bad, because it couples the shim to the importer permanently.** Legacy id resolution depends on
  `import_id_map`, so any importer change must keep that table's contract, and a guild that never
  imported has no ids for the shim to map.
- **Bad, because harder rate limits will surprise bot authors** whose polling loops were tuned against
  an unlimited EQdkp install, and the first symptom is a `429` in someone's raid-night Discord.

### Reversal cost

Deleting the shim is an afternoon and a major-version bump; the cost is entirely borne by users whose
bots stop. Never removing it is the more likely failure and costs a permanent maintenance tail.
