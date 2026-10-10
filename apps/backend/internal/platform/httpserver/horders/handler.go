// Package horders implements the org-scoped order read/cancel surface
// (W1-A6d, feature #489, spec §14.2):
//
//	GET  /v1/organizations/{org_id}/orders             — search / list
//	GET  /v1/organizations/{org_id}/orders/{id}         — detail (items/events/tickets)
//	POST /v1/organizations/{org_id}/orders/{id}/cancel  — cancel a pending order
//
// List/Get require the `order.read` permission; Cancel requires `order.write`.
// Every route is additionally scoped via GetOrderByID's own org_id predicate,
// so an id belonging to another organization is indistinguishable from a
// missing one (404), matching the tenant-isolation rule used across the rest
// of the org-scoped surface (hcustomers, hcatalog, ...).
package horders

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/ordering"
)

const (
	defaultLimit = 50
	maxLimit     = 200
)

// TxStarter is the narrow transaction-starting surface HandleCancel needs to
// release the order's hold via hcheckout.ReleaseHold.
type TxStarter interface {
	BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error)
}

// Handler holds the shared dependencies for the orders read/cancel handlers.
type Handler struct {
	queries *gen.Queries
	pool    TxStarter
	logger  *slog.Logger
}

// New constructs a Handler. A nil queries/pool is allowed; handlers self-gate
// with a 503 dependency.database_unavailable envelope.
func New(queries *gen.Queries, pool TxStarter, logger *slog.Logger) *Handler {
	return &Handler{queries: queries, pool: pool, logger: logger}
}

// orderSummary is the shape returned for one order by both the list and
// detail endpoints.
func orderSummary(o gen.OrderRow) map[string]any {
	m := map[string]any{
		"id":                  o.ID.String(),
		"system_id":           o.SystemID,
		"org_id":              o.OrgID.String(),
		"channel_id":          o.ChannelID.String(),
		"event_id":            o.EventID.String(),
		"session_id":          o.SessionID.String(),
		"checkout_session_id": o.CheckoutSessionID.String(),
		"reservation_id":      o.ReservationID.String(),
		"source":              o.Source,
		"status":              o.Status,
		"currency":            o.Currency,
		"subtotal":            o.Subtotal,
		"discount":            o.Discount,
		"charge":              o.Charge,
		"total":               o.Total,
		"charge_percent_bp":   o.ChargePercentBP,
		"buyer_name":          o.BuyerName,
		"buyer_email":         o.BuyerEmail,
		"buyer_phone":         o.BuyerPhone,
		"payment_method":      o.PaymentMethod,
		"external_ref":        o.ExternalRef,
		"created_at":          o.CreatedAt.Format(time.RFC3339),
		"updated_at":          o.UpdatedAt.Format(time.RFC3339),
	}
	if o.CustomerID != nil {
		m["customer_id"] = o.CustomerID.String()
	} else {
		m["customer_id"] = nil
	}
	if o.PromoCodeID != nil {
		m["promo_code_id"] = o.PromoCodeID.String()
	} else {
		m["promo_code_id"] = nil
	}
	if o.PaidAt != nil {
		m["paid_at"] = o.PaidAt.Format(time.RFC3339)
	} else {
		m["paid_at"] = nil
	}
	if o.CancelledAt != nil {
		m["cancelled_at"] = o.CancelledAt.Format(time.RFC3339)
	} else {
		m["cancelled_at"] = nil
	}
	if o.ExpiresAt != nil {
		m["expires_at"] = o.ExpiresAt.Format(time.RFC3339)
	} else {
		m["expires_at"] = nil
	}
	return m
}

