import { Button } from "./Button";
import { Dialog } from "./Dialog";
import { Field, Input, TextArea } from "./Field";
import { Radio } from "./Radio";
import { Seg, SegOption } from "./Seg";
import { VirtualTable } from "./VirtualTable";

/*
 * TYPE-LEVEL NEGATIVE TESTS for the component contracts that carry an accessibility guarantee.
 *
 * `tsc --noEmit` runs over web/src in `make vet` and in CI's `typecheck` job, and a `@ts-expect-error`
 * FAILS THE BUILD IF THE ERROR STOPS HAPPENING. So each block below is a live assertion that the prop
 * is still impossible to pass, not a comment hoping so.
 *
 * This is the coverage available without a browser harness. `make test-e2e` is still a Phase 3 stub
 * ("Playwright against the built binary"), so nothing in this repository can yet click a segmented
 * control or press Escape — which is exactly why each guarantee below is expressed as a type the
 * compiler enforces rather than as behaviour a future test might check. When the Playwright harness
 * lands, the interaction tests belong beside it against /_design (issue #33); these stay either way,
 * because they fail at author time rather than in CI.
 *
 * Every entry here is a defect that SHIPPED and was caught in review, not a hypothetical. Each one
 * compiled, rendered, and looked correct in a screenshot.
 *
 * Nothing imports this module, so it is not in the entry graph and contributes nothing to the bundle.
 */

// --- Field owns the control's id -----------------------------------------------------------------
//
// The defect: <Field label="Name"><Input id="name" /></Field> pointed the label at Field's generated
// id and the input at "name". Both rendered, neither was associated, and no screenshot could show it.

export const inputRejectsAnId = (
  <Field label="Name">
    {/* @ts-expect-error Field owns the id — accepting one here silently unassociates the label. */}
    <Input id="name" />
  </Field>
);

export const textAreaRejectsAnId = (
  <Field label="Notes">
    {/* @ts-expect-error Field owns the id — see inputRejectsAnId. */}
    <TextArea id="notes" />
  </Field>
);

// --- Seg owns the radio group's name --------------------------------------------------------------
//
// The defect: a per-option `name` (or none) produced role="group" wrapped around unrelated radios —
// several could be checked at once and arrow keys did not traverse the segment.

export const segOptionRejectsAName = (
  <Seg label="Filter">
    {/* @ts-expect-error Seg generates the shared name; a per-option name breaks the radio group. */}
    <SegOption name="mine">All</SegOption>
  </Seg>
);

// --- A radio cannot be ungrouped ------------------------------------------------------------------
//
// React's InputHTMLAttributes makes `name` optional. Radio makes it required, because unnamed radios
// are independent controls that can all be checked at once.

// @ts-expect-error a Radio without a name is not in any group.
export const radioRequiresAName = <Radio>Quarantine</Radio>;

// --- An icon button must carry its own accessible name --------------------------------------------
//
// The defect: `icon?: boolean` was unrelated to `aria-label`, so an icon button whose only child is an
// aria-hidden glyph compiled into a control that announces as "button" and nothing else.

// @ts-expect-error icon buttons require aria-label — the glyph carries no accessible name.
export const iconButtonRequiresALabel = <Button icon>+</Button>;

// The control: with a label it compiles. Without this, narrowing the type to reject *every* icon button
// would satisfy the negative test above while making icon buttons unusable.
export const iconButtonWithALabelIsFine = (
  <Button icon aria-label="Add">
    +
  </Button>
);

// --- The modal contract ---------------------------------------------------------------------------
//
// onClose is required. A modal with no close path is a trap: Escape fires `cancel`, and if nothing is
// wired to it the dialog cannot be dismissed by keyboard at all.

// @ts-expect-error onClose is required — Escape must have somewhere to go.
export const dialogRequiresOnClose = <Dialog title="Reverse?">Body</Dialog>;

// --- The virtualised table's row identity ---------------------------------------------------------
//
// rowKey is required: without a stable key, React reuses row DOM across scroll positions and the
// virtualizer's measurements attach to the wrong rows.

export const virtualTableRequiresARowKey = (
  // @ts-expect-error rowKey is required for stable row identity under virtualisation.
  <VirtualTable columns={[]} rows={[]} height={100} label="Sample" />
);
