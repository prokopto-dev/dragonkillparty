package auth

import "errors"

// The resolution sentinels. EVERY ONE OF THEM IS THE SAME 401 TO THE CALLER — the middleware maps
// the whole set to one `unauthorized` problem with one detail string — and a different line in the
// server log. That asymmetry is the design:
//
//   - The caller must learn nothing. "Expired" versus "revoked" versus "no such token" tells an
//     attacker holding a stolen credential which of their credentials are still worth trying, and
//     tells a scanner that a prefix it guessed exists.
//   - The operator must learn everything. "Was this token used after we revoked it, and when" is the
//     first question of every incident, and a log that says only "401" cannot answer it.
//
// They are compared with errors.Is, never ==, so a wrapped error from the store still matches
// (.claude/rules/go-idioms.md).
var (
	// ErrNoCredential means the request carried neither an Authorization header nor a session
	// cookie. It is NOT a failure on a public operation — the middleware proceeds with no principal
	// — which is why it is a distinct sentinel rather than folded into ErrInvalidCredential.
	ErrNoCredential = errors.New("no credential presented")

	// ErrMalformedCredential means something was presented and it was not a credential this product
	// issues: an Authorization scheme that is not Bearer, a bearer that is not `dkp_pat_…`, a prefix
	// or secret of the wrong length, a cookie that is not base64url.
	//
	// It never reaches the database. A malformed credential is refused before the lookup, so a
	// scanner cannot use the auth path as a query generator.
	ErrMalformedCredential = errors.New("malformed credential")

	// ErrTokenInQueryString means a token-shaped query parameter was present. ADR-0011 and §6.3:
	// transport is `Authorization: Bearer dkp_pat_…` only, and a query-string token is rejected with
	// a 401 that explains itself, because that is what fifteen years of EQdkp bots send and a silent
	// 401 would read as "my token is wrong" rather than "move it to a header".
	//
	// The sole documented exception is the compat shim's `?atoken=` (ADR-0013), which is not part of
	// this API and does not route through this package.
	ErrTokenInQueryString = errors.New("token in query string")

	// ErrUnknownCredential means the lookup found no row: a prefix nobody minted, a session hash
	// nobody holds, or a token whose secret did not survive the constant-time compare. The three are
	// ONE sentinel deliberately — distinguishing "no such prefix" from "wrong secret" is the oracle
	// that turns a prefix guess into a confirmation.
	ErrUnknownCredential = errors.New("unknown credential")

	// ErrExpiredCredential means the row exists and its time is up: a session past its idle or
	// absolute expiry, or a token past expires_at.
	ErrExpiredCredential = errors.New("expired credential")

	// ErrRevokedCredential means the row exists and revoked_at is set. Revocation is instantaneous
	// precisely because this check reads a row the auth path was already reading (ADR-0011).
	ErrRevokedCredential = errors.New("revoked credential")

	// ErrStaleSessionEpoch means the session was minted under an older app_user.session_epoch: the
	// user signed out everywhere, changed their password, disabled MFA, unlinked an identity, or had
	// their roles changed. It is separate from ErrRevokedCredential because it is the one failure
	// that can arrive in bulk, and an officer looking at a hundred of them needs to see WHY.
	ErrStaleSessionEpoch = errors.New("stale session epoch")

	// ErrPrincipalNotActive means the credential is fine and the identity behind it may not act: an
	// app_user that is pending, suspended, disabled or soft-deleted, or a service_account that is
	// disabled. Deactivating a person is meant to end their sessions immediately, and it does so
	// here rather than by hunting down rows.
	ErrPrincipalNotActive = errors.New("principal not active")

	// ErrNoStore means the resolver was built without a database handle, so no credential can be
	// looked up at all.
	//
	// A WIRING BUG, NOT AN INPUT, and it fails closed with a name rather than panicking on a nil
	// dereference: the middleware maps it to the same refusal every other failure gets, and the log
	// line says which of the two nil-shaped mistakes it was. cmd/dkp only builds a Service when the
	// store opened, so reaching this means somebody constructed one by hand.
	ErrNoStore = errors.New("no store wired")

	// ErrNoPepper means a bearer token was presented and this process has no PAT pepper to verify it
	// with — the keyring was never wired, or <data-dir>/secrets.json could not be read.
	//
	// IT FAILS CLOSED AND LOUDLY. Sessions keep working without a pepper (their hash is unkeyed), so
	// a missing keyring would otherwise be invisible until a bot author reported that their token
	// "just stopped working". Refusing the bearer with a distinct sentinel puts the cause in the log
	// line rather than in a support thread.
	ErrNoPepper = errors.New("no pat pepper configured")
)
