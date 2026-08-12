// Negative fixture tests for the dependency licence gate (internal/licence, run by
// `make licence-gate`).
//
// The fixtures are self-contained Go modules in t.TempDir(): a root module with one dependency
// wired in by a filesystem `replace`, so `go list` resolves the whole graph OFFLINE and no module
// is ever downloaded. That is what makes a GPL-licensed dependency testable at all — one cannot be
// added to this repo's go.mod to prove the gate fires, and a fabricated module in the module cache
// would leak across tests.
//
// The gate reads each module's licence from `{{.Module.Dir}}`, which follows a `replace` to the
// local directory exactly as it follows a cache-resident module to $GOMODCACHE. The fixtures need
// no cooperation from the gate: there is no test-only environment variable and no branch in the
// gate that exists for these tests.
//
// Every fixture here predates the move of the classifier from scripts/licence-gate.sh to Go (issue
// #130) and carried over UNCHANGED, which is the point: the gate was rewritten, and the same trees
// that made it go red still make it go red, naming the same rules. These tests are the black-box
// half — they drive the whole gate, exactly as `make licence-gate` and CI do. The classifier's own
// patterns are unit-tested directly in internal/licence/classify_test.go, which is the testability
// the move was for; neither layer replaces the other.
//
// The rules in gates_test.go's package doc apply here too, with one deliberate departure. Those
// tests assert `require.NotContains(t, out, tree)` because repo-gates.sh reports repo-root-relative
// paths. This gate reports `module@version`, since a dependency's licence lives in $GOMODCACHE and
// has no repo-relative path to report — so the equivalent assertion here is that the failure names
// the offending MODULE.
package repo_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/licence"
)

// The fabricated dependency every fixture hangs off. The path is under example.com, which is
// reserved by RFC 2606 and can never resolve to a real module.
const fixtureDep = "example.com/dep"

// Licence bodies. Each is the opening of the real text, which is what a licence file actually looks
// like and what the gate's identification patterns are written against.
const (
	licenceGPL3 = `                    GNU GENERAL PUBLIC LICENSE
                       Version 3, 29 June 2007

 Copyright (C) 2007 Free Software Foundation, Inc. <https://fsf.org/>
 Everyone is permitted to copy and distribute verbatim copies
 of this license document, but changing it is not allowed.

  The GNU General Public License is a free, copyleft license for
software and other kinds of works.
`

	licenceAGPL3 = `                    GNU AFFERO GENERAL PUBLIC LICENSE
                       Version 3, 19 November 2007

 Copyright (C) 2007 Free Software Foundation, Inc. <https://fsf.org/>
 Everyone is permitted to copy and distribute verbatim copies
 of this license document, but changing it is not allowed.
`

	licenceLGPL21 = `                  GNU LESSER GENERAL PUBLIC LICENSE
                       Version 2.1, February 1999

 Copyright (C) 1991, 1999 Free Software Foundation, Inc.
`

	licenceCCBYNC = `Attribution-NonCommercial-ShareAlike 4.0 International

Creative Commons Corporation is not a law firm and does not provide
legal services or legal advice.
`

	// MPL-2.0 is the licence that makes ordering matter, and this fixture proves it.
	//
	// Section 1.12 of the real text names the GPL, the LGPL and the AGPL. A gate that applies its
	// denylist before identifying the licence fires on every MPL-2.0 module — including
	// github.com/hashicorp/golang-lru/v2, which is already in this repo's module graph. The
	// paragraph below is reproduced from the real licence for exactly that reason; deleting it
	// would make this test pass while the bug it guards went unnoticed.
	licenceMPL2 = `Mozilla Public License Version 2.0
==================================

1. Definitions
--------------

1.12. "Secondary License"
    means either the GNU General Public License, Version 2.0, the GNU
    Lesser General Public License, Version 2.1, the GNU Affero General
    Public License, Version 3.0, or any later versions of those licenses.
`

	licenceMIT = `MIT License

Copyright (c) 2026 Example

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction.
`

	licenceApache2 = `                                 Apache License
                           Version 2.0, January 2004
                        http://www.apache.org/licenses/

   TERMS AND CONDITIONS FOR USE, REPRODUCTION, AND DISTRIBUTION
`

	licenceBSD3 = `Copyright (c) 2026 The Example Authors. All rights reserved.

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are met:
`

	// A GPL-3.0 module whose licence file opens by naming the MPL. This is the shape a module takes
	// when it vendors a component under a different licence and concatenates the notices — and it is
	// the case that breaks any classifier which identifies MPL first and stops looking.
	licenceGPL3WithMPLPreamble = `This package is distributed under the GNU General Public License version 3.
It bundles a vendored copy of foolib, which is under the Mozilla Public
License, Version 2.0. The full text of both follows.

                    GNU GENERAL PUBLIC LICENSE
                       Version 3, 29 June 2007

 Copyright (C) 2007 Free Software Foundation, Inc.
`

	licenceAGPL3WithMPLSPDX = `SPDX-License-Identifier: AGPL-3.0-or-later
Portions: MPL-2.0

                    GNU AFFERO GENERAL PUBLIC LICENSE
                       Version 3, 19 November 2007
`

	// A GPL grant sitting AFTER an MPL Secondary License definition.
	//
	// The gate removes MPL-2.0's §1.12 sentence before applying the denylist, because that sentence
	// names the GNU licences and would otherwise fire on every MPL module. This fixture is what
	// keeps that removal honest: an earlier implementation matched a greedy span from "Secondary
	// License" to the last "licenses." and deleted the GPL title and grant along with it, leaving
	// the denylist nothing to find.
	licenceGPL3AfterSecondaryLicenceDefinition = `Notice. "Secondary License" means either the GNU General Public
License, Version 2.0, the GNU Lesser General Public License, Version 2.1,
the GNU Affero General Public License, Version 3.0, or any later versions
of those licenses.

Some intervening text.

                    GNU GENERAL PUBLIC LICENSE
                       Version 3, 29 June 2007

This program is free software under the terms of the GNU General Public
License as published by the Free Software Foundation, or any later
versions of those licenses.
`

	// BUSL-1.1's template names a permissive licence in its own Change License parameter.
	// HashiCorp's modules fill it with the MPL; others use Apache-2.0. Either string is enough to
	// make a first-match classifier call a source-available licence permissive.
	licenceBUSL = `Business Source License 1.1

Parameters

Licensor:             Example, Inc.
Licensed Work:        Example 1.0
Change License:       Mozilla Public License, version 2.0
Change Date:          2030-01-01

Notice

The Business Source License (this document) is not an Open Source license.
`

	licenceSSPL = `                  SERVER SIDE PUBLIC LICENSE
                       Version 1, October 16, 2018

 Copyright (C) 2018 MongoDB, Inc.
`

	licenceElastic = `Elastic License 2.0

Acceptance
By using the software, you agree to all of the terms and conditions below.
`

	// The Redis Modules shape: a permissive grant with a no-Sell rider bolted on.
	licenceCommonsClause = `                                 Apache License
                           Version 2.0, January 2004

"Commons Clause" License Condition v1.0

The Software is provided to you by the Licensor under the License, as defined
below, subject to the following condition. Without limiting other conditions
in the License, the grant of rights under the License will not include, and
the License does not grant to you, the right to Sell the Software.
`

	// The JSON.org licence: verbatim MIT plus a field-of-use restriction. Debian and the FSF both
	// classify it as non-free.
	licenceJSON = `Copyright (c) 2002 JSON.org

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction.

The Software shall be used for Good, not Evil.
`

	licenceCCBYSA = `Attribution-ShareAlike 4.0 International

Creative Commons Corporation is not a law firm.
`

	// BSD-4-Clause. The advertising clause makes it GPL-incompatible and is a human decision, but
	// it opens with the same grant as BSD-2 and BSD-3.
	licenceBSD4 = `Copyright (c) 1990 The Regents of the University of California.

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are met:

3. All advertising materials mentioning features or use of this software must
   display the following acknowledgement: This product includes software
   developed by the University of California.
`

	// Deliberately not any licence. The gate must fail closed rather than wave it through.
	licenceGibberish = `Terms of Use

You may do whatever you feel is reasonable with this code, within reason.
Be excellent to each other. Ask first if unsure.
`
)

