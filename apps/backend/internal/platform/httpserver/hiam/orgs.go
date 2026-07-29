// orgs.go implements the organization CRUD API endpoints (feature #119).
//
// Organizations are the primary multi-tenant boundary (ADR-016). Every
// ticketing resource (events, catalog, orders, tickets) is scoped under
// an organization.
//
// Endpoints:
//
//	POST   /v1/organizations            — create a new organization (org.create)
//	GET    /v1/organizations            — list all active organizations (org.read)
//	GET    /v1/organizations/{id}       — get a single organization (org.read)
//	PATCH  /v1/organizations/{id}       — update an organization (org.update)
//	DELETE /v1/organizations/{id}       — soft-delete an organization (org.delete)
//
// All write endpoints are gated by JWT auth + a named permission.
// The list / get endpoints are also gated by org.read so the org registry
// is not publicly enumerable.
//
// Soft-delete policy:
//
//	DELETE does not remove the row; it sets deleted_at = now(). All
//	subsequent reads filter WHERE deleted_at IS NULL. An audit event is
//	written inside the same transaction as the soft-delete UPDATE.
package hiam

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/mail"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

// OrgResponse is the exported JSON representation of a single organization,
// for use by the httpserver shim layer (orgs_test.go references orgResponse
// from package httpserver via iam_shims.go).
type OrgResponse struct {
	ID                     string  `json:"id"`
	DisplayNumber          int64   `json:"display_number"`
	Name                   string  `json:"name"`
	Slug                   string  `json:"slug"`
	Country                string  `json:"country"`
	DefaultLocale          string  `json:"default_locale"`
	ReservationTTLSeconds  int32   `json:"reservation_ttl_seconds"`
	LegalName              *string `json:"legal_name"`
	TaxID                  *string `json:"tax_id"`
	TaxIDScheme            *string `json:"tax_id_scheme"`
	RegistrationNumber     *string `json:"registration_number"`
	LegalAddressLine1      *string `json:"legal_address_line1"`
	LegalAddressLine2      *string `json:"legal_address_line2"`
	LegalAddressPostalCode *string `json:"legal_address_postal_code"`
	LegalAddressCity       *string `json:"legal_address_city"`
	LegalAddressCountry    *string `json:"legal_address_country"`
	ContactEmail           *string `json:"contact_email"`
	ContactPhone           *string `json:"contact_phone"`
	WebsiteURL             *string `json:"website_url"`
	KybStatus              string  `json:"kyb_status"`
	KybVerifiedAt          *string `json:"kyb_verified_at"`
	CreatedAt              string  `json:"created_at"`
	UpdatedAt              string  `json:"updated_at"`
}

// orgResponse is a package-level alias for OrgResponse so existing handler code
// continues to compile unchanged.
type orgResponse = OrgResponse

// createOrgRequest is the request body for POST /v1/organizations.
type createOrgRequest struct {
	Name                  string `json:"name"`
	Slug                  string `json:"slug"`
	Country               string `json:"country"`
	DefaultLocale         string `json:"default_locale"`
	ReservationTTLSeconds int32  `json:"reservation_ttl_seconds"`
}

// updateOrgRequest is the request body for PATCH /v1/organizations/{id}.
// All fields are optional; empty/zero values leave the existing value unchanged.
type updateOrgRequest struct {
	Name                   string         `json:"name"`
	Slug                   string         `json:"slug"`
	Country                string         `json:"country"`
	DefaultLocale          string         `json:"default_locale"`
	ReservationTTLSeconds  int32          `json:"reservation_ttl_seconds"`
	LegalName              optionalString `json:"legal_name"`
	TaxID                  optionalString `json:"tax_id"`
	TaxIDScheme            optionalString `json:"tax_id_scheme"`
	RegistrationNumber     optionalString `json:"registration_number"`
	LegalAddressLine1      optionalString `json:"legal_address_line1"`
	LegalAddressLine2      optionalString `json:"legal_address_line2"`
	LegalAddressPostalCode optionalString `json:"legal_address_postal_code"`
	LegalAddressCity       optionalString `json:"legal_address_city"`
	LegalAddressCountry    optionalString `json:"legal_address_country"`
	ContactEmail           optionalString `json:"contact_email"`
	ContactPhone           optionalString `json:"contact_phone"`
	WebsiteURL             optionalString `json:"website_url"`
	KybStatus              optionalString `json:"kyb_status"`
	SenderEmail            optionalString `json:"sender_email"`
}

