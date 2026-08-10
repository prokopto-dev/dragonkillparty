package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/prokopto-dev/dragonkillparty/internal/core"
	"github.com/prokopto-dev/dragonkillparty/internal/guild"
)

// This file is the whole HTTP surface of the guild resource: the two operations GET and PATCH
// /api/v1/guild, their input and output structs, and the mapping between the domain guild.Guild and
// the wire GuildDTO. One file per resource (.claude/rules/api-endpoints.md); never a shared registry
// file, which conflicts on every parallel feature PR.
//
// The handlers make no domain decision. They marshal the request into a call on internal/guild,
// marshal the result back, and translate the service's sentinel errors into the closed error enum.
// Whether an If-Match matches is a domain rule and lives in the service; whether an absent If-Match
// is a 428 is a transport rule and lives in etag.go.

// GuildDTO is the wire representation of the guild singleton.
//
// It is a SEPARATE type from guild.Guild on purpose (.claude/rules/go-idioms.md: one exported type
// per concept, and "the guild on the wire" and "the guild in the domain" are different concepts).
// This is where snake_case field names, the RFC 3339 timestamps and the 0..2 precision bound are
// declared; the domain type carries none of those, and the spec is derived from THIS struct's tags.
//
// created_at and updated_at are RFC 3339 strings with microsecond precision and a Z, per canonical
// §2. They are formatted from the stored Micros here rather than exposed as raw integers, because a
// timestamp on the wire is a date-time, not a point count.
type GuildDTO struct {
	Name            string `json:"name"             doc:"The guild's display name"`
	Tag             string `json:"tag"              doc:"The <Guild Tag> as it appears in /who"`
	Timezone        string `json:"timezone"         doc:"IANA timezone; renders all UI and buckets every day-scoped value"`
	WeekStart       int64  `json:"week_start"       doc:"First day of the guild's week, 0 (Sunday) through 6 (Saturday)"`
	PointsLabel     string `json:"points_label"     doc:"What this guild calls its points, e.g. DKP"`
	PointsPrecision int64  `json:"points_precision" doc:"Display rounding depth, 0 through 2; storage is always centipoints"`
	// InactiveAfterDays is a pointer so null (never auto-flag) is distinct from 0. omitempty is NOT
	// set: a null must appear on the wire so a client can tell "the sweep is off" from "the field was
	// omitted from this response".
	InactiveAfterDays *int64 `json:"inactive_after_days" doc:"Days of inactivity before the sweep flags a member, or null to never auto-flag"`
	AutoSetInactive   bool   `json:"auto_set_inactive"   doc:"Whether the sweep sets members inactive or merely reports them"`
	HideInactive      bool   `json:"hide_inactive"       doc:"Whether inactive members are hidden from standings"`
	CreatedAt         string `json:"created_at"          format:"date-time" doc:"When the guild was created"`
	UpdatedAt         string `json:"updated_at"          format:"date-time" doc:"When the guild was last updated"`
}

// GuildOutput is the response envelope for both operations: the DTO body plus the strong ETag header
// a client stores and sends back as If-Match on a PATCH.
type GuildOutput struct {
	ETag string `header:"ETag"`
	Body GuildDTO
}

// UpdateGuildInput is the PATCH request.
//
// If-Match is declared OPTIONAL (no `required:"true"`), and the presence check is in the handler via
// requireIfMatch — see etag.go for why a required tag would yield 422 instead of the 428 canonical
// §7 requires. Every body field is a pointer with omitempty so "absent" (leave unchanged) is
// distinguishable from "set to the zero value" (.claude/rules/api-endpoints.md).
type UpdateGuildInput struct {
	IfMatch string `header:"If-Match" doc:"The ETag from a prior GET. Required; its absence is a 428."`

	Body struct {
		Name            *string `json:"name,omitempty"             minLength:"1" maxLength:"64" doc:"The guild's display name"`
		Tag             *string `json:"tag,omitempty"              maxLength:"8"                doc:"The <Guild Tag>"`
		Timezone        *string `json:"timezone,omitempty"         doc:"IANA timezone"`
		WeekStart       *int64  `json:"week_start,omitempty"       minimum:"0" maximum:"6" doc:"First day of the week, 0..6"`
		PointsLabel     *string `json:"points_label,omitempty"     maxLength:"24" doc:"What this guild calls its points"`
		PointsPrecision *int64  `json:"points_precision,omitempty" minimum:"0" maximum:"2" doc:"Display rounding depth, 0..2"`
		// InactiveAfterDays: a value sets the field; an omitted (or null) field leaves it unchanged.
		// Huma decodes an omitted field and an explicit null identically as a nil *int64, so the
		// handler cannot tell them apart, and toInput takes the safe reading — nil means "leave
		// unchanged", never "clear to NULL" — because clearing on omission would silently disable the
		// inactivity sweep on any unrelated PATCH. Clearing the field back to NULL through the wire is
		// a deliberate later addition, not a default.
		InactiveAfterDays *int64 `json:"inactive_after_days,omitempty" minimum:"0" doc:"Days before the sweep flags a member; a value sets it, omitting it leaves it unchanged"`
		AutoSetInactive   *bool  `json:"auto_set_inactive,omitempty"   doc:"Whether the sweep sets members inactive"`
		HideInactive      *bool  `json:"hide_inactive,omitempty"       doc:"Whether inactive members are hidden from standings"`
	}
}

