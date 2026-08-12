#!/usr/bin/env python3
"""Fail on broken relative links in the markdown corpus.

Broken links are the highest-frequency documentation defect, and this corpus cross-references
heavily — the design docs, the ADRs and the officer guides all point at each other. External URLs
are checked by lychee in the nightly job; this runs on every PR and needs no network.

A link to a file that does not exist YET (a per-phase deliverable) is still a failure: reference it
in backticks until it exists, so the corpus never promises a page a reader cannot open.
"""

# PEP 563, and it is load-bearing rather than stylistic (issue #83): an annotation is evaluated when
# its function is DEFINED, so `-> dict | None` raises TypeError on 3.9 and a module-level `list[str]`
# raises on 3.8 — at IMPORT, before the script reads a single byte of its subject. What the
# contributor then sees is the GATE failing on a corpus that is fine, which is an environment fault
# wearing a content fault's clothes. With this import every annotation is a string nobody evaluates,
# and the floor below is about the interpreter rather than about the type syntax. This file annotates
# with `list[tuple[...]]`. Every scripts/*.py carries the import — test/repo/python_floor_test.go
# fails if one does not — so none of them can acquire that failure mode by accident.
#
# The script the bug actually happened in was scripts/verify-spec.py, which is no longer here: the
# spec gate is internal/specgate since issue #127. The rule outlived it because the shape did.
from __future__ import annotations

import os
import re
import sys

# The repository's Python floor, checked rather than assumed. 3.9 and not 3.10 because macOS ships
# /usr/bin/python3 at 3.9.6 and this gate is deliberately runnable on a laptop's stock interpreter —
# `make setup` installs no Python. The same number is in the Makefile's PYTHON_REQUIRED, which carries
# the full reasoning, and in scripts/subset-fonts.sh; test/repo/python_floor_test.go fails if the
# copies disagree, if any scripts/*.py stops parsing at the floor, or if one stops enforcing it. CI is
# held to a HIGHER floor (3.10, asserted by .github/actions/setup-toolchain) so the runner image's
# interpreter is a checked fact, and the parse test is what keeps CI's newer Python from accepting
# syntax the floor cannot run.
MINIMUM_PYTHON = (3, 9)

if sys.version_info < MINIMUM_PYTHON:
    sys.stderr.write(
        "check-links.py needs Python %d.%d or newer; this is Python %d.%d.%d (%s).\n"
        "No link was checked. Re-run with a newer interpreter.\n"
        % (MINIMUM_PYTHON + sys.version_info[:3] + (sys.executable or "python3",))
    )
    sys.exit(2)

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
