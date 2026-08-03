---
name: triage-bug
description: Turn a user bug report into a reproducing failing test, then a diagnosis, then a fix. Use for any incoming report — a parser-bug issue, an import failure, a balance dispute, a 500, a "the site says 412 and I had 430" — and any time someone says "it's broken" without a test.
argument-hint: "[issue-number | short description]"
allowed-tools: Read, Grep, Glob, Edit, Write, Bash(gh issue *), Bash(make test-unit), Bash(make test), Bash(make test-importer), Bash(make check)
---

# Triage a bug

**The reproduction comes first. Not the diagnosis, not the fix, not the theory.**

A fix without a failing test that preceded it is a fix for the bug you imagined. This order is the
entire content of this skill; everything else is which fixture to reach for.

---

## Steps

### 1. Collect, before reading any code

| Ask for | Why |
|---|---|
| `dkp version` output | Which binary, which commit |
| `dkp doctor` output (or `--json`) | Proxy headers, SSE buffering, TLS, clock skew, WAL size, pending migrations, last `verify-ledger` result, top rate-limited tokens |
| `dkp support-bundle` (`--redact=standard`) | The whole diagnostic picture. A canary test in the required CI suite greps every file at both redaction levels for seeded canaries and fails the build on a hit — that is what makes it safe to attach to a public issue |
| The `request_id` | Echoed in `X-Request-Id` and in the server log. The support workflow is "paste me the request id". |
| The exact input | The log line, the dump file, the request body, the two balances that disagree |

**Never ask for a raw database.** Never ask for a PAT. If a report contains one, treat the token as
leaked: the 8-char public prefix is greppable and `dkp token revoke --prefix <prefix>` is the
one-liner.

### 2. Classify

| Class | Signal | The fixture it becomes |
|---|---|---|
| **Parser** | Reconciliation queue filling with `unparsed_line`; attendance missing for one person | A golden file under `test/golden/parse/<format>/` |
| **Import** | Δ set ≠ predicted set; any `unexplained` classification | A case on an existing EQdkp fixture, or a new hostile-fixture row |
| **Ledger / point maths** | "The site says 412, I had 430"; `verify-ledger` drift | A property or a replay case in `internal/ledger` |
| **API contract** | Wrong status, wrong error code, missing header, SDK mismatch | An integration test in `test/integration/` |
| **Auth / permissions** | Someone can do something they should not, or cannot do something they should | A row in `test/integration/testdata/authz_matrix.tsv` |
| **Realtime** | Dropped `event_seq`, stuck SSE, dead-lettered webhook | A delivery or replay test |
| **Ops / upgrade** | Migration hung, boot failed, restore failed | An upgrade-ladder case against a published refdb |

Classification decides the fixture, and the fixture decides the test. If you cannot classify it, you
do not understand the report yet — ask.

### 3. Reproduce as a **failing test**, before any diagnosis

Write the smallest test that fails for the reason the user described. Do not read the implementation
first: reading it biases the test toward the code that exists rather than the behaviour that was
promised.

```
parser        → drop the exact reported line into the golden dir, run it, watch it fail
import        → add the row shape to a fixture, run `make test-importer`, watch the Δ appear
ledger        → post the reported sequence of batches, assert the balance the user expected
api           → replay the request against the real server in t.TempDir(), assert the contract
auth          → add the principal × operation row, assert the expected verdict
```

### 4. Confirm it fails **for the right reason**

Read the failure output. A test that fails because a fixture path is wrong, or because the harness
did not boot, has reproduced nothing.

The assertion must be the user's claim, not a proxy for it. "Returns 500" is a proxy; "returns
`insufficient_balance` when the account has 43000 spendable centipoints" is the claim.

If you cannot make it fail, say so explicitly and go back to step 1. **"Could not reproduce" is a
legitimate and useful outcome.** Guessing at a fix for a bug you never saw is not.

### 5. Only now, diagnose

With a red test you have an oracle: you can change one thing and know immediately whether it mattered.

Wrap what you learn into the test as additional assertions rather than into a comment.

### 6. Fix — and do not touch the test

The test that reproduced the bug is the specification now.

**Never** weaken, skip, delete, `-update`, or loosen it to go green. A test-diff analyser flags
removed assertions, added `t.Skip`, `cmpopts.Ignore*`, `require`→`assert` swaps, raised budgets and
changed golden files, and forces CODEOWNERS review. If the test is wrong, say it is wrong and why, in
the PR body, with the `test-relax:` prefix convention.

### 7. Ledger damage is repaired by reversal only

If the bug wrote wrong ledger rows on a real instance:

- **Never** `UPDATE` or `DELETE` them. The trigger raises, in Go, in SQL and in migrations, and a
  test asserts the trigger fires.
- Write a **reversal batch** with `reverses_batch_id` set. The original stays visible, struck through.
- The remediation is a documented officer action, not a hidden data patch. "Fixing" a balance by
  editing history destroys the product's entire trust argument.

### 8. Widen the net once

Ask one question: *what else has this shape?* One wrong tiebreak in one parser is usually the same
wrong tiebreak in three. Add the sibling cases to the same golden directory now — they cost one line
each and they are the difference between one fix and four issues.

### 9. Close the loop with the reporter

- Parser bugs are **free golden-file test cases**. They must never be closed unread, and the golden
  is permanent — the format will not un-break itself.
- Point the reporter at the artefact that proves the fix: their own statement page, the regenerated
  verification report, or the golden file with their line in it.
- If the root cause was an unverified format assumption, mark the remaining unverified fixtures in
  that family — one wrong guess usually means the others were guessed the same day.

### 10. `make check`, and a changelog line

`fix:` in the PR title so release-please picks it up. A user-visible fix gets a line in
`CHANGELOG.md`; an API-visible one also gets a line in `docs/api-changelog.md`.

---

## Stop and ask if

- **You cannot reproduce it.** Say so and ask for the missing input. Do not ship a speculative fix.
- **The fix would require editing a golden file or a fixture** to go green. That is the top way an
  agent damages this project. State it in the PR body and get a human decision.
- **The report implies a P99 log format nobody has verified.** Add an `unverified` fixture and open an
  issue; do not invent a regex.
- **The bug is in `internal/ledger`, `internal/strategy`, `internal/auth`, or `db/schema.hcl`.** Plan
  first. These are the four places where a plausible-looking wrong change is expensive and hard to
  spot in review.
- **A real guild's balances are already wrong in production.** That is an incident with an officer
  communication component, not just a code fix. Reversal batches are the mechanism; who tells the
  guild is a human decision.
