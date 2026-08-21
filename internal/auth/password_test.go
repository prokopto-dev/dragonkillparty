package auth_test

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/auth"
)

// TestHashPassword_Shape_IsThePHCStringTheDesignSpecifies pins the stored format against
// docs/design/03-security.md §3.1, field by field:
//
//	$argon2id$v=19$m=19456,t=2,p=1$<22-char b64 salt>$<43-char b64 tag>
//
// The format is not internal: it is what a restore, a support session and any future migration read,
// and PHC is the shape every other argon2 implementation can also read. A hash that drifted from it
// would be one nothing else could verify.
func TestHashPassword_Shape_IsThePHCStringTheDesignSpecifies(t *testing.T) {
	t.Parallel()

	encoded, err := auth.HashPassword(auth.DefaultArgon2Profile(), "correct horse battery staple")
	require.NoError(t, err)

	fields := strings.Split(encoded, "$")
	require.Len(t, fields, 6, "PHC is $alg$v$params$salt$tag: %q", encoded)
	require.Empty(t, fields[0])
	require.Equal(t, "argon2id", fields[1])
	require.Equal(t, "v=19", fields[2], "argon2 1.3 is what x/crypto implements")
	require.Equal(t, "m=19456,t=2,p=1", fields[3], "OWASP's password-storage baseline (§3.1)")
	require.Len(t, fields[4], 22, "16 salt bytes, unpadded base64")
	require.Len(t, fields[5], 43, "32 tag bytes, unpadded base64")
}

// TestHashPassword_TwoHashesOfOnePassword_Differ is the per-hash salt doing its job: two members who
// choose the same password must be indistinguishable in the table, and a stolen table must not be
// sortable into "these fifty accounts share a password".
func TestHashPassword_TwoHashesOfOnePassword_Differ(t *testing.T) {
	t.Parallel()

	profile := auth.DefaultArgon2Profile()

	first, err := auth.HashPassword(profile, "the same password")
	require.NoError(t, err)

	second, err := auth.HashPassword(profile, "the same password")
	require.NoError(t, err)

	require.NotEqual(t, first, second, "the salt is per hash, not per user and not per instance")

	// Both still verify: different salt, same password.
	for _, encoded := range []string{first, second} {
		ok, verifyErr := auth.VerifyPassword(encoded, "the same password")
		require.NoError(t, verifyErr)
		require.True(t, ok)
	}
}

// TestVerifyPassword_RightAndWrong is the round trip, including the neighbours of the real password
// that a substring or prefix comparison would accept.
func TestVerifyPassword_RightAndWrong(t *testing.T) {
	t.Parallel()

	const password = "raid night at eight sharp"

	encoded, err := auth.HashPassword(auth.DefaultArgon2Profile(), password)
	require.NoError(t, err)

	ok, err := auth.VerifyPassword(encoded, password)
	require.NoError(t, err)
	require.True(t, ok)

	for _, wrong := range []string{
		"",
		"raid night at eight shar",   // a prefix
		"raid night at eight sharp ", // a trailing space
		"Raid night at eight sharp",  // case
		"raid night at eight sharpX",
	} {
		ok, err = auth.VerifyPassword(encoded, wrong)
		require.NoError(t, err)
		require.Falsef(t, ok, "%q must not verify", wrong)
	}
}

// TestVerifyPassword_ParametersComeFromTheHash is the property that lets a guild change its cost
// profile without invalidating a single existing password.
//
// A verifier that used TODAY's parameters instead of the row's would reject every account at once, on
// the boot after an operator followed `dkp doctor`'s advice — which is the worst possible outcome of
// taking that advice.
func TestVerifyPassword_ParametersComeFromTheHash(t *testing.T) {
	t.Parallel()

	profiles := auth.Argon2Profiles()
	require.Len(t, profiles, 5, "§3.1's ladder has five rungs")

	for _, profile := range profiles {
		encoded, err := auth.HashPassword(profile, "one password, five profiles")
		require.NoError(t, err)

		require.Contains(t, encoded, fmt.Sprintf("m=%d,t=%d,p=%d",
			profile.MemoryKiB, profile.Iterations, profile.Parallelism),
			"the rung's parameters travel with the hash")

		ok, err := auth.VerifyPassword(encoded, "one password, five profiles")
		require.NoError(t, err)
		require.Truef(t, ok, "a hash written under %q must verify under any current profile", profile.Name)
	}
}

