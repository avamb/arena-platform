// venues.go implements the venue CRUD API endpoints (feature #124).
//
// V-1 extended fields (migration 0050) — address_line1/2, postal_code,
// country, geo_lat/geo_lng, timezone, contact_phone, contact_email,
// website_url, status — were added to the openapi CreateVenueRequest /
// UpdateVenueRequest / VenueItem schemas and to the venues table, but the
// handler and SQL layer never caught up: CREATE silently dropped every one
// of them and PATCH accepted them, answered 200, and left the DB untouched
// (bug B-2, found 2026-09-18). Every field the request types and the admin
// form (apps/admin-web/src/routes/venues.tsx) send is now persisted, with
// PATCH tri-state semantics (omitted key = unchanged, explicit JSON null =
// cleared where clearing is allowed, a value = set).
//
// Bug B-3: timezone is now REQUIRED on create (422 venue.timezone_required
// when absent/blank) and cannot be cleared on update (the same code, since a
// PATCH that blanks a previously-set timezone is exactly the situation the
// Bil24 gateway's GET_ALL_ACTIONS silently drops sessions for — see
// venueLocation in hbil24/cmd_catalog_events.go). The geo.cities registry
// (internal/adapters/postgres/gen/geo.sql.go CityRow) does not carry a
// timezone column, so there is no per-city default to fall back to; a
// legacy venue that predates this fix and still has no timezone keeps that
// state until an operator explicitly sets one — HandleUpdateVenue never
// forces a fill on an edit that doesn't touch the field.
package hcatalog

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

// ─────────────────────────────────────────────────────────────────────────────
// Response type
// ─────────────────────────────────────────────────────────────────────────────

type venueResponse struct {
	ID              string   `json:"id"`
	DisplayNumber   int64    `json:"display_number"`
	OrgID           string   `json:"org_id"`
	CityID          *string  `json:"city_id"`
	Name            string   `json:"name"`
	Address         *string  `json:"address"`
	AddressLine1    *string  `json:"address_line1"`
	AddressLine2    *string  `json:"address_line2"`
	PostalCode      *string  `json:"postal_code"`
	Country         *string  `json:"country"`
	GeoLat          *float64 `json:"geo_lat"`
	GeoLng          *float64 `json:"geo_lng"`
	Timezone        *string  `json:"timezone"`
	ContactPhone    *string  `json:"contact_phone"`
	ContactEmail    *string  `json:"contact_email"`
	WebsiteUrl      *string  `json:"website_url"`
	Status          string   `json:"status"`
	CapacityDefault *int32   `json:"capacity_default"`
	CreatedAt       string   `json:"created_at"`
	UpdatedAt       string   `json:"updated_at"`
}

// VenueResponse is the exported alias of venueResponse for use by the httpserver
// shim layer (venues_test.go in package httpserver references venueFromRow via
// catalog_shims.go and reads response fields directly).
type VenueResponse = venueResponse

// VenueFromRow is the exported alias of venueFromRow for use by the httpserver
// shim layer (venues_test.go calls venueFromRow via catalog_shims.go).
func VenueFromRow(v gen.VenueRow) VenueResponse { return venueFromRow(v) }

func venueFromRow(v gen.VenueRow) venueResponse {
	resp := venueResponse{
		ID:              v.ID.String(),
		DisplayNumber:   v.DisplayNumber,
		OrgID:           v.OrgID.String(),
		Name:            v.Name,
		Address:         v.Address,
		AddressLine1:    v.AddressLine1,
		AddressLine2:    v.AddressLine2,
		PostalCode:      v.PostalCode,
		Country:         v.Country,
		GeoLat:          v.GeoLat,
		GeoLng:          v.GeoLng,
		Timezone:        v.Timezone,
		ContactPhone:    v.ContactPhone,
		ContactEmail:    v.ContactEmail,
		WebsiteUrl:      v.WebsiteUrl,
		Status:          v.Status,
		CapacityDefault: v.CapacityDefault,
		CreatedAt:       v.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:       v.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if v.CityID != nil {
		s := v.CityID.String()
		resp.CityID = &s
	}
	return resp
}

// ─────────────────────────────────────────────────────────────────────────────
// Shared V-1 field validation (used by both create and update)
// ─────────────────────────────────────────────────────────────────────────────

const (
	venueStatusActive   = "active"
	venueStatusDraft    = "draft"
	venueStatusArchived = "archived"
)

func isValidVenueStatus(s string) bool {
	return s == venueStatusActive || s == venueStatusDraft || s == venueStatusArchived
}

var (
	venueCountryRe = regexp.MustCompile(`^[A-Z]{2}$`)
	// Mirrors the admin-web client-side check (venues.tsx validateVenueContactEmail):
	// a conservative shape check, not a full RFC 5322 validator.
	venueEmailRe = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)
	// Mirrors validateVenueContactPhone: digits, spaces, +, -, parentheses.
	venuePhoneRe = regexp.MustCompile(`^[+0-9 ()\-]+$`)
)

