import { mkdirSync, rmSync } from "node:fs";
import { fileURLToPath, URL } from "node:url";

import { defineConfig, devices } from "@playwright/test";

/*
 * The end-to-end harness, run by `make test-e2e` (scripts/test-e2e.sh).
 *
 * IT RUNS AGAINST THE BUILT BINARY, NOT AGAINST VITE. docs/design/04-testing.md §"The twelve
 * journeys" is explicit — "Runs against the shipped binary, not a dev server" — and the reason is
 * that half of what E2E uniquely proves lives in the Go half: the embedded SPA, its cache headers,
 * its CSP, and the index.html fallback that gives the client-side router /_design at all. A Vite dev
 * server serves none of that, so a suite pointed at :5173 would be green on a binary that ships a
 * broken embed.
 *
 * Scope today is the Nocturne design system's own guarantees, rendered on /_design — the page that
 * exists precisely so one small suite covers the whole vocabulary rather than one screen's worth.
 * The twelve product journeys are Phase 3+ and land on this harness; see issue #33.
 */

// Everything is resolved from this file's own location rather than from process.cwd(), so
// `pnpm exec playwright test` from web/ and `make test-e2e` from the repo root behave identically.
const here = fileURLToPath(new URL(".", import.meta.url));
const repoRoot = fileURLToPath(new URL("..", import.meta.url));

// A fixed port, because Playwright's webServer.url must be known before the server starts. Not 8080:
// a developer with `make dev` running would otherwise have the suite silently talk to their dev
// server — reuseExistingServer is false, so the boot would fail instead, but on the wrong port.
const port = Number(process.env.DKP_E2E_PORT ?? 8099);
const baseURL = `http://127.0.0.1:${port}`;

// The binary under test. `make build` writes bin/dkp; CI downloads the same artefact from
// `build / binary` rather than building a second time (a second Go+Vite build would put this job
// past the whole CI wall-clock budget — see .github/workflows/ci.yml).
const binary = process.env.DKP_E2E_BINARY ?? `${repoRoot}bin/dkp`;

// A throwaway data directory per run. Recreated rather than reused: a suite that inherits the
// previous run's database is a suite whose first failure is unreproducible.
const dataDir = `${here}.e2e-data`;
rmSync(dataDir, { recursive: true, force: true });
mkdirSync(dataDir, { recursive: true });

export default defineConfig({
  testDir: "./e2e",
  // CI uploads this directory on failure (playwright-trace-<shard>), so traces and screenshots have
  // to land inside it.
  outputDir: "./test-results",

  fullyParallel: true,
  forbidOnly: process.env.CI !== undefined,

  // ZERO RETRIES. A flaky e2e is quarantined, never retried — .github/workflows/ci.yml's e2e step,
  // scripts/test-e2e.sh's `--retries=0`, and docs/design/04-testing.md §"Flake policy" now all say
  // the same thing, which they did not before #60: that section specified `retries: 1` plus a
  // fail-on-flaky report, and the two mechanisms cannot both be followed. Retries-0 won as the
  // conservative reading — it can only turn a flake into a red build, never into a green one.
  //
  // Raising this to 1 without also passing --fail-on-flaky-tests is strictly weaker than either
  // policy: Playwright does not fail the build on a `flaky` result by default, so a pass-on-retry
  // would go green.
  retries: 0,

  reporter: [
    ["list"],
    // scripts/test-e2e.sh reads this to assert the run was not vacuous. A shard that selects zero
    // tests exits 0 from Playwright and would otherwise report a green browser suite that ran no
    // browser — the same failure mode `make test-property`'s test counter exists for.
    ["json", { outputFile: "./test-results/results.json" }],
  ],

  use: {
    baseURL,
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
  },

  // Chromium only on the PR path. docs/design/04-testing.md puts the
  // {Chromium, Firefox, WebKit} × {desktop, mobile} matrix in the nightly/pre-release lane; three
  // browser downloads per PR buys coverage of engine differences this suite is not yet asserting.
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],

  webServer: {
    command: `${binary} serve --addr 127.0.0.1:${port}`,
    // /healthz, not /readyz: canonical §13 keeps /healthz off the database, so it answers as soon as
    // the listener is up. Readiness is a migration question and this suite has no schema to wait on.
    url: `${baseURL}/healthz`,
    // Never reuse: a stale server is a stale SPA, and the whole point is to exercise the binary this
    // change produced.
    reuseExistingServer: false,
    stdout: "pipe",
    stderr: "pipe",
    env: {
      DKP_DB_PATH: `${dataDir}/dkp.db`,
      DKP_AUTO_MIGRATE: "true",
    },
  },
});
