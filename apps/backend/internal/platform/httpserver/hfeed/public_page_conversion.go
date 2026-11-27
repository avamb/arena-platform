// public_page_conversion.go queues the hosted page's ViewContent and
// TicketsClick for Meta CAPI and GA4 (spec 68 section 3.4):
//
//	POST /v1/public/pages/{org_slug}/{event_slug}/conversion
//	POST /v1/public/pages/{org_slug}/series/{series_slug}/conversion
//
// Body (JSON, at most 2 KiB):
//
//	{"kind": "view_content" | "tickets_click",
//	 "event_id": "1..64 characters [A-Za-z0-9_-]",
//	 "buyer_context": {...}}
//
// Nothing is stored without an explicit yes in buyer_context (the same rule
// as the cart events); the anonymous page statistics are a separate route and
// stay untouched. The limits run before any read, on their own bucket key.
// Answers: 204, 400 bad body, kind or event_id, 404 page.not_found, 429, 503.
// Queuing is best effort: a failure is logged without buyer data and never
// changes the answer.
package hfeed

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/conversions"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/pagestats"
)

// maxPageConversionBody bounds the request body.
const maxPageConversionBody = 2048

type pageConversionRequest struct {
	Kind         string          `json:"kind"`
	EventID      string          `json:"event_id"`
	BuyerContext json.RawMessage `json:"buyer_context"`
}

// parsePageConversion reads the body: one JSON object and nothing after it,
// a known kind and a valid event_id. It returns the conversions kind and the
// event id prefixed per kind ("vc:" / "tc:").
func parsePageConversion(body io.Reader) (kind, eventID string, buyerContext json.RawMessage, ok bool) {
	var req pageConversionRequest
	dec := json.NewDecoder(body)
	if err := dec.Decode(&req); err != nil {
		return "", "", nil, false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return "", "", nil, false
	}
	id := parseAddToCartID(mustJSONString(req.EventID))
	if id == "" || id != req.EventID {
		return "", "", nil, false
	}
	switch req.Kind {
	case "view_content":
		return conversions.KindViewContent, "vc:" + id, req.BuyerContext, true
	case "tickets_click":
		return conversions.KindTicketsClick, "tc:" + id, req.BuyerContext, true
	}
	return "", "", nil, false
}

func mustJSONString(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return b
}

// pageContentName is "Event name — City" when every live session is in the
// same city, else the bare name.
func pageContentName(name string, cities []string) string {
	if len(cities) == 1 && strings.TrimSpace(cities[0]) != "" {
		return name + " — " + strings.TrimSpace(cities[0])
	}
	return name
}

// pageConversionEvent builds the queued event; ok is false without the
// buyer's explicit yes.
func pageConversionEvent(kind, eventID string, buyerContext json.RawMessage,
	ip, userAgent, contentID, contentName string) (conversions.SiteEvent, bool) {
	if !buyerContextConsents(buyerContext) {
		return conversions.SiteEvent{}, false
	}
	var bc conversions.BuyerContext
	if cleaned := conversions.CleanBuyerContext(buyerContext, ip, userAgent); len(cleaned) > 0 {
		_ = json.Unmarshal(cleaned, &bc)
	}
	return conversions.SiteEvent{
		Kind:        kind,
		EventID:     eventID,
		Context:     bc,
		ContentID:   contentID,
		ContentName: contentName,
	}, true
}

// pageContentLookup is the name and the cities of an event's live sessions.
// It reads on a short read-only transaction of the pool; any failure leaves
// the name empty (the event is still queued).
func (h *Handler) pageContentLookup(ctx context.Context, eventID uuid.UUID) (string, []string) {
	if h.pool == nil {
		return "", nil
	}
	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return "", nil
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var name string
	var cities []string
	err = tx.QueryRow(ctx, `
SELECT e.name,
       ARRAY(
           SELECT DISTINCT COALESCE(t_en.value, ci.slug, '')
           FROM   sessions s
           LEFT JOIN venues    v    ON v.id = s.venue_id
           LEFT JOIN cities    ci   ON ci.id = v.city_id
           LEFT JOIN i18n_text t_en ON t_en.namespace = 'geo.cities'
                  AND t_en.key = ci.slug AND t_en.locale = 'en'
           WHERE  s.event_id = e.id
             AND  s.deleted_at IS NULL
             AND  s.status <> 'cancelled'
       )
FROM events e
WHERE e.id = $1`, eventID).Scan(&name, &cities)
	if err != nil {
		return "", nil
	}
	return name, cities
}

