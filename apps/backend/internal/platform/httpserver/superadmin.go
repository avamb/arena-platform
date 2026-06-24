// superadmin.go implements the platform superadmin console HTTP API (feature #166).
//
// The superadmin console provides read-only cross-tenant views for platform
// operators who need to inspect data across all organizations. Every request
// must carry a mandatory X-Admin-Reason header explaining the business reason
// for the access (audit trail requirement).
//
// Role requirement: platform_superadmin (or admin, which inherits all permissions).
// Permission checked: superadmin.read
//
// Endpoints:
//
//	GET /v1/admin/organizations  — list all organizations (all tenants)
//	GET /v1/admin/orders         — list all checkout sessions (all tenants)
//	GET /v1/admin/tickets        — list all tickets (all tenants)
//	GET /v1/admin/refunds        — list all refunds (all tenants)
//
// Query parameters (all endpoints):
//
//	?org_id=<uuid>   — filter by organization UUID (optional)
//	?limit=<int>     — page size (default 50, max 200)
//	?offset=<int>    — page offset (default 0)
//
// Additional filters:
//
//	GET /v1/admin/orders  — ?state=<state>  (e.g. "completed", "created")
//	GET /v1/admin/tickets — ?status=<status> (e.g. "active", "cancelled")
//	GET /v1/admin/refunds — ?state=<state>  (e.g. "succeeded", "requested")
//
// Every request is recorded in audit_events with:
//
//	action        = "superadmin.<entity>.list"
//	resource_type = "superadmin"
//	resource_id   = "<entity>"
//	metadata      = {"reason": "<X-Admin-Reason>", "filters": {...}}
package httpserver

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
	"github.com/google/uuid"
)

// superadminAdminReasonHeader is the required request header that carries the
// human-readable business reason for the superadmin access (audit trail).
const superadminAdminReasonHeader = "X-Admin-Reason"

// superadminDefaultLimit is the default number of records returned when no
// limit query parameter is specified.
const superadminDefaultLimit = 50

// superadminMaxLimit is the maximum page size accepted by the superadmin endpoints.
const superadminMaxLimit = 200

// ─────────────────────────────────────────────────────────────────────────────
// Shared helpers
// ─────────────────────────────────────────────────────────────────────────────

// parseSuperadminPagination extracts limit and offset from the request query
// parameters. Returns the validated values or writes an error response and
// returns false for ok.
func parseSuperadminPagination(w http.ResponseWriter, r *http.Request) (limit int32, offset int32, ok bool) {
	limit = superadminDefaultLimit
	offset = 0

	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.ParseInt(v, 10, 32)
		if err != nil || n < 1 {
			writeJSON(w, http.StatusBadRequest, errorEnvelope("superadmin.invalid_limit",
				"limit must be a positive integer", r))
			return 0, 0, false
		}
		if n > superadminMaxLimit {
			n = superadminMaxLimit
		}
		limit = int32(n)
	}

	if v := r.URL.Query().Get("offset"); v != "" {
		n, err := strconv.ParseInt(v, 10, 32)
		if err != nil || n < 0 {
			writeJSON(w, http.StatusBadRequest, errorEnvelope("superadmin.invalid_offset",
				"offset must be a non-negative integer", r))
			return 0, 0, false
		}
		offset = int32(n)
	}

	return limit, offset, true
}

// parseSuperadminOrgID parses the optional ?org_id query parameter.
// Returns (nil, true) when absent, (nil, false) with error written when malformed,
// or (&id, true) when present and valid.
func parseSuperadminOrgID(w http.ResponseWriter, r *http.Request) (*uuid.UUID, bool) {
	raw := r.URL.Query().Get("org_id")
	if raw == "" {
		return nil, true
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("superadmin.invalid_org_id",
			"org_id must be a valid UUID", r))
		return nil, false
	}
	return &id, true
}

// requireAdminReason validates the X-Admin-Reason header. Returns the reason
// string on success or writes a 400 error and returns "" on failure.
func requireAdminReason(w http.ResponseWriter, r *http.Request) (string, bool) {
	reason := strings.TrimSpace(r.Header.Get(superadminAdminReasonHeader))
	if reason == "" {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("superadmin.missing_reason",
			"X-Admin-Reason header is required for superadmin operations", r))
		return "", false
	}
	return reason, true
}

