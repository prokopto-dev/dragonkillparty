---
paths: ["web/src/**"]
description: The SPA is an API client — generated types only, TanStack Query patterns, the useEffect-fetch ban, and the bundle budget.
---

# Web

React 19 + Vite 7 + TanStack Router v1 + TanStack Query v5, built to `web/dist` and embedded in the
binary via `go:embed`. No SSR, no meta-framework, no server components, no BFF.

## The generated client is the only client

```
Go handler types ──huma──► openapi/openapi.json ──openapi-typescript──► web/src/api/schema.d.ts
                                                 ──openapi-fetch───────► web/src/api/client.ts
```

`web/src/api/` is **generated**. Never hand-edit it; run `make gen` and commit the diff.

| Banned | Instead |
|---|---|
| `fetch(...)` / `XMLHttpRequest` outside `web/src/api` | the generated client — ESLint `no-restricted-globals`, a required CI gate |
| A hand-written request or response `interface` | `import type { components } from "@/api/schema"` |
| A hand-written URL string | the typed path from the generated client |
| `axios`, `ky`, or any second HTTP library | there is one client |

If a response type you need does not exist in `schema.d.ts`, **the spec does not have it** and the
answer is an API change, not a local type. Writing the type by hand makes the SPA silently disagree
with every bot the moment the server changes.

Money is `*_centipoints`, an unquoted integer. Divide by 100 for display using the guild's
`points_precision`; never do arithmetic on a formatted string and never introduce a decimal type.
Realistic values are ~10¹¹, four orders below `MAX_SAFE_INTEGER`, so plain `number` is safe.

## TanStack Query patterns

```ts
// Query key = the operationId plus its parameters. Nothing else.
export const raidQuery = (id: string) => queryOptions({
  queryKey: ["getRaid", id],
  queryFn: async ({ signal }) => {
    const { data, error } = await api.GET("/api/v1/raids/{raid_id}", {
      params: { path: { raid_id: id } }, signal,
    });
    if (error) throw error;   // a problem+json body, discriminated on error.code
    return data;
  },
});
```

- **Every read goes through `useQuery`/`useSuspenseQuery`.** Every write goes through `useMutation`
  and ends in `queryClient.invalidateQueries`.
- **`useEffect` containing a fetch is banned** — a custom ESLint rule fails the build. It
  re-fetches on every render path you did not think about, has no cancellation, no dedupe, no cache
  and no error boundary, and it is how a standings page ends up making 200 requests.
- Mutations set `Idempotency-Key` (generate one per user intent, not per attempt) and `If-Match`
  where the operation requires it. A `412` carries `meta.current` — merge and retry, do not just
  surface an error.
- Errors discriminate on `error.code` from the closed enum, never on the message text and never on
  the `type` URI.
- Never mutate the cache with data from an SSE frame. Frames carry ids; invalidate and refetch
  through the same endpoint everything else uses. There is exactly one representation of a resource.

```ts
// SSE: one EventSource, multiplexed topics. HTTP/1.1 caps 6 connections per origin.
es.addEventListener("bid_session.bid_placed", (e) => {
  const { resource } = JSON.parse(e.data);
  queryClient.invalidateQueries({ queryKey: ["getBidSession", idFrom(resource)] });
});
```

Two failed `EventSource` connects → degrade to 2-second polling with a visible "live updates
unavailable, polling" indicator. At 30–70 concurrent clients polling is genuinely free.

**Countdowns render `closes_at − server_time + local_elapsed`**, never the client's own clock. Every
frame and every bid response carries `server_time`.

## Budgets and gates

| Gate | Limit | Where |
|---|---|---|
| Initial-route bundle | **≤ 250 KB gzipped**, hard fail | `advisory / bundle-size` with a committed budget file |
| Accessibility | zero serious/critical on primary routes | axe in the Playwright run |
| Traffic conformance | every observed `(method, path-template)` exists in `openapi.json` | recording proxy over the Playwright run |
| PAT parity | SPA request sequences replayed with a scoped PAT return identical responses | integration suite |

Standings for 200 characters × 12 columns is the heaviest view: **TanStack Virtual with server-side
sort and filter**. Never fetch the full collection and sort in the browser — the budget exists
because a volunteer's Raspberry Pi is the deployment target.

## House rules

- **No bare user-facing string literals in JSX.** English-only at 1.0, but every string goes through
  the message catalogue from Phase 3 — an ESLint rule enforces it. Retrofitting i18n costs 10×.
- **`dangerouslySetInnerHTML` is banned** by lint. Sanitised HTML arrives as `body_html` from
  `internal/richtext` and renders through the one approved component.
- `API_BASE` comes from a runtime `/config.json`, not from build-time env, so the SPA can be pointed
  at a remote instance. That capability is the proof it is a client.
- Guild-configurable class colours go through the contrast validator — an officer will otherwise
  pick an unreadable one, and there is a unit test.
- No React component snapshot tests. They are brittle and low-value; the Playwright journeys and the
  integration suite carry the weight.
- `web/src/dev/` (the "copy as curl" request inspector) is dev-only and must be tree-shaken out of
  the production bundle.

## A UI need is an API change

There are **no UI-private endpoints** and three CI gates prove it. If a screen needs a capability:

1. Add it to the public API following `internal/api/EXAMPLE_ENDPOINT.md`.
2. `make gen`, commit the spec and client diffs.
3. Use it from `web/src`.

Saved table layouts are a real API resource (`/api/v1/table-views`), not a localStorage blob — so a
bot can read standings in exactly the columns the officers see. That is the pattern for anything
that feels UI-shaped.

## Stop and ask if

- You want a response field that no operation returns.
- You want to hold authoritative state in the client, or compute a balance in the browser.
- The design needs a request the published spec cannot express.
