package eventbot

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

// RefundQueue reads one page of the organization's refunds in a state
// (GET .../refunds?state=&limit=&offset=).
func (c *ArenaClient) RefundQueue(ctx context.Context, jwt string, orgID uuid.UUID, state string, limit, offset int) (openapi.OrderRefundQueue, error) {
	q := url.Values{}
	if state != "" {
		q.Set("state", state)
	}
	q.Set("limit", strconv.Itoa(limit))
	q.Set("offset", strconv.Itoa(offset))
	var out openapi.OrderRefundQueue
	err := c.do(ctx, http.MethodGet, "/v1/organizations/"+orgID.String()+"/refunds?"+q.Encode(), jwt, nil, &out)
	return out, err
}

// RefundGet reads one refund of the organization with its queue fields
// (GET .../refunds/{id}).
func (c *ArenaClient) RefundGet(ctx context.Context, jwt string, orgID, refundID uuid.UUID) (openapi.OrderRefundQueueItem, error) {
	var out openapi.OrderRefundDetail
	err := c.do(ctx, http.MethodGet, "/v1/organizations/"+orgID.String()+"/refunds/"+refundID.String(), jwt, nil, &out)
	return out.Refund, err
}

// RetryRefund sends a failed or never-sent refund again as a new operation
// (POST .../refunds/{id}/retry). The route is idempotent per refund (the
// server derives the operation's key from the refund id), so repeating the
// call after an answer was lost returns the operation already started. Like a
// create, it waits as long as the server may work.
func (c *ArenaClient) RetryRefund(ctx context.Context, jwt string, orgID, refundID uuid.UUID) (openapi.CreateOrderRefundsResponse, error) {
	var out openapi.CreateOrderRefundsResponse
	slow := *c
	hc := *c.http
	hc.Timeout = refundCallTimeout
	slow.http = &hc
	err := slow.do(ctx, http.MethodPost, "/v1/organizations/"+orgID.String()+"/refunds/"+refundID.String()+"/retry", jwt, map[string]any{}, &out)
	return out, err
}
