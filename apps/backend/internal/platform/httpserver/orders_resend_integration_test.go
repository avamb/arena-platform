//go:build integration

// orders_resend_integration_test.go — EC-13 (spec 35 section 6.4): POST
// /v1/organizations/{org_id}/orders/{order_id}/resend-tickets through the REAL
// router with a manager's JWT (membership role organizer, empty roles claim —
// what the Telegram bot mints). It proves the 404 for a foreign organization,
// the 403 for an agent, the 409s (unpaid order, no active ticket, seller-site
// order) that queue nothing, the 202 for the order's own address and for a
// one-time one: every active ticket's delivery job is requeued (and folds into
// ONE letter), the one-time address and its 24-hour deadline live on the
// delivery job and the worker payload and never on the order, refunded
// tickets are skipped, and the order history and the audit event carry no
// address.
//
// Run against a FRESH migrated database (AGENTS.md CI-Integration recipe).
package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// resendPost posts to the resend route as token and decodes the answer.
func (f *ecOrdersFixture) resendPost(orgID, orderID uuid.UUID, token, body string) (int, map[string]any) {
	f.t.Helper()
	url := f.ts.URL + "/v1/organizations/" + orgID.String() + "/orders/" + orderID.String() + "/resend-tickets"
	resp := integDoRequest(f.t, f.ts.Client(), http.MethodPost, url, token, body)
	raw := integReadBody(f.t, resp)
	out := map[string]any{}
	if strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			f.t.Fatalf("POST resend: decode %q: %v", raw, err)
		}
	}
	return resp.StatusCode, out
}

