// The worked example from internal/api/EXAMPLE_ENDPOINT.md step 6, as real, type-checked source.
//
// EXAMPLE_ENDPOINT.md's ```ts fence is a transcription of this file. The doc's fence is prose to the
// snippet-compile gate (it only builds ```go/```sql/```hcl), so THIS file is where the TypeScript
// caller of the generated client is actually held to the spec: `make vet` runs `tsc --noEmit` over
// web/src, and the CI `typecheck` job (node:"true") does the same. If openapi-typescript regenerates
// schema.d.ts and `getGuild`/`updateGuild` change shape, this file stops compiling — which is the
// point. Keep it and the doc fence in step with each other.
//
// It is a module of pure functions (no React) so it type-checks without pulling in a component tree;
// the TanStack Query wrappers around them live in .claude/rules/web.md and web/src/routes.
import { api } from "@/api/client";

import type { components } from "@/api/schema";

// The response DTO and the partial-update body come from the generated schema — never hand-written.
// If a field you need is absent here, the spec does not have it and the answer is an API change.
type GuildDTO = components["schemas"]["GuildDTO"];
type UpdateGuildInput = components["schemas"]["UpdateGuildInputBody"];

// A GET (guild + its ETag). The generated client resolves the path against schema.d.ts: mistype
// "/api/v1/guild" and this line stops compiling. `data` is typed as GuildDTO; `error` is the
// problem+json body, discriminated on error.code. The ETag comes off the raw Response, and a caller
// stores it to send back as If-Match on the next PATCH.
export async function getGuild(
  signal?: AbortSignal,
): Promise<{ guild: GuildDTO; etag: string | null }> {
  const { data, error, response } = await api.GET("/api/v1/guild", { signal });
  if (error) {
    throw error;
  }

  return { guild: data, etag: response.headers.get("ETag") };
}

// A PATCH under an If-Match precondition. `body` is the partial UpdateGuildInput — only the fields
// being changed. If-Match is the ETag from a prior getGuild; a missing one is 428 and a stale one is
// 412 with the current representation in meta.current (merge and retry, per .claude/rules/web.md).
// Idempotency-Key is generated once per user intent, not per attempt.
export async function updateGuild(
  body: UpdateGuildInput,
  ifMatch: string,
  idempotencyKey: string,
  signal?: AbortSignal,
): Promise<GuildDTO> {
  const { data, error } = await api.PATCH("/api/v1/guild", {
    params: { header: { "If-Match": ifMatch } },
    headers: { "Idempotency-Key": idempotencyKey },
    body,
    signal,
  });
  if (error) {
    throw error;
  }

  return data;
}