// HandleList serves GET /v1/organizations/{org_id}/orders (EC-04, spec 35
// §5.5). Filters: q (one value classified into barcode / order number /
// e-mail / phone / name similarity — see classifyQuery), tab (recent |
// paid | unpaid — see tabFilter), status, session_id, event_id, from/to
// (RFC3339 created_at range), plus limit/offset paging with total_count.
func (h *Handler) HandleList(w http.ResponseWriter, r *http.Request) {
	if h.queries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable,
			httputil.ErrorEnvelope("dependency.database_unavailable", "orders store not configured", r))
		return
	}
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	limit, offset, ok := parsePagination(w, r)
	if !ok {
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(q) > 200 {
		httputil.WriteJSON(w, http.StatusBadRequest,
			httputil.ErrorEnvelope("orders.invalid_query", "q must be at most 200 characters", r))
		return
	}
	tab, ok := tabFilter(r.URL.Query().Get("tab"))
	if !ok {
		httputil.WriteJSON(w, http.StatusBadRequest,
			httputil.ErrorEnvelope("orders.invalid_tab", "tab must be recent, paid or unpaid", r))
		return
	}
	params := gen.OrderSearchParams{
		OrgID:  orgID,
		Status: strings.TrimSpace(r.URL.Query().Get("status")),
		Tab:    tab,
	}
	classifyQuery(q).apply(&params)

	params.SessionID, ok = h.scopedIDParam(w, r, orgID, "session_id", h.orgOwnsSession)
	if !ok {
		return
	}
	params.EventID, ok = h.scopedIDParam(w, r, orgID, "event_id", h.orgOwnsEvent)
	if !ok {
		return
	}
	if params.From, ok = parseTimeParam(w, r, "from"); !ok {
		return
	}
	if params.To, ok = parseTimeParam(w, r, "to"); !ok {
		return
	}

	rows, err := h.queries.SearchOrdersByOrg(r.Context(), params, limit, offset)
	if err != nil {
		h.logger.Error("horders: list failed", slog.Any("error", err))
		httputil.WriteJSON(w, http.StatusInternalServerError,
			httputil.ErrorEnvelope("orders.internal", "failed to list orders", r))
		return
	}
	total, err := h.queries.CountOrdersByOrg(r.Context(), params)
	if err != nil {
		h.logger.Error("horders: count failed", slog.Any("error", err))
		httputil.WriteJSON(w, http.StatusInternalServerError,
			httputil.ErrorEnvelope("orders.internal", "failed to count orders", r))
		return
	}

	out := make([]map[string]any, 0, len(rows))
	for _, o := range rows {
		m := orderSummary(o.OrderRow)
		m["event_name"] = o.EventName
		m["session_start_at"] = o.SessionStartAt.UTC().Format(time.RFC3339)
		m["session_timezone"] = o.SessionTimezone
		out = append(out, m)
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"orders":      out,
		"limit":       limit,
		"offset":      offset,
		"total_count": total,
		"has_more":    int64(offset)+int64(len(out)) < total,
	})
}

// HandleGet serves GET /v1/organizations/{org_id}/orders/{id} — order detail
// including its line items, audit-trail events, issued tickets (with
// category, price, EAN-13 and entry time), per-ticket delivery, the payment
// behind the order, the unpaid reason and the sales channel (EC-05).
func (h *Handler) HandleGet(w http.ResponseWriter, r *http.Request) {
	if h.queries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable,
			httputil.ErrorEnvelope("dependency.database_unavailable", "orders store not configured", r))
		return
	}
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	orderID, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}

	// GetOrderByID is org-scoped: an id belonging to another org comes back
	// as pgx.ErrNoRows, indistinguishable from a truly missing order.
	order, err := h.queries.GetOrderByID(r.Context(), orderID, orgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound,
				httputil.ErrorEnvelope("orders.not_found", "order not found", r))
			return
		}
		h.logger.Error("horders: get failed", slog.Any("error", err))
		httputil.WriteJSON(w, http.StatusInternalServerError,
			httputil.ErrorEnvelope("orders.internal", "failed to load order", r))
		return
	}

	itemRows, err := h.queries.ListOrderItemsByOrder(r.Context(), orderID)
	if err != nil {
		h.logger.Error("horders: list items failed", slog.Any("error", err))
		httputil.WriteJSON(w, http.StatusInternalServerError,
			httputil.ErrorEnvelope("orders.internal", "failed to load order items", r))
		return
	}

	items := make([]map[string]any, 0, len(itemRows))
	for _, it := range itemRows {
		item := map[string]any{
			"id":         it.ID.String(),
			"ordinal":    it.Ordinal,
			"kind":       it.Kind,
			"tier_id":    it.TierID.String(),
			"unit_price": it.UnitPrice,
			"discount":   it.Discount,
			"charge":     it.Charge,
			"total":      it.Total,
		}
		if it.SessionSeatID != nil {
			item["session_seat_id"] = it.SessionSeatID.String()
		} else {
			item["session_seat_id"] = nil
		}
		if it.TicketID != nil {
			item["ticket_id"] = it.TicketID.String()
		} else {
			item["ticket_id"] = nil
		}
		items = append(items, item)
	}

	// Tickets, delivery, payment and channel (EC-05) — see detail.go.
	extras, err := h.loadDetailExtras(r.Context(), order)
	if err != nil {
		h.logger.Error("horders: load tickets failed", slog.Any("error", err))
		httputil.WriteJSON(w, http.StatusInternalServerError,
			httputil.ErrorEnvelope("orders.internal", "failed to load order tickets", r))
		return
	}

	eventRows, err := h.queries.ListOrderEventsByOrder(r.Context(), orderID)
	if err != nil {
		h.logger.Error("horders: list events failed", slog.Any("error", err))
		httputil.WriteJSON(w, http.StatusInternalServerError,
			httputil.ErrorEnvelope("orders.internal", "failed to load order events", r))
		return
	}
	events := make([]map[string]any, 0, len(eventRows))
	for _, e := range eventRows {
		events = append(events, map[string]any{
			"id":         e.ID.String(),
			"type":       e.Type,
			"actor":      e.Actor,
			"payload":    json.RawMessage(e.Payload),
			"created_at": e.CreatedAt.Format(time.RFC3339),
		})
	}

	resp := orderSummary(order)
	resp["items"] = items
	resp["tickets"] = extras.tickets
	resp["events"] = events
	resp["delivery"] = extras.delivery
	resp["delivery_state"] = extras.state
	resp["payment"] = extras.payment
	resp["unpaid_reason"] = extras.reason
	resp["channel"] = extras.channel
	httputil.WriteJSON(w, http.StatusOK, resp)
}

