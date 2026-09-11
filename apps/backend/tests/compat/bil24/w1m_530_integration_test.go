//go:build integration

// w1m_530_integration_test.go — feature #530 (W1-M [EPIC-VERIFY]), spec
// 08_architecture/20_bil24_gateway_money_units_spec_ru.md.
//
// #528 (W1-M1) pinned the fractional-cart arithmetic on a hand-seeded tier;
// #529 (W1-M2) pinned MACS's order.paid/ticket.refunded encoding against a
// hand-seeded order row. Neither exercises the FULL pipeline: an event
// bundle imported through the real handler, priced by the real catalog,
// carted and paid through the real Bil24 gateway commands, and delivered to
// BOTH receiving sites (the WordPress stub AND the MACS stub) by the real
// outbox dispatchers — all in major units, end to end.
//
// This test is that missing link:
//
//	POST /v1/organizations/{org}/imports/event-bundle (categoryList 450/900)
//	  → GET_ALL_ACTIONS quotes price 450 / 900, minPrice 450
//	  → RESERVATION of one GA unit of the 450 category: sum=450,
//	    charge=22.5 (the harness channel's flat 5 %), totalSum=472.5
//	  → CREATE_ORDER_EXT totalSum echoes the same 472.5
//	  → PAY_ORDER(amount=472.5) → resultCode 0
//	  → the real ticket-issuance path publishes v1.order.paid; the real
//	    bil24wire AND macs dispatchers deliver it to the wpstub and the MACS
//	    stub, each carrying totalSum=472.5 as a JSON number (not "472.50").
package compat_bil24_test

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/bil24wire"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/macs"
	macsstub "github.com/abhteam/arena_new/apps/backend/internal/platform/macs/stub"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/outbox"
	"github.com/abhteam/arena_new/apps/backend/tests/compat/bil24/wpstub"
)

// The bundle's Standard category (450) is what the whole scenario buys, so
// every downstream money figure derives from it and the harness channel's
// flat 5 % service charge: 450 * 500bp/10000 = 22.5, total 472.5.
const (
	w530SubtotalMajor = 450.0
	w530ChargeMajor   = 22.5
	w530TotalMajor    = 472.5
	w530Currency      = "CZK"
	w530MinPrice      = 450.0
	w530WPSecret      = "harness-530-wp-secret"
	w530MACSSecret    = "harness-530-macs-secret"
)

