package licence_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/licence"
)

// TestIsPrimaryGrant_SplitsTheGrantFromTheAuxiliaryFiles pins the second pure-text decision the gate
// makes, and the one that is wrong in both directions if it drifts.
//
// A module's primary LICENSE is its grant and is fully classified. Everything else beside it —
// LICENSE-GPL, LICENSE-3RD-PARTY.md, NOTICE, COPYRIGHT — is either a second grant or a bibliography
// of embedded code, and is deny-scanned by LIC003 instead. Which question a file gets asked is
// decided here:
//
//   - Too generous, and a bibliography is read as a grant: modernc.org/memory ships LICENSE-LOGO
//     containing nothing but a URL to a Wikimedia image page, which would become LIC002 and turn the
//     real tree red. A gate that is red on the current dependency set is a gate that gets deleted.
//   - Too strict, and a module hides its real copyleft grant in LICENSE-GPL beside a permissive
//     LICENSE — the standard dual-licence layout. A glob requiring a literal dot after LICENSE read
//     exactly one of the four files modernc.org/memory really ships.
func TestIsPrimaryGrant_SplitsTheGrantFromTheAuxiliaryFiles(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		primary bool
	}{
		// The module's own grant, in the spellings the real module cache holds.
		{name: "LICENSE", primary: true},
		{name: "LICENSE.md", primary: true},
		{name: "LICENSE.txt", primary: true}, // github.com/spf13/cobra ships this one
		{name: "license", primary: true},
		{name: "LICENCE", primary: true},
		{name: "COPYING", primary: true},
		{name: "COPYING.md", primary: true},
		{name: "UNLICENSE", primary: true},

		// Auxiliary: a second grant, or a bibliography. Never the module's own licence.
		{name: "LICENSE-GPL", primary: false},
		{name: "LICENSE-AGPL", primary: false},
		{name: "LICENSE_GPL", primary: false},
		{name: "LICENSE-GO", primary: false},
		{name: "LICENSE-MMAP-GO", primary: false},
		{name: "LICENSE-LOGO", primary: false},
		{name: "LICENSE-3RD-PARTY.md", primary: false},
		{name: "LICENSE.APACHE2.txt", primary: false}, // two extensions is not <NAME>.<ext>
		{name: "COPYING.THIRD-PARTY", primary: false},
		{name: "COPYRIGHT", primary: false},
		{name: "NOTICE", primary: false},
		{name: "NOTICE.txt", primary: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			require.Equalf(t, tc.primary, licence.IsPrimaryGrant(tc.name),
				"%q classified as the wrong kind of licence file", tc.name)
		})
	}
}
