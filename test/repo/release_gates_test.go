package repo_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// These tests assert the SHAPE of the release train's config files. They cannot run `docker buildx`
// or `cosign` in a unit test, and they do not try to: what is checkable offline, and what a
// regression would actually break, is the structural guarantees — the base is scratch, the container
// runs unprivileged, the healthcheck is not the database-touching one, moving tags advance only
// after smoke, and the high-risk dependencies never automerge. Each is a line an agent under
// wall-clock pressure would plausibly "simplify" away, so each is pinned.

// readRepoFile reads a repo-relative file, failing the test if it is absent.
func readRepoFile(t *testing.T, rel string) string {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(repoRoot(t), filepath.FromSlash(rel)))
	require.NoErrorf(t, err, "%s must exist", rel)

	return string(data)
}

// TestDockerfile_IsScratchAndHardened asserts the PR 7 image criteria: multi-stage ending
// FROM scratch, CA certs and tzdata copied in, USER 65532, VOLUME /data, and the HEALTHCHECK
// invoking `dkp healthcheck` — NOT `dkp doctor`, which touches the database (canonical §13).
func TestDockerfile_IsScratchAndHardened(t *testing.T) {
	t.Parallel()

	df := readRepoFile(t, "deploy/Dockerfile")

	require.Contains(t, df, "FROM scratch", "the final stage must be FROM scratch")
	require.Contains(t, df, "USER 65532", "the container must run as the non-root uid 65532")
	require.Contains(t, df, "VOLUME /data", "the writable volume must be declared")
	require.Contains(t, df, "ca-certificates.crt", "CA certificates must be copied into the scratch image")
	require.Contains(t, df, "zoneinfo", "the tzdata/zoneinfo database must be copied into the scratch image")

	require.Contains(t, df, "HEALTHCHECK", "the image must declare a HEALTHCHECK")
	require.Contains(t, df, "dkp\", \"healthcheck", "HEALTHCHECK must invoke `dkp healthcheck`")

	// Check the HEALTHCHECK instruction itself, not the whole file: the surrounding comments
	// legitimately name `dkp doctor` to explain why it is NOT used. What must never happen is the
	// instruction invoking it — a database-touching healthcheck lets Docker kill the container
	// mid-migration (canonical §13).
	hc := healthcheckInstruction(t, df)
	require.NotContains(t, hc, "doctor",
		"canonical §13: the HEALTHCHECK must be `dkp healthcheck`, never `dkp doctor`:\n  %s", hc)

	// A multi-stage build: at least two FROM lines, one of them the build stage.
	require.GreaterOrEqual(t, strings.Count(df, "FROM "), 2, "the Dockerfile must be multi-stage")
}

// healthcheckInstruction returns the full HEALTHCHECK instruction, joining any `\`-continued lines,
// so a test can assert on the command it runs rather than on the whole file.
func healthcheckInstruction(t *testing.T, dockerfile string) string {
	t.Helper()

	lines := strings.Split(dockerfile, "\n")
	for i, line := range lines {
		if !strings.HasPrefix(strings.TrimSpace(line), "HEALTHCHECK") {
			continue
		}

		instr := line
		for strings.HasSuffix(strings.TrimSpace(instr), "\\") && i+1 < len(lines) {
			i++
			instr = strings.TrimSuffix(strings.TrimRight(instr, " "), "\\") + " " + lines[i]
		}

		return instr
	}

	t.Fatal("no HEALTHCHECK instruction found in the Dockerfile")

	return ""
}

// TestReleaseWorkflow_PromoteNeedsSmoke asserts the ordering rule that makes a release safe: the
// moving tags (:1, :1.x, :latest) advance only after smoke. Encoded as `promote needs smoke`. If a
// refactor dropped `smoke` from `promote`'s needs, a build that failed to boot could still move :1,
// which is the exact incident the whole design exists to prevent.
func TestReleaseWorkflow_PromoteNeedsSmoke(t *testing.T) {
	t.Parallel()

	rel := readRepoFile(t, ".github/workflows/release.yml")

	promote := jobBlock(t, rel, "promote:")
	require.Contains(t, promote, "smoke",
		"the promote job must depend on smoke — moving tags advance only after the smoke gate passes")

	// The manifest/immutable job publishes first and does NOT advance a moving tag; that is what
	// leaves :1 on the previous digest when a build is broken. A smoke-less promote would defeat it.
	require.Contains(t, rel, "needs: [prepare, manifest, smoke]",
		"promote must need prepare, manifest AND smoke")
}