// fixtureOptions describes the fabricated dependency a fixture module pulls in.
type fixtureOptions struct {
	// licence is the body written to the dependency's LICENSE file. Empty writes no licence file
	// at all, which is its own violation.
	licence string
	// licenceName overrides the licence file's name. Empty means "LICENSE".
	licenceName string
	// extraLicence, when non-empty, is written alongside as a second COPYING file — the
	// dual-licensed case, where the more restrictive half still binds.
	extraLicence string
	// thirdParty, when non-empty, is written as LICENSE-3RD-PARTY.md: a bibliography of embedded
	// code rather than a grant, in the shape modernc.org/libc ships.
	thirdParty string
	// suffixedLicences are extra files beside the primary LICENSE, keyed by filename. This is the
	// dual-licence layout — LICENSE-GPL, LICENSE-GO — that modernc.org/memory really ships.
	suffixedLicences map[string]string
	// workspace writes a go.work that `use`s the dependency, as a developer working across two
	// modules would. Under workspace mode every member reports itself as a main module.
	workspace bool
	// testOnly imports the dependency from a _test.go file rather than from the main package, so
	// it is reachable only by a test binary and is NOT part of the runtime graph.
	testOnly bool
}

// writeLicenceFixture builds a complete, offline, single-dependency Go module in tree.
//
// The dependency is wired in with a filesystem `replace`, so `go list` never reaches the network
// and no go.sum entry is required. The root package always compiles: the gate shells out to
// `go list`, which reports a load error rather than a module graph if the fixture is malformed,
// and a malformed fixture must not be mistaken for a licence violation.
func writeLicenceFixture(t *testing.T, tree string, opts fixtureOptions) {
	t.Helper()

	depDir := filepath.Join(tree, "dep")
	require.NoError(t, os.MkdirAll(depDir, 0o755))

	write := func(rel, body string) {
		t.Helper()
		require.NoError(t, os.WriteFile(filepath.Join(tree, filepath.FromSlash(rel)), []byte(body), 0o644))
	}

	write("go.mod", "module fixture\n\ngo 1.26\n\n"+
		"require "+fixtureDep+" v1.0.0\n\n"+
		"replace "+fixtureDep+" => ./dep\n")

	write("dep/go.mod", "module "+fixtureDep+"\n\ngo 1.26\n")
	write("dep/dep.go", "package dep\n\n// Name is referenced so the import is not unused.\nconst Name = \"dep\"\n")

	if opts.licence != "" {
		name := opts.licenceName
		if name == "" {
			name = "LICENSE"
		}
		write("dep/"+name, opts.licence)
	}
	if opts.extraLicence != "" {
		write("dep/COPYING", opts.extraLicence)
	}
	if opts.thirdParty != "" {
		write("dep/LICENSE-3RD-PARTY.md", opts.thirdParty)
	}

	for name, body := range opts.suffixedLicences {
		write("dep/"+name, body)
	}

	if opts.workspace {
		write("go.work", "go 1.26\n\nuse (\n\t.\n\t./dep\n)\n")
	}

	// Where the import lives is the whole runtime-vs-test-only distinction the gate rests on.
	if opts.testOnly {
		write("main.go", "package main\n\nfunc main() {}\n")
		write("main_test.go", "package main\n\nimport (\n\t\"testing\"\n\n\tdep \""+fixtureDep+"\"\n)\n\n"+
			"func TestName(t *testing.T) {\n\tif dep.Name == \"\" {\n\t\tt.Fatal(\"empty\")\n\t}\n}\n")

		return
	}

	write("main.go", "package main\n\nimport dep \""+fixtureDep+"\"\n\nfunc main() {\n\t_ = dep.Name\n}\n")
}

