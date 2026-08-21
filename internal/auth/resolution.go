package auth

import (
	"github.com/prokopto-dev/dragonkillparty/internal/core"
)

// ResolutionError is a credential that was presented and did not resolve, carrying the facts the
// published error catalogue promises the caller.
//
// WHY A TYPE AND NOT JUST A SENTINEL. docs/api/errors.md is the contract, and it is specific: a
// revoked token answers `token_revoked` with `meta.revoked_at` and `meta.token_prefix`, an expired
// one answers `token_expired` with `meta.expired_at`, and an unknown prefix or a failed MAC answers
// `token_invalid`. A bot author reading `token_revoked` stops; one reading a bare 401 retries
// forever, which is what "retrying a revoked token looks like an attack" in that table is about. The
// sentinel says what happened and this says which credential it happened to.
//
// A SESSION'S FAILURES DO NOT GET THEIR OWN CODES, and that is the catalogue's decision rather than
// an omission here: every one of them answers `unauthenticated`, because the SPA's response to all
// of them is identical (send the user to the login screen) and because a browser session — unlike a
// token pasted into a bot config six months ago — has a human present who can simply sign in again.
// The distinctions still exist in the LOG, where the sentinels are.
//
// It is never constructed outside this package: the middleware reads it with errors.As and the
// sentinel with errors.Is.
type ResolutionError struct {
	// Credential is which class was presented. Empty when the request presented something this
	// package could not classify at all.
	Credential Credential

	// TokenPrefix is the public 8-character prefix of the token that failed, empty for a session and
	// for a bearer that never parsed. IT IS THE ONLY PART OF A TOKEN THAT MAY BE LOGGED OR RETURNED
	// (.claude/rules/go-idioms.md), and it is what `dkp token revoke <prefix>` and the token list
	// name.
	TokenPrefix string

	// At is the instant the reason names — revoked_at for a revoked token, expires_at for an expired
	// one — and nil for a reason that has no instant.
	At *core.Micros

	// err is the wrapped chain, already carrying one of the package sentinels. Unexported so the
	// only way to ask what went wrong is errors.Is, which keeps a caller from switching on a string.
	err error
}

// Error satisfies error, and reports the whole wrapped chain: the context, then the sentinel.
func (e *ResolutionError) Error() string { return e.err.Error() }

// Unwrap exposes the chain, so errors.Is(err, ErrRevokedCredential) still answers through this type.
func (e *ResolutionError) Unwrap() error { return e.err }

// tokenFailure wraps a bearer failure with what the caller may be told about it.
//
// The prefix and instant are captured by the caller as it learns them, so a failure before the parse
// carries neither and one after revocation carries both — which is exactly the difference between
// `unauthenticated` and `token_revoked` with a `meta.revoked_at` a bot author can act on.
func tokenFailure(prefix string, at *core.Micros, err error) error {
	return &ResolutionError{Credential: CredentialToken, TokenPrefix: prefix, At: at, err: err}
}

// sessionFailure wraps a cookie failure. It carries no prefix and no instant: every session failure
// is one code on the wire, and the detail lives in the log.
func sessionFailure(err error) error {
	return &ResolutionError{Credential: CredentialSession, err: err}
}

// micros returns a pointer to the Micros value of a nullable database column, or nil.
func micros(v *int64) *core.Micros {
	if v == nil {
		return nil
	}

	m := core.Micros(*v)

	return &m
}
