package repogate

import "regexp"

// textRules is the config-shaped rule catalogue: data, in the order the rules run and report.
//
// Read it as a table. Each entry is one (rule, tree) pair, exactly as the shell script wrote one
// `gate` call per pair — so a rule covering two trees appears twice and losing one of them is
// visible in a diff rather than hidden in a loop that quietly lost an element.
func textRules() []textRule {
	var rules []textRule

	rules = append(rules, moneyRules()...)
	rules = append(rules, webRules()...)
	rules = append(rules, migrationRules()...)
	rules = append(rules, supplyChainRules()...)
	rules = append(rules, agplRules()...)

	return rules
}

// --- Money is integer centipoints -------------------------------------------------------------

// totalFn is assembled rather than written, and the same trick appears in test/repo for the same
// reason: this file lives under internal/, which is a tree MONEY002 scans, so spelling the banned
// call in the rule's own description would make the catalogue fire on itself. The shell script did
// not need the trick only because scripts/ is not one of the trees it scans.
const totalFn = "total"

func moneyRules() []textRule {
	// MONEY002 reaches SQL embedded in Go as well as db/*.sql, because SQL arrives at SQLite as a
	// Go string literal just as readily as from a .sql file — and when the rule was written db/ did
	// not exist yet, so the Go half was the only one of the pair that ran.
	//
	// The call returns a REAL where sum() returns an INTEGER, so a single one silently converts the
	// centipoint ledger to floating point: no error, no warning, just a balance that is wrong by a
	// fraction of a point for years.
	moneyTotal := regexp.MustCompile(`\b` + totalFn + `\s*\(`)
	moneyDesc := "SQLite " + totalFn + "() returns a float — use sum()"

	return []textRule{
		{
			id: "MONEY002", desc: moneyDesc,
			tree: "db", include: []string{"*.sql"},
			pattern: moneyTotal, strip: hashAndSlash,
		},
		{
			id: "MONEY002", desc: moneyDesc,
			tree: "internal", include: []string{"*.go"},
			pattern: moneyTotal, strip: hashAndSlash,
		},
		{
			id: "MONEY002", desc: moneyDesc,
			tree: "cmd", include: []string{"*.go"},
			pattern: moneyTotal, strip: hashAndSlash,
		},
		{
			// SQLite's type affinity makes this quiet in a way the Go ban is not: a REAL column
			// accepts every integer a correct writer inserts, so the taint is invisible until a
			// value arrives that cannot be represented exactly — and by then the column holds years
			// of history. There is no compensating linter; this rule is the only mechanism.
			id: "MONEY003", desc: "REAL/NUMERIC/DECIMAL column type",
			tree: "db", include: []string{"*.sql"},
			pattern: regexp.MustCompile(`\b(REAL|NUMERIC|DECIMAL)\b`), strip: hashAndSlash,
		},
	}
}

// --- Law 4: the SPA calls only the generated client ---------------------------------------------