// TestNeedsRehash_OnlyWhenTheParametersMoved. Called inside the successful-login transaction, which
// is the only moment the plaintext exists to re-derive from.
func TestNeedsRehash_OnlyWhenTheParametersMoved(t *testing.T) {
	t.Parallel()

	weaker, err := auth.ParseArgon2Profile("lowest")
	require.NoError(t, err)

	encoded, err := auth.HashPassword(weaker, "a password hashed on a small box")
	require.NoError(t, err)

	same, err := auth.NeedsRehash(encoded, weaker)
	require.NoError(t, err)
	require.False(t, same, "an unchanged profile must not rehash every login")

	moved, err := auth.NeedsRehash(encoded, auth.DefaultArgon2Profile())
	require.NoError(t, err)
	require.True(t, moved, "a guild that moved to better hardware upgrades its hashes as members log in")
}

// TestParseArgon2Profile_TheLadder holds §3.1's five rungs and the two names the docs pin.
//
// EVERY RUNG IS p=1: it keeps peak memory predictable under concurrency, which is what makes the
// semaphore's bound (slots × m) a real bound rather than an estimate.
func TestParseArgon2Profile_TheLadder(t *testing.T) {
	t.Parallel()

	want := map[string][2]uint32{
		"high":    {47104, 1},
		"default": {19456, 2},
		"low":     {12288, 3},
		"lower":   {9216, 4},
		"lowest":  {7168, 5},
	}

	for name, params := range want {
		profile, err := auth.ParseArgon2Profile(name)
		require.NoError(t, err)
		require.Equal(t, params[0], profile.MemoryKiB, "%s memory", name)
		require.Equal(t, params[1], profile.Iterations, "%s iterations", name)
		require.Equal(t, uint8(1), profile.Parallelism, "%s parallelism", name)
	}

	// Empty is the default: the variable is optional and an operator who exported it empty meant
	// "leave it alone".
	empty, err := auth.ParseArgon2Profile("")
	require.NoError(t, err)
	require.Equal(t, auth.DefaultArgon2Profile(), empty)

	// A typo is an ERROR, not a silent fallback: somebody who typed `lo` believing their Pi was
	// catered for must be told, not left to discover it in login latency.
	_, err = auth.ParseArgon2Profile("lo")
	require.ErrorIs(t, err, auth.ErrUnknownArgon2Profile)
	require.Contains(t, err.Error(), "lowest", "the error names the legal values")
}

// TestVerifyPassword_MalformedHash_IsNotAWrongPassword is the distinction that decides what an
// officer does next.
//
// A row whose hash is unreadable — a corrupt restore, a hand-edit, an import that should have written
// NULL — reported as "wrong password" sends somebody resetting a credential to fix a database
// problem, while the real cause never reaches a log. Each case below is one an attacker with write
// access to the row would try, and the m=8 case is the one that matters: a parameter this package
// accepted without reading would turn a stored hash into one a laptop cracks.
func TestVerifyPassword_MalformedHash_IsNotAWrongPassword(t *testing.T) {
	t.Parallel()

	good, err := auth.HashPassword(auth.DefaultArgon2Profile(), "a password")
	require.NoError(t, err)

	fields := strings.Split(good, "$")

	tests := []struct {
		name    string
		encoded string
	}{
		{name: "empty", encoded: ""},
		{name: "not phc", encoded: "hunter2"},
		{name: "bcrypt", encoded: "$2y$10$abcdefghijklmnopqrstuv"},
		{name: "argon2i", encoded: strings.Replace(good, "argon2id", "argon2i", 1)},
		{name: "argon2d", encoded: strings.Replace(good, "argon2id", "argon2d", 1)},
		{name: "wrong version", encoded: strings.Replace(good, "v=19", "v=16", 1)},
		{name: "zero memory", encoded: strings.Replace(good, "m=19456", "m=0", 1)},
		{name: "unparseable params", encoded: strings.Replace(good, "m=19456,t=2,p=1", "m=lots", 1)},
		{name: "memory beyond the ceiling", encoded: strings.Replace(good, "m=19456", "m=4194304", 1)},
		{name: "iterations beyond the ceiling", encoded: strings.Replace(good, "t=2", "t=9999", 1)},
		{name: "truncated salt", encoded: "$argon2id$v=19$m=19456,t=2,p=1$YWJj$" + fields[5]},
		{name: "truncated tag", encoded: "$argon2id$v=19$m=19456,t=2,p=1$" + fields[4] + "$YWJj"},
		{name: "salt is not base64", encoded: "$argon2id$v=19$m=19456,t=2,p=1$!!!!!!!!!!!!!!!!!!!!!!$" + fields[5]},
		{name: "missing a field", encoded: "$argon2id$v=19$m=19456,t=2,p=1$" + fields[4]},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ok, verifyErr := auth.VerifyPassword(tt.encoded, "a password")
			require.False(t, ok)
			require.ErrorIs(t, verifyErr, auth.ErrHashMalformed,
				"an unreadable hash is a database problem, not a failed login")
		})
	}

	// AND THE ONE THAT IS NOT MALFORMED, which is the finding this table produced. A row edited DOWN
	// to m=8 parses, computes, and simply does not match — because the tag was derived under the
	// original parameters. Weakening a stored hash's parameters does not weaken the hash; it destroys
	// it. An earlier draft of this test asserted a refusal here and was wrong about what the check
	// buys, which is how the ceiling above got written: the real exposure is a row edited UP, where
	// one login attempt allocates whatever the row asks for.
	weakened := strings.Replace(good, "m=19456,t=2", "m=8,t=1", 1)
	require.NotEqual(t, good, weakened, "fixture is stale")

	ok, err := auth.VerifyPassword(weakened, "a password")
	require.NoError(t, err, "weakened parameters are readable; they are not corruption")
	require.False(t, ok, "the tag was derived under the original parameters, so it cannot match")
}

