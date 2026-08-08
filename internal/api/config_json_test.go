package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/api"
)

// TestConfigJSON_ReportsAPIBase asserts GET /config.json returns the API_BASE the server was
// configured with. The SPA reads this at boot to know where to send every request, so the wire key
// is API_BASE exactly — the SPA's RuntimeConfig interface binds to that literal.
func TestConfigJSON_ReportsAPIBase(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		base string
	}{
		{name: "same origin default", base: ""},
		{name: "absolute base", base: "https://dkp.example.org"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			srv := httptest.NewServer(api.New(api.Config{APIBase: tc.base}))
			t.Cleanup(srv.Close)

			res, err := http.Get(srv.URL + api.ConfigPath)
			require.NoError(t, err)
			t.Cleanup(func() { _ = res.Body.Close() })

			require.Equal(t, http.StatusOK, res.StatusCode)
			require.Equal(t, "application/json", res.Header.Get("Content-Type"))
			require.Equal(t, "no-store", res.Header.Get("Cache-Control"),
				"a stale API_BASE points the whole SPA at the wrong instance — it must not be cached")

			var body map[string]any
			require.NoError(t, json.NewDecoder(res.Body).Decode(&body))

			got, ok := body["API_BASE"]
			require.True(t, ok, "the body must carry the API_BASE key exactly")
			require.Equal(t, tc.base, got)

			// Exactly one key: a config document that grows silent fields is a config document the
			// SPA cannot trust. Adding a field is a deliberate change to spaConfig and to this count.
			require.Len(t, body, 1, "config.json must carry only API_BASE for now")
		})
	}
}

// TestConfigJSON_NotInSpec proves /config.json is not API surface: it must not appear in the OpenAPI
// document, because it is runtime configuration, not a versioned operation, and no SDK method should
// derive from it. This is the config.json half of the "raw handler, not a Huma operation" decision.
func TestConfigJSON_NotInSpec(t *testing.T) {
	t.Parallel()

	doc, err := api.SpecJSON()
	require.NoError(t, err)

	require.NotContains(t, string(doc), api.ConfigPath,
		"/config.json is runtime config, not API surface — it must not be in the spec")
}
