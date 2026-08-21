package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"runtime"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Password storage, from docs/design/03-security.md §3.1: argon2id, PHC-encoded so the parameters
// travel with the hash.
//
//	$argon2id$v=19$m=19456,t=2,p=1$<22-char b64 salt>$<43-char b64 tag>
//
// PARAMETERS TRAVEL WITH THE HASH, and that is the whole reason for the PHC string rather than four
// columns or a bare tag. A guild that moves to slower hardware and steps its profile down still has
// to verify every password stored under the old one; a verifier that read today's parameters instead
// of the row's would reject every existing account at once, on the boot after the change.
//
// NO PEPPER, DELIBERATELY (§3.1). A pepper helps only against a database-only leak, which for a
// single-file SQLite deployment is nearly the same event as a filesystem leak, and it makes rotation
// require every user to re-authenticate. Tokens ARE peppered (see keyring.go) because they need a
// fast keyed hash, not a slow one. This is written down so it is not "fixed" later by someone who
// read a blog post.
//
// LEGACY EQdkp HASHES ARE NEVER IMPORTED (AGENTS.md). The source population mixes bcrypt three ways,
// argon2i and argon2d, phpass, ext_des and bare MD5; the importer sets password_hash = NULL,
// must_reset = 1 and mints claim invitations. That is why user_identity.password_algo has exactly
// one legal value, and why there is no verifier here for anything but argon2id.

// The PHC field sizes §3.1 specifies.
const (
	// argon2SaltLen is 16 bytes from crypto/rand, per hash — 22 characters of unpadded base64.
	argon2SaltLen = 16

	// argon2TagLen is the 32-byte derived key — 43 characters.
	argon2TagLen = 32

	// argon2Version is the only version this product writes or reads. `v=19` is argon2 1.3, which is
	// what golang.org/x/crypto/argon2 implements; a hash claiming any other version is refused rather
	// than verified under an assumption.
	argon2Version = argon2.Version

	// The ceiling decodePHC refuses parameters above, and it is a RESOURCE bound rather than a
	// strength one.
	//
	// WHAT IT DEFENDS. A stored hash's parameters are what a verify computes with, so a row carrying
	// m=4194304 makes one login attempt allocate four gibibytes — a memory-exhaustion DoS out of a
	// single crafted or corrupted row, on a box whose whole point is that it might be a Raspberry Pi.
	// The semaphore bounds how MANY hashes run at once; nothing else bounds how large one is.
	//
	// WHAT IT DOES NOT DEFEND, so that nobody adds a floor to match it: weakening a stored row's
	// parameters does not weaken the hash. The tag was derived under the ORIGINAL parameters, so a row
	// edited down to m=8 simply stops verifying — the password is not easier to find, it is impossible
	// to use. An attacker who can write the row can set their own password outright, which no
	// parameter check addresses. The ceiling is generous for the same reason: it must never refuse a
	// hash this product legitimately wrote, including under a future rung stronger than today's, and a
	// hash that stops verifying is a member locked out permanently.
	maxHashMemoryKiB  = 1 << 20 // 1 GiB
	maxHashIterations = 32

	// MaxPasswordBytes is a request sanity bound, not a cryptographic need (§3.1). NIST SP 800-63B's
	// posture is 12–128 characters with no composition rules; the floor and the breached-password
	// blocklist belong to the endpoint that SETS a password, because they are policy a guild's
	// operator can be told about. This is the ceiling, and it is here because argon2's cost is
	// independent of input length — an unbounded input is a way to make the server read a gigabyte
	// before it hashes 19 MiB.
	MaxPasswordBytes = 128
)

