// network_orgs.go implements the network-organization assignment endpoints
// (feature #210). These let an operator attach / detach / list the
// organizations that participate in an operator_network as either an
// `organizer` (event-organizer that the network coordinates) or an `agent`
// (reseller acting on behalf of those organizers).
//
// Endpoints (mounted in mount_networks.go):
//
//	POST   /v1/admin/networks/{id}/organizers              — attach organizer
//	DELETE /v1/admin/networks/{id}/organizers/{orgId}      — detach organizer
//	GET    /v1/admin/networks/{id}/organizers              — list organizers
//	POST   /v1/admin/networks/{id}/agents                  — attach agent
//	DELETE /v1/admin/networks/{id}/agents/{orgId}          — detach agent
//	GET    /v1/admin/networks/{id}/agents                  — list agents
//
// Permission gating uses the existing applyAuth middleware:
//
//   - network.manage_organizers  — bound to platform_superadmin,
//                                  network_operator, and admin
//                                  (per 0044_network_permissions.sql).
//   - network.manage_agents      — same binding set as above.
//
// Implementation notes:
//
//   - Backed by the network_organizations sqlc helpers landed in feature #205:
//     InsertNetworkOrganization, GetNetworkOrganization,
//     SetNetworkOrganizationStatus, ListOrganizersByNetwork,
//     ListAgentsByNetwork.
//   - Attach is idempotent: a 23505 on the
//     (network_id, organization_id, assignment_kind) unique constraint
//     triggers SetNetworkOrganizationStatus(..., 'active') so a previously
//     revoked attachment can be reactivated without manual SQL.
//   - Detach is a soft-revoke (SetNetworkOrganizationStatus(..., 'revoked'))
//     so the lifecycle remains audit-visible.
//   - The parent operator_network is pre-flighted via GetOperatorNetworkByID
//     (same helper that network_users uses) so a missing or archived parent
//     returns a typed 404/409 rather than leaking a 23503 FK violation.
//   - 23503 on the organization_id FK is translated to a typed 404
//     network_org.organization_not_found so callers can distinguish a
//     missing organization from a missing network.
//   - Each mutation writes a v1.network.<kind>s.<verb> audit_events row.
package httpserver

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
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

