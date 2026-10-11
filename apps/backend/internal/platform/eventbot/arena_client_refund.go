package eventbot

import (
	"context"
	"net/http"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

// RefundOptions reads what an order can return
// (GET .../orders/{id}/refund-options): the route, the provider and, per
// ticket, what is refundable. The bot never works these out itself.
func (c *ArenaClient) RefundOptions(ctx context.Context, jwt string, orgID, orderID uuid.UUID) (openapi.OrderRefundOptions, error) {
	var out openapi.OrderRefundOptions
	err := c.do(ctx, http.MethodGet, "/v1/organizations/"+orgID.String()+"/orders/"+orderID.String()+"/refund-options", jwt, nil, &out)
	return out, err
}

// CreateRefunds starts one refund operation (POST .../orders/{id}/refunds).
// The Idempotency-Key makes a retry of the same confirmed operation answer
// the first result.
func (c *ArenaClient) CreateRefunds(ctx context.Context, jwt string, orgID, orderID uuid.UUID, key string, req openapi.CreateOrderRefundsRequest) (openapi.CreateOrderRefundsResponse, error) {
	var out openapi.CreateOrderRefundsResponse
	err := c.do(withIdempotencyKey(ctx, key), http.MethodPost, "/v1/organizations/"+orgID.String()+"/orders/"+orderID.String()+"/refunds", jwt, req, &out)
	return out, err
}