// TestReleaseWorkflow_NoQemuAnywhere is the workflow-file half of the "no QEMU" criterion, checked
// against the real committed workflows rather than a fixture. Multi-arch is cross-compiled; the
// string must not appear in a step. Comments explaining the choice are allowed (they contain "No
// QEMU"), so this checks non-comment lines only, mirroring QEMU001's comment stripping.
func TestReleaseWorkflow_NoQemuAnywhere(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	dir := filepath.Join(root, ".github", "workflows")

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)

	commentLine := regexp.MustCompile(`^\s*#`)

	for _, e := range entries {
		if e.IsDir() {
			continue
		}

		body, err := os.ReadFile(filepath.Join(dir, e.Name()))
		require.NoError(t, err)

		for _, line := range strings.Split(string(body), "\n") {
			if commentLine.MatchString(line) {
				continue // prose about "No QEMU" is not a QEMU step
			}

			require.NotContainsf(t, strings.ToLower(line), "qemu",
				"%s has a non-comment QEMU line — multi-arch is cross-compiled, never emulated:\n  %s",
				e.Name(), line)
		}
	}
}

// TestRenovate_HighRiskDepsNeverAutomerge asserts the release-policy criterion: huma, goose, river,
// the SQLite driver (and the toolchain pins) never automerge — they sit behind dashboard approval.
// A green CI run is not sufficient evidence to bump the storage engine or the job driver.
func TestRenovate_HighRiskDepsNeverAutomerge(t *testing.T) {
	t.Parallel()

	cfg := readRepoFile(t, ".github/renovate.json5")

	// The high-risk rule must name each of these and set dependencyDashboardApproval with no
	// automerge. We assert the names are present and that the block carries the approval gate; the
	// json5 is small enough that substring presence is a faithful check.
	for _, dep := range []string{
		"github.com/danielgtaylor/huma/**",
		"github.com/pressly/goose/**",
		"github.com/riverqueue/river",
		"modernc.org/sqlite",
	} {
		require.Containsf(t, cfg, dep,
			"renovate.json5 must list %q in the high-risk, never-automerge block", dep)
	}

	require.Contains(t, cfg, "dependencyDashboardApproval: true",
		"the high-risk block must gate on dashboard approval")

	// The auth firewall: whatever internal/auth ends up depending on must not automerge either. The
	// package does not exist yet, so this is enforced by a path rule that freezes automerge for any
	// module a future internal/auth pulls in — asserted here so the rule is not quietly dropped.
	require.Contains(t, cfg, "internal/auth",
		"renovate.json5 must carry an internal/auth dependency-set rule (PR 7 criterion): auth deps "+
			"never automerge")
}

