// public_schema.go implements the unauthenticated small/medium-venue tier
// endpoints from Wave SEAT-B3 (feature #307):
//
//	GET /v1/event-sessions/{id}/schema
//	GET /v1/event-sessions/{id}/seat-status[?since_version=N]
//
// Contract source: 09_autoforge/seating_backlog.md §7 SEAT-B3.
//
//   - Both endpoints are unauthenticated. The visibility gate mirrors the
//     public feed: events.status = 'published' AND the session is not
//     general_admission (GA sessions do not expose a per-seat schema).
//   - /schema returns the canonical geometry payload with category → tier /
//     price resolution overlayed as a separate `category_prices` map so the
//     raw geometry stays byte-for-byte identical to the stored version.
//     Response headers:
//     ETag:          "<geometry_checksum>"   (strong)
//     Cache-Control: public, max-age=86400, immutable
//     When If-None-Match matches, the handler responds 304 with no body.
//   - /seat-status without query returns the full snapshot
//     { status_version: N, seats: { "<seat_key>": "<status>" } }
//   - /seat-status?since_version=N returns only rows whose status_version
//     strictly exceeds N, plus the new status_version cursor. Deltas are
//     short-lived / uncacheable so seat holds propagate promptly.
package hseating

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/seating"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/priceresolve"
)

// schemaCacheControl is the Cache-Control header for the /schema endpoint.
// The response body is content-addressed by geometry_checksum (ETag) — a
// new version of the seating plan produces a new checksum, so caches can
// safely treat the response as immutable for the max-age window.
const schemaCacheControl = "public, max-age=86400, immutable"

// seatStatusCacheControl is the Cache-Control header for both /seat-status
// variants. Deltas invalidate on every reservation touching the session,
// so we serve them uncacheable at the edge.
const seatStatusCacheControl = "no-cache"

// adminSchemaCacheControl is the Cache-Control header of the organizer's
// schema: a draft event's map is nobody else's, and a repaint must show at
// once (the ETag still saves the body on a revalidation).
const adminSchemaCacheControl = "private, no-cache"

// HandleGetPublicSessionSchema serves the geometry + category price
// resolution payload for a published seated session.
func (h *Handler) HandleGetPublicSessionSchema(w http.ResponseWriter, r *http.Request) {
	h.serveSessionSchema(w, r, false)
}

// HandleGetSessionSchemaAdmin serves the same payload to the organizer for any
// seated session, published or not: the seat map of a draft event is painted
// before the event goes on sale. The route's shim has checked the session's
// organization (session.seats.manage).
func (h *Handler) HandleGetSessionSchemaAdmin(w http.ResponseWriter, r *http.Request) {
	h.serveSessionSchema(w, r, true)
}

