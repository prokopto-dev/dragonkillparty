package specgate

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// PermissionKey is the OpenAPI extension every served operation must carry (SPEC004).
//
// It mirrors internal/api.ExtensionPermission, and TestSpecGate_PermissionKey_MatchesAPI asserts the
// two agree. Note where it has to be declared: the Huma Operation's Extensions map, never Metadata,
// which is tagged `yaml:"-"` and never reaches the document.
const PermissionKey = "x-dkp-permission"

// BasePath is the version prefix every documented path must carry (SPEC007).
const BasePath = "/api/v1"

// operationIDRe mirrors internal/api.lowerCamelCase: a lowercase letter followed by letters and
// digits only.
//
// The two definitions must agree — one checks the Huma registry in Go, this checks the committed JSON
// — and an operationId that passes one and fails the other is a merge blocked for a reason nobody can
// reproduce. internal/api's TestErrors_LowerCamelCase drives both over the same table so that
// agreement is enforced rather than asserted in a comment.
var operationIDRe = regexp.MustCompile(`^[a-z][A-Za-z0-9]*$`)

// IsOperationID reports whether s is a legal operationId: lowerCamelCase, per canonical §16.
func IsOperationID(s string) bool {
	return operationIDRe.MatchString(s)
}

// SentinelPermissions returns the x-dkp-permission values that are NOT catalogue keys.
//
// Mirrors internal/api.SentinelPermissions(); docs/design/02-api-design.md §4.1 defines exactly two.
// TestSpecGate_SentinelPermissions_MatchAPI asserts the two lists agree.
func SentinelPermissions() []string {
	return []string{"public", "self"}
}

// checkOperationIDs covers SPEC001 (an explicit lowerCamelCase id) and SPEC002 (uniqueness), and
// returns the (path, method) -> id index SPEC003 compares against the base revision.
func checkOperationIDs(rep *report, ops []operation) map[key]string {
	seen := make(map[string]string, len(ops))
	index := make(map[key]string, len(ops))

	for _, o := range ops {
		id, _ := o.op["operationId"].(string)

		if id == "" {
			rep.violation("SPEC001",
				"%s has no operationId. It is public API — the generated SDK method name derives "+
					"from it — so it must be explicit, never auto-derived (canonical conventions §7).",
				o.where())

			continue
		}

		if !IsOperationID(id) {
			rep.violation("SPEC001",
				"%s has operationId %q, which is not lowerCamelCase. Canonical conventions §16: "+
					"verb + resource, e.g. createRaidTick.", o.where(), id)
		}

		if prior, duplicate := seen[id]; duplicate {
			rep.violation("SPEC002",
				"operationId %q is used by both %s and %s. The SDK generators would emit two "+
					"methods with one name.", id, prior, o.where())
		} else {
			seen[id] = o.where()
		}

		index[key{name: o.name, method: o.method}] = id
	}

	return index
}

// checkNoRenames covers SPEC003: an operationId may never be renamed.
//
// A rename is a breaking change to every generated SDK while the HTTP surface is untouched, so
// neither the drift gate nor a reader of the HTTP diff would notice. It is detected as: the same
// (path, method) exists on both sides and carries a different id. A REMOVED operation is also
// breaking and is deliberately not this gate's job — `oasdiff` owns that, in the api-breaking job.
func checkNoRenames(rep *report, root, baseRef string, head map[key]string) {
	baseDoc := readBaseSpec(rep, root, baseRef)
	if baseDoc == nil {
		return
	}

	base := make(map[key]string)

	for _, o := range operations(baseDoc) {
		if id, _ := o.op["operationId"].(string); id != "" {
			base[key{name: o.name, method: o.method}] = id
		}
	}

	// Sorted, for the reason operations() is sorted: a map range would report the same renames in a
	// different order on every run.
	keys := slices.SortedFunc(maps.Keys(base), func(a, b key) int {
		if c := strings.Compare(a.name, b.name); c != 0 {
			return c
		}

		return strings.Compare(a.method, b.method)
	})

	for _, k := range keys {
		oldID := base[k]

		newID, ok := head[k]
		if !ok || newID == oldID {
			continue
		}

		rep.violation("SPEC003",
			"%s %s renamed its operationId from %q to %q. That is a breaking change for every SDK "+
				"consumer with an unchanged HTTP surface. Restore the old id; if the rename is "+
				"genuinely intended it needs the !breaking-api label and a docs/api-changelog.md entry.",
			strings.ToUpper(k.method), k.name, oldID, newID)
	}
}

