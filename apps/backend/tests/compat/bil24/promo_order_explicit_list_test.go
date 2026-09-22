//go:build integration

// CREATE_ORDER_EXT and the documented `promoCodeList` key (spec §7.7 step 6).
//
// The gateway session's promo_codes array only ever grows — ADD_PROMO_CODES
// appends, and the protocol has no command to take a code off again — so a
// buyer who applied a code in the site's picker and then removed it would
// still be discounted at CREATE_ORDER_EXT if the order only ever looked at
// the session. Since 2026-09-22 the WordPress plugin sends the picker's
// CURRENT code, or `[]`, as `promoCodeList` with every CREATE_ORDER_EXT, and
// the gateway treats that key as the whole truth. The older `promoCodes: []`
// spelling (what the checked-in `CREATE_ORDER_EXT/ga` fixture carries) must
// keep meaning "use the session" — every site on the older plugin relies on
// it — which scenario 02 already proves.

package compat_bil24_test

import (
	"strconv"
	"testing"

	"github.com/google/uuid"
)

func TestCompatBil24_CreateOrder_PromoCodeListIsTheWholeTruth(t *testing.T) {
	st := setupHarness(t)
	base := startHarnessServer(t, st)

	gaActionEventID := mustActionEventID(t, st, st.GAsessID)
	gaTierWireID := sc6TierWireID(t, st, sc2EarlyBirdTierID(t, st))

	// A buyer of its own: the one-open-order rule is per customer+session.
	sess, user := createGatewayUser(t, base, st, "harness-promo-list-"+uuid.NewString()[:8]+"@example.test")
	runtime := map[string]string{
		"actionEventId":   strconv.FormatInt(gaActionEventID, 10),
		"categoryPriceId": strconv.FormatInt(gaTierWireID, 10),
		"sessionId":       sess,
	}
	stamp := func(req map[string]interface{}) map[string]interface{} {
		req = resolveGolden(req, st, runtime)
		req["fid"] = st.ChannelFID
		req["token"] = st.ChannelToken
		req["userId"] = user
		return req
	}

	// ── hold 2 GA units, exactly as scenario 02 does ────────────────────────
	reqReserve, _ := loadWPFixture(t, "RESERVATION", "reserve_by_category")
	reqReserve = stamp(reqReserve)
	reqReserve["categoryList"] = []any{map[string]any{
		"categoryPriceId": gaTierWireID,
		"quantity":        sc2Quantity,
		"tariffPlanId":    nil,
	}}
	sc2RequireOK(t, "RESERVATION", postBil24(t, base, reqReserve))

	// ── the buyer applied WAVE1 in the picker: it is now ON the session ─────
	reqPromo, _ := loadWPFixture(t, "ADD_PROMO_CODES", "basic")
	promo := postBil24(t, base, stamp(reqPromo))
	sc2RequireOK(t, "ADD_PROMO_CODES", promo)

	// ── then took it off again: the plugin sends promoCodeList: [] ──────────
	externalRef := "wc-promo-list-" + uuid.NewString()[:8]
	reqOrder, _ := loadWPFixture(t, "CREATE_ORDER_EXT", "ga")
	reqOrder = stamp(reqOrder)
	reqOrder["orderId"] = externalRef
	reqOrder["promoCodeList"] = []any{}
	first := postBil24(t, base, reqOrder)
	sc2RequireOK(t, "CREATE_ORDER_EXT promoCodeList=[]", first)
	sc6AssertMoney(t, "promoCodeList=[]", first, sc6Money{
		sum: sc2Gross, discount: 0, charge: sc2ChargeNoPromo, total: sc2TotalNoPromo, currency: sc2Currency,
	})
	firstOrderID := sc6OrderID(t, st, "promoCodeList=[]", first)

	// ── applied it once more and bought: promoCodeList names the code ───────
	// Same buyer, same live hold: spec §7.7 step 5 answers the SAME order,
	// re-priced — so the discount must appear on it now.
	reqOrder2, _ := loadWPFixture(t, "CREATE_ORDER_EXT", "ga")
	reqOrder2 = stamp(reqOrder2)
	reqOrder2["orderId"] = externalRef
	reqOrder2["promoCodeList"] = []any{"WAVE1"}
	second := postBil24(t, base, reqOrder2)
	sc2RequireOK(t, "CREATE_ORDER_EXT promoCodeList=[WAVE1]", second)
	sc6AssertMoney(t, "promoCodeList=[WAVE1]", second, sc6Money{
		sum: sc2Gross, discount: sc2Discount, charge: sc2Charge, total: sc2Total, currency: sc2Currency,
	})
	if got := sc6OrderID(t, st, "promoCodeList=[WAVE1]", second); got != firstOrderID {
		t.Errorf("second CREATE_ORDER_EXT answered order %s, want the same open order %s", got, firstOrderID)
	}
}
