import { expect, test } from "@playwright/test";

import { focusVisibly, openDesignPage, resolveColourToken } from "./design-page";

/*
 * The form controls' platform guarantees, and the focus-ring trio.
 *
 * Seg and Field both prevent a defect BY CONSTRUCTION — Seg owns the radio group's `name`, Field
 * owns its control's `id`, and neither is an accepted prop on the child. props.test-d.tsx locks the
 * types so the contracts cannot be re-widened. What a type test cannot check is whether the browser
 * then does the thing the types were protecting: whether arrow keys traverse, whether exactly one
 * option ends up checked, whether the label actually names the control.
 *
 * The focus offsets are the one Nocturne detail with three different correct values — 2px by
 * default, 0 on .input, -2px on .seg-opt — and nothing else in the repository asserts a computed
 * style. A specificity change is all it takes.
 */

test.describe("Seg", () => {
  test.beforeEach(async ({ page }) => {
    await openDesignPage(page);
  });

  test("a segmented control is one radio group", async ({ page }) => {
    const seg = page.getByRole("group", { name: "Sample filter" });
    const options = seg.getByRole("radio");

    await expect(options).toHaveCount(4);

    const names = await options.evaluateAll((inputs) =>
      inputs.map((input) => (input as HTMLInputElement).name),
    );

    // ONE shared name is what makes this a group at all. Drop SegNameContext and each option gets a
    // distinct useId: the radios still render, still look right, and are no longer related.
    expect(new Set(names).size, `options carry ${String(names.length)} distinct names: ${names.join(", ")}`).toBe(1);
    expect(names[0]).not.toBe("");
  });

  test("arrow keys traverse the group and only one option stays checked", async ({ page }) => {
    const seg = page.getByRole("group", { name: "Sample filter" });
    const options = seg.getByRole("radio");

    await expect(options.nth(0)).toBeChecked();

    await options.nth(0).focus();
    await page.keyboard.press("ArrowRight");

    await expect(options.nth(1)).toBeChecked();
    await expect(options.nth(1)).toBeFocused();
    await expect(options.nth(0)).not.toBeChecked();

    await page.keyboard.press("ArrowRight");
    await expect(options.nth(2)).toBeChecked();

    // Wraps at the end, which is roving-tabindex radio-group behaviour straight from the platform.
    await page.keyboard.press("ArrowRight");
    await page.keyboard.press("ArrowRight");
    await expect(options.nth(0)).toBeChecked();

    const checkedCount = await options.evaluateAll(
      (inputs) => inputs.filter((input) => (input as HTMLInputElement).checked).length,
    );
    expect(checkedCount, "a radio group has exactly one checked option").toBe(1);
  });

  test("the checked option takes the accent, unchecked ones do not", async ({ page }) => {
    // :has(input:checked) is the system's chosen mechanism (§5 of the design document) rather than a
    // React-managed class, so the checked input stays the single source of truth. It is also a
    // browser-floor bet, and this is what would notice the floor moving.
    const accent = await resolveColourToken(page, "--color-accent");
    const seg = page.getByRole("group", { name: "Sample filter" });
    const labels = seg.locator(".seg-opt");

    await expect(labels.nth(0)).toHaveCSS("color", accent);
    await expect(labels.nth(1)).not.toHaveCSS("color", accent);
  });
});

test.describe("Field", () => {
  test.beforeEach(async ({ page }) => {
    await openDesignPage(page);
  });

  test("a label names its own control", async ({ page }) => {
    // Resolved through the accessible name, which is the property that matters: it is computed the
    // same way a screen reader computes it, and it is what breaks when the context does not reach a
    // control rendered through an intermediate component.
    await expect(page.getByRole("textbox", { name: "Text input" })).toHaveValue("Grimwald Ashvane");
    await expect(page.getByRole("textbox", { name: "Textarea" })).toHaveValue("Reversal reason");
  });

  test("every field's htmlFor resolves to a control inside that field", async ({ page }) => {
    const orphans = await page.evaluate(() =>
      [...document.querySelectorAll(".field")].flatMap((field) => {
        const label = field.querySelector(":scope > label");
        const target = label?.getAttribute("for");
        if (target === null || target === undefined || target === "") {
          return [`field with no labelled control: ${field.textContent ?? ""}`];
        }

        const control = document.getElementById(target);

        return control !== null && field.contains(control)
          ? []
          : [`label for="${target}" points outside its own field`];
      }),
    );

    expect(orphans).toEqual([]);
  });
});

