// networks.go implements the operator-network CRUD API endpoints (feature #208).
package hnetworks

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
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

const pgUniqueViolation = "23505"

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

// WriteOperatorNetworkAudit emits an audit_events row for a network mutation.
// Fire-and-forget: a failed audit write is logged but does not affect the
// response status.
func (h *Handler) WriteOperatorNetworkAudit(r *http.Request, action, networkID string, metadata map[string]any) {
	if h.audit == nil {
		return
	}
	actorID := ""
	actorType := "anonymous"
	if a, ok := auth.ActorFromContext(r.Context()); ok {
		actorID = a.ID
		actorType = "user"
	}
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
		IP:           httputil.ExtractClientIP(r),
		Metadata:     metadata,
	}
	if err := h.audit.Write(r.Context(), ev); err != nil {
		h.logger.Warn("operator_network: audit write failed",
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

func (h *Handler) HandleCreateOperatorNetwork(w http.ResponseWriter, r *http.Request) {
	if h.queries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	reason, ok := httputil.RequireAdminReason(w, r)
	if !ok {
		return
	}
	ctx := r.Context()

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"operator_network.invalid_body", "cannot read request body: "+err.Error(), r))
		return
	}
	if len(body) == 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"operator_network.empty_body", "request body is required", r))
		return
	}
	var req createOperatorNetworkRequest
	if err := json.Unmarshal(body, &req); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"operator_network.invalid_json", "request body is not valid JSON", r))
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	req.Slug = strings.TrimSpace(strings.ToLower(req.Slug))

	if req.Name == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"operator_network.invalid_name", "name is required", r,
			map[string]any{"field": "name"},
		))
		return
	}
	if req.Slug == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"operator_network.invalid_slug", "slug is required", r,
			map[string]any{"field": "slug"},
		))
		return
	}
	if !operatorNetworkSlugRE.MatchString(req.Slug) {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"operator_network.invalid_slug",
			"slug must match ^[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?$",
			r, map[string]any{"field": "slug"},
		))
		return
	}

	row, err := h.queries.InsertOperatorNetwork(ctx, req.Name, req.Slug)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
			httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
				"operator_network.duplicate_slug",
				"an active operator network with that slug already exists", r,
			))
			return
		}
		h.logger.Error("operator_network: insert failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"operator_network.insert_failed", "failed to create operator network", r,
		))
		return
	}

	h.WriteOperatorNetworkAudit(r, "v1.operator_network.create", row.ID.String(),
		map[string]any{"name": row.Name, "slug": row.Slug, "reason": reason})

	httputil.WriteJSON(w, http.StatusCreated, map[string]any{
		"operator_network": operatorNetworkFromRow(row),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/operator-networks
// ─────────────────────────────────────────────────────────────────────────────

func (h *Handler) HandleListOperatorNetworks(w http.ResponseWriter, r *http.Request) {
	if h.queries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	rows, err := h.queries.ListOperatorNetworks(r.Context())
	if err != nil {
		h.logger.Error("operator_network: list failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"operator_network.list_failed", "failed to list operator networks", r,
		))
		return
	}
	out := make([]operatorNetworkResponse, 0, len(rows))
	for _, row := range rows {
		out = append(out, operatorNetworkFromRow(row))
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"operator_networks": out,
		"total":             len(out),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/operator-networks/{id}
// ─────────────────────────────────────────────────────────────────────────────

func (h *Handler) HandleGetOperatorNetwork(w http.ResponseWriter, r *http.Request) {
	if h.queries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	id, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}
	row, err := h.queries.GetOperatorNetworkByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"operator_network.not_found", "operator network not found", r))
			return
		}
		h.logger.Error("operator_network: get failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"operator_network.get_failed", "failed to get operator network", r,
		))
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
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

func (h *Handler) HandleUpdateOperatorNetwork(w http.ResponseWriter, r *http.Request) {
	if h.queries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	id, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}
	reason, ok := httputil.RequireAdminReason(w, r)
	if !ok {
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"operator_network.invalid_body", "cannot read request body: "+err.Error(), r))
		return
	}
	if len(body) == 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"operator_network.empty_body", "request body is required", r))
		return
	}
	var req updateOperatorNetworkRequest
	if err := json.Unmarshal(body, &req); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"operator_network.invalid_json", "request body is not valid JSON", r))
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	req.Slug = strings.TrimSpace(strings.ToLower(req.Slug))

	if req.Name == "" && req.Slug == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"operator_network.no_changes",
			"at least one of name or slug must be supplied", r))
		return
	}
	if req.Slug != "" && !operatorNetworkSlugRE.MatchString(req.Slug) {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"operator_network.invalid_slug",
			"slug must match ^[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?$",
			r, map[string]any{"field": "slug"},
		))
		return
	}

	row, err := h.queries.UpdateOperatorNetwork(r.Context(), id, req.Name, req.Slug)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"operator_network.not_found",
				"operator network not found or already archived", r))
			return
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
			httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
				"operator_network.duplicate_slug",
				"an active operator network with that slug already exists", r,
			))
			return
		}
		h.logger.Error("operator_network: update failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"operator_network.update_failed", "failed to update operator network", r,
		))
		return
	}

	h.WriteOperatorNetworkAudit(r, "v1.operator_network.update", row.ID.String(),
		map[string]any{
			"name_changed": req.Name != "",
			"slug_changed": req.Slug != "",
			"reason":       reason,
		})

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"operator_network": operatorNetworkFromRow(row),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/operator-networks/{id}/archive
// ─────────────────────────────────────────────────────────────────────────────

func (h *Handler) HandleArchiveOperatorNetwork(w http.ResponseWriter, r *http.Request) {
	if h.queries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	id, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}
	reason, ok := httputil.RequireAdminReason(w, r)
	if !ok {
		return
	}

	row, err := h.queries.ArchiveOperatorNetwork(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"operator_network.not_found",
				"operator network not found or already archived", r))
			return
		}
		h.logger.Error("operator_network: archive failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"operator_network.archive_failed", "failed to archive operator network", r,
		))
		return
	}

	h.WriteOperatorNetworkAudit(r, "v1.operator_network.archive", row.ID.String(),
		map[string]any{"slug": row.Slug, "reason": reason})

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"operator_network": operatorNetworkFromRow(row),
		"archived":         true,
	})
}