// optionalString preserves the difference between an omitted PATCH member and
// an explicit JSON null. That distinction prevents a basic org PATCH from
// clearing legal data submitted by a previous save.
type optionalString struct {
	Present bool
	Value   *string
}

func (v *optionalString) UnmarshalJSON(data []byte) error {
	v.Present = true
	if string(data) == "null" {
		v.Value = nil
		return nil
	}
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	v.Value = &value
	return nil
}

func decodeUpdateOrgRequest(body []byte, req *updateOrgRequest) error {
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(req); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("request body contains multiple JSON values")
		}
		return err
	}
	return nil
}

func optionalValue(value optionalString, existing *string) *string {
	if !value.Present {
		return existing
	}
	if value.Value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value.Value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

var (
	alpha2Country = regexp.MustCompile(`^[A-Z]{2}$`)
	euVAT         = regexp.MustCompile(`^[A-Z]{2}[A-Z0-9]{2,12}$`)
	gbVAT         = regexp.MustCompile(`^GB(?:[0-9]{9}|[0-9]{12}|GD[0-9]{3}|HA[0-9]{3})$`)
	ilVAT         = regexp.MustCompile(`^[0-9]{9}$`)
	usEIN         = regexp.MustCompile(`^[0-9]{2}-?[0-9]{7}$`)
)

func validateLegalUpdate(req updateOrgRequest, current gen.OrganizationRow) (legalName, taxID, taxScheme, registrationNumber, line1, line2, postalCode, city, addressCountry, email, phone, website *string, kybStatus string, code string) {
	legalName = optionalValue(req.LegalName, current.LegalName)
	taxID = optionalValue(req.TaxID, current.TaxID)
	taxScheme = optionalValue(req.TaxIDScheme, current.TaxIDScheme)
	registrationNumber = optionalValue(req.RegistrationNumber, current.RegistrationNumber)
	line1 = optionalValue(req.LegalAddressLine1, current.LegalAddressLine1)
	line2 = optionalValue(req.LegalAddressLine2, current.LegalAddressLine2)
	postalCode = optionalValue(req.LegalAddressPostalCode, current.LegalAddressPostalCode)
	city = optionalValue(req.LegalAddressCity, current.LegalAddressCity)
	addressCountry = optionalValue(req.LegalAddressCountry, current.LegalAddressCountry)
	email = optionalValue(req.ContactEmail, current.ContactEmail)
	phone = optionalValue(req.ContactPhone, current.ContactPhone)
	website = optionalValue(req.WebsiteURL, current.WebsiteURL)
	kybStatus = current.KybStatus
	if req.KybStatus.Present {
		if req.KybStatus.Value == nil {
			return nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "", "invalid_kyb_status"
		}
		kybStatus = strings.TrimSpace(*req.KybStatus.Value)
	}
	if kybStatus != "unverified" && kybStatus != "pending" && kybStatus != "verified" && kybStatus != "rejected" {
		return nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "", "invalid_kyb_status"
	}
	if (kybStatus == "pending" || kybStatus == "verified") && legalName == nil {
		return nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "", "legal_name_required"
	}
	if addressCountry != nil {
		country := strings.ToUpper(*addressCountry)
		if !alpha2Country.MatchString(country) {
			return nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "", "invalid_legal_address_country"
		}
		addressCountry = &country
	}
	if (taxID == nil) != (taxScheme == nil) {
		return nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "", "invalid_tax_id_scheme"
	}
	if taxID != nil {
		valid := false
		switch *taxScheme {
		case "eu_vat":
			valid = euVAT.MatchString(strings.ToUpper(*taxID))
		case "gb_vat":
			valid = gbVAT.MatchString(strings.ToUpper(*taxID))
		case "il_vat":
			valid = ilVAT.MatchString(*taxID)
		case "us_ein":
			valid = usEIN.MatchString(*taxID)
		case "other":
			valid = len(*taxID) >= 2 && len(*taxID) <= 64
		default:
			return nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "", "invalid_tax_id_scheme"
		}
		if !valid {
			return nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "", "invalid_tax_id"
		}
	}
	if email != nil {
		parsed, err := mail.ParseAddress(*email)
		if err != nil || parsed.Address != *email {
			return nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "", "invalid_contact_email"
		}
	}
	if website != nil {
		parsed, err := url.ParseRequestURI(*website)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "", "invalid_website_url"
		}
	}
	return legalName, taxID, taxScheme, registrationNumber, line1, line2, postalCode, city, addressCountry, email, phone, website, kybStatus, ""
}