func TestCompatBil24_530_MoneyRoundTripBundleToWebhooks(t *testing.T) {
	st := setupHarness(t)
	// Booted first: startHarnessServer registers cleanupHarnessWireRows with
	// t.Cleanup (LIFO), so the orders/reservations this test creates are torn
	// down before the bundle's own event/session cleanup runs.
	base := startHarnessServer(t, st)

	orgID, err := uuid.Parse(st.OrgID)
	if err != nil {
		t.Fatalf("parse st.OrgID: %v", err)
	}
	rawKey := sc8ImportKey(t, st, base, orgID)

	// ── step 1: import the event bundle through the real handler ───────────
	body := bundle527Fixture(t, "")
	status, resp := restJSON(t, base, "POST",
		"/v1/organizations/"+st.OrgID+"/imports/event-bundle", rawKey, nil, body)
	if status != 200 {
		t.Fatalf("POST imports/event-bundle status = %d, want 200 (body %v)", status, resp)
	}
	if created, _ := resp["created"].(bool); !created {
		t.Errorf("event-bundle created = %v, want true", resp["created"])
	}
	eventID := sc8UUIDField(t, resp, "event_id")
	sessionID := sc8UUIDField(t, resp, "session_id")
	bundle527RegisterCleanup(t, st, eventID, sessionID)

	compatIDs, ok := resp["compat_ids"].(map[string]interface{})
	if !ok {
		t.Fatalf("event-bundle response has no compat_ids object: %v", resp)
	}
	actionEventID := numberField(t, compatIDs, "action_event_id")
	catIDsRaw, _ := compatIDs["category_price_ids"].([]interface{})
	if len(catIDsRaw) != 2 {
		t.Fatalf("compat_ids.category_price_ids = %v, want 2 entries", catIDsRaw)
	}
	standardCatID, ok := catIDsRaw[0].(float64)
	if !ok {
		t.Fatalf("category_price_ids[0] = %#v, want a number", catIDsRaw[0])
	}

	// ── step 2: GET_ALL_ACTIONS quotes the bundle's major-unit prices ──────
	req, _ := loadWPFixture(t, "GET_ALL_ACTIONS", "basic")
	req["fid"] = st.ChannelFID
	req["token"] = st.ChannelToken
	catalog := postBil24(t, base, req)
	if code := numberField(t, catalog, "resultCode"); code != 0 {
		t.Fatalf("GET_ALL_ACTIONS resultCode = %v, want 0 (description %v)", code, catalog["description"])
	}
	action := sc1FindActionByEvent(t, catalog, actionEventID)
	if got := numberField(t, action, "minPrice"); got != w530MinPrice {
		t.Errorf("GET_ALL_ACTIONS minPrice = %v, want %v", got, w530MinPrice)
	}
	events := sc1Objects(t, action, "actionEventList")
	event := sc1FindEvent(t, events, actionEventID)
	catLimits := sc1Objects(t, event, "categoryLimitList")
	if len(catLimits) != 1 {
		t.Fatalf("categoryLimitList has %d entries, want 1", len(catLimits))
	}
	cats := sc1Objects(t, catLimits[0], "categoryList")
	if len(cats) != 2 {
		t.Fatalf("categoryList has %d entries, want 2 (Standard + VIP)", len(cats))
	}
	sc1WantNumber(t, cats[0], "price", w530SubtotalMajor)
	sc1WantNumber(t, cats[1], "price", 900)

	// ── step 3: RESERVATION of one GA unit of the Standard category ────────
	sess, user := createGatewayUser(t, base, st, "harness-530-buyer@example.test")
	tierWireID := int64(standardCatID)
	actionEventWireID := int64(actionEventID)

	reserved := postBil24(t, base, map[string]any{
		"command":       "RESERVATION",
		"fid":           st.ChannelFID,
		"token":         st.ChannelToken,
		"locale":        "ru-RU",
		"type":          "RESERVE",
		"userId":        user,
		"sessionId":     sess,
		"actionEventId": actionEventWireID,
		"categoryList": []any{map[string]any{
			"categoryPriceId": tierWireID,
			"quantity":        1,
			"tariffPlanId":    nil,
		}},
	})
	if code := numberField(t, reserved, "resultCode"); code != 0 {
		t.Fatalf("RESERVATION resultCode = %v, want 0 (description %v)", code, reserved["description"])
	}
	w530AssertMoney(t, "RESERVATION", reserved, map[string]float64{
		"sum":      w530SubtotalMajor,
		"discount": 0,
		"charge":   w530ChargeMajor,
		"totalSum": w530TotalMajor,
	})

	// ── step 4: CREATE_ORDER_EXT echoes the same total ──────────────────────
	runTag := uuid.NewString()[:8]
	created := postBil24(t, base, map[string]any{
		"command":         "CREATE_ORDER_EXT",
		"fid":             st.ChannelFID,
		"token":           st.ChannelToken,
		"locale":          "ru-RU",
		"orderId":         "wc-530-" + runTag,
		"userId":          user,
		"sessionId":       sess,
		"currency":        w530Currency,
		"total":           w530TotalMajor,
		"actionEventId":   actionEventWireID,
		"longReservation": false,
		"lines": []any{map[string]any{
			"categoryPriceId": tierWireID,
			"quantity":        1,
			"tariffPlanId":    nil,
		}},
		"email":         "harness-530-buyer@example.test",
		"phone":         "+420123456789",
		"fullName":      "Petra Novakova",
		"chargePercent": 5,
		"promoCodes":    []any{},
	})
	if code := numberField(t, created, "resultCode"); code != 0 {
		t.Fatalf("CREATE_ORDER_EXT resultCode = %v, want 0 (description %v)", code, created["description"])
	}
	w530AssertMoney(t, "CREATE_ORDER_EXT", created, map[string]float64{
		"sum":      w530SubtotalMajor,
		"discount": 0,
		"charge":   w530ChargeMajor,
		"totalSum": w530TotalMajor,
	})
	orderID := sc6OrderID(t, "530", created)

	// bundle527RegisterCleanup (registered above, right after the import)
	// deletes ticket_tiers/sessions/events/venues; cleanupHarnessWireRows
	// (registered earlier still, inside startHarnessServer) sweeps
	// orders/reservations/checkout_sessions/tickets by org_id afterwards.
	// t.Cleanup is LIFO, so without a cleanup of our own registered AFTER
	// bundle527RegisterCleanup, the order-chain rows this test creates are
	// still around when the session/tier delete runs and trip their FKs.
	// Registering this one here makes it run BEFORE bundle527RegisterCleanup.
	t.Cleanup(func() {
		c := context.Background()
		w530CleanupExec(t, c, st, `DELETE FROM barcodes WHERE ticket_id IN
			(SELECT id FROM tickets WHERE session_id=$1)`, sessionID)
		w530CleanupExec(t, c, st, `UPDATE order_items SET ticket_id = NULL WHERE order_id=$1`, orderID)
		w530CleanupExec(t, c, st, `DELETE FROM tickets WHERE session_id=$1`, sessionID)
		w530CleanupExec(t, c, st, `DELETE FROM payment_intents WHERE checkout_session_id IN
			(SELECT id FROM checkout_sessions WHERE reservation_id IN
			     (SELECT id FROM reservations WHERE session_id=$1))`, sessionID)
		w530CleanupExec(t, c, st, `DELETE FROM order_events WHERE order_id=$1`, orderID)
		w530CleanupExec(t, c, st, `DELETE FROM order_items WHERE order_id=$1`, orderID)
		w530CleanupExec(t, c, st, `DELETE FROM orders WHERE id=$1`, orderID)
		w530CleanupExec(t, c, st, `DELETE FROM checkout_sessions WHERE reservation_id IN
			(SELECT id FROM reservations WHERE session_id=$1)`, sessionID)
		// session_seats.reservation_id and reservation_seats do not cascade
		// (kept "informational" on convert per migration 0058's own comment),
		// so both have to clear before the reservations row itself can go.
		w530CleanupExec(t, c, st, `UPDATE session_seats SET reservation_id = NULL, status = 'available'
			WHERE session_id=$1`, sessionID)
		w530CleanupExec(t, c, st, `DELETE FROM reservation_seats WHERE reservation_id IN
			(SELECT id FROM reservations WHERE session_id=$1)`, sessionID)
		w530CleanupExec(t, c, st, `DELETE FROM reservations WHERE session_id=$1`, sessionID)
		w530CleanupExec(t, c, st, `DELETE FROM session_seats WHERE session_id=$1`, sessionID)
	})

	// ── step 5: the two receiving sites, wired BEFORE the payment fires ────
	var channelID uuid.UUID
	if err := st.Pool.QueryRow(context.Background(),
		`SELECT id FROM sales_channels WHERE display_number=$1`, st.ChannelFID,
	).Scan(&channelID); err != nil {
		t.Fatalf("read channel uuid for fid %d: %v", st.ChannelFID, err)
	}

	wpRecv := wpstub.New()
	t.Cleanup(wpRecv.Close)
	macsRecv := macsstub.NewWithSecret(w530MACSSecret)
	t.Cleanup(macsRecv.Close)

	w530Exec(t, st, `INSERT INTO webhook_subscribers (site_url, callback_url, signing_secret,
			event_types, active, kind, org_id, channel_id)
		VALUES ('', $1, $2, '{}', TRUE, 'bil24_wp', $3, $4)`,
		wpRecv.URL(), w530WPSecret, orgID, channelID)
	w530Exec(t, st, `INSERT INTO webhook_subscribers (site_url, callback_url, signing_secret,
			event_types, active, kind, org_id)
		VALUES ('', $1, $2, '{}', TRUE, 'macs', $3)`,
		macsRecv.WebhookURL(), w530MACSSecret, orgID)
	t.Cleanup(func() {
		_, _ = st.Pool.Exec(context.Background(),
			`DELETE FROM webhook_subscribers WHERE org_id=$1`, orgID)
	})

	// ── step 6: PAY_ORDER with the buyer-reported amount ────────────────────
	paid := postBil24(t, base, map[string]any{
		"command":   "PAY_ORDER",
		"fid":       st.ChannelFID,
		"token":     st.ChannelToken,
		"locale":    "ru-RU",
		"orderId":   orderID.String(),
		"userId":    user,
		"sessionId": sess,
		"amount":    w530TotalMajor,
		"currency":  w530Currency,
		"method":    "woo_bank_card",
	})
	if code := numberField(t, paid, "resultCode"); code != 0 {
		t.Fatalf("PAY_ORDER resultCode = %v, want 0 (description %v)", code, paid["description"])
	}

	// ── step 7: drain the real outbox — both sites, once each, same total ──
	fanOut := &wp508MultiDispatcher{dispatchers: []outbox.Dispatcher{
		bil24wire.NewDispatcher(st.Pool),
		macs.NewDispatcher(st.Pool),
	}}
	dispatchOpts := outbox.OutboxEventsDispatcherOptions{
		Store:        outbox.NewPGOutboxEventStore(st.Pool),
		Dispatcher:   fanOut,
		PollInterval: 20 * time.Millisecond,
		MaxAttempts:  5,
		BackoffFunc:  func(int) time.Duration { return time.Hour },
	}
	if !wp508Drain(t, dispatchOpts, func() bool {
		return wp508Occurrences(wpRecv, bil24wire.SiteEventOrderPaid) >= 1 &&
			len(macsRecv.EventsByType("order.paid")) >= 1
	}, 20*time.Second) {
		t.Fatalf("order.paid never reached both sites (wp=%d macs=%d)",
			wp508Occurrences(wpRecv, bil24wire.SiteEventOrderPaid),
			len(macsRecv.EventsByType("order.paid")))
	}

	// ── step 8: both deliveries carry the identical major-unit total ───────
	wpEvent, found := wp508Last(wpRecv, bil24wire.SiteEventOrderPaid)
	if !found {
		t.Fatal("wpstub: no order.paid event recorded")
	}
	if got, _ := wpEvent.Data["totalSum"].(float64); got != w530TotalMajor {
		t.Errorf("wpstub order.paid totalSum = %v, want %v", wpEvent.Data["totalSum"], w530TotalMajor)
	}

	macsEvents := macsRecv.EventsByType("order.paid")
	if len(macsEvents) != 1 {
		t.Fatalf("MACS stub saw order.paid %d time(s), want exactly 1", len(macsEvents))
	}
	macsEnvelope := macsEvents[0]
	if got, _ := macsEnvelope.Data["totalSum"].(float64); got != w530TotalMajor {
		t.Errorf("MACS order.paid totalSum = %v, want %v", macsEnvelope.Data["totalSum"], w530TotalMajor)
	}
	// The raw MACS payload is what proves the JSON RENDERING is "472.5", not
	// "472.50" or "472.5000000001" — json.Unmarshal into float64 cannot tell
	// those apart, so this checks the wire bytes directly (spec 20 §2.1).
	if !w530ContainsBareNumber(macsEnvelope.Raw, `"totalSum":472.5`) {
		t.Errorf("MACS order.paid raw payload = %s, want a literal \"totalSum\":472.5", macsEnvelope.Raw)
	}
}

