//go:build integration

// promo_d02_followup_p0_integration_test.go — P0-1 defect D-02, the cases the
// review of PR #346 asked for (review-345-346-2026-11-02, findings 1 and 3):
//
//   - one buyer, no race: an unpaid order on one date must count against a
//     max_uses=1 code when the same buyer checks out another date;
//   - a recovered widget checkout takes the code under the same lock, drops
//     it when the code was used up meanwhile, and its order follows the new
//     hold, so the recovered use counts for the next buyer;
//   - the authenticated checkout API (POST /v1/checkout/{id}/confirm) counts
//     the orders other checkouts wrote with the code.
package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
)

// d02Fixture is a hosted-checkout organization with one max_uses=1 code.
type d02Fixture struct {
	*hostedOrgFixture
	srv    *Server
	q      *gen.Queries
	code   string
	promo  uuid.UUID
	buyers []string
}

func newD02Fixture(t *testing.T, ctx context.Context, label string) *d02Fixture {
	t.Helper()
	pool := integrationPool(t)
	f := &d02Fixture{hostedOrgFixture: newHostedOrgFixture(t, ctx, pool, label), q: gen.New(pool)}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `UPDATE checkout_sessions SET promo_code_id = NULL WHERE org_id = $1`, f.orgID)
		f.cleanupPromos()
		for _, b := range f.buyers {
			_, _ = pool.Exec(c, `DELETE FROM customers WHERE id IN
				(SELECT customer_id FROM customer_identities WHERE value_normalized = $1)`, b)
		}
		f.cleanup()
	})
	f.srv = buildHostedCheckoutServer(t, pool, newStubStripe(t).baseURL())
	f.code = "D02" + strings.ToUpper(label[:3]) + f.orgID.String()[:6]
	f.promo = f.insertPromo(t, ctx, f.code, "percent", 50, ", max_uses", ", 1")
	return f
}

// buyer is a fresh e-mail of this run (customer identities are global).
func (f *d02Fixture) buyer(name string) string {
	mail := fmt.Sprintf("d02-%s-%s@arena-integration.test", name, f.orgID.String()[:8])
	f.buyers = append(f.buyers, mail)
	return mail
}

// start sends checkout/start with the code for one place of session/tier.
func (f *d02Fixture) start(t *testing.T, mail string, session, tier uuid.UUID) (int, []byte) {
	t.Helper()
	return f.postJSON(t, f.srv, f.feedToken, "/checkout/start", map[string]any{
		"session_id": session.String(),
		"ga_items":   []map[string]any{{"tier_id": tier.String(), "quantity": 1}},
		"promo_code": f.code,
		"buyer": map[string]any{
			"email": mail, "name": "D02 Buyer",
			"phone": fmt.Sprintf("+3635%07d", time.Now().UnixNano()%10_000_000),
		},
		"return_url": hostedTicketsBaseURL + "/p0",
	})
}

