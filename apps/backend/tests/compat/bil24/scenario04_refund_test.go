//go:build integration

// scenario04_refund_test.go — spec §15.3 scenario 4, "refund + dedup"
// (feature #509, W1-B8).
//
// The scenario proves the REFUND_TICKET gateway command end to end against a
// live database and the real dispatch chain:
//
//	REFUND_TICKET (ok)  → tickets.status='cancelled', refund_price written,
//	                      refund_date stamped, orders.status='partially_refunded',
//	                      order_events.ticket_refunded row with actor gateway:<fid>
//	outbox drain        → ticket.refunded reaches the WordPress stub (§9.2) AND
//	                      the MACS stub (/_wh/tickets) exactly once each
//	REFUND_TICKET (repeat) → resultCode 0, same payload, NO new deliveries
//	REFUND_TICKET (other_org) → resultCode -3, envelope only
//
// Everything below the HTTP boundary is production code: the real httpserver,
// the real htickets cancel transaction, the real outbox dispatcher and the
// real bil24wire / MACS dispatchers. Only the two receiving sites are stubs.
package compat_bil24_test

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/bil24wire"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/macs"
	macsstub "github.com/abhteam/arena_new/apps/backend/internal/platform/macs/stub"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/outbox"
	"github.com/abhteam/arena_new/apps/backend/tests/compat/bil24/wpstub"
)

// sc4Fixture is the seeded sale the scenario refunds against.
type sc4Fixture struct {
	OrgID     uuid.UUID
	ChannelID uuid.UUID
	SessionID uuid.UUID
	OrderID   uuid.UUID
	TicketIDs []uuid.UUID
	// SystemTicketID is the int64 the WordPress site knows ticket #1 by.
	SystemTicketID int64
	// ForeignSystemTicketID belongs to a ticket in a DIFFERENT organization;
	// spec §7.13 requires it to answer -3, indistinguishable from "not found".
	ForeignSystemTicketID int64
}

