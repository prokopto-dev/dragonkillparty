# Domain model and database schema

**Status:** normative for the schema. **Audience:** contributor, agent.
**Implements:** Phase 1 (ledger, pools, roster) and Phase 4 (raids, ticks, ingest). Phase 8 adds the
portal/CMS tables in §19.

`db/schema.hcl` is the single source of schema truth; Atlas generates the migrations from it. The
DDL below is **pseudo-DDL** — it is precise about columns, types, nullability, uniqueness, foreign
keys and indexes, and it is what `schema.hcl` must say. Where the two disagree, `schema.hcl` is the
artefact that ships and this file is the bug.

## 1. Conventions

Read [`00-canonical-conventions.md`](00-canonical-conventions.md) first. It is normative for money
(`_cp` integer centipoints), time (`_at` integer Unix microseconds, `_day` guild-local `TEXT`),
identifiers (ULID in `TEXT`), the two sequences (`seq` per-pool on `ledger_batch`, `event_seq`
global on `event_outbox`), enum casing, the permission and scope catalogues, tenancy, and the
database conventions block. None of that is restated here.

Four SQLite facts are load-bearing for this schema and are **not** in the conventions file:

| | Rule | Why | Enforcement |
|---|---|---|---|
| C1 | Every table is `STRICT`. `BIGINT`, `BOOLEAN`, `DATETIME`, `NUMERIC`, `DECIMAL` are illegal; use `INTEGER`. | `PRAGMA integrity_check` then verifies column content types — the cheapest guard against `"350.00"` landing in a centipoint column. | Atlas emits `STRICT`; a schema test asserts every table has it. |
| C2 | `*_norm` columns are **plain `TEXT`, normalised in Go** (NFKC + casefold + strip `'` `` ` `` `-`, collapse spaces), never `GENERATED`. | Generated-column expressions may use only deterministic scalar functions; core SQLite has no NFKC and `lower()` is ASCII-only. `ALTER TABLE ADD COLUMN` also cannot add a `STORED` column, so a future normalisation change would force a 12-step table rebuild. | `dkp doctor --check norm-drift` re-derives every `*_norm` and reports mismatches; an integration test asserts round-trip. |
| C3 | **Never `total()` in the ledger.** `sum()` returns an integer for integer inputs and raises on overflow; `total()` always returns a float and never raises. | `total()` over centipoints silently reintroduces float. | CI greps `total(` alongside `float64` in `internal/ledger` and `internal/strategy`. |
| C4 | Importer staging is a `stg_` **table-name prefix in the main database**, not an `ATTACH`ed file. | SQLite has no schemas, and WAL mode does not give atomic commits across attached databases — a crash mid-transform would leave the id-map and the domain rows inconsistent. | The importer has no `ATTACH`; an architectural test greps for it. |

**Reading the DDL.** `NULL` in a column list means nullable. `→` in a comment names the invariant or
job that maintains a derived column. A line starting `✗ Not copied from EQdkp:` marks a deliberate
divergence; §23 collects all of them.

**Reference to `permission(key)`.** Several tables FK to `permission(key)`, which is reconciled from
`internal/authz/catalogue.go` at boot. A hand-written key that is not in the catalogue is therefore
a **boot failure**, not a review finding.

---

## 2. Guild and server

```sql
CREATE TABLE guild (                              -- singleton; §9 of the conventions
  id               INTEGER NOT NULL PRIMARY KEY CHECK (id = 1),
  name             TEXT    NOT NULL,
  tag              TEXT    NOT NULL DEFAULT '',   -- <Guild Tag> as it appears in /who
  timezone         TEXT    NOT NULL DEFAULT 'UTC',-- IANA; renders all UI, buckets every *_day
  week_start       INTEGER NOT NULL DEFAULT 1 CHECK (week_start BETWEEN 0 AND 6),
  points_label     TEXT    NOT NULL DEFAULT 'DKP',
  points_precision INTEGER NOT NULL DEFAULT 2 CHECK (points_precision BETWEEN 0 AND 2),
                                                  -- DISPLAY rounding only; storage is always _cp
  locale           TEXT    NOT NULL DEFAULT 'en',
  public_standings INTEGER NOT NULL DEFAULT 0 CHECK (public_standings IN (0,1)),
  inactive_after_days INTEGER NULL,               -- NULL ⇒ never auto-flag; drives the sweep job
  auto_set_inactive   INTEGER NOT NULL DEFAULT 0 CHECK (auto_set_inactive IN (0,1)),
  hide_inactive       INTEGER NOT NULL DEFAULT 0 CHECK (hide_inactive IN (0,1)),
  artifact_retention_days INTEGER NOT NULL DEFAULT 180,   -- conventions §11
  redact_tells     INTEGER NOT NULL DEFAULT 1 CHECK (redact_tells IN (0,1)),
  settings_json    TEXT    NOT NULL DEFAULT '{}', -- cosmetic/uncontested settings only
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
) STRICT;
```

`points_precision`, `inactive_after_days`, `auto_set_inactive` and `hide_inactive` are **columns,
not `settings_json` keys**, because a job and a query read them. `settings_json` is validated on
write and never queried into.

```sql
CREATE TABLE server (
  id         TEXT NOT NULL PRIMARY KEY,
  key        TEXT NOT NULL,                       -- 'blue' | 'green' | 'red' | free-form (TAKP, Quarm)
  key_norm   TEXT NOT NULL,
  name       TEXT NOT NULL,
  ruleset    TEXT NOT NULL DEFAULT 'pve'  CHECK (ruleset IN ('pve','pvp')),
  era        TEXT NOT NULL DEFAULT 'velious'
             CHECK (era IN ('classic','kunark','velious','post_velious')),
  log_token  TEXT NOT NULL DEFAULT '',            -- the eqlog_<Char>_<token>.txt token; CAPTURED,
                                                  -- never assumed — the Green/Red tokens are unverified
  archived_at INTEGER NULL,                       -- Green cycle rollover: freeze, keep readable
  sort_order INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
) STRICT;
CREATE UNIQUE INDEX ux_server_key ON server(key_norm);
```

**Scope of `server` in 1.0.** `server` is a dimension *inside* the single tenant, not a tenant.
`character` and `raid` are server-scoped; `person`, `app_user`, `role` and the item catalogue are
not. Per the critique's cut list, 1.0 ships the column and the natural composite keys and **nothing
else**: `pool.server_id` is `NOT NULL` (no cross-server pools), the main-character index is per
person and not per `(person, server)`, and there is no multi-server UI. Adding those later changes
two index definitions and one column's nullability — it does not change the model. Building them
now would mean testing a combinatorial surface no guild has asked for.

---

## 3. Reference data

Seeded by migration, guild-editable. Class, race, title and zone data ship as **our own literals**;
EQdkp's game modules are CC BY-NC-SA and are never transcribed (conventions §15).

```sql
CREATE TABLE game_class (
  id        TEXT NOT NULL PRIMARY KEY,
  code      TEXT NOT NULL,                        -- 'shadow_knight'
  name      TEXT NOT NULL,                        -- 'Shadow Knight'
  name_norm TEXT NOT NULL,
  archetype TEXT NOT NULL CHECK (archetype IN ('tank','melee','priest','caster')),
  color     TEXT NOT NULL DEFAULT '',             -- #rrggbb, UI only
  sort_order INTEGER NOT NULL,
  enabled   INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
) STRICT;
CREATE UNIQUE INDEX ux_game_class_code ON game_class(code);

CREATE TABLE class_title (         -- the parser's single most load-bearing lookup: [52 Heretic] Zibaxia
  id        TEXT NOT NULL PRIMARY KEY,
  class_id  TEXT NOT NULL REFERENCES game_class(id),
  title     TEXT NOT NULL,                        -- 'Grave Lord', 'High Priest', 'Arch Mage'
  title_norm TEXT NOT NULL,
  min_level INTEGER NOT NULL,
  max_level INTEGER NOT NULL
) STRICT;
CREATE UNIQUE INDEX ux_class_title_norm ON class_title(title_norm);
CREATE INDEX ix_class_title_class ON class_title(class_id, min_level);

CREATE TABLE game_race (
  id TEXT NOT NULL PRIMARY KEY, code TEXT NOT NULL, name TEXT NOT NULL, name_norm TEXT NOT NULL,
  min_era TEXT NOT NULL DEFAULT 'classic',        -- Iksar are Kunark+
  sort_order INTEGER NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
) STRICT;
CREATE UNIQUE INDEX ux_game_race_code ON game_race(code);

CREATE TABLE game_race_class (     -- legality matrix; validates roster entry
  race_id  TEXT NOT NULL REFERENCES game_race(id),
  class_id TEXT NOT NULL REFERENCES game_class(id),
  PRIMARY KEY (race_id, class_id)
) STRICT, WITHOUT ROWID;

CREATE TABLE zone (
  id TEXT NOT NULL PRIMARY KEY, name TEXT NOT NULL, name_norm TEXT NOT NULL,
  short_name TEXT NOT NULL DEFAULT '',            -- from `You have entered <zone>.`
  era TEXT NOT NULL,
  is_raid_zone INTEGER NOT NULL DEFAULT 0 CHECK (is_raid_zone IN (0,1)),
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
) STRICT;
CREATE UNIQUE INDEX ux_zone_norm ON zone(name_norm);
```

> ✗ **Not copied from EQdkp:** classes and races as integer ids inside a `profiledata` JSON blob
> whose meaning depends on `config.default_game` — that makes "how many Clerics do we have?" a
> full-table JSON scan and makes the values meaningless without a PHP module.

---

## 4. Identity

### 4.1 Users and identities

```sql
CREATE TABLE app_user (
  id            TEXT NOT NULL PRIMARY KEY,
  username      TEXT NOT NULL, username_norm TEXT NOT NULL,
  email         TEXT NULL,     email_norm    TEXT NULL,
  email_verified_at INTEGER NULL,
  display_name  TEXT NOT NULL DEFAULT '',
  timezone      TEXT NULL,                        -- NULL ⇒ inherit guild.timezone
  locale        TEXT NULL,
  avatar_media_id TEXT NULL REFERENCES media(id),
  state         TEXT NOT NULL DEFAULT 'active'
                CHECK (state IN ('pending','active','suspended','disabled')),
  last_login_at INTEGER NULL,
  failed_logins INTEGER NOT NULL DEFAULT 0,
  locked_until_at INTEGER NULL,
  mfa_totp_secret_enc BLOB NULL,                  -- AES-GCM under the instance key
  mfa_enrolled_at INTEGER NULL,
  mfa_required  INTEGER NOT NULL DEFAULT 0 CHECK (mfa_required IN (0,1)),
  deleted_at    INTEGER NULL,
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
) STRICT;
CREATE UNIQUE INDEX ux_user_username ON app_user(username_norm) WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX ux_user_email    ON app_user(email_norm)
  WHERE email_norm IS NOT NULL AND deleted_at IS NULL;

CREATE TABLE user_identity (
  id           TEXT NOT NULL PRIMARY KEY,
  user_id      TEXT NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
  provider     TEXT NOT NULL CHECK (provider IN ('local','discord','oidc')),
  provider_key TEXT NOT NULL DEFAULT '',          -- OIDC issuer discriminator; '' for local/discord
  subject      TEXT NOT NULL,                     -- local: username_norm | discord: snowflake | oidc: sub
  -- local only
  password_hash   TEXT NULL,                      -- argon2id PHC string; NULL ⇒ login disabled
  password_algo   TEXT NULL CHECK (password_algo IS NULL OR password_algo IN ('argon2id')),
  password_set_at INTEGER NULL,
  must_reset      INTEGER NOT NULL DEFAULT 0 CHECK (must_reset IN (0,1)),
  -- oauth/oidc only
  access_token_enc BLOB NULL, refresh_token_enc BLOB NULL, token_expires_at INTEGER NULL,
  scopes       TEXT NOT NULL DEFAULT '',          -- space-separated, as granted
  profile_json TEXT NOT NULL DEFAULT '{}',        -- last-seen avatar, global_name, guild roles
  last_used_at INTEGER NULL,
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
) STRICT;
CREATE UNIQUE INDEX ux_identity_subject ON user_identity(provider, provider_key, subject);
CREATE INDEX ix_identity_user ON user_identity(user_id);

CREATE TABLE mfa_recovery_code (                  -- TOTP enrolment ships in Phase 2
  id TEXT NOT NULL PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
  code_hash BLOB NOT NULL, used_at INTEGER NULL, created_at INTEGER NOT NULL
) STRICT;
CREATE UNIQUE INDEX ux_recovery_hash ON mfa_recovery_code(code_hash);
```

- **`password_algo` has exactly one legal value.** EQdkp carries seven verifiers (bcrypt ×3,
  argon2 ×2, phpass sha512, ext_des, bare MD5, plus a `hash:salt` pre-hash scheme). The importer sets
  `password_hash = NULL`, `must_reset = 1`, and mints claim invitations (§4.4).
  > ✗ **Not copied:** legacy password verifiers. Migration is the natural moment to force one reset.
- **A local identity is structurally mandatory.** The first-run wizard creates one before any OAuth
  app can exist. An integration test asserts at least one non-suspended user holds `admin.owner`
  through a usable local identity.
- **Discord guild-role mapping is a hint, not authority.** `discord_role_map(discord_role_id,
  role_id, scope_type, scope_id, auto_sync)` drives *suggested* assignments; the grant is still a
  `role_assignment` row, written with `granted_via = 'discord_sync'` and an audit row.
- **MFA/TOTP enrolment ships in Phase 2**, not post-1.0, because step-up (§5) gates token minting,
  role edits, backup download and import commit. Advertising a gate that cannot be satisfied is
  worse than having no gate.

### 4.2 Sessions

```sql
CREATE TABLE session (
  id           TEXT NOT NULL PRIMARY KEY,         -- ULID; NOT the cookie value
  user_id      TEXT NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
  token_hash   BLOB NOT NULL,                     -- SHA-256 of the 32-byte opaque cookie secret
  identity_id  TEXT NULL REFERENCES user_identity(id),
  created_at   INTEGER NOT NULL,
  last_seen_at INTEGER NOT NULL,
  expires_at   INTEGER NOT NULL,
  absolute_expires_at INTEGER NOT NULL,           -- non-extendable ceiling
  revoked_at   INTEGER NULL,
  ip           TEXT NOT NULL DEFAULT '',
  user_agent   TEXT NOT NULL DEFAULT '',
  mfa_satisfied_at INTEGER NULL                   -- step-up clock: re-auth within 5 min
) STRICT;
CREATE UNIQUE INDEX ux_session_token ON session(token_hash);
CREATE INDEX ix_session_user_active ON session(user_id, expires_at) WHERE revoked_at IS NULL;
```

The cookie is `__Host-dkp_session` (conventions §7) — that exact string appears in the OpenAPI
`securitySchemes`. Rotate `token_hash` (new row, old row `revoked_at`) on login and on any privilege
change. "Sign out everywhere" is one `UPDATE … WHERE user_id = ?`.

### 4.3 Service accounts, PATs, feed tokens

```sql
CREATE TABLE service_account (
  id            TEXT NOT NULL PRIMARY KEY,
  name          TEXT NOT NULL, name_norm TEXT NOT NULL,
  description   TEXT NOT NULL DEFAULT '',
  owner_user_id TEXT NOT NULL REFERENCES app_user(id),   -- a HUMAN, for audit + notification
  state         TEXT NOT NULL DEFAULT 'active' CHECK (state IN ('active','disabled')),
  created_by    TEXT NOT NULL REFERENCES app_user(id),
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
) STRICT;
CREATE UNIQUE INDEX ux_service_account_name ON service_account(name_norm);

CREATE TABLE api_token (
  id                 TEXT NOT NULL PRIMARY KEY,
  prefix             TEXT NOT NULL,               -- 8 chars, public, printed in logs and the UI
  token_hash         BLOB NOT NULL,               -- HMAC-SHA256(server_pepper, secret):
                                                  -- one indexed lookup + one constant-time compare
  service_account_id TEXT NOT NULL REFERENCES service_account(id) ON DELETE CASCADE,
  name               TEXT NOT NULL,
  scopes             TEXT NOT NULL,               -- space-separated, closed enum (conventions §6)
  expires_at         INTEGER NULL,
  last_used_at       INTEGER NULL, last_used_ip TEXT NOT NULL DEFAULT '',
  rate_limit_rpm     INTEGER NOT NULL DEFAULT 600,
  created_by         TEXT NOT NULL REFERENCES app_user(id),
  revoked_at         INTEGER NULL,
  revoked_by         TEXT NULL REFERENCES app_user(id),
  revoke_reason      TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL
) STRICT;
CREATE UNIQUE INDEX ux_api_token_prefix ON api_token(prefix);
CREATE UNIQUE INDEX ux_api_token_hash   ON api_token(token_hash);
CREATE INDEX ix_api_token_sa ON api_token(service_account_id) WHERE revoked_at IS NULL;

CREATE TABLE feed_token (          -- single-purpose, path-embedded, never a PAT
  id TEXT NOT NULL PRIMARY KEY, token_hash BLOB NOT NULL,
  user_id TEXT NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
  kind TEXT NOT NULL CHECK (kind IN ('raids_ical','calendar_ical','standings_rss','articles_rss')),
  revoked_at INTEGER NULL, last_used_at INTEGER NULL, created_at INTEGER NOT NULL
) STRICT;
CREATE UNIQUE INDEX ux_feed_token_hash ON feed_token(token_hash);
```

> ✗ **Not copied:** `__config.api_key` — one global token that impersonates the first superadmin,
> with no scopes, no expiry, no owner, no last-used record, accepted in a query string. `?atoken=`
> is honoured **only** by the compat shim, resolves to a real `api_token` row, and logs a
> deprecation warning naming the token's `prefix`. `users.exchange_key` is likewise never imported.

### 4.4 Invitations and claim tokens

```sql
CREATE TABLE invitation (
  id         TEXT NOT NULL PRIMARY KEY,
  code_hash  BLOB NOT NULL,
  email      TEXT NULL, email_norm TEXT NULL,     -- NULL ⇒ shareable link
  role_id    TEXT NULL REFERENCES role(id),
  scope_type TEXT NOT NULL DEFAULT 'global' CHECK (scope_type IN ('global','pool','raid_group')),
  scope_id   TEXT NULL,
  person_id  TEXT NULL REFERENCES person(id),     -- pre-bind to a roster entry (the importer uses this)
  max_uses   INTEGER NOT NULL DEFAULT 1,
  uses       INTEGER NOT NULL DEFAULT 0,
  expires_at INTEGER NOT NULL,
  revoked_at INTEGER NULL,
  created_by TEXT NOT NULL REFERENCES app_user(id),
  note       TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL,
  CHECK (uses <= max_uses),
  CHECK ((scope_type = 'global') = (scope_id IS NULL))
) STRICT;
CREATE UNIQUE INDEX ux_invitation_code ON invitation(code_hash);

CREATE TABLE invitation_redemption (
  id TEXT NOT NULL PRIMARY KEY,
  invitation_id TEXT NOT NULL REFERENCES invitation(id),
  user_id       TEXT NOT NULL REFERENCES app_user(id),
  redeemed_at INTEGER NOT NULL, ip TEXT NOT NULL DEFAULT ''
) STRICT;
CREATE UNIQUE INDEX ux_invite_redeem ON invitation_redemption(invitation_id, user_id);
```

The importer's "print a list of one-time claim tokens for Discord distribution" is exactly
`invitation` rows with `person_id` pre-bound and `max_uses = 1`. There is no second mechanism.

---

## 5. Roles and permissions

**RBAC with named permissions plus scoped role assignments. Not ABAC, and no deny.**

| Dimension | EQdkp Plus | Dragon Kill Party |
|---|---|---|
| Grant algebra | tri-state Y/N/inherit, union of groups, plus per-user overrides | **allow-only, set union.** No deny. |
| Superuser | hardcoded `group_id = 2` short-circuit in `acl.class.php` | `admin.owner`, an **ordinary permission row** held by ≥1 user |
| Scoping | two hardcoded `*_grpleader` permissions | `role_assignment.scope_type` / `scope_id` — any role, scoped to a pool or a raid group |
| Page-view perms | ~40 auto-generated `po_*` keys, one per page object | the read permissions in the catalogue, plus `guild.public_standings`. Pages are not resources. |
| Catalogue | `__auth_options`, mutable at runtime | code-defined, reconciled into a table at boot for FK integrity |
| Bots | token = all-or-nothing, becomes superadmin | role permissions **∩** token scopes |

**Why no deny.** Deny plus union is a lattice: adding a role can *remove* capability and evaluation
order becomes load-bearing. The two things deny is used for have better answers — temporary
revocation is `role_assignment.suspended_until_at`, and "this one person must not touch loot" is
"don't grant the role, or split the role". Per-user deny is a documented non-goal.

**Why no ABAC.** Rules like "an officer may adjust points only for characters in their own raid
group, within 72 hours of the raid" cannot be rendered as a table, a volunteer officer cannot audit
them, and "who can do X?" becomes a solver query. The residual is expressed as explicit domain
columns that are visible in the settings UI — `pool.dispute_window_hours`,
`pool.retro_edit_max_age_days` — not as invisible policy attributes.

```sql
CREATE TABLE permission (          -- reconciled from internal/authz/catalogue.go on every boot
  key         TEXT NOT NULL PRIMARY KEY,          -- 'dkp.adjust'
  category    TEXT NOT NULL,                      -- 'dkp' — groups the matrix UI
  label       TEXT NOT NULL,
  description TEXT NOT NULL,
  is_dangerous INTEGER NOT NULL DEFAULT 0 CHECK (is_dangerous IN (0,1)),
  requires_step_up INTEGER NOT NULL DEFAULT 0 CHECK (requires_step_up IN (0,1)),
  orphaned_at INTEGER NULL,                       -- in the DB, absent from code (post-downgrade)
  sort_order  INTEGER NOT NULL DEFAULT 0
) STRICT, WITHOUT ROWID;

CREATE TABLE role (
  id          TEXT NOT NULL PRIMARY KEY,
  key         TEXT NULL,                          -- non-NULL ⇒ built-in: not deletable, not renamable
  name        TEXT NOT NULL, name_norm TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  is_builtin  INTEGER NOT NULL DEFAULT 0 CHECK (is_builtin IN (0,1)),
  applies_to  TEXT NOT NULL DEFAULT 'both' CHECK (applies_to IN ('user','service_account','both')),
  sort_order  INTEGER NOT NULL DEFAULT 0,
  deleted_at  INTEGER NULL,
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
) STRICT;
CREATE UNIQUE INDEX ux_role_key  ON role(key) WHERE key IS NOT NULL;
CREATE UNIQUE INDEX ux_role_name ON role(name_norm) WHERE deleted_at IS NULL;

CREATE TABLE role_permission (
  role_id        TEXT NOT NULL REFERENCES role(id) ON DELETE CASCADE,
  permission_key TEXT NOT NULL REFERENCES permission(key),   -- FK ⇒ a bad key is a BOOT FAILURE
  PRIMARY KEY (role_id, permission_key)
) STRICT, WITHOUT ROWID;

CREATE TABLE role_assignment (
  id           TEXT NOT NULL PRIMARY KEY,
  subject_kind TEXT NOT NULL CHECK (subject_kind IN ('user','service_account')),
  subject_id   TEXT NOT NULL,
  role_id      TEXT NOT NULL REFERENCES role(id) ON DELETE CASCADE,
  scope_type   TEXT NOT NULL DEFAULT 'global' CHECK (scope_type IN ('global','pool','raid_group')),
  scope_id     TEXT NULL,                         -- NULL iff scope_type = 'global'
  suspended_until_at INTEGER NULL,                -- temporary revocation; no deny needed
  granted_by   TEXT NULL REFERENCES app_user(id),
  granted_via  TEXT NOT NULL DEFAULT 'manual'
               CHECK (granted_via IN ('manual','invitation','discord_sync','import','bootstrap')),
  expires_at   INTEGER NULL,
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL,
  CHECK ((scope_type = 'global') = (scope_id IS NULL))
) STRICT;
CREATE UNIQUE INDEX ux_role_assign
  ON role_assignment(subject_kind, subject_id, role_id, scope_type, COALESCE(scope_id,''));
CREATE INDEX ix_role_assign_subject ON role_assignment(subject_kind, subject_id);
```

**Effective permission check** — one query, cached per request on the `Principal`:

```sql
-- name: EffectivePermissions :many
SELECT rp.permission_key, ra.scope_type, ra.scope_id
  FROM role_assignment ra
  JOIN role_permission rp ON rp.role_id = ra.role_id
 WHERE ra.subject_kind = ?1 AND ra.subject_id = ?2
   AND (ra.expires_at IS NULL OR ra.expires_at > ?3)
   AND (ra.suspended_until_at IS NULL OR ra.suspended_until_at <= ?3);
```

`Can(perm, scope)` is true when a row matches `perm` and either `scope_type = 'global'` or
`(scope_type, scope_id)` matches. **`admin.owner` short-circuits to true as a row, not as a
hardcoded id** — revoking it is an ordinary `role_assignment` delete, and an integration test
asserts the last holder cannot be removed.

**Capability floor.** Effective capability = role permissions ∩ token scopes (conventions §6). There
is no `admin:*` scope. Operations that alter authentication, authorisation or bulk-export state —
minting/rotating/revoking tokens, editing roles and assignments, downloading backups, bulk PII
reads, committing an import — are **session + step-up only** and carry no scope at all;
`permission.requires_step_up` is the column the middleware reads.

### 5.1 Built-in role seed

`is_builtin = 1`, `key` non-NULL. Permission keys come from the catalogue in
[conventions §6](00-canonical-conventions.md); this table is the seed, not a second catalogue.

| `role.key` | `applies_to` | Grants |
|---|---|---|
| `guest` | user | the six `*.read` keys, visible only when `guild.public_standings = 1` |
| `member` | user | `guest` + `cms.read`. Signing yourself up and claiming your own character are **ownership**, not permissions. |
| `raider` | user | `= member`. A distinct assignable name for guilds that want the rank distinction. |
| `raid_leader` | user | `member` + `raid.create` `raid.update` `raid.finalize` `raid.tick.create` `raid.tick.delete` `item.award` `signup.manage`. Commonly assigned **scoped to a raid group**. |
| `officer` | user | `raid_leader` + `roster.write` `person.merge` `character.claim.approve` `dkp.adjust` `bid.manage` `bid.reveal_early` `item.alias.manage` `calendar.write` `cms.write` `audit.read` |
| `admin` | user | `officer` + `dkp.decay.run` `ledger.reverse` `cms.moderate` `import.run` `import.commit` `webhook.manage` `token.mint` `token.revoke` `admin.settings` `admin.security.manage` `admin.roles.manage` `admin.backup` `person.pii.read` `ops.read` |
| `owner` | user | `admin` + `admin.owner` |
| `bot_readonly` | service_account | the `*.read` keys only |
| `bot_raid` | service_account | `bot_readonly` + `raid.create` `raid.update` `raid.tick.create` `item.award`. **No `dkp.adjust`.** |

`bid.reveal_early` is seeded `is_dangerous = 1`: the UI requires an extra confirmation and every use
writes an `audit_log` row naming the session.

---

## 6. Roster: person, account, character

The largest deliberate divergence from EQdkp, which modelled alts as a `members.member_main_id`
self-join.

```
person        the human being. Roster identity. Attendance aggregates here.
  ├─ account    the balance holder. 1:1 with person, PLUS system accounts with no person.
  └─ character  an in-game name on a server. Attendance rows and log lines land here.
```

**Why three layers and not two.** `account` exists separately because zero-sum splits, rot handling
and write-offs need ledger-addressable non-human targets. Without them `SumZero` is unsatisfiable in
the N=1 and rotted-item cases and largest-remainder residue has nowhere to go. One extra join buys a
total conservation invariant over the whole pool.

### 6.1 person and account

```sql
CREATE TABLE person (
  id           TEXT NOT NULL PRIMARY KEY,
  display_name TEXT NOT NULL,                     -- usually the main's name; editable
  display_name_norm TEXT NOT NULL,
  user_id      TEXT NULL REFERENCES app_user(id), -- the login, if any
  rank_id      TEXT NULL REFERENCES guild_rank(id),
  state        TEXT NOT NULL DEFAULT 'active'
               CHECK (state IN ('trial','active','inactive','loa','retired','removed')),
  state_changed_at INTEGER NOT NULL,
  joined_at    INTEGER NULL,                      -- tenure start: the pct_real denominator floor
  left_at      INTEGER NULL,
  -- away mode (EQdkp __users.awaymode_*): drives the signup filter and the raid-planning sheet
  away_from_at  INTEGER NULL,
  away_until_at INTEGER NULL,
  away_note     TEXT NOT NULL DEFAULT '',
  merged_into_person_id TEXT NULL REFERENCES person(id),
  is_system    INTEGER NOT NULL DEFAULT 0 CHECK (is_system IN (0,1)),
  notes        TEXT NOT NULL DEFAULT '',
  deleted_at   INTEGER NULL,
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL,
  CHECK (away_until_at IS NULL OR away_from_at IS NOT NULL)
) STRICT;
CREATE UNIQUE INDEX ux_person_user ON person(user_id)
  WHERE user_id IS NOT NULL AND deleted_at IS NULL;
CREATE INDEX ix_person_state  ON person(state)
  WHERE deleted_at IS NULL AND merged_into_person_id IS NULL;
CREATE INDEX ix_person_merged ON person(merged_into_person_id) WHERE merged_into_person_id IS NOT NULL;
CREATE INDEX ix_person_away   ON person(away_until_at) WHERE away_until_at IS NOT NULL;

CREATE TABLE account (
  id         TEXT NOT NULL PRIMARY KEY,
  kind       TEXT NOT NULL CHECK (kind IN ('person','system')),
  person_id  TEXT NULL REFERENCES person(id),
  system_key TEXT NULL CHECK (system_key IS NULL OR
              system_key IN ('guild_bank','residue','write_off','import_opening')),
  label      TEXT NOT NULL,
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL,
  CHECK ((kind = 'person') = (person_id  IS NOT NULL)),
  CHECK ((kind = 'system') = (system_key IS NOT NULL))
) STRICT;
CREATE UNIQUE INDEX ux_account_person ON account(person_id)  WHERE person_id  IS NOT NULL;
CREATE UNIQUE INDEX ux_account_system ON account(system_key) WHERE system_key IS NOT NULL;
```

**Away mode** was flagged by the critique because the importer maps `__users.awaymode_*` to a
`person.away` column that did not exist. It is three columns and one predicate: the signup list and
the CSV export mark anyone whose `[away_from_at, away_until_at]` window covers the event, and
`ix_person_away` answers "who is away this week" without a scan. Expired windows are cleared by the
nightly sweep, and the change is audit-logged, so away history lives in `audit_log`, not in a
history table nobody would read.

**System accounts are addressable through the API** at `/accounts/{account_id}` — otherwise the
`Conserved` invariant cannot be verified by a member, which is the point of publishing it.

### 6.2 character

```sql
CREATE TABLE character (
  id           TEXT NOT NULL PRIMARY KEY,
  person_id    TEXT NOT NULL REFERENCES person(id),
  server_id    TEXT NOT NULL REFERENCES server(id),
  name         TEXT NOT NULL, name_norm TEXT NOT NULL,
  class_id     TEXT NULL REFERENCES game_class(id),   -- NULL when only ever seen as [ANONYMOUS]
  race_id      TEXT NULL REFERENCES game_race(id),
  level        INTEGER NULL CHECK (level IS NULL OR level BETWEEN 1 AND 60),
  role         TEXT NOT NULL DEFAULT 'alt'
               CHECK (role IN ('main','alt','box','bank','mule','unknown')),
  guild_rank_id TEXT NULL REFERENCES guild_rank(id),  -- in-game rank, per character
  default_raid_role_id TEXT NULL REFERENCES raid_role(id),
  raid_group_id TEXT NULL REFERENCES raid_group(id),
  active       INTEGER NOT NULL DEFAULT 1 CHECK (active IN (0,1)),
  hidden       INTEGER NOT NULL DEFAULT 0 CHECK (hidden IN (0,1)),
  first_seen_at INTEGER NULL,
  last_seen_at  INTEGER NULL,                     -- last appearance in ANY tick or artifact
  created_from TEXT NOT NULL DEFAULT 'manual'
               CHECK (created_from IN ('manual','claim','parser','import','roster_dump','reconcile')),
  avatar_media_id TEXT NULL REFERENCES media(id),
  notes        TEXT NOT NULL DEFAULT '',
  deleted_at   INTEGER NULL,
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
) STRICT;
CREATE UNIQUE INDEX ux_character_name ON character(server_id, name_norm) WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX ux_character_main ON character(person_id)
  WHERE role = 'main' AND deleted_at IS NULL;    -- ONE main per person (see §2 on server scope)
CREATE INDEX ix_character_person   ON character(person_id) WHERE deleted_at IS NULL;
CREATE INDEX ix_character_class    ON character(class_id, active) WHERE deleted_at IS NULL;
CREATE INDEX ix_character_lastseen ON character(last_seen_at);
```

| EQdkp Plus | Consequence | Dragon Kill Party |
|---|---|---|
| `members.member_main_id` self-FK where `NULL`, `0` and self-reference all mean "is a main" | three sentinels for one concept; every read normalises `main_id = (main_id > 0) ? main_id : member_id` | `character.person_id NOT NULL` + a partial unique index for the main. No sentinel. |
| Single-level only, enforced by an insert-time ring check that stale data defeats | `member_main_id` **cycles** exist in real databases | Cycles are structurally impossible. `person` self-references only through `merged_into_person_id`, which merge keeps a DAG (merge always targets a non-merged person; test-enforced). |
| `with_twink` threaded through every point and attendance read; `show_twinks` is global | every query exists twice, and a guild cannot mix policies | No such parameter. Points are on `account`, which is per person. Rolling up alts is the storage layout, not an operation. Per-pool policy is `pool.alt_policy`. |
| Attendance and points both keyed on a character | a boxed raider in one dump earns twice | Attendance is on `character`, ledger on `account`; the planner does `SELECT DISTINCT account_id`. A schema fix, not a query fix. |
| `rank_hide` is a property of the **rank** | guilds create a fake "Hidden" rank to hide one bank mule | `character.hidden` **and** `guild_rank.hidden_default`. Both. |
| Re-binding an alt rewrites nothing; totals silently change | no record of when or why | Re-parenting is an event (§6.6). |

**Character auto-creation is off.** Per [conventions §12](00-canonical-conventions.md), an unknown
name from a parse lands in `reconciliation_item`; it does **not** auto-provision a person. The award
is quarantined, never dropped, never silently attributed. `on_unresolved = 'create'` is an explicit
officer choice on a submission.

> This supersedes the earlier `person(state='trial')` auto-provision design. `person_id NOT NULL`
> survives; what changed is that the person is created by a human decision, not by the parser.

### 6.3 Name and attribute history

```sql
CREATE TABLE character_name_history (   -- renames and rerolls; the parser resolves against this
  id TEXT NOT NULL PRIMARY KEY,
  character_id TEXT NOT NULL REFERENCES character(id) ON DELETE CASCADE,
  name TEXT NOT NULL, name_norm TEXT NOT NULL,
  server_id TEXT NOT NULL REFERENCES server(id),
  valid_from_at INTEGER NOT NULL, valid_to_at INTEGER NULL,
  changed_by TEXT NULL REFERENCES app_user(id),
  reason TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL
) STRICT;
CREATE INDEX ix_cnh_lookup ON character_name_history(server_id, name_norm, valid_to_at);

CREATE TABLE character_attribute_history (  -- level/class/rank as of a raid night
  id TEXT NOT NULL PRIMARY KEY,
  character_id TEXT NOT NULL REFERENCES character(id) ON DELETE CASCADE,
  observed_at INTEGER NOT NULL,
  level INTEGER NULL,
  class_id TEXT NULL REFERENCES game_class(id),
  guild_tag TEXT NOT NULL DEFAULT '',
  source TEXT NOT NULL CHECK (source IN ('who','raid_dump','guild_dump','manual','import')),
  artifact_id TEXT NULL REFERENCES artifact(id)
) STRICT;
CREATE INDEX ix_cah_char ON character_attribute_history(character_id, observed_at DESC);
```

`character_attribute_history` exists because a raid dump records the level *at the time of the
raid* and guilds gate DKP on level. Without it, "was Bob 55 on 12 March?" is unanswerable and a
retroactive level edit silently changes past eligibility.

### 6.4 Custom profile fields — typed and queryable

EQdkp's `__member_profilefields` / `__user_profilefields` are admin-definable typed fields that
guilds use for alt tracking, DKP notes and class-spec. The critique flagged that the importer maps
them to a `character_field_def` table that did not exist, and that storing them in an unqueryable
JSON blob is a parity regression: "show me every character whose *Alt of* field says Tankguy" would
become a full-table scan.

```sql
CREATE TABLE character_field_def (
  id         TEXT NOT NULL PRIMARY KEY,
  key        TEXT NOT NULL, key_norm TEXT NOT NULL,   -- stable API/CSV name
  label      TEXT NOT NULL,
  help       TEXT NOT NULL DEFAULT '',
  kind       TEXT NOT NULL CHECK (kind IN ('text','int','enum','bool','date','url')),
  options_json TEXT NOT NULL DEFAULT '[]',        -- enum choices; the VALUE is queried, not this
  applies_to TEXT NOT NULL DEFAULT 'character' CHECK (applies_to IN ('character','person')),
  required   INTEGER NOT NULL DEFAULT 0 CHECK (required IN (0,1)),
  visibility TEXT NOT NULL DEFAULT 'members'
             CHECK (visibility IN ('public','members','officers')),
  searchable INTEGER NOT NULL DEFAULT 1 CHECK (searchable IN (0,1)),
  sort_order INTEGER NOT NULL DEFAULT 0,
  deleted_at INTEGER NULL,
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
) STRICT;
CREATE UNIQUE INDEX ux_cfd_key ON character_field_def(key_norm) WHERE deleted_at IS NULL;

CREATE TABLE character_field_value (            -- typed EAV; one populated value_* column per row
  id           TEXT NOT NULL PRIMARY KEY,
  field_def_id TEXT NOT NULL REFERENCES character_field_def(id) ON DELETE CASCADE,
  character_id TEXT NULL REFERENCES character(id) ON DELETE CASCADE,
  person_id    TEXT NULL REFERENCES person(id)   ON DELETE CASCADE,
  value_text      TEXT NULL,
  value_text_norm TEXT NULL,                     -- normalised in Go; the equality/prefix index
  value_int       INTEGER NULL,                  -- int, bool (0/1) and date (Micros) all land here
  updated_by TEXT NULL REFERENCES app_user(id),
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL,
  CHECK ((character_id IS NULL) <> (person_id IS NULL))
) STRICT;
CREATE UNIQUE INDEX ux_cfv_char   ON character_field_value(field_def_id, character_id)
  WHERE character_id IS NOT NULL;
CREATE UNIQUE INDEX ux_cfv_person ON character_field_value(field_def_id, person_id)
  WHERE person_id IS NOT NULL;
CREATE INDEX ix_cfv_text ON character_field_value(field_def_id, value_text_norm)
  WHERE value_text_norm IS NOT NULL;
CREATE INDEX ix_cfv_int  ON character_field_value(field_def_id, value_int)
  WHERE value_int IS NOT NULL;
```

Three typed columns, not six: `bool` stores 0/1 and `date` stores Micros in `value_int`, so filter
and sort work for every kind with two indexes. The Go layer validates against `kind` and
`options_json` on write and rejects a mismatched write with `422 invalid_field_value`.

> ✗ **Not copied:** custom fields as PHP-serialised blobs in `members.profiledata` and
> `users.custom_fields`. Every queryable fact is a real column or an indexed EAV value.

### 6.5 Claims and verification

```sql
CREATE TABLE character_claim (
  id           TEXT NOT NULL PRIMARY KEY,
  character_id TEXT NOT NULL REFERENCES character(id),
  user_id      TEXT NOT NULL REFERENCES app_user(id),
  state        TEXT NOT NULL DEFAULT 'pending'
               CHECK (state IN ('pending','verified','rejected','revoked','expired')),
  method       TEXT NULL CHECK (method IS NULL OR
               method IN ('officer_manual','roster_dump','log_nonce','import')),
  nonce            TEXT NULL,                    -- 'dkp-verify a7f3x' — typed in guild chat
  nonce_expires_at INTEGER NULL,
  evidence_artifact_id TEXT NULL REFERENCES artifact(id),
  evidence_json TEXT NOT NULL DEFAULT '{}',      -- matched log line, dump row, officer note
  requested_at INTEGER NOT NULL,
  decided_at   INTEGER NULL,
  decided_by   TEXT NULL REFERENCES app_user(id),
  decision_note TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
) STRICT;
CREATE UNIQUE INDEX ux_claim_open     ON character_claim(character_id) WHERE state = 'pending';
CREATE UNIQUE INDEX ux_claim_verified ON character_claim(character_id) WHERE state = 'verified';
CREATE UNIQUE INDEX ux_claim_nonce    ON character_claim(nonce)
  WHERE nonce IS NOT NULL AND state = 'pending';
CREATE INDEX ix_claim_user ON character_claim(user_id, state);
```

Strength order, recorded so a dispute can weigh it: `officer_manual` (always available, ships
first) < `roster_dump` (the officer's token-bearing client is the trust anchor) < `log_nonce`
(proves *current control* of the character). Never screenshots. A verified claim sets
`character.person_id` to the claimant's person, merging (§6.6) if the character already belonged to
a placeholder person. Approving requires `character.claim.approve`.

### 6.6 Merge and re-parenting

```sql
CREATE TABLE person_merge (        -- the addressable receipt; ledger_batch.source_ref points here
  id               TEXT NOT NULL PRIMARY KEY,
  source_person_id TEXT NOT NULL REFERENCES person(id),
  target_person_id TEXT NOT NULL REFERENCES person(id),
  characters_moved INTEGER NOT NULL,
  balances_json    TEXT NOT NULL DEFAULT '{}',   -- per (pool, balance_kind) amount moved; display only
  re_attribution_batch_id TEXT NULL REFERENCES ledger_batch(id),
  reason      TEXT NOT NULL DEFAULT '',
  performed_by TEXT NULL REFERENCES app_user(id),
  created_at  INTEGER NOT NULL,
  CHECK (source_person_id <> target_person_id)
) STRICT;
CREATE INDEX ix_person_merge_target ON person_merge(target_person_id, created_at DESC);
```

A merge sets `source.merged_into_person_id = target`, re-parents every `character`, and — at the
officer's explicit choice — writes one `ledger_batch(kind='re_attribution', source_ref =
'person_merge:<ulid>')` that debits the source account and credits the target so the *current*
balance follows the human. **Historic `ledger_entry` rows never move**; they keep the `account_id`
that was correct when the money moved.

Merge is the **only** corrective operation for a mis-attributed character — one well-tested code
path instead of "claim", "link" and "adopt". `person.merge` is the permission.

**The consequence to publish, per the critique.** Ledger entries keep their historic `account_id`,
but attendance joins through `character` **as of now**. Re-parenting an alt therefore changes a
member's *published attendance percentage* while leaving their balance untouched. Mechanisms, not
prose: the merge/re-parent confirmation screen shows the before/after `attendance_rollup.pct_bp`
for both persons and requires acknowledgement; an `audit_log` row with `action = 'person.merge'`
carries both figures in `before_json`/`after_json`; and an integration test asserts the recomputed
rollup matches the figure the UI predicted.

### 6.7 Ranks, raid roles, raid groups

```sql
CREATE TABLE guild_rank (
  id TEXT NOT NULL PRIMARY KEY, name TEXT NOT NULL, name_norm TEXT NOT NULL,
  prefix TEXT NOT NULL DEFAULT '', suffix TEXT NOT NULL DEFAULT '',
  icon_media_id TEXT NULL REFERENCES media(id), color TEXT NOT NULL DEFAULT '',
  hidden_default INTEGER NOT NULL DEFAULT 0 CHECK (hidden_default IN (0,1)),
  is_special INTEGER NOT NULL DEFAULT 0 CHECK (is_special IN (0,1)),  -- bank mules, DKP sinks
  is_default INTEGER NOT NULL DEFAULT 0 CHECK (is_default IN (0,1)),
  sort_order INTEGER NOT NULL, deleted_at INTEGER NULL,
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
) STRICT;
CREATE UNIQUE INDEX ux_rank_name    ON guild_rank(name_norm) WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX ux_rank_default ON guild_rank(is_default) WHERE is_default = 1;

CREATE TABLE raid_role (           -- tank/healer/dps, for calendar signups
  id TEXT NOT NULL PRIMARY KEY, name TEXT NOT NULL, name_norm TEXT NOT NULL,
  icon_media_id TEXT NULL REFERENCES media(id),
  sort_order INTEGER NOT NULL, deleted_at INTEGER NULL,
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
) STRICT;
CREATE UNIQUE INDEX ux_raid_role_name ON raid_role(name_norm) WHERE deleted_at IS NULL;

CREATE TABLE raid_role_class (     -- ✗ Not copied: EQdkp's pipe-delimited role_classes VARCHAR(255)
  raid_role_id TEXT NOT NULL REFERENCES raid_role(id) ON DELETE CASCADE,
  class_id     TEXT NOT NULL REFERENCES game_class(id),
  PRIMARY KEY (raid_role_id, class_id)
) STRICT, WITHOUT ROWID;

CREATE TABLE raid_group (
  id TEXT NOT NULL PRIMARY KEY, name TEXT NOT NULL, name_norm TEXT NOT NULL,
  color TEXT NOT NULL DEFAULT '', description TEXT NOT NULL DEFAULT '',
  is_default INTEGER NOT NULL DEFAULT 0 CHECK (is_default IN (0,1)),
  sort_order INTEGER NOT NULL, deleted_at INTEGER NULL,
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
) STRICT;
CREATE UNIQUE INDEX ux_raid_group_name ON raid_group(name_norm) WHERE deleted_at IS NULL;
```

`raid_group.id` is a legal `role_assignment.scope_id`, which is how "raid leader, but only for
Tuesday group" is expressed without a hardcoded `*_grpleader` permission.

---

## 7. Pools, item pools, event types

```sql
CREATE TABLE pool (
  id             TEXT NOT NULL PRIMARY KEY,
  name           TEXT NOT NULL, name_norm TEXT NOT NULL,
  description    TEXT NOT NULL DEFAULT '',
  currency_label TEXT NOT NULL DEFAULT 'DKP',
  server_id      TEXT NOT NULL REFERENCES server(id),  -- NOT NULL in 1.0; see §2
  strategy_id      TEXT NOT NULL,                  -- 'zero_sum' | 'tick' | 'fixed_price' | 'cap' | …
  strategy_version TEXT NOT NULL,                  -- semver of the in-tree strategy
  strategy_config_json TEXT NOT NULL DEFAULT '{}', -- validated against strategy.ConfigSchema()
  balance_kinds  TEXT NOT NULL DEFAULT 'dkp',      -- space-separated; declared by the strategy
  alt_policy     TEXT NOT NULL DEFAULT 'shared'
                 CHECK (alt_policy IN ('shared','separate','none')),
  allow_negative INTEGER NOT NULL DEFAULT 0 CHECK (allow_negative IN (0,1)),
  min_balance_cp INTEGER NOT NULL DEFAULT 0,
  hold_policy    TEXT NOT NULL DEFAULT 'strict' CHECK (hold_policy IN ('strict','soft','none')),
  attendance_windows TEXT NOT NULL DEFAULT '30 60 90',   -- days; drives attendance_rollup
  dispute_window_hours    INTEGER NOT NULL DEFAULT 72,
  retro_edit_max_age_days INTEGER NULL,            -- NULL = unlimited
  active      INTEGER NOT NULL DEFAULT 1 CHECK (active IN (0,1)),
  archived_at INTEGER NULL,                        -- Green-cycle rollover: freeze, keep readable
  sort_order  INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
) STRICT;
CREATE UNIQUE INDEX ux_pool_name ON pool(name_norm);

CREATE TABLE pool_config_change (  -- config is versioned as events, never overwritten silently
  id TEXT NOT NULL PRIMARY KEY,
  pool_id TEXT NOT NULL REFERENCES pool(id),
  changed_at INTEGER NOT NULL, changed_by TEXT NULL REFERENCES app_user(id),
  from_strategy_id TEXT NOT NULL, from_strategy_version TEXT NOT NULL, from_config_json TEXT NOT NULL,
  to_strategy_id   TEXT NOT NULL, to_strategy_version   TEXT NOT NULL, to_config_json   TEXT NOT NULL,
  reason TEXT NOT NULL DEFAULT '',
  migration_batch_id TEXT NULL REFERENCES ledger_batch(id)
) STRICT;
CREATE INDEX ix_pcc_pool ON pool_config_change(pool_id, changed_at DESC);

CREATE TABLE item_pool (           -- which items are purchasable from which currency
  id TEXT NOT NULL PRIMARY KEY, name TEXT NOT NULL, name_norm TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '', sort_order INTEGER NOT NULL DEFAULT 0,
  deleted_at INTEGER NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
) STRICT;
CREATE UNIQUE INDEX ux_item_pool_name ON item_pool(name_norm) WHERE deleted_at IS NULL;

CREATE TABLE pool_item_pool (
  pool_id      TEXT NOT NULL REFERENCES pool(id) ON DELETE CASCADE,
  item_pool_id TEXT NOT NULL REFERENCES item_pool(id) ON DELETE CASCADE,
  PRIMARY KEY (pool_id, item_pool_id)
) STRICT, WITHOUT ROWID;

CREATE TABLE event_type (          -- the ENCOUNTER catalogue: Vulak`Aerr, Trakanon, "Sky rotation"
  id        TEXT NOT NULL PRIMARY KEY,
  name      TEXT NOT NULL, name_norm TEXT NOT NULL,
  zone_id   TEXT NULL REFERENCES zone(id),
  era       TEXT NOT NULL DEFAULT 'velious',
  server_id TEXT NULL REFERENCES server(id),      -- NULL ⇒ every server
  kind      TEXT NOT NULL DEFAULT 'boss'
            CHECK (kind IN ('boss','zone','rotation','bonus','tracking','other')),
  default_kill_value_cp INTEGER NOT NULL DEFAULT 0,
  default_tick_value_cp INTEGER NOT NULL DEFAULT 0,
  default_item_pool_id  TEXT NULL REFERENCES item_pool(id),
  slain_pattern      TEXT NOT NULL DEFAULT '',    -- exact mob name for `has been slain by` credit
  slain_pattern_norm TEXT NOT NULL DEFAULT '',
  slain_priority INTEGER NOT NULL DEFAULT 0,      -- see the tiebreak note below
  retired_at INTEGER NULL,                        -- Kerafyrm awakened ⇒ the Warders can never fire again
  icon_media_id TEXT NULL REFERENCES media(id),
  show_on_profile INTEGER NOT NULL DEFAULT 1 CHECK (show_on_profile IN (0,1)),
  deleted_at INTEGER NULL,
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
) STRICT;
CREATE UNIQUE INDEX ux_event_type_name ON event_type(name_norm) WHERE deleted_at IS NULL;
CREATE INDEX ix_event_type_slain ON event_type(slain_pattern_norm, slain_priority DESC)
  WHERE slain_pattern_norm <> '' AND deleted_at IS NULL AND retired_at IS NULL;

CREATE TABLE pool_event_type (
  pool_id       TEXT NOT NULL REFERENCES pool(id) ON DELETE CASCADE,
  event_type_id TEXT NOT NULL REFERENCES event_type(id) ON DELETE CASCADE,
  no_attendance INTEGER NOT NULL DEFAULT 0 CHECK (no_attendance IN (0,1)),  -- EQdkp parity
  value_override_cp INTEGER NULL,
  PRIMARY KEY (pool_id, event_type_id)
) STRICT, WITHOUT ROWID;
```

**`slain_pattern_norm` is deliberately non-unique** — the same mob legitimately appears standalone
and inside a rotation event. The critique flagged that the parser's tiebreak was unspecified. It is:
**highest `slain_priority`, then the event already credited in this raid, then the most specific
`zone_id` match, then fail into `reconciliation_item(kind='unknown_event')`.** The parser never
guesses silently; a tie that survives all three steps becomes a queue item.

`retired_at` looks small and is not: an event that exists historically but can never fire again must
stay selectable in reports and unselectable in raid entry.

> ✗ **Not copied:** `adjustments.event_id VARCHAR(255)` holding an integer (sometimes a list) to
> imply a pool. Ours is `ledger_batch.pool_id`, a real FK — type-correct, indexable, joinable.
> ✗ **Not copied:** APA rules living in `data/<md5>/eqdkp/apa/apatab.php`, a PHP-serialised file on
> disk **outside the database**, so a DB-only backup silently loses every decay, cap and
> start-points rule. Ours are `pool.strategy_config_json` plus `decay_run` rows: in the DB, in the
> backup, in the audit log, previewable, idempotent per period.

`cap` and `start_points` ship as 1.0 strategies. Without them the importer's own remediation
message ("re-enter your decay rules") points at a UI that cannot express what the guild had.

---

## 8. Raids, ticks, attendance

The second-largest deliberate divergence. EQdkp: one `__raids` row = one `event_id` = one flat
`(raid_id, member_id)` attendee list, with a JSON `raid_connected_attendance` array bolted on to
make N rows count once in the denominator. That cannot express a P99 raid night — a five-hour
session at a race line, spanning three zones, killing zero to five targets, with 15-minute
attendance ticks, standby players and people zoning in late.

```
raid (session) ─┬─* raid_pool          (which pools this session feeds)
                ├─* raid_tick          (attendance observation — pool-agnostic)
                │     └─* raid_tick_credit  (per-pool value → one ledger batch each)
                │     └─* raid_attendance   (who was there, at what weight)
                ├─* raid_event_credit  (0..N targets killed/credited)
                ├─* item_instance      (loot that dropped)
                └─* raid_artifact      (the dumps and log slices that produced it)
```

### 8.1 Resolving the tick/pool defect

The critique found a contract-level defect: `ux_tick_seq(raid_id, pool_id, seq)` says a tick belongs
to exactly one pool, but `POST /raids/{id}/ticks` carries `value_centipoints` and `event_type_id`
and **no `pool_id`** — so the design's own headline example (a raid feeding `Velious Main` *and*
`Cross-class Rares`) is inexpressible.

**Decision: the tick is pool-agnostic and fans out at plan time.** `raid_tick` records *who was
there*; `raid_tick_credit` records *what that was worth, in which pool*.

Why not the other option (add `pool_id` to the tick payload):

| | `pool_id` on the tick | Pool-agnostic tick + fan-out |
|---|---|---|
| Two pools, one roster | the client uploads the roster **twice** | one roster, two credit rows |
| `content_sha256` dedupe | the same dump produces two ticks with the same hash — the natural idempotency key breaks | one hash, one tick, dedupe intact |
| `raid_attendance` rows | duplicated per pool; a 50-person tick becomes 100 rows | 50 rows, once |
| "Was Bob at tick 6?" | pool-dependent, which is nonsense | one answer |
| Reversibility | per tick | per `(tick, pool)` — strictly finer |
| Attendance denominator | must exclude pools the raid did not feed, by convention | falls out of the join |

Attendance is a fact about the raid night; value is a fact about the pool. Splitting them is the
only shape where both stay true.

**API contract this fixes.** The tick payload's `value_centipoints` is shorthand for "this value in
every pool this raid feeds". The explicit form is
`"pools": [{"pool_id": "…", "value_centipoints": …}]`, and when neither is given the value comes
from `pool_event_type.value_override_cp`, falling back to `event_type.default_tick_value_cp`. An
architectural test asserts every `raid_tick` insert path writes at least one `raid_tick_credit`.

### 8.2 DDL

```sql
CREATE TABLE raid (
  id         TEXT NOT NULL PRIMARY KEY,
  server_id  TEXT NOT NULL REFERENCES server(id),
  name       TEXT NOT NULL DEFAULT '',            -- optional; derived from credits when blank
  started_at INTEGER NOT NULL,
  ended_at   INTEGER NULL,
  raid_day   TEXT    NOT NULL,                    -- 'YYYY-MM-DD' in guild.timezone, computed in Go
  note       TEXT NOT NULL DEFAULT '',
  state      TEXT NOT NULL DEFAULT 'open'
             CHECK (state IN ('draft','open','finalized','voided')),
  finalized_at INTEGER NULL, finalized_by TEXT NULL REFERENCES app_user(id),
  dispute_open_until_at INTEGER NULL,
  created_by  TEXT NULL REFERENCES app_user(id),
  created_via TEXT NOT NULL DEFAULT 'web'
              CHECK (created_via IN ('web','api','parser','import','calendar')),
  attendance_group_id TEXT NULL,                  -- N raids sharing a group count ONCE in the
                                                  -- denominator; ✗ replaces the JSON array
  version    INTEGER NOT NULL DEFAULT 1,          -- optimistic concurrency; exposed as ETag
  deleted_at INTEGER NULL,
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
) STRICT;
CREATE INDEX ix_raid_started ON raid(started_at DESC) WHERE deleted_at IS NULL;
CREATE INDEX ix_raid_day     ON raid(raid_day)        WHERE deleted_at IS NULL;
CREATE INDEX ix_raid_group   ON raid(attendance_group_id) WHERE attendance_group_id IS NOT NULL;

CREATE TABLE raid_pool (           -- the fan-out input, chosen once at raid creation
  raid_id TEXT NOT NULL REFERENCES raid(id) ON DELETE CASCADE,
  pool_id TEXT NOT NULL REFERENCES pool(id),
  PRIMARY KEY (raid_id, pool_id)
) STRICT, WITHOUT ROWID;

CREATE TABLE raid_officer (
  raid_id TEXT NOT NULL REFERENCES raid(id) ON DELETE CASCADE,
  user_id TEXT NOT NULL REFERENCES app_user(id),
  role    TEXT NOT NULL DEFAULT 'leader' CHECK (role IN ('leader','loot','tracker','scribe')),
  PRIMARY KEY (raid_id, user_id, role)
) STRICT, WITHOUT ROWID;

CREATE TABLE raid_artifact (       -- N dumps and log slices per raid
  raid_id     TEXT NOT NULL REFERENCES raid(id) ON DELETE CASCADE,
  artifact_id TEXT NOT NULL REFERENCES artifact(id),
  PRIMARY KEY (raid_id, artifact_id)
) STRICT, WITHOUT ROWID;

CREATE TABLE raid_event_credit (   -- 0..N targets per session. NOT raids.event_id.
  id TEXT NOT NULL PRIMARY KEY,
  raid_id       TEXT NOT NULL REFERENCES raid(id) ON DELETE CASCADE,
  event_type_id TEXT NOT NULL REFERENCES event_type(id),
  occurred_at   INTEGER NOT NULL,
  tick_id  TEXT NULL REFERENCES raid_tick(id),    -- the kill tick, if the kill awarded points
  source   TEXT NOT NULL DEFAULT 'manual'
           CHECK (source IN ('manual','parser_slain','parser_fte','api','import')),
  artifact_id TEXT NULL REFERENCES artifact(id),
  note TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL
) STRICT;
CREATE UNIQUE INDEX ux_rec_dedupe ON raid_event_credit(raid_id, event_type_id, occurred_at);
CREATE INDEX ix_rec_event ON raid_event_credit(event_type_id, occurred_at DESC);

CREATE TABLE raid_tick (           -- an ATTENDANCE OBSERVATION. Pool-agnostic. Carries no value.
  id       TEXT NOT NULL PRIMARY KEY,
  raid_id  TEXT NOT NULL REFERENCES raid(id),
  seq      INTEGER NOT NULL,                      -- 1-based within the raid
  at       INTEGER NOT NULL,                      -- becomes effective_at on every derived batch
  kind     TEXT NOT NULL DEFAULT 'time'
           CHECK (kind IN ('time','kill','start','end','bonus','tracking','manual')),
  event_type_id TEXT NULL REFERENCES event_type(id),
  artifact_id   TEXT NULL REFERENCES artifact(id),
  content_sha256 BLOB NULL,                       -- hash of the attendee set: natural idempotency
  note     TEXT NOT NULL DEFAULT '',
  created_by  TEXT NULL REFERENCES app_user(id),
  created_via TEXT NOT NULL DEFAULT 'web',
  voided_at   INTEGER NULL, voided_by TEXT NULL REFERENCES app_user(id),
  void_reason TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
) STRICT;
CREATE UNIQUE INDEX ux_tick_seq  ON raid_tick(raid_id, seq);
CREATE UNIQUE INDEX ux_tick_hash ON raid_tick(raid_id, content_sha256)
  WHERE content_sha256 IS NOT NULL AND voided_at IS NULL;   -- two officers, one dump ⇒ one tick
CREATE INDEX ix_tick_at ON raid_tick(at);

CREATE TABLE raid_tick_credit (    -- the fan-out. One row per (tick, pool). THIS carries the money.
  id       TEXT NOT NULL PRIMARY KEY,
  tick_id  TEXT NOT NULL REFERENCES raid_tick(id) ON DELETE CASCADE,
  pool_id  TEXT NOT NULL REFERENCES pool(id),
  value_cp INTEGER NOT NULL CHECK (value_cp >= 0),
  ledger_batch_id TEXT NULL REFERENCES ledger_batch(id),    -- set at commit
  voided_at     INTEGER NULL,
  void_batch_id TEXT NULL REFERENCES ledger_batch(id),      -- the reversal
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
) STRICT;
CREATE UNIQUE INDEX ux_tick_credit  ON raid_tick_credit(tick_id, pool_id);
CREATE INDEX ix_tick_credit_pool ON raid_tick_credit(pool_id, tick_id) WHERE voided_at IS NULL;

CREATE TABLE raid_attendance (
  id       TEXT NOT NULL PRIMARY KEY,
  tick_id  TEXT NOT NULL REFERENCES raid_tick(id) ON DELETE CASCADE,
  character_id TEXT NOT NULL REFERENCES character(id),
  status   TEXT NOT NULL DEFAULT 'present'
           CHECK (status IN ('present','standby','bench','pilot','excused','late','left_early')),
  weight_bp INTEGER NOT NULL DEFAULT 10000 CHECK (weight_bp BETWEEN 0 AND 10000),
  group_no INTEGER NULL,                          -- RaidRoster group; 0 = ungrouped/bench
  level    INTEGER NULL,                          -- as observed in the dump
  class_id TEXT NULL REFERENCES game_class(id),
  piloted_by_person_id TEXT NULL REFERENCES person(id),
  joined_at INTEGER NULL, left_at INTEGER NULL,   -- data EQdkp's own XSD carries then discards
  source   TEXT NOT NULL DEFAULT 'raid_dump'
           CHECK (source IN ('raid_dump','who','manual','api','import','guild_dump')),
  raw_extra TEXT NOT NULL DEFAULT '',             -- trailing RaidRoster columns we do not yet parse
  created_at INTEGER NOT NULL
) STRICT;
CREATE UNIQUE INDEX ux_attendance    ON raid_attendance(tick_id, character_id);
CREATE INDEX        ix_attendance_char ON raid_attendance(character_id, tick_id);
```

Notes that matter:

- **`weight_bp` and `status` are the point.** EQdkp's `__raid_attendees` is `(raid_id, member_id)`
  with no primary key, no unique constraint, no weight and no timestamps, so partial credit, standby
  rates and bench are all faked with adjustments. Here the planner reads `weight_bp` directly and the
  invariant engine checks the result.
- **`raid_attendance` does not denormalise `account_id`.** Re-parenting must not rewrite history.
  The planner resolves character → person → account **at plan time** and snapshots both `account_id`
  and `character_id` onto the `ledger_entry`. The published consequence is in §6.6.
- **A tick is never hard-deleted.** `voided_at` plus a mandatory reversal batch per credit, in the
  same transaction. An integration test asserts that voiding a credit without writing
  `void_batch_id` raises.
- **`raw_extra`** exists because whether Titanium's `RaidRoster-*.txt` emits trailing columns (raid
  leader, group leader, main assist, loot rank) is **unverified**. Parse fields 0–3, keep the rest
  verbatim rather than guessing a format.

> ✗ **Not copied:** `raids.event_id` mandatory (one raid = one event = one flat attendee list);
> `raid_connected_attendance` as a JSON array of raid ids — denominator dedupe must be an indexed
> join key, not a blob parsed per row.

---

## 9. The ledger

The only source of truth for points. Every other points-bearing table is either a **fact** (what
happened) or a **cache** (droppable). A balance is a `SUM`.

### 9.1 DDL

```sql
CREATE TABLE ledger_batch (
  id       TEXT NOT NULL PRIMARY KEY,             -- ULID
  pool_id  TEXT NOT NULL REFERENCES pool(id),
  seq      INTEGER NOT NULL,                      -- PER-POOL monotonic; THE ordering authority
  -- The live list is GENERATED from internal/ledger/kinds into db/schema.hcl (canonical §5);
  -- this sketch is illustrative and db/schema.hcl is the schema truth.
  kind     TEXT NOT NULL CHECK (kind IN (
             'attendance','award','adjustment','decay','cap','start_points',
             'zero_sum_credit','reversal','correction','re_attribution',
             'migration','import','seed','write_off')),
  strategy_id      TEXT NOT NULL,
  strategy_version TEXT NOT NULL,
  config_snapshot_json TEXT NOT NULL DEFAULT '{}',-- the EXACT rules in force when planned
  rng_seed  INTEGER NULL,                         -- persisted ⇒ replays are byte-identical
  source    TEXT NOT NULL CHECK (source IN ('web','api','discord','parser','import','system')),
  source_ref TEXT NULL,                           -- 'tick_credit:<ulid>' | 'bid_session:<ulid>' |
                                                  -- 'item_award:<ulid>' | 'decay_run:<ulid>' |
                                                  -- 'person_merge:<ulid>' | 'raid_submission:<ulid>'
  actor_user_id  TEXT NULL REFERENCES app_user(id),
  actor_token_id TEXT NULL REFERENCES api_token(id),
  actor_is_beneficiary INTEGER NOT NULL DEFAULT 0 CHECK (actor_is_beneficiary IN (0,1)),
                                                  -- computed by the writer; drives the UI badge,
                                                  -- the Discord wording and the self-dealing report
  reason    TEXT NOT NULL DEFAULT '',
  reverses_batch_id TEXT NULL REFERENCES ledger_batch(id),
  effective_at  INTEGER NOT NULL,                 -- GAME truth; may be backdated
  recorded_at   INTEGER NOT NULL,                 -- SYSTEM truth; never backdated
  effective_day TEXT    NOT NULL,                 -- 'YYYY-MM-DD' guild-local, computed in Go
  idempotency_key TEXT NULL,
  entry_count   INTEGER NOT NULL CHECK (entry_count > 0),
  net_amount_cp INTEGER NOT NULL,                 -- Σ entries; 0 for zero-sum. Column-comparison
                                                  -- invariant instead of an aggregate.
  prev_hash BLOB NULL,                            -- NULL only at seq = 1 for this pool
  hash      BLOB NOT NULL,
  CHECK (recorded_at >= 0 AND effective_at >= 0)
) STRICT;

CREATE UNIQUE INDEX ux_batch_seq      ON ledger_batch(pool_id, seq);
CREATE UNIQUE INDEX ux_batch_srcref   ON ledger_batch(pool_id, source_ref)      WHERE source_ref IS NOT NULL;
CREATE UNIQUE INDEX ux_batch_idem     ON ledger_batch(pool_id, idempotency_key) WHERE idempotency_key IS NOT NULL;
CREATE UNIQUE INDEX ux_batch_reverses ON ledger_batch(reverses_batch_id)        WHERE reverses_batch_id IS NOT NULL;
CREATE INDEX ix_batch_effective ON ledger_batch(pool_id, effective_at);
CREATE INDEX ix_batch_kind      ON ledger_batch(pool_id, kind, seq);
CREATE INDEX ix_batch_selfdeal  ON ledger_batch(actor_is_beneficiary, recorded_at DESC)
  WHERE actor_is_beneficiary = 1;

CREATE TABLE ledger_entry (
  id       TEXT NOT NULL PRIMARY KEY,
  batch_id TEXT NOT NULL REFERENCES ledger_batch(id),
  pool_id  TEXT NOT NULL REFERENCES pool(id),     -- denormalised: every read filters on it
  seq      INTEGER NOT NULL,                      -- denormalised batch.seq: the balance index
  account_id   TEXT NOT NULL REFERENCES account(id),
  character_id TEXT NULL REFERENCES character(id),-- attribution ONLY; never affects a balance
  balance_kind TEXT NOT NULL,                     -- 'dkp' | 'ep' | 'gp' | …
  amount_cp    INTEGER NOT NULL CHECK (amount_cp <> 0),
  item_id       TEXT NULL REFERENCES item(id),
  item_award_id TEXT NULL REFERENCES item_award(id),
  raid_id       TEXT NULL REFERENCES raid(id),
  tick_id       TEXT NULL REFERENCES raid_tick(id),
  metadata_json TEXT NOT NULL DEFAULT '{}'
) STRICT;

-- THE balance index. Covering: SUM(amount_cp) is answered from the index alone.
CREATE INDEX ix_entry_balance ON ledger_entry(pool_id, account_id, balance_kind, seq, amount_cp);
CREATE INDEX ix_entry_batch   ON ledger_entry(batch_id);
CREATE INDEX ix_entry_stmt    ON ledger_entry(account_id, pool_id, seq DESC);  -- the statement view
CREATE INDEX ix_entry_item    ON ledger_entry(item_id) WHERE item_id IS NOT NULL;
```

`actor_is_beneficiary` and the `prev_hash`/`hash` pair were omitted from the earlier domain model
while the security design depended on all three. They are here because the daily anchoring control
(§9.6), the self-dealing badge and the optional two-person report are unimplementable without them.

### 9.2 Append-only, enforced by the database

```sql
CREATE TRIGGER trg_ledger_batch_no_update BEFORE UPDATE ON ledger_batch
  BEGIN SELECT RAISE(ABORT, 'ledger_batch is append-only'); END;
CREATE TRIGGER trg_ledger_batch_no_delete BEFORE DELETE ON ledger_batch
  BEGIN SELECT RAISE(ABORT, 'ledger_batch is append-only'); END;
CREATE TRIGGER trg_ledger_entry_no_update BEFORE UPDATE ON ledger_entry
  BEGIN SELECT RAISE(ABORT, 'ledger_entry is append-only'); END;
CREATE TRIGGER trg_ledger_entry_no_delete BEFORE DELETE ON ledger_entry
  BEGIN SELECT RAISE(ABORT, 'ledger_entry is append-only'); END;
```

**Enforced by:** the triggers, **plus** `TestLedger_UpdateEntry_RaisesAbort` and
`TestLedger_DeleteBatch_RaisesAbort`, so the guardrail itself cannot be silently regressed. Atlas
must preserve hand-written triggers across `migrate diff`; verifying that is a Phase 0 task, and if
it does not, the trigger DDL moves to a hand-authored migration that Atlas is told to leave alone.

Two consequences the rest of the design respects:

- **"Is this batch reversed?" is a query, not a column.** `ux_batch_reverses` makes
  `EXISTS (SELECT 1 FROM ledger_batch WHERE reverses_batch_id = ?)` an index-only lookup *and*
  enforces that a batch is reversed at most once.
- **Forward pointers live on the mutable fact tables** (`raid_tick_credit.ledger_batch_id`,
  `item_award.void_batch_id`). Facts point at money; money never points at mutable state.

### 9.3 `seq` allocation

SQLite has no sequences. Inside the write transaction — `BEGIN IMMEDIATE` on a
`SetMaxOpenConns(1)` pool, so it is the only writer:

```sql
INSERT INTO ledger_batch (..., seq, ...)
VALUES (..., (SELECT COALESCE(MAX(seq),0) + 1 FROM ledger_batch WHERE pool_id = ?1), ...);
```

`ux_batch_seq` is the guardrail if the single-writer property is ever lost. On Postgres this becomes
`nextval()` or a `SELECT … FOR UPDATE` counter row. This is one of **three** genuine dialect
divergences (the others: the bid-hold lock in §11.4 and full-text search in §20) and it is recorded
in `db/RECIPES.md`.

### 9.4 How a balance is computed

```sql
-- name: BalanceAsOfSeq :one
SELECT COALESCE(SUM(amount_cp), 0) AS balance_cp   -- sum(), NEVER total()  [C3]
  FROM ledger_entry
 WHERE pool_id = ?1 AND account_id = ?2 AND balance_kind = ?3 AND seq <= ?4;
```

That is the entire definition. Corollaries:

| Question | Query |
|---|---|
| Current balance | the same, with `?4 = (SELECT MAX(seq) FROM ledger_batch WHERE pool_id = ?1)` |
| "As of a date" | derive the seq first: `SELECT COALESCE(MAX(seq),0) FROM ledger_batch WHERE pool_id = ?1 AND effective_at <= ?2` |
| "What did the site say last Tuesday?" | the same derivation on `recorded_at` instead — the bitemporal payoff, and the query an officer actually needs during a dispute |

**A balance is defined as of a `seq`, never as of a timestamp** (conventions §4): timestamps tie,
sequences do not, and a dispute needs one number. `ix_entry_balance` is covering, so this is an
index-range `SUM` with no table access.

### 9.5 `balance_snapshot` — when it is materialised

```sql
CREATE TABLE balance_snapshot (    -- a CACHE. Droppable. Rebuildable. Verified nightly.
  pool_id      TEXT NOT NULL REFERENCES pool(id),
  account_id   TEXT NOT NULL REFERENCES account(id),
  balance_kind TEXT NOT NULL,
  amount_cp    INTEGER NOT NULL,
  as_of_seq    INTEGER NOT NULL,
  entry_count  INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL,
  PRIMARY KEY (pool_id, account_id, balance_kind)
) STRICT, WITHOUT ROWID;
CREATE INDEX ix_snapshot_standings ON balance_snapshot(pool_id, balance_kind, amount_cp DESC);
```

**Materialised synchronously, in the same transaction as the batch.** A batch has at most ~70
entries (one per raider); the upsert is ≤70 indexed writes on a `WITHOUT ROWID` table, sub-
millisecond, under a serialised single writer with no concurrency hazard.

```sql
INSERT INTO balance_snapshot (pool_id, account_id, balance_kind, amount_cp, as_of_seq, entry_count, updated_at)
SELECT pool_id, account_id, balance_kind, SUM(amount_cp), ?seq, COUNT(*), ?now
  FROM ledger_entry WHERE batch_id = ?batch
 GROUP BY pool_id, account_id, balance_kind
ON CONFLICT (pool_id, account_id, balance_kind) DO UPDATE SET
  amount_cp   = amount_cp   + excluded.amount_cp,
  entry_count = entry_count + excluded.entry_count,
  as_of_seq   = excluded.as_of_seq,
  updated_at  = excluded.updated_at;
```

In exchange, `/standings` for 200 members is **one indexed scan** joined to the roster: no
watermark, no delta query, no async lag, and no "the page was stale for 30 seconds after the raid"
bug report.

**It is still a cache and is treated as one.** `dkp verify-ledger` — a nightly River job, plus CI
against a synthetic 10⁵-entry ledger — recomputes every row from `seq = 0` and asserts equality;
drift raises a visible admin alert, and `dkp verify-ledger --rebuild` truncates and recomputes.

The distinguishing property versus EQdkp is **not** "we have no cache". It is that the cache is
derived from an *immutable* log, so rebuilding is total and cheap and drift is *detectable*.
EQdkp's `__members.points` is derived from mutable rows, so drift is undetectable and "recalculate"
is a prayer.

> ✗ **Not copied:** `members.points` / `points_apa` as PHP-serialised caches on the member row, with
> a "recalculate" button.
> **Cut:** a `balance_history` daily rollup. It is ~220k rows/year for a chart nobody has requested,
> and `ix_entry_balance` produces the same series on demand. Imported `__member_points` snapshots
> therefore feed `import_reconciliation` as an oracle only (§16.3).

### 9.6 Hash chain and anchors

```
hash = SHA-256( prev_hash ‖ canonical_json(batch without `hash`) ‖ canonical_json(entries ORDER BY id) )
```

The chain is **per pool**, because `seq` is per pool. `audit_log` (§17) maintains an independent,
instance-wide chain.

```sql
CREATE TABLE dkp_meta (            -- chain heads and other single-row instance state
  key   TEXT NOT NULL PRIMARY KEY, -- 'ledger_head:<pool_id>' | 'audit_head' | 'schema_epoch'
  value TEXT NOT NULL,
  updated_at INTEGER NOT NULL
) STRICT, WITHOUT ROWID;

CREATE TABLE chain_anchor (        -- the published, off-box-mirrored checkpoint
  id        TEXT NOT NULL PRIMARY KEY,
  chain     TEXT NOT NULL CHECK (chain IN ('ledger','audit')),
  scope_id  TEXT NULL REFERENCES pool(id),        -- the pool, for chain = 'ledger'
  seq_from  INTEGER NOT NULL, seq_to INTEGER NOT NULL,
  row_count INTEGER NOT NULL,
  head_hash BLOB NOT NULL,
  published_to TEXT NOT NULL DEFAULT '',          -- space-separated: 'file' 'discord' 'email'
  at INTEGER NOT NULL
) STRICT;
CREATE INDEX ix_anchor_chain ON chain_anchor(chain, scope_id, seq_to DESC);
```

**The honest limitation, which the docs must state and must not overclaim.** An actor with
filesystem access can rewrite rows *and* recompute the chain. A local-only hash chain proves nothing
against a local adversary. The control is *publication*: a daily job writes an anchor to
`/data/audit-anchors.log`, posts it to the guild's Discord webhook, and optionally emails the
officer list. `dkp verify-ledger` recomputes both chains and reports the exact `seq` at which they
diverge from the last N published anchors.

**Enforced by:** `dkp doctor --check anchors` **fails** when `chain_anchor.published_to` has
contained only `file` for the last 7 days — i.e. when neither Discord nor SMTP is configured, so the
only anti-tamper control does not actually exist. The docs say "tamper-evident *when anchors are
published off-box*", never unconditionally.

### 9.7 Entry types, reversal, corrections

- **`kind` is on the batch, not the entry.** An award batch contains a debit *and*, under zero-sum,
  N credits; they are one economic event and must reverse together.
- **`net_amount_cp` is precomputed** so `SumZero` is a column comparison (`= 0`) rather than an
  aggregate, and the nightly verifier checks conservation per pool in one pass.
- **Reversal:** a new batch with `kind = 'reversal'`, `reverses_batch_id` set, entries negated (or
  the strategy's inverse — `PlanReversal` is on the interface for exactly this). The original stays
  visible, rendered struck through. A reversal of a reversal is just another reversal.
- **Retroactive zero-sum edits compensate, never replay.** One `kind = 'correction'` batch at
  today's `seq`, surfaced in a per-account Corrections tab. Full replay exists only as an explicit
  officer job with a mandatory dry-run diff, and it too emits a single net-delta `correction` batch.
- **Decay is posted, not computed** (conventions §10), with idempotency key `(pool_id,
  cadence_period)` — the `decay_run` table in §12.3.
- **Zero-sum splits use largest-remainder allocation** with a deterministic tiebreak on
  `account_id`; unallocatable residue goes to the `residue` system account so conservation holds
  globally. Rounding each credit independently mints or destroys points on nearly every award.
- **`GET /accounts/{id}/ledger`** is literally `ix_entry_stmt` scanned backwards with a running
  total: date, kind, description, delta, running balance, actor, source, link to the batch. This one
  screen eliminates most guild loot drama.

> ✗ **Not copied:** decay computed at read time by APA transforms over cached totals. A balance is
> always literally a `SUM`, so "why did my points change?" is answerable by pointing at a row.
> ✗ **Not copied:** `items.item_group_key` and `adjustments.adjustment_group_key` magic strings. A
> mass adjustment is **one batch with N entries** — a batch *is* the grouping — and a multi-buyer
> award is N `item_award` rows sharing an `item_instance_id` (§12.2).

**Adjustments need no table.** An adjustment is `ledger_batch(kind='adjustment', reason=…)` with N
entries; `/api/v1/adjustments` projects over `ledger_batch WHERE kind = 'adjustment'`.

---

## 10. Attendance statistics

### 10.1 Why it is materialised

Not write volume. It is that **a rolling window's denominator changes with the passage of time, with
zero writes** — a 30-day percentage is stale tomorrow even if nothing happened. There is no
cache-invalidation strategy that works; there is only a recompute *schedule*. Once you accept a
schedule you must put an explicit `as_of_day` on the stored value, and once the value is explicitly
dated, materialising is strictly better than deriving, because the number in the UI is the number
the tie-break used.

EQdkp reaches the same conclusion by caching "until midnight" — right instinct, invisible mechanism,
and no way to answer "what percentage did the tie-break use?".

### 10.2 The derived query — the ground truth the rollup must match

```sql
-- name: AttendancePctWindow :many
-- Numerator:   distinct qualifying ticks the PERSON attended, through any of their characters.
-- Denominator: qualifying ticks HELD in the pool in the window, with connected-raid dedupe
--              and `no_attendance` event exclusion.
WITH held AS (
  SELECT t.id AS tick_id,
         COALESCE(r.attendance_group_id, r.id) AS dedupe_key,
         r.raid_day
    FROM raid_tick_credit tc
    JOIN raid_tick t ON t.id = tc.tick_id
    JOIN raid      r ON r.id = t.raid_id
    LEFT JOIN pool_event_type pet
           ON pet.pool_id = tc.pool_id AND pet.event_type_id = t.event_type_id
   WHERE tc.pool_id    = @pool_id
     AND tc.voided_at IS NULL
     AND t.voided_at  IS NULL
     AND r.deleted_at IS NULL
     AND r.state       IN ('open','finalized')
     AND t.at         >= @window_start_at
     AND COALESCE(pet.no_attendance, 0) = 0
),
attended AS (
  SELECT DISTINCT c.person_id, h.tick_id, h.dedupe_key
    FROM raid_attendance a
    JOIN held      h ON h.tick_id = a.tick_id
    JOIN character c ON c.id      = a.character_id
   WHERE a.status IN ('present','pilot')          -- standby/bench are counted separately
)
SELECT p.id AS person_id,
       COUNT(DISTINCT at.tick_id)                        AS ticks_attended,
       (SELECT COUNT(*)                   FROM held)     AS ticks_held,
       COUNT(DISTINCT at.dedupe_key)                     AS raids_attended,
       (SELECT COUNT(DISTINCT dedupe_key) FROM held)     AS raids_held
  FROM person p
  LEFT JOIN attended at ON at.person_id = p.id
 WHERE p.deleted_at IS NULL AND p.merged_into_person_id IS NULL
 GROUP BY p.id;
```

`pct_bp = ticks_attended * 10000 / NULLIF(ticks_held, 0)`. The `_real` variant clamps the
denominator to ticks held **since `person.joined_at`**, so a three-day-old recruit is not 2%.

Note that "held" is now defined through `raid_tick_credit`: a tick that produced no credit in pool P
was genuinely not held in P. That falls out of §8.1 rather than being a convention to remember.

### 10.3 The rollup

```sql
CREATE TABLE attendance_rollup (
  pool_id    TEXT NOT NULL REFERENCES pool(id),
  person_id  TEXT NOT NULL REFERENCES person(id),
  window_key TEXT NOT NULL,                       -- 'd30' | 'd60' | 'd90' | 'lifetime' | 'season:<ulid>'
  as_of_day  TEXT NOT NULL,                       -- guild-local; staleness is VISIBLE, not hidden
  as_of_seq  INTEGER NOT NULL,
  ticks_attended INTEGER NOT NULL, ticks_held INTEGER NOT NULL,
  raids_attended INTEGER NOT NULL, raids_held INTEGER NOT NULL,
  days_attended  INTEGER NOT NULL, days_held  INTEGER NOT NULL,
  standby_ticks  INTEGER NOT NULL DEFAULT 0,
  pct_bp      INTEGER NOT NULL,
  pct_real_bp INTEGER NOT NULL,                   -- denominator clamped to the person's tenure
  computed_at INTEGER NOT NULL,
  PRIMARY KEY (pool_id, person_id, window_key)
) STRICT, WITHOUT ROWID;
CREATE INDEX ix_att_rank ON attendance_rollup(pool_id, window_key, pct_bp DESC);
```

Recomputed by a River periodic job at guild-local midnight **and** synchronously for the affected
pool on `raid.finalize` and on tick/credit void, so the post-raid page is correct immediately. 200
persons × 3 pools × 4 windows = 2,400 rows in a single pass.

Ad-hoc `?from=&to=` ranges bypass the rollup and run §10.2 directly. That path is rare, officer
initiated, and has its own statement budget.

**`/standings` therefore costs four statements:** `balance_snapshot` scan, `attendance_rollup` scan,
roster join, pagination count. *(Empirical assumption: that this stays inside 150 ms p99 on
SD-card storage at 520k entries. Hand-write the four queries against a generated ledger before
building the API — this decides whether the snapshot + rollup design survives.)*

---

## 11. Bid sessions

Server-authoritative bidding ships in 1.0. Enum values are exactly the canonical ones
([conventions §5](00-canonical-conventions.md)) — the wire value **is** the database value.

```sql
CREATE TABLE bid_session (
  id      TEXT NOT NULL PRIMARY KEY,
  pool_id TEXT NOT NULL REFERENCES pool(id),
  raid_id TEXT NOT NULL REFERENCES raid(id),
  item_instance_id TEXT NOT NULL REFERENCES item_instance(id),
  mode    TEXT NOT NULL CHECK (mode IN
          ('auction_open','auction_sealed_first','auction_sealed_second')),
  state   TEXT NOT NULL DEFAULT 'draft' CHECK (state IN
          ('draft','open','extended','closing','resolved','settled',
           'reversed','rot','resolution_failed')),
  strategy_id TEXT NOT NULL, strategy_version TEXT NOT NULL,
  config_snapshot_json TEXT NOT NULL DEFAULT '{}',
  min_bid_cp   INTEGER NOT NULL DEFAULT 0,
  increment_cp INTEGER NOT NULL DEFAULT 100,
  cap_policy   TEXT NOT NULL DEFAULT 'balance'
               CHECK (cap_policy IN ('balance','balance_plus_credit','unlimited')),
  hold_policy  TEXT NOT NULL DEFAULT 'strict' CHECK (hold_policy IN ('strict','soft','none')),
  tie_break_json TEXT NOT NULL DEFAULT '[]',      -- ordered chain, snapshotted at open
  opens_at  INTEGER NULL,
  closes_at INTEGER NULL,                         -- SERVER authority; echoed in every response
  anti_snipe_window_secs INTEGER NOT NULL DEFAULT 0,
  anti_snipe_extend_secs INTEGER NOT NULL DEFAULT 0,
  max_extensions  INTEGER NOT NULL DEFAULT 0,
  extensions_used INTEGER NOT NULL DEFAULT 0,
  seq_at_open INTEGER NULL,                       -- frozen balance position for sealed modes
  balance_snapshot_policy TEXT NOT NULL DEFAULT 'at_settle'
               CHECK (balance_snapshot_policy IN ('at_open','at_settle')),
  rot_policy  TEXT NOT NULL DEFAULT 'guild_bank'
               CHECK (rot_policy IN ('free_roll','guild_bank','reopen_lower','mark_rotted')),
  revealed_at INTEGER NULL,                       -- sealed modes: set on entering `closing`
  version     INTEGER NOT NULL DEFAULT 1,         -- optimistic concurrency; exposed as ETag
  created_by  TEXT NULL REFERENCES app_user(id),
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
) STRICT;

-- LIVE UNIQUENESS. Two bots racing to open produce ONE session.
CREATE UNIQUE INDEX ux_bid_live ON bid_session(item_instance_id)
  WHERE state IN ('draft','open','extended','closing','resolved');
CREATE INDEX ix_bid_open ON bid_session(state, closes_at) WHERE state IN ('open','extended');

CREATE TABLE bid (
  id         TEXT NOT NULL PRIMARY KEY,
  session_id TEXT NOT NULL REFERENCES bid_session(id),
  account_id TEXT NOT NULL REFERENCES account(id),
  character_id TEXT NULL REFERENCES character(id),
  amount_cp     INTEGER NOT NULL,
  max_amount_cp INTEGER NULL,                     -- proxy bid; PRIVATE, and the hold is taken on THIS
  seq        INTEGER NOT NULL,                    -- server-assigned per session: ordering authority
  is_retracted INTEGER NOT NULL DEFAULT 0 CHECK (is_retracted IN (0,1)),
  retracted_by_bid_id TEXT NULL REFERENCES bid(id),
  source     TEXT NOT NULL CHECK (source IN ('web','api','discord','ingame_tell','guild_chat')),
  actor_user_id  TEXT NULL REFERENCES app_user(id),
  actor_token_id TEXT NULL REFERENCES api_token(id),
  idempotency_key TEXT NULL,
  created_at INTEGER NOT NULL
) STRICT;
CREATE UNIQUE INDEX ux_bid_seq  ON bid(session_id, seq);
CREATE UNIQUE INDEX ux_bid_idem ON bid(session_id, idempotency_key) WHERE idempotency_key IS NOT NULL;
CREATE INDEX ix_bid_session ON bid(session_id, amount_cp DESC, seq ASC);   -- the leaderboard query

CREATE TRIGGER trg_bid_no_update BEFORE UPDATE OF amount_cp, account_id, seq ON bid
  BEGIN SELECT RAISE(ABORT, 'bids are append-only; retract by inserting a retraction'); END;

CREATE TABLE bid_hold (
  id         TEXT NOT NULL PRIMARY KEY,
  session_id TEXT NOT NULL REFERENCES bid_session(id),
  pool_id    TEXT NOT NULL REFERENCES pool(id),
  account_id TEXT NOT NULL REFERENCES account(id),
  balance_kind TEXT NOT NULL DEFAULT 'dkp',
  amount_cp  INTEGER NOT NULL CHECK (amount_cp > 0),
  state      TEXT NOT NULL DEFAULT 'active'
             CHECK (state IN ('active','released','converted')),
  bid_id     TEXT NULL REFERENCES bid(id),
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
) STRICT;
CREATE UNIQUE INDEX ux_hold_active   ON bid_hold(session_id, account_id, balance_kind)
  WHERE state = 'active';
CREATE INDEX        ix_hold_account  ON bid_hold(pool_id, account_id, balance_kind)
  WHERE state = 'active';

CREATE TABLE bid_resolution (
  session_id          TEXT NOT NULL PRIMARY KEY REFERENCES bid_session(id),
  winner_account_id   TEXT NULL REFERENCES account(id),
  winner_character_id TEXT NULL REFERENCES character(id),
  winning_bid_id TEXT NULL REFERENCES bid(id),
  price_cp    INTEGER NULL,
  tie_break_reason TEXT NOT NULL DEFAULT '',      -- the chain step that decided it, in plain English
  rng_seed    INTEGER NULL,
  resolved_at INTEGER NOT NULL, resolved_by TEXT NULL REFERENCES app_user(id),
  overridden_by   TEXT NULL REFERENCES app_user(id),
  override_reason TEXT NOT NULL DEFAULT '',
  settled_batch_id TEXT NULL REFERENCES ledger_batch(id),
  item_award_id    TEXT NULL REFERENCES item_award(id),
  failure_code TEXT NOT NULL DEFAULT ''
) STRICT;

CREATE TABLE account_lock (        -- one row per balance. A NO-OP on SQLite; the Postgres lock target.
  account_id   TEXT NOT NULL REFERENCES account(id),
  pool_id      TEXT NOT NULL REFERENCES pool(id),
  balance_kind TEXT NOT NULL,
  PRIMARY KEY (account_id, pool_id, balance_kind)
) STRICT, WITHOUT ROWID;
```

**Cut from the source design:** `relative` and `council` bid modes, and the `bid_council_vote`
table. Five of fourteen strategies served WoW-lineage systems, and the council table was the
lowest-confidence part of the schema. `PlanReversal` for those variants is the hardest single piece
of ledger code in the spec. Ship one if a pilot guild asks.

**Cancellation.** The canonical state list has no `cancelled`. A `draft` session has no money and no
bids and is hard-deleted; an `open` session with no valid bids goes to `rot` under `rot_policy`.

### 11.1 State machine

```
draft ─open→ open ⇄ extended ─close/timer→ closing ─resolve→ resolved ─settle→ settled ─reverse→ reversed
                                               │                  │
                          no valid bids ───────┴────→ rot         └─ precondition fails
                                                                     → resolution_failed
```

**`settled` is the only transition that writes to the ledger.** `resolved` produces winner, price
and a recorded tie-break reason and nothing else, so an officer can override (mandatory reason)
before any money moves.

### 11.2 Available balance

```
available(account, pool, kind)
  = balance_snapshot.amount_cp
  − Σ bid_hold.amount_cp WHERE pool_id = ? AND account_id = ? AND balance_kind = ? AND state = 'active'
```

The hold is inserted **in the same transaction** that validates the bid.

### 11.3 Concurrency hazards

| Hazard | Mitigation |
|---|---|
| Two bots open a session for the same drop | `ux_bid_live` partial unique + `Idempotency-Key` returns the existing session |
| 100 DKP, two simultaneous auctions, 100 bid in each | `writeDB.SetMaxOpenConns(1)` + `_txlock=immediate` serialises bid validation globally. `TestBid_ConcurrentSpend_OneSucceeds` (two goroutines, exactly one 201, one 409 `insufficient_balance`) passes with no locking code. |
| Decay lands between `resolved` and `settled` | settle re-validates `available(winner) ≥ price` **inside** the committing transaction → `resolution_failed`, never a silent award at a stale price |
| Settled twice | `ledger_batch.source_ref = 'bid_session:<id>'` + `ux_batch_srcref`; the second settle is a no-op returning the first batch |
| Bot retry duplicates a bid | `ux_bid_idem(session_id, idempotency_key)` |
| Clock skew | the server assigns `seq`, `created_at` and `closes_at`; every response carries `server_time` |
| Sealed-bid leakage | `amount_cp` is excluded from every read path, SSE frame and officer UI until `revealed_at IS NOT NULL`; `bid.reveal_early` is `is_dangerous` and every use writes an audit row |
| A bid arrives during `closing` | deterministic `409 session_closed` carrying the server's `closed_at` and the request's arrival `seq` |
| Proxy-bid war | the hold is taken on `max_amount_cp`, not the visible `amount_cp` |

### 11.4 The portability hazard, flagged now

On Postgres the single-writer serialisation disappears and the same code silently permits
double-spend. `account_lock` therefore ships in 1.0 even though it is a no-op on SQLite, and an
architectural test asserts the bid-validation path acquires it — so the Postgres port is a driver
detail rather than a redesign.

> ✗ **Not copied:** nothing, because EQdkp has no auction concept at all. Every guild bolts on a bot
> that keeps its own totals; this is the double-spend factory, and closing it is the reason the bot
> becomes a dumb terminal.

---

## 12. Items, loot, awards

### 12.1 Catalogue

```sql
CREATE TABLE item (
  id        TEXT NOT NULL PRIMARY KEY,
  name      TEXT NOT NULL,                        -- canonical display: "Vulak`Aerr's Robe"
  name_norm TEXT NOT NULL,                        -- NFKC + casefold + strip ' ` - + collapse spaces
  era       TEXT NULL,
  slot      TEXT NOT NULL DEFAULT '',
  is_no_drop INTEGER NOT NULL DEFAULT 0 CHECK (is_no_drop IN (0,1)),
  is_lore    INTEGER NOT NULL DEFAULT 0 CHECK (is_lore IN (0,1)),
  icon_id    INTEGER NULL,                        -- rendered locally; no bundled game assets
  stats_json TEXT NOT NULL DEFAULT '{}',          -- parsed statsblock; display only, never queried
  default_price_cp INTEGER NULL,                  -- the fixed_price strategy's catalogue
  default_item_pool_id TEXT NULL REFERENCES item_pool(id),
  tier      TEXT NOT NULL DEFAULT '',
  state     TEXT NOT NULL DEFAULT 'confirmed'
            CHECK (state IN ('confirmed','provisional','merged')),
  merged_into_item_id TEXT NULL REFERENCES item(id),
  source    TEXT NOT NULL DEFAULT 'manual'
            CHECK (source IN ('manual','seed','wiki_import','parser','eqdkp_import')),
  deleted_at INTEGER NULL,
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
) STRICT;
CREATE UNIQUE INDEX ux_item_name ON item(name_norm)
  WHERE deleted_at IS NULL AND state <> 'merged';
CREATE INDEX ix_item_tier ON item(tier) WHERE deleted_at IS NULL;

CREATE TABLE item_alias (
  id         TEXT NOT NULL PRIMARY KEY,
  item_id    TEXT NOT NULL REFERENCES item(id) ON DELETE CASCADE,
  alias      TEXT NOT NULL,                       -- 'CoF', 'flame cloak', 'Trakanon&#39;s Tooth'
  alias_norm TEXT NOT NULL,
  source     TEXT NOT NULL DEFAULT 'officer'
             CHECK (source IN ('officer','parser','import','wiki','auto_merge')),
  created_by TEXT NULL REFERENCES app_user(id),
  hit_count  INTEGER NOT NULL DEFAULT 0,          -- promotes good aliases in the resolver UI
  created_at INTEGER NOT NULL
) STRICT;
CREATE UNIQUE INDEX ux_item_alias ON item_alias(alias_norm);   -- global: an alias means ONE item

CREATE TABLE item_external_id (
  id      TEXT NOT NULL PRIMARY KEY,
  item_id TEXT NOT NULL REFERENCES item(id) ON DELETE CASCADE,
  source  TEXT NOT NULL
          CHECK (source IN ('p99_wiki','lucy_icon','lucy_item','eqemu','eqdkp_game_itemid')),
  external_id TEXT NOT NULL,
  url TEXT NOT NULL DEFAULT '',
  fetched_at INTEGER NULL,
  created_at INTEGER NOT NULL
) STRICT;
CREATE UNIQUE INDEX ux_item_ext      ON item_external_id(source, external_id);
CREATE INDEX        ix_item_ext_item ON item_external_id(item_id);
```

> **Cut:** a hand-built `item_trigram` table. Two fuzzy-search mechanisms is one too many; FTS5 plus
> Levenshtein over the top N is enough (§20).

### 12.2 The resolve ladder, and the provisional-item upsert

Single code path, `internal/loot.Resolve`:

1. `item.name_norm` exact → confirmed.
2. `item_alias.alias_norm` exact → confirmed; `hit_count++`.
3. Longest-match article strip: try the raw string, then strip a leading `a `/`an `/`the `, and
   **prefer the longest candidate that matches** — never a greedy prefix strip, which mangles
   `a Shiny Brass Idol`.
4. FTS5 top-N candidates re-ranked by Levenshtein in Go. If the top score ≥ `auto_threshold` **and**
   the margin over #2 ≥ `margin_threshold`, auto-map and record an `item_alias(source='parser')`.
5. Otherwise **never drop the award**: open (or increment) a `reconciliation_item` and award against
   a `provisional` item so the ledger is not blocked on a human.

**The critique found a real defect in step 5:** `ux_item_name` is partial
(`WHERE deleted_at IS NULL AND state <> 'merged'`), so a *second* parse of the same unknown name
raises a constraint violation instead of reusing the provisional row. The resolve path is therefore
an **upsert**, and the conflict target must repeat the index predicate exactly or SQLite will not
match the partial index:

```sql
-- name: UpsertProvisionalItem :one
INSERT INTO item (id, name, name_norm, state, source, created_at, updated_at)
VALUES (?1, ?2, ?3, 'provisional', 'parser', ?4, ?4)
ON CONFLICT (name_norm) WHERE deleted_at IS NULL AND state <> 'merged'
DO UPDATE SET updated_at = excluded.updated_at
RETURNING id, state;
```

**Enforced by:** `TestResolve_SameUnknownNameTwice_ReusesProvisional`, which parses the same unknown
loot line in two separate runs and asserts one `item` row, one `reconciliation_item` row with
`occurrences = 2`, and two awards pointing at the same item.

Resolving the queue item sets `item.merged_into_item_id`, writes an `item_alias`, and re-points
`item_instance.item_id` and `item_award.item_id` through a small audited job. Those are **fact**
tables and may be updated; the ledger is never touched.

### 12.3 Instances, awards, decay runs

```sql
CREATE TABLE item_instance (       -- a specific drop, in a specific raid. Bid sessions attach here.
  id        TEXT NOT NULL PRIMARY KEY,
  raid_id   TEXT NOT NULL REFERENCES raid(id),
  item_id   TEXT NULL REFERENCES item(id),        -- NULL only while wholly unresolved
  raw_name  TEXT NOT NULL,                        -- exactly as it appeared in the log
  raw_name_norm TEXT NOT NULL,
  quantity  INTEGER NOT NULL DEFAULT 1 CHECK (quantity >= 1),
  dropped_at INTEGER NULL,
  event_type_id TEXT NULL REFERENCES event_type(id),
  looter_character_id TEXT NULL REFERENCES character(id),   -- who the LOG says looted it
  state     TEXT NOT NULL DEFAULT 'dropped'
            CHECK (state IN ('dropped','in_auction','awarded','rotted','banked','voided')),
  artifact_id   TEXT NULL REFERENCES artifact(id),
  parse_line_id TEXT NULL REFERENCES parse_line(id) ON DELETE SET NULL,   -- pruned at 90 days; §21
  reconciliation_item_id TEXT NULL REFERENCES reconciliation_item(id),
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
) STRICT;
CREATE INDEX ix_item_instance_raid ON item_instance(raid_id, state);
CREATE INDEX ix_item_instance_item ON item_instance(item_id);

CREATE TABLE item_award (
  id       TEXT NOT NULL PRIMARY KEY,
  item_instance_id TEXT NOT NULL REFERENCES item_instance(id),
  item_id  TEXT NULL REFERENCES item(id),
  raid_id  TEXT NOT NULL REFERENCES raid(id),
  tick_id  TEXT NULL REFERENCES raid_tick(id),
  pool_id  TEXT NOT NULL REFERENCES pool(id),
  item_pool_id TEXT NULL REFERENCES item_pool(id),
  account_id   TEXT NOT NULL REFERENCES account(id),  -- who PAYS
  character_id TEXT NULL REFERENCES character(id),    -- who RECEIVES (attribution)
  price_cp INTEGER NOT NULL,
  share_bp INTEGER NOT NULL DEFAULT 10000
           CHECK (share_bp BETWEEN 1 AND 10000),      -- multi-buyer split; must sum to 10000
  award_type TEXT NOT NULL CHECK (award_type IN
             ('dkp_auction','fixed_price','loot_council','random',
              'rot','guild_bank','staged','free','import')),
  bid_session_id TEXT NULL REFERENCES bid_session(id),
  council_reason TEXT NOT NULL DEFAULT '',
  awarded_at INTEGER NOT NULL,
  awarded_by TEXT NULL REFERENCES app_user(id),
  ledger_batch_id TEXT NULL REFERENCES ledger_batch(id),
  voided_at   INTEGER NULL, voided_by TEXT NULL REFERENCES app_user(id),
  void_reason TEXT NOT NULL DEFAULT '',
  void_batch_id TEXT NULL REFERENCES ledger_batch(id),
  note TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
) STRICT;
CREATE INDEX ix_award_account  ON item_award(account_id, awarded_at DESC) WHERE voided_at IS NULL;
CREATE INDEX ix_award_item     ON item_award(item_id, awarded_at DESC)    WHERE voided_at IS NULL;
CREATE INDEX ix_award_raid     ON item_award(raid_id)                     WHERE voided_at IS NULL;
CREATE INDEX ix_award_instance ON item_award(item_instance_id);

CREATE TABLE decay_run (
  id      TEXT NOT NULL PRIMARY KEY,
  pool_id TEXT NOT NULL REFERENCES pool(id),
  cadence_period TEXT NOT NULL,                   -- '2026-W31' | '2026-08' | 'raid:<ulid>'
  scheduled_for_at INTEGER NOT NULL,
  executed_at INTEGER NULL,
  state   TEXT NOT NULL DEFAULT 'planned'
          CHECK (state IN ('planned','preview','committed','skipped','failed')),
  dry_run_result_json  TEXT NOT NULL DEFAULT '{}',
  config_snapshot_json TEXT NOT NULL DEFAULT '{}',
  ledger_batch_id TEXT NULL REFERENCES ledger_batch(id),
  triggered_by TEXT NULL REFERENCES app_user(id),
  error TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
) STRICT;
CREATE UNIQUE INDEX ux_decay_period ON decay_run(pool_id, cadence_period);
```

**Multi-buyer awards** are N `item_award` rows sharing an `item_instance_id`, with `share_bp`
summing to exactly 10000 — invariant-checked in the same transaction, not by a `VARCHAR(32)` group
key. *(Empirical assumption: whether any P99 guild actually splits an item's cost is unknown. If
none do, the invariant is trivially satisfied and the column costs nothing; it is modelled for
EQdkp import fidelity.)*

### 12.4 Item priority lists

EQdkp's `plugin-itemprio`, called out in the dossier as "quite relevant to P99" and previously
absent rather than deliberately deferred.

```sql
CREATE TABLE item_priority_list (
  id       TEXT NOT NULL PRIMARY KEY,
  name     TEXT NOT NULL, name_norm TEXT NOT NULL,
  class_id TEXT NULL REFERENCES game_class(id),   -- NULL ⇒ applies to everyone
  raid_role_id TEXT NULL REFERENCES raid_role(id),
  slot     TEXT NOT NULL DEFAULT '',
  notes    TEXT NOT NULL DEFAULT '',
  published INTEGER NOT NULL DEFAULT 0 CHECK (published IN (0,1)),
  sort_order INTEGER NOT NULL DEFAULT 0,
  deleted_at INTEGER NULL,
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
) STRICT;
CREATE UNIQUE INDEX ux_item_prio_name ON item_priority_list(name_norm) WHERE deleted_at IS NULL;

CREATE TABLE item_priority_entry (
  id      TEXT NOT NULL PRIMARY KEY,
  list_id TEXT NOT NULL REFERENCES item_priority_list(id) ON DELETE CASCADE,
  rank_no INTEGER NOT NULL,
  item_id TEXT NULL REFERENCES item(id),          -- NULL while the raw name is unresolved
  raw_name TEXT NOT NULL DEFAULT '',
  note    TEXT NOT NULL DEFAULT ''
) STRICT;
CREATE UNIQUE INDEX ux_item_prio_rank ON item_priority_entry(list_id, rank_no);
CREATE INDEX        ix_item_prio_item ON item_priority_entry(item_id) WHERE item_id IS NOT NULL;
```

---

## 13. Ingest: artifacts, parse runs, submissions

### 13.1 Artifacts and parsing

```sql
CREATE TABLE artifact (
  id        TEXT NOT NULL PRIMARY KEY,
  kind      TEXT NOT NULL CHECK (kind IN
            ('raid_dump','who_paste','log_slice','guild_dump','eqdkp_xml','csv','other')),
  filename  TEXT NOT NULL DEFAULT '',
  content_sha256 BLOB NOT NULL,
  size_bytes INTEGER NOT NULL,
  storage_path TEXT NOT NULL,                     -- /data/artifacts/<hh>/<sha256>
  media_type TEXT NOT NULL DEFAULT 'text/plain',
  uploaded_by       TEXT NULL REFERENCES app_user(id),
  uploaded_token_id TEXT NULL REFERENCES api_token(id),
  uploaded_at INTEGER NOT NULL,
  server_id   TEXT NULL REFERENCES server(id),
  source_character TEXT NOT NULL DEFAULT '',      -- from eqlog_<Char>_<server>.txt
  client_timezone  TEXT NOT NULL DEFAULT '',      -- the officer's PC tz — logs carry NO offset
  clock_offset_secs INTEGER NOT NULL DEFAULT 0,   -- detected skew, applied when normalising to UTC
  redacted_at INTEGER NULL,                       -- /tell-line redaction pass, on ingest
  retained_until_at INTEGER NULL,                 -- guild.artifact_retention_days, default 180
  purged_at   INTEGER NULL,                       -- content gone, hash and row KEPT (see below)
  created_at  INTEGER NOT NULL
) STRICT;
CREATE UNIQUE INDEX ux_artifact_sha ON artifact(content_sha256);   -- re-upload dedupe, free

CREATE TABLE parse_run (
  id          TEXT NOT NULL PRIMARY KEY,
  artifact_id TEXT NOT NULL REFERENCES artifact(id),
  parser_id   TEXT NOT NULL,                      -- 'p99_raid_dump' | 'p99_who' | 'p99_log_loot' | …
  parser_version TEXT NOT NULL,
  raid_id     TEXT NULL REFERENCES raid(id),
  state       TEXT NOT NULL DEFAULT 'pending'
              CHECK (state IN ('pending','running','preview','committed','failed','superseded')),
  options_json TEXT NOT NULL DEFAULT '{}',        -- award grammar, tick value, tz override
  stats_json   TEXT NOT NULL DEFAULT '{}',        -- lines total/matched/unmatched/ignored per kind
  error TEXT NOT NULL DEFAULT '',
  started_at INTEGER NULL, finished_at INTEGER NULL,
  committed_at INTEGER NULL, committed_by TEXT NULL REFERENCES app_user(id),
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
) STRICT;
CREATE INDEX ix_parse_artifact ON parse_run(artifact_id, created_at DESC);

CREATE TABLE parse_line (          -- staged lines. PRUNED at 90 days after commit; see §21.
  id           TEXT NOT NULL PRIMARY KEY,
  parse_run_id TEXT NOT NULL REFERENCES parse_run(id) ON DELETE CASCADE,
  line_no      INTEGER NOT NULL,
  raw          TEXT NOT NULL,                     -- exact bytes; the "report a parser bug" payload
  line_kind    TEXT NOT NULL,                     -- 'who_player'|'raid_row'|'looted_item'|'random'|
                                                  -- 'engage'|'slain'|'chat_award'|'zone'|'unknown'
  parsed_at    INTEGER NULL,                      -- the [ ] prefix, normalised to UTC
  payload_json TEXT NOT NULL DEFAULT '{}',
  status       TEXT NOT NULL DEFAULT 'matched'
               CHECK (status IN ('matched','unmatched','ambiguous','ignored','superseded','committed')),
  target_kind  TEXT NULL,                         -- 'character' | 'item' | 'event_type'
  target_id    TEXT NULL,
  reconciliation_item_id TEXT NULL REFERENCES reconciliation_item(id),
  created_at INTEGER NOT NULL
) STRICT;
CREATE UNIQUE INDEX ux_parse_line        ON parse_line(parse_run_id, line_no);
CREATE INDEX        ix_parse_line_status ON parse_line(parse_run_id, status);
```

**Artifacts are retained by default**, 180 days, with `/tell`-line redaction at ingest and a guild
opt-out (conventions §11). "Any member can download the dump behind this tick" is the strongest
anti-drama mechanism in the design and it dies under parse-and-discard. A purge sets `purged_at`,
**keeps the row and the hash**, writes an audit row, and is broadcast — so members see "the evidence
for tick 6 was deleted by X on date Y" rather than nothing.

`parse_run.state` `preview` → `committed` is the preview-and-commit substrate: nothing touches
`raid_tick` or `item_instance` until an officer (or a scoped token) commits, and the diff shown is a
query over `parse_line`, not an in-memory structure.

### 13.2 `raid_submission` — a real table, not a projection

The critique was clear: `POST /raid-submissions` returns a durable, ETagged, re-fetchable object
with `expires_at`, a per-person `ledger_preview`, `warnings`, `deduplicated`, and a commit receipt
carrying `first_seq`/`last_seq`. **That is state, not a projection.** `parse_run`/`parse_line` model
*one artifact*; a submission is *N artifacts plus ticks plus kills plus awards plus adjustments*,
with an `on_unresolved` policy attached. A projection cannot carry the policy, the diff or the
receipt.

```sql
CREATE TABLE raid_submission (
  id        TEXT NOT NULL PRIMARY KEY,
  state     TEXT NOT NULL DEFAULT 'preview'
            CHECK (state IN ('preview','committed','abandoned','expired','failed')),
  version   INTEGER NOT NULL DEFAULT 1,           -- ETag is "{id}:{version}"
  server_id TEXT NOT NULL REFERENCES server(id),
  raid_id   TEXT NULL REFERENCES raid(id),        -- set on commit, or on submission against an open raid
  on_unresolved TEXT NOT NULL DEFAULT 'quarantine'
            CHECK (on_unresolved IN ('fail','quarantine','create')),
  auto_commit INTEGER NOT NULL DEFAULT 0 CHECK (auto_commit IN (0,1)),
  request_hash BLOB NOT NULL,                     -- method + path template + body
  summary_json  TEXT NOT NULL DEFAULT '{}',       -- counts; rendered, never queried into
  warnings_json TEXT NOT NULL DEFAULT '[]',
  ledger_preview_json TEXT NOT NULL DEFAULT '[]', -- per-person deltas at preview time
  expires_at   INTEGER NOT NULL,                  -- 24 h; an expired preview must be re-posted
  committed_at INTEGER NULL,
  committed_by TEXT NULL REFERENCES app_user(id),
  first_seq INTEGER NULL, last_seq INTEGER NULL,  -- the commit receipt (per pool; see note)
  error_json TEXT NOT NULL DEFAULT '[]',          -- EVERY failure, not the first
  created_by       TEXT NULL REFERENCES app_user(id),
  created_token_id TEXT NULL REFERENCES api_token(id),
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
) STRICT;
CREATE INDEX ix_submission_state ON raid_submission(state, created_at DESC);
CREATE INDEX ix_submission_raid  ON raid_submission(raid_id) WHERE raid_id IS NOT NULL;

CREATE TABLE raid_submission_item (
  id            TEXT NOT NULL PRIMARY KEY,
  submission_id TEXT NOT NULL REFERENCES raid_submission(id) ON DELETE CASCADE,
  ordinal       INTEGER NOT NULL,                 -- position in the submitted array; stable for diffs
  kind          TEXT NOT NULL
                CHECK (kind IN ('tick','kill','award','adjustment','artifact')),
  payload_json  TEXT NOT NULL,                    -- exactly what the client sent
  resolved_json TEXT NOT NULL DEFAULT '{}',       -- names → ids, as resolved at preview time
  status        TEXT NOT NULL DEFAULT 'planned'
                CHECK (status IN ('planned','deduplicated','quarantined','committed','failed')),
  content_sha256 BLOB NULL,                       -- for kind='tick': the dedupe key
  created_entity_kind TEXT NULL,                  -- 'raid_tick' | 'item_award' | 'ledger_batch' | …
  created_entity_id   TEXT NULL,
  reconciliation_item_id TEXT NULL REFERENCES reconciliation_item(id),
  parse_line_id TEXT NULL REFERENCES parse_line(id) ON DELETE SET NULL,
  error TEXT NOT NULL DEFAULT ''
) STRICT;
CREATE UNIQUE INDEX ux_submission_item ON raid_submission_item(submission_id, ordinal);
CREATE INDEX ix_submission_item_status ON raid_submission_item(submission_id, status);
```

- **`version` is the ETag source.** `If-Match` is required on `/commit` and `/abandon`; resolving a
  reconciliation item that a submission depends on bumps `version`, so a stale client is told to
  re-read rather than committing a preview it never saw.
- **Atomicity.** Commit runs in one SQLite write transaction: raid, ticks, tick credits, attendance,
  kill credits, item resolution, awards, adjustments, every resulting `ledger_batch`/`ledger_entry`,
  artifact links, audit rows and outbox events. Any violation → nothing is written and `error_json`
  lists **every** failure.
- **`first_seq`/`last_seq`** are per pool. When a submission feeds more than one pool the receipt
  carries one `{pool_id, first_seq, last_seq}` triple per pool — a single global range would be
  meaningless, because `seq` is per pool (conventions §4).
- **Dedupe** is `content_sha256` per tick against `ux_tick_hash`. Two officers uploading overlapping
  dumps converge: the second submission reports `ticks_deduplicated` and creates only the genuinely
  new ones. This is the behaviour that makes "just upload your log, we'll sort it out" safe.
- **`on_unresolved`** defaults to `quarantine` (conventions §12). `create` is an explicit officer
  choice requiring `roster.write`.

---

## 14. The reconciliation queue

```sql
CREATE TABLE reconciliation_item (
  id     TEXT NOT NULL PRIMARY KEY,
  kind   TEXT NOT NULL CHECK (kind IN
         ('unknown_character','ambiguous_character','unknown_item','ambiguous_item',
          'unknown_event','duplicate_tick','orphan_award','unparsed_line',
          'anonymous_who','import_conflict')),
  -- ONE column, named `status`, holding the canonical enum. There is no second `resolution`
  -- column and no second vocabulary: the wire value IS the database value (conventions §5).
  status TEXT NOT NULL DEFAULT 'open'
         CHECK (status IN ('open','mapped','created','ignored','merged')),
  raw_value      TEXT NOT NULL,                   -- 'Whitened Treant Fists' | 'Zibaxia'
  raw_value_norm TEXT NOT NULL,
  occurrences    INTEGER NOT NULL DEFAULT 1,      -- the SAME unknown name collapses to ONE item
  first_seen_at INTEGER NOT NULL, last_seen_at INTEGER NOT NULL,
  context_json  TEXT NOT NULL DEFAULT '{}',       -- raid, zone, timestamp, neighbouring lines
  artifact_id   TEXT NULL REFERENCES artifact(id),
  parse_run_id  TEXT NULL REFERENCES parse_run(id),
  parse_line_id TEXT NULL REFERENCES parse_line(id) ON DELETE SET NULL,
  import_run_id TEXT NULL REFERENCES import_run(id),
  suggested_kind TEXT NULL, suggested_id TEXT NULL, suggested_score_bp INTEGER NULL,
  suggestions_json TEXT NOT NULL DEFAULT '[]',    -- top-5 fuzzy candidates with scores
  resolved_kind TEXT NULL, resolved_id TEXT NULL,
  resolved_by TEXT NULL REFERENCES app_user(id),
  resolved_at INTEGER NULL,
  create_alias INTEGER NOT NULL DEFAULT 1 CHECK (create_alias IN (0,1)),  -- learn from the fix
  note TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL,
  CHECK ((status = 'open') = (resolved_at IS NULL))
) STRICT;
CREATE UNIQUE INDEX ux_recon_open  ON reconciliation_item(kind, raw_value_norm) WHERE status = 'open';
CREATE INDEX        ix_recon_queue ON reconciliation_item(status, last_seen_at DESC);
```

`ux_recon_open` is the whole ergonomic argument: **"this looted item name matched nothing" produces
one queue entry, not one per occurrence.** Resolving it once with `create_alias = 1` writes an
`item_alias` row and every future occurrence auto-resolves. The queue shrinks monotonically as the
guild uses the product.

**Nothing is ever dropped.** An award against an unresolved name commits against a `provisional`
item, so the ledger is never blocked on a human; resolution re-points the mutable fact rows and
leaves the ledger untouched.

> ✗ **Not copied:** nothing — EQdkp has no equivalent. "This looted item name matched nothing" is
> the single most common P99 ingest event, and it gets a queue rather than a dropped row.

---

## 15. Outbox, idempotency, webhooks, disputes, notifications

```sql
CREATE TABLE event_outbox (
  event_seq  INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,   -- the GLOBAL sequence (conventions §4).
                                                  -- AUTOINCREMENT never reuses a rowid, so
                                                  -- Last-Event-ID replay stays sound after a prune.
  id         TEXT    NOT NULL,
  topic      TEXT    NOT NULL,                    -- 'bid:<ulid>' | 'raid:<ulid>' | 'guild'
  event_type TEXT    NOT NULL,                    -- 'bid.placed' | 'raid.tick.created'
  resource_ref TEXT  NOT NULL,                    -- '/api/v1/bid-sessions/<ulid>' — IDs, not documents
  created_at INTEGER NOT NULL
) STRICT;
CREATE INDEX ix_outbox_topic ON event_outbox(topic, event_seq);
```

`event_seq` is **not** `seq`. It is the outbox position that feeds the SSE frame `id:`, the
`X-DKP-Event-Sequence` header and `Last-Event-ID`. Ledger `seq` is per pool and answers a different
question. Sharing the name would guarantee that bot authors mix them.

```sql
CREATE TABLE idempotency_key (
  id            TEXT NOT NULL PRIMARY KEY,
  principal_ref TEXT NOT NULL,                    -- 'user:<ulid>' | 'service_account:<ulid>'
                                                  -- NEVER 'token:<ulid>': rotation mid-retry must replay
  key    TEXT NOT NULL,
  method TEXT NOT NULL, path TEXT NOT NULL,       -- the path TEMPLATE
  request_hash BLOB NOT NULL,                     -- method + path template + body
  state  TEXT NOT NULL DEFAULT 'in_flight' CHECK (state IN ('in_flight','completed')),
  status_code   INTEGER NULL,
  response_body TEXT NULL,
  created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL   -- 24 h TTL
) STRICT;
CREATE UNIQUE INDEX ux_idem        ON idempotency_key(principal_ref, key);
CREATE INDEX        ix_idem_expiry ON idempotency_key(expires_at);

CREATE TABLE webhook_endpoint (
  id  TEXT NOT NULL PRIMARY KEY,
  url TEXT NOT NULL, secret_enc BLOB NOT NULL,
  event_types TEXT NOT NULL DEFAULT '*',
  active      INTEGER NOT NULL DEFAULT 1 CHECK (active IN (0,1)),
  description TEXT NOT NULL DEFAULT '',
  created_by  TEXT NULL REFERENCES app_user(id),
  last_success_at INTEGER NULL,
  consecutive_failures INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
) STRICT;

CREATE TABLE webhook_delivery (
  id TEXT NOT NULL PRIMARY KEY,                   -- X-DKP-Delivery: stable across retries
  endpoint_id TEXT NOT NULL REFERENCES webhook_endpoint(id) ON DELETE CASCADE,
  outbox_event_seq INTEGER NOT NULL,
  event_type TEXT NOT NULL,
  payload_json TEXT NOT NULL,
  attempt INTEGER NOT NULL DEFAULT 0,
  state TEXT NOT NULL DEFAULT 'pending'
        CHECK (state IN ('pending','delivered','failed','dead_lettered')),
  next_attempt_at INTEGER NULL, last_status INTEGER NULL, last_error TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
) STRICT;
CREATE INDEX        ix_wh_pending ON webhook_delivery(state, next_attempt_at) WHERE state = 'pending';
CREATE UNIQUE INDEX ux_wh_once    ON webhook_delivery(endpoint_id, outbox_event_seq);

CREATE TABLE dispute (
  id TEXT NOT NULL PRIMARY KEY,
  opened_by_user_id TEXT NOT NULL REFERENCES app_user(id),
  person_id TEXT NULL REFERENCES person(id),
  pool_id   TEXT NULL REFERENCES pool(id),
  subject_kind TEXT NOT NULL CHECK (subject_kind IN
               ('raid','tick','award','adjustment','bid_session','balance','other')),
  subject_id TEXT NULL,
  state TEXT NOT NULL DEFAULT 'open' CHECK (state IN ('open','accepted','rejected','withdrawn')),
  body TEXT NOT NULL,
  resolution_note  TEXT NOT NULL DEFAULT '',
  resolution_batch_id TEXT NULL REFERENCES ledger_batch(id),   -- the correction, if any
  resolved_by TEXT NULL REFERENCES app_user(id), resolved_at INTEGER NULL,
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
) STRICT;
CREATE INDEX ix_dispute_open ON dispute(state, created_at DESC);

CREATE TABLE notification (        -- in-app; EQdkp's __notifications, with a real type column
  id      TEXT NOT NULL PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
  event_type TEXT NOT NULL,                       -- same vocabulary as event_outbox.event_type
  title TEXT NOT NULL, body TEXT NOT NULL DEFAULT '',
  resource_ref TEXT NOT NULL DEFAULT '',
  read_at INTEGER NULL,
  created_at INTEGER NOT NULL
) STRICT;
CREATE INDEX ix_notification_unread ON notification(user_id, created_at DESC) WHERE read_at IS NULL;

CREATE TABLE notification_pref (
  user_id    TEXT NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
  event_type TEXT NOT NULL,
  channel    TEXT NOT NULL CHECK (channel IN ('in_app','email','discord_dm')),
  enabled    INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
  PRIMARY KEY (user_id, event_type, channel)
) STRICT, WITHOUT ROWID;
```

`notification_pref` exists because "a bot can DM the outbid member" is punting a core feature to
software the guild has to write. `discord_dm` is honoured by the post-1.0 first-party bot; the
column and the preference ship now so that bot needs **no server change**.

River owns its own tables (`river_job`, `river_leader`, …), created by its migrator, in the same
SQLite file the officer already backs up — so a 3 a.m. import failure is visible at
`/api/v1/admin/jobs` without a second system. They are not modelled here.

---

## 16. EQdkp Plus import

Two phases with a staging boundary. Per **C4**, staging is a `stg_<eqdkp_table>` prefix **in the
main database**. Each `stg_*` table is a near-verbatim TEXT-typed mirror with three transforms
applied — byte decoding, entity unsanitising (`&#39;` → `'`, `&amp;` last), and per-value mojibake
repair — plus `stg_row_id` and `import_run_id`. They are `DROP`ped on finalise unless
`--keep-staging`.

**Why the staging boundary earns its cost:** the dry-run report becomes SQL the officer can run
themselves rather than an in-memory pass; the import is crash-resumable at table granularity;
row-level diffs are showable; and no MySQL-shaped code touches the domain model.

### 16.1 Run, id map, findings

```sql
CREATE TABLE import_run (
  id          TEXT NOT NULL PRIMARY KEY,
  source_kind TEXT NOT NULL CHECK (source_kind IN ('mysql','sql_dump','acp_backup_zip')),
  source_label TEXT NOT NULL DEFAULT '',          -- host/db or filename; NEVER credentials
  detected_prefix  TEXT NOT NULL DEFAULT '',
  detected_version TEXT NOT NULL DEFAULT '',
  detected_game    TEXT NOT NULL DEFAULT '',
  capability_json  TEXT NOT NULL DEFAULT '{}',    -- flags by COLUMN EXISTENCE, never by version
  phase TEXT NOT NULL DEFAULT 'fingerprint' CHECK (phase IN
        ('fingerprint','staging','transform','reconcile','done','failed','rolled_back')),
  dry_run INTEGER NOT NULL DEFAULT 1 CHECK (dry_run IN (0,1)),   -- writing is always explicit
  report_json TEXT NOT NULL DEFAULT '{}',         -- the permanent, linkable dry-run artifact
  report_artifact_id TEXT NULL REFERENCES artifact(id),
  snapshot_path TEXT NOT NULL DEFAULT '',         -- pre-write VACUUM INTO snapshot: the Undo button
  started_at INTEGER NULL, finished_at INTEGER NULL,
  started_by TEXT NULL REFERENCES app_user(id),
  error TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
) STRICT;

CREATE TABLE import_id_map (       -- PERSISTED: reruns, incremental passes, provenance, compat shim
  import_run_id TEXT NOT NULL REFERENCES import_run(id),
  entity    TEXT NOT NULL,         -- 'member' | 'raid' | 'item' | 'event' | 'user' | 'multidkp'
  legacy_id TEXT NOT NULL,
  new_id    TEXT NOT NULL,
  PRIMARY KEY (import_run_id, entity, legacy_id)
) STRICT, WITHOUT ROWID;
CREATE INDEX        ix_idmap_reverse ON import_id_map(entity, new_id);
CREATE UNIQUE INDEX ux_idmap_compat  ON import_id_map(entity, legacy_id);  -- api.php member_id lookup

CREATE TABLE import_finding (
  id TEXT NOT NULL PRIMARY KEY,
  import_run_id TEXT NOT NULL REFERENCES import_run(id),
  severity TEXT NOT NULL CHECK (severity IN ('info','warn','error')),
  code TEXT NOT NULL,              -- 'mojibake_repaired' | 'float_rounding_delta' | 'orphan_attendee'
                                   -- 'main_id_cycle' | 'plugin_table_skipped' | 'apa_rules_missing'
                                   -- 'duplicate_character_name' | 'unknown_column' | 'era_impossible'
  source_table TEXT NOT NULL DEFAULT '', source_pk TEXT NOT NULL DEFAULT '',
  detail_json TEXT NOT NULL DEFAULT '{}',
  created_at INTEGER NOT NULL
) STRICT;
CREATE INDEX ix_finding_run ON import_finding(import_run_id, severity, code);
```

### 16.2 Passwords, tokens, IPs

`__users.user_password` is **never imported**: `password_hash = NULL`, `must_reset = 1`, and one
`invitation` per person (§4.4). `exchange_key`, `user_login_key` and `auth_account` are never
imported either. `__logs.log_ipaddress` is dropped by default — volunteer guilds should not inherit
a decade of members' IP addresses.

### 16.3 `import_reconciliation` — the CI oracle

```sql
CREATE TABLE import_reconciliation (
  import_run_id TEXT NOT NULL REFERENCES import_run(id),
  legacy_member_id TEXT NOT NULL, legacy_mdkp_id TEXT NOT NULL,
  computed_current_cp INTEGER NOT NULL,
  eqdkp_current_cp    INTEGER NOT NULL,
  delta_cp            INTEGER NOT NULL,
  predicted_by_apa    INTEGER NOT NULL CHECK (predicted_by_apa IN (0,1)),
  PRIMARY KEY (import_run_id, legacy_member_id, legacy_mdkp_id)
) STRICT, WITHOUT ROWID;
```

The assertion, stated precisely: after an import, **the set of members whose totals mismatch
`__member_points` must equal the set predicted by the APA-detection step.** That turns "import as
much as possible" from a hope into a machine-verifiable property, and turns decay and cap deltas
into a plain-language "this guild used rules that live in a file we could not read — please re-enter
them" rather than an error.

> ✗ **Not copied — a licence rule, not a taste rule.** EQdkp Plus core is AGPL-3.0 and its game
> modules are CC BY-NC-SA. Reading a user's own database at runtime creates no derivative work;
> transcribing their PHP, their DDL text, their language strings or their icons does. The
> identifiers `pdh_`, `gen_class`, `plus_exchange` and `__multidkp2event` may appear only in
> `internal/importer/legacy_names.go` and `internal/api/compat/`. **Enforced by:** a CI grep
> everywhere else.

---

## 17. Audit log

```sql
CREATE TABLE audit_log (
  id  TEXT NOT NULL PRIMARY KEY,
  seq INTEGER NOT NULL,                           -- GAPLESS within the instance; allocated in-tx
  at  INTEGER NOT NULL,
  actor_kind TEXT NOT NULL
             CHECK (actor_kind IN ('user','service_account','system','boot','import','anonymous')),
  actor_user_id  TEXT NULL REFERENCES app_user(id),
  actor_service_account_id TEXT NULL REFERENCES service_account(id),
  actor_token_id TEXT NULL REFERENCES api_token(id),
  actor_label    TEXT NOT NULL DEFAULT '',        -- denormalised: survives user deletion/erasure
  action         TEXT NOT NULL,                   -- the PERMISSION key, verbatim: 'raid.tick.create'
  permission_used TEXT NULL REFERENCES permission(key),   -- WHICH permission authorised this
  operation_id   TEXT NOT NULL DEFAULT '',        -- the OpenAPI operationId
  resource_kind  TEXT NOT NULL,
  resource_id    TEXT NULL,
  resource_label TEXT NOT NULL DEFAULT '',
  outcome     TEXT NOT NULL CHECK (outcome IN ('success','denied','error')),
  before_json TEXT NULL,                          -- NULL on create
  after_json  TEXT NULL,                          -- NULL on delete
  reason      TEXT NOT NULL DEFAULT '',           -- MANDATORY for destructive/self-dealing actions
  actor_is_beneficiary INTEGER NOT NULL DEFAULT 0 CHECK (actor_is_beneficiary IN (0,1)),
  request_id TEXT NOT NULL DEFAULT '',
  ip         TEXT NOT NULL DEFAULT '',
  ip_truncated_at INTEGER NULL,                   -- retention: the octets go before the row does
  user_agent TEXT NOT NULL DEFAULT '',
  ledger_batch_id TEXT NULL REFERENCES ledger_batch(id),  -- the cross-link
  prev_hash BLOB NULL,
  hash      BLOB NOT NULL
) STRICT;
CREATE UNIQUE INDEX ux_audit_seq ON audit_log(seq);
CREATE INDEX ix_audit_at       ON audit_log(at DESC);
CREATE INDEX ix_audit_actor    ON audit_log(actor_user_id, at DESC);
CREATE INDEX ix_audit_resource ON audit_log(resource_kind, resource_id, at DESC);
CREATE INDEX ix_audit_action   ON audit_log(action, at DESC);

CREATE TRIGGER trg_audit_no_update BEFORE UPDATE ON audit_log
  BEGIN SELECT RAISE(ABORT, 'audit_log is append-only'); END;

CREATE TABLE audit_gap_marker (    -- pruning leaves a SCAR, not a silence
  id TEXT NOT NULL PRIMARY KEY,
  seq_from INTEGER NOT NULL, seq_to INTEGER NOT NULL,
  row_count INTEGER NOT NULL,
  boundary_hash_before BLOB NOT NULL, boundary_hash_after BLOB NOT NULL,
  pruned_by TEXT NULL REFERENCES app_user(id),
  reason TEXT NOT NULL DEFAULT '',
  at INTEGER NOT NULL
) STRICT;
```

`seq` is gapless and allocated inside the writing transaction, exactly like the ledger's, so the
chain in §9.6 has an ordering to hash over. `hash = SHA-256(prev_hash ‖ canonical_json(row without
hash))`, with the head in `dkp_meta('audit_head')`.

### 17.1 How it differs from the ledger

| | **Ledger** | **Audit log** |
|---|---|---|
| Answers | *What is the balance, and why is it that number?* | *Who did this, from where, with what authority, and what did it look like before?* |
| Granularity | one batch per economic event; one entry per (account, kind) delta | one row per **mutating action** (HTTP request or job step) |
| Coverage | only things that move points | **every** mutating action — role grants, settings changes, PAT mints, renames, a sealed-bid peek, a failed 403 |
| Arithmetic | participates in `SUM`; `net_amount_cp`, invariants | none |
| Correction | a reversal batch, linked | append a new row describing the fix |
| Ordering | per-pool `seq`, gapless, meaningful | instance-wide `seq`, gapless; `at` is wall-clock |
| Deletability | **never** — deleting corrupts every downstream balance | prunable by retention (default 3 years), leaving an `audit_gap_marker` |
| Reads | member-facing statement view | officer-facing forensic view (`audit.read`) |

**They overlap deliberately.** Committing a ledger batch also writes an audit row carrying
`ledger_batch_id`, IP, user agent and token. The batch is the *what*; the audit row is the
*who/how/from where*. Neither is derivable from the other.

**Audited reads**, not just writes: any officer read of a sealed bid before `closing`, backup
download, audit export, PII export, another member's email or IP history, token mint, import report
download.

**No endpoint deletes audit rows, at any permission level.** `dkp audit prune --before` requires an
interactive confirmation and writes an `audit_gap_marker`.

> ✗ **Not copied:** `__logs.log_value mediumtext` holding a *rendered language string*, plus an
> `a_logs_del` permission. A rendered sentence is neither queryable nor diffable, and a
> delete-the-audit-log permission is an anti-pattern with a name.

---

## 18. Calendar, signups, raid templates

```sql
CREATE TABLE calendar (
  id   TEXT NOT NULL PRIMARY KEY,
  name TEXT NOT NULL, name_norm TEXT NOT NULL,
  color TEXT NOT NULL DEFAULT '',
  kind  TEXT NOT NULL DEFAULT 'raid' CHECK (kind IN ('raid','user_raid','general')),
  is_private INTEGER NOT NULL DEFAULT 0 CHECK (is_private IN (0,1)),
  deleted_at INTEGER NULL,
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
) STRICT;
CREATE UNIQUE INDEX ux_calendar_name ON calendar(name_norm) WHERE deleted_at IS NULL;

CREATE TABLE calendar_event (
  id          TEXT NOT NULL PRIMARY KEY,
  calendar_id TEXT NOT NULL REFERENCES calendar(id),
  title TEXT NOT NULL, notes TEXT NOT NULL DEFAULT '',
  starts_at INTEGER NOT NULL, ends_at INTEGER NULL,
  all_day   INTEGER NOT NULL DEFAULT 0 CHECK (all_day IN (0,1)),
  timezone  TEXT NULL,                            -- overrides guild tz for THIS event
  signup_deadline_at INTEGER NULL,
  event_type_id TEXT NULL REFERENCES event_type(id),
  pool_id       TEXT NULL REFERENCES pool(id),
  raid_id       TEXT NULL REFERENCES raid(id),    -- the transform-to-raid BACK-REFERENCE
  template_id   TEXT NULL REFERENCES calendar_raid_template(id),
  repeat_rule   TEXT NOT NULL DEFAULT '',         -- RFC 5545 RRULE; expanded by a periodic job
  repeat_parent_id TEXT NULL REFERENCES calendar_event(id),
  state TEXT NOT NULL DEFAULT 'open' CHECK (state IN ('draft','open','closed','cancelled')),
  class_limits_json TEXT NOT NULL DEFAULT '{}',
  legacy_config_json TEXT NOT NULL DEFAULT '{}',  -- decoded EQdkp `extension` blob, NEVER interpreted
  created_by TEXT NULL REFERENCES app_user(id),
  deleted_at INTEGER NULL,
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
) STRICT;
CREATE INDEX ix_calevent_time ON calendar_event(starts_at) WHERE deleted_at IS NULL;
CREATE INDEX ix_calevent_raid ON calendar_event(raid_id)   WHERE raid_id IS NOT NULL;

CREATE TABLE signup (
  id TEXT NOT NULL PRIMARY KEY,
  calendar_event_id TEXT NOT NULL REFERENCES calendar_event(id) ON DELETE CASCADE,
  character_id  TEXT NULL REFERENCES character(id),
  guest_name    TEXT NOT NULL DEFAULT '',         -- guest signups have no character
  guest_class_id TEXT NULL REFERENCES game_class(id),
  raid_role_id  TEXT NULL REFERENCES raid_role(id),
  raid_group_id TEXT NULL REFERENCES raid_group(id),
  status TEXT NOT NULL,                           -- guild-configurable vocabulary; see below
  note   TEXT NOT NULL DEFAULT '',
  random_value INTEGER NULL,
  signed_by_user_id TEXT NULL REFERENCES app_user(id),   -- an officer signed someone else up
  signed_at  INTEGER NOT NULL,
  changed_at INTEGER NOT NULL,
  CHECK ((character_id IS NULL) <> (guest_name = ''))
) STRICT;
CREATE UNIQUE INDEX ux_signup ON signup(calendar_event_id, character_id)
  WHERE character_id IS NOT NULL;
CREATE INDEX ix_signup_event ON signup(calendar_event_id, status);

CREATE TABLE signup_status (       -- the guild's status vocabulary; the importer seeds it from
  id TEXT NOT NULL PRIMARY KEY,    -- the distinct set found in __calendar_raid_attendees
  key TEXT NOT NULL, key_norm TEXT NOT NULL,      -- 'yes' | 'maybe' | 'no' | 'late' | 'bench'
  label TEXT NOT NULL,
  counts_as TEXT NOT NULL DEFAULT 'attending'
            CHECK (counts_as IN ('attending','tentative','declined','other')),
  color TEXT NOT NULL DEFAULT '', sort_order INTEGER NOT NULL DEFAULT 0,
  enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1))
) STRICT;
CREATE UNIQUE INDEX ux_signup_status ON signup_status(key_norm);

CREATE TABLE calendar_raid_template (   -- saved signup templates; EQdkp __calendar_raid_templates
  id   TEXT NOT NULL PRIMARY KEY,
  name TEXT NOT NULL, name_norm TEXT NOT NULL,
  calendar_id   TEXT NOT NULL REFERENCES calendar(id),
  event_type_id TEXT NULL REFERENCES event_type(id),
  pool_id       TEXT NULL REFERENCES pool(id),
  raid_group_id TEXT NULL REFERENCES raid_group(id),
  default_title TEXT NOT NULL DEFAULT '',
  default_notes TEXT NOT NULL DEFAULT '',
  default_start_local TEXT NOT NULL DEFAULT '',   -- 'HH:MM' in guild.timezone
  default_duration_secs INTEGER NULL,
  signup_lead_hours INTEGER NULL,
  repeat_rule TEXT NOT NULL DEFAULT '',
  class_limits_json TEXT NOT NULL DEFAULT '{}',
  sort_order INTEGER NOT NULL DEFAULT 0,
  deleted_at INTEGER NULL,
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
) STRICT;
CREATE UNIQUE INDEX ux_cal_template ON calendar_raid_template(name_norm) WHERE deleted_at IS NULL;
```

- **`calendar_event.raid_id` is the back-reference EQdkp lacks.** Its transform creates a fresh
  `__raids` row with no link, so nobody can answer "which signup became which raid?". One nullable
  FK fixes it, and the post-import heuristic ("these 47 calendar raids look like these 47 raids")
  writes into the same column, previewed, never automatically.
- **`calendar_raid_template`** was flagged by the critique as absent: a guild that raids the same
  four nights recreates the event by hand every week. One table, one endpoint.
- **The signup CSV export** joins `signup` → `character` → `person` → `balance_snapshot` and
  `attendance_rollup`. That annotated list is the sheet the raid leader actually uses to pick the
  raid, and it is why away mode (§6.1) and standings must be queryable together.
- **`legacy_config_json`** holds EQdkp's PHP-serialised `extension` blob, decoded to JSON and stored
  verbatim. Its internal key set is **unverified**, so it is surfaced in the UI as "legacy settings"
  text and never interpreted.

---

## 19. Portal and CMS

Full EQdkp portal parity is in scope, sequenced last (Phase 8) so it can slip to 1.1 without
blocking guild adoption. It is not optional: the importer already reads `__articles`,
`__article_categories` and `__comments`, and `feed_token.kind` includes `articles_rss` — a design
where those have no destination is self-contradictory.

**Untrusted rich text lives here.** Everything with a `body_source` is sanitised on **write** and
again on **render**, in `internal/richtext`. EQdkp stores raw and renders at display time, which is
a documented XSS surface with a published advisory.

### 19.1 Articles, categories, comments

```sql
CREATE TABLE article_category (
  id   TEXT NOT NULL PRIMARY KEY,
  name TEXT NOT NULL, name_norm TEXT NOT NULL,
  slug TEXT NOT NULL,
  parent_id   TEXT NULL REFERENCES article_category(id),
  description TEXT NOT NULL DEFAULT '',
  per_page    INTEGER NOT NULL DEFAULT 10,
  published   INTEGER NOT NULL DEFAULT 1 CHECK (published IN (0,1)),
  sort_order  INTEGER NOT NULL DEFAULT 0,
  deleted_at  INTEGER NULL,
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
) STRICT;
CREATE UNIQUE INDEX ux_article_cat_slug ON article_category(slug) WHERE deleted_at IS NULL;

CREATE TABLE article (
  id          TEXT NOT NULL PRIMARY KEY,
  category_id TEXT NULL REFERENCES article_category(id),
  title TEXT NOT NULL, title_norm TEXT NOT NULL,
  slug  TEXT NOT NULL,
  body_source TEXT NOT NULL,                      -- what the author typed
  body_format TEXT NOT NULL DEFAULT 'markdown'
              CHECK (body_format IN ('markdown','html','bbcode_legacy')),
  body_html   TEXT NOT NULL,                      -- sanitised render, regenerated on edit
  excerpt     TEXT NOT NULL DEFAULT '',
  preview_media_id TEXT NULL REFERENCES media(id),
  state TEXT NOT NULL DEFAULT 'draft'
        CHECK (state IN ('draft','published','archived')),
  featured INTEGER NOT NULL DEFAULT 0 CHECK (featured IN (0,1)),
  comments_enabled INTEGER NOT NULL DEFAULT 1 CHECK (comments_enabled IN (0,1)),
  publish_from_at  INTEGER NULL, publish_until_at INTEGER NULL,
  author_user_id   TEXT NULL REFERENCES app_user(id),
  last_edited_at INTEGER NULL, last_edited_by TEXT NULL REFERENCES app_user(id),
  hits INTEGER NOT NULL DEFAULT 0,
  visibility TEXT NOT NULL DEFAULT 'public'
             CHECK (visibility IN ('public','members','officers')),
  legacy_i18n_json TEXT NOT NULL DEFAULT '{}',    -- non-default-language titles from the import
  deleted_at INTEGER NULL,
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
) STRICT;
CREATE UNIQUE INDEX ux_article_slug ON article(slug) WHERE deleted_at IS NULL;
CREATE INDEX ix_article_feed ON article(state, publish_from_at DESC) WHERE deleted_at IS NULL;
CREATE INDEX ix_article_cat  ON article(category_id, publish_from_at DESC) WHERE deleted_at IS NULL;

CREATE TABLE article_tag (
  article_id TEXT NOT NULL REFERENCES article(id) ON DELETE CASCADE,
  tag        TEXT NOT NULL,
  tag_norm   TEXT NOT NULL,
  PRIMARY KEY (article_id, tag_norm)
) STRICT, WITHOUT ROWID;
CREATE INDEX ix_article_tag ON article_tag(tag_norm);

CREATE TABLE article_comment (
  id         TEXT NOT NULL PRIMARY KEY,
  article_id TEXT NOT NULL REFERENCES article(id) ON DELETE CASCADE,
  author_user_id TEXT NULL REFERENCES app_user(id),
  author_label   TEXT NOT NULL DEFAULT '',        -- survives user erasure
  body_source TEXT NOT NULL,
  body_html   TEXT NOT NULL,
  state TEXT NOT NULL DEFAULT 'visible'
        CHECK (state IN ('visible','pending','spam','removed')),
  reply_to_id TEXT NULL REFERENCES article_comment(id),
  removed_by  TEXT NULL REFERENCES app_user(id),
  ip TEXT NOT NULL DEFAULT '',
  deleted_at INTEGER NULL,
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
) STRICT;
CREATE INDEX ix_comment_article ON article_comment(article_id, created_at)
  WHERE deleted_at IS NULL AND state = 'visible';
```

`article_category.permissions` is **not** imported: EQdkp serialises a nested
`rea/cre/upd/del/chs → group_id → flag` map whose `-1` semantics are unverified, and guessing an ACL
is the one thing least-privilege forbids. Categories import officer-visible and the report says so.

### 19.2 Media library

```sql
CREATE TABLE media (
  id       TEXT NOT NULL PRIMARY KEY,
  kind     TEXT NOT NULL CHECK (kind IN ('image','file','avatar','icon')),
  filename TEXT NOT NULL,
  content_sha256 BLOB NOT NULL,
  size_bytes INTEGER NOT NULL,
  storage_path TEXT NOT NULL,
  media_type TEXT NOT NULL,
  width INTEGER NULL, height INTEGER NULL,
  alt_text TEXT NOT NULL DEFAULT '',
  folder   TEXT NOT NULL DEFAULT '',
  uploaded_by TEXT NULL REFERENCES app_user(id),
  legacy_path TEXT NOT NULL DEFAULT '',           -- the EQdkp path string, for rewriting inline markup
  deleted_at INTEGER NULL,
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
) STRICT;
CREATE UNIQUE INDEX ux_media_sha ON media(content_sha256) WHERE deleted_at IS NULL;
CREATE INDEX ix_media_folder ON media(folder, created_at DESC) WHERE deleted_at IS NULL;
```

> ✗ **Not copied:** EQdkp has **no media table at all** — the media manager is a filesystem browser
> and every reference is a raw path string in `members.picture`, `articles.previewimage`,
> `styles.*_img` and inline article markup. A real table is what makes avatars, rank icons and
> article images referential (`media_id` FKs throughout this document) and what makes the import's
> asset-rewriting pass verifiable.

### 19.3 Portal blocks, menu, team page, shoutbox

```sql
CREATE TABLE portal_block (        -- the configurable dashboard
  id     TEXT NOT NULL PRIMARY KEY,
  module TEXT NOT NULL,            -- 'standings' | 'leaderboard' | 'next_raids' | 'last_items' |
                                   -- 'last_raids' | 'news' | 'shoutbox' | 'recruitment' | 'html'
  title  TEXT NOT NULL DEFAULT '',
  region TEXT NOT NULL DEFAULT 'main' CHECK (region IN ('left','main','right','top','bottom')),
  column_no  INTEGER NOT NULL DEFAULT 1,
  sort_order INTEGER NOT NULL DEFAULT 0,
  route      TEXT NOT NULL DEFAULT '*',           -- which SPA route this block appears on
  visibility TEXT NOT NULL DEFAULT 'public'
             CHECK (visibility IN ('public','members','officers')),
  config_json TEXT NOT NULL DEFAULT '{}',         -- per-module settings; validated per module
  collapsible INTEGER NOT NULL DEFAULT 0 CHECK (collapsible IN (0,1)),
  enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
) STRICT;
CREATE INDEX ix_portal_block ON portal_block(route, region, column_no, sort_order)
  WHERE enabled = 1;

CREATE TABLE menu_item (
  id        TEXT NOT NULL PRIMARY KEY,
  parent_id TEXT NULL REFERENCES menu_item(id),   -- 3 levels max; depth is validated in Go
  label     TEXT NOT NULL,
  target_kind TEXT NOT NULL DEFAULT 'route'
              CHECK (target_kind IN ('route','url','article','category','divider')),
  target     TEXT NOT NULL DEFAULT '',
  open_in_new_window INTEGER NOT NULL DEFAULT 0 CHECK (open_in_new_window IN (0,1)),
  icon       TEXT NOT NULL DEFAULT '',
  required_permission_key TEXT NULL REFERENCES permission(key),  -- FK ⇒ no invented keys
  visibility TEXT NOT NULL DEFAULT 'public'
             CHECK (visibility IN ('public','members','officers')),
  sort_order INTEGER NOT NULL DEFAULT 0,
  enabled    INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
) STRICT;
CREATE INDEX ix_menu ON menu_item(parent_id, sort_order) WHERE enabled = 1;

CREATE TABLE team_member (         -- the officers/staff page
  id TEXT NOT NULL PRIMARY KEY,
  person_id TEXT NULL REFERENCES person(id),
  user_id   TEXT NULL REFERENCES app_user(id),
  display_name TEXT NOT NULL,
  title TEXT NOT NULL DEFAULT '',                 -- 'Raid Leader', 'Loot Officer'
  blurb TEXT NOT NULL DEFAULT '',
  contact TEXT NOT NULL DEFAULT '',
  avatar_media_id TEXT NULL REFERENCES media(id),
  sort_order INTEGER NOT NULL DEFAULT 0,
  published  INTEGER NOT NULL DEFAULT 1 CHECK (published IN (0,1)),
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
) STRICT;

CREATE TABLE shoutbox_message (
  id TEXT NOT NULL PRIMARY KEY,
  author_user_id TEXT NULL REFERENCES app_user(id),
  author_label   TEXT NOT NULL DEFAULT '',        -- survives erasure
  body_source TEXT NOT NULL,
  body_html   TEXT NOT NULL,
  deleted_at INTEGER NULL, deleted_by TEXT NULL REFERENCES app_user(id),
  created_at INTEGER NOT NULL
) STRICT;
CREATE INDEX ix_shout_recent ON shoutbox_message(created_at DESC) WHERE deleted_at IS NULL;
```

The **leaderboard** block that appears on every EQdkp front page is `portal_block(module =
'leaderboard')` with `config_json` selecting top-N, per-class or per-role. It is one query over
`balance_snapshot` grouped by `character.class_id` — no new table.

**Style manager.** The ~70-variable LESS style editor is **not** reproduced. Theming is a small set
of CSS custom properties in `guild.settings_json` plus a logo `media_id`. EQdkp's `__styles` table
is never imported (it is 80 cosmetic columns describing a template engine we do not have).

### 19.4 Guild bank

```sql
CREATE TABLE guild_bank_item (
  id      TEXT NOT NULL PRIMARY KEY,
  item_id TEXT NULL REFERENCES item(id),
  raw_name TEXT NOT NULL DEFAULT '',              -- unresolved names still get shelved
  quantity INTEGER NOT NULL DEFAULT 1 CHECK (quantity >= 1),
  holder_character_id TEXT NULL REFERENCES character(id),   -- which mule is carrying it
  location TEXT NOT NULL DEFAULT '',
  state TEXT NOT NULL DEFAULT 'held'
        CHECK (state IN ('held','listed','issued','sold','lost')),
  acquired_from_award_id TEXT NULL REFERENCES item_award(id),
  issued_to_character_id TEXT NULL REFERENCES character(id),
  issued_at INTEGER NULL,
  price_cp  INTEGER NULL,
  ledger_batch_id TEXT NULL REFERENCES ledger_batch(id),    -- when an issue costs points
  note TEXT NOT NULL DEFAULT '',
  deleted_at INTEGER NULL,
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
) STRICT;
CREATE INDEX ix_bank_state ON guild_bank_item(state, updated_at DESC) WHERE deleted_at IS NULL;
CREATE INDEX ix_bank_item  ON guild_bank_item(item_id) WHERE deleted_at IS NULL;
```

A rotted item awarded to the `guild_bank` system account (§6.1) produces a `guild_bank_item` row, so
"where did that go?" has an answer. Issuing it later for points writes an ordinary award batch.

### 19.5 Recruitment and applications

```sql
CREATE TABLE recruitment_opening (
  id TEXT NOT NULL PRIMARY KEY,
  class_id     TEXT NULL REFERENCES game_class(id),
  raid_role_id TEXT NULL REFERENCES raid_role(id),
  status TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open','limited','closed')),
  note   TEXT NOT NULL DEFAULT '',
  sort_order INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
) STRICT;
CREATE UNIQUE INDEX ux_opening ON recruitment_opening(COALESCE(class_id,''), COALESCE(raid_role_id,''));

CREATE TABLE application_question (
  id  TEXT NOT NULL PRIMARY KEY,
  key TEXT NOT NULL, key_norm TEXT NOT NULL,
  label TEXT NOT NULL, help TEXT NOT NULL DEFAULT '',
  kind  TEXT NOT NULL CHECK (kind IN ('text','textarea','int','enum','bool','url')),
  options_json TEXT NOT NULL DEFAULT '[]',
  required INTEGER NOT NULL DEFAULT 0 CHECK (required IN (0,1)),
  sort_order INTEGER NOT NULL DEFAULT 0,
  enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1))
) STRICT;
CREATE UNIQUE INDEX ux_app_question ON application_question(key_norm);

CREATE TABLE application (
  id TEXT NOT NULL PRIMARY KEY,
  applicant_user_id TEXT NULL REFERENCES app_user(id),
  character_name TEXT NOT NULL, character_name_norm TEXT NOT NULL,
  class_id TEXT NULL REFERENCES game_class(id),
  level    INTEGER NULL,
  contact  TEXT NOT NULL DEFAULT '',              -- Discord handle, usually
  status TEXT NOT NULL DEFAULT 'new'
         CHECK (status IN ('new','in_review','accepted','rejected','withdrawn')),
  decided_by TEXT NULL REFERENCES app_user(id), decided_at INTEGER NULL,
  decision_note TEXT NOT NULL DEFAULT '',
  created_person_id TEXT NULL REFERENCES person(id),   -- what acceptance produced
  ip TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
) STRICT;
CREATE INDEX ix_application_queue ON application(status, created_at DESC);

CREATE TABLE application_answer (
  application_id TEXT NOT NULL REFERENCES application(id) ON DELETE CASCADE,
  question_id    TEXT NOT NULL REFERENCES application_question(id),
  value_text TEXT NOT NULL DEFAULT '',
  value_int  INTEGER NULL,
  PRIMARY KEY (application_id, question_id)
) STRICT, WITHOUT ROWID;

CREATE TABLE application_comment (  -- the officer discussion thread, never visible to the applicant
  id TEXT NOT NULL PRIMARY KEY,
  application_id TEXT NOT NULL REFERENCES application(id) ON DELETE CASCADE,
  author_user_id TEXT NOT NULL REFERENCES app_user(id),
  body TEXT NOT NULL,
  created_at INTEGER NOT NULL
) STRICT;
CREATE INDEX ix_app_comment ON application_comment(application_id, created_at);
```

`application.created_person_id` is the acceptance receipt: accepting an application creates the
`person` (and optionally the `character` and an `invitation`) in one transaction and records which
row it produced, so recruitment history and roster history are joinable.

---

## 20. Search

Search sits behind a `Search` interface in `internal/search` with two implementations (~120 lines
each): SQLite FTS5 today, Postgres `tsvector` post-1.2. Until then the Postgres path returns
`501 engine_unsupported`. This is the third and last genuine dialect divergence (§9.3, §11.4).

**One index, one document table, four entity kinds.** Person/character search is how officers find
anything, and a single `/search` endpoint covering characters, persons, items and articles is what
EQdkp's search page did.

```sql
CREATE TABLE search_doc (          -- a plain rowid table with an EXPLICIT INTEGER PRIMARY KEY,
  rowid_i     INTEGER NOT NULL PRIMARY KEY,       -- so the rowid is VACUUM-stable
  entity_kind TEXT NOT NULL CHECK (entity_kind IN ('character','person','item','article')),
  entity_id   TEXT NOT NULL,                      -- the ULID
  title  TEXT NOT NULL,                           -- name / display_name / item name / article title
  body   TEXT NOT NULL DEFAULT '',                -- aliases, notes, excerpt, searchable field values
  visibility TEXT NOT NULL DEFAULT 'members'
             CHECK (visibility IN ('public','members','officers')),
  updated_at INTEGER NOT NULL
) STRICT;
CREATE UNIQUE INDEX ux_search_entity ON search_doc(entity_kind, entity_id);

-- EXTERNAL-content FTS5 over search_doc. NOT contentless.
CREATE VIRTUAL TABLE search_fts USING fts5(
  title, body,
  content='search_doc', content_rowid='rowid_i',
  tokenize='unicode61 remove_diacritics 2'
);
```

**Why external content and not `content=''`.** The critique flagged contentless FTS5 with sync
triggers as a classic source of silent index corruption: a contentless table cannot return column
content, and every delete must supply the *old* values by hand — get one wrong and the index rots
without any error. External content keeps the values in `search_doc`, so the delete trigger reads
them from `old.*` and correctness does not depend on the caller remembering. *(The specific failure
mode is reported rather than reproduced here — treat it as unverified detail, but the safer shape
costs nothing.)*

**Two layers, deliberately:**

1. `search_doc` is maintained **in Go**, inside the same transaction as the write, via one
   `search.Index(tx, kind, id, doc)` call in `internal/store`. Triggers on eight domain tables would
   be eight places to get wrong; one function is one place to test.
2. `search_fts` is maintained by **three triggers on `search_doc` only** — the standard
   external-content pattern.

```sql
CREATE TRIGGER trg_search_ai AFTER INSERT ON search_doc BEGIN
  INSERT INTO search_fts(rowid, title, body) VALUES (new.rowid_i, new.title, new.body);
END;
CREATE TRIGGER trg_search_ad AFTER DELETE ON search_doc BEGIN
  INSERT INTO search_fts(search_fts, rowid, title, body) VALUES ('delete', old.rowid_i, old.title, old.body);
END;
CREATE TRIGGER trg_search_au AFTER UPDATE ON search_doc BEGIN
  INSERT INTO search_fts(search_fts, rowid, title, body) VALUES ('delete', old.rowid_i, old.title, old.body);
  INSERT INTO search_fts(rowid, title, body) VALUES (new.rowid_i, new.title, new.body);
END;
```

**Fuzzy item resolution** is FTS5 `bm25()` for the top 50 candidates, re-ranked by Levenshtein
distance in Go (§12.2). There is **no `item_trigram` table** — two fuzzy mechanisms is one too many,
and the trigram table was the one with no query planner behind it.

**Enforced by:** `dkp doctor --check search-index` compares `COUNT(*)` and a sampled checksum
between `search_doc` and its source tables and reports drift; `dkp reindex-search` rebuilds both
layers from scratch. An integration test deletes a row and asserts the FTS row is gone — the exact
failure contentless FTS5 makes silent.

---

## 21. Soft delete, retention, pruning

**The default is hard delete. Soft delete is an opt-in list and every entry justifies itself.** A
universal `deleted_at` is a known trap: every query needs a predicate, every unique index must be
partial, FKs stop meaning what they say, and "deleted" rows keep turning up in aggregates.

| Policy | Tables | Rationale |
|---|---|---|
| **Immutable** (DB trigger) | `ledger_batch`, `ledger_entry`, `bid` (amount/account/seq), `audit_log` | Deleting corrupts balances or destroys forensics. Corrections are appends. |
| **Void/tombstone** (`voided_at` + a mandatory reversal batch in the same transaction) | `raid_tick`, `raid_tick_credit`, `item_award` | These carry money. They stay visible, struck through. Test-enforced pairing. |
| **Soft delete** (`deleted_at`, **partial** unique indexes, every read filters) | `person`, `character`, `item`, `raid`, `event_type`, `guild_rank`, `raid_role`, `raid_group`, `item_pool`, `calendar`, `calendar_event`, `calendar_raid_template`, `app_user`, `role`, `article`, `article_category`, `article_comment`, `media`, `shoutbox_message`, `guild_bank_item`, `item_priority_list`, `character_field_def` | All are referenced by immutable ledger or audit rows. Hard-deleting them breaks the statement view: "who was this award for?" |
| **Hard delete** | `session`, `idempotency_key`, `event_outbox`, `webhook_delivery`, `parse_line`, `parse_run`, `stg_*`, `import_id_map` (with its run), `balance_snapshot`, `attendance_rollup`, `search_doc`, `bid_hold`, `notification`, `raid_submission` (expired previews) | Caches, transient state or replaceable derivations. Pruned by retention jobs. |
| **Hard delete, cascading** | `role_permission`, `role_assignment`, `pool_event_type`, `pool_item_pool`, `raid_pool`, `raid_officer`, `raid_artifact`, `raid_role_class`, `game_race_class`, `raid_attendance` (with its tick), `article_tag`, `application_answer` | Pure junctions with no independent identity. |

Rules that make this workable:

- **Every soft-deleted table's unique indexes are partial** (`WHERE deleted_at IS NULL`), so a name
  can be reused after deletion. Omit this and deletion becomes irreversible name squatting.
- **Soft delete never cascades.** Deleting a `person` does not delete their characters; the roster
  hides them. Cascading soft delete is the second-most-common source of "where did our data go".
- **GDPR/erasure is pseudonymisation, not deletion.** `app_user` gets `deleted_at`, `email = NULL`
  and `username` rewritten to `deleted-<ulid>`; `audit_log.actor_label`, `article_comment.author_label`
  and `shoutbox_message.author_label` already hold denormalised snapshots so historical rows still
  render. `person` and the ledger are untouched — guild point history is guild data.
- **The one true hard delete** is `dkp admin reset --scope=…`: destructive, typed confirmation,
  snapshot first, and itself audit-logged in the *post-reset* database with `actor_label` preserved.

### 21.1 Retention defaults

| Table | Default | Note |
|---|---|---|
| `artifact` (blob) | **180 days**, guild-configurable, opt-out | `/tell`-line redaction at ingest. Purge sets `purged_at` and keeps the row + hash. |
| `parse_line` | 90 days after commit | See the FK note below. |
| `audit_log` | 3 years | Pruning writes an `audit_gap_marker`; IP octets are truncated at 90 days via `ip_truncated_at`. |
| `event_outbox` | 24 h | `AUTOINCREMENT` keeps `Last-Event-ID` sound past the prune. |
| `idempotency_key` | 24 h | |
| `webhook_delivery` | 30 days | |
| `session` | 30 days past expiry | |
| `raid_submission` | `expires_at`, 24 h, for `preview`; committed submissions are kept | The receipt is the audit trail for a commit. |
| `notification` | 90 days | |

### 21.2 The `parse_line` FK problem

`parse_line` is pruned at 90 days while `item_instance` points at the line that produced it. Under
`PRAGMA foreign_keys = ON` a hard FK either blocks the prune or fails it. Per
[conventions §11](00-canonical-conventions.md), **every reference to `parse_line` is nullable with
`ON DELETE SET NULL`**:

```sql
item_instance.parse_line_id        TEXT NULL REFERENCES parse_line(id) ON DELETE SET NULL
reconciliation_item.parse_line_id  TEXT NULL REFERENCES parse_line(id) ON DELETE SET NULL
raid_submission_item.parse_line_id TEXT NULL REFERENCES parse_line(id) ON DELETE SET NULL
```

The `artifact` row survives the prune, so "show me the source" still resolves to the file, one level
coarser. **Enforced by:** `TestPrune_ParseLines_NullsReferences`, which prunes a run and asserts the
referencing rows survive with `parse_line_id IS NULL` and the artifact still downloads.

---

## 22. Hot query → index

| Query | Index |
|---|---|
| Standings page (200 members, 12 columns) | `ix_snapshot_standings` + `ix_att_rank` + `ix_person_state` |
| Account statement / `GET /accounts/{id}/ledger` | `ix_entry_stmt(account_id, pool_id, seq DESC)` |
| Balance as of a seq (verifier, dispute, settle revalidation) | `ix_entry_balance(pool_id, account_id, balance_kind, seq, amount_cp)` — **covering** |
| Self-dealing report | `ix_batch_selfdeal(actor_is_beneficiary, recorded_at DESC)` |
| Bid leaderboard | `ix_bid_session(session_id, amount_cp DESC, seq ASC)` |
| Available balance during a bid | `ix_hold_account(pool_id, account_id, balance_kind) WHERE state='active'` |
| Bid supervisor re-arm on boot | `ix_bid_open(state, closes_at)` |
| Parser: title → class | `ux_class_title_norm(title_norm)` |
| Parser: name → character | `ux_character_name(server_id, name_norm)`, then `ix_cnh_lookup` |
| Parser: loot name → item | `ux_item_name` → `ux_item_alias` → `search_fts` |
| Parser: `X has been slain by Y` → event | `ix_event_type_slain(slain_pattern_norm, slain_priority DESC)` |
| Duplicate tick from a re-uploaded dump | `ux_tick_hash(raid_id, content_sha256)` |
| Re-uploaded artifact | `ux_artifact_sha(content_sha256)` |
| Attendance denominator (§10.2) | `ix_tick_credit_pool` + `ix_tick_at` + `ix_raid_group` + `ux_attendance` |
| Reconciliation queue | `ux_recon_open(kind, raw_value_norm)` + `ix_recon_queue` |
| Item purchase history across the guild | `ix_award_item(item_id, awarded_at DESC)` |
| Custom-field filter ("Alt of = Tankguy") | `ix_cfv_text(field_def_id, value_text_norm)` |
| Who is away this week | `ix_person_away(away_until_at)` |
| PAT auth on every request | `ux_api_token_hash` — one lookup, one constant-time compare |
| Session auth | `ux_session_token(token_hash)` |
| SSE replay from `Last-Event-ID` | `ix_outbox_topic(topic, event_seq)` |
| Audit forensics ("everything Bob did", "everything to raid 481") | `ix_audit_actor`, `ix_audit_resource` |
| Compat shim: EQdkp `member_id` → our id | `ux_idmap_compat(entity, legacy_id)` |
| News feed / RSS | `ix_article_feed(state, publish_from_at DESC)` |

---

## 23. Where EQdkp Plus's model is deliberately not copied

| # | EQdkp Plus | Dragon Kill Party | Why, in one line |
|---|---|---|---|
| 1 | `members.member_main_id` self-FK, single level, three sentinel values, real cycles in the wild | `person` → `account` → `character`; `role='main'` + a partial unique index | Alts are the #1 P99 pain point; cycles become structurally impossible. |
| 2 | `with_twink` threaded through every read; a global `show_twinks` | no such parameter — aggregation *is* the storage layout | Halves the query surface and makes per-pool alt policy possible. |
| 3 | A boxed character counted twice in one raid | attendance on `character`, ledger on `account`, planner does `DISTINCT account_id` | A schema fix for a problem every guild hits. |
| 4 | `members.points`/`points_apa` PHP-serialised caches + a "recalculate" button | derived `SUM` + `balance_snapshot` in the same transaction + nightly full recompute + drift alarm | The cache derives from an *immutable* log, so rebuild is total and drift is detectable. |
| 5 | `float(6,2)` / `float(10,2)` / `float(11,2)` | `INTEGER` centipoints, `STRICT`, `sum()` never `total()` | Float cannot express `SumZero` exactly. |
| 6 | `raids.event_id` mandatory: one raid = one event = one flat attendee list | `raid` + `raid_tick` + `raid_tick_credit` + `raid_event_credit` | A P99 raid night is a session, not an event. |
| 7 | `raid_connected_attendance` JSON array of raid ids | `raid.attendance_group_id`, a real indexed column | Denominator dedupe must be a join key, not a blob parsed per row. |
| 8 | `__raid_attendees(raid_id, member_id)` — no PK, no unique, no weight, no timestamps | `raid_attendance` with `status`, `weight_bp`, `group_no`, `joined_at`/`left_at`, `piloted_by_person_id`, `raw_extra` | This is data EQdkp's own XSD carries and then throws away. |
| 9 | `items.item_group_key` / `adjustments.adjustment_group_key` magic strings | `item_award.item_instance_id` + `share_bp`; a mass adjustment is one batch with N entries | A batch *is* the grouping. |
| 10 | `adjustments.event_id VARCHAR(255)` holding an integer to imply a pool | `ledger_batch.pool_id` FK | Type-correct, indexable, joinable. |
| 11 | Zero FKs and three UNIQUE constraints across 47 tables | FKs everywhere with `foreign_keys=ON`; partial uniques on every natural key | Orphan attendance and duplicate names are *routine* in EQdkp data. |
| 12 | `profiledata` JSON holding class/race as ints meaningful only against a PHP module | `class_id`/`race_id`/`level` FK columns + `game_class`/`game_race`/`class_title` | "How many Clerics?" becomes an index scan, and drops a CC BY-NC-SA dependency. |
| 13 | Serialised PHP in 20+ columns; `__config` unserialised by a `strpos(':{')` heuristic | JSON only for snapshots/stats/config, validated on write, never queried into | Every queryable fact is a real column. |
| 14 | Custom profile fields as serialised blobs | `character_field_def` + `character_field_value` with typed, indexed value columns | Guilds filter on these; a blob makes that a full scan. |
| 15 | APA decay/cap/start-points rules in `data/<md5>/…/apatab.php`, outside the database | `pool.strategy_config_json` + `decay_run` with `UNIQUE(pool_id, cadence_period)` | A DB backup that silently loses every decay rule is data loss wearing a config hat. |
| 16 | Decay computed at read time by APA transforms over cached totals | decay **posted** as explicit batches | A balance is always literally a `SUM`, so "why did my points change?" is answerable. |
| 17 | ACL: tri-state Y/N/inherit, group union, per-user overrides, `po_*` per page, hardcoded superadmin group 2 | allow-only union, scoped `role_assignment`, `admin.owner` as an ordinary row, code-defined catalogue reconciled into a table | Expressive where it matters (scope), simple where it does not (three-valued logic). |
| 18 | `config.api_key`: one global token impersonating the first superadmin, accepted in a query string | service accounts + scoped, expiring, revocable, rate-limited PATs; `Bearer` only; permission ∩ scope | There is no all-powerful token, ever. |
| 19 | Seven password verifiers including bare MD5 and ext_des | one: argon2id; import forces a reset and mints claim invitations | Carrying four legacy verifiers forever is unacceptable. |
| 20 | 24 forum "bridges" reading foreign user tables directly | local + Discord OAuth2 + generic OIDC, nothing else | Bridges are the largest single source of EQdkp install fragility. |
| 21 | `__logs.log_value mediumtext` holding a rendered sentence; an `a_logs_del` permission | `audit_log` with `before_json`/`after_json`, actor kind/id/label/token/IP, `permission_used`, gapless `seq`, hash chain; no delete endpoint | A rendered string is neither queryable nor diffable. |
| 22 | No auction concept at all; every guild bolts on a bot with its own totals | `bid_session` + append-only `bid` + `bid_hold` + `bid_resolution`, server-authoritative | Closes the double-spend factory; the bot becomes a dumb terminal. |
| 23 | `rank_hide` is a property of the rank | `character.hidden` **and** `guild_rank.hidden_default` | Hiding one bank mule should not need a fake rank. |
| 24 | `items.item_name` free text + `game_itemid VARCHAR(50)`; no aliases, no fuzzy resolution | `item` / `item_alias` / `item_external_id` / `item_instance` + `reconciliation_item` | "This looted name matched nothing" is the most common P99 ingest event; it gets a queue. |
| 25 | Calendar → raid transform leaves no back-reference | `calendar_event.raid_id` | One nullable FK answers "which signup became which raid?". |
| 26 | No media table; every asset is a raw path string in six places | `media` with a content hash, plus `media_id` FKs everywhere | Makes assets referential and the import's rewriting pass verifiable. |
| 27 | `utf8_bin` collation makes `Bob` and `bob` distinct rows; no normalisation | explicit Go-computed `*_norm` columns, indexed; the import reports collisions rather than crashing | Correction C2. |
| 28 | `varchar(30)` character names, not unique-constrained | `UNIQUE(server_id, name_norm) WHERE deleted_at IS NULL` + `character_name_history` | Renames and rerolls happen; name-keyed history must survive them. |
| 29 | `__styles`: 80 cosmetic columns driving a server-side LESS compiler | a handful of CSS custom properties + a logo `media_id` | We do not have that template engine, and reproducing it imports its bugs. |
| 30 | `__article_categories.permissions` serialised ACL with unverified `-1` semantics | not imported; categories land officer-visible and the report says so | Guessing an ACL is the one thing least-privilege forbids. |

**Deliberately dropped, and named here so their absence is a decision rather than an oversight:**
`onetime_current` (a mass-adjustment batch already is this), `decay_ria` (incompatible with "a
balance is literally a `SUM`"), EQdkp-as-an-OAuth-*provider* (`plugin-eqdkp_sso` — guilds using it
to log into a WoltLab forum lose that SSO on migration), Steam/Twitch/Google/Battle.net/Facebook
login (generic OIDC covers most), mass mail, the maintenance-user support account, `format=lua`
output, print views, and every non-EQ game module. Each appears in
`docs/migration/what-does-not-migrate.md` with this reason attached.

---

## 24. Open questions

Genuinely undecided, or decided on evidence that has not been gathered. Each is a work item, not a
caveat.

1. **The permission catalogue is missing keys this schema needs.** The canonical catalogue has no
   key for log ingest, reconciliation resolution, pool/event catalogue writes, disputes, backdating
   a raid, or job administration — all of which are real operations here, and `role_permission` is
   FK-constrained, so there is no way to grant them. **Resolve before Phase 2** by adding them to
   `internal/authz/catalogue.go` (and therefore to the conventions file); do not invent a second
   list. The built-in role seed in §5.1 uses only keys that exist today.

2. **`class_title` uniqueness.** `ux_class_title_norm` asserts titles are globally unique across
   classes, so `[52 Heretic] Zibaxia` resolves without ambiguity. The Warrior 51/55 ordering
   (Champion/Myrmidon) is reported both ways in the wild. The numeric level is a separate field, so
   this is not load-bearing — but relax the index to `UNIQUE(title_norm, min_level)` the first time
   a collision is found. **(unverified)**

3. **`raid_attendance.raw_extra`.** Whether Titanium's `RaidRoster-*.txt` emits trailing columns
   (raid leader, group leader, main assist, loot rank) is unverified. Fields 0–3 are parsed and the
   remainder kept verbatim. Resolve by collecting 20 real dumps from the P99 Discord — a week-one
   task, not a Phase-4 task. **(unverified)**

4. **`server.log_token`.** The Green and Red filename tokens in `eqlog_<Char>_<token>.txt` are
   unverified; the column captures whatever is observed rather than encoding a guess. **(unverified)**

5. **Multi-buyer `share_bp`.** Modelled for EQdkp import fidelity. Whether any P99 guild splits an
   item's cost is unknown; if none do, the invariant is trivially satisfied and the column is free.
   **(empirical assumption)**

6. **Imported `__member_points` snapshots have no home** now that `balance_history` is cut. They
   feed `import_reconciliation` as an oracle and are then discarded. If guilds ask for the imported
   points-over-time chart, the answer is a query over `ledger_entry`, not a new table — verify that
   the covering index makes it fast enough before promising it.

7. **`/standings` in ≤4 statements at ≤150 ms p99 on SD-card storage, at 520k ledger entries.**
   The whole `balance_snapshot` + `attendance_rollup` design rests on this. Hand-write the four
   queries against a generated ledger **before** building any API. One afternoon.
   **(empirical assumption)**

8. **Guild scale** (280 characters, 3,400 raids, 520k ledger entries) is an estimate. Ask two large
   guilds to run three `COUNT(*)` queries. If real guilds are 5× larger, the snapshot and
   single-writer decisions need re-examining. **(empirical assumption)**

9. **Atlas must preserve hand-added triggers, partial indexes and CHECKs across `migrate diff`.**
   The append-only guarantee depends on it. One hour in Phase 0: add a trigger, change an unrelated
   column, diff. If Atlas drops it, the trigger DDL moves to a hand-authored migration and the
   preservation strategy becomes explicit. **(unverified)**

10. **A 90-second import on a single-writer database blocks raid-night writes.** Per-job locks
    serialise conflicting *jobs* and do nothing about writer starvation. The decision is: chunk
    importer commits at ≤2,000 rows with an explicit yield, add a write-latency SLO test, and refuse
    to start an import while any raid is `open`. The chunk size and fairness are still unmeasured.
    **(empirical assumption)**

11. **Contentless-FTS5 corruption.** §20 chooses external content on the strength of a reported
    failure mode rather than a reproduction. The choice costs nothing either way; the integration
    test that deletes a row and asserts the FTS row is gone is what actually protects us.
    **(unverified)**

12. **German localisation.** EQdkp Plus is German-first and a large share of its install base is
    German, so English-only at 1.0 is a parity *regression* for the incumbent's actual users. Nothing
    in this schema blocks it — `article.legacy_i18n_json` preserves imported translations — but the
    message-catalogue scaffolding must land in Phase 3 or retrofitting costs 10×.
