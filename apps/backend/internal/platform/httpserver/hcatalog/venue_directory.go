// venue_directory.go serves GET /v1/organizations/{org_id}/venue-directory
// (spec 64 section 2.1.1, contract "Venue directory"): the venues an
// organization may hold a date at — its own venues that are not archived and
// the verified venues of other organizations — grouped for a picker by
// country and city, with whether a seating plan is ready there. Contacts of a
// venue are never part of the answer. Access is the one of shared-venues:
// venue.read and the organization check of requireOrgMembership.
//
// One SQL round per block: countries, quick-pick sources, cities (only with
// ?country=), venues (only with a filter).
package hcatalog

import (
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/worldgeo"
)

const (
	venueDirectoryDefaultLimit = 100
	venueDirectoryMaxLimit     = 200
	venueDirectoryQuickPickMax = 8
	venueDirectoryMaxQueryLen  = 100
)

type venueDirectoryCountry struct {
	Iso2      string  `json:"iso2"`
	CountryID *string `json:"country_id"`
	Name      string  `json:"name"`
	Region    string  `json:"region"`
	Venues    int64   `json:"venues"`
	OwnVenues int64   `json:"own_venues"`
}

type venueDirectoryCity struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	CountryIso2 string `json:"country_iso2"`
	Venues      int64  `json:"venues"`
	OwnVenues   int64  `json:"own_venues"`
}

type venueDirectoryVenue struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	CityID          *string  `json:"city_id"`
	CityName        *string  `json:"city_name"`
	CountryIso2     *string  `json:"country_iso2"`
	Address         *string  `json:"address"`
	AddressLine1    *string  `json:"address_line1"`
	GeoLat          *float64 `json:"geo_lat"`
	GeoLng          *float64 `json:"geo_lng"`
	Timezone        *string  `json:"timezone"`
	CapacityDefault *int32   `json:"capacity_default"`
	Own             bool     `json:"own"`
	ReviewStatus    string   `json:"review_status"`
	HasPlan         bool     `json:"has_plan"`
	HasOwnPlan      bool     `json:"has_own_plan"`
}

