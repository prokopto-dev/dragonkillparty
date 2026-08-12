package licence

import (
	"regexp"
	"slices"
)

// Pattern is one licence-identification rule: the id the gate reports, and the expression that
// recognises it. Every expression is case-insensitive and unanchored, matched against text that
// [Normalise] has already collapsed to a single line.
type Pattern struct {
	// ID is what a violation names — "GPL", "CommonsClause", "Apache-2.0". It is the id the
	// allowlist decides about, so it is API between deny, allow and [Allowed].
	ID string
	// Re recognises the licence. Written against the licence's title, its SPDX identifier or its
	// operative grant, because a short notice file may carry only one of the three.
	Re *regexp.Regexp
}

// mpl2SecondaryLicenseDefinition is MPL-2.0 §1.12, removed by [Normalise] before the denylist runs.
//
// The removal is an EXACT literal, not a span. A bounded-wildcard form such as
// `"Secondary License"[^"]*licen[cs]es\.` is greedy: in a file that names the Secondary License
// early and carries a real GPL grant later, it deletes everything between the two — the GNU title
// and the operative grant with it — and the denylist then sees nothing. Verified; it collapses such
// a file to its first sentence. Matching the canonical MPL-2.0 §1.12 sentence verbatim cannot
// over-reach. If a future MPL variant rewords it the strip stops matching and the gate fails closed
// on MPL, which is the safe direction, and TestLicenceGate_AllowedLicence_Passes/mpl2 says so.
var mpl2SecondaryLicenseDefinition = regexp.MustCompile(
	`"Secondary License" means either the GNU General Public License, Version 2\.0, the GNU ` +
		`Lesser General Public License, Version 2\.1, the GNU Affero General Public License, ` +
		`Version 3\.0, or any later versions of those licenses\.`)

// asciiSpace is the byte-wise whitespace class. ASCII only, deliberately: a dependency shipping a
// Latin-1 LICENSE (an ordinary thing for older European projects) must be classified rather than
// abort the run. The shell this replaced needed LC_ALL=C for the same reason — under a UTF-8 locale
// BSD sed rejects an invalid multibyte sequence outright, which failed closed on a developer laptop
// while the ubuntu runner stayed green, the worst possible split. Go's regexp treats an invalid
// byte as U+FFFD and matches on regardless, so the split cannot happen here at all.
var asciiSpace = regexp.MustCompile(`[\t\n\v\f\r ]+`)

