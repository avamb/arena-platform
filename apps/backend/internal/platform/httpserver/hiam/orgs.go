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
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

// OrgResponse is the exported JSON representation of a single organization,
// for use by the httpserver shim layer (orgs_test.go references orgResponse
// from package httpserver via iam_shims.go).
type OrgResponse struct {
	ID                    string `json:"id"`
	Name                  string `json:"name"`
	Slug                  string `json:"slug"`
	Country               string `json:"country"`
	DefaultLocale         string `json:"default_locale"`
	ReservationTTLSeconds int32  `json:"reservation_ttl_seconds"`
	CreatedAt             string `json:"created_at"`
	UpdatedAt             string `json:"updated_at"`
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
	Name                  string `json:"name"`
	Slug                  string `json:"slug"`
	Country               string `json:"country"`
	DefaultLocale         string `json:"default_locale"`
	ReservationTTLSeconds int32  `json:"reservation_ttl_seconds"`
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
		"organization": orgResponse{
			ID:                    org.ID.String(),
			Name:                  org.Name,
			Slug:                  org.Slug,
			Country:               org.Country,
			DefaultLocale:         org.DefaultLocale,
			ReservationTTLSeconds: org.ReservationTTLSeconds,
			CreatedAt:             org.CreatedAt.UTC().Format(time.RFC3339),
			UpdatedAt:             org.UpdatedAt.UTC().Format(time.RFC3339),
		},
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
		result = append(result, orgResponse{
			ID:                    o.ID.String(),
			Name:                  o.Name,
			Slug:                  o.Slug,
			Country:               o.Country,
			DefaultLocale:         o.DefaultLocale,
			ReservationTTLSeconds: o.ReservationTTLSeconds,
			CreatedAt:             o.CreatedAt.UTC().Format(time.RFC3339),
			UpdatedAt:             o.UpdatedAt.UTC().Format(time.RFC3339),
		})
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
		"organization": orgResponse{
			ID:                    o.ID.String(),
			Name:                  o.Name,
			Slug:                  o.Slug,
			Country:               o.Country,
			DefaultLocale:         o.DefaultLocale,
			ReservationTTLSeconds: o.ReservationTTLSeconds,
			CreatedAt:             o.CreatedAt.UTC().Format(time.RFC3339),
			UpdatedAt:             o.UpdatedAt.UTC().Format(time.RFC3339),
		},
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
	if err := json.Unmarshal(body, &req); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("org.invalid_json", "request body is not valid JSON", r))
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	req.Slug = strings.TrimSpace(strings.ToLower(req.Slug))
	req.Country = strings.TrimSpace(req.Country)
	req.DefaultLocale = strings.TrimSpace(req.DefaultLocale)

	updated, err := h.orgQueries.UpdateOrganization(ctx,
		orgID, req.Name, req.Slug, req.Country, req.DefaultLocale, req.ReservationTTLSeconds,
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
		"organization": orgResponse{
			ID:                    updated.ID.String(),
			Name:                  updated.Name,
			Slug:                  updated.Slug,
			Country:               updated.Country,
			DefaultLocale:         updated.DefaultLocale,
			ReservationTTLSeconds: updated.ReservationTTLSeconds,
			CreatedAt:             updated.CreatedAt.UTC().Format(time.RFC3339),
			UpdatedAt:             updated.UpdatedAt.UTC().Format(time.RFC3339),
		},
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