// TestImageSize_ReleaseEnforcesBudget backs the "hard gate" claim with a test. The 30 MB compressed
// budget is advisory on the PR path (MODE=advise → exit 0 + `::warning::`) but a HARD gate on the
// release path. The only thing that makes it a gate is scripts/release-image.sh invoking
// scripts/image-size.sh with MODE=enforce AFTER the image is built and pushed: image-size.sh's
// enforce branch (`[ "$MODE" = "enforce" ] && exit 1`) is dead code unless a caller sets it, and the
// release matrix is fail-fast so a single over-budget arch stops the release before the manifest is
// joined. If that enforcing invocation is ever removed, an oversized image would warn-and-ship — so
// this test fails the moment the gate is laundered back into a no-op.
func TestImageSize_ReleaseEnforcesBudget(t *testing.T) {
	t.Parallel()

	// The release-path caller must invoke the measurement in enforce mode.
	rel := readRepoFile(t, "scripts/release-image.sh")
	require.Regexp(t, regexp.MustCompile(`MODE=enforce[^\n]*image-size\.sh`), rel,
		"scripts/release-image.sh must invoke scripts/image-size.sh with MODE=enforce so a "+
			"breach of the 30 MB budget HARD-FAILS the release (not just warns)")

	// And image-size.sh's enforce branch must actually exit non-zero on a breach — otherwise
	// MODE=enforce would be cosmetic and the gate a no-op even with the caller wired up.
	script := readRepoFile(t, "scripts/image-size.sh")
	require.Contains(t, script, `[ "$MODE" = "enforce" ] && exit 1`,
		"image-size.sh's enforce mode must exit non-zero on a breach; without it MODE=enforce is a "+
			"laundered gate")

	// The enforcement runs where a built image and its size genuinely exist and can fail the release:
	// after the per-arch image is pushed. Assert the enforcing call lives AFTER the buildx push, not
	// before it (where there would be nothing to measure and the gate could not fire).
	push := strings.Index(rel, "--push")
	enforce := strings.Index(rel, "MODE=enforce")
	require.NotEqual(t, -1, push, "release-image.sh must push the image it then measures")
	require.NotEqual(t, -1, enforce, "release-image.sh must enforce the image-size budget")
	require.Greater(t, enforce, push,
		"the MODE=enforce measurement must run AFTER the image is pushed, so it measures a real image")
}

// TestReleaseWorkflow_PrepareVerifiesShippedLock keeps the shipped-migration manifest wired into the
// release, and wired in at the only place where a failure is still free.
//
// db/migrations-sqlite/SHIPPED.lock records which migrations have run on a user's database. Every
// migration present at a tag ships with that tag, so the release must assert the manifest already
// lists them all — an unsealed one leaves a hole nobody notices until somebody edits that file two
// releases later and MIG003, which only checks the rows that ARE there, says fine.
//
// Three things have to hold together, and each is a line a refactor could drop without breaking
// anything visible: the step runs in `prepare` (before any image, binary, attestation or moving tag
// exists), the Makefile target passes --complete (without it the release runs the per-PR check and
// the completeness assertion silently disappears), and the command's --complete branch actually
// fails. The middle one is the laundering risk: `verify` alone still prints a reassuring green line.
func TestReleaseWorkflow_PrepareVerifiesShippedLock(t *testing.T) {
	t.Parallel()

	rel := readRepoFile(t, ".github/workflows/release.yml")

	prepare := jobBlock(t, rel, "prepare:")
	require.Contains(t, prepare, "make release-shipped-lock",
		"the prepare job must verify db/migrations-sqlite/SHIPPED.lock before anything is published")

	mk := readRepoFile(t, "Makefile")
	require.Regexp(t, regexp.MustCompile(`release-shipped-lock:\n\t@[^\n]*shippedlock verify --complete`), mk,
		"`make release-shipped-lock` must run the manifest check with --complete; plain `verify` is "+
			"the per-PR gate and would pass a release whose manifest was never sealed")

	// The manifest check is Go since issue #129, and a library the gate engine imports since #173 —
	// so the argument parsing lives in internal/migrate/lockmanifest and the command is a wrapper.
	// The flag has to be REACHED as well as passed:
	// TestShippedLock_ReleaseMode_RequiresEveryMigrationBeSealed proves the completeness assertion
	// fires, and this proves the release target's argument is the one that reaches it.
	cmd := readRepoFile(t, "internal/migrate/lockmanifest/lockmanifest.go")
	require.Contains(t, cmd, `args[1] == "--complete"`,
		"the shipped-lock command must honour --complete; without it the flag is cosmetic and the "+
			"release gate is a laundered no-op")
}