// denyPatterns are the licences this project cannot ship. Order is presentation only: [Classify]
// evaluates all of them.
var denyPatterns = []Pattern{
	// Source-available and non-open-source. Matched by title: unlike the GNU names, these never
	// appear as a cross-reference inside a permissive licence. BUSL is the reason this block runs
	// over the raw title rather than relying on the permissive patterns not matching — BUSL's own
	// "Change License" parameter names the MPL (HashiCorp) or Apache-2.0 (most others), so it reads
	// as permissive to anything that looks for a grant.
	{ID: "BUSL", Re: regexp.MustCompile(`(?i)Business Source License`)},
	{ID: "SSPL", Re: regexp.MustCompile(`(?i)Server Side Public License`)},
	{ID: "Elastic", Re: regexp.MustCompile(`(?i)Elastic License`)},
	{ID: "FSL", Re: regexp.MustCompile(`(?i)Functional Source License`)},
	{ID: "PolyForm", Re: regexp.MustCompile(`(?i)PolyForm [A-Za-z-]+ License`)},

	// Copyleft. Title, SPDX identifier and operative grant are all matched: a short notice file may
	// carry only one of the three.
	{ID: "AGPL", Re: regexp.MustCompile(`(?i)GNU AFFERO GENERAL PUBLIC LICENSE|\bAGPL-[0-9]|\bAGPLv[0-9]`)},
	{ID: "LGPL", Re: regexp.MustCompile(`(?i)GNU LESSER GENERAL PUBLIC LICENSE|GNU LIBRARY GENERAL PUBLIC LICENSE|\bLGPL-[0-9]|\bLGPLv[0-9]`)},
	{ID: "GPL", Re: regexp.MustCompile(`(?i)GNU GENERAL PUBLIC LICENSE|\bGPL-[0-9]|\bGPLv[0-9]`)},
	{ID: "EPL", Re: regexp.MustCompile(`(?i)Eclipse Public License|\bEPL-[0-9]`)},
	// CDDL needs a version or the word License adjacent. A bare token match fires on ordinary prose
	// in a NOTICE file, and LIC003 applies this list to NOTICE files.
	{ID: "CDDL", Re: regexp.MustCompile(`(?i)Common Development and Distribution License|\bCDDL[- ]?[0-9]|\bCDDL License`)},

	// Creative Commons variants that are not CC0. NonCommercial and NoDerivatives are unusable;
	// ShareAlike is copyleft. None belongs on source we redistribute under Apache-2.0.
	//
	// Matched only as part of a licence name. A bare \bNonCommercial\b fires on any NOTICE that uses
	// the word in a sentence, and a gate that goes red on prose gets deleted rather than fixed.
	{ID: "CC-BY-NC", Re: regexp.MustCompile(`(?i)Attribution-NonCommercial|\bCC[ -]BY-NC`)},
	{ID: "CC-BY-SA", Re: regexp.MustCompile(`(?i)Attribution-ShareAlike|\bCC[ -]BY-SA`)},
	{ID: "CC-BY-ND", Re: regexp.MustCompile(`(?i)Attribution-NoDerivatives|\bCC[ -]BY-ND`)},

	// Restrictions layered onto an otherwise permissive grant. These are the riders rule 1 in the
	// package doc exists to catch, and each is matched on its operative phrase rather than on a
	// licence name.
	{ID: "CommonsClause", Re: regexp.MustCompile(`(?i)Commons Clause|right to Sell the Software`)},
	{ID: "JSON-Evil", Re: regexp.MustCompile(`(?i)shall be used for Good, not Evil`)},
	{ID: BSD4Clause, Re: regexp.MustCompile(`(?i)All advertising materials mentioning features or use of this software`)},
}

// BSD4Clause is the one denied id an auxiliary file does not fire on — see [DenyHitsAuxiliary].
const BSD4Clause = "BSD-4-Clause"

// allowPatterns are the licences this gate can recognise as permissive. Recognising one is NOT the
// same as permitting it: [Allowed] is the separate, explicit decision.
var allowPatterns = []Pattern{
	{ID: "Apache-2.0", Re: regexp.MustCompile(`(?i)Apache License.{0,80}Version 2\.0|\bApache-2\.0\b`)},
	{ID: "MPL-2.0", Re: regexp.MustCompile(`(?i)Mozilla Public License,? [Vv]ersion 2\.0|\bMPL-2\.0\b`)},
	{ID: "MIT", Re: regexp.MustCompile(`(?i)Permission is hereby granted, free of charge|\bMIT License\b`)},
	{ID: "ISC", Re: regexp.MustCompile(`(?i)Permission to use, copy, modify, and/or distribute this software`)},
	{ID: "BSD", Re: regexp.MustCompile(`(?i)Redistribution and use in source and binary forms`)},
	{ID: "CC0-1.0", Re: regexp.MustCompile(`(?i)CC0 1\.0 Universal|\bCC0-1\.0\b`)},
	{ID: "Unlicense", Re: regexp.MustCompile(`(?i)free and unencumbered software released into the public domain`)},
	{ID: "Zlib", Re: regexp.MustCompile(`(?i)This software is provided .as-is., without any express or implied warranty`)},
}

// allowedIDs is an explicit allowlist, deliberately not "anything not denied".
//
// The default must be STOP, not permit. Otherwise adding a pattern to allowPatterns for a licence
// nobody decided about — which is exactly what the LIC002 message invites the next engineer to do —
// silently admits it. Adding an id here is a licence decision and needs a human.
var allowedIDs = []string{"Apache-2.0", "MIT", "ISC", "BSD", "MPL-2.0", "CC0-1.0", "Unlicense", "Zlib"}

// DenyPatterns returns the denylist. The copy is what keeps the table read-only from outside the
// package while still letting the drift test iterate the real thing rather than a restatement of it.
func DenyPatterns() []Pattern { return slices.Clone(denyPatterns) }

