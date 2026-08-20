package auth

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// SecretsFileName is the file inside DKP_DATA_DIR that holds the instance root key
// (docs/operations/configuration.md: "The session key, the token pepper and the webhook signing key
// are not configuration. They are generated on first boot and persisted to
// <data-dir>/secrets.json").
const SecretsFileName = "secrets.json"

// The modes §9.1 requires: 0600 for the file, 0700 for the directory holding it.
const (
	secretsFileMode = 0o600
	secretsDirMode  = 0o700
)

// secretsVersion is the on-disk format version. It exists so that a future rotation — which adds a
// SECOND key under a new kid rather than replacing this one — can be read by a binary that knows
// the shape changed, instead of one that reports "malformed" and refuses to start.
const secretsVersion = 1

var (
	// ErrSecretsFileMode reports a secrets file that is readable by anyone but its owner. It is a
	// REFUSAL TO START rather than a warning or a silent chmod: on a shared box the group-readable
	// window has already happened, and quietly tightening the mode would hide that it ever existed.
	ErrSecretsFileMode = errors.New("secrets file must be mode 0600")

	// ErrSecretsMalformed reports a secrets file that exists and cannot be understood — truncated
	// JSON, an unknown version, a key of the wrong length, a base64 payload that will not decode.
	//
	// REFUSE TO START; NEVER SILENTLY REGENERATE (§9.1). A fresh root key invalidates every session
	// and every token in the guild at once, which looks exactly like a mass-logout bug and sends an
	// officer hunting a phantom. A named file and a named recovery path is the whole difference
	// between a five-minute fix and a lost afternoon.
	ErrSecretsMalformed = errors.New("secrets file is malformed")
)

// secretsFile is the on-disk shape. Deliberately minimal: one version, one key.
type secretsFile struct {
	Version int    `json:"version"`
	RootKey string `json:"root_key"` // standard base64 of RootKeyLen bytes
}

// LoadOrCreateKeyring reads <dir>/secrets.json and derives the keyring from it, generating the file
// on first boot if it does not exist.
//
// THE THREE OUTCOMES, and the middle one is the point:
//
//   - The file is absent: generate 32 bytes from crypto/rand, write it 0600 in a 0700 directory, and
//     carry on. There is no default key and no key in the image — §9.1's `FROM scratch` container
//     contains a binary, CA certs and tzdata, and nothing else.
//   - The file is present and unreadable, malformed, or badly permissioned: RETURN AN ERROR, so the
//     caller refuses to start. Never regenerate.
//   - The file is present and valid: derive and return.
//
// IT IS NOT ATOMIC-WITH-A-RENAME, and that is on purpose. O_EXCL is what makes two processes racing
// on first boot produce one key rather than two, and a temp-file-and-rename would let the loser
// overwrite the winner's key with a second one — after the winner had already minted a session
// against the first. The cost is that a crash mid-write leaves a short file, which the next boot
// refuses as malformed and an operator deletes; the alternative silently invalidates credentials.
func LoadOrCreateKeyring(dir string) (*Keyring, error) {
	path := filepath.Join(dir, SecretsFileName)

	raw, err := os.ReadFile(path) //nolint:gosec // the path is the operator's configured data dir
	switch {
	case err == nil:
		return keyringFromFile(path, raw)

	case errors.Is(err, fs.ErrNotExist):
		return createKeyring(dir, path)

	default:
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
}

// keyringFromFile validates an existing secrets file and derives from it.
func keyringFromFile(path string, raw []byte) (*Keyring, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", path, err)
	}

	if info.Mode().Perm()&^fs.FileMode(secretsFileMode) != 0 {
		return nil, fmt.Errorf("%s is mode %#o: %w — run `chmod 600 %s`",
			path, info.Mode().Perm(), ErrSecretsFileMode, path)
	}

	var parsed secretsFile
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("parse %s: %w: %w", path, ErrSecretsMalformed, err)
	}

	if parsed.Version != secretsVersion {
		return nil, fmt.Errorf("%s declares version %d, this binary writes version %d: %w",
			path, parsed.Version, secretsVersion, ErrSecretsMalformed)
	}

	key, err := base64.StdEncoding.DecodeString(parsed.RootKey)
	if err != nil {
		return nil, fmt.Errorf("decode root_key in %s: %w: %w", path, ErrSecretsMalformed, err)
	}

	if len(key) != RootKeyLen {
		return nil, fmt.Errorf("root_key in %s is %d bytes, want %d: %w",
			path, len(key), RootKeyLen, ErrSecretsMalformed)
	}

	ring, err := NewKeyring(key)
	if err != nil {
		return nil, fmt.Errorf("derive from %s: %w", path, err)
	}

	return ring, nil
}

// createKeyring generates the root key and writes it, on first boot only.
func createKeyring(dir, path string) (*Keyring, error) {
	if err := os.MkdirAll(dir, secretsDirMode); err != nil {
		return nil, fmt.Errorf("create data directory %s: %w", dir, err)
	}

	key := make([]byte, RootKeyLen)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate root key: %w", err)
	}

	body, err := json.MarshalIndent(secretsFile{
		Version: secretsVersion,
		RootKey: base64.StdEncoding.EncodeToString(key),
	}, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode secrets file: %w", err)
	}

	// O_EXCL: two processes booting at once produce ONE key. Without it the loser of the race
	// overwrites the winner's key after the winner may already have minted credentials against it.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, secretsFileMode)
	if err != nil {
		return nil, fmt.Errorf("create %s: %w", path, err)
	}

	if _, err := f.Write(append(body, '\n')); err != nil {
		_ = f.Close() // the write error is the one worth reporting
		return nil, fmt.Errorf("write %s: %w", path, err)
	}

	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("close %s: %w", path, err)
	}

	ring, err := NewKeyring(key)
	if err != nil {
		return nil, fmt.Errorf("derive from generated key: %w", err)
	}

	return ring, nil
}
