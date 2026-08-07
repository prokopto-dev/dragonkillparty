#!/usr/bin/env python3
"""Assert the properties of openapi/openapi.json that regenerating it cannot establish.

`make verify-generated` proves the committed spec matches the code. It cannot prove the spec is
*correct*, because a wrong spec regenerates to the same wrong bytes every time. This gate covers the
properties named in .github/workflows/ci.yml's spec-drift job.

Python rather than jq, for the reason `generated-digest` in the Makefile picks between sha256sum and
shasum: CI is Ubuntu and the laptops are macOS, jq ships on one of them, and a gate that only runs in
CI is a gate contributors discover by pushing. python3 is already a build dependency of this repo
(scripts/check-links.py, `make docs-links`), so this adds no pin and nothing to `make setup`.

WHAT THIS GATE DELIBERATELY DOES NOT CHECK: the `Hidden: true` allowlist of canonical conventions §7.
ci.yml:281 lists it here, and it is not implementable here. Huma never adds a hidden operation to
`paths` (huma/v2 openapi.go:1570), so a hidden operation is simply ABSENT from this document and no
amount of reading the JSON can tell a correctly-hidden operation from one that was never written.
That assertion lives in internal/api/arch_test.go, which sees the in-process registry — which is
also where docs/development/first-ten-prs.md's acceptance criteria put it. ci.yml's comment is
corrected in the same change as this file.
"""

import json
import os
import re
import subprocess
import sys

# Rule ids, in the shape scripts/repo-gates.sh uses. Tests assert on the id in the output rather than
# on the exit code, so a gate that fires for the wrong reason is distinguishable from one that fires
# for the right one.
SPEC_FILE = "openapi/openapi.json"
CATALOGUE_FILE = "internal/authz/catalogue.go"

# Mirrors internal/api.SentinelPermissions(). docs/design/02-api-design.md §4.1 defines exactly two
# values that are not catalogue keys.
SENTINEL_PERMISSIONS = {"public", "self"}

# Mirrors internal/api.lowerCamelCase. The two definitions must agree: one checks the Huma registry
# in Go, this checks the committed JSON, and an operationId that passes one and fails the other is a
# merge blocked for a reason nobody can reproduce.
OPERATION_ID_RE = re.compile(r"^[a-z][A-Za-z0-9]*$")

BASE_PATH = "/api/v1"
PERMISSION_KEY = "x-dkp-permission"

# HTTP methods an OpenAPI path item may carry. Anything else in a path item ($ref, parameters,
# summary, servers) is not an operation and must not be treated as one.
METHODS = ("get", "put", "post", "delete", "options", "head", "patch", "trace")

RED = "\033[31m"
GREEN = "\033[32m"
YELLOW = "\033[33m"
RESET = "\033[0m"

violations: list[str] = []


def violation(rule: str, message: str) -> None:
    violations.append(f"  {RED}[{rule}]{RESET} {message}")


def note(message: str) -> None:
    print(f"  {YELLOW}note{RESET}  {message}")


def operations(doc: dict) -> list[tuple[str, str, str, dict]]:
    """Yield (section, path-or-event, method, operation) for paths and webhooks.

    Webhooks are included for the operationId rules and excluded from the security and permission
    rules: a webhook describes a request this server SENDS to a subscriber's endpoint, so it has no
    permission of ours to declare and its security is the subscriber's business. Its operationId
    still has to be unique, because both SDK generators derive a type name from it in the same
    namespace as the REST methods.
    """
    found = []
    for section in ("paths", "webhooks"):
        for name, item in (doc.get(section) or {}).items():
            if not isinstance(item, dict):
                continue
            for method in METHODS:
                op = item.get(method)
                if isinstance(op, dict):
                    found.append((section, name, method, op))
    return found


