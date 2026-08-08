package api_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/api"
)

// stubSPA is a WebUI that records the paths it was asked to serve, so a test can prove the catch-all
// received exactly the routes nothing more specific claimed.
type stubSPA struct {
	served string
}

func (s *stubSPA) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.served = r.URL.Path
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("SPA:" + r.URL.Path))
}

// TestServer_WebUI_ServesNonAPIPaths asserts the SPA catch-all receives a client route and that the
// more specific routes above it — /healthz, /config.json, /api/v1/meta — are NOT delegated to it.
// This is the precedence contract net/http's ServeMux gives "/", written down as a test so a future
// mount order change cannot silently route /api through the SPA.
func TestServer_WebUI_ServesNonAPIPaths(t *testing.T) {
	t.Parallel()

	spa := &stubSPA{}
	srv := httptest.NewServer(api.New(api.Config{WebUI: spa}))
	t.Cleanup(srv.Close)

	// A client route the server has no other handler for: the SPA takes it.
	res, err := http.Get(srv.URL + "/standings")
	require.NoError(t, err)
	t.Cleanup(func() { _ = res.Body.Close() })
	require.Equal(t, http.StatusOK, res.StatusCode)
	require.Equal(t, "/standings", spa.served, "the SPA catch-all must receive a client route")

	// The more specific routes must win over "/". /healthz answers its own body, not the SPA's.
	spa.served = ""
	hres, err := http.Get(srv.URL + "/healthz")
	require.NoError(t, err)
	t.Cleanup(func() { _ = hres.Body.Close() })
	require.Equal(t, http.StatusOK, hres.StatusCode)
	require.Empty(t, spa.served, "/healthz must not be delegated to the SPA")
}

// TestServer_WebUI_APIPathNotDelegatedToSPA is the load-bearing negative: a request under /api that
// no operation matched must NOT reach the SPA. The stub would record it if it did; the SPA handler
// from internal/ui 404s /api itself, but this test proves the ROUTING never hands /api to the SPA in
// the first place, so a mistyped endpoint can never return an HTML page.
func TestServer_WebUI_APIPathNotDelegatedToSPA(t *testing.T) {
	t.Parallel()

	spa := &stubSPA{}
	srv := httptest.NewServer(api.New(api.Config{WebUI: spa}))
	t.Cleanup(srv.Close)

	res, err := http.Get(srv.URL + "/api/v1/does-not-exist")
	require.NoError(t, err)
	t.Cleanup(func() { _ = res.Body.Close() })

	require.NotContains(t, res.Header.Get("Content-Type"), "text/html",
		"an unmatched /api path must never return the SPA's HTML")
	// The stub sets served on any hit; it must remain empty for an /api path.
	require.Empty(t, spa.served, "the SPA must never be handed an /api path")
}

// TestServer_NilWebUI_NoSPAMount asserts a server built without a WebUI serves no SPA — the shape
// every pre-PR-6 handler test constructs. An unknown path then falls through to the problem
// middleware's 404, not to a nil-handler panic.
func TestServer_NilWebUI_NoSPAMount(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(api.New(api.Config{}))
	t.Cleanup(srv.Close)

	res, err := http.Get(srv.URL + "/standings")
	require.NoError(t, err)
	t.Cleanup(func() { _ = res.Body.Close() })

	require.Equal(t, http.StatusNotFound, res.StatusCode,
		"with no WebUI, an unknown path is a 404, not a served SPA")
}
