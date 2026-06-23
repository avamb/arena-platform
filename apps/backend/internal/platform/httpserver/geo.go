// geo.go implements the geo reference API endpoints for countries and cities
// (feature #123, Wave 3 — Catalog).
//
// Public read endpoints (no authentication required):
//   - GET /v1/geo/countries        — list all countries with localized names
//   - GET /v1/geo/cities           — list cities (optional ?country_id= filter)
//
// Admin write endpoints (require JWT + "geo.admin" permission):
//   - POST  /v1/admin/geo/countries          — create a new country
//   - PATCH /v1/admin/geo/countries/{iso2}   — update a country's iso3 / slug
//   - POST  /v1/admin/geo/cities             — create a new city
//   - PATCH /v1/admin/geo/cities/{id}        — update a city's slug
//
// i18n linkage:
//   Localized country/city names are stored in the i18n_text table under the
//   namespaces "geo.countries" (key = ISO 3166-1 alpha-2 code) and "geo.cities"
//   (key = city slug). The SQL queries perform LEFT JOINs against i18n_text with
//   locale fallback: requested locale → English → iso2/slug.
//
//   Admin POST/PATCH endpoints accept optional "name_en" and "name_ru" fields in
//   the request body and upsert the corresponding i18n_text rows in the same
//   transaction.
package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/i18n"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ─────────────────────────────────────────────────────────────────────────────
// Response types
// ─────────────────────────────────────────────────────────────────────────────

// countryResponse is the JSON body of a single country in list/create/update
// responses.
type countryResponse struct {
	ID   string `json:"id"`
	Iso2 string `json:"iso2"`
	Iso3 string `json:"iso3"`
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// cityResponse is the JSON body of a single city in list/create/update
// responses.
type cityResponse struct {
	ID          string `json:"id"`
	CountryID   string `json:"country_id"`
	CountryIso2 string `json:"country_iso2,omitempty"`
	Slug        string `json:"slug"`
	Name        string `json:"name"`
}

// ─────────────────────────────────────────────────────────────────────────────
// geoLocale extracts the effective locale from the HTTP request.
//
// Priority chain (mirrors LocaleMiddleware):
//  1. ?lang= query parameter
//  2. Accept-Language header
//  3. configured default locale (from Server.cfg)
//
// Returns "en" as the ultimate fallback when cfg is nil or has no default.
// ─────────────────────────────────────────────────────────────────────────────
func (s *Server) geoLocale(r *http.Request) string {
	defaultLocale := "en"
	var supported []string
	if s.cfg != nil {
		if s.cfg.DefaultLocale != "" {
			defaultLocale = s.cfg.DefaultLocale
		}
		supported = s.cfg.ActiveLocales
	}
	return i18n.NegotiateLocale(
		r.Header.Get("Accept-Language"),
		r.URL.Query().Get("lang"),
		"",
		defaultLocale,
		supported,
	)
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/geo/countries
// ─────────────────────────────────────────────────────────────────────────────

// handleListCountries serves GET /v1/geo/countries.
//
// Returns a JSON array of all countries sorted by iso2. Each item includes
// the localized name resolved from i18n_text (falls back to English, then to
// the iso2 code itself).
func (s *Server) handleListCountries(w http.ResponseWriter, r *http.Request) {
	if s.geoQueries == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable",
			"database is not available",
			r,
		))
		return
	}
	ctx := r.Context()
	locale := s.geoLocale(r)

	rows, err := s.geoQueries.ListCountries(ctx, locale)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"geo.list_countries_failed",
			"failed to list countries",
			r,
		))
		return
	}

	// Guarantee a non-nil JSON array in the response body.
	result := make([]countryResponse, 0, len(rows))
	for _, row := range rows {
		result = append(result, countryResponse{
			ID:   row.ID.String(),
			Iso2: row.Iso2,
			Iso3: row.Iso3,
			Slug: row.Slug,
			Name: row.Name,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"countries": result})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/geo/cities
// ─────────────────────────────────────────────────────────────────────────────

// handleListCities serves GET /v1/geo/cities.
//
// Optional query parameter:
//   - country_id (UUID) — when provided, only cities belonging to that country
//     are returned.
//
// Returns a JSON array of cities. Localized names are resolved from i18n_text
// with the same fallback chain as handleListCountries.
func (s *Server) handleListCities(w http.ResponseWriter, r *http.Request) {
	if s.geoQueries == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable",
			"database is not available",
			r,
		))
		return
	}
	ctx := r.Context()
	locale := s.geoLocale(r)

	// Parse optional country_id filter.
	var countryID *uuid.UUID
	if raw := r.URL.Query().Get("country_id"); raw != "" {
		parsed, err := uuid.Parse(raw)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
				"geo.invalid_country_id",
				"query parameter 'country_id' must be a valid UUID",
				r,
				map[string]any{"param": "country_id"},
			))
			return
		}
		countryID = &parsed
	}

	rows, err := s.geoQueries.ListCities(ctx, locale, countryID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"geo.list_cities_failed",
			"failed to list cities",
			r,
		))
		return
	}

	result := make([]cityResponse, 0, len(rows))
	for _, row := range rows {
		result = append(result, cityResponse{
			ID:          row.ID.String(),
			CountryID:   row.CountryID.String(),
			CountryIso2: row.CountryIso2,
			Slug:        row.Slug,
			Name:        row.Name,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"cities": result})
}