def check_operation_ids(ops: list[tuple[str, str, str, dict]]) -> dict[tuple[str, str], str]:
    """SPEC001 explicit lowerCamelCase ids; SPEC002 uniqueness. Returns {(path, method): id}."""
    seen: dict[str, str] = {}
    index: dict[tuple[str, str], str] = {}

    for _, name, method, op in ops:
        where = f"{method.upper()} {name}"
        op_id = op.get("operationId")

        if not op_id:
            violation(
                "SPEC001",
                f"{where} has no operationId. It is public API — the generated SDK method name "
                f"derives from it — so it must be explicit, never auto-derived "
                f"(canonical conventions §7).",
            )
            continue

        if not OPERATION_ID_RE.match(op_id):
            violation(
                "SPEC001",
                f"{where} has operationId {op_id!r}, which is not lowerCamelCase. Canonical "
                f"conventions §16: verb + resource, e.g. createRaidTick.",
            )

        if op_id in seen:
            violation(
                "SPEC002",
                f"operationId {op_id!r} is used by both {seen[op_id]} and {where}. The SDK "
                f"generators would emit two methods with one name.",
            )
        else:
            seen[op_id] = where

        index[(name, method)] = op_id

    return index


def check_no_renames(head: dict[tuple[str, str], str], base_ref: str) -> None:
    """SPEC003: an operationId may never be renamed.

    A rename is a breaking change to every generated SDK while the HTTP surface is untouched, so
    neither the drift gate nor a reader of the HTTP diff would notice. It is detected as: the same
    (path, method) exists on both sides and carries a different id. A REMOVED operation is also
    breaking and is deliberately not this gate's job — `oasdiff` owns that, in the api-breaking job.
    """
    base_doc = read_base_spec(base_ref)
    if base_doc is None:
        return

    base = {}
    for _, name, method, op in operations(base_doc):
        if op.get("operationId"):
            base[(name, method)] = op["operationId"]

    for key, old_id in base.items():
        new_id = head.get(key)
        if new_id is not None and new_id != old_id:
            name, method = key
            violation(
                "SPEC003",
                f"{method.upper()} {name} renamed its operationId from {old_id!r} to {new_id!r}. "
                f"That is a breaking change for every SDK consumer with an unchanged HTTP surface. "
                f"Restore the old id; if the rename is genuinely intended it needs the "
                f"!breaking-api label and a docs/api-changelog.md entry.",
            )


def read_base_spec(base_ref: str) -> dict | None:
    """Return the spec at base_ref, or None when that revision legitimately has no spec."""
    if base_ref == "":
        # DKP_SPEC_BASE_REF="" disables the comparison. It exists for ONE caller: the negative
        # fixtures in test/repo, which build a spec in t.TempDir() where there is no git repository
        # to compare against and would otherwise all fail on a spurious SPEC003.
        #
        # This is a way to weaken the gate, so it is fenced the same way DKP_REPO_ROOT is: the
        # Makefile recipe strips it with `env -u`, and TestMakefile_VerifySpec_StripsBaseRefEnv
        # asserts that it does. Do not add a caller.
        note("DKP_SPEC_BASE_REF is empty; the operationId-rename check is disabled.")
        return None

    try:
        proc = subprocess.run(
            ["git", "cat-file", "-e", f"{base_ref}:{SPEC_FILE}"],
            capture_output=True,
            check=False,
        )
    except FileNotFoundError:
        violation("SPEC003", "git is not on PATH, so the operationId-rename check cannot run.")
        return None

    if proc.returncode != 0:
        # Distinguish "the base revision has no spec yet" from "the base revision is not here".
        # The first is the normal state of the PR that first commits a spec; the second is a shallow
        # clone, and silently passing on it would make this gate vacuous in exactly the CI
        # configuration most likely to have it.
        rev = subprocess.run(
            ["git", "rev-parse", "--verify", "--quiet", base_ref],
            capture_output=True,
            check=False,
        )
        if rev.returncode != 0:
            violation(
                "SPEC003",
                f"{base_ref} is not available, so no operationId could be compared against it. "
                f"In CI the spec-drift job needs `fetch-depth: 0`; locally run "
                f"`git fetch origin main`. Set DKP_SPEC_BASE_REF to compare against another "
                f"revision.",
            )
            return None

        note(f"{base_ref} has no {SPEC_FILE}; this is the change that introduces it.")
        return None

    blob = subprocess.run(
        ["git", "show", f"{base_ref}:{SPEC_FILE}"],
        capture_output=True,
        check=False,
    )
    if blob.returncode != 0:
        violation("SPEC003", f"could not read {SPEC_FILE} at {base_ref}.")
        return None

    try:
        return json.loads(blob.stdout)
    except json.JSONDecodeError as exc:
        violation("SPEC003", f"{SPEC_FILE} at {base_ref} is not valid JSON: {exc}")
        return None


