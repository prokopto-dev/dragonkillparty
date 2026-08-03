# Troubleshooting

**Status:** nothing is implemented, so nothing on this page has been observed in the wild. These are
the failure modes the design predicts and the diagnostics it commits to providing. The real
troubleshooting pages get written from actual alpha failures — invented symptoms are worse than none.

Run this first. It checks most of what follows and prints the fix rather than the error:

```bash
dkp doctor
docker exec dkp /dkp doctor      # in a container
```

The same output renders at `/ops` for anyone with the `ops.read` permission.

## It will not start

| Symptom | Cause | Fix |
|---|---|---|
| Exits naming an image tag | The database is newer than this binary — you rolled back | Run the tag it names. A downgrade is refused, not attempted, because an old binary writing a new schema corrupts data quietly. |
| Exits naming a failing migration and a snapshot path | A migration failed | Your database was **already restored** from the pre-migration snapshot. Report the migration name; do not retry the same version. |
| Refuses to start over `secrets.json` | The file is missing, malformed, or not mode `0600` | Fix the permissions, or restore it from a backup. It is never regenerated silently — that would invalidate every token and session and look exactly like a mass logout bug. |
| `503 setup_required` on every route | Setup was never completed | Read the bootstrap token from the logs and open `/setup`. See [First run](../getting-started/first-run.md). |
| Port already in use | Something else has `:8080` | Change `DKP_LISTEN`, or stop the other process. |
| Permission denied on the data directory | The container user is `65532:65532` | `chown` the volume, or use a named Docker volume rather than a bind mount. |

## Everyone is logged out, or nobody can log in

| Symptom | Cause | Fix |
|---|---|---|
| Every session dropped at once after a restart | The data volume changed — a new volume, a bind mount pointing somewhere else | Check the volume mapping. The session key lives in `<data-dir>/secrets.json`; a different directory means different keys. |
| Discord login redirects to an error | `DKP_BASE_URL` does not match the redirect URI registered with Discord | Make them identical, scheme and trailing slash included. `dkp doctor` compares them. |
| Password resets arrive with the wrong hostname | They will not — links are built from `DKP_BASE_URL`, never the `Host` header | Set `DKP_BASE_URL`. |
| `421 Misdirected Request` | The request's `Host` matches neither `DKP_BASE_URL` nor `DKP_EXTRA_HOSTS` | Add the hostname to `DKP_EXTRA_HOSTS`. |
| Logins take several seconds | The password-hash profile is too expensive for this hardware | `dkp doctor` measures it and names the profile to switch to. Set `DKP_ARGON2_PROFILE`. |

## The live bid board is frozen

Almost always response buffering in a reverse proxy eating the event stream.

| Check | Fix |
|---|---|
| `dkp doctor` opens a stream through your actual proxy and reports it | It names `proxy_buffering` explicitly when nginx is the cause |
| nginx | `proxy_buffering off;` and `proxy_read_timeout 3600s;` |
| Apache | `mod_proxy` with `flushpackets=on` |
| Cloudflare | Streaming works through the proxy; a Worker in the path may not |

Confirm it end to end with `curl`, which shows frames as they arrive:

```bash
curl -N "$DKP_URL/api/v1/events/stream?topics=guild" \
  -H "Authorization: Bearer $DKP_TOKEN" -H "Accept: text/event-stream"
```

A heartbeat comment arrives every 15 seconds. If you see heartbeats but no events, the stream is fine
and the problem is your subscription or your permissions — the connect frame lists which topics were
subscribed and which were denied.

## Rate limits or IPs look wrong

| Symptom | Cause | Fix |
|---|---|---|
| Every client appears to come from one address | Behind a proxy with `DKP_TRUSTED_PROXIES` unset | Set it to the proxy's CIDR. While it is empty, forwarded headers are ignored entirely — deliberately, because a trusted-by-default forwarded header lets anyone spoof past your limits. |
| Rate limits trip for everyone at once | Same cause | Same fix. |
| Links are `http://` behind TLS termination | `X-Forwarded-Proto` is being ignored | Same fix. |

## The import did not do what I expected

