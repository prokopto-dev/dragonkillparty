# ADR-0009 — Apache-2.0 and DCO, not MIT and not a CLA

**Status:** accepted · **Date:** 2026-08-03 · **Deciders:** owner

## Context and problem statement

The incumbent is AGPL-3.0 with non-commercial game modules, which is part of why it is hard to build
on ([ADR-0010](0010-agpl-clean-room-firewall.md)). This project needs a licence that lets guilds and
small hosts run it however they like, that says something explicit about patents and trademarks, and
that does not put a signing wall in front of the contributor we most need — the officer fixing one
wrong regex in a P99 log parser.

## Considered options

| Option | For | Against |
|---|---|---|
| A — MIT | Shortest, most familiar, zero friction | Silent on patents; silent on trademarks, so "Dragon Kill Party" as a name has no stated boundary; nothing to point at when a fork ships under the same name |
| B — AGPL-3.0 (match the incumbent) | A hosted fork must publish its changes; strongest copyleft for a network service | Poisons the audience: guilds and small commercial hosts avoid it; incompatible with an Apache-2.0 ecosystem; and it would make *our* dependency policy stricter than our own users need |
| C — Apache-2.0 + a CLA | Retains the ability to relicense or dual-licence later | A CLA is a wall exactly where drive-by contributions come from, and requires an entity to hold the rights that this project does not have |
| D — Apache-2.0 + DCO | Explicit patent grant (§3) and explicit *absence* of a trademark grant (§6); sign-off is one flag | No relicensing power; sign-off is a claim, not verification |

## Decision outcome

**Chosen: D.** Code is Apache-2.0. Documentation is CC BY 4.0. Contributions are under the
[DCO](https://developercertificate.org/) — `git commit -s` — and **there is no CLA**. The name and
logo are not licensed; forks must rename, which is what Apache-2.0 §6 makes sayable and `TRADEMARK.md`
says plainly.

**Enforced by:** the DCO GitHub App as a required status check named `DCO`; `lefthook.yml` installs a
local hook so the failure happens before push; the `security / licenses` CI job fails on any GPL or
AGPL **runtime** dependency, with no exception process; and `.github/CODEOWNERS` puts `LICENSE`,
`NOTICE` and `TRADEMARK.md` behind a named reviewer.

### Consequences

- Good, because the patent grant and the explicit no-trademark-grant are stated in the licence text
  rather than assumed, which matters for a product commercial hosts may run.
- Good, because contributing costs one flag and no account, no email exchange and no paperwork —
  measured against a volunteer fixing one parser regex, that is the whole game.
- Good, because the dependency licence gate keeps the tree clean automatically, so the AGPL firewall
  has a machine behind it and not just a paragraph.
- Good, because "fork it and rename it" is an answer we can give quickly and without rancour.
- **Bad, because a permissive licence lets someone run a closed-source hosted fork with no obligation
  to contribute anything back.** AGPL would have prevented that. We are giving it up deliberately: the
  audience we want is guilds and small hosts, and copyleft on a network service is precisely what
  makes them nervous.
- **Bad, because a DCO gives us no relicensing power.** Changing the licence later needs every
  contributor's agreement, which for a project of any size means: never. If dual-licensing or a
  hosted-service exception is ever wanted, this decision has already closed that door.
- **Bad, because sign-off is an assertion, not a verification.** The DCO does not check whether a
  contributor actually had the right to submit the code, so the AGPL firewall still rests on review
  and CI greps ([ADR-0010](0010-agpl-clean-room-firewall.md)), not on legal assurance.
- **Bad, because Apache-2.0 imposes real attribution obligations downstream** (§4(a), 4(b), 4(d)) that
  forks routinely get wrong, and `NOTICE` is a file that must be maintained rather than forgotten.

### Reversal cost

Effectively infinite in the restrictive direction: relicensing to AGPL or adding a CLA requires
agreement from every past contributor. Loosening (Apache-2.0 → MIT) is possible but pointless, and
would discard the patent and trademark clauses that are the reason for the choice.
