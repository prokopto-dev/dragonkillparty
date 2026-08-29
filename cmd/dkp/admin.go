package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/prokopto-dev/dragonkillparty/internal/auth"
	"github.com/prokopto-dev/dragonkillparty/internal/authz"
	assignmentkinds "github.com/prokopto-dev/dragonkillparty/internal/authz/roleassignment/kinds"
	"github.com/prokopto-dev/dragonkillparty/internal/clock"
	"github.com/prokopto-dev/dragonkillparty/internal/store"
)

// `dkp admin` — the host-access half of first-run (docs/design/03-security.md §9.2, ROADMAP Phase 2
// deliverable 6).
//
// Wiring only, per the repo map: the account creation, the password policy, the role grant and the
// one-time token all live in internal/authz and internal/auth. This file resolves flags, opens the
// store, runs the same boot reconciliation `dkp serve` runs, and prints what happened.
//
// WHY THERE IS A CLI PATH AT ALL, when there is also a setup form. Three reasons, and each is a real
// operator on a real evening:
//
//   - THE TOKEN IS DEAD. Five wrong pastes, or sixty minutes went by. §9.2's recovery is
//     `dkp admin bootstrap --new-token`, and the bar it sets is deliberate: you must be able to run a
//     command on the host, which is strictly more than the token proves.
//   - THERE IS NO BROWSER. A headless box, a first deploy over ssh, a desktop install on a raid PC
//     that has not started yet.
//   - THE OWNER LEFT. Adding a second owner needs an account that can grant one, and the guild whose
//     only owner has stopped playing does not have one. Host access is the answer, and it is the same
//     answer as "somebody can read your database file", so it grants nothing that was not already
//     granted.
//
// THE PASSWORD IS READ FROM STDIN AND NEVER FROM A FLAG. A flag value is in the process table for
// every user on the box, in the shell history, and in whatever ships that history off the machine.
// There is no interactive prompt because a prompt without echo suppression would print the password
// to the terminal, and suppressing echo needs a dependency this project has not taken (AGENTS.md:
// a dependency is a human decision) — so the shape is the one Docker uses for exactly this reason:
//
//	printf '%s' "$DKP_OWNER_PASSWORD" | dkp admin create --username courtney --password-stdin

const (
	// argon2ProfileEnv selects the password-hashing cost profile, documented in
	// docs/operations/configuration.md.
	argon2ProfileEnv = "DKP_ARGON2_PROFILE"

	// passwordStdinFlag is named for what it does rather than for the secret it carries, so that a
	// shell history holding the command line holds no password. Docker's `--password-stdin` is the
	// same flag for the same reason, which is why it has that spelling.
	passwordStdinFlag = "password-stdin"

	// maxPasswordRead bounds how much of standard input is read. ONE BYTE PAST THE POLICY CEILING,
	// deliberately: a password over the ceiling has to be REPORTED as too long rather than truncated
	// into one that is accepted and can then never be typed again.
	maxPasswordRead = auth.MaxPasswordBytes + 1
)

// newAdminCmd builds `dkp admin`.
func newAdminCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "admin",
		Short: "Create and recover the accounts that administer this instance",
		Long: "Create and recover the accounts that administer this instance.\n\n" +
			"Every subcommand needs the database, which means it needs host access — and host\n" +
			"access is the bar first-run is set at: there is no default password on any DKP\n" +
			"instance, ever.\n\n" +
			"DKP_DB_PATH selects the SQLite database. Run `dkp migrate` first on a new install.",
	}

	cmd.AddCommand(newAdminCreateCmd(), newAdminBootstrapCmd())

	return cmd
}

