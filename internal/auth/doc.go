// Package auth is identity: who is making this request, proved by one of exactly two credentials.
//
// ONE Principal, TWO CREDENTIALS, AND NO THIRD WAY IN. docs/design/03-security.md §5 states the
// property this package exists to hold: "Cookie and bearer resolve to the identical struct before
// any handler runs. No handler contains `if session { … } else { … }`." A browser session and a bot's
// personal access token differ in how they are proved and in what they may narrow to — a session
// carries no scopes, a token does — and in nothing else. Divergence between the API and the UI
// starts the moment the browser gets a privileged channel, so it does not get one.
//
// WHAT THIS PACKAGE DECIDES, AND WHAT IT DELIBERATELY DOES NOT.
//
// It decides AUTHENTICATION: is this credential real, live, and attached to an account that may act
// at all. It does not decide AUTHORIZATION — whether the principal holds the permission the
// operation declares, and whether the token's scopes reach it. That is internal/authz's
// `authz.Check` and the capability floor (`effective capability = role permissions ∩ token scopes`),
// which landed in Wave 0e (#276) against the permission and role tables Wave 0b reconciles.
//
// THE BOUNDARY SURVIVED THE JOIN, and that is what made 0e an addition rather than a rewrite
// (ADR-0028, commitment 4). This package still imports nothing from internal/authz, holds no
// permissions on the Principal, and answers no "may this principal do X" question. The dependency
// runs the other way: internal/authz reads a *Principal. A capability field on the struct below
// would make every consumer of an identity a consumer of a policy decision taken at resolution time,
// which is a cache with none of the invalidation.
//
// THE FOUR PROPERTIES THAT MAKE A CREDENTIAL SAFE HERE:
//
//  1. THE SERVER STORES NO SECRET IT COULD LEAK. A session cookie is 32 random bytes and the
//     database holds their SHA-256; a PAT is 32 random bytes and the database holds
//     HMAC-SHA256(pepper, secret) under a pepper that lives in <data-dir>/secrets.json, outside the
//     database. A read-only database leak yields no live session and no usable token.
//  2. VERIFICATION IS ONE INDEXED LOOKUP AND ONE CONSTANT-TIME COMPARE. The cookie's hash is the
//     index key; the token's PUBLIC 8-character prefix is, and the secret half is compared with
//     subtle.ConstantTimeCompare against that one row. ADR-0011 accepted a round trip per request as
//     the price of instant revocation, and it is one.
//  3. PRECEDENCE IS FIXED, NOT NEGOTIATED. `Authorization` wins outright: when it is present the
//     cookie is not read at all (§6.3), so "send both, get the union" cannot happen. A query-string
//     token is refused rather than honoured, which is the one thing every EQdkp bot does today.
//  4. EVERY FAILURE IS THE SAME 401 TO THE CALLER AND A DIFFERENT LINE IN THE LOG. Unknown, expired,
//     revoked, epoch-bumped and disabled are five distinct sentinels here and one response out
//     there: the caller learns nothing from the difference and the officer reading the log learns
//     everything.
//
// WHAT IS NOT HERE YET, each with a reason rather than an omission:
//
//   - THE LOGIN, LOGOUT AND TOKEN-MINT ENDPOINTS. Minting a credential is a session-and-step-up
//     operation and first-run bootstrap owns the first one (issue #264). This package ships the
//     primitives those endpoints call — NewSessionSecret, NewToken, HashPassword — so that the
//     endpoint is wiring rather than cryptography.
//   - MFA/TOTP. The columns are on app_user and session.mfa_satisfied_at is the step-up clock;
//     enrolment and verification are Wave 2.
//   - THE FEED-TOKEN RESOLVER. feed_token is a path-embedded credential for routes that do not exist
//     yet; the table and its catalogue ship here so the routes arrive against a schema that fits.
//   - RATE LIMITING AND THE 250 ms RESPONSE FLOOR of §3.3. Both belong to the login endpoint, which
//     is where a guessable credential is actually guessed; nothing in this package can be brute
//     forced, because a 32-byte random secret is not guessable and its lookup is O(1) either way.
package auth