// serveSessionSchema is the body of both schema routes; admin drops the
// published gate and makes the answer private.
func (h *Handler) serveSessionSchema(w http.ResponseWriter, r *http.Request, admin bool) {
	if h.queries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	sessionID, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}
	ctx := r.Context()

	lookup, cacheControl := h.queries.GetPublicSessionSchema, schemaCacheControl
	if admin {
		lookup, cacheControl = h.queries.GetAdminSessionSchema, adminSchemaCacheControl
	}
	schemaRow, err := lookup(ctx, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"event_session.schema_not_found",
				"session is not published, is general_admission, or does not exist",
				r,
			))
			return
		}
		h.logger.Error("seating_public: schema lookup failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"event_session.schema_failed", "failed to load session schema", r,
		))
		return
	}

	// Strong ETag = geometry_checksum + seat_status_version. The version
	// component makes tier/category reassignment (AB-39) — which changes
	// the category_prices overlay without touching the geometry —
	// cache-visible: the bump invalidates If-None-Match revalidation.
	// Published early so a match short-circuits before any per-seat /
	// tier queries.
	etag := `"` + schemaRow.GeometryChecksum + "-v" +
		strconv.FormatInt(schemaRow.SeatStatusVersion, 10) + `"`
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", cacheControl)
	if matchesETag(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	// Parse geometry to extract category metadata + build the
	// seat_key -> category_index index used to resolve tiers below.
	var geom seating.Geometry
	if err := json.Unmarshal(schemaRow.Geometry, &geom); err != nil {
		h.logger.Error("seating_public: geometry decode failed",
			slog.String("session_id", sessionID.String()),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"event_session.schema_failed", "failed to decode session geometry", r,
		))
		return
	}

	// Resolve every seat's category from its own session_seats.tier_id
	// (S1a, seat_categories.go - shared with the BSS layout.svg and the
	// Bil24 image renderers).
	seats, err := h.queries.ListSessionSeats(ctx, sessionID)
	if err != nil {
		h.logger.Error("seating_public: list session seats failed",
			slog.String("session_id", sessionID.String()),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"event_session.schema_failed", "failed to load session seats", r,
		))
		return
	}

	// Load tiers for the session so we can attach names + prices to the
	// resolved categories.
	tiers, err := h.queries.ListTicketTiersBySession(ctx, sessionID)
	if err != nil {
		h.logger.Error("seating_public: list ticket tiers failed",
			slog.String("session_id", sessionID.String()),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"event_session.schema_failed", "failed to load ticket tiers", r,
		))
		return
	}
	// A category without places on the plan (an extra price category of the
	// event wizard, or one made in the seat manager) shows the colour the
	// organizer chose for it, not a palette swatch.
	tierColors, err := h.queries.ListSessionTierColors(ctx, sessionID)
	if err != nil {
		h.logger.Error("seating_public: list tier colours failed",
			slog.String("session_id", sessionID.String()),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"event_session.schema_failed", "failed to load ticket tiers", r,
		))
		return
	}
	res := resolveSeatCategories(geom, seats, tiers, seatKeyIndex(geom)).withTierColors(tierColors)

	// AB-48: resolve scheduled prices once for all tiers (ONE resolver).
	effPrices, err := priceresolve.ForTiers(ctx, h.queries, tiers, time.Now().UTC())
	if err != nil {
		h.logger.Error("seating_public: price window lookup failed",
			slog.String("session_id", sessionID.String()),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"event_session.schema_failed", "failed to resolve tier prices", r,
		))
		return
	}

	categories, extraCategories := buildCategoryOverlay(geom, res, effPrices)

	sessSeatVersion := schemaRow.SeatStatusVersion
	planVersionID := ""
	if schemaRow.SeatingPlanVersionID != nil {
		planVersionID = schemaRow.SeatingPlanVersionID.String()
	}
	response := map[string]any{
		"session_id":              schemaRow.ID.String(),
		"event_id":                schemaRow.EventID.String(),
		"admission_mode":          schemaRow.AdmissionMode,
		"seating_plan_version_id": planVersionID,
		"seat_status_version":     sessSeatVersion,
		"geometry_checksum":       schemaRow.GeometryChecksum,
		"capacity_seated":         schemaRow.CapacitySeated,
		"capacity_standing":       schemaRow.CapacityStanding,
		"geometry":                json.RawMessage(schemaRow.Geometry),
		"category_prices":         categories,
		// S1a: per-seat truth. seat_category_index maps seat_key to the
		// category index (a category_prices or extra_categories entry)
		// that carries the seat's own session_seats.tier_id, ONLY for seats
		// whose category differs from geometry.seats[].category_index.
		// The geometry itself stays byte-for-byte the stored one.
		"seat_category_index": res.SeatIndex,
		"extra_categories":    extraCategories,
	}
	httputil.WriteJSON(w, http.StatusOK, response)
}

// buildCategoryOverlay builds the `category_prices` and `extra_categories`
// arrays of the /schema response from the per-seat resolution (S1a).
// category_prices keeps one entry per plan category in geometry order;
// extra_categories lists the synthetic categories of tiers that carry
// repainted seats but are the home tier of no plan category. Pure, so the
// response shape is unit-testable without a database.
func buildCategoryOverlay(
	geom seating.Geometry,
	res seatCategoryResolution,
	effPrices map[uuid.UUID]priceresolve.Effective,
) (categories, extraCategories []map[string]any) {
	applyTier := func(entry map[string]any, tier gen.TicketTierRow) {
		entry["tier_id"] = tier.ID.String()
		entry["tier_name"] = tier.Name
		entry["pricing_mode"] = tier.PricingMode
		entry["price_amount"] = tier.PriceAmount
		entry["currency"] = tier.Currency
		// AB-48: effective (scheduled) price + next change for the widget.
		entry["current_price"] = tier.PriceAmount
		entry["next_price_change_at"] = nil
		if eff, ok := effPrices[tier.ID]; ok {
			entry["current_price"] = eff.Amount
			if eff.NextChangeAt != nil {
				entry["next_price_change_at"] = eff.NextChangeAt.UTC().Format(time.RFC3339)
			}
		}
	}
	categories = make([]map[string]any, 0, len(geom.Categories))
	for _, cat := range geom.Categories {
		entry := map[string]any{
			"index":         cat.Index,
			"name":          cat.Name,
			"color":         cat.Color,
			"price_hint":    cat.PriceHint,
			"currency_hint": cat.CurrencyHint,
		}
		if tier, hasTier := res.Home[cat.Index]; hasTier {
			applyTier(entry, tier)
		}
		categories = append(categories, entry)
	}
	extraCategories = make([]map[string]any, 0, len(res.Extra))
	for _, e := range res.Extra {
		entry := map[string]any{
			"index":         e.Index,
			"name":          e.Tier.Name,
			"color":         e.Color,
			"price_hint":    "",
			"currency_hint": "",
		}
		applyTier(entry, e.Tier)
		extraCategories = append(extraCategories, entry)
	}
	return categories, extraCategories
}

