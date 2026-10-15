// geo_helpers.go holds the three read helpers the web event center's venue
// form needs (plan stage 0, A5), all public like the other geo reads:
//
//   - GET /v1/geo/countries/{iso2}/timezones - the zones of a country, one of
//     them marked as the only one when the country has a single zone;
//   - GET /v1/geo/countries/{iso2}/currency  - the country's ISO 4217 currency;
//   - the ?country= and ?q= parameters of GET /v1/geo/cities (see
//     HandleListCities), which add city suggestions from the embedded GeoNames
//     data of internal/platform/geotz.
//
// Nothing here writes anything: a suggestion is only ever a hint the client
// shows, and a city or venue is created through the existing routes.
package hgeo

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/geotz"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// iso2Pattern is the shape of a country code in a path or query.
var iso2Pattern = regexp.MustCompile(`^[A-Za-z]{2}$`)

// maxCityQueryRunes caps a city search text so a long string cannot make the
// scan expensive.
const maxCityQueryRunes = 64

// Sources of an item of the cities list.
const (
	sourcePlatform = "platform"
	sourceGeoNames = "geonames"
)

// timezoneResponse is one zone of a country.
type timezoneResponse struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// zoneLabel names a zone for a person: the largest city in it, else the last
// part of the IANA name.
func zoneLabel(iso2, zone string) string {
	if c := geotz.CityOf(iso2, zone); c != "" {
		return c
	}
	if i := strings.LastIndex(zone, "/"); i >= 0 {
		zone = zone[i+1:]
	}
	return strings.ReplaceAll(zone, "_", " ")
}

// HandleCountryTimezones serves GET /v1/geo/countries/{iso2}/timezones.
//
// A country with one zone answers that zone and single=true; a country with
// several answers all of them, the zone of its biggest city first, and
// single=false (the client must ask - Spain has the Canaries, Portugal the
// Azores). A country the data does not know is 404 geo.country_not_found.
func (h *Handler) HandleCountryTimezones(w http.ResponseWriter, r *http.Request) {
	iso2, ok := pathISO2(w, r)
	if !ok {
		return
	}
	zones := []timezoneResponse{}
	if only := geotz.ForCountry(iso2); only != "" {
		zones = append(zones, timezoneResponse{ID: only, Label: zoneLabel(iso2, only)})
	} else {
		for _, z := range geotz.ZonesFor(iso2) {
			zones = append(zones, timezoneResponse{ID: z.ID, Label: zoneLabel(iso2, z.ID)})
		}
	}
	if len(zones) == 0 {
		writeCountryNotFound(w, r, iso2)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"timezones": zones,
		"single":    len(zones) == 1,
	})
}

// HandleCountryCurrency serves GET /v1/geo/countries/{iso2}/currency.
//
// The currency is the one stored on the platform's country row
// (countries.currency, the base of the session currency chain), never a second
// table: a country that has no row is 404 geo.country_not_found.
func (h *Handler) HandleCountryCurrency(w http.ResponseWriter, r *http.Request) {
	iso2, ok := pathISO2(w, r)
	if !ok {
		return
	}
	lookup := h.currencyLookup
	if lookup == nil {
		if h.queries == nil {
			httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
				"dependency.database_unavailable", "database is not available", r))
			return
		}
		lookup = func(ctx context.Context, code string) (string, error) {
			row, err := h.queries.GetCountryByISO2(ctx, code)
			return row.Currency, err
		}
	}
	cur, err := lookup(r.Context(), iso2)
	if errors.Is(err, pgx.ErrNoRows) {
		writeCountryNotFound(w, r, iso2)
		return
	}
	if err != nil {
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"geo.country_currency_failed", "failed to read the country currency", r))
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"currency": strings.TrimSpace(cur)})
}

// pathISO2 reads and validates {iso2}; on a malformed one it answers 400 and
// reports false.
func pathISO2(w http.ResponseWriter, r *http.Request) (string, bool) {
	raw := chi.URLParam(r, "iso2")
	if !iso2Pattern.MatchString(raw) {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"geo.invalid_iso2", "the country code must be two letters", r,
			map[string]any{"param": "iso2"}))
		return "", false
	}
	return strings.ToUpper(raw), true
}

