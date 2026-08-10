// @ts-check
import js from "@eslint/js";
import tseslint from "typescript-eslint";

// Law 4: web/src contains no fetch/XMLHttpRequest outside web/src/api, and a useEffect body never
// contains a fetch. Two mechanisms guard law 4 and they are deliberately redundant:
//
//   1. scripts/repo-gates.sh WEB001 — a grep that fires in CI's `lint / repo` job with no Node
//      toolchain at all. It cannot see a useEffect-wrapped fetch (grep has no AST), so it is the
//      floor, not the ceiling.
//   2. this config — the AST-aware half. It catches the useEffect-fetch shape the grep cannot, and
//      it runs in `lint / web`.
//
// Both must stay. Removing either is a law-4 regression. The negative fixtures live under
// web/test-fixtures/lint/ — OUTSIDE web/src, so neither the WEB001 grep nor the project's own
// `eslint .` flags them (they are in `ignores` below) — and test/repo/web_lint_test.go runs eslint
// against them with --no-ignore, requiring a non-zero exit that names the rule.
const bannedFetchGlobals = {
  // no-restricted-globals reports a bare reference to the named global. `fetch(...)` and
  // `new XMLHttpRequest()` both resolve the identifier as a global here, because the generated
  // client is the only sanctioned caller and it lives under src/api (exempted below).
  fetch:
    "Use the generated client from @/api, never a bare fetch. See .claude/rules/web.md — the SPA is an API client.",
  XMLHttpRequest:
    "Use the generated client from @/api, never XMLHttpRequest. See .claude/rules/web.md.",
};

// A useEffect (or React.useEffect) whose callback body mentions a fetch call. This is the shape the
// grep gate cannot see: it re-fetches on every render path nobody thought about, has no
// cancellation, no dedupe, no cache and no error boundary. Reads go through useQuery.
const useEffectFetchSelector =
  "CallExpression[callee.name='useEffect'] :matches(CallExpression[callee.name='fetch'], NewExpression[callee.name='XMLHttpRequest']), " +
  "CallExpression[callee.property.name='useEffect'] :matches(CallExpression[callee.name='fetch'], NewExpression[callee.name='XMLHttpRequest'])";

// Canonical §17: raw hex and raw `px` live in the token layer and nowhere else in web/src.
//
// This is the AST half. DS001/DS002 in scripts/repo-gates.sh are the CSS half, and the split is the
// same one law 4 uses above, for the same reason: a grep over *.tsx cannot tell a value from prose —
// web/src/routes/design.tsx renders the sentences "Base unit 4px x 0.70" and "a 1px accent border on
// transparent" as visible copy — but an AST can, because JSX text is not a string literal. So the
// grep covers CSS, this covers code, and between them the rule holds over all of web/src as §17
// states. A CSS-only gate could not honestly be described as enforcing it.
//
// Two shapes are matched, and the pair is what makes this precise rather than noisy:
//
//   1. A value that is ENTIRELY a length or a colour, anywhere — `const PAD = "4px"`,
//      `{ color: "#ff0000" }`. Anchored, so a sentence that merely contains "4px" is untouched.
//   2. ANY length or colour inside a `style` attribute, anchored or not — `style={{ padding: "0 4px" }}`,
//      `style={{ border: "1px solid #fff" }}`. Inside a style attribute there is no prose to confuse
//      it with.
//
// BOTH SHAPES MATCH `Literal` AND `TemplateElement`, and the second half of that is not decoration.
// An earlier version anchored only on `Literal`, so the backtick spelling walked straight through:
//
//   const gap = `4px`;  const brand = `#fff`;  <div style={{ gap, color: brand }} />
//
// Neither template element is syntactically inside the `style` attribute, so the descendant selector
// could not see them either, and eslint exited 0 on exactly that file. Found in review; the fixture
// now carries it.
//
// A NUMBER IN A STYLE OBJECT IS ALSO A RAW px, and an earlier version of this comment wrongly called
// numbers a sanctioned shape. React serialises `style={{ padding: 4 }}` as `padding: 4px` for every
// length-valued property, so a bare number is the same violation with the unit left implicit — and it
// was the one spelling the rule steered authors towards. Found in review.
//
// The distinction that has to be preserved is design value vs runtime measurement:
//
//   style={{ padding: 4 }}      a design value with the unit hidden        BANNED
//   style={{ height }}          a measured length from the virtualizer     fine, and load-bearing
//   style={{ padding: 0 }}      zero needs no unit and no token           fine
//   style={{ opacity: 0.8 }}    React adds no unit to these properties    fine
//
// So the rule bans a non-zero numeric LITERAL, exempts React's own unitless property list, and says
// nothing about identifiers or expressions — a computed layout measurement is not a design token and
// there is no rung for it.
const HEX_BODY = "#(?:[0-9a-fA-F]{3,4}|[0-9a-fA-F]{6}|[0-9a-fA-F]{8})";
const PX_BODY = "-?[0-9]+(?:\\.[0-9]+)?px";
// Unanchored, for use inside a `style` attribute where a multi-value declaration is ordinary.
const LOOSE_VALUE = "[0-9]px|#[0-9a-fA-F]{3}";