// checkSecurityAndPermission covers SPEC004: every served operation declares both. It returns the
// permission values seen, sorted and deduplicated, for SPEC005 to resolve.
func checkSecurityAndPermission(rep *report, ops []operation) []string {
	seen := make(map[string]bool)

	for _, o := range ops {
		if o.section != "paths" {
			continue
		}

		// Presence, not truthiness. `"security": []` is the EXPLICIT declaration that an operation
		// needs no credential (docs/design/02-api-design.md:144), and it is empty. Omitting the key
		// means the opposite — inherit the document-level requirement — so the two must not be
		// conflated. A check written as "is it non-empty" would reject every public endpoint and push
		// the next author into omitting the field, which is the failure this rule exists to prevent.
		if _, ok := o.op["security"]; !ok {
			rep.violation("SPEC004",
				"%s does not declare `security`. Every endpoint declares it, with no exceptions "+
					"(AGENTS.md); a public operation declares an explicit empty array.", o.where())
		}

		permission, _ := o.op[PermissionKey].(string)
		if permission == "" {
			rep.violation("SPEC004",
				"%s does not declare `%s`. Note that it belongs in the Huma Operation's Extensions "+
					"map — Metadata is tagged `yaml:\"-\"` and never reaches this document.",
				o.where(), PermissionKey)

			continue
		}

		seen[permission] = true
	}

	return slices.Sorted(maps.Keys(seen))
}

// checkPermissionsResolve covers SPEC005: every non-sentinel permission key exists in the authz
// catalogue.
//
// The catalogue is read as TEXT and matched as a QUOTED EXACT SUBSTRING, so `raid.tick` does not
// satisfy a requirement for `raid.tick.create`. That is load-bearing rather than lazy, and
// internal/authz/doc.go says so from the other side: a composed key (Resource + "." + Action, or a
// Key() method) produces the right runtime value and fails this gate, because the literal never
// appears in the source. Parsing the Go instead would accept a composed key and lose the property.
func checkPermissionsResolve(rep *report, root string, permissions []string) {
	// Every permission that is not one of the two sentinels, which are allowlisted rather than
	// catalogued. Named for what it holds rather than `real`, which is a Go builtin.
	mustResolve := slices.DeleteFunc(slices.Clone(permissions), func(p string) bool {
		return slices.Contains(SentinelPermissions(), p)
	})
	if len(mustResolve) == 0 {
		return
	}

	quoted := make([]string, 0, len(mustResolve))
	for _, p := range mustResolve {
		quoted = append(quoted, fmt.Sprintf("%q", p))
	}

	src, err := os.ReadFile(filepath.Join(root, CatalogueFile))
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			rep.violation("SPEC005", "could not read %s: %v", CatalogueFile, err)

			return
		}

		rep.violation("SPEC005",
			"%s named, but %s does not exist. `role_permission` is FK-constrained to "+
				"`permission(key)`, so a key with no catalogue entry is a boot failure. Adding one is "+
				"a schema change — see .claude/rules/api-endpoints.md.",
			strings.Join(quoted, ", "), CatalogueFile)

		return
	}

	catalogue := string(src)

	for _, permission := range mustResolve {
		if !strings.Contains(catalogue, `"`+permission+`"`) {
			rep.violation("SPEC005",
				"permission %q is not in %s. A divergent key is a boot failure, not a 403.",
				permission, CatalogueFile)
		}
	}
}

// checkMoneyAndFloats covers SPEC006: money is an unquoted integer named *_centipoints, and nothing
// on the wire is a float.
//
// Canonical conventions §1: point arithmetic is Centipoints (int64) only — not in Go, not in SQL, and
// not on the wire. A float on the wire is the specific failure that makes a ledger disagree with
// itself, because JSON numbers round-trip through IEEE-754 doubles in every client language.
func checkMoneyAndFloats(rep *report, doc map[string]any) {
	checkField := func(name string, schema map[string]any, at string) {
		types := schemaTypes(schema)

		if strings.HasSuffix(name, "_centipoints") && !slices.Contains(types, "integer") {
			rep.violation("SPEC006",
				"%s is money and its type is %s. Centipoints are unquoted JSON integers — never a "+
					"string, never a float.", at, describeType(schema))
		}

		if strings.HasSuffix(name, "_cp") {
			rep.violation("SPEC006",
				"%s uses the SQL suffix `_cp`. On the wire the suffix is `_centipoints` "+
					"(canonical conventions §16).", at)
		}

		if slices.Contains(types, "number") {
			rep.violation("SPEC006",
				"%s is `type: number`, a float on the wire. Money is int64 centipoints and ratios "+
					"are integer basis points (`_bp`); neither is a float.", at)
		}
	}

	walkDocument(doc, fieldVisitor{
		property: checkField,
		parameter: func(param map[string]any, trail string, index int) {
			// A parameter is a DIFFERENT SHAPE from a body field and was missed by the first version
			// of this gate: {"name": ..., "in": ..., "schema": {...}} rather than an entry under a
			// `properties` map, so walking properties alone never saw one. A money-suffixed query
			// parameter — `?min_value_centipoints=` on a future filter — is exactly the field this
			// rule exists to catch, and it would have passed.
			name, okName := param["name"].(string)
			schema, okSchema := param["schema"].(map[string]any)

			if okName && okSchema {
				checkField(name, schema, fmt.Sprintf("%s.parameters[%d].%s", trail, index, name))
			}
		},
	})
}