// ghcrOps are the commands that CANNOT work without registry credentials. A job that reaches one of
// them — directly, or through a `make` target that shells to a script — must have authenticated
// first.
//
// `docker manifest inspect` is deliberately NOT in this list, even though it talks to a registry:
// scripts/image-size.sh calls it and falls back to the local image, and it runs in ci.yml's
// `build / image` job, which builds with --load, pushes nothing and correctly holds no credentials.
// A list that flagged that job would be a gate that has to be argued with, and a gate that has to be
// argued with gets deleted.
var ghcrOps = []struct{ needle, why string }{
	{"docker buildx imagetools", "imagetools reads and writes manifest lists in the registry"},
	{"--push", "a buildx build that pushes is writing to the registry"},
	{"${IMAGE}@${DIGEST}", "a digest-addressed ref is pulled from the registry, never built locally"},
	{"cosign sign", "cosign writes the signature to the registry beside the image"},
	{"cosign verify", "cosign reads the signature from the registry"},
	{`syft "${IMAGE}`, "syft resolves the image through the registry to build the SBOM"},
	{"gh attestation verify", "the attestation is read for a registry ref"},
}

// TestReleaseWorkflows_EveryGHCRJob_LogsInFirst is the assertion that would have caught issue #113.
//
// release.yml's `smoke` job — the gate that decides whether :1, :1.x and :latest advance — carried
// `packages: read` and no `docker/login-action`, so its first action, `docker run $IMAGE@$DIGEST`,
// was an ANONYMOUS pull. A permission scopes the TOKEN; it does not authenticate the docker daemon.
// A GHCR package is private on first publish until somebody changes its visibility, so the first
// real tag would have failed there with `denied` / `manifest unknown` — after the immutable tag was
// published, before anything booted, at the one moment in the pipeline where nobody wants a surprise.
//
// So the rule is asserted rather than the instance. The GHCR-touching set is DERIVED, not listed: a
// script joins it by containing a command that needs credentials, a make target joins it by shelling
// to such a script (following `$(MAKE) …` too, so wrapping one target in another does not launder
// it), and a job joins it by running such a target or the command itself inline. A new publish job
// is covered the day it is written, without anyone remembering this test exists.
func TestReleaseWorkflows_EveryGHCRJob_LogsInFirst(t *testing.T) {
	t.Parallel()

	ghcrTargets := ghcrMakeTargets(t)

	// NO VACUOUS PASS. If the Makefile parsing or the needle list breaks, every job below has zero
	// GHCR targets and the sweep passes on a tree with the bug in it. These four are the release
	// train's registry writers and its gate; a derivation that cannot see them is broken, not clean.
	for _, want := range []string{"release-image", "release-manifest", "release-promote", "release-smoke"} {
		require.Containsf(t, ghcrTargets, want,
			"the GHCR-touching derivation did not classify `make %s` as touching the registry, so "+
				"this test would pass on the tree issue #113 reported. Derived set: %v", want, ghcrTargets)
	}

	covered := map[string]bool{}

	for _, wf := range []string{"release.yml", "edge.yml"} {
		body := readRepoFile(t, filepath.Join(".github", "workflows", wf))

		for name, job := range workflowJobs(t, body) {
			steps := stripYAMLComments(job)

			// Why the job needs credentials: a make target that reaches the registry, or a registry
			// command written inline (edge.yml's `promote` re-tags with imagetools directly).
			var reason string

			for _, target := range regexp.MustCompile(`\bmake ([a-z][a-z0-9-]*)`).FindAllStringSubmatch(steps, -1) {
				if ghcrTargets[target[1]] != "" {
					reason = "`make " + target[1] + "` " + ghcrTargets[target[1]]

					break
				}
			}

			if reason == "" {
				for _, op := range ghcrOps {
					if strings.Contains(steps, op.needle) {
						reason = "it runs `" + op.needle + "` inline — " + op.why

						break
					}
				}
			}

			if reason == "" {
				continue
			}

			covered[wf+"/"+name] = true

			require.Containsf(t, steps, "docker/login-action",
				"%s's `%s` job touches GHCR (%s) and never logs in. `packages: read` scopes the "+
					"token; it does not authenticate the docker daemon, and a GHCR package is PRIVATE "+
					"on first publish — this is issue #113 verbatim. Add the pinned login step every "+
					"other GHCR job in this file carries.", wf, name, reason)

			// Ordering, not just presence: a login step after the pull authenticates nothing.
			login := strings.Index(steps, "docker/login-action")
			first := firstGHCRStep(steps, ghcrTargets)
			require.Lessf(t, login, first,
				"%s's `%s` job logs in AFTER it has already touched the registry (%s), so the first "+
					"pull or push is still anonymous", wf, name, reason)
		}
	}

	// The jobs the derivation must have reached. Named rather than counted: a count says "six of
	// something", and the thing worth pinning is that the gate covers the gate — release.yml's
	// `smoke` is the job #113 was about, and edge.yml's `smoke` is the same code on the same
	// scripts one channel down.
	for _, want := range []string{
		"release.yml/image", "release.yml/manifest", "release.yml/promote", "release.yml/smoke",
		"edge.yml/image", "edge.yml/manifest", "edge.yml/promote", "edge.yml/smoke",
	} {
		require.Truef(t, covered[want],
			"the GHCR-login sweep never classified %s as touching the registry — the derivation is "+
				"broken, so its clean run proves nothing. Covered: %v", want, covered)
	}
}