// licenceGate runs the licence gate against a fixture tree built from opts.
//
// Skipped under -short, and only under -short. Each fixture makes the gate resolve a real module
// graph across the release platforms, which costs roughly 250 ms; the ~25 fixtures here would put
// `make test-unit` at 7.5 s against the < 5 s budget in AGENTS.md. Raising that budget to fit these
// tests would be moving the goalpost — `-short` is the split the repo already has for exactly this.
//
// They are NOT excluded from anything that gates a merge. `make check` runs `make test`, not
// `make test-unit`, so the full suite runs there; CI's `test / integration` job does the same; and
// `security / licences` runs the gate itself against the real tree on every PR. `scripts/**` is in
// the `build` path-filter anchor precisely so a change to this gate cannot skip that job.
func licenceGate(t *testing.T, opts fixtureOptions) (output string, exitCode int) {
	t.Helper()

	if testing.Short() {
		t.Skip("licence fixtures resolve a real module graph; run `make test` or `make check`")
	}

	tree := t.TempDir()
	writeLicenceFixture(t, tree, opts)

	return runLicenceGate(t, tree)
}

// runLicenceGate runs the licence gate against tree, returning its combined output and exit code.
//
// This is runGateScript's counterpart for the gate that is no longer a script, and it keeps that
// helper's two guards for the same reasons: DKP_REPO_ROOT must be absolute, because the gate
// resolves a module graph in it from a different working directory, and it must never be empty,
// because the gate then falls back to the working directory and would inspect the real checkout
// while every negative fixture above went green for the wrong reason.
//
// It runs the gate the way `make licence-gate` and CI run it — `go run` from the repo root — rather
// than calling licence.Run in-process. In-process would be faster, but it would stop exercising the
// exit status, the DKP_REPO_ROOT resolution and the entry point that actually ships in the Makefile,
// which are three of the things these fixtures exist to keep honest. The compile lands in the build
// cache once and every later fixture reuses it.
func runLicenceGate(t *testing.T, tree string) (output string, exitCode int) {
	t.Helper()

	require.NotEmpty(t, tree, "DKP_REPO_ROOT must not be empty — the gate falls back to the working directory")
	require.True(t, filepath.IsAbs(tree), "DKP_REPO_ROOT must be absolute, got %q", tree)

	cmd := exec.Command("go", "run", "./internal/licence/cmd/licence", "gate")
	cmd.Dir = repoRoot(t)
	cmd.Env = append(os.Environ(), "DKP_REPO_ROOT="+tree)

	out, err := cmd.CombinedOutput()
	if err == nil {
		return string(out), 0
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return string(out), exitErr.ExitCode()
	}

	t.Fatalf("run the licence gate: %v\n%s", err, out)

	return "", 0
}

// TestLicenceGate_DeniedLicence_FailsGate is the acceptance criterion from
// docs/development/first-ten-prs.md: "fails on any dependency under GPL, AGPL or a CC BY-NC
// variant. A fixture go.mod entry proves it fires."
func TestLicenceGate_DeniedLicence_FailsGate(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		licence string
		id      string
	}{
		{name: "gpl3", licence: licenceGPL3, id: "GPL"},
		{name: "agpl3", licence: licenceAGPL3, id: "AGPL"},
		{name: "lgpl21", licence: licenceLGPL21, id: "LGPL"},
		{name: "cc_by_nc", licence: licenceCCBYNC, id: "CC-BY-NC"},

		// A copyleft licence that also names a permissive one. Any classifier that identifies the
		// permissive mention first and stops looking lets these through, and they are the exact
		// shape a vendoring module takes.
		{name: "gpl3_with_mpl_preamble", licence: licenceGPL3WithMPLPreamble, id: "GPL"},
		{name: "agpl3_with_mpl_spdx", licence: licenceAGPL3WithMPLSPDX, id: "AGPL"},

		// The MPL §1.12 removal must not swallow a GPL grant that follows it.
		{
			name:    "gpl3_after_mpl_secondary_licence_definition",
			licence: licenceGPL3AfterSecondaryLicenceDefinition,
			id:      "GPL",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			out, code := licenceGate(t, fixtureOptions{licence: tc.licence})

			require.NotZero(t, code, "a %s runtime dependency must fail the gate\n%s", tc.id, out)
			require.Contains(t, out, "LIC001",
				"the gate went red, but not because of the denied-licence rule\n%s", out)
			require.Contains(t, out, tc.id,
				"LIC001 must name the licence it identified\n%s", out)
			require.Contains(t, out, fixtureDep+"@v1.0.0",
				"LIC001 must name the offending module and version\n%s", out)
			require.NotContains(t, out, "LIC002",
				"the licence was identifiable, so the fail-closed rule must not also fire\n%s", out)
		})
	}
}

// TestLicenceGate_DualLicensedWithCopyleft_FailsGate covers a module shipping a permissive LICENSE
// beside a copyleft COPYING. Stopping at the first identifiable file would clear it on the MIT half
// while the GPL half still binds.
func TestLicenceGate_DualLicensedWithCopyleft_FailsGate(t *testing.T) {
	t.Parallel()

	out, code := licenceGate(t, fixtureOptions{licence: licenceMIT, extraLicence: licenceGPL3})

	require.NotZero(t, code, "a dependency whose second licence file is GPL must fail\n%s", out)
	require.Contains(t, out, "LIC001",
		"the permissive LICENSE must not excuse the copyleft COPYING\n%s", out)
	require.Contains(t, out, fixtureDep+"@v1.0.0", "LIC001 must name the offending module\n%s", out)
}

