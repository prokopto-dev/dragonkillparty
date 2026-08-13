// mockup-runtime.js — a clean-room renderer for the .dc.html design mockups.
//
// The mockups were authored in a design tool whose runtime is third-party and unlicensed, so it is
// not vendored here (see NOTICE and ../README.md). This file implements the same template contract
// from scratch, in ~230 lines of dependency-free DOM code, so the mockups can be published as a
// static site under this repository's own licence.
//
// It renders exactly the surface the five mockups use and nothing more:
//
//   {{ path }}                          interpolation in text nodes and attribute values
//   <sc-for list="{{ p }}" as="x">      repeat children, binding `x` (and `x_index`)
//   <sc-if value="{{ p }}">             render children when truthy
//   <helmet>                            contents are hoisted into <head> once, at mount
//   <x-import component-from-global-scope="Name" ...>   render window.Name(props, childrenFragment)
//   onClick / onChange / onScroll       bound to a function resolved from the same path space
//
// Three of those have an equivalent ATTRIBUTE form — data-sc-for/data-sc-as, data-sc-if and
// data-sc-import/data-sc-prop-* — carried on the element the block wraps. Nothing authors them: the
// build step (internal/mockup) lifts a single-child block onto its child wherever the element form
// would be foster-parented out of a <table> and arrive empty. See the notes at each branch below.
//
// EVERY binding in the five mockups is a plain dotted path or a literal (`true`, `false`, a number)
// — verified by survey, and asserted by internal/mockup (MOCK001) before a build proceeds. So
// there is deliberately **no expression evaluator**: no eval, no `new Function`, no Babel. A binding
// the resolver cannot walk renders as an empty string rather than executing anything.
//
// The authored logic is an ordinary class in a classic <script>:
//
//   class Component extends DCLogic { state = {...}; renderVals() { return {...} } }
//
// Classic scripts share the global lexical scope, so the runtime picks `Component` up by name after
// parsing. The build step strips `type="text/x-dc"` from that tag so the browser executes it
// normally — which is why no dynamic evaluation is needed here.

