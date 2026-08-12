# ADR-0010 — AGPL clean-room firewall

**Status:** accepted · **Date:** 2026-08-03 · **Deciders:** owner

## Context and problem statement

EQdkp Plus core is **AGPL-3.0**; its game modules and its raidlog XSD are **CC BY-NC-SA 3.0**
(non-commercial). This project is Apache-2.0 ([ADR-0009](0009-apache-2-0-and-dco.md)), and those
licences are incompatible in the direction that matters: AGPL code cannot be relicensed into an
Apache-2.0 project, and a non-commercial clause would poison a project that guilds and hosts must be
free to run however they like. The whole product is defined as "at least feature parity with EQdkp
Plus", so the temptation to copy appears in almost every task.

## Considered options

| Option | For | Against |
|---|---|---|
| A — Fork EQdkp Plus and stay AGPL | Instant parity; fifteen years of behaviour comes free | Inherits a PHP/LAMP codebase, an omnipotent `api_key`, raw stored HTML and a plugin system that executes third-party PHP unsandboxed. Also inherits the licence, which loses the audience |
| B — Reimplement, but copy DDL, language strings and calculations "for parity" | Fast; exact behaviour; nobody would notice | A licence violation, and the kind that is discovered by exactly the people best placed to sue. Also inherits their bugs verbatim |
| C — Strict firewall: read a user's database at runtime, reimplement behaviour from written descriptions | Legally simple and defensible; forces us to understand each behaviour rather than transcribe it | Parity work is slower; some behaviours will differ in ways guilds notice |

## Decision outcome

**Chosen: C.** Reading a user's own EQdkp Plus database at runtime creates no derivative work.
Transcribing their PHP, their DDL text, their language strings or their assets does.

| Allowed | Not allowed |
|---|---|
| Reading a user's own database at runtime | Copying their PHP, in whole or in part, in any language |
| Observing external behaviour and reimplementing from a written description | Copying their DDL text, `CREATE TABLE` bodies or column comments |
| Naming their tables and columns in the importer's mapping tables | Copying their language strings, help text, error messages or templates |
| Documenting what their software does | Copying their icons, CSS, images or theme assets |

The identifiers `pdh_`, `gen_class`, `plus_exchange` and `__multidkp2event` may appear in **exactly
two places**: `internal/importer/legacy_names.go` and `internal/api/compat/`.

No game data ships in core either — item names, stats and icons are Darkpaw IP. `dkp-p99-seed` is a
separate, optional, user-run repository. Class, race and zone tables ship as our own literals.

**Enforced by:** the `lint / repo` CI job greps for those four identifiers everywhere outside the two
permitted paths and fails the build; `security / licences` fails on any GPL/AGPL runtime dependency;
`CODEOWNERS` gates `LICENSE`, `NOTICE` and `TRADEMARK.md`; and `AGENTS.md`, `CONTRIBUTING.md` and the
PR template all state the rule, because the temptation arrives phrased as a legitimate task.

### Consequences

- Good, because the legal position is simple enough to state in one paragraph to a contributor, a
  host, or a rights holder.
- Good, because the importer still works fully — the one capability that actually matters for
  migration is the one that is unambiguously fine.
- Good, because writing the behaviour down before implementing it surfaces things reading the code
  would not. The discovery that EQdkp's APA decay rules live in `data/<md5>/eqdkp/apa/apatab.php`
  rather than in the database — and therefore *cannot* be imported — came out of exactly that
  discipline, and is now reported to the user in plain language instead of silently producing wrong
  balances.
- **Bad, because parity is slower and imperfect.** Some behaviours will differ in ways a guild
  notices, and "match EQdkp exactly" is not an available answer to a bug report.
- **Bad, because German parity is doubly expensive.** Their language strings are the obvious source
  for the terminology German guilds expect, and they are off-limits — so every translated string must
  be written fresh. See [ADR-0012](0012-english-only-at-1-0.md).
- **Bad, because the contributors most able to help are the most likely to violate the rule.** Someone
  who has run EQdkp for a decade has the source open in another tab by reflex. The instruction —
  close the tab, write the behaviour down in prose — is a discipline, and discipline is not a control.
- **Bad, because the greps catch identifiers, not paraphrased logic.** A transcribed algorithm with
  renamed variables passes CI. The firewall is ultimately review, and a grep is not a lawyer.
- **Bad, because "no game data in core" means item names and icons need `dkp-p99-seed`**, which is one
  more thing a new officer must do before the loot screen looks finished.

### Reversal cost

Not reversible in the permissive direction — any AGPL code that entered would have to be removed and
its history rewritten, and every downstream fork notified. Treat a violation as an incident, not a
cleanup.
