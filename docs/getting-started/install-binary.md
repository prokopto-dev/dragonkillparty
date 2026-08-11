# Install a binary

**Status:** no binaries are published yet. This page is the specification the release must satisfy.
Binary and package publication lands in Phase 9.

A single executable with no runtime dependencies. Use this if you run your guild's site on the same
Windows PC you raid on, or if you would rather not learn Docker. It is a supported deployment, not a
fallback.

## Download

| Platform | Asset |
|---|---|
| Windows | `dkp_<version>_windows_amd64.zip` |
| macOS (Apple silicon) | `dkp_<version>_darwin_arm64.tar.gz` |
| macOS (Intel) | `dkp_<version>_darwin_amd64.tar.gz` |
| Linux | `dkp_<version>_linux_amd64.tar.gz`, `..._arm64`, `..._armv7` |
| Debian/Ubuntu | `dkp_<version>_amd64.deb` — installs a systemd unit |
| RHEL/Fedora | `dkp_<version>_amd64.rpm` — installs a systemd unit |
| macOS via Homebrew | `brew install dragonkillparty/tap/dkp` |

Every asset is listed in `checksums.txt`, which is signed. Verify before you run it:

```bash
sha256sum -c checksums.txt --ignore-missing
cosign verify-blob checksums.txt \
  --signature checksums.txt.sig --certificate checksums.txt.pem \
  --certificate-identity-regexp '^https://github\.com/prokopto-dev/dragonkillparty/\.github/workflows/release\.yml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

## Run it

```bash
./dkp serve
```

With no arguments and no configuration, `dkp serve` creates `./dkp-data/`, generates its own secrets,
applies its migrations, prints a one-time setup URL, and listens on `:8080`. On Windows,
double-clicking `dkp.exe` does the same thing and leaves a console window open showing the URL.

Continue at [First run](first-run.md).

## Choose where the data lives

```bash
./dkp serve --data-dir /srv/dkp-data      # or DKP_DATA_DIR=/srv/dkp-data
```

The data directory holds everything that matters:

```
dkp-data/
├── dkp.db                  the guild — database, ledger, roster, everything
├── dkp.db-wal              write-ahead log; part of the database, do not delete
├── secrets.json  (0600)    session key, PAT pepper, webhook signing key
├── first-run-token.txt     one-time bootstrap token, deleted after setup
├── artifacts/              uploaded RaidRoster dumps and log slices
└── backups/                nightly snapshots
```

**Back up the whole directory, not just `dkp.db`.** Copying `dkp.db` while the process is running
gives you a torn database; use `dkp backup` instead, which is safe against a live instance.

If `secrets.json` is missing or malformed the instance refuses to start rather than regenerating it.
Silent regeneration would invalidate every token and every session, which looks exactly like a mass
logout bug and is much harder to diagnose than a clear refusal.

## Run it as a service

### systemd

The `.deb` and `.rpm` packages install this for you. Manually:

```ini
# /etc/systemd/system/dkp.service
[Unit]
Description=Dragon Kill Party
After=network-online.target

[Service]
Type=simple
User=dkp
Environment=DKP_DATA_DIR=/var/lib/dkp
Environment=DKP_BASE_URL=https://dkp.example.org
ExecStart=/usr/local/bin/dkp serve
Restart=on-failure
RestartSec=5s
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
ReadWritePaths=/var/lib/dkp

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl enable --now dkp
sudo journalctl -u dkp -f
```

### Windows

Run it under a service supervisor such as NSSM, or create a Scheduled Task with trigger *At startup*
and *Run whether user is logged on or not*. A `dkp.exe` started from Explorer stops when the user logs
out, which is exactly what happens when the raid PC is rebooted after a patch.

Windows Defender may quarantine an unsigned executable on first run. The release is signed; if
SmartScreen still objects, verify the checksum against `checksums.txt` before you allow it.

## Ports and TLS

The binary serves plain HTTP on `DKP_LISTEN` (default `:8080`). **Terminate TLS in front of it**
with a reverse proxy — Caddy, nginx or a Cloudflare Tunnel. See
[Install with Docker](install-docker.md#behind-a-reverse-proxy) for the configuration; it is
identical here.

There is no built-in ACME. It needs ports 80 and 443, breaks behind Cloudflare's proxy and behind
any existing reverse proxy, and can get you rate-limited by Let's Encrypt if the process
restart-loops — so it is on the roadmap's deliberately-deferred list rather than shipped.
`dkp doctor` checks the certificate your proxy actually presents and warns before it expires.

## Upgrading

Stop the service, replace the executable, start it again. The instance snapshots the database before
migrating and auto-restores that snapshot if a migration fails. It refuses to start against a database
newer than itself, naming the version you need. Read
[Upgrade and backup](../operations/upgrade-and-backup.md) before your first upgrade.

## Next

- [First run](first-run.md) — the setup wizard
- [Configuration](../operations/configuration.md) — every `DKP_*` variable
- [Troubleshooting](../operations/troubleshooting.md) — when the above does not happen
