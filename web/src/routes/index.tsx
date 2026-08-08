import { queryOptions, useSuspenseQuery } from "@tanstack/react-query";
import { createRoute } from "@tanstack/react-router";

import { api } from "@/api/client";

import { rootRoute } from "./root";

// The one query the current spec supports: GET /api/v1/meta. The guild resource (PR 5a) is being
// built in parallel and is not on this branch, so the scaffold demonstrates the sanctioned pattern
// against the only operation that exists. When 5a merges and the client regenerates, adding a guild
// query is copy-this-file-change-the-nouns.
//
// Query key = the operationId plus its parameters. Nothing else (.claude/rules/web.md).
const metaQuery = queryOptions({
  queryKey: ["getMeta"],
  queryFn: async ({ signal }) => {
    const { data, error } = await api.GET("/api/v1/meta", { signal });
    if (error) {
      // A problem+json body, discriminated on error.code. Reads throw so the error boundary and the
      // suspense boundary above handle it — no per-component try/catch.
      throw error;
    }
    return data;
  },
});

export const indexRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/",
  component: Landing,
});

function Landing() {
  const { data } = useSuspenseQuery(metaQuery);

  return (
    <main className="landing">
      <h1>Dragon Kill Party</h1>
      <p className="landing-sub">
        DKP and guild management for Project 1999 EverQuest raiding guilds.
      </p>
      <dl className="landing-meta">
        <div>
          <dt>Version</dt>
          <dd>{data.server.version}</dd>
        </div>
        <div>
          <dt>Spec</dt>
          <dd>{data.spec_version}</dd>
        </div>
        <div>
          <dt>Commit</dt>
          <dd>{data.server.commit}</dd>
        </div>
      </dl>
    </main>
  );
}
