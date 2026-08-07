# ADR-0014 — Full portal and CMS parity stays in scope

**Status:** accepted · **Date:** 2026-08-03 · **Deciders:** owner (over the design team's and the adversarial reviewer's recommendation)

## Context and problem statement

EQdkp Plus is not only a DKP tracker — for most installs it *is* the guild's website: articles with
an editor, comments, categories, a media library, a shoutbox, portal blocks, a menu manager, a style
manager, a team page, a guild bank, recruitment and applications, and item priority lists. Both the
design team and the adversarial reviewer recommended dropping that surface and shipping a DKP
product. The owner overruled them. This ADR records both cases, because it is the decision most
likely to be re-litigated when a phase runs long.

## Considered options

| Option | For | Against |
|---|---|---|
| A — Drop the portal/CMS; ship DKP only | Guilds now live in Discord, so the news feed and shoutbox are arguably dead surfaces; it is roughly **20–25% less to build**; it is the **largest scope-creep risk in the plan** (ROADMAP R3); and it removes the **largest XSS attack surface in the product** — untrusted rich text, file uploads and a public unauthenticated application form (R14) | The parity claim becomes qualified, and a migrating guild loses their whole web presence rather than just changing DKP tools |
| B — Portal in a later major version | Keeps 1.0 small; defers the risk without denying the requirement | "1.1" for a scope this large is a promise a volunteer project may not keep, and Phase 5's article import would have nowhere to land |
| C — Full parity in scope, sequenced last | Genuine feature parity, which is a headline product requirement; the guild's website and their DKP move together | The costs in column two of option A are all real and are all accepted |

## Decision outcome

**Chosen: C.** The owner's rationale: *genuine* feature parity is the headline requirement, and for a
product whose front page is the guild's website, parity without the website is not parity. The person
who decides whether a guild migrates is usually the officer who maintains that website; telling them
they keep their DKP and lose their site is not a migration, it is a partial one.

**The mitigation is sequencing.** The portal is **Phase 8 — ninth of ten, last of the feature
phases, with only the hardening phase after it** — at ≈38 pt
of a ≈438 pt plan, and it depends only on `internal/richtext` (Phase 4) and the outbox (Phase 6). It
is deliberately off the critical path so that **if it slips, it slips to 1.1 without blocking guild
adoption**. The DKP product is complete and useful at the end of Phase 6.

**The security mitigation is structural**, because option A's strongest argument is the attack
surface:

- `internal/richtext` ships in **Phase 4**, four phases before its heaviest consumer, with its grep
  gate — so Phases 5–7 inherit a choke point instead of grandfathering one.
- `internal/cms` is the only package that holds untrusted rich text; server-rendered HTML is the only
  HTML displayed, and `dangerouslySetInnerHTML` is banned by lint anywhere in `web/src`.
- Uploaded images are re-encoded server-side (EXIF destroyed); **SVG is rejected outright**. CSP is
  `script-src 'self'` with no `unsafe-inline`.
- An **XSS corpus** — stored, reflected, mutation-XSS, bidi controls, SVG upload, polyglot image —
  against every CMS write path is a **1.0 exit criterion**, asserting `internal/richtext` is the
  single sanitisation point.

### Consequences

- Good, because a guild can point their domain at one binary and it is their whole site — news,
  recruitment, bank, priorities and DKP — not a DKP tool bolted next to one.
- Good, because it resolves a contradiction the reviewer caught: the migration design already imports
  `__articles`, `__comments` and `__article_categories`, and `feed_token.kind` already includes
  `articles_rss`. Those imports now have tables to land in.
- Good, because the parity claim survives contact with a launch thread. "What about our news page?"
  has an answer.
- **Bad, because it is roughly 20–25% more product to build** (the dedicated phase is ≈11% of the
  plan; the rest is carried in Phase 4's `richtext` and Phase 5's article import), all of it after the
  DKP product is already useful.
- **Bad, because it is the largest untyped blast radius in the product** (ROADMAP R14). Every
  mitigation above is real and none of them makes a rich-text editor, a media library and a public
  application form a small attack surface.
- **Bad, because it is the plan's biggest scope-creep vector** (R3). Portal blocks, menu trees and
  theme tokens are a long tail of individually-small features that never naturally ends; the defence
  is the published deferred list and the rule that no work item enters a phase after its exit
  criterion is written.
- **Bad, because if Phase 8 slips, 1.0 ships with a headline requirement unmet** and the README's
  parity claim must be qualified honestly rather than quietly.
- **Bad, because parts of it may genuinely be dead surface.** Whether guilds still want a shoutbox and
  a news feed when they live in Discord is *an assumption, not a finding* — answerable by the two
  pilot guilds (`docs/development/verify-before-phase-0.md` V2) long before Phase 8 starts. That
  answer should reorder Phase 8's deliverables, though it does not change this decision.

### Reversal cost

Cutting Phase 8 before it starts is free — that is the entire point of sequencing it last. Cutting it
after it ships means removing tables a guild's content lives in, which is a data-loss event and
therefore not a real option.
