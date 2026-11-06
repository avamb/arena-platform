package hfeed

import (
	"net/http"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// ErrCodePriceChanged answers a checkout/start whose expected_total is not
// the total arena priced the cart at.
const ErrCodePriceChanged = "checkout.price_changed"

// priceChangedDetails compares the total the selling site showed the buyer
// with the one arena computed (after discounts and the channel fee). It
// returns the error details and true when they differ; an absent
// expected_total never differs. A free cart compares too: a site that showed
// 10.00 must not silently hand out a 0 order.
func priceChangedDetails(expected *int64, bd hcheckout.PricingBreakdown) (map[string]any, bool) {
	if expected == nil || *expected == bd.Total {
		return nil, false
	}
	return map[string]any{
		"expected_total": *expected,
		"actual_total":   bd.Total,
		"currency":       bd.Currency,
	}, true
}

// refuseChangedPrice answers 409 checkout.price_changed and reports true when
// the cart's total is not the one the buyer saw. It runs after pricing and
// before the hold transaction commits, so the caller's deferred rollback
// leaves nothing behind. The idempotency middleware stores only a 2xx answer,
// so this refusal is never replayed for the key: the same key runs the check
// again, and the site normally retries with a new key and the new total.
func (h *Handler) refuseChangedPrice(w http.ResponseWriter, r *http.Request, expected *int64, bd hcheckout.PricingBreakdown) bool {
	details, changed := priceChangedDetails(expected, bd)
	if !changed {
		return false
	}
	httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelopeWithDetails(
		ErrCodePriceChanged,
		"The price of this cart has changed. Review the new total and confirm again.",
		r, details,
	))
	return true
}