// venueFieldError builds the standard 422 ErrorEnvelope with a field detail,
// matching the shape the admin-web mapServerError switch already expects.
func venueFieldError(w http.ResponseWriter, r *http.Request, code, field, message string) {
	httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
		code, message, r, map[string]any{"field": field},
	))
}

// validateVenueAddressLine checks a trimmed, already-non-empty address line.
func validateVenueAddressLine(v, field string) (code, msg string, ok bool) {
	if len(v) > 200 {
		return "venue.invalid_address_line", field + " must be at most 200 characters", false
	}
	return "", "", true
}

func validateVenuePostalCode(v string) (code, msg string, ok bool) {
	if len(v) > 32 {
		return "venue.invalid_postal_code", "postal_code must be at most 32 characters", false
	}
	return "", "", true
}

// validateVenueCountry normalizes and validates a trimmed, non-empty country
// value. Returns the upper-cased ISO-3166-1 alpha-2 code.
func validateVenueCountry(v string) (normalized string, ok bool) {
	normalized = strings.ToUpper(v)
	return normalized, venueCountryRe.MatchString(normalized)
}

func validateVenueGeoLat(v float64) bool { return v >= -90 && v <= 90 }
func validateVenueGeoLng(v float64) bool { return v >= -180 && v <= 180 }

func validateVenueContactEmail(v string) bool {
	return len(v) <= 320 && venueEmailRe.MatchString(v)
}

func validateVenueContactPhone(v string) bool {
	return len(v) <= 40 && venuePhoneRe.MatchString(v)
}

