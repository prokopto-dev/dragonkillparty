import AxeBuilder from "@axe-core/playwright";
import { expect, test, type Page } from "@playwright/test";

import allowlist from "./axe-allowlist.json" with { type: "json" };
import { DESIGN_ROUTE, openDesignPage } from "./design-page";

/*
 * axe over /_design.
 *
 * This is the first enforcement of .claude/rules/web.md's accessibility gate — "zero
 * serious/critical on primary routes", listed as a budget since PR 6 and unenforced anywhere until
 * now. /_design is the right first route because it renders every base component class on one page:
 * a violation in any primitive surfaces here rather than waiting for the screen that first uses it.
 *
 * SERIOUS AND CRITICAL ONLY, which is the gate as written, not a softened version of it. axe's
 * `minor` and `moderate` findings are advisory by axe's own definition, and a gate that fails on
 * them is a gate that gets disabled. (/_design does carry one moderate `heading-order` finding; it
 * is issue #61 rather than an allowlist row, because allowlisting something the gate does not check
 * would be noise.)
 *
 * State the limits honestly, as docs/design/04-testing.md §Accessibility does: axe covers roughly
 * 30-50% of WCAG. Its value is regression-catching on this product's specific risks — form labels,
 * contrast on guild-configurable colours, header semantics on the standings grid, focus order in a
 * dialog — not a claim of conformance.
 */

type Violation = Awaited<ReturnType<AxeBuilder["analyze"]>>["violations"][number];

/** One allowlisted exception: a rule id and the selector it is allowed to fire on. */
type Exception = { rule: string; target: string; issue: number; why: string };

/** A serious/critical finding, flattened to one entry per offending node. */
type Finding = { rule: string; target: string; description: string };

/** Every serious or critical violation on the current page state, one entry per node. */
async function scan(page: Page): Promise<Finding[]> {
  const results = await new AxeBuilder({ page }).analyze();

  return results.violations
    .filter((violation: Violation) => violation.impact === "serious" || violation.impact === "critical")
    .flatMap((violation: Violation) =>
      violation.nodes.map((node) => ({
        rule: violation.id,
        target: node.target.join(" "),
        description: `[${violation.impact ?? "unknown"}] ${violation.id} on ${node.target.join(" ")} — ${violation.help}`,
      })),
    );
}

/** The exceptions recorded for a route, or none. */
function exceptionsFor(route: string): Exception[] {
  return (allowlist.routes as Record<string, Exception[] | undefined>)[route] ?? [];
}

/**
 * Hold a scan to the route's allowlist, in BOTH directions.
 *
 * Unallowlisted findings fail, which is the gate. Allowlisted entries that matched nothing also
 * fail, which is what makes the list shrink-only: a violation someone fixes cannot leave its
 * exception behind, so the allowlist can never come to describe a bar lower than the one actually
 * being met. It is the same anti-tampering posture as a golden file.
 */
function assertWithinAllowlist(route: string, findings: Finding[]): void {
  const exceptions = exceptionsFor(route);
  const matched = new Set<number>();

  const unexpected = findings
    .filter((finding) => {
      const index = exceptions.findIndex(
        (exception) => exception.rule === finding.rule && exception.target === finding.target,
      );
      if (index === -1) {
        return true;
      }
      matched.add(index);

      return false;
    })
    .map((finding) => finding.description);

  expect(
    unexpected,
    `serious/critical axe violations on ${route} that are not in web/e2e/axe-allowlist.json. ` +
      "Fix the markup. Adding a row to the allowlist is for a violation whose fix is somebody " +
      "else's decision, and it needs an issue number.",
  ).toEqual([]);

  const stale = exceptions
    .filter((_, index) => !matched.has(index))
    .map((exception) => `${exception.rule} on ${exception.target} (issue #${String(exception.issue)})`);

  expect(
    stale,
    `web/e2e/axe-allowlist.json allows violations on ${route} that no longer occur. The allowlist ` +
      "is shrink-only: delete these entries and close their issues.",
  ).toEqual([]);
}

test.describe("Accessibility", () => {
  test("/_design has no unallowlisted serious or critical axe violations", async ({ page }) => {
    await openDesignPage(page);

    assertWithinAllowlist(DESIGN_ROUTE, await scan(page));
  });

  test("the open dialog has no serious or critical axe violations", async ({ page }) => {
    await openDesignPage(page);
    await page.getByRole("button", { name: "Open dialog" }).click();
    await expect(page.getByRole("dialog")).toBeVisible();

    // A separate scan because a modal is a different DOM state, and it is the state
    // docs/design/04-testing.md names by hand ("focus order in the bid dialog"). Scanning only the
    // closed page would never look at it. The open dialog covers the page, so axe sees the modal
    // rather than the content behind it and the route's own allowlist does not apply.
    expect(await scan(page)).toEqual([]);
  });
});
