//go:build integration

// public_feed_quote_matrix_integration_test.go — the quote and checkout/start
// price one cart through the same code, so for every kind of cart the two
// must agree to the minor unit: percent and fixed-amount codes, a minimum, a
// scope by category and by currency, a seated cart, a seated + general
// admission mix, and a scheduled price. A category that may not be sold is
// refused by both. The quote route also answers a browser's CORS preflight.
//
// Run with:
//
//	go test -tags integration ./apps/backend/internal/platform/httpserver/ \
//	    -run TestPublicQuoteMatrix
package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// hybridSession adds a plan-bound hybrid session to the fixture's event: six
// seats at 40.00 and a general-admission category at 15.99 with places. It
// returns the session, the seat tier and the GA tier, and registers its own
// teardown (to run BEFORE the fixture's, i.e. deferred after it).
func (f *hostedOrgFixture) addHybridSession(t *testing.T, ctx context.Context) (session, seatTier, gaTier uuid.UUID) {
	t.Helper()
	planID, versionID := uuid.New(), uuid.New()
	session, seatTier, gaTier = uuid.New(), uuid.New(), uuid.New()
	steps := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO seating_plans (id, venue_id, owner_org_id, name, plan_type, status)
		  VALUES ($1, $2, $3, $4, 'assigned_seats', 'active')`,
			[]any{planID, f.venueID, f.orgID, "Matrix Plan " + planID.String()[:8]}},
		{`INSERT INTO seating_plan_versions (id, seating_plan_id, version_number, geometry, geometry_checksum, capacity_seated)
		  VALUES ($1, $2, 1, '{}'::jsonb, $3, 12)`,
			[]any{versionID, planID, "mx-" + versionID.String()[:8]}},
		{`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, status,
		    admission_mode, currency, currency_source, seating_plan_version_id)
		  VALUES ($1, $2, $3, now() + interval '30 days', now() + interval '30 days 2 hours', 40,
		    'scheduled', 'hybrid', 'EUR', 'override', $4)`,
			[]any{session, f.eventID, f.venueID, versionID}},
		{`INSERT INTO ticket_tiers (id, session_id, name, pricing_mode, price_amount, currency, sort_order)
		  VALUES ($1, $2, 'Seated', 'fixed', 4000, 'EUR', 0), ($3, $2, 'GA', 'fixed', 1599, 'EUR', 1)`,
			[]any{seatTier, session, gaTier}},
		{`INSERT INTO session_seats (session_id, seat_key, sector_name, row_name, seat_number, tier_id, status, kind)
		  SELECT $1, 'A|1|' || gs, 'A', '1', gs::text, $2, 'available', 'seat' FROM generate_series(1, 12) gs`,
			[]any{session, seatTier}},
		{`INSERT INTO inventory_ledger (session_id, tier_id, capacity_total) VALUES ($1, NULL, 40)`,
			[]any{session}},
	}
	for i, s := range steps {
		if _, err := f.pool.Exec(ctx, s.sql, s.args...); err != nil {
			t.Fatalf("hybrid fixture step %d: %v", i, err)
		}
	}
	if _, err := gen.New(f.pool).InsertGAUnits(ctx, session, "ga|t2", 0, &gaTier, 30); err != nil {
		t.Fatalf("hybrid GA units: %v", err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		// The checkouts started on the hybrid session hang off the organization,
		// so they go first; the fixture's own cleanup then removes the rest.
		for _, sql := range []string{
			`DELETE FROM payment_intent_events WHERE payment_intent_id IN (SELECT id FROM payment_intents WHERE org_id = $1)`,
			`DELETE FROM payment_intents WHERE org_id = $1`,
			`DELETE FROM order_events WHERE order_id IN (SELECT id FROM orders WHERE org_id = $1)`,
			`DELETE FROM order_items WHERE order_id IN (SELECT id FROM orders WHERE org_id = $1)`,
			`DELETE FROM orders WHERE org_id = $1`,
			`DELETE FROM checkout_sessions WHERE org_id = $1`,
		} {
			if _, err := f.pool.Exec(bg, sql, f.orgID); err != nil {
				t.Logf("hybrid cleanup (%.40s): %v", sql, err)
			}
		}
		for _, sql := range []string{
			`DELETE FROM reservation_seats WHERE reservation_id IN (SELECT id FROM reservations WHERE session_id = $1)`,
			`DELETE FROM reservation_ga_items WHERE reservation_id IN (SELECT id FROM reservations WHERE session_id = $1)`,
			`DELETE FROM session_seats WHERE session_id = $1`,
			`DELETE FROM reservations WHERE session_id = $1`,
			`DELETE FROM inventory_ledger WHERE session_id = $1`,
			`DELETE FROM ticket_tiers WHERE session_id = $1`,
			`DELETE FROM sessions WHERE id = $1`,
		} {
			if _, err := f.pool.Exec(bg, sql, session); err != nil {
				t.Logf("hybrid cleanup (%.40s): %v", sql, err)
			}
		}
		_, _ = f.pool.Exec(bg, `DELETE FROM seating_plan_versions WHERE id = $1`, versionID)
		_, _ = f.pool.Exec(bg, `DELETE FROM seating_plans WHERE id = $1`, planID)
	})
	return session, seatTier, gaTier
}

func TestPublicQuoteMatrix_QuoteEqualsStartForEveryKindOfCart(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()

	f := newHostedOrgFixture(t, ctx, pool, "mx")
	t.Cleanup(f.cleanup)
	t.Cleanup(f.cleanupPromos)
	hs, _, gaTier := f.addHybridSession(t, ctx)

	stripe := newStubStripe(t)
	srv := buildHostedCheckoutServer(t, pool, stripe.baseURL())
	if _, err := gen.New(pool).InsertGAUnits(ctx, f.sessionID, "ga|t1x", 100, &f.tierID, 30); err != nil {
		t.Fatalf("more GA units: %v", err)
	}

	f.insertPromo(t, ctx, "PCT15", "percent", 15, "", "")
	f.insertPromo(t, ctx, "FIX333", "fixed_amount", 333, ", currency", ", 'EUR'")
	f.insertPromo(t, ctx, "FIXHUGE", "fixed_amount", 999999, ", currency", ", 'EUR'")
	f.insertPromo(t, ctx, "MIN5000", "percent", 10, ", min_order_amount", ", 5000")
	f.insertPromo(t, ctx, "GAONLY", "percent", 50, ", applies_to_tier_ids", ", ARRAY[$6::uuid]", gaTier.String())
	f.insertPromo(t, ctx, "GAFIX", "fixed_amount", 700, ", currency, applies_to_tier_ids", ", 'EUR', ARRAY[$6::uuid]", gaTier.String())
	f.insertPromo(t, ctx, "CZKONLY", "fixed_amount", 500, ", currency", ", 'CZK'")
	f.insertPromo(t, ctx, "WRONGTIER", "percent", 10, ", applies_to_tier_ids", ", ARRAY[$6::uuid]", uuid.New().String())

	ga := func(tier uuid.UUID, q int) map[string]any {
		return map[string]any{"tier_id": tier.String(), "quantity": q}
	}
	cases := []struct {
		name    string
		session uuid.UUID
		seats   []string
		ga      []map[string]any
		promo   string
		refused string // want both routes to refuse with this code ("" = both accept)
	}{
		{"ga plain", f.sessionID, nil, []map[string]any{ga(f.tierID, 3)}, "", ""},
		{"ga percent", f.sessionID, nil, []map[string]any{ga(f.tierID, 3)}, "PCT15", ""},
		{"ga fixed amount", f.sessionID, nil, []map[string]any{ga(f.tierID, 3)}, "FIX333", ""},
		{"ga fixed amount above the subtotal", f.sessionID, nil, []map[string]any{ga(f.tierID, 1)}, "FIXHUGE", ""},
		{"ga minimum met", f.sessionID, nil, []map[string]any{ga(f.tierID, 3)}, "MIN5000", ""},
		{"ga minimum not met", f.sessionID, nil, []map[string]any{ga(f.tierID, 1)}, "MIN5000", "promo.minimum_not_reached"},
		{"ga code of another currency", f.sessionID, nil, []map[string]any{ga(f.tierID, 3)}, "CZKONLY", "promo.not_applicable"},
		{"ga code of another category", f.sessionID, nil, []map[string]any{ga(f.tierID, 3)}, "WRONGTIER", "promo.not_applicable"},
		{"seats percent", hs, []string{"A|1|1", "A|1|2"}, nil, "PCT15", ""},
		{"mix percent", hs, []string{"A|1|3", "A|1|4"}, []map[string]any{ga(gaTier, 3)}, "PCT15", ""},
		{"mix fixed amount", hs, []string{"A|1|5"}, []map[string]any{ga(gaTier, 1)}, "FIX333", ""},
		{"mix code of the GA category only", hs, []string{"A|1|6"}, []map[string]any{ga(gaTier, 3)}, "GAONLY", ""},
		{"mix fixed amount of the GA category only", hs, []string{"A|1|7"}, []map[string]any{ga(gaTier, 3)}, "GAFIX", ""},
		{"mix minimum counts every line", hs, []string{"A|1|8"}, []map[string]any{ga(gaTier, 1)}, "MIN5000", ""},
	}
	for n, c := range cases {
		payload := map[string]any{"session_id": c.session.String()}
		if len(c.seats) > 0 {
			payload["seats"] = c.seats
		}
		if len(c.ga) > 0 {
			payload["ga_items"] = c.ga
		}
		if c.promo != "" {
			payload["promo_code"] = c.promo
		}
		start := map[string]any{}
		for k, v := range payload {
			start[k] = v
		}
		start["buyer"] = map[string]any{
			"email": fmt.Sprintf("mx-%d-%s", n, f.buyerMail),
			"name":  "Matrix Buyer",
			"phone": fmt.Sprintf("+3699%07d", (n+1)*7919+int(uuid.New().ID()%1000)),
		}

		qCode, qBody := f.quote(t, srv, payload)
		sCode, sBody := f.postJSON(t, srv, f.feedToken, "/checkout/start", start)

		if c.refused != "" {
			var qe, se quoteErrBody
			_ = json.Unmarshal(qBody, &qe)
			_ = json.Unmarshal(sBody, &se)
			if qCode != http.StatusUnprocessableEntity || sCode != http.StatusUnprocessableEntity ||
				qe.Error.Code != c.refused || se.Error.Code != c.refused {
				t.Errorf("%s: quote %d %q, start %d %q; want both 422 %s", c.name, qCode, qe.Error.Code, sCode, se.Error.Code, c.refused)
			}
			continue
		}
		if qCode != http.StatusOK || sCode != http.StatusCreated {
			t.Errorf("%s: quote %d (%s), start %d (%s); want 200 and 201", c.name, qCode, qBody, sCode, sBody)
			continue
		}
		var q quoteBody
		var st hostedStartResponse
		if err := json.Unmarshal(qBody, &q); err != nil {
			t.Fatalf("%s: decode quote: %v", c.name, err)
		}
		if err := json.Unmarshal(sBody, &st); err != nil {
			t.Fatalf("%s: decode start: %v", c.name, err)
		}
		csID, _ := uuid.Parse(st.CheckoutSession.ID)
		cs, err := gen.New(pool).GetCheckoutSessionByID(ctx, csID)
		if err != nil || cs.Total == nil || cs.Discount == nil || cs.Subtotal == nil || cs.PlatformFee == nil {
			t.Fatalf("%s: stored snapshot: %v %+v", c.name, err, cs)
		}
		if *cs.Subtotal != q.Pricing.Subtotal || *cs.Discount != q.Pricing.Discount ||
			*cs.PlatformFee != q.Pricing.PlatformFee || *cs.Total != q.Pricing.Total {
			t.Errorf("%s: quote %d/%d/%d/%d, stored %d/%d/%d/%d (subtotal/discount/fee/total)", c.name,
				q.Pricing.Subtotal, q.Pricing.Discount, q.Pricing.PlatformFee, q.Pricing.Total,
				*cs.Subtotal, *cs.Discount, *cs.PlatformFee, *cs.Total)
		}
		if q.Pricing.Total > 0 {
			if got, want := stripe.lastForm(t).Get("line_items[0][price_data][unit_amount]"), strconv.FormatInt(q.Pricing.Total, 10); got != want {
				t.Errorf("%s: payment page asks %s, quote said %s", c.name, got, want)
			}
		}
		if (c.promo != "") != q.PromoApplied {
			t.Errorf("%s: promo_applied = %v for code %q", c.name, q.PromoApplied, c.promo)
		}
	}
}

// A scheduled price (a price window) is the price the buyer pays now; the quote
// reads it exactly as checkout/start does.
func TestPublicQuoteMatrix_ScheduledPriceIsQuotedAndCharged(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()

	f := newHostedOrgFixture(t, ctx, pool, "mxw")
	defer f.cleanup()
	defer f.cleanupPromos()
	stripe := newStubStripe(t)
	srv := buildHostedCheckoutServer(t, pool, stripe.baseURL())

	if _, err := pool.Exec(ctx,
		`INSERT INTO ticket_tier_prices (tier_id, valid_from, valid_to, price_amount)
		 VALUES ($1, now() - interval '1 hour', now() + interval '1 hour', 1000)`, f.tierID); err != nil {
		t.Fatalf("insert price window: %v", err)
	}
	q := f.mustQuote(t, srv, f.gaCart(2, "", ""))
	if q.Pricing.Subtotal != 2000 { // 2 x 10.00 inside the window, not 2 x 25.00
		t.Fatalf("quote inside the price window: subtotal %d, want 2000", q.Pricing.Subtotal)
	}
	code, body := f.startWithPromo(t, srv, 2, "")
	if code != http.StatusCreated {
		t.Fatalf("start = %d; body: %s", code, body)
	}
	var st hostedStartResponse
	_ = json.Unmarshal(body, &st)
	csID, _ := uuid.Parse(st.CheckoutSession.ID)
	cs, err := gen.New(pool).GetCheckoutSessionByID(ctx, csID)
	if err != nil || cs.Total == nil || *cs.Total != q.Pricing.Total {
		t.Fatalf("stored total %v (err %v), quote said %d", cs.Total, err, q.Pricing.Total)
	}
}

// A category that may not be sold is refused by the quote the way the hold
// refuses it: a price for it would be a promise checkout/start cannot keep.
func TestPublicQuoteMatrix_ClosedAndNotYetOnSaleCategoriesAreRefused(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()

	f := newHostedOrgFixture(t, ctx, pool, "mxc")
	defer f.cleanup()
	defer f.cleanupPromos()
	stripe := newStubStripe(t)
	srv := buildHostedCheckoutServer(t, pool, stripe.baseURL())

	for _, c := range []struct{ name, set string }{
		{"closed", `is_open = false`},
		{"sale starts later", `sale_window_start = now() + interval '1 day'`},
		{"sale ended", `sale_window_end = now() - interval '1 day'`},
	} {
		if _, err := pool.Exec(ctx, `UPDATE ticket_tiers SET is_open = true, sale_window_start = NULL, sale_window_end = NULL WHERE id = $1`, f.tierID); err != nil {
			t.Fatalf("reset tier: %v", err)
		}
		if _, err := pool.Exec(ctx, `UPDATE ticket_tiers SET `+c.set+` WHERE id = $1`, f.tierID); err != nil {
			t.Fatalf("%s: update tier: %v", c.name, err)
		}
		qCode, qBody := f.quote(t, srv, f.gaCart(1, "", ""))
		sCode, sBody := f.startWithPromo(t, srv, 1, "")
		if qCode != http.StatusConflict || sCode != http.StatusConflict {
			t.Errorf("%s: quote %d (%s), start %d (%s); want both 409", c.name, qCode, qBody, sCode, sBody)
			continue
		}
		var qe, se quoteErrBody
		_ = json.Unmarshal(qBody, &qe)
		_ = json.Unmarshal(sBody, &se)
		if qe.Error.Code != se.Error.Code {
			t.Errorf("%s: quote code %q, start code %q; want the same", c.name, qe.Error.Code, se.Error.Code)
		}
	}
	f.nothingCreated(t, ctx, stripe)
}

// The widget runs on a customer's own origin, so a browser sends a CORS
// preflight before the first POST to the quote route.
func TestPublicQuoteMatrix_AnswersACORSPreflight(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()

	f := newHostedOrgFixture(t, ctx, pool, "mxo")
	defer f.cleanup()
	srv := buildHostedCheckoutServer(t, pool, newStubStripe(t).baseURL())

	req := httptest.NewRequest(http.MethodOptions, "/v1/public/feeds/"+f.feedToken+"/checkout/quote", nil)
	req.Header.Set("Origin", hostedTicketsBaseURL)
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "content-type")
	rec := httptest.NewRecorder()
	srv.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent && rec.Code != http.StatusOK {
		t.Fatalf("preflight = %d, want 200 or 204", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != hostedTicketsBaseURL {
		t.Errorf("Access-Control-Allow-Origin = %q, want %q", got, hostedTicketsBaseURL)
	}
	if got := rec.Header().Get("Access-Control-Allow-Methods"); !strings.Contains(strings.ToUpper(got), "POST") {
		t.Errorf("Access-Control-Allow-Methods = %q, want POST allowed", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Headers"); !strings.Contains(strings.ToLower(got), "content-type") {
		t.Errorf("Access-Control-Allow-Headers = %q, want Content-Type allowed", got)
	}

	// The answer itself carries the CORS headers too, even a refusal.
	post := httptest.NewRequest(http.MethodPost, "/v1/public/feeds/"+f.feedToken+"/checkout/quote", strings.NewReader(`{`))
	post.Header.Set("Origin", hostedTicketsBaseURL)
	post.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.router.ServeHTTP(rec, post)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != hostedTicketsBaseURL {
		t.Errorf("POST answer: Access-Control-Allow-Origin = %q, want %q", got, hostedTicketsBaseURL)
	}
}
