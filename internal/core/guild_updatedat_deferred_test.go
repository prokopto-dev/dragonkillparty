package core_test

import (
	"testing"
)

// TestMicros_GuildUpdatedAt_RoundTrips is the deferred acceptance criterion from PR 8:
// "guild.updated_at from PR 5a now round-trips through core.Micros end to end — the smallest
// possible proof that the type is adoptable rather than merely defined."
//
// It is SKIPPED, not faked. The proof needs the guild resource and its updated_at field, which are
// built by PR 5a (feat(api): guild settings resource) in parallel and are NOT on this branch's base.
// Writing it against a type that does not exist yet would either not compile or assert against a
// stand-in — both of which are worse than an honest, visible gap. This skipped test is the follow-up
// hook: when 5a merges and this branch rebases onto it, delete the Skip and implement the round trip
// (fetch the guild, read updated_at as a core.Micros, marshal it, and assert the RFC 3339 µs/Z wire
// form and an integer-microsecond store form), and it becomes the load-bearing adoption proof.
//
// Everything the round trip depends on already exists and is proven here: FromTime/Time,
// MarshalJSON/UnmarshalJSON at microsecond precision always ending in Z
// (TestMicros_RFC3339RoundTrip_AtMicrosecondPrecision,
// TestMicros_MarshalJSON_AlwaysZAndSixFractionalDigits). Only the wiring into guild.updated_at is
// deferred.
func TestMicros_GuildUpdatedAt_RoundTrips(t *testing.T) {
	t.Skip("deferred to the PR 5a rebase: guild.updated_at does not exist on this branch's base " +
		"(docs/development/first-ten-prs.md §PR 8). Remove this Skip and wire it once 5a merges.")
}
