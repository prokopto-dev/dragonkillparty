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

### Changed

- **Authentication is now enforced** (Phase 2 Wave 0d). Every operation that declares `Security` —
  today `getGuild` and `updateGuild` — requires a live credential: a `__Host-dkp_session` cookie or
  `Authorization: Bearer dkp_pat_…`. Without one the answer is `401` with `code:
  "unauthenticated"` and a `WWW-Authenticate: Bearer` header. `getMeta` declares an explicitly empty
  `security` and stays public.

  **This is not a breaking change**, because the spec has advertised the requirement since PR 5a and
  the previous behaviour was the documented gap below. A client that was relying on the endpoints
  being open was relying on something the contract never promised.

  Three refusals a bot author will meet, all `401`, each with its own `code` from the published
  catalogue (`docs/api/errors.md`): `token_expired` (with `meta.expired_at` and `meta.token_prefix`)
  means mint a new one; `token_revoked` (same meta) means stop — retrying a revoked token looks like
  an attack; `token_invalid` means check what was pasted. A token sent as a query parameter is
  `token_in_query_string`, refused rather than honoured, because a token in a URL is a token in three
  logs.

### Known gap

- **Authorization is not enforced yet.** Identity is resolved; capability is not. No permission is
  checked, token scopes are not intersected with the service account's role, and the capability floor
  is documented rather than enforced — so **any live credential passes every operation** until Wave
  0e lands `authz.Check`. The `pat` and `session` security schemes say so in their descriptions, and
  `SECURITY.md` carries the detail. Nothing in this window should be relied on as scope-limited.
- **There is no endpoint that issues a credential.** Login and the first-run bootstrap (issue #264)
  come next; until then a fresh instance answers `401` to every operation that needs one.