// newAdminCreateCmd builds `dkp admin create`.
func newAdminCreateCmd() *cobra.Command {
	var (
		username      string
		displayName   string
		email         string
		roleKey       string
		passwordStdin bool
	)

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create an account and grant it a built-in role",
		Long: "Create an account with a local password and grant it a built-in role, globally.\n\n" +
			"On a fresh instance this is first-run: the account gets the `owner` role, which is\n" +
			"the only role that grants admin.owner, and the instance stops asking to be set up.\n" +
			"On a running instance it is how a second owner or an extra admin is added.\n\n" +
			"The password is read from standard input and is never accepted as a flag — a flag\n" +
			"value is visible in the process table and in shell history:\n\n" +
			"  printf '%s' \"$PASSWORD\" | dkp admin create --username courtney --password-stdin\n\n" +
			"It must be 12 to 128 bytes and must not contain the username, the email local part\n" +
			"or the guild name (docs/design/03-security.md §3.2).\n\n" +
			"The last account holding admin.owner cannot afterwards be demoted, disabled or\n" +
			"deleted — the database refuses it — so an instance cannot be locked out by an\n" +
			"officer tidying up the roster.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !passwordStdin {
				return fmt.Errorf("--%s is required: the password is read from standard input, "+
					"never from a flag", passwordStdinFlag)
			}

			if strings.TrimSpace(username) == "" {
				return errors.New("--username is required")
			}

			password, err := readPassword(cmd.InOrStdin())
			if err != nil {
				return err
			}

			return withAdminStore(cmd, func(ctx context.Context, st *store.Store) error {
				boot, err := adminBootstrapper(ctx, cmd, st)
				if err != nil {
					return err
				}

				before, err := boot.OwnerHolders(ctx)
				if err != nil {
					return err
				}

				account, err := boot.CreateAccount(ctx, authz.AccountParams{
					Username:    username,
					DisplayName: displayName,
					Email:       email,
					Password:    password,
					RoleKey:     roleKey,
					// `bootstrap` when this account is the one that ends first-run and `manual`
					// otherwise, because that distinction is the whole reason the column exists
					// (internal/authz/roleassignment/kinds): a bootstrap grant is the one nobody may
					// be left without, and a manual one is an officer's decision.
					GrantedVia: adminGrantedVia(before),
					GuildName:  guildName(ctx, st),
				})
				if err != nil {
					return err
				}

				return reportAccount(cmd.OutOrStdout(), account, username, before)
			})
		},
	}

	cmd.Flags().StringVar(&username, "username", "", "the login name (required)")
	cmd.Flags().StringVar(&displayName, "display-name", "", "the officer-facing name (defaults to the username)")
	cmd.Flags().StringVar(&email, "email", "", "optional email address")
	cmd.Flags().StringVar(&roleKey, "role", authz.RoleKeyOwner,
		"the built-in role to grant globally: owner, admin, officer, raid_leader, raider, member or guest")
	cmd.Flags().BoolVar(&passwordStdin, passwordStdinFlag, false,
		"read the password from standard input (required; the password is never a flag)")

	return cmd
}

// newAdminBootstrapCmd builds `dkp admin bootstrap`.
func newAdminBootstrapCmd() *cobra.Command {
	var newToken bool

	cmd := &cobra.Command{
		Use:   "bootstrap",
		Short: "Arm a fresh one-time first-run token",
		Long: "Arm a fresh one-time first-run token and print it once.\n\n" +
			"This is the recovery §9.2 names: a first-run token dies after five attempts or\n" +
			"sixty minutes, and re-arming it requires running a command on the host — which\n" +
			"proves host access and is the right bar.\n\n" +
			"It REFUSES an instance that already has an admin.owner holder. A token armed on a\n" +
			"live instance would be a credential for creating a second owner, printed into a log\n" +
			"file; `dkp admin create` is the way to add one, and it needs the same host access\n" +
			"without printing anything.\n\n" +
			"The token is written to <data-dir>/" + authz.BootstrapTokenFileName + " at 0600 as\n" +
			"well as to standard output.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// A flag rather than a bare `dkp admin bootstrap`, because §9.2 spells the recovery
			// `dkp admin bootstrap --new-token` and because minting a credential should be something
			// somebody asked for in as many words.
			if !newToken {
				return errors.New("--new-token is required: arming a token mints a credential, " +
					"so it is not something this command does by default")
			}

			return withAdminStore(cmd, func(ctx context.Context, st *store.Store) error {
				boot, err := adminBootstrapper(ctx, cmd, st)
				if err != nil {
					return err
				}

				armed, err := boot.Arm(ctx)
				if err != nil {
					return err
				}

				return reportArmed(cmd.OutOrStdout(), armed)
			})
		},
	}

	cmd.Flags().BoolVar(&newToken, "new-token", false, "arm a fresh token, invalidating any earlier one")

	return cmd
}

// withAdminStore opens DKP_DB_PATH, runs fn, and closes the store.
//
// It is the same open-and-defer-close `dkp seed` does, factored because both admin subcommands need
// it and because a missing DKP_DB_PATH deserves one message rather than two spellings of it.
func withAdminStore(cmd *cobra.Command, fn func(context.Context, *store.Store) error) error {
	dbPath := os.Getenv(dbPathEnv)
	if dbPath == "" {
		return fmt.Errorf("%s is not set", dbPathEnv)
	}

	st, err := store.Open(cmd.Context(), dbPath)
	if err != nil {
		return fmt.Errorf("open %s: %w", dbPath, err)
	}

	defer func() {
		if closeErr := st.Close(); closeErr != nil {
			slog.Error("close the database", "path", dbPath, "error", closeErr)
		}
	}()

	return fn(cmd.Context(), st)
}

// adminBootstrapper reconciles the permission catalogue and returns a Bootstrapper.
//
// THE RECONCILIATION IS NOT OPTIONAL HERE, and it is the same call `dkp serve` makes. A role
// assignment references role(id), the built-in roles are SEEDED AT BOOT rather than by a migration —
// role_permission is FK-constrained to permission(key) and that table is empty at migration time
// (db/queries/authz.sql) — so on a database that has been migrated and never served, the role rows do
// not exist yet and the grant would fail a foreign key with a message about an id nobody typed.
//
// The required set is empty because this command registers no routes. Reconcile's own contract says
// that is legitimate rather than a skip.
func adminBootstrapper(ctx context.Context, cmd *cobra.Command, st *store.Store) (*authz.Bootstrapper, error) {
	if _, err := authz.NewReconciler(st, clock.System{}).Reconcile(ctx, nil); err != nil {
		return nil, fmt.Errorf("%w\n\nOn a new install run `dkp migrate` first", err)
	}

	return authz.NewBootstrapper(st, clock.System{}, argon2Profile(ctx),
		resolveDataDir(os.Getenv(dbPathEnv))), nil
}

