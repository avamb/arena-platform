package eventbot

// arena_client_resend.go — the call behind "Resend tickets" (EC-13). The body
// carries a buyer's address when the person typed one: nothing here logs it.

import (
	"context"
	"net/http"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

// ResendOrderTickets queues the active tickets of a paid order again
// (POST .../orders/{id}/resend-tickets). email "" sends them to the order's
// own address; any other value is a one-time address of this resend that the
// API keeps for 24 hours and never writes onto the order. A 409 carries one of
// order.seller_site_order, order.not_paid or order.no_active_tickets.
func (c *ArenaClient) ResendOrderTickets(ctx context.Context, jwt string, orgID, orderID uuid.UUID, email string) (openapi.OrderResendTicketsResponse, error) {
	body := map[string]string{}
	if email != "" {
		body["email"] = email
	}
	var out openapi.OrderResendTicketsResponse
	path := "/v1/organizations/" + orgID.String() + "/orders/" + orderID.String() + "/resend-tickets"
	err := c.do(ctx, http.MethodPost, path, jwt, body, &out)
	return out, err
}