func writeCountryNotFound(w http.ResponseWriter, r *http.Request, iso2 string) {
	httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelopeWithDetails(
		"geo.country_not_found", "no country with that code", r,
		map[string]any{"iso2": iso2}))
}

// cityQuery reads the optional ?country= (two letters) and ?q= parameters of
// the cities list. A blank q counts as absent. On a malformed country it
// answers 400 and reports false.
func cityQuery(w http.ResponseWriter, r *http.Request) (country, q string, ok bool) {
	if raw := strings.TrimSpace(r.URL.Query().Get("country")); raw != "" {
		if !iso2Pattern.MatchString(raw) {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"geo.invalid_country", "query parameter 'country' must be a two-letter country code", r,
				map[string]any{"param": "country"}))
			return "", "", false
		}
		country = strings.ToUpper(raw)
	}
	q = strings.TrimSpace(r.URL.Query().Get("q"))
	if utf8.RuneCountInString(q) > maxCityQueryRunes {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"geo.invalid_query", "query parameter 'q' is too long", r,
			map[string]any{"param": "q", "max_length": maxCityQueryRunes}))
		return "", "", false
	}
	if geotz.Normalize(q) == "" {
		q = ""
	}
	return country, q, true
}

// platformCityZone is the zone of a platform city, best effort: the zone the
// city's name points at, else the country's only zone, else "".
func platformCityZone(iso2, name, slug string) string {
	if z := geotz.ForCity(iso2, name); z != "" {
		return z
	}
	if z := geotz.ForCity(iso2, slug); z != "" {
		return z
	}
	return geotz.SingleZone(iso2)
}

// matchesCityQuery reports whether the normalised query is a prefix of the
// city's normalised name or slug.
func matchesCityQuery(nq, name, slug string) bool {
	return strings.HasPrefix(geotz.Normalize(name), nq) || strings.HasPrefix(geotz.Normalize(slug), nq)
}

// assembleCities builds the list answer from the platform's city rows: with a
// country code and no country_id filter (byID) it keeps that country's cities;
// with q it keeps the cities whose name or slug starts with it, caps the
// result to 20, and returns the embedded GeoNames suggestions (at most 20, for
// the country named by `country`) as the second value. Suggestions are nil
// without q.
func assembleCities(rows []gen.ListCityRow, country string, byID bool, q string) ([]cityResponse, []citySuggestion) {
	nq := geotz.Normalize(q)
	result := make([]cityResponse, 0, len(rows))
	for _, row := range rows {
		if country != "" && !byID && !strings.EqualFold(row.CountryIso2, country) {
			continue
		}
		if nq != "" && !matchesCityQuery(nq, row.Name, row.Slug) {
			continue
		}
		result = append(result, cityResponse{
			ID:          row.ID.String(),
			CountryID:   row.CountryID.String(),
			CountryIso2: row.CountryIso2,
			Slug:        row.Slug,
			Name:        row.Name,
			Timezone:    platformCityZone(row.CountryIso2, row.Name, row.Slug),
			Source:      sourcePlatform,
		})
	}
	if nq == "" {
		return result, nil
	}
	if len(result) > geotz.SearchLimit {
		result = result[:geotz.SearchLimit]
	}
	return result, geoNamesSuggestions(country, q, result)
}

// geoNamesSuggestions turns the embedded data's matches into suggestions,
// leaving out a place the platform already has (same normalised name or slug),
// which is listed with its own id in `cities` instead. Without a country there
// is nothing to search, and the answer is an empty (non-nil) array.
func geoNamesSuggestions(country, q string, have []cityResponse) []citySuggestion {
	out := []citySuggestion{}
	if country == "" {
		return out
	}
	known := map[string]bool{}
	for _, c := range have {
		known[geotz.Normalize(c.Name)] = true
		known[geotz.Normalize(c.Slug)] = true
	}
	for _, m := range geotz.SearchCities(country, q) {
		if known[geotz.Normalize(m.Name)] {
			continue
		}
		out = append(out, citySuggestion{
			Name:        m.Name,
			CountryIso2: country,
			Timezone:    m.Timezone,
			Source:      sourceGeoNames,
		})
	}
	return out
}
