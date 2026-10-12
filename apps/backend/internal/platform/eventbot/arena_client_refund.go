package eventbot

import (
	"context"
	"net/http"
	"time"

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

// refundCallTimeout is how long one create call may take: the server sends the
// refund to the payment provider before it answers, and the work it started
// finishes even if the bot gave up (it is detached from the request).
const refundCallTimeout = 100 * time.Second

// CreateRefunds starts one refund operation (POST .../orders/{id}/refunds).
// The Idempotency-Key makes a retry of the same confirmed operation answer
// the first result.
func (c *ArenaClient) CreateRefunds(ctx context.Context, jwt string, orgID, orderID uuid.UUID, key string, req openapi.CreateOrderRefundsRequest) (openapi.CreateOrderRefundsResponse, error) {
	var out openapi.CreateOrderRefundsResponse
	slow := *c
	hc := *c.http
	hc.Timeout = refundCallTimeout
	slow.http = &hc
	err := slow.do(withIdempotencyKey(ctx, key), http.MethodPost, "/v1/organizations/"+orgID.String()+"/orders/"+orderID.String()+"/refunds", jwt, req, &out)
	return out, err
}
