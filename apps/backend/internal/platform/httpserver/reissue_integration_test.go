//go:build integration

// reissue_integration_test.go — spec 63 (ticket reissue for an order sent to
// a wrong address) through the REAL router with a manager's JWT. It proves:
// every active ticket of the order is closed as transferred with its codes
// revoked and replaced in the same order and seat by a new ticket with a new
// EAN-13 (money and order lines unchanged), the purchase token changes, one
// letter is queued to the new address, the order address moves only after the
// relay accepted the letter (a refused send is send_failed and a resend
// requeues it), a repeat with the same key answers the first result, a second
// operation is 409 while the first has not been sent, every refusal writes
// nothing, a used code does not stop it, a live selling site refuses it and a
// switched-off one does not, the journal keeps both addresses while the audit
// and the order history do not, and another organization only gets 404.
//
// Run against a FRESH migrated database (AGENTS.md CI-Integration recipe).
package httpserver

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/ticketreissue"
)

func (f *ecOrdersFixture) reissueDo(method, path, token, key, body string) (int, map[string]any) {
	f.t.Helper()
	req, err := http.NewRequest(method, f.ts.URL+path, strings.NewReader(body))
	if err != nil {
		f.t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := f.ts.Client().Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	out := map[string]any{}
	if strings.TrimSpace(string(raw)) != "" {
		if err := json.Unmarshal(raw, &out); err != nil {
			f.t.Fatalf("%s %s: decode %q: %v", method, path, raw, err)
		}
	}
	return resp.StatusCode, out
}

func reissuePath(org, order uuid.UUID, tail string) string {
	return "/v1/organizations/" + org.String() + "/orders/" + order.String() + "/" + tail
}

// reissueCleanup removes what a reissue of these orders wrote, before the
// fixture's own cleanup deletes the orders (t.Cleanup runs last-in first).
func (f *ecOrdersFixture) reissueCleanup(orders []uuid.UUID, emails []string) {
	f.t.Cleanup(func() {
		c := context.Background()
		ids := func(sql string, args ...any) []uuid.UUID {
			rows, err := f.pool.Query(c, sql, args...)
			if err != nil {
				f.t.Logf("reissue cleanup: %v", err)
				return nil
			}
			defer rows.Close()
			var out []uuid.UUID
			for rows.Next() {
				var id uuid.UUID
				if rows.Scan(&id) == nil {
					out = append(out, id)
				}
			}
			return out
		}
		newTickets := ids(`SELECT i.new_ticket_id FROM ticket_reissue_items i JOIN ticket_reissues r ON r.id = i.reissue_id
			WHERE r.order_id = ANY($1)`, orders)
		allTickets := ids(`SELECT id FROM tickets WHERE order_id = ANY($1)`, orders)
		customers := ids(`SELECT customer_id FROM customer_identities WHERE value_normalized = ANY($1)`, emails)
		ticketText := make([]string, 0, len(allTickets))
		for _, id := range allTickets {
			ticketText = append(ticketText, id.String())
		}
		for _, s := range []struct {
			sql  string
			args []any
		}{
			{`DELETE FROM worker_jobs WHERE job_type = 'reissue.provider_email'
			    AND payload->>'reissue_id' IN (SELECT id::text FROM ticket_reissues WHERE order_id = ANY($1))`, []any{orders}},
			{`DELETE FROM worker_jobs WHERE payload->>'ticket_id' = ANY($1)`, []any{ticketText}},
			{`DELETE FROM outbox_events WHERE aggregate_id = ANY($1)`, []any{ticketText}},
			{`DELETE FROM ticket_reissue_items WHERE reissue_id IN (SELECT id FROM ticket_reissues WHERE order_id = ANY($1))`, []any{orders}},
			{`DELETE FROM ticket_reissues WHERE order_id = ANY($1)`, []any{orders}},
			{`DELETE FROM order_items WHERE order_id = ANY($1)`, []any{orders}},
			{`DELETE FROM delivery_jobs WHERE ticket_id = ANY($1)`, []any{newTickets}},
			{`DELETE FROM barcodes WHERE ticket_id = ANY($1)`, []any{newTickets}},
			{`DELETE FROM ticket_credentials WHERE ticket_id = ANY($1)`, []any{newTickets}},
			{`DELETE FROM tickets WHERE id = ANY($1)`, []any{newTickets}},
			{`UPDATE orders SET customer_id = NULL WHERE id = ANY($1) AND customer_id = ANY($2)`, []any{orders, customers}},
			{`DELETE FROM customer_org_links WHERE customer_id = ANY($1)`, []any{customers}},
			{`DELETE FROM customer_identities WHERE customer_id = ANY($1)`, []any{customers}},
			{`DELETE FROM customers WHERE id = ANY($1)`, []any{customers}},
		} {
			if _, err := f.pool.Exec(c, s.sql, s.args...); err != nil {
				f.t.Logf("reissue cleanup: %s: %v", s.sql, err)
			}
		}
	})
}

type reissueSnapshot struct {
	active, transferred, items, journal, jobs int
	total                                     int64
	email                                     string
}

func (f *ecOrdersFixture) reissueSnap(order uuid.UUID) reissueSnapshot {
	f.t.Helper()
	var s reissueSnapshot
	if err := f.pool.QueryRow(context.Background(), `
		SELECT (SELECT count(*) FROM tickets WHERE order_id = $1 AND status = 'active'),
		       (SELECT count(*) FROM tickets WHERE order_id = $1 AND status = 'transferred'),
		       (SELECT count(*) FROM order_items WHERE order_id = $1),
		       (SELECT count(*) FROM ticket_reissues WHERE order_id = $1),
		       (SELECT count(*) FROM worker_jobs WHERE job_type = 'ticket.deliver'
		          AND payload->>'ticket_id' IN (SELECT id::text FROM tickets WHERE order_id = $1)),
		       o.total, COALESCE(o.buyer_email, '')
		FROM orders o WHERE o.id = $1`, order).Scan(&s.active, &s.transferred, &s.items, &s.journal, &s.jobs, &s.total, &s.email); err != nil {
		f.t.Fatalf("snapshot: %v", err)
	}
	return s
}

func TestReissue_Integration(t *testing.T) {
	f := newECOrdersFixture(t)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	suffix := uuid.NewString()[:8]
	newEmail := "real-" + suffix + "@example.org"
	order := f.paidOrder

	// A purchase token of the first letter, to prove it stops working.
	oldToken := "tok-" + uuid.NewString()
	if _, err := f.pool.Exec(ctx, `UPDATE checkout_sessions SET checkout_token = $2
		WHERE id = (SELECT checkout_session_id FROM orders WHERE id = $1)`, order, oldToken); err != nil {
		t.Fatal(err)
	}
	scanned, scannedTicket := f.resendSeedOrder(f.channelA, "public_feed", "scan-"+suffix+"@example.test")
	gateway, _ := f.resendSeedOrder(f.channelA, "bil24_gateway", "gw-"+suffix+"@example.test")

	siteChannel := uuid.New()
	if _, err := f.pool.Exec(ctx, `INSERT INTO sales_channels (id, org_id, name, settings) VALUES ($1, $2, 'Reissue site', '{}'::jsonb)`,
		siteChannel, f.orgA); err != nil {
		t.Fatal(err)
	}
	if _, err := gen.New(f.pool).CreateWPWebhookSubscriber(ctx, siteChannel, "https://site.example.test/hook", "secret"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = f.pool.Exec(c, `DELETE FROM webhook_subscribers WHERE channel_id = $1`, siteChannel)
		_, _ = f.pool.Exec(c, `DELETE FROM sales_channels WHERE id = $1`, siteChannel)
	})
	site, _ := f.resendSeedOrder(siteChannel, "public_feed", "site-"+suffix+"@example.test")
	all := []uuid.UUID{order, scanned, gateway, site, f.expiredOrder}
	f.reissueCleanup(all, []string{newEmail, "scan2-" + suffix + "@example.org", "gw2-" + suffix + "@example.org", "site2-" + suffix + "@example.org"})

	body := func(email string) string {
		return `{"email":"` + email + `","reason":"sent_to_wrong_address","note":"Called the box office, checked the name"}`
	}

	t.Run("access_and_refusals_write_nothing", func(t *testing.T) {
		before := f.reissueSnap(order)
		cases := []struct {
			name, org, tok, key, body string
			orderID                   uuid.UUID
			code                      int
			errCode                   string
		}{
			{"agent", f.orgA.String(), f.agentTok, "k1", body(newEmail), order, 403, ""},
			{"foreign organization", f.orgB.String(), f.managerTok, "k2", body(newEmail), order, 404, "orders.not_found"},
			{"no key", f.orgA.String(), f.managerTok, "", body(newEmail), order, 400, "reissue.idempotency_key_required"},
			{"bad address", f.orgA.String(), f.managerTok, "k3", body("not-an-address"), order, 422, "reissue.invalid_email"},
			{"bad reason", f.orgA.String(), f.managerTok, "k4", `{"email":"` + newEmail + `","reason":"typo"}`, order, 422, "reissue.invalid_reason"},
			{"long note", f.orgA.String(), f.managerTok, "k5", `{"email":"` + newEmail + `","reason":"other","note":"` + strings.Repeat("x", 501) + `"}`, order, 422, "reissue.note_too_long"},
			{"unknown field", f.orgA.String(), f.managerTok, "k6", `{"email":"` + newEmail + `","reason":"other","x":1}`, order, 400, "reissue.invalid_body"},
			{"unpaid order", f.orgA.String(), f.managerTok, "k7", body(newEmail), f.expiredOrder, 409, "order.not_paid"},
			{"live selling site", f.orgA.String(), f.managerTok, "k8", body("site2-" + suffix + "@example.org"), site, 409, "order.seller_site_order"},
		}
		for _, c := range cases {
			path := "/v1/organizations/" + c.org + "/orders/" + c.orderID.String() + "/reissue-tickets"
			code, b := f.reissueDo(http.MethodPost, path, c.tok, c.key, c.body)
			if code != c.code || (c.errCode != "" && ecErrCode(b) != c.errCode) {
				t.Fatalf("%s: %d %v, want %d %s", c.name, code, b, c.code, c.errCode)
			}
		}
		if after := f.reissueSnap(order); after != before {
			t.Fatalf("refusals changed the order: %+v -> %+v", before, after)
		}
		if n := f.count(`SELECT count(*) FROM ticket_reissues WHERE order_id = ANY($1)`, all); n != 0 {
			t.Fatalf("%d journal rows after refusals", n)
		}
		// The options screen says the same before any write.
		code, b := f.reissueDo(http.MethodGet, reissuePath(f.orgA, site, "reissue-options"), f.managerTok, "", "")
		if code != 200 || b["route"] != "seller_site" || b["blocked"] != "order.seller_site_order" {
			t.Fatalf("site options: %d %v", code, b)
		}
		if code, _ := f.reissueDo(http.MethodGet, reissuePath(f.orgB, order, "reissue-options"), f.managerTok, "", ""); code != 404 {
			t.Fatalf("foreign options: %d, want 404", code)
		}
	})

	var reissueID string
	var pairs []any
	t.Run("reissue_whole_order", func(t *testing.T) {
		before := f.reissueSnap(order)
		code, b := f.reissueDo(http.MethodPost, reissuePath(f.orgA, order, "reissue-tickets"), f.managerTok, "key-"+suffix, body(newEmail))
		if code != 200 || b["state"] != ticketreissue.StatePending || b["replayed"] != false {
			t.Fatalf("reissue: %d %v", code, b)
		}
		reissueID, _ = b["reissue_id"].(string)
		pairs, _ = b["tickets"].([]any)
		if len(pairs) != 2 || strings.Contains(b["recipient_masked"].(string), "real-") {
			t.Fatalf("answer: %v", b)
		}
		after := f.reissueSnap(order)
		if after.active != 2 || after.transferred != 2 || after.items != before.items || after.total != before.total {
			t.Fatalf("tickets or money moved: %+v -> %+v", before, after)
		}
		if after.email != before.email {
			t.Fatalf("the order address changed before the letter: %q", after.email)
		}
		if after.jobs != 2 {
			t.Fatalf("%d letter jobs, want 2 (one per new ticket, folded into one letter)", after.jobs)
		}
		// Old tickets: transferred, codes and credentials revoked. New ones:
		// same seat, a fresh EAN-13, the order lines point at them.
		if n := f.count(`SELECT count(*) FROM tickets WHERE id = ANY($1) AND status = 'transferred' AND cancellation_reason = 'reissued'`,
			[]uuid.UUID{f.ticket1, f.ticket2}); n != 2 {
			t.Fatalf("%d old tickets closed, want 2", n)
		}
		if n := f.count(`SELECT count(*) FROM ticket_credentials WHERE ticket_id = $1 AND revoked_at IS NULL`, f.ticket1); n != 0 {
			t.Fatalf("old ticket keeps %d live credentials", n)
		}
		for _, p := range pairs {
			m := p.(map[string]any)
			newID := uuid.MustParse(m["new_ticket_id"].(string))
			oldID := uuid.MustParse(m["old_ticket_id"].(string))
			if n := f.count(`SELECT count(*) FROM tickets n JOIN tickets o ON o.id = $2
				WHERE n.id = $1 AND n.status = 'active' AND n.seat_key IS NOT DISTINCT FROM o.seat_key
				  AND n.seat_row IS NOT DISTINCT FROM o.seat_row AND n.tier_id = o.tier_id
				  AND n.system_ticket_id <> o.system_ticket_id`, newID, oldID); n != 1 {
				t.Fatalf("replacement %s is not on the old seat", newID)
			}
			if n := f.count(`SELECT count(*) FROM ticket_credentials WHERE ticket_id = $1 AND type = 'ean13' AND revoked_at IS NULL`, newID); n != 1 {
				t.Fatalf("replacement %s has %d barcodes", newID, n)
			}
			if n := f.count(`SELECT count(*) FROM order_items WHERE ticket_id = $1 AND total = 2500`, newID); n != 1 {
				t.Fatalf("order line did not move to %s", newID)
			}
			if j := f.deliveryJob(newID); j.status != "pending" || j.recipient != newEmail {
				t.Fatalf("delivery job of %s: %+v", newID, j)
			}
		}
		// MACS: one refunded old number and one paid new ticket per pair.
		if n := f.count(`SELECT count(*) FROM outbox_events WHERE event_type = $1 AND aggregate_id = ANY($2)`,
			ticketreissue.EventOldTicket, []string{f.ticket1.String(), f.ticket2.String()}); n != 2 {
			t.Fatalf("%d old-ticket events, want 2", n)
		}
		// The purchase token of the first letter no longer opens the order.
		code, _ = f.reissueDo(http.MethodGet, "/v1/public/checkout/"+oldToken, "", "", "")
		if code != http.StatusNotFound {
			t.Fatalf("old purchase token: %d, want 404", code)
		}
		// Journal keeps both addresses; history and audit carry none.
		if n := f.count(`SELECT count(*) FROM ticket_reissues WHERE id = $1 AND new_email = $2 AND old_email = $3
			AND reason = 'sent_to_wrong_address' AND note <> ''`, uuid.MustParse(reissueID), newEmail, before.email); n != 1 {
			t.Fatal("journal row lacks the addresses")
		}
		if n := f.count(`SELECT count(*) FROM order_events WHERE order_id = $1 AND type = 'tickets_reissued'
			AND payload::text NOT LIKE '%' || $2 || '%'`, order, newEmail); n != 1 {
			t.Fatal("order history row missing or carries the address")
		}
		if n := f.count(`SELECT count(*) FROM audit_events WHERE action = $1 AND resource_id = $2
			AND metadata::text NOT LIKE '%' || $3 || '%' AND metadata::text NOT LIKE '%' || $4 || '%'`,
			ticketreissue.AuditAction, order.String(), newEmail, before.email); n != 1 {
			t.Fatal("audit row missing or carries an address")
		}
	})

	t.Run("replay_and_second_operation", func(t *testing.T) {
		before := f.reissueSnap(order)
		code, b := f.reissueDo(http.MethodPost, reissuePath(f.orgA, order, "reissue-tickets"), f.managerTok, "key-"+suffix, body(newEmail))
		if code != 200 || b["replayed"] != true || b["reissue_id"] != reissueID {
			t.Fatalf("replay: %d %v", code, b)
		}
		code, b = f.reissueDo(http.MethodPost, reissuePath(f.orgA, order, "reissue-tickets"), f.managerTok, "key-"+suffix, body("other-"+suffix+"@example.org"))
		if code != 409 || ecErrCode(b) != "reissue.idempotency_key_reused" {
			t.Fatalf("same key other request: %d %v", code, b)
		}
		code, b = f.reissueDo(http.MethodPost, reissuePath(f.orgA, order, "reissue-tickets"), f.managerTok, "key2-"+suffix, body(newEmail))
		if code != 409 || ecErrCode(b) != "reissue.in_progress" {
			t.Fatalf("second operation while open: %d %v", code, b)
		}
		if after := f.reissueSnap(order); after != before {
			t.Fatalf("replay or refusal wrote something: %+v -> %+v", before, after)
		}
	})

	newIDs := func() []uuid.UUID {
		var ids []uuid.UUID
		for _, p := range pairs {
			ids = append(ids, uuid.MustParse(p.(map[string]any)["new_ticket_id"].(string)))
		}
		return ids
	}

	t.Run("address_moves_only_after_the_letter", func(t *testing.T) {
		before := f.reissueSnap(order)
		if err := ticketreissue.AfterLetter(ctx, f.pool, logger, newIDs(), newEmail, false); err != nil {
			t.Fatal(err)
		}
		if n := f.count(`SELECT count(*) FROM ticket_reissues WHERE id = $1 AND state = 'send_failed'`, uuid.MustParse(reissueID)); n != 1 {
			t.Fatal("a refused letter must mark the reissue send_failed")
		}
		if s := f.reissueSnap(order); s.email != before.email {
			t.Fatalf("a refused letter moved the address to %q", s.email)
		}
		code, b := f.reissueDo(http.MethodPost, reissuePath(f.orgA, order, "reissues/"+reissueID+"/resend"), f.managerTok, "", "")
		if code != http.StatusAccepted || b["state"] != ticketreissue.StatePending {
			t.Fatalf("resend: %d %v", code, b)
		}
		var customerBefore *uuid.UUID
		_ = f.pool.QueryRow(ctx, `SELECT customer_id FROM orders WHERE id = $1`, order).Scan(&customerBefore)
		if err := ticketreissue.AfterLetter(ctx, f.pool, logger, newIDs(), newEmail, true); err != nil {
			t.Fatal(err)
		}
		if s := f.reissueSnap(order); s.email != newEmail {
			t.Fatalf("after the letter the order address is %q, want the new one", s.email)
		}
		var customerAfter *uuid.UUID
		_ = f.pool.QueryRow(ctx, `SELECT customer_id FROM orders WHERE id = $1`, order).Scan(&customerAfter)
		if customerAfter == nil || (customerBefore != nil && *customerAfter == *customerBefore) {
			t.Fatalf("the order was not tied to the new address's customer: %v -> %v", customerBefore, customerAfter)
		}
		if customerBefore != nil {
			if n := f.count(`SELECT count(*) FROM customers WHERE id = $1`, *customerBefore); n != 1 {
				t.Fatal("the old customer must stay")
			}
		}
		if n := f.count(`SELECT count(*) FROM ticket_reissues WHERE id = $1 AND state = 'applied'
			AND provider_email_state = 'not_applicable'`, uuid.MustParse(reissueID)); n != 1 {
			t.Fatal("reissue not applied, or a provider step for an order without a payment")
		}
		code, b = f.reissueDo(http.MethodPost, reissuePath(f.orgA, order, "reissues/"+reissueID+"/resend"), f.managerTok, "", "")
		if code != 409 || ecErrCode(b) != "reissue.already_applied" {
			t.Fatalf("resend after apply: %d %v", code, b)
		}
		code, b = f.reissueDo(http.MethodGet, reissuePath(f.orgA, order, "reissues"), f.managerTok, "", "")
		list, _ := b["reissues"].([]any)
		if code != 200 || len(list) != 1 {
			t.Fatalf("history: %d %v", code, b)
		}
		if code, _ := f.reissueDo(http.MethodGet, reissuePath(f.orgB, order, "reissues"), f.managerTok, "", ""); code != 404 {
			t.Fatalf("foreign history: %d, want 404", code)
		}
	})

	t.Run("payment_provider_address", func(t *testing.T) {
		for _, c := range []struct {
			provider, wantState string
			jobs                int
		}{{"stripe", "queued", 1}, {"allpay", "manual", 0}} {
			buyer := c.provider + "-" + suffix + "@example.test"
			o, _ := f.resendSeedOrder(f.channelA, "public_feed", buyer)
			target := c.provider + "2-" + suffix + "@example.org"
			f.reissueCleanup([]uuid.UUID{o}, []string{target})
			if _, err := f.pool.Exec(ctx, `INSERT INTO payment_intents (checkout_session_id, org_id, provider, provider_payment_id, amount, currency, state)
				SELECT checkout_session_id, org_id, $2, $3, 2500, 'EUR', 'succeeded' FROM orders WHERE id = $1`,
				o, c.provider, "cs_test_reissue_"+uuid.NewString()); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_, _ = f.pool.Exec(context.Background(), `DELETE FROM payment_intents
					WHERE checkout_session_id = (SELECT checkout_session_id FROM orders WHERE id = $1)`, o)
			})
			code, b := f.reissueDo(http.MethodPost, reissuePath(f.orgA, o, "reissue-tickets"), f.managerTok, c.provider+"-"+suffix,
				`{"email":"`+target+`","reason":"sent_to_wrong_address"}`)
			if code != 200 {
				t.Fatalf("%s: %d %v", c.provider, code, b)
			}
			ts, _ := b["tickets"].([]any)
			newID := uuid.MustParse(ts[0].(map[string]any)["new_ticket_id"].(string))
			if err := ticketreissue.AfterLetter(ctx, f.pool, logger, []uuid.UUID{newID}, target, true); err != nil {
				t.Fatal(err)
			}
			rid := uuid.MustParse(b["reissue_id"].(string))
			if n := f.count(`SELECT count(*) FROM ticket_reissues WHERE id = $1 AND provider_email_state = $2`, rid, c.wantState); n != 1 {
				t.Fatalf("%s: provider step is not %s", c.provider, c.wantState)
			}
			if n := f.count(`SELECT count(*) FROM worker_jobs WHERE job_type = 'reissue.provider_email' AND payload->>'reissue_id' = $1`,
				rid.String()); n != c.jobs {
				t.Fatalf("%s: %d provider jobs, want %d", c.provider, n, c.jobs)
			}
			// The options screen names the provider and whether arena can move its address.
			_, opts := f.reissueDo(http.MethodGet, reissuePath(f.orgA, o, "reissue-options"), f.managerTok, "", "")
			p, _ := opts["provider"].(map[string]any)
			if p == nil || p["name"] != c.provider || p["email_updatable"] != (c.provider == "stripe") {
				t.Fatalf("%s options provider: %v", c.provider, opts["provider"])
			}
		}
	})

	t.Run("invitation_order_gets_an_invitation_letter", func(t *testing.T) {
		o, _ := f.resendSeedOrder(f.channelA, "complimentary", "inv-order-"+suffix+"@example.test")
		target := "inv-order2-" + suffix + "@example.org"
		f.reissueCleanup([]uuid.UUID{o}, []string{target})
		code, b := f.reissueDo(http.MethodPost, reissuePath(f.orgA, o, "reissue-tickets"), f.managerTok, "invorder-"+suffix,
			`{"email":"`+target+`","reason":"other"}`)
		if code != 200 {
			t.Fatalf("invitation order: %d %v", code, b)
		}
		ts, _ := b["tickets"].([]any)
		newID := ts[0].(map[string]any)["new_ticket_id"].(string)
		if n := f.count(`SELECT count(*) FROM worker_jobs WHERE job_type = 'ticket.deliver' AND payload->>'ticket_id' = $1
			AND payload->>'template' = 'invitation'`, newID); n != 1 {
			t.Fatal("an invitation order must be replaced by an invitation letter")
		}
	})

	t.Run("used_code_does_not_stop_it", func(t *testing.T) {
		if _, err := f.pool.Exec(ctx, `UPDATE tickets SET used_at = now() - interval '5 minutes' WHERE id = $1`, scannedTicket); err != nil {
			t.Fatal(err)
		}
		code, b := f.reissueDo(http.MethodPost, reissuePath(f.orgA, scanned, "reissue-tickets"), f.managerTok, "scan-"+suffix,
			`{"email":"scan2-`+suffix+`@example.org","reason":"forwarded_to_stranger"}`)
		if code != 200 {
			t.Fatalf("scanned order: %d %v", code, b)
		}
		ts, _ := b["tickets"].([]any)
		if len(ts) != 1 || ts[0].(map[string]any)["old_code_scanned_at"] == nil {
			t.Fatalf("the used code's time is missing: %v", b)
		}
		if n := f.count(`SELECT count(*) FROM ticket_reissues WHERE order_id = $1 AND old_code_scanned_at IS NOT NULL`, scanned); n != 1 {
			t.Fatal("journal lacks old_code_scanned_at")
		}
	})

	t.Run("gateway_order_without_a_live_site", func(t *testing.T) {
		code, b := f.reissueDo(http.MethodPost, reissuePath(f.orgA, gateway, "reissue-tickets"), f.managerTok, "gw-"+suffix,
			`{"email":"gw2-`+suffix+`@example.org","reason":"other"}`)
		if code != 200 {
			t.Fatalf("a former site order with no live subscriber: %d %v, want 200", code, b)
		}
		// The same site switched off: its order reissues too (decision R-8).
		if _, err := f.pool.Exec(ctx, `UPDATE webhook_subscribers SET active = false WHERE channel_id = $1`, siteChannel); err != nil {
			t.Fatal(err)
		}
		code, b = f.reissueDo(http.MethodPost, reissuePath(f.orgA, site, "reissue-tickets"), f.managerTok, "site-"+suffix,
			`{"email":"site2-`+suffix+`@example.org","reason":"other"}`)
		if code != 200 {
			t.Fatalf("switched-off site: %d %v, want 200", code, b)
		}
	})
}