// toInput maps the wire PATCH body to the service's UpdateInput.
//
// InactiveAfterDays crosses a shape boundary here, and it is the one field where getting the pointer
// levels wrong silently corrupts data. The service takes a **int64 so it can tell absent (nil outer,
// leave unchanged) from set-to-null (non-nil outer, nil inner, write NULL) from set-to-value. The
// wire field is *int64 with omitempty, and Huma decodes BOTH an omitted field and an explicit JSON
// null as a nil *int64 — the handler cannot distinguish them. So PR 5a takes the SAFE reading: a nil
// wire field is "absent, leave unchanged", never "clear to NULL". Wrapping a nil inner in a non-nil
// outer here would make every PATCH that omits inactive_after_days write NULL, silently turning the
// inactivity sweep off — so the outer pointer is nil exactly when the wire field is nil.
//
// The cost is that the wire cannot yet clear the field back to NULL through this endpoint; the value
// is set on the way up and left alone otherwise. Adding an explicit clear (a JSON null distinct from
// an omitted field) needs a custom decoder and a caller that wants it, and is a deliberate later
// change — not a silent data-loss default.
func (in *UpdateGuildInput) toInput() guild.UpdateInput {
	var inactiveAfterDays **int64
	if in.Body.InactiveAfterDays != nil {
		inner := in.Body.InactiveAfterDays
		inactiveAfterDays = &inner
	}

	return guild.UpdateInput{
		IfMatch:           in.IfMatch,
		Name:              in.Body.Name,
		Tag:               in.Body.Tag,
		Timezone:          in.Body.Timezone,
		WeekStart:         in.Body.WeekStart,
		PointsLabel:       in.Body.PointsLabel,
		PointsPrecision:   in.Body.PointsPrecision,
		InactiveAfterDays: inactiveAfterDays,
		AutoSetInactive:   in.Body.AutoSetInactive,
		HideInactive:      in.Body.HideInactive,
	}
}

// toDTO maps a domain guild.Guild to the wire GuildDTO, formatting the stored Micros into RFC 3339.
//
// The timestamps go out through core.Micros.String(), which is the ONE RFC 3339 layout in the repo
// (internal/core/micros.go): UTC, an explicit Z, and a fixed six-digit fraction, per canonical §2 and
// .claude/rules/api-endpoints.md. This file carried a private microsToRFC3339 with the layout spelled
// out a second time, written before core.Micros existed and marked "until then"; core.Micros exists
// now, so the copy is gone. A second formatter is a second thing to get wrong — time.RFC3339Nano
// trims trailing fractional zeros, so the obvious rewrite of either copy silently makes the field
// variable-width — and one exported type per concept (.claude/rules/go-idioms.md) applies to the
// function that renders it too.
//
// guild.Guild stores CreatedAt/UpdatedAt as a bare int64 (its own comment defers the retype to
// core.Micros), so the conversion happens here, at the wire boundary, which is where every other
// shape change in this file already happens.
func toDTO(g guild.Guild) GuildDTO {
	return GuildDTO{
		Name:              g.Name,
		Tag:               g.Tag,
		Timezone:          g.Timezone,
		WeekStart:         g.WeekStart,
		PointsLabel:       g.PointsLabel,
		PointsPrecision:   g.PointsPrecision,
		InactiveAfterDays: g.InactiveAfterDays,
		AutoSetInactive:   g.AutoSetInactive,
		HideInactive:      g.HideInactive,
		CreatedAt:         core.Micros(g.CreatedAt).String(),
		UpdatedAt:         core.Micros(g.UpdatedAt).String(),
	}
}

