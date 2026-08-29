package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/authz"
	"github.com/prokopto-dev/dragonkillparty/internal/store"
)

// The `dkp admin` wiring (issue #264). internal/authz has the bootstrap's own suite against a real
// database; what is asserted HERE is the half cmd/ owns and could get wrong on its own — that the
// password comes off standard input and never a flag, that the command reconciles before it grants
// (a migrated-but-never-served database has no role rows), and that first run through the CLI ends
// with an admin.owner holder.
//
// It calls t.Setenv, so none of these may be parallel.

// adminPassword satisfies §3.2 and contains neither the fixture username nor its email local part.
const adminPassword = "seventeen-oaks-drift"

// runAdmin executes one `dkp admin …` invocation against a fresh migrated database and returns its
// stdout, the data directory, and the error.
//
// THROUGH THE REAL COMMAND TREE, for the reason cmd/dkp/authz_test.go gives about the boot path: the
// thing most likely to be wrong is not the bootstrap — that has its own suite — but a flag that is
// not wired, a subcommand that is not registered, or a password that never reaches the reader.
func runAdmin(t *testing.T, stdin string, args ...string) (string, string, error) {
	t.Helper()

	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "dkp.db")

	t.Setenv(dbPathEnv, dbPath)
	t.Setenv(dataDirEnv, dataDir)

	migrateFixture(t)

	out, err := runAdminIn(t, stdin, args...)

	return out, dataDir, err
}

// runAdminIn runs another invocation against the database the environment already names, so a test
// can create an account and then act on the instance it produced.
func runAdminIn(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()

	cmd := newRootCmd()

	var out bytes.Buffer

	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetArgs(args)

	err := cmd.ExecuteContext(t.Context())

	return out.String(), err
}

// migrateFixture applies the migrations the way an operator would, with `dkp migrate`.
func migrateFixture(t *testing.T) {
	t.Helper()

	cmd := newRootCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"migrate"})

	require.NoError(t, cmd.ExecuteContext(t.Context()), "dkp migrate")
}

// TestAdminCreate_FreshInstance_ProducesTheOwnerHolder is issue #264's acceptance criterion driven
// through the command an operator actually types.
func TestAdminCreate_FreshInstance_ProducesTheOwnerHolder(t *testing.T) {
	out, _, err := runAdmin(t, adminPassword,
		"admin", "create", "--username", "courtney", "--password-stdin")
	require.NoError(t, err)

	require.Contains(t, out, "created courtney")
	require.Contains(t, out, authz.RoleKeyOwner)
	require.Contains(t, out, "first-run setup is complete")

	st, err := store.Open(t.Context(), os.Getenv(dbPathEnv))
	require.NoError(t, err)

	defer func() { require.NoError(t, st.Close()) }()

	boot := authz.NewBootstrapper(st, nil, argon2Profile(t.Context()), "")

	holders, err := boot.OwnerHolders(t.Context())
	require.NoError(t, err)
	require.Equal(t, int64(1), holders,
		"a fresh instance must end `dkp admin create` with exactly one admin.owner holder")
}

// TestAdminCreate_MigratedButNeverServed_StillGrants is the failure this command would otherwise hit
// on every genuinely fresh install: the built-in roles are seeded at BOOT, not by a migration, so a
// database that has only been migrated has no role row for the grant to reference. The command runs
// the same reconciliation `dkp serve` does.
func TestAdminCreate_MigratedButNeverServed_StillGrants(t *testing.T) {
	// runAdmin migrates and does not serve, which is exactly the state under test.
	_, _, err := runAdmin(t, adminPassword,
		"admin", "create", "--username", "courtney", "--password-stdin")
	require.NoError(t, err,
		"the grant needs role rows the reconciler seeds; `dkp admin create` must not require a prior boot")
}

// TestAdminCreate_PasswordIsNotAFlag is the property this command's whole shape exists for: a
// password in argv is in the process table for every user on the box and in the shell history.
func TestAdminCreate_PasswordIsNotAFlag(t *testing.T) {
	cmd := newAdminCreateCmd()

	require.Nil(t, cmd.Flags().Lookup("password"),
		"a --password flag would put the secret in argv, the process table and the shell history")

	require.NotNil(t, cmd.Flags().Lookup(passwordStdinFlag))
}

// TestAdminCreate_WithoutPasswordStdin_IsRefused keeps the flag from being optional-by-accident: a
// command that silently read a terminal would hang, and one that defaulted to something would be a
// default password.
func TestAdminCreate_WithoutPasswordStdin_IsRefused(t *testing.T) {
	_, _, err := runAdmin(t, adminPassword, "admin", "create", "--username", "courtney")
	require.Error(t, err)
	require.Contains(t, err.Error(), passwordStdinFlag)
}

// TestAdminCreate_MissingUsername_IsRefused: cobra cannot require a string flag, so the check is the
// command's.
func TestAdminCreate_MissingUsername_IsRefused(t *testing.T) {
	_, _, err := runAdmin(t, adminPassword, "admin", "create", "--password-stdin")
	require.Error(t, err)
	require.Contains(t, err.Error(), "--username")
}

// TestAdminCreate_EmptyStdin_IsRefused stops an empty pipe from becoming an empty password.
func TestAdminCreate_EmptyStdin_IsRefused(t *testing.T) {
	_, _, err := runAdmin(t, "", "admin", "create", "--username", "courtney", "--password-stdin")
	require.Error(t, err)
	require.Contains(t, err.Error(), "no password on standard input")
}

