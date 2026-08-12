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
| [0016](0016-bespoke-licence-classifier.md) | A bespoke licence classifier | accepted | A similarity classifier reports a rider's permissive base; this one evaluates every pattern and any denial wins |
| [0017](0017-mockup-build-on-a-real-html5-parser.md) | Mockup build on a real HTML5 parser | accepted | `x/net/html` replaces a regex parser; the tokenizer rewrites, the tree builder verifies |
| [0018](0018-repo-gates-as-a-go-engine.md) | Repository gates as a Go engine | accepted | A typed rule catalogue plus `go/parser` analyzers; the rules must read a tree that does not build, which is what rules out `go/analysis` |
| [0019](0019-two-buckets-for-repository-scripts.md) | Two buckets for `scripts/` | accepted | Thin glue around a real CLI stays bash; anything that parses, rewrites or computes moves to Go |
| [0020](0020-two-test-lanes-and-a-nightly-shuffle.md) | Two test lanes, nightly shuffle | accepted | `-count=1` stays where a test can shell out; everything else caches, and `-shuffle=on` runs nightly |

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

**Enforced by:** `ADR001` in `scripts/repo-gates.sh`, in the `lint / repo` job. A PR that adds a new
*direct* requirement to `go.mod`, touches `deploy/Dockerfile` or `db/schema.hcl`, or adds a
top-level `internal/` package must contain either a new file under `docs/adr/` or an
`adr: n/a — <reason>` line in the PR body. The reason is required: a bare `adr: n/a` fails, because
harvesting the reason is the entire value of the waiver.

Two of those four are **path** triggers, deliberately broader than the sentence above them:
"a new port, volume or process" and "a new table or a changed constraint" are judgements no grep can
make, so any change to those two files asks the question. Over-asking costs one line in the PR body
and is visible; under-asking is invisible, which is the failure this gate exists to end. The `go.mod`
trigger *is* precise — it compares the direct requirements against the base revision, so a version
bump or a new indirect does not fire.

The gate reads the PR body, so it runs on pull requests only: on a laptop, and on `push` or
`merge_group`, it prints a skip naming the rule. `ci.yml` supplies the context and
`TestCI_LintRepoJob_PassesPullRequestContext` fails if that stops happening — this paragraph claimed
enforcement for months while nothing enforced it ([#85](https://github.com/prokopto-dev/dragonkillparty/issues/85)),
and a pinned CI line is what stops it becoming untrue again.

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

Budget: one screen, about 900 words, and 1,000 is the ceiling — `wc -w` over the whole file. **No
gate counts them**; this is guidance for you and your reviewer, and the template says so at the point
where you would otherwise trust a machine to catch it. Longer than the budget almost always means it
is two decisions in one file, which is the defect the number is a proxy for.
