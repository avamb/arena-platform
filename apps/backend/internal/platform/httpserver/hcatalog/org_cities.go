// org_cities.go lets an organization add a city inside an existing country
// (POST /v1/organizations/{org_id}/cities, permission city.create, migration
// 0113). An organizer creating a venue in a city arena does not know yet must
// not wait for a platform operator; geo.admin (/v1/admin/geo/cities) stays
// the platform-level surface and can never be carried by an API key.
//
// The call is idempotent by NAME: when the country already has a city whose
// stored name in any locale matches (case- and whitespace-insensitive), or
// whose slug is the one the name would produce, that city is returned with
// 200 and nothing is written. Otherwise the city is created (201) with a slug
// derived from the name (geoslug.Slugify, the same derivation the imports
// use) and made unique against the GLOBAL cities_slug_uniq constraint by
// suffixing the country code and then a counter. Concurrent creates of the
// same name in one country are serialized by a transaction-scoped advisory
// lock, so they cannot both miss the lookup.
package hcatalog

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/geoslug"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// cityI18nNamespace is where localized city names live (key = cities.slug).
const cityI18nNamespace = "geo.cities"

// maxCitySlugAttempts bounds the suffix search for a free slug.
const maxCitySlugAttempts = 50

// OrgCityResponse mirrors a GET /v1/geo/cities item.
type OrgCityResponse struct {
	ID          string `json:"id"`
	CountryID   string `json:"country_id"`
	CountryIso2 string `json:"country_iso2,omitempty"`
	Slug        string `json:"slug"`
	Name        string `json:"name"`
}

type createOrgCityRequest struct {
	CountryID string `json:"country_id"`
	Name      string `json:"name"`
	Locale    string `json:"locale"`
}

// normalizeCityLocale reduces a locale tag to the form i18n_text rows are
// keyed by ("cs-CZ" → "cs"). Empty or unusable input means English.
func normalizeCityLocale(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	if i := strings.IndexAny(s, "-_"); i >= 0 {
		s = s[:i]
	}
	if len(s) < 2 || len(s) > 3 {
		return "en"
	}
	for _, r := range s {
		if r < 'a' || r > 'z' {
			return "en"
		}
	}
	return s
}

// CitySlugCandidates lists, in order of preference, the slugs a new city
// may take: the slug of its name, then that slug with the country code, then
// numbered variants. A name with nothing transliterable (Hebrew, for one)
// starts from "city-<iso2>". Exported for unit tests.
func CitySlugCandidates(name, iso2 string, n int) []string {
	base := geoslug.Slugify(name)
	cc := strings.ToLower(strings.TrimSpace(iso2))
	out := make([]string, 0, n)
	if base != "" {
		out = append(out, base)
		if cc != "" {
			out = append(out, base+"-"+cc)
		}
	} else {
		base = "city"
		if cc != "" {
			base += "-" + cc
		}
		out = append(out, base)
	}
	stem := out[len(out)-1]
	for i := 2; len(out) < n; i++ {
		out = append(out, stem+"-"+strconv.Itoa(i))
	}
	return out
}

// HandleCreateOrgCity serves POST /v1/organizations/{org_id}/cities.
func (h *Handler) HandleCreateOrgCity(w http.ResponseWriter, r *http.Request) {
	if h.venueQueries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	if !h.requireOrgMembership(w, r, h.venueQueries, orgID) {
		return
	}
	var req createOrgCityRequest
	if !readJSONBody(w, r, "city", &req) {
		return
	}
	countryID, err := uuid.Parse(strings.TrimSpace(req.CountryID))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"city.invalid_country_id", "country_id must be a valid UUID", r,
			map[string]any{"field": "country_id"},
		))
		return
	}
	name := NormalizePromoterName(req.Name) // same trim + collapse rule
	if name == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"city.invalid_name", "name is required", r, map[string]any{"field": "name"},
		))
		return
	}
	locale := normalizeCityLocale(req.Locale)

	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope("dependency.database_unavailable", "failed to begin transaction", r))
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := h.venueQueries.WithTx(tx)

	country, err := qtx.GetCountryByID(ctx, countryID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
				"city.country_not_found", "the specified country does not exist", r,
				map[string]any{"field": "country_id"},
			))
			return
		}
		h.logger.Error("city: country lookup failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("city.create_failed", "failed to create city", r))
		return
	}

	cityID, created, err := createOrFindOrgCity(ctx, qtx, country, name, locale)
	if err != nil {
		h.logger.Error("city: create failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("city.create_failed", "failed to create city", r))
		return
	}
	city, err := qtx.GetCityWithName(ctx, cityID, locale)
	if err != nil {
		h.logger.Error("city: read back failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("city.create_failed", "failed to create city", r))
		return
	}
	if created {
		if err := h.promoterAudit(ctx, tx, r, "v1.city.create", "city", city.ID.String(), map[string]any{
			"org_id":     orgID.String(),
			"country_id": countryID.String(),
			"slug":       city.Slug,
			"city_name":  name,
			"locale":     locale,
		}); err != nil {
			h.logger.Error("city: audit write failed", slog.String("error", err.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("city.audit_failed", "failed to write audit event", r))
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("city.commit_failed", "failed to commit transaction", r))
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	httputil.WriteJSON(w, status, map[string]any{
		"city": OrgCityResponse{
			ID:          city.ID.String(),
			CountryID:   city.CountryID.String(),
			CountryIso2: city.CountryIso2,
			Slug:        city.Slug,
			Name:        city.Name,
		},
		"created": created,
	})
}

// createOrFindOrgCity returns the id of the country's city called name,
// creating it when there is none. Runs inside the caller's transaction and
// takes the per-country advisory lock first.
func createOrFindOrgCity(ctx context.Context, q *gen.Queries, country gen.CountryRow, name, locale string) (uuid.UUID, bool, error) {
	if err := q.LockCountryCities(ctx, country.ID); err != nil {
		return uuid.Nil, false, err
	}
	id, err := q.FindCityInCountryByName(ctx, country.ID, geoslug.NormalizeName(name), geoslug.Slugify(name))
	switch {
	case err == nil:
		return id, false, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return uuid.Nil, false, err
	}

	slug := ""
	for _, candidate := range CitySlugCandidates(name, country.Iso2, maxCitySlugAttempts) {
		taken, err := q.CitySlugTaken(ctx, candidate)
		if err != nil {
			return uuid.Nil, false, err
		}
		if !taken {
			slug = candidate
			break
		}
	}
	if slug == "" {
		// Every candidate is taken — astronomically unlikely; fall back to a
		// slug that cannot collide.
		slug = "city-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	}
	city, err := q.InsertCity(ctx, country.ID, slug)
	if err != nil {
		return uuid.Nil, false, err
	}
	if err := q.InsertI18nTextIfAbsent(ctx, cityI18nNamespace, slug, locale, name); err != nil {
		return uuid.Nil, false, err
	}
	// Readers fall back to English, then to the slug: without an English row
	// a city named in Czech would show as its slug everywhere else.
	if locale != "en" {
		if err := q.InsertI18nTextIfAbsent(ctx, cityI18nNamespace, slug, "en", name); err != nil {
			return uuid.Nil, false, err
		}
	}
	return city.ID, true, nil
}
