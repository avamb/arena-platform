// public_page_track.go implements the hosted page's statistics beacon (HP-2a,
// task 2-3):
//
//	POST /v1/public/pages/{org_slug}/{event_slug}/track
//
// Body (JSON, at most 1 KiB; the router requires Content-Type: application/json
// like on every other POST):
//
//	{"kind": "page_view" | "video_play" | "partner_click",
//	 "utm_source": "...", "utm_campaign": "..."}
//
// The route is public and writes, so it is narrow on purpose:
//
//   - a row holds only the event, the day, the kind and the two cleaned utm
//     values (pagestats.CleanUTM); the visitor's IP address and User-Agent are
//     read to decide and are never stored, logged or put in a metric label;
//   - no cookie is read or set;
//   - the day is the calendar day in the event's venue zone;
//   - an obvious bot (pagestats.IsSuspectAgent) is counted in suspect_hits;
//   - two limits run BEFORE any database read: the page's own bucket and the
//     client IP's (Handler.enforceRateLimit), so an unknown address costs no
//     query and a flood is answered 429 track.rate_limited and not counted;
//   - one event keeps at most pagestats.MaxUTMPairsPerDay different utm pairs
//     a day, further pairs are counted without utm.
//
// Answers: 204 counted, 400 bad body or kind, 404 page.not_found (the same
// answer as the page itself — the page already tells whether an address
// exists, so the beacon reveals nothing new), 429, 503.
package hfeed

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/observability"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/pagestats"
)

// PageTrackLimiter is the optional third half of the rate limiter: the bucket
// of one event page and the bucket of the client IP for the statistics beacon.
// A limiter that does not implement it leaves the route on the general
// per-IP limit only.
type PageTrackLimiter interface {
	// CheckPageTrack counts one beacon in the page's bucket and in the IP's
	// bucket, both on every call, and reports whether it may proceed with the
	// seconds until the blocking window renews.
	CheckPageTrack(pageKey, ip string) (allowed bool, retryAfterSeconds int)
}

// WithMetrics wires the metrics registry the beacon counts into. Nil keeps the
// route working without metrics (bare test handlers).
func (h *Handler) WithMetrics(m *observability.Metrics) *Handler {
	h.metrics = m
	return h
}

type pageTrackRequest struct {
	Kind        string `json:"kind"`
	UTMSource   string `json:"utm_source"`
	UTMCampaign string `json:"utm_campaign"`
}

// HandlePublicPageTrack counts one hit of the statistics beacon.
func (h *Handler) HandlePublicPageTrack(w http.ResponseWriter, r *http.Request) {
	if h.publicFeedQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "The service is temporarily unavailable. Try again in a minute.", r,
		))
		return
	}
	w.Header().Set("Cache-Control", "no-store")

	orgSlug := chi.URLParam(r, "org_slug")
	eventSlug := chi.URLParam(r, "event_slug")

	// The limits come first: they need no database and no body, so a flood
	// (or a scan of unknown addresses) is turned away before it costs either.
	pageKey := pagestats.PageKey(orgSlug, eventSlug)
	trackCheck := func(key string) (bool, int) {
		lim, ok := h.rl.(PageTrackLimiter)
		if !ok {
			return true, 0
		}
		return lim.CheckPageTrack(key, h.clientIP(r))
	}
	if !h.enforceRateLimit(w, r, "track.rate_limited", trackCheck, pageKey) {
		h.metrics.RecordPageTrack("", observability.PageTrackRateLimited)
		return
	}

	var req pageTrackRequest
	body := http.MaxBytesReader(w, r.Body, pagestats.MaxBodyBytes)
	dec := json.NewDecoder(body)
	if err := dec.Decode(&req); err != nil {
		h.metrics.RecordPageTrack("", observability.PageTrackRejected)
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"track.invalid_body", "Send a JSON object with a kind.", r,
		))
		return
	}
	// Nothing but whitespace may follow the object.
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		h.metrics.RecordPageTrack("", observability.PageTrackRejected)
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"track.invalid_body", "Send a JSON object with a kind.", r,
		))
		return
	}
	kind, ok := pagestats.ParseKind(req.Kind)
	if !ok {
		h.metrics.RecordPageTrack(req.Kind, observability.PageTrackRejected)
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"track.invalid_kind", "The kind must be page_view, video_play or partner_click.", r,
		))
		return
	}

	ctx := r.Context()
	target, err := h.publicFeedQueries.ResolvePageTrackTarget(ctx, orgSlug, eventSlug)
	if errors.Is(err, pgx.ErrNoRows) {
		// Not an organization's page: the first segment may be a promoter's.
		target, err = h.publicFeedQueries.ResolvePageTrackTargetByPromoter(ctx, orgSlug, eventSlug)
	}
	if err != nil {
		h.metrics.RecordPageTrack(kind, observability.PageTrackRejected)
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"page.not_found", "We cannot find this page. Check the address.", r,
			))
			return
		}
		h.logger.Error("page_track: resolve failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"track.failed", "We could not count this visit.", r,
		))
		return
	}

	suspect := pagestats.IsSuspectAgent(r.UserAgent())
	tz := ""
	if target.Timezone != nil {
		tz = *target.Timezone
	}
	err = h.publicFeedQueries.RecordEventPageHit(ctx, gen.RecordEventPageHitParams{
		EventID:     target.EventID,
		OrgID:       target.OrgID,
		Day:         pagestats.Day(time.Now(), tz),
		Kind:        kind,
		UTMSource:   pagestats.CleanUTM(req.UTMSource),
		UTMCampaign: pagestats.CleanUTM(req.UTMCampaign),
		Suspect:     suspect,
		MaxUTMPairs: pagestats.MaxUTMPairsPerDay,
	})
	if err != nil {
		h.metrics.RecordPageTrack(kind, observability.PageTrackRejected)
		// The log line names the event only: never the address or the agent.
		h.logger.Error("page_track: record failed",
			slog.String("event_id", target.EventID.String()), slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"track.failed", "We could not count this visit.", r,
		))
		return
	}
	if suspect {
		h.metrics.RecordPageTrack(kind, observability.PageTrackSuspect)
	} else {
		h.metrics.RecordPageTrack(kind, observability.PageTrackCounted)
	}
	w.WriteHeader(http.StatusNoContent)
}