// TestLicenceGate_NonOpenSourceLicence_FailsGate covers the source-available family and the
// restriction-rider shapes.
//
// None of these is copyleft, so none is caught by the GPL/AGPL patterns — but every one of them
// forbids something this project needs. BUSL says in its own text that it "is not an Open Source
// license"; Commons Clause removes the right to sell; the JSON licence's "Good, not Evil" clause is
// why Debian and the FSF both call it non-free. Dragon Kill Party is Apache-2.0 and is run by
// volunteer officers, some of whom will host it for money.
//
// Each of these is a licence a human must decide about. None may pass silently.
func TestLicenceGate_NonOpenSourceLicence_FailsGate(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		licence string
	}{
		// BUSL's Change License parameter names the MPL, so it reads as permissive to a
		// first-match classifier.
		{name: "busl_naming_mpl_as_change_licence", licence: licenceBUSL},
		{name: "sspl", licence: licenceSSPL},
		{name: "elastic", licence: licenceElastic},
		// A permissive grant with a restriction bolted on. The grant matches; the rider is what
		// matters.
		{name: "commons_clause_on_apache2", licence: licenceCommonsClause},
		{name: "json_good_not_evil", licence: licenceJSON},
		{name: "cc_by_sa", licence: licenceCCBYSA},
		{name: "bsd_4_clause_advertising", licence: licenceBSD4},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			out, code := licenceGate(t, fixtureOptions{licence: tc.licence})

			require.NotZero(t, code,
				"a %s dependency must not pass silently — it is a human decision\n%s", tc.name, out)
			require.Contains(t, out, fixtureDep+"@v1.0.0", "the gate must name the module\n%s", out)
			require.Regexp(t, `LIC00[12]`,
				out, "the gate must fail with a named rule id, not an unexplained non-zero exit")
		})
	}
}

// TestLicenceGate_WorkspaceMode_StillExaminesDependencies covers the `go.work` vacuous pass.
//
// Under a workspace, `go list` reports EVERY member as a main module with an empty version — so a
// third-party module brought in with `use ./thing` looks exactly like the repository itself. A gate
// that identifies the main module by its empty version skips the dependency as first-party, counts
// zero modules, and prints its green banner having examined nothing.
//
// The gate sets GOWORK=off so it always resolves the graph the shipped binary actually has. This
// fixture is a real workspace with a GPL dependency; it must still be caught.
func TestLicenceGate_WorkspaceMode_StillExaminesDependencies(t *testing.T) {
	t.Parallel()

	out, code := licenceGate(t, fixtureOptions{licence: licenceGPL3, workspace: true})

	require.NotZero(t, code,
		"a GPL dependency must be caught even inside a go.work workspace\n%s", out)
	require.Contains(t, out, "LIC001",
		"the dependency was skipped as if it were a main module — is GOWORK=off still set?\n%s", out)
	require.NotContains(t, out, "0 runtime dependencies",
		"the gate examined nothing and reported success\n%s", out)
}

// TestLicenceGate_SuffixedLicenceFile_FailsGate covers the dual-licence layout, which is the
// commonest way a copyleft grant arrives beside a permissive one.
//
// A glob of `LICENSE` + `LICENSE.*` requires a literal dot, so it matches LICENSE.md and misses
// LICENSE-GPL entirely. That is not a contrived filename: modernc.org/memory — a live runtime
// dependency of this repo — ships LICENSE, LICENSE-GO, LICENSE-MMAP-GO and LICENSE-LOGO, and such a
// glob reads exactly one of the four. A module could put its real AGPL grant in the second file and
// the gate would classify the first and report the tree clean.
func TestLicenceGate_SuffixedLicenceFile_FailsGate(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		file string
	}{
		{name: "license_gpl", file: "LICENSE-GPL"},
		{name: "license_agpl", file: "LICENSE-AGPL"},
		{name: "license_underscore_gpl", file: "LICENSE_GPL"},
		{name: "copyright", file: "COPYRIGHT"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			out, code := licenceGate(t, fixtureOptions{
				licence:          licenceMIT,
				suffixedLicences: map[string]string{tc.file: licenceAGPL3},
			})

			require.NotZero(t, code,
				"a permissive LICENSE must not excuse an AGPL grant in %s\n%s", tc.file, out)
			require.Contains(t, out, "LIC003",
				"a denied licence beside the primary grant is LIC003\n%s", out)
			require.Contains(t, out, tc.file, "the gate must name the offending file\n%s", out)
			require.Contains(t, out, fixtureDep+"@v1.0.0", "the gate must name the module\n%s", out)
		})
	}
}

// TestLicenceGate_SuffixedPermissiveLicences_Pass is the control for the test above, and the reason
// auxiliary files are deny-scanned rather than classified.
//
// modernc.org/memory ships LICENSE-LOGO containing nothing but a URL to a Wikimedia image page.
// Classifying auxiliary files the way the primary grant is classified would make that LIC002 and
// turn the real tree red — and a gate that goes red on the current dependency set is a gate that
// gets deleted rather than fixed.
func TestLicenceGate_SuffixedPermissiveLicences_Pass(t *testing.T) {
	t.Parallel()

	out, code := licenceGate(t, fixtureOptions{
		licence: licenceBSD3,
		suffixedLicences: map[string]string{
			"LICENSE-GO":   licenceBSD3,
			"LICENSE-LOGO": "https://commons.wikimedia.org/wiki/File:Example_logo.png\n",
		},
	})

	require.Zero(t, code,
		"permissive and non-grant auxiliary files must not fail the gate\n%s", out)
	require.NotContains(t, out, "LIC003", "%s", out)
	require.NotContains(t, out, "LIC002",
		"an auxiliary file that is not a grant must not be treated as an unidentifiable licence\n%s", out)
}

