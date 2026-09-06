package ordering

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// ─────────────────────────────────────────────────────────────────────────────
// UpdateOrderFromCheckout — the "same orderId" branch of the one-open-order rule
// ─────────────────────────────────────────────────────────────────────────────
//
// Spec §7.7 step 5: when the buyer already holds a pending_payment order for
// this event session and its reservation is still alive, a repeat
// CREATE_ORDER_EXT must return THE SAME orderId with refreshed external_ref,
// lines and sums — never a second order. WooCommerce happily mints its own
// pending orders on the site side, so the gateway would otherwise accumulate
// one arena order per checkout retry, each holding inventory.
//
// The update is a full rewrite of the money snapshot and of the per-unit lines,
// not a patch: the cart has just been reconciled against the request `lines`,
// so the previous item rows describe a composition that no longer exists.

// UpdateStore is the query surface UpdateOrderFromCheckout needs: everything
// CreateOrderFromCheckout uses, plus the two mutations that let an existing
// order be re-pointed at a freshly priced checkout session.
type UpdateStore interface {
	CreateStore
	UpdateOrderCheckout(
		ctx context.Context,
		id, orgID uuid.UUID,
		externalRef *string,
		checkoutSessionID, reservationID uuid.UUID,
		currency string,
		subtotal, discount, charge, total int64,
		chargePercentBP int32,
		promoCodeID *uuid.UUID,
		buyerName, buyerEmail, buyerPhone *string,
		expiresAt *time.Time,
	) (gen.OrderRow, error)
	DeleteOrderItemsByOrder(ctx context.Context, orderID uuid.UUID) error
}

// UpdateInput identifies the order to rewrite; the embedded CreateInput
// supplies the same pricing/buyer material a fresh create would use.
// CreateInput.CustomerID and Source are ignored — an order never changes owner
// or origin — but are accepted so callers can pass one struct through both
// branches of the one-open-order rule.
type UpdateInput struct {
	OrderID uuid.UUID
	OrgID   uuid.UUID
	CreateInput
}

// UpdateOrderFromCheckout rewrites an open order against a pricing-confirmed
// checkout session and returns the refreshed aggregate. It writes an
// order_events 'created' row with the same shape the create path emits (plus
// "repeat": true) so the audit trail records each CREATE_ORDER_EXT attempt and
// the client-reported numbers that came with it.
//
// q must already be bound to the caller's transaction.
func UpdateOrderFromCheckout(ctx context.Context, q UpdateStore, in UpdateInput) (CreateResult, error) {
	cs, res, units, err := loadPricedAggregate(ctx, q, in.CreateInput)
	if err != nil {
		return CreateResult{}, err
	}

	subtotal := *cs.Subtotal
	discount := *cs.Discount
	charge := deref(cs.PlatformFee) + deref(cs.ProviderFee) + deref(cs.Tax)
	total := subtotal - discount + charge
	expiresAt := res.ExpiresAt

	order, err := q.UpdateOrderCheckout(
		ctx,
		in.OrderID, in.OrgID,
		in.ExternalRef,
		cs.ID, res.ID,
		*cs.Currency,
		subtotal, discount, charge, total,
		in.ChargePercentBP,
		cs.PromoCodeID,
		in.BuyerName, in.BuyerEmail, in.BuyerPhone,
		&expiresAt,
	)
	if err != nil {
		return CreateResult{}, fmt.Errorf("ordering: update order: %w", err)
	}

	// Old lines go before new ones: order_items has a (order_id, ordinal)
	// uniqueness expectation, and a shrunk cart would otherwise leave stale
	// high-ordinal units behind.
	if err := q.DeleteOrderItemsByOrder(ctx, order.ID); err != nil {
		return CreateResult{}, fmt.Errorf("ordering: clear order items: %w", err)
	}
	items, err := writeOrderItems(ctx, q, order.ID, units, discount, charge)
	if err != nil {
		return CreateResult{}, err
	}

	payload := map[string]any{
		"source":    order.Source,
		"repeat":    true,
		"unitCount": len(units),
		"subtotal":  subtotal,
		"discount":  discount,
		"charge":    charge,
		"total":     total,
		"currency":  *cs.Currency,
	}
	if in.ClientReported != nil {
		payload["client_reported"] = in.ClientReported
	}
	if _, err := q.InsertOrderEvent(ctx, order.ID, EventCreated, actorOrSystem(in.Actor), marshalPayload(payload)); err != nil {
		return CreateResult{}, fmt.Errorf("ordering: insert created event: %w", err)
	}

	return CreateResult{Order: order, Items: items}, nil
}