// runScenario04Refund is the body of the 04_refund_dedup sub-test.
func runScenario04Refund(t *testing.T, st *harnessState) {
	t.Helper()
	ctx := context.Background()

	const (
		sc4RefundMajor  = 500.00
		sc4RefundMinor  = int64(50000)
		sc4TicketCount  = 2
		sc4UnitPrice    = int64(90000) // GA "Early Bird" is CZK/EUR 900.00
		sc4SigningKey   = "harness-509-wp-secret"
		sc4MACSSecret   = "harness-509-macs-secret"
		sc4BuyerEmail   = "harness-509-buyer@example.test"
		sc4RefundReason = "Customer requested a refund in WooCommerce"
	)

	// The server is booted FIRST on purpose: startHarnessServer registers
	// cleanupHarnessWireRows, which deletes the org's reservations. t.Cleanup
	// is LIFO, so seeding afterwards guarantees this scenario's own
	// checkout_sessions/orders are gone before that reservation sweep runs —
	// otherwise the sweep trips checkout_sessions_reservation_id_fkey.
	base := startHarnessServer(t, st)

	fx := sc4Seed(t, st, sc4TicketCount, sc4UnitPrice, sc4BuyerEmail)

	// ── the two receiving sites ─────────────────────────────────────────────
	wpRecv := wpstub.New()
	t.Cleanup(wpRecv.Close)
	macsRecv := macsstub.NewWithSecret(sc4MACSSecret)
	t.Cleanup(macsRecv.Close)

	sc4Exec(t, st, `INSERT INTO webhook_subscribers (site_url, callback_url, signing_secret,
			event_types, active, kind, org_id, channel_id)
		VALUES ('', $1, $2, '{}', TRUE, 'bil24_wp', $3, $4)`,
		wpRecv.URL(), sc4SigningKey, fx.OrgID, fx.ChannelID)
	sc4Exec(t, st, `INSERT INTO webhook_subscribers (site_url, callback_url, signing_secret,
			event_types, active, kind, org_id)
		VALUES ('', $1, $2, '{}', TRUE, 'macs', $3)`,
		macsRecv.WebhookURL(), sc4MACSSecret, fx.OrgID)
	t.Cleanup(func() {
		_, _ = st.Pool.Exec(context.Background(),
			`DELETE FROM webhook_subscribers WHERE org_id=$1`, fx.OrgID)
	})

	// ── step 1: REFUND_TICKET (ok) ──────────────────────────────────────────
	reqOK, gldOK := loadWPFixture(t, "REFUND_TICKET", "ok")
	reqOK["fid"] = st.ChannelFID
	reqOK["token"] = st.ChannelToken
	reqOK["ticketId"] = fx.SystemTicketID

	resp := postBil24(t, base, reqOK)
	if code := numberField(t, resp, "resultCode"); code != 0 {
		t.Fatalf("REFUND_TICKET resultCode = %v, want 0 (description %v)", code, resp["description"])
	}
	firstRefundDate, _ := resp["refundDate"].(string)
	if firstRefundDate == "" {
		t.Fatalf("REFUND_TICKET refundDate = %#v, want a non-empty RFC3339 string", resp["refundDate"])
	}
	if _, err := time.Parse(time.RFC3339, firstRefundDate); err != nil {
		t.Errorf("REFUND_TICKET refundDate %q is not RFC3339: %v", firstRefundDate, err)
	}
	assertGoldenKeySet(t, resp, sc4ResolveGolden(gldOK, fx.SystemTicketID, firstRefundDate))
	if got := int64(numberField(t, resp, "ticketId")); got != fx.SystemTicketID {
		t.Errorf("REFUND_TICKET ticketId = %d, want %d", got, fx.SystemTicketID)
	}

	// ── step 2: the database side of spec §7.13 ─────────────────────────────
	var (
		status      string
		refundPrice *int64
		refundDate  *time.Time
	)
	if err := st.Pool.QueryRow(ctx,
		`SELECT status, refund_price, refund_date FROM tickets WHERE id=$1`,
		fx.TicketIDs[0],
	).Scan(&status, &refundPrice, &refundDate); err != nil {
		t.Fatalf("read refunded ticket: %v", err)
	}
	if status != "cancelled" {
		t.Errorf("tickets.status = %q, want cancelled", status)
	}
	if refundPrice == nil || *refundPrice != sc4RefundMinor {
		t.Errorf("tickets.refund_price = %v, want %d minor units (%.2f major)",
			refundPrice, sc4RefundMinor, sc4RefundMajor)
	}
	if refundDate == nil {
		t.Error("tickets.refund_date is NULL; spec §7.13 requires it stamped")
	}

	var orderStatus string
	if err := st.Pool.QueryRow(ctx,
		`SELECT status FROM orders WHERE id=$1`, fx.OrderID).Scan(&orderStatus); err != nil {
		t.Fatalf("read order status: %v", err)
	}
	// One of two tickets refunded → the order is only partially refunded.
	if orderStatus != "partially_refunded" {
		t.Errorf("orders.status = %q, want partially_refunded", orderStatus)
	}

	wantActor := "gateway:" + strconv.FormatInt(st.ChannelFID, 10)
	var evActor string
	var evPayload []byte
	if err := st.Pool.QueryRow(ctx,
		`SELECT actor, payload FROM order_events
		 WHERE order_id=$1 AND type='ticket_refunded'
		 ORDER BY created_at DESC LIMIT 1`, fx.OrderID,
	).Scan(&evActor, &evPayload); err != nil {
		t.Fatalf("read order_events.ticket_refunded: %v", err)
	}
	if evActor != wantActor {
		t.Errorf("order_events.actor = %q, want %q", evActor, wantActor)
	}
	if !strings.Contains(string(evPayload), sc4RefundReason) {
		t.Errorf("order_events.payload = %s, want it to carry the request reason %q",
			evPayload, sc4RefundReason)
	}

	// ── step 3: the fan-out — wpstub AND MACS stub, once each ───────────────
	fanOut := &wp508MultiDispatcher{dispatchers: []outbox.Dispatcher{
		bil24wire.NewDispatcher(st.Pool),
		macs.NewDispatcher(st.Pool),
	}}
	dispatchOpts := outbox.OutboxEventsDispatcherOptions{
		Store:        outbox.NewPGOutboxEventStore(st.Pool),
		Dispatcher:   fanOut,
		PollInterval: 20 * time.Millisecond,
		MaxAttempts:  5,
		// An hour of backoff keeps redelivery under the test's control, so the
		// "exactly once" assertions cannot race a spontaneous retry.
		BackoffFunc: func(int) time.Duration { return time.Hour },
	}

	if !wp508Drain(t, dispatchOpts, func() bool {
		return wp508Occurrences(wpRecv, bil24wire.SiteEventTicketRefunded) >= 1 &&
			len(macsRecv.EventsByType("ticket.refunded")) >= 1
	}, 20*time.Second) {
		t.Fatalf("ticket.refunded never reached both sites (wp=%d macs=%d)",
			wp508Occurrences(wpRecv, bil24wire.SiteEventTicketRefunded),
			len(macsRecv.EventsByType("ticket.refunded")))
	}
	if got := wp508Occurrences(wpRecv, bil24wire.SiteEventTicketRefunded); got != 1 {
		t.Errorf("wpstub saw ticket.refunded %d time(s), want exactly 1", got)
	}
	if got := len(macsRecv.EventsByType("ticket.refunded")); got != 1 {
		t.Errorf("MACS stub saw ticket.refunded %d time(s), want exactly 1", got)
	}

	// ── step 4: the replay is a no-op (spec §7.13: already cancelled → 0) ───
	reqRepeat, gldRepeat := loadWPFixture(t, "REFUND_TICKET", "repeat")
	reqRepeat["fid"] = st.ChannelFID
	reqRepeat["token"] = st.ChannelToken
	reqRepeat["ticketId"] = fx.SystemTicketID

	respRepeat := postBil24(t, base, reqRepeat)
	if code := numberField(t, respRepeat, "resultCode"); code != 0 {
		t.Fatalf("repeat REFUND_TICKET resultCode = %v, want 0 (idempotent) — description %v",
			code, respRepeat["description"])
	}
	repeatRefundDate, _ := respRepeat["refundDate"].(string)
	assertGoldenKeySet(t, respRepeat, sc4ResolveGolden(gldRepeat, fx.SystemTicketID, repeatRefundDate))
	if repeatRefundDate != firstRefundDate {
		t.Errorf("repeat refundDate = %q, want the original %q (a replay must not re-stamp)",
			repeatRefundDate, firstRefundDate)
	}

	// Drain again: the replay must not have produced a second delivery.
	_ = wp508Drain(t, dispatchOpts, func() bool {
		return wp508Occurrences(wpRecv, bil24wire.SiteEventTicketRefunded) > 1 ||
			len(macsRecv.EventsByType("ticket.refunded")) > 1
	}, 2*time.Second)
	if got := wp508Occurrences(wpRecv, bil24wire.SiteEventTicketRefunded); got != 1 {
		t.Errorf("after replay wpstub saw %d ticket.refunded, want 1 (deduped)", got)
	}
	if got := len(macsRecv.EventsByType("ticket.refunded")); got != 1 {
		t.Errorf("after replay MACS stub saw %d ticket.refunded, want 1 (deduped)", got)
	}

	var refundedCount int
	if err := st.Pool.QueryRow(ctx,
		`SELECT count(*) FROM tickets WHERE order_id=$1 AND status='cancelled'`, fx.OrderID,
	).Scan(&refundedCount); err != nil {
		t.Fatalf("count cancelled tickets: %v", err)
	}
	if refundedCount != 1 {
		t.Errorf("cancelled tickets = %d, want 1 (the replay must not cancel a sibling)", refundedCount)
	}

	// ── step 5: a ticket outside the channel's org is -3, payload-free ──────
	reqForeign, gldForeign := loadWPFixture(t, "REFUND_TICKET", "other_org")
	reqForeign["fid"] = st.ChannelFID
	reqForeign["token"] = st.ChannelToken
	reqForeign["ticketId"] = fx.ForeignSystemTicketID

	respForeign := postBil24(t, base, reqForeign)
	if code := numberField(t, respForeign, "resultCode"); code != -3 {
		t.Fatalf("cross-org REFUND_TICKET resultCode = %v, want -3 (description %v)",
			code, respForeign["description"])
	}
	assertGoldenKeySet(t, respForeign, gldForeign)

	var foreignStatus string
	if err := st.Pool.QueryRow(ctx,
		`SELECT status FROM tickets WHERE system_ticket_id=$1`, fx.ForeignSystemTicketID,
	).Scan(&foreignStatus); err != nil {
		t.Fatalf("read foreign ticket: %v", err)
	}
	if foreignStatus != "active" {
		t.Errorf("foreign ticket status = %q, want active (a -3 must not mutate)", foreignStatus)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// fixtures
// ─────────────────────────────────────────────────────────────────────────────

func sc4Exec(t *testing.T, st *harnessState, sql string, args ...any) {
	t.Helper()
	if _, err := st.Pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("scenario 4 seed exec: %v\nSQL: %s", err, sql)
	}
}

// sc4Seed builds a paid order of `count` GA tickets on the harness org/channel
// plus a single ticket in a foreign organization for the -3 case.
func sc4Seed(t *testing.T, st *harnessState, count int, unitPrice int64, buyerEmail string) sc4Fixture {
	t.Helper()
	ctx := context.Background()

	orgID, err := uuid.Parse(st.OrgID)
	if err != nil {
		t.Fatalf("parse st.OrgID: %v", err)
	}
	sessionID, err := uuid.Parse(st.GAsessID)
	if err != nil {
		t.Fatalf("parse st.GAsessID: %v", err)
	}
	venueID, err := uuid.Parse(st.VenueID)
	if err != nil {
		t.Fatalf("parse st.VenueID: %v", err)
	}

	var channelID uuid.UUID
	if err := st.Pool.QueryRow(ctx,
		`SELECT id FROM sales_channels WHERE display_number=$1`, st.ChannelFID,
	).Scan(&channelID); err != nil {
		t.Fatalf("resolve harness channel uuid: %v", err)
	}

	var tierID uuid.UUID
	var currency string
	if err := st.Pool.QueryRow(ctx,
		`SELECT id, currency FROM ticket_tiers WHERE session_id=$1 ORDER BY sort_order LIMIT 1`,
		sessionID,
	).Scan(&tierID, &currency); err != nil {
		t.Fatalf("resolve GA tier: %v", err)
	}

	suffix := uuid.New().String()[:8]
	fx := sc4Fixture{
		OrgID:     orgID,
		ChannelID: channelID,
		SessionID: sessionID,
		OrderID:   uuid.New(),
	}
	resID := uuid.New()
	csID := uuid.New()
	for i := 0; i < count; i++ {
		fx.TicketIDs = append(fx.TicketIDs, uuid.New())
	}

	// Foreign org chain (spec §7.13: "not yours" must look like "not found").
	fOrgID := uuid.New()
	fEventID := uuid.New()
	fSessionID := uuid.New()
	fChannelID := uuid.New()
	fResID := uuid.New()
	fCsID := uuid.New()
	fOrderID := uuid.New()
	fTicketID := uuid.New()

	subtotal := unitPrice * int64(count)

	t.Cleanup(func() {
		c := context.Background()
		ids := []string{fx.OrderID.String(), fOrderID.String()}
		for _, id := range fx.TicketIDs {
			ids = append(ids, id.String())
		}
		ids = append(ids, fTicketID.String())
		_, _ = st.Pool.Exec(c, `DELETE FROM outbox_events WHERE aggregate_id = ANY($1)`, ids)
		_, _ = st.Pool.Exec(c, `DELETE FROM order_events WHERE order_id = ANY($1)`,
			[]uuid.UUID{fx.OrderID, fOrderID})
		_, _ = st.Pool.Exec(c, `DELETE FROM tickets WHERE id = ANY($1)`,
			append(append([]uuid.UUID{}, fx.TicketIDs...), fTicketID))
		_, _ = st.Pool.Exec(c, `DELETE FROM orders WHERE id = ANY($1)`,
			[]uuid.UUID{fx.OrderID, fOrderID})
		_, _ = st.Pool.Exec(c, `DELETE FROM checkout_sessions WHERE id = ANY($1)`,
			[]uuid.UUID{csID, fCsID})
		_, _ = st.Pool.Exec(c, `DELETE FROM reservations WHERE id = ANY($1)`,
			[]uuid.UUID{resID, fResID})
		_, _ = st.Pool.Exec(c, `DELETE FROM compatibility_id_map WHERE platform_id = ANY($1)`,
			[]uuid.UUID{fSessionID, fEventID})
		_, _ = st.Pool.Exec(c, `DELETE FROM sessions WHERE id=$1`, fSessionID)
		_, _ = st.Pool.Exec(c, `DELETE FROM events WHERE id=$1`, fEventID)
		_, _ = st.Pool.Exec(c, `DELETE FROM sales_channels WHERE id=$1`, fChannelID)
		_, _ = st.Pool.Exec(c, `DELETE FROM organizations WHERE id=$1`, fOrgID)
	})

	// The cancellation transaction restores ledger capacity for the ticket's
	// scope (tier-scoped here, because a hand-seeded ticket holds no
	// session_seats row to release). RestoreSoldCapacity answers ErrNoRows
	// both when the row is missing AND when capacity_sold < 1, so the fixture
	// must present a tier row that really has the units sold.
	tag, err := st.Pool.Exec(ctx,
		`UPDATE inventory_ledger SET capacity_sold = capacity_sold + $3
		 WHERE session_id=$1 AND tier_id=$2`, sessionID, tierID, count)
	if err != nil {
		t.Fatalf("bump tier ledger: %v", err)
	}
	ledgerCreated := tag.RowsAffected() == 0
	if ledgerCreated {
		sc4Exec(t, st, `INSERT INTO inventory_ledger (session_id, tier_id, capacity_total, capacity_sold)
			VALUES ($1,$2,$3,$4)`, sessionID, tierID, 100, count)
	}
	t.Cleanup(func() {
		c := context.Background()
		if ledgerCreated {
			_, _ = st.Pool.Exec(c,
				`DELETE FROM inventory_ledger WHERE session_id=$1 AND tier_id=$2`, sessionID, tierID)
			return
		}
		_, _ = st.Pool.Exec(c,
			`UPDATE inventory_ledger SET capacity_sold = GREATEST(capacity_sold - $3, 0)
			 WHERE session_id=$1 AND tier_id=$2`, sessionID, tierID, count)
	})

	// ── the harness-org sale ────────────────────────────────────────────────
	sc4Exec(t, st, `INSERT INTO reservations (id, org_id, channel_id, session_id, quantity, expires_at)
		VALUES ($1,$2,$3,$4,$5,$6)`,
		resID, orgID, channelID, sessionID, count, time.Now().Add(30*time.Minute))
	completedAt := time.Now().Add(-2 * time.Hour)
	sc4Exec(t, st, `INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state,
			subtotal, discount, total, currency, payment_provider, completed_at)
		VALUES ($1,$2,$3,$4,'completed',$5,0,$5,$6,'yookassa',$7)`,
		csID, orgID, channelID, resID, subtotal, currency, completedAt)

	var eventID uuid.UUID
	if err := st.Pool.QueryRow(ctx,
		`SELECT event_id FROM sessions WHERE id=$1`, sessionID).Scan(&eventID); err != nil {
		t.Fatalf("resolve GA session event: %v", err)
	}
	sc4Exec(t, st, `INSERT INTO orders (id, org_id, channel_id, event_id, session_id,
			checkout_session_id, reservation_id, source, status, currency,
			subtotal, discount, charge, total)
		VALUES ($1,$2,$3,$4,$5,$6,$7,'checkout_api','paid',$8,$9,0,0,$9)`,
		fx.OrderID, orgID, channelID, eventID, sessionID, csID, resID, currency, subtotal)

	for i, tid := range fx.TicketIDs {
		sc4Exec(t, st, `INSERT INTO tickets (id, session_id, checkout_session_id, order_id, tier_id,
				status, issued_at, ordinal, holder_email)
			VALUES ($1,$2,$3,$4,$5,'active',NOW(),$6,$7)`,
			tid, sessionID, csID, fx.OrderID, tierID, i+1, buyerEmail)
	}
	if err := st.Pool.QueryRow(ctx,
		`SELECT system_ticket_id FROM tickets WHERE id=$1`, fx.TicketIDs[0],
	).Scan(&fx.SystemTicketID); err != nil {
		t.Fatalf("read system_ticket_id: %v", err)
	}
	if fx.SystemTicketID <= 0 {
		t.Fatalf("system_ticket_id = %d, want a minted positive bigint", fx.SystemTicketID)
	}

	// ── the foreign-org ticket ──────────────────────────────────────────────
	sc4Exec(t, st, `INSERT INTO organizations (id, name, legal_name, slug, tax_id)
		VALUES ($1,$2,$3,$4,$5)`,
		fOrgID, "Harness 509 Foreign Org", "Harness 509 Foreign s.r.o.",
		"harness-509-foreign-"+suffix, "CZ98765432")
	sc4Exec(t, st, `INSERT INTO events (id, org_id, name, status, visibility)
		VALUES ($1,$2,$3,'published','public')`,
		fEventID, fOrgID, "Harness 509 Foreign Event "+suffix)
	fStart := time.Now().Add(72 * time.Hour).UTC().Truncate(time.Hour)
	sc4Exec(t, st, `INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total,
			status, admission_mode, currency, currency_source)
		VALUES ($1,$2,$3,$4,$5,10,'scheduled','general_admission',$6,'override')`,
		fSessionID, fEventID, venueID, fStart, fStart.Add(2*time.Hour), currency)
	sc4Exec(t, st, `INSERT INTO sales_channels (id, org_id, name) VALUES ($1,$2,$3)`,
		fChannelID, fOrgID, "Harness 509 Foreign Channel "+suffix)
	sc4Exec(t, st, `INSERT INTO reservations (id, org_id, channel_id, session_id, quantity, expires_at)
		VALUES ($1,$2,$3,$4,1,$5)`,
		fResID, fOrgID, fChannelID, fSessionID, time.Now().Add(30*time.Minute))
	sc4Exec(t, st, `INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state,
			subtotal, discount, total, currency, payment_provider, completed_at)
		VALUES ($1,$2,$3,$4,'completed',$5,0,$5,$6,'yookassa',$7)`,
		fCsID, fOrgID, fChannelID, fResID, unitPrice, currency, completedAt)
	sc4Exec(t, st, `INSERT INTO orders (id, org_id, channel_id, event_id, session_id,
			checkout_session_id, reservation_id, source, status, currency,
			subtotal, discount, charge, total)
		VALUES ($1,$2,$3,$4,$5,$6,$7,'checkout_api','paid',$8,$9,0,0,$9)`,
		fOrderID, fOrgID, fChannelID, fEventID, fSessionID, fCsID, fResID, currency, unitPrice)
	sc4Exec(t, st, `INSERT INTO tickets (id, session_id, checkout_session_id, order_id,
			status, issued_at, ordinal, holder_email)
		VALUES ($1,$2,$3,$4,'active',NOW(),1,$5)`,
		fTicketID, fSessionID, fCsID, fOrderID, "harness-509-foreign@example.test")
	if err := st.Pool.QueryRow(ctx,
		`SELECT system_ticket_id FROM tickets WHERE id=$1`, fTicketID,
	).Scan(&fx.ForeignSystemTicketID); err != nil {
		t.Fatalf("read foreign system_ticket_id: %v", err)
	}

	return fx
}

// sc4ResolveGolden fills the two REFUND_TICKET-specific placeholders that
// harness_test.go's generic resolveGolden does not know about. Values only
// matter for readability — assertGoldenKeySet compares key sets, not values.
func sc4ResolveGolden(g map[string]interface{}, systemTicketID int64, refundDate string) map[string]interface{} {
	return walk(g, func(v string) string {
		v = strings.ReplaceAll(v, "{{systemTicketId}}", strconv.FormatInt(systemTicketID, 10))
		v = strings.ReplaceAll(v, "{{refundDate}}", refundDate)
		return v
	}).(map[string]interface{})
}
