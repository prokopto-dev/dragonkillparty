package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/api"
	"github.com/prokopto-dev/dragonkillparty/internal/auth"
	"github.com/prokopto-dev/dragonkillparty/internal/authz"
	assignmentkinds "github.com/prokopto-dev/dragonkillparty/internal/authz/roleassignment/kinds"
	"github.com/prokopto-dev/dragonkillparty/internal/clock"
	"github.com/prokopto-dev/dragonkillparty/internal/core"
	"github.com/prokopto-dev/dragonkillparty/internal/store"
	"github.com/prokopto-dev/dragonkillparty/internal/store/sqlitegen"
)

// The shared harness for the two write-safety middleware — idempotency_test.go and
// precondition_test.go — because both need the same thing and neither can get it from the registry.
//
// WHY THERE IS A HARNESS AT ALL. Canonical §7 fences every mutating POST that creates domain state
// and requires If-Match on every transition; the first of each ships two waves from now, so an
// honest test of either middleware has to supply the operation. api.NewWithOperations
// (export_test.go) registers one into the REAL tree, behind the REAL middleware, in the REAL order,
// which is the difference between "this middleware works" and "the product runs this middleware".
//
// A FULLY BOOTED INSTANCE, not a minimal one. The middleware under test runs downstream of the
// authorization gate and the credential-and-capability check, so a fixture that skipped either would
// let a 503 or a 403 stand in for the assertion. Everything here is stated for the reason
// auth_test.go's fixture states it: the zero values of Authorization and Auth REFUSE.

// instance is a booted server with one credential of each class and whatever operations a test
// registered.
type instance struct {
	server *httptest.Server
	clock  *clock.Fake
	store  *store.Store
	keys   *auth.Keyring
	auth   *auth.Service

	user   core.ULID
	cookie *http.Cookie
	bot    core.ULID
	token  string
}

// newInstance boots a server whose extra operations `register` declares.
//
// The roles are the two that reach the operations these tests register: `admin` gives the human
// raid.tick.create (through officer → raid_leader) and admin.settings; `bot_raid` is the
// service-account role that records ticks, and the token's `raids:write` scope then narrows it —
// the intersection the capability middleware enforces upstream of the two under test here.
func newInstance(t *testing.T, register func(huma.API)) instance {
	t.Helper()

	st := store.NewDB(t)
	clk := clock.NewFake(time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC))
	keys := auth.NewTestKeyring(t)
	svc := auth.NewService(st, clk, keys)

	// The guild singleton, so an unfenced control that reads it answers 200 rather than the 404 an
	// unseeded instance correctly gives.
	err := st.Tx(t.Context(), func(ctx context.Context, q store.Queries) error {
		_, insErr := q.InsertGuild(ctx, sqlitegen.InsertGuildParams{
			Name: "Kittens Who Say Ni", Tag: "KWSN", Timezone: "America/New_York",
			WeekStart: 1, PointsLabel: "DKP", PointsPrecision: 2,
			CreatedAt: 1_000, UpdatedAt: 1_000,
		})

		return insErr
	})
	require.NoError(t, err, "seed the guild row")

	// The boot step: a migrated database has an empty permission table — the catalogue is projected
	// into it by authz.Reconcile on the boot path, not by a migration — so without this every
	// operation answers 503 and nothing below exercises the middleware.
	authz.Boot(t, st, clk)

	user := auth.SeedUser(t, st, clk, "officer")
	cookie, _ := auth.SeedSession(t, svc, user)
	bot := auth.SeedServiceAccount(t, st, clk, user, "raidbot")

	authz.GrantRole(t, st, clk, assignmentkinds.SubjectKindUser, user, authz.RoleIDAdmin)
	authz.GrantRole(t, st, clk, assignmentkinds.SubjectKindServiceAccount, bot, authz.RoleIDBotRaid)

	token := auth.SeedToken(t, st, keys, clk, auth.SeedTokenParams{
		ServiceAccount: bot, CreatedBy: user, Scopes: "raids:write",
	})

	srv := httptest.NewServer(api.NewWithOperations(api.Config{
		Store:         st,
		Clock:         clk,
		Auth:          svc,
		Authorization: api.AuthorizationReconciled(),
	}, register))
	t.Cleanup(srv.Close)

	return instance{
		server: srv, clock: clk, store: st, keys: keys, auth: svc,
		user: user, cookie: cookie, bot: bot, token: token,
	}
}

// call issues one request against the instance. headers is applied last, so a test can send an empty
// value to prove an ABSENT header and a present-but-empty one are treated the same.
func (in instance) call(
	t *testing.T,
	method, path, body string,
	headers map[string]string,
	cookie *http.Cookie,
	bearer string,
) *http.Response {
	t.Helper()

	var reader *strings.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}

	var req *http.Request

	var err error

	if reader == nil {
		req, err = http.NewRequestWithContext(context.Background(), method, in.server.URL+path, nil)
	} else {
		req, err = http.NewRequestWithContext(context.Background(), method, in.server.URL+path, reader)
	}

	require.NoError(t, err)

	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}

	for name, value := range headers {
		req.Header.Set(name, value)
	}

	if cookie != nil {
		req.AddCookie(cookie)
	}

	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}

	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = res.Body.Close() })

	return res
}
