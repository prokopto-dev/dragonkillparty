# Install with Docker

**Status:** no image is published yet. This page is the specification the container must satisfy; the
commands below are what will work at 1.0, not a transcript of a working system. Image publication
lands in Phase 9.

Docker is the recommended install for anything that is not a desktop PC. If you want to double-click
an executable instead, read [Install a binary](install-binary.md).

## One command

```bash
docker run -d \
  --name dkp \
  -p 8080:8080 \
  -v dkp-data:/data \
  --restart unless-stopped \
  ghcr.io/dragonkillparty/dkp:1
```

Then read the bootstrap token out of the logs and open the URL it prints:

```bash
docker logs dkp
```

Continue at [First run](first-run.md).

## What that command commits you to

| Choice | Consequence |
|---|---|
| `:1` | You get every non-breaking release automatically on `docker pull`. Pin `:1.4` or `:1.4.2` if you want to move deliberately. |
| `-v dkp-data:/data` | The named volume holds the database, backups, uploaded raid dumps and the generated secrets. **Losing it loses the guild.** |
| `-p 8080:8080` | The instance is reachable on port 8080 in plain HTTP. Put a reverse proxy in front before exposing it to the internet. |
| `--restart unless-stopped` | The container comes back after a host reboot. Without it, your DKP site disappears the next time the raid PC is rebooted. |

There is no database to provision, no cron entry, no Redis, no PHP, and no reverse proxy needed for a
LAN-only install. One process, one container, one volume, one port.

## Compose

```yaml
services:
  dkp:
    image: ghcr.io/dragonkillparty/dkp:1
    container_name: dkp
    restart: unless-stopped
    ports:
      - "8080:8080"
    volumes:
      - dkp-data:/data
    environment:
      DKP_BASE_URL: https://dkp.example.org
      DKP_LOG_LEVEL: info

volumes:
  dkp-data:
```

Set `DKP_BASE_URL` to the URL members actually type. Every emailed link, every OAuth redirect and
every webhook `User-Agent` is built from it, never from the incoming `Host` header — a request whose
`Host` does not match is rejected with `421 Misdirected Request`. Getting it wrong breaks Discord
login and password resets, and nothing else, which makes it hard to notice.

Full setting list: [Configuration](../operations/configuration.md).

## Behind a reverse proxy

The container speaks plain HTTP. Terminate TLS in front of it. Caddy is the shortest correct config:

```caddyfile
dkp.example.org {
    reverse_proxy localhost:8080
}
```

For nginx, disable response buffering or the live bid board and the raid feed will appear frozen:

```nginx
location / {
    proxy_pass         http://127.0.0.1:8080;
    proxy_http_version 1.1;
    proxy_set_header   Host              $host;
    proxy_set_header   X-Forwarded-For   $proxy_add_x_forwarded_for;
    proxy_set_header   X-Forwarded-Proto $scheme;
    proxy_buffering    off;              # required for Server-Sent Events
    proxy_read_timeout 3600s;
}
```

Then tell the instance which proxy to believe:

```
DKP_TRUSTED_PROXIES=127.0.0.1/32
```

`DKP_TRUSTED_PROXIES` is **empty by default**, and while it is empty `X-Forwarded-For` and
`X-Forwarded-Proto` are ignored entirely. That is deliberate: a trusted-by-default forwarded header
lets anyone spoof their client IP past your rate limits.

`dkp doctor` tests the whole path end to end — it opens an SSE stream through your actual proxy and
names `proxy_buffering` explicitly if the frames do not arrive.

## Health checks

| Endpoint | Touches the database | Use it for |
|---|---|---|
| `/healthz` | No | The container `HEALTHCHECK` and process supervision |
| `/readyz` | Yes — database reachable, migrations at the expected version, the ledger's append-only protection intact, worker heartbeat fresh | Load balancers and deploy gates |

The image's `HEALTHCHECK` calls `/healthz`, not `/readyz` and not `dkp doctor`. A health check that
touches the database lets Docker kill the container part-way through a migration, which is the one
failure this design refuses to allow.

## Image facts

| | |
|---|---|
| Base | `FROM scratch` — no shell, no package manager, no base-image CVE surface |
| Size | ~25 MB |
| User | `65532:65532`, non-root |
| Platforms | `linux/amd64`, `linux/arm64`, `linux/arm/v7` |
| Registry | GHCR. Not Docker Hub, whose pull limits will bite you on a raid night. |
| Signing | Cosign keyless signatures, SBOM and provenance attached to every tag |
| Debug variant | `:1-debug` on Alpine, for when you need to `exec` into it |

Verify a tag before you run it:

```bash
cosign verify ghcr.io/dragonkillparty/dkp:1 \
  --certificate-identity-regexp 'https://github.com/dragonkillparty/.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

## Upgrading

```bash
docker pull ghcr.io/dragonkillparty/dkp:1
docker restart dkp
```

The instance snapshots the database before it applies any migration, and restores that snapshot
automatically if a migration fails. Read [Upgrade and backup](../operations/upgrade-and-backup.md)
once before your first upgrade — it is short, and it is the page that saves ten years of DKP.

## Next

- [First run](first-run.md) — the setup wizard
- [Configuration](../operations/configuration.md) — every `DKP_*` variable
- [Troubleshooting](../operations/troubleshooting.md) — when the above does not happen
