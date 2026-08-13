// The config-shaped rule catalogue: DATA, in the order the rules run and report.
//
// Read it as a table. Each block is one (rule, tree) pair, exactly as the shell script wrote one
// `gate` call per pair — so a rule covering two trees appears twice and losing one of them is
// visible in a diff rather than hidden in a loop that quietly lost an element.
//
// WHY THIS IS HCL AND NOT GO (issue #171, ADR-0018 chose the Go table and ADR-0021 revisits it):
// everything here is configuration — a tree, a glob, a pattern, an allowlist, the comment openers to
// strip — and it is read far more often than it is changed, by people deciding whether their change
// is allowed rather than by people editing the engine. A Go table asks a non-Go reader to read Go to
// learn what is banned, which was ADR-0018's stated cost. The rules that need BEHAVIOUR are not here
// and cannot be: the Go-syntax laws are `go/parser` analyzers in ast.go, ENUM001 parses the schema,
// MIG001 and MIG002 read a migration's structure, MIG003 hashes files and reads git history, and
// ADR001 reads a diff. A rule that needs a field this schema does not have is a rule that belongs in
// one of those, not a new attribute here.
//
// PATTERNS ARE HEREDOCS. HCL does not process backslash escapes inside one, so a regexp appears here
// exactly as it would in Go source — no doubling, nothing to get wrong in transcription. The decoder
// trims surrounding whitespace and compiles it; a pattern that does not compile is a GATE FAILURE
// and never an empty catalogue (see catalogue.go).
//
// EVERY FIELD IS OPTIONAL EXCEPT description, tree AND pattern:
//
//	extra    files scanned in addition to the tree
//	include  base-name globs; absent means every file in the tree
//	reject   patterns applied to the rendered "path:line:text" of a hit, which is what lets an
//	         allowlist key on a path as readily as on the text
//	strip    whole-line comment openers dropped before matching; absent strips nothing
//	quiet    suppress the "tree does not exist yet" skip line

// --- Money is integer centipoints -------------------------------------------------------------