// ─────────────────────────────────────────────────────────────────────────────
// Admin: POST /v1/admin/geo/countries
// ─────────────────────────────────────────────────────────────────────────────

// createCountryRequest is the request body for POST /v1/admin/geo/countries.
type createCountryRequest struct {
	Iso2   string `json:"iso2"`
	Iso3   string `json:"iso3"`
	Slug   string `json:"slug"`
	NameEn string `json:"name_en"`
	NameRu string `json:"name_ru"`
}

// handleCreateCountry serves POST /v1/admin/geo/countries.
// Requires JWT + "geo.admin" permission (enforced by middleware in mountV1Routes).
func (s *Server) handleCreateCountry(w http.ResponseWriter, r *http.Request) {
	if s.geoQueries == nil || s.pool == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable",
			"database is not available",
			r,
		))
		return
	}
	ctx := r.Context()

	body, err := io.ReadAll(io.LimitReader(r.Body, 16*1024))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("geo.invalid_body", "cannot read request body: "+err.Error(), r))
		return
	}
	if len(body) == 0 {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("geo.empty_body", "request body is required", r))
		return
	}

	var req createCountryRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("geo.invalid_json", "request body is not valid JSON", r))
		return
	}

	// Validate and normalize required fields.
	req.Iso2 = strings.TrimSpace(strings.ToUpper(req.Iso2))
	req.Iso3 = strings.TrimSpace(strings.ToUpper(req.Iso3))
	req.Slug = strings.TrimSpace(strings.ToLower(req.Slug))

	if len(req.Iso2) != 2 {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"geo.invalid_iso2", "iso2 must be exactly 2 uppercase letters", r,
			map[string]any{"field": "iso2"},
		))
		return
	}
	if len(req.Iso3) != 3 {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"geo.invalid_iso3", "iso3 must be exactly 3 uppercase letters", r,
			map[string]any{"field": "iso3"},
		))
		return
	}
	if req.Slug == "" {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"geo.invalid_slug", "slug is required", r,
			map[string]any{"field": "slug"},
		))
		return
	}

	// Begin transaction: InsertCountry + upsert i18n_text in one round-trip.
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "failed to begin transaction", r,
		))
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	qtx := s.geoQueries.WithTx(tx)

	country, err := qtx.InsertCountry(ctx, req.Iso2, req.Iso3, req.Slug)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
			writeJSON(w, http.StatusConflict, errorEnvelope(
				"geo.country_exists",
				"a country with that iso2 or slug already exists",
				r,
			))
			return
		}
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"geo.insert_country_failed", "failed to insert country", r,
		))
		return
	}

	// Upsert i18n_text for localized names (if provided).
	if err := geoUpsertI18nName(ctx, tx, "geo.countries", req.Iso2, req.NameEn, req.NameRu); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"geo.upsert_name_failed", "failed to upsert localized name", r,
		))
		return
	}

	if err := tx.Commit(ctx); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"geo.commit_failed", "failed to commit transaction", r,
		))
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"country": countryResponse{
			ID:   country.ID.String(),
			Iso2: country.Iso2,
			Iso3: country.Iso3,
			Slug: country.Slug,
			Name: geoFirstNonEmpty(req.NameEn, req.Iso2),
		},
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Admin: PATCH /v1/admin/geo/countries/{iso2}
// ─────────────────────────────────────────────────────────────────────────────

// updateCountryRequest is the request body for PATCH /v1/admin/geo/countries/{iso2}.
type updateCountryRequest struct {
	Iso3   string `json:"iso3"`
	Slug   string `json:"slug"`
	NameEn string `json:"name_en"`
	NameRu string `json:"name_ru"`
}

