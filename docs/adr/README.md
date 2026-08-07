# Architecture decision records

An ADR records **a decision that a reasonable person would otherwise re-litigate**, together with the
options that lost and the price of the option that won. It is not documentation of how the system
works — that is `docs/reference/` and `docs/concepts/`. It is the answer you link when someone
asks "why is there no Postgres option?" or "why did you not just fork EQdkp Plus?" for the twentieth
time.

These are published, not kept in a private wiki. Self-hosters ask these questions in issue threads,
and one link is cheaper than one argument.

## The seed set

| # | Title | Status | One line |
|---|---|---|---|
| [0001](0001-go-single-binary-and-sqlite.md) | Go single binary and SQLite | accepted | The bake-off was close; Go won on self-hostability and agent loop speed, and cost us the contributor pool |
| [0002](0002-append-only-ledger.md) | Append-only ledger | accepted | Corrections are reversal batches; a DB trigger enforces it |
| [0003](0003-integer-centipoints.md) | Integer centipoints | accepted | `int64` points × 100, no floats anywhere, unquoted integers on the wire |
| [0004](0004-single-guild-per-instance.md) | One guild per instance | accepted | No `guild_id` column; the owner reversed an earlier multi-guild-ready choice |
| [0005](0005-api-first-no-bff.md) | API-first, no BFF | accepted | The SPA is a pure API client, proved by five CI gates |
| [0006](0006-rest-openapi-derived-from-code.md) | REST, spec derived from code | accepted | Huma v2 generates OpenAPI from the Go types the handler compiles against |
| [0007](0007-sse-over-websockets.md) | SSE over WebSockets | accepted | WebSocket `Upgrade` misconfiguration is the top self-hoster support ticket |
| [0008](0008-atlas-authors-goose-applies.md) | Atlas authors, goose applies | accepted | Atlas is a CI dependency; the officer installs nothing |
| [0009](0009-apache-2-0-and-dco.md) | Apache-2.0 and DCO | accepted | Permissive licence, no CLA, name and logo unlicensed |
| [0010](0010-agpl-clean-room-firewall.md) | AGPL clean-room firewall | accepted | Read their database, never their source |
| [0011](0011-opaque-pats-no-superadmin-token.md) | Opaque PATs, no superadmin token | accepted | Capability = role permissions ∩ token scopes; there is no `admin:*` |
| [0012](0012-english-only-at-1-0.md) | English only at 1.0 | accepted | A parity regression for German guilds, stated plainly, scaffolded from Phase 3 |
| [0013](0013-eqdkp-compat-shim.md) | EQdkp compatibility shim | accepted | ~700 lines so every existing P99 bot works on cutover day; deprecated at birth |
| [0014](0014-full-portal-parity-in-scope.md) | Full portal parity in scope | accepted | The owner overruled the recommendation to drop the CMS; Phase 8 can slip to 1.1 |
| [0015](0015-nocturne-dark-only-design-tokens.md) | Nocturne, dark-only, plain CSS and design tokens | accepted | Plain CSS with custom properties; one dark palette themed by overriding token values; status colours added |

## When an ADR is required

**Required** when a change:

- alters a public contract — API shape, wire format, CLI, config keys, backup format;
- adds a runtime dependency, process, port or volume;
- changes a data-model invariant or a rule of the ledger;
- picks between two or more viable libraries;
- takes more than about two weeks to reverse;
- or answers a question a reader would otherwise re-litigate.

**Not required** for ordinary features, refactors, bug fixes, new doc pages, or adding a strategy or
a parser that fits the existing interface. Those are the interfaces working as intended, and an ADR
per strategy would bury the fourteen above in noise.

**Enforced by:** a PR touching `go.mod` (a new *direct* dependency), `deploy/Dockerfile` (a new port,
volume or process), `db/schema.hcl` (a new table or a changed constraint), or adding a top-level
`internal/` package must contain either a new file under `docs/adr/` or an `adr: n/a — <reason>` line
in the PR body. The check is part of the `lint / repo` job.

## Numbering

Sequential four-digit numbers, `NNNN-kebab-title.md`, starting at `0001`. `0000-template.md` is the
template. On a merge conflict the **later** PR renumbers — volume is low enough that this is cheaper
than a date-based scheme, and numbers sort and cite better than dates.

Numbers are never reused, and a file is never renamed after merge: `ADR-0010` appears in
`CONTRIBUTING.md`, in `README.md` and in review comments, and those links must not rot.

## Status values

| Status | Meaning |
|---|---|
| `proposed` | Open for discussion. Not yet binding on anything. |
| `accepted` | Binding. The decision is in effect and the code is expected to match it. |
| `superseded by ADR-NNNN` | A later ADR replaced it. Both files link each other; the old text stays as written. |
| `deprecated` | The decision no longer applies and nothing replaced it — the question stopped existing. |

Never edit an accepted ADR to reflect a new decision. Write the new one, mark the old one superseded,
and link both. The value of the record is that it shows what was believed at the time, including the
parts that turned out to be wrong.

## Format

MADR 4, minimal template — see [`0000-template.md`](0000-template.md). Required sections: status and
date · context and problem statement · considered options (at least two, each with a real for and a
real against) · decision outcome · consequences split into good and **bad** · reversal cost.

Two of those carry the weight. **An ADR with no negative consequences is rejected in review** — it is
advocacy, not a record. And the reversal-cost line is the thing a future maintainer actually needs,
because the question is never "was this right?" but "what does it cost to change now?"

Budget: one screen, about 900 words, checked by the docs word-count gate. Longer than that almost
always means it is two decisions in one file.
