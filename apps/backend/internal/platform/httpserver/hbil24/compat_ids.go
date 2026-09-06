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
	"sync"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/bil24compat"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/compatids"
)

// ─────────────────────────────────────────────────────────────────────────────
// Request-scoped compat-id cache (feature #498, W1-B3b)
// ─────────────────────────────────────────────────────────────────────────────
//
// GET_ALL_ACTIONS was calling compatids.Ensure once per event, per session and
// per ticket tier while building the response — with a catalog of 100 events
// x 3 sessions that is 700+ sequential DB round trips (~1.3s), blowing the
// spec §7.1 "no N+1" budget by an order of magnitude
// (scenario01_catalog_perf_test.go). The fix is a request-scoped cache
// carried on the context: a handler that knows all the platform ids it will
// need up front (loadActionEvents, buildCountryCityLists) calls
// prewarmCompatIDs once per kind — a genuine 2-round-trip
// compatids.EnsureMany — and every subsequent per-entity compatEnsure call
// for an id already in the cache becomes a pure map lookup. A cache miss
// (an id nothing prewarmed) still falls back to the old single-entity
// compatids.Ensure path so correctness never depends on prewarming being
// exhaustive — only performance does.

type compatCacheCtxKey struct{}

// compatCache is a mutex-guarded id map keyed first by compatids.Kind, then
// by platform uuid. Safe for concurrent use, though in practice a single
// gateway command handles one request on one goroutine.
type compatCache struct {
	mu sync.Mutex
	m  map[compatids.Kind]map[uuid.UUID]int64
}

// withCompatCache attaches a fresh, empty compatCache to ctx. Handlers that
// intend to prewarm call this once at the top of the command before any
// compatEnsure / prewarmCompatIDs call.
func withCompatCache(ctx context.Context) context.Context {
	return context.WithValue(ctx, compatCacheCtxKey{}, &compatCache{
		m: make(map[compatids.Kind]map[uuid.UUID]int64),
	})
}

func compatCacheFromContext(ctx context.Context) *compatCache {
	c, _ := ctx.Value(compatCacheCtxKey{}).(*compatCache)
	return c
}

func (c *compatCache) get(kind compatids.Kind, id uuid.UUID) (int64, bool) {
	if c == nil {
		return 0, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.m[kind][id]
	return v, ok
}

func (c *compatCache) put(kind compatids.Kind, resolved map[uuid.UUID]int64) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	dst := c.m[kind]
	if dst == nil {
		dst = make(map[uuid.UUID]int64, len(resolved))
		c.m[kind] = dst
	}
	for k, v := range resolved {
		dst[k] = v
	}
}

// prewarmCompatIDs batch-resolves every id in ids (of the same kind) through
// compatids.EnsureMany — two round trips regardless of len(ids) — and stores
// the result in ctx's compatCache so the per-entity compatEnsure calls that
// follow become map lookups instead of individual DB round trips.
//
// A no-op when h.compatDB is nil (fallback/unit-test mode — nothing to
// prewarm), ids is empty, or ctx carries no compatCache (withCompatCache was
// not called — compatEnsure still works, just without the speed-up). A
// prewarm failure is logged and swallowed rather than failing the command:
// the per-entity compatEnsure fallback path still produces a correct (if
// slower) response.
func (h *Handler) prewarmCompatIDs(ctx context.Context, kind compatids.Kind, ids []uuid.UUID) {
	if h.compatDB == nil || len(ids) == 0 {
		return
	}
	cache := compatCacheFromContext(ctx)
	if cache == nil {
		return
	}
	resolved, err := compatids.EnsureMany(ctx, h.compatDB, kind, ids)
	if err != nil {
		if h.logger != nil {
			h.logger.Error("bil24_compat: compatids.EnsureMany prewarm failed; falling back to per-entity Ensure",
				slog.String("kind", string(kind)),
				slog.Int("count", len(ids)),
				slog.String("error", err.Error()),
			)
		}
		return
	}
	cache.put(kind, resolved)
}

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

// compatVenueID returns the spec-§4 / §7.1 int64 wire form for a venue UUID
// (compatibility_id_map kind = venue). Fallback semantics match
// compatCategoryPriceID. Prepared for the deferred GET_ALL_ACTIONS
// countryList/cityList/venueList aggregation slice (spec §7.1) so the
// response projection can call one uniform helper per entity kind.
func (h *Handler) compatVenueID(ctx context.Context, venueID uuid.UUID) any {
	return h.compatEnsure(ctx, compatids.KindVenue, venueID, "venue")
}

// compatCityID returns the spec-§4 / §7.1 int64 wire form for a city UUID
// (compatibility_id_map kind = city). Fallback semantics match
// compatCategoryPriceID.
func (h *Handler) compatCityID(ctx context.Context, cityID uuid.UUID) any {
	return h.compatEnsure(ctx, compatids.KindCity, cityID, "city")
}

// compatCountryID returns the spec-§4 / §7.1 int64 wire form for a country
// UUID (compatibility_id_map kind = country). Fallback semantics match
// compatCategoryPriceID.
func (h *Handler) compatCountryID(ctx context.Context, countryID uuid.UUID) any {
	return h.compatEnsure(ctx, compatids.KindCountry, countryID, "country")
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

// resolveVenueID converts a wire venueId (a Bil24 venue identifier —
// spec §7.1 catalog filters) to the platform venue UUID used by downstream
// queries.
//
// Spec §4 (feature #476, W1-A2b) makes int64 the sole wire form: when
// h.compatDB is wired the raw is parsed as a positive int64 via
// bil24compat.ParseLegacyIntID and reverse-mapped through
// compatids.Resolve(KindVenue, n) so a UUID in the request field is
// rejected with ErrLegacyIDUUIDRejected before any DB round-trip. When
// h.compatDB is nil (unit tests that construct a Handler without a
// *pgxpool.Pool) the helper falls back to TranslateLegacyID (UUID
// passthrough) so the pre-W1 unit-test harness keeps passing during the
// step-by-step migration.
//
// Prepared ahead of the deferred GET_ALL_ACTIONS catalog-filter slice
// (spec §7.1) — no production callsite yet. Callers map any returned
// error to Bil24 result code -2 (invalid request).
func (h *Handler) resolveVenueID(ctx context.Context, raw string) (uuid.UUID, error) {
	if h.compatDB == nil {
		return TranslateLegacyID(raw)
	}
	return bil24compat.ResolveLegacyIntID(ctx, h.compatDB, compatids.KindVenue, raw)
}

// resolveCityID converts a wire cityId (a Bil24 city identifier — spec
// §7.1 catalog filters) to the platform city UUID. Fallback semantics
// match resolveVenueID.
func (h *Handler) resolveCityID(ctx context.Context, raw string) (uuid.UUID, error) {
	if h.compatDB == nil {
		return TranslateLegacyID(raw)
	}
	return bil24compat.ResolveLegacyIntID(ctx, h.compatDB, compatids.KindCity, raw)
}

// resolveCountryID converts a wire countryId (a Bil24 country identifier
// — spec §7.1 catalog filters) to the platform country UUID. Fallback
// semantics match resolveVenueID.
func (h *Handler) resolveCountryID(ctx context.Context, raw string) (uuid.UUID, error) {
	if h.compatDB == nil {
		return TranslateLegacyID(raw)
	}
	return bil24compat.ResolveLegacyIntID(ctx, h.compatDB, compatids.KindCountry, raw)
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
	if cache := compatCacheFromContext(ctx); cache != nil {
		if id, ok := cache.get(kind, platformID); ok {
			return id
		}
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
