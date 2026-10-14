package eventbot

// arena_client_orders.go — the calls behind the bot's Orders screens (spec 35
// EC-04 and EC-05): the paged, searchable list, one order's card data, and the
// cancellation of an unpaid order. Every body here holds buyers' names,
// e-mails and phones: nothing in this file logs them.

import (
	"context"
	"net/http"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

// The tabs of the list, as the API spells them in its `tab` parameter.
const (
	ordTabRecent = "recent"
	ordTabPaid   = "paid"
	ordTabUnpaid = "unpaid"
)

// OrderListQuery is one page request of the orders list.
type OrderListQuery struct {
	Tab       string     // recent (default), paid or unpaid
	Q         string     // the one free-text search (barcode, number, e-mail, phone, name)
	SessionID *uuid.UUID // only the orders of this date
	EventID   *uuid.UUID // only the orders of this event
	Limit     int
	Offset    int
}

// OrderPage is the list answer: one page and the count behind it.
type OrderPage struct {
	Orders     []openapi.OrderListItem `json:"orders"`
	Limit      int                     `json:"limit"`
	Offset     int                     `json:"offset"`
	TotalCount int64                   `json:"total_count"`
	HasMore    bool                    `json:"has_more"`
}

// orderSearchBody is the body of POST .../orders/search: the same filters as
// the GET query string, kept out of the URL so a buyer's e-mail or phone
// never reaches a proxy log (owner decision 2026-10-14).
type orderSearchBody struct {
	Tab       string `json:"tab,omitempty"`
	Q         string `json:"q,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	EventID   string `json:"event_id,omitempty"`
	Limit     int    `json:"limit,omitempty"`
	Offset    int    `json:"offset,omitempty"`
}

// ListOrders returns a page of the organization's orders
// (POST .../orders/search). The search text goes to `q` untouched: the server
// decides whether it is a barcode, an order number, an e-mail, a phone or a
// name.
func (c *ArenaClient) ListOrders(ctx context.Context, jwt string, orgID uuid.UUID, q OrderListQuery) (OrderPage, error) {
	body := orderSearchBody{Q: q.Q, Limit: q.Limit, Offset: q.Offset}
	if q.Tab != "" && q.Tab != ordTabRecent {
		body.Tab = q.Tab
	}
	if q.SessionID != nil {
		body.SessionID = q.SessionID.String()
	}
	if q.EventID != nil {
		body.EventID = q.EventID.String()
	}
	var out OrderPage
	err := c.do(ctx, http.MethodPost, "/v1/organizations/"+orgID.String()+"/orders/search", jwt, body, &out)
	return out, err
}

// GetOrder returns one order with its tickets, delivery, payment, channel
// and the reason it is unpaid (GET .../orders/{id}). An order of another
// organization is a 404, like a missing one.
func (c *ArenaClient) GetOrder(ctx context.Context, jwt string, orgID, orderID uuid.UUID) (openapi.OrderDetail, error) {
	var out openapi.OrderDetail
	path := "/v1/organizations/" + orgID.String() + "/orders/" + orderID.String()
	err := c.do(ctx, http.MethodGet, path, jwt, nil, &out)
	return out, err
}

// CancelOrder cancels a pending_payment order and releases its hold
// (POST .../orders/{id}/cancel). Any other status is a 409
// orders.invalid_transition.
func (c *ArenaClient) CancelOrder(ctx context.Context, jwt string, orgID, orderID uuid.UUID, reason string) error {
	path := "/v1/organizations/" + orgID.String() + "/orders/" + orderID.String() + "/cancel"
	body := map[string]string{}
	if reason != "" {
		body["reason"] = reason
	}
	return c.do(ctx, http.MethodPost, path, jwt, body, nil)
}
