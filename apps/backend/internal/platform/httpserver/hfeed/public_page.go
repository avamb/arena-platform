// public_page.go implements the hosted-sales-page resolve endpoint that backs
// https://tickets.arenasoldout.com/{org_slug}/{event_slug}.
//
//	GET /v1/public/pages/{org_slug}/{event_slug}
//
// Unauthenticated, IP-rate-limited (there is no feed token in the path — the
// endpoint's whole job is to resolve one). Resolution rule (see
// gen.GetHostedPageResolution / queries/public_page.sql for the exact SQL):
//
//	active org by slug
//	  -> non-deleted, status='published' event owned by that org, by slug
//	  -> an active publication of that event on a channel of the SAME org
//	     whose settings.hosted_page.enabled = true and which is itself
//	     not soft-deleted
//	  -> the newest active, non-revoked feed token of that channel
//
// Any missing link answers the SAME 404 `page.not_found` — the response
// deliberately never reveals which step of the chain failed, so the
// endpoint cannot be used to enumerate org/event slugs or channel
// configuration.
package hfeed

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// hostedPageOrgResponse is the org-branding slice of the hosted page response.
// Only public-safe columns are exposed — never legal/KYB/contact fields.
type hostedPageOrgResponse struct {
	Slug    string  `json:"slug"`
	Name    string  `json:"name"`
	LogoURL *string `json:"logo_url"`
}

// hostedPageEventResponse is the event slice of the hosted page response.
type hostedPageEventResponse struct {
	ID               string   `json:"id"`
	Slug             string   `json:"slug"`
	Title            string   `json:"title"`
	Description      *string  `json:"description"`
	ShortDescription *string  `json:"short_description"`
	ImageURL         *string  `json:"image_url"`
	PosterURL        *string  `json:"poster_url"`
	AgeRating        *string  `json:"age_rating"`
	VenueNames       []string `json:"venue_names"`
	FirstSessionAt   *string  `json:"first_session_at"`
	LastSessionAt    *string  `json:"last_session_at"`
}

// hostedPageResponse is the full JSON envelope for GET
// /v1/public/pages/{org_slug}/{event_slug}.
type hostedPageResponse struct {
	Org           hostedPageOrgResponse   `json:"org"`
	Event         hostedPageEventResponse `json:"event"`
	FeedToken     string                  `json:"feed_token"`
	DefaultLocale string                  `json:"default_locale"`
}

// HandlePublicPage resolves the hosted-page context for an (org_slug,
// event_slug) pair.
//
// Responses:
//
//	200 — org/event/feed_token payload; Cache-Control: public, max-age=30, stale-while-revalidate=15
//	404 — page.not_found (org unknown, event unknown, not published-visible,
//	      no hosted_page-enabled channel, or no active feed token — all
//	      indistinguishable on purpose)
//	429 — rate limited (per-IP only; there is no feed token to key a
//	      site-wide bucket on yet)
//	503 — database not available
func (h *Handler) HandlePublicPage(w http.ResponseWriter, r *http.Request) {
	if h.publicFeedQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}

	// No feed token exists yet at this point in the flow — only the per-IP
	// bucket applies. enforceRateLimit always evaluates the token check too,
	// so a permanently-allowed stub keeps the "both buckets, never
	// short-circuited" invariant intact while contributing nothing.
	if !h.enforceRateLimit(w, r, "page.rate_limited", func(string) (bool, int) { return true, 0 }, "") {
		return
	}

	orgSlug := chi.URLParam(r, "org_slug")
	eventSlug := chi.URLParam(r, "event_slug")
	if orgSlug == "" || eventSlug == "" {
		httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
			"page.not_found", "page not found", r,
		))
		return
	}

	ctx := r.Context()
	resolved, err := h.publicFeedQueries.GetHostedPageResolution(ctx, orgSlug, eventSlug)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"page.not_found", "page not found", r,
			))
			return
		}
		h.logger.Error("public_page: resolve failed",
			slog.String("org_slug", orgSlug),
			slog.String("event_slug", eventSlug),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"page.resolve_failed", "failed to resolve page", r,
		))
		return
	}

	resp := hostedPageResponse{
		Org: hostedPageOrgResponse{
			Slug: resolved.OrgSlug,
			Name: resolved.OrgName,
		},
		Event: hostedPageEventResponse{
			ID:               resolved.EventID.String(),
			Title:            resolved.EventName,
			Description:      resolved.EventDescription,
			ShortDescription: resolved.EventShortDescription,
			ImageURL:         resolved.EventImageURL,
			AgeRating:        resolved.EventAgeRating,
			VenueNames:       []string{},
		},
		FeedToken:     resolved.FeedToken,
		DefaultLocale: resolved.OrgDefaultLocale,
	}
	if resolved.EventSlug != nil {
		resp.Event.Slug = *resolved.EventSlug
	}
	if resolved.OrgLogoMediaID != nil {
		url := h.mediaFileURL(ctx, *resolved.OrgLogoMediaID)
		resp.Org.LogoURL = &url
	}
	if resolved.EventPosterMediaID != nil {
		url := h.mediaFileURL(ctx, *resolved.EventPosterMediaID)
		resp.Event.PosterURL = &url
	}
	if resolved.FirstSessionAt != nil {
		s := resolved.FirstSessionAt.UTC().Format(time.RFC3339)
		resp.Event.FirstSessionAt = &s
	}
	if resolved.LastSessionAt != nil {
		s := resolved.LastSessionAt.UTC().Format(time.RFC3339)
		resp.Event.LastSessionAt = &s
	}

	// Venue name(s) are cheap (one aggregate query keyed by event id) and
	// presentational — a lookup failure must not turn a resolved page into a
	// 500 (mirrors hydrateFeedVenueNames in public_feed.go).
	if h.publicFeedQueries != nil {
		names, err := h.publicFeedQueries.ListEventVenueNames(ctx, []uuid.UUID{resolved.EventID})
		if err != nil {
			h.logger.Warn("public_page: venue-name hydration failed",
				slog.String("event_id", resolved.EventID.String()),
				slog.String("error", err.Error()),
			)
		} else if vn, ok := names[resolved.EventID]; ok {
			resp.Event.VenueNames = vn
		}
	}

	w.Header().Set("Cache-Control", "public, max-age=30, stale-while-revalidate=15")
	httputil.WriteJSON(w, http.StatusOK, resp)
}
