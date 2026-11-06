// public_sales_funnel.go implements the buyer-funnel beacon (spec 50, stage A7):
//
//	POST /v1/public/funnel
//
// Body (JSON, at most 2 KiB; the router requires Content-Type: application/json
// like on every other POST):
//
//	{"feed_token": "...", "event_id": "uuid"}          the widget (surface "widget"), or
//	{"page": {"slug": "...", "event_slug": "..."}}     a hosted / tickets page
//	                                                   (surface "hosted_page", "tickets_page" or "external_site")
//	+ "session_id": "uuid"                              optional
//	+ "step": "view" | "session_open" | "seats_chosen"
//	+ "surface": "widget" | "hosted_page" | "tickets_page" | "external_site"
//	+ "utm_source", "utm_campaign"                      optional
//
// It counts how far a visitor got, per event and day, without knowing who the
// visitor is. Like the hosted page's statistics beacon (public_page_track.go)
// it is public and writes, so it is narrow on purpose:
//
//   - a row holds only the event, the day, the step, the surface, the session
//     and the two cleaned utm values (pagestats.CleanUTM); the visitor's IP
//     address and User-Agent are read to decide and are never stored, logged
//     or put in a metric label;
//   - no cookie is read or set and nothing is kept on the device;
//   - the day is the calendar day in the event's venue zone;
//   - an obvious bot (pagestats.IsSuspectAgent) is counted in suspect_hits;
//   - two limits run BEFORE any database read: the context's own bucket and the
//     client IP's (the same CheckPageTrack limiter as the page beacon), so an
//     unknown address costs no query and a flood is answered 429 and not
//     counted;
//   - the same visitor's identical count in one day is dropped by an in-memory
//     salted hash (salesfunnel.Deduper), never by anything stored;
//   - one event keeps at most pagestats.MaxUTMPairsPerDay different utm pairs a
//     day, further pairs are counted without utm.
//
// The surface is tied to how the event is addressed: a feed token is the
// widget on a seller's site ("widget" only), a page address is one of the pages
// Arena hosts ("hosted_page" or "tickets_page"). A beacon that disagrees with
// itself is not counted. "external_site" (a customer's own domain) may use either
// addressing.
//
// Answers: 204 counted or deliberately not counted (an unknown step, surface,
// event, session or page, or a body that disagrees with itself, is answered
// like a counted one: the beacon tells nothing about why), 400 only an
// unreadable JSON body, 429, 503. Never 502: Cloudflare replaces an origin 502
// with its own page.
package hfeed

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/pagestats"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/salesfunnel"
)

// WithFunnelDeduper wires the process-wide duplicate filter of the funnel
// beacon. The Handler is built per request, so the filter must come from the
// Server; nil counts every beacon.
func (h *Handler) WithFunnelDeduper(d *salesfunnel.Deduper) *Handler {
	h.funnelDedupe = d
	return h
}

type funnelPage struct {
	Slug      string `json:"slug"`
	EventSlug string `json:"event_slug"`
}

type funnelRequest struct {
	FeedToken   string      `json:"feed_token"`
	EventID     string      `json:"event_id"`
	Page        *funnelPage `json:"page"`
	SessionID   string      `json:"session_id"`
	Step        string      `json:"step"`
	Surface     string      `json:"surface"`
	UTMSource   string      `json:"utm_source"`
	UTMCampaign string      `json:"utm_campaign"`
}

// funnelContext is a validated request: exactly one way of naming the event.
type funnelContext struct {
	feedToken string
	eventID   uuid.UUID
	slug      string
	eventSlug string
	sessionID *uuid.UUID
}

func (c funnelContext) byToken() bool { return c.feedToken != "" }

// parseFunnelRequest checks the shape of the body. It reads no database. ok is
// false for a beacon that must not be counted (answered 204 all the same).
func parseFunnelRequest(req funnelRequest) (ctx funnelContext, step, surface string, ok bool) {
	step, ok = salesfunnel.ParseStep(req.Step)
	if !ok {
		return ctx, "", "", false
	}
	surface, ok = salesfunnel.ParseSurface(req.Surface)
	if !ok {
		return ctx, "", "", false
	}
	token := strings.TrimSpace(req.FeedToken)
	switch {
	case token != "" && req.Page == nil:
		if surface != salesfunnel.SurfaceWidget && surface != salesfunnel.SurfaceExternalSite {
			return ctx, "", "", false
		}
		id, err := uuid.Parse(strings.TrimSpace(req.EventID))
		if err != nil || len(token) > salesfunnel.MaxTokenLength {
			return ctx, "", "", false
		}
		ctx.feedToken, ctx.eventID = token, id
	case token == "" && req.Page != nil && req.EventID == "":
		if surface == salesfunnel.SurfaceWidget {
			return ctx, "", "", false
		}
		slug := strings.TrimSpace(req.Page.Slug)
		eventSlug := strings.TrimSpace(req.Page.EventSlug)
		if slug == "" || eventSlug == "" || len(slug) > salesfunnel.MaxSlugLength || len(eventSlug) > salesfunnel.MaxSlugLength {
			return ctx, "", "", false
		}
		ctx.slug, ctx.eventSlug = slug, eventSlug
	default:
		return ctx, "", "", false
	}
	if sid := strings.TrimSpace(req.SessionID); sid != "" {
		id, err := uuid.Parse(sid)
		if err != nil {
			return ctx, "", "", false
		}
		ctx.sessionID = &id
	}
	return ctx, step, surface, true
}

