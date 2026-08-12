// Tests that the Docker layer cache is a mechanism and not a paragraph.
//
// `ci.yml`'s `build / image` job set BUILDX_CACHE_FROM and BUILDX_CACHE_TO, explained the policy
// behind them in a five-line comment, and nothing read either one: `make docker` passed no cache
// flags, and those names are not variables buildx itself honours — they are this repository's
// convention, which scripts/release-image.sh implements by assembling the flags in shell. So the PR
// path's image build had no layer cache at all, and a reader auditing CI time found a documented
// cache policy and concluded the question was settled (issue #119, the same shape as #84, #99 and
// #107).
//
// The assertion that catches that exact defect is the one tying the two halves together: the `src=`
// the build reads must be the directory `actions/cache` archives. Either half alone can be present
// and correct while the pair does nothing.
package repo_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The `name:` of each layer-cache step in ci.yml's build / image job.
const (
	layerCacheRestoreStep = "Restore the Docker layer cache"
	layerCacheSaveStep    = "Restore and save the Docker layer cache (main only)"
	layerCacheRollStep    = "Roll the layer cache forward (main only)"
)

// localCacheDirRe pulls the directory out of a `type=local,src=…` / `type=local,dest=…` cache
// specification.
var localCacheDirRe = regexp.MustCompile(`type=local,(?:src|dest)=([^,\s'"]+)`)

// TestDockerRecipe_ConsumesTheLayerCacheVariables is the half issue #119 was actually about: the
// variables reach the `buildx build` command line.
//
// Guarded expansions rather than unconditional flags, because `make docker` is also what a
// contributor types on a laptop and what nightly-verify.yml's arm64 cross-build runs, and neither
// sets these. `$${VAR:+…}` emits the flag only when the variable is non-empty, which is also how the
// write-on-main split is expressed: off main, BUILDX_CACHE_TO is the empty string and no `--cache-to`
// is passed at all.
func TestDockerRecipe_ConsumesTheLayerCacheVariables(t *testing.T) {
	t.Parallel()

	recipe := makefileRecipes(t)["docker"]
	require.NotEmpty(t, recipe, "the Makefile has no `docker` target — CI's build / image job runs it")

	require.Contains(t, recipe, "docker buildx build",
		"the docker recipe must build with buildx — the layer cache, --platform and the Dockerfile's "+
			"cross-compilation ARGs all depend on it")

	for _, want := range []struct {
		variable string
		flag     string
	}{
		{"BUILDX_CACHE_FROM", "--cache-from"},
		{"BUILDX_CACHE_TO", "--cache-to"},
	} {
		require.Regexpf(t, regexp.MustCompile(`\$\$\{`+want.variable+`:\+`+want.flag+` \$\$`+want.variable+`\}`), recipe,
			"the docker recipe must pass %s through to `buildx build` as %s. ci.yml's build / image "+
				"job sets it and documents a cache policy; a variable nobody reads plus a paragraph "+
				"describing its policy is issue #119.", want.variable, want.flag)
	}
}

// TestBuildImage_LayerCache_WritesOnMainReadsEverywhere pins the layer cache to the doctrine the Go
// build cache in setup-toolchain follows: PR branches restore, only main writes.
//
// Actions caches are branch-scoped with read-through to the default branch, so a write from a PR is
// invisible to every other PR and is evicted almost immediately — it burns quota against the shared
// 10 GB limit while evicting the entries that ARE shared. `mode=max` makes this the largest entry in
// the pool, so the difference is not academic.
func TestBuildImage_LayerCache_WritesOnMainReadsEverywhere(t *testing.T) {
	t.Parallel()

	job := jobBlock(t, readCIWorkflow(t), "build-image:")

	restore := workflowCacheStep(t, job, layerCacheRestoreStep)
	save := workflowCacheStep(t, job, layerCacheSaveStep)

	require.True(t, strings.HasPrefix(restore.uses, "actions/cache/restore@"),
		"the off-main step must be restore-only — actions/cache/restore has no post step, so it "+
			"cannot write. Got %s", restore.uses)
	require.True(t, strings.HasPrefix(save.uses, "actions/cache@"),
		"the main-only step must be the full action, whose post step saves at job end. Got %s", save.uses)

	require.Contains(t, restore.body, "github.ref != 'refs/heads/main'",
		"the restore-only step must be the one that runs OFF main:\n%s", restore.body)
	require.Contains(t, save.body, "github.ref == 'refs/heads/main'",
		"only main may save the shared layer cache:\n%s", save.body)

	// GitHub supports no YAML anchors in a workflow file, so the key and restore-keys are a literal
	// copy in the two steps. A PR restoring under a key main never writes is a cache that silently
	// never hits.
	require.Equal(t, restore.path, save.path, "both cache steps must archive the same directory")
	require.Equal(t, restore.key, save.key, "the two cache steps must share one key")
	require.Equal(t, restore.restoreKeys, save.restoreKeys, "the two cache steps must share one restore-keys list")

	require.NotEmpty(t, restore.restoreKeys, "without restore-keys a key that misses restores nothing")

	for _, prefix := range restore.restoreKeys {
		require.True(t, strings.HasSuffix(prefix, "-"),
			"a restore-key is matched as a PREFIX; %q does not end in a separator, so it can match a "+
				"neighbouring key it was never meant to", prefix)
		require.True(t, strings.HasPrefix(restore.key, prefix),
			"restore-key %q is not a prefix of the key %q, so it can only ever restore another "+
				"cache's entry or nothing at all", prefix, restore.key)
	}

	// The key is content, not the commit: a `mode=max` export of this build is the biggest thing in
	// the cache pool, and a per-commit key would upload it again on every merge to main for layers
	// that did not change. What decides whether the Node and Go builder stages can be reused is the
	// Dockerfile and the two lockfiles.
	require.NotContains(t, restore.key, "github.sha",
		"the layer cache key must not roll per commit — it would re-upload the whole export on every "+
			"push to main. Got %q", restore.key)
	require.Contains(t, restore.key, "hashFiles(",
		"the layer cache must be keyed on the files that decide what the builder stages can reuse. "+
			"Got %q", restore.key)
}