// ghcrMakeTargets returns every Makefile target that reaches a container registry, mapped to the
// reason it does, by reading the scripts each target shells to.
func ghcrMakeTargets(t *testing.T) map[string]string {
	t.Helper()

	recipes := makefileRecipes(t)

	// Which scripts touch a registry, from the scripts themselves.
	scriptReason := map[string]string{}

	for _, recipe := range recipes {
		for _, m := range regexp.MustCompile(`scripts/([a-z0-9-]+\.sh)`).FindAllStringSubmatch(recipe, -1) {
			script := m[1]
			if _, done := scriptReason[script]; done {
				continue
			}

			body, err := os.ReadFile(filepath.Join(repoRoot(t), "scripts", script))
			if err != nil {
				continue // a recipe may name a script that does not exist yet; that touches nothing
			}

			for _, op := range ghcrOps {
				if strings.Contains(stripShellComments(string(body)), op.needle) {
					scriptReason[script] = "runs `" + op.needle + "` in scripts/" + script + " — " + op.why

					break
				}
			}
		}
	}

	// Then the targets, following `$(MAKE) <target>` so a wrapper target inherits its callee's reach.
	targets := map[string]string{}

	var resolve func(name string, seen map[string]bool) string

	resolve = func(name string, seen map[string]bool) string {
		if seen[name] {
			return ""
		}

		seen[name] = true

		recipe, ok := recipes[name]
		if !ok {
			return ""
		}

		for _, m := range regexp.MustCompile(`scripts/([a-z0-9-]+\.sh)`).FindAllStringSubmatch(recipe, -1) {
			if why := scriptReason[m[1]]; why != "" {
				return why
			}
		}

		for _, m := range regexp.MustCompile(`\$\(MAKE\) ([a-z][a-z0-9-]*)`).FindAllStringSubmatch(recipe, -1) {
			if why := resolve(m[1], seen); why != "" {
				return why
			}
		}

		return ""
	}

	for name := range recipes {
		if why := resolve(name, map[string]bool{}); why != "" {
			targets[name] = why
		}
	}

	return targets
}

// firstGHCRStep returns the offset of the earliest registry-touching thing in a job's step text, so
// a login step can be required to come before it.
func firstGHCRStep(steps string, ghcrTargets map[string]string) int {
	first := len(steps)

	for target := range ghcrTargets {
		if at := strings.Index(steps, "make "+target); at != -1 && at < first {
			first = at
		}
	}

	for _, op := range ghcrOps {
		if at := strings.Index(steps, op.needle); at != -1 && at < first {
			first = at
		}
	}

	return first
}