// logSuperadminAudit records the superadmin read in audit_events.
// entity is a short label like "organizations", "orders", "tickets", "refunds".
// filters is a map of the query filters applied (for metadata).
// This is a fire-and-forget write — audit failure is logged but does NOT abort
// the response, since the access has already occurred.
func (s *Server) logSuperadminAudit(r *http.Request, entity, reason string, filters map[string]any) {
	if s.audit == nil {
		return
	}
	actorID := ""
	actorType := "anonymous"
	if a, ok := auth.ActorFromContext(r.Context()); ok {
		actorID = a.ID
		actorType = "user"
	}

	ev := audit.Event{
		OccurredAt:   time.Now().UTC(),
		ActorType:    actorType,
		ActorID:      actorID,
		Action:       "superadmin." + entity + ".list",
		ResourceType: "superadmin",
		ResourceID:   entity,
		RequestID:    logging.RequestID(r.Context()),
		TraceID:      logging.TraceID(r.Context()),
		IP:           extractClientIP(r),
		Metadata: map[string]any{
			"reason":  reason,
			"filters": filters,
		},
	}

	if err := s.audit.Write(r.Context(), ev); err != nil {
		s.logger.Warn("superadmin: audit write failed",
			slog.String("entity", entity),
			slog.Any("error", err),
		)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/admin/organizations — list all organizations (cross-tenant)
// ─────────────────────────────────────────────────────────────────────────────

func (s *Server) handleSuperadminListOrganizations(w http.ResponseWriter, r *http.Request) {
	if s.superadminQueries == nil {
		writeJSON(w, http.StatusServiceUnavailable,
			errorEnvelope("superadmin.unavailable", "superadmin console not configured", r))
		return
	}

	reason, ok := requireAdminReason(w, r)
	if !ok {
		return
	}

	rows, err := s.superadminQueries.ListOrganizations(r.Context())
	if err != nil {
		s.logger.Error("superadmin: list organizations failed", slog.Any("error", err))
		writeJSON(w, http.StatusInternalServerError,
			errorEnvelope("superadmin.internal", "failed to list organizations", r))
		return
	}

	// Audit log this cross-tenant read.
	s.logSuperadminAudit(r, "organizations", reason, map[string]any{})

	orgs := make([]map[string]any, 0, len(rows))
	for _, o := range rows {
		m := map[string]any{
			"id":                      o.ID.String(),
			"name":                    o.Name,
			"slug":                    o.Slug,
			"country":                 o.Country,
			"default_locale":          o.DefaultLocale,
			"reservation_ttl_seconds": o.ReservationTTLSeconds,
			"created_at":              o.CreatedAt.Format(time.RFC3339),
			"updated_at":              o.UpdatedAt.Format(time.RFC3339),
		}
		if o.DeletedAt != nil {
			m["deleted_at"] = o.DeletedAt.Format(time.RFC3339)
		} else {
			m["deleted_at"] = nil
		}
		orgs = append(orgs, m)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"organizations": orgs,
		"total":         len(orgs),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/admin/orders — list all checkout sessions (cross-tenant)
// ─────────────────────────────────────────────────────────────────────────────

func (s *Server) handleSuperadminListOrders(w http.ResponseWriter, r *http.Request) {
	if s.superadminQueries == nil {
		writeJSON(w, http.StatusServiceUnavailable,
			errorEnvelope("superadmin.unavailable", "superadmin console not configured", r))
		return
	}

	reason, ok := requireAdminReason(w, r)
	if !ok {
		return
	}

	orgID, ok := parseSuperadminOrgID(w, r)
	if !ok {
		return
	}

	limit, offset, ok := parseSuperadminPagination(w, r)
	if !ok {
		return
	}

	var stateFilter *string
	if v := strings.TrimSpace(r.URL.Query().Get("state")); v != "" {
		stateFilter = &v
	}

	rows, err := s.superadminQueries.ListAllCheckoutSessions(r.Context(), orgID, stateFilter, limit, offset)
	if err != nil {
		s.logger.Error("superadmin: list orders failed", slog.Any("error", err))
		writeJSON(w, http.StatusInternalServerError,
			errorEnvelope("superadmin.internal", "failed to list orders", r))
		return
	}

	filters := map[string]any{"limit": limit, "offset": offset}
	if orgID != nil {
		filters["org_id"] = orgID.String()
	}
	if stateFilter != nil {
		filters["state"] = *stateFilter
	}
	s.logSuperadminAudit(r, "orders", reason, filters)

	orders := make([]map[string]any, 0, len(rows))
	for _, cs := range rows {
		m := map[string]any{
			"id":             cs.ID.String(),
			"org_id":         cs.OrgID.String(),
			"channel_id":     cs.ChannelID.String(),
			"reservation_id": cs.ReservationID.String(),
			"state":          cs.State,
			"created_at":     cs.CreatedAt.Format(time.RFC3339),
			"updated_at":     cs.UpdatedAt.Format(time.RFC3339),
		}
		if cs.UserID != nil {
			m["user_id"] = cs.UserID.String()
		} else {
			m["user_id"] = nil
		}
		if cs.Total != nil {
			m["total"] = *cs.Total
		} else {
			m["total"] = nil
		}
		if cs.Currency != nil {
			m["currency"] = *cs.Currency
		} else {
			m["currency"] = nil
		}
		if cs.CompletedAt != nil {
			m["completed_at"] = cs.CompletedAt.Format(time.RFC3339)
		} else {
			m["completed_at"] = nil
		}
		orders = append(orders, m)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"orders": orders,
		"total":  len(orders),
		"limit":  limit,
		"offset": offset,
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/admin/tickets — list all tickets (cross-tenant)
// ─────────────────────────────────────────────────────────────────────────────

func (s *Server) handleSuperadminListTickets(w http.ResponseWriter, r *http.Request) {
	if s.superadminQueries == nil {
		writeJSON(w, http.StatusServiceUnavailable,
			errorEnvelope("superadmin.unavailable", "superadmin console not configured", r))
		return
	}

	reason, ok := requireAdminReason(w, r)
	if !ok {
		return
	}

	orgID, ok := parseSuperadminOrgID(w, r)
	if !ok {
		return
	}

	limit, offset, ok := parseSuperadminPagination(w, r)
	if !ok {
		return
	}

	var statusFilter *string
	if v := strings.TrimSpace(r.URL.Query().Get("status")); v != "" {
		statusFilter = &v
	}

	rows, err := s.superadminQueries.ListAllTickets(r.Context(), orgID, statusFilter, limit, offset)
	if err != nil {
		s.logger.Error("superadmin: list tickets failed", slog.Any("error", err))
		writeJSON(w, http.StatusInternalServerError,
			errorEnvelope("superadmin.internal", "failed to list tickets", r))
		return
	}

	filters := map[string]any{"limit": limit, "offset": offset}
	if orgID != nil {
		filters["org_id"] = orgID.String()
	}
	if statusFilter != nil {
		filters["status"] = *statusFilter
	}
	s.logSuperadminAudit(r, "tickets", reason, filters)

	tickets := make([]map[string]any, 0, len(rows))
	for _, t := range rows {
		m := map[string]any{
			"id":                  t.ID.String(),
			"checkout_session_id": t.CheckoutSessionID.String(),
			"session_id":          t.SessionID.String(),
			"status":              t.Status,
			"issued_at":           t.IssuedAt.Format(time.RFC3339),
			"created_at":          t.CreatedAt.Format(time.RFC3339),
			"updated_at":          t.UpdatedAt.Format(time.RFC3339),
		}
		if t.TierID != nil {
			m["tier_id"] = t.TierID.String()
		} else {
			m["tier_id"] = nil
		}
		if t.HolderEmail != nil {
			m["holder_email"] = *t.HolderEmail
		} else {
			m["holder_email"] = nil
		}
		tickets = append(tickets, m)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"tickets": tickets,
		"total":   len(tickets),
		"limit":   limit,
		"offset":  offset,
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/admin/refunds — list all refunds (cross-tenant)
// ─────────────────────────────────────────────────────────────────────────────

func (s *Server) handleSuperadminListRefunds(w http.ResponseWriter, r *http.Request) {
	if s.superadminQueries == nil {
		writeJSON(w, http.StatusServiceUnavailable,
			errorEnvelope("superadmin.unavailable", "superadmin console not configured", r))
		return
	}

	reason, ok := requireAdminReason(w, r)
	if !ok {
		return
	}

	orgID, ok := parseSuperadminOrgID(w, r)
	if !ok {
		return
	}

	limit, offset, ok := parseSuperadminPagination(w, r)
	if !ok {
		return
	}

	var stateFilter *string
	if v := strings.TrimSpace(r.URL.Query().Get("state")); v != "" {
		stateFilter = &v
	}

	rows, err := s.superadminQueries.ListAllRefunds(r.Context(), orgID, stateFilter, limit, offset)
	if err != nil {
		s.logger.Error("superadmin: list refunds failed", slog.Any("error", err))
		writeJSON(w, http.StatusInternalServerError,
			errorEnvelope("superadmin.internal", "failed to list refunds", r))
		return
	}

	filters := map[string]any{"limit": limit, "offset": offset}
	if orgID != nil {
		filters["org_id"] = orgID.String()
	}
	if stateFilter != nil {
		filters["state"] = *stateFilter
	}
	s.logSuperadminAudit(r, "refunds", reason, filters)

	refunds := make([]map[string]any, 0, len(rows))
	for _, rf := range rows {
		m := map[string]any{
			"id":                rf.ID.String(),
			"payment_intent_id": rf.PaymentIntentID.String(),
			"org_id":            rf.OrgID.String(),
			"amount":            rf.Amount,
			"currency":          rf.Currency,
			"state":             rf.State,
			"requested_at":      rf.RequestedAt.Format(time.RFC3339),
			"created_at":        rf.CreatedAt.Format(time.RFC3339),
			"updated_at":        rf.UpdatedAt.Format(time.RFC3339),
		}
		if rf.Reason != nil {
			m["reason"] = *rf.Reason
		} else {
			m["reason"] = nil
		}
		if rf.RequestedBy != nil {
			m["requested_by"] = *rf.RequestedBy
		} else {
			m["requested_by"] = nil
		}
		if rf.ProviderRefundID != nil {
			m["provider_refund_id"] = *rf.ProviderRefundID
		} else {
			m["provider_refund_id"] = nil
		}
		if rf.ApprovedAt != nil {
			m["approved_at"] = rf.ApprovedAt.Format(time.RFC3339)
		} else {
			m["approved_at"] = nil
		}
		if rf.SucceededAt != nil {
			m["succeeded_at"] = rf.SucceededAt.Format(time.RFC3339)
		} else {
			m["succeeded_at"] = nil
		}
		refunds = append(refunds, m)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"refunds": refunds,
		"total":   len(refunds),
		"limit":   limit,
		"offset":  offset,
	})
}