var (
	// ErrPasswordTooLong reports an input over MaxPasswordBytes.
	ErrPasswordTooLong = errors.New("password exceeds 128 bytes")

	// ErrPasswordEmpty reports an empty input. The 12-character policy floor is the endpoint's; this
	// refuses only the case that is a bug rather than a weak choice.
	ErrPasswordEmpty = errors.New("password is empty")

	// ErrHashMalformed reports a stored hash this package cannot read: not PHC, not argon2id, a
	// version it does not implement, parameters that do not parse, or fields of the wrong length.
	//
	// IT IS NOT "WRONG PASSWORD". A row whose hash is unreadable is a corrupt or hand-edited row, and
	// reporting it as a failed login would send an officer resetting a password to fix a database
	// problem — while the real cause never appears in a log.
	ErrHashMalformed = errors.New("password hash is malformed")

	// ErrUnknownArgon2Profile reports a DKP_ARGON2_PROFILE value that names no rung of the ladder.
	ErrUnknownArgon2Profile = errors.New("unknown argon2 profile")
)

// Argon2Profile is one rung of OWASP's equivalent-cost ladder (§3.1). Every rung is the same work
// factor bought differently: more memory and fewer passes, or less memory and more.
//
// THE LADDER EXISTS FOR THE RASPBERRY PI. 19 MiB per login is nothing on a VPS and material on a Pi
// 3, and a login that takes 1.4 seconds is a login an officer retries — turning one hash into three.
// `dkp doctor` measures the real wall time and names the rung to move to, which is why the rungs
// have names an operator can type rather than numbers they would have to look up.
type Argon2Profile struct {
	// Name is the DKP_ARGON2_PROFILE value.
	Name string

	// MemoryKiB is argon2's `m`, in kibibytes — the dominant cost and the one that OOMs a small box.
	MemoryKiB uint32

	// Iterations is `t`.
	Iterations uint32

	// Parallelism is `p`, and it is 1 on every rung: it keeps peak memory predictable under
	// concurrency, which is the property the semaphore below depends on.
	Parallelism uint8
}

// Argon2Profiles returns the ladder, strongest first, exactly as §3.1 enumerates it:
// m=47104,t=1 / m=19456,t=2 (default) / m=12288,t=3 / m=9216,t=4 / m=7168,t=5, all p=1.
//
// A FUNCTION RETURNING A FRESH SLICE, never a package-level var — .claude/rules/go-idioms.md bans
// package-level mutable state, and a shared slice is one append in a test away from an intermittent
// failure under -shuffle=on.
//
// ONLY TWO OF THE FIVE NAMES ARE PINNED BY THE DESIGN DOCS: `default`
// (docs/operations/configuration.md's default value) and `low` (§3.1's worked doctor hint, "set
// DKP_ARGON2_PROFILE=low — your hardware takes 1.4 s per login"). The other three follow the same
// scheme in the direction an operator actually travels, which is downwards.
func Argon2Profiles() []Argon2Profile {
	return []Argon2Profile{
		{Name: "high", MemoryKiB: 47104, Iterations: 1, Parallelism: 1},
		{Name: "default", MemoryKiB: 19456, Iterations: 2, Parallelism: 1},
		{Name: "low", MemoryKiB: 12288, Iterations: 3, Parallelism: 1},
		{Name: "lower", MemoryKiB: 9216, Iterations: 4, Parallelism: 1},
		{Name: "lowest", MemoryKiB: 7168, Iterations: 5, Parallelism: 1},
	}
}

// DefaultArgon2Profile is OWASP's password-storage baseline and this product's default: m=19456 KiB,
// t=2, p=1.
func DefaultArgon2Profile() Argon2Profile {
	for _, p := range Argon2Profiles() {
		if p.Name == "default" {
			return p
		}
	}

	// Unreachable while the ladder contains its own default, and a panic here would be a panic
	// outside main wiring (.claude/rules/go-idioms.md). The zero value would hash with m=0, so the
	// honest fallback is the parameters the design names, spelled out.
	return Argon2Profile{Name: "default", MemoryKiB: 19456, Iterations: 2, Parallelism: 1}
}

