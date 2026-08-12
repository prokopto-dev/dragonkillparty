package repogate

import "regexp"

// The AGPL firewall.
//
// EQdkp Plus is AGPL-3.0; its game modules are CC BY-NC-SA. Reading a user's database at runtime is
// fine. Transcribing their PHP is a licence violation — and it is exactly what a helpful agent does
// when the task is "match EQdkp's behaviour".
//
// THIS FILE IS ITSELF ALLOWLISTED, and it is the only file in internal/ that is. The rule's pattern
// has to name the identifiers it bans, and a file naming them is a file the firewall would fire on;
// scripts/repo-gates.sh carried the same exemption for the same reason while the pattern lived
// there. Keeping the pattern in a file of its own is what keeps that exemption one file wide rather
// than one package wide — nothing else in internal/repogate may spell an EQdkp identifier, and
// nothing else needs to.

// agplAllow is the allowlist, applied to the rendered "path:line:text" of a hit.
//
// This half is what keeps the importer possible at all: reading a user's database at runtime
// requires naming their tables somewhere. legacy_names.go is that somewhere, and the compat shim
// answers their api.php function names. The documentation trees are listed because a design
// document that could not name the thing it is migrating from would be useless — though of the
// entries below only the first three are reachable, since the scan covers internal, web, cmd and db
// alone. The unreachable ones are kept: an allowlist that describes the whole intent survives a
// tree being added to the scan, and dropping one silently would be a firewall change nobody reads.
var agplAllow = regexp.MustCompile(`^(internal/importer/legacy_names\.go` +
	`|internal/api/compat/` +
	`|internal/repogate/agpl\.go` +
	`|docs/|\.claude/|\.github/|scripts/repo-gates\.sh` +
	`|AGENTS\.md|README\.md|ROADMAP\.md|CONTRIBUTING\.md)`)

// agplRules covers the two halves of the firewall.
func agplRules() []textRule {
	var rules []textRule

	// AGPL001. The four identifiers are distinctive enough that a hit is always transcription and
	// never coincidence.
	//
	// NO COMMENT STRIPPING, and this is the one rule where that matters. Everywhere else a banned
	// token inside a comment is prose about the rule; here it is the thing itself. Pasting AGPL
	// source into a Go comment "just as a reference" infringes exactly as much as pasting it into
	// code. A well-meaning refactor that gave this rule the same `strip` as its neighbours — for
	// consistency — would open the firewall.
	//
	// NO INCLUDE GLOB either: a transcription can land in a .go file, a .ts file, a schema or a
	// migration, and a firewall that only read the extensions somebody thought of has a hole per
	// extension.
	//
	// One entry per tree, because a transcription can land in any of them and losing a tree from
	// the list is a silent hole.
	for _, tree := range []string{"internal", "web", "cmd", "db"} {
		rules = append(rules, textRule{
			id: "AGPL001", desc: "EQdkp Plus identifier outside the allowlisted files",
			tree: tree, include: nil,
			pattern: regexp.MustCompile(`\b(pdh_|gen_class|plus_exchange|__multidkp2event)`),
			reject:  []*regexp.Regexp{agplAllow},
			strip:   nil,
			quiet:   true,
		})
	}

	// AGPL002 is a narrower rule than AGPL001, and a different failure. AGPL001 catches distinctive
	// EQdkp identifiers that could only arrive by transcription. These are ordinary names that
	// arrive by a subtler route: someone reads docs/design/05-migration.md's list of EQdkp
	// `<prefix>config` keys — which is there because the importer must read them — and writes one
	// into DKP's own schema instead of DKP's name for the same concept.
	//
	// That is not hypothetical. docs/design/02-api-design.md's `/guild` row shipped
	// `inactive_period` and `auto_set_active` for exactly this reason, and `auto_set_active` is the
	// OPPOSITE control from DKP's `auto_set_inactive` — a bot written from the published contract
	// would have set the wrong value. Some keys in that same row had been correctly renamed
	// (`dkp_name` -> `points_label`, `guildtag` -> `tag`), which is what made the survivors
	// invisible.
	//
	// SCOPE IS db/ ONLY, AND THE DOCUMENTATION HALF IS DELIBERATELY NOT GATED. A scan over the
	// design documents cannot tell a leak from a lesson: docs/design/01-domain-model.md names
	// `show_twinks` to explain why DKP rejects that design, and the correction notes accompanying
	// this rule quote the banned names in order to document them. Both are correct writing this
	// rule would reject, and a gate that is usually wrong is a gate people route around. The wire
	// half is covered where it is unambiguous — SPEC008 in internal/specgate reads the generated
	// OpenAPI document, which contains field names and no prose.
	//
	// `hide_inactive` and `timezone` are in EQdkp's list too and are ALSO DKP's own column names:
	// the concepts coincide and the words are ordinary English. They are not banned. Every name
	// below is one DKP does not use, so a hit is always a transcription and never a collision.
	rules = append(rules, textRule{
		id: "AGPL002", desc: "EQdkp Plus config key used as a DKP schema name — use DKP's own name " +
			"(canonical §15, §16)",
		tree: "db", include: nil,
		pattern: regexp.MustCompile(`\b(inactive_period|auto_set_active|round_activate|round_precision` +
			`|dkp_name|guildtag|servername|show_twinks|detail_twink|special_members|default_game` +
			`|enable_leaderboard)\b`),
		strip: hashAndSlash,
		quiet: true,
	})

	return rules
}