func validateVenueWebsiteURL(v string) bool {
	u, err := url.Parse(v)
	if err != nil {
		return false
	}
	return (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// ─────────────────────────────────────────────────────────────────────────────
// Tri-state optional float64 (absent=keep, null=clear, number=set) — the
// float64 analogue of optionalString / optionalInt32 (events.go). geo_lat /
// geo_lng need this because a bare *float64 field cannot distinguish an
// omitted PATCH key from an explicit JSON null; both decode to nil.
// ─────────────────────────────────────────────────────────────────────────────

type optionalFloat64 struct {
	Present bool
	Value   *float64
}

func (v *optionalFloat64) UnmarshalJSON(data []byte) error {
	v.Present = true
	if string(data) == "null" {
		v.Value = nil
		return nil
	}
	var f float64
	if err := json.Unmarshal(data, &f); err != nil {
		return err
	}
	v.Value = &f
	return nil
}

// resolveFloat64 implements the tri-state merge: absent keeps existing,
// present+nil clears, present+non-nil sets. The float64 analogue of
// resolveStr / resolveInt32 (events.go).
func resolveFloat64(opt optionalFloat64, existing *float64) *float64 {
	if !opt.Present {
		return existing
	}
	return opt.Value
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/organizations/{org_id}/venues
// ─────────────────────────────────────────────────────────────────────────────

type createVenueRequest struct {
	Name            string   `json:"name"`
	CityID          string   `json:"city_id"`
	Address         string   `json:"address"`
	AddressLine1    string   `json:"address_line1"`
	AddressLine2    string   `json:"address_line2"`
	PostalCode      string   `json:"postal_code"`
	Country         string   `json:"country"`
	GeoLat          *float64 `json:"geo_lat"`
	GeoLng          *float64 `json:"geo_lng"`
	Timezone        string   `json:"timezone"`
	ContactPhone    string   `json:"contact_phone"`
	ContactEmail    string   `json:"contact_email"`
	WebsiteUrl      string   `json:"website_url"`
	Status          string   `json:"status"`
	CapacityDefault *int32   `json:"capacity_default"`
}

func (h *Handler) HandleCreateVenue(w http.ResponseWriter, r *http.Request) {
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

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("venue.invalid_body", "cannot read request body: "+err.Error(), r))
		return
	}
	if len(body) == 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("venue.empty_body", "request body is required", r))
		return
	}

	var req createVenueRequest
	if err := json.Unmarshal(body, &req); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("venue.invalid_json", "request body is not valid JSON", r))
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	req.CityID = strings.TrimSpace(req.CityID)
	req.Address = strings.TrimSpace(req.Address)
	req.AddressLine1 = strings.TrimSpace(req.AddressLine1)
	req.AddressLine2 = strings.TrimSpace(req.AddressLine2)
	req.PostalCode = strings.TrimSpace(req.PostalCode)
	req.Country = strings.TrimSpace(req.Country)
	req.Timezone = strings.TrimSpace(req.Timezone)
	req.ContactPhone = strings.TrimSpace(req.ContactPhone)
	req.ContactEmail = strings.TrimSpace(req.ContactEmail)
	req.WebsiteUrl = strings.TrimSpace(req.WebsiteUrl)
	req.Status = strings.TrimSpace(req.Status)

	if req.Name == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"venue.invalid_name", "name is required", r,
			map[string]any{"field": "name"},
		))
		return
	}

	var cityID *uuid.UUID
	if req.CityID != "" {
		parsed, parseErr := uuid.Parse(req.CityID)
		if parseErr != nil {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"venue.invalid_city_id", "city_id must be a valid UUID", r,
				map[string]any{"field": "city_id"},
			))
			return
		}
		cityID = &parsed
	}

	var address *string
	if req.Address != "" {
		a := req.Address
		address = &a
	}

	var addressLine1, addressLine2, postalCode, country *string
	if req.AddressLine1 != "" {
		if code, msg, ok := validateVenueAddressLine(req.AddressLine1, "address_line1"); !ok {
			venueFieldError(w, r, code, "address_line1", msg)
			return
		}
		v := req.AddressLine1
		addressLine1 = &v
	}
	if req.AddressLine2 != "" {
		if code, msg, ok := validateVenueAddressLine(req.AddressLine2, "address_line2"); !ok {
			venueFieldError(w, r, code, "address_line2", msg)
			return
		}
		v := req.AddressLine2
		addressLine2 = &v
	}
	if req.PostalCode != "" {
		if code, msg, ok := validateVenuePostalCode(req.PostalCode); !ok {
			venueFieldError(w, r, code, "postal_code", msg)
			return
		}
		v := req.PostalCode
		postalCode = &v
	}
	if req.Country != "" {
		normalized, ok := validateVenueCountry(req.Country)
		if !ok {
			venueFieldError(w, r, "venue.invalid_country", "country", "country must be a 2-letter ISO-3166-1 code")
			return
		}
		country = &normalized
	}

	if (req.GeoLat != nil) != (req.GeoLng != nil) {
		venueFieldError(w, r, "venue.invalid_geo", "geo_lat", "geo_lat and geo_lng must be provided together")
		return
	}
	if req.GeoLat != nil && !validateVenueGeoLat(*req.GeoLat) {
		venueFieldError(w, r, "venue.invalid_geo_lat", "geo_lat", "geo_lat must be between -90 and 90")
		return
	}
	if req.GeoLng != nil && !validateVenueGeoLng(*req.GeoLng) {
		venueFieldError(w, r, "venue.invalid_geo_lng", "geo_lng", "geo_lng must be between -180 and 180")
		return
	}

	// Bug B-3: timezone is required on create. A venue without one silently
	// drops every one of its sessions from the Bil24 gateway's
	// GET_ALL_ACTIONS catalog (hbil24.venueLocation) — the event shows on
	// the site with nothing to buy. The geo.cities registry does not carry
	// a timezone to default from (see package doc comment), so this is a
	// hard requirement rather than a city-derived default.
	if req.Timezone == "" {
		venueFieldError(w, r, "venue.timezone_required", "timezone", "timezone is required (IANA zone name, e.g. Europe/Berlin)")
		return
	}
	if _, err := time.LoadLocation(req.Timezone); err != nil {
		venueFieldError(w, r, "venue.invalid_timezone", "timezone", "timezone must be a known IANA zone name")
		return
	}
	timezone := &req.Timezone

	var contactPhone, contactEmail, websiteURL *string
	if req.ContactPhone != "" {
		if !validateVenueContactPhone(req.ContactPhone) {
			venueFieldError(w, r, "venue.invalid_contact_phone", "contact_phone", "contact_phone may contain digits, spaces, +, -, and parentheses only, up to 40 characters")
			return
		}
		v := req.ContactPhone
		contactPhone = &v
	}
	if req.ContactEmail != "" {
		if !validateVenueContactEmail(req.ContactEmail) {
			venueFieldError(w, r, "venue.invalid_contact_email", "contact_email", "contact_email must be a valid email address")
			return
		}
		v := req.ContactEmail
		contactEmail = &v
	}
	if req.WebsiteUrl != "" {
		if !validateVenueWebsiteURL(req.WebsiteUrl) {
			venueFieldError(w, r, "venue.invalid_website_url", "website_url", "website_url must be a valid http(s) URL")
			return
		}
		v := req.WebsiteUrl
		websiteURL = &v
	}

	status := venueStatusActive
	if req.Status != "" {
		if !isValidVenueStatus(req.Status) {
			venueFieldError(w, r, "venue.invalid_status", "status", "status must be one of active, draft, archived")
			return
		}
		status = req.Status
	}

	v, err := h.venueQueries.InsertVenue(
		ctx, orgID, cityID, req.Name, address, req.CapacityDefault,
		addressLine1, addressLine2, postalCode, country,
		req.GeoLat, req.GeoLng, timezone,
		contactPhone, contactEmail, websiteURL, status,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
			httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
				"venue.duplicate",
				"a venue with that name already exists in this organization",
				r,
			))
			return
		}
		h.logger.Error("venue: insert failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"venue.insert_failed", "failed to create venue", r,
		))
		return
	}

	httputil.WriteJSON(w, http.StatusCreated, map[string]any{
		"venue": venueFromRow(v),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/venues
// ─────────────────────────────────────────────────────────────────────────────

func (h *Handler) HandleListVenues(w http.ResponseWriter, r *http.Request) {
	if h.venueQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	rows, err := h.venueQueries.ListVenues(ctx)
	if err != nil {
		h.logger.Error("venue: list all failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"venue.list_failed", "failed to list venues", r,
		))
		return
	}

	result := make([]venueResponse, 0, len(rows))
	for _, v := range rows {
		result = append(result, venueFromRow(v))
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"venues": result})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/venues/{id}
// ─────────────────────────────────────────────────────────────────────────────

func (h *Handler) HandleGetVenue(w http.ResponseWriter, r *http.Request) {
	if h.venueQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	venueID, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}

	v, err := h.venueQueries.GetVenueByID(ctx, venueID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("venue.not_found", "venue not found", r))
			return
		}
		h.logger.Error("venue: get failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"venue.get_failed", "failed to get venue", r,
		))
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"venue": venueFromRow(v),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/organizations/{org_id}/venues
// ─────────────────────────────────────────────────────────────────────────────