func webRules() []textRule {
	// The generated client is where a fetch is SUPPOSED to happen, and WEB003 shares the exemption
	// for a related reason: web/src/api is written by `make gen` from openapi/openapi.json, a URL
	// in it is a spec description or a documentation link, and a .d.ts cannot fetch anything. If a
	// real off-origin load ever appears there, the spec is the bug and hand-editing the generated
	// file is not the fix.
	generatedClient := regexp.MustCompile(`^web/src/api/`)

	// BANNED: ANY off-origin URL literal — an absolute `https?://host`, or a protocol-relative
	// `//host`. Not a list of asset-bearing shapes.
	//
	// The first version of this rule did enumerate the shapes: `url(…)`, `@import url(…)`,
	// `<link …>`, and the known CDNs by name. It was reviewed and it was wrong — `<script
	// src="https://…">`, `<img src="//…">` and the perfectly valid `@import "https://…";` (the
	// quoted spelling, no `url()`) all walked straight through, and every one of them makes the SPA
	// depend on another origin exactly as much as the shapes that were caught. An enumeration is
	// only ever as complete as the imagination of whoever wrote it, and the failure mode is silent.
	//
	// The host must contain a dot. That is what separates `//example.com` from a trailing `//` JS
	// comment or a doubled slash in a path, and it is why the rule can be this broad without being
	// noisy.
	//
	// The named font and script CDNs stay in the pattern even though the generic rule subsumes
	// them: they cost nothing, they name the specific mistake docs/design/09 §3 is about, and they
	// still fire on a bare `fonts.googleapis.com` written with no scheme at all.
	offOrigin := `(https?:)?//[a-zA-Z0-9-]+(\.[a-zA-Z0-9-]+)+`
	thirdParty := `(` + offOrigin + `|fonts\.googleapis\.com|fonts\.gstatic\.com|fonts\.bunny\.net` +
		`|use\.typekit\.net|cdn\.jsdelivr\.net|unpkg\.com|cdnjs\.cloudflare\.com)`

	// THE TOKEN LAYER IS tokens.css ALONE, not all of web/src/styles. canonical §17 is normative
	// and says "outside the token layer"; .claude/rules/web.md says "outside web/src/styles/",
	// which is looser. AGENTS.md resolves that conflict in canonical's favour, so these rules
	// implement canonical and the rules file's wording is the thing that needs correcting.
	tokenLayer := regexp.MustCompile(`^web/src/styles/tokens\.css:`)

	return []textRule{
		{
			// The eslint twin (no-restricted-globals, proven by TestWebLint_BareFetch_FailsLint) is
			// AST-aware and catches shapes this cannot, but it needs a Node toolchain — so on a job
			// that has only Go, this is law 4's entire enforcement.
			//
			// The glob is `*.ts*`, and narrowing it to `*.tsx` would silently stop scanning every
			// hook, loader and lib file in the SPA — which is where a raw fetch actually gets
			// written.
			id: "WEB001", desc: "raw fetch/XMLHttpRequest outside web/src/api",
			tree: "web/src", include: []string{"*.ts*"},
			pattern: regexp.MustCompile(`\b(fetch|XMLHttpRequest)\s*\(`),
			reject:  []*regexp.Regexp{generatedClient}, strip: hashAndSlash,
		},
		{
			// internal/cms accepts untrusted rich text — articles, comments, signatures written by
			// whoever the officers gave an account to — and this is the one prop that turns it into
			// script running with the reader's session. The repo has no eslint react/no-danger
			// rule, so unlike WEB001 this is not defence in depth: it is the only thing standing
			// between a CMS field and stored XSS.
			id: "WEB002", desc: "dangerouslySetInnerHTML",
			tree: "web/src", include: []string{"*.tsx"},
			pattern: regexp.MustCompile(`dangerouslySetInnerHTML`), strip: hashAndSlash,
		},
		{
			// docs/design/09-frontend-and-design-system.md §3: the type is "loaded self-hosted, not
			// through the Google Fonts @import the source sheet uses — the binary serves the SPA
			// offline and a render-blocking third-party request contradicts that". §5 says the same
			// of the icon subset.
			//
			// This is a rule rather than a sentence in a document because the tempting line is
			// already written and sitting in the repository: mockups/nocturne/styles.css opens with
			// `@import url('https://fonts.googleapis.com/...')`, and the mockups are transcribed on
			// purpose. Copying that one line is a single keystroke, it looks like fidelity, and
			// nothing about the result looks wrong on a developer's machine — it only fails on a
			// volunteer's LAN-only server, where the request hangs and the page renders in the
			// wrong face after a three-second block.
			//
			// Deliberately broader than "assets": an `<a href="https://…">` would trip it too, and
			// that is a decision worth making in a diff rather than a hole to leave open (a link
			// leaks a referrer and a screen full of them is a design change). Nothing in the tree
			// needs one today; the day one does, it is one allowlisted line here.
			//
			// index.html is scanned as well as web/src: it is the document the browser parses
			// first, so a render-blocking <link> or <script> in its <head> is the worst version of
			// exactly this bug and the only place it can be written outside src.
			id: "WEB003", desc: "off-origin URL in the SPA — type, icons and every other asset are " +
				"self-hosted so the binary works offline (docs/design/09 §3)",
			tree: "web/src", extra: []string{"web/index.html"},
			include: []string{"*.css", "*.ts", "*.tsx", "*.html"},
			pattern: regexp.MustCompile(thirdParty),
			reject:  []*regexp.Regexp{generatedClient}, strip: webComments,
			quiet: true,
		},
		{
			// canonical §17 promises this rule and names ESLint as the mechanism. ESLint does not
			// lint CSS, so the promise went unkept and web/src/styles/base.css shipped a
			// `text-underline-offset: 3px` that nothing caught.
			//
			// SCOPED TO CSS, because canonical §17's rule needs TWO mechanisms and this is the dumb
			// half — the same split law 4 uses above, for the same reason. A scan over *.tsx cannot
			// tell a value from prose: web/src/routes/design.tsx renders the sentences "Base unit
			// 4px x 0.70" and "a 1px accent border on transparent" as visible copy, and both would
			// trip it. eslint.config.js covers TS/TSX by AST in `lint / web`, where it can see
			// `style={{ padding: "4px" }}` and know that JSX text is not a string literal. NEITHER
			// IS SUFFICIENT ALONE and removing either is a §17 regression.
			id: "DS001", desc: "raw hex colour outside web/src/styles/tokens.css — a colour with no " +
				"named rung is a colour no guild theme can reach (canonical §17)",
			tree: "web/src", include: []string{"*.css"},
			pattern: regexp.MustCompile(`#([0-9a-fA-F]{8}|[0-9a-fA-F]{6}|[0-9a-fA-F]{4}|[0-9a-fA-F]{3})\b`),
			reject:  []*regexp.Regexp{tokenLayer}, strip: cssComments,
			quiet: true,
		},
		{
			id: "DS002", desc: "raw px outside web/src/styles/tokens.css — a value the scale does not " +
				"carry gets a named rung, not an inline literal (canonical §17)",
			tree: "web/src", include: []string{"*.css"},
			pattern: regexp.MustCompile(`[0-9]+(\.[0-9]+)?px`),
			reject:  []*regexp.Regexp{tokenLayer}, strip: cssComments,
			quiet: true,
		},
	}
}

