// networks.go implements the operator-network CRUD API endpoints (feature #208).
//
// Operator networks (introduced by migration 0043_operator_networks.sql,
// design note in 09_autoforge/admin_ui/operator_network_design_note.md)
// represent a platform-level grouping that overlays organizations: a
// network_operator user (role added in #203) is assigned to one or more
// networks, and each network is in turn attached to organizations as either
// 'organizer' or 'agent' via network_organizations.
//
// Endpoints:
//
//	POST   /v1/operator-networks            — create (network.create)
//	GET    /v1/operator-networks            — list non-archived (network.read)
//	GET    /v1/operator-networks/{id}       — get one (network.read)
//	PATCH  /v1/operator-networks/{id}       — update mutable fields (network.update)
//	POST   /v1/operator-networks/{id}/archive — soft-archive (network.archive)
//
// Permission gating uses the existing applyAuth helper. Per migration
// 0044_network_permissions.sql, network.create and network.archive are bound
// only to platform_superadmin and the legacy admin role, so the standard
// permission middleware is sufficient to satisfy the "only platform_superadmin
// can create/archive" requirement in #208.
//
// Audit:
//
//	Create/update/archive write an audit_events row with action
//	v1.operator_network.<verb> describing the change. Per SAUI-09 the
//	caller must supply an X-Admin-Reason header; the value is stamped
//	into the audit_events metadata under "reason" so the immutable audit
//	trail records why each mutation was performed.
package httpserver

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