func (h *Handler) HandleListVenuesByOrg(w http.ResponseWriter, r *http.Request) {
	if h.venueQueries == nil {
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

	rows, err := h.venueQueries.ListVenuesByOrg(ctx, orgID)
	if err != nil {
		h.logger.Error("venue: list by org failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"venue.list_failed", "failed to list venues", r,
		))
		return
	}

	result := make([]venueResponse, 0, len(rows))
	for _, v := range rows {
		result = append(result, venueFromRow(v))
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"venues": result})
}

// ─────────────────────────────────────────────────────────────────────────────
// PATCH /v1/organizations/{org_id}/venues/{id}
// ─────────────────────────────────────────────────────────────────────────────

type updateVenueRequest struct {
	Name            string          `json:"name"`
	CityID          *string         `json:"city_id"`
	Address         *string         `json:"address"`
	AddressLine1    optionalString  `json:"address_line1"`
	AddressLine2    optionalString  `json:"address_line2"`
	PostalCode      optionalString  `json:"postal_code"`
	Country         optionalString  `json:"country"`
	GeoLat          optionalFloat64 `json:"geo_lat"`
	GeoLng          optionalFloat64 `json:"geo_lng"`
	Timezone        optionalString  `json:"timezone"`
	ContactPhone    optionalString  `json:"contact_phone"`
	ContactEmail    optionalString  `json:"contact_email"`
	WebsiteUrl      optionalString  `json:"website_url"`
	Status          *string         `json:"status,omitempty"`
	CapacityDefault optionalInt32   `json:"capacity_default"`
}

