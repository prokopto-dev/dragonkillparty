package api

import (
	"net/http"
	"strings"
)

// This file is the HTTP side of optimistic concurrency: the If-Match precondition, and the 428/412
// problem bodies that go with it. The ETag VALUE is computed in the domain (guild.ETagOf) because it
// is a function of the representation; this file only decides what to do when a request's If-Match is
// absent (428) or stale (412), which are transport concerns.
//
// The 428 is hand-rolled rather than expressed as a required header tag, and the reason is a Huma
// behaviour that would otherwise turn a correct-looking struct into a wrong status. See
// requireIfMatch.

// requireIfMatch returns the caller's If-Match, or a 428 precondition_required problem when it is
// absent.
//
// The If-Match header parameter MUST be declared optional on the input struct — NOT
// `required:"true"`. Huma v2.39.1 raises a missing required parameter as 422 (huma.go:899,980,
// errStatus initialised to http.StatusUnprocessableEntity), so a `required:"true"` If-Match would
// answer a missing precondition with 422 validation_failed instead of the 428 precondition_required
// that canonical §7 and this PR's acceptance criteria require. The parameter is therefore optional
// and the presence check lives here, where it can return the right status. A test asserts the status
// AND the code, because a 422 would otherwise look like a passing negative test.
//
// An If-Match of "*" is deliberately NOT special-cased. RFC 9110 gives "*" the meaning "the resource
// exists"; the guild singleton always exists after migration, so "*" would always match and silently
// disable the concurrency check a PATCH exists to enforce. Treating it as an ordinary (non-matching)
// value makes a caller sending "*" get a clean 412 rather than a lost update. If a use for "*"
// appears, it is added deliberately with its own test.
func requireIfMatch(ifMatch string) (string, *ProblemDetail) {
	if strings.TrimSpace(ifMatch) == "" {
		return "", NewProblem(http.StatusPreconditionRequired, CodePreconditionRequired,
			"If-Match is required on a PATCH of this resource. Send the ETag from a prior GET so a "+
				"concurrent edit is detected rather than silently overwritten.")
	}

	return ifMatch, nil
}

// preconditionFailed builds the 412 body carrying the current representation and its ETag, so a bot
// merges its change onto the current state in one round trip (canonical §7,
// .claude/rules/api-endpoints.md). `current` is the wire DTO of the current resource and
// `currentETag` is its strong ETag.
//
// meta.current and meta.current_etag are the exact keys the rule file and the decision record name;
// a bot reads meta.current, applies its patch, and retries with meta.current_etag as the new
// If-Match.
func preconditionFailed(current any, currentETag string) *ProblemDetail {
	p := NewProblem(http.StatusPreconditionFailed, CodePreconditionFailed,
		"If-Match does not match the current ETag. The resource changed since you last read it; "+
			"merge your change onto meta.current and retry with meta.current_etag.")
	p.Meta = map[string]any{
		"current":      current,
		"current_etag": currentETag,
	}

	return p
}
