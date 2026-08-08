import { fileURLToPath, URL } from "node:url";

import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

// The SPA is built to web/dist and embedded in the binary via go:embed (internal/ui). No SSR, no
// meta-framework, no BFF — see .claude/rules/web.md. Every asset is content-hashed so internal/ui
// can serve it with a one-year immutable cache; index.html is the only unhashed file and is the
// non-/api fallback.
export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: {
      // Matches the tsconfig `@/*` path. The generated client is imported as `@/api/...`, which is
      // the one import path .claude/rules/web.md sanctions for HTTP access.
      "@": fileURLToPath(new URL("./src", import.meta.url)),
    },
  },
  build: {
    // Emit into web/dist, which internal/ui embeds. Gitignored: the artefact is produced in CI and
    // baked into the binary, never committed.
    outDir: "dist",
    // Fail the build rather than silently shipping an oversized asset the budget would reject.
    // The 250 KB gzip initial-route budget is enforced separately by `make budget-bundle`.
    chunkSizeWarningLimit: 250,
    rollupOptions: {
      output: {
        // Content hashes are the whole reason internal/ui can send an immutable one-year cache
        // header. A file whose name changes when its bytes change can never be stale.
        entryFileNames: "assets/[name]-[hash].js",
        chunkFileNames: "assets/[name]-[hash].js",
        assetFileNames: "assets/[name]-[hash][extname]",
      },
    },
  },
  server: {
    // Vite on :5173, Go API on :8080. `make dev` runs both; the SPA reads API_BASE from the runtime
    // /config.json, so pointing it at the Go server is a proxy concern, not a rebuild.
    port: 5173,
    proxy: {
      "/api": "http://localhost:8080",
      "/config.json": "http://localhost:8080",
    },
  },
});
