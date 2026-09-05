// checkout_order.go — the pricing_confirmed transition and the order
// aggregate, written in ONE transaction (W1-A6c, feature #488; spec §14.1).
//
// Before this file HandleConfirmCheckout ended each of its three pricing
// branches with a bare ConfirmCheckoutSession call. That is no longer enough:
// spec §14.1 makes orders the explicit money aggregate, and an order that
// exists without its checkout snapshot — or a snapshot without its order — is
// a reconciliation bug that no later step can repair. So both writes go
// through confirmAndCreateOrder, which begins a transaction, confirms, mints
// the aggregate and commits.
//
// Order creation is BEST-EFFORT-FREE in exactly one direction: a checkout
// session that already produced an order (a retried confirm) is not an error,
// because ConfirmCheckoutSession itself is the state guard — it returns
// pgx.ErrNoRows unless the session is still in 'created', so a second confirm
// never reaches the ordering call at all.
package hcheckout

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/ordering"
)

// orderBuyer carries the identity fields snapshotted onto the order. Every
// field is optional: the authenticated checkout API collects none of them,
// while the public feed collects at least an email.
type orderBuyer struct {
	CustomerID    *uuid.UUID
	Name          *string
	Email         *string
	Phone         *string
	PaymentMethod *string
}

// confirmAndCreateOrder runs, atomically:
//
//	checkout_sessions: created → pricing_confirmed (+ money snapshot)
//	orders / order_items / order_events: the aggregate for that snapshot
//
// seatUnitPrices and tierUnitPrices price the seated units; for a GA cart both
// may be nil because reservation_ga_items.unit_price is authoritative there.
//
// Returns the confirmed checkout session row. A pgx.ErrNoRows error means the
// session was not in 'created' state and the caller must answer 409 — the same
// contract the bare ConfirmCheckoutSession call had.
func (h *Handler) confirmAndCreateOrder(
	ctx context.Context,
	id uuid.UUID,
	reservation gen.ReservationRow,
	bd PricingBreakdown,
	promoCodeID *uuid.UUID,
	tierUnitPrices map[uuid.UUID]int64,
	buyer orderBuyer,
) (gen.CheckoutSessionRow, error) {
	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return gen.CheckoutSessionRow{}, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	txq := gen.New(tx)

	cs, err := txq.ConfirmCheckoutSession(ctx, id,
		bd.Subtotal, bd.Discount, bd.PlatformFee, bd.ProviderFee, bd.Tax, bd.Total,
		bd.Currency, promoCodeID,
	)
	if err != nil {
		return gen.CheckoutSessionRow{}, err
	}

	eventID, err := txq.GetSessionEventID(ctx, reservation.SessionID)
	if err != nil {
		return gen.CheckoutSessionRow{}, fmt.Errorf("resolve event for session %s: %w", reservation.SessionID, err)
	}

	// The channel fee percentage is an audit snapshot; a lookup failure must
	// not cost the buyer their order, so it degrades to 0 (see
	// ordering.ChargePercentBP).
	var chargePercentBP int32
	if h.channelQueries != nil {
		if ch, chErr := txq.GetSalesChannelByID(ctx, cs.ChannelID, cs.OrgID); chErr == nil {
			chargePercentBP = ordering.ChargePercentBP(ch.FeePercent)
		}
	}

	actor := ordering.ActorSystem
	if cs.UserID != nil {
		actor = "user:" + cs.UserID.String()
	}

	if _, err := ordering.CreateOrderFromCheckout(ctx, txq, ordering.CreateInput{
		CheckoutSessionID: cs.ID,
		EventID:           eventID,
		CustomerID:        buyer.CustomerID,
		Source:            ordering.SourceCheckoutAPI,
		Actor:             actor,
		ChargePercentBP:   chargePercentBP,
		BuyerName:         buyer.Name,
		BuyerEmail:        buyer.Email,
		BuyerPhone:        buyer.Phone,
		PaymentMethod:     buyer.PaymentMethod,
		TierUnitPrices:    tierUnitPrices,
	}); err != nil {
		return gen.CheckoutSessionRow{}, fmt.Errorf("create order: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return gen.CheckoutSessionRow{}, fmt.Errorf("commit: %w", err)
	}
	return cs, nil
}

// isCheckoutStateConflict reports whether the error means "the session was not
// in 'created'", which every confirm branch answers with a 409.
func isCheckoutStateConflict(err error) bool { return errors.Is(err, pgx.ErrNoRows) }