// --- Migrations are forward-only ----------------------------------------------------------------

func migrationRules() []textRule {
	return []textRule{
		{
			id: "MIG001", desc: "DDL inside a goose Down block (migrations are forward-only)",
			tree: "db/migrations-sqlite", include: []string{"*.sql"},
			pattern: regexp.MustCompile(`^\s*(DROP|ALTER)\b`), strip: hashAndSlash,
		},
		{
			// Atlas emits `dkp_meta` — MySQL-style backtick quoting, which SQLite accepts as a
			// compatibility extension. sqlc's SQLite parser does not, and it fails SILENTLY: the
			// schema file is accepted, no table is parsed out of it, and the error surfaces as
			// `relation "x" does not exist` against the query file, which is the one file that was
			// correct. scripts/new-migration.sh rewrites them to double quotes at generation time;
			// this rule catches a migration that got here another way, because "make gen produced
			// an empty sqlitegen package" is not a thing anyone notices.
			id: "MIG002", desc: "backtick-quoted identifier in a migration (sqlc parses no table " +
				"and says so about the query)",
			tree: "db/migrations-sqlite", include: []string{"*.sql"},
			pattern: regexp.MustCompile("`"), strip: hashAndSlash,
		},
	}
}

// --- Supply chain -------------------------------------------------------------------------------

func supplyChainRules() []textRule {
	return []textRule{
		{
			// `go test -update` regenerates test/golden/ to match whatever the code currently
			// produces. Run on a laptop that is a deliberate act; run in CI it is a machine that
			// makes every golden assertion agree with the change under test, and the parser suite —
			// the thing standing between a P99 log format and silently wrong attendance — stops
			// being a test at all. AGENTS.md bans it in prose; this is the mechanism, and nothing
			// else enforces it: there is no linter for a shell command inside a YAML string.
			//
			// Only `run:` lines count, plus deeply-indented continuation lines inside a `run: |`
			// block. A comment explaining the fence is not a breach of it.
			id: "GOLD001", desc: "'-update' in a CI command rewrites golden files",
			tree: ".github/workflows", include: nil,
			pattern: regexp.MustCompile(`^\s*(-?\s*(run|cmd):|\s{4,})[^#]*(\s|^)-{1,2}update\b`),
			reject: []*regexp.Regexp{
				// `apt-get update` is a package index refresh; `actions/…`, Dependabot and Renovate
				// lines are the tooling that keeps the pins current. None of them rewrites a golden
				// file, and a rule that fired on them would be routed around within a week.
				regexp.MustCompile(`(?i)allow_?update_?branch|update-branch|dependabot|renovate|apt-get|actions/`),
			},
			strip: hashOnly,
			quiet: true,
		},
		{
			// Any `uses:` that is not a local ./ action must carry a 40-char SHA. The action name
			// itself contains '/', so the exclusion has to be anchored on the value, not on a
			// character class.
			id: "PIN001", desc: "action not pinned to a 40-char commit SHA",
			tree: ".github", include: nil,
			pattern: regexp.MustCompile(`^[[:space:]]*(-[[:space:]]*)?uses:[[:space:]]*[^[:space:]]+`),
			reject: []*regexp.Regexp{
				regexp.MustCompile(`uses:[[:space:]]*\./`),
				regexp.MustCompile(`@[0-9a-f]{40}`),
			},
			strip: hashOnly,
			quiet: true,
		},
		{
			// Multi-arch images are produced by CROSS-COMPILATION joined with `docker buildx
			// imagetools`, never by an emulated build. Go cross-compiles at native speed and
			// modernc.org/sqlite is pure Go, so the arm64 and arm/v7 images cost one more `go
			// build` each — about 90 seconds for the whole matrix.
			//
			// The rule exists because a QEMU-emulated build runs 10-25x slower, which turns a
			// 90-second image stage into 20-50 minutes, and the predictable response to that is
			// someone deleting the arm64 leg "to make CI fast" — silently dropping the architecture
			// half this audience runs on (older Raspberry Pis). Banning the string is what stops
			// `docker/setup-qemu-action` or a `--platform` emulation from being reintroduced under
			// wall-clock pressure.
			//
			// Comments are stripped: the committed workflows say "No QEMU" in prose to document the
			// choice, and prose about a rule is not a breach of it.
			id: "QEMU001", desc: "QEMU in a workflow — multi-arch is cross-compiled, never emulated",
			tree: ".github/workflows", include: nil,
			pattern: regexp.MustCompile(`(?i)qemu`), strip: hashOnly,
			quiet: true,
		},
	}
}
