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
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
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

	// The code's max_uses is taken with the order, under the code's lock
	// (P0-1 defect D-02), as on the widget and the gateway. This API knows
	// no buyer identity on the order, so the per-customer limit stays with
	// the completion check (completeCheckoutWithPromoTx, by user).
	if promoCodeID != nil {
		refusal, err := ReservePromoUseTx(ctx, txq, PromoUse{
			PromoCodeID: *promoCodeID, OrgID: reservation.OrgID,
			SessionID: &reservation.SessionID, ReservationID: &reservation.ID,
			CustomerID: buyer.CustomerID, BuyerEmail: derefStr(buyer.Email),
		})
		if err != nil {
			return gen.CheckoutSessionRow{}, err
		}
		if refusal != "" {
			return gen.CheckoutSessionRow{}, &PromoRefusalError{Code: refusal}
		}
	}

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
		if ch, chErr := txq.GetSalesChannelByIDAnyKind(ctx, cs.ChannelID, cs.OrgID); chErr == nil {
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

// PromoRefusalError is a promo code that had no use left for this order when
// the order was written. Code is PromoErrExhausted or PromoErrPerCustomerLimit.
type PromoRefusalError struct{ Code string }

func (e *PromoRefusalError) Error() string { return "promo code refused: " + e.Code }

// writePromoRefusal answers a PromoRefusalError with the same 409 the
// pre-transaction check (applyPromoCode) gives, and reports whether err was one.
func writePromoRefusal(w http.ResponseWriter, r *http.Request, err error) bool {
	var refusal *PromoRefusalError
	if !errors.As(err, &refusal) {
		return false
	}
	msg := "this promo code has reached its maximum number of uses"
	if refusal.Code == PromoErrPerCustomerLimit {
		msg = "you have already used this promo code the maximum number of times"
	}
	httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(refusal.Code, msg, r))
	return true
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