// TestLicenceGate_EmbeddedThirdPartyCopyleft_FailsGate covers LIC003.
//
// A permissively-licensed module that transpiles or vendors C sources declares what it embedded in
// a LICENSE-3RD-PARTY.md or NOTICE. Those files are excluded from classification — reading a
// bibliography as a grant would fire on every licence it lists — but they are exactly where a
// module admits to carrying GPL code.
//
// This is not hypothetical for this repo: modernc.org/libc ships such a file (Go/BSD and musl/MIT
// today), and modernc.org/sqlite is the largest single dependency in the shipped binary.
func TestLicenceGate_EmbeddedThirdPartyCopyleft_FailsGate(t *testing.T) {
	t.Parallel()

	thirdParty := `# Third-party licences

This package embeds transpiled C sources from:

- musl — MIT License
- readline — GNU GENERAL PUBLIC LICENSE, Version 3
`

	out, code := licenceGate(t, fixtureOptions{licence: licenceBSD3, thirdParty: thirdParty})

	require.NotZero(t, code,
		"a BSD module declaring embedded GPL code must not pass on its own licence alone\n%s", out)
	require.Contains(t, out, "LIC003",
		"embedded third-party copyleft is LIC003, distinct from the module's own licence\n%s", out)
	require.Contains(t, out, fixtureDep+"@v1.0.0", "LIC003 must name the module\n%s", out)
	require.Contains(t, out, "LICENSE-3RD-PARTY.md",
		"LIC003 must name the file that declared it\n%s", out)
}

// TestLicenceGate_ThirdPartyManifestOfPermissiveCode_Passes is the control for LIC003, and the
// reason the bibliography is not simply fed to the normal classifier.
//
// modernc.org/libc's real LICENSE-3RD-PARTY.md lists BSD and MIT components. Firing on it would
// make the gate red on the current tree, which is how a gate gets disabled.
func TestLicenceGate_ThirdPartyManifestOfPermissiveCode_Passes(t *testing.T) {
	t.Parallel()

	thirdParty := `# Third-party licences

- musl — MIT License
- Go standard library — BSD 3-Clause
- public domain fragments
`

	out, code := licenceGate(t, fixtureOptions{licence: licenceBSD3, thirdParty: thirdParty})

	require.Zero(t, code,
		"a bibliography listing only permissive components must not fail the gate\n%s", out)
	require.NotContains(t, out, "LIC003", "%s", out)
}

// TestLicenceGate_NoticeProse_DoesNotFalselyFire is the false-positive control for LIC003.
//
// LIC003 applies the denylist to NOTICE and third-party files as an unanchored substring match, so
// ordinary prose that merely names a licence can trip it. That matters more than it sounds: there
// is no waiver mechanism, so a maintainer who hits a false positive on a dependency they need has
// only two options, and AGENTS.md forbids the second. A gate that goes red on a legitimate NOTICE
// gets commented out on a raid night.
//
// These are the shapes that were verified to false-positive before the patterns were tightened.
func TestLicenceGate_NoticeProse_DoesNotFalselyFire(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		notice string
	}{
		{
			name:   "noncommercial_as_an_ordinary_word",
			notice: "This library is used by both commercial and noncommercial projects.\n",
		},
		{
			name:   "cddl_as_a_bare_token_in_prose",
			notice: "Historical note: the CDDL debate shaped this project's licence choice.\n",
		},
		{
			name: "openssl_advertising_clause_reproduced_in_a_notice",
			notice: "This product includes software developed by the OpenSSL Project.\n" +
				"All advertising materials mentioning features or use of this software\n" +
				"must display the following acknowledgment.\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			out, code := licenceGate(t, fixtureOptions{licence: licenceMIT, thirdParty: tc.notice})

			require.Zero(t, code,
				"prose in a NOTICE file must not fail the gate — a gate that is red on a "+
					"legitimate dependency gets deleted\n%s", out)
			require.NotContains(t, out, "LIC003", "%s", out)
		})
	}
}

// TestLicenceGate_PlatformSubset_CoversTheFullMatrix enforces the claim the gatePlatforms comment
// makes, rather than leaving it as prose that was true once.
//
// The gate queries one GOARCH per GOOS on the grounds that build constraints which add a MODULE are
// GOOS-gated. That is measured, not guaranteed: a future dependency behind `//go:build arm64` would
// be resolved by no query and classified by nothing, which is a licence hole with a silent onset.
func TestLicenceGate_PlatformSubset_CoversTheFullMatrix(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("resolves the module graph across nine platforms; run `make test` or `make check`")
	}

	root := repoRoot(t)

	modulesFor := func(goos, goarch string) []string {
		cmd := exec.Command("go", "list", "-deps", "-f",
			"{{if .Module}}{{if not .Module.Main}}{{.Module.Path}}{{end}}{{end}}", "./...")
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GOWORK=off", "GOOS="+goos, "GOARCH="+goarch)

		out, err := cmd.Output()
		require.NoError(t, err, "go list for %s/%s", goos, goarch)

		var mods []string
		for _, l := range strings.Split(string(out), "\n") {
			if l = strings.TrimSpace(l); l != "" {
				mods = append(mods, l)
			}
		}

		return mods
	}

	union := func(platforms [][2]string) map[string]bool {
		set := make(map[string]bool)
		for _, p := range platforms {
			for _, m := range modulesFor(p[0], p[1]) {
				set[m] = true
			}
		}

		return set
	}

	// What the gate actually queries, read from the gate rather than restated here. Hardcoding the
	// three platforms would make this a test of this file's own constant: reducing GatePlatforms to
	// linux alone would still pass, which is exactly the drift the test exists to catch. It used to
	// be regexed out of the shell script's PLATFORMS line; asking the package is the same assertion
	// with the parsing removed.
	var gatePlatforms [][2]string

	for _, p := range licence.GatePlatforms() {
		gatePlatforms = append(gatePlatforms, [2]string{p.GOOS, p.GOARCH})
	}

	require.NotEmpty(t, gatePlatforms, "licence.GatePlatforms() is empty")

	queried := union(gatePlatforms)

	// The full release matrix from ROADMAP.md: {linux,darwin,windows} x {amd64,arm64}, plus 386
	// and arm as the cheapest way to catch a 32-bit-only dependency.
	full := union([][2]string{
		{"linux", "amd64"},
		{"linux", "arm64"},
		{"linux", "386"},
		{"linux", "arm"},
		{"darwin", "amd64"},
		{"darwin", "arm64"},
		{"windows", "amd64"},
		{"windows", "arm64"},
	})

	require.NotEmpty(t, queried, "the three-platform query resolved no modules")

	for m := range full {
		require.True(t, queried[m],
			"module %q is in the full release matrix but not in the three platforms "+
				"licence.GatePlatforms() queries, so its licence is never classified. Add the "+
				"platform that resolves it to gatePlatforms in internal/licence.", m)
	}
}