// makefileRecipes maps each Makefile target to its recipe body.
func makefileRecipes(t *testing.T) map[string]string {
	t.Helper()

	recipes := map[string]string{}

	var (
		current string
		body    strings.Builder
	)

	targetLine := regexp.MustCompile(`^([a-z][a-z0-9-]*):`)

	flush := func() {
		if current != "" {
			recipes[current] = body.String()
		}

		current, body = "", strings.Builder{}
	}

	for _, line := range strings.Split(readRepoFile(t, "Makefile"), "\n") {
		switch {
		case strings.HasPrefix(line, "\t"):
			body.WriteString(line + "\n")
		case targetLine.MatchString(line):
			flush()
			current = targetLine.FindStringSubmatch(line)[1]
		case strings.TrimSpace(line) == "":
			flush()
		}
	}

	flush()

	return recipes
}

// workflowJobs splits a workflow into its top-level jobs, keyed by the job id.
//
// Scanning starts at the `jobs:` key on purpose: `on:` and `env:` also carry two-space children
// (`  push:`, `  schedule:`), and a splitter that started at the top of the file would invent jobs
// called `push` and `schedule` and then find no login step in them.
func workflowJobs(t *testing.T, workflow string) map[string]string {
	t.Helper()

	lines := strings.Split(workflow, "\n")

	start := -1

	for i, line := range lines {
		if line == "jobs:" {
			start = i + 1

			break
		}
	}

	require.NotEqual(t, -1, start, "workflow has no `jobs:` key")

	jobs := map[string]string{}
	jobLine := regexp.MustCompile(`^ {2}([a-z][a-z0-9-]*):\s*$`)

	var (
		current string
		body    strings.Builder
	)

	for _, line := range lines[start:] {
		if m := jobLine.FindStringSubmatch(line); m != nil {
			if current != "" {
				jobs[current] = body.String()
			}

			current, body = m[1], strings.Builder{}

			continue
		}

		body.WriteString(line + "\n")
	}

	if current != "" {
		jobs[current] = body.String()
	}

	require.NotEmpty(t, jobs, "no jobs parsed out of the workflow — the splitter is broken")

	return jobs
}

// stripYAMLComments drops `#` comments, whole-line AND trailing, so prose describing a rule is never
// mistaken for a breach of it — the same move QEMU001 makes in scripts/repo-gates.sh.
//
// Trailing comments matter here and not only whole-line ones: release.yml's smoke job documents its
// own permissions inline (`attestations: read # gh attestation verify reads the provenance …`), and
// a sweep that read that as a step would report the job as touching the registry before its first
// real step — which is a gate firing on the documentation of the thing it is checking.
func stripYAMLComments(body string) string {
	kept := make([]string, 0, strings.Count(body, "\n")+1)

	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}

		if at := strings.Index(line, " #"); at != -1 {
			line = line[:at]
		}

		kept = append(kept, line)
	}

	return strings.Join(kept, "\n")
}

// stripShellComments drops whole-line comments from a shell script. Trailing ones are left alone:
// unlike a workflow's, a script's `#` can sit inside a quoted argument, and the scripts' headers
// (which quote the very commands this file greps for) are whole-line comments anyway.
func stripShellComments(body string) string {
	kept := make([]string, 0, strings.Count(body, "\n")+1)

	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "#") {
			kept = append(kept, line)
		}
	}

	return strings.Join(kept, "\n")
}