// React's unitless properties, verbatim from its own CSSProperty list. A number here is not a length,
// so it is not a hidden px. Kept complete rather than trimmed to the ones in use: a partial list would
// make the rule fire on correct code, and a rule that is sometimes wrong gets disabled.
const REACT_UNITLESS = [
  "animationIterationCount",
  "aspectRatio",
  "borderImageOutset",
  "borderImageSlice",
  "borderImageWidth",
  "boxFlex",
  "boxFlexGroup",
  "boxOrdinalGroup",
  "columnCount",
  "columns",
  "flex",
  "flexGrow",
  "flexPositive",
  "flexShrink",
  "flexNegative",
  "flexOrder",
  "gridArea",
  "gridRow",
  "gridRowEnd",
  "gridRowSpan",
  "gridRowStart",
  "gridColumn",
  "gridColumnEnd",
  "gridColumnSpan",
  "gridColumnStart",
  "fontWeight",
  "lineClamp",
  "lineHeight",
  "opacity",
  "order",
  "orphans",
  "scale",
  "tabSize",
  "widows",
  "zIndex",
  "zoom",
  "fillOpacity",
  "floodOpacity",
  "stopOpacity",
  "strokeDasharray",
  "strokeDashoffset",
  "strokeMiterlimit",
  "strokeOpacity",
  "strokeWidth",
].join("|");

// Non-zero numeric literal, and its unary-minus spelling (`marginTop: -4` parses as a UnaryExpression,
// so a Literal-only selector would miss it).
const numericLengthInStyleSelector =
  `JSXAttribute[name.name='style'] Property:not([key.name=/^(?:${REACT_UNITLESS})$/]) > Literal[value!=0][raw=/^[0-9]/], ` +
  `JSXAttribute[name.name='style'] Property:not([key.name=/^(?:${REACT_UNITLESS})$/]) > UnaryExpression[operator='-'] > Literal[value!=0]`;

const numericLengthMessage =
  "A number in a style object is a raw px — React serialises `padding: 4` as `padding: 4px` " +
  "(canonical §17). Use var(--rung) from web/src/styles/tokens.css. A measured runtime length may be " +
  "passed as an identifier or expression; 0 and React's unitless properties are exempt.";

const rawHexAnywhereSelector =
  `Literal[value=/^${HEX_BODY}$/], TemplateElement[value.raw=/^${HEX_BODY}$/]`;
const rawPxAnywhereSelector =
  `Literal[value=/^${PX_BODY}$/], TemplateElement[value.raw=/^${PX_BODY}$/]`;
const rawValueInStyleSelector =
  `JSXAttribute[name.name='style'] Literal[value=/${LOOSE_VALUE}/], ` +
  `JSXAttribute[name.name='style'] TemplateElement[value.raw=/${LOOSE_VALUE}/]`;

const tokenLayerMessage =
  "Raw hex and raw px belong in web/src/styles/tokens.css, never in a component (canonical §17). " +
  "A value the scale does not carry gets a named rung there; reference it as var(--rung).";

// The law-4 and token-layer rule sets, shared by src/** and the negative fixtures so the fixtures are
// held to exactly the rules they are meant to trip.
//
// Both rule sets live in ONE `no-restricted-syntax` entry because ESLint takes a single options array
// per rule — declaring the rule twice silently keeps only the last, which would disable law 4's
// useEffect-fetch selector and leave every existing test still passing.
const lawFourRules = {
  "no-restricted-globals": ["error", ...objectToRestrictedGlobals(bannedFetchGlobals)],
  "no-restricted-syntax": [
    "error",
    {
      selector: useEffectFetchSelector,
      message:
        "A useEffect containing a fetch is banned — use useQuery/useSuspenseQuery. See .claude/rules/web.md.",
    },
    { selector: rawHexAnywhereSelector, message: tokenLayerMessage },
    { selector: rawPxAnywhereSelector, message: tokenLayerMessage },
    { selector: rawValueInStyleSelector, message: tokenLayerMessage },
    { selector: numericLengthInStyleSelector, message: numericLengthMessage },
  ],
};

export default tseslint.config(
  {
    // web/dist is build output; node_modules is dependencies; src/api is generated; test-fixtures
    // are DELIBERATE violations the negative test runs eslint against in isolation (with --no-ignore
    // so this exclusion does not hide them from the test). None is hand-authored source that the
    // project's own `eslint .` should flag, so none is linted by a bare run.
    ignores: ["dist/**", "node_modules/**", "src/api/**", "test-fixtures/**"],
  },
  js.configs.recommended,
  ...tseslint.configs.recommended,
  {
    files: ["src/**/*.{ts,tsx}"],
    languageOptions: {
      parserOptions: {
        ecmaFeatures: { jsx: true },
      },
    },
    rules: lawFourRules,
  },
  {
    // The negative fixtures. Ignored by a bare `eslint .` (above), so this block only takes effect
    // when the test targets a fixture with --no-ignore. Applying the same rule set here is what makes
    // the test assert the real rule rather than a fixture-only stand-in.
    files: ["test-fixtures/lint/**/*.{ts,tsx}"],
    languageOptions: {
      parserOptions: {
        ecmaFeatures: { jsx: true },
      },
    },
    rules: lawFourRules,
  },
  {
    // src/api is the ONE place the client may hold the HTTP primitives openapi-fetch is built on
    // (schema.d.ts is generated; client.ts instantiates openapi-fetch). It is already ignored above;
    // this block documents the exemption so a future hand-written helper under src/api does not look
    // like an accident.
    files: ["src/api/**/*.{ts,tsx}"],
    rules: {
      "no-restricted-globals": "off",
    },
  },
);

// no-restricted-globals wants a flat list of {name, message} entries; this keeps the two banned
// names and their messages together above rather than duplicated into the rule options.
function objectToRestrictedGlobals(globals) {
  return Object.entries(globals).map(([name, message]) => ({ name, message }));
}
