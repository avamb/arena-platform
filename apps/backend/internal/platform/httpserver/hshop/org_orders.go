package hshop

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/refunds"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/salesnotify"
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

// listFilter is the filter of the order list as the caller spelled it: the
// query string of GET .../shop/orders, or the body of POST .../shop/orders/search.
type listFilter struct {
	Status, View, Q, From, To string
	Limit, Offset             *int
}

// toFilter validates it into a shop.OrderFilter.
func (l listFilter) toFilter() (shop.OrderFilter, bool) {
	f := shop.OrderFilter{Status: l.Status, View: l.View, Query: l.Q}
	var okFrom, okTo bool
	f.From, okFrom = parseTimeParam(l.From)
	f.To, okTo = parseTimeParam(l.To)
	if !okFrom || !okTo {
		return f, false
	}
	if l.Limit != nil {
		f.Limit = *l.Limit
	}
	if l.Offset != nil {
		f.Offset = *l.Offset
	}
	if f.Limit == 0 {
		f.Limit = 50
	}
	return f, true
}

// HandleListOrders serves GET .../shop/orders?status=&view=&q=&from=&to=&limit=&offset=.
// The buyer's name or e-mail in `q` would land in the access logs of the
// proxy and of Cloudflare, so the web event center searches through
// POST .../shop/orders/search (HandleSearchOrders) with the same filters in
// the body; GET stays for API keys and scripts without a search text.
func (h *Handler) HandleListOrders(w http.ResponseWriter, r *http.Request) {
	org, ok := orgID(w, r)
	if !ok {
		return
	}
	qs := r.URL.Query()
	l := listFilter{Status: qs.Get("status"), View: qs.Get("view"), Q: qs.Get("q"), From: qs.Get("from"), To: qs.Get("to")}
	for name, dst := range map[string]**int{"limit": &l.Limit, "offset": &l.Offset} {
		if v := strings.TrimSpace(qs.Get(name)); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				writeErr(w, r, http.StatusBadRequest, "shop.invalid_filter", name+" must be a whole number")
				return
			}
			*dst = &n
		}
	}
	h.writeOrderPage(w, r, org, l)
}

// HandleSearchOrders serves POST .../shop/orders/search with the filters of
// the list in a JSON body: {q, view, status, from, to, limit, offset}. from
// is the first moment and to the first moment NOT included, each an RFC 3339
// time with a zone (the web sends the shop's day boundaries) or a
// YYYY-MM-DD date (UTC midnight). The answer is the list's.
func (h *Handler) HandleSearchOrders(w http.ResponseWriter, r *http.Request) {
	org, ok := orgID(w, r)
	if !ok {
		return
	}
	var b struct {
		Q      string `json:"q"`
		View   string `json:"view"`
		Status string `json:"status"`
		From   string `json:"from"`
		To     string `json:"to"`
		Limit  *int   `json:"limit"`
		Offset *int   `json:"offset"`
	}
	if !readBody(w, r, &b, true) {
		return
	}
	h.writeOrderPage(w, r, org, listFilter{Status: b.Status, View: b.View, Q: b.Q, From: b.From, To: b.To, Limit: b.Limit, Offset: b.Offset})
}

func (h *Handler) writeOrderPage(w http.ResponseWriter, r *http.Request, org uuid.UUID, l listFilter) {
	f, ok := l.toFilter()
	if !ok {
		writeErr(w, r, http.StatusBadRequest, "shop.invalid_filter", "from and to must be RFC 3339 timestamps or YYYY-MM-DD dates")
		return
	}
	orders, more, err := h.o.Service.ListOrdersPage(r.Context(), org, f)
	if err != nil {
		h.fail(w, r, "list orders", err)
		return
	}
	out := make([]orderJSON, 0, len(orders))
	for _, o := range orders {
		out = append(out, toOrderJSON(o))
	}
	var next *int
	if more {
		n := f.Offset + f.Limit
		next = &n
	}
	w.Header().Set("Cache-Control", "no-store")
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"orders": out, "next_offset": next})
}

