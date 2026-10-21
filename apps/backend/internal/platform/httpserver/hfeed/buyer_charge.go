// buyer_charge.go adds the platform's commission to the buyer's service
// charge when the organizer's billing terms say fee_mode buyer_pays. It is
// part of the ONE pricing path checkout/quote, checkout/start and checkout
// recover share (public_feed_pricing.go): all three take their rules from
// cartPricingRules, so the commission a quote shows is the commission the
// payment page asks for.
package hfeed

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// cartPricingRules is channelPricingRules (the channel's fee_percent) plus the
// buyer's commission of a buyer_pays organizer (PricingRules.BuyerCharge),
// resolved for the session's venue at "now". Every read runs on the pool, so
// callers take it BEFORE opening a hold transaction.
//
// ok=false is a read failure: the caller answers 503 (writePricingUnavailable)
// rather than price the cart without a commission the organizer's terms add,
// which would give the quote and the payment page two different totals. A
// defect in the billing terms themselves (no usable price list) is not a
// reason to refuse a sale: it is logged and the cart is priced without the
// commission; billing then rates the sale at what the buyer paid.
func (h *Handler) cartPricingRules(ctx context.Context, orgID, channelID, sessionID uuid.UUID) (hcheckout.PricingRules, bool) {
	rules := h.channelPricingRules(ctx, orgID, channelID)
	if h.publicFeedQueries == nil {
		return rules, true
	}
	bc, err := billing.LoadBuyerCharge(ctx, h.publicFeedQueries, orgID, sessionID, time.Now().UTC())
	if err != nil {
		if errors.Is(err, billing.ErrBuyerChargeTerms) {
			h.logger.Error("public_feed_checkout: buyer_pays terms unusable, pricing without the commission",
				slog.String("org_id", orgID.String()), slog.String("session_id", sessionID.String()),
				slog.String("error", err.Error()))
			return rules, true
		}
		h.logger.Error("public_feed_checkout: buyer commission lookup failed",
			slog.String("org_id", orgID.String()), slog.String("session_id", sessionID.String()),
			slog.String("error", err.Error()))
		return rules, false
	}
	rules.BuyerCharge = bc.Func()
	return rules, true
}

// feedBuyerCharge is the commission shown next to a session's categories in
// the public feed (service_charge_minor). Display only: a failure is logged
// and shows 0, because the buyer still sees the real amount in the quote and
// on the payment page.
func (h *Handler) feedBuyerCharge(ctx context.Context, orgID, sessionID uuid.UUID, now time.Time) *billing.BuyerCharge {
	if h.publicFeedQueries == nil {
		return nil
	}
	bc, err := billing.LoadBuyerCharge(ctx, h.publicFeedQueries, orgID, sessionID, now)
	if err != nil {
		h.logger.Warn("public_feed: buyer commission lookup failed",
			slog.String("session_id", sessionID.String()), slog.String("error", err.Error()))
		return nil
	}
	return bc
}

// writePricingUnavailable answers a cart that could not be priced because a
// pricing input could not be read. 503, never 502: Cloudflare replaces a 502
// body (AGENTS.md).
func writePricingUnavailable(w http.ResponseWriter, r *http.Request) {
	httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
		"checkout.pricing_unavailable", "We could not work out the price right now. Try again in a minute.", r,
	))
}