// trimOptionalString trims a present, non-nil optionalString's value in
// place so downstream merge/validation logic always sees a normalized
// string, mirroring the plain-field trimming create already does.
func trimOptionalString(opt optionalString) optionalString {
	if opt.Present && opt.Value != nil {
		trimmed := strings.TrimSpace(*opt.Value)
		opt.Value = &trimmed
	}
	return opt
}

func (h *Handler) HandleUpdateVenue(w http.ResponseWriter, r *http.Request) {
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
	venueID, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}

	if !h.requireOrgMembership(w, r, h.venueQueries, orgID) {
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("venue.invalid_body", "cannot read request body: "+err.Error(), r))
		return
	}
	if len(body) == 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("venue.empty_body", "request body is required", r))
		return
	}

	var req updateVenueRequest
	if err := json.Unmarshal(body, &req); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("venue.invalid_json", "request body is not valid JSON", r))
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	req.AddressLine1 = trimOptionalString(req.AddressLine1)
	req.AddressLine2 = trimOptionalString(req.AddressLine2)
	req.PostalCode = trimOptionalString(req.PostalCode)
	req.Country = trimOptionalString(req.Country)
	req.Timezone = trimOptionalString(req.Timezone)
	req.ContactPhone = trimOptionalString(req.ContactPhone)
	req.ContactEmail = trimOptionalString(req.ContactEmail)
	req.WebsiteUrl = trimOptionalString(req.WebsiteUrl)

	var cityID *uuid.UUID
	if req.CityID != nil {
		trimmed := strings.TrimSpace(*req.CityID)
		if trimmed != "" {
			parsed, parseErr := uuid.Parse(trimmed)
			if parseErr != nil {
				httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
					"venue.invalid_city_id", "city_id must be a valid UUID", r,
					map[string]any{"field": "city_id"},
				))
				return
			}
			cityID = &parsed
		}
	}

	var address *string
	if req.Address != nil {
		trimmed := strings.TrimSpace(*req.Address)
		address = &trimmed
	}

	// Validate every actively-SET value (Present && Value != nil). A clear
	// (Present && Value == nil) or an omission needs no shape validation.
	if req.AddressLine1.Present && req.AddressLine1.Value != nil && *req.AddressLine1.Value != "" {
		if code, msg, ok := validateVenueAddressLine(*req.AddressLine1.Value, "address_line1"); !ok {
			venueFieldError(w, r, code, "address_line1", msg)
			return
		}
	}
	if req.AddressLine2.Present && req.AddressLine2.Value != nil && *req.AddressLine2.Value != "" {
		if code, msg, ok := validateVenueAddressLine(*req.AddressLine2.Value, "address_line2"); !ok {
			venueFieldError(w, r, code, "address_line2", msg)
			return
		}
	}
	if req.PostalCode.Present && req.PostalCode.Value != nil && *req.PostalCode.Value != "" {
		if code, msg, ok := validateVenuePostalCode(*req.PostalCode.Value); !ok {
			venueFieldError(w, r, code, "postal_code", msg)
			return
		}
	}
	if req.Country.Present && req.Country.Value != nil && *req.Country.Value != "" {
		normalized, ok := validateVenueCountry(*req.Country.Value)
		if !ok {
			venueFieldError(w, r, "venue.invalid_country", "country", "country must be a 2-letter ISO-3166-1 code")
			return
		}
		req.Country.Value = &normalized
	}
	if req.GeoLat.Present && req.GeoLat.Value != nil && !validateVenueGeoLat(*req.GeoLat.Value) {
		venueFieldError(w, r, "venue.invalid_geo_lat", "geo_lat", "geo_lat must be between -90 and 90")
		return
	}
	if req.GeoLng.Present && req.GeoLng.Value != nil && !validateVenueGeoLng(*req.GeoLng.Value) {
		venueFieldError(w, r, "venue.invalid_geo_lng", "geo_lng", "geo_lng must be between -180 and 180")
		return
	}
	// Bug B-3: forbid clearing timezone (explicit null, or an explicit empty
	// string, both read as "remove the timezone this venue already has").
	// Setting a NEW value is validated below like create.
	if req.Timezone.Present {
		if req.Timezone.Value == nil || *req.Timezone.Value == "" {
			venueFieldError(w, r, "venue.timezone_required", "timezone", "timezone cannot be cleared once set; provide a replacement IANA zone name instead")
			return
		}
		if _, err := time.LoadLocation(*req.Timezone.Value); err != nil {
			venueFieldError(w, r, "venue.invalid_timezone", "timezone", "timezone must be a known IANA zone name")
			return
		}
	}
	if req.ContactPhone.Present && req.ContactPhone.Value != nil && *req.ContactPhone.Value != "" {
		if !validateVenueContactPhone(*req.ContactPhone.Value) {
			venueFieldError(w, r, "venue.invalid_contact_phone", "contact_phone", "contact_phone may contain digits, spaces, +, -, and parentheses only, up to 40 characters")
			return
		}
	}
	if req.ContactEmail.Present && req.ContactEmail.Value != nil && *req.ContactEmail.Value != "" {
		if !validateVenueContactEmail(*req.ContactEmail.Value) {
			venueFieldError(w, r, "venue.invalid_contact_email", "contact_email", "contact_email must be a valid email address")
			return
		}
	}
	if req.WebsiteUrl.Present && req.WebsiteUrl.Value != nil && *req.WebsiteUrl.Value != "" {
		if !validateVenueWebsiteURL(*req.WebsiteUrl.Value) {
			venueFieldError(w, r, "venue.invalid_website_url", "website_url", "website_url must be a valid http(s) URL")
			return
		}
	}
	if req.Status != nil {
		trimmed := strings.TrimSpace(*req.Status)
		if trimmed != "" && !isValidVenueStatus(trimmed) {
			venueFieldError(w, r, "venue.invalid_status", "status", "status must be one of active, draft, archived")
			return
		}
		req.Status = &trimmed
	}

	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "failed to begin transaction", r,
		))
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := h.venueQueries.WithTx(tx)

	existing, err := qtx.GetVenueForUpdate(ctx, venueID, orgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("venue.not_found", "venue not found", r))
			return
		}
		h.logger.Error("venue: get-for-update failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"venue.update_failed", "failed to update venue", r,
		))
		return
	}

	// Merge: absent keeps the existing value, an allowed clear (null) drops
	// it to NULL, a supplied value replaces it. city_id / address / status
	// are not tri-state (matches the openapi UpdateVenueRequest contract):
	// omitted or empty leaves the existing value, there is no way to clear
	// them via PATCH.
	finalName := existing.Name
	if req.Name != "" {
		finalName = req.Name
	}
	finalCityID := existing.CityID
	if cityID != nil {
		finalCityID = cityID
	}
	finalAddress := existing.Address
	if address != nil {
		finalAddress = address
	}
	finalCapacity := resolveInt32(req.CapacityDefault, existing.CapacityDefault)
	finalAddressLine1 := resolveStr(req.AddressLine1, existing.AddressLine1)
	finalAddressLine2 := resolveStr(req.AddressLine2, existing.AddressLine2)
	finalPostalCode := resolveStr(req.PostalCode, existing.PostalCode)
	finalCountry := resolveStr(req.Country, existing.Country)
	finalGeoLat := resolveFloat64(req.GeoLat, existing.GeoLat)
	finalGeoLng := resolveFloat64(req.GeoLng, existing.GeoLng)
	finalTimezone := resolveStr(req.Timezone, existing.Timezone)
	finalContactPhone := resolveStr(req.ContactPhone, existing.ContactPhone)
	finalContactEmail := resolveStr(req.ContactEmail, existing.ContactEmail)
	finalWebsiteURL := resolveStr(req.WebsiteUrl, existing.WebsiteUrl)
	finalStatus := existing.Status
	if req.Status != nil && *req.Status != "" {
		finalStatus = *req.Status
	}

	// Post-merge invariant: geo_lat/geo_lng must both be set or both be nil,
	// even when a single PATCH only touches one of the two against an
	// existing half-set pair (defensive; the columns are already
	// range-checked individually above and by the venues_geo_*_range_check
	// constraints).
	if (finalGeoLat == nil) != (finalGeoLng == nil) {
		venueFieldError(w, r, "venue.invalid_geo", "geo_lat", "geo_lat and geo_lng must be provided together")
		return
	}

	updated, err := qtx.UpdateVenue(
		ctx, venueID, orgID, finalCityID, finalName, finalAddress, finalCapacity,
		finalAddressLine1, finalAddressLine2, finalPostalCode, finalCountry,
		finalGeoLat, finalGeoLng, finalTimezone,
		finalContactPhone, finalContactEmail, finalWebsiteURL, finalStatus,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("venue.not_found", "venue not found", r))
			return
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
			httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
				"venue.duplicate",
				"a venue with that name already exists in this organization",
				r,
			))
			return
		}
		h.logger.Error("venue: update failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"venue.update_failed", "failed to update venue", r,
		))
		return
	}

	if err := tx.Commit(ctx); err != nil {
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"venue.commit_failed", "failed to commit transaction", r,
		))
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"venue": venueFromRow(updated),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// DELETE /v1/organizations/{org_id}/venues/{id}
// ─────────────────────────────────────────────────────────────────────────────

