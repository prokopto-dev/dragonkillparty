#!/usr/bin/env python3
"""Fail on broken relative links in the markdown corpus.

Broken links are the highest-frequency documentation defect, and this corpus cross-references
heavily — the design docs, the ADRs and the officer guides all point at each other. External URLs
are checked by lychee in the nightly job; this runs on every PR and needs no network.

A link to a file that does not exist YET (a per-phase deliverable) is still a failure: reference it
in backticks until it exists, so the corpus never promises a page a reader cannot open.
"""

import os
import re
import sys

LINK = re.compile(r"\[[^\]]*\]\(([^)]+)\)")
# A fenced code block opens and closes on a line whose first non-space characters are ``` (or ~~~).
# Everything between an opening fence and its closing fence is CODE, not prose, and must not be
# scanned for links: a Go generic instantiation — a call whose type argument sits in square brackets
# immediately before the parenthesised argument list, e.g. `Budget[int64](t, 1)` — is
# indistinguishable from a markdown link `[int64](t, 1)` to this regex. Before this skip,
# `make docs-links` went red on Go samples inside code fences in the two most valuable documents in
# the repo (EXAMPLE_ENDPOINT.md, RECIPES.md), which is a merge-blocking gate mis-firing on correct
# content. Backticks around the token do not help; only skipping the fenced region does.
FENCE = re.compile(r"^\s*(```+|~~~+)")
SKIP_DIRS = {".git", "node_modules", "dist", ".astro", "bin"}


def main() -> int:
    root = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
    os.chdir(root)

    broken: list[tuple[str, int, str]] = []
    checked = 0

    for dirpath, dirnames, filenames in os.walk("."):
        dirnames[:] = [d for d in dirnames if d not in SKIP_DIRS]
        for fn in filenames:
            if not fn.endswith(".md"):
                continue
            path = os.path.relpath(os.path.join(dirpath, fn), ".")
            with open(path, encoding="utf-8", errors="replace") as fh:
                in_fence = False
                for lineno, line in enumerate(fh, 1):
                    if FENCE.match(line):
                        # A fence line toggles the code region on and off. The fence line itself is
                        # never prose, so it is skipped in both directions.
                        in_fence = not in_fence
                        continue
                    if in_fence:
                        continue
                    for m in LINK.finditer(line):
                        target = m.group(1).strip()
                        # Strip a title: [text](path "Title")
                        target = target.split(" ", 1)[0]
                        if target.startswith(("http://", "https://", "mailto:", "#", "<")):
                            continue
                        target = target.split("#", 1)[0]
                        if not target:
                            continue
                        checked += 1
                        resolved = os.path.normpath(os.path.join(os.path.dirname(path), target))
                        if not os.path.exists(resolved):
                            broken.append((path, lineno, target))

    if broken:
        print(f"\033[31mFAIL\033[0m {len(broken)} broken relative link(s):")
        for path, lineno, target in broken:
            print(f"  {path}:{lineno} -> {target}")
        print(
            "\nIf the target is a future deliverable, reference it in backticks rather than as a "
            "link until it exists."
        )
        return 1

    print(f"  \033[32m{checked} relative links, none broken\033[0m")
    return 0


if __name__ == "__main__":
    sys.exit(main())
