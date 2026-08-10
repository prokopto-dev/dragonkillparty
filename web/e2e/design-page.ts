import { expect, type Locator, type Page } from "@playwright/test";

/*
 * Shared helpers for the /_design specs.
 *
 * /_design renders every token and every base component class on one page, which is what lets a
 * suite this small cover the whole Nocturne vocabulary rather than one screen's worth. It is served
 * by the binary's index.html fallback (internal/ui), so reaching it at all also proves the embed and
 * the client-side router agree about routes the server does not know.
 */

export const DESIGN_ROUTE = "/_design";

/** Navigate to /_design and wait until it has rendered. */
export async function openDesignPage(page: Page): Promise<void> {
  await page.goto(DESIGN_ROUTE);
  await expect(page.getByRole("heading", { name: "Nocturne", level: 1 })).toBeVisible();
}

/**
 * Focus `target` in a way that matches `:focus-visible`.
 *
 * Every focus-ring rule in the system is `:focus-visible`, never `:focus` — base.css suppresses the
 * UA ring on plain `:focus` on purpose — so a computed-style assertion made after a bare
 * `locator.focus()` reads the unfocused value and passes against a stylesheet with no ring at all.
 *
 * Chromium decides `:focus-visible` from the input MODALITY, not from how focus was moved: once the
 * user has interacted with the keyboard, focus moved by script still matches. So press Tab once to
 * establish keyboard modality, then move focus to the element under test. Tabbing all the way to a
 * given control instead would make every assertion depend on the page's whole tab order.
 */
export async function focusVisibly(page: Page, target: Locator): Promise<void> {
  await page.keyboard.press("Tab");
  await target.focus();
  await expect(target).toBeFocused();
}

/**
 * Resolve a design token to the value the browser computes for it, as a CSS `<color>`.
 *
 * Assertions compare against this rather than against a hard-coded `rgb(145, 132, 217)`: the claim
 * under test is "the focus ring is the accent token", not "the accent is #9184d9". The literal
 * values are already pinned against the normative document by test/repo/design_tokens_test.go, and
 * duplicating them here would mean a palette change failed in two places for one reason.
 */
export async function resolveColourToken(page: Page, token: string): Promise<string> {
  return page.evaluate((name) => {
    const probe = document.createElement("span");
    probe.style.color = `var(${name})`;
    document.body.append(probe);
    const resolved = getComputedStyle(probe).color;
    probe.remove();

    return resolved;
  }, token);
}
