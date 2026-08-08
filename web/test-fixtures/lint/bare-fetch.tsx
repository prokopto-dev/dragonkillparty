// DELIBERATE VIOLATION — do not "fix" this file.
//
// A component making a bare fetch() outside src/api. test/repo/web_lint_test.go runs eslint against
// this file and requires a non-zero exit naming no-restricted-globals. If eslint ever passes it,
// law 4's AST-aware half has gone blind. See .claude/rules/web.md — the SPA is an API client.
//
// It lives under web/test-fixtures/lint/, NOT under web/src, so neither the project's own
// `make lint` (eslint scopes to src/**) nor the WEB001 grep gate (which scans web/src) flags it —
// only the negative test that targets it deliberately does. This mirrors the Go lint fixtures under
// testdata/lintfixtures/, which live outside every scanned tree for the same reason.
export function BareFetch() {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const load = async (): Promise<any> => {
    const res = await fetch("/api/v1/meta");
    return res.json();
  };
  void load;
  return null;
}