// resendSeedOrder inserts a paid one-ticket order of org A on the given channel
// with the given source, with a delivery job already 'sent', and returns the
// order and ticket ids.
func (f *ecOrdersFixture) resendSeedOrder(channel uuid.UUID, source, buyerEmail string) (order, ticket uuid.UUID) {
	f.t.Helper()
	ctx := context.Background()
	order, ticket = uuid.New(), uuid.New()
	res, cs := uuid.New(), uuid.New()
	var tier uuid.UUID
	if err := f.pool.QueryRow(ctx, `SELECT id FROM ticket_tiers WHERE session_id = $1 LIMIT 1`, f.sessionA).Scan(&tier); err != nil {
		f.t.Fatalf("fixture tier: %v", err)
	}
	steps := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO reservations (id, org_id, channel_id, session_id, quantity, state, expires_at, converted_at)
		  VALUES ($1, $2, $3, $4, 1, 'converted', now() + interval '1 hour', now())`, []any{res, f.orgA, channel, f.sessionA}},
		{`INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state) VALUES ($1, $2, $3, $4, 'completed')`,
			[]any{cs, f.orgA, channel, res}},
		{`INSERT INTO orders (id, org_id, channel_id, event_id, session_id, checkout_session_id, reservation_id,
		                      source, status, currency, subtotal, discount, charge, total, buyer_name, buyer_email, paid_at)
		  VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'paid', 'EUR', 2500, 0, 0, 2500, 'Resend Buyer', $9, now())`,
			[]any{order, f.orgA, channel, f.eventA, f.sessionA, cs, res, source, buyerEmail}},
		{`INSERT INTO tickets (id, checkout_session_id, session_id, tier_id, holder_email, order_id, ordinal)
		  VALUES ($1, $2, $3, $4, $5, $6, 0)`, []any{ticket, cs, f.sessionA, tier, buyerEmail, order}},
		{`INSERT INTO order_items (id, order_id, ordinal, kind, tier_id, ticket_id, unit_price, discount, charge, total)
		  VALUES ($1, $2, 0, 'ticket', $3, $4, 2500, 0, 0, 2500)`, []any{uuid.New(), order, tier, ticket}},
		{`INSERT INTO delivery_jobs (ticket_id, recipient_email, status, sent_at) VALUES ($1, $2, 'sent', now())`,
			[]any{ticket, buyerEmail}},
	}
	for i, s := range steps {
		if _, err := f.pool.Exec(ctx, s.sql, s.args...); err != nil {
			f.t.Fatalf("resend seed step %d: %v", i, err)
		}
	}
	f.t.Cleanup(func() {
		c := context.Background()
		for _, s := range []string{
			`DELETE FROM worker_jobs WHERE payload->>'ticket_id' = '` + ticket.String() + `'`,
			`DELETE FROM delivery_jobs WHERE ticket_id = '` + ticket.String() + `'`,
			`DELETE FROM order_items WHERE order_id = '` + order.String() + `'`,
			`DELETE FROM tickets WHERE id = '` + ticket.String() + `'`,
			`DELETE FROM orders WHERE id = '` + order.String() + `'`,
			`DELETE FROM checkout_sessions WHERE id = '` + cs.String() + `'`,
			`DELETE FROM reservations WHERE id = '` + res.String() + `'`,
		} {
			if _, err := f.pool.Exec(c, s); err != nil {
				f.t.Logf("resend seed cleanup: %s: %v", s, err)
			}
		}
	})
	return order, ticket
}

type resendJob struct {
	status, recipient string
}

func (f *ecOrdersFixture) deliveryJob(ticket uuid.UUID) resendJob {
	f.t.Helper()
	var j resendJob
	var rcpt *string
	if err := f.pool.QueryRow(context.Background(),
		`SELECT status, recipient_email FROM delivery_jobs WHERE ticket_id = $1`, ticket).Scan(&j.status, &rcpt); err != nil {
		f.t.Fatalf("delivery job of %s: %v", ticket, err)
	}
	if rcpt != nil {
		j.recipient = *rcpt
	}
	return j
}

// workerPayloads returns the payloads of the ticket.deliver jobs of a ticket.
func (f *ecOrdersFixture) workerPayloads(ticket uuid.UUID) []map[string]any {
	f.t.Helper()
	rows, err := f.pool.Query(context.Background(),
		`SELECT payload FROM worker_jobs WHERE job_type = 'ticket.deliver' AND payload->>'ticket_id' = $1 ORDER BY created_at`, ticket.String())
	if err != nil {
		f.t.Fatalf("worker payloads: %v", err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			f.t.Fatal(err)
		}
		m := map[string]any{}
		if err := json.Unmarshal(raw, &m); err != nil {
			f.t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}

func (f *ecOrdersFixture) count(sql string, args ...any) int {
	f.t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		f.t.Fatalf("count %q: %v", sql, err)
	}
	return n
}

func TestOrderResendTickets_Integration(t *testing.T) {
	f := newECOrdersFixture(t)
	ctx := context.Background()
	order := f.paidOrder
	var buyerEmail string
	if err := f.pool.QueryRow(ctx, `SELECT buyer_email FROM orders WHERE id = $1`, order).Scan(&buyerEmail); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(),
			`DELETE FROM worker_jobs WHERE payload->>'ticket_id' = ANY($1)`, []string{f.ticket1.String(), f.ticket2.String()})
	})

	t.Run("access", func(t *testing.T) {
		// An agent holds no ticket.update.
		if code, body := f.resendPost(f.orgA, order, f.agentTok, ""); code != http.StatusForbidden {
			t.Fatalf("agent: %d %v, want 403", code, body)
		}
		// The manager is a member of org B too, but the order is org A's: its own 404.
		if code, body := f.resendPost(f.orgB, order, f.managerTok, ""); code != http.StatusNotFound || ecErrCode(body) != "orders.not_found" {
			t.Fatalf("foreign organization: %d %v, want 404 orders.not_found", code, body)
		}
		if code, body := f.resendPost(f.orgA, uuid.New(), f.managerTok, ""); code != http.StatusNotFound {
			t.Fatalf("unknown order: %d %v, want 404", code, body)
		}
		if code, _ := f.resendPost(f.orgA, order, "", ""); code != http.StatusUnauthorized {
			t.Fatalf("no token: %d, want 401", code)
		}
	})

	t.Run("bad_input_and_unpaid_queue_nothing", func(t *testing.T) {
		for _, bad := range []string{`{"email":"not-an-email"}`, `{"email":"a b@example.com"}`, `{"email":"x@nodot"}`,
			`{"email":"Name <x@example.com>"}`, `{"nope":1}`, `[1]`} {
			if code, body := f.resendPost(f.orgA, order, f.managerTok, bad); code != http.StatusBadRequest {
				t.Fatalf("body %s: %d %v, want 400", bad, code, body)
			}
		}
		if code, body := f.resendPost(f.orgA, f.expiredOrder, f.managerTok, ""); code != http.StatusConflict || ecErrCode(body) != "order.not_paid" {
			t.Fatalf("expired order: %d %v, want 409 order.not_paid", code, body)
		}
		if n := len(f.workerPayloads(f.ticket1)) + len(f.workerPayloads(f.ticket2)); n != 0 {
			t.Fatalf("%d worker jobs after refusals, want 0", n)
		}
		if j := f.deliveryJob(f.ticket1); j.status != "sent" {
			t.Fatalf("a refused resend must leave the job alone, got %+v", j)
		}
	})

	t.Run("same_address_one_letter", func(t *testing.T) {
		code, body := f.resendPost(f.orgA, order, f.managerTok, "")
		if code != http.StatusAccepted {
			t.Fatalf("resend: %d %v, want 202", code, body)
		}
		if body["queued_tickets"] != float64(2) || body["different_address"] != false || body["expires_at"] != nil {
			t.Fatalf("answer = %v", body)
		}
		if m, _ := body["recipient_masked"].(string); !strings.Contains(m, "***") || strings.Contains(m, buyerEmail) {
			t.Fatalf("recipient_masked = %q", m)
		}
		for _, tk := range []uuid.UUID{f.ticket1, f.ticket2} {
			if j := f.deliveryJob(tk); j.status != "pending" || j.recipient != buyerEmail {
				t.Fatalf("ticket %s job = %+v, want pending to the order's address", tk, j)
			}
			ps := f.workerPayloads(tk)
			if len(ps) != 1 {
				t.Fatalf("ticket %s: %d worker jobs, want 1", tk, len(ps))
			}
			if _, has := ps[0]["recipient_expires_at"]; has {
				t.Fatalf("the order's own address carries no deadline: %v", ps[0])
			}
		}
		// ONE letter: claiming for the first ticket takes both jobs.
		claimed, err := gen.New(f.pool).ClaimPendingDeliveryJobsForOrder(ctx, f.ticket1)
		if err != nil || len(claimed) != 2 {
			t.Fatalf("claim = %d jobs, err %v, want both tickets in one letter", len(claimed), err)
		}
		// History and audit hold no address.
		var payload string
		if err := f.pool.QueryRow(ctx, `SELECT payload::text FROM order_events WHERE order_id = $1 AND type = 'tickets_resent'`, order).Scan(&payload); err != nil {
			t.Fatalf("order history row: %v", err)
		}
		if strings.Contains(payload, "@") || !strings.Contains(payload, `"tickets": 2`) {
			t.Fatalf("history payload = %s", payload)
		}
		var meta string
		if err := f.pool.QueryRow(ctx,
			`SELECT metadata::text FROM audit_events WHERE action = 'v1.order.tickets_resend' AND resource_id = $1`, order.String()).Scan(&meta); err != nil {
			t.Fatalf("audit row: %v", err)
		}
		if strings.Contains(meta, "@") || !strings.Contains(meta, `"different_address": false`) {
			t.Fatalf("audit metadata = %s", meta)
		}
		// Park the jobs as sent again for the next sub-test.
		if _, err := f.pool.Exec(ctx, `UPDATE delivery_jobs SET status = 'sent', sent_at = now() WHERE ticket_id = ANY($1)`,
			[]uuid.UUID{f.ticket1, f.ticket2}); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("other_address_is_one_time_and_never_on_the_order", func(t *testing.T) {
		other := "someone-" + uuid.NewString()[:8] + "@example.org"
		before := time.Now().UTC()
		code, body := f.resendPost(f.orgA, order, f.managerTok, `{"email":"  `+other+`  "}`)
		if code != http.StatusAccepted || body["different_address"] != true || body["queued_tickets"] != float64(2) {
			t.Fatalf("resend: %d %v", code, body)
		}
		exp, _ := body["expires_at"].(string)
		at, err := time.Parse(time.RFC3339, exp)
		if err != nil || at.Before(before.Add(23*time.Hour+59*time.Minute)) || at.After(before.Add(24*time.Hour+time.Minute)) {
			t.Fatalf("expires_at = %q (%v), want about 24 hours ahead", exp, err)
		}
		for _, tk := range []uuid.UUID{f.ticket1, f.ticket2} {
			if j := f.deliveryJob(tk); j.status != "pending" || j.recipient != other {
				t.Fatalf("ticket %s job = %+v, want pending to the one-time address", tk, j)
			}
			ps := f.workerPayloads(tk)
			if len(ps) != 2 {
				t.Fatalf("ticket %s: %d worker jobs, want 2 (both resends)", tk, len(ps))
			}
			stored, _ := ps[1]["recipient_expires_at"].(string)
			if st, err := time.Parse(time.RFC3339, stored); err != nil || st.Sub(at).Abs() > time.Second {
				t.Fatalf("payload recipient_expires_at = %q, want %s", stored, exp)
			}
			if strings.Contains(string(mustJSON(ps[1])), other) {
				t.Fatalf("the worker payload must not carry the address itself: %v", ps[1])
			}
		}
		// The order's own address is untouched.
		var now string
		if err := f.pool.QueryRow(ctx, `SELECT buyer_email FROM orders WHERE id = $1`, order).Scan(&now); err != nil || now != buyerEmail {
			t.Fatalf("order buyer_email = %q (%v), want unchanged %q", now, err, buyerEmail)
		}
		var holder string
		if err := f.pool.QueryRow(ctx, `SELECT holder_email FROM tickets WHERE id = $1`, f.ticket1).Scan(&holder); err != nil || holder != buyerEmail {
			t.Fatalf("ticket holder_email = %q (%v), want unchanged", holder, err)
		}
		var meta string
		if err := f.pool.QueryRow(ctx,
			`SELECT metadata::text FROM audit_events WHERE action = 'v1.order.tickets_resend' AND resource_id = $1 ORDER BY occurred_at DESC LIMIT 1`,
			order.String()).Scan(&meta); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(meta, other) || !strings.Contains(meta, `"different_address": true`) {
			t.Fatalf("audit metadata = %s", meta)
		}
		if n := f.count(`SELECT count(*) FROM order_events WHERE order_id = $1 AND type = 'tickets_resent'`, order); n != 2 {
			t.Fatalf("%d history rows, want 2", n)
		}
		// Same letter, same recipient: still one claim for the whole order.
		claimed, err := gen.New(f.pool).ClaimPendingDeliveryJobsForOrder(ctx, f.ticket2)
		if err != nil || len(claimed) != 2 {
			t.Fatalf("claim = %d jobs, err %v, want 2", len(claimed), err)
		}
		if _, err := f.pool.Exec(ctx, `UPDATE delivery_jobs SET status = 'sent', sent_at = now() WHERE ticket_id = ANY($1)`,
			[]uuid.UUID{f.ticket1, f.ticket2}); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("refunded_ticket_is_skipped_and_none_left_is_409", func(t *testing.T) {
		if _, err := f.pool.Exec(ctx, `UPDATE tickets SET status = 'cancelled', cancelled_at = now() WHERE id = $1`, f.ticket2); err != nil {
			t.Fatal(err)
		}
		before := len(f.workerPayloads(f.ticket2))
		code, body := f.resendPost(f.orgA, order, f.managerTok, "")
		if code != http.StatusAccepted || body["queued_tickets"] != float64(1) {
			t.Fatalf("resend with one refunded ticket: %d %v", code, body)
		}
		if j := f.deliveryJob(f.ticket2); j.status != "sent" {
			t.Fatalf("the refunded ticket's job must stay as it was, got %+v", j)
		}
		if after := len(f.workerPayloads(f.ticket2)); after != before {
			t.Fatalf("a worker job was queued for the refunded ticket (%d -> %d)", before, after)
		}
		if _, err := f.pool.Exec(ctx, `UPDATE tickets SET status = 'cancelled', cancelled_at = now() WHERE id = $1`, f.ticket1); err != nil {
			t.Fatal(err)
		}
		if code, body := f.resendPost(f.orgA, order, f.managerTok, ""); code != http.StatusConflict || ecErrCode(body) != "order.no_active_tickets" {
			t.Fatalf("no active ticket: %d %v, want 409 order.no_active_tickets", code, body)
		}
	})

	t.Run("seller_site_orders_are_refused", func(t *testing.T) {
		// A channel with an active bil24_wp subscriber.
		siteChannel := uuid.New()
		if _, err := f.pool.Exec(ctx, `INSERT INTO sales_channels (id, org_id, name, settings) VALUES ($1, $2, 'Resend site channel', '{}'::jsonb)`,
			siteChannel, f.orgA); err != nil {
			t.Fatal(err)
		}
		if _, err := gen.New(f.pool).CreateWPWebhookSubscriber(ctx, siteChannel, "https://site.example.test/hook", "secret"); err != nil {
			t.Fatalf("CreateWPWebhookSubscriber: %v", err)
		}
		f.t.Cleanup(func() {
			c := context.Background()
			_, _ = f.pool.Exec(c, `DELETE FROM webhook_subscribers WHERE channel_id = $1`, siteChannel)
			_, _ = f.pool.Exec(c, `DELETE FROM sales_channels WHERE id = $1`, siteChannel)
		})
		viaWebhook, tkWebhook := f.resendSeedOrder(siteChannel, "public_feed", "site-buyer@example.test")
		viaGateway, tkGateway := f.resendSeedOrder(f.channelA, "bil24_gateway", "gateway-buyer@example.test")
		for name, c := range map[string]struct {
			order  uuid.UUID
			ticket uuid.UUID
		}{"active webhook subscriber": {viaWebhook, tkWebhook}, "bil24_gateway source": {viaGateway, tkGateway}} {
			code, body := f.resendPost(f.orgA, c.order, f.managerTok, `{"email":"other@example.org"}`)
			if code != http.StatusConflict || ecErrCode(body) != "order.seller_site_order" {
				t.Fatalf("%s: %d %v, want 409 order.seller_site_order", name, code, body)
			}
			if j := f.deliveryJob(c.ticket); j.status != "sent" {
				t.Fatalf("%s: the job must be untouched, got %+v", name, j)
			}
			if n := len(f.workerPayloads(c.ticket)); n != 0 {
				t.Fatalf("%s: %d worker jobs queued, want 0", name, n)
			}
			if n := f.count(`SELECT count(*) FROM order_events WHERE order_id = $1 AND type = 'tickets_resent'`, c.order); n != 0 {
				t.Fatalf("%s: %d history rows, want 0", name, n)
			}
		}
		// An ordinary order of the same organization still works (control).
		plain, _ := f.resendSeedOrder(f.channelA, "public_feed", "plain-buyer@example.test")
		if code, body := f.resendPost(f.orgA, plain, f.managerTok, ""); code != http.StatusAccepted {
			t.Fatalf("control order: %d %v, want 202", code, body)
		}
	})
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