// TestLicenceModules_FeedsTheNoticesGenerator covers the other consumer of the shared enumeration.
//
// scripts/third-party-notices.sh redirects `licence modules` into a file and walks it with
// `IFS=$'\t' read -r path version dir`. Nothing else runs that subcommand: `make check` does not
// regenerate the notices file, so a break in this output shape would first be seen by goreleaser's
// before-hook at RELEASE time, on the one run where nobody wants a surprise. This is that coverage.
//
// It asserts the contract the script depends on, not the contents: three tab-separated fields, a
// module path in the first, and a directory that exists in the third — because an empty Dir is how
// the generator silently emits "(no licence file found)" for a module whose licence is sitting in
// the cache.
func TestLicenceModules_FeedsTheNoticesGenerator(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("resolves the module graph across the release platforms; run `make test` or `make check`")
	}

	cmd := exec.Command("go", "run", "./internal/licence/cmd/licence", "modules")
	cmd.Dir = repoRoot(t)

	out, err := cmd.Output()
	require.NoError(t, err, "go run licence modules\n%s", out)

	rows := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	require.NotEmpty(t, rows, "the notices generator refuses to write an empty file, and so should this")

	seen := make(map[string]bool, len(rows))

	for _, row := range rows {
		fields := strings.Split(row, "\t")
		require.Lenf(t, fields, 3,
			"every row must be path\\tversion\\tdir — scripts/third-party-notices.sh splits on tabs "+
				"and a fourth field would land in $dir: %q", row)

		path, dir := fields[0], fields[2]

		require.NotEmptyf(t, path, "a row with no module path: %q", row)
		require.Falsef(t, seen[path],
			"module %q appears twice, so its licence text would be reproduced twice in "+
				"THIRD_PARTY_NOTICES.txt — the union across platforms must be deduplicated", path)
		seen[path] = true

		info, err := os.Stat(dir)
		require.NoErrorf(t, err,
			"module %q reports a directory the generator cannot read, so its licence would be "+
				"replaced by '(no licence file found)' in the shipped attribution file", path)
		require.Truef(t, info.IsDir(), "%s is not a directory", dir)
	}

	require.NotContains(t, seen, "github.com/prokopto-dev/dragonkillparty",
		"the main module is not a dependency of itself and owes itself no attribution")
}

// TestLicence_IsNotLinkedIntoTheBinary keeps the gate on the tooling side of the line.
//
// internal/licence is repo tooling with its own main package, which is a deviation from "cmd/dkp is
// the only binary" and is allowed on exactly one condition: an operator never gets it. A product
// package importing the gate — for a "what licence is this" endpoint, for a doctor check — would put
// a classifier, a `go list` shell-out and an ANSI report inside the shipped binary, and nothing else
// in the repo would notice.
//
// AGENTS.md's repo map states this; this is the mechanism behind the statement.
func TestLicence_IsNotLinkedIntoTheBinary(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("resolves the binary's package graph; run `make test` or `make check`")
	}

	cmd := exec.Command("go", "list", "-deps", "./cmd/dkp")
	cmd.Dir = repoRoot(t)
	cmd.Env = append(os.Environ(), "GOWORK=off")

	out, err := cmd.Output()
	require.NoError(t, err, "go list -deps ./cmd/dkp")

	packages := strings.Split(string(out), "\n")
	require.NotEmpty(t, packages, "the binary's package graph resolved to nothing")

	for _, p := range packages {
		require.NotContainsf(t, strings.TrimSpace(p), "internal/licence",
			"%s is in the shipped binary's package graph. internal/licence is repo tooling: it "+
				"shells out to `go list`, writes ANSI to stdout and carries the whole licence "+
				"denylist. None of that belongs in the binary a volunteer officer runs.", p)
	}
}

