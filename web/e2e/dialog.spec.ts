import { expect, test } from "@playwright/test";

import { openDesignPage } from "./design-page";

/*
 * Dialog — the three guarantees the component's own comment advertises.
 *
 * Dialog.tsx chooses a native <dialog> opened with showModal() SPECIFICALLY so that Escape, focus
 * containment and focus restoration come from the platform rather than from hand-written code. That
 * choice is only worth anything if it holds, and none of it is observable from a type test, a
 * computed-style diff or an accessibility-tree dump: all three are things a browser DOES.
 *
 * "Focus returns to the element that opened the dialog" is the priority one. It has already
 * regressed once (caught in review of #30, fixed in 7c6cbee): the cleanup was a useEffect, whose
 * cleanup runs AFTER React detaches the node, and removing an open <dialog> drops it from the top
 * layer and resets document focus — so the subsequent close() no longer took the modal close path
 * that refocuses the opener. Switching to useLayoutEffect fixed it. Reverting that one word would be
 * silent today; it fails this file.
 */

test.describe("Dialog", () => {
  test.beforeEach(async ({ page }) => {
    await openDesignPage(page);
  });

  test("Escape closes the dialog", async ({ page }) => {
    const trigger = page.getByRole("button", { name: "Open dialog" });
    await trigger.click();

    const dialog = page.getByRole("dialog");
    await expect(dialog).toBeVisible();

    await page.keyboard.press("Escape");

    // Gone from the DOM, not merely hidden: Dialog's onCancel calls preventDefault and hands the
    // decision to the caller, which unmounts. A dialog still present here means `cancel` was
    // swallowed without onClose being wired — the exact break the issue predicts.
    await expect(dialog).toHaveCount(0);
  });

  test("focus is contained while the dialog is open", async ({ page }) => {
    const trigger = page.getByRole("button", { name: "Open dialog" });
    await trigger.click();
    await expect(page.getByRole("dialog")).toBeVisible();

    // What "contained" means precisely, and it is NOT "activeElement is always inside the dialog".
    //
    // Chromium's modal tab cycle passes through the document body once per lap — Cancel, Write
    // reversal, <body>, Cancel, ... — because the document itself is the wrap point. Observed, not
    // assumed. Body is not a control and holds nothing the user can operate, so focus is still
    // contained; the guarantee that matters is that no focusable element BEHIND the dialog is ever
    // reachable. That is exactly what the top layer provides and exactly what a div with
    // aria-modal="true" does not: there, this loop walks straight out into the page.
    const focusedElement = () =>
      page.evaluate(() => {
        const active = document.activeElement;
        if (active === null || active === document.body || active === document.documentElement) {
          return "document";
        }

        return active.closest("dialog") === null
          ? `OUTSIDE: <${active.tagName.toLowerCase()}> ${(active.textContent ?? "").trim().slice(0, 40)}`
          : "inside";
      });

    // The platform moves focus into the dialog on open. If this fails, the element is no longer a
    // modal <dialog>.
    expect(await focusedElement(), "focus did not move into the dialog on open").toBe("inside");

    // Two full laps forwards, then two backwards — enough to wrap several times past both ends of
    // the dialog's focusable content.
    for (let stop = 1; stop <= 8; stop += 1) {
      await page.keyboard.press("Tab");
      expect(await focusedElement(), `Tab stop ${String(stop)} escaped the dialog`).not.toContain(
        "OUTSIDE",
      );
    }

    for (let stop = 1; stop <= 8; stop += 1) {
      await page.keyboard.press("Shift+Tab");
      expect(
        await focusedElement(),
        `Shift+Tab stop ${String(stop)} escaped the dialog`,
      ).not.toContain("OUTSIDE");
    }

    // And the page behind really is inert: the control that opened the dialog is not reachable.
    await expect(trigger).not.toBeFocused();
  });

  test("focus returns to the control that opened the dialog — Escape", async ({ page }) => {
    const trigger = page.getByRole("button", { name: "Open dialog" });

    // Activated from the keyboard so the trigger unambiguously holds focus when showModal() records
    // the element to restore to.
    await trigger.focus();
    await expect(trigger).toBeFocused();
    await page.keyboard.press("Enter");

    await expect(page.getByRole("dialog")).toBeVisible();
    await expect(trigger).not.toBeFocused();

    await page.keyboard.press("Escape");
    await expect(page.getByRole("dialog")).toHaveCount(0);

    await expect(
      trigger,
      "focus did not return to the trigger — Dialog's cleanup must run BEFORE React detaches the " +
        "node (useLayoutEffect, not useEffect), or close() no longer takes the modal close path",
    ).toBeFocused();
  });

  test("focus returns to the control that opened the dialog — action button", async ({ page }) => {
    // The second close path. Both end in the same layout-effect cleanup, and asserting only Escape
    // would leave the one a user actually clicks unobserved.
    const trigger = page.getByRole("button", { name: "Open dialog" });
    await trigger.focus();
    await page.keyboard.press("Enter");
    await expect(page.getByRole("dialog")).toBeVisible();

    await page.getByRole("button", { name: "Cancel" }).click();
    await expect(page.getByRole("dialog")).toHaveCount(0);

    await expect(trigger).toBeFocused();
  });

  test("the dialog is labelled by its own title", async ({ page }) => {
    await page.getByRole("button", { name: "Open dialog" }).click();

    // aria-labelledby points at the generated title id. A dialog announced as "dialog" with no name
    // is the shape this component's useId wiring exists to prevent.
    await expect(page.getByRole("dialog", { name: "Reverse this batch?" })).toBeVisible();
  });
});
