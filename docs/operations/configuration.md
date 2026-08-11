<!--
  MAINTAINER NOTE — this page is REPLACED by a generated one.

  From Phase 2, `make gen` produces `docs/reference/configuration.md` from the struct tags on
  `internal/config.Config`, and CI fails if the regenerated file differs from the committed one
  (the same gate that guards `openapi/openapi.json`). At that point this page becomes a stub that
  links there. Until then it is hand-maintained and is the only configuration reference.

  The structure is deliberately generator-friendly. Preserve it:

    * One H2 per config group; the H2 text is the group's struct-tag `group:"..."` value.
    * One table per group, always the same four columns, in this order:
        | Variable | Default | Secret | What it does |
    * `Variable` is exactly the env var name in backticks — no prose, no alternate spellings.
    * `Default` is a backticked literal, or the word "none". Never a sentence.
    * `Secret` is "yes" or "no", from whether the field's type is `config.Secret`.
    * `What it does` is one sentence, from the field's `doc:"..."` tag. No line breaks.
    * Prose that is not a row lives under an H3 *below* the table, never between rows.
    * Do not add a column. A generator that has to reconcile per-page columns is a generator
      nobody runs.
-->

# Configuration

**Status:** nothing reads these yet. The typed configuration lands in Phase 2 and this page becomes
generated from the `Config` struct at that point. Treat the defaults below as the specification.

Every setting is an environment variable prefixed `DKP_`. **The defaults boot a working instance with
zero configuration** — you only set something when you want to change it.

Settings can also live in `<data-dir>/dkp.env` with mode `0600`. Environment variables win over the
file.

## Secrets

Any variable marked **Secret: yes** also accepts a `_FILE` suffix pointing at a file containing the
value:

```
DKP_DISCORD_CLIENT_SECRET_FILE=/run/secrets/discord_client_secret
```

Use it with Docker, Podman or Kubernetes secrets. A value in the environment leaks into
`docker inspect`, crash dumps and child processes; a value in a file does not.

Secret-valued settings are a *type*, not a convention: they render as `***` in logs, in `/ops`, in
`dkp doctor --json`, in `GET /api/v1/admin/settings` and in any serialisation of the configuration.
A test marshals the whole configuration and asserts no known secret value appears in the output.

**`***` means set; `null` means unset.** The distinction is load-bearing and is part of the type, not
a per-caller choice: an operator has to be able to answer "is Discord login configured?" without
being told the secret, and a settings screen cannot render "not configured" from `***` alone. The two
render identically in logs, where the question never arises, and differently in any structured
output, where it always does.

```json
{ "discord_client_id":     "1234567890",
  "discord_client_secret": "***",
  "oidc_issuer":           null,
  "oidc_client_secret":    null }
```

Redaction hides the value, not the shape — which is why `GET /api/v1/admin/settings` is gated at
`admin.security.manage` rather than `admin.settings` even though it returns no secrets. The response
still says which identity provider is configured, whether MFA is enforced, and what
`DKP_OUTBOUND_ALLOW_CIDRS` contains; that last one is not a secret and names a reachable internal
range, which is reconnaissance. See [`../design/02-api-design.md`](../design/02-api-design.md) §4.2.

The session key, the token pepper and the webhook signing key are **not** configuration. They are
generated on first boot and persisted to `<data-dir>/secrets.json`. There is never a default
password.

## Core

| Variable | Default | Secret | What it does |
|---|---|---|---|
| `DKP_DATA_DIR` | `/data` | no | Directory holding the database, backups, artifacts and generated secrets. When unset, the directory containing `DKP_DB_PATH` is used — which is `/data` in the container and the right answer for a binary install elsewhere. |
| `DKP_LISTEN` | `:8080` | no | Address the HTTP server binds. |
| `DKP_BASE_URL` | none | no | The public URL members type; every generated link, OAuth redirect and webhook user-agent is built from it. |
| `DKP_EXTRA_HOSTS` | none | no | Additional `Host` values accepted alongside `DKP_BASE_URL`, comma-separated. |
| `DKP_ENV` | `production` | no | Environment name; anything other than `production` enables response-validation middleware. |
| `DKP_WEB_DIR` | none | no | Serve the web assets from this directory instead of the copy embedded in the binary. |

### Why `DKP_BASE_URL` matters more than it looks

Links in emails, the Discord OAuth `redirect_uri` and password-reset URLs are built from this value
and **never** from the incoming `Host` header. A request whose `Host` matches neither `DKP_BASE_URL`
nor `DKP_EXTRA_HOSTS` is rejected with `421 Misdirected Request`, which closes host-header poisoning
of reset links.

Setting it wrong breaks Discord login and password resets and nothing else, which makes it hard to
notice until someone needs one.

