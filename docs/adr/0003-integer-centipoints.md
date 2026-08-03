# ADR-0003 — Integer centipoints

**Status:** accepted · **Date:** 2026-08-03 · **Deciders:** owner

## Context and problem statement

EQdkp Plus stores points as `float(11,2)`, which is why guild members see balances like
`339.99999998` and why a zero-sum split there does not always sum to the debit. Points are money in
every way that matters to a raider — they are earned, spent, decayed and disputed — so the arithmetic
has to be exact, provable, and identical in Go, in SQL, on the wire and in every generated SDK.

## Considered options

| Option | For | Against |
|---|---|---|
| A — `float64` / SQL `REAL` | Trivial; what the incumbent does; fractional decay is natural | Not exact; `a + b - b ≠ a`; zero-sum splits leak points; equality comparisons are unsafe; the incumbent's displayed-balance bug is the proof |
| B — Decimal strings on the wire (`"350.00"`), `NUMERIC` in SQL | Human-readable; arbitrary precision | SQLite `STRICT` forbids `NUMERIC`; every SDK must parse; invites locale bugs (`350,00`); still needs an internal integer type, so it is a second representation |
| C — `int64` centipoints (points × 100) | Exact; one representation everywhere; sums and comparisons are trivially correct | Every display divides by 100 and every input multiplies; the scale factor is fixed forever |

## Decision outcome

**Chosen: C.** Points are `Centipoints int64` in `internal/core`. Columns are `INTEGER` with a `_cp`
suffix. On the wire the field name carries a `_centipoints` suffix and the value is an **unquoted
JSON integer**. Realistic maxima are ~10¹¹ centipoints, four orders of magnitude below JavaScript's
`MAX_SAFE_INTEGER` (9.007×10¹⁵), so an unquoted integer is safe for every consumer; strings would
force every SDK to parse and would invite exactly the locale bugs they were meant to avoid.

Float conversion happens **at one boundary only** — the EQdkp importer — using round-half-even, and
every row whose round-trip differs is logged. Zero-sum splits use largest-remainder allocation with a
deterministic tiebreak on `account_id`, so credits sum to exactly the debit; rounding each credit
independently mints or destroys points.

**Enforced by:** a `golangci-lint` rule banning `float32`/`float64` in `internal/ledger` and
`internal/strategy`; the `NoFloat` runtime invariant; a contract test asserting the JSON Schema type
is `integer`; and `STRICT` tables, which reject `"350.00"` in an `INTEGER` column rather than
coercing it — the cheapest available guard against an agent writing a decimal string into a
centipoint column.

### Consequences

- Good, because balance arithmetic is exact and every invariant about it is checkable by equality
  rather than by epsilon.
- Good, because there is exactly one representation of an amount from the database to the SDK, so
  no conversion layer can disagree with another.
- Good, because `STRICT` plus the `_cp` suffix makes a float mistake visible at the call site, in
  review, rather than as a rounding complaint six months later.
- **Bad, because the ×100 papercut recurs forever.** Every UI display, every form input, every CLI
  argument, every doc example and the compat shim's response formatting all convert. It is small
  each time and it never goes away, and it is the most likely place for a new contributor's first bug.
- **Bad, because an imported balance can differ from what EQdkp displayed.** Half-even rounding of a
  `float(11,2)` column is the correct answer and it is still a number a member may argue with. The
  importer's verification report has to show those rows rather than hide them.
- **Bad, because two decimal places is a guess, permanently.** A guild running a decay rule that
  produces thousandths cannot express it; it rounds. Changing the scale later is a rewrite of every
  stored amount and every wire contract, so in practice the answer to "can we have three decimals?"
  is no.
- **Bad, because unquoted integers are a bet on the maxima.** The bound is generous, but a guild that
  runs points in the millions with a ×100 scale is closer to the JavaScript limit than the design
  assumes. This is an assumption about realistic guild scale, checkable against the first real
  imports.

### Reversal cost

A schema migration over every amount column, a breaking wire change (`/api/v2`), regenerated SDKs,
and a re-verification of every historical balance. Months, and it invalidates every published
integration.
