//go:build integration

// session_summary_sources_integration_test.go — QL-6, spec 43 section 4.5:
// GET .../sessions/{session_id}/summary groups the session's paid orders by
// sales link, then by utm_source/utm_campaign, the rest as direct. Revenue is
// the orders' totals minus their succeeded refunds, per currency.
//
// Run against a FRESH migrated database (AGENTS.md CI-Integration recipe).
package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
)

func TestSessionSummarySources_LinkUTMAndDirect(t *testing.T) {
	x := newECSumFixture(t)
	ctx := context.Background()
	db := x.q.DB()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("fixture %q: %v", sql, err)
		}
	}

	link := attrTestLink(t, ctx, x.q, x.org, "Poster at the door")
	directOrder, directRes, directCS, directTicket := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	// Runs BEFORE the summary fixture's own cleanup (LIFO): the extra
	// ticket, the refund and the link must go before the orders and the
	// organization they reference.
	t.Cleanup(func() {
		c := context.Background()
		_, _ = db.Exec(c, `DELETE FROM refunds WHERE org_id = $1`, x.org)
		_, _ = db.Exec(c, `DELETE FROM tickets WHERE id = $1`, directTicket)
		attrTestCleanup(db, []uuid.UUID{x.org}, nil)
	})

	var channel uuid.UUID
	if err := db.QueryRow(ctx, `SELECT channel_id FROM orders WHERE id = $1`, x.paidOrder).Scan(&channel); err != nil {
		t.Fatalf("channel of the paid order: %v", err)
	}

	// 1. The paid order (2 tickets, total 4700) came through the link, and
	//    one of its tickets was refunded (2350).
	exec(`UPDATE orders SET sales_link_id = $2, attribution = $3::jsonb WHERE id = $1`,
		x.paidOrder, link.ID, `{"ref":"`+link.Code+`","utm_source":"qr"}`)
	var ticketID, otherTicketID uuid.UUID
	if err := db.QueryRow(ctx, `SELECT id FROM tickets WHERE order_id = $1 ORDER BY ordinal LIMIT 1`, x.paidOrder).Scan(&ticketID); err != nil {
		t.Fatalf("paid order ticket: %v", err)
	}
	if err := db.QueryRow(ctx, `SELECT id FROM tickets WHERE order_id = $1 ORDER BY ordinal OFFSET 1 LIMIT 1`, x.paidOrder).Scan(&otherTicketID); err != nil {
		t.Fatalf("second paid order ticket: %v", err)
	}
	exec(`INSERT INTO refunds (id, org_id, amount, currency, state, settlement, order_id, ticket_id, succeeded_at)
	      VALUES ($1, $2, 2350, 'EUR', 'succeeded', 'external', $3, $4, now())`, uuid.New(), x.org, x.paidOrder, ticketID)
	// A refund that did not succeed is not subtracted.
	exec(`INSERT INTO refunds (id, org_id, amount, currency, state, settlement, order_id, ticket_id)
	      VALUES ($1, $2, 100, 'EUR', 'failed', 'external', $3, $4)`, uuid.New(), x.org, x.paidOrder, otherTicketID)

	// 2. The invitation (paid, total 0, 1 ticket) has utm labels and no link.
	exec(`UPDATE orders SET attribution = '{"utm_source":"instagram","utm_campaign":"c1"}'::jsonb WHERE id = $1`, x.complOrder)

	// 3. A direct paid order of one ticket, total 1000, and a pending order
	//    with a link that must not count.
	exec(`INSERT INTO reservations (id, org_id, channel_id, session_id, quantity, state, expires_at, converted_at)
	      VALUES ($1, $2, $3, $4, 1, 'converted', now() + interval '1 hour', now())`, directRes, x.org, channel, x.sessFuture)
	exec(`INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state) VALUES ($1, $2, $3, $4, 'completed')`,
		directCS, x.org, channel, directRes)
	exec(`INSERT INTO orders (id, org_id, channel_id, event_id, session_id, checkout_session_id, reservation_id,
	                          source, status, currency, subtotal, discount, charge, total, buyer_name, buyer_email)
	      VALUES ($1, $2, $3, $4, $5, $6, $7, 'public_feed', 'paid', 'EUR', 1000, 0, 0, 1000, 'x', 'ql6-direct@example.test')`,
		directOrder, x.org, channel, x.evOnSale, x.sessFuture, directCS, directRes)
	exec(`INSERT INTO tickets (id, checkout_session_id, session_id, tier_id, holder_email, order_id, ordinal)
	      VALUES ($1, $2, $3, $4, 'ql6-direct@example.test', $5, 0)`, directTicket, directCS, x.sessFuture, x.tierFuture, directOrder)
	pendingRes, pendingCS := uuid.New(), uuid.New()
	exec(`INSERT INTO reservations (id, org_id, channel_id, session_id, quantity, state, expires_at)
	      VALUES ($1, $2, $3, $4, 1, 'active', now() + interval '1 hour')`, pendingRes, x.org, channel, x.sessFuture)
	exec(`INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state) VALUES ($1, $2, $3, $4, 'pricing_confirmed')`,
		pendingCS, x.org, channel, pendingRes)
	exec(`INSERT INTO orders (id, org_id, channel_id, event_id, session_id, checkout_session_id, reservation_id,
	                          source, status, currency, subtotal, discount, charge, total, buyer_name, buyer_email, sales_link_id)
	      VALUES ($1, $2, $3, $4, $5, $6, $7, 'public_feed', 'pending_payment', 'EUR', 9000, 0, 0, 9000, 'x', 'ql6-pending@example.test', $8)`,
		uuid.New(), x.org, channel, x.evOnSale, x.sessFuture, pendingCS, pendingRes, link.ID)

	path := "/v1/organizations/" + x.org.String() + "/sessions/" + x.sessFuture.String() + "/summary"
	st, body := x.get(path, x.manager)
	if st != http.StatusOK {
		t.Fatalf("session summary: %d %v", st, body)
	}
	raw, err := json.Marshal(body["sources"])
	if err != nil {
		t.Fatal(err)
	}
	want := `[` +
		`{"kind":"sales_link","label":"Poster at the door","orders":1,"revenue":[{"amount_minor":2350,"currency":"EUR"}],"sales_link_id":"` + link.ID.String() + `","tickets":2},` +
		`{"kind":"utm","orders":1,"revenue":[{"amount_minor":0,"currency":"EUR"}],"tickets":1,"utm_campaign":"c1","utm_source":"instagram"},` +
		`{"kind":"direct","orders":1,"revenue":[{"amount_minor":1000,"currency":"EUR"}],"tickets":1}` +
		`]`
	if string(raw) != want {
		t.Fatalf("sources =\n%s\nwant\n%s", raw, want)
	}

	// The event summary carries no sources (Р-QL9).
	st, ev := x.get("/v1/organizations/"+x.org.String()+"/events/"+x.evOnSale.String()+"/summary", x.manager)
	if st != http.StatusOK {
		t.Fatalf("event summary: %d %v", st, ev)
	}
	if _, has := ev["sources"]; has {
		t.Fatalf("event summary has sources: %v", ev["sources"])
	}
}