def check_security_and_permission(ops: list[tuple[str, str, str, dict]]) -> set[str]:
    """SPEC004: every served operation declares both. Returns the permission values seen."""
    permissions: set[str] = set()

    for section, name, method, op in ops:
        if section != "paths":
            continue

        where = f"{method.upper()} {name}"

        # `in`, not truthiness. `"security": []` is the EXPLICIT declaration that an operation needs
        # no credential (docs/design/02-api-design.md:144), and it is falsy. Omitting the key means
        # the opposite — inherit the document-level requirement — so the two must not be conflated.
        if "security" not in op:
            violation(
                "SPEC004",
                f"{where} does not declare `security`. Every endpoint declares it, with no "
                f"exceptions (AGENTS.md); a public operation declares an explicit empty array.",
            )

        permission = op.get(PERMISSION_KEY)
        if not permission:
            violation(
                "SPEC004",
                f"{where} does not declare `{PERMISSION_KEY}`. Note that it belongs in the Huma "
                f"Operation's Extensions map — Metadata is tagged `yaml:\"-\"` and never reaches "
                f"this document.",
            )
            continue

        permissions.add(permission)

    return permissions


def check_permissions_resolve(permissions: set[str]) -> None:
    """SPEC005: every non-sentinel permission key exists in the authz catalogue."""
    real = sorted(p for p in permissions if p not in SENTINEL_PERMISSIONS)
    if not real:
        return

    if not os.path.exists(CATALOGUE_FILE):
        violation(
            "SPEC005",
            f"{', '.join(repr(p) for p in real)} named, but {CATALOGUE_FILE} does not exist. "
            f"`role_permission` is FK-constrained to `permission(key)`, so a key with no catalogue "
            f"entry is a boot failure. Adding one is a schema change — see "
            f".claude/rules/api-endpoints.md.",
        )
        return

    with open(CATALOGUE_FILE, encoding="utf-8") as fh:
        catalogue = fh.read()

    for permission in real:
        # A quoted exact match, so `raid.tick` does not satisfy a requirement for `raid.tick.create`.
        if f'"{permission}"' not in catalogue:
            violation(
                "SPEC005",
                f"permission {permission!r} is not in {CATALOGUE_FILE}. A divergent key is a boot "
                f"failure, not a 403.",
            )


def check_money_and_floats(doc: dict) -> None:
    """SPEC006: money is an unquoted integer named *_centipoints, and nothing on the wire is a float.

    Canonical conventions §1: point arithmetic is Centipoints (int64) only — not in Go, not in SQL,
    and not on the wire. A float on the wire is the specific failure that makes a ledger disagree
    with itself, because JSON numbers round-trip through IEEE-754 doubles in every client language.
    """

    def check_field(name: str, schema: dict, at: str) -> None:
        """Apply the money and float rules to one named field."""
        types = schema.get("type")
        types = types if isinstance(types, list) else [types]

        if name.endswith("_centipoints") and "integer" not in types:
            violation(
                "SPEC006",
                f"{at} is money and its type is {schema.get('type')!r}. Centipoints are "
                f"unquoted JSON integers — never a string, never a float.",
            )

        if name.endswith("_cp"):
            violation(
                "SPEC006",
                f"{at} uses the SQL suffix `_cp`. On the wire the suffix is `_centipoints` "
                f"(canonical conventions §16).",
            )

        if "number" in types:
            violation(
                "SPEC006",
                f"{at} is `type: number`, a float on the wire. Money is int64 centipoints and "
                f"ratios are integer basis points (`_bp`); neither is a float.",
            )

    def walk(node, trail: str) -> None:
        if isinstance(node, list):
            for i, item in enumerate(node):
                walk(item, f"{trail}[{i}]")
            return

        if not isinstance(node, dict):
            return

        for name, prop in (node.get("properties") or {}).items():
            if isinstance(prop, dict):
                check_field(name, prop, f"{trail}.{name}")

        # Parameters are a DIFFERENT SHAPE and were missed by the first version of this gate: a
        # query, header or path parameter is {"name": ..., "in": ..., "schema": {...}}, not an entry
        # under a `properties` map, so walking properties alone never saw one. A money-suffixed
        # query parameter — `?min_value_centipoints=` on a future filter — is exactly the field this
        # rule exists to catch, and it would have passed.
        for i, param in enumerate(node.get("parameters") or []):
            if not isinstance(param, dict):
                continue

            name = param.get("name")
            schema = param.get("schema")

            if isinstance(name, str) and isinstance(schema, dict):
                check_field(name, schema, f"{trail}.parameters[{i}].{name}")

        for key, value in node.items():
            if key != "properties":
                walk(value, f"{trail}.{key}" if trail else key)

    walk(doc.get("components") or {}, "components")
    walk(doc.get("paths") or {}, "paths")
    walk(doc.get("webhooks") or {}, "webhooks")


