// admin_ticket_scans.go — support-console scan-events read endpoint
// (feature #295, S-4).
//
// Surfaces the rows persisted into scan_events (see 0055_scan_events.sql,
// feature #293 S-2) for a single ticket so the support drawer in the
// admin web app can render a read-only "Scans" panel.  No mutations are
// exposed: scan_events is append-only ingest from the external scanner
// callback and the support console only inspects history.
//
//	GET /v1/admin/tickets/{id}/scans            — list scan attempts
//
// Lives under /v1/admin so it shares the existing X-Admin-Reason +
// audit-log + cross-tenant superadmin gate already used by
// GET /v1/admin/tickets and the companion /v1/admin/tickets/{id}/delivery
// surface.  RBAC: scan_event.read (seeded for admin / org_admin / support
// in 0055_scan_events.sql).
package htickets

import (
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

// AdminTicketScansDefaultLimit is the page size used when the client does
// not specify ?limit. Matches the support console's "show last 50 scans"
// default and keeps the response body bounded for pathological replays.
const AdminTicketScansDefaultLimit = 50

// AdminTicketScansMaxLimit caps the user-overridable ?limit query parameter.
// A higher cap is allowed than the default so an operator investigating an
// abusive scanner can pull the full history without paging.
const AdminTicketScansMaxLimit = 500

// HandleAdminListTicketScanEvents returns the scan_events rows that match
// the given ticket id, newest scan first.  Each row exposes gate, device_id
// and result (the columns the spec calls out) plus scanned_at, the canonical
// row id, and received_at so the UI can tell scanner-clock from server-clock.
//
// Requires JWT + scan_event.read + X-Admin-Reason (mounted via applyAuth).
//
// Pagination: ?limit (default 50, capped at 500) only. The drawer renders
// a top-N list, not an infinitely scrolling table, so we deliberately do
// not surface an offset cursor here — operators wanting older history can
// raise the limit.
func (h *Handler) HandleAdminListTicketScanEvents(w http.ResponseWriter, r *http.Request) {
	if h.feedTokenQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "scan_events store is not available", r,
		))
		return
	}
	reason, ok := httputil.RequireAdminReason(w, r)
	if !ok {
		return
	}
	ticketID, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}

	limit := AdminTicketScansDefaultLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, parseErr := strconv.Atoi(raw)
		if parseErr != nil || parsed <= 0 {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
				"scan_events.invalid_limit", "limit must be a positive integer", r,
			))
			return
		}
		if parsed > AdminTicketScansMaxLimit {
			parsed = AdminTicketScansMaxLimit
		}
		limit = parsed
	}

	rows, err := h.feedTokenQueries.ListScanEventsByTicketID(r.Context(), ticketID, int32(limit))
	if err != nil {
		h.logger.Error("admin_ticket_scans: list query failed",
			slog.String("ticket_id", ticketID.String()),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"scan_events.internal", "failed to load scan events", r,
		))
		return
	}

	out := make([]map[string]any, 0, len(rows))
	for _, sc := range rows {
		out = append(out, ScanEventToMap(sc))
	}

	h.logTicketScansAudit(r, ticketID.String(), reason, len(out), limit)

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"scans": out,
		"total": len(out),
		"limit": limit,
	})
}

// ScanEventToMap renders a ScanEventRow as the JSON object expected by the
// admin UI.  Nullable FKs surface as explicit JSON nulls so the frontend
// can render an em-dash without type narrowing on optional fields.
func ScanEventToMap(sc gen.ScanEventRow) map[string]any {
	m := map[string]any{
		"id":              sc.ID.String(),
		"org_id":          sc.OrgID.String(),
		"credential_code": sc.CredentialCode,
		"scanned_at":      sc.ScannedAt.UTC().Format(time.RFC3339),
		"received_at":     sc.ReceivedAt.UTC().Format(time.RFC3339),
		"gate":            sc.Gate,
		"device_id":       sc.DeviceID,
		"result":          sc.Result,
	}
	if sc.EventID != nil {
		m["event_id"] = sc.EventID.String()
	} else {
		m["event_id"] = nil
	}
	if sc.SessionID != nil {
		m["session_id"] = sc.SessionID.String()
	} else {
		m["session_id"] = nil
	}
	if sc.TicketID != nil {
		m["ticket_id"] = sc.TicketID.String()
	} else {
		m["ticket_id"] = nil
	}
	return m
}

// logTicketScansAudit emits a fire-and-forget audit event for the read.
// Mirrors the shape used by logTicketDeliveryAudit so reviewers grepping
// for "v1.admin.ticket." find every drawer-side admin action in one go.
func (h *Handler) logTicketScansAudit(
	r *http.Request,
	ticketID, reason string,
	returned, limit int,
) {
	if h.audit == nil {
		return
	}
	actor, _ := auth.ActorFromContext(r.Context())
	ev := audit.Event{
		OccurredAt:   time.Now().UTC(),
		ActorType:    "user",
		ActorID:      actor.ID,
		Action:       "v1.admin.ticket.scans.read",
		ResourceType: "ticket",
		ResourceID:   ticketID,
		RequestID:    logging.RequestID(r.Context()),
		TraceID:      logging.TraceID(r.Context()),
		IP:           httputil.ExtractClientIP(r),
		Metadata: map[string]any{
			"reason":   reason,
			"returned": returned,
			"limit":    limit,
		},
	}
	if err := h.audit.Write(r.Context(), ev); err != nil {
		h.logger.Warn("admin_ticket_scans: audit write failed",
			slog.String("action", ev.Action),
			slog.Any("error", err),
		)
	}
}