// registerGuild declares GET and PATCH /api/v1/guild.
//
// It takes a *guild.Service so the handlers can call the domain. The service may be nil when this is
// called only to build the spec (NewHumaAPI from `dkp openapi` and the arch tests): the operations
// still register — which is the whole point, they must appear in the document — and the handler
// closures are never invoked in that path. A nil service reaching a handler at RUNTIME is a
// programming error in the wiring, not an input to guard here.
func registerGuild(api huma.API, svc *guild.Service) {
	huma.Register(api, huma.Operation{
		OperationID: "getGuild",
		Method:      http.MethodGet,
		Path:        BasePath + "/guild",
		Summary:     "Get guild settings",
		Description: "Returns the single guild's identity and officer-editable settings, with a " +
			"strong ETag a client stores and sends back as If-Match on a PATCH.",
		Tags: []string{"guild"},
		// PAT-callable: a bot with roster:read may read the guild. The session alternative carries no
		// scope (cookie sessions never do). x-dkp-scopes is non-empty and every member resolves in the
		// catalogue — the "PAT-callable" case of the three-case scope rule.
		Security: []map[string][]string{
			{"pat": {"roster:read"}},
			{"session": {}},
		},
		Extensions: map[string]any{
			ExtensionPermission: "roster.read",
			ExtensionScopes:     []string{"roster:read"},
		},
		DefaultStatus: http.StatusOK,
		// 503 is declared because a degraded boot (database could not be opened, process kept up so
		// /healthz stays green) maps ErrNoStore to service_unavailable. 401/403 are forward-looking:
		// the spec declares the security requirement, so its statuses are documented even though no
		// auth middleware emits them until Phase 2 (see SECURITY.md's known gap).
		Errors: []int{
			http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound,
			http.StatusServiceUnavailable,
		},
	}, func(ctx context.Context, _ *struct{}) (*GuildOutput, error) {
		g, err := svc.Get(ctx)
		if err != nil {
			return nil, guildError(err, guild.Guild{})
		}

		return &GuildOutput{ETag: guild.ETagOf(g), Body: toDTO(g)}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "updateGuild",
		Method:      http.MethodPatch,
		Path:        BasePath + "/guild",
		Summary:     "Update guild settings",
		Description: "Applies a partial update to the guild under an If-Match precondition. A missing " +
			"If-Match is 428; a stale one is 412 with the current representation in meta.current.",
		Tags: []string{"guild"},
		// Session-only, and NOT PAT-forbidden. admin.settings is session-only because no PAT scope
		// family covers instance configuration — not because it alters authentication state, which is
		// what x-dkp-pat-forbidden would assert (canonical §6, decision record §U6). So this declares
		// neither x-dkp-scopes nor x-dkp-pat-forbidden: the "session-only by omission" case of the
		// three-case scope rule.
		Security: []map[string][]string{
			{"session": {}},
		},
		Extensions: map[string]any{
			ExtensionPermission: "admin.settings",
		},
		DefaultStatus: http.StatusOK,
		// 404 when the singleton was never seeded (ErrNotFound), 503 in the degraded no-database boot
		// state (ErrNoStore). 401/403 are forward-looking, as on getGuild.
		Errors: []int{
			http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden,
			http.StatusNotFound, http.StatusPreconditionRequired, http.StatusPreconditionFailed,
			http.StatusUnprocessableEntity, http.StatusServiceUnavailable,
		},
	}, func(ctx context.Context, in *UpdateGuildInput) (*GuildOutput, error) {
		if _, problem := requireIfMatch(in.IfMatch); problem != nil {
			return nil, problem
		}

		g, err := svc.Update(ctx, in.toInput())
		if err != nil {
			// On a precondition failure the service returns the CURRENT representation in g. The 412
			// body carries it in meta.current and its ETag in meta.current_etag, both computed from the
			// same domain value a fresh GET would return, so a bot merges in one round trip.
			return nil, guildError(err, g)
		}

		return &GuildOutput{ETag: guild.ETagOf(g), Body: toDTO(g)}, nil
	})
}

// guildError maps a guild-service sentinel to the closed error enum. `current` is the current domain
// representation, used only by the 412 path; callers pass a zero guild.Guild when there is none.
func guildError(err error, current guild.Guild) error {
	switch {
	case errors.Is(err, guild.ErrNotFound):
		return NewProblem(http.StatusNotFound, CodeNotFound,
			"The guild has not been initialised. This instance has no guild row yet.")
	case errors.Is(err, guild.ErrNoStore):
		// The process is up so /healthz stays green, but the database could not be opened. A guild
		// request cannot be served in that state; 503 is the honest answer and it names the condition
		// rather than leaking a nil-pointer 500.
		return NewProblem(http.StatusServiceUnavailable, CodeServiceUnavailable,
			"The database is unavailable. The server is running but could not open its database.")
	case errors.Is(err, guild.ErrPreconditionFailed):
		// meta.current is the wire DTO; meta.current_etag is the strong ETag of the same value, so it
		// is byte-identical to the one a fresh GET would return and is the caller's next If-Match.
		return preconditionFailed(toDTO(current), guild.ETagOf(current))
	default:
		// An unmapped error is a 500 through the closed error enum, not huma.Error500InternalServerError
		// — the latter carries no stable `code`, and .claude/rules/api-endpoints.md requires every
		// error to be discriminable by `code`. No detail is leaked; the request id in the problem body
		// (added by the error middleware) ties it to the slog line carrying the real error.
		return NewProblem(http.StatusInternalServerError, CodeInternalError, "internal error")
	}
}