// checkPathsAreVersioned covers SPEC007: every documented path lives under /api/v1.
//
// The nearest thing to a Hidden-allowlist check this document can support. Canonical §7 puts every
// operation under the version prefix and puts /healthz, /readyz, /metrics, the OAuth callback and the
// compat shim outside it as Hidden. So an unversioned path appearing HERE means one of those five was
// registered without Hidden and is now published API — which is the failure the allowlist exists to
// prevent, seen from the other side.
func checkPathsAreVersioned(rep *report, ops []operation) {
	for _, o := range ops {
		if o.section != "paths" {
			continue
		}

		if !strings.HasPrefix(o.name, BasePath+"/") {
			rep.violation("SPEC007",
				"%s is documented but is not under %s. Canonical conventions §7: infrastructure "+
					"routes stay outside the prefix AND out of the document (Hidden); everything in "+
					"the document is versioned API surface.", o.where(), BasePath)
		}
	}
}

// bannedEQdkpKeys maps an EQdkp Plus config key to what DKP calls the same thing, or to the reason
// DKP has no equivalent.
//
// From docs/design/05-migration.md's `<prefix>config` carry-list, minus the two DKP also uses.
//
// NOT EVERY EQdkp KEY IS BANNED. `hide_inactive` and `timezone` appear in that same EQdkp list and are
// also DKP's own column names — the concepts coincide and the names are ordinary English. This list is
// exactly the keys DKP does NOT use, so a hit is always a transcription and never a collision.
func bannedEQdkpKeys() map[string]string {
	return map[string]string{
		"inactive_period":    "inactive_after_days",
		"auto_set_active":    "auto_set_inactive (note: the OPPOSITE control)",
		"round_activate":     "points_precision (DKP has one rounding setting, not two)",
		"round_precision":    "points_precision",
		"dkp_name":           "points_label",
		"guildtag":           "tag",
		"servername":         "the `server` table's `name`",
		"show_twinks":        "no equivalent — points live on `account`, per canonical §9",
		"detail_twink":       "no equivalent — see above",
		"special_members":    "no equivalent — use a role",
		"default_game":       "no equivalent — this is a P99 EverQuest product",
		"enable_leaderboard": "no equivalent — portal blocks are CMS configuration",
	}
}

// checkNoEQdkpConfigKeys covers SPEC008: no EQdkp Plus config key is a field name in DKP's own
// contract.
//
// This rule exists because it already happened. docs/design/02-api-design.md's `/guild` row was
// written from docs/design/05-migration.md's list of EQdkp `<prefix>config` keys rather than from
// DKP's schema, and two of them survived unrenamed: `inactive_period` (DKP: `inactive_after_days`) and
// `auto_set_active` — which is the OPPOSITE control from DKP's `auto_set_inactive`, so a bot written
// from the published contract would have set the wrong value with nothing to say so. The same
// transcription produced "rounding on/off and precision", because EQdkp carries `round_activate` AND
// `round_precision` where DKP carries one `points_precision`. Other keys in that list were correctly
// renamed on the way in (`dkp_name` -> `points_label`, `guildtag` -> `tag`), which is exactly what
// made the survivors hard to see.
//
// WHY THIS IS A SPEC RULE AND NOT A GREP OVER THE DESIGN DOCUMENTS, which is where the defect actually
// lived. A markdown gate cannot tell a leak from a lesson. `docs/design/01-domain-model.md` names
// `show_twinks` twice — at :572 and :2870 — precisely to explain why DKP rejects the design, and the
// correction notes alongside this rule quote `inactive_period` and `auto_set_active` in order to
// document them. Every one of those is correct writing that a grep would reject, and a gate whose
// failures are usually false is a gate people learn to route around.
//
// The spec has no prose. A name here is a field a client will bind to, in a document generated from Go
// types rather than written by hand, so a hit is unambiguous and is caught at the moment the name
// becomes real. The documentation half is left to review, deliberately and on the record.
func checkNoEQdkpConfigKeys(rep *report, doc map[string]any) {
	banned := bannedEQdkpKeys()

	checkName := func(name, at string) {
		replacement, hit := banned[name]
		if !hit {
			return
		}

		rep.violation("SPEC008",
			"%s is named %q, which is an EQdkp Plus config key, not a DKP field name. Use %s. "+
				"docs/design/05-migration.md names EQdkp's keys because the importer must read them; "+
				"this document defines DKP's own contract and uses DKP's own names (canonical "+
				"conventions §15, §16).", at, name, replacement)
	}

	walkDocument(doc, fieldVisitor{
		property: func(name string, _ map[string]any, at string) {
			checkName(name, at)
		},
		parameter: func(param map[string]any, trail string, index int) {
			// Parameters carry their name in a field rather than as a map key — the same shape trap
			// SPEC006 documents. A `?show_twinks=` filter is exactly this rule's case.
			if name, ok := param["name"].(string); ok {
				checkName(name, fmt.Sprintf("%s.parameters[%d]", trail, index))
			}
		},
	})
}

