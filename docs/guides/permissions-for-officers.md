# Permissions for officers

**Status:** the permission catalogue and roles land in Phase 2. The normative key list is generated
from `internal/authz/catalogue.go`; the table below is a copy of the canonical list and will be
replaced by the generated `reference/permissions.md` when it exists.

Two questions, deliberately kept separate: what a **person** may do, and what a **token** may do. The
answer to "can this request go through" is always the intersection.

## The one rule that matters

```
effective capability = the actor's role permissions ∩ the token's scopes
```

A token can only ever **narrow** what its service account's role already grants. There is no
`admin:*` scope, no wildcard, and no token that becomes a superadmin. That is the single biggest
deliberate fix over EQdkp Plus, whose `api_key` impersonates the first superadmin account.

## Permissions — what a role grants

Keys are `resource.action`, lowercase, dot-separated.

| Family | Keys |
|---|---|
| Roster | `roster.read` `roster.write` `person.merge` `character.claim.approve` |
| Raids | `raid.read` `raid.create` `raid.update` `raid.finalize` `raid.tick.create` `raid.tick.delete` |
| Items | `item.read` `item.award` `item.alias.manage` |
| Points | `dkp.read` `dkp.adjust` `dkp.decay.run` `ledger.reverse` |
| Bidding | `bid.read` `bid.manage` `bid.reveal_early` |
| Calendar | `calendar.read` `calendar.write` `signup.manage` |
| Portal | `cms.read` `cms.write` `cms.moderate` |
| Import | `import.run` `import.commit` |
| Integrations | `webhook.manage` `token.mint` `token.revoke` |
| Administration | `admin.settings` `admin.roles.manage` `admin.backup` `admin.owner` |
| Sensitive reads | `person.pii.read` `audit.read` `ops.read` |

`role_permission` is foreign-keyed to `permission(key)`, so a role referring to a key that does not
exist is a **boot failure**, not a typo that lurks. Hand-written permission lists are forbidden
anywhere in the codebase.

## Scopes — what a token grants

Keys are `family:verb`, colon-separated. Scopes are coarser than permissions on purpose: a scope
narrows a token, a permission narrows a role.

```
roster:read  roster:write     raids:read  raids:write     dkp:read  dkp:adjust
loot:read    loot:award       bids:read   bids:manage     logs:ingest
calendar:read  calendar:write  cms:read  cms:write        events:subscribe  webhooks:manage
```

Mint the narrowest set that does the job. A Discord `!dkp` command needs `dkp:read` and nothing else.
A log tailer needs `logs:ingest` and `raids:write`.

`dkp:adjust` is deliberately separate from `loot:award`. A bid bot must be able to charge for an item
and must **not** be able to hand out points.

## Operations no token can perform

These alter authentication, authorisation or bulk-export state. They are **session plus step-up**
only — a re-authentication within the last five minutes — and they have **no scope at all**:

| Operation | Why |
|---|---|
| Mint, rotate or revoke a token | Otherwise a leaked token mints a better one |
| Edit roles or role assignments | Otherwise a leaked token promotes itself |
| Download a backup | A backup is the entire guild in one file |
| Read personally identifying data in bulk | Email addresses are the one genuinely sensitive field here |
| Commit an import | The one operation that can overwrite ten years of history |

If you are looking for the scope that lets your bot do one of these, there is not one, and there will
not be one.

## The seeded roles

Assign a role, not a permission list. These are the defaults; you can create your own.

Each row **adds to the row above it**. This is a copy of the built-in seed in
[01 Domain model §5.1](../design/01-domain-model.md); that table is the source it is copied from.

| Role | Adds | Give it to |
|---|---|---|
| `guest` | `roster.read` `raid.read` `item.read` `dkp.read` `bid.read` `calendar.read`, and only if public standings are enabled | Nobody, explicitly — it is what an unauthenticated visitor gets |
| `member` | `cms.read`, plus own signups, own disputes and own claims, which are **ownership, not permissions** | Everyone in the guild |
| `raider` | Nothing — identical to `member`. A distinct assignable name for guilds that want the rank distinction | Guilds that track a Raider rank |
| `raid_leader` | `raid.create` `raid.update` `raid.finalize` `raid.tick.create` `raid.tick.delete` `item.award` `signup.manage` | Whoever calls the raid. Commonly assigned scoped to one raid group. |
| `officer` | `roster.write` `person.merge` `character.claim.approve` `dkp.adjust` `bid.manage` `bid.reveal_early` `item.alias.manage` `calendar.write` `cms.write` `audit.read` | Your officer corps |
| `admin` | `dkp.decay.run` `ledger.reverse` `cms.moderate` `import.run` `import.commit` `webhook.manage` `token.mint` `token.revoke` `admin.settings` `admin.roles.manage` `admin.backup` `person.pii.read` `ops.read` | One or two people |
| `owner` | `admin.owner` | Exactly one person |
| `bot_readonly` | The `*.read` keys only | A `!dkp` bot |
| `bot_raid` | `raid.create` `raid.update` `raid.tick.create` `item.award`. **Never `dkp.adjust`.** | A log-tailing bot |

`ledger.reverse` and `dkp.decay.run` sit on `admin`, not `officer`: reversing a batch and running a
decay both move points for the whole guild at once.

**Rank is not role.** Your guild's Raider rank is a label your members care about; a role is what the
software permits. Wiring them together means promoting someone socially also hands them the backup
download.

## Giving out access, in order of what actually goes wrong

| Situation | Do | Not |
|---|---|---|
| A new officer | `officer` | `admin` "because it is easier" |
| Someone who runs the raid but is not an officer | `raid_leader`, scoped to their raid group | `officer` |
| A Discord bot that answers `!dkp` | A service account with `bot_readonly` and the `dkp:read` scope | An officer's personal token |
| A bot that uploads dumps | A service account with `bot_raid`, scopes `logs:ingest` and `raids:write` | Adding `dkp:adjust` "in case" |
| A member who wants to see their own history | Nothing — `member` already can | A one-off permission |
| An officer leaving the guild | Revoke their user; **the service account's tokens keep working** | Deleting the tokens their bot depends on |

That last row is the reason tokens belong to service accounts rather than to people. Deleting the
owning user flags the token as orphaned and notifies admins; it does not revoke it, so the guild's bot
does not die because someone stopped raiding.

## Self-dealing

An officer with `dkp.adjust` can adjust their own balance. The system does not block it, because
`dkp.adjust` exists precisely to create adjustments and any rule blocking it is trivially defeated by
asking another officer.

What it does instead: **flags it**. Every ledger batch records whether the actor is also the
beneficiary, and those batches are surfaced separately in the audit view. The control is visibility,
not prevention — and visibility is the control that works in a volunteer guild.

## Auditing

| Question | Answer |
|---|---|
| Who changed this? | Every ledger batch, award and role change records the actor and, when the actor was a token, the token's public prefix |
| What did it look like before? | The audit log records before and after values |
| Was a sealed bid read early? | Yes, that read is itself audited |
| Can an officer edit history to hide something? | No. The ledger is append-only and a database trigger enforces it. |

`audit.read` is on the `officer` role because an audit trail nobody can read is decoration.

## Next

- [Auth and scopes](../api/auth-and-scopes.md) — the same model from a bot author's side
- [The ledger](../concepts/ledger.md) — why editing history is not an option for anyone
- [Roster, mains and alts](roster-and-alts.md) — rank versus role