// handleUpdateCountry serves PATCH /v1/admin/geo/countries/{iso2}.
func (s *Server) handleUpdateCountry(w http.ResponseWriter, r *http.Request) {
	if s.geoQueries == nil || s.pool == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()
	iso2 := strings.ToUpper(chi.URLParam(r, "iso2"))
	if len(iso2) != 2 {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"geo.invalid_iso2", "iso2 path parameter must be exactly 2 letters", r,
			map[string]any{"param": "iso2"},
		))
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 16*1024))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("geo.invalid_body", "cannot read request body: "+err.Error(), r))
		return
	}
	if len(body) == 0 {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("geo.empty_body", "request body is required", r))
		return
	}

	// Load current country for partial-update semantics.
	current, err := s.geoQueries.GetCountryByISO2(ctx, iso2)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, errorEnvelope("geo.country_not_found", "country not found", r))
			return
		}
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"geo.get_country_failed", "failed to get country", r,
		))
		return
	}

	var req updateCountryRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("geo.invalid_json", "request body is not valid JSON", r))
		return
	}

	// Apply partial update: keep existing value when field is empty.
	newIso3 := strings.TrimSpace(strings.ToUpper(req.Iso3))
	if newIso3 == "" {
		newIso3 = current.Iso3
	}
	newSlug := strings.TrimSpace(strings.ToLower(req.Slug))
	if newSlug == "" {
		newSlug = current.Slug
	}

	if len(newIso3) != 3 {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"geo.invalid_iso3", "iso3 must be exactly 3 uppercase letters", r,
			map[string]any{"field": "iso3"},
		))
		return
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "failed to begin transaction", r,
		))
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	qtx := s.geoQueries.WithTx(tx)

	updated, err := qtx.UpdateCountry(ctx, iso2, newIso3, newSlug)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, errorEnvelope("geo.country_not_found", "country not found", r))
			return
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
			writeJSON(w, http.StatusConflict, errorEnvelope(
				"geo.country_slug_exists", "a country with that slug already exists", r,
			))
			return
		}
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"geo.update_country_failed", "failed to update country", r,
		))
		return
	}

	if err := geoUpsertI18nName(ctx, tx, "geo.countries", iso2, req.NameEn, req.NameRu); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"geo.upsert_name_failed", "failed to upsert localized name", r,
		))
		return
	}

	if err := tx.Commit(ctx); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"geo.commit_failed", "failed to commit transaction", r,
		))
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"country": countryResponse{
			ID:   updated.ID.String(),
			Iso2: updated.Iso2,
			Iso3: updated.Iso3,
			Slug: updated.Slug,
			Name: geoFirstNonEmpty(req.NameEn, updated.Iso2),
		},
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Admin: POST /v1/admin/geo/cities
// ─────────────────────────────────────────────────────────────────────────────

// createCityRequest is the request body for POST /v1/admin/geo/cities.
type createCityRequest struct {
	CountryID string `json:"country_id"`
	Slug      string `json:"slug"`
	NameEn    string `json:"name_en"`
	NameRu    string `json:"name_ru"`
}

// handleCreateCity serves POST /v1/admin/geo/cities.
func (s *Server) handleCreateCity(w http.ResponseWriter, r *http.Request) {
	if s.geoQueries == nil || s.pool == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	body, err := io.ReadAll(io.LimitReader(r.Body, 16*1024))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("geo.invalid_body", "cannot read request body: "+err.Error(), r))
		return
	}
	if len(body) == 0 {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("geo.empty_body", "request body is required", r))
		return
	}

	var req createCityRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("geo.invalid_json", "request body is not valid JSON", r))
		return
	}

	countryID, err := uuid.Parse(req.CountryID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"geo.invalid_country_id", "country_id must be a valid UUID", r,
			map[string]any{"field": "country_id"},
		))
		return
	}

	req.Slug = strings.TrimSpace(strings.ToLower(req.Slug))
	if req.Slug == "" {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"geo.invalid_slug", "slug is required", r,
			map[string]any{"field": "slug"},
		))
		return
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "failed to begin transaction", r,
		))
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	qtx := s.geoQueries.WithTx(tx)

	city, err := qtx.InsertCity(ctx, countryID, req.Slug)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			switch pgErr.Code {
			case pgUniqueViolation:
				writeJSON(w, http.StatusConflict, errorEnvelope(
					"geo.city_exists", "a city with that slug already exists", r,
				))
				return
			case "23503": // foreign key violation
				writeJSON(w, http.StatusBadRequest, errorEnvelope(
					"geo.country_not_found", "the specified country does not exist", r,
				))
				return
			}
		}
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"geo.insert_city_failed", "failed to insert city", r,
		))
		return
	}

	if err := geoUpsertI18nName(ctx, tx, "geo.cities", req.Slug, req.NameEn, req.NameRu); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"geo.upsert_name_failed", "failed to upsert localized name", r,
		))
		return
	}

	if err := tx.Commit(ctx); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"geo.commit_failed", "failed to commit transaction", r,
		))
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"city": cityResponse{
			ID:        city.ID.String(),
			CountryID: city.CountryID.String(),
			Slug:      city.Slug,
			Name:      geoFirstNonEmpty(req.NameEn, req.Slug),
		},
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Admin: PATCH /v1/admin/geo/cities/{id}
// ─────────────────────────────────────────────────────────────────────────────