| Symptom | Cause | Fix |
|---|---|---|
| Nothing was written | Dry run is the default | Re-run with `--commit`. Commit is session plus step-up; no token can do it. |
| Attendance percentages differ from the old site by a few points, for everyone | The `no_attendance` mapping did not come across | Check the event-to-pool mappings. This is the classic symptom. See [Attendance and windows](../guides/attendance-and-windows.md). |
| The importer cannot reach MySQL | Outbound requests to private addresses are blocked by default | Set `DKP_IMPORT_ALLOW_PRIVATE=true`. It is a deliberate opt-in, not an oversight. |
| Balances are right but history is missing | You chose the opening-balance strategy | That is what it does. See [Migrating from EQdkp Plus](../migration/from-eqdkp.md). |
| Members cannot log in after the import | Passwords are never migrated | Distribute the one-time claim codes the importer printed. |

## Numbers look wrong

| Symptom | What it means | Do |
|---|---|---|
| A member's balance disagrees with their statement | The cached balance drifted from the log | Run `dkp verify-ledger`. This is a **bug in this software**, not data loss: the log is intact and the cache is derived. `--rebuild` recomputes it. Please report the drift. |
| A tick credited nobody | The dump parsed but every name was unresolved | Check the reconciliation queue. Nothing is dropped. |
| A tick credited people twice | Two officers uploaded dumps whose sequence and contents both differed | Void the duplicates. Ticks void; they never delete. |
| Attendance dropped with no missed raids | The rolling window moved | Compare `as_of_day` between the two figures. |
| A balance ends in a row of nines | It cannot | There are no floats anywhere in the arithmetic. If you see this, it is a display bug — report it. |

## Backups and disk

| Symptom | Cause | Fix |
|---|---|---|
| Disk filling up | Artifacts are retained 180 days; backups keep 14 daily and 8 weekly | Adjust `DKP_BACKUP_RETENTION`, or opt out of artifact retention — but read [the retention rationale](../guides/running-a-raid-night.md#upload) first, because artifacts are the dispute evidence. |
| `dkp.db-wal` is enormous | A long-running read is blocking a checkpoint | `dkp doctor` flags WAL size. A restart checkpoints it. |
| A restore fails on a file you copied with `cp` | You copied a live database and got a torn file | Use `dkp backup`. |
| No backups have run | `DKP_BACKUP_SCHEDULE=off`, or the job queue is stuck | `dkp doctor` reports backup freshness and worker heartbeat. |

## A bot stopped working

| Symptom | Cause | Fix |
|---|---|---|
| `401` on every request | Token revoked, expired, or the pepper was rotated | Mint a new one. Expiry warnings fire at 30, 7 and 1 days — see [auth and scopes](../api/auth-and-scopes.md#rotation-and-revocation). |
| `401 token_in_query_string` | The token is in the URL | Move it to `Authorization: Bearer`. Only the compat shim accepts a query-string token. |
| `403` on something that used to work | A role changed, or the token's scopes do not cover it | `GET /me` shows `effective_permissions`, which is role permissions ∩ token scopes. That intersection is your answer. |
| `409` on a retry | The retry is being deduplicated correctly | Reuse the same `Idempotency-Key` for a retry and you get the original response back, not a conflict. |
| `412` on a state transition | Something changed since you read it | Refetch, check `meta.current` in the response body, and retry with the new `ETag`. |
| The bot's owner left the guild | Nothing — tokens belong to service accounts | The token is flagged orphaned and admins are notified. It keeps working. |

## Getting help

Before opening an issue:

```bash
dkp support-bundle
```

Collects the configuration with secrets redacted, `dkp doctor` output, recent logs, migration state,
the last ledger verification result and version information, into one file you can attach.

| Where | For |
|---|---|
| GitHub Issues | Bugs, with a support bundle |
| GitHub Discussions | Questions. There is no SLA — this is volunteer-maintained. |
| [SECURITY.md](../../SECURITY.md) | Vulnerabilities. Report privately, never in an issue. |

**Redact character names and tells from any log you attach.** The in-app "report a parser bug" button
does this for you. A DKP tool sits on top of the most drama-prone data in a guild.

## Next

- [Configuration](configuration.md) · [Upgrade and backup](upgrade-and-backup.md)
- [The ledger](../concepts/ledger.md) — for "the number is wrong" arguments