// HandleOrderCounts serves GET .../shop/orders/counts: how many orders each
// tab of the list holds (all, to_ship, unpaid, shipped, refunds).
func (h *Handler) HandleOrderCounts(w http.ResponseWriter, r *http.Request) {
	org, ok := orgID(w, r)
	if !ok {
		return
	}
	c, err := h.o.Service.CountOrders(r.Context(), org)
	if err != nil {
		h.fail(w, r, "count orders", err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httputil.WriteJSON(w, http.StatusOK, c)
}

// HandlePatchOrder serves PATCH .../shop/orders/{order_id} {staff_note}: the
// internal note of the order, for staff only.
func (h *Handler) HandlePatchOrder(w http.ResponseWriter, r *http.Request) {
	org, ok := orgID(w, r)
	if !ok {
		return
	}
	id, ok := pathID(w, r, "order_id", shop.ErrOrderNotFound)
	if !ok {
		return
	}
	var b struct {
		StaffNote *string `json:"staff_note"`
	}
	if !readBody(w, r, &b, false) {
		return
	}
	if b.StaffNote == nil {
		writeErr(w, r, http.StatusBadRequest, "shop.invalid_body", "staff_note is required (an empty text erases the note)")
		return
	}
	author := shop.StaffNoteAuthor{Label: "staff"}
	if actor, ok := auth.ActorFromContext(r.Context()); ok {
		switch actor.Type {
		case auth.ActorTypeUser:
			if uid, err := uuid.Parse(actor.ID); err == nil {
				author.UserID = &uid
			}
		case auth.ActorTypeService:
			author.Label = "API key"
		}
	}
	o, err := h.o.Service.SetStaffNote(r.Context(), org, id, *b.StaffNote, author)
	if err != nil {
		h.fail(w, r, "save order note", err)
		return
	}
	// The note's text is never copied into the audit trail: it may name the buyer.
	h.writeAudit(r, "v1.shop.order.note", "shop_order", id.String(), org, map[string]any{"number": o.Number, "cleared": *b.StaffNote == ""})
	httputil.WriteJSON(w, http.StatusOK, toOrderJSON(o))
}

// HandleResendEmail serves POST .../shop/orders/{order_id}/resend-email
// {kind: received|shipped|refunded}: queues the buyer's letter once more. One request
// per order per minute (429 shop.rate_limited and Retry-After).
func (h *Handler) HandleResendEmail(w http.ResponseWriter, r *http.Request) {
	org, ok := orgID(w, r)
	if !ok {
		return
	}
	id, ok := pathID(w, r, "order_id", shop.ErrOrderNotFound)
	if !ok {
		return
	}
	var b struct {
		Kind string `json:"kind"`
	}
	if !readBody(w, r, &b, false) {
		return
	}
	if b.Kind != shop.EmailReceived && b.Kind != shop.EmailShipped && b.Kind != shop.EmailRefunded {
		writeErr(w, r, http.StatusBadRequest, "shop.invalid_email_kind", "kind must be received, shipped or refunded")
		return
	}
	// The budget is per order, and the order is looked up with its organization
	// first: a foreign id must answer its own 404, not spend a minute.
	if _, err := h.o.Service.GetOrder(r.Context(), org, id); err != nil {
		h.fail(w, r, "resend e-mail", err)
		return
	}
	if ok, retry := h.resend.allow(id.String(), time.Now()); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(retry))
		writeErr(w, r, http.StatusTooManyRequests, "shop.rate_limited", "The letter was queued a moment ago. Wait one minute before sending it again.")
		return
	}
	if err := h.o.Service.ResendEmail(r.Context(), org, id, b.Kind); err != nil {
		// Nothing was queued, so the order's minute is not spent.
		h.resend.release(id.String())
		h.fail(w, r, "resend e-mail", err)
		return
	}
	h.writeAudit(r, "v1.shop.order.resend_email", "shop_order", id.String(), org, map[string]any{"kind": b.Kind})
	httputil.WriteJSON(w, http.StatusAccepted, map[string]any{"queued": true, "kind": b.Kind})
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
	// The work is detached from the request: a manager whose connection
	// drops (or the router's timeout) must not leave a refund half driven,
	// and the client repeats the call with the same Idempotency-Key to read
	// the result.
	wctx, cancel := refundWorkCtx(w, r)
	defer cancel()
	res, err := h.o.Service.Refund(wctx, shop.RefundInput{
		OrgID: org, OrderID: id, Amount: b.AmountMinor, Reason: b.Reason, RequestedBy: actorLabel(r.Context()),
		IdempotencyKey: r.Header.Get("Idempotency-Key"),
	})
	if err != nil {
		h.fail(w, r, "refund order", err)
		return
	}
	if !res.Replayed {
		h.writeAudit(r, "v1.shop.order.refund", "shop_order", id.String(), org, map[string]any{
			"number": res.Order.Number, "refund_id": res.Refund.ID.String(), "amount": res.Refund.Amount,
			"refund_state": res.Refund.State,
		})
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"order": toOrderJSON(res.Order), "refund": toRefundJSON(res.Refund), "replayed": res.Replayed,
	})
}

// refundWorkBudget bounds the work one refund request starts (the same
// numbers as the ticket refund routes, httpserver/refund_org_routes.go): a
// slow provider must not be cut by the router's timeout.
const refundWorkBudget = 2 * time.Minute

// refundWriteSlack is the time after the work budget left to write the answer.
const refundWriteSlack = 15 * time.Second

// refundWorkCtx is the detached context of the refund work. It also moves the
// connection's write deadline past the budget, because the server's own
// WriteTimeout is shorter than a slow provider.
func refundWorkCtx(w http.ResponseWriter, r *http.Request) (context.Context, context.CancelFunc) {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(refundWorkBudget + refundWriteSlack))
	return context.WithTimeout(context.WithoutCancel(r.Context()), refundWorkBudget)
}

// HandleCourierText serves GET .../shop/orders/{order_id}/courier-text: the
// courier form as text/plain, ready to paste: the order number, the
// recipient and address, map links, the goods, the total and the buyer's
// note, in the organization's language.
func (h *Handler) HandleCourierText(w http.ResponseWriter, r *http.Request) {
	org, ok := orgID(w, r)
	if !ok {
		return
	}
	id, ok := pathID(w, r, "order_id", shop.ErrOrderNotFound)
	if !ok {
		return
	}
	src, err := h.o.Service.CourierSourceFor(r.Context(), org, id)
	if err != nil {
		h.fail(w, r, "courier text", err)
		return
	}
	// The same builder as the logistics group's Telegram message.
	text := salesnotify.FormatShopCourierText(salesnotify.ShopOrder{
		Locale: src.Locale, ShopCountry: src.ShopCountry, Order: src.Order, Items: src.Items,
	})
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(text)) //nolint:gosec // G705 false positive: text/plain with nosniff, never rendered as HTML
}