// Canonical assignment-kind values matching the
// network_organizations.assignment_kind CHECK constraint in 0043_operator_networks.sql.
const (
	networkAssignmentKindOrganizer = "organizer"
	networkAssignmentKindAgent     = "agent"
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

// writeNetworkOrgAudit emits a v1.network.<kind>s.<verb> audit_events row
// (kind is the singular assignment_kind, the action verb spells out the
// plural — matches the network.users.* style used by network_users.go).
func (s *Server) writeNetworkOrgAudit(r *http.Request, action, networkID, orgID, kind string, metadata map[string]any) {
	if s.audit == nil {
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

	ev := audit.Event{
		OccurredAt:   time.Now().UTC(),
		ActorType:    actorType,
		ActorID:      actorID,
		Action:       action,
		ResourceType: "network_organization",
		ResourceID:   networkID + ":" + orgID + ":" + kind,
		RequestID:    logging.RequestID(r.Context()),
		TraceID:      logging.TraceID(r.Context()),
		IP:           extractClientIP(r),
		Metadata:     metadata,
	}
	if err := s.audit.Write(r.Context(), ev); err != nil {
		s.logger.Warn("network_org: audit write failed",
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

// handleAttachNetworkOrganization is closed over the assignment kind so a
// single implementation backs both POST /organizers and POST /agents.
func (s *Server) handleAttachNetworkOrganization(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.networkQueries == nil || s.pool == nil {
			writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
				"dependency.database_unavailable", "database is not available", r,
			))
			return
		}
		networkID, ok := uuidPathParam(w, r, "id")
		if !ok {
			return
		}

		body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errorEnvelope(
				"network_org.invalid_body", "cannot read request body: "+err.Error(), r))
			return
		}
		if len(body) == 0 {
			writeJSON(w, http.StatusBadRequest, errorEnvelope(
				"network_org.empty_body", "request body is required", r))
			return
		}
		var req attachNetworkOrgRequest
		if err := json.Unmarshal(body, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, errorEnvelope(
				"network_org.invalid_json", "request body is not valid JSON", r))
			return
		}
		req.OrganizationID = strings.TrimSpace(req.OrganizationID)
		if req.OrganizationID == "" {
			writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
				"network_org.invalid_organization_id", "organization_id is required", r,
				map[string]any{"field": "organization_id"},
			))
			return
		}
		orgID, err := uuid.Parse(req.OrganizationID)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
				"network_org.invalid_organization_id", "organization_id must be a valid UUID", r,
				map[string]any{"field": "organization_id"},
			))
			return
		}

		if !s.assertNetworkExists(w, r, networkID) {
			return
		}

		row, err := s.networkQueries.InsertNetworkOrganization(r.Context(), networkID, orgID, kind)
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
				existing, sErr := s.networkQueries.SetNetworkOrganizationStatus(
					r.Context(), networkID, orgID, kind, "active")
				if sErr != nil {
					if errors.Is(sErr, pgx.ErrNoRows) {
						writeJSON(w, http.StatusConflict, errorEnvelope(
							"network_org.duplicate",
							"organization is already attached to this network", r))
						return
					}
					s.logger.Error("network_org: re-activate failed",
						slog.String("assignment_kind", kind),
						slog.String("error", sErr.Error()))
					writeJSON(w, http.StatusInternalServerError, errorEnvelope(
						"network_org.attach_failed",
						"failed to re-activate network organization attachment", r))
					return
				}
				s.writeNetworkOrgAudit(r, "v1.network."+kind+"s.reactivate",
					networkID.String(), orgID.String(), kind,
					map[string]any{"status": existing.Status})
				writeJSON(w, http.StatusOK, map[string]any{
					"network_organization": networkOrgFromRow(existing),
					"reactivated":          true,
				})
				return
			}
			if errors.As(err, &pgErr) && pgErr.Code == pgForeignKeyViolation {
				writeJSON(w, http.StatusNotFound, errorEnvelope(
					"network_org.organization_not_found",
					"target organization does not exist", r))
				return
			}
			s.logger.Error("network_org: insert failed",
				slog.String("assignment_kind", kind),
				slog.String("error", err.Error()))
			writeJSON(w, http.StatusInternalServerError, errorEnvelope(
				"network_org.attach_failed",
				"failed to attach organization to network", r))
			return
		}

		s.writeNetworkOrgAudit(r, "v1.network."+kind+"s.attach",
			networkID.String(), orgID.String(), kind,
			map[string]any{"status": row.Status})

		writeJSON(w, http.StatusCreated, map[string]any{
			"network_organization": networkOrgFromRow(row),
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Detach: DELETE /v1/admin/networks/{id}/{organizers|agents}/{orgId}
// ─────────────────────────────────────────────────────────────────────────────

func (s *Server) handleDetachNetworkOrganization(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.networkQueries == nil || s.pool == nil {
			writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
				"dependency.database_unavailable", "database is not available", r,
			))
			return
		}
		networkID, ok := uuidPathParam(w, r, "id")
		if !ok {
			return
		}
		orgID, ok := uuidPathParam(w, r, "orgId")
		if !ok {
			return
		}

		if !s.assertNetworkExists(w, r, networkID) {
			return
		}

		row, err := s.networkQueries.SetNetworkOrganizationStatus(
			r.Context(), networkID, orgID, kind, "revoked")
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeJSON(w, http.StatusNotFound, errorEnvelope(
					"network_org.not_found",
					"organization is not attached to this network", r))
				return
			}
			s.logger.Error("network_org: revoke failed",
				slog.String("assignment_kind", kind),
				slog.String("error", err.Error()))
			writeJSON(w, http.StatusInternalServerError, errorEnvelope(
				"network_org.detach_failed",
				"failed to detach organization from network", r))
			return
		}

		s.writeNetworkOrgAudit(r, "v1.network."+kind+"s.detach",
			networkID.String(), orgID.String(), kind,
			map[string]any{"status": row.Status})

		writeJSON(w, http.StatusOK, map[string]any{
			"network_organization": networkOrgFromRow(row),
			"detached":             true,
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// List: GET /v1/admin/networks/{id}/{organizers|agents}
// ─────────────────────────────────────────────────────────────────────────────

func (s *Server) handleListNetworkOrganizations(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.networkQueries == nil {
			writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
				"dependency.database_unavailable", "database is not available", r,
			))
			return
		}
		networkID, ok := uuidPathParam(w, r, "id")
		if !ok {
			return
		}

		if !s.assertNetworkExists(w, r, networkID) {
			return
		}

		var (
			rows []gen.NetworkOrganizationRow
			err  error
		)
		switch kind {
		case networkAssignmentKindOrganizer:
			rows, err = s.networkQueries.ListOrganizersByNetwork(r.Context(), networkID)
		case networkAssignmentKindAgent:
			rows, err = s.networkQueries.ListAgentsByNetwork(r.Context(), networkID)
		default:
			// Defensive: the router only mounts these two kinds.
			writeJSON(w, http.StatusInternalServerError, errorEnvelope(
				"network_org.invalid_kind",
				"unsupported assignment kind: "+kind, r))
			return
		}
		if err != nil {
			s.logger.Error("network_org: list failed",
				slog.String("assignment_kind", kind),
				slog.String("error", err.Error()))
			writeJSON(w, http.StatusInternalServerError, errorEnvelope(
				"network_org.list_failed",
				"failed to list network organizations", r))
			return
		}
		out := make([]networkOrgResponse, 0, len(rows))
		for _, row := range rows {
			out = append(out, networkOrgFromRow(row))
		}
		// Response key matches the plural kind (organizers / agents) so the
		// JSON shape reads naturally from the route URL.
		writeJSON(w, http.StatusOK, map[string]any{
			"network_id":      networkID.String(),
			"assignment_kind": kind,
			kind + "s":        out,
			"total":           len(out),
		})
	}
}
