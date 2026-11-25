// checkout_buyer_track.go — InitiateCheckout of a prepared (spec 65) checkout.
//
// The embedded checkout opens its order when the order sheet opens, long
// before anybody pays, so checkout/start with "prepare" does not queue
// InitiateCheckout. The event fires when the buyer is attached at "Pay"
// (checkout_buyer_attach.go), with the same event id as the classic path
// ("ic:<checkout id>"), so the dedup holds. Everything here runs on the pool
// after the attach transaction has committed, is bounded, and never changes
// the buyer's answer.
package hfeed

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/conversions"
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

// queueAttachedInitiateCheckout queues InitiateCheckout for a prepared order
// whose buyer was just attached. It needs the checkout's stored explicit yes
// and a stored buyer context; without either nothing is queued. Best effort.
func (h *Handler) queueAttachedInitiateCheckout(ctx context.Context, csID uuid.UUID, pc gen.CheckoutPaymentContextRow) {
	if h.cartConversions == nil || h.checkoutQueries == nil {
		return
	}
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), attachTrackReadTimeout)
	defer cancel()
	db := h.checkoutQueries.DB()
	stored, err := trackingconsent.ForCheckout(rctx, db, csID)
	if err != nil || !stored.Allows() {
		h.logAttachTrackSkip(err, csID)
		return
	}
	buyerCtx, err := readStoredBuyerContext(rctx, db, csID)
	if err != nil || len(buyerCtx) == 0 {
		h.logAttachTrackSkip(err, csID)
		return
	}
	sessionID, groups, err := readOrderCart(rctx, db, pc.OrderID)
	if err != nil {
		h.logAttachTrackSkip(err, csID)
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
	h.queueCartEvent(ctx, pc.OrgID, conversions.KindInitiateCheckout, checkoutEventIDPfx+csID.String(),
		sessionID, bd, buyerCtx, 0)
}

// logAttachTrackSkip logs a failed read (never the buyer's data); a plain
// "no row / no consent" is silent.
func (h *Handler) logAttachTrackSkip(err error, csID uuid.UUID) {
	if err == nil || h.logger == nil || errors.Is(err, pgx.ErrNoRows) {
		return
	}
	h.logger.Warn("public_checkout_buyer: initiate-checkout inputs not read",
		slog.String("checkout_session_id", csID.String()), slog.String("error", err.Error()))
}
