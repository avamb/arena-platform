// public_feed.go implements the unauthenticated public feed event API (feature #152).
//
// These endpoints allow external consumers (widgets, embeds) to browse events
// published to a specific agent feed token without requiring a JWT session.
// The feed token in the path serves as the credential (ADR-013: federated feeds).
//
// Endpoints (all unauthenticated):
//
//	GET /v1/public/feeds/{feed_token}/events              — list published events
//	GET /v1/public/feeds/{feed_token}/events/{event_id}   — event detail with tiers
//
// Rate limiting:
//
//	Per-token: 100 requests/minute
//	Per-IP:    300 requests/minute
//
// The concrete in-memory limiter (publicFeedRateLimiter) lives in the parent
// package's feed_shims.go; the handlers here consume it through the narrow
// RateLimiter interface declared in handler.go.
//
// Cache-Control:
//
//	List:   public, max-age=60, stale-while-revalidate=30
//	Detail: public, max-age=30, stale-while-revalidate=15
package hfeed

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// ─────────────────────────────────────────────────────────────────────────────
// Response types
// ─────────────────────────────────────────────────────────────────────────────

// publicFeedEventResponse is the JSON shape for a single event in the list and
// detail endpoints.
type publicFeedEventResponse struct {
	ID          string  `json:"id"`
	OrgID       string  `json:"org_id"`
	VenueID     *string `json:"venue_id"`
	Name        string  `json:"name"`
	Description *string `json:"description"`
	Status      string  `json:"status"`
	StartAt     string  `json:"start_at"`
	EndAt       string  `json:"end_at"`
	Visibility  string  `json:"visibility"`
	ImageURL    *string `json:"image_url"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
}

func publicFeedEventFromRow(e gen.EventRow) publicFeedEventResponse {
	resp := publicFeedEventResponse{
		ID:          e.ID.String(),
		OrgID:       e.OrgID.String(),
		Name:        e.Name,
		Description: e.Description,
		Status:      e.Status,
		StartAt:     e.StartAt.UTC().Format(time.RFC3339),
		EndAt:       e.EndAt.UTC().Format(time.RFC3339),
		Visibility:  e.Visibility,
		ImageURL:    e.ImageURL,
		CreatedAt:   e.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:   e.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if e.VenueID != nil {
		s := e.VenueID.String()
		resp.VenueID = &s
	}
	return resp
}

// publicFeedTierResponse is the JSON shape for a ticket tier in the detail response.
type publicFeedTierResponse struct {
	ID              string  `json:"id"`
	Name            string  `json:"name"`
	PricingMode     string  `json:"pricing_mode"`
	PriceAmount     int64   `json:"price_amount"`
	Currency        string  `json:"currency"`
	PwywMin         *int64  `json:"pwyw_min"`
	PwywMax         *int64  `json:"pwyw_max"`
	Capacity        *int32  `json:"capacity"`
	SaleWindowStart *string `json:"sale_window_start"`
	SaleWindowEnd   *string `json:"sale_window_end"`
	SortOrder       int32   `json:"sort_order"`
}

func publicFeedTierFromRow(t gen.TicketTierRow) publicFeedTierResponse {
	resp := publicFeedTierResponse{
		ID:          t.ID.String(),
		Name:        t.Name,
		PricingMode: t.PricingMode,
		PriceAmount: t.PriceAmount,
		Currency:    t.Currency,
		PwywMin:     t.PwywMin,
		PwywMax:     t.PwywMax,
		Capacity:    t.Capacity,
		SortOrder:   t.SortOrder,
	}
	if t.SaleWindowStart != nil {
		s := t.SaleWindowStart.UTC().Format(time.RFC3339)
		resp.SaleWindowStart = &s
	}
	if t.SaleWindowEnd != nil {
		s := t.SaleWindowEnd.UTC().Format(time.RFC3339)
		resp.SaleWindowEnd = &s
	}
	return resp
}

// publicFeedSessionResponse is the JSON shape for a session in the detail response.
//
// AdmissionMode, SchemaURL and SeatStatusURL are populated only when the
// session is seated (admission_mode != 'general_admission'); they are
// emitted as top-level fields so widget consumers can decide whether to
// call the SEAT-B3 endpoints without pattern-matching the session shape.
type publicFeedSessionResponse struct {
	ID            string                   `json:"id"`
	StartAt       string                   `json:"start_at"`
	EndAt         string                   `json:"end_at"`
	CapacityTotal int32                    `json:"capacity_total"`
	Status        string                   `json:"status"`
	AdmissionMode string                   `json:"admission_mode,omitempty"`
	SchemaURL     string                   `json:"schema_url,omitempty"`
	SeatStatusURL string                   `json:"seat_status_url,omitempty"`
	Tiers         []publicFeedTierResponse `json:"tiers"`
}

func publicFeedSessionFromRow(s gen.SessionRow) publicFeedSessionResponse {
	return publicFeedSessionResponse{
		ID:            s.ID.String(),
		StartAt:       s.StartAt.UTC().Format(time.RFC3339),
		EndAt:         s.EndAt.UTC().Format(time.RFC3339),
		CapacityTotal: s.CapacityTotal,
		Status:        s.Status,
		Tiers:         []publicFeedTierResponse{},
	}
}

// applySeatingLinks fills in schema_url / seat_status_url and
// admission_mode when the underlying session is seated. Called by the
// event detail serializer after ListSessionAdmissionModesByEvent has been
// consulted. Keeps the visibility check (admission_mode != GA) in one
// place so callers can't accidentally leak the links for GA sessions.
func (r *publicFeedSessionResponse) applySeatingLinks(admissionMode string) {
	if admissionMode == "" || admissionMode == "general_admission" {
		return
	}
	r.AdmissionMode = admissionMode
	r.SchemaURL = "/v1/event-sessions/" + r.ID + "/schema"
	r.SeatStatusURL = "/v1/event-sessions/" + r.ID + "/seat-status"
}

// publicFeedPagination is the pagination metadata in the list response.
type publicFeedPagination struct {
	Page       int `json:"page"`
	PerPage    int `json:"per_page"`
	Total      int `json:"total"`
	TotalPages int `json:"total_pages"`
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/public/feeds/{feed_token}/events — list published events
// ─────────────────────────────────────────────────────────────────────────────

// HandlePublicFeedEvents returns published events for the given feed token.
// Unauthenticated; the token in the path acts as the read credential.
//
// Query parameters:
//
//	city_id    — filter by publication city (UUID, optional)
//	date_from  — filter events starting at or after this time (RFC3339, optional)
//	date_to    — filter events ending at or before this time (RFC3339, optional)
//	page       — 1-indexed page number (default 1)
//	per_page   — results per page, 1-100 (default 20)
//
// Responses:
//
//	200 — event list + pagination; Cache-Control: public, max-age=60, stale-while-revalidate=30
//	404 — token unknown or revoked
//	429 — rate limited
//	503 — database not available
func (h *Handler) HandlePublicFeedEvents(w http.ResponseWriter, r *http.Request) {
	if h.publicFeedQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}

	feedToken := chi.URLParam(r, "feed_token")
	clientIP := httputil.ExtractClientIP(r)

	// Rate limit: per-token + per-IP
	if !h.rl.CheckToken(feedToken) || !h.rl.CheckIP(clientIP) {
		httputil.WriteJSON(w, http.StatusTooManyRequests, httputil.ErrorEnvelope(
			"feed.rate_limited", "too many requests; please slow down", r,
		))
		return
	}

	// Validate the feed token by looking it up. Revoked / unknown → 404.
	if h.feedTokenQueries != nil {
		ft, err := h.feedTokenQueries.GetFeedTokenByToken(r.Context(), feedToken)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
					"feed.token_not_found", "feed token not found or has been revoked", r,
				))
				return
			}
			h.logger.Error("public_feed: token lookup failed",
				slog.String("feed_token", feedToken),
				slog.String("error", err.Error()),
			)
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"feed.token_lookup_failed", "failed to validate feed token", r,
			))
			return
		}
		if !ft.IsActive {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"feed.token_not_found", "feed token not found or has been revoked", r,
			))
			return
		}
	}

	q := r.URL.Query()

	// Parse optional city_id filter.
	var cityID *uuid.UUID
	if raw := q.Get("city_id"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
				"feed.invalid_city_id", "city_id must be a valid UUID", r,
			))
			return
		}
		cityID = &id
	}

	// Parse optional date_from filter.
	var dateFrom *time.Time
	if raw := q.Get("date_from"); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
				"feed.invalid_date_from", "date_from must be RFC3339", r,
			))
			return
		}
		dateFrom = &t
	}

	// Parse optional date_to filter.
	var dateTo *time.Time
	if raw := q.Get("date_to"); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
				"feed.invalid_date_to", "date_to must be RFC3339", r,
			))
			return
		}
		dateTo = &t
	}

	// Parse pagination parameters.
	page := 1
	if raw := q.Get("page"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
				"feed.invalid_page", "page must be a positive integer", r,
			))
			return
		}
		page = n
	}

	perPage := 20
	if raw := q.Get("per_page"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 100 {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
				"feed.invalid_per_page", "per_page must be between 1 and 100", r,
			))
			return
		}
		perPage = n
	}

	ctx := r.Context()
	offset := int32((page - 1) * perPage) //nolint:gosec // page,perPage bounded above by validation
	limit := int32(perPage)               //nolint:gosec // perPage bounded above by validation

	// Count total matching events for pagination metadata.
	total, err := h.publicFeedQueries.CountPublishedEventsByFeedToken(ctx, feedToken, cityID, dateFrom, dateTo)
	if err != nil {
		h.logger.Error("public_feed: count events failed",
			slog.String("feed_token", feedToken),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"feed.count_failed", "failed to count events", r,
		))
		return
	}

	// Fetch the page of events.
	rows, err := h.publicFeedQueries.ListPublishedEventsByFeedToken(ctx, feedToken, cityID, dateFrom, dateTo, limit, offset)
	if err != nil {
		h.logger.Error("public_feed: list events failed",
			slog.String("feed_token", feedToken),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"feed.list_failed", "failed to list events", r,
		))
		return
	}

	events := make([]publicFeedEventResponse, 0, len(rows))
	for _, row := range rows {
		events = append(events, publicFeedEventFromRow(row))
	}

	totalPages := int(total) / perPage
	if int(total)%perPage != 0 {
		totalPages++
	}
	if totalPages == 0 {
		totalPages = 1
	}

	w.Header().Set("Cache-Control", "public, max-age=60, stale-while-revalidate=30")
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"events": events,
		"pagination": publicFeedPagination{
			Page:       page,
			PerPage:    perPage,
			Total:      int(total),
			TotalPages: totalPages,
		},
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/public/feeds/{feed_token}/events/{event_id} — event detail + tiers
// ─────────────────────────────────────────────────────────────────────────────

// HandlePublicFeedEvent returns the full detail for a single published event,
// including its sessions and each session's ticket tiers.
// Unauthenticated; the token in the path acts as the read credential.
//
// Responses:
//
//	200 — event detail; Cache-Control: public, max-age=30, stale-while-revalidate=15
//	400 — event_id is not a valid UUID
//	404 — token unknown/revoked OR event not published to this token
//	429 — rate limited
//	503 — database not available
func (h *Handler) HandlePublicFeedEvent(w http.ResponseWriter, r *http.Request) {
	if h.publicFeedQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}

	feedToken := chi.URLParam(r, "feed_token")
	clientIP := httputil.ExtractClientIP(r)

	// Rate limit: per-token + per-IP
	if !h.rl.CheckToken(feedToken) || !h.rl.CheckIP(clientIP) {
		httputil.WriteJSON(w, http.StatusTooManyRequests, httputil.ErrorEnvelope(
			"feed.rate_limited", "too many requests; please slow down", r,
		))
		return
	}

	// Parse event_id from path.
	eventID, err := uuid.Parse(chi.URLParam(r, "event_id"))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"feed.invalid_event_id", "event_id must be a valid UUID", r,
		))
		return
	}

	ctx := r.Context()

	// Fetch the event — validates token + event visibility in one query.
	event, err := h.publicFeedQueries.GetPublishedEventByFeedToken(ctx, feedToken, eventID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"feed.event_not_found", "event not found or not published to this feed", r,
			))
			return
		}
		h.logger.Error("public_feed: get event failed",
			slog.String("feed_token", feedToken),
			slog.String("event_id", eventID.String()),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"feed.fetch_failed", "failed to fetch event", r,
		))
		return
	}

	eventResp := publicFeedEventFromRow(event)

	// Fetch sessions for this event.
	type eventDetailResponse struct {
		publicFeedEventResponse
		Sessions []publicFeedSessionResponse `json:"sessions"`
	}

	detail := eventDetailResponse{
		publicFeedEventResponse: eventResp,
		Sessions:                []publicFeedSessionResponse{},
	}

	if h.sessionQueries != nil {
		sessions, err := h.sessionQueries.ListSessionsByEvent(ctx, eventID)
		if err != nil {
			h.logger.Error("public_feed: list sessions failed",
				slog.String("event_id", eventID.String()),
				slog.String("error", err.Error()),
			)
			// Non-fatal: return the event without sessions.
		} else {
			// Fetch (id, admission_mode) for every session on this event
			// so we can annotate seated sessions with schema_url /
			// seat_status_url (feature #307, Wave SEAT-B3). A lookup
			// failure is non-fatal: the payload degrades to GA-only.
			admissionByID := map[string]string{}
			if h.sessionQueries != nil {
				modes, modeErr := h.sessionQueries.ListSessionAdmissionModesByEvent(ctx, eventID)
				if modeErr != nil {
					h.logger.Error("public_feed: list admission modes failed",
						slog.String("event_id", eventID.String()),
						slog.String("error", modeErr.Error()),
					)
				} else {
					for _, m := range modes {
						admissionByID[m.ID.String()] = m.AdmissionMode
					}
				}
			}
			for _, sess := range sessions {
				sessResp := publicFeedSessionFromRow(sess)
				sessResp.applySeatingLinks(admissionByID[sess.ID.String()])

				// Fetch tiers for each session.
				if h.tierQueries != nil {
					tiers, err := h.tierQueries.ListTicketTiersBySession(ctx, sess.ID)
					if err != nil {
						h.logger.Error("public_feed: list tiers failed",
							slog.String("session_id", sess.ID.String()),
							slog.String("error", err.Error()),
						)
						// Non-fatal: return the session without tiers.
					} else {
						for _, tier := range tiers {
							sessResp.Tiers = append(sessResp.Tiers, publicFeedTierFromRow(tier))
						}
					}
				}

				detail.Sessions = append(detail.Sessions, sessResp)
			}
		}
	}

	w.Header().Set("Cache-Control", "public, max-age=30, stale-while-revalidate=15")
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"event": detail,
	})
}