// ParseArgon2Profile resolves a DKP_ARGON2_PROFILE value.
//
// An empty value is the default rather than an error: the variable is optional, and an operator who
// exports it empty meant "leave it alone". A value that names no rung IS an error — silently falling
// back would leave somebody who typed `DKP_ARGON2_PROFILE=lo` believing their Pi had been catered
// for while it timed out on every login.
func ParseArgon2Profile(name string) (Argon2Profile, error) {
	if strings.TrimSpace(name) == "" {
		return DefaultArgon2Profile(), nil
	}

	for _, p := range Argon2Profiles() {
		if p.Name == name {
			return p, nil
		}
	}

	names := make([]string, 0, len(Argon2Profiles()))
	for _, p := range Argon2Profiles() {
		names = append(names, p.Name)
	}

	return Argon2Profile{}, fmt.Errorf("%q: %w (one of: %s)",
		name, ErrUnknownArgon2Profile, strings.Join(names, ", "))
}

// argon2Slots bounds how many argon2 computations run at once, process-wide.
//
// MEMORY EXHAUSTION IS THE OPERATIONAL RISK, NOT CRACKING (§3.1). 19 MiB × unbounded concurrent
// logins OOMs a Raspberry Pi, and the shape of that failure is the whole server dying during a raid
// rather than a slow login page. Every argon2 call — verify AND derive — passes through here, so peak
// password memory is bounded at slots × m regardless of how many requests arrive.
//
// PACKAGE-LEVEL, AND THAT IS THE POINT rather than an exception to
// .claude/rules/go-idioms.md's ban on package-level mutable state. Memory is a PROCESS resource: a
// limiter owned by a Service would be multiplied by however many Services exist, which is exactly the
// bound this is supposed to be. The channel value is written once here and never reassigned; what
// varies is its occupancy, which is what a semaphore is.
//
// max(2, NumCPU): two is the floor because one would serialise every login behind a slow hash, and a
// single-core box still has to let an officer in while a bot is authenticating.
var argon2Slots = make(chan struct{}, max(2, runtime.NumCPU()))

// withArgon2Slot runs fn holding one slot.
//
// It BLOCKS rather than shedding, because the caller that would shed is the rate limiter in front of
// it (§3.3's 429 before argon2) and it has more context than this does: a queue of three logins on a
// busy Pi is correct behaviour, and turning it into an error would make every guild's login page
// flaky under exactly the load it is meant to survive.
func withArgon2Slot[T any](fn func() T) T {
	argon2Slots <- struct{}{}
	defer func() { <-argon2Slots }()

	return fn()
}

// HashPassword derives a PHC-encoded argon2id hash under the given profile.
//
// The salt is 16 fresh bytes from crypto/rand per hash — never derived from the username, never
// reused — which is what makes two members who choose the same password indistinguishable in the
// table.
func HashPassword(profile Argon2Profile, plaintext string) (string, error) {
	switch {
	case plaintext == "":
		return "", ErrPasswordEmpty
	case len(plaintext) > MaxPasswordBytes:
		return "", fmt.Errorf("%d bytes: %w", len(plaintext), ErrPasswordTooLong)
	}

	salt := make([]byte, argon2SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate password salt: %w", err)
	}

	tag := withArgon2Slot(func() []byte {
		return argon2.IDKey([]byte(plaintext), salt,
			profile.Iterations, profile.MemoryKiB, profile.Parallelism, argon2TagLen)
	})

	return encodePHC(profile, salt, tag), nil
}

// VerifyPassword reports whether plaintext produced encoded.
//
// THE PARAMETERS COME FROM THE HASH, not from the current profile, which is what lets a guild change
// its profile without invalidating a single existing password. Pair it with NeedsRehash inside the
// successful-login transaction (§3.1's "rehash on login").
//
// A too-long input is FALSE rather than an error: it cannot have produced any stored hash, and
// answering differently would make the length of a guess observable in a way the response to a wrong
// password is not.
func VerifyPassword(encoded, plaintext string) (bool, error) {
	profile, salt, tag, err := decodePHC(encoded)
	if err != nil {
		return false, err
	}

	if plaintext == "" || len(plaintext) > MaxPasswordBytes {
		return false, nil
	}

	candidate := withArgon2Slot(func() []byte {
		return argon2.IDKey([]byte(plaintext), salt,
			profile.Iterations, profile.MemoryKiB, profile.Parallelism, uint32(len(tag)))
	})

	return subtle.ConstantTimeCompare(candidate, tag) == 1, nil
}