def check_paths_are_versioned(ops: list[tuple[str, str, str, dict]]) -> None:
    """SPEC007: every documented path lives under /api/v1.

    The nearest thing to a Hidden-allowlist check this document can support. Canonical §7 puts every
    operation under the version prefix and puts /healthz, /readyz, /metrics, the OAuth callback and
    the compat shim outside it as Hidden. So an unversioned path appearing HERE means one of those
    five was registered without Hidden and is now published API — which is the failure the allowlist
    exists to prevent, seen from the other side.
    """
    for section, name, method, _ in ops:
        if section != "paths":
            continue
        if not name.startswith(BASE_PATH + "/"):
            violation(
                "SPEC007",
                f"{method.upper()} {name} is documented but is not under {BASE_PATH}. Canonical "
                f"conventions §7: infrastructure routes stay outside the prefix AND out of the "
                f"document (Hidden); everything in the document is versioned API surface.",
            )


def check_no_eqdkp_config_keys(doc: dict) -> None:
    """SPEC008: no EQdkp Plus config key is a field name in DKP's own contract.

    This rule exists because it already happened. docs/design/02-api-design.md's `/guild` row was
    written from docs/design/05-migration.md's list of EQdkp `<prefix>config` keys rather than from
    DKP's schema, and two of them survived unrenamed: `inactive_period` (DKP: `inactive_after_days`)
    and `auto_set_active` — which is the OPPOSITE control from DKP's `auto_set_inactive`, so a bot
    written from the published contract would have set the wrong value with nothing to say so. The
    same transcription produced "rounding on/off and precision", because EQdkp carries
    `round_activate` AND `round_precision` where DKP carries one `points_precision`. Other keys in
    that list were correctly renamed on the way in (`dkp_name` -> `points_label`, `guildtag` ->
    `tag`), which is exactly what made the survivors hard to see.

    WHY THIS IS A SPEC RULE AND NOT A GREP OVER THE DESIGN DOCUMENTS, which is where the defect
    actually lived. A markdown gate cannot tell a leak from a lesson. `docs/design/01-domain-model.md`
    names `show_twinks` twice — at :572 and :2870 — precisely to explain why DKP rejects the design,
    and the correction notes added alongside this rule quote `inactive_period` and `auto_set_active`
    in order to document them. Every one of those is correct writing that a grep would reject, and a
    gate whose failures are usually false is a gate people learn to route around.

    The spec has no prose. A name here is a field a client will bind to, in a document generated
    from Go types rather than written by hand, so a hit is unambiguous and is caught at the moment
    the name becomes real. The documentation half is left to review, deliberately and on the record.

    NOT EVERY EQdkp KEY IS BANNED. `hide_inactive` and `timezone` appear in that same EQdkp list and
    are also DKP's own column names — the concepts coincide and the names are ordinary English. This
    list is exactly the keys DKP does NOT use, so a hit is always a transcription and never a
    collision.
    """
    # From docs/design/05-migration.md's `<prefix>config` carry-list, minus the two DKP also uses.
    banned = {
        "inactive_period": "inactive_after_days",
        "auto_set_active": "auto_set_inactive (note: the OPPOSITE control)",
        "round_activate": "points_precision (DKP has one rounding setting, not two)",
        "round_precision": "points_precision",
        "dkp_name": "points_label",
        "guildtag": "tag",
        "servername": "the `server` table's `name`",
        "show_twinks": "no equivalent — points live on `account`, per canonical §9",
        "detail_twink": "no equivalent — see above",
        "special_members": "no equivalent — use a role",
        "default_game": "no equivalent — this is a P99 EverQuest product",
        "enable_leaderboard": "no equivalent — portal blocks are CMS configuration",
    }

    def check_name(name: str, at: str) -> None:
        replacement = banned.get(name)
        if replacement is None:
            return

        violation(
            "SPEC008",
            f"{at} is named {name!r}, which is an EQdkp Plus config key, not a DKP field name. "
            f"Use {replacement}. docs/design/05-migration.md names EQdkp's keys because the "
            f"importer must read them; this document defines DKP's own contract and uses DKP's own "
            f"names (canonical conventions §15, §16).",
        )

    def walk(node, trail: str) -> None:
        if isinstance(node, list):
            for i, item in enumerate(node):
                walk(item, f"{trail}[{i}]")
            return

        if not isinstance(node, dict):
            return

        for name, prop in (node.get("properties") or {}).items():
            if isinstance(prop, dict):
                check_name(name, f"{trail}.{name}")

        # Parameters carry their name in a field rather than as a map key — the same shape trap
        # SPEC006 documents. A `?show_twinks=` filter is exactly this rule's case.
        for i, param in enumerate(node.get("parameters") or []):
            if isinstance(param, dict) and isinstance(param.get("name"), str):
                check_name(param["name"], f"{trail}.parameters[{i}]")

        for key, value in node.items():
            if key != "properties":
                walk(value, f"{trail}.{key}" if trail else key)

    walk(doc.get("components") or {}, "components")
    walk(doc.get("paths") or {}, "paths")
    walk(doc.get("webhooks") or {}, "webhooks")