// fieldVisitor is the pair of callbacks walk invokes for the two shapes a named field takes in an
// OpenAPI document: an entry under a `properties` map, and an entry of a `parameters` array.
//
// The parameter callback receives the raw parameter and its index rather than an extracted name,
// because the two rules that use it disagree on what they need — SPEC006 wants the name AND the
// schema and reports the field, SPEC008 wants only the name and reports the parameter — and on how
// they render the trail.
type fieldVisitor struct {
	property  func(name string, schema map[string]any, at string)
	parameter func(param map[string]any, trail string, index int)
}

// walkDocument applies v to the three regions of the document where a field name becomes contract.
func walkDocument(doc map[string]any, v fieldVisitor) {
	for _, region := range []string{"components", "paths", "webhooks"} {
		walk(doc[region], region, v)
	}
}

// walk descends node, invoking v for every named field it finds.
//
// IT DESCENDS INTO A `properties` MAP AS WELL AS ACROSS IT. Each direct child is checked, and then
// walked, so a field one level deeper than a schema's own properties is reached:
// `Thing.properties.inner.properties.value_centipoints` and the array form
// `Thing.properties.list.items.properties.amount_cp` are both seen. Crossing without descending is
// what scripts/verify-spec.py did and what the Go port carried over unchanged so that #127 was a
// move rather than a silent widening of the rules; issue #144 is that gap, closed here.
//
// The trail SKIPS the word "properties" — `components.schemas.Thing.inner.value_centipoints` — which
// is why the recursion happens at the point the child is checked rather than by letting the generic
// loop below reach the `properties` key. The generic loop still skips it, or every field would be
// reported twice under two different trails.
//
// A document is a tree: `$ref` is a string, never a cycle, so this recursion terminates.
func walk(node any, trail string, v fieldVisitor) {
	switch n := node.(type) {
	case []any:
		for i, item := range n {
			walk(item, fmt.Sprintf("%s[%d]", trail, i), v)
		}

	case map[string]any:
		if props, ok := n["properties"].(map[string]any); ok {
			for _, name := range slices.Sorted(maps.Keys(props)) {
				if prop, ok := props[name].(map[string]any); ok {
					v.property(name, prop, trail+"."+name)
					walk(prop, trail+"."+name, v)
				}
			}
		}

		if params, ok := n["parameters"].([]any); ok {
			for i, p := range params {
				if param, ok := p.(map[string]any); ok {
					v.parameter(param, trail, i)
				}
			}
		}

		for _, k := range slices.Sorted(maps.Keys(n)) {
			if k == "properties" {
				continue
			}

			walk(n[k], trail+"."+k, v)
		}
	}
}

// schemaTypes returns a schema's declared types, flattening the OpenAPI 3.1 union form
// (`"type": ["integer", "null"]`) into the same shape as the scalar form. A schema with no `type` at
// all returns nil, which is a type that is neither an integer nor a number and is treated as such.
func schemaTypes(schema map[string]any) []string {
	switch t := schema["type"].(type) {
	case string:
		return []string{t}

	case []any:
		out := make([]string, 0, len(t))

		for _, item := range t {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}

		return out

	default:
		return nil
	}
}

// describeType renders a schema's `type` for a failure message: the declared value when there is one,
// and "absent" when there is not. A message reading "its type is <nil>" sends the reader looking for a
// null they never wrote.
func describeType(schema map[string]any) string {
	t, ok := schema["type"]
	if !ok || t == nil {
		return "absent"
	}

	types := schemaTypes(schema)
	if len(types) == 0 {
		return fmt.Sprintf("%v", t)
	}

	quoted := make([]string, 0, len(types))
	for _, s := range types {
		quoted = append(quoted, fmt.Sprintf("%q", s))
	}

	if _, isList := t.([]any); isList {
		return "[" + strings.Join(quoted, ", ") + "]"
	}

	return quoted[0]
}