## Database and migrations

| Variable | Default | Secret | What it does |
|---|---|---|---|
| `DKP_AUTO_MIGRATE` | `true` | no | Apply pending migrations at boot, after taking a snapshot. |
| `DKP_DATABASE_URL` | none | yes | Reserved for the post-1.0 Postgres target. SQLite is the only supported engine in 1.0. |

### Turning auto-migrate off

With `DKP_AUTO_MIGRATE=false`, an instance with pending migrations serves `503` from `/readyz` with
the exact command to run, and the web UI shows a banner containing that command. Use it if you want a
maintenance window; leave it on otherwise. The migration path snapshots first and auto-restores on
failure — see [Upgrade and backup](upgrade-and-backup.md).

## Health and readiness

| Variable | Default | Secret | What it does |
|---|---|---|---|
| `DKP_READYZ_DETAIL` | `never` | no | Who may see the `detail` field of a `/readyz` response: `never`, `local` or `always`. |

`/readyz` always reports `check` and `state`, to everyone — your monitoring has to be able to see that
something is wrong. `detail` is the actionable string that goes with it ("missing append-only
triggers: trg_ledger_entry_no_update"), and it is withheld from every caller until you say otherwise.

That default is deliberate and it is not paranoia about the string itself: the recommended deployment
is this binary behind a reverse proxy on the same host, and there *every* request arrives from
`127.0.0.1`. Nothing in the process can tell your laptop from the public internet, so it does not
guess.

| Value | Who sees `detail` | Use it when |
|---|---|---|
| `never` | nobody | The default. Read the fault out of the logs instead — the boot path logs the same thing at error level. |
| `local` | a loopback or RFC-1918/RFC-4193 peer, **and** only when nothing in the request claims to be relaying somebody (`Forwarded`, any `X-Forwarded-*`, `X-Real-IP`, `CF-Connecting-IP`, `True-Client-IP`) | The binary is exposed directly — `docker run -p 8080:8080`, no proxy. If a proxy *is* in front and strips its own headers, this discloses through it. |
| `always` | every caller | `/readyz` is not reachable from the internet — a private network, or a proxy that does not expose it — and you want the string from your monitoring. |

An unrecognised value logs a warning and behaves as `never`; it does not stop the server. The
migrations-pending body `{"check":"migrations","state":"pending","command":"dkp migrate"}` is public
under every value, because the web UI renders that command as an upgrade banner for an operator who
may have no shell access at that moment.

## Jobs

| Variable | Default | Secret | What it does |
|---|---|---|---|
| `DKP_WORKER_CONCURRENCY` | `4` | no | Maximum background jobs running at once. |

## Backups

| Variable | Default | Secret | What it does |
|---|---|---|---|
| `DKP_BACKUP_SCHEDULE` | `daily` | no | Cadence for automatic backups; `off` disables them. |
| `DKP_BACKUP_RETENTION` | `14d,8w` | no | How many daily and weekly snapshots to keep. |
| `DKP_S3_BACKUP_URL` | none | yes | S3-compatible destination for off-box copies. |

Backups are on by default and land in `<data-dir>/backups/`. Backups on the same disk protect you
from mistakes, not from disk failure — set `DKP_S3_BACKUP_URL` on day one.

## Authentication

| Variable | Default | Secret | What it does |
|---|---|---|---|
| `DKP_DISCORD_CLIENT_ID` | none | no | Discord OAuth application ID. |
| `DKP_DISCORD_CLIENT_SECRET` | none | yes | Discord OAuth application secret. |
| `DKP_OIDC_ISSUER` | none | no | Generic OIDC issuer URL. |
| `DKP_OIDC_CLIENT_ID` | none | no | Generic OIDC client ID. |
| `DKP_OIDC_CLIENT_SECRET` | none | yes | Generic OIDC client secret. |
| `DKP_ARGON2_PROFILE` | `default` | no | Password-hashing cost profile for constrained hardware. |
| `DKP_HIBP_CHECK` | `false` | no | Check new passwords against the Have I Been Pwned range API, sending only a five-character hash prefix. |

`dkp doctor` measures actual password-hash wall time and warns outside 50–500 ms, naming the profile
to switch to — for example "set `DKP_ARGON2_PROFILE=low`; your hardware takes 1.4 s per login".

## Mail

| Variable | Default | Secret | What it does |
|---|---|---|---|
| `DKP_SMTP_URL` | none | yes | SMTP connection URL; without it, email is disabled and claim codes are the account-recovery path. |

Email is optional throughout. Nothing in the product requires working SMTP.

## Logging

| Variable | Default | Secret | What it does |
|---|---|---|---|
| `DKP_LOG_LEVEL` | `info` | no | One of `debug`, `info`, `warn`, `error`. |
| `DKP_LOG_FORMAT` | `auto` | no | `json`, `text`, or `auto` which picks text when standard output is a terminal. |

Request logs carry a `request_id` that is echoed in the `X-Request-Id` response header and in every
error body, so a member's screenshot is a grep key. Routes are logged as OpenAPI path templates, never
as raw paths, which keeps identifiers out of the logs and cardinality bounded.

## Metrics

| Variable | Default | Secret | What it does |
|---|---|---|---|
| `DKP_METRICS_ENABLED` | `false` | no | Expose Prometheus metrics. |
| `DKP_METRICS_LISTEN` | `127.0.0.1:9090` | no | Separate listener for metrics when enabled. |
| `DKP_METRICS_TOKEN` | none | yes | Bearer token required on the metrics endpoint. |

Metrics are off by default and are never gated by an API token scope. An unauthenticated metrics
endpoint on a home box with a forwarded port leaks member counts, route inventory, raid cadence and
version — and fewer than one instance in twenty will ever be scraped, so the default is the one that
cannot hurt the other nineteen.

## Outbound network policy

| Variable | Default | Secret | What it does |
|---|---|---|---|
| `DKP_OUTBOUND_ALLOW_PRIVATE` | `false` | no | Allow webhooks and other outbound requests to reach private address ranges. |
| `DKP_OUTBOUND_ALLOW_CIDRS` | none | no | Explicit CIDR allowlist for outbound requests, comma-separated. |
| `DKP_IMPORT_ALLOW_PRIVATE` | `false` | no | Allow the EQdkp importer to connect to a MySQL server on a private address. |

`DKP_IMPORT_ALLOW_PRIVATE=true` is the common case for a LAN MySQL box, which is why it is a
documented deliberate opt-in rather than a silent default. Changing any of these requires
`admin.settings` plus step-up, is audited, and cannot be done by a token — so a leaked token cannot
relax the policy and then pivot.

## Reverse proxy

| Variable | Default | Secret | What it does |
|---|---|---|---|
| `DKP_TRUSTED_PROXIES` | none | no | CIDRs whose forwarded headers are believed. |

While `DKP_TRUSTED_PROXIES` is empty, `X-Forwarded-For`, `X-Forwarded-Proto`, `X-Real-IP` and
`Forwarded` are **ignored entirely** and the client IP is the socket's remote address. When it is
configured, the forwarded chain is walked right to left, popping trusted entries; the first untrusted
entry is the client. The leftmost entry is never taken, because it is attacker-controlled.

**There is no in-process ACME setting.** `autocert` is on the roadmap's deliberately-deferred list:
it needs ports 80 and 443, has four common failure modes behind Cloudflare or an existing reverse
proxy, and generates support tickets exclusively from the small minority who enable it. The binary
speaks plain HTTP and a reverse proxy terminates TLS — see
[Install with Docker](../getting-started/install-docker.md#behind-a-reverse-proxy).

## Compatibility and updates

| Variable | Default | Secret | What it does |
|---|---|---|---|
| `DKP_COMPAT_ENABLED` | `true` | no | Serve the EQdkp `api.php` compatibility shim for legacy bots. |
| `DKP_UPDATE_CHECK` | `true` | no | Fetch a static JSON once a day to show an available-update banner. |
| `DKP_TELEMETRY` | `off` | no | Reserved. No telemetry is collected or sent, in any release. |

The update check is a plain `GET` of a static file with no body, no query parameters and no
identifiers beyond a `dkp/<version> (<os>/<arch>)` user agent. The instance never updates itself.

Turn `DKP_COMPAT_ENABLED` off once your last legacy bot has migrated. The admin UI counts days since
the shim was first used and names the token prefixes still calling it, so you know which bot to chase.

## Testing

| Variable | Default | Secret | What it does |
|---|---|---|---|
| `DKP_TEST_CLOCK` | none | no | Pin the injected clock to a fixed time. Rejected when `DKP_ENV=production`. |

## Checking what is in effect

```bash
dkp doctor
```

`dkp doctor` prints the effective configuration with secrets redacted, and validates it: unreachable
SMTP, a Discord redirect URI that does not match `DKP_BASE_URL`, TLS expiry on the certificate your
proxy presents, forwarded headers arriving from an untrusted source. Each failure prints the fix
rather than the error. The same output renders at `/ops`.

Validation errors at boot name the fix, not the field.

## Next

- [Upgrade and backup](upgrade-and-backup.md)
- [Troubleshooting](troubleshooting.md)
- [Install with Docker](../getting-started/install-docker.md) · [Install a binary](../getting-started/install-binary.md)
