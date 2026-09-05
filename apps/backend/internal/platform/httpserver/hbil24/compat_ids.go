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
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/bil24compat"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/compatids"
)

// ErrSeatIDInvalid is returned by resolveSeatToRow when the raw wire
// value cannot be parsed as a legal seat identifier (int64 on the
// compatDB path, UUID on the fallback path). Callers map this to Bil24
// result code -2 (invalid request). DB / driver failures (including
// pgx.ErrNoRows for a missing seat) are NEVER wrapped in this error so
// callers can still distinguish them via errors.Is.
var ErrSeatIDInvalid = errors.New("hbil24: seatId is not a valid seat identifier")

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

// resolveCategoryPriceID converts a wire categoryPriceId (a Bil24 ticket-
// tier identifier — spec §7.4) to the platform tier UUID used by downstream
// queries.
//
// Spec §4 (feature #476, W1-A2b) makes int64 the sole wire form: when
// h.compatDB is wired the raw is parsed as a positive int64 via
// bil24compat.ParseLegacyIntID and reverse-mapped through
// compatids.Resolve(KindCategoryPrice, n) so a UUID in the request field is
// rejected with ErrLegacyIDUUIDRejected before any DB round-trip.  When
// h.compatDB is nil (unit tests that construct a Handler without a
// *pgxpool.Pool) the helper falls back to TranslateLegacyID (UUID
// passthrough) so the pre-W1 unit-test harness keeps passing during the
// step-by-step migration.
//
// Callers map any returned error to Bil24 result code -2 (invalid request);
// the message attached at the callsite carries the field name so operators
// can grep the log.
func (h *Handler) resolveCategoryPriceID(ctx context.Context, raw string) (uuid.UUID, error) {
	if h.compatDB == nil {
		return TranslateLegacyID(raw)
	}
	return bil24compat.ResolveLegacyIntID(ctx, h.compatDB, compatids.KindCategoryPrice, raw)
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

// resolveSeatToRow resolves a wire seatId (spec §7.4 seatList entry) to a
// SessionSeatRow inside the target session.
//
// Spec §4 (feature #476, W1-A2b): when h.compatDB is wired, the raw is
// parsed as a positive int64 via bil24compat.ParseLegacyIntID (rejecting
// a UUID request field with ErrLegacyIDUUIDRejected before any DB
// round-trip) and the row is fetched by (session_id, system_seat_id)
// via GetSessionSeatBySystemSeatID — session_seats.system_seat_id
// (bigint, migration 0088) IS the wave-1 wire form so no compatids table
// lookup is needed.  When h.compatDB is nil (unit tests that construct
// a Handler without a *pgxpool.Pool) the helper falls back to the ADR-005
// UUID passthrough (uuid.Parse + GetSessionSeatByID) so the pre-W1
// unit-test harness (seat_d1_312 / seat_d2_313 / bil24_374) keeps passing
// during the step-by-step migration.
//
// Callers map any parse error to Bil24 result code -2 (invalid request);
// pgx.ErrNoRows from the lookup surfaces to callers as-is so they can
// return the spec-mandated -3 (not found) envelope with the seatId echo.
func (h *Handler) resolveSeatToRow(ctx context.Context, raw string, sessionID uuid.UUID) (gen.SessionSeatRow, error) {
	if h.compatDB == nil {
		id, err := uuid.Parse(raw)
		if err != nil {
			return gen.SessionSeatRow{}, fmt.Errorf("%w: %w", ErrSeatIDInvalid, err)
		}
		return h.seatQ.GetSessionSeatByID(ctx, id, sessionID)
	}
	n, err := bil24compat.ParseLegacyIntID(raw)
	if err != nil {
		return gen.SessionSeatRow{}, fmt.Errorf("%w: %w", ErrSeatIDInvalid, err)
	}
	return h.seatQ.GetSessionSeatBySystemSeatID(ctx, sessionID, n)
}

// validateSeatIDFormat returns nil when raw parses as a legal seat wire
// value (int64 on the compatDB path, UUID on the fallback path) and
// ErrSeatIDInvalid otherwise. Used up-front by reservationSeated to
// reject malformed seatList entries BEFORE the seat-service self-gate,
// so the wave-1 -2 (invalid request) contract keeps priority over the
// -99 (service unavailable) envelope.
func (h *Handler) validateSeatIDFormat(raw string) error {
	if h.compatDB == nil {
		if _, err := uuid.Parse(raw); err != nil {
			return fmt.Errorf("%w: %w", ErrSeatIDInvalid, err)
		}
		return nil
	}
	if _, err := bil24compat.ParseLegacyIntID(raw); err != nil {
		return fmt.Errorf("%w: %w", ErrSeatIDInvalid, err)
	}
	return nil
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
