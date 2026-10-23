// buyer_charge.go adds a buyer_pays organizer's platform commission to the
// Bil24 cart's service charge. The cart's `charge` / `chargeAmount` is the
// channel's fee_percent rounded half away from zero (feeChargeMinor, spec 20
// §5) PLUS the commission, a whole number of minor units per ticket that
// needs no rounding. Every money view of a cart (RESERVATION, GET_CART,
// CHECK_KDP) and CREATE_ORDER_EXT go through cartChargeMinor, so the charge
// the site shows is the charge the order is written with.
package hbil24

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
)

// errBuyerChargeUnavailable is logged when the commission could not be read.
var errBuyerChargeUnavailable = errors.New("buyer commission unavailable")

// WithBuyerCharges wires the billing reads that resolve a buyer_pays
// organizer's commission (billing.LoadBuyerCharge). Nil (every unit test that
// builds a Handler without a pool) means no commission is ever added, which
// is the organizer_pays behaviour. Returns the receiver for chaining.
func (h *Handler) WithBuyerCharges(q billing.BuyerChargeQueries) *Handler {
	h.buyerChargeQ = q
	return h
}

// buyerChargeFor loads the commission a buyer of one event session pays.
// ok=false is a read failure the caller answers as a transient error (-1):
// pricing the cart without a commission the organizer's terms add would show
// the buyer one total and write the order with another. A defect in the
// terms themselves is logged and priced without the commission.
func (h *Handler) buyerChargeFor(ctx context.Context, orgID, sessionID uuid.UUID) (*billing.BuyerCharge, bool) {
	if h.buyerChargeQ == nil || orgID == uuid.Nil || sessionID == uuid.Nil {
		return nil, true
	}
	bc, err := billing.LoadBuyerCharge(ctx, h.buyerChargeQ, orgID, sessionID, time.Now().UTC())
	if err != nil {
		if errors.Is(err, billing.ErrBuyerChargeTerms) {
			h.logger.Error("bil24_compat: buyer_pays terms unusable, pricing without the commission",
				slog.String("org_id", orgID.String()), slog.String("session_id", sessionID.String()),
				slog.String("error", err.Error()))
			return nil, true
		}
		h.logger.Error("bil24_compat: buyer commission lookup failed",
			slog.String("org_id", orgID.String()), slog.String("session_id", sessionID.String()),
			slog.String("error", err.Error()))
		return nil, false
	}
	return bc, true
}

// snapCommission is the buyer commission on a whole cart snapshot: one
// BuyerCharge per event session in it, applied to every unit at its price.
// The charge comes on top of the percentage only while the net sum is above
// zero, so a fully discounted (or free) cart stays free.
func (h *Handler) snapCommission(ctx context.Context, orgID uuid.UUID, snap cartSnapshot, net int64) (int64, bool) {
	if h.buyerChargeQ == nil || net <= 0 {
		return 0, true
	}
	by := map[uuid.UUID]*billing.BuyerCharge{}
	var sum int64
	for _, l := range snap.lines {
		bc, seen := by[l.sessionID]
		if !seen {
			var ok bool
			bc, ok = h.buyerChargeFor(ctx, orgID, l.sessionID)
			if !ok {
				return 0, false
			}
			by[l.sessionID] = bc
		}
		sum += bc.PerTicket(snap.currency, l.price, l.seated)
	}
	return sum, true
}

// cartChargeMinor is the cart's whole service charge: the channel percentage
// with the gateway's rounding on the net sum, plus the buyer commission.
func cartChargeMinor(netMinor int64, feePercent float64, commission int64) int64 {
	if netMinor <= 0 {
		commission = 0
	}
	return feeChargeMinor(netMinor, feePercent) + commission
}

// gatewayPricingRules are the CREATE_ORDER_EXT pricing rules: the channel's
// fee_percent in basis points (re-stated with the cart's rounding by
// applyGatewayCharge right after) and the buyer commission of the session.
func gatewayPricingRules(feePercent float64, bc *billing.BuyerCharge) hcheckout.PricingRules {
	// PlatformFeeRate is in basis points, fee_percent is a percentage:
	// percent × 100 = bp (percentScale, not a money conversion).
	return hcheckout.PricingRules{
		PlatformFeeRate: int64(feePercent * percentScale),
		BuyerCharge:     bc.Func(),
	}
}