// HandlePublicFunnel counts one funnel step.
func (h *Handler) HandlePublicFunnel(w http.ResponseWriter, r *http.Request) {
	if h.publicFeedQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "The service is temporarily unavailable. Try again in a minute.", r,
		))
		return
	}
	w.Header().Set("Cache-Control", "no-store")

	// The body comes first only because the rate-limit key is named in it; it
	// is a bounded read and no database is touched before the limits pass.
	var req funnelRequest
	body := http.MaxBytesReader(w, r.Body, salesfunnel.MaxBodyBytes)
	dec := json.NewDecoder(body)
	if err := dec.Decode(&req); err != nil {
		h.badFunnelBody(w, r)
		return
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		h.badFunnelBody(w, r)
		return
	}
	fc, step, surface, ok := parseFunnelRequest(req)
	if !ok {
		// Not counted, and not told why.
		w.WriteHeader(http.StatusNoContent)
		return
	}

	trackCheck := func(key string) (bool, int) {
		lim, ok := h.rl.(PageTrackLimiter)
		if !ok {
			return true, 0
		}
		return lim.CheckPageTrack(key, h.clientIP(r))
	}
	if !h.enforceRateLimit(w, r, "funnel.rate_limited", trackCheck, salesfunnel.LimitKey(fc.feedToken, fc.slug, fc.eventSlug)) {
		return
	}

	ctx := r.Context()
	target, found, err := h.resolveFunnelTarget(r, fc)
	if err != nil {
		h.logger.Error("sales_funnel: resolve failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"funnel.unavailable", "We could not count this step. Try again in a minute.", r,
		))
		return
	}
	if !found {
		// Nothing to count, nothing to tell: the answer is the counted one.
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if fc.sessionID != nil {
		belongs, err := h.publicFeedQueries.FunnelSessionBelongsToEvent(ctx, target.EventID, *fc.sessionID)
		if err != nil {
			h.logger.Error("sales_funnel: session check failed",
				slog.String("event_id", target.EventID.String()), slog.String("error", err.Error()))
			httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
				"funnel.unavailable", "We could not count this step. Try again in a minute.", r,
			))
			return
		}
		if !belongs {
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}

	utmSource := pagestats.CleanUTM(req.UTMSource)
	utmCampaign := pagestats.CleanUTM(req.UTMCampaign)
	session := ""
	if fc.sessionID != nil {
		session = fc.sessionID.String()
	}
	ip, ua := h.clientIP(r), r.UserAgent()
	parts := []string{target.EventID.String(), session, step, surface, utmSource, utmCampaign}
	if h.funnelDedupe != nil && h.funnelDedupe.SeenToday(ip, ua, parts...) {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	tz := ""
	if target.Timezone != nil {
		tz = *target.Timezone
	}
	err = h.publicFeedQueries.RecordSalesFunnelHit(ctx, gen.RecordSalesFunnelHitParams{
		EventID:     target.EventID,
		OrgID:       target.OrgID,
		SessionID:   fc.sessionID,
		Day:         pagestats.Day(time.Now(), tz),
		Step:        step,
		Surface:     surface,
		UTMSource:   utmSource,
		UTMCampaign: utmCampaign,
		Suspect:     pagestats.IsSuspectAgent(ua),
		MaxUTMPairs: pagestats.MaxUTMPairsPerDay,
	})
	if err != nil {
		if h.funnelDedupe != nil {
			h.funnelDedupe.Forget(ip, ua, parts...)
		}
		// The log line names the event only: never the address or the agent.
		h.logger.Error("sales_funnel: record failed",
			slog.String("event_id", target.EventID.String()), slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"funnel.unavailable", "We could not count this step. Try again in a minute.", r,
		))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) badFunnelBody(w http.ResponseWriter, r *http.Request) {
	httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
		"funnel.invalid_body", "Send a JSON object.", r,
	))
}

// resolveFunnelTarget turns the named context into a published event. found is
// false for an unknown or unpublished one; err only for a database failure.
func (h *Handler) resolveFunnelTarget(r *http.Request, fc funnelContext) (gen.PageTrackTarget, bool, error) {
	ctx := r.Context()
	if fc.byToken() {
		t, err := h.publicFeedQueries.ResolveFunnelTargetByFeedToken(ctx, fc.feedToken, fc.eventID)
		return finishFunnelResolve(t, err)
	}
	t, err := h.publicFeedQueries.ResolvePageTrackTarget(ctx, fc.slug, fc.eventSlug)
	if errors.Is(err, pgx.ErrNoRows) {
		// Not an organization's page: the first segment may be a promoter's.
		t, err = h.publicFeedQueries.ResolvePageTrackTargetByPromoter(ctx, fc.slug, fc.eventSlug)
	}
	return finishFunnelResolve(t, err)
}

func finishFunnelResolve(t gen.PageTrackTarget, err error) (gen.PageTrackTarget, bool, error) {
	if errors.Is(err, pgx.ErrNoRows) {
		return t, false, nil
	}
	if err != nil {
		return t, false, err
	}
	return t, true, nil
}