// adminGrantedVia picks role_assignment.granted_via from whether this instance had an owner.
func adminGrantedVia(ownersBefore int64) string {
	if ownersBefore == 0 {
		return assignmentkinds.GrantedViaBootstrap
	}

	return assignmentkinds.GrantedViaManual
}

// readPassword reads the whole of standard input as the password.
//
// THE WHOLE STREAM, with only a trailing newline trimmed. Trimming more would silently change a
// password that legitimately ends in a space, and a password nobody can retype is worse than a
// rejected one; the newline is trimmed because `printf` and `echo` and a heredoc all add one and no
// operator means it.
//
// It is bounded, because standard input is not: without the limit, `dkp admin create < /dev/urandom`
// reads until the machine gives up. The bound is one byte past the policy ceiling so that a password
// over it is REPORTED as too long rather than truncated into one that is accepted and then cannot be
// typed again.
func readPassword(in io.Reader) (string, error) {
	raw, err := io.ReadAll(io.LimitReader(in, maxPasswordRead))
	if err != nil {
		return "", fmt.Errorf("read the password from standard input: %w", err)
	}

	password := strings.TrimRight(string(raw), "\r\n")
	if password == "" {
		return "", errors.New("no password on standard input")
	}

	return password, nil
}

// argon2Profile resolves DKP_ARGON2_PROFILE.
//
// An unrecognised value logs and falls back to the default, which is readyDetailPolicy's posture and
// is the safe direction here too: `default` is the STRONGEST rung an operator reaches by typo, since
// every other named rung trades memory for iterations to be cheaper. Refusing to run over a typo in
// an optional performance setting would be worse than hashing at a cost somebody did not choose.
func argon2Profile(ctx context.Context) auth.Argon2Profile {
	profile, err := auth.ParseArgon2Profile(os.Getenv(argon2ProfileEnv))
	if err != nil {
		slog.WarnContext(ctx, "unrecognised argon2 profile; hashing at the default cost",
			"error", err, "env", argon2ProfileEnv, "profile", profile.Name)
	}

	return profile
}

// guildName reads the guild's name, for the password policy's third forbidden substring (§3.2).
//
// AN EMPTY STRING IS THE ORDINARY ANSWER ON A FRESH INSTANCE and is not an error: the guild row is
// written by the setup wizard, and first run happens before there is one. A failure to read is logged
// and treated the same way, because a database problem must not stop an operator creating the account
// that would let them look at it — and the two substring rules that DO apply, the username and the
// email local part, are the ones an attacker guesses first.
func guildName(ctx context.Context, st *store.Store) string {
	row, err := st.Q().GetGuild(ctx)
	if err != nil {
		slog.DebugContext(ctx, "no guild row to read a name from; the password policy checks the "+
			"username and the email local part only", "error", err)

		return ""
	}

	return row.Name
}

// reportAccount prints what `dkp admin create` did.
//
// IT NAMES THE FIRST-RUN CASE SEPARATELY, because the two outcomes are different events for the
// person reading them: creating an extra officer is bookkeeping, and creating the account that ends
// first-run is the moment an instance becomes usable and stops answering "set me up".
func reportAccount(out io.Writer, account authz.Account, username string, ownersBefore int64) error {
	lines := fmt.Sprintf("created %s (%s) with the %s role\n", username, account.UserID, account.RoleKey)

	if ownersBefore == 0 && account.RoleKey == authz.RoleKeyOwner {
		lines += "this instance now has an admin.owner holder; first-run setup is complete\n" +
			"any first-run token that was armed has been retired\n"
	}

	if _, err := io.WriteString(out, lines); err != nil {
		return fmt.Errorf("write summary: %w", err)
	}

	return nil
}

// reportArmed prints a freshly armed token — once, to stdout, exactly as §9.2 requires.
//
// THE SECRET IS PRINTED AND NEVER LOGGED. slog goes wherever an operator has pointed it, which may be
// a file, a journal or a log aggregator that a guild's whole Discord can read; stdout is the stream
// the person who just ran the command is looking at, and `docker logs dkp` is the documented way to
// recover it on a container. internal/authz logs that a token was armed and never what it says.
func reportArmed(out io.Writer, armed authz.ArmedToken) error {
	lines := "first-run token (this is the only time it is shown):\n\n" +
		"  " + armed.Secret + "\n\n" +
		fmt.Sprintf("it expires at %s, allows %d attempts in total, and is redeemed at /setup\n",
			armed.ExpiresAt, authz.BootstrapMaxAttempts)

	switch {
	case armed.FileErr != nil:
		lines += "it could NOT be written to disk (" + armed.FileErr.Error() + "), so this is the only copy\n"
	case armed.Path != "":
		lines += "a copy is at " + armed.Path + " (mode 0600); delete it once setup is done\n"
	}

	if _, err := io.WriteString(out, lines); err != nil {
		return fmt.Errorf("write summary: %w", err)
	}

	return nil
}
