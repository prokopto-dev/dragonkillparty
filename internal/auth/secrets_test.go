package auth_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/auth"
)

// TestLoadOrCreateKeyring_FirstBoot_WritesA0600File is §9.1's first-boot path: no bundled config, no
// default key, no seeded admin — the key is generated on the box and written 0600.
func TestLoadOrCreateKeyring_FirstBoot_WritesA0600File(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "data")

	ring, err := auth.LoadOrCreateKeyring(dir)
	require.NoError(t, err)
	require.NotNil(t, ring)

	path := filepath.Join(dir, auth.SecretsFileName)

	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm(),
		"the file holding every credential's pepper must not be readable by other users on the box")

	dirInfo, err := os.Stat(dir)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), dirInfo.Mode().Perm())
}

// TestLoadOrCreateKeyring_SecondBoot_DerivesTheSameKey. If a restart produced a different pepper,
// every token in the guild would stop working at once and it would look like a mass revocation bug.
func TestLoadOrCreateKeyring_SecondBoot_DerivesTheSameKey(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	first, err := auth.LoadOrCreateKeyring(dir)
	require.NoError(t, err)

	second, err := auth.LoadOrCreateKeyring(dir)
	require.NoError(t, err)

	secret := []byte("a secret that must hash the same!")

	a, err := first.TokenHash(auth.PepperKIDv1, secret)
	require.NoError(t, err)

	b, err := second.TokenHash(auth.PepperKIDv1, secret)
	require.NoError(t, err)

	require.Equal(t, a, b, "a restart must not invalidate every credential in the guild")
}

// TestLoadOrCreateKeyring_TwoDirectories_DeriveDifferentKeys is the control for the test above: it
// would pass trivially if the key were a constant.
func TestLoadOrCreateKeyring_TwoDirectories_DeriveDifferentKeys(t *testing.T) {
	t.Parallel()

	first, err := auth.LoadOrCreateKeyring(t.TempDir())
	require.NoError(t, err)

	second, err := auth.LoadOrCreateKeyring(t.TempDir())
	require.NoError(t, err)

	secret := []byte("a secret that must hash the same!")

	a, err := first.TokenHash(auth.PepperKIDv1, secret)
	require.NoError(t, err)

	b, err := second.TokenHash(auth.PepperKIDv1, secret)
	require.NoError(t, err)

	require.NotEqual(t, a, b, "two instances must not share a pepper")
}

// TestLoadOrCreateKeyring_BadFile_RefusesToStartAndNeverRegenerates is the §9.1 rule that matters
// most: a secrets file that exists and cannot be understood is a REFUSAL, never a fresh key.
//
// Silently regenerating would invalidate every session and every token at once, which looks exactly
// like a mass-logout bug and is unrecoverable — the plaintext secrets are not stored, so nothing can
// re-derive the old hashes. Each case additionally asserts THE FILE IS LEFT ALONE, because a
// "helpful" repair is the same data loss with better manners.
func TestLoadOrCreateKeyring_BadFile_RefusesToStartAndNeverRegenerates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
		mode    os.FileMode
		wantErr error
	}{
		{name: "truncated json", content: `{"version":1,`, mode: 0o600, wantErr: auth.ErrSecretsMalformed},
		{name: "unknown version", content: `{"version":99,"root_key":"aaaa"}`, mode: 0o600, wantErr: auth.ErrSecretsMalformed},
		{name: "root key is not base64", content: `{"version":1,"root_key":"not base64!!"}`, mode: 0o600, wantErr: auth.ErrSecretsMalformed},
		{name: "root key is too short", content: `{"version":1,"root_key":"YWJj"}`, mode: 0o600, wantErr: auth.ErrSecretsMalformed},
		{name: "group readable", content: `{"version":1,"root_key":"MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="}`, mode: 0o640, wantErr: auth.ErrSecretsFileMode},
		{name: "world readable", content: `{"version":1,"root_key":"MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="}`, mode: 0o644, wantErr: auth.ErrSecretsFileMode},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			path := filepath.Join(dir, auth.SecretsFileName)

			require.NoError(t, os.WriteFile(path, []byte(tt.content), tt.mode))
			require.NoError(t, os.Chmod(path, tt.mode), "WriteFile honours umask; the mode is the fixture")

			ring, err := auth.LoadOrCreateKeyring(dir)
			require.Nil(t, ring)
			require.ErrorIs(t, err, tt.wantErr)
			require.Contains(t, err.Error(), auth.SecretsFileName,
				"the error must name the file an operator has to look at")

			after, readErr := os.ReadFile(path) //nolint:gosec // a fixture path under t.TempDir
			require.NoError(t, readErr)
			require.Equal(t, tt.content, string(after),
				"a bad secrets file must be left exactly as it was found")
		})
	}
}

// TestLoadOrCreateKeyring_ValidStrictFile_Loads is the positive control for the mode check: 0600 is
// accepted, so the test above is not passing because everything fails.
func TestLoadOrCreateKeyring_ValidStrictFile_Loads(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, auth.SecretsFileName)

	require.NoError(t, os.WriteFile(path,
		[]byte(`{"version":1,"root_key":"MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="}`), 0o600))
	require.NoError(t, os.Chmod(path, 0o600))

	ring, err := auth.LoadOrCreateKeyring(dir)
	require.NoError(t, err)
	require.NotNil(t, ring)
}