func responseFromOrganization(o gen.OrganizationRow) orgResponse {
	var verifiedAt *string
	if o.KybVerifiedAt != nil {
		value := o.KybVerifiedAt.UTC().Format(time.RFC3339)
		verifiedAt = &value
	}
	return orgResponse{ID: o.ID.String(), DisplayNumber: o.DisplayNumber, Name: o.Name, Slug: o.Slug, Country: o.Country, DefaultLocale: o.DefaultLocale, ReservationTTLSeconds: o.ReservationTTLSeconds,
		LegalName: o.LegalName, TaxID: o.TaxID, TaxIDScheme: o.TaxIDScheme, RegistrationNumber: o.RegistrationNumber, LegalAddressLine1: o.LegalAddressLine1, LegalAddressLine2: o.LegalAddressLine2, LegalAddressPostalCode: o.LegalAddressPostalCode, LegalAddressCity: o.LegalAddressCity, LegalAddressCountry: o.LegalAddressCountry, ContactEmail: o.ContactEmail, ContactPhone: o.ContactPhone, WebsiteURL: o.WebsiteURL, KybStatus: o.KybStatus, KybVerifiedAt: verifiedAt,
		CreatedAt: o.CreatedAt.UTC().Format(time.RFC3339), UpdatedAt: o.UpdatedAt.UTC().Format(time.RFC3339)}
}

// HandleCreateOrg serves POST /v1/organizations.
// Requires JWT + "org.create" permission (enforced by middleware in mountV1Routes).
func (h *Handler) HandleCreateOrg(w http.ResponseWriter, r *http.Request) {
	if h.orgQueries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("org.invalid_body", "cannot read request body: "+err.Error(), r))
		return
	}
	if len(body) == 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("org.empty_body", "request body is required", r))
		return
	}

	var req createOrgRequest
	if err := json.Unmarshal(body, &req); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("org.invalid_json", "request body is not valid JSON", r))
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	req.Slug = strings.TrimSpace(strings.ToLower(req.Slug))

	if req.Name == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"org.invalid_name", "name is required", r,
			map[string]any{"field": "name"},
		))
		return
	}
	if req.Slug == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"org.invalid_slug", "slug is required", r,
			map[string]any{"field": "slug"},
		))
		return
	}

	if req.DefaultLocale == "" {
		req.DefaultLocale = "en"
	}
	if req.ReservationTTLSeconds <= 0 {
		req.ReservationTTLSeconds = 1200
	}

	org, err := h.orgQueries.InsertOrganization(ctx,
		req.Name, req.Slug, req.Country, req.DefaultLocale, req.ReservationTTLSeconds,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
			httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
				"org.duplicate",
				"an organization with that name or slug already exists",
				r,
			))
			return
		}
		h.logger.Error("org: insert failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"org.insert_failed", "failed to create organization", r,
		))
		return
	}

	httputil.WriteJSON(w, http.StatusCreated, map[string]any{
		"organization": responseFromOrganization(org),
	})
}

// HandleListOrgs serves GET /v1/organizations.
// Requires JWT + "org.read" permission (enforced by middleware in mountV1Routes).
func (h *Handler) HandleListOrgs(w http.ResponseWriter, r *http.Request) {
	if h.orgQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	rows, err := h.orgQueries.ListOrganizations(ctx)
	if err != nil {
		h.logger.Error("org: list failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"org.list_failed", "failed to list organizations", r,
		))
		return
	}

	result := make([]orgResponse, 0, len(rows))
	for _, o := range rows {
		result = append(result, responseFromOrganization(o))
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"organizations": result})
}

// HandleGetOrg serves GET /v1/organizations/{id}.
// Requires JWT + "org.read" permission.
func (h *Handler) HandleGetOrg(w http.ResponseWriter, r *http.Request) {
	if h.orgQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	orgID, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}

	if !h.requireOrgMembership(w, r, orgID) {
		return
	}

	o, err := h.orgQueries.GetOrganizationByID(ctx, orgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("org.not_found", "organization not found", r))
			return
		}
		h.logger.Error("org: get failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"org.get_failed", "failed to get organization", r,
		))
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"organization": responseFromOrganization(o),
	})
}

