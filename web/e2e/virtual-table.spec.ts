import { expect, test, type Page } from "@playwright/test";

import { openDesignPage } from "./design-page";

/*
 * VirtualTable — the row-identity guarantee, and what it tells assistive technology.
 *
 * `getItemKey` is the entry in issue #33's table that has ALREADY BROKEN ONCE. Without it the
 * virtualizer keys its measurement cache by INDEX, so after a server-side re-order every measured
 * height stays attached to the old position: getTotalSize() and the spacer offsets are computed from
 * other rows' heights, and offscreen rows are never remeasured at all. The React `<tr key>` is a
 * separate identity map and does not cover it, so the two can silently diverge again.
 *
 * The observable consequence, stated as an invariant:
 *
 *   ONCE EVERY ROW HAS BEEN MEASURED, RE-ORDERING THEM CANNOT CHANGE THE TOTAL SCROLL HEIGHT.
 *
 * It is the same 200 rows and the same 200 heights; only their order changed. A cache keyed by row
 * satisfies this exactly. A cache keyed by index does not: the rows now visible are remeasured into
 * the indices they landed on, overwriting the heights that belonged to whatever used to sit there,
 * while the offscreen majority keeps stale values — so the total moves and stays moved.
 *
 * THREE PRECONDITIONS, and the suite is worthless without any of them. Row heights must be
 * NONUNIFORM (with every row the same height, stale and fresh heights are the same number and the
 * bug cannot be seen at all); a row's height must be a function of the ROW rather than of its
 * neighbours (a <table> in auto layout sizes columns from the cells currently rendered, so a
 * wrapping cell breaks the invariant for honest reasons — design.tsx makes alts two lines with
 * markup instead, and says so); and every row must have been measured before the re-order, hence
 * measureEveryRow below. The "By rank" order exists for this test and carries a comment saying so.
 */

/** The virtualised table's scroll viewport. */
const VIEWPORT = ".virtual-table";

/** Bounded polling ceiling for "the virtualizer has settled", in milliseconds. */
const SETTLE_TIMEOUT = 10_000;

/*
 * How far the total may move across a re-order, in CSS pixels, and both numbers below are measured
 * rather than guessed — taken from this suite against this fixture, with and without `getItemKey`.
 *
 *   correct implementation:   7926 -> 7923   (3px)
 *   getItemKey deleted:       7926 -> 8209   (283px)
 *
 * The residual 3px is honest and is not the virtualizer's fault: a <table> in auto layout sizes its
 * columns from the cells CURRENTLY rendered, and the two orders put different content in the "Alt
 * of" and "Status" columns of the top window, so the whole table reflows by a hair. design.tsx
 * removes the large version of that effect deliberately (heights come from markup, not from wrapping
 * text) and this is what is left.
 *
 * 16px — under half a short row — sits two orders of magnitude below the defect and five times above
 * the noise. It is a threshold on ROUNDING, not a softened assertion: a cache keyed by position
 * cannot land inside it.
 */
const REORDER_TOLERANCE = 16;

/**
 * Scroll the viewport from top to bottom so the virtualizer measures every row.
 *
 * Rows start at the estimated size and are replaced with a real measurement as they mount, so the
 * scroll range GROWS as the pass proceeds — the loop re-reads scrollHeight each iteration for that
 * reason. The iteration guard is a runaway backstop, not a coverage limit.
 */
async function measureEveryRow(page: Page): Promise<void> {
  await page.evaluate(async (selector) => {
    const viewport = document.querySelector(selector);
    if (!(viewport instanceof HTMLElement)) {
      throw new Error(`no ${selector} on the page`);
    }

    const settle = () =>
      new Promise<void>((resolve) => {
        requestAnimationFrame(() => {
          requestAnimationFrame(() => {
            resolve();
          });
        });
      });

    for (let guard = 0, top = 0; guard < 500 && top <= viewport.scrollHeight; guard += 1) {
      viewport.scrollTop = top;
      await settle();
      top += viewport.clientHeight;
    }

    viewport.scrollTop = 0;
    await settle();
  }, VIEWPORT);
}

