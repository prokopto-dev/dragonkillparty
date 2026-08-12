#!/usr/bin/env python3
"""Rewrite one vendored .dc.html mockup into a publishable page.

The vendored files under docs/design/mockups/ stay byte-exact so a diff against a fresh export is
readable. Everything needed to publish them happens here, on a copy:

  1. point the runtime <script> at our own harness instead of the design tool's
  2. drop the design tool's no-op namespace bundle
  3. rewrite the design system's stylesheet path to the layout this repo uses
  4. strip type="text/x-dc" so the authored logic executes as an ordinary classic script
  5. lift <sc-for>/<sc-if> onto their child element where the child is unambiguous
  6. inject the "MOCKUP - not a live instance" banner
  7. inject <meta name="robots" content="noindex">

Step 5 is the one with teeth. An unknown element inside a <table> is *foster-parented* out of the
table by the HTML parser — the browser hoists <sc-for> above the <table> and then discards the <tr>
children it contained, because a <tr> is meaningless outside table context. 37 of the mockups'
tables loop their rows that way, so left alone they render headers and no body. Moving the directive
onto the repeated element itself (<tr data-sc-for="..." data-sc-as="...">) is valid HTML everywhere
and survives parsing intact.

Usage: dc-publish.py <src> <dest> <surface-title>
"""

from __future__ import annotations

import re
import sys
from pathlib import Path

# The repository's Python floor. See the Makefile's PYTHON_REQUIRED for why it is 3.9 and not 3.10;
# test/repo/python_floor_test.go asserts every scripts/*.py declares the same number and still
# parses at it. This file already had the future import above, which is what kept it off the list of
# scripts issue #83 was about — the guard is here so the floor is declared everywhere it applies.
MINIMUM_PYTHON = (3, 9)

if sys.version_info < MINIMUM_PYTHON:
    sys.stderr.write(
        "dc-publish.py needs Python %d.%d or newer; this is Python %d.%d.%d (%s).\n"
        "No page was written. Re-run `make mockup-site` with a newer interpreter.\n"
        % (MINIMUM_PYTHON + sys.version_info[:3] + (sys.executable or "python3",))
    )
    sys.exit(2)

VOID = {
    "area", "base", "br", "col", "embed", "hr", "img", "input",
    "link", "meta", "param", "source", "track", "wbr",
}

# Opening/closing tags of the two directives, non-greedy so an attribute containing '>' cannot
# swallow the rest of the document.
SC_TAG = re.compile(r"<(/?)(sc-for|sc-if)\b([^>]*)>")
ANY_TAG = re.compile(r"<(/?)([a-zA-Z][\w-]*)")
ATTR = re.compile(r'([a-zA-Z_:][-\w:.]*)\s*=\s*"([^"]*)"')


def attrs_of(s: str) -> dict[str, str]:
    return {m.group(1).lower(): m.group(2) for m in ATTR.finditer(s)}


def open_tag_end(s: str, start: int) -> int:
    """Index of the '>' closing the tag that begins at `start`, ignoring quoted attribute values."""
    quote = None
    for i in range(start, len(s)):
        c = s[i]
        if quote:
            if c == quote:
                quote = None
        elif c in "\"'":
            quote = c
        elif c == ">":
            return i
    return -1


def top_level_children(inner: str) -> list[re.Match]:
    """Opening tags of the immediate element children of `inner`.

    Self-closing tags must not open a level. Miss that and every sibling after an `<x/>` is counted
    one level too deep, so a multi-child block looks like a single-child one and the directive gets
    lifted onto the first child while the rest silently escape the loop.
    """
    kids, depth = [], 0
    for t in ANY_TAG.finditer(inner):
        closing, name = t.group(1), t.group(2).lower()
        if closing:
            depth -= 1
            continue
        if depth == 0:
            kids.append(t)
        gt = open_tag_end(inner, t.start())
        self_closing = gt != -1 and inner[gt - 1] == "/"
        if name not in VOID and not self_closing:
            depth += 1
    return kids