// AllowPatterns returns the permissive-licence patterns, on the same terms as [DenyPatterns].
func AllowPatterns() []Pattern { return slices.Clone(allowPatterns) }

// AllowedIDs returns the explicit allowlist — the licences a human has decided this project ships.
func AllowedIDs() []string { return slices.Clone(allowedIDs) }

// Allowed reports whether id is on the explicit allowlist.
func Allowed(id string) bool { return slices.Contains(allowedIDs, id) }

// Normalise returns text as one line, with MPL-2.0's Secondary License definition removed.
//
// Newlines collapse to spaces because every phrase the patterns match is long enough to be wrapped
// across lines in a real licence file.
func Normalise(text string) string {
	oneLine := asciiSpace.ReplaceAllString(text, " ")

	return mpl2SecondaryLicenseDefinition.ReplaceAllString(oneLine, "")
}

// hits returns the ids of every pattern in table that matches text. EVERY pattern is evaluated —
// see rule 1 in the package doc; a first-match classifier cannot see a rider on a permissive base.
func hits(table []Pattern, text string) []string {
	var ids []string

	for _, p := range table {
		if p.Re.MatchString(text) {
			ids = append(ids, p.ID)
		}
	}

	return ids
}

// DenyHits returns the ids of every denied licence family text matches.
func DenyHits(text string) []string { return hits(denyPatterns, text) }

// AllowHits returns the ids of every allowed-licence pattern text matches.
func AllowHits(text string) []string { return hits(allowPatterns, text) }

// DenyHitsAuxiliary is DenyHits for a file that is NOT the module's own grant — a NOTICE, a
// LICENSE-3RD-PARTY.md, a second LICENSE-<something>.
//
// BSD-4-Clause is dropped. Its advertising clause is an attribution obligation, not a bar on
// shipping, and NOTICE files routinely reproduce the OpenSSL text that contains it. Firing LIC003 on
// that would make the gate red on a legitimate dependency, and there is no waiver mechanism yet for
// a maintainer to record a reviewed exception — so the only available response would be to delete
// the gate. It still fires as LIC001 when it is the module's own grant, which is where the decision
// actually belongs.
func DenyHitsAuxiliary(text string) []string {
	ids := DenyHits(text)

	return slices.DeleteFunc(ids, func(id string) bool { return id == BSD4Clause })
}

// Classification is what the gate concluded about one licence file.
type Classification struct {
	// Denied is every denied family the file matched. Non-empty is LIC001.
	Denied []string
	// Recognised is every permissive licence the file matched, allowlisted or not.
	Recognised []string
}

// Unidentified reports whether the file matched nothing at all — neither a denial nor a recognised
// grant. That is LIC002: fail closed, because a licence nobody can name is a licence nobody vetted.
func (c Classification) Unidentified() bool {
	return len(c.Denied) == 0 && len(c.Recognised) == 0
}

// Unvetted returns the recognised licences that are NOT on the explicit allowlist.
//
// EVERY recognised licence must clear, not merely one of them. Stopping at the first id that passes
// would mean "at least one recognised licence is allowed", which is the opposite of how Denied and
// Unidentified are treated. It also disarms the guard the allowlist exists to be: someone who
// teaches allowPatterns a licence nobody decided about would get a silent pass on any module whose
// text also matches the MIT or BSD pattern — and those patterns match a single sentence, so most
// modules match them.
func (c Classification) Unvetted() []string {
	var unvetted []string

	for _, id := range c.Recognised {
		if !Allowed(id) {
			unvetted = append(unvetted, id)
		}
	}

	return unvetted
}

// Verdict returns the first allowlisted licence recognised, or "" when none was.
func (c Classification) Verdict() string {
	for _, id := range c.Recognised {
		if Allowed(id) {
			return id
		}
	}

	return ""
}

// Classify runs the whole classifier over one licence file's raw text.
func Classify(text string) Classification {
	normalised := Normalise(text)

	return Classification{
		Denied:     DenyHits(normalised),
		Recognised: AllowHits(normalised),
	}
}