func (h *Handler) HandleDeleteVenue(w http.ResponseWriter, r *http.Request) {
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
	venueID, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}

	if !h.requireOrgMembership(w, r, h.venueQueries, orgID) {
		return
	}

	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "failed to begin transaction", r,
		))
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	qtx := h.venueQueries.WithTx(tx)

	deleted, err := qtx.SoftDeleteVenue(ctx, venueID, orgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("venue.not_found", "venue not found", r))
			return
		}
		h.logger.Error("venue: soft-delete failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"venue.delete_failed", "failed to delete venue", r,
		))
		return
	}

	if h.audit != nil {
		actor, _ := auth.ActorFromContext(ctx)
		auditEv := audit.Event{
			OccurredAt:   time.Now().UTC(),
			ActorType:    "user",
			ActorID:      actor.ID,
			Action:       "v1.venue.delete",
			ResourceType: "venue",
			ResourceID:   venueID.String(),
			RequestID:    logging.RequestID(ctx),
			TraceID:      logging.TraceID(ctx),
			IP:           httputil.ExtractClientIP(r),
			Metadata: map[string]any{
				"venue_name": deleted.Name,
				"org_id":     orgID.String(),
			},
		}
		if err := h.audit.WriteTx(ctx, tx, auditEv); err != nil {
			h.logger.Error("venue: audit write failed", slog.String("error", err.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"venue.audit_failed", "failed to write audit event", r,
			))
			return
		}
	}

	if err := tx.Commit(ctx); err != nil {
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"venue.commit_failed", "failed to commit transaction", r,
		))
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"venue":   venueFromRow(deleted),
		"deleted": true,
	})
}