// TestHashPassword_InputBounds. The 128-byte ceiling is §3.1's request sanity bound — argon2's cost
// is independent of input length, so an unbounded input is a way to make the server read a gigabyte
// before it hashes 19 MiB. The 12-character FLOOR and the breached-password blocklist are policy and
// belong to the endpoint that sets a password, which is why they are not here.
func TestHashPassword_InputBounds(t *testing.T) {
	t.Parallel()

	profile := auth.DefaultArgon2Profile()

	_, err := auth.HashPassword(profile, "")
	require.ErrorIs(t, err, auth.ErrPasswordEmpty)

	_, err = auth.HashPassword(profile, strings.Repeat("x", auth.MaxPasswordBytes+1))
	require.ErrorIs(t, err, auth.ErrPasswordTooLong)

	// Exactly at the bound is legal.
	_, err = auth.HashPassword(profile, strings.Repeat("x", auth.MaxPasswordBytes))
	require.NoError(t, err)
}

// TestVerifyPassword_OverlongInput_IsFalseNotAnError. It cannot have produced any stored hash, and
// answering with an error would make the length of a guess observable in a way a wrong password is
// not.
func TestVerifyPassword_OverlongInput_IsFalseNotAnError(t *testing.T) {
	t.Parallel()

	encoded, err := auth.HashPassword(auth.DefaultArgon2Profile(), "a password")
	require.NoError(t, err)

	ok, err := auth.VerifyPassword(encoded, strings.Repeat("x", auth.MaxPasswordBytes+1))
	require.NoError(t, err)
	require.False(t, ok)
}

// TestHashPassword_UnderConcurrency_IsBounded exercises the semaphore every argon2 call passes
// through.
//
// WHAT IT CAN AND CANNOT ASSERT. It cannot measure RSS portably, so it does not pretend to: what it
// holds is that concurrent hashing COMPLETES and stays correct, which is what fails first if the
// limiter deadlocks, releases a slot it never took, or is bypassed by one of the two call paths. The
// memory bound itself is argued in the code (slots × m, with p=1 keeping m predictable) and measured
// by §3.1's own mechanism — an integration test that fires 200 concurrent logins against a real
// server, which needs the login endpoint that does not exist yet.
//
// The `lowest` rung on purpose: this test is about the limiter, and paying 19 MiB × 32 to assert it
// would make the unit suite the slowest thing in the repository.
func TestHashPassword_UnderConcurrency_IsBounded(t *testing.T) {
	t.Parallel()

	profile, err := auth.ParseArgon2Profile("lowest")
	require.NoError(t, err)

	const workers = 32

	var wg sync.WaitGroup

	results := make([]string, workers)

	for i := range workers {
		wg.Add(1)

		go func() {
			defer wg.Done()

			encoded, hashErr := auth.HashPassword(profile, "concurrent password")
			require.NoError(t, hashErr)

			results[i] = encoded
		}()
	}

	wg.Wait()

	for i, encoded := range results {
		require.NotEmptyf(t, encoded, "worker %d produced nothing", i)

		ok, verifyErr := auth.VerifyPassword(encoded, "concurrent password")
		require.NoError(t, verifyErr)
		require.True(t, ok)
	}
}