// queuePageConversion enqueues one event best effort (2 s at most).
func (h *Handler) queuePageConversion(ctx context.Context, orgID uuid.UUID, ev conversions.SiteEvent) {
	if h.cartConversions == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if err := h.cartConversions.EnqueueEvent(ctx, orgID, ev); err != nil && h.logger != nil {
		h.logger.Warn("public_page: conversion event not queued",
			slog.String("kind", ev.Kind),
			slog.String("org_id", orgID.String()),
			slog.String("error", err.Error()),
		)
	}
}

// pageConversionPrologue is the common start: database gate, limits, body.
func (h *Handler) pageConversionPrologue(w http.ResponseWriter, r *http.Request, pageKey string) (kind, eventID string, buyerContext json.RawMessage, ok bool) {
	if h.publicFeedQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "The service is temporarily unavailable. Try again in a minute.", r,
		))
		return "", "", nil, false
	}
	w.Header().Set("Cache-Control", "no-store")
	check := func(key string) (bool, int) {
		lim, isLim := h.rl.(PageTrackLimiter)
		if !isLim {
			return true, 0
		}
		return lim.CheckPageTrack(key, h.clientIP(r))
	}
	if !h.enforceRateLimit(w, r, "track.rate_limited", check, "conv:"+pageKey) {
		return "", "", nil, false
	}
	kind, eventID, buyerContext, ok = parsePageConversion(http.MaxBytesReader(w, r.Body, maxPageConversionBody))
	if !ok {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"track.invalid_body", "Send a JSON object with a kind (view_content or tickets_click) and an event_id.", r,
		))
		return "", "", nil, false
	}
	return kind, eventID, buyerContext, true
}

func (h *Handler) pageConversionNotFound(w http.ResponseWriter, r *http.Request) {
	httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
		"page.not_found", "We cannot find this page. Check the address.", r,
	))
}

func (h *Handler) pageConversionFailed(w http.ResponseWriter, r *http.Request, err error) {
	h.logger.Error("page_conversion: resolve failed", slog.String("error", err.Error()))
	httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
		"track.failed", "We could not count this visit.", r,
	))
}

// HandlePublicPageConversion answers the event page's route.
func (h *Handler) HandlePublicPageConversion(w http.ResponseWriter, r *http.Request) {
	orgSlug, eventSlug := chi.URLParam(r, "org_slug"), chi.URLParam(r, "event_slug")
	kind, eventID, bctx, ok := h.pageConversionPrologue(w, r, pagestats.PageKey(orgSlug, eventSlug))
	if !ok {
		return
	}
	ctx := r.Context()
	target, err := h.publicFeedQueries.ResolvePageTrackTarget(ctx, orgSlug, eventSlug)
	if errors.Is(err, pgx.ErrNoRows) {
		target, err = h.publicFeedQueries.ResolvePageTrackTargetByPromoter(ctx, orgSlug, eventSlug)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		h.pageConversionNotFound(w, r)
		return
	}
	if err != nil {
		h.pageConversionFailed(w, r, err)
		return
	}
	if h.cartConversions != nil && buyerContextConsents(bctx) {
		name, cities := h.pageContentLookup(ctx, target.EventID)
		if ev, yes := pageConversionEvent(kind, eventID, bctx,
			httputil.TrustedClientIP(r, 0), r.UserAgent(),
			target.EventID.String(), pageContentName(name, cities)); yes {
			h.queuePageConversion(ctx, target.OrgID, ev)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// HandlePublicSeriesPageConversion answers the tour page's route.
func (h *Handler) HandlePublicSeriesPageConversion(w http.ResponseWriter, r *http.Request) {
	orgSlug, seriesSlug := chi.URLParam(r, "org_slug"), chi.URLParam(r, "series_slug")
	kind, eventID, bctx, ok := h.pageConversionPrologue(w, r, "series/"+pagestats.PageKey(orgSlug, seriesSlug))
	if !ok {
		return
	}
	ctx := r.Context()
	org, err := h.publicFeedQueries.GetHostedPromoterPageOrg(ctx, orgSlug)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !org.HasHostedChannel) {
		h.pageConversionNotFound(w, r)
		return
	}
	if err != nil {
		h.pageConversionFailed(w, r, err)
		return
	}
	series, err := h.publicFeedQueries.GetActiveEventSeriesBySlug(ctx, org.OrgID, seriesSlug)
	if errors.Is(err, pgx.ErrNoRows) {
		h.pageConversionNotFound(w, r)
		return
	}
	if err != nil {
		h.pageConversionFailed(w, r, err)
		return
	}
	if ev, yes := pageConversionEvent(kind, eventID, bctx,
		httputil.TrustedClientIP(r, 0), r.UserAgent(),
		series.ID.String(), series.Name); yes && h.cartConversions != nil {
		h.queuePageConversion(ctx, org.OrgID, ev)
	}
	w.WriteHeader(http.StatusNoContent)
}