// NeedsRehash reports whether encoded was produced under different parameters from want.
//
// Called inside the successful-login transaction, where the plaintext is in hand and the user is
// already waiting: that is the only moment a password can be re-derived, because the plaintext is
// never stored. A guild that steps its profile UP gets stronger hashes as its members log in, and one
// that steps DOWN stops paying for the old cost.
func NeedsRehash(encoded string, want Argon2Profile) (bool, error) {
	have, _, _, err := decodePHC(encoded)
	if err != nil {
		return false, err
	}

	return have.MemoryKiB != want.MemoryKiB ||
		have.Iterations != want.Iterations ||
		have.Parallelism != want.Parallelism, nil
}

// encodePHC renders the PHC string. base64 WITHOUT padding and with the standard alphabet, which is
// what the PHC format specifies and what every other argon2 implementation reads.
func encodePHC(profile Argon2Profile, salt, tag []byte) string {
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2Version, profile.MemoryKiB, profile.Iterations, profile.Parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(tag))
}

// decodePHC reads a stored hash back into its parameters, salt and tag.
//
// EVERY FIELD IS CHECKED, and the rejections are not paranoia about a format this package also
// writes: this is what reads a row after a restore from backup, after a hand-edit at 1 a.m., or after
// an import that was supposed to write NULL. A parameter this package accepted without reading would
// be a parameter an attacker with write access to the row could set to m=8,t=1 — turning a stored
// hash into one a laptop cracks, with nothing anywhere reporting a change.
func decodePHC(encoded string) (Argon2Profile, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")

	// "", "argon2id", "v=19", "m=…,t=…,p=…", salt, tag — the leading empty field is the string's
	// opening separator.
	if len(parts) != 6 || parts[0] != "" {
		return Argon2Profile{}, nil, nil, fmt.Errorf("not a PHC string: %w", ErrHashMalformed)
	}

	if parts[1] != "argon2id" {
		return Argon2Profile{}, nil, nil, fmt.Errorf("algorithm %q is not argon2id: %w",
			parts[1], ErrHashMalformed)
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2Version {
		return Argon2Profile{}, nil, nil, fmt.Errorf("version %q is not v=%d: %w",
			parts[2], argon2Version, ErrHashMalformed)
	}

	var (
		memory      uint32
		iterations  uint32
		parallelism uint8
	)

	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &parallelism); err != nil {
		return Argon2Profile{}, nil, nil, fmt.Errorf("parameters %q: %w: %w",
			parts[3], ErrHashMalformed, err)
	}

	if memory == 0 || iterations == 0 || parallelism == 0 {
		return Argon2Profile{}, nil, nil, fmt.Errorf("parameters %q include a zero: %w",
			parts[3], ErrHashMalformed)
	}

	if memory > maxHashMemoryKiB || iterations > maxHashIterations {
		return Argon2Profile{}, nil, nil, fmt.Errorf(
			"parameters %q exceed the resource ceiling (m<=%d, t<=%d): %w",
			parts[3], maxHashMemoryKiB, maxHashIterations, ErrHashMalformed)
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) != argon2SaltLen {
		return Argon2Profile{}, nil, nil, fmt.Errorf("salt is not %d base64 bytes: %w",
			argon2SaltLen, ErrHashMalformed)
	}

	tag, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(tag) != argon2TagLen {
		return Argon2Profile{}, nil, nil, fmt.Errorf("tag is not %d base64 bytes: %w",
			argon2TagLen, ErrHashMalformed)
	}

	return Argon2Profile{
		MemoryKiB:   memory,
		Iterations:  iterations,
		Parallelism: parallelism,
	}, salt, tag, nil
}
