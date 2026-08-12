// Unit tests for the licence classifier.
//
// These are what the move from shell to Go was for (issue #130). The gate's black-box fixtures in
// test/repo/licence_gate_test.go build a whole fabricated module tree per case and cost ~250 ms
// each, so they cover the shapes that MATTER most and cannot afford to cover every licence family.
// Here a case is a string and a function call, so every pattern in both tables gets one — including
// the families no fixture reaches (EPL, CDDL, FSL, PolyForm, CC BY-ND, ISC, CC0, Unlicense, Zlib),
// which until now were never executed by anything.
//
// The bodies below are probes, not fixtures: each is the smallest text that exercises one pattern,
// written against the licence's real title, SPDX identifier or operative grant. They deliberately do
// NOT restate the module fixtures in test/repo, which serve the other layer.
package licence_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/licence"
)

// TestDenyHits_EveryDeniedFamily_Fires gives each denylist pattern one case.
//
// A pattern nobody has ever seen match is a pattern nobody knows works — the same argument
// test/repo makes for the gates themselves, one level down.
func TestDenyHits_EveryDeniedFamily_Fires(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		id   string
		text string
	}{
		{name: "busl", id: "BUSL", text: "Business Source License 1.1\n\nChange License: Apache-2.0\n"},
		{name: "sspl", id: "SSPL", text: "          SERVER SIDE PUBLIC LICENSE\n   Version 1, October 16, 2018\n"},
		{name: "elastic", id: "Elastic", text: "Elastic License 2.0\n\nAcceptance\n"},
		{name: "fsl", id: "FSL", text: "Functional Source License, Version 1.1, ALv2 Future License\n"},
		{name: "polyform", id: "PolyForm", text: "PolyForm Noncommercial License 1.0.0\n"},
		{name: "polyform_hyphenated", id: "PolyForm", text: "PolyForm Small-Business License 1.0.0\n"},

		{name: "agpl_title", id: "AGPL", text: "GNU AFFERO GENERAL PUBLIC LICENSE\nVersion 3\n"},
		{name: "agpl_spdx", id: "AGPL", text: "SPDX-License-Identifier: AGPL-3.0-or-later\n"},
		{name: "agpl_short", id: "AGPL", text: "Licensed under AGPLv3.\n"},
		{name: "lgpl_title", id: "LGPL", text: "GNU LESSER GENERAL PUBLIC LICENSE\nVersion 2.1\n"},
		{name: "lgpl_library_title", id: "LGPL", text: "GNU LIBRARY GENERAL PUBLIC LICENSE\nVersion 2\n"},
		{name: "lgpl_spdx", id: "LGPL", text: "SPDX-License-Identifier: LGPL-2.1\n"},
		{name: "gpl_title", id: "GPL", text: "GNU GENERAL PUBLIC LICENSE\nVersion 3, 29 June 2007\n"},
		{name: "gpl_spdx", id: "GPL", text: "SPDX-License-Identifier: GPL-3.0-only\n"},
		{name: "gpl_short", id: "GPL", text: "Released under GPLv2.\n"},
		{name: "epl_title", id: "EPL", text: "Eclipse Public License - v 2.0\n"},
		{name: "epl_spdx", id: "EPL", text: "SPDX-License-Identifier: EPL-2.0\n"},
		{name: "cddl_title", id: "CDDL", text: "COMMON DEVELOPMENT AND DISTRIBUTION LICENSE (CDDL) Version 1.0\n"},
		{name: "cddl_versioned", id: "CDDL", text: "This component is under CDDL-1.1.\n"},
		{name: "cddl_named", id: "CDDL", text: "Distributed under the CDDL License.\n"},

		{name: "cc_by_nc_long", id: "CC-BY-NC", text: "Attribution-NonCommercial 4.0 International\n"},
		{name: "cc_by_nc_short", id: "CC-BY-NC", text: "Licensed CC-BY-NC-4.0.\n"},
		{name: "cc_by_sa_long", id: "CC-BY-SA", text: "Attribution-ShareAlike 4.0 International\n"},
		{name: "cc_by_sa_short", id: "CC-BY-SA", text: "Licensed CC BY-SA 3.0.\n"},
		{name: "cc_by_nd_long", id: "CC-BY-ND", text: "Attribution-NoDerivatives 4.0 International\n"},
		{name: "cc_by_nd_short", id: "CC-BY-ND", text: "Licensed CC-BY-ND-4.0.\n"},

		{name: "commons_clause_named", id: "CommonsClause", text: `"Commons Clause" License Condition v1.0` + "\n"},
		{name: "commons_clause_operative", id: "CommonsClause", text: "the License does not grant to you, the right to Sell the Software.\n"},
		{name: "json_evil", id: "JSON-Evil", text: "The Software shall be used for Good, not Evil.\n"},
		{name: "bsd_4_clause", id: "BSD-4-Clause", text: "3. All advertising materials mentioning features or use of this software\n   must display the following acknowledgement.\n"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			hits := licence.DenyHits(licence.Normalise(tc.text))

			require.Containsf(t, hits, tc.id,
				"the denylist must recognise this as %s, and did not. A denied family the gate cannot "+
					"see is a copyleft or source-available dependency that ships.\ntext: %q\nhits: %v",
				tc.id, tc.text, hits)
		})
	}
}

