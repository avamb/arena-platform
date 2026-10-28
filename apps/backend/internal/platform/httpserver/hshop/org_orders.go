package hshop

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/refunds"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/shop"
)

// parseTimeParam reads an RFC 3339 timestamp or a YYYY-MM-DD date (UTC
// midnight) query parameter.
func parseTimeParam(raw string) (*time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, true
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return &t, true
	}
	if t, err := time.Parse(time.DateOnly, raw); err == nil {
		return &t, true
	}
	return nil, false
}

// HandleListOrders serves GET .../shop/orders?status=&q=&from=&to=&limit=&offset=.
func (h *Handler) HandleListOrders(w http.ResponseWriter, r *http.Request) {
	org, ok := orgID(w, r)
	if !ok {
		return
	}
	qs := r.URL.Query()
	f := shop.OrderFilter{Status: qs.Get("status"), Query: qs.Get("q")}
	var okFrom, okTo bool
	f.From, okFrom = parseTimeParam(qs.Get("from"))
	f.To, okTo = parseTimeParam(qs.Get("to"))
	if !okFrom || !okTo {
		writeErr(w, r, http.StatusBadRequest, "shop.invalid_filter", "from and to must be RFC 3339 timestamps or YYYY-MM-DD dates")
		return
	}
	for name, dst := range map[string]*int{"limit": &f.Limit, "offset": &f.Offset} {
		if v := strings.TrimSpace(qs.Get(name)); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				writeErr(w, r, http.StatusBadRequest, "shop.invalid_filter", name+" must be a whole number")
				return
			}
			*dst = n
		}
	}
	if f.Limit == 0 {
		f.Limit = 50
	}
	orders, err := h.o.Service.ListOrders(r.Context(), org, f)
	if err != nil {
		h.fail(w, r, "list orders", err)
		return
	}
	out := make([]orderJSON, 0, len(orders))
	for _, o := range orders {
		out = append(out, toOrderJSON(o))
	}
	var next *int
	if len(orders) == f.Limit {
		n := f.Offset + f.Limit
		next = &n
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"orders": out, "next_offset": next})
}

// HandleGetOrder serves GET .../shop/orders/{order_id}.
func (h *Handler) HandleGetOrder(w http.ResponseWriter, r *http.Request) {
	org, ok := orgID(w, r)
	if !ok {
		return
	}
	id, ok := pathID(w, r, "order_id", shop.ErrOrderNotFound)
	if !ok {
		return
	}
	o, err := h.o.Service.GetOrder(r.Context(), org, id)
	if err != nil {
		h.fail(w, r, "read order", err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, toOrderJSON(o))
}

// HandleShipOrder serves POST .../shop/orders/{order_id}/ship.
func (h *Handler) HandleShipOrder(w http.ResponseWriter, r *http.Request) {
	org, ok := orgID(w, r)
	if !ok {
		return
	}
	id, ok := pathID(w, r, "order_id", shop.ErrOrderNotFound)
	if !ok {
		return
	}
	var b struct {
		TrackingNote string `json:"tracking_note"`
	}
	if !readBody(w, r, &b, true) {
		return
	}
	o, err := h.o.Service.Ship(r.Context(), org, id, b.TrackingNote)
	if err != nil {
		h.fail(w, r, "ship order", err)
		return
	}
	h.writeAudit(r, "v1.shop.order.ship", "shop_order", id.String(), org, map[string]any{"number": o.Number})
	httputil.WriteJSON(w, http.StatusOK, toOrderJSON(o))
}

// HandleCancelOrder serves POST .../shop/orders/{order_id}/cancel.
func (h *Handler) HandleCancelOrder(w http.ResponseWriter, r *http.Request) {
	org, ok := orgID(w, r)
	if !ok {
		return
	}
	id, ok := pathID(w, r, "order_id", shop.ErrOrderNotFound)
	if !ok {
		return
	}
	o, err := h.o.Service.Cancel(r.Context(), org, id)
	if err != nil {
		h.fail(w, r, "cancel order", err)
		return
	}
	h.writeAudit(r, "v1.shop.order.cancel", "shop_order", id.String(), org, map[string]any{"number": o.Number})
	httputil.WriteJSON(w, http.StatusOK, toOrderJSON(o))
}

// refundJSON is the refund a refund request created.
type refundJSON struct {
	ID               string  `json:"id"`
	State            string  `json:"state"`
	Amount           int64   `json:"amount"`
	Currency         string  `json:"currency"`
	ProviderRefundID *string `json:"provider_refund_id"`
	FailureCode      *string `json:"failure_code"`
	FailureReason    *string `json:"failure_reason"`
}

func toRefundJSON(r refunds.Refund) refundJSON {
	return refundJSON{ID: r.ID.String(), State: r.State, Amount: r.Amount, Currency: r.Currency,
		ProviderRefundID: r.ProviderRefundID, FailureCode: r.FailureCode, FailureReason: r.FailureReason}
}

// HandleRefundOrder serves POST .../shop/orders/{order_id}/refund
// {amount_minor?, reason?}. The answer carries the order after the refund and
// the refund's own state: succeeded, provider_pending (the provider has not
// answered yet; refund.sweep follows it up), failed or manual_review.
func (h *Handler) HandleRefundOrder(w http.ResponseWriter, r *http.Request) {
	org, ok := orgID(w, r)
	if !ok {
		return
	}
	id, ok := pathID(w, r, "order_id", shop.ErrOrderNotFound)
	if !ok {
		return
	}
	var b struct {
		AmountMinor *int64 `json:"amount_minor"`
		Reason      string `json:"reason"`
	}
	if !readBody(w, r, &b, true) {
		return
	}
	res, err := h.o.Service.Refund(r.Context(), shop.RefundInput{
		OrgID: org, OrderID: id, Amount: b.AmountMinor, Reason: b.Reason, RequestedBy: actorLabel(r.Context()),
	})
	if err != nil {
		h.fail(w, r, "refund order", err)
		return
	}
	h.writeAudit(r, "v1.shop.order.refund", "shop_order", id.String(), org, map[string]any{
		"number": res.Order.Number, "refund_id": res.Refund.ID.String(), "amount": res.Refund.Amount,
		"refund_state": res.Refund.State,
	})
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"order": toOrderJSON(res.Order), "refund": toRefundJSON(res.Refund)})
}

// HandleCourierText serves GET .../shop/orders/{order_id}/courier-text: the
// delivery details one per line, as text/plain, ready to paste.
func (h *Handler) HandleCourierText(w http.ResponseWriter, r *http.Request) {
	org, ok := orgID(w, r)
	if !ok {
		return
	}
	id, ok := pathID(w, r, "order_id", shop.ErrOrderNotFound)
	if !ok {
		return
	}
	text, err := h.o.Service.CourierTextFor(r.Context(), org, id)
	if err != nil {
		h.fail(w, r, "courier text", err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(text)) //nolint:gosec // G705 false positive: text/plain with nosniff, never rendered as HTML
}
