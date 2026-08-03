# What does not migrate

**Audience:** officer. Read this before you announce the move, not after.

Everything on this page is a deliberate decision, and every one of them appears in your
[verification report](reading-your-verification-report.md) with a count. Nothing here is dropped
quietly.

## First, what does come across — in full

Raids · raid attendance · ticks · items and who bought them · prices · adjustments and their reasons
· characters · ranks · main/alt relationships · point pools · item pools · event types and their
values · usernames and email addresses · which characters belong to which account · calendar events
and signups · raid roles and raid groups · your audit log · news articles, comments and categories ·
guild settings that have an equivalent here.

**Every member's balance ends up matching the number your old site displayed**, by construction. How
that is achieved when decay is involved is explained under
[APA rules](#apa-rules-decay-caps-and-start-points) below.

---

## Passwords

**Not migrated. None of them. Ever.**

**Why.** A ten-year-old EQdkp install stores passwords in up to seven different formats, and some of
them are unsalted MD5 from 2010. Carrying those forward means keeping a weak login mechanism alive
forever for the accounts least likely to notice. There is no version of "we imported your passwords"
that is safe, and migration day is the one day everybody already expects to do something different.

**Do this instead.** Every account arrives unable to log in until it is claimed. Members claim by
logging in with Discord (automatic if their Discord email matches their old one), or by entering a
one-time code you hand out, or by clicking a link you generate for them individually. See
[step 5 of the walkthrough](from-eqdkp.md#step-5--verify-then-hand-out-accounts).

**Also not migrated:** remember-me tokens, active sessions, linked social accounts (their stored
form is encrypted with a key from your `config.php` and is not recoverable), and per-user API keys.
Members re-link Discord on first login.

---

## Permission detail

**Your groups become roles. The fine-grained permission matrix does not come across intact.**

**Why.** EQdkp has roughly 60 permission keys, tri-state per group, plus per-user overrides, plus a
super-admin group that bypasses the whole table in code. Most of those keys control features that do
not exist here — bridge management, style editing, menu management, page-object permissions. There is
no honest mapping, and guessing one means granting somebody a capability nobody chose to grant.

**What actually happens.**

| Legacy thing | Result |
|---|---|
| A group | Mapped to the **lowest built-in role that covers everything the group had** — never rounded up. If nothing fits, you get a custom role called `Legacy: <group name>` with only the permissions that mapped |
| A permission with no equivalent here | Dropped and listed by name |
| A **direct grant** to one person, outside any group | **Not applied.** Listed as "Zibaxia held a direct grant of item-delete — add them to Officer, or make a custom role" |
| A **direct deny** to one person | **Honoured.** They are removed from the role that would have granted it. Taking a capability away is always safe; adding one is not |
| Super-admin group members | Role set to Admin with **nobody assigned**. Listed as "promote after they claim" |
| Site-wide API keys | Not migrated. EQdkp's API key impersonates your first super-admin; nothing here is allowed to do that |

**Do this instead.** Read report §10. It contains a per-person list of everyone whose capability
shrank, naming the specific capability, so you can tell someone "you can no longer delete raids"
before they discover it during a raid.

---

## APA rules (decay, caps, and start points)

**Your decay and cap *rules* do not migrate. Your decay and cap *effects* do.**

**Why.** EQdkp does not store its automatic point adjustment rules in the database. They live in a
PHP file on your web server, under `data/<a long hash>/eqdkp/apa/`. A database backup does not
contain that file, which means an importer reading your database has no way to know that your guild
decays 10% every 30 days.

**What actually happens.** We rebuild every raid, item and adjustment exactly. That total will not
match your old screen, by precisely the amount your decay took away. So we detect *that* decay ran —
EQdkp does record its per-member results — and post one visible, labelled line per member:

> **Legacy reconciliation** — EQdkp point decay (APA #3, ~10%/30d).
> Imported history totals 1,842.00; EQdkp displayed 1,699.50. **−142.50**

Your members' balances match. The residual is visible in the statement, not hidden in an opening
balance, and it can be reversed like anything else.

**Do this instead.** Re-enter your decay, cap and start-point rules under **Pools → Rules**. The
wizard pre-fills the rate it detected, so this is usually a confirm rather than a retype. Decay,
soft/hard caps and starting points all exist here as first-class, previewable, dry-runnable rules.

---

## Per-row decay (`decay_ria`)

**Not supported, and there is no equivalent.**

**Why.** `decay_ria` decays each individual raid, item and adjustment row and injects extra "decayed
value" columns into your standings table. A balance here is literally the sum of the ledger, and the
ledger is append-only — a mechanism that silently rewrites the value of a five-year-old raid is
incompatible with that on purpose.

**Do this instead.** Use a scheduled decay rule on the pool. It produces the same *shape* of outcome
— balances shrink over time — as a posted, dated, explainable batch rather than a display-time
recalculation.

**Watch out for this one.** If your guild used `decay_ria`, the per-member differences in your report
may land in the **unexplained** bucket rather than being labelled as decay. That is the report doing
its job. Tell us in an issue and bring your report JSON.

---

## Plugins

**No plugin data is imported. All of it is reported.**

**Why.** EQdkp plugins ship their own tables with their own undocumented schemas. There are around
sixty official ones and an unknown number of guild-local ones.

**What actually happens.** The importer lists every table it found that is not core, **with row
counts**, so you know exactly what is being left behind. Common ones get a named line:

> plugin `itemprio` had 340 rows; no import path exists yet — open an issue if you need it.

**Do this instead.** If a plugin's data matters to your guild, open an issue with your report §1
attached and the row counts. Item priority lists, the guild bank and recruitment/applications are all
planned as first-class features here, so the useful question is "which of these do you need first".

---

## Styles, layout presets, portal layouts, menus

**Not migrated.**

**Why.** EQdkp's style manager has around 70 colour and typography variables per style plus free-form
LESS; its layout manager stores column selections as preset trees; the portal manager stores block
layouts per route. None of it describes anything in this product, which has one good theme with light
and dark modes and a plain column picker.

**Do this instead.** Pick your columns once, in the standings table's column picker, and save the
view. If your guild had a custom logo or banner, upload it under Settings → Appearance.

**What this loses in practice:** if your standings page showed a computed column that only existed
because of a layout preset — effective DKP, a zero-sum column, a decay column — you need to choose the
matching point system for the pool instead. The wizard suggests one based on what it detected.

---

## Forum bridges

**Not supported. Any of the 24 of them.**

**Why.** An EQdkp "bridge" is not OAuth. It reads your forum's user table directly, authenticates
against the forum's own password hashes, and syncs group membership on a cron. It is direct
cross-application database coupling, it is the single largest source of EQdkp install fragility, and
it means two applications share one password store.

**Do this instead.** Log in with **Discord**, or with any standard OIDC provider. If your forum can
be an OIDC provider, point at it. If your members currently log in to EQdkp with their forum account,
they will claim their Dragon Kill Party account with Discord or a claim code instead — plan one
announcement for this.

---

## EQdkp as a login provider for something else

**Not supported.**

**Why.** Some guilds run the `eqdkp_sso` plugin so their WoltLab forum can log in *against* EQdkp.
Turning this product into an identity provider for third-party forums is a security surface with a
very small audience.

**Do this instead.** If your forum currently authenticates against EQdkp, **it will stop working on
the day you retire EQdkp.** Decide before cutover whether the forum moves to its own accounts, to
Discord login, or to another provider — and keep EQdkp read-only until you have. This is the item on
this page most likely to be discovered too late.

---

## Smaller things, itemised

| Thing | Status | What to do |
|---|---|---|
| **Away mode** (`awaymode_*`) | **Imported.** `awaymode_start`/`awaymode_end`/`awaymode_text` map onto `person.away_from_at` / `away_until_at` / `away_note` | Nothing. Check the count in the report and tell anyone whose window already expired that the nightly sweep cleared it |
| **Custom character/user profile fields** | **Imported as typed, queryable fields** — a `character_field_def` per legacy field, values in indexed columns | Report lists each field with how many rows use it and the type it was mapped to. Check the type guesses; a field mapped `text` that you wanted to sort numerically is a one-click change |
| **Article category permissions** | Dropped | Their meaning is undocumented and we will not guess an ACL. Imported categories are officer-visible; open them up yourself |
| **Article votes, page objects, featured/sort flags** | Dropped | Cosmetic or CMS-internal |
| **Class colours, item colours** | Dropped | Theming |
| **Cron job configuration** | Dropped | Scheduled jobs here are built in and need no configuration |
| **Notification settings** | Dropped | Members re-set preferences; webhooks and Discord notifications are configured centrally |
| **Shoutbox messages** | Not imported | Reported with a row count |
| **Member IP addresses in the audit log** | **Dropped by default** | Deliberate. Your volunteer guild should not inherit a decade of members' IP addresses. There is a flag to keep them if you have a reason |
| **Date and time format preferences** | Dropped | Formats here follow the member's locale and timezone, which *is* imported |
| **Calendar → raid links** | Do not exist in EQdkp | After import you are offered a previewed one-click "these 47 calendar raids look like these 47 raids" match. Never automatic |
| **Sessions, login keys, sign-up confirmations** | Not imported | Members log in fresh |
| **Anything only in EQdkp v1** | Unsupported | Run EQdkp Plus's own 1.x → 2.x upgrader first, then import from 2.x |

---

## One thing that is not about data

**Dragon Kill Party is English-only at 1.0.** EQdkp Plus is German-first and a large part of its
install base is German. If your guild runs EQdkp in German, this is a real regression and the honest
answer is that a 1.0 migration is not yet for you. Translations are planned; the groundwork ships
early so adding a language later is a translation job, not a rewrite.

---

## If something on this page is a blocker

Open an issue with your verification report attached — the JSON version, which contains counts and no
personal data. "We cannot move because we need X" with a row count next to it is the most useful bug
report this project can receive, and it is how this list gets shorter.