// HandleUpdateOrg serves PATCH /v1/organizations/{id}.
// Requires JWT + "org.update" permission.
func (h *Handler) HandleUpdateOrg(w http.ResponseWriter, r *http.Request) {
	if h.orgQueries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	orgID, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}

	if !h.requireOrgMembership(w, r, orgID) {
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("org.invalid_body", "cannot read request body: "+err.Error(), r))
		return
	}
	if len(body) == 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("org.empty_body", "request body is required", r))
		return
	}

	var req updateOrgRequest
	if err := decodeUpdateOrgRequest(body, &req); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("org.invalid_json", "request body is not valid JSON", r))
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	req.Slug = strings.TrimSpace(strings.ToLower(req.Slug))
	req.Country = strings.TrimSpace(req.Country)
	req.DefaultLocale = strings.TrimSpace(req.DefaultLocale)
	current, err := h.orgQueries.GetOrganizationByID(ctx, orgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("org.not_found", "organization not found", r))
			return
		}
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("org.get_failed", "failed to get organization", r))
		return
	}
	legalName, taxID, taxScheme, registrationNumber, line1, line2, postalCode, city, addressCountry, email, phone, website, kybStatus, validationCode := validateLegalUpdate(req, current)
	if validationCode != "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails("org."+validationCode, strings.ReplaceAll(validationCode, "_", " "), r, map[string]any{"field": strings.TrimPrefix(validationCode, "invalid_")}))
		return
	}

	updated, err := h.orgQueries.UpdateOrganization(ctx,
		orgID, req.Name, req.Slug, req.Country, req.DefaultLocale, req.ReservationTTLSeconds,
		legalName, taxID, taxScheme, registrationNumber, line1, line2, postalCode, city, addressCountry, email, phone, website, kybStatus,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("org.not_found", "organization not found", r))
			return
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
			httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
				"org.duplicate",
				"an organization with that name or slug already exists",
				r,
			))
			return
		}
		h.logger.Error("org: update failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"org.update_failed", "failed to update organization", r,
		))
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"organization": responseFromOrganization(updated),
	})
}

// HandleDeleteOrg serves DELETE /v1/organizations/{id}.
// Performs a soft-delete (sets deleted_at = now()) and writes an audit event
// inside the same transaction. Requires JWT + "org.delete" permission.
func (h *Handler) HandleDeleteOrg(w http.ResponseWriter, r *http.Request) {
	if h.orgQueries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	orgID, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}

	if !h.requireOrgMembership(w, r, orgID) {
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

	qtx := h.orgQueries.WithTx(tx)

	deleted, err := qtx.SoftDeleteOrganization(ctx, orgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("org.not_found", "organization not found", r))
			return
		}
		h.logger.Error("org: soft-delete failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"org.delete_failed", "failed to delete organization", r,
		))
		return
	}

	if h.audit != nil {
		actor, _ := auth.ActorFromContext(ctx)
		auditEv := audit.Event{
			OccurredAt:   time.Now().UTC(),
			ActorType:    "user",
			ActorID:      actor.ID,
			Action:       "v1.org.delete",
			ResourceType: "organization",
			ResourceID:   orgID.String(),
			RequestID:    logging.RequestID(ctx),
			TraceID:      logging.TraceID(ctx),
			IP:           httputil.ExtractClientIP(r),
			Metadata: map[string]any{
				"org_name": deleted.Name,
				"org_slug": deleted.Slug,
			},
		}
		if err := h.audit.WriteTx(ctx, tx, auditEv); err != nil {
			h.logger.Error("org: audit write failed", slog.String("error", err.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"org.audit_failed", "failed to write audit event", r,
			))
			return
		}
	}

	if err := tx.Commit(ctx); err != nil {
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"org.commit_failed", "failed to commit transaction", r,
		))
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"organization": orgResponse{
			ID:                    deleted.ID.String(),
			Name:                  deleted.Name,
			Slug:                  deleted.Slug,
			Country:               deleted.Country,
			DefaultLocale:         deleted.DefaultLocale,
			ReservationTTLSeconds: deleted.ReservationTTLSeconds,
			CreatedAt:             deleted.CreatedAt.UTC().Format(time.RFC3339),
			UpdatedAt:             deleted.UpdatedAt.UTC().Format(time.RFC3339),
		},
		"deleted": true,
	})
}