// TestAdminCreate_PasswordPolicy_IsEnforced: host access is the bar for RUNNING the command, not an
// exemption from §3.2.
func TestAdminCreate_PasswordPolicy_IsEnforced(t *testing.T) {
	_, _, err := runAdmin(t, "short", "admin", "create", "--username", "courtney", "--password-stdin")
	require.Error(t, err)
	require.Contains(t, err.Error(), "floor")
}

// TestAdminCreate_SecondAccount_IsGrantedManually is the granted_via distinction: the first owner is
// `bootstrap` — the assignment nobody may be left without — and everything after it is an officer's
// decision.
func TestAdminCreate_SecondAccount_IsGrantedManually(t *testing.T) {
	_, _, err := runAdmin(t, adminPassword,
		"admin", "create", "--username", "courtney", "--password-stdin")
	require.NoError(t, err)

	out, err := runAdminIn(t, adminPassword,
		"admin", "create", "--username", "second", "--role", authz.RoleKeyAdmin, "--password-stdin")
	require.NoError(t, err)
	require.Contains(t, out, "created second")
	require.NotContains(t, out, "first-run setup is complete",
		"the instance was already set up; only the account that ends first run says so")
}

// TestAdminCreate_UnknownRole_NamesTheBuiltins is the mistyped --role, and the error has to be
// self-correcting because the operator is at a prompt.
func TestAdminCreate_UnknownRole_NamesTheBuiltins(t *testing.T) {
	_, _, err := runAdmin(t, adminPassword,
		"admin", "create", "--username", "courtney", "--role", "onwer", "--password-stdin")
	require.ErrorIs(t, err, authz.ErrUnknownRole)
	require.Contains(t, err.Error(), authz.RoleKeyOwner)
}

// TestAdminBootstrap_NewToken_ArmsAndPrintsOnce is §9.2's recovery path: the token is dead, and the
// way back is a command on the host.
func TestAdminBootstrap_NewToken_ArmsAndPrintsOnce(t *testing.T) {
	out, dataDir, err := runAdmin(t, "", "admin", "bootstrap", "--new-token")
	require.NoError(t, err)

	path := filepath.Join(dataDir, authz.BootstrapTokenFileName)
	require.FileExists(t, path)

	contents, err := os.ReadFile(path)
	require.NoError(t, err)

	secret := strings.TrimSpace(string(contents))
	require.Len(t, secret, authz.BootstrapTokenLen)
	require.Contains(t, out, secret, "the token is printed to stdout as well as written to disk")
	require.Contains(t, out, path)
}

// TestAdminBootstrap_WithoutNewToken_IsRefused: minting a credential should be something somebody
// asked for in as many words.
func TestAdminBootstrap_WithoutNewToken_IsRefused(t *testing.T) {
	_, _, err := runAdmin(t, "", "admin", "bootstrap")
	require.Error(t, err)
	require.Contains(t, err.Error(), "--new-token")
}

// TestAdminBootstrap_BootstrappedInstance_IsRefused closes the backdoor: a token armed on a live
// instance is a credential for creating a second owner, printed into a log file.
func TestAdminBootstrap_BootstrappedInstance_IsRefused(t *testing.T) {
	_, dataDir, err := runAdmin(t, adminPassword,
		"admin", "create", "--username", "courtney", "--password-stdin")
	require.NoError(t, err)

	_, err = runAdminIn(t, "", "admin", "bootstrap", "--new-token")
	require.ErrorIs(t, err, authz.ErrAlreadyBootstrapped)

	require.NoFileExists(t, filepath.Join(dataDir, authz.BootstrapTokenFileName))
}

// TestAdmin_NoDatabasePath_SaysSo is the one misconfiguration every subcommand shares.
func TestAdmin_NoDatabasePath_SaysSo(t *testing.T) {
	t.Setenv(dbPathEnv, "")

	for _, args := range [][]string{
		{"admin", "create", "--username", "courtney", "--password-stdin"},
		{"admin", "bootstrap", "--new-token"},
	} {
		_, err := runAdminIn(t, adminPassword, args...)
		require.Errorf(t, err, "%v", args)
		require.Contains(t, err.Error(), dbPathEnv)
	}
}

// TestReadPassword_TrimsOnlyTheTrailingNewline: `printf`, `echo` and a heredoc all add one and no
// operator means it — but a password that legitimately ends in a space must survive, because a
// password nobody can retype is worse than a rejected one.
func TestReadPassword_TrimsOnlyTheTrailingNewline(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"plain\n":           "plain",
		"plain\r\n":         "plain",
		"plain":             "plain",
		"trailing space \n": "trailing space ",
		"inner  spaces\n":   "inner  spaces",
		"two\nlines\n":      "two\nlines",
	}

	for input, want := range cases {
		got, err := readPassword(strings.NewReader(input))
		require.NoErrorf(t, err, "input %q", input)
		require.Equalf(t, want, got, "input %q", input)
	}
}

// TestReadPassword_IsBounded stops `dkp admin create < /dev/urandom` from reading until the machine
// gives up, and reports a too-long password as too long rather than truncating it into one that is
// accepted and can never be typed again.
func TestReadPassword_IsBounded(t *testing.T) {
	t.Parallel()

	huge := strings.Repeat("a", 1<<20)

	got, err := readPassword(strings.NewReader(huge))
	require.NoError(t, err)
	require.Len(t, got, maxPasswordRead,
		"the reader must stop one byte past the policy ceiling, so an over-long password is refused "+
			"by the policy rather than silently truncated into an accepted one")
}
