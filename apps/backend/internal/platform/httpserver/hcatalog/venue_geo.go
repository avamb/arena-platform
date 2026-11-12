// venue_geo.go: a venue that names its city by country and name (spec 64
// section 2.1.1, contract "A new country comes with a venue").
//
// POST /v1/organizations/{org_id}/venues takes country_iso2 + city_name
// instead of city_id. In ONE transaction the handler finds or creates the
// city (the rules of POST .../cities), creates the country from the embedded
// world table when the reference does not have it, inserts the venue, and
// queues the venue_created notification plus, for a created country, the
// country_created one. An organization API key never creates a country. Any
// failure (a duplicate venue name, say) rolls everything back, so no country
// or city is left without its venue.
package hcatalog

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/i18n"
	venueplannotify "github.com/abhteam/arena_new/apps/backend/internal/platform/venueplan/notify"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/worldgeo"
)

var iso2Re = regexp.MustCompile(`^[A-Z]{2}$`)

// venueInsert carries the validated optional columns of a new venue.
type venueInsert struct {
	address, addressLine1, addressLine2, postalCode, country, timezone *string
	contactPhone, contactEmail, websiteURL                             *string
	status                                                             string
}

// venueCityResponse is the "city" of the create answer when the city came by name.
type venueCityResponse struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	CountryIso2 string `json:"country_iso2"`
}

// createdCountryResponse is the "created_country" of the create answer.
type createdCountryResponse struct {
	ID       string `json:"id"`
	Iso2     string `json:"iso2"`
	Name     string `json:"name"`
	Currency string `json:"currency"`
	Region   string `json:"region"`
}

// validateCityByName checks the country_iso2 + city_name group (already
// trimmed, iso2 upper-cased). It writes the 400 and returns false on a
// refusal.
func validateCityByName(w http.ResponseWriter, r *http.Request, cityID, iso2, name string) bool {
	switch {
	case cityID != "":
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"venue.city_conflict", "Send city_id or country_iso2 with city_name, not both.", r,
			map[string]any{"field": "city_id"}))
		return false
	case iso2 == "":
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"venue.country_required", "Send country_iso2 with city_name.", r,
			map[string]any{"field": "country_iso2"}))
		return false
	case name == "":
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"venue.city_name_required", "Send city_name with country_iso2.", r,
			map[string]any{"field": "city_name"}))
		return false
	case !iso2Re.MatchString(iso2):
		writeUnknownCountry(w, r, iso2)
		return false
	}
	return true
}

func writeUnknownCountry(w http.ResponseWriter, r *http.Request, iso2 string) {
	httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
		"venue.unknown_country", "We do not know this country code. Send a 2-letter ISO 3166-1 code, for example ES.", r,
		map[string]any{"field": "country_iso2", "country_iso2": iso2}))
}

// requestLocale is the language of the request: ?lang=, else ?locale=, else
// Accept-Language, else English.
func requestLocale(r *http.Request) string {
	lang := strings.TrimSpace(r.URL.Query().Get("lang"))
	if lang == "" {
		lang = strings.TrimSpace(r.URL.Query().Get("locale"))
	}
	return i18n.NegotiateLocale(r.Header.Get("Accept-Language"), lang, "", "en", nil)
}

