// Command enumgen writes every enum catalogue into db/schema.hcl's generated regions.
//
// It is the `make gen` half of canonical §5 ("the enum catalogue is a Go const block; make gen
// writes it into the migration CHECK"): internal/ledger/kinds holds ledger_batch's kind and source,
// internal/audit/kinds holds audit_log's actor_kind and outcome, internal/account/kinds holds
// account's kind and system_key, internal/decay/kinds holds decay_run's state,
// internal/authz/role/kinds holds role's applies_to, internal/authz/roleassignment/kinds holds
// role_assignment's subject_kind, scope_type and granted_via, and the auth quartet holds one table
// each — internal/auth/appuser/kinds (app_user.state), internal/auth/useridentity/kinds
// (user_identity.provider and password_algo), internal/auth/serviceaccount/kinds
// (service_account.state) and internal/auth/feedtoken/kinds (feed_token.kind); this rewrites their
// CHECK constraints from them, and
// `make migration` turns the resulting schema into SQL. It never writes a migration itself, for
// the reason scripts/gen-db.sh gives — `make gen` runs reflexively and must not create numbered,
// permanent, append-only files as a side effect.
//
// A GENERATOR, NOT A GATE. It rewrites and says nothing; the drift assertions are
// TestLedgerKinds_CheckMatchesCatalogue, TestAuditKinds_CheckMatchesCatalogue,
// TestAccountKinds_CheckMatchesCatalogue, TestDecayKinds_CheckMatchesCatalogue,
// TestRoleKinds_CheckMatchesCatalogue, TestRoleAssignmentKinds_CheckMatchesCatalogue, the four
// auth catalogues' own CheckMatchesCatalogue tests and
// `make verify-generated`. Keeping the two apart is what
// lets `make gen` be the fix rather than another thing to interpret.
//
// It lives beside the ledger catalogue rather than in cmd/dkp because it is dev tooling: cmd/dkp is
// the product binary and an officer never runs a code generator. It stays here, rather than moving
// somewhere neutral now that it serves ten catalogues, because moving a command renames the path in
// scripts/gen-enums.sh, the Makefile and test/repo/verify_generated_test.go to buy tidiness and
// nothing else — and its name, not its parent directory, is what a reader looks up.
//
// It imports the ten catalogues and NOTHING ELSE from this repository. Importing internal/ledger,
// internal/authz, internal/auth or internal/audit's future service package would pull in
// internal/store/sqlitegen —
// generated code — and make `make gen` unable to repair a tree whose generated code does not build.
// See the package comment on internal/ledger/kinds.
package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	accountkinds "github.com/prokopto-dev/dragonkillparty/internal/account/kinds"
	auditkinds "github.com/prokopto-dev/dragonkillparty/internal/audit/kinds"
	appuserkinds "github.com/prokopto-dev/dragonkillparty/internal/auth/appuser/kinds"
	feedtokenkinds "github.com/prokopto-dev/dragonkillparty/internal/auth/feedtoken/kinds"
	serviceaccountkinds "github.com/prokopto-dev/dragonkillparty/internal/auth/serviceaccount/kinds"
	useridentitykinds "github.com/prokopto-dev/dragonkillparty/internal/auth/useridentity/kinds"
	rolekinds "github.com/prokopto-dev/dragonkillparty/internal/authz/role/kinds"
	assignmentkinds "github.com/prokopto-dev/dragonkillparty/internal/authz/roleassignment/kinds"
	decaykinds "github.com/prokopto-dev/dragonkillparty/internal/decay/kinds"
	"github.com/prokopto-dev/dragonkillparty/internal/ledger/kinds"
	"github.com/prokopto-dev/dragonkillparty/internal/schemaenum"
)

// defaultSchemaPath is relative to the repo root, which is where `make gen` runs its scripts from.
const defaultSchemaPath = "db/schema.hcl"

func main() {
	path := defaultSchemaPath
	if len(os.Args) > 1 {
		path = os.Args[1]
	}

	if err := run(path); err != nil {
		fmt.Fprintf(os.Stderr, "enumgen: %v\n", err)
		os.Exit(1)
	}
}

// catalogue is one enum catalogue: its import path, for the error message, and the render that
// rewrites its region of db/schema.hcl.
type catalogue struct {
	name   string
	render func(string) (string, error)
}