// updateCityRequest is the request body for PATCH /v1/admin/geo/cities/{id}.
type updateCityRequest struct {
	Slug   string `json:"slug"`
	NameEn string `json:"name_en"`
	NameRu string `json:"name_ru"`
}

// handleUpdateCity serves PATCH /v1/admin/geo/cities/{id}.
func (s *Server) handleUpdateCity(w http.ResponseWriter, r *http.Request) {
	if s.geoQueries == nil || s.pool == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	cityID, ok := uuidPathParam(w, r, "id")
	if !ok {
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 16*1024))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("geo.invalid_body", "cannot read request body: "+err.Error(), r))
		return
	}
	if len(body) == 0 {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("geo.empty_body", "request body is required", r))
		return
	}

	// Load current city to enable partial update and get slug for i18n.
	current, err := s.geoQueries.GetCityByID(ctx, cityID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, errorEnvelope("geo.city_not_found", "city not found", r))
			return
		}
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"geo.get_city_failed", "failed to get city", r,
		))
		return
	}

	var req updateCityRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("geo.invalid_json", "request body is not valid JSON", r))
		return
	}

	newSlug := strings.TrimSpace(strings.ToLower(req.Slug))
	if newSlug == "" {
		newSlug = current.Slug
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "failed to begin transaction", r,
		))
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	qtx := s.geoQueries.WithTx(tx)

	updated, err := qtx.UpdateCity(ctx, cityID, newSlug)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, errorEnvelope("geo.city_not_found", "city not found", r))
			return
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
			writeJSON(w, http.StatusConflict, errorEnvelope(
				"geo.city_slug_exists", "a city with that slug already exists", r,
			))
			return
		}
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"geo.update_city_failed", "failed to update city", r,
		))
		return
	}

	if err := geoUpsertI18nName(ctx, tx, "geo.cities", newSlug, req.NameEn, req.NameRu); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"geo.upsert_name_failed", "failed to upsert localized name", r,
		))
		return
	}

	if err := tx.Commit(ctx); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"geo.commit_failed", "failed to commit transaction", r,
		))
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"city": cityResponse{
			ID:        updated.ID.String(),
			CountryID: updated.CountryID.String(),
			Slug:      updated.Slug,
			Name:      geoFirstNonEmpty(req.NameEn, updated.Slug),
		},
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

// geoUpsertI18nName inserts or updates i18n_text rows for the given namespace+key
// within the provided transaction. Only non-empty name values are written — if
// nameEn is empty the English row is not touched.
//
// SQL pattern:
//
//	INSERT INTO i18n_text (namespace, key, locale, value)
//	VALUES ($1, $2, $3, $4)
//	ON CONFLICT (namespace, key, locale)
//	DO UPDATE SET value = EXCLUDED.value, updated_at = now()
const geoUpsertI18nSQL = `
INSERT INTO i18n_text (namespace, key, locale, value)
VALUES ($1, $2, $3, $4)
ON CONFLICT (namespace, key, locale)
DO UPDATE SET value = EXCLUDED.value, updated_at = now()`

func geoUpsertI18nName(ctx context.Context, tx pgx.Tx, namespace, key, nameEn, nameRu string) error {
	if nameEn != "" {
		if _, err := tx.Exec(ctx, geoUpsertI18nSQL, namespace, key, "en", nameEn); err != nil {
			return err
		}
	}
	if nameRu != "" {
		if _, err := tx.Exec(ctx, geoUpsertI18nSQL, namespace, key, "ru", nameRu); err != nil {
			return err
		}
	}
	return nil
}

// geoFirstNonEmpty returns the first non-empty string from the provided values.
func geoFirstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