// TestLicencePlatforms_CoverTheGoreleaserBuildMatrix ties the one platform list to the one that
// decides what actually ships.
//
// scripts/third-party-notices.sh carried the instruction as a comment — "keep in step with
// .goreleaser.yaml's build matrix" — which is exactly the kind of promise the consolidation in
// issue #130 was meant to stop relying on. A platform added to the release and not to
// internal/licence is a binary whose runtime graph is neither classified by the licence gate nor
// attributed in THIRD_PARTY_NOTICES.txt, and nothing about that fails.
//
// A superset is fine and is the current state: licence.ReleasePlatforms() also carries linux/arm,
// which goreleaser does not build. Over-covering costs one paragraph of notices; under-covering
// costs the firewall.
func TestLicencePlatforms_CoverTheGoreleaserBuildMatrix(t *testing.T) {
	t.Parallel()

	b, err := os.ReadFile(filepath.Join(repoRoot(t), ".goreleaser.yaml"))
	require.NoError(t, err)

	// The `builds:` entry's goos/goarch lists. Read with a narrow regexp rather than a YAML parser:
	// AGENTS.md requires a human to approve a dependency, and one 12-line block does not earn one.
	// A shape change fails here with this message rather than passing vacuously.
	list := func(key string) []string {
		m := regexp.MustCompile(`(?m)^    ` + key + `:\n((?:      - \w+\n)+)`).FindStringSubmatch(string(b))
		require.NotNilf(t, m, "could not read the builds %s list out of .goreleaser.yaml", key)

		var values []string
		for _, line := range strings.Split(strings.TrimSpace(m[1]), "\n") {
			values = append(values, strings.TrimPrefix(strings.TrimSpace(line), "- "))
		}

		return values
	}

	goos, goarch := list("goos"), list("goarch")
	require.NotEmpty(t, goos)
	require.NotEmpty(t, goarch)

	released := licence.ReleasePlatforms()

	for _, system := range goos {
		for _, arch := range goarch {
			p := licence.Platform{GOOS: system, GOARCH: arch}
			require.Containsf(t, released, p,
				"goreleaser builds %s, but licence.ReleasePlatforms() does not list it. Its runtime "+
					"graph is then neither licence-classified nor attributed in "+
					"THIRD_PARTY_NOTICES.txt — add it to releasePlatforms in internal/licence.", p)
		}
	}
}

// TestLicenceGate_NoPackages_FailsGate closes a vacuous-pass path.
//
// `go list ./...` on a module with no Go packages exits ZERO and writes "matched no packages" to
// stderr. A gate that folds stderr into its output sees a non-empty result, parses no modules, and
// reports success having examined nothing — while ci-required positively asserts the job ran.
//
// The gate's package doc claims it has no vacuous-pass path. This is the test that makes that true.
func TestLicenceGate_NoPackages_FailsGate(t *testing.T) {
	t.Parallel()

	tree := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(tree, "go.mod"),
		[]byte("module fixture\n\ngo 1.26\n"), 0o644))

	out, code := runLicenceGate(t, tree)

	require.NotZero(t, code,
		"a module with no packages must be an error — the gate resolved nothing\n%s", out)
	require.NotContains(t, out, "all under allowed licences",
		"the gate reported success having examined no dependencies\n%s", out)
}

// TestLicenceGate_AllowedPlusUnidentifiableLicence_FailsGate is the other half of the dual-licence
// case, and the one that is easy to get wrong.
//
// A module shipping a recognisable LICENSE beside a second licence file the gate cannot classify
// must still stop the build. Accumulating only the identified licences and taking the first allowed
// one as the verdict silently drops the unclassifiable file — the module passes, and the fail-closed
// guarantee holds only for modules that ship exactly one licence.
//
// This is a realistic shape: a standard LICENSE alongside a bespoke COPYING, PATENTS or
// COPYING.THIRD-PARTY that no pattern matches.
func TestLicenceGate_AllowedPlusUnidentifiableLicence_FailsGate(t *testing.T) {
	t.Parallel()

	out, code := licenceGate(t, fixtureOptions{licence: licenceMIT, extraLicence: licenceGibberish})

	require.NotZero(t, code,
		"an unclassifiable second licence file must fail, even beside an allowed one\n%s", out)
	require.Contains(t, out, "LIC002",
		"the identified MIT licence must not excuse the file the gate could not classify\n%s", out)
	require.Contains(t, out, "COPYING",
		"LIC002 must name the licence file it could not identify\n%s", out)
	require.Contains(t, out, fixtureDep+"@v1.0.0", "LIC002 must name the module\n%s", out)
}

// TestLicenceGate_UnidentifiableLicence_FailsGate pins the fail-closed decision. AGENTS.md already
// requires a human to approve every new dependency, so a licence the gate cannot classify must stop
// the build rather than pass quietly.
func TestLicenceGate_UnidentifiableLicence_FailsGate(t *testing.T) {
	t.Parallel()

	out, code := licenceGate(t, fixtureOptions{licence: licenceGibberish})

	require.NotZero(t, code, "an unidentifiable licence must fail the gate, not pass it\n%s", out)
	require.Contains(t, out, "LIC002",
		"an unidentifiable licence is LIC002, not LIC001\n%s", out)
	require.Contains(t, out, fixtureDep+"@v1.0.0", "LIC002 must name the module\n%s", out)
	require.NotContains(t, out, "LIC001",
		"nothing was identified, so the denied-licence rule cannot have fired\n%s", out)
}

// TestLicenceGate_NoLicenceFile_FailsGate is the same fail-closed rule for a module that ships no
// licence at all — the most common shape of an unvetted dependency.
func TestLicenceGate_NoLicenceFile_FailsGate(t *testing.T) {
	t.Parallel()

	out, code := licenceGate(t, fixtureOptions{})

	require.NotZero(t, code, "a dependency with no licence file must fail the gate\n%s", out)
	require.Contains(t, out, "LIC002", "a missing licence file is LIC002\n%s", out)
	require.Contains(t, out, fixtureDep+"@v1.0.0", "LIC002 must name the module\n%s", out)
}