(() => {
  'use strict';

  // Base class the authored component extends. Must exist as a global before the inline script runs,
  // which is why this file is loaded from <head>.
  class DCLogic {
    constructor(props) {
      this.props = props || {};
      this.__mounted = false;
    }
    setState(patch) {
      this.state = Object.assign({}, this.state, patch);
      if (this.__mounted) this.__render();
    }
    renderVals() {
      return {};
    }
  }
  window.DCLogic = DCLogic;

  const BINDING = /\{\{\s*([^}]*?)\s*\}\}/g;
  const WHOLE_BINDING = /^\{\{\s*([^}]*?)\s*\}\}$/;
  const BOOLEAN_ATTRS = new Set(['disabled', 'checked', 'selected', 'readonly']);
  // Design-tool authoring hints. They carry no runtime meaning; drop them rather than emitting
  // unknown attributes into the published DOM.
  const HINT_ATTRS = /^hint-/;

  // Resolve a dotted path against the loop scope first, then the renderVals() output.
  // Literals are returned as themselves. An unresolvable path yields undefined, never a throw —
  // one bad binding should not blank a whole screen.
  function resolve(path, scope, vals) {
    if (path === 'true') return true;
    if (path === 'false') return false;
    if (path === 'null') return null;
    if (/^-?\d+(\.\d+)?$/.test(path)) return Number(path);

    const parts = path.split('.');
    let cur = Object.prototype.hasOwnProperty.call(scope, parts[0]) ? scope : vals;
    for (const part of parts) {
      if (cur === null || cur === undefined) return undefined;
      cur = cur[part];
    }
    return cur;
  }

  function interpolate(str, scope, vals) {
    return str.replace(BINDING, (_, path) => {
      const v = resolve(path, scope, vals);
      return v === undefined || v === null ? '' : String(v);
    });
  }

  // A whole-value binding keeps its native type (a function for onClick, a boolean for disabled).
  // A partial one — style="flex:{{ s.flex }};min-width:0" — is string interpolation.
  function attrValue(raw, scope, vals) {
    const whole = raw.match(WHOLE_BINDING);
    if (whole) return resolve(whole[1], scope, vals);
    return interpolate(raw, scope, vals);
  }

  function renderChildren(node, scope, vals, out) {
    for (const child of Array.from(node.childNodes)) renderNode(child, scope, vals, out);
  }

  function renderNode(node, scope, vals, out) {
    if (node.nodeType === Node.TEXT_NODE) {
      const text = node.nodeValue;
      out.appendChild(document.createTextNode(text.includes('{{') ? interpolate(text, scope, vals) : text));
      return;
    }
    if (node.nodeType === Node.COMMENT_NODE) return;
    if (node.nodeType !== Node.ELEMENT_NODE) return;

    const tag = node.tagName.toLowerCase();

    if (tag === 'helmet') return; // hoisted once at mount

    // Attribute form. The build step rewrites <sc-for>/<sc-if> onto their child element wherever
    // the child is unambiguous, because an unknown element inside a <table> is foster-parented out
    // of it by the HTML parser — which silently drops every <tr> in 37 of the mockups' tables.
    // data-sc-for repeats this element; data-sc-if is re-evaluated per iteration.
    if (node.hasAttribute('data-sc-for')) {
      const list = attrValue(node.getAttribute('data-sc-for'), scope, vals);
      const as = node.getAttribute('data-sc-as') || 'item';
      if (!Array.isArray(list)) return;
      list.forEach((item, i) => {
        const inner = Object.assign({}, scope);
        inner[as] = item;
        inner[as + '_index'] = i;
        if (node.hasAttribute('data-sc-if') && !attrValue(node.getAttribute('data-sc-if'), inner, vals)) return;
        renderElement(node, inner, vals, out);
      });
      return;
    }
    if (node.hasAttribute('data-sc-if')) {
      if (!attrValue(node.getAttribute('data-sc-if'), scope, vals)) return;
      renderElement(node, scope, vals, out);
      return;
    }

    // Element form, still used where a block wraps several siblings (only ever outside tables —
    // the build step asserts it).
    if (tag === 'sc-for') {
      const list = attrValue(node.getAttribute('list') || '', scope, vals);
      const as = node.getAttribute('as') || 'item';
      if (!Array.isArray(list)) return;
      list.forEach((item, i) => {
        const inner = Object.assign({}, scope);
        inner[as] = item;
        inner[as + '_index'] = i;
        renderChildren(node, inner, vals, out);
      });
      return;
    }

    if (tag === 'sc-if') {
      if (attrValue(node.getAttribute('value') || '', scope, vals)) {
        renderChildren(node, scope, vals, out);
      }
      return;
    }

    renderElement(node, scope, vals, out);
  }

  const DIRECTIVE_ATTRS = new Set(['data-sc-for', 'data-sc-as', 'data-sc-if']);
  // The attribute form of <x-import>, and the prefix its props are carried under. The build step
  // rewrites the element into these wherever the element form cannot survive the parser — inside a
  // <table>, where an unknown element is foster-parented out and arrives empty, exactly as <sc-for>
  // is. One attribute per prop rather than one serialised blob, so a value stays a value.
  const IMPORT_ATTR = 'data-sc-import';
  const IMPORT_PROP = 'data-sc-prop-';

  // Render an imported component: window[Name](props, childrenFragment). Shared by both forms, so
  // there is one answer to "what happens when the component is missing".
  function mountImport(name, props, children, out) {
    const comp = name && window[name];
    if (typeof comp === 'function') {
      const rendered = comp(props, children);
      if (rendered) out.appendChild(rendered);
      return;
    }
    // Missing component: emit the children bare rather than losing the screen.
    console.warn('mockup-runtime: no global component named', name);
    out.appendChild(children);
  }

  function renderElement(node, scope, vals, out) {
    const tag = node.tagName.toLowerCase();

    if (tag === 'x-import') {
      const props = {};
      for (const attr of Array.from(node.attributes)) {
        if (attr.name === 'component-from-global-scope' || attr.name === 'from') continue;
        if (HINT_ATTRS.test(attr.name)) continue;
        props[attr.name] = attrValue(attr.value, scope, vals);
      }
      const inner = document.createDocumentFragment();
      renderChildren(node, scope, vals, inner);
      mountImport(node.getAttribute('component-from-global-scope'), props, inner, out);
      return;
    }

    // Attribute form. The element carrying it IS the single child the <x-import> wrapped, so it is
    // rendered INTO the children fragment rather than into `out`, and what lands in `out` is
    // whatever the component returns — the same shape the element form produces.
    if (node.hasAttribute(IMPORT_ATTR)) {
      const props = {};
      for (const attr of Array.from(node.attributes)) {
        if (!attr.name.startsWith(IMPORT_PROP)) continue;
        const key = attr.name.slice(IMPORT_PROP.length);
        if (HINT_ATTRS.test(key)) continue;
        props[key] = attrValue(attr.value, scope, vals);
      }
      const inner = document.createDocumentFragment();
      renderTag(node, scope, vals, inner);
      mountImport(node.getAttribute(IMPORT_ATTR), props, inner, out);
      return;
    }

    renderTag(node, scope, vals, out);
  }

  function renderTag(node, scope, vals, out) {
    const tag = node.tagName.toLowerCase();
    const el = document.createElement(tag);
    for (const attr of Array.from(node.attributes)) {
      const { name, value } = attr;
      if (HINT_ATTRS.test(name) || DIRECTIVE_ATTRS.has(name)) continue;
      if (name === IMPORT_ATTR || name.startsWith(IMPORT_PROP)) continue;

      // The mockups author these as onClick/onChange/onScroll, but the HTML parser lowercases every
      // attribute name, so match on the lowercased form. `continue` unconditionally: a handler that
      // did not resolve to a function must never fall through to setAttribute, which would stringify
      // it into an inline on* attribute.
      if (/^on[a-z]+$/.test(name)) {
        const fn = attrValue(value, scope, vals);
        if (typeof fn === 'function') el.addEventListener(name.slice(2), fn);
        continue;
      }
      const resolved = attrValue(value, scope, vals);
      if (BOOLEAN_ATTRS.has(name)) {
        if (resolved) el.setAttribute(name, '');
        continue;
      }
      if (resolved === undefined || resolved === null || resolved === false) continue;
      el.setAttribute(name, String(resolved));
    }
    renderChildren(node, scope, vals, el);
    out.appendChild(el);
  }

  function hoistHelmet(template) {
    for (const helmet of Array.from(template.querySelectorAll('helmet'))) {
      for (const child of Array.from(helmet.childNodes)) {
        if (child.nodeType === Node.ELEMENT_NODE) {
          // A <script> or <link> moved with appendChild does not execute/apply reliably across
          // browsers once it has been parsed, so re-create it.
          const fresh = document.createElement(child.tagName.toLowerCase());
          for (const a of Array.from(child.attributes)) fresh.setAttribute(a.name, a.value);
          fresh.textContent = child.textContent;
          document.head.appendChild(fresh);
        }
      }
      helmet.remove();
    }
  }

  function mount() {
    const host = document.querySelector('x-dc');
    if (!host) return;

    const scriptEl = document.querySelector('script[data-dc-script]');
    let props = {};
    if (scriptEl && scriptEl.dataset.props) {
      try {
        const declared = JSON.parse(scriptEl.dataset.props);
        for (const [key, spec] of Object.entries(declared)) props[key] = spec && spec.default;
      } catch (err) {
        console.warn('mockup-runtime: could not parse data-props', err);
      }
    }

    // Snapshot the authored template, then hoist <helmet> out of it before the first render.
    const template = document.createElement('div');
    template.innerHTML = host.innerHTML;
    hoistHelmet(template);

    const Ctor = window.Component || (typeof Component !== 'undefined' ? Component : null);
    if (typeof Ctor !== 'function') {
      console.error('mockup-runtime: no Component class found');
      return;
    }

    const instance = new Ctor(props);
    instance.__render = () => {
      const vals = instance.renderVals() || {};
      const out = document.createDocumentFragment();
      renderChildren(template, {}, vals, out);
      host.replaceChildren(out);
    };
    instance.__mounted = true;
    instance.__render();
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', mount);
  } else {
    mount();
  }
})();