// catalogues returns every generated region of db/schema.hcl and the render that owns it, in the
// order run applies them.
//
// Each render touches only its own marked region and is idempotent, so composing them is ordinary
// function application and the order is not load-bearing — it is fixed only so that a failure reports
// the same catalogue on every run. A FUNCTION rather than a package-level slice, for the reason the
// catalogues' own accessors are functions: .claude/rules/go-idioms.md bans package-level mutable
// state.
//
// ADDING A CATALOGUE IS ONE ROW HERE. That is the whole extension point; nothing below this line
// knows how many regions exist.
func catalogues() []catalogue {
	return []catalogue{
		{name: "internal/ledger/kinds", render: kinds.RenderSchemaHCL},
		{name: "internal/audit/kinds", render: auditkinds.RenderSchemaHCL},
		{name: "internal/account/kinds", render: accountkinds.RenderSchemaHCL},
		{name: "internal/decay/kinds", render: decaykinds.RenderSchemaHCL},
		// The RBAC pair. Two packages for two tables rather than one for the subsystem: a catalogue
		// owns its region through a schemaEnumBegin/schemaEnumEnd const pair, ENUM001 matches those by
		// identifier name, and two pairs cannot live in one Go package. Each package's comment says so.
		{name: "internal/authz/role/kinds", render: rolekinds.RenderSchemaHCL},
		{name: "internal/authz/roleassignment/kinds", render: assignmentkinds.RenderSchemaHCL},
		// The auth quartet (Phase 2 Wave 0d, issue #273), and it is four packages for the same reason
		// the RBAC pair is two: four tables carry a string enum, and a region cannot span two `table`
		// blocks. app_user and service_account both govern a column called `state` with DIFFERENT
		// vocabularies, which is the case that makes merging them impossible rather than merely
		// untidy.
		{name: "internal/auth/appuser/kinds", render: appuserkinds.RenderSchemaHCL},
		{name: "internal/auth/useridentity/kinds", render: useridentitykinds.RenderSchemaHCL},
		{name: "internal/auth/serviceaccount/kinds", render: serviceaccountkinds.RenderSchemaHCL},
		{name: "internal/auth/feedtoken/kinds", render: feedtokenkinds.RenderSchemaHCL},
	}
}

// run reads path, re-renders every generated region from its catalogue and writes the result back
// only when the bytes actually change.
//
// The write is skipped on a no-op so that `make gen` on an up-to-date tree leaves mtimes alone —
// touching the single source of schema truth on every run would make every `make` in the repository
// look like the schema had moved. It is skipped ONCE, after every render: a per-render write would
// rewrite the file for a change to a region that came later anyway.
func run(path string) error {
	src, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}

	out := string(src)

	for _, c := range catalogues() {
		out, err = c.render(out)
		if err != nil {
			if errors.Is(err, schemaenum.ErrMarkersMissing) {
				return fmt.Errorf("render %s from %s: %w\n\nthe markers delimit the region that catalogue "+
					"owns; without them it cannot tell generated lines from hand-authored schema",
					path, c.name, err)
			}

			return fmt.Errorf("render %s from %s: %w", path, c.name, err)
		}
	}

	if out == string(src) {
		return nil
	}

	return writeAtomic(path, []byte(out))
}

// writeAtomic replaces path's contents through a temp file in the same directory and a rename, so an
// interrupted run cannot leave a truncated db/schema.hcl behind.
//
// Same reasoning as scripts/gen-openapi.sh's tmp-then-move: the obvious direct write truncates the
// target before it knows it has anything to put there, and this particular target is the file every
// migration in the repository is diffed against.
func writeAtomic(path string, content []byte) error {
	dir := filepath.Dir(path)

	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".enumgen-*")
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", dir, err)
	}

	tmpName := tmp.Name()

	defer func() {
		// Best-effort cleanup of the temp file on the failure paths; on success the rename has
		// already moved it and this removes nothing.
		_ = os.Remove(tmpName)
	}()

	if _, err = tmp.Write(content); err != nil {
		_ = tmp.Close()

		return fmt.Errorf("write %s: %w", tmpName, err)
	}

	if err = tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmpName, err)
	}

	// CreateTemp makes the file 0600. The target is a committed, world-readable source file, and a
	// generator that quietly narrowed its mode would surprise the next container build.
	if err = os.Chmod(tmpName, 0o644); err != nil {
		return fmt.Errorf("chmod %s: %w", tmpName, err)
	}

	if err = os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename %s to %s: %w", tmpName, path, err)
	}

	return nil
}