// createVenueWithCityByName is the country_iso2 + city_name branch of
// HandleCreateVenue. Every write shares one transaction.
func (h *Handler) createVenueWithCityByName(w http.ResponseWriter, r *http.Request, orgID uuid.UUID, req createVenueRequest, in venueInsert) {
	ctx := r.Context()
	locale := requestLocale(r)

	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "failed to begin transaction", r))
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := h.venueQueries.WithTx(tx)

	// An API key may use a country the reference has, never create one.
	if _, err := qtx.GetCountryByISO2(ctx, req.CountryIso2); errors.Is(err, pgx.ErrNoRows) {
		if _, inWorld := worldgeo.Lookup(req.CountryIso2); !inWorld {
			writeUnknownCountry(w, r, req.CountryIso2)
			return
		}
		if actor, ok := auth.ActorFromContext(ctx); ok && actor.IsService() {
			httputil.WriteJSON(w, http.StatusForbidden, httputil.ErrorEnvelopeWithDetails(
				"venue.country_create_forbidden", "An organization API key cannot add a new country. Sign in as a person to add the venue.", r,
				map[string]any{"field": "country_iso2", "country_iso2": req.CountryIso2}))
			return
		}
	} else if err != nil {
		h.venueGeoFailed(w, r, "country lookup", err)
		return
	}

	ref, countryCreated, err := worldgeo.Ensure(ctx, tx, req.CountryIso2)
	if errors.Is(err, worldgeo.ErrUnknownCountry) {
		writeUnknownCountry(w, r, req.CountryIso2)
		return
	}
	if err != nil {
		h.venueGeoFailed(w, r, "country ensure", err)
		return
	}

	cityID, cityCreated, err := createOrFindOrgCity(ctx, qtx, gen.CountryRow{ID: ref.ID, Iso2: ref.Iso2, Iso3: ref.Iso3, Slug: ref.Slug, Currency: ref.Currency},
		req.CityName, normalizeCityLocale(locale))
	if err != nil {
		h.venueGeoFailed(w, r, "city", err)
		return
	}
	city, err := qtx.GetCityWithName(ctx, cityID, locale)
	if err != nil {
		h.venueGeoFailed(w, r, "city read back", err)
		return
	}

	v, err := qtx.InsertVenue(
		ctx, orgID, &cityID, req.Name, in.address, req.CapacityDefault,
		in.addressLine1, in.addressLine2, in.postalCode, in.country,
		req.GeoLat, req.GeoLng, in.timezone,
		in.contactPhone, in.contactEmail, in.websiteURL, in.status,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
			httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
				"venue.duplicate", "a venue with that name already exists in this organization", r))
			return
		}
		h.venueGeoFailed(w, r, "insert venue", err)
		return
	}

	if countryCreated {
		if err := h.promoterAudit(ctx, tx, r, "v1.country.create", "country", ref.ID.String(), map[string]any{
			"org_id": orgID.String(), "iso2": ref.Iso2, "venue_id": v.ID.String(), "source": "venue",
		}); err != nil {
			h.venueGeoFailed(w, r, "country audit", err)
			return
		}
	}
	if cityCreated {
		if err := h.promoterAudit(ctx, tx, r, "v1.city.create", "city", city.ID.String(), map[string]any{
			"org_id": orgID.String(), "country_id": ref.ID.String(), "slug": city.Slug, "city_name": req.CityName,
		}); err != nil {
			h.venueGeoFailed(w, r, "city audit", err)
			return
		}
	}
	if err := venueplannotify.EnqueueVenueCreated(ctx, tx, v.ID); err != nil {
		h.venueGeoFailed(w, r, "venue notification", err)
		return
	}
	if countryCreated {
		if err := venueplannotify.EnqueueCountryCreatedByVenue(ctx, tx, ref.ID, ref.Iso2, v.ID); err != nil {
			h.venueGeoFailed(w, r, "country notification", err)
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"venue.commit_failed", "failed to commit transaction", r))
		return
	}

	body := map[string]any{
		"venue": venueFromRow(v),
		"city":  venueCityResponse{ID: city.ID.String(), Name: city.Name, CountryIso2: city.CountryIso2},
	}
	if countryCreated {
		body["created_country"] = createdCountryResponse{
			ID: ref.ID.String(), Iso2: ref.Iso2, Name: ref.Name(locale), Currency: ref.Currency, Region: ref.Region,
		}
	}
	httputil.WriteJSON(w, http.StatusCreated, body)
}

func (h *Handler) venueGeoFailed(w http.ResponseWriter, r *http.Request, step string, err error) {
	h.logger.Error("venue: create with city by name failed", slog.String("step", step), slog.String("error", err.Error()))
	httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
		"venue.insert_failed", "failed to create venue", r))
}

// countryForCity resolves the country of POST .../cities: country_id when
// sent, else country_iso2, which must be in the arena reference (a country is
// created only together with a venue). It writes the refusal and returns
// false.
func (h *Handler) countryForCity(ctx context.Context, w http.ResponseWriter, r *http.Request, q *gen.Queries, rawID, rawIso2 string) (gen.CountryRow, bool) {
	if strings.TrimSpace(rawID) != "" {
		countryID, err := uuid.Parse(strings.TrimSpace(rawID))
		if err != nil {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"city.invalid_country_id", "country_id must be a valid UUID", r,
				map[string]any{"field": "country_id"}))
			return gen.CountryRow{}, false
		}
		country, err := q.GetCountryByID(ctx, countryID)
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
				"city.country_not_found", "the specified country does not exist", r,
				map[string]any{"field": "country_id"}))
			return gen.CountryRow{}, false
		}
		if err != nil {
			h.logger.Error("city: country lookup failed", slog.String("error", err.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("city.create_failed", "failed to create city", r))
			return gen.CountryRow{}, false
		}
		return country, true
	}
	iso2 := strings.ToUpper(strings.TrimSpace(rawIso2))
	if !iso2Re.MatchString(iso2) {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"city.invalid_country_id", "Send country_id as a UUID or country_iso2 as a 2-letter code.", r,
			map[string]any{"field": "country_id"}))
		return gen.CountryRow{}, false
	}
	country, err := q.GetCountryByISO2(ctx, iso2)
	if errors.Is(err, pgx.ErrNoRows) {
		if _, inWorld := worldgeo.Lookup(iso2); inWorld {
			httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelopeWithDetails(
				"city.country_not_in_reference", "This country is not on the platform yet. Add the venue with country_iso2 and city_name, and the country comes with it.", r,
				map[string]any{"field": "country_iso2", "country_iso2": iso2}))
			return gen.CountryRow{}, false
		}
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
			"city.country_not_found", "We do not know this country code.", r,
			map[string]any{"field": "country_iso2"}))
		return gen.CountryRow{}, false
	}
	if err != nil {
		h.logger.Error("city: country lookup failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("city.create_failed", "failed to create city", r))
		return gen.CountryRow{}, false
	}
	return country, true
}
