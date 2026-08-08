package api

import (
	"encoding/json"
	"net/http"
)

// ConfigPath serves the SPA's runtime configuration.
//
// Deliberately at the ROOT, not under /api/v1 and not a Huma operation. It is not API surface: no
// bot consumes it, no SDK method derives from it, and it carries no versioned contract. It exists so
// the built SPA bundle can be pointed at a different instance WITHOUT a rebuild — the browser reads
// /config.json at boot and prepends API_BASE to every request the generated client makes. That the
// API base is a runtime value rather than a build-time env is the proof the SPA is a client
// (.claude/rules/web.md), so this endpoint is the mechanism behind that claim.
//
// It is a raw net/http handler for the same reason /healthz is: it serves a fixed JSON shape that is
// not problem+json and has no place in the published spec. registerConfigJSON mounts it on the mux
// alongside /healthz, before the Huma tree.
const ConfigPath = "/config.json"

// spaConfig is the exact body of GET /config.json.
//
// One field today. It is a struct rather than a map so the wire shape is a single typed thing the
// SPA's RuntimeConfig interface mirrors, and so adding a field is a visible change here rather than a
// silent key appearing in a map literal.
type spaConfig struct {
	// APIBase is prepended by the SPA's generated client to every request path. Empty means
	// same-origin, which is what a co-hosted binary serves and the overwhelming common case. A
	// reverse-proxied or split deploy sets DKP_API_BASE to the absolute base of the API.
	APIBase string `json:"API_BASE"`
}

// registerConfigJSON mounts GET /config.json on the mux.
//
// apiBase is captured once at construction from api.Config, which reads it from DKP_API_BASE in
// cmd/dkp. Reading the env here instead would make internal/api reach into the process environment,
// which is cmd/'s job, and would make the handler untestable without setting a global.
func registerConfigJSON(mux *http.ServeMux, apiBase string) {
	body := spaConfig{APIBase: apiBase}

	mux.HandleFunc("GET "+ConfigPath, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// no-store: the config is cheap and a stale API_BASE points the whole SPA at the wrong
		// instance. Correctness beats the one saved request.
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusOK)
		// Encode rather than a string constant so a future field cannot be misquoted by hand. The
		// error is unreachable for this fixed struct, and there is nothing useful to do with it after
		// a header is written, so it is deliberately dropped.
		_ = json.NewEncoder(w).Encode(body)
	})
}
