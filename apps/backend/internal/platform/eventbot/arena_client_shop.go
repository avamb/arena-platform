package eventbot

// arena_client_shop.go — the routes behind the "Shop" section (spec 61
// section 7): the organization's shop orders, the storefront switch and the
// settings. The bot computes nothing here: the server decides what an order
// can do, the bot shows it and asks.

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

func shopBase(orgID uuid.UUID) string {
	return "/v1/organizations/" + orgID.String() + "/shop"
}

func shopOrderPath(orgID, orderID uuid.UUID) string {
	return shopBase(orgID) + "/orders/" + orderID.String()
}

// ShopOrderQuery is a page of the shop orders: the tab (view), a status, the
// search text and the window. Q is sent in a body, never in the URL, so a
// buyer's e-mail stays out of the access logs.
type ShopOrderQuery struct {
	View   string
	Status string
	Q      string
	Limit  int
	Offset int
}

// ShopOrders reads one page of orders. A search text goes to the POST route
// (the same answer, the filters in the body); without one the GET route is
// used.
func (c *ArenaClient) ShopOrders(ctx context.Context, jwt string, orgID uuid.UUID, q ShopOrderQuery) (openapi.ShopOrderList, error) {
	var out openapi.ShopOrderList
	if q.Q != "" {
		body := openapi.ShopOrderSearchRequest{Q: &q.Q}
		if q.View != "" {
			body.View = &q.View
		}
		if q.Status != "" {
			body.Status = &q.Status
		}
		if q.Limit > 0 {
			body.Limit = &q.Limit
		}
		if q.Offset > 0 {
			body.Offset = &q.Offset
		}
		err := c.do(ctx, http.MethodPost, shopBase(orgID)+"/orders/search", jwt, body, &out)
		return out, err
	}
	v := url.Values{}
	if q.View != "" {
		v.Set("view", q.View)
	}
	if q.Status != "" {
		v.Set("status", q.Status)
	}
	if q.Limit > 0 {
		v.Set("limit", strconv.Itoa(q.Limit))
	}
	if q.Offset > 0 {
		v.Set("offset", strconv.Itoa(q.Offset))
	}
	path := shopBase(orgID) + "/orders"
	if enc := v.Encode(); enc != "" {
		path += "?" + enc
	}
	err := c.do(ctx, http.MethodGet, path, jwt, nil, &out)
	return out, err
}

// ShopOrderCounts reads the size of each tab.
func (c *ArenaClient) ShopOrderCounts(ctx context.Context, jwt string, orgID uuid.UUID) (openapi.ShopOrderCounts, error) {
	var out openapi.ShopOrderCounts
	err := c.do(ctx, http.MethodGet, shopBase(orgID)+"/orders/counts", jwt, nil, &out)
	return out, err
}

// ShopOrder reads one order.
func (c *ArenaClient) ShopOrder(ctx context.Context, jwt string, orgID, orderID uuid.UUID) (openapi.ShopOrder, error) {
	var out openapi.ShopOrder
	err := c.do(ctx, http.MethodGet, shopOrderPath(orgID, orderID), jwt, nil, &out)
	return out, err
}

// ShopCourierText reads the delivery details of an order as the server
// builds them, one per line, ready to paste into a courier form.
func (c *ArenaClient) ShopCourierText(ctx context.Context, jwt string, orgID, orderID uuid.UUID) (string, error) {
	var out rawText
	err := c.do(ctx, http.MethodGet, shopOrderPath(orgID, orderID)+"/courier-text", jwt, nil, &out)
	return string(out), err
}

// ShopShip marks an order shipped with an optional tracking note.
func (c *ArenaClient) ShopShip(ctx context.Context, jwt string, orgID, orderID uuid.UUID, tracking string) (openapi.ShopOrder, error) {
	var out openapi.ShopOrder
	err := c.do(ctx, http.MethodPost, shopOrderPath(orgID, orderID)+"/ship", jwt, openapi.ShopShipRequest{TrackingNote: &tracking}, &out)
	return out, err
}

// ShopNote replaces the internal note of an order (an empty text erases it).
func (c *ArenaClient) ShopNote(ctx context.Context, jwt string, orgID, orderID uuid.UUID, note string) (openapi.ShopOrder, error) {
	var out openapi.ShopOrder
	err := c.do(ctx, http.MethodPatch, shopOrderPath(orgID, orderID), jwt, openapi.ShopOrderNoteRequest{StaffNote: note}, &out)
	return out, err
}

