// Package swap prices a main swap. It is the rule evaluator behind the main-swap policy screen and
// the member's character page, and it is PURE: no database, no wall clock of its own, no randomness.
// The clock is injected, so the same request evaluated at the same instant produces the same quote
// forever — which is what lets Phase 7 hold a quote at request time and refuse to re-price it.
//
// A main swap is a PRICED, APPROVED EVENT rather than a profile edit
// (docs/design/10-ui-decisions.md §4). Two questions have to be answered before an officer sees the
// request at all, and this package answers both:
//
//   - WHAT DOES IT COST? A base cost with an ordered rule stack over it. Evaluate returns the cost
//     and the trail of rules that produced it, in the order they applied.
//   - MAY THEY EVEN ASK? Guards — cooldown, an attendance floor, affordability, and the two shapes
//     that are not swaps at all — are separate from cost and produce blocks, not a price.
//
// # The rule stack
//
// The shipped default, from docs/design/10-ui-decisions.md §4. DefaultPolicy returns exactly this:
//
//	base                                                                        750.00
//	1  an open swap period is active               free, no allowance used       on
//	2  the member has an annual allowance left     free, consumes one            on   2 per year
//	3  the target character is level 60            -250.00                       on
//	4  the target class is on the needed list      -300.00                       on
//	5  above 80% attendance over 90 days           -100.00                       off
//	6  returning to a previous main                x2                            off
//
// Rules 1 and 2 are FIRST-MATCH FREE: the first one that matches sets the cost to zero and stops the
// evaluation, because nothing after it can change a price of zero. Rules 3 to 6 STACK — settled by
// the guild lead in docs/design/10-ui-decisions.md §12, against the alternative of taking the single
// best discount: "the conditions that stack are all things the guild wants to happen". A level-60
// needed-class main therefore costs 200.00 of 750.00, which is intended.
//
// ORDER IS THE POLICY'S, NOT THIS PACKAGE'S. Rules apply top to bottom exactly as the policy lists
// them, and that matters as soon as a multiplier is enabled: x2 after the discounts prices a
// returning level-60 cleric at 400.00, and x2 before them at 1000.00. The default order is the
// design's; a guild that reorders its rules gets the arithmetic it asked for, and Quote.Applied
// records which rule ran when so the answer is auditable rather than asserted.
//
// # What is deliberately not here
//
//   - THE LEDGER POSTING. Approving a swap posts a reversible adjustment batch and moves the main
//     flag in one transaction (ROADMAP Phase 7). A quote is not money until that batch is committed,
//     and this package cannot commit anything.
//   - THE STATE MACHINE. main_swap_request.state (quoted, pending, approved, applied, denied,
//     expired — canonical §5) and the two-officer approval count are Phase 7's. Guards.ApprovalsRequired
//     travels through Decision so the screen can say "1 of 2"; counting the approvals is not a
//     pricing question.
//   - READING ANY OF IT. Every fact the evaluator needs arrives in the Request: balances, attendance,
//     prior swaps, the roster's view of the target character. A package that could read the store
//     could price a swap on state the held quote never saw.
//
// # Purity
//
// Law 3 names internal/strategy, and the repo gates PURE001, PURE002, CLOCK002 and MONEY001 are
// scoped to that tree. Nothing scoped to it reaches here, so arch_test.go is this package's own
// proof rather than a second opinion: no path to internal/store, no direct import of time or
// math/rand outside the tests, no construction of the real clock, and no float in a package whose
// whole job is money. Its negative fixture builds a tainted tree and requires every assertion to go
// red on it, because a gate nobody has seen fail is a gate nobody knows works.
//
// Why that proof lives here rather than in the repo-gate catalogue — and what would move it there —
// is ADR-0025, docs/adr/0025-pure-evaluators-outside-strategy.md.
package swap