// operatorNetworkSlugRE mirrors the regex CHECK constraint enforced by the
// 0043_operator_networks.sql migration so we reject bad slugs before the DB
// round-trip and produce a friendlier error envelope than a 500 from a
// CHECK violation.
var operatorNetworkSlugRE = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?$`)

// operatorNetworkResponse is the JSON representation of an operator_network row.
type operatorNetworkResponse struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	Slug       string  `json:"slug"`
	Status     string  `json:"status"`
	ArchivedAt *string `json:"archived_at"`
	CreatedAt  string  `json:"created_at"`
	UpdatedAt  string  `json:"updated_at"`
}

// operatorNetworkFromRow projects a gen.OperatorNetworkRow into the JSON shape.
func operatorNetworkFromRow(row gen.OperatorNetworkRow) operatorNetworkResponse {
	resp := operatorNetworkResponse{
		ID:        row.ID.String(),
		Name:      row.Name,
		Slug:      row.Slug,
		Status:    row.Status,
		CreatedAt: row.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt: row.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if row.ArchivedAt != nil {
		s := row.ArchivedAt.UTC().Format(time.RFC3339)
		resp.ArchivedAt = &s
	}
	return resp
}

// writeOperatorNetworkAudit emits an audit_events row for a network mutation.
// Fire-and-forget: a failed audit write is logged but does not affect the
// response status, mirroring the superadmin/orgs handlers.
func (s *Server) writeOperatorNetworkAudit(r *http.Request, action, networkID string, metadata map[string]any) {
	if s.audit == nil {
		return
	}
	actorID := ""
	actorType := "anonymous"
	if a, ok := auth.ActorFromContext(r.Context()); ok {
		actorID = a.ID
		actorType = "user"
	}
	// Stamp network_id (and target=network_id, since the network is itself the
	// target of the mutation) into metadata so audit consumers can filter
	// platform-level network mutations by originating network without parsing
	// the resource_id column. This matches the contract used by
	// writeNetworkUserAudit / writeNetworkOrgAudit (see feature #215).
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata["network_id"] = networkID
	metadata["target"] = networkID
	ev := audit.Event{
		OccurredAt:   time.Now().UTC(),
		ActorType:    actorType,
		ActorID:      actorID,
		Action:       action,
		ResourceType: "operator_network",
		ResourceID:   networkID,
		RequestID:    logging.RequestID(r.Context()),
		TraceID:      logging.TraceID(r.Context()),
		IP:           extractClientIP(r),
		Metadata:     metadata,
	}
	if err := s.audit.Write(r.Context(), ev); err != nil {
		s.logger.Warn("operator_network: audit write failed",
			slog.String("action", action),
			slog.String("network_id", networkID),
			slog.Any("error", err),
		)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/operator-networks
// ─────────────────────────────────────────────────────────────────────────────

type createOperatorNetworkRequest struct {
	Name string `json:"name"`
	Slug string `json:"slug"`
}

func (s *Server) handleCreateOperatorNetwork(w http.ResponseWriter, r *http.Request) {
	if s.networkQueries == nil || s.pool == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	// SAUI-09: every operator-network mutation must carry an audit reason
	// via X-Admin-Reason; the value is stamped into the audit_events
	// metadata under "reason" alongside the existing network_id/target fields.
	reason, ok := requireAdminReason(w, r)
	if !ok {
		return
	}
	ctx := r.Context()

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope(
			"operator_network.invalid_body", "cannot read request body: "+err.Error(), r))
		return
	}
	if len(body) == 0 {
		writeJSON(w, http.StatusBadRequest, errorEnvelope(
			"operator_network.empty_body", "request body is required", r))
		return
	}
	var req createOperatorNetworkRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope(
			"operator_network.invalid_json", "request body is not valid JSON", r))
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	req.Slug = strings.TrimSpace(strings.ToLower(req.Slug))

	if req.Name == "" {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"operator_network.invalid_name", "name is required", r,
			map[string]any{"field": "name"},
		))
		return
	}
	if req.Slug == "" {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"operator_network.invalid_slug", "slug is required", r,
			map[string]any{"field": "slug"},
		))
		return
	}
	if !operatorNetworkSlugRE.MatchString(req.Slug) {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"operator_network.invalid_slug",
			"slug must match ^[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?$",
			r, map[string]any{"field": "slug"},
		))
		return
	}

	row, err := s.networkQueries.InsertOperatorNetwork(ctx, req.Name, req.Slug)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
			writeJSON(w, http.StatusConflict, errorEnvelope(
				"operator_network.duplicate_slug",
				"an active operator network with that slug already exists", r,
			))
			return
		}
		s.logger.Error("operator_network: insert failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"operator_network.insert_failed", "failed to create operator network", r,
		))
		return
	}

	s.writeOperatorNetworkAudit(r, "v1.operator_network.create", row.ID.String(),
		map[string]any{"name": row.Name, "slug": row.Slug, "reason": reason})

	writeJSON(w, http.StatusCreated, map[string]any{
		"operator_network": operatorNetworkFromRow(row),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/operator-networks
// ─────────────────────────────────────────────────────────────────────────────

func (s *Server) handleListOperatorNetworks(w http.ResponseWriter, r *http.Request) {
	if s.networkQueries == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	rows, err := s.networkQueries.ListOperatorNetworks(r.Context())
	if err != nil {
		s.logger.Error("operator_network: list failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"operator_network.list_failed", "failed to list operator networks", r,
		))
		return
	}
	out := make([]operatorNetworkResponse, 0, len(rows))
	for _, row := range rows {
		out = append(out, operatorNetworkFromRow(row))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"operator_networks": out,
		"total":             len(out),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/operator-networks/{id}
// ─────────────────────────────────────────────────────────────────────────────

func (s *Server) handleGetOperatorNetwork(w http.ResponseWriter, r *http.Request) {
	if s.networkQueries == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	id, ok := uuidPathParam(w, r, "id")
	if !ok {
		return
	}
	row, err := s.networkQueries.GetOperatorNetworkByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, errorEnvelope(
				"operator_network.not_found", "operator network not found", r))
			return
		}
		s.logger.Error("operator_network: get failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"operator_network.get_failed", "failed to get operator network", r,
		))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"operator_network": operatorNetworkFromRow(row),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// PATCH /v1/operator-networks/{id}
// ─────────────────────────────────────────────────────────────────────────────

type updateOperatorNetworkRequest struct {
	Name string `json:"name"`
	Slug string `json:"slug"`
}

func (s *Server) handleUpdateOperatorNetwork(w http.ResponseWriter, r *http.Request) {
	if s.networkQueries == nil || s.pool == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	id, ok := uuidPathParam(w, r, "id")
	if !ok {
		return
	}
	// SAUI-09: same audit-reason gate as create/archive.
	reason, ok := requireAdminReason(w, r)
	if !ok {
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope(
			"operator_network.invalid_body", "cannot read request body: "+err.Error(), r))
		return
	}
	if len(body) == 0 {
		writeJSON(w, http.StatusBadRequest, errorEnvelope(
			"operator_network.empty_body", "request body is required", r))
		return
	}
	var req updateOperatorNetworkRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope(
			"operator_network.invalid_json", "request body is not valid JSON", r))
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	req.Slug = strings.TrimSpace(strings.ToLower(req.Slug))

	if req.Name == "" && req.Slug == "" {
		writeJSON(w, http.StatusBadRequest, errorEnvelope(
			"operator_network.no_changes",
			"at least one of name or slug must be supplied", r))
		return
	}
	if req.Slug != "" && !operatorNetworkSlugRE.MatchString(req.Slug) {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"operator_network.invalid_slug",
			"slug must match ^[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?$",
			r, map[string]any{"field": "slug"},
		))
		return
	}

	row, err := s.networkQueries.UpdateOperatorNetwork(r.Context(), id, req.Name, req.Slug)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, errorEnvelope(
				"operator_network.not_found",
				"operator network not found or already archived", r))
			return
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
			writeJSON(w, http.StatusConflict, errorEnvelope(
				"operator_network.duplicate_slug",
				"an active operator network with that slug already exists", r,
			))
			return
		}
		s.logger.Error("operator_network: update failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"operator_network.update_failed", "failed to update operator network", r,
		))
		return
	}

	s.writeOperatorNetworkAudit(r, "v1.operator_network.update", row.ID.String(),
		map[string]any{
			"name_changed": req.Name != "",
			"slug_changed": req.Slug != "",
			"reason":       reason,
		})

	writeJSON(w, http.StatusOK, map[string]any{
		"operator_network": operatorNetworkFromRow(row),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/operator-networks/{id}/archive
// ─────────────────────────────────────────────────────────────────────────────

// handleArchiveOperatorNetwork soft-archives an operator network. Idempotent
// at the DB layer: re-archiving a row already in 'archived' state returns
// pgx.ErrNoRows, which the handler maps to 404.
func (s *Server) handleArchiveOperatorNetwork(w http.ResponseWriter, r *http.Request) {
	if s.networkQueries == nil || s.pool == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	id, ok := uuidPathParam(w, r, "id")
	if !ok {
		return
	}
	// SAUI-09: archive is a destructive operator-network mutation; require
	// an audit reason just like create/update.
	reason, ok := requireAdminReason(w, r)
	if !ok {
		return
	}

	row, err := s.networkQueries.ArchiveOperatorNetwork(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, errorEnvelope(
				"operator_network.not_found",
				"operator network not found or already archived", r))
			return
		}
		s.logger.Error("operator_network: archive failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"operator_network.archive_failed", "failed to archive operator network", r,
		))
		return
	}

	s.writeOperatorNetworkAudit(r, "v1.operator_network.archive", row.ID.String(),
		map[string]any{"slug": row.Slug, "reason": reason})

	writeJSON(w, http.StatusOK, map[string]any{
		"operator_network": operatorNetworkFromRow(row),
		"archived":         true,
	})
}