test.describe("Focus", () => {
  test.beforeEach(async ({ page }) => {
    await openDesignPage(page);
  });

  test("the ring is 2px solid accent, offset 2px by default", async ({ page }) => {
    const accent = await resolveColourToken(page, "--color-accent");
    const button = page.getByRole("button", { name: "Primary", exact: true });

    await focusVisibly(page, button);

    await expect(button).toHaveCSS("outline-width", "2px");
    await expect(button).toHaveCSS("outline-style", "solid");
    await expect(button).toHaveCSS("outline-color", accent);
    await expect(button).toHaveCSS("outline-offset", "2px");
  });

  test("the ring hugs a .input at offset 0", async ({ page }) => {
    const input = page.getByRole("textbox", { name: "Text input" });

    await focusVisibly(page, input);

    await expect(input).toHaveCSS("outline-width", "2px");
    // 0, not the document's 2px: the ring sits on the bordered control rather than floating off it.
    await expect(input).toHaveCSS("outline-offset", "0px");
  });

  test("the ring sits inside a .seg-opt at offset -2px", async ({ page }) => {
    const seg = page.getByRole("group", { name: "Sample filter" });
    const option = seg.getByRole("radio").nth(1);
    // The outline is painted on the LABEL via :has(input:focus-visible); the input itself is the
    // visually hidden thing that takes focus.
    const label = seg.locator(".seg-opt").nth(1);

    await focusVisibly(page, option);

    await expect(label).toHaveCSS("outline-width", "2px");
    await expect(label).toHaveCSS("outline-offset", "-2px");
  });
});

test.describe("Hover and active states", () => {
  test.beforeEach(async ({ page }) => {
    await openDesignPage(page);
  });

  test("the primary button takes tint(12) on hover and tint(22) on active", async ({ page }) => {
    const tint12 = await resolveColourToken(page, "--tint-12");
    const tint22 = await resolveColourToken(page, "--tint-22");
    const button = page.getByRole("button", { name: "Primary", exact: true });

    // Outlined, never filled: transparent at rest is half of what makes a Nocturne button a Nocturne
    // button, and a later rule winning would show up here first.
    await expect(button).toHaveCSS("background-color", "rgba(0, 0, 0, 0)");

    await button.hover();
    await expect(button).toHaveCSS("background-color", tint12);

    const box = await button.boundingBox();
    expect(box).not.toBeNull();
    await page.mouse.move(box!.x + box!.width / 2, box!.y + box!.height / 2);
    await page.mouse.down();
    try {
      await expect(button).toHaveCSS("background-color", tint22);
    } finally {
      await page.mouse.up();
    }
  });

  test("a table row's hover tint LAYERS OVER the fading rule", async ({ page }) => {
    // The row rule is a row-level background gradient — the most identifiable thing in the system —
    // and the hover paint is a SECOND background listed before it, not a replacement. A
    // single-background hover makes the rule flicker away under the pointer, which is exactly what a
    // declaration-level fidelity diff cannot see.
    //
    // `:not([aria-rowcount])` picks the plain .table rather than the virtualised one, which sets it.
    const row = page.locator("table.table:not([aria-rowcount]) tbody tr").first();

    const layersAtRest = await row.evaluate(
      (tr) => getComputedStyle(tr).backgroundImage.split("gradient(").length - 1,
    );
    expect(layersAtRest, "the row rule itself").toBe(1);

    await row.hover();

    const layersOnHover = await row.evaluate(
      (tr) => getComputedStyle(tr).backgroundImage.split("gradient(").length - 1,
    );
    expect(layersOnHover, "the hover tint must layer over the rule, not replace it").toBe(2);
  });
});
