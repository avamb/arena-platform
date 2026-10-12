//go:build integration

// public_feed_quote_integration_test.go — live-DB, real-router coverage of
//
//	POST /v1/public/feeds/{feed_token}/checkout/quote
//
// the read-only price the widget's cart sheet shows before the buyer pays. The
// guarantees that matter:
//
//   - the quote IS the amount checkout/start stores and sends to the payment
//     page (same pipeline: channel fee, tier prices, promo, basis-point floor);
//   - a quote leaves nothing behind: no reservation, no held place, no reserved
//     capacity, no checkout session, order or payment intent, no provider call;
//   - a code that does not fit the cart is answered with the neutral
//     promo.not_applicable whatever the reason, and a usage cap (total or
//     per-customer) is named only for a code that does fit.
//
// Prerequisites: DATABASE_URL against a migrated database.
//
// Run with:
//
//	go test -tags integration ./apps/backend/internal/platform/httpserver/ \
//	    -run TestPublicQuote
package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// quoteBody is the decoded 200 answer.
type quoteBody struct {
	Pricing struct {
		Subtotal    int64  `json:"subtotal"`
		Discount    int64  `json:"discount"`
		PlatformFee int64  `json:"platform_fee"`
		Total       int64  `json:"total"`
		Currency    string `json:"currency"`
		Quantity    int32  `json:"quantity"`
	} `json:"pricing"`
	PromoApplied bool `json:"promo_applied"`
}

type quoteErrBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (f *hostedOrgFixture) postJSON(t *testing.T, srv *Server, token, path string, payload map[string]any) (int, []byte) {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/public/feeds/"+token+path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.router.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

// gaCart is the fixture's GA cart: qty places of the 25.00 EUR category.
func (f *hostedOrgFixture) gaCart(qty int, promo, email string) map[string]any {
	p := map[string]any{
		"session_id": f.sessionID.String(),
		"ga_items":   []map[string]any{{"tier_id": f.tierID.String(), "quantity": qty}},
	}
	if promo != "" {
		p["promo_code"] = promo
	}
	if email != "" {
		p["buyer"] = map[string]any{"email": email}
	}
	return p
}

func (f *hostedOrgFixture) quote(t *testing.T, srv *Server, payload map[string]any) (int, []byte) {
	t.Helper()
	return f.postJSON(t, srv, f.feedToken, "/checkout/quote", payload)
}

func (f *hostedOrgFixture) mustQuote(t *testing.T, srv *Server, payload map[string]any) quoteBody {
	t.Helper()
	code, body := f.quote(t, srv, payload)
	if code != http.StatusOK {
		t.Fatalf("checkout/quote = %d, want 200; body: %s", code, body)
	}
	var q quoteBody
	if err := json.Unmarshal(body, &q); err != nil {
		t.Fatalf("decode quote: %v (body: %s)", err, body)
	}
	return q
}

func (f *hostedOrgFixture) quoteErr(t *testing.T, srv *Server, payload map[string]any, wantStatus int) quoteErrBody {
	t.Helper()
	code, body := f.quote(t, srv, payload)
	if code != wantStatus {
		t.Fatalf("checkout/quote = %d, want %d; body: %s", code, wantStatus, body)
	}
	var e quoteErrBody
	if err := json.Unmarshal(body, &e); err != nil {
		t.Fatalf("decode error envelope: %v (body: %s)", err, body)
	}
	return e
}

// startWithPromo drives the real checkout/start with a promo code.
func (f *hostedOrgFixture) startWithPromo(t *testing.T, srv *Server, qty int, promo string) (int, []byte) {
	t.Helper()
	p := f.gaCart(qty, promo, "")
	p["buyer"] = map[string]any{"email": f.buyerMail, "name": "Quote Buyer", "phone": f.buyerPhone}
	return f.postJSON(t, srv, f.feedToken, "/checkout/start", p)
}

// insertPromo adds a promo code to the fixture's organization and returns its
// id. extra is appended to the column list verbatim.
func (f *hostedOrgFixture) insertPromo(t *testing.T, ctx context.Context, code, discountType string, value int64, extraCols, extraVals string, args ...any) uuid.UUID {
	t.Helper()
	id := uuid.New()
	sql := `INSERT INTO promo_codes (id, org_id, code, discount_type, discount_value` + extraCols + `)
	        VALUES ($1, $2, $3, $4, $5` + extraVals + `)`
	all := append([]any{id, f.orgID, code, discountType, value}, args...)
	if _, err := f.pool.Exec(ctx, sql, all...); err != nil {
		t.Fatalf("insert promo %s: %v", code, err)
	}
	return id
}

// cleanupPromos removes every promo (and redemption) of the fixture's org. It
// must run BEFORE the fixture's own cleanup deletes the orders redemptions
// point at, i.e. be deferred AFTER it.
func (f *hostedOrgFixture) cleanupPromos() {
	ctx := context.Background()
	for _, sql := range []string{
		`DELETE FROM promo_code_redemptions WHERE promo_code_id IN (SELECT id FROM promo_codes WHERE org_id = $1)`,
		`UPDATE orders SET promo_code_id = NULL WHERE org_id = $1`,
		`DELETE FROM promo_codes WHERE org_id = $1`,
	} {
		if _, err := f.pool.Exec(ctx, sql, f.orgID); err != nil {
			f.t.Logf("quote promo cleanup (%.50s): %v", sql, err)
		}
	}
}

// nothingCreated proves the org has no checkout, order, payment intent or
// reservation, and the provider was never called: what a quote must leave.
func (f *hostedOrgFixture) nothingCreated(t *testing.T, ctx context.Context, stripe *stubStripe) {
	t.Helper()
	for _, tbl := range []string{"checkout_sessions", "orders", "payment_intents"} {
		var n int
		if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM `+tbl+` WHERE org_id = $1`, f.orgID).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", tbl, err)
		}
		if n != 0 {
			t.Errorf("%s rows after a quote = %d, want 0", tbl, n)
		}
	}
	var res int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM reservations WHERE org_id = $1`, f.orgID).Scan(&res); err != nil {
		t.Fatalf("count reservations: %v", err)
	}
	if res != 0 {
		t.Errorf("reservations after a quote = %d, want 0", res)
	}
	if n := len(stripe.sessions); n != 0 {
		t.Errorf("a quote called the payment provider %d time(s)", n)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// The quote equals what checkout/start stores and sends to the payment page
// ─────────────────────────────────────────────────────────────────────────────

func TestPublicQuote_EqualsStartTotal_AndLeavesNothingBehind(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()

	f := newHostedOrgFixture(t, ctx, pool, "q1")
	defer f.cleanup()
	defer f.cleanupPromos()

	stripe := newStubStripe(t)
	srv := buildHostedCheckoutServer(t, pool, stripe.baseURL())
	f.insertPromo(t, ctx, "TEN", "percent", 10, "", "")

	before := f.footprint(t, ctx)

	// No code: 2 × 25.00 = 5000; the channel's 1.25 % floors to 62.
	plain := f.mustQuote(t, srv, f.gaCart(2, "", ""))
	if plain.Pricing.Subtotal != 5000 || plain.Pricing.Discount != 0 ||
		plain.Pricing.PlatformFee != 62 || plain.Pricing.Total != 5062 ||
		plain.Pricing.Currency != "EUR" || plain.PromoApplied {
		t.Errorf("plain quote = %+v, want subtotal 5000, discount 0, fee 62, total 5062 EUR, promo_applied false", plain)
	}

	// 10 %: the discount comes off the subtotal, the fee is charged on what is
	// left (4500 * 1.25 % = 56).
	promo := f.mustQuote(t, srv, f.gaCart(2, "TEN", ""))
	if promo.Pricing.Discount != 500 || promo.Pricing.PlatformFee != 56 ||
		promo.Pricing.Total != 4556 || !promo.PromoApplied {
		t.Errorf("promo quote = %+v, want discount 500, fee 56, total 4556, promo_applied true", promo)
	}
	// The code is matched the way checkout/start matches it.
	if again := f.mustQuote(t, srv, f.gaCart(2, "  ten ", "")); again.Pricing.Total != promo.Pricing.Total {
		t.Errorf("padded lower-case code quoted %d, want %d", again.Pricing.Total, promo.Pricing.Total)
	}
	// A blank code is "no code", not an error.
	if blank := f.mustQuote(t, srv, f.gaCart(2, "   ", "")); blank.Pricing.Total != plain.Pricing.Total || blank.PromoApplied {
		t.Errorf("blank code quoted %+v, want the plain price", blank)
	}

	// Quotes took nothing.
	f.assertNoInventoryTaken(t, ctx, before)
	f.nothingCreated(t, ctx, stripe)

	// The real start with the same cart and code: the stored snapshot and the
	// amount sent to the payment page are the quoted total.
	code, body := f.startWithPromo(t, srv, 2, "TEN")
	if code != http.StatusCreated {
		t.Fatalf("checkout/start = %d, want 201; body: %s", code, body)
	}
	var start hostedStartResponse
	if err := json.Unmarshal(body, &start); err != nil {
		t.Fatalf("decode start: %v (body: %s)", err, body)
	}
	csID, err := uuid.Parse(start.CheckoutSession.ID)
	if err != nil {
		t.Fatalf("checkout_session.id: %v", err)
	}
	cs, err := gen.New(pool).GetCheckoutSessionByID(ctx, csID)
	if err != nil {
		t.Fatalf("GetCheckoutSessionByID: %v", err)
	}
	if cs.Total == nil || cs.Subtotal == nil || cs.PlatformFee == nil {
		t.Fatalf("checkout session pricing snapshot incomplete: %+v", cs)
	}
	if *cs.Total != promo.Pricing.Total || *cs.Subtotal != promo.Pricing.Subtotal || *cs.PlatformFee != promo.Pricing.PlatformFee {
		t.Errorf("start stored subtotal/fee/total %d/%d/%d, quote said %d/%d/%d",
			*cs.Subtotal, *cs.PlatformFee, *cs.Total,
			promo.Pricing.Subtotal, promo.Pricing.PlatformFee, promo.Pricing.Total)
	}
	if got, want := stripe.lastForm(t).Get("line_items[0][price_data][unit_amount]"), strconv.FormatInt(promo.Pricing.Total, 10); got != want {
		t.Errorf("payment page amount = %s, quote said %s", got, want)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Wrong codes: one neutral answer
// ─────────────────────────────────────────────────────────────────────────────

func TestPublicQuote_CodeThatDoesNotFitIsNeutral(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()

	f := newHostedOrgFixture(t, ctx, pool, "q2")
	defer f.cleanup()
	defer f.cleanupPromos()

	stripe := newStubStripe(t)
	srv := buildHostedCheckoutServer(t, pool, stripe.baseURL())

	otherSession := uuid.New().String()
	f.insertPromo(t, ctx, "EXPIRED", "percent", 10,
		", valid_from, valid_until", ", now() - interval '10 days', now() - interval '1 day'")
	f.insertPromo(t, ctx, "SOON", "percent", 10,
		", valid_from", ", now() + interval '1 day'")
	f.insertPromo(t, ctx, "OFF", "percent", 10, ", status", ", 'inactive'")
	f.insertPromo(t, ctx, "OTHEREVENT", "percent", 10,
		", applies_to_session_ids", ", ARRAY[$6::uuid]", otherSession)
	f.insertPromo(t, ctx, "BIGORDER", "percent", 10, ", min_order_amount", ", 100000")

	// A code of ANOTHER organization is invisible here, exactly like a code that
	// does not exist at all.
	foreign := newHostedOrgFixture(t, ctx, pool, "q2f")
	defer foreign.cleanup()
	defer foreign.cleanupPromos()
	foreign.insertPromo(t, ctx, "FOREIGN", "percent", 50, "", "")

	neutral := []string{"EXPIRED", "SOON", "OFF", "OTHEREVENT", "FOREIGN", "NOSUCHCODE"}
	var wantMsg string
	for _, c := range neutral {
		e := f.quoteErr(t, srv, f.gaCart(2, c, ""), http.StatusUnprocessableEntity)
		if e.Error.Code != "promo.not_applicable" {
			t.Errorf("code %s: error code = %q, want promo.not_applicable", c, e.Error.Code)
		}
		if wantMsg == "" {
			wantMsg = e.Error.Message
		} else if e.Error.Message != wantMsg {
			t.Errorf("code %s: message %q differs from %q — the wording must not tell codes apart", c, e.Error.Message, wantMsg)
		}
	}

	// Below the minimum is named, but only because the code DOES fit this cart.
	if e := f.quoteErr(t, srv, f.gaCart(2, "BIGORDER", ""), http.StatusUnprocessableEntity); e.Error.Code != "promo.minimum_not_reached" {
		t.Errorf("BIGORDER: error code = %q, want promo.minimum_not_reached", e.Error.Code)
	}

	f.nothingCreated(t, ctx, stripe)
}

// ─────────────────────────────────────────────────────────────────────────────
// Usage caps
// ─────────────────────────────────────────────────────────────────────────────

func TestPublicQuote_UsageCaps(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()

	f := newHostedOrgFixture(t, ctx, pool, "q3")
	defer f.cleanup()
	defer f.cleanupPromos()

	stripe := newStubStripe(t)
	srv := buildHostedCheckoutServer(t, pool, stripe.baseURL())
	q := gen.New(pool)

	f.insertPromo(t, ctx, "ONCEEACH", "percent", 10, ", max_uses_per_customer", ", 1")
	capID := f.insertPromo(t, ctx, "FIRSTONE", "percent", 10, ", max_uses", ", 1")

	// Unused: both quote fine, with and without an e-mail.
	for _, c := range []string{"ONCEEACH", "FIRSTONE"} {
		if got := f.mustQuote(t, srv, f.gaCart(1, c, f.buyerMail)); !got.PromoApplied || got.Pricing.Discount != 250 {
			t.Fatalf("%s before any use = %+v, want a 250 discount", c, got)
		}
	}

	// ── Per customer: a REAL purchase (start → paid webhook) records the use. ──
	code, body := f.startWithPromo(t, srv, 1, "ONCEEACH")
	if code != http.StatusCreated {
		t.Fatalf("checkout/start = %d, want 201; body: %s", code, body)
	}
	var start hostedStartResponse
	if err := json.Unmarshal(body, &start); err != nil {
		t.Fatalf("decode start: %v", err)
	}
	csID, _ := uuid.Parse(start.CheckoutSession.ID)
	intents, err := q.ListPaymentIntentsByCheckout(ctx, csID)
	if err != nil || len(intents) != 1 || intents[0].ProviderPaymentID == nil {
		t.Fatalf("payment intents for the checkout: %v (%d)", err, len(intents))
	}
	paid := sessionCompletedEvent(*intents[0].ProviderPaymentID, "pi_quote_"+uuid.New().String()[:12], "paid")
	if c, b := postSignedWebhook(t, srv, paid, f.webhookSecret); c != http.StatusOK {
		t.Fatalf("paid webhook = %d; body: %s", c, b)
	}

	// The quote takes no buyer details, so it cannot tell whether an address
	// already used a code: it answers the same for the buyer who is over the
	// cap, for anybody else and for a request that names no one.
	for _, email := range []string{f.buyerMail, "someone-else-" + f.buyerMail, ""} {
		if got := f.mustQuote(t, srv, f.gaCart(1, "ONCEEACH", email)); !got.PromoApplied {
			t.Errorf("quote for %q = %+v, want the code to apply whoever asks", email, got)
		}
	}
	// checkout/start is where the per-customer cap bites, with the platform's code.
	if c, b := f.startWithPromo(t, srv, 1, "ONCEEACH"); c != http.StatusUnprocessableEntity || !strings.Contains(string(b), "promo.per_customer_limit") {
		t.Errorf("second start with the same buyer = %d %s, want 422 promo.per_customer_limit", c, b)
	}

	// ── Total cap: one recorded use exhausts max_uses=1 for everybody. ─────────
	if err := q.InsertPromoCodeRedemption(ctx, capID, nil, nil, 250, 2500, nil, nil, nil); err != nil {
		t.Fatalf("InsertPromoCodeRedemption: %v", err)
	}
	if e := f.quoteErr(t, srv, f.gaCart(1, "FIRSTONE", ""), http.StatusUnprocessableEntity); e.Error.Code != "promo.exhausted" {
		t.Errorf("exhausted code: error code = %q, want promo.exhausted", e.Error.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Seated carts price from the seats' tier bindings, without locking them
// ─────────────────────────────────────────────────────────────────────────────

func TestPublicQuote_SeatedCart(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()

	f := newHostedOrgFixture(t, ctx, pool, "q4")
	defer f.cleanup()
	defer f.cleanupPromos()

	stripe := newStubStripe(t)
	srv := buildHostedCheckoutServer(t, pool, stripe.baseURL())

	planID, versionID, seatedSession, seatedTier := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	defer func() {
		bg := context.Background()
		// The fixture's own teardown first: it removes the org's orders and
		// checkouts, which reference the reservation this test's start made.
		f.cleanup()
		for _, sql := range []string{
			`DELETE FROM reservation_seats WHERE reservation_id IN (SELECT id FROM reservations WHERE session_id = $1)`,
			`DELETE FROM reservation_ga_items WHERE reservation_id IN (SELECT id FROM reservations WHERE session_id = $1)`,
			`DELETE FROM reservations WHERE session_id = $1`,
			`DELETE FROM session_seats WHERE session_id = $1`,
			`DELETE FROM inventory_ledger WHERE session_id = $1`,
			`DELETE FROM ticket_tiers WHERE session_id = $1`,
			`DELETE FROM sessions WHERE id = $1`,
		} {
			if _, err := pool.Exec(bg, sql, seatedSession); err != nil {
				t.Logf("seated cleanup (%.40s): %v", sql, err)
			}
		}
		_, _ = pool.Exec(bg, `DELETE FROM seating_plan_versions WHERE id = $1`, versionID)
		_, _ = pool.Exec(bg, `DELETE FROM seating_plans WHERE id = $1`, planID)
		// ...and once more, now that nothing references the event and venue.
		f.cleanup()
	}()
	for i, s := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO seating_plans (id, venue_id, owner_org_id, name, plan_type, status)
		  VALUES ($1, $2, $3, $4, 'assigned_seats', 'active')`,
			[]any{planID, f.venueID, f.orgID, "Quote Plan " + planID.String()[:8]}},
		{`INSERT INTO seating_plan_versions (id, seating_plan_id, version_number, geometry, geometry_checksum, capacity_seated)
		  VALUES ($1, $2, 1, '{}'::jsonb, $3, 3)`,
			[]any{versionID, planID, "q4-" + versionID.String()[:8]}},
		{`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, status,
		    admission_mode, currency, currency_source, seating_plan_version_id)
		  VALUES ($1, $2, $3, now() + interval '30 days', now() + interval '30 days 2 hours', 3,
		    'scheduled', 'assigned_seats', 'EUR', 'override', $4)`,
			[]any{seatedSession, f.eventID, f.venueID, versionID}},
		{`INSERT INTO ticket_tiers (id, session_id, name, pricing_mode, price_amount, currency, sort_order)
		  VALUES ($1, $2, 'Seated', 'fixed', 4000, 'EUR', 0)`,
			[]any{seatedTier, seatedSession}},
		{`INSERT INTO session_seats (session_id, seat_key, sector_name, row_name, seat_number, tier_id, status, kind)
		  VALUES ($1, 'A|1|1', 'A', '1', '1', $2, 'available', 'seat'),
		         ($1, 'A|1|2', 'A', '1', '2', $2, 'available', 'seat'),
		         ($1, 'A|1|3', 'A', '1', '3', $2, 'sold', 'seat')`,
			[]any{seatedSession, seatedTier}},
		{`INSERT INTO inventory_ledger (session_id, tier_id, capacity_total) VALUES ($1, NULL, 3)`,
			[]any{seatedSession}},
	} {
		if _, err := pool.Exec(ctx, s.sql, s.args...); err != nil {
			t.Fatalf("seated fixture step %d: %v", i, err)
		}
	}
	f.insertPromo(t, ctx, "SEATTEN", "percent", 10, "", "")

	seatsBefore := func() (held int) {
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM session_seats WHERE session_id = $1 AND status = 'held'`, seatedSession).Scan(&held); err != nil {
			t.Fatalf("count held seats: %v", err)
		}
		return held
	}()

	cart := func(seats []string, promo string) map[string]any {
		p := map[string]any{"session_id": seatedSession.String(), "seats": seats}
		if promo != "" {
			p["promo_code"] = promo
		}
		return p
	}

	// 2 × 40.00 = 8000, fee 1.25 % = 100, total 8100.
	plain := f.mustQuote(t, srv, cart([]string{"A|1|2", "A|1|1"}, ""))
	if plain.Pricing.Subtotal != 8000 || plain.Pricing.PlatformFee != 100 || plain.Pricing.Total != 8100 {
		t.Errorf("seated quote = %+v, want subtotal 8000, fee 100, total 8100", plain)
	}
	// 10 % off 8000 = 800; fee on 7200 = 90.
	promo := f.mustQuote(t, srv, cart([]string{"A|1|1", "A|1|2"}, "SEATTEN"))
	if promo.Pricing.Discount != 800 || promo.Pricing.PlatformFee != 90 || promo.Pricing.Total != 7290 {
		t.Errorf("seated promo quote = %+v, want discount 800, fee 90, total 7290", promo)
	}

	// A taken seat and an unknown seat are the 409 checkout/start answers.
	for _, bad := range [][]string{{"A|1|3"}, {"A|1|1", "Z|9|9"}} {
		code, body := f.quote(t, srv, cart(bad, ""))
		if code != http.StatusConflict {
			t.Errorf("seats %v: quote = %d, want 409; body: %s", bad, code, body)
		}
	}
	// Seats on a general-admission session are refused the way start refuses them.
	gaWithSeats := cart([]string{"A|1|1"}, "")
	gaWithSeats["session_id"] = f.sessionID.String()
	if e := f.quoteErr(t, srv, gaWithSeats, http.StatusUnprocessableEntity); e.Error.Code != "reservation.seats_not_supported" {
		t.Errorf("seats on a GA session: code = %q, want reservation.seats_not_supported", e.Error.Code)
	}

	if after := func() int {
		var n int
		_ = pool.QueryRow(ctx, `SELECT count(*) FROM session_seats WHERE session_id = $1 AND status = 'held'`, seatedSession).Scan(&n)
		return n
	}(); after != seatsBefore {
		t.Errorf("held seats = %d after quotes, want %d", after, seatsBefore)
	}
	f.nothingCreated(t, ctx, stripe)

	// The same seated cart through start matches the quote.
	startCart := cart([]string{"A|1|1", "A|1|2"}, "SEATTEN")
	startCart["buyer"] = map[string]any{"email": f.buyerMail, "name": "Quote Buyer", "phone": f.buyerPhone}
	code, body := f.postJSON(t, srv, f.feedToken, "/checkout/start", startCart)
	if code != http.StatusCreated {
		t.Fatalf("seated checkout/start = %d, want 201; body: %s", code, body)
	}
	var start hostedStartResponse
	if err := json.Unmarshal(body, &start); err != nil {
		t.Fatalf("decode start: %v", err)
	}
	csID, _ := uuid.Parse(start.CheckoutSession.ID)
	cs, err := gen.New(pool).GetCheckoutSessionByID(ctx, csID)
	if err != nil || cs.Total == nil {
		t.Fatalf("GetCheckoutSessionByID: %v", err)
	}
	if *cs.Total != promo.Pricing.Total {
		t.Errorf("seated start stored total %d, quote said %d", *cs.Total, promo.Pricing.Total)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// checkout/start speaks the same promo language as the quote
// ─────────────────────────────────────────────────────────────────────────────

// Until the quote existed, checkout/start matched a code byte for byte (a
// buyer typing "ten" for "TEN" was refused) and named the exact reason a code
// failed, including "expired" and "belongs to another event" — which told a
// buyer which codes exist. Both are the same lookup and the same verdicts now.
func TestPublicQuote_StartAcceptsTheCodeAsTypedAndRefusesLikeTheQuote(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()

	f := newHostedOrgFixture(t, ctx, pool, "q6")
	defer f.cleanup()
	defer f.cleanupPromos()

	stripe := newStubStripe(t)
	srv := buildHostedCheckoutServer(t, pool, stripe.baseURL())

	f.insertPromo(t, ctx, "EXPIRED", "percent", 10,
		", valid_from, valid_until", ", now() - interval '10 days', now() - interval '1 day'")
	f.insertPromo(t, ctx, "OTHEREVENT", "percent", 10,
		", applies_to_session_ids", ", ARRAY[$6::uuid]", uuid.New().String())
	f.insertPromo(t, ctx, "TEN", "percent", 10, "", "")

	// Refused by start with exactly the quote's code and wording, before any hold.
	before := f.footprint(t, ctx)
	for _, c := range []string{"EXPIRED", "OTHEREVENT", "NOSUCHCODE"} {
		q := f.quoteErr(t, srv, f.gaCart(1, c, ""), http.StatusUnprocessableEntity)
		code, body := f.startWithPromo(t, srv, 1, c)
		if code != http.StatusUnprocessableEntity {
			t.Fatalf("start with %s = %d, want 422; body: %s", c, code, body)
		}
		var s quoteErrBody
		if err := json.Unmarshal(body, &s); err != nil {
			t.Fatalf("decode start error: %v", err)
		}
		if s.Error.Code != "promo.not_applicable" || s.Error.Code != q.Error.Code || s.Error.Message != q.Error.Message {
			t.Errorf("start with %s answered %+v, quote answered %+v; want the same neutral verdict", c, s.Error, q.Error)
		}
	}
	f.assertNoInventoryTaken(t, ctx, before)

	// A code as a buyer types it: lower case, with the spaces of a paste.
	code, body := f.startWithPromo(t, srv, 1, "  ten ")
	if code != http.StatusCreated {
		t.Fatalf("start with %q = %d, want 201; body: %s", "  ten ", code, body)
	}
	var start hostedStartResponse
	if err := json.Unmarshal(body, &start); err != nil {
		t.Fatalf("decode start: %v", err)
	}
	csID, _ := uuid.Parse(start.CheckoutSession.ID)
	cs, err := gen.New(pool).GetCheckoutSessionByID(ctx, csID)
	if err != nil || cs.Discount == nil || *cs.Discount != 250 {
		t.Fatalf("start with a typed code: discount = %v (err %v), want 250", cs.Discount, err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Request validation
// ─────────────────────────────────────────────────────────────────────────────

func TestPublicQuote_BadRequestsAndForeignFeed(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()

	f := newHostedOrgFixture(t, ctx, pool, "q5")
	defer f.cleanup()
	other := newHostedOrgFixture(t, ctx, pool, "q5o")
	defer other.cleanup()

	stripe := newStubStripe(t)
	srv := buildHostedCheckoutServer(t, pool, stripe.baseURL())

	if code, body := f.quote(t, srv, map[string]any{"session_id": "not-a-uuid"}); code != http.StatusBadRequest {
		t.Errorf("bad session id = %d, want 400; body: %s", code, body)
	}
	if code, body := f.quote(t, srv, map[string]any{"session_id": f.sessionID.String()}); code != http.StatusBadRequest {
		t.Errorf("no lines = %d, want 400; body: %s", code, body)
	}
	zero := f.gaCart(0, "", "")
	if code, body := f.quote(t, srv, zero); code != http.StatusBadRequest {
		t.Errorf("zero quantity = %d, want 400; body: %s", code, body)
	}
	// Another organization's session on this feed token is not priced.
	if code, body := f.quote(t, srv, other.gaCart(1, "", "")); code != http.StatusForbidden {
		t.Errorf("foreign session = %d, want 403; body: %s", code, body)
	}
	// A category that is not in the session.
	missing := f.gaCart(1, "", "")
	missing["ga_items"] = []map[string]any{{"tier_id": uuid.New().String(), "quantity": 1}}
	if code, body := f.quote(t, srv, missing); code != http.StatusNotFound {
		t.Errorf("unknown category = %d, want 404; body: %s", code, body)
	}
	// An unknown feed token behaves as checkout/start does.
	if code, body := f.postJSON(t, srv, "no-such-token", "/checkout/quote", f.gaCart(1, "", "")); code != http.StatusForbidden {
		t.Errorf("unknown feed token = %d, want 403; body: %s", code, body)
	}
}