func (f *d02Fixture) mustStart(t *testing.T, mail string, session, tier uuid.UUID) hostedStartResponse {
	t.Helper()
	code, body := f.start(t, mail, session, tier)
	if code != http.StatusCreated {
		t.Fatalf("checkout/start of %s = %d; body: %s", mail, code, body)
	}
	var s hostedStartResponse
	if err := json.Unmarshal(body, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func wantPromoRefusal(t *testing.T, what string, code int, body []byte, want int, errCode string) {
	t.Helper()
	if code != want || !strings.Contains(string(body), `"`+errCode+`"`) {
		t.Errorf("%s = %d %s, want %d %s", what, code, body, want, errCode)
	}
}

// TestORD08_OneBuyerTwoDatesInARowWithAMaxUsesOneCode — finding 1: one buyer
// orders date A with the code and does not pay, then orders date B with the
// same code. Before 3c338865 the buyer's own unpaid order did not count on
// ANY date, so both orders got the discount and both payments recorded a
// redemption of a max_uses=1 code.
func TestORD08_OneBuyerTwoDatesInARowWithAMaxUsesOneCode(t *testing.T) {
	ctx := t.Context()
	f := newD02Fixture(t, ctx, "seq")
	session2, tier2 := f.addSecondDate(t, ctx)
	mail := f.buyer("one")

	first := f.mustStart(t, mail, f.sessionID, f.tierID)
	code, body := f.start(t, mail, session2, tier2)
	wantPromoRefusal(t, "second date of the same buyer", code, body, http.StatusUnprocessableEntity, "promo.exhausted")

	// The first order is paid and is the code's only use.
	csID := uuid.MustParse(first.CheckoutSession.ID)
	intents, err := f.q.ListPaymentIntentsByCheckout(ctx, csID)
	if err != nil || len(intents) != 1 || intents[0].ProviderPaymentID == nil {
		t.Fatalf("payment intent: %v", err)
	}
	paid := sessionCompletedEvent(*intents[0].ProviderPaymentID, "pi_d02seq_"+uuid.NewString()[:8], "paid")
	if c, b := postSignedWebhookTo(t, f.srv, f.configWebhookPath(), paid, f.webhookSecret); c != http.StatusOK || !processed(t, b) {
		t.Fatalf("paid event = %d; body: %s", c, b)
	}
	var redemptions, withCode int
	if err := f.pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM promo_code_redemptions WHERE promo_code_id = $1),
		(SELECT count(*) FROM orders WHERE promo_code_id = $1)`, f.promo).Scan(&redemptions, &withCode); err != nil {
		t.Fatal(err)
	}
	if redemptions != 1 || withCode != 1 {
		t.Errorf("redemptions = %d, orders with the code = %d; want 1 and 1", redemptions, withCode)
	}
}

// expireHold ends the hold behind a checkout as the hold sweep would, before
// the order sweep reached its order: the places are free again and the
// order, still waiting for its payment, is past its expiry.
func (f *d02Fixture) expireHold(t *testing.T, ctx context.Context, csID uuid.UUID) {
	t.Helper()
	cs, err := f.q.GetCheckoutSessionByID(ctx, csID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hcheckout.ReleaseHold(ctx, f.pool, f.q, cs.ReservationID); err != nil {
		t.Fatalf("release hold: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE orders SET expires_at = now() - interval '1 minute'
		WHERE checkout_session_id = $1`, csID); err != nil {
		t.Fatal(err)
	}
}

