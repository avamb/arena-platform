// network_orgs.go implements the network-organization assignment endpoints (feature #210).
package hnetworks

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
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

// networkOrgResponse is the JSON projection of a network_organizations row.
type networkOrgResponse struct {
	ID             string `json:"id"`
	NetworkID      string `json:"network_id"`
	OrganizationID string `json:"organization_id"`
	AssignmentKind string `json:"assignment_kind"`
	Status         string `json:"status"`
	AttachedAt     string `json:"attached_at"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
}

func networkOrgFromRow(row gen.NetworkOrganizationRow) networkOrgResponse {
	return networkOrgResponse{
		ID:             row.ID.String(),
		NetworkID:      row.NetworkID.String(),
		OrganizationID: row.OrganizationID.String(),
		AssignmentKind: row.AssignmentKind,
		Status:         row.Status,
		AttachedAt:     row.AttachedAt.UTC().Format(time.RFC3339),
		CreatedAt:      row.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:      row.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

// WriteNetworkOrgAudit emits a v1.network.<kind>s.<verb> audit_events row.
func (h *Handler) WriteNetworkOrgAudit(r *http.Request, action, networkID, orgID, kind string, metadata map[string]any) {
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
	metadata["organization_id"] = orgID
	metadata["assignment_kind"] = kind
	metadata["target"] = orgID

	ev := audit.Event{
		OccurredAt:   time.Now().UTC(),
		ActorType:    actorType,
		ActorID:      actorID,
		Action:       action,
		ResourceType: "network_organization",
		ResourceID:   networkID + ":" + orgID + ":" + kind,
		RequestID:    logging.RequestID(r.Context()),
		TraceID:      logging.TraceID(r.Context()),
		IP:           httputil.ExtractClientIP(r),
		Metadata:     metadata,
	}
	if err := h.audit.Write(r.Context(), ev); err != nil {
		h.logger.Warn("network_org: audit write failed",
			slog.String("action", action),
			slog.String("network_id", networkID),
			slog.String("organization_id", orgID),
			slog.String("assignment_kind", kind),
			slog.Any("error", err),
		)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Attach: POST /v1/admin/networks/{id}/{organizers|agents}
// ─────────────────────────────────────────────────────────────────────────────

type attachNetworkOrgRequest struct {
	OrganizationID string `json:"organization_id"`
}

// HandleAttachNetworkOrganization is closed over the assignment kind so a
// single implementation backs both POST /organizers and POST /agents.
func (h *Handler) HandleAttachNetworkOrganization(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.queries == nil {
			httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
				"dependency.database_unavailable", "database is not available", r,
			))
			return
		}
		networkID, ok := httputil.UUIDPathParam(w, r, "id")
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
				"network_org.invalid_body", "cannot read request body: "+err.Error(), r))
			return
		}
		if len(body) == 0 {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
				"network_org.empty_body", "request body is required", r))
			return
		}
		var req attachNetworkOrgRequest
		if err := json.Unmarshal(body, &req); err != nil {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
				"network_org.invalid_json", "request body is not valid JSON", r))
			return
		}
		req.OrganizationID = strings.TrimSpace(req.OrganizationID)
		if req.OrganizationID == "" {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"network_org.invalid_organization_id", "organization_id is required", r,
				map[string]any{"field": "organization_id"},
			))
			return
		}
		orgID, err := uuid.Parse(req.OrganizationID)
		if err != nil {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"network_org.invalid_organization_id", "organization_id must be a valid UUID", r,
				map[string]any{"field": "organization_id"},
			))
			return
		}

		if !h.assertNetworkExists(w, r, networkID) {
			return
		}

		row, err := h.queries.InsertNetworkOrganization(r.Context(), networkID, orgID, kind)
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
				existing, sErr := h.queries.SetNetworkOrganizationStatus(
					r.Context(), networkID, orgID, kind, "active")
				if sErr != nil {
					if errors.Is(sErr, pgx.ErrNoRows) {
						httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
							"network_org.duplicate",
							"organization is already attached to this network", r))
						return
					}
					h.logger.Error("network_org: re-activate failed",
						slog.String("assignment_kind", kind),
						slog.String("error", sErr.Error()))
					httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
						"network_org.attach_failed",
						"failed to re-activate network organization attachment", r))
					return
				}
				h.WriteNetworkOrgAudit(r, "v1.network."+kind+"s.reactivate",
					networkID.String(), orgID.String(), kind,
					map[string]any{"status": existing.Status, "reason": reason})
				httputil.WriteJSON(w, http.StatusOK, map[string]any{
					"network_organization": networkOrgFromRow(existing),
					"reactivated":          true,
				})
				return
			}
			if errors.As(err, &pgErr) && pgErr.Code == pgForeignKeyViolation {
				httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
					"network_org.organization_not_found",
					"target organization does not exist", r))
				return
			}
			h.logger.Error("network_org: insert failed",
				slog.String("assignment_kind", kind),
				slog.String("error", err.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"network_org.attach_failed",
				"failed to attach organization to network", r))
			return
		}

		h.WriteNetworkOrgAudit(r, "v1.network."+kind+"s.attach",
			networkID.String(), orgID.String(), kind,
			map[string]any{"status": row.Status, "reason": reason})

		httputil.WriteJSON(w, http.StatusCreated, map[string]any{
			"network_organization": networkOrgFromRow(row),
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Detach: DELETE /v1/admin/networks/{id}/{organizers|agents}/{orgId}
// ─────────────────────────────────────────────────────────────────────────────

func (h *Handler) HandleDetachNetworkOrganization(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.queries == nil {
			httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
				"dependency.database_unavailable", "database is not available", r,
			))
			return
		}
		networkID, ok := httputil.UUIDPathParam(w, r, "id")
		if !ok {
			return
		}
		orgID, ok := httputil.UUIDPathParam(w, r, "orgId")
		if !ok {
			return
		}
		reason, ok := httputil.RequireAdminReason(w, r)
		if !ok {
			return
		}

		if !h.assertNetworkExists(w, r, networkID) {
			return
		}

		row, err := h.queries.SetNetworkOrganizationStatus(
			r.Context(), networkID, orgID, kind, "revoked")
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
					"network_org.not_found",
					"organization is not attached to this network", r))
				return
			}
			h.logger.Error("network_org: revoke failed",
				slog.String("assignment_kind", kind),
				slog.String("error", err.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"network_org.detach_failed",
				"failed to detach organization from network", r))
			return
		}

		h.WriteNetworkOrgAudit(r, "v1.network."+kind+"s.detach",
			networkID.String(), orgID.String(), kind,
			map[string]any{"status": row.Status, "reason": reason})

		httputil.WriteJSON(w, http.StatusOK, map[string]any{
			"network_organization": networkOrgFromRow(row),
			"detached":             true,
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// List: GET /v1/admin/networks/{id}/{organizers|agents}
// ─────────────────────────────────────────────────────────────────────────────

func (h *Handler) HandleListNetworkOrganizations(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.queries == nil {
			httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
				"dependency.database_unavailable", "database is not available", r,
			))
			return
		}
		networkID, ok := httputil.UUIDPathParam(w, r, "id")
		if !ok {
			return
		}

		if !h.assertNetworkExists(w, r, networkID) {
			return
		}

		var (
			rows []gen.NetworkOrganizationRow
			err  error
		)
		switch kind {
		case NetworkAssignmentKindOrganizer:
			rows, err = h.queries.ListOrganizersByNetwork(r.Context(), networkID)
		case NetworkAssignmentKindAgent:
			rows, err = h.queries.ListAgentsByNetwork(r.Context(), networkID)
		default:
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"network_org.invalid_kind",
				"unsupported assignment kind: "+kind, r))
			return
		}
		if err != nil {
			h.logger.Error("network_org: list failed",
				slog.String("assignment_kind", kind),
				slog.String("error", err.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"network_org.list_failed",
				"failed to list network organizations", r))
			return
		}
		out := make([]networkOrgResponse, 0, len(rows))
		for _, row := range rows {
			out = append(out, networkOrgFromRow(row))
		}
		httputil.WriteJSON(w, http.StatusOK, map[string]any{
			"network_id":      networkID.String(),
			"assignment_kind": kind,
			kind + "s":        out,
			"total":           len(out),
		})
	}
}
