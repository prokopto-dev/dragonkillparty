# ADR-0012 — English only at 1.0

**Status:** accepted · **Date:** 2026-08-03 · **Deciders:** owner

## Context and problem statement

**EQdkp Plus is a German-first project, and a large share of its install base is German** — its
German documentation and community have historically been ahead of its English ones. This product's
headline claim is at least feature parity with EQdkp Plus. Shipping English-only is therefore not a
neutral scoping decision: **it is a parity regression for the incumbent's largest user population**,
and those guilds cannot migrate at 1.0. Say that in the README, not in a launch thread.

*The size of the German share is an estimate drawn from the incumbent's documentation and community
footprint, not a measurement. It does not change the decision — it changes how loudly the regression
is stated.*

## Considered options

| Option | For | Against |
|---|---|---|
| A — English + German at 1.0 | No parity regression; the largest incumbent population can migrate on day one | Doubles the surface of every string-producing change during the phases where the domain is still moving; needs a fluent DKP-literate translator recruited before there is a product to translate; and the terminology cannot be lifted from EQdkp because their strings are AGPL ([ADR-0010](0010-agpl-clean-room-firewall.md)) |
| B — English only, hardcoded strings, i18n "later" | Cheapest path to 1.0; no infrastructure for zero current benefit | Retrofitting i18n after the UI exists costs roughly **10×** — every hardcoded string in the SPA, every server-side error `detail`, every email, every doc page. It is the classic deferred cost that never gets paid |
| C — English only, with message-catalogue scaffolding from Phase 3 | Ships one language done well; every string lives in a catalogue from the moment the UI exists, so 1.1 German is a data change rather than a rewrite | Real Phase 3 effort for zero 1.0 user benefit; discipline must hold for five phases before anyone sees a payoff |

## Decision outcome

**Chosen: C.** The 1.0 UI is English. From **Phase 3** onward there are **no hardcoded user-facing
strings** — every one goes through a message catalogue, so adding German in 1.1 is translation work,
not refactoring work. A German-speaking translator is recruited during the RC, when there is a stable
string set to hand them.

Where this is stated, deliberately and in advance: the README, `docs/migration/what-does-not-migrate.md`,
and ROADMAP risk **R13**. A German guild should learn this from our own documentation before they
invest an evening in a trial import.

**Enforced by:** an ESLint rule that fails on bare JSX text nodes, so a hardcoded string cannot land
in `web/src`; a test asserting every catalogue key referenced in code exists; and the closed error-code
enum, which gives an API client a machine-readable `code` regardless of the language of the `detail`.

### Consequences

- Good, because one language is done properly rather than two done partially, while the domain
  vocabulary is still changing every phase.
- Good, because the 1.1 German release is scoped and estimable — a catalogue file and a reviewer — and
  can be contributed by a guild rather than by a maintainer.
- Good, because the machine-readable error `code` means a German-speaking bot author is not blocked
  even before the UI is translated.
- **Bad, because it is a genuine regression for the incumbent's biggest population, and it will show
  up as lost migrations.** "Is there a German UI?" is a question that ends an evaluation, and no
  mitigation in this ADR changes the answer at 1.0.
- **Bad, because the scaffolding costs Phase 3 effort that no 1.0 user benefits from**, and it is
  exactly the kind of work that gets cut when a phase runs long. It must not be.
- **Bad, because the server side is the half everyone forgets.** Error `detail` text, emails, RFC 9457
  `title`s, CLI output, `dkp doctor` fixes and the docs themselves are not covered by an ESLint rule
  on JSX, and a 1.1 German UI in front of English error messages is a poor result.
- **Bad, because DKP jargon does not crowdsource well.** "Tick", "suicide kings", "zero-sum", "decay
  window" have established German-guild renderings that a generic translator will get wrong, and we
  cannot copy the incumbent's wording to check ourselves against.

### Reversal cost

Shipping German in 1.1 is a catalogue file, a reviewer and a language switcher — days, if the Phase 3
discipline held. If it did not, it is the 10× retrofit this ADR exists to prevent.
