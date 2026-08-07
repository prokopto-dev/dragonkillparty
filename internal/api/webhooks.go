package api

import (
	"github.com/danielgtaylor/huma/v2"
)

// WebhookPlaceholderEvent is the name of the single placeholder entry in the `webhooks` block.
//
// Exported so the test and scripts/verify-spec.sh can refer to it by name rather than by a string
// literal repeated in three places.
const WebhookPlaceholderEvent = "ping"

// webhookPlaceholder returns the OpenAPI 3.1 `webhooks` block.
//
// THIS IS ITEM V7 OF docs/development/verify-before-phase-0.md, and it is deliberately one entry.
//
// The question V7 asks is whether the "one document" promise holds — whether webhooks can live in
// the same OpenAPI file as the REST surface, be generated into both SDKs, and be rendered by the
// embedded reference. `webhooks` is a top-level OpenAPI 3.1 keyword with no 3.0 equivalent, so it is
// the single construct most likely to make a 3.0-era generator fall over. PR 4 answers the first
// half: Huma can emit it and the document still parses. PR 6 runs openapi-typescript,
// openapi-python-client and Scalar over the committed file and answers the rest.
//
// The event is called `ping` and carries nothing but a timestamp, on purpose. The real catalogue is
// generated from openapi/registry/events.yaml (docs/design/02-api-design.md:551-563) and its event
// names follow canonical §16's resource.past_tense_verb shape — `bid_session.settled` and the like.
// Writing one of those here would mean inventing a payload for an event no code emits, which is a
// guess shipped into a committed, diff-gated artefact. A placeholder that is obviously a placeholder
// is the honest way to test the mechanism.
func webhookPlaceholder() map[string]*huma.PathItem {
	return map[string]*huma.PathItem{
		WebhookPlaceholderEvent: {
			Post: &huma.Operation{
				OperationID: "webhookPing",
				Summary:     "Placeholder webhook event",
				Description: "A no-op event that exists to prove the `webhooks` block round-trips " +
					"through the spec pipeline. Replaced by the generated event catalogue; it is " +
					"not emitted by any code and should not be subscribed to.",
				Tags: []string{"webhooks"},
				RequestBody: &huma.RequestBody{
					Required: true,
					Content: map[string]*huma.MediaType{
						"application/json": {
							Schema: &huma.Schema{
								Type:     "object",
								Required: []string{"event", "occurred_at"},
								Properties: map[string]*huma.Schema{
									"event": {
										Type:        "string",
										Description: "The event name.",
										Enum:        []any{WebhookPlaceholderEvent},
									},
									"occurred_at": {
										Type:        "string",
										Format:      "date-time",
										Description: "RFC 3339 timestamp, always UTC (canonical §2).",
									},
								},
							},
						},
					},
				},
				Responses: map[string]*huma.Response{
					"204": {
						Description: "The consumer accepted the event. Any 2xx is treated as success.",
					},
				},
			},
		},
	}
}
