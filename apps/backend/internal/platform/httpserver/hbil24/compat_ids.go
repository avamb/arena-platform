// compat_ids.go — Handler helpers that translate platform UUIDs to the
// wave-1 int64 wire form via package compatids (spec §3.1, §4). Concentrated
// in one place so future migrations of GET_ALL_ACTIONS, GET_SEAT_LIST GA,
// CREATE_ORDER_EXT, SCAN_TICKET, etc. can call these helpers without
// duplicating the nil-safe fallback logic.
//
// Fallback contract: when h.compatDB is nil the helpers return the legacy
// UUID string via TranslatePlatformID. This keeps every unit test that
// constructs a Handler without a *pgxpool.Pool (the majority — see
// bil24compat_layout_188_test.go, seat_d1_312_test.go, ...) green during the
// step-by-step migration. The wire-fixture guardrail
// (tests/compat/bil24/no_uuid_in_wire_test.go) catches any regression on the
// production path because it walks the goldens the integration harness
// captures against a real pool.
//
// Feature #476 (W1-A2b).
package hbil24

import (
	"context"
	"log/slog"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/bil24compat"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/compatids"
)

// compatCategoryPriceID returns the spec-§4 int64 wire form for a ticket-tier
// UUID (compatibility_id_map kind = category_price) when h.compatDB is
// wired. Falls back to TranslatePlatformID(UUID string) when compatDB is nil
// so unit tests that omit the pool keep the pre-W1 wire behaviour.
//
// The return type is `any` because Bil24 response envelopes carry mixed
// scalar shapes on the same key during the step-by-step migration: an int64
// on production (compatDB wired) and a string in fallback mode. Downstream
// json.Marshal handles both.
func (h *Handler) compatCategoryPriceID(ctx context.Context, tierID uuid.UUID) any {
	return h.compatEnsure(ctx, compatids.KindCategoryPrice, tierID, "category_price")
}

// compatActionID returns the spec-§4 int64 wire form for an event UUID
// (compatibility_id_map kind = action). Fallback semantics match
// compatCategoryPriceID.
func (h *Handler) compatActionID(ctx context.Context, eventID uuid.UUID) any {
	return h.compatEnsure(ctx, compatids.KindAction, eventID, "action")
}

// compatActionEventID returns the spec-§4 int64 wire form for a session UUID
// (compatibility_id_map kind = action_event). Fallback semantics match
// compatCategoryPriceID.
func (h *Handler) compatActionEventID(ctx context.Context, sessionID uuid.UUID) any {
	return h.compatEnsure(ctx, compatids.KindActionEvent, sessionID, "action_event")
}

// resolveActionEventID converts a wire actionEventId (a Bil24 session
// identifier — spec §7.2 / §7.4 / §7.15) to the platform session UUID used
// by downstream queries.
//
// Spec §4 (feature #476, W1-A2b) makes int64 the sole wire form: when
// h.compatDB is wired the raw is parsed as a positive int64 via
// bil24compat.ParseLegacyIntID and reverse-mapped through
// compatids.Resolve(KindActionEvent, n) so a UUID in the request field is
// rejected with ErrLegacyIDUUIDRejected. When h.compatDB is nil (unit
// tests that construct a Handler without a *pgxpool.Pool) the helper falls
// back to TranslateLegacyID (UUID passthrough) so the pre-W1 unit-test
// harness keeps passing during the step-by-step migration.
//
// Callers map any returned error to Bil24 result code -2 (invalid request);
// the message attached at the callsite carries the field name so operators
// can grep the log.
func (h *Handler) resolveActionEventID(ctx context.Context, raw string) (uuid.UUID, error) {
	if h.compatDB == nil {
		return TranslateLegacyID(raw)
	}
	return bil24compat.ResolveLegacyIntID(ctx, h.compatDB, compatids.KindActionEvent, raw)
}

// compatEnsure is the shared body of the per-kind helpers. Kept private so
// the public surface reads as a per-entity vocabulary and each callsite
// carries the entity kind explicitly.
//
// The kindLabel argument mirrors the compatids.Kind value in a slog-friendly
// string so log lines are grep-able without depending on the compatids
// package internals.
func (h *Handler) compatEnsure(ctx context.Context, kind compatids.Kind, platformID uuid.UUID, kindLabel string) any {
	if h.compatDB == nil {
		return TranslatePlatformID(platformID)
	}
	id, err := compatids.Ensure(ctx, h.compatDB, kind, platformID)
	if err != nil {
		// Log and fall back to the UUID form so a compatibility_id_map hiccup
		// never black-holes a legitimate response. The wire-fixture guardrail
		// will catch persistent regressions during golden regeneration.
		if h.logger != nil {
			h.logger.Error("bil24_compat: compatids.Ensure failed; falling back to UUID string",
				slog.String("kind", kindLabel),
				slog.String("platform_id", platformID.String()),
				slog.String("error", err.Error()),
			)
		}
		return TranslatePlatformID(platformID)
	}
	return id
}
