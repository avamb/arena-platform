// world_countries.go serves GET /v1/geo/world-countries (spec 64 section
// 2.1.1): every ISO 3166-1 country of the embedded world table
// (internal/platform/worldgeo) with its region, currency and whether the arena
// reference and an organization already have it. Public, like the other geo
// reads. A client groups the list by region and puts the countries with
// organizers on top as a quick pick.
package hgeo

import (
	"net/http"
	"sort"
	"strings"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/geotz"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/i18n"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/worldgeo"
)

// worldCountryResponse is one country of GET /v1/geo/world-countries.
type worldCountryResponse struct {
	Iso2          string  `json:"iso2"`
	Iso3          string  `json:"iso3"`
	Name          string  `json:"name"`
	NameEn        string  `json:"name_en"`
	Currency      string  `json:"currency"`
	Region        string  `json:"region"`
	InReference   bool    `json:"in_reference"`
	CountryID     *string `json:"country_id"`
	HasOrganizers bool    `json:"has_organizers"`
}

// requestLocale is GeoLocale with ?locale= accepted as an alias of ?lang=
// (?lang= wins when both are sent).
func (h *Handler) requestLocale(r *http.Request) string {
	lang := strings.TrimSpace(r.URL.Query().Get("lang"))
	if lang == "" {
		lang = strings.TrimSpace(r.URL.Query().Get("locale"))
	}
	defaultLocale := "en"
	var supported []string
	if h.cfg != nil {
		if h.cfg.DefaultLocale != "" {
			defaultLocale = h.cfg.DefaultLocale
		}
		supported = h.cfg.ActiveLocales
	}
	return i18n.NegotiateLocale(r.Header.Get("Accept-Language"), lang, "", defaultLocale, supported)
}

// HandleListWorldCountries serves GET /v1/geo/world-countries. The name is
// Russian for the locale ru and English for any other; the list is sorted by
// that name.
func (h *Handler) HandleListWorldCountries(w http.ResponseWriter, r *http.Request) {
	if h.queries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r))
		return
	}
	ctx := r.Context()
	locale := h.requestLocale(r)

	refs, err := h.queries.ListCountries(ctx, "en")
	if err != nil {
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"geo.list_countries_failed", "failed to list countries", r))
		return
	}
	refID := make(map[string]string, len(refs))
	for _, c := range refs {
		refID[strings.ToUpper(c.Iso2)] = c.ID.String()
	}
	organizers, err := h.queries.ListOrganizerCountries(ctx)
	if err != nil {
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"geo.list_countries_failed", "failed to list countries", r))
		return
	}
	withOrganizers := make(map[string]bool, len(organizers))
	for _, c := range organizers {
		withOrganizers[c] = true
	}

	all := worldgeo.All()
	out := make([]worldCountryResponse, 0, len(all))
	for _, c := range all {
		item := worldCountryResponse{
			Iso2:          c.Iso2,
			Iso3:          c.Iso3,
			Name:          c.Name(locale),
			NameEn:        c.NameEn,
			Currency:      c.Currency,
			Region:        c.Region,
			HasOrganizers: withOrganizers[c.Iso2],
		}
		if id, ok := refID[c.Iso2]; ok {
			idCopy := id
			item.InReference, item.CountryID = true, &idCopy
		}
		out = append(out, item)
	}
	sort.SliceStable(out, func(a, b int) bool {
		na, nb := geotz.Normalize(out[a].Name), geotz.Normalize(out[b].Name)
		if na != nb {
			return na < nb
		}
		return out[a].Iso2 < out[b].Iso2
	})
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"regions":   worldgeo.Regions(),
		"countries": out,
	})
}