// HandleCancel serves POST /v1/organizations/{org_id}/orders/{id}/cancel.
// Only a pending_payment order can be cancelled (ordering.Cancel's guard);
// anything else answers 409 orders.invalid_transition. A successful
// cancellation also releases the order's hold via hcheckout.ReleaseHold —
// ordering.Cancel only flips the order's own status/audit-trail, it never
// touches reservations (spec §14.1).
func (h *Handler) HandleCancel(w http.ResponseWriter, r *http.Request) {
	if h.queries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable,
			httputil.ErrorEnvelope("dependency.database_unavailable", "orders store not configured", r))
		return
	}
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	orderID, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}

	order, err := h.queries.GetOrderByID(r.Context(), orderID, orgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound,
				httputil.ErrorEnvelope("orders.not_found", "order not found", r))
			return
		}
		h.logger.Error("horders: cancel lookup failed", slog.Any("error", err))
		httputil.WriteJSON(w, http.StatusInternalServerError,
			httputil.ErrorEnvelope("orders.internal", "failed to load order", r))
		return
	}

	actorLabel := ordering.ActorSystem
	if a, ok := auth.ActorFromContext(r.Context()); ok && a.ID != "" {
		actorLabel = "user:" + a.ID
	}

	var body struct {
		Reason string `json:"reason"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}

	updated, err := ordering.Cancel(r.Context(), h.queries, ordering.CancelInput{
		OrderID: orderID,
		OrgID:   orgID,
		Actor:   actorLabel,
		Reason:  strings.TrimSpace(body.Reason),
	})
	if err != nil {
		if errors.Is(err, ordering.ErrInvalidTransition) {
			httputil.WriteJSON(w, http.StatusConflict,
				httputil.ErrorEnvelope("orders.invalid_transition", "order cannot be cancelled from its current status", r))
			return
		}
		h.logger.Error("horders: cancel failed", slog.Any("error", err))
		httputil.WriteJSON(w, http.StatusInternalServerError,
			httputil.ErrorEnvelope("orders.internal", "failed to cancel order", r))
		return
	}

	// A hold already released by a racing expiry/cancel is not an error here:
	// the order transition above already succeeded and is what the caller asked
	// for. Only an unexpected failure gets logged.
	if _, relErr := hcheckout.ReleaseHold(r.Context(), h.pool, h.queries, order.ReservationID); relErr != nil {
		var notReleasable *hcheckout.NotReleasableError
		if !errors.As(relErr, &notReleasable) && !errors.Is(relErr, hcheckout.ErrHoldNotFound) {
			h.logger.Error("horders: release hold failed", slog.Any("error", relErr), slog.String("order_id", orderID.String()))
		}
	}

	httputil.WriteJSON(w, http.StatusOK, orderSummary(updated))
}
