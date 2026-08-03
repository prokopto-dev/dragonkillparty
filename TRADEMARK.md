# Trademark policy

The code is yours. The name is not.

Apache-2.0 section 6 grants **no trademark rights**, and this page says plainly what that means in
practice so you never have to ask a lawyer about it.

## Two different things, two different rules

| Thing | Licence | What you may do |
|---|---|---|
| Source code in this repository | [Apache-2.0](LICENSE) | Fork it, change it, run it, sell it, embed it in a commercial product. No permission needed, no royalty, no notice to us beyond what the licence itself requires. |
| Documentation under `docs/` | CC BY 4.0 | Reuse, translate, republish, with attribution. |
| The name **Dragon Kill Party** and the project logo | **Not licensed** | See below. A modified build must be renamed. |

## What is *not* claimed

- **"DKP"** and **"dragon kill points"** are generic terms of art in EverQuest and every other MMO
  that predates this project by two decades. Nobody owns them and we do not claim them. Name your
  tool `something-dkp` freely.
- The `dkp` binary name, the `/api/v1` shapes, the config keys, and the database schema. All
  Apache-2.0, all yours.
- Any EverQuest or Project 1999 term. Those belong to their owners; see the disclaimer below.

The claim is narrow on purpose: it covers **"Dragon Kill Party"** as the name of this software, and
the logo. That is all.

## Permitted without asking

- Say your fork is "a fork of Dragon Kill Party" or "based on Dragon Kill Party".
- Say your bot, importer, theme, or spreadsheet "works with Dragon Kill Party".
- Use the name in articles, reviews, comparisons, talks, guild recruitment posts, and bug reports.
- Link to the project using its name and its unmodified logo.
- Run an **unmodified** official build for your guild and call it what it is.
- Ship local patches you have not published — a private build is nobody's business but yours.

## Requires renaming

You must pick a different name and drop the logo if you **distribute** a modified build. Concretely:

| Not permitted | Why |
|---|---|
| Publishing a modified build under the name Dragon Kill Party, or a confusingly similar name | People cannot tell whose bug they are reporting |
| A container image, package, or repository named to look official (`ghcr.io/you/dragon-kill-party`, `dragonkillparty` on any registry) | Same reason, plus supply-chain confusion |
| Using the logo — or a recoloured, redrawn, or otherwise modified logo — as your project's mark | A logo's whole job is to identify one source |
| A domain name, Discord server, or social handle that suggests official status | Implies endorsement that does not exist |
| Stating or implying that we endorse, maintain, support, or have reviewed your fork | We have not |

"Modified" means modified. Rebuilding the released source unchanged and running it is not a fork.

## Renaming a fork — the checklist

| Change | Where |
|---|---|
| Product name string | The guild-facing title, the About screen, and the message catalogue |
| Binary name | `cmd/<name>/`, the `Makefile` `BIN` variable, and the release artifacts |
| Container image name | The `IMAGE` variable and your registry namespace |
| Logo and favicon | `web/` assets — delete ours, add yours |
| Documentation title and URLs | `docs/`, and your published site |
| This file | Replace it with your own policy, or delete it |

Keep `LICENSE` and `NOTICE`, and keep the existing copyright and attribution notices in them — that
is required by Apache-2.0 sections 4(a), 4(b), and 4(d), and it is unrelated to trademark.

## Asking

If your case is not covered here — a hosting service, a distro package, a book cover, a conference
logo wall — open an issue or write to `trademark@dragonkillparty.example` and ask. The answer is
usually yes, quickly, in writing. *(Placeholder address; replace it before the repository is
published.)*

This policy may change. Changes are not retroactive: a fork that complied on the day it shipped
stays compliant for that release.

---

Not affiliated with, endorsed by, or connected to Daybreak Game Company LLC, Darkpaw Games LLC, or
Project 1999. EverQuest is a trademark of Daybreak Game Company LLC. No game assets are bundled.
