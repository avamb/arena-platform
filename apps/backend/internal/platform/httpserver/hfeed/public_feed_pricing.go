// public_feed_pricing.go holds the pieces of pricing a public cart that
// checkout/start and checkout/quote MUST share, so the amount a buyer is quoted
// is the amount the payment page asks for by construction, not by a second copy
// of the same rules staying in step.
//
//   - priceGAItems / gaPricingLines / cartCurrency: the general-admission lines
//     of a cart and the currency the cart is priced in;
//   - findPromoCode / applyPromoDiscount (public_feed_checkout.go): which code
//     a buyer's text names and whether it fits the cart;
//   - writePublicPromoError: the buyer-facing verdict on a code that does not.
//
// The seated lines come from seatPricingLines and the money from
// hcheckout.ComputePricingLines; both were already shared.
package hfeed

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// Caps on one public cart, shared by checkout/start and checkout/quote. A
// request is priced with one database read per line and per distinct seat
// category, so an unbounded list is an unbounded amount of work for one
// anonymous request; the caps sit far above any real purchase.
const (
	// maxCartGALines is the most general-admission lines (categories) in one cart.
	maxCartGALines = 20
	// maxLineQuantity is the most tickets on one general-admission line. It is
	// the cap checkout/start has always enforced on its legacy qty field.
	maxLineQuantity = 50
	// maxCartSeats is the most assigned seats in one cart.
	maxCartSeats = 100
)

// checkCartCaps answers "" for a cart within the caps, otherwise the stable
// error code to refuse it with (always a 400, before any database work):
// checkout.cart_too_large for too many lines or seats, checkout.invalid_qty
// for a line over maxLineQuantity.
func checkCartCaps(seats []string, items []PublicGAItem) (code, message string) {
	if len(items) > maxCartGALines {
		return "checkout.cart_too_large", fmt.Sprintf("a cart holds at most %d ticket lines", maxCartGALines)
	}
	if len(seats) > maxCartSeats {
		return "checkout.cart_too_large", fmt.Sprintf("a cart holds at most %d seats", maxCartSeats)
	}
	for i, it := range items {
		if it.Quantity > maxLineQuantity {
			return "checkout.invalid_qty", fmt.Sprintf("ga_items[%d].quantity must be at most %d", i, maxLineQuantity)
		}
	}
	return "", ""
}

// gaItemPriced is one general-admission line with its platform-resolved unit
// price (the tier's price window at "now").
type gaItemPriced struct {
	tierID    uuid.UUID
	qty       int32
	unitPrice int64
	currency  string
}

// priceGAItems resolves every GA item of a cart against the session's tiers.
// tierIDs are the already-parsed tier_id of each item, index for index. The
// returned code is "" on success; otherwise one of the codes writePricingError
// answers: dependency.tier_unavailable, checkout.tier_not_found,
// checkout.tier_lookup_failed, checkout.pwyw_not_supported and friends.
//
// Reads go through the pool: callers run this BEFORE any hold transaction.
func (h *Handler) priceGAItems(ctx context.Context, sessionID uuid.UUID, items []PublicGAItem, tierIDs []uuid.UUID) ([]gaItemPriced, string) {
	if len(items) == 0 {
		return nil, ""
	}
	if h.tierQueries == nil {
		return nil, "dependency.tier_unavailable"
	}
	priced := make([]gaItemPriced, 0, len(items))
	for i, item := range items {
		tierID := tierIDs[i]
		tier, err := h.tierQueries.GetTicketTierByID(ctx, tierID, sessionID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, "checkout.tier_not_found"
			}
			h.logger.Error("public_feed_checkout: tier lookup failed",
				slog.String("tier_id", tierID.String()),
				slog.String("error", err.Error()),
			)
			return nil, "checkout.tier_lookup_failed"
		}
		unitPrice, priceErr := h.publicTierUnitPrice(ctx, h.tierQueries, tier)
		if priceErr != "" {
			return nil, priceErr
		}
		priced = append(priced, gaItemPriced{tierID: tierID, qty: item.Quantity, unitPrice: unitPrice, currency: tier.Currency})
	}
	return priced, ""
}

// gaPricingLines turns the priced GA items into pricing-pipeline input
// (guardrail #15: every line is priced platform-side).
func gaPricingLines(priced []gaItemPriced) []hcheckout.PricingLineInput {
	lines := make([]hcheckout.PricingLineInput, 0, len(priced))
	for _, g := range priced {
		lines = append(lines, hcheckout.PricingLineInput{
			TierID:    g.tierID.String(),
			Quantity:  g.qty,
			UnitPrice: g.unitPrice,
		})
	}
	return lines
}

// cartCurrency is the currency a cart is priced in: the first GA line's when
// the cart has one, otherwise the seats' (which may be "" when no seat has a
// tier bound — the caller decides the fallback).
func cartCurrency(priced []gaItemPriced, seatCurrency string) string {
	if len(priced) > 0 {
		return priced[0].currency
	}
	return seatCurrency
}

// errPromoUnavailable is findPromoCode's answer when the promo store is not
// wired: callers answer it as a failed lookup (500), never as "no code".
var errPromoUnavailable = errors.New("promo store unavailable")

// findPromoCode resolves the text a buyer typed to one of the organization's
// promo codes. Buyers type codes by hand, so the match ignores case and the
// stray spaces of a paste, like the Bil24 gateway's. A blank input is "no
// code" (nil, nil); an unknown one is pgx.ErrNoRows. The lookup is scoped to
// the organization, so another organization's code does not exist here.
func (h *Handler) findPromoCode(ctx context.Context, orgID uuid.UUID, raw string) (*gen.PromoCodeRow, error) {
	code := strings.TrimSpace(raw)
	if code == "" {
		return nil, nil
	}
	if h.promoQueries == nil {
		// A code was named and cannot be checked: fail closed. Charging the
		// full price for a cart the buyer was promised a discount on, or the
		// reverse, is worse than refusing.
		return nil, errPromoUnavailable
	}
	row, err := h.promoQueries.GetPromoCodeByCodeCI(ctx, orgID, code)
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// Public promo verdicts. The platform's internal checks are finer than what a
// buyer may be told: within one organization, "this code exists but belongs to
// another event" must read exactly like "no such code". Everything that would
// show the shape of a code the cart is not entitled to collapses into
// promoNotApplicable; only verdicts reached AFTER the code is proven to fit the
// cart (a usage cap, the minimum order) name their cause.
const (
	promoNotApplicable = "promo.not_applicable"
	promoMinimum       = "promo.minimum_not_reached"
)

// publicPromoError maps an internal promo verdict onto the stable public code
// and a neutral message.
func publicPromoError(internal string) (code, message string) {
	switch internal {
	case hcheckout.PromoErrExhausted:
		return hcheckout.PromoErrExhausted, "This code has been used the maximum number of times."
	case hcheckout.PromoErrPerCustomerLimit:
		return hcheckout.PromoErrPerCustomerLimit, "You have already used this code the maximum number of times."
	case "promo.invalid_order_amount":
		return promoMinimum, "The order is below the minimum amount for this code."
	default:
		return promoNotApplicable, "This code does not apply to this order."
	}
}

// writePublicPromoError answers a refused promo code on a public route: 422
// with the stable public code, or 500 when the check itself failed (a cap the
// platform cannot verify is never waved through).
func writePublicPromoError(w http.ResponseWriter, r *http.Request, internal string) {
	if internal == "checkout.promo_lookup_failed" {
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			internal, "failed to check the promo code", r,
		))
		return
	}
	code, msg := publicPromoError(internal)
	httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelope(code, msg, r))
}
