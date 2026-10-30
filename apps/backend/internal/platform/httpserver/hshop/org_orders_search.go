package hshop

import (
	"net/http"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/shop"
)

// orderSearchBody carries the filters of POST .../shop/orders/search, the same
// ones GET .../shop/orders reads from its query string.
type orderSearchBody struct {
	Q      string `json:"q"`
	View   string `json:"view"`
	Status string `json:"status"`
	From   string `json:"from"`
	To     string `json:"to"`
	Limit  *int   `json:"limit"`
	Offset *int   `json:"offset"`
}

// HandleSearchOrders serves POST .../shop/orders/search. It answers exactly
// what HandleListOrders answers, but the filters (a buyer's e-mail or name
// among them) travel in the body, not in an address a proxy logs (SA-17).
func (h *Handler) HandleSearchOrders(w http.ResponseWriter, r *http.Request) {
	org, ok := orgID(w, r)
	if !ok {
		return
	}
	var b orderSearchBody
	if !readBody(w, r, &b, true) {
		return
	}
	f := shop.OrderFilter{Status: b.Status, View: b.View, Query: b.Q}
	var okFrom, okTo bool
	f.From, okFrom = parseTimeParam(b.From)
	f.To, okTo = parseTimeParam(b.To)
	if !okFrom || !okTo {
		writeErr(w, r, http.StatusBadRequest, "shop.invalid_filter", "from and to must be RFC 3339 timestamps or YYYY-MM-DD dates")
		return
	}
	if b.Limit != nil {
		f.Limit = *b.Limit
	}
	if b.Offset != nil {
		f.Offset = *b.Offset
	}
	if f.Limit == 0 {
		f.Limit = 50
	}
	orders, err := h.o.Service.ListOrders(r.Context(), org, f)
	if err != nil {
		h.fail(w, r, "search orders", err)
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
