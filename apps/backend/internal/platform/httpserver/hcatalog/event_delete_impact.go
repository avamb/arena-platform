// event_delete_impact.go — the dry run of deleting an event, and the guard the
// delete itself shares with it (EC-10, spec 35 §6.1).
//
//	GET /v1/organizations/{org_id}/events/{event_id}/delete-impact   (event.delete)
//
// An event that has sold anything — a paid, partially refunded or refunded
// order, or any issued ticket — cannot be deleted: its buyers hold valid
// tickets. The only exit is the archive, which hides the event and leaves the
// tickets alone. The dry run says so BEFORE anybody types a confirmation.
package hcatalog

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// eventDeleteBlockedPaidOrders is the `blocked` value of the dry run, and the
// tail of the 409 code, when the event has sales.
const eventDeleteBlockedPaidOrders = "has_paid_orders"

// eventDeleteImpactResponse is the body of the dry run.
type eventDeleteImpactResponse struct {
	EventID    string `json:"event_id"`
	Status     string `json:"status"`
	Sessions   int64  `json:"sessions"`
	PaidOrders int64  `json:"paid_orders"`
	Tickets    int64  `json:"tickets"`
	CanDelete  bool   `json:"can_delete"`
	CanArchive bool   `json:"can_archive"`
	Blocked    string `json:"blocked"`
}

// eventHasSales reports whether the impact forbids deleting the event.
func eventHasSales(imp gen.EventDeleteImpactRow) bool {
	return imp.PaidOrders > 0 || imp.Tickets > 0
}

// writeEventHasPaidOrders answers 409 event.has_paid_orders with the counts the
// caller needs to explain it.
func writeEventHasPaidOrders(w http.ResponseWriter, r *http.Request, imp gen.EventDeleteImpactRow) {
	httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelopeWithDetails(
		"event.has_paid_orders",
		"an event with paid orders cannot be deleted: archive it instead",
		r,
		map[string]any{
			"paid_orders": imp.PaidOrders,
			"tickets":     imp.Tickets,
			"sessions":    imp.Sessions,
		},
	))
}

// HandleEventDeleteImpact answers what deleting the event would touch and
// whether it is allowed. It writes nothing. An event of another organization
// (or a deleted one) is the route's own 404.
func (h *Handler) HandleEventDeleteImpact(w http.ResponseWriter, r *http.Request) {
	if h.eventQueries == nil {
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
	eventID, ok := httputil.UUIDPathParam(w, r, "event_id")
	if !ok {
		return
	}
	if !h.requireOrgMembership(w, r, h.eventQueries, orgID) {
		return
	}

	ev, err := h.eventQueries.GetEventRaw(ctx, eventID)
	if err != nil || ev.OrgID != orgID || ev.DeletedAt != nil {
		if err == nil || errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("event.not_found", "event not found", r))
			return
		}
		h.logger.Error("event: delete impact load failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"event.delete_impact_failed", "failed to compute the delete impact", r,
		))
		return
	}
	imp, err := h.eventQueries.EventDeleteImpact(ctx, eventID)
	if err != nil {
		h.logger.Error("event: delete impact count failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"event.delete_impact_failed", "failed to compute the delete impact", r,
		))
		return
	}
	out := eventDeleteImpactResponse{
		EventID:    eventID.String(),
		Status:     ev.Status,
		Sessions:   imp.Sessions,
		PaidOrders: imp.PaidOrders,
		Tickets:    imp.Tickets,
		CanDelete:  !eventHasSales(imp),
		CanArchive: IsValidEventTransition(ev.Status, "archived"),
	}
	if !out.CanDelete {
		out.Blocked = eventDeleteBlockedPaidOrders
	}
	httputil.WriteJSON(w, http.StatusOK, out)
}