/** The viewport's current scroll height, which is the header plus the virtualizer's total size. */
function scrollHeight(page: Page): Promise<number> {
  return page.evaluate((selector) => document.querySelector(selector)?.scrollHeight ?? -1, VIEWPORT);
}

/** Poll until the scroll height stops moving, so a measurement pass is not read mid-flight. */
async function settledScrollHeight(page: Page): Promise<number> {
  let previous = -1;

  await expect
    .poll(
      async () => {
        const current = await scrollHeight(page);
        const stable = current === previous;
        previous = current;

        return stable;
      },
      { timeout: SETTLE_TIMEOUT, message: "the virtualizer never stopped remeasuring" },
    )
    .toBe(true);

  return previous;
}

test.describe("VirtualTable", () => {
  test.beforeEach(async ({ page }) => {
    await openDesignPage(page);
  });

  test("a re-order does not change the total scroll height", async ({ page }) => {
    await measureEveryRow(page);
    const measured = await settledScrollHeight(page);

    // A floor, so the test cannot pass against a collapsed or unrendered table. 200 rows at the
    // 32px estimate is already 6400; a fully measured, partly wrapping set is larger still.
    expect(measured).toBeGreaterThan(6400);

    const firstCell = page.locator(`${VIEWPORT} tbody tr:not([aria-hidden]) td`).first();
    await expect(firstCell).toHaveText("Character 001");

    // Ask the "server" for the same rows in a different order. The input is visually hidden and
    // takes no pointer events by design, so the click lands on its .seg-opt label — which is how a
    // user drives it too.
    await page.getByRole("group", { name: "Server order" }).getByText("By rank").click();

    // The re-order really happened: without this the height assertion below could pass by doing
    // nothing at all. "Alt" sorts first and the ranks cycle every four rows, so the collection now
    // leads with row index 3 — which, being an alt, is also one of the tall ones.
    await expect(firstCell).toHaveText("Character 004alt");

    const reordered = await settledScrollHeight(page);

    expect(
      Math.abs(reordered - measured),
      `the total scroll height moved from ${String(measured)} to ${String(reordered)} when the ` +
        "rows were only re-ordered. The same 200 rows cannot total a different height: the " +
        "virtualizer's measurement cache is keyed by position rather than by row, so every " +
        "measured height stayed attached to the old index. Restore getItemKey in " +
        "web/src/components/VirtualTable.tsx.",
    ).toBeLessThanOrEqual(REORDER_TOLERANCE);
  });

  test("the row count reported to assistive technology counts the header", async ({ page }) => {
    const table = page.getByRole("table", { name: "Sample standings" });

    // 200 data rows plus the header, which occupies aria-rowindex 1 in the same 1-based space.
    // Passing rows.length instead makes the last row announce "row 201 of 200".
    await expect(table).toHaveAttribute("aria-rowcount", "201");
    await expect(table.locator("thead tr")).toHaveAttribute("aria-rowindex", "1");

    const firstDataRow = table.locator("tbody tr:not([aria-hidden])").first();
    await expect(firstDataRow).toHaveAttribute("aria-rowindex", "2");
  });

  test("the spacer rows are hidden from assistive technology", async ({ page }) => {
    await page.evaluate((selector) => {
      const viewport = document.querySelector(selector);
      if (viewport instanceof HTMLElement) {
        viewport.scrollTop = viewport.clientHeight * 4;
      }
    }, VIEWPORT);

    // Scrolled into the middle there is a spacer at each end. They hold scroll height, not data; a
    // screen reader that announced them would report two empty rows in every table.
    const spacers = page.locator(`${VIEWPORT} tr.virtual-table-spacer`);
    await expect(spacers.first()).toBeAttached();

    const exposed = await spacers.evaluateAll((rows) =>
      rows.filter((row) => row.getAttribute("aria-hidden") !== "true").length,
    );
    expect(exposed).toBe(0);
  });
});
