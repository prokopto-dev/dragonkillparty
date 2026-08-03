# Roster, mains and alts

**Status:** the roster model lands in Phase 2 (persons, characters, claims) and Phase 4 (attribution
history, merges at scale). This page is the specification, not a record of behaviour.

Alts are the single thing EQdkp Plus does worst for EverQuest, because its points belong to a
character row. Here they do not. Set your policy before the first raid; changing attribution later is
supported but visible, and it moves published attendance percentages.

## Three words, three different things

| Term | Is | Holds |
|---|---|---|
| **Person** | A human being in your guild | The DKP balance, attendance statistics, rank |
| **Character** | One in-game name on one server | Nothing. It is attribution. |
| **User** | A login — local password, Discord, or OIDC | Access, not points |

One person owns many characters, one of which is flagged the main. The ledger is keyed on the
person's **account**; the character that appeared in the raid dump is recorded on each entry as
metadata. That is why:

- Your rezzer alt earning a tick credits *you*.
- Boxing two characters in one tick counts once, not twice.
- Renaming a character does not disturb a single ledger row.
- A member who rerolls from Warrior to Shadow Knight keeps their bank.

## Getting characters attached to people

### Claim codes

The normal path. Each member receives a one-time code, signs in, and claims their characters. After a
migration this is also how members get accounts at all — **passwords are never imported from EQdkp
Plus**, only usernames and email addresses.

The setup wizard and the importer both print the list of codes with a copy-as-Discord-message button.

### The three claim strengths

| Method | Proves | Available |
|---|---|---|
| `officer_manual` | An officer says so | Always. Ships first. |
| `roster_dump` | The character appeared in a dump uploaded by a token-bearing officer | Once ingest exists |
| `log_nonce` | The claimant currently controls the character — they say a one-time phrase in game and it appears in the log | Once ingest exists |

`log_nonce` is the strongest because it proves current control, not past association. Screenshots are
never accepted as evidence: they are trivially faked and impossible to audit.

The schema records **which** method was used, so a contested claim can be weighed rather than argued.

### Unknown names from a parse

A character name that has never been seen before does **not** silently create a person. It lands in
the [reconciliation queue](loot-and-reconciliation.md), the award is quarantined, and an officer
decides. Nothing is dropped and nothing is guessed.

The alternative — auto-creating a person for every typo, every pickup and every anonymous `/who`
entry — produces a roster full of ghosts that someone has to merge later, one by one.

## Choosing an alt policy

Set it per pool. Three choices:

| `alt_policy` | Behaviour | Suits |
|---|---|---|
| `shared` (default) | Every character feeds one balance for the person | Almost every P99 guild |
| `separate` | Each character has its own balance in this pool | A dedicated alt/rare pool where mains must not compete |
| `none` | Only the main earns and spends in this pool | Guilds that want alts to raid but not to bank |

Then decide the four questions guilds actually fight about, and write your answers where members can
read them:

| Question | Common answers |
|---|---|
| Do alts earn full ticks? | Full · a percentage · nothing |
| Can you bid for a non-raiding alt? | No · yes at a surcharge · only after the item has rotted once |
| Who gets the DKP when someone pilots another member's box? | The pilot · the box's owner · nobody |
| Do standby and bench earn? | A percentage · a flat reduced value · nothing |

Piloting is a real P99 mechanic — guilds run parked ports, rezzers and pullers on other people's
accounts — so attendance records a `pilot` status distinctly from `present`. Both count toward
attendance; who gets the points is your policy.

## Fixing attribution later

Two operations, and they are deliberately different.

### Re-parenting a character

Move a character from one person to another going forward. Past ledger entries **keep the account they
were posted to** — the points do not follow the character, because the points were never the
character's.

Re-parenting is audited and it changes published attendance percentages, since attendance is per
person. The interface says so before you confirm.

If a guild genuinely wants the history moved as well, that is an explicit, previewed
**re-attribution** batch: a new set of ledger entries at today's sequence that moves the balance. The
original entries stay exactly where they are. You see the diff before it commits.

### Merging two persons

The corrective operation when the same human ended up as two people — usually a pickup raider who was
later recruited. Merging folds one person into the other, retargets their characters, and leaves a
`merged_into_person_id` pointer so old links keep resolving.

Merge is the *only* corrective path for duplicate humans, which is deliberate: one well-tested code
path beats three half-tested ones called "claim", "link" and "adopt".

## Ranks, roles and away mode

| Concept | What it controls |
|---|---|
| Rank | A label your guild uses — Trial, Member, Raider, Officer. Often driven by attendance. |
| Role | What the account can *do* in the software. See [Permissions for officers](permissions-for-officers.md). |
| Away mode | A dated absence with a note. It filters the signup lists so a raid leader does not chase someone who is on holiday. |

Rank and role are not the same thing and should not be wired together. A guild's most trusted raider
does not need `admin.settings`, and a raid leader who is not an officer still needs
`raid.tick.create`.

## The custom fields question

Guilds track things the software does not model: an alt's purpose, a class spec, a note about a
long-standing loot agreement. Typed custom fields on characters and persons exist for this.

A field that only ever gets read on a profile page can be free-form. A field you want to **filter or
sort by** must be declared with a type, because unqueryable JSON is where a migrated EQdkp profile
field goes to be forgotten. If you are migrating, check
[what does not migrate](../migration/what-does-not-migrate.md) for how your existing profile fields
land.

## Next

- [Attendance and windows](attendance-and-windows.md) — why boxing counts once
- [Permissions for officers](permissions-for-officers.md) — roles, not ranks
- [Migrating from EQdkp Plus](../migration/from-eqdkp.md) — how the `member_main_id` chain is untangled