// TestAllowHits_EveryAllowedFamily_IsRecognised is the same coverage for the permissive side.
//
// A permissive licence the classifier fails to recognise is not a silent hole — it is LIC002 and
// the build stops — but it stops on a dependency that is perfectly fine, and a gate that goes red on
// a legitimate module is a gate that gets deleted rather than fixed.
func TestAllowHits_EveryAllowedFamily_IsRecognised(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		id   string
		text string
	}{
		{name: "apache2_title", id: "Apache-2.0", text: "                Apache License\n           Version 2.0, January 2004\n"},
		{name: "apache2_spdx", id: "Apache-2.0", text: "SPDX-License-Identifier: Apache-2.0\n"},
		{name: "mpl2_title", id: "MPL-2.0", text: "Mozilla Public License Version 2.0\n"},
		{name: "mpl2_comma_form", id: "MPL-2.0", text: "This Source Code Form is subject to the Mozilla Public License, v. 2.0.\nMozilla Public License, version 2.0\n"},
		{name: "mpl2_spdx", id: "MPL-2.0", text: "SPDX-License-Identifier: MPL-2.0\n"},
		{name: "mit_grant", id: "MIT", text: "Permission is hereby granted, free of charge, to any person obtaining a copy\n"},
		{name: "mit_title", id: "MIT", text: "The MIT License\n\nCopyright (c) 2026 Example\n"},
		{name: "isc", id: "ISC", text: "Permission to use, copy, modify, and/or distribute this software for any\npurpose with or without fee is hereby granted.\n"},
		{name: "bsd", id: "BSD", text: "Redistribution and use in source and binary forms, with or without\nmodification, are permitted.\n"},
		{name: "cc0_title", id: "CC0-1.0", text: "CC0 1.0 Universal\n"},
		{name: "cc0_spdx", id: "CC0-1.0", text: "SPDX-License-Identifier: CC0-1.0\n"},
		{name: "unlicense", id: "Unlicense", text: "This is free and unencumbered software released into the public domain.\n"},
		{name: "zlib", id: "Zlib", text: `This software is provided 'as-is', without any express or implied warranty.` + "\n"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			hits := licence.AllowHits(licence.Normalise(tc.text))

			require.Containsf(t, hits, tc.id,
				"the classifier must recognise this as %s, and did not — it would be LIC002 on a "+
					"licence this project has decided about.\ntext: %q\nhits: %v", tc.id, tc.text, hits)
		})
	}
}

