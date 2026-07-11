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

	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/seating"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
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

// HandleGetPublicSessionSchema serves the geometry + category price
// resolution payload for a published seated session.
func (h *Handler) HandleGetPublicSessionSchema(w http.ResponseWriter, r *http.Request) {
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

	schemaRow, err := h.queries.GetPublicSessionSchema(ctx, sessionID)
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

	// Strong ETag = geometry_checksum. Publish it early so If-None-Match
	// short-circuits before we do any per-seat / tier queries.
	etag := `"` + schemaRow.GeometryChecksum + `"`
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", schemaCacheControl)
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

	// Resolve category -> tier by inspecting session_seats: any seat that
	// carries a resolvable tier_id anchors the category. First-seat-wins
	// keeps the resolution stable across renames (shared helper in
	// types.go, also used by the BSS layout.svg renderer).
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
	categoryToTier := resolveCategoryTiers(geom, seats, tiers, seatKeyIndex(geom))

	categories := make([]map[string]any, 0, len(geom.Categories))
	for _, cat := range geom.Categories {
		entry := map[string]any{
			"index":         cat.Index,
			"name":          cat.Name,
			"color":         cat.Color,
			"price_hint":    cat.PriceHint,
			"currency_hint": cat.CurrencyHint,
		}
		if tier, hasTier := categoryToTier[cat.Index]; hasTier {
			entry["tier_id"] = tier.ID.String()
			entry["tier_name"] = tier.Name
			entry["pricing_mode"] = tier.PricingMode
			entry["price_amount"] = tier.PriceAmount
			entry["currency"] = tier.Currency
		}
		categories = append(categories, entry)
	}

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
	}
	httputil.WriteJSON(w, http.StatusOK, response)
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