def main() -> int:
    # Same DKP_REPO_ROOT contract as the gate scripts, so the negative fixtures in test/repo can run
    # against a tree in t.TempDir(). `make verify-spec` strips it with `env -u`.
    root = os.environ.get("DKP_REPO_ROOT") or os.path.dirname(
        os.path.dirname(os.path.abspath(__file__))
    )
    os.chdir(root)

    if not os.path.exists(SPEC_FILE):
        print(f"  {RED}{SPEC_FILE} does not exist — run `make gen`{RESET}", file=sys.stderr)
        return 1

    with open(SPEC_FILE, encoding="utf-8") as fh:
        try:
            doc = json.load(fh)
        except json.JSONDecodeError as exc:
            print(f"  {RED}{SPEC_FILE} is not valid JSON: {exc}{RESET}", file=sys.stderr)
            return 1

    ops = operations(doc)
    if not ops:
        # An empty document passes every rule below vacuously. The whole point of this gate is that
        # it cannot pass without having looked at something.
        print(
            f"  {RED}{SPEC_FILE} declares no operations — the gate would pass vacuously{RESET}",
            file=sys.stderr,
        )
        return 1

    index = check_operation_ids(ops)
    check_no_renames(index, os.environ.get("DKP_SPEC_BASE_REF", "origin/main"))
    check_permissions_resolve(check_security_and_permission(ops))
    check_money_and_floats(doc)
    check_paths_are_versioned(ops)
    check_no_eqdkp_config_keys(doc)

    if violations:
        print(f"\n{RED}  openapi/openapi.json failed {len(violations)} check(s){RESET}\n", file=sys.stderr)
        for line in violations:
            print(line, file=sys.stderr)
        print(file=sys.stderr)
        return 1

    print(f"  {GREEN}{len(ops)} operation(s), all conforming{RESET}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