// HandleGetPublicSessionSeatStatus serves both the full snapshot and the
// ?since_version=N delta variant. Response shape:
//
//	{ "status_version": <int>, "seats": {"<seat_key>": "<status>"} }
//
// Delta callers see only rows whose status_version strictly exceeds their
// cursor; the returned status_version is the live session cursor so they
// can advance monotonically without re-reading the snapshot.
func (h *Handler) HandleGetPublicSessionSeatStatus(w http.ResponseWriter, r *http.Request) {
	if h.queries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	sessionID, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}
	ctx := r.Context()

	meta, err := h.queries.GetPublicSessionSeatStatusMeta(ctx, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"event_session.seat_status_not_found",
				"session is not published, is general_admission, or does not exist",
				r,
			))
			return
		}
		h.logger.Error("seating_public: seat-status meta lookup failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"event_session.seat_status_failed", "failed to load seat status meta", r,
		))
		return
	}

	// Parse the optional cursor. `since_version` MUST be a non-negative
	// integer; anything else is a 400 to help callers spot bugs early
	// rather than silently returning a full snapshot.
	rawCursor := r.URL.Query().Get("since_version")
	isDelta := rawCursor != ""
	var sinceVersion int64
	if isDelta {
		n, parseErr := strconv.ParseInt(rawCursor, 10, 64)
		if parseErr != nil || n < 0 {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
				"event_session.invalid_since_version",
				"since_version must be a non-negative integer",
				r,
			))
			return
		}
		sinceVersion = n
	}

	w.Header().Set("Cache-Control", seatStatusCacheControl)

	// Fast-path for callers already at the head cursor: no seats to
	// stream, just echo the cursor back.
	if isDelta && sinceVersion >= meta.SeatStatusVersion {
		httputil.WriteJSON(w, http.StatusOK, map[string]any{
			"session_id":     meta.ID.String(),
			"status_version": meta.SeatStatusVersion,
			"seats":          map[string]string{},
			"delta":          true,
		})
		return
	}

	var rows []gen.SessionSeatRow
	if isDelta {
		rows, err = h.queries.ListSessionSeatsChangedSince(ctx, sessionID, sinceVersion)
	} else {
		rows, err = h.queries.ListSessionSeats(ctx, sessionID)
	}
	if err != nil {
		h.logger.Error("seating_public: seat status query failed",
			slog.String("session_id", sessionID.String()),
			slog.Bool("delta", isDelta),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"event_session.seat_status_failed", "failed to load seat status", r,
		))
		return
	}

	seats := make(map[string]string, len(rows))
	for _, row := range rows {
		seats[row.SeatKey] = row.Status
	}

	response := map[string]any{
		"session_id":     meta.ID.String(),
		"status_version": meta.SeatStatusVersion,
		"seats":          seats,
		"delta":          isDelta,
	}
	httputil.WriteJSON(w, http.StatusOK, response)
}

// matchesETag reports whether the raw If-None-Match header value contains
// the given strong ETag. Handles the "*" wildcard, comma-separated lists,
// and OWS around each entry. Weak validators (W/"…") never match a strong
// tag per RFC 7232 §2.3.2.
func matchesETag(ifNoneMatch, strongETag string) bool {
	if ifNoneMatch == "" {
		return false
	}
	trimmed := strings.TrimSpace(ifNoneMatch)
	if trimmed == "*" {
		return true
	}
	for _, entry := range strings.Split(trimmed, ",") {
		candidate := strings.TrimSpace(entry)
		if candidate == "" {
			continue
		}
		// Ignore weak validators; strong comparison per RFC 7232.
		if strings.HasPrefix(candidate, "W/") {
			continue
		}
		if candidate == strongETag {
			return true
		}
	}
	return false
}
