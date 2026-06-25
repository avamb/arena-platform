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
// Cache-Control:
//
//	List:   public, max-age=60, stale-while-revalidate=30
//	Detail: public, max-age=30, stale-while-revalidate=15
package httpserver

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// ─────────────────────────────────────────────────────────────────────────────
// In-memory rate limiter (per token + per IP)
// ─────────────────────────────────────────────────────────────────────────────

// rateLimiterWindow tracks request count within a rolling 1-minute window.
type rateLimiterWindow struct {
	count   int
	resetAt time.Time
}

// publicFeedRateLimiter is a simple in-memory token-bucket rate limiter.
// It tracks per-token and per-IP request counts with 1-minute windows.
// The limiter is safe for concurrent use.
type publicFeedRateLimiter struct {
	mu         sync.Mutex
	tokenLimit int
	ipLimit    int
	tokens     map[string]*rateLimiterWindow
	ips        map[string]*rateLimiterWindow
}

// newPublicFeedRateLimiter creates a rate limiter with the given per-token and
// per-IP limits (requests per minute).
func newPublicFeedRateLimiter(tokenLimit, ipLimit int) *publicFeedRateLimiter {
	return &publicFeedRateLimiter{
		tokenLimit: tokenLimit,
		ipLimit:    ipLimit,
		tokens:     make(map[string]*rateLimiterWindow),
		ips:        make(map[string]*rateLimiterWindow),
	}
}

// check increments the counter for key in the given window map and returns true
// when the request is allowed (counter <= limit) and false when it is blocked.
func (rl *publicFeedRateLimiter) check(m map[string]*rateLimiterWindow, key string, limit int) bool {
	now := time.Now()
	w, ok := m[key]
	if !ok || now.After(w.resetAt) {
		m[key] = &rateLimiterWindow{count: 1, resetAt: now.Add(time.Minute)}
		return true
	}
	w.count++
	return w.count <= limit
}

// checkToken increments the per-token counter and returns true when allowed.
func (rl *publicFeedRateLimiter) checkToken(token string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	return rl.check(rl.tokens, token, rl.tokenLimit)
}

// checkIP increments the per-IP counter and returns true when allowed.
func (rl *publicFeedRateLimiter) checkIP(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	return rl.check(rl.ips, ip, rl.ipLimit)
}

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
type publicFeedSessionResponse struct {
	ID            string                   `json:"id"`
	StartAt       string                   `json:"start_at"`
	EndAt         string                   `json:"end_at"`
	CapacityTotal int32                    `json:"capacity_total"`
	Status        string                   `json:"status"`
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

// handlePublicFeedEvents returns published events for the given feed token.
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
func (s *Server) handlePublicFeedEvents(w http.ResponseWriter, r *http.Request) {
	if s.publicFeedQueries == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}

	feedToken := chi.URLParam(r, "feed_token")
	clientIP := extractClientIP(r)

	// Rate limit: per-token + per-IP
	if !s.publicFeedRL.checkToken(feedToken) || !s.publicFeedRL.checkIP(clientIP) {
		writeJSON(w, http.StatusTooManyRequests, errorEnvelope(
			"feed.rate_limited", "too many requests; please slow down", r,
		))
		return
	}

	// Validate the feed token by looking it up. Revoked / unknown → 404.
	if s.feedTokenQueries != nil {
		ft, err := s.feedTokenQueries.GetFeedTokenByToken(r.Context(), feedToken)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeJSON(w, http.StatusNotFound, errorEnvelope(
					"feed.token_not_found", "feed token not found or has been revoked", r,
				))
				return
			}
			s.logger.Error("public_feed: token lookup failed",
				slog.String("feed_token", feedToken),
				slog.String("error", err.Error()),
			)
			writeJSON(w, http.StatusInternalServerError, errorEnvelope(
				"feed.token_lookup_failed", "failed to validate feed token", r,
			))
			return
		}
		if !ft.IsActive {
			writeJSON(w, http.StatusNotFound, errorEnvelope(
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
			writeJSON(w, http.StatusBadRequest, errorEnvelope(
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
			writeJSON(w, http.StatusBadRequest, errorEnvelope(
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
			writeJSON(w, http.StatusBadRequest, errorEnvelope(
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
			writeJSON(w, http.StatusBadRequest, errorEnvelope(
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
			writeJSON(w, http.StatusBadRequest, errorEnvelope(
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
	total, err := s.publicFeedQueries.CountPublishedEventsByFeedToken(ctx, feedToken, cityID, dateFrom, dateTo)
	if err != nil {
		s.logger.Error("public_feed: count events failed",
			slog.String("feed_token", feedToken),
			slog.String("error", err.Error()),
		)
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"feed.count_failed", "failed to count events", r,
		))
		return
	}

	// Fetch the page of events.
	rows, err := s.publicFeedQueries.ListPublishedEventsByFeedToken(ctx, feedToken, cityID, dateFrom, dateTo, limit, offset)
	if err != nil {
		s.logger.Error("public_feed: list events failed",
			slog.String("feed_token", feedToken),
			slog.String("error", err.Error()),
		)
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
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
	writeJSON(w, http.StatusOK, map[string]any{
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

// handlePublicFeedEvent returns the full detail for a single published event,
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
func (s *Server) handlePublicFeedEvent(w http.ResponseWriter, r *http.Request) {
	if s.publicFeedQueries == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}

	feedToken := chi.URLParam(r, "feed_token")
	clientIP := extractClientIP(r)

	// Rate limit: per-token + per-IP
	if !s.publicFeedRL.checkToken(feedToken) || !s.publicFeedRL.checkIP(clientIP) {
		writeJSON(w, http.StatusTooManyRequests, errorEnvelope(
			"feed.rate_limited", "too many requests; please slow down", r,
		))
		return
	}

	// Parse event_id from path.
	eventID, err := uuid.Parse(chi.URLParam(r, "event_id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope(
			"feed.invalid_event_id", "event_id must be a valid UUID", r,
		))
		return
	}

	ctx := r.Context()

	// Fetch the event — validates token + event visibility in one query.
	event, err := s.publicFeedQueries.GetPublishedEventByFeedToken(ctx, feedToken, eventID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, errorEnvelope(
				"feed.event_not_found", "event not found or not published to this feed", r,
			))
			return
		}
		s.logger.Error("public_feed: get event failed",
			slog.String("feed_token", feedToken),
			slog.String("event_id", eventID.String()),
			slog.String("error", err.Error()),
		)
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
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

	if s.sessionQueries != nil {
		sessions, err := s.sessionQueries.ListSessionsByEvent(ctx, eventID)
		if err != nil {
			s.logger.Error("public_feed: list sessions failed",
				slog.String("event_id", eventID.String()),
				slog.String("error", err.Error()),
			)
			// Non-fatal: return the event without sessions.
		} else {
			for _, sess := range sessions {
				sessResp := publicFeedSessionFromRow(sess)

				// Fetch tiers for each session.
				if s.tierQueries != nil {
					tiers, err := s.tierQueries.ListTicketTiersBySession(ctx, sess.ID)
					if err != nil {
						s.logger.Error("public_feed: list tiers failed",
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
	writeJSON(w, http.StatusOK, map[string]any{
		"event": detail,
	})
}
