// cmd_cart_get.go — the spec §7.5 GET_CART contract (feature #485, W1-A5c).
//
// GET_CART is the read-only twin of RESERVATION: it never mutates a hold and
// never refreshes the TTL, it only projects the cart the gateway session
// already owns. The wire shape differs from §7.4 in three ways the WordPress
// plugin depends on:
//
//   - the seats are GROUPED by action event, under actionEventList[], each
//     group carrying chargePercent — the channel service fee truncated to a
//     whole percent, because the legacy field is an int;
//   - the money keys are the long-form sum / discountAmount / chargeAmount /
//     totalSum. totalSum is the ONLY total: no totalAmount, no estimatedTotal
//     and no estimateTotal, whatever older plugin builds may have sent;
//   - an empty cart is a success, not an error: actionEventList is [], every
//     money field is 0, cartTimeout is 0 and resultCode is 0.
//
// discountAmount carries the promo discount of the gateway session's accepted
// codes (feature #491, spec §7.6), prorated onto the per-seat `discount` rows
// with the rounding remainder on the last row. It is 0 for a session with no
// accepted code, or when no accepted code yields a non-zero discount.
package hbil24

import (
	"net/http"

	"github.com/google/uuid"
)

// handleBil24GetCart serves spec §7.5. The credential / gateway-session /
// self-gate preamble mirrors handleBil24ReservationCart exactly — the two
// commands address the same cart and must reject the same requests the same
// way — after which the shared collectCart snapshot is regrouped.
func (h *Handler) handleBil24GetCart(w http.ResponseWriter, r *http.Request, req bil24Request) {
	ctx := r.Context()

	// Without the cart surface there is no session cart to read; answering -99
	// keeps the pre-#484 Handler builds (unit tests, degraded composition)
	// honest instead of reporting an empty cart that does not exist.
	if !h.cartDeps.wired() {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInternalError, "cart service unavailable",
		))
		return
	}

	// Like CREATE_USER, GET_CART always needs a resolved channel: the cart is
	// scoped to exactly one (org, channel) pair and chargePercent comes from
	// the channel fee, so an unresolved fid is -4 even when requireToken is
	// off. authenticateCommand accepts both the current settings.gateway
	// object and the legacy top-level gateway_token_hash.
	channel, authed := h.authenticateCommand(ctx, w, req)
	if !authed {
		if h.requireToken {
			// authenticateCommand already wrote the -4 envelope.
			return
		}
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeUnauthorized,
			"unknown fid or channel disabled",
		))
		return
	}

	locale := parseGatewaySettings(channel.Settings).DefaultLocale

	gw, ok := h.resolveGatewaySession(ctx, w, req, channel, locale)
	if !ok {
		return
	}
	if gw.ID == uuid.Nil {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInternalError, "cart service unavailable",
		))
		return
	}

	cc := cartCtx{
		gw:      gw,
		channel: channel,
		locale:  locale,
		ttl:     cartHoldTTL(channel),
		orgID:   gw.OrgID,
	}

	snap, err := h.collectCart(ctx, cc)
	if err != nil {
		h.writeCartTransient(w, req, cc)
		return
	}

	// Feature #491 / spec §7.6: apply the gateway session's accepted promo
	// codes. Only the first code that yields a non-zero discount is applied —
	// see cmd_promo.go and BEHAVIOR_DIFFERENCES.md.
	disc := h.sessionCartDiscount(ctx, promoCtx{cc: cc, snap: snap})

	writeBil24JSON(w, http.StatusOK, bil24OK(req.Command,
		getCartExtra(snap, cartFeePercent(channel), disc)))
}

// getCartExtra is the pure §7.5 projection of a cart snapshot: the money block
// plus actionEventList grouped by action event, in first-seen order so the
// response is stable across calls.
func getCartExtra(snap cartSnapshot, feePercent float64, disc cartDiscount) map[string]any {
	byEvent := make(map[any][]map[string]any, len(snap.events))
	for i, l := range snap.lines {
		// Per-row discount: the prorated share of the applied promo code,
		// 0 when no code applies (feature #491, spec §7.6).
		var rowDiscount int64
		if i < len(disc.perLine) {
			rowDiscount = disc.perLine[i]
		}
		byEvent[l.actionEventID] = append(byEvent[l.actionEventID], map[string]any{
			"seatId":          l.seatID,
			"categoryPriceId": l.categoryPriceID,
			"tariffPlanId":    nil,
			"price":           l.price,
			"discount":        rowDiscount,
		})
	}

	actionEventList := make([]map[string]any, 0, len(snap.events))
	for _, ev := range snap.events {
		seatList := byEvent[ev]
		if seatList == nil {
			seatList = make([]map[string]any, 0)
		}
		actionEventList = append(actionEventList, map[string]any{
			"actionEventId": ev,
			// Spec §7.5: chargePercent is an int — the fee percent truncated,
			// while chargeAmount below stays exact.
			"chargePercent": int64(feePercent),
			"seatList":      seatList,
		})
	}

	// discountAmount is the promo discount actually applied to this cart
	// (feature #491); chargeAmount and totalSum are computed on the NET sum,
	// so the fee follows the discount rather than the list price.
	discount := disc.total
	sum := snap.sum
	charge := float64(sum-discount) * feePercent / 100
	total := float64(sum-discount) + charge

	return map[string]any{
		"cartTimeout":     snap.timeout(),
		"currency":        snap.currency,
		"sum":             sum,
		"discountAmount":  discount,
		"chargeAmount":    charge,
		"totalSum":        total,
		"actionEventList": actionEventList,
	}
}
