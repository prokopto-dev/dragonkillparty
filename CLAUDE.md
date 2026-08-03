@AGENTS.md

## Claude Code

Prefer plan mode for anything touching `internal/ledger/`, `internal/strategy/`, `internal/auth/`,
or `db/schema.hcl` — these are the four places where a plausible-looking wrong change is expensive
and hard to spot in review.

Path-scoped rules live in `.claude/rules/` and load when you read a matching file. Repeatable
procedures live in `.claude/skills/`. Read them; don't re-derive them.

Before writing a new endpoint, read `internal/api/EXAMPLE_ENDPOINT.md`. Before writing a query, read
`db/RECIPES.md`. Copying an in-repo worked example is more reliable than recalling a library API.