// TestClassify_LayeredRider_IsDeniedWithoutHidingItsPermissiveBase is rule 1 of the package doc,
// asserted rather than described.
//
// Each of these is a permissive grant with a restriction bolted on. A classifier that stops at the
// first licence it recognises reports the base and waves the module through. The assertion is
// therefore in two halves: the rider is denied, AND the permissive base was still recognised —
// which is what proves every pattern was evaluated rather than the denial happening to come first.
func TestClassify_LayeredRider_IsDeniedWithoutHidingItsPermissiveBase(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		text  string
		deny  string
		allow string
	}{
		{
			// The Redis Modules shape: Apache-2.0 with a no-Sell condition.
			name: "commons_clause_on_apache2",
			text: "Apache License\nVersion 2.0, January 2004\n\n" +
				`"Commons Clause" License Condition v1.0` + "\n\n" +
				"the License does not grant to you, the right to Sell the Software.\n",
			deny:  "CommonsClause",
			allow: "Apache-2.0",
		},
		{
			// The JSON.org licence: verbatim MIT plus a field-of-use restriction. Debian and the
			// FSF both classify it as non-free.
			name: "json_good_not_evil_on_mit",
			text: "Permission is hereby granted, free of charge, to any person obtaining a copy\n" +
				"of this software.\n\nThe Software shall be used for Good, not Evil.\n",
			deny:  "JSON-Evil",
			allow: "MIT",
		},
		{
			// BUSL's own Change License parameter names a permissive licence, so it reads as
			// permissive to anything looking for a grant.
			name: "busl_naming_mpl_as_its_change_licence",
			text: "Business Source License 1.1\n\nChange License: Mozilla Public License, version 2.0\n\n" +
				"The Business Source License (this document) is not an Open Source license.\n",
			deny:  "BUSL",
			allow: "MPL-2.0",
		},
		{
			// BSD-4-Clause: the same opening grant as BSD-2 and BSD-3, plus the advertising clause.
			name: "advertising_clause_on_a_bsd_grant",
			text: "Redistribution and use in source and binary forms, with or without\n" +
				"modification, are permitted provided that the following conditions are met:\n\n" +
				"3. All advertising materials mentioning features or use of this software must\n" +
				"   display the following acknowledgement.\n",
			deny:  licence.BSD4Clause,
			allow: "BSD",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			c := licence.Classify(tc.text)

			require.Containsf(t, c.Denied, tc.deny,
				"the rider must be denied — this is the shape a first-match classifier waves "+
					"through on its permissive base\n%+v", c)
			require.Containsf(t, c.Recognised, tc.allow,
				"the permissive base must ALSO have been recognised. If it was not, the denial "+
					"above may be an accident of pattern order rather than every pattern being "+
					"evaluated, and the next rider on a base whose pattern sorts first would "+
					"escape.\n%+v", c)
			require.False(t, c.Unidentified(), "a classified file is not unidentified\n%+v", c)
		})
	}
}

// TestHits_CollectEveryMatchWithinATable_NotJustTheFirst is the other half of rule 1, and the half
// the rider cases above cannot see.
//
// Those cases match one deny pattern and one allow pattern, so they hold even for a classifier that
// stops at the first match within each table — verified by mutation: adding a `break` to the match
// loop leaves every one of them green. What the break actually breaks is a file that matches TWO
// patterns from the SAME table, and one of those is a silent pass rather than a cosmetic one:
//
//   - two denials, and the violation names only the first. Cosmetic — any denial fails the module —
//     but the maintainer is told about one of the two licences they have to deal with.
//   - two recognised licences, and only the first is reported. NOT cosmetic. Unvetted() decides
//     whether a recognised licence is on the allowlist, so a module whose text matches MIT (one
//     sentence, which most licences contain) and then an unvetted licence would clear on the MIT
//     half and the second grant would never be examined. That is exactly the silent admission the
//     explicit allowlist exists to prevent.
func TestHits_CollectEveryMatchWithinATable_NotJustTheFirst(t *testing.T) {
	t.Parallel()

	// The concatenated-notices shape: one file, two grants.
	dualCopyleft := licence.Normalise("GNU AFFERO GENERAL PUBLIC LICENSE\nVersion 3\n\n" +
		"and, for the vendored parts, the GNU LESSER GENERAL PUBLIC LICENSE Version 2.1\n")

	require.Subset(t, licence.DenyHits(dualCopyleft), []string{"AGPL", "LGPL"},
		"both denied families must be named. A classifier that stops at the first match reports one "+
			"licence to a maintainer who has two to deal with.")

	dualPermissive := licence.Normalise("MIT License\n\n" +
		"Permission is hereby granted, free of charge, to any person obtaining a copy.\n\n" +
		"Portions are under the Mozilla Public License, version 2.0.\n")

	hits := licence.AllowHits(dualPermissive)
	require.Subset(t, hits, []string{"MIT", "MPL-2.0"},
		"EVERY recognised licence must be collected, not the first. Unvetted() checks the collected "+
			"set against the allowlist, so a licence hidden behind an earlier match is a licence "+
			"nobody decided about being admitted silently — the precise failure the allowlist exists "+
			"to prevent.\nhits: %v", hits)
}

