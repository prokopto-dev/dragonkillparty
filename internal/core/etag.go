package core

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash"
	"strconv"
)

// The strong-ETag computation, in one place, so the next mutable resource does not hand-roll a
// second one.
//
// PR 5a's guild ETag was the first, and it worked out the three properties that matter. This is the
// same computation with the resource-specific field list left to the caller — it is deliberately NOT
// a reflection over a struct, because which fields a representation is made of is a decision the
// resource owns and a hash that silently followed a struct would change every time somebody added an
// unrelated internal field.

// etagDigestBytes is how much of the SHA-256 digest reaches the header. 128 bits is far more than
// enough to make an accidental collision impossible between two representations of one resource, and
// a shorter tag keeps a header a bot sends back on every write small.
const etagDigestBytes = 16

// ETagBuilder accumulates a representation's fields into a strong entity tag.
//
// LENGTH-PREFIXED, NOT CONCATENATED, and that is the property the whole type exists to hold. Without
// the prefixes a Name of "ab" beside a Tag of "c" hashes identically to a Name of "a" beside a Tag
// of "bc" — two distinct representations sharing an ETag, which makes If-Match answer "unchanged"
// about a resource that changed. Every method below writes a length before its bytes, so a field
// boundary is part of the digest.
//
// USE EVERY FIELD OF THE REPRESENTATION, including its updated_at. A validator that omits a field is
// a validator that reports "unchanged" after that field changed, which is a lost update; including
// the timestamp additionally means a successful PATCH always yields a DIFFERENT tag than the one the
// caller sent, which is what makes "the write landed" assertable at all.
//
// The zero value is not usable; call NewETagBuilder. A builder is single-use: ETag reads the digest
// and the builder is not reset, because a reused one would silently produce a tag for the
// concatenation of two representations.
type ETagBuilder struct {
	h hash.Hash
}

// NewETagBuilder returns a builder over a fresh SHA-256.
func NewETagBuilder() *ETagBuilder {
	return &ETagBuilder{h: sha256.New()}
}

// Bytes appends one raw field.
//
// The write errors are waived rather than returned, and this is the case .claude/rules/go-idioms.md
// names: hash.Hash documents that Write never returns an error, so there is no failure to handle and
// no caller to return one to. Threading an error through every field of every representation would
// buy nothing and would make the call sites unreadable.
func (b *ETagBuilder) Bytes(field []byte) *ETagBuilder {
	var lenBuf [8]byte

	binary.BigEndian.PutUint64(lenBuf[:], uint64(len(field)))

	_, _ = b.h.Write(lenBuf[:])
	_, _ = b.h.Write(field)

	return b
}

// Text appends one string field.
func (b *ETagBuilder) Text(field string) *ETagBuilder {
	return b.Bytes([]byte(field))
}

// Int appends one integer field, big-endian and fixed-width, so 1 and 10 cannot share an encoding
// with a neighbouring field's first digit.
func (b *ETagBuilder) Int(field int64) *ETagBuilder {
	var buf [8]byte

	binary.BigEndian.PutUint64(buf[:], uint64(field))

	return b.Bytes(buf[:])
}

// Bool appends one boolean field, as the 0/1 an INTEGER boolean column stores under STRICT.
func (b *ETagBuilder) Bool(field bool) *ETagBuilder {
	if field {
		return b.Int(1)
	}

	return b.Int(0)
}

// OptionalInt appends a nullable integer.
//
// NULL GETS ITS OWN SENTINEL rather than collapsing to zero, because for the columns that are
// nullable in this schema the difference is the whole meaning: guild.inactive_after_days NULL is
// "the inactivity sweep is off" and 0 is "flag after zero days". A tag that could not tell them
// apart would report "unchanged" across the write that turned the sweep off.
func (b *ETagBuilder) OptionalInt(field *int64) *ETagBuilder {
	if field == nil {
		return b.Text("null")
	}

	return b.Text(strconv.FormatInt(*field, 10))
}

// ETag returns the strong validator: the digest's first 16 bytes as a quoted hex string.
//
// STRONG, so there is no `W/` prefix (RFC 9110 §8.8.1, canonical §7). The hex digest contains only
// [0-9a-f], so it needs no escaping and the quotes are literal — but they are REQUIRED: an unquoted
// entity tag is not an entity tag, and a client comparing it against a quoted If-Match would never
// match.
func (b *ETagBuilder) ETag() string {
	sum := b.h.Sum(nil)

	return fmt.Sprintf("%q", hex.EncodeToString(sum[:etagDigestBytes]))
}
