---
paths: ["**/*.go"]
description: Go mechanics for this repo — error wrapping, context, the injected Clock, slog, and the test style CI enforces.
---

# Go idioms

House Go, not general Go. Everything here has a mechanism; where it doesn't, it isn't a rule.

## Errors

| Rule | Shape |
|---|---|
| Wrap with `%w` **and** context | `fmt.Errorf("load pool %s: %w", id, err)` |
| Context reads as a noun phrase, lowercase, no punctuation | `"decode who block"`, not `"Failed to decode!"` |
| Sentinels live in the owning package | `var ErrNotFound = errors.New("not found")`, `ErrConflict`, `ErrImmutable` |
| Compare with `errors.Is` / `errors.As`, never `==` | `errors.Is(err, sql.ErrNoRows)` |
| Never discard | An error is returned, handled or logged. `_ = f()` is a waiver, not a default |
| Never `panic` outside `main` wiring | A panic in a handler kills the raid night, not the request |

Handlers translate at the edge: `problem.From(err)` maps sentinels to Huma error types. A handler
never writes to `http.ResponseWriter` and never formats an error string for the wire — the
`code` comes from the closed enum in `internal/api/errors.go`.

**Enforced by:** `errcheck` in `golangci-lint`, which fires on an error return that is never
assigned at all — `resp.Body.Close()` on its own line. It does **not** see `_ = f()`, and its
`check-blank` option is deliberately off (`.golangci.yml`); nor would `check-blank` see a bare
`_ = err` on an error you already hold, because that is an identifier and not a call. So the waiver
is caught in review, not by a linter, which is the reason it has to stay rare enough to notice: use
it only where a failure genuinely cannot be acted on — a deferred `Close` on a read-only body, a
write to a response already committed — and say so in a comment when it is not obvious.

`wrapcheck` is **not** enabled, despite the `%w` rule above; `.golangci.yml` records why (almost
entirely false positives on stdlib returns at the current call count). The `%w`-plus-context rule is
a review rule today.

## Context

- `ctx context.Context` is the **first** parameter of every function that does I/O. No exceptions,
  including ones that "don't need it yet" — adding it later churns every call site.
- Never store a `ctx` in a struct field. Services hold a `store`, a `clock`, and config; they
  receive `ctx` per call.
- `context.Background()` appears only in `main`, `TestMain`, and job-worker roots.
- Every `store` call, every HTTP call and every job enqueue takes the caller's `ctx`. Cancellation
  on client disconnect is how SSE fan-out and a 90-second import stay bounded.

## The clock

```go
type Service struct {
    store store.Store
    clock clock.Clock // injected. ALWAYS.
}
now := s.clock.Now() // Micros
```

`time.Now` is banned outside `internal/clock` — repo gate `CLOCK001`, an AST analyzer in
`internal/repogate` that `scripts/repo-gates.sh` runs, so an aliased import does not defeat it. Time is
`core.Micros` (int64 Unix microseconds, UTC); see canonical conventions §2 for the wire form.

Time-dependent tests use `testing/synctest`, never `time.Sleep`. `time.Sleep` is grep-banned in
`**/*_test.go` and `test/` — it is the single largest flake source in Go services, and the fake
clock inside a synctest bubble is what makes bid timers, anti-snipe, SSE reconnect, webhook backoff
and decay catch-up deterministic rather than hopeful.

## Logging

`slog`, structured, no `fmt.Printf`, no `log.` package.

```go
slog.InfoContext(ctx, "committed ledger batch",
    "batch_id", b.ID, "pool_id", b.PoolID, "seq", b.Seq, "entries", b.EntryCount)
```

Never log: token secrets or bearer values, session ids, password hashes, PII in bulk, or **bid
amounts before reveal**. The 8-character public token prefix is loggable and is how a leaked token
is found; the secret never is. `request_id` is injected by the middleware's context handler and is
the support workflow ("paste me the request id").

**Enforced by:** the `security-reviewer` subagent on `internal/auth`, `internal/bids` and
`internal/webhook` diffs; a grep gate on `log.` and `fmt.Print`.

## Tests

- Table-driven, named `TestThing_Condition_Expectation` (`TestBalance_AfterReversal_ReturnsOriginal`).
- `t.Parallel()` in every test. Suites run `-race`; `-shuffle=on -count=1` on the packages that
  reach their subject through a subprocess, and over the whole suite in the nightly
  `suite / shuffled` job (ADR-0020). Write for a shuffled order regardless — the nightly re-roll is
  what finds order-dependence, and it files an issue against whoever's package it lands in.
- **`require`, never `assert`.** `testify/assert` continues after a failed assertion, so one broken
  invariant produces a page of cascading noise and the real first failure scrolls away. Banned by a
  `golangci-lint` `depguard` rule on `github.com/stretchr/testify/assert`.
- Whole-value comparisons over cherry-picked fields: `go-cmp` on the struct, or the canonical-JSON
  golden. Asserting three fields of a `BatchProposal` hides the fourth that changed.
- No mocks of the database. There is no fake `Queries` implementation and a lint rule forbids adding
  one — integration tests use real SQLite in `t.TempDir()` and cost ~25 s for the whole suite.
- Never weaken an assertion to go green. The `test-integrity-auditor` subagent diffs assertions
  against the merge base and reports loosening side by side.

### goleak

Any package that starts a goroutine gets:

```go
func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }
```

Required in `internal/events`, `internal/webhook`, `internal/bids`, `internal/jobs`,
`internal/server`. A leaked tailer or timer goroutine is invisible in a green run and shows up
three weeks later as RSS growth on someone's Raspberry Pi.

## Style

| Banned | Why |
|---|---|
| Naked returns | A named result returned bare hides which value is being returned in every branch |
| Package-level mutable state (`var registry = map[...]`, mutating `init()`) | `-shuffle=on` plus `t.Parallel()` turn it into an intermittent failure; wire dependencies in `main` instead |
| `interface{}` / `any` in domain signatures | The generated boundaries are typed; keep the middle typed too |
| A second type for the same concept | One exported type per concept. If two files define "the shape of a bid", one is wrong |
| Manual formatting | `gofumpt` + `goimports` run in a PostToolUse hook; don't fight it |

Interfaces are declared by the **consumer**, small, and satisfied implicitly — `store.Queries` is
the deliberate exception (it is the dual-dialect contract, see `.claude/rules/store-and-sql.md`).

Floats do not exist in `internal/ledger` or `internal/strategy`. `float32`/`float64` there is a
`golangci-lint` failure, and `core.Centipoints` (int64) is the only money type in the repo.