// TestClassify_MPL2_IsNotDeniedByItsOwnCrossReference is rule 2 of the package doc.
//
// MPL-2.0 §1.12 names the GPL, the LGPL and the AGPL. Applying the denylist to the raw text fires on
// every MPL-2.0 module, including github.com/hashicorp/golang-lru/v2, which is in this repo's
// `go list -m all` today.
func TestClassify_MPL2_IsNotDeniedByItsOwnCrossReference(t *testing.T) {
	t.Parallel()

	const mpl2 = `Mozilla Public License Version 2.0
==================================

1. Definitions
--------------

1.12. "Secondary License"
    means either the GNU General Public License, Version 2.0, the GNU
    Lesser General Public License, Version 2.1, the GNU Affero General
    Public License, Version 3.0, or any later versions of those licenses.
`

	c := licence.Classify(mpl2)

	require.Empty(t, c.Denied,
		"MPL-2.0's own §1.12 names the GNU licences; the gate must strip that sentence before the "+
			"denylist runs, or every MPL-2.0 dependency is a false LIC001\n%+v", c)
	require.Contains(t, c.Recognised, "MPL-2.0", "%+v", c)
	require.Equal(t, "MPL-2.0", c.Verdict(), "%+v", c)
}

// TestNormalise_SecondaryLicenceStrip_DoesNotSwallowAGrantAfterIt is the other half of rule 2, and
// the regression that made the strip an exact literal.
//
// An earlier implementation matched a greedy span from "Secondary License" to the last "licenses."
// and deleted the GPL title and the operative grant along with it, leaving the denylist nothing to
// find. This is the fixture shape from test/repo, asserted directly against the normaliser.
func TestNormalise_SecondaryLicenceStrip_DoesNotSwallowAGrantAfterIt(t *testing.T) {
	t.Parallel()

	const text = `Notice. "Secondary License" means either the GNU General Public
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

	normalised := licence.Normalise(text)

	require.NotContains(t, normalised, `"Secondary License" means either`,
		"the §1.12 sentence must be removed — it is why MPL-2.0 modules would otherwise be LIC001")
	require.Contains(t, normalised, "Some intervening text.",
		"the strip took out everything between the cross-reference and the last 'licenses.' — that "+
			"is the greedy-span bug, and it deletes the GPL grant with it")
	require.Contains(t, licence.DenyHits(normalised), "GPL",
		"a GPL grant AFTER the Secondary License definition must still be denied")
}

// TestNormalise_CollapsesWrappedText covers why normalisation exists at all: every phrase the
// patterns match is long enough to be wrapped across lines in a real licence file.
func TestNormalise_CollapsesWrappedText(t *testing.T) {
	t.Parallel()

	wrapped := "Permission is hereby\n\tgranted, free\r\n   of charge\n"

	require.Contains(t, licence.Normalise(wrapped), "Permission is hereby granted, free of charge")
	require.Contains(t, licence.AllowHits(licence.Normalise(wrapped)), "MIT",
		"a grant wrapped across lines is the normal case, not the exception")
}

// TestNormalise_NonUTF8Text_IsStillClassified covers the licence file that killed the shell gate on
// a laptop.
//
// An older European project ships a Latin-1 LICENSE, and BSD sed under a UTF-8 locale rejected the
// invalid multibyte sequence outright: the run aborted mid-loop with "RE error: illegal byte
// sequence", no rule id, no module name and every remaining module unexamined — while the ubuntu
// runner stayed green. Go's regexp treats the byte as U+FFFD and matches on, so the split cannot
// happen; this test is what says the replacement kept that promise.
func TestNormalise_NonUTF8Text_IsStillClassified(t *testing.T) {
	t.Parallel()

	// 0xE9 is é in Latin-1 and an invalid UTF-8 sequence on its own.
	latin1 := "Copyright (c) 2026 Cl\xe9ment Example\n\nGNU GENERAL PUBLIC LICENSE\nVersion 3\n"

	require.False(t, strings.ToValidUTF8(latin1, "") == latin1, "the fixture must be invalid UTF-8")
	require.Contains(t, licence.DenyHits(licence.Normalise(latin1)), "GPL",
		"a licence file in a legacy encoding must be classified, not abort the gate")
}

// TestClassify_UnrecognisableText_IsUnidentified pins the fail-closed decision at the classifier
// level: text that matches nothing is not permissive by default.
func TestClassify_UnrecognisableText_IsUnidentified(t *testing.T) {
	t.Parallel()

	c := licence.Classify("Terms of Use\n\nYou may do whatever you feel is reasonable, within reason.\n")

	require.Empty(t, c.Denied, "%+v", c)
	require.Empty(t, c.Recognised, "%+v", c)
	require.True(t, c.Unidentified(),
		"an unclassifiable licence must be LIC002, never a silent pass\n%+v", c)
	require.Empty(t, c.Verdict(), "%+v", c)
}

// TestDenyHitsAuxiliary_DropsOnlyTheAdvertisingClause covers the one deliberate difference between
// the primary and auxiliary deny scans.
//
// LIC003 applies the denylist to NOTICE and third-party files, where the OpenSSL advertising text is
// routinely reproduced. Firing on that would make the gate red on a legitimate dependency with no
// waiver mechanism available, so BSD-4-Clause is dropped there — and ONLY there. It still fires as
// LIC001 when it is the module's own grant, which is where the decision belongs.
func TestDenyHitsAuxiliary_DropsOnlyTheAdvertisingClause(t *testing.T) {
	t.Parallel()

	notice := licence.Normalise("This product includes software developed by the OpenSSL Project.\n" +
		"All advertising materials mentioning features or use of this software\n" +
		"must display the following acknowledgment.\n")

	require.Contains(t, licence.DenyHits(notice), licence.BSD4Clause,
		"the primary scan must still see the advertising clause — that is LIC001 on a module's own grant")
	require.Empty(t, licence.DenyHitsAuxiliary(notice),
		"a NOTICE reproducing the OpenSSL advertising clause must not fire LIC003")

	embedded := licence.Normalise("This package embeds transpiled C sources from:\n" +
		"- musl — MIT License\n- readline — GNU GENERAL PUBLIC LICENSE, Version 3\n")

	require.Contains(t, licence.DenyHitsAuxiliary(embedded), "GPL",
		"a bibliography admitting embedded GPL code is exactly what LIC003 is for")
}

// TestDenyHits_NoticeProse_DoesNotFalselyFire is the false-positive control, at the level where the
// patterns were tightened.
//
// LIC003 applies the denylist to NOTICE files as an unanchored match, so ordinary prose that merely
// names a licence can trip it. A gate that goes red on a legitimate NOTICE gets commented out on a
// raid night. These are the shapes that were verified to false-positive before the patterns were
// tightened.
func TestDenyHits_NoticeProse_DoesNotFalselyFire(t *testing.T) {
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
			name:   "share_alike_as_two_words",
			notice: "Contributors are asked to share alike where practical.\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			require.Empty(t, licence.DenyHits(licence.Normalise(tc.notice)),
				"prose that merely mentions a licence must not be a denial\ntext: %q", tc.notice)
		})
	}
}

// TestAllowlistAndClassifier_DoNotDrift asserts every licence the classifier can recognise has an
// explicit decision recorded against it, and vice versa.
//
// This is the gate that used to regex allow_hits and `allowed` out of the shell script. Asking the
// package for its own tables is the same assertion with the parsing removed — and it now runs in
// both directions, which the regex version could not: an allowlist entry with no pattern is an id
// that can never be produced, i.e. a decision recorded about a licence the gate cannot see.
//
// The promise being kept is the one the allowlist exists for: adding a pattern for a licence nobody
// decided about would otherwise silently admit it, because a module whose text also matches the MIT
// or BSD pattern would clear on that half.
func TestAllowlistAndClassifier_DoNotDrift(t *testing.T) {
	t.Parallel()

	patterns := licence.AllowPatterns()
	require.NotEmpty(t, patterns, "the allow table is empty — the classifier can recognise nothing")

	allowed := licence.AllowedIDs()
	require.NotEmpty(t, allowed, "the allowlist is empty")

	recognisable := make(map[string]bool, len(patterns))

	for _, p := range patterns {
		recognisable[p.ID] = true

		require.Truef(t, licence.Allowed(p.ID),
			"the classifier can identify a licence as %q, but the allowlist does not name it. Either "+
				"add it to allowedIDs as a deliberate licence decision, or move it to the denylist — "+
				"an id in neither list is a licence the gate recognises and nobody decided about.", p.ID)
	}

	for _, id := range allowed {
		require.Truef(t, recognisable[id],
			"the allowlist permits %q, but no pattern can ever produce that id. A decision recorded "+
				"about a licence the classifier cannot recognise reads as coverage and is not.", id)
	}
}

// TestDenyAndAllowTables_ShareNoIDs keeps the two tables from disagreeing about a single id.
//
// An id in both would make a module's verdict depend on which list was consulted, and the whole
// any-denial-wins posture rests on the two being disjoint.
func TestDenyAndAllowTables_ShareNoIDs(t *testing.T) {
	t.Parallel()

	denied := make(map[string]bool)

	for _, p := range licence.DenyPatterns() {
		require.Falsef(t, denied[p.ID], "the denylist declares %q twice", p.ID)
		denied[p.ID] = true
	}

	for _, p := range licence.AllowPatterns() {
		require.Falsef(t, denied[p.ID],
			"%q is in BOTH the deny and allow tables — a licence cannot be both, and which one wins "+
				"would depend on the order the gate happens to consult them in", p.ID)
	}
}

// TestClassification_Unvetted_ReportsEveryRecognisedButUnallowedLicence covers the case no fixture
// can reach today, because every pattern the classifier carries is currently allowlisted.
//
// It is the guard for the day someone teaches the classifier a licence and forgets the decision:
// EVERY recognised licence must clear, not merely one of them. Stopping at the first id that passes
// would mean "at least one recognised licence is allowed", which is the opposite of how a denial or
// an unidentifiable file is treated.
func TestClassification_Unvetted_ReportsEveryRecognisedButUnallowedLicence(t *testing.T) {
	t.Parallel()

	c := licence.Classification{Recognised: []string{"MIT", "SomeoneAddedThis", "BSD", "AndThis"}}

	require.Equal(t, []string{"SomeoneAddedThis", "AndThis"}, c.Unvetted(),
		"an id the classifier can emit but the allowlist does not name must be reported, even when "+
			"a recognised-and-allowed licence sits beside it")
	require.Equal(t, "MIT", c.Verdict(),
		"the verdict is the first ALLOWED licence, so the gate can still name what it cleared under")
}

// TestJSExpressionAllowed_RequiresEveryTokenInTheExpression covers the SPDX-expression rule for the
// JavaScript half.
//
// `pnpm licenses list --json` keys a compound expression like "(MIT OR CC0-1.0)" as its own id. For
// an AND, requiring both is exactly right — both grants bind. For an OR the recipient could pick one
// branch, so requiring all is STRICTER than SPDX demands: a deliberate fail-closed choice that can
// only over-deny (forcing a human to review), never admit a copyleft branch.
func TestJSExpressionAllowed_RequiresEveryTokenInTheExpression(t *testing.T) {
	t.Parallel()

	cases := []struct {
		expr    string
		allowed bool
	}{
		{expr: "MIT", allowed: true},
		{expr: "BSD-3-Clause", allowed: true},
		{expr: "(MIT OR CC0-1.0)", allowed: true},
		{expr: "(MIT AND Apache-2.0)", allowed: true},
		{expr: "Python-2.0", allowed: true},
		{expr: "CC-BY-4.0", allowed: true},

		{expr: "GPL-3.0", allowed: false},
		{expr: "(MIT OR GPL-3.0)", allowed: false},
		{expr: "(MIT AND GPL-3.0)", allowed: false},
		{expr: "GPL-2.0 WITH Classpath-exception-2.0", allowed: false},
		{expr: "CC-BY-NC-4.0", allowed: false},
		{expr: "SEE LICENSE IN LICENSE.md", allowed: false},
	}

	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			t.Parallel()

			require.Equalf(t, tc.allowed, licence.JSExpressionAllowed(tc.expr),
				"SPDX expression %q", tc.expr)
		})
	}
}

// TestJSAllowlist_IsTheGoAllowlistPlusTheReviewedExtras keeps the two allowlists from drifting into
// unrelated policies.
//
// The JS half deliberately permits more than the Go half — BSD's SPDX spellings, plus Python-2.0
// (argparse) and CC-BY-4.0 (caniuse-lite's data), both reviewed when web/ landed. What it must never
// do is permit LESS, which would mean a licence this project has decided about being denied on one
// side of the tree and allowed on the other.
func TestJSAllowlist_IsTheGoAllowlistPlusTheReviewedExtras(t *testing.T) {
	t.Parallel()

	for _, id := range licence.AllowedIDs() {
		require.Truef(t, licence.JSAllowed(id),
			"%q is on the Go allowlist but not the JS one. The two may differ — the JS half carries "+
				"reviewed extras — but a licence decided about for the Go graph cannot be a denial "+
				"for the JS graph.", id)
	}

	for _, extra := range []string{"BSD-2-Clause", "BSD-3-Clause", "0BSD", "Python-2.0", "CC-BY-4.0"} {
		require.Truef(t, licence.JSAllowed(extra), "the reviewed JS addition %q must stay allowed", extra)
	}

	for _, denied := range []string{"GPL-3.0", "AGPL-3.0", "LGPL-2.1", "CC-BY-NC-4.0", "BUSL-1.1", "SSPL-1.0"} {
		require.Falsef(t, licence.JSAllowed(denied),
			"%q must not be on the JS allowlist — the default is DENY and a copyleft id is never a "+
				"silent pass", denied)
	}
}
