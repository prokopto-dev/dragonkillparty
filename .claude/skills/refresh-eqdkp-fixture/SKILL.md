---
name: refresh-eqdkp-fixture
description: Build or refresh an EQdkp Plus fixture database by running the real PHP installer in Docker and publishing the result as a public OCI artifact. Use when adding a new EQdkp version to the importer matrix, when a fixture is stale, or when a donated real-guild dump must be anonymised into a fixture.
argument-hint: "[eqdkp-version | hostile | donated-<guild>]"
allowed-tools: Read, Grep, Glob, Edit, Write, Bash(docker *), Bash(make test-importer), Bash(make check)
---

# Refresh an EQdkp Plus fixture

Fixtures are built by running EQdkp's **actual PHP installer** in Docker and capturing the resulting
MariaDB data directory. That is what makes them real: an aborted EQdkp update produces a schema whose
version string lies, and only a real installer reproduces that.

The importer matrix is EQdkp Plus **2.0.5 / 2.1.5 / 2.2.27 / 2.3.39**, plus a hand-crafted
**hostile** fixture, plus an anonymised donated dump.

**This lane has a long lead time and zero coupling — it blocks Phase 5 and can start on day one.**

---

## Steps

### 1. Decide which fixture

| Fixture | Purpose |
|---|---|
| A version fixture (2.0.5 / 2.1.5 / 2.2.27 / 2.3.39) | Column-existence coverage across the versions guilds actually run |
| `hostile` | latin1 double-encoding, duplicate attendee rows, orphaned items, a `member_main_id` cycle, a bare MD5 password, an unknown plugin table, a half-applied 2.3.x migration |
| A donated dump | The only fixture with real-world shape. Requires anonymisation and `PROVENANCE.md`. |

### 2. Licence firewall

You are **running** their installer, not copying it. That is the whole point of doing it this way.

- Do not transcribe their DDL into our repo.
- Do not copy their language strings, icons, or seed data.
- The identifiers `pdh_`, `gen_class`, `plus_exchange`, `__multidkp2event` may appear only in
  `internal/importer/legacy_names.go` and `internal/api/compat/`. CI greps for them elsewhere.

### 3. Write the build inputs, and keep them small

```
test/fixtures/eqdkp/<version>/
├── Dockerfile          # PHP + MariaDB + the EQdkp release tarball, pinned by checksum
├── install.sh          # drives the real web installer non-interactively
├── seed.sql            # OUR synthetic guild data — members, raids, items, adjustments
└── README.md           # version, install date, what this fixture is for
```

These live in git because they are small, reviewable and diffable. **The MariaDB data directory does
not.** No git-lfs: its bandwidth quota is charged to the repo owner and bites forks hardest —
precisely the contributors you want.

### 4. Never assume the table prefix

Seed with a **non-default prefix** in at least one fixture. `eqdkp23_` is a default, not a guarantee,
and `fingerprint.go` discovers it. A fixture matrix that only ever uses the default prefix cannot
catch a discovery bug.

Likewise, seed at least one fixture whose `config` version string disagrees with its actual columns —
capability detection is by **column existence**, never by version string.

### 5. Seed synthetic data only

Members, characters, raids, items, adjustments, alt groups, twink-mode rows — all invented.

**No real member PII in a version fixture.** Names, emails and Discord ids are generated. The
canonical values are the ones the support-bundle canary test already knows: `CANARY-GUILD`,
`Canaryname`, `Canary Blade`, `canary.example`.

Seed enough shape to exercise the classifier: a decay history, a points cap, start points, at least
one orphan row, at least one unattributed adjustment, and at least one float-rounding residue.

### 6. Build and publish as a public OCI artifact

```bash
gh workflow run fixtures.yml -f version=2.3.39
# publishes ghcr.io/dragonkillparty/dkp-fixtures:eqdkp-2.3.39
```

The package must be **public** so anonymous pulls work from fork PRs. Without that, `test / importer`
is red for every external contributor — unacceptable, since the migration story is the product's
main adoption argument.

Nobody rebuilds a PHP installer locally. If you find yourself doing that in the inner loop, the
artifact is missing or the pin is wrong.

### 7. Pin the digest

Pin the fixture by **digest**, not by tag, in the importer test harness. A moving fixture tag turns
an importer regression into an unreproducible mystery.

Record in the fixture's `README.md`: EQdkp version, installer date, prefix used, digest, and which
capability quirks it deliberately carries.

### 8. Anonymise a donated dump, deterministically

Only for a donated real-guild dump:

- Run the **checked-in** anonymiser — deterministic, so re-running produces identical output and the
  diff is reviewable.
- Write `PROVENANCE.md`: who donated it, what permission was given, what was replaced, what was kept.
- Verify: grep the anonymised output for every canary and for the original guild name. Any hit is a
  build failure, not a warning.

### 9. Run the importer suite

```bash
make test-importer   # needs Docker, ~120 s
```

Then check all four:

- [ ] All fixtures pass invariants **I1–I11**.
- [ ] The **classifier oracle** holds exactly: the set of members with `Δ ≠ 0` equals the predicted
      union of `apa_decay`, `apa_cap`, `apa_start_points`, `orphan_rows`, `stale_cache`,
      `unattributed_adjustment`, `float_rounding`, `twink_mode_mismatch`. **Exact, not a tolerance.**
- [ ] Double commit produces an unchanged head hash; crash-resume is byte-identical.
- [ ] Performance budget: 500k rows ≤ 90 s at ≤ 512 MB.

### 10. Golden reports and the ratchet

Each fixture has one committed golden dry-run report — the contract with the officer. They are
CODEOWNERS-protected and `-update` is refused when `CI=true`.

Adding a fixture legitimately changes the golden set. **Regenerating a golden to make an existing
fixture pass does not.** Say which you did, in the PR body. The fixture-count test asserts the count
is non-decreasing.

---

## Stop and ask if

- **The donated dump cannot be anonymised deterministically**, or the donor's permission is unclear.
- **A fixture would need real member emails or password hashes.** Passwords are never imported and
  never fixtured — usernames and emails only, and emails are synthetic in fixtures.
- **The installer will not run non-interactively** for a version. Ask before hand-crafting the schema
  — a hand-written schema is exactly the transcription the licence firewall forbids, and it also
  defeats the purpose of building from the real installer.
- **An existing fixture's golden report changes and you cannot explain why in one sentence.** That is
  an importer regression wearing a fixture-refresh costume.
