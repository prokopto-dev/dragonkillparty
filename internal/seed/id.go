package seed

import "github.com/prokopto-dev/dragonkillparty/internal/core"

// crockford is the Crockford base32 alphabet a ULID is written in (canonical §3).
//
// Note what is NOT in it: I, L, O and U. They are excluded so that a human reading an id aloud, or
// typing one from a screenshot into a support ticket, cannot turn a 1 into an I or a 0 into an O.
// The omission is the whole reason a plain strconv.FormatInt(n, 32) will not do here — its alphabet
// is 0-9a-v, which contains all four, so roughly one id in eight it produces is not a ULID at all.
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// idCounterWidth is how many base32 digits the numeric tail carries: 32^7 ≈ 34 billion, which is
// more indices than any profile will ever ask for, and fixed-width so the tail sorts numerically.
const idCounterWidth = 7

// DeterministicID builds a genuinely valid 26-character ULID from a tag and an index.
//
// Two properties, both load-bearing and neither free:
//
//   - VALID. core.ULID(DeterministicID(...)).Valid() is true, so a seeded row can travel through a
//     `format:"ulid"` API field and through ulid.ParseStrict without an exception being carved for
//     seeded data. The tag must therefore be drawn from the Crockford alphabet above — "RAID" and
//     "ITEM" are not usable tags, because both contain an I.
//   - ORDER-PRESERVING. DeterministicID(tag, i) sorts before DeterministicID(tag, j) exactly when
//     i < j, because the numeric tail is zero-padded to a fixed width. ledger.Allocate's tiebreak is
//     account_id ASC, so a generator whose ids did not follow its indices would make the split
//     depend on the encoder rather than on the roster.
//
// The leading character is always '0', which keeps the encoded timestamp field at zero and well
// inside the overflow rule ParseStrict enforces. These are seeded ids, not minted ones: they carry
// no real creation time and pretend to none.
func DeterministicID(tag string, n int) core.ULID {
	buf := make([]byte, core.ULIDLength)
	for i := range buf {
		buf[i] = '0'
	}

	// The numeric tail, right-aligned, least-significant digit last.
	v := n
	for i := range idCounterWidth {
		buf[core.ULIDLength-1-i] = crockford[v%len(crockford)]
		v /= len(crockford)
	}

	// The tag, immediately before the tail, so an id reads as ...ACCT0000042.
	copy(buf[core.ULIDLength-idCounterWidth-len(tag):], tag)

	return core.ULID(buf)
}

// The id tags this package mints under. Each is checked against the Crockford alphabet by
// TestDeterministicID_EveryTag_IsValidULID, which is the test that would have caught "RAID".
const (
	tagAccount   = "ACCT"
	tagPerson    = "PRSN"
	tagCharacter = "CHAR"
	tagRaid      = "RAD"
	tagTick      = "TCK"
	tagItem      = "GEAR"
	tagAward     = "AWRD"
)
