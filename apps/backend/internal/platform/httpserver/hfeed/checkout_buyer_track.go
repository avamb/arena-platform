// checkout_buyer_track.go — conversion events of an existing checkout read
// from what the checkout stored (spec 68).
//
// AcceptTerms of a prepared (spec 65) checkout fires when the buyer attach
// writes the checkout_consents row (checkout_buyer_attach.go), and
// AddPaymentInfo fires when the widget reports a complete payment form
// (checkout_payment_info.go). Both read the checkout's stored explicit yes,
// its stored buyer context and the order's cart; without the yes or the
// context nothing is queued. InitiateCheckout no longer fires here: the
// widget queues it through checkout/quote ("ic:<add_to_cart_id>").
// Everything here runs on the pool after any transaction of the request has
// committed, is bounded, and never changes the buyer's answer.
package hfeed

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/trackingconsent"
)

// attachTrackReadTimeout bounds the reads that decide the event.
const attachTrackReadTimeout = 2 * time.Second

// orderItemGroup is the order's items of one category at one unit price.
type orderItemGroup struct {
	TierID    string
	UnitPrice int64
	Quantity  int32
}

// breakdownFromItems builds the cart breakdown of an order: total and
// currency as the order holds them, one line per (category, unit price).
func breakdownFromItems(total int64, currency string, groups []orderItemGroup) hcheckout.PricingBreakdown {
	bd := hcheckout.PricingBreakdown{Total: total, Currency: currency}
	for _, g := range groups {
		bd.Quantity += g.Quantity
		bd.Subtotal += g.UnitPrice * int64(g.Quantity)
		bd.Lines = append(bd.Lines, hcheckout.PricingLineItem{
			TierID: g.TierID, Quantity: g.Quantity, UnitPrice: g.UnitPrice, Subtotal: g.UnitPrice * int64(g.Quantity),
		})
	}
	return bd
}

// readOrderCart reads the order's session and item groups.
func readOrderCart(ctx context.Context, db gen.DBTX, orderID uuid.UUID) (uuid.UUID, []orderItemGroup, error) {
	var sessionID uuid.UUID
	if err := db.QueryRow(ctx, `SELECT session_id FROM orders WHERE id = $1`, orderID).Scan(&sessionID); err != nil {
		return uuid.Nil, nil, err
	}
	rows, err := db.Query(ctx, `
		SELECT COALESCE(tier_id::text, ''), unit_price, count(*)::int
		FROM   order_items
		WHERE  order_id = $1
		GROUP  BY tier_id, unit_price
		ORDER  BY tier_id, unit_price`, orderID)
	if err != nil {
		return uuid.Nil, nil, err
	}
	defer rows.Close()
	var groups []orderItemGroup
	for rows.Next() {
		var g orderItemGroup
		var n int
		if err := rows.Scan(&g.TierID, &g.UnitPrice, &n); err != nil {
			return uuid.Nil, nil, err
		}
		g.Quantity = int32(n) //nolint:gosec // a cart has a handful of tickets
		groups = append(groups, g)
	}
	return sessionID, groups, rows.Err()
}

// readStoredBuyerContext reads the cleaned buyer context stored at prepare;
// nil when there is none.
func readStoredBuyerContext(ctx context.Context, db gen.DBTX, csID uuid.UUID) ([]byte, error) {
	var raw []byte
	err := db.QueryRow(ctx, `SELECT context FROM checkout_buyer_contexts WHERE checkout_session_id = $1`, csID).Scan(&raw)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

// queueStoredCheckoutEvent queues one event (kind, eventID) of the checkout
// csID whose order is pc. It needs the checkout's stored explicit yes and a
// stored buyer context; without either nothing is queued. The event id
// dedups repeats. Best effort.
func (h *Handler) queueStoredCheckoutEvent(ctx context.Context, csID uuid.UUID, pc gen.CheckoutPaymentContextRow, kind, eventID string) {
	if h.cartConversions == nil || h.checkoutQueries == nil {
		return
	}
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), attachTrackReadTimeout)
	defer cancel()
	db := h.checkoutQueries.DB()
	stored, err := trackingconsent.ForCheckout(rctx, db, csID)
	if err != nil || !stored.Allows() {
		h.logAttachTrackSkip(err, csID, kind)
		return
	}
	buyerCtx, err := readStoredBuyerContext(rctx, db, csID)
	if err != nil || len(buyerCtx) == 0 {
		h.logAttachTrackSkip(err, csID, kind)
		return
	}
	sessionID, groups, err := readOrderCart(rctx, db, pc.OrderID)
	if err != nil {
		h.logAttachTrackSkip(err, csID, kind)
		return
	}
	var total int64
	if pc.Total != nil {
		total = *pc.Total
	}
	currency := ""
	if pc.Currency != nil {
		currency = *pc.Currency
	}
	bd := breakdownFromItems(total, currency, groups)
	h.queueCartEvent(ctx, pc.OrgID, kind, eventID, sessionID, bd, buyerCtx, 0)
}

// logAttachTrackSkip logs a failed read (never the buyer's data); a plain
// "no row / no consent" is silent.
func (h *Handler) logAttachTrackSkip(err error, csID uuid.UUID, kind string) {
	if err == nil || h.logger == nil || errors.Is(err, pgx.ErrNoRows) {
		return
	}
	h.logger.Warn("public_checkout: conversion event inputs not read",
		slog.String("kind", kind),
		slog.String("checkout_session_id", csID.String()), slog.String("error", err.Error()))
}