// TestNightly_CrossBuildsAndValidatesTheArm64Image pins the fix for issue #108.
//
// Deleting the merge-queue configuration (#101) removed `mq / image-arm64`, the only job that ever
// passed PLATFORM=linux/arm64 to `make docker`. It never ran — there is no merge queue — so nothing
// was lost, but nothing was covered either: arm64 was built in release.yml's per-arch matrix and
// nowhere else, which means an arm64-only break (a GOARCH build tag, a base-image assumption, a cgo
// dependency from a bump) is found while cutting a release.
//
// Four things have to hold together for the nightly job to be worth its 70 seconds, and each is a
// line a refactor could drop while leaving something that still looks like arm64 coverage.
func TestNightly_CrossBuildsAndValidatesTheArm64Image(t *testing.T) {
	t.Parallel()

	nightly := readRepoFile(t, ".github/workflows/nightly-verify.yml")

	job := jobBlock(t, nightly, "image-arm64:")
	require.Contains(t, job, "make verify-image-arm64",
		"nightly-verify.yml's image-arm64 job must run `make verify-image-arm64` — the target is what "+
			"cross-builds and then checks the result (issue #108)")

	// 1. An amd64 runner. The point is the CROSS-compile release.yml performs, so building this on
	//    `ubuntu-24.04-arm` would be a natively-compiled image that shares no code path with the one
	//    that ships — green here, broken at the tag, which is the exact failure #108 is about.
	require.Contains(t, job, "runs-on: ubuntu-24.04\n",
		"the arm64 image must be CROSS-built on an amd64 runner, as release.yml builds it")
	require.NotContains(t, job, "ubuntu-24.04-arm",
		"an arm64 runner would compile natively and stop exercising the cross-compilation path that "+
			"release.yml actually uses (and QEMU is banned, so nothing else here needs one)")

	// 2. A failure has to reach a human. nightly-report files ONE issue for the whole night from its
	//    `needs`; a job missing from that list fails into an empty room.
	require.Contains(t, jobBlock(t, nightly, "nightly-report:"), "- image-arm64",
		"nightly-report must need image-arm64, or an arm64 break is a red job nobody is told about")

	// 3. The target really cross-builds. `make docker` without PLATFORM is a host build that would
	//    pass every night while proving nothing about arm64.
	recipe := makefileRecipes(t)["verify-image-arm64"]
	require.Contains(t, recipe, "PLATFORM=linux/arm64",
		"`make verify-image-arm64` must pass PLATFORM=linux/arm64 to `make docker`; without it this "+
			"is a second amd64 build wearing the name of an arm64 check")
	require.Contains(t, recipe, "scripts/verify-image-arch.sh",
		"`make verify-image-arm64` must VALIDATE the image it built, not only build it")

	// 4. And the validation must be the one that cannot be satisfied by a label. buildx writes the
	//    image config's architecture from --platform, so `docker image inspect` reports what was
	//    asked for; only the ELF header of the binary inside reports what was produced. Nothing in
	//    CI boots an arm64 image — that needs the QEMU this project bans — so those bytes are the
	//    only evidence available.
	script := readRepoFile(t, "scripts/verify-image-arch.sh")
	require.Contains(t, script, "{{.Architecture}}",
		"verify-image-arch.sh must read the image config's architecture")
	require.Contains(t, script, "-j 18 -N 2",
		"verify-image-arch.sh must read the ELF machine type (e_machine, offset 18) of the binary "+
			"inside the image: an image whose Dockerfile stopped threading TARGETARCH into `go build` "+
			"is still LABELLED arm64 by buildx, and no other check in this repository can see that")
	require.Contains(t, script, `want_machine="b700"`,
		"verify-image-arch.sh must expect EM_AARCH64 (0x00b7) for linux/arm64")

	for _, comparison := range []string{
		`[ "$got_arch" != "$want_arch" ]`,
		`[ "$machine" != "$want_machine" ]`,
	} {
		require.Containsf(t, script, comparison,
			"verify-image-arch.sh must COMPARE and fail on %s — a script that reads the value and "+
				"prints it is a log line, not a gate", comparison)
	}
}

// jobBlock extracts the text of a top-level workflow job, from its `name:` key to the next
// top-level job key (a line indented by exactly two spaces ending in `:`), so an assertion about one
// job cannot accidentally match text in another.
func jobBlock(t *testing.T, workflow, jobKey string) string {
	t.Helper()

	// jobKey is like "promote:"; jobs are indented two spaces under `jobs:`.
	marker := "  " + jobKey
	start := strings.Index(workflow, marker)
	require.NotEqualf(t, -1, start, "job %q not found in workflow", jobKey)

	rest := workflow[start+len(marker):]
	next := regexp.MustCompile(`\n {2}[a-zA-Z0-9_-]+:`).FindStringIndex(rest)
	if next == nil {
		return workflow[start:]
	}

	return workflow[start : start+len(marker)+next[0]]
}
