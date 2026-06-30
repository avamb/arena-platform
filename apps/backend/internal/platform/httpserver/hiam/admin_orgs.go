// admin_orgs.go implements admin-namespace Organizations CRUD endpoints
// (feature #233).
//
// These are the admin-console-facing counterparts to /v1/organizations:
//
//	POST  /v1/admin/organizations              — create a new organization
//	PATCH /v1/admin/organizations/{id}         — update an existing organization
//	POST  /v1/admin/organizations/{id}/archive — archive (soft-delete) an organization
//
// All three are gated by:
//   - JWT auth (Server.applyAuth middleware)
//   - RBAC permission (org.create / org.update / org.delete)
//   - X-Admin-Reason header (the same audit-reason gate that the rest of
//     the /v1/admin namespace already requires; see superadmin.go and
//     apps/admin-web/src/lib/api/reason.ts)
//
// Every successful write records an audit event with action
// "v1.admin.org.{create,update,archive}", resource_type "organization",
// and the X-Admin-Reason string carried in metadata.
//
// Errors are reported via the canonical JSON error envelope (code +
// message), matching the rest of the /v1 API surface.
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

// HandleAdminCreateOrg serves POST /v1/admin/organizations.
// Requires JWT + org.create + X-Admin-Reason.
// handleAdminCreateOrg is the legacy name for this operation.
func (h *Handler) HandleAdminCreateOrg(w http.ResponseWriter, r *http.Request) {
	if h.orgQueries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	reason, ok := requireAdminReason(w, r)
	if !ok {
		return
	}
	ctx := r.Context()

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("admin_org.invalid_body",
			"cannot read request body: "+err.Error(), r))
		return
	}
	if len(body) == 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("admin_org.empty_body",
			"request body is required", r))
		return
	}

	var req createOrgRequest
	if err := json.Unmarshal(body, &req); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("admin_org.invalid_json",
			"request body is not valid JSON", r))
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	req.Slug = strings.TrimSpace(strings.ToLower(req.Slug))

	if req.Name == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"admin_org.invalid_name", "name is required", r,
			map[string]any{"field": "name"},
		))
		return
	}
	if req.Slug == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"admin_org.invalid_slug", "slug is required", r,
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
				"admin_org.duplicate",
				"an organization with that name or slug already exists",
				r,
			))
			return
		}
		h.logger.Error("admin_org: insert failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"admin_org.insert_failed", "failed to create organization", r,
		))
		return
	}

	h.writeAdminOrgAudit(r, "v1.admin.org.create", org.ID.String(), reason, map[string]any{
		"org_name": org.Name,
		"org_slug": org.Slug,
	})

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

// HandleAdminUpdateOrg serves PATCH /v1/admin/organizations/{id}.
// Requires JWT + org.update + X-Admin-Reason.
// handleAdminUpdateOrg is the legacy name for this operation.
func (h *Handler) HandleAdminUpdateOrg(w http.ResponseWriter, r *http.Request) {
	if h.orgQueries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	reason, ok := requireAdminReason(w, r)
	if !ok {
		return
	}
	ctx := r.Context()

	orgID, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("admin_org.invalid_body",
			"cannot read request body: "+err.Error(), r))
		return
	}
	if len(body) == 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("admin_org.empty_body",
			"request body is required", r))
		return
	}

	var req updateOrgRequest
	if err := json.Unmarshal(body, &req); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("admin_org.invalid_json",
			"request body is not valid JSON", r))
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
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("admin_org.not_found",
				"organization not found", r))
			return
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
			httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
				"admin_org.duplicate",
				"an organization with that name or slug already exists",
				r,
			))
			return
		}
		h.logger.Error("admin_org: update failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"admin_org.update_failed", "failed to update organization", r,
		))
		return
	}

	h.writeAdminOrgAudit(r, "v1.admin.org.update", updated.ID.String(), reason, map[string]any{
		"org_name": updated.Name,
		"org_slug": updated.Slug,
		"fields": map[string]any{
			"name":                    req.Name,
			"slug":                    req.Slug,
			"country":                 req.Country,
			"default_locale":          req.DefaultLocale,
			"reservation_ttl_seconds": req.ReservationTTLSeconds,
		},
	})

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

// HandleAdminArchiveOrg serves POST /v1/admin/organizations/{id}/archive.
// Performs a soft-delete (sets deleted_at = now()) and writes an audit event
// inside the same transaction. Requires JWT + org.delete + X-Admin-Reason.
// handleAdminArchiveOrg is the legacy name for this operation.
func (h *Handler) HandleAdminArchiveOrg(w http.ResponseWriter, r *http.Request) {
	if h.orgQueries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	reason, ok := requireAdminReason(w, r)
	if !ok {
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

	archived, err := qtx.SoftDeleteOrganization(ctx, orgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("admin_org.not_found",
				"organization not found", r))
			return
		}
		h.logger.Error("admin_org: archive failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"admin_org.archive_failed", "failed to archive organization", r,
		))
		return
	}

	if h.audit != nil {
		actor, _ := auth.ActorFromContext(ctx)
		ev := audit.Event{
			OccurredAt:   time.Now().UTC(),
			ActorType:    "user",
			ActorID:      actor.ID,
			Action:       "v1.admin.org.archive",
			ResourceType: "organization",
			ResourceID:   orgID.String(),
			RequestID:    logging.RequestID(ctx),
			TraceID:      logging.TraceID(ctx),
			IP:           httputil.ExtractClientIP(r),
			Metadata: map[string]any{
				"reason":   reason,
				"org_name": archived.Name,
				"org_slug": archived.Slug,
			},
		}
		if err := h.audit.WriteTx(ctx, tx, ev); err != nil {
			h.logger.Error("admin_org: audit write failed", slog.String("error", err.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"admin_org.audit_failed", "failed to write audit event", r,
			))
			return
		}
	}

	if err := tx.Commit(ctx); err != nil {
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"admin_org.commit_failed", "failed to commit transaction", r,
		))
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"organization": orgResponse{
			ID:                    archived.ID.String(),
			Name:                  archived.Name,
			Slug:                  archived.Slug,
			Country:               archived.Country,
			DefaultLocale:         archived.DefaultLocale,
			ReservationTTLSeconds: archived.ReservationTTLSeconds,
			CreatedAt:             archived.CreatedAt.UTC().Format(time.RFC3339),
			UpdatedAt:             archived.UpdatedAt.UTC().Format(time.RFC3339),
		},
		"archived": true,
	})
}

// writeAdminOrgAudit emits a single audit event for an admin-org write. The
// archive path uses an in-transaction WriteTx for atomicity; create/update do
// not need that guarantee, so this fire-and-forget helper logs failures but
// does not abort the response.
func (h *Handler) writeAdminOrgAudit(r *http.Request, action, resourceID, reason string, extra map[string]any) {
	if h.audit == nil {
		return
	}
	actor, _ := auth.ActorFromContext(r.Context())
	metadata := map[string]any{"reason": reason}
	for k, v := range extra {
		metadata[k] = v
	}
	ev := audit.Event{
		OccurredAt:   time.Now().UTC(),
		ActorType:    "user",
		ActorID:      actor.ID,
		Action:       action,
		ResourceType: "organization",
		ResourceID:   resourceID,
		RequestID:    logging.RequestID(r.Context()),
		TraceID:      logging.TraceID(r.Context()),
		IP:           httputil.ExtractClientIP(r),
		Metadata:     metadata,
	}
	if err := h.audit.Write(r.Context(), ev); err != nil {
		h.logger.Warn("admin_org: audit write failed",
			slog.String("action", action),
			slog.Any("error", err),
		)
	}
}