// escapeLike escapes the LIKE wildcards of a typed text (backslash escape).
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// HandleVenueDirectory answers GET /v1/organizations/{org_id}/venue-directory.
func (h *Handler) HandleVenueDirectory(w http.ResponseWriter, r *http.Request) {
	if h.venueQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r))
		return
	}
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	if !h.requireOrgMembership(w, r, h.venueQueries, orgID) {
		return
	}

	query := r.URL.Query()
	country := strings.ToUpper(strings.TrimSpace(query.Get("country")))
	if country != "" && !iso2Re.MatchString(country) {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"venue.invalid_country", "Send country as a 2-letter ISO 3166-1 code, for example ES.", r,
			map[string]any{"field": "country"}))
		return
	}
	var cityID *uuid.UUID
	if raw := strings.TrimSpace(query.Get("city_id")); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"venue.invalid_city_id", "Send city_id as a UUID.", r,
				map[string]any{"field": "city_id"}))
			return
		}
		cityID = &id
	}
	q := strings.Join(strings.Fields(query.Get("q")), " ")
	if utf8.RuneCountInString(q) > venueDirectoryMaxQueryLen {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"venue.invalid_query", "The search text is too long. Use at most 100 characters.", r,
			map[string]any{"field": "q", "max_length": venueDirectoryMaxQueryLen}))
		return
	}
	limit := venueDirectoryDefaultLimit
	if raw := strings.TrimSpace(query.Get("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > venueDirectoryMaxLimit {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"venue.invalid_limit", "Send limit as a whole number from 1 to 200.", r,
				map[string]any{"field": "limit"}))
			return
		}
		limit = n
	}
	locale := requestLocale(r)
	ctx := r.Context()

	countryRows, err := h.venueQueries.ListVenueDirectoryCountries(ctx, orgID, locale)
	if err != nil {
		h.venueDirectoryFailed(w, r, "countries", err)
		return
	}
	countries := make([]venueDirectoryCountry, 0, len(countryRows))
	for _, c := range countryRows {
		item := venueDirectoryCountry{Iso2: c.Iso2, Venues: c.Venues, OwnVenues: c.OwnVenues}
		world, inWorld := worldgeo.Lookup(c.Iso2)
		switch {
		case c.Name != nil && *c.Name != "":
			item.Name = *c.Name
		case inWorld:
			item.Name = world.Name(locale)
		default:
			item.Name = c.Iso2
		}
		if c.CountryID != nil {
			id := c.CountryID.String()
			item.CountryID = &id
		}
		switch {
		case c.Region != nil && worldgeo.ValidRegion(*c.Region):
			item.Region = *c.Region
		case inWorld:
			item.Region = world.Region
		default:
			item.Region = "other"
		}
		countries = append(countries, item)
	}

	orgCountry, refCountries, err := h.venueQueries.ListVenueDirectoryQuickPickSources(ctx, orgID)
	if err != nil {
		h.venueDirectoryFailed(w, r, "quick pick", err)
		return
	}
	quickPick := venueDirectoryQuickPick(countries, orgCountry, refCountries)

	cities := []venueDirectoryCity{}
	if country != "" {
		cityRows, err := h.venueQueries.ListVenueDirectoryCities(ctx, orgID, locale, country)
		if err != nil {
			h.venueDirectoryFailed(w, r, "cities", err)
			return
		}
		for _, c := range cityRows {
			cities = append(cities, venueDirectoryCity{ID: c.ID.String(), Name: c.Name, CountryIso2: c.CountryIso2, Venues: c.Venues, OwnVenues: c.OwnVenues})
		}
	}

	venues := []venueDirectoryVenue{}
	var total int64
	if country != "" || cityID != nil || q != "" {
		rows, err := h.venueQueries.ListVenueDirectoryVenues(ctx, orgID, locale, country, cityID, escapeLike(q), int32(limit)) //nolint:gosec // 1..200
		if err != nil {
			h.venueDirectoryFailed(w, r, "venues", err)
			return
		}
		for _, v := range rows {
			total = v.Total
			item := venueDirectoryVenue{
				ID: v.ID.String(), Name: v.Name, CityName: v.CityName, CountryIso2: v.CountryIso2,
				Address: v.Address, AddressLine1: v.AddressLine1, GeoLat: v.GeoLat, GeoLng: v.GeoLng,
				Timezone: v.Timezone, CapacityDefault: v.CapacityDefault, Own: v.Own,
				ReviewStatus: v.ReviewStatus, HasPlan: v.HasPlan, HasOwnPlan: v.HasOwnPlan,
			}
			if v.CityID != nil {
				id := v.CityID.String()
				item.CityID = &id
			}
			venues = append(venues, item)
		}
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"quick_pick": quickPick,
		"countries":  countries,
		"cities":     cities,
		"venues":     venues,
		"total":      total,
	})
}

// venueDirectoryQuickPick orders the quick pick: the countries of the
// organization's own venues (most own venues first), the organization's own
// country, then the reference countries where organizers work. At most eight
// codes, no duplicates.
func venueDirectoryQuickPick(countries []venueDirectoryCountry, orgCountry string, ref []gen.OrgCountryCount) []string {
	out := []string{}
	seen := map[string]bool{}
	add := func(iso2 string) {
		iso2 = strings.ToUpper(strings.TrimSpace(iso2))
		if iso2 == "" || seen[iso2] || len(out) >= venueDirectoryQuickPickMax {
			return
		}
		seen[iso2] = true
		out = append(out, iso2)
	}
	own := make([]venueDirectoryCountry, 0, len(countries))
	for _, c := range countries {
		if c.OwnVenues > 0 {
			own = append(own, c)
		}
	}
	sort.SliceStable(own, func(a, b int) bool {
		if own[a].OwnVenues != own[b].OwnVenues {
			return own[a].OwnVenues > own[b].OwnVenues
		}
		return own[a].Iso2 < own[b].Iso2
	})
	for _, c := range own {
		add(c.Iso2)
	}
	add(orgCountry)
	for _, c := range ref {
		add(c.Iso2)
	}
	return out
}

func (h *Handler) venueDirectoryFailed(w http.ResponseWriter, r *http.Request, block string, err error) {
	h.logger.Error("venue: directory read failed", slog.String("block", block), slog.String("error", err.Error()))
	httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
		"venue.list_failed", "failed to list venues", r))
}
