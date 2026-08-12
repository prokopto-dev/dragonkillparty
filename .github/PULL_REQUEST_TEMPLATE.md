<!-- Keep this template. Delete the guidance comments, not the checkboxes. -->

## What and why

<!-- One paragraph. What changes, and what problem it solves. Link the issue: Closes #123 -->

## How to verify

<!-- The commands a reviewer runs, or the UI path they click. If a reviewer cannot check it in two
     minutes, say what you did instead. -->

```
make check
```

## Checklist

- [ ] **Licence firewall.** I have not copied code, DDL, language strings or assets from EQdkp
      Plus. Reading a user's database at runtime is fine; transcribing their PHP, their schema text,
      their `lang/` files or their icons is not — EQdkp Plus core is AGPL-3.0, its game modules are
      CC BY-NC-SA (non-commercial), and this project is Apache-2.0.
      <sub>Enforced by `lint / repo`, which greps for `pdh_`, `gen_class`, `plus_exchange` and
      `__multidkp2event` outside `internal/importer/legacy_names.go` and `internal/api/compat/`.
      The temptation peaks when the task is literally "match EQdkp's behaviour".</sub>

- [ ] **Test integrity.** I have not weakened, skipped or regenerated any existing test or golden
      file to make CI pass. No deleted assertion, no new `cmpopts.Ignore*`, no `Equal` loosened to
      `Contains`, no shrunk table-test case list, no raised coverage or size budget, nothing
      rewritten under `test/golden/` or `test/fixtures/`, no workflow edited to make a job green.
      <sub>If a test is genuinely wrong: say so below, say why, and put that change in its own
      commit prefixed `test-relax:` so `git log --grep '^test-relax'` stays a two-second audit of
      every assertion ever loosened. That is allowed. Doing it quietly is not.</sub>

- [ ] **Docs updated in this PR.** Behaviour changed and the docs did not is an unfinished PR; the
      follow-up never comes. Endpoint or field → `make gen` plus the `docs/api/` page and
      `docs/api-changelog.md`. Strategy → `docs/reference/strategies/<id>.md` and a fixture. Config
      key or CLI flag → regenerate the reference page. Log format →
      `docs/reference/log-formats.md` and a golden directory. Anything an officer sees → the guide
      that covers it.

- [ ] `make check` passes locally.
- [ ] Every commit is signed off (`git commit -s`). This project uses the DCO, not a CLA.
- [ ] The PR title is a conventional commit (`feat(ledger): …`). Squash-merge makes it the commit
      subject, and release-please parses it. `BREAKING CHANGE:` goes in this body.

## Invariants this PR touches

<!-- Tick only what applies, and say how you know it still holds. Leave the rest. -->

- [ ] **Ledger** — still append-only. No `UPDATE` or `DELETE` on `ledger_batch` / `ledger_entry` in
      Go, in SQL, or in a migration. Corrections are reversal batches with `reverses_batch_id` set.
- [ ] **Point arithmetic** — `Centipoints` (`int64`) only. No `float64`, no `NUMERIC`, no decimal
      strings, not in Go, not in SQL, not on the wire. Zero-sum splits use largest-remainder
      allocation and credits sum to exactly the debit.
- [ ] **Strategies are pure** — no `internal/store`, no `time.Now`, no `math/rand`. The clock and a
      seeded RNG are injected and the seed is persisted onto the batch.
- [ ] **Single guild** — no `guild_id` column was added "for later". Scope comes from the request
      principal.
- [ ] **Migrations** — forward-only, additive, and a new file rather than an edit to one that
      shipped. A destructive migration carries `-- dkp:destructive-approved: #<issue>` and the
      previous minor already stopped writing to that object.
- [ ] **API** — every new endpoint declares `Security` and `x-dkp-permission`, every mutating POST
      that creates domain state requires `Idempotency-Key`, and no `operationId` was renamed
      (a rename is a breaking SDK change even when the HTTP surface is unchanged).
- [ ] **Breaking API change** — I added the `!breaking-api` label and updated
      `docs/api-changelog.md`. `/api/v1` is additive-only; a real break mints `/api/v2`.

## Decision record

<!-- Needed when this PR adds a new *direct* dependency to go.mod, touches deploy/Dockerfile or
     db/schema.hcl, or adds a top-level internal/ package. Either add a file under docs/adr/ in
     this PR, or write a line HERE, in this body, starting with the word "adr" then a colon, then
     "n/a", then a dash and the reason it does not need one. ADR001 in `lint / repo` reads this
     body and fails without one, so the reason is harvested rather than remembered.

     Do not paste a filled-in example into this template: every PR inherits it and the gate would
     be satisfied for all of them. TestPRTemplate_DoesNotPreSatisfyTheADRGate fails if you do.

     docs/adr/README.md § "When an ADR is required" is the list. Ordinary features, refactors, bug
     fixes and new doc pages need nothing here. -->

## Issues filed

<!-- Out-of-scope findings from this work, one row per issue. AGENTS.md § "Out-of-scope findings:
     file an issue" — you are empowered to file these yourself with `gh issue create`, without
     asking first, and one issue per distinct item. Do not grow this PR to fix them.

     "None" is a fine answer. A follow-up described only in prose here is not: the issue is the
     durable artefact, this table is the pointer. Say what each one blocks, so triage need not
     re-derive it. -->

| Issue | What it is | What it blocks |
|---|---|---|
| #___ | | |

## Anything a reviewer should push back on

<!-- Shortcuts taken, a test you could not write, a guess you made about a P99 log format, a
     decision you would like a second opinion on. Writing "nothing" is a valid answer and a much
     better one than silence. -->
