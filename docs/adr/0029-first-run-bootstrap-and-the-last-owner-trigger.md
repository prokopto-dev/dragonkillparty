# ADR-0029 — First run mints a token, and the last owner is kept by the database

**Status:** accepted · **Date:** 2026-08-23 · **Deciders:** owner

## Context and problem statement

`internal/authz` seeds the nine built-in roles and the `owner` role grants `admin.owner`. Nothing
holds it, so a fresh instance authenticates nobody into anything — the gap ADR-0028 deliberately
left, and the half issue #264 needed `app_user` for.

Two questions answer together, because each is the other's failure mode.

**How does the first owner exist without a default password?** EQdkp Plus, phpBB and most of what a
volunteer officer has installed before ship one; the instances still on the internet with
`admin/admin` are why `docs/design/03-security.md` §9.2 reads as it does.

**How is the last owner kept?** §4.3 requires the last holder to be undemotable, through "a DB-level
check plus an integration test" rather than a Go check. An instance with no owner cannot mint a
token, edit a role, take a backup, read PII or commit an import — all capability-floor,
session-and-step-up operations — so it is not degraded, it is locked.

## Considered options

| Option | For | Against |
|---|---|---|
| A — Token state in `dkp_meta`, last-owner rule in Go | No new table, no migration, no ADR | The attempt counter becomes read-parse-write over untyped TEXT, which loses a race with a second redeem; and a Go-only rule holds for the callers that remember to ask. The role editor, an import and a Discord sync are all callers that do not exist yet |
| B — A `bootstrap_token` table, and eight triggers over the four tables that can break the invariant | `attempts = attempts + 1 WHERE attempts < 5` is one statement; the rule holds against a hand-edited database and against code nobody has written | A table used once in an instance's life, and eight trigger bodies that repeat one predicate because SQL cannot factor it |
| C — B, but with the triggers evaluating `expires_at` against the clock | Counts a time-boxed owner grant as a holder, which is arithmetically truer | A schema-level guarantee that depends on wall time: the same `DELETE` succeeds or fails depending on when it runs, and an instance can lock itself out at midnight with nothing in the diff to show why |
| D — Enforce only on the two statements that exist today | Smallest diff | The two that exist are the least likely to be the problem: `role_assignment` has no DELETE *because its editor has not shipped*, and shipping it is when the rule would stop being enforced |

## Decision outcome

**Chosen: B.** `bootstrap_token` holds one row per arming — the SHA-256 of 32 random bytes, an expiry,
an attempt counter and a consumption stamp — and `000011` carries eight `BEFORE` triggers over
`app_user`, `role_assignment`, `role` and `role_permission`.

Four things make it hold rather than merely exist:

1. **"Holds `admin.owner`" is structural and time-free.** An assignment counts only when `expires_at`
   and `suspended_until_at` are both NULL. It is conservative in one direction only: it can refuse a
   change that would have been survivable, never permit one that is not.
2. **Eight triggers, because a trigger is attached to a table and an operation.** `role` and
   `role_permission` need their own despite `role_assignment` having two: both cascade `ON DELETE`,
   and SQLite fires a trigger on a cascaded row only under `PRAGMA recursive_triggers`, which this
   product does not set — so a cascade walks past the pair that looks like it covers it.
3. **The literals are guarded.** A trigger body cannot be parameterised, so `'admin.owner'`, `'user'`,
   `'both'` and `'active'` are written into the SQL. `TestOwnerTriggers_LiteralsMatchTheGoCatalogues`
   reads the shipped migration and requires each to be the Go constant it stands in for — a rename
   would otherwise leave the triggers matching nothing while every permissive test stayed green.
4. **Whether an instance is bootstrapped is derived, never stored.** It is `CountOwnerHolders() > 0`.
   A flag would be a second source of truth for a fact the authorization tables already answer, and
   the two would disagree the first time somebody restored a backup.

`Redeem` ships with its tests and without its endpoint — the `/setup` form, the `503 setup_required`
gate and the `410 Gone` are the web slice (#307). That is ADR-0028 commitment 3's shape, and
`dkp admin create` is a real caller for the same account-creation path today.

### Consequences

- Good, because there is no default password and no code path that can create one.
- Good, because the last-owner rule survives a badly written role editor, an import, a Discord sync
  and an officer with a SQLite client — and `dkp admin create` needs host access, which is the same
  bar as reading the database file, so it grants nothing that was not already granted.
- **Bad, because eight trigger bodies repeat one predicate.** SQL cannot factor it, so a change to
  the definition of "holds `admin.owner`" is a new migration touching eight statements, and the only
  thing keeping them consistent is that they were written together and are tested together.
- **Bad, because a 12-step rebuild of any of the four tables drops its triggers silently.** The
  fresh-install fingerprint is the backstop, which is a CI gate rather than a runtime one — where the
  ledger's append-only triggers additionally have a boot check and a `/readyz` rung (#309).
- **Bad, because a time-boxed owner grant is refused where it might have been survivable.** An
  officer giving somebody `owner` until Friday cannot be the only holder, and the refusal says the
  rule rather than the reason.
- **Bad, because first run writes no audit row.** The audit hash chain is written inside
  `ledger.Service.Commit` and there is no writer for a non-ledger action yet (#308); the boot log
  names the account in the meantime.

### Reversal cost

**The table is cheap and the triggers are not.** Dropping `bootstrap_token` is a migration nobody
would notice; nothing references it. Changing the triggers' predicate means a new migration
recreating all eight — SQLite has no `ALTER TRIGGER` — whose interim state is an instance that can be
locked out. Widening it later (counting a time-boxed grant, say) is the safe direction; narrowing it
is not.
