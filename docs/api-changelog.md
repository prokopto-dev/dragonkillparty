# API changelog

Human-readable notes on changes to the HTTP API contract (`openapi/openapi.json`), newest first.

**What belongs here.** Every change to the public API surface: a new operation, a new field, a new
status code or error code, a new parameter, a changed default. The machine record is the committed
OpenAPI document and the `oasdiff` output; this file is the prose a human reads to understand *why* a
change was made and, for a breaking change, *what a client must do*.

**Breaking changes.** Within `/api/v1` the surface is additive only (canonical §7). A genuinely
breaking change mints `/api/v2`; the rare in-`v1` breaking change the `oasdiff` gate flags needs a
`!breaking-api` label **and** an entry here — this file is CODEOWNERS-protected, which is what turns
an author-settable label into a review control (docs/design/06-cicd-and-release.md). Mark such an
entry **BREAKING** and state the migration.

**operationIds are permanent.** A generated SDK method name derives from an `operationId`, so renaming
one is breaking even when the HTTP surface is unchanged. `operationId`s are never renamed.

The format follows [Keep a Changelog](https://keepachangelog.com/); the project has no released API
version yet, so everything lands under Unreleased until the first tag.

## Unreleased

### Added

- **`GET /api/v1/guild`** (`getGuild`) — read the single guild's identity and officer-editable
  settings. Returns a strong `ETag`; PAT-callable with scope `roster:read`, or a session. Permission
  `roster.read`.
- **`PATCH /api/v1/guild`** (`updateGuild`) — update the guild under an `If-Match` precondition.
  Session-only (permission `admin.settings`); no PAT scope covers instance configuration, so this is
  not PAT-callable and, per canonical §6, is deliberately **not** marked PAT-forbidden either. A
  missing `If-Match` is `428 precondition_required`; a stale one is `412 precondition_failed` carrying
  the current representation in `meta.current` and its ETag in `meta.current_etag`, so a bot merges in
  one round trip. A successful update returns a new `ETag`.
- **`GuildDTO`** — the wire representation of the guild: `name`, `tag`, `timezone`, `week_start`,
  `points_label`, `points_precision`, `inactive_after_days` (nullable), `auto_set_inactive`,
  `hide_inactive`, `created_at`, `updated_at`.
- **`x-dkp-scopes`** — the PAT-scope OpenAPI extension is now emitted on every PAT-callable operation,
  alongside `Security` and `x-dkp-permission`. `getGuild` carries `["roster:read"]`; session-only
  operations carry none. This is a metadata addition, not a wire-surface change.

### Known gap

- `GET` and `PATCH /api/v1/guild` are currently **served without authentication**, while the spec
  declares a `security` requirement. This is a deliberate, documented Phase 0 gap — there is no auth
  middleware until Phase 2 — pinned by `TestGuild_Unauthenticated_IsAKnownPhase0Gap` and described in
  `SECURITY.md`. No client should rely on the endpoints being open; they will require a credential the
  day auth lands, which is not a breaking change because the spec has advertised the requirement all
  along.