// TestBuildImage_LayerCache_IsWiredToTheDirectoryItArchives is the anti-#119 assertion.
//
// A cache step archiving one directory while the build reads another is indistinguishable, in a
// green run, from the defect this file exists for: both produce a job that looks cached and is not.
func TestBuildImage_LayerCache_IsWiredToTheDirectoryItArchives(t *testing.T) {
	t.Parallel()

	job := jobBlock(t, readCIWorkflow(t), "build-image:")
	cached := workflowCacheStep(t, job, layerCacheRestoreStep).path

	from := workflowEnvValue(t, job, "BUILDX_CACHE_FROM")
	to := workflowEnvValue(t, job, "BUILDX_CACHE_TO")

	src := localCacheDirRe.FindStringSubmatch(from)
	require.NotNilf(t, src, "BUILDX_CACHE_FROM must name a local cache directory. Got %q", from)

	require.Equal(t, cached, src[1],
		"BUILDX_CACHE_FROM reads %q but actions/cache archives %q, so the build imports a directory "+
			"nothing restores — a cache in name only (issue #119)", src[1], cached)

	require.Contains(t, to, "github.ref == 'refs/heads/main'",
		"BUILDX_CACHE_TO must be empty off main: a PR's write is invisible to every other PR. Got %q", to)
	require.Contains(t, to, "mode=max",
		"mode=min exports only the layers of the resulting image, and the final stage of "+
			"deploy/Dockerfile is `FROM scratch` with three COPYs — every expensive layer is in the "+
			"Node and Go builder stages, which only mode=max exports. Got %q", to)

	dest := localCacheDirRe.FindStringSubmatch(to)
	require.NotNilf(t, dest, "BUILDX_CACHE_TO must name a local cache directory. Got %q", to)

	// Exporting over the restored copy would grow the entry every run: buildkit's local exporter
	// writes a whole cache and prunes nothing already there.
	require.NotEqual(t, cached, dest[1],
		"BUILDX_CACHE_TO must export to a different directory than it imports from, and the roll "+
			"step must swap it into place")

	roll := workflowStep(t, job, layerCacheRollStep)
	require.Contains(t, roll, "github.ref == 'refs/heads/main'", "the roll step runs on main only")
	require.Containsf(t, roll, "mv "+dest[1]+" "+cached,
		"the roll step must move the fresh export (%s) into the directory actions/cache archives "+
			"(%s), or main saves the copy it restored and the cache never advances", dest[1], cached)

	// Commands only: the step's own comment says `|| true` in prose about why it is absent, and an
	// assertion a comment can fail is the mirror image of one a comment can satisfy.
	require.NotContains(t, uncommented(roll), "|| true",
		"a failed swap means buildx exported nothing and the policy above is not happening — that is "+
			"a failure to report, not one to swallow")
}

// uncommented drops whole-line `#` comments, so an assertion about what a step DOES is not answered
// by what its comment SAYS.
func uncommented(body string) string {
	var kept []string

	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "#") {
			kept = append(kept, line)
		}
	}

	return strings.Join(kept, "\n")
}

// workflowSteps splits a workflow job's block into its steps.
func workflowSteps(job string) []string {
	return strings.Split(job, "\n      - ")[1:]
}

// workflowStep returns the body of the single step of a workflow job named name.
func workflowStep(t *testing.T, job, name string) string {
	t.Helper()

	var found []string

	for _, step := range workflowSteps(job) {
		if strings.HasPrefix(step, "name: "+name+"\n") {
			found = append(found, step)
		}
	}

	require.Lenf(t, found, 1, "expected exactly one step named %q, found %d", name, len(found))

	return found[0]
}

// workflowCacheStep parses the cache inputs of the step of a workflow job named name.
func workflowCacheStep(t *testing.T, job, name string) cacheStep {
	t.Helper()

	parsed := parseCacheStep(workflowStep(t, job, name))

	require.Regexpf(t, `@[0-9a-f]{40}$`, parsed.uses,
		"every action is pinned to a 40-character commit SHA (PIN001): %s", parsed.uses)
	require.Truef(t, isCacheAction(parsed.uses), "step %q is not a cache step; it uses %s", name, parsed.uses)
	require.NotEmptyf(t, parsed.path, "no `path:` in the %q step", name)
	require.NotEmptyf(t, parsed.key, "no `key:` in the %q step", name)

	return parsed
}

// workflowEnvValue returns the value of one `env:` key declared anywhere in a workflow job's block.
func workflowEnvValue(t *testing.T, job, name string) string {
	t.Helper()

	var found []string

	for _, line := range strings.Split(job, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") || !strings.HasPrefix(trimmed, name+":") {
			continue
		}

		found = append(found, strings.TrimSpace(strings.TrimPrefix(trimmed, name+":")))
	}

	require.Lenf(t, found, 1, "expected exactly one %s: declaration in the job, found %d", name, len(found))

	return found[0]
}