// MONEY002 reaches SQL embedded in Go as well as db/*.sql, because SQL arrives at SQLite as a Go
// string literal just as readily as from a .sql file — and when the rule was written db/ did not
// exist yet, so the Go half was the only one of the pair that ran.
//
// The call returns a REAL where sum() returns an INTEGER, so a single one silently converts the
// centipoint ledger to floating point: no error, no warning, just a balance that is wrong by a
// fraction of a point for years.
//
// The banned call is spelled out below rather than assembled from pieces. In rules.go it could not
// be: that file lives under internal/, which is a tree MONEY002 scans for *.go, so writing the call
// in the rule's own source made the catalogue fire on itself. This file is not Go, so the glob does
// not reach it and the pattern can say what it means.
rule "MONEY002" {
  description = "SQLite total() returns a float — use sum()"
  tree        = "db"
  include     = ["*.sql"]
  strip       = ["#", "//"]

  pattern = <<-RE
    \btotal\s*\(
  RE
}

rule "MONEY002" {
  description = "SQLite total() returns a float — use sum()"
  tree        = "internal"
  include     = ["*.go"]
  strip       = ["#", "//"]

  pattern = <<-RE
    \btotal\s*\(
  RE
}

rule "MONEY002" {
  description = "SQLite total() returns a float — use sum()"
  tree        = "cmd"
  include     = ["*.go"]
  strip       = ["#", "//"]

  pattern = <<-RE
    \btotal\s*\(
  RE
}

// SQLite's type affinity makes this quiet in a way the Go ban is not: a REAL column accepts every
// integer a correct writer inserts, so the taint is invisible until a value arrives that cannot be
// represented exactly — and by then the column holds years of history. There is no compensating
// linter; this rule is the only mechanism.
rule "MONEY003" {
  description = "REAL/NUMERIC/DECIMAL column type"
  tree        = "db"
  include     = ["*.sql"]
  strip       = ["#", "//"]

  pattern = <<-RE
    \b(REAL|NUMERIC|DECIMAL)\b
  RE
}

// --- Law 4: the SPA calls only the generated client ---------------------------------------------

// The eslint twin (no-restricted-globals, proven by TestWebLint_BareFetch_FailsLint) is AST-aware
// and catches shapes this cannot, but it needs a Node toolchain — so on a job that has only Go, this
// is law 4's entire enforcement.
//
// The glob is `*.ts*`, and narrowing it to `*.tsx` would silently stop scanning every hook, loader
// and lib file in the SPA — which is where a raw fetch actually gets written.
//
// web/src/api is where a fetch is SUPPOSED to happen: it is written by `make gen` from
// openapi/openapi.json.
rule "WEB001" {
  description = "raw fetch/XMLHttpRequest outside web/src/api"
  tree        = "web/src"
  include     = ["*.ts*"]
  strip       = ["#", "//"]
  reject      = ["^web/src/api/"]

  pattern = <<-RE
    \b(fetch|XMLHttpRequest)\s*\(
  RE
}

// internal/cms accepts untrusted rich text — articles, comments, signatures written by whoever the
// officers gave an account to — and this is the one prop that turns it into script running with the
// reader's session. The repo has no eslint react/no-danger rule, so unlike WEB001 this is not
// defence in depth: it is the only thing standing between a CMS field and stored XSS.
rule "WEB002" {
  description = "dangerouslySetInnerHTML"
  tree        = "web/src"
  include     = ["*.tsx"]
  strip       = ["#", "//"]

  pattern = <<-RE
    dangerouslySetInnerHTML
  RE
}

// docs/design/09-frontend-and-design-system.md §3: the type is "loaded self-hosted, not through the
// Google Fonts @import the source sheet uses — the binary serves the SPA offline and a
// render-blocking third-party request contradicts that". §5 says the same of the icon subset.
//
// This is a rule rather than a sentence in a document because the tempting line is already written
// and sitting in the repository: mockups/nocturne/styles.css opens with
// `@import url('https://fonts.googleapis.com/...')`, and the mockups are transcribed on purpose.
// Copying that one line is a single keystroke, it looks like fidelity, and nothing about the result
// looks wrong on a developer's machine — it only fails on a volunteer's LAN-only server, where the
// request hangs and the page renders in the wrong face after a three-second block.
//
// BANNED: ANY off-origin URL literal — an absolute `https?://host`, or a protocol-relative `//host`.
// Not a list of asset-bearing shapes. The first version of this rule did enumerate them: `url(…)`,
// `@import url(…)`, `<link …>`, and the known CDNs by name. It was reviewed and it was wrong —
// `<script src="https://…">`, `<img src="//…">` and the perfectly valid `@import "https://…";` (the
// quoted spelling, no `url()`) all walked straight through, and every one of them makes the SPA
// depend on another origin exactly as much as the shapes that were caught. An enumeration is only
// ever as complete as the imagination of whoever wrote it, and the failure mode is silent.
//
// The host must contain a dot. That is what separates `//example.com` from a trailing `//` JS
// comment or a doubled slash in a path, and it is why the rule can be this broad without being
// noisy.
//
// The named font and script CDNs stay in the pattern even though the generic rule subsumes them:
// they cost nothing, they name the specific mistake docs/design/09 §3 is about, and they still fire
// on a bare `fonts.googleapis.com` written with no scheme at all.
//
// Deliberately broader than "assets": an `<a href="https://…">` would trip it too, and that is a
// decision worth making in a diff rather than a hole to leave open (a link leaks a referrer and a
// screen full of them is a design change). Nothing in the tree needs one today; the day one does, it
// is one allowlisted line here.
//
// index.html is scanned as well as web/src: it is the document the browser parses first, so a
// render-blocking <link> or <script> in its <head> is the worst version of exactly this bug and the
// only place it can be written outside src.
// The strip set is CSS's openers plus HTML's, because this rule reads index.html as well as the
// stylesheets. `*` is the continuation line of a block comment.
rule "WEB003" {
  description = "off-origin URL in the SPA — type, icons and every other asset are self-hosted so the binary works offline (docs/design/09 §3)"
  tree        = "web/src"
  extra       = ["web/index.html"]
  include     = ["*.css", "*.ts", "*.tsx", "*.html"]
  strip       = ["/*", "*", "//", "#", "<!--"]
  reject      = ["^web/src/api/"]
  quiet       = true

  pattern = <<-RE
    ((https?:)?//[a-zA-Z0-9-]+(\.[a-zA-Z0-9-]+)+|fonts\.googleapis\.com|fonts\.gstatic\.com|fonts\.bunny\.net|use\.typekit\.net|cdn\.jsdelivr\.net|unpkg\.com|cdnjs\.cloudflare\.com)
  RE
}

// canonical §17 promises this rule and names ESLint as the mechanism. ESLint does not lint CSS, so
// the promise went unkept and web/src/styles/base.css shipped a `text-underline-offset: 3px` that
// nothing caught.
//
// SCOPED TO CSS, because canonical §17's rule needs TWO mechanisms and this is the dumb half — the
// same split law 4 uses above, for the same reason. A scan over *.tsx cannot tell a value from
// prose: web/src/routes/design.tsx renders the sentences "Base unit 4px x 0.70" and "a 1px accent
// border on transparent" as visible copy, and both would trip it. eslint.config.js covers TS/TSX by
// AST in `lint / web`, where it can see `style={{ padding: "4px" }}` and know that JSX text is not a
// string literal. NEITHER IS SUFFICIENT ALONE and removing either is a §17 regression.
//
// THE TOKEN LAYER IS tokens.css ALONE, not all of web/src/styles. canonical §17 is normative and
// says "outside the token layer"; .claude/rules/web.md says "outside web/src/styles/", which is
// looser. AGENTS.md resolves that conflict in canonical's favour, so these two rules implement
// canonical and the rules file's wording is the thing that needs correcting.
// The strip set covers a component stylesheet, whose every value is documented in prose beside it —
// "a 1px accent border", "fades over 48px at each end". Both DS rules would otherwise fire on the
// sentences explaining them.
rule "DS001" {
  description = "raw hex colour outside web/src/styles/tokens.css — a colour with no named rung is a colour no guild theme can reach (canonical §17)"
  tree        = "web/src"
  include     = ["*.css"]
  strip       = ["/*", "*", "//"]
  reject      = ["^web/src/styles/tokens\\.css:"]
  quiet       = true

  pattern = <<-RE
    #([0-9a-fA-F]{8}|[0-9a-fA-F]{6}|[0-9a-fA-F]{4}|[0-9a-fA-F]{3})\b
  RE
}

rule "DS002" {
  description = "raw px outside web/src/styles/tokens.css — a value the scale does not carry gets a named rung, not an inline literal (canonical §17)"
  tree        = "web/src"
  include     = ["*.css"]
  strip       = ["/*", "*", "//"]
  reject      = ["^web/src/styles/tokens\\.css:"]
  quiet       = true

  pattern = <<-RE
    [0-9]+(\.[0-9]+)?px
  RE
}

// --- Supply chain -------------------------------------------------------------------------------

// `go test -update` regenerates test/golden/ to match whatever the code currently produces. Run on a
// laptop that is a deliberate act; run in CI it is a machine that makes every golden assertion agree
// with the change under test, and the parser suite — the thing standing between a P99 log format and
// silently wrong attendance — stops being a test at all. AGENTS.md bans it in prose; this is the
// mechanism, and nothing else enforces it: there is no linter for a shell command inside a YAML
// string.
//
// Only `run:` lines count, plus deeply-indented continuation lines inside a `run: |` block. A
// comment explaining the fence is not a breach of it.
//
// The rejected words are the tooling that keeps the pins current: `apt-get update` is a package
// index refresh, and the Dependabot, Renovate and `actions/…` lines rewrite no golden file. A rule
// that fired on them would be routed around within a week.
// The strip set is `#` alone, as it is for every workflow rule below: YAML has one comment syntax,
// and a `//` in a workflow is part of a URL or a path far more often than it is a comment.
rule "GOLD001" {
  description = "'-update' in a CI command rewrites golden files"
  tree        = ".github/workflows"
  strip       = ["#"]
  quiet       = true

  reject = [
    <<-RE
      (?i)allow_?update_?branch|update-branch|dependabot|renovate|apt-get|actions/
    RE
  ]

  pattern = <<-RE
    ^\s*(-?\s*(run|cmd):|\s{4,})[^#]*(\s|^)-{1,2}update\b
  RE
}

// Any `uses:` that is not a local ./ action must carry a 40-char SHA. The action name itself
// contains '/', so the exclusion has to be anchored on the value, not on a character class.
rule "PIN001" {
  description = "action not pinned to a 40-char commit SHA"
  tree        = ".github"
  strip       = ["#"]
  quiet       = true

  reject = [
    <<-RE
      uses:[[:space:]]*\./
    RE
    ,
    <<-RE
      @[0-9a-f]{40}
    RE
  ]

  pattern = <<-RE
    ^[[:space:]]*(-[[:space:]]*)?uses:[[:space:]]*[^[:space:]]+
  RE
}

// Multi-arch images are produced by CROSS-COMPILATION joined with `docker buildx imagetools`, never
// by an emulated build. Go cross-compiles at native speed and modernc.org/sqlite is pure Go, so the
// arm64 and arm/v7 images cost one more `go build` each — about 90 seconds for the whole matrix.
//
// The rule exists because a QEMU-emulated build runs 10-25x slower, which turns a 90-second image
// stage into 20-50 minutes, and the predictable response to that is someone deleting the arm64 leg
// "to make CI fast" — silently dropping the architecture half this audience runs on (older Raspberry
// Pis). Banning the string is what stops `docker/setup-qemu-action` or a `--platform` emulation from
// being reintroduced under wall-clock pressure.
//
// Comments are stripped: the committed workflows say "No QEMU" in prose to document the choice, and
// prose about a rule is not a breach of it.
rule "QEMU001" {
  description = "QEMU in a workflow — multi-arch is cross-compiled, never emulated"
  tree        = ".github/workflows"
  strip       = ["#"]
  quiet       = true

  pattern = <<-RE
    (?i)qemu
  RE
}

// --- The AGPL firewall ---------------------------------------------------------------------------
//
// EQdkp Plus is AGPL-3.0; its game modules are CC BY-NC-SA. Reading a user's database at runtime is
// fine. Transcribing their PHP is a licence violation — and it is exactly what a helpful agent does
// when the task is "match EQdkp's behaviour".
//
// THIS FILE IS ITSELF ALLOWLISTED, and it is the only file in internal/ that is. The rule's pattern
// has to name the identifiers it bans, and a file naming them is a file the firewall would fire on;
// internal/repogate/agpl.go carried the same exemption while the pattern lived there, and
// scripts/repo-gates.sh before that. The exemption stays ONE FILE WIDE rather than one package wide:
// nothing else in internal/repogate may spell an EQdkp identifier, and nothing else needs to.
//
// The rest of the allowlist is what keeps the importer possible at all: reading a user's database at
// runtime requires naming their tables somewhere. legacy_names.go is that somewhere, and the compat
// shim answers their api.php function names. The documentation trees are listed because a design
// document that could not name the thing it is migrating from would be useless — though of those
// entries only the first three are reachable, since the scan covers internal, web, cmd and db alone.
// The unreachable ones are kept: an allowlist that describes the whole intent survives a tree being
// added to the scan, and dropping one silently would be a firewall change nobody reads.
//
// AGPL001 — the four identifiers are distinctive enough that a hit is always transcription and never
// coincidence.
//
// NO COMMENT STRIPPING, and this is the one rule where that matters. Everywhere else a banned token
// inside a comment is prose about the rule; here it is the thing itself. Pasting AGPL source into a
// Go comment "just as a reference" infringes exactly as much as pasting it into code. A well-meaning
// refactor that gave this rule the same `strip` as its neighbours — for consistency — would open the
// firewall.
//
// NO INCLUDE GLOB either: a transcription can land in a .go file, a .ts file, a schema or a
// migration, and a firewall that only read the extensions somebody thought of has a hole per
// extension.
//
// One block per tree, because a transcription can land in any of them and losing a tree from the
// list is a silent hole.
rule "AGPL001" {
  description = "EQdkp Plus identifier outside the allowlisted files"
  tree        = "internal"
  quiet       = true

  reject = [
    <<-RE
      ^(internal/importer/legacy_names\.go|internal/api/compat/|internal/repogate/rules\.hcl|docs/|\.claude/|\.github/|scripts/repo-gates\.sh|AGENTS\.md|README\.md|ROADMAP\.md|CONTRIBUTING\.md)
    RE
  ]

  pattern = <<-RE
    \b(pdh_|gen_class|plus_exchange|__multidkp2event)
  RE
}

rule "AGPL001" {
  description = "EQdkp Plus identifier outside the allowlisted files"
  tree        = "web"
  quiet       = true

  reject = [
    <<-RE
      ^(internal/importer/legacy_names\.go|internal/api/compat/|internal/repogate/rules\.hcl|docs/|\.claude/|\.github/|scripts/repo-gates\.sh|AGENTS\.md|README\.md|ROADMAP\.md|CONTRIBUTING\.md)
    RE
  ]

  pattern = <<-RE
    \b(pdh_|gen_class|plus_exchange|__multidkp2event)
  RE
}

rule "AGPL001" {
  description = "EQdkp Plus identifier outside the allowlisted files"
  tree        = "cmd"
  quiet       = true

  reject = [
    <<-RE
      ^(internal/importer/legacy_names\.go|internal/api/compat/|internal/repogate/rules\.hcl|docs/|\.claude/|\.github/|scripts/repo-gates\.sh|AGENTS\.md|README\.md|ROADMAP\.md|CONTRIBUTING\.md)
    RE
  ]

  pattern = <<-RE
    \b(pdh_|gen_class|plus_exchange|__multidkp2event)
  RE
}

rule "AGPL001" {
  description = "EQdkp Plus identifier outside the allowlisted files"
  tree        = "db"
  quiet       = true

  reject = [
    <<-RE
      ^(internal/importer/legacy_names\.go|internal/api/compat/|internal/repogate/rules\.hcl|docs/|\.claude/|\.github/|scripts/repo-gates\.sh|AGENTS\.md|README\.md|ROADMAP\.md|CONTRIBUTING\.md)
    RE
  ]

  pattern = <<-RE
    \b(pdh_|gen_class|plus_exchange|__multidkp2event)
  RE
}

// AGPL002 is a narrower rule than AGPL001, and a different failure. AGPL001 catches distinctive
// EQdkp identifiers that could only arrive by transcription. These are ordinary names that arrive by
// a subtler route: someone reads docs/design/05-migration.md's list of EQdkp `<prefix>config` keys —
// which is there because the importer must read them — and writes one into DKP's own schema instead
// of DKP's name for the same concept.
//
// That is not hypothetical. docs/design/02-api-design.md's `/guild` row shipped `inactive_period`
// and `auto_set_active` for exactly this reason, and `auto_set_active` is the OPPOSITE control from
// DKP's `auto_set_inactive` — a bot written from the published contract would have set the wrong
// value. Some keys in that same row had been correctly renamed (`dkp_name` -> `points_label`,
// `guildtag` -> `tag`), which is what made the survivors invisible.
//
// SCOPE IS db/ ONLY, AND THE DOCUMENTATION HALF IS DELIBERATELY NOT GATED. A scan over the design
// documents cannot tell a leak from a lesson: docs/design/01-domain-model.md names `show_twinks` to
// explain why DKP rejects that design, and the correction notes accompanying this rule quote the
// banned names in order to document them. Both are correct writing this rule would reject, and a
// gate that is usually wrong is a gate people route around. The wire half is covered where it is
// unambiguous — SPEC008 in internal/specgate reads the generated OpenAPI document, which contains
// field names and no prose.
//
// `hide_inactive` and `timezone` are in EQdkp's list too and are ALSO DKP's own column names: the
// concepts coincide and the words are ordinary English. They are not banned. Every name below is one
// DKP does not use, so a hit is always a transcription and never a collision.
rule "AGPL002" {
  description = "EQdkp Plus config key used as a DKP schema name — use DKP's own name (canonical §15, §16)"
  tree        = "db"
  strip       = ["#", "//"]
  quiet       = true

  pattern = <<-RE
    \b(inactive_period|auto_set_active|round_activate|round_precision|dkp_name|guildtag|servername|show_twinks|detail_twink|special_members|default_game|enable_leaderboard)\b
  RE
}