def lift_directives(s: str) -> str:
    """Rewrite <sc-for>/<sc-if> onto their single child element, innermost blocks included."""
    out, pos = [], 0
    while True:
        m = SC_TAG.search(s, pos)
        if not m:
            out.append(s[pos:])
            return "".join(out)
        if m.group(1):  # an unmatched close; copy through
            out.append(s[pos:m.end()])
            pos = m.end()
            continue

        out.append(s[pos:m.start()])

        depth, close = 1, None
        for m2 in SC_TAG.finditer(s, m.end()):
            depth += -1 if m2.group(1) else 1
            if depth == 0:
                close = m2
                break
        if close is None:  # unbalanced; leave the tag alone rather than corrupt the document
            out.append(s[m.start():m.end()])
            pos = m.end()
            continue

        kind = m.group(2)
        a = attrs_of(m.group(3))
        inner = lift_directives(s[m.end():close.start()])
        kids = top_level_children(inner)

        # Never lift onto another directive element: <sc-if><sc-for>…</sc-for></sc-if> would put
        # data-sc-if on the <sc-for>, and the runtime's element-form branch returns before it looks
        # at data-* attributes — silently dropping the condition. Keep the outer element form.
        nested_directive = bool(kids) and kids[0].group(2).lower() in ("sc-for", "sc-if")

        if len(kids) == 1 and not nested_directive:
            if kind == "sc-for":
                injected = ' data-sc-for="{}" data-sc-as="{}"'.format(
                    a.get("list", ""), a.get("as", "item")
                )
            else:
                injected = ' data-sc-if="{}"'.format(a.get("value", ""))
            tag_start = kids[0].start()
            gt = open_tag_end(inner, tag_start)
            if gt == -1:
                raise SystemExit("dc-publish: unterminated tag in <%s> block" % kind)
            insert_at = gt - 1 if inner[gt - 1] == "/" else gt
            out.append(inner[:insert_at] + injected + inner[insert_at:])
        else:
            # Several siblings under one condition: keep the element form. The caller asserts this
            # never lands inside table context, where it would be foster-parented.
            out.append("<{}{}>{}</{}>".format(kind, m.group(3), inner, kind))

        pos = close.end()


TABLE_CTX = {"table", "thead", "tbody", "tfoot", "tr"}


def assert_no_directive_in_table(template: str, name: str) -> None:
    """Raise if any <sc-for>/<sc-if> element survives inside table context.

    This is the whole reason lift_directives() exists, so it is checked properly rather than by
    looking at a fixed window of characters: walk the template keeping a stack of open elements and
    raise the moment a directive opens while a table element is anywhere below it.
    """
    stack: list[str] = []
    for t in ANY_TAG.finditer(template):
        closing, tag = t.group(1), t.group(2).lower()
        if closing:
            if tag in stack:  # tolerate the unclosed tags a real document contains
                while stack and stack.pop() != tag:
                    pass
            continue
        if tag in ("sc-for", "sc-if") and any(s in TABLE_CTX for s in stack):
            enclosing = next(s for s in reversed(stack) if s in TABLE_CTX)
            line = template.count("\n", 0, t.start()) + 1
            raise SystemExit(
                "dc-publish: <%s> survives inside <%s> at %s:%d — the HTML parser will "
                "foster-parent it out of the table and drop the rows it contains. It needs to be "
                "lifted onto its child element." % (tag, enclosing, name, line)
            )
        gt = open_tag_end(template, t.start())
        self_closing = gt != -1 and template[gt - 1] == "/"
        if tag not in VOID and not self_closing:
            stack.append(tag)


BANNER_STYLE = """<style id="dkp-mockup-banner-style">
body{padding-bottom:38px}
sc-if,sc-for{display:contents}
#dkp-mockup-banner{position:fixed;left:0;right:0;bottom:0;z-index:2147483647;
  display:flex;align-items:center;gap:10px;flex-wrap:wrap;
  padding:8px 16px;font:500 12px/1.3 Inter,system-ui,sans-serif;
  color:#e9e9ed;background:#2b2741;border-top:1px solid #5d5294;
  box-shadow:0 -6px 18px rgba(0,0,0,.45)}
#dkp-mockup-banner strong{letter-spacing:.14em;font-size:10px;color:#161826;
  background:#b5abfc;border-radius:4px;padding:3px 7px}
#dkp-mockup-banner span{opacity:.72}
#dkp-mockup-banner .dkp-mockup-surface{margin-left:auto;opacity:.5}
#dkp-mockup-banner a{color:#d2cefd;text-decoration:none;
  border-bottom:1px solid rgba(210,206,253,.35)}
#dkp-mockup-banner a:hover{color:#f5f4ff}
@media print{#dkp-mockup-banner{display:none}}
</style>"""