func (f *d02Fixture) recover(t *testing.T, token string) map[string]any {
	t.Helper()
	code, body := f.postJSONAt(t, "/v1/public/checkout/"+token+"/recover")
	if code != http.StatusOK {
		t.Fatalf("recover = %d; body: %s", code, body)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func (f *d02Fixture) postJSONAt(t *testing.T, path string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	f.srv.router.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

// TestORD08_RecoveredCheckoutTakesTheCodeUnderTheLock — finding 3, the widget
// half. A recovered checkout used to re-check the code without its limits and
// left its order on the dead hold, so its use never counted again.
func TestORD08_RecoveredCheckoutTakesTheCodeUnderTheLock(t *testing.T) {
	ctx := t.Context()
	f := newD02Fixture(t, ctx, "rec")
	a, b, c := f.buyer("a"), f.buyer("b"), f.buyer("c")

	// A takes the code, the hold ends, A recovers: nobody else used the
	// code, so A keeps the discount.
	sa := f.mustStart(t, a, f.sessionID, f.tierID)
	csA := uuid.MustParse(sa.CheckoutSession.ID)
	f.expireHold(t, ctx, csA)
	f.recover(t, sa.CheckoutToken)
	cs, err := f.q.GetCheckoutSessionByID(ctx, csA)
	if err != nil {
		t.Fatal(err)
	}
	if cs.PromoCodeID == nil || cs.Discount == nil || *cs.Discount == 0 {
		t.Fatalf("recovered checkout lost an unused code: promo %v discount %v", cs.PromoCodeID, cs.Discount)
	}
	// The order followed the checkout onto the new hold.
	ord, err := f.q.GetOrderByCheckoutSession(ctx, csA)
	if err != nil {
		t.Fatal(err)
	}
	if ord.ReservationID != cs.ReservationID || ord.ExpiresAt == nil || !ord.ExpiresAt.After(time.Now()) {
		t.Fatalf("order after recover: reservation %s (checkout %s), expires %v; want the new live hold",
			ord.ReservationID, cs.ReservationID, ord.ExpiresAt)
	}
	if ord.PromoCodeID == nil || ord.Discount != *cs.Discount || ord.Total != *cs.Total {
		t.Errorf("order money after recover: promo %v discount %d total %d; checkout discount %d total %d",
			ord.PromoCodeID, ord.Discount, ord.Total, *cs.Discount, *cs.Total)
	}

	// So the recovered use counts: C is refused.
	code, body := f.start(t, c, f.sessionID, f.tierID)
	wantPromoRefusal(t, "checkout after a recovered use", code, body, http.StatusUnprocessableEntity, "promo.exhausted")

	// A's hold ends again, B takes the code meanwhile, A recovers: the code
	// is dropped from A's checkout and from A's order.
	f.expireHold(t, ctx, csA)
	f.mustStart(t, b, f.sessionID, f.tierID)
	f.recover(t, sa.CheckoutToken)
	cs, err = f.q.GetCheckoutSessionByID(ctx, csA)
	if err != nil {
		t.Fatal(err)
	}
	if cs.PromoCodeID != nil || (cs.Discount != nil && *cs.Discount != 0) {
		t.Errorf("recovered checkout kept a code another buyer used meanwhile: promo %v discount %v", cs.PromoCodeID, cs.Discount)
	}
	if ord, err = f.q.GetOrderByCheckoutSession(ctx, csA); err != nil {
		t.Fatal(err)
	}
	if ord.PromoCodeID != nil || ord.Discount != 0 {
		t.Errorf("order kept the dropped code: promo %v discount %d", ord.PromoCodeID, ord.Discount)
	}
	var live int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM orders WHERE promo_code_id = $1
		AND status = 'pending_payment' AND expires_at > now()`, f.promo).Scan(&live); err != nil {
		t.Fatal(err)
	}
	if live != 1 {
		t.Errorf("live orders carrying the max_uses=1 code = %d, want 1 (B's)", live)
	}
}

// TestORD08_RestConfirmCountsOrdersOfOtherCheckouts — finding 3, the
// authenticated checkout API: its pre-check counted recorded redemptions
// only, so two confirmed checkouts both carried a max_uses=1 code.
func TestORD08_RestConfirmCountsOrdersOfOtherCheckouts(t *testing.T) {
	e := newSweepEnv(t)
	w := e.newWorld(t, e.orgB, e.orgB)
	key := e.checkoutKey(t)
	code := "D02REST" + strings.ToUpper(uuid.NewString()[:6])
	var promo uuid.UUID
	if err := e.pool.QueryRow(context.Background(), `INSERT INTO promo_codes (org_id, code, discount_type, discount_value, max_uses)
		VALUES ($1, $2, 'percent', 10, 1) RETURNING id`, e.orgB, code).Scan(&promo); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = e.pool.Exec(c, `DELETE FROM order_items WHERE order_id IN (SELECT id FROM orders WHERE promo_code_id = $1)`, promo)
		_, _ = e.pool.Exec(c, `DELETE FROM order_events WHERE order_id IN (SELECT id FROM orders WHERE promo_code_id = $1)`, promo)
		_, _ = e.pool.Exec(c, `DELETE FROM orders WHERE promo_code_id = $1`, promo)
		_, _ = e.pool.Exec(c, `UPDATE checkout_sessions SET promo_code_id = NULL WHERE promo_code_id = $1`, promo)
		_, _ = e.pool.Exec(c, `DELETE FROM promo_codes WHERE id = $1`, promo)
	})

	statuses := make([]int, 2)
	bodies := make([]map[string]any, 2)
	for i := range statuses {
		res, cs := e.seedReservationAndCheckout(t, w)
		e.exec(t, `INSERT INTO reservation_ga_items (reservation_id, tier_id, quantity, unit_price)
		           VALUES ($1, $2, 1, 2500)`, res, w.tier)
		statuses[i], bodies[i] = e.do(t, "POST", "/v1/checkout/"+cs.String()+"/confirm", key,
			fmt.Sprintf(`{"promo_code":%q}`, code))
	}
	if statuses[0] != http.StatusOK {
		t.Fatalf("first confirm = %d %v", statuses[0], bodies[0])
	}
	errObj, _ := bodies[1]["error"].(map[string]any)
	if statuses[1] != http.StatusConflict || errObj["code"] != "promo.exhausted" {
		t.Errorf("second confirm = %d %v, want 409 promo.exhausted", statuses[1], bodies[1])
	}
	var withCode int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM orders WHERE promo_code_id = $1`, promo).Scan(&withCode); err != nil {
		t.Fatal(err)
	}
	if withCode != 1 {
		t.Errorf("orders written with the max_uses=1 code = %d, want 1", withCode)
	}
}