// w530CleanupExec is a t.Logf-on-error (never t.Fatalf) cleanup statement
// runner, matching cleanupHarnessWireRows/bundle527RegisterCleanup: a leaked
// row is annoying, a panic inside t.Cleanup hides the real test failure.
func w530CleanupExec(t *testing.T, c context.Context, st *harnessState, sql string, args ...any) {
	t.Helper()
	if _, err := st.Pool.Exec(c, sql, args...); err != nil {
		t.Logf("w530 cleanup %.60s… : %v", sql, err)
	}
}

// w530Exec is a thin t.Fatalf-on-error wrapper around st.Pool.Exec, mirroring
// sc4Exec (scenario04_refund_test.go) for this file's own seed writes.
func w530Exec(t *testing.T, st *harnessState, sql string, args ...any) {
	t.Helper()
	if _, err := st.Pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("w530 seed exec: %v\nSQL: %s", err, sql)
	}
}

// w530AssertMoney checks a flat set of money fields on a decoded Bil24
// response object against their expected major-unit values.
func w530AssertMoney(t *testing.T, label string, resp map[string]interface{}, want map[string]float64) {
	t.Helper()
	for key, wantVal := range want {
		if got := numberField(t, resp, key); got != wantVal {
			t.Errorf("%s: %s = %v, want %v", label, key, got, wantVal)
		}
	}
}

// w530ContainsBareNumber reports whether raw contains the exact byte
// sequence needle — a plain strings.Contains, spelled out locally so the
// intent (checking JSON RENDERING, not decoded value) is unambiguous at the
// call site. json.Valid guards against a malformed fixture masking a
// substring coincidence.
func w530ContainsBareNumber(raw []byte, needle string) bool {
	if !json.Valid(raw) {
		return false
	}
	return bytes.Contains(raw, []byte(needle))
}
