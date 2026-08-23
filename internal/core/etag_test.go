package core_test

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/core"
)

// The strong-validator computation's own tests. The properties are the three an If-Match depends on:
// a tag is deterministic, it changes when any field changes, and two different field LAYOUTS that
// share a byte stream do not share a tag.

// TestETagBuilder_SameFields_ProduceTheSameTag is determinism. Without it, every PATCH's If-Match
// would fail against a resource nobody touched.
func TestETagBuilder_SameFields_ProduceTheSameTag(t *testing.T) {
	t.Parallel()

	build := func() string {
		return core.NewETagBuilder().Text("Kittens Who Say Ni").Int(7).Bool(true).ETag()
	}

	require.Equal(t, build(), build())
}

// TestETagBuilder_IsAStrongValidator pins the wire shape: a quoted hex string with no `W/` prefix
// (RFC 9110 §8.8.1, canonical §7).
//
// THE QUOTES ARE NOT DECORATION. An unquoted entity tag is not an entity tag, and a client comparing
// the value it stored against a quoted If-Match would never match — so a "tidying" that dropped them
// would disable every precondition in the product without failing anything else.
func TestETagBuilder_IsAStrongValidator(t *testing.T) {
	t.Parallel()

	tag := core.NewETagBuilder().Text("anything").ETag()

	require.Regexp(t, regexp.MustCompile(`^"[0-9a-f]{32}"$`), tag)
	require.NotContains(t, tag, "W/", "a weak validator cannot be used with If-Match")
}

// TestETagBuilder_EveryFieldKind_ChangesTheTag is the field-sensitivity property, one accessor at a
// time. A field that does not reach the digest is a field an If-Match cannot detect a change in,
// which is a lost update.
func TestETagBuilder_EveryFieldKind_ChangesTheTag(t *testing.T) {
	t.Parallel()

	seven := int64(7)
	eight := int64(8)
	zero := int64(0)

	tests := []struct {
		name string
		a    func(*core.ETagBuilder) *core.ETagBuilder
		b    func(*core.ETagBuilder) *core.ETagBuilder
	}{
		{
			name: "text",
			a:    func(e *core.ETagBuilder) *core.ETagBuilder { return e.Text("KWSN") },
			b:    func(e *core.ETagBuilder) *core.ETagBuilder { return e.Text("kwsn") },
		},
		{
			name: "bytes",
			a:    func(e *core.ETagBuilder) *core.ETagBuilder { return e.Bytes([]byte{1, 2}) },
			b:    func(e *core.ETagBuilder) *core.ETagBuilder { return e.Bytes([]byte{2, 1}) },
		},
		{
			name: "int",
			a:    func(e *core.ETagBuilder) *core.ETagBuilder { return e.Int(seven) },
			b:    func(e *core.ETagBuilder) *core.ETagBuilder { return e.Int(eight) },
		},
		{
			name: "bool",
			a:    func(e *core.ETagBuilder) *core.ETagBuilder { return e.Bool(true) },
			b:    func(e *core.ETagBuilder) *core.ETagBuilder { return e.Bool(false) },
		},
		{
			// The case the sentinel exists for: guild.inactive_after_days NULL means "the sweep is
			// off" and 0 means "flag after zero days". A tag that collapsed them would report
			// "unchanged" across the write that turned the sweep off.
			name: "optional int, null versus zero",
			a:    func(e *core.ETagBuilder) *core.ETagBuilder { return e.OptionalInt(nil) },
			b:    func(e *core.ETagBuilder) *core.ETagBuilder { return e.OptionalInt(&zero) },
		},
		{
			name: "optional int, two values",
			a:    func(e *core.ETagBuilder) *core.ETagBuilder { return e.OptionalInt(&seven) },
			b:    func(e *core.ETagBuilder) *core.ETagBuilder { return e.OptionalInt(&eight) },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.NotEqual(t,
				tt.a(core.NewETagBuilder()).ETag(),
				tt.b(core.NewETagBuilder()).ETag(),
				"two different %s values produced one tag", tt.name)
		})
	}
}

// TestETagBuilder_FieldBoundaries_AreEncoded is the property the length prefix exists for, and the
// one a naive concatenation gets wrong.
//
// "ab" + "c" and "a" + "bc" are the same byte stream. Without a length before each field they hash
// identically, two distinct representations share an ETag, and If-Match reports "unchanged" about a
// resource that changed — the lost update the precondition exists to prevent.
func TestETagBuilder_FieldBoundaries_AreEncoded(t *testing.T) {
	t.Parallel()

	left := core.NewETagBuilder().Text("ab").Text("c").ETag()
	right := core.NewETagBuilder().Text("a").Text("bc").ETag()

	require.NotEqual(t, left, right,
		"field boundaries must be encoded, or two representations could share an ETag")
}

// TestETagBuilder_FieldOrder_IsPartOfTheTag. Two representations holding the same values in
// different fields are different representations.
func TestETagBuilder_FieldOrder_IsPartOfTheTag(t *testing.T) {
	t.Parallel()

	require.NotEqual(t,
		core.NewETagBuilder().Text("a").Text("b").ETag(),
		core.NewETagBuilder().Text("b").Text("a").ETag())
}

// TestETagBuilder_NoFields_IsStillATag. A representation with no fields is degenerate rather than an
// error, and it must not produce an empty header value: an empty ETag is a header a client would
// store and send back as an If-Match that matches everything.
func TestETagBuilder_NoFields_IsStillATag(t *testing.T) {
	t.Parallel()

	require.Regexp(t, regexp.MustCompile(`^"[0-9a-f]{32}"$`), core.NewETagBuilder().ETag())
}
