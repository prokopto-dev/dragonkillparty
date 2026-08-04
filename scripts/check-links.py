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
                for lineno, line in enumerate(fh, 1):
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
