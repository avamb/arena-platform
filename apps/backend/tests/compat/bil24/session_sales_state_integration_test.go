//go:build integration

// session_sales_state_integration_test.go — the organizer's sales state of a
// session (migration 0183) on the gateway wire. One state for every channel
// (owner decision 2026-10-31):
//
//   - GET_ALL_ACTIONS reports salesState (open / soon / soldout) and
//     arenaSessionId, and advertises no place while a state is set;
//   - RESERVATION answers 101 with the state in salesState and takes
//     no hold;
//   - an order whose hold was taken BEFORE the state was set still pays
//     (PAY_ORDER does not run the gate), and lifting the state brings the
//     sale back.
package compat_bil24_test

import (
	"context"
	"strconv"
	"testing"
)

func ssSetState(t *testing.T, st *harnessState, sessionID, state string) {
	t.Helper()
	ctx := context.Background()
	var err error
	if state == "" {
		_, err = st.Pool.Exec(ctx, `DELETE FROM session_sales_states WHERE session_id = $1`, sessionID)
	} else {
		_, err = st.Pool.Exec(ctx, `
			INSERT INTO session_sales_states (session_id, state) VALUES ($1, $2)
			ON CONFLICT (session_id) DO UPDATE SET state = EXCLUDED.state`, sessionID, state)
	}
	if err != nil {
		t.Fatalf("set sales state %q on %s: %v", state, sessionID, err)
	}
}

// ssCatalogEntry returns the GET_ALL_ACTIONS entry of one action event.
func ssCatalogEntry(t *testing.T, base string, st *harnessState, actionEventID int64) map[string]any {
	t.Helper()
	resp := postBil24(t, base, map[string]any{
		"command": "GET_ALL_ACTIONS",
		"fid":     st.ChannelFID,
		"token":   st.ChannelToken,
		"locale":  "en-US",
	})
	if code := numberField(t, resp, "resultCode"); code != 0 {
		t.Fatalf("GET_ALL_ACTIONS resultCode = %v (description %v)", code, resp["description"])
	}
	actions, _ := resp["actionList"].([]any)
	for _, a := range actions {
		am, _ := a.(map[string]any)
		events, _ := am["actionEventList"].([]any)
		for _, e := range events {
			em, _ := e.(map[string]any)
			if id, ok := em["actionEventId"].(float64); ok && int64(id) == actionEventID {
				return em
			}
		}
	}
	t.Fatalf("action event %d is missing from GET_ALL_ACTIONS", actionEventID)
	return nil
}

func TestCompatBil24_SalesState_CatalogAndReservation(t *testing.T) {
	st := setupHarness(t)
	base := startHarnessServer(t, st)
	ctx := context.Background()
	defer ssSetState(t, st, st.GAsessID, "")

	actionEventID := mustActionEventID(t, st, st.GAsessID)
	tierWireID := sc6TierWireID(t, st, sc2EarlyBirdTierID(t, st))

	entry := ssCatalogEntry(t, base, st, actionEventID)
	if got := entry["salesState"]; got != "open" {
		t.Fatalf("baseline salesState = %v, want open", got)
	}
	if got := entry["salesStateManual"]; got != false {
		t.Errorf("baseline salesStateManual = %v, want false", got)
	}
	if got := entry["arenaSessionId"]; got != st.GAsessID {
		t.Errorf("arenaSessionId = %v, want %s", got, st.GAsessID)
	}
	if a, _ := entry["availability"].(float64); a <= 0 {
		t.Fatalf("baseline availability = %v, want places on sale", entry["availability"])
	}

	reservations := func() (n int) {
		t.Helper()
		if err := st.Pool.QueryRow(ctx,
			`SELECT count(*) FROM reservations WHERE session_id = $1`, st.GAsessID).Scan(&n); err != nil {
			t.Fatalf("count reservations: %v", err)
		}
		return n
	}
	reserve := func(sess string, user float64) map[string]any {
		return postBil24(t, base, map[string]any{
			"command":       "RESERVATION",
			"fid":           st.ChannelFID,
			"token":         st.ChannelToken,
			"locale":        "en-US",
			"type":          "RESERVE",
			"userId":        user,
			"sessionId":     sess,
			"actionEventId": actionEventID,
			"categoryList": []any{map[string]any{
				"categoryPriceId": tierWireID,
				"quantity":        1,
				"tariffPlanId":    nil,
			}},
		})
	}

	for _, tc := range []struct{ state, wire string }{
		{"sold_out", "soldout"},
		{"soon", "soon"},
	} {
		t.Run(tc.state, func(t *testing.T) {
			ssSetState(t, st, st.GAsessID, tc.state)
			defer ssSetState(t, st, st.GAsessID, "")

			e := ssCatalogEntry(t, base, st, actionEventID)
			if got := e["salesState"]; got != tc.wire {
				t.Errorf("salesState = %v, want %s", got, tc.wire)
			}
			if got := e["salesStateManual"]; got != true {
				t.Errorf("salesStateManual = %v, want true", got)
			}
			if a, _ := e["availability"].(float64); a != 0 {
				t.Errorf("availability = %v, want 0 while the organizer's state is set", e["availability"])
			}

			sess, user := createGatewayUser(t, base, st, "harness-salesstate-"+tc.state+"@example.test")
			before := reservations()
			resp := reserve(sess, user)
			if code := numberField(t, resp, "resultCode"); code != 101 {
				t.Fatalf("RESERVATION resultCode = %v, want 101 (description %v)", code, resp["description"])
			}
			if got := resp["salesState"]; got != tc.wire {
				t.Errorf("salesState = %v, want %s", got, tc.wire)
			}
			if after := reservations(); after != before {
				t.Errorf("reservations %d -> %d; a refused RESERVATION must take no hold", before, after)
			}

			// Lifted, the same request goes through with the places as before.
			ssSetState(t, st, st.GAsessID, "")
			if ok := reserve(sess, user); numberField(t, ok, "resultCode") != 0 {
				t.Fatalf("RESERVATION after lifting the state: resultCode = %v (description %v)",
					ok["resultCode"], ok["description"])
			}
		})
	}
}

