// public_site.go — GET /v1/public/feeds/{feed_token}/site: the organizer's
// own block for the "thank you" page of a static site. Reads only
// sales_channels.settings.site.thank_you and .socials and answers nothing
// else of the channel's settings.
package hfeed

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
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

// SiteSocial is one social-network link of the organizer.
type SiteSocial struct {
	Network string `json:"network"`
	URL     string `json:"url"`
}

const (
	socialsMax   = 8
	socialURLMax = 2048
)

var socialNetworks = map[string]bool{
	"facebook": true, "instagram": true, "telegram": true, "tiktok": true, "youtube": true,
	"x": true, "whatsapp": true, "linkedin": true, "vk": true, "threads": true,
}

func cleanSocial(network, raw string) (SiteSocial, bool) {
	network = strings.ToLower(strings.TrimSpace(network))
	if !socialNetworks[network] || len(raw) > socialURLMax {
		return SiteSocial{}, false
	}
	u := strings.TrimSpace(raw)
	p, err := url.Parse(u)
	if err != nil || p.Scheme != "https" || p.Host == "" || p.User != nil {
		return SiteSocial{}, false
	}
	return SiteSocial{Network: network, URL: u}, true
}

// ParseSiteSocials sanitises the stored settings.site.socials JSON: either an
// array of {network,url} or an object network -> url. Unknown networks and
// links that are not plain https are dropped, at most 8 are kept, in order
// (an object has no order of its own: it is read in alphabetical key order).
// Never nil: absent or invalid gives an empty list.
func ParseSiteSocials(raw []byte) []SiteSocial {
	out := []SiteSocial{}
	if len(raw) == 0 {
		return out
	}
	var arr []map[string]any
	if json.Unmarshal(raw, &arr) == nil {
		for _, e := range arr {
			n, _ := e["network"].(string)
			u, _ := e["url"].(string)
			if c, ok := cleanSocial(n, u); ok && len(out) < socialsMax {
				out = append(out, c)
			}
		}
		return out
	}
	var obj map[string]any
	if json.Unmarshal(raw, &obj) == nil {
		keys := make([]string, 0, len(obj))
		for k := range obj {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			u, _ := obj[k].(string)
			if c, ok := cleanSocial(k, u); ok && len(out) < socialsMax {
				out = append(out, c)
			}
		}
	}
	return out
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
	raw, socialsRaw, err := h.publicFeedQueries.GetFeedTokenSiteBlocks(r.Context(), feedToken)
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
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"thank_you": ParseSiteThankYou(raw), "socials": ParseSiteSocials(socialsRaw)})
}
