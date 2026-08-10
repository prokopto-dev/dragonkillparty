// DELIBERATE VIOLATIONS of canonical §17: raw hex and raw px outside the token layer.
//
// This file must FAIL eslint on no-restricted-syntax. scripts/lint-web-fixtures.sh runs eslint over it
// with --no-ignore in CI's `lint / web` job and fails if it PASSES, and
// test/repo/web_lint_test.go asserts the same thing on a laptop. A fixture that stops tripping its rule
// means the AST half of the token-layer rule has gone blind — DS001/DS002 in scripts/repo-gates.sh
// only see CSS, so nothing else would notice.
//
// It lives OUTSIDE web/src so neither the DS gates nor the project's own `eslint .` flags it (it is in
// eslint.config.js's `ignores`).
//
// Each line below is one of the shapes the rule has to catch. The prose above deliberately mentions
// 4px and #ff0000 to prove comments are not what trips it: an AST rule sees literals, not text.

export function RawValues() {
  // A literal that is entirely a length, in a style attribute.
  const pad = "4px";
  // A literal that is entirely a colour.
  const brand = "#9184d9";

  // The BACKTICK spelling of both. This is the shape that bypassed the rule until review caught it:
  // the template elements are not syntactically inside the `style` attribute, so neither the anchored
  // Literal selectors nor the descendant style selector saw them, and eslint exited 0 on this exact
  // file. Both anchored selectors now match TemplateElement as well.
  const gap = `4px`;
  const tint = `#fff`;

  return (
    <div>
      <span style={{ gap, color: tint }}>indirect via template literal</span>
      <span style={{ padding: pad, color: brand }}>indirect</span>
      {/* Inline, the most likely real-world shape. */}
      <span style={{ padding: "8px" }}>inline length</span>
      <span style={{ color: "#e9e9ed" }}>inline colour</span>
      {/* Multi-value, so the anchored selectors alone would miss it. */}
      <span style={{ border: "1px solid #3f424d" }}>multi-value</span>
      {/* The template-literal spelling. */}
      <span style={{ margin: `0 ${"2"}px 12px` }}>template</span>
      {/* The NUMERIC spelling. React serialises these as `4px` / `-4px`, so they are the same
          violation with the unit left implicit — and they were the shape the rule's own comment used
          to call sanctioned. Both the plain and the unary-minus form must trip. */}
      <span style={{ padding: 4 }}>numeric length</span>
      <span style={{ marginTop: -4 }}>negative numeric length</span>
      {/* Prose containing 4px and #ff0000 is NOT a violation — it is JSX text, not a literal. */}
      <span>Base unit 4px x 0.70, accent #ff0000</span>
    </div>
  );
}