// A buyer who held a seat and created the order before the organizer closed
// the sale still pays and gets the ticket: the state stops NEW holds only.
func TestCompatBil24_SalesState_StartedPaymentCompletes(t *testing.T) {
	st := setupHarness(t)
	base := startHarnessServer(t, st)
	defer ssSetState(t, st, st.AssignedSessID, "")

	actionEventID := mustActionEventID(t, st, st.AssignedSessID)
	labels := sortedSeatLabels(st)
	if len(labels) < 1 {
		t.Fatalf("seed produced %d seats, need at least 1", len(labels))
	}
	tierWireID := sc6TierWireID(t, st, st.AssignedTierID)
	const email = "harness-salesstate-paid@example.test"

	sess, user, _, orderID, _, _ := pwNewOrder(t, base, st, actionEventID, tierWireID, email, labels[0], "ss-paid-1")
	ssSetState(t, st, st.AssignedSessID, "sold_out")

	resp := pwPay(t, base, st, sess, user, strconv.FormatInt(sc5SystemID(t, st, orderID), 10))
	if code := numberField(t, resp, "resultCode"); code != 0 {
		t.Fatalf("PAY_ORDER after sold out: resultCode = %v, want 0 (description %v)", code, resp["description"])
	}
	sc5AssertOrderPaid(t, st, orderID, "woo_bank_card")
	sc5AssertTickets(t, st, orderID, 1, email)
}

// SUBSCRIBE_SALE (arena extension): a WordPress site's "notify me when sales
// open" form. A coming-soon session takes the address with its consent and
// queues the confirmation letter; a missing consent is -2; an open session is
// 101 with salesState "open" and saves nothing.
func TestCompatBil24_SubscribeSale(t *testing.T) {
	st := setupHarness(t)
	base := startHarnessServer(t, st)
	ctx := context.Background()
	defer ssSetState(t, st, st.GAsessID, "")
	defer func() {
		_, _ = st.Pool.Exec(ctx, `DELETE FROM worker_jobs WHERE job_type = 'sales_state.subscribe_confirm_email'
			AND payload->>'subscription_id' IN (SELECT id::text FROM session_sale_subscriptions WHERE session_id = $1)`, st.GAsessID)
		_, _ = st.Pool.Exec(ctx, `DELETE FROM session_sale_subscriptions WHERE session_id = $1`, st.GAsessID)
	}()
	actionEventID := mustActionEventID(t, st, st.GAsessID)
	subscribe := func(email string, consent any) map[string]any {
		return postBil24(t, base, map[string]any{
			"command":        "SUBSCRIBE_SALE",
			"fid":            st.ChannelFID,
			"token":          st.ChannelToken,
			"locale":         "ru",
			"actionEventId":  actionEventID,
			"email":          email,
			"pageUrl":        "https://site.example/event/#buy",
			"consent":        consent,
			"consentVersion": "sale-notify-2026-11-01",
		})
	}
	saved := func(email string) (n int) {
		t.Helper()
		if err := st.Pool.QueryRow(ctx, `SELECT count(*) FROM session_sale_subscriptions
			WHERE session_id = $1 AND email_normalized = $2`, st.GAsessID, email).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// Sales are open: refused, the site may switch to the buy button.
	resp := subscribe("open-sub@example.test", true)
	if code := numberField(t, resp, "resultCode"); code != 101 || resp["salesState"] != "open" {
		t.Fatalf("open session: resultCode %v salesState %v (%v)", code, resp["salesState"], resp["description"])
	}
	if saved("open-sub@example.test") != 0 {
		t.Fatal("a refused subscription was saved")
	}

	ssSetState(t, st, st.GAsessID, "soon")
	if code := numberField(t, subscribe("noconsent@example.test", "0"), "resultCode"); code != -2 {
		t.Fatalf("no consent: resultCode %v, want -2", code)
	}
	resp = subscribe("Site-Sub@Example.test", "1")
	if code := numberField(t, resp, "resultCode"); code != 0 || resp["status"] != "check_email" {
		t.Fatalf("SUBSCRIBE_SALE: resultCode %v status %v (%v)", code, resp["status"], resp["description"])
	}
	var source, page string
	var channel *string
	if err := st.Pool.QueryRow(ctx, `SELECT source, page_url, channel_id::text FROM session_sale_subscriptions
		WHERE session_id = $1 AND email_normalized = 'site-sub@example.test'`, st.GAsessID).Scan(&source, &page, &channel); err != nil {
		t.Fatalf("subscription row: %v", err)
	}
	if source != "site" || page != "https://site.example/event/" || channel == nil {
		t.Errorf("row = source %s page %s channel %v", source, page, channel)
	}
	var jobs int
	if err := st.Pool.QueryRow(ctx, `SELECT count(*) FROM worker_jobs WHERE job_type = 'sales_state.subscribe_confirm_email'
		AND payload->>'subscription_id' IN (SELECT id::text FROM session_sale_subscriptions WHERE session_id = $1)`, st.GAsessID).Scan(&jobs); err != nil || jobs != 1 {
		t.Fatalf("confirmation jobs = %d, %v", jobs, err)
	}
}