// ShopResend queues a buyer letter again: received, shipped or refunded.
func (c *ArenaClient) ShopResend(ctx context.Context, jwt string, orgID, orderID uuid.UUID, kind string) error {
	return c.do(ctx, http.MethodPost, shopOrderPath(orgID, orderID)+"/resend-email", jwt, openapi.ShopResendEmailRequest{Kind: kind}, nil)
}

// ShopCancel cancels an order that still waits for its payment.
func (c *ArenaClient) ShopCancel(ctx context.Context, jwt string, orgID, orderID uuid.UUID) (openapi.ShopOrder, error) {
	var out openapi.ShopOrder
	err := c.do(ctx, http.MethodPost, shopOrderPath(orgID, orderID)+"/cancel", jwt, struct{}{}, &out)
	return out, err
}

// shopMoneyTimeout is how long one refund or resolve call may take: the
// server drives the refund to the payment provider before it answers, and the
// work it started finishes even if the bot gave up.
const shopMoneyTimeout = refundCallTimeout

// slowClient is a copy of the client whose calls may take shopMoneyTimeout.
func (c *ArenaClient) slowClient() *ArenaClient {
	slow := *c
	hc := *c.http
	hc.Timeout = shopMoneyTimeout
	slow.http = &hc
	return &slow
}

// ShopRefund returns money of an order. The Idempotency-Key makes a repeat of
// the same confirmed operation answer the first refund instead of starting a
// second one. amountMinor nil refunds everything that is left.
func (c *ArenaClient) ShopRefund(ctx context.Context, jwt string, orgID, orderID uuid.UUID, key string, amountMinor *int64, reason string) (openapi.ShopRefundResponse, error) {
	var out openapi.ShopRefundResponse
	body := openapi.ShopRefundRequest{AmountMinor: amountMinor}
	if reason != "" {
		body.Reason = &reason
	}
	err := c.slowClient().do(withIdempotencyKey(ctx, key), http.MethodPost, shopOrderPath(orgID, orderID)+"/refund", jwt, body, &out)
	return out, err
}

// ShopResolve settles an order parked for a check: "fulfil" or
// "refund_and_cancel". The key works like a refund's.
func (c *ArenaClient) ShopResolve(ctx context.Context, jwt string, orgID, orderID uuid.UUID, key, action string) (openapi.ShopResolveResponse, error) {
	var out openapi.ShopResolveResponse
	err := c.slowClient().do(withIdempotencyKey(ctx, key), http.MethodPost, shopOrderPath(orgID, orderID)+"/resolve", jwt, openapi.ShopResolveRequest{Action: action}, &out)
	return out, err
}

// ShopSettings reads the shop settings.
func (c *ArenaClient) ShopSettings(ctx context.Context, jwt string, orgID uuid.UUID) (openapi.ShopSettings, error) {
	var out openapi.ShopSettings
	err := c.do(ctx, http.MethodGet, shopBase(orgID)+"/settings", jwt, nil, &out)
	return out, err
}

// ShopStorefront opens or closes the storefront.
func (c *ArenaClient) ShopStorefront(ctx context.Context, jwt string, orgID uuid.UUID, enabled bool) (openapi.ShopSettings, error) {
	var out openapi.ShopSettings
	err := c.do(ctx, http.MethodPut, shopBase(orgID)+"/storefront", jwt, openapi.ShopStorefrontRequest{Enabled: enabled}, &out)
	return out, err
}

// shopCallWindow bounds a whole money send: every attempt and the pauses
// between them (the refund screens use the same arithmetic).
const shopCallWindow = shopReplayAttempts*shopMoneyTimeout + (shopReplayAttempts-1)*5*time.Second

// shopReplayAttempts is how many times a money call with no answer is made
// (always with the SAME Idempotency-Key).
const shopReplayAttempts = 3

// ShopChannel is a sales channel as the settings screen names it: the API
// answers the payment provider next to the name.
type ShopChannel struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Provider string `json:"provider"`
}

// ShopChannels lists the organization's sales channels with their payment
// provider.
func (c *ArenaClient) ShopChannels(ctx context.Context, jwt string, orgID uuid.UUID) ([]ShopChannel, error) {
	var out struct {
		Channels []ShopChannel `json:"channels"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/organizations/"+orgID.String()+"/channels", jwt, nil, &out); err != nil {
		return nil, err
	}
	return out.Channels, nil
}
