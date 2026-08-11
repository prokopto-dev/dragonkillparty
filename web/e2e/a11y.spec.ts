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
 * them is a gate that gets disabled.
 *
 * So the two defects this page actually had get a NAMED test each, below the sweep, rather than a
 * tier change:
 *
 *   - `.card-meta` contrast (issue #58) was serious, allowlisted, and is now fixed — the allowlist
 *     entry is gone. The sweep would catch a regression on its own; the named test states the
 *     number, so the failure says "4.25:1, floor is 4.5" instead of "an axe rule fired".
 *   - Heading order (issue #61) was MODERATE, so the sweep never covered it and never will. Asserted
 *     directly instead. An outline is not a matter of degree: either the levels step by one or a
 *     screen-reader user gets a wrong map of the page.
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

/** What `.card-meta` actually paints, measured in the page. */
type Contrast = { ratio: number; fontSizePx: number; foreground: string; background: string };

/**
 * The WCAG contrast ratio between an element's text colour and the ground it is painted on.
 *
 * MEASURED IN THE PAGE, not computed from token values on this side, because the answer depends on
 * the COMPOSITE. Every muted colour in Nocturne is a soft() rung — `color-mix(…, transparent)` — so
 * the foreground is translucent and its effective colour is whatever it is layered over: reading
 * --soft-60 and --color-surface out of the sheet and comparing them would compare two colours,
 * neither of which is on screen. That is exactly how the 4.25:1 in issue #58 went unnoticed.
 *
 * Scoped to text on a solid ground, which is what this system paints: element `opacity`, background
 * images and gradients behind text are not handled, and there is no text over them today. The
 * general case is what the axe sweep above is for.
 */
async function contrastOf(page: Page, selector: string): Promise<Contrast> {
  return page.evaluate((sel) => {
    const element = document.querySelector(sel);
    if (element === null) {
      throw new Error(`no element matches ${sel} — this check must not pass vacuously`);
    }

    // Chromium serialises a computed colour as `rgb(r, g, b)`, `rgba(r, g, b, a)` or
    // `color(srgb r g b / a)` depending on how the source value was written; color()'s channels run
    // 0-1 where rgb()'s run 0-255, and either may spell a channel as a percentage.
    const toRgba = (value: string): [number, number, number, number] => {
      if (value === "transparent" || value === "") {
        return [0, 0, 0, 0];
      }

      const scale = value.startsWith("color(") ? 255 : 1;
      // Numbers are read from inside the parentheses only: a colour space can have a digit in its
      // name (`color(rec2020 …)`), and a function name is not a channel.
      const args = value.slice(value.indexOf("(") + 1);
      const parts = (args.match(/-?[\d.]+%?/g) ?? []).map((part) =>
        part.endsWith("%")
          ? { value: Number(part.slice(0, -1)) / 100, isPercent: true }
          : { value: Number(part), isPercent: false },
      );
      if (parts.length < 3) {
        throw new Error(`cannot read the colour ${value}`);
      }

      const channels = parts.slice(0, 3).map((part) => part.value * (part.isPercent ? 255 : scale));
      const alpha = parts.length > 3 ? parts[3].value : 1;

      return [channels[0], channels[1], channels[2], alpha];
    };

    // Every background from the element up to the first opaque one, then composited back down. A
    // translucent surface over a translucent surface is a shape this system uses, so the stack is
    // walked rather than assuming the parent is solid.
    const stack: [number, number, number, number][] = [];
    for (let node: Element | null = element; node !== null; node = node.parentElement) {
      const background = toRgba(getComputedStyle(node).backgroundColor);
      if (background[3] === 0) {
        continue;
      }
      stack.push(background);
      if (background[3] === 1) {
        break;
      }
    }

    const over = (
      top: [number, number, number, number],
      bottom: [number, number, number, number],
    ): [number, number, number, number] => [
      top[0] * top[3] + bottom[0] * (1 - top[3]),
      top[1] * top[3] + bottom[1] * (1 - top[3]),
      top[2] * top[3] + bottom[2] * (1 - top[3]),
      1,
    ];

    // White as the floor if nothing in the chain is opaque: the optimistic assumption on a dark
    // theme, so an unmeasurable ground cannot silently produce a passing number.
    const ground = stack.reduceRight<[number, number, number, number]>(
      (below, layer) => over(layer, below),
      [255, 255, 255, 1],
    );

    const style = getComputedStyle(element);
    const text = over(toRgba(style.color), ground);

    const luminance = ([r, g, b]: [number, number, number, number]): number => {
      const linear = [r, g, b].map((channel) => {
        const srgb = channel / 255;

        return srgb <= 0.03928 ? srgb / 12.92 : Math.pow((srgb + 0.055) / 1.055, 2.4);
      });

      return 0.2126 * linear[0] + 0.7152 * linear[1] + 0.0722 * linear[2];
    };

    const lighter = Math.max(luminance(text), luminance(ground));
    const darker = Math.min(luminance(text), luminance(ground));

    return {
      ratio: (lighter + 0.05) / (darker + 0.05),
      fontSizePx: Number.parseFloat(style.fontSize),
      foreground: style.color,
      background: `rgb(${String(Math.round(ground[0]))}, ${String(Math.round(ground[1]))}, ${String(Math.round(ground[2]))})`,
    };
  }, selector);
}

/** Every heading on the page, in document order. */
async function headingOutline(page: Page): Promise<{ level: number; text: string }[]> {
  return page.evaluate(() =>
    [...document.querySelectorAll("h1, h2, h3, h4, h5, h6")].map((heading) => ({
      level: Number(heading.tagName.slice(1)),
      text: (heading.textContent ?? "").trim().slice(0, 40),
    })),
  );
}

test.describe("Accessibility", () => {
  test("/_design has no unallowlisted serious or critical axe violations", async ({ page }) => {
    // A CONTENTION BUDGET, NOT A LOOSENED ASSERTION (issue #88). test.slow() triples this test's
    // timeout to 90 s; it changes nothing about what counts as a violation, and a healthy run is
    // unaffected because a timeout is a ceiling rather than a wait.
    //
    // The scan itself takes 2-11 s when this file runs alone and was observed at 44.7 s against the
    // 30 s default with the full suite in flight. `fullyParallel: true` puts every spec on the
    // machine at once and /_design is the heaviest page in the repo — every token plus a 200-row x
    // 12-column virtualised table, all of which axe walks. With `retries: 0` (deliberate: a flaky
    // e2e is quarantined, never retried) a 30 s budget against a 44.7 s worst case is a red build
    // with no product defect behind it, which is how a suite ends up `.skip`ped.
    //
    // The alternative was AxeBuilder.exclude(".virtual-table"), which buys the same headroom by
    // giving up the a11y of the surface this suite most needs to check. Headroom is cheaper.
    test.slow();

    await openDesignPage(page);

    assertWithinAllowlist(DESIGN_ROUTE, await scan(page));
  });

  // Issue #58. `.card-meta` is the smallest text in the system — --font-size-2xs, 11px — so the AA
  // floor that applies is 4.5:1, not the 3:1 large-text one. The mockup paints it soft(50), which is
  // 4.25:1 over --color-surface; web/src/components/Card.css diverges to soft(60) and says why.
  //
  // The number is asserted rather than the token, because the token is not the claim. A palette
  // change that darkened --color-text, a card that moved onto a different ground, or a rung edited
  // in tokens.css would each leave `color: var(--soft-60)` reading correctly while the text on
  // screen stopped being legible.
  test(".card-meta clears the AA contrast floor for small text", async ({ page }) => {
    await openDesignPage(page);

    const measured = await contrastOf(page, ".card-meta");

    expect(
      measured.fontSizePx,
      ".card-meta is no longer small text. Below 18.66px (or 14px bold) AA asks 4.5:1 and above it " +
        "3:1, so the floor asserted below is the wrong one for this size — re-derive it.",
    ).toBeLessThan(18.66);

    expect(
      measured.ratio,
      `.card-meta paints ${measured.foreground} on ${measured.background} at ` +
        `${measured.fontSizePx.toFixed(0)}px — ${measured.ratio.toFixed(2)}:1, under the 4.5:1 WCAG ` +
        "AA floor for text this size (issue #58). Raise the soft() rung; do not allowlist it.",
    ).toBeGreaterThanOrEqual(4.5);
  });

  // Issue #61. axe grades heading-order `moderate`, so the sweep above does not cover it and the
  // page shipped an h2 followed by h6 swatch-group titles — a level chosen for its SIZE.
  //
  // Levels may DROP by any amount (a new section legitimately returns to h2) and may only RISE by
  // one, which is the rule axe applies and the one a screen reader's heading navigation assumes.
  test("/_design heading levels never skip", async ({ page }) => {
    await openDesignPage(page);

    const outline = await headingOutline(page);

    // /_design carries ~20 headings; a floor well under that catches a selector that stopped
    // matching without failing on a section being added or removed.
    expect(
      outline.length,
      "found barely any headings on /_design — the selector stopped matching and this check would pass vacuously",
    ).toBeGreaterThan(10);

    const skips = outline
      .slice(1)
      .map((heading, index) => ({ previous: outline[index], heading }))
      .filter(({ previous, heading }) => heading.level > previous.level + 1)
      .map(
        ({ previous, heading }) =>
          `h${String(previous.level)} "${previous.text}" is followed by h${String(heading.level)} "${heading.text}"`,
      );

    expect(
      skips,
      "heading levels skip on /_design. A heading level is the outline a screen-reader user " +
        "navigates by, not a size: use the level the document needs and .label-heading for h6's " +
        "type (docs/design/09-frontend-and-design-system.md §5).",
    ).toEqual([]);
  });

  test("the open dialog has no serious or critical axe violations", async ({ page }) => {
    // The same budget as the sweep above, and for the same reason: this is a second full axe walk
    // of /_design (the modal covers the page but the DOM behind it is still there), so it carries
    // the same worst case under parallel load. See issue #88.
    test.slow();

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
