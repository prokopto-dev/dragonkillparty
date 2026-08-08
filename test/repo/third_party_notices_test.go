package repo_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestThirdPartyNotices_CoversRuntimeGraph asserts the committed THIRD_PARTY_NOTICES.txt names every
// runtime dependency of the binary on this platform.
//
// NOTICE promises this file and .goreleaser.yaml attaches it to every release archive and Linux
// package; it is the attribution the Apache-2.0, MIT, BSD and MPL-2.0 dependencies require. A missing
// module is a licence-compliance gap that ships silently, so this is a gate rather than a hope.
//
// It is a SUBSET check, deliberately. The committed file is the UNION of the runtime graph across
// every release platform (scripts/third-party-notices.sh), which cannot be reproduced on one OS —
// golang.org/x/sys and modernc.org/libc pull different modules on linux, darwin and windows. What is
// checkable on any single platform is that every module THIS platform links is present; the union
// can only be larger. `make third-party-notices` regenerates the full file.
func TestThirdPartyNotices_CoversRuntimeGraph(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)

	data, err := os.ReadFile(filepath.Join(root, "THIRD_PARTY_NOTICES.txt"))
	require.NoError(t, err, "THIRD_PARTY_NOTICES.txt must exist — NOTICE promises it and the release attaches it")
	require.NotEmpty(t, data, "THIRD_PARTY_NOTICES.txt must not be empty")

	notices := string(data)

	for _, mod := range runtimeModules(t, root) {
		require.Containsf(t, notices, mod,
			"THIRD_PARTY_NOTICES.txt does not mention runtime module %q — run `make third-party-notices` and commit", mod)
	}
}

// TestThirdPartyNotices_HasLicenceText asserts the file actually reproduces licence text rather than
// only listing module names. A notices file that names modules without their licences is not the
// attribution the licences require. The Apache and MIT permission grants are distinctive phrases that
// must appear if any real licence body was included.
func TestThirdPartyNotices_HasLicenceText(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "THIRD_PARTY_NOTICES.txt"))
	require.NoError(t, err)

	notices := string(data)
	// modernc.org/sqlite is BSD-3-Clause and huma is MIT; between the graph's licences at least one
	// of these permission phrases must be present, or the file is a bare list.
	hasLicenceProse := strings.Contains(notices, "Permission is hereby granted") || // MIT/ISC
		strings.Contains(notices, "Redistribution and use") || // BSD
		strings.Contains(notices, "Apache License") // Apache-2.0
	require.True(t, hasLicenceProse,
		"THIRD_PARTY_NOTICES.txt names modules but reproduces no licence text — the generator is broken")

	// And it must not have silently substituted a placeholder for a module whose licence it could not
	// find: that would ship an unattributed dependency wearing an "ok" label.
	require.NotContains(t, notices, "no licence file found",
		"a runtime module has no licence file in the notices — investigate before shipping")
}

// runtimeModules returns the module paths that provide non-test packages of ./cmd/dkp on this
// platform, excluding the main module itself.
func runtimeModules(t *testing.T, root string) []string {
	t.Helper()

	cmd := exec.Command("go", "list", "-deps", "-json", "./cmd/dkp")
	cmd.Dir = root

	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	require.NoError(t, cmd.Run(), "go list -deps -json ./cmd/dkp")

	self := mainModulePath(t, root)

	seen := map[string]struct{}{}
	dec := json.NewDecoder(&stdout)

	for dec.More() {
		var pkg struct {
			Module *struct{ Path string }
		}
		require.NoError(t, dec.Decode(&pkg))

		if pkg.Module == nil || pkg.Module.Path == self {
			continue
		}

		seen[pkg.Module.Path] = struct{}{}
	}

	require.NotEmpty(t, seen, "the binary must have at least one runtime dependency")

	mods := make([]string, 0, len(seen))
	for m := range seen {
		mods = append(mods, m)
	}

	return mods
}

// mainModulePath returns this module's path from `go list -m`.
func mainModulePath(t *testing.T, root string) string {
	t.Helper()

	cmd := exec.Command("go", "list", "-m")
	cmd.Dir = root

	out, err := cmd.Output()
	require.NoError(t, err, "go list -m")

	return strings.TrimSpace(string(out))
}
