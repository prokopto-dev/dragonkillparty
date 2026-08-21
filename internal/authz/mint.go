package authz

import (
	"errors"
	"fmt"
	"slices"
)

// ErrUnknownScope is returned when a token is asked to carry a scope the catalogue does not define.
var ErrUnknownScope = errors.New("scope is not in the catalogue")

// ValidateScopes is the scope-subsetting rule that can be enforced with no caller's help: every scope
// a token is minted with must resolve in Scopes().
//
// THIS IS WHAT MAKES "THERE IS NO `admin:*` SCOPE" A PROPERTY RATHER THAN A PROMISE (ADR-0011,
// canonical §6). EQdkp Plus's `api_key` impersonates the first superadmin; the fix is not only that
// this product refuses to honour such a scope at the choke point, but that it refuses to WRITE one
// onto a token row in the first place. A scope string the catalogue never defined is unreachable by
// Check — Requirement.ScopeSets only ever names catalogue scopes — so an invented scope grants
// nothing today, and this is what stops it sitting in the database until somebody adds a route that
// happens to name it.
//
// Errors name EVERY offending scope rather than the first: a bot author fixing a mint call wants the
// whole list, for the reason requireKeys collects every missing permission key.
//
// WHAT THIS IS NOT, STATED PRECISELY, because the missing half is the more interesting one.
// docs/api/auth-and-scopes.md says "Scope subsetting is enforced on mint. You cannot mint a token
// with a scope you do not hold." That rule needs an answer to "which scopes does this MINTER hold",
// and a minter is a browser session — token.mint is in the capability floor, so the only principal
// that can ever reach the mint endpoint carries no scopes at all. A human holds PERMISSIONS, so
// bounding the mint means mapping the requested scopes back to the permissions they reach, and this
// repository has no such mapping: canonical §6 publishes the two vocabularies and relates them only
// per-operation, through the `x-dkp-permission` / `x-dkp-scopes` pair each route declares. Deriving
// the relation from the operation registry is the right mechanism and it belongs in the change that
// adds the mint endpoint, where its behaviour is observable — with one PAT-callable operation
// registered today it would be a rule that refuses twenty-six of the twenty-seven scopes for reasons
// nobody could see. Issue #281 carries it.
//
// The guarantee that does NOT wait for it: a token's capability is `role permissions ∩ token scopes`
// evaluated on every request (Check), so an over-scoped token still cannot exceed its service
// account's roles. Mint-time subsetting is defence in depth and a better error message; the floor is
// the control.
func ValidateScopes(requested []string) error {
	catalogue := Scopes()

	valid := make([]string, 0, len(catalogue))
	for _, s := range catalogue {
		valid = append(valid, s.Key)
	}

	var unknown []string

	for _, scope := range requested {
		if !slices.Contains(valid, scope) && !slices.Contains(unknown, scope) {
			unknown = append(unknown, scope)
		}
	}

	if len(unknown) > 0 {
		return fmt.Errorf("%w: %v — canonical §6 publishes the scope vocabulary and a token may carry "+
			"nothing outside it; there is no admin:* scope (ADR-0011)", ErrUnknownScope, unknown)
	}

	return nil
}
