import { StrictMode } from "react";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider } from "@tanstack/react-router";
import { createRoot } from "react-dom/client";

import { bootstrapClient } from "@/api/client";

import { router } from "./router";
import "./styles/base.css";

// Boot order is load-bearing:
//   1. read /config.json so the client knows its API_BASE before any request fires;
//   2. configure the client with that base;
//   3. render.
// Rendering first would let a query fire against the wrong origin on the first paint.
//
// bootstrapClient does steps 1 and 2. It lives in @/api/client — the one sanctioned place a raw
// fetch may appear, because it fetches the CLIENT's own base URL and so cannot itself route through
// the client. WEB001's allowlist (^web/src/api/) and eslint's src/api exemption both cover it.
async function boot() {
  await bootstrapClient();

  const queryClient = new QueryClient({
    defaultOptions: {
      queries: {
        // Reads discriminate errors on error.code from the closed enum; retrying a 4xx problem is
        // pointless, so let the query surface it to the error boundary instead.
        retry: false,
      },
    },
  });

  const rootElement = document.getElementById("root");
  if (!rootElement) {
    throw new Error("missing #root element");
  }

  createRoot(rootElement).render(
    <StrictMode>
      <QueryClientProvider client={queryClient}>
        <RouterProvider router={router} />
      </QueryClientProvider>
    </StrictMode>,
  );
}

void boot();