// TestLicenceGate_AllowedLicence_Passes is the allowlist control, and it is not optional.
//
// Every test above would also pass if the gate simply failed on everything. These are the cases
// that prove it does not — a gate that fires on the whole module graph is a gate that gets bypassed
// the first time it does.
func TestLicenceGate_AllowedLicence_Passes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		licence string
		id      string
	}{
		{name: "mit", licence: licenceMIT, id: "MIT"},
		{name: "apache2", licence: licenceApache2, id: "Apache-2.0"},
		{name: "bsd3", licence: licenceBSD3, id: "BSD"},
		// MPL-2.0 names the GPL, LGPL and AGPL in its own §1.12. This case fails on any gate that
		// applies its denylist before identifying the licence.
		{name: "mpl2_names_the_gnu_licences", licence: licenceMPL2, id: "MPL-2.0"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			out, code := licenceGate(t, fixtureOptions{licence: tc.licence})

			require.Zero(t, code, "a %s runtime dependency must pass the gate\n%s", tc.id, out)
			require.NotContains(t, out, "LIC001", "%s must not trip the denied-licence rule\n%s", tc.id, out)
			require.NotContains(t, out, "LIC002", "%s must be identifiable\n%s", tc.id, out)
			require.Contains(t, out, tc.id, "the gate must report the licence it identified\n%s", out)
		})
	}
}

// TestLicenceGate_LicenceNamedLicenceTxt_Passes covers the spelling variants that appear in the
// real module cache — github.com/spf13/cobra ships LICENSE.txt, not LICENSE.
func TestLicenceGate_LicenceNamedLicenceTxt_Passes(t *testing.T) {
	t.Parallel()

	out, code := licenceGate(t, fixtureOptions{licence: licenceApache2, licenceName: "LICENSE.txt"})

	require.Zero(t, code, "LICENSE.txt must be found, not treated as a missing licence\n%s", out)
	require.NotContains(t, out, "LIC002", "LICENSE.txt must be discovered\n%s", out)
}

// TestLicenceGate_TestOnlyDeniedLicence_Passes is the golang-lru case, asserted rather than
// described.
//
// github.com/hashicorp/golang-lru/v2 is in this repo's `go list -m all` because modernc.org/libc's
// own tests import it. It is not linked into dkp. A gate that fails on a module reachable only from
// a dependency's test binary fails on code the project does not ship — and is a gate someone
// disables the first time it fires. The fixture uses a DENIED licence deliberately: if the gate
// ever widens its scope to `-test` or `go list -m all`, this test goes red immediately.
func TestLicenceGate_TestOnlyDeniedLicence_Passes(t *testing.T) {
	t.Parallel()

	out, code := licenceGate(t, fixtureOptions{licence: licenceGPL3, testOnly: true})

	require.Zero(t, code,
		"a GPL module reachable only from a _test.go import is not in the runtime graph\n%s", out)
	require.NotContains(t, out, "LIC001",
		"the gate must scope to `go list -deps ./...` without -test\n%s", out)
	require.NotContains(t, out, fixtureDep,
		"a test-only dependency must not appear in the runtime module set at all\n%s", out)
}

// TestLicenceGate_RealTree_Passes is the control for every negative test above. Without it, a
// harness that is simply broken — a bad script path, a DKP_REPO_ROOT that never resolves — would
// still make the failing-fixture tests go green.
func TestLicenceGate_RealTree_Passes(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	out, code := runLicenceGate(t, root)

	require.Zero(t, code, "the repository's own runtime dependencies must pass the gate\n%s", out)
	require.NotContains(t, out, "LIC001", "%s", out)
	require.NotContains(t, out, "LIC002", "%s", out)
	require.Contains(t, out, "all under allowed licences", "%s", out)

	// The gate must have actually resolved a module graph. Without this the test would pass on a
	// tree where `go list` returned nothing, which is the vacuous-pass failure mode.
	require.NotContains(t, out, "0 runtime dependencies", "the gate resolved no dependencies\n%s", out)

	// The MPL-2.0 module in `go list -m all` is reachable only through modernc.org/libc's test
	// binary. It must not be in the runtime set — this is the real-world instance of
	// TestLicenceGate_TestOnlyDeniedLicence_Passes.
	require.NotContains(t, out, "golang-lru",
		"golang-lru is reachable only from a dependency's tests and is not linked into dkp\n%s", out)
}

// TestMakefile_LicenceGate_StripsRepoRootEnv mirrors the guard on lint-repo: a hostile
// DKP_REPO_ROOT in the environment must not redirect the gate CI runs.
//
// The `env -u DKP_REPO_ROOT` on the recipe is what enforces it. Without that prefix the variable
// would leak from a developer's shell into `make check` and the gate would inspect the wrong tree
// while still printing that it passed.
func TestMakefile_LicenceGate_StripsRepoRootEnv(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)

	// A tree the gate would hard-fail on: it exists, but holds no go.mod.
	empty := t.TempDir()

	cmd := exec.Command("make", "licence-gate")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "DKP_REPO_ROOT="+empty)

	out, err := cmd.CombinedOutput()
	require.NoError(t, err,
		"make licence-gate must still scan the real tree with a hostile DKP_REPO_ROOT set — is "+
			"`env -u DKP_REPO_ROOT` still on the licence-gate recipe in the Makefile?\n%s", out)

	require.NotContains(t, string(out), "no go.mod",
		"the gate ran against %s instead of the repo — the env -u prefix is missing\n%s", empty, out)
	require.Contains(t, string(out), "all under allowed licences", "%s", out)
}

// TestLicenceGate_HonoursRepoRootOverride guards the mechanism every other test in this file rests
// on. If the gate stopped honouring DKP_REPO_ROOT it would silently scan the real repo, and
// every negative fixture above would go green for the wrong reason.
func TestLicenceGate_HonoursRepoRootOverride(t *testing.T) {
	t.Parallel()

	empty := t.TempDir()
	out, code := runLicenceGate(t, empty)

	require.NotZero(t, code, "a tree with no go.mod must be an error, never a silent pass\n%s", out)
	require.Contains(t, strings.ToLower(out), "no go.mod",
		"the gate must say why it could not run, and must not fall back to the real repo\n%s", out)
	require.NotContains(t, out, "all under allowed licences",
		"the gate scanned the real repo instead of %s — DKP_REPO_ROOT is not being honoured\n%s", empty, out)
}
