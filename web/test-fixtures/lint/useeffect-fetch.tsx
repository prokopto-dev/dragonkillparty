// DELIBERATE VIOLATION — do not "fix" this file.
//
// A useEffect whose body contains a fetch. This is the shape scripts/repo-gates.sh WEB001 cannot
// see, because grep has no AST — only the custom no-restricted-syntax rule in eslint.config.js
// catches the useEffect wrapper. test/repo/web_lint_test.go runs eslint against this file and
// requires a non-zero exit naming no-restricted-syntax.
//
// A useEffect containing a fetch re-fetches on every render path nobody thought about, has no
// cancellation, no dedupe, no cache and no error boundary. Reads go through useQuery.
//
// It lives under web/test-fixtures/lint/, outside web/src, so the project's own gates do not flag
// it — see bare-fetch.tsx.
import { useEffect, useState } from "react";

export function UseEffectFetch() {
  const [n] = useState(0);
  useEffect(() => {
    void fetch("/api/v1/meta").then((r) => r.json());
  }, []);
  return <span>{n}</span>;
}
