// public_site.go — GET /v1/public/feeds/{feed_token}/site: the organizer's
// own block for the "thank you" page of a static site. Reads only
// sales_channels.settings.site.thank_you and answers nothing else of the
// channel's settings.
package hfeed

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

const (
	thankYouTitleMax = 120
	thankYouTextMax  = 1000
	thankYouCodeMax  = 64
)

// SiteThankYou is the organizer's thank-you block.
type SiteThankYou struct {
	Title     string  `json:"title"`
	Text      string  `json:"text"`
	PromoCode *string `json:"promo_code"`
	URL       *string `json:"url"`
}

func clipRunes(s string, n int) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return strings.TrimSpace(string([]rune(s)[:n]))
}

// ParseSiteThankYou sanitises the stored settings.site.thank_you JSON. It
// returns nil for a missing or invalid value or when title and text are both
// empty.
func ParseSiteThankYou(raw []byte) *SiteThankYou {
	if len(raw) == 0 {
		return nil
	}
	var in map[string]any
	if json.Unmarshal(raw, &in) != nil || in == nil {
		return nil
	}
	str := func(k string, n int) string {
		v, _ := in[k].(string)
		return clipRunes(v, n)
	}
	out := &SiteThankYou{Title: str("title", thankYouTitleMax), Text: str("text", thankYouTextMax)}
	if out.Title == "" && out.Text == "" {
		return nil
	}
	if c := str("promo_code", thankYouCodeMax); c != "" {
		out.PromoCode = &c
	}
	// A url longer than the cap is dropped, never cut: a cut link would lead elsewhere.
	if raw, _ := in["url"].(string); len(raw) <= 2048 {
		u := strings.TrimSpace(raw)
		if p, err := url.Parse(u); err == nil && p.Scheme == "https" && p.Host != "" && p.User == nil {
			out.URL = &u
		}
	}
	return out
}

// HandlePublicFeedSite answers GET /v1/public/feeds/{feed_token}/site.
func (h *Handler) HandlePublicFeedSite(w http.ResponseWriter, r *http.Request) {
	feedToken := chi.URLParam(r, "feed_token")
	if !h.checkFeedToken(w, r, feedToken) {
		return
	}
	raw, err := h.publicFeedQueries.GetFeedTokenSiteThankYou(r.Context(), feedToken)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		h.logger.Error("public_site: read failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"feed.fetch_failed", "We could not load the site settings. Try again in a minute.", r,
		))
		return
	}
	if errors.Is(err, pgx.ErrNoRows) {
		httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
			"feed.token_not_found", "This ticket link does not work any more. Ask the organizer for a new link.", r,
		))
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=60, stale-while-revalidate=30")
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"thank_you": ParseSiteThankYou(raw)})
}