BANNER = (
    '<div id="dkp-mockup-banner">'
    "<strong>MOCKUP</strong>"
    "<span>&mdash; not a live instance. Static design reference; nothing here is wired "
    "to a server.</span>"
    '<span class="dkp-mockup-surface">{title}</span>'
    '<a href="./index.html">All surfaces</a>'
    '<a href="https://github.com/prokopto-dev/dragonkillparty">Repository</a>'
    "</div>"
)

# Every surface carries its own noindex. The mockups are fabricated guild data for an unreleased
# product, and a stray search result would read as a live instance — the banner says "not a live
# instance", but a search snippet does not show the banner.
#
# It has to be per page. index.html has had this by hand since the site was created, but noindex
# does not propagate to the pages it links to, so the five surfaces were indexable. A robots.txt
# cannot cover them either: Pages serves this repo as a *project* site under /dragonkillparty/, and
# crawlers only read robots.txt at the origin root — which belongs to a different repository. Nor
# can we set X-Robots-Tag, since Pages does not let us add response headers. The meta tag is the
# only mechanism available here, which is why [MOCK004] enforces it rather than trusting it.
ROBOTS = '<meta name="robots" content="noindex">'


def main() -> None:
    if len(sys.argv) != 4:
        raise SystemExit(__doc__)
    src, dest, title = Path(sys.argv[1]), Path(sys.argv[2]), sys.argv[3]
    html = src.read_text(encoding="utf-8")

    # 1 + 2 — our harness replaces the design tool's runtime; its namespace bundle is a no-op stub.
    html = html.replace(
        '<script src="./support.js"></script>',
        '<script src="./mockup-runtime.js"></script>\n<script src="./ios-frame.js"></script>',
    )
    html = re.sub(r'\s*<script src="_ds/[^"]*/_ds_bundle\.js"></script>', "", html)

    # 3 — the design system lives at nocturne/ here, not under the tool's _ds/<uuid>/ layout.
    html = re.sub(r'(?:\./)?_ds/[^"]*/styles\.css', "./nocturne/styles.css", html)

    # 4 — let the authored logic run as a classic script, so the runtime needs no evaluator.
    html = html.replace('<script type="text/x-dc" data-dc-script', "<script data-dc-script")

    # 5 — lift the directives, over the template only. The trailing <script> holds authored JS whose
    # string literals must not be touched.
    split = html.find("<script data-dc-script")
    if split == -1:
        raise SystemExit("dc-publish: no data-dc-script block in %s" % src)
    template, tail = html[:split], html[split:]
    before = len(SC_TAG.findall(template))
    template = lift_directives(template)
    after = len(SC_TAG.findall(template))
    html = template + tail

    assert_no_directive_in_table(template, src.name)

    # 6 + 7 — the banner (plus the styles that keep it clear of the mockups' own sticky chrome) and
    # the noindex. Both go in the head, so the absence of one is checked once, here: str.replace
    # returns the string unchanged when there is no match, which would drop both silently.
    if "</head>" not in html:
        raise SystemExit("dc-publish: no </head> in %s" % src)
    html = html.replace("</head>", ROBOTS + "\n" + BANNER_STYLE + "\n</head>", 1)

    body = re.search(r"<body[^>]*>", html)
    if not body:
        raise SystemExit("dc-publish: no <body> in %s" % src)
    html = html[: body.end()] + "\n" + BANNER.format(title=title) + html[body.end():]

    dest.write_text(html, encoding="utf-8")
    print("  built {:<24} {:<15} ({} directives lifted)".format(src.name, "(%s)" % title, before - after))


if __name__ == "__main__":
    main()