// TestReissue_InvitationWithoutOrder: an invitation issued before spec 46 has
// no order and no checkout session. Its tickets are replaced the same way
// (stage R-5) and, after the letter, the invitation's recipient moves.
func TestReissue_InvitationWithoutOrder(t *testing.T) {
	f := newECOrdersFixture(t)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	suffix := uuid.NewString()[:8]
	guest := "guest-" + suffix + "@example.test"
	newEmail := "guest2-" + suffix + "@example.org"
	issuance, ticket := uuid.New(), uuid.New()
	var tier uuid.UUID
	if err := f.pool.QueryRow(ctx, `SELECT id FROM ticket_tiers WHERE session_id = $1 LIMIT 1`, f.sessionA).Scan(&tier); err != nil {
		t.Fatal(err)
	}
	for i, st := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO complimentary_issuances (id, org_id, session_id, tier_id, qty, recipients, batch_id, status)
		  VALUES ($1, $2, $3, $4, 1, ARRAY[$5::text], $6, 'issued')`, []any{issuance, f.orgA, f.sessionA, tier, guest, "batch-" + suffix}},
		{`INSERT INTO tickets (id, session_id, tier_id, holder_email, complimentary_issuance_id, ordinal)
		  VALUES ($1, $2, $3, $4, $5, 0)`, []any{ticket, f.sessionA, tier, guest, issuance}},
	} {
		if _, err := f.pool.Exec(ctx, st.sql, st.args...); err != nil {
			t.Fatalf("invitation seed %d: %v", i, err)
		}
	}
	t.Cleanup(func() {
		c := context.Background()
		var ids []uuid.UUID
		rows, _ := f.pool.Query(c, `SELECT id FROM tickets WHERE complimentary_issuance_id = $1`, issuance)
		for rows.Next() {
			var id uuid.UUID
			if rows.Scan(&id) == nil {
				ids = append(ids, id)
			}
		}
		rows.Close()
		texts := make([]string, 0, len(ids))
		for _, id := range ids {
			texts = append(texts, id.String())
		}
		for _, st := range []struct {
			sql  string
			args []any
		}{
			{`DELETE FROM worker_jobs WHERE payload->>'ticket_id' = ANY($1)`, []any{texts}},
			{`DELETE FROM outbox_events WHERE aggregate_id = ANY($1)`, []any{texts}},
			{`DELETE FROM ticket_reissue_items WHERE reissue_id IN (SELECT id FROM ticket_reissues WHERE complimentary_issuance_id = $1)`, []any{issuance}},
			{`DELETE FROM ticket_reissues WHERE complimentary_issuance_id = $1`, []any{issuance}},
			{`DELETE FROM delivery_jobs WHERE ticket_id = ANY($1)`, []any{ids}},
			{`DELETE FROM barcodes WHERE ticket_id = ANY($1)`, []any{ids}},
			{`DELETE FROM ticket_credentials WHERE ticket_id = ANY($1)`, []any{ids}},
			{`DELETE FROM tickets WHERE id = ANY($1)`, []any{ids}},
			{`DELETE FROM complimentary_issuances WHERE id = $1`, []any{issuance}},
		} {
			if _, err := f.pool.Exec(c, st.sql, st.args...); err != nil {
				t.Logf("invitation cleanup: %s: %v", st.sql, err)
			}
		}
	})
	base := "/v1/organizations/" + f.orgA.String() + "/complimentary/" + issuance.String() + "/"

	code, opts := f.reissueDo(http.MethodGet, base+"reissue-options", f.managerTok, "", "")
	if code != 200 || opts["guest_email"] != guest || opts["blocked"] != nil {
		t.Fatalf("options: %d %v", code, opts)
	}
	if code, _ := f.reissueDo(http.MethodGet, "/v1/organizations/"+f.orgB.String()+"/complimentary/"+issuance.String()+"/reissue-options",
		f.managerTok, "", ""); code != 404 {
		t.Fatalf("foreign organization: %d, want 404", code)
	}
	if code, _ := f.reissueDo(http.MethodPost, base+"reissue-tickets", f.agentTok, "inv-a-"+suffix,
		`{"email":"`+newEmail+`","reason":"other"}`); code != 403 {
		t.Fatalf("agent: %d, want 403", code)
	}
	code, b := f.reissueDo(http.MethodPost, base+"reissue-tickets", f.managerTok, "inv-"+suffix,
		`{"email":"`+newEmail+`","reason":"sent_to_wrong_address"}`)
	if code != 200 || b["state"] != ticketreissue.StatePending {
		t.Fatalf("reissue: %d %v", code, b)
	}
	ts, _ := b["tickets"].([]any)
	if len(ts) != 1 {
		t.Fatalf("tickets: %v", b)
	}
	newID := uuid.MustParse(ts[0].(map[string]any)["new_ticket_id"].(string))
	if n := f.count(`SELECT count(*) FROM tickets WHERE id = $1 AND status = 'transferred'`, ticket); n != 1 {
		t.Fatal("old invitation ticket not closed")
	}
	if n := f.count(`SELECT count(*) FROM tickets WHERE id = $1 AND status = 'active' AND complimentary_issuance_id = $2
		AND order_id IS NULL AND checkout_session_id IS NULL`, newID, issuance); n != 1 {
		t.Fatal("replacement is not an invitation ticket of the same issuance")
	}
	if n := f.count(`SELECT count(*) FROM worker_jobs WHERE job_type = 'ticket.deliver' AND payload->>'ticket_id' = $1
		AND payload->>'template' = 'invitation'`, newID.String()); n != 1 {
		t.Fatal("the letter is not an invitation letter")
	}
	// Replay, then a second operation while the first is open.
	if code, b := f.reissueDo(http.MethodPost, base+"reissue-tickets", f.managerTok, "inv-"+suffix,
		`{"email":"`+newEmail+`","reason":"sent_to_wrong_address"}`); code != 200 || b["replayed"] != true {
		t.Fatalf("replay: %d %v", code, b)
	}
	if code, b := f.reissueDo(http.MethodPost, base+"reissue-tickets", f.managerTok, "inv2-"+suffix,
		`{"email":"`+newEmail+`","reason":"other"}`); code != 409 || ecErrCode(b) != "reissue.in_progress" {
		t.Fatalf("second operation: %d %v", code, b)
	}
	// The recipient moves only after the letter.
	if n := f.count(`SELECT count(*) FROM complimentary_issuances WHERE id = $1 AND recipients = ARRAY[$2::text]`, issuance, guest); n != 1 {
		t.Fatal("the recipient moved before the letter")
	}
	if err := ticketreissue.AfterLetter(ctx, f.pool, logger, []uuid.UUID{newID}, newEmail, true); err != nil {
		t.Fatal(err)
	}
	if n := f.count(`SELECT count(*) FROM complimentary_issuances WHERE id = $1 AND recipients = ARRAY[$2::text]`, issuance, newEmail); n != 1 {
		t.Fatal("the recipient did not move after the letter")
	}
	code, h := f.reissueDo(http.MethodGet, base+"reissues", f.managerTok, "", "")
	list, _ := h["reissues"].([]any)
	if code != 200 || len(list) != 1 || list[0].(map[string]any)["state"] != ticketreissue.StateApplied {
		t.Fatalf("history: %d %v", code, h)
	}
	// A revoked invitation is not replaced.
	if _, err := f.pool.Exec(ctx, `UPDATE complimentary_issuances SET status = 'revoked' WHERE id = $1`, issuance); err != nil {
		t.Fatal(err)
	}
	if code, b := f.reissueDo(http.MethodPost, base+"reissue-tickets", f.managerTok, "inv3-"+suffix,
		`{"email":"`+newEmail+`","reason":"other"}`); code != 409 || ecErrCode(b) != "reissue.invitation_not_issued" {
		t.Fatalf("revoked invitation: %d %v", code, b)
	}
}
