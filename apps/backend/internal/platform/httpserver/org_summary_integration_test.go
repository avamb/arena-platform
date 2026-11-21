//go:build integration

// org_summary_integration_test.go — EC-V1 and EC-V2 (spec 62 §12.2) through
// the REAL router: the organization summary (GET .../summary) and the
// accounting workbook (GET .../exports/accounting.xlsx).
//
// The EC-07 fixture supplies one paid order of two tickets (promo code, 22.50
// each). On top of it this file adds an external refund on one of them, an
// invitation, a paid order of a TEST sales channel and a second session in
// another month, and checks that the cards, the breakdown, the three sheets,
// the test-order exclusion, the date filter, the audit row and the access
// rules all agree.
package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/xlsxexport"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/xlsxexport/xlsxtest"
)

type ecv12Extras struct {
	otherSess uuid.UUID
}

// addEcv12Rows adds the refund, the invitation, the test order and the second
// session to the EC-07 fixture and registers their cleanup (it runs before the
// fixture's own, which deletes the parents).
func addEcv12Rows(t *testing.T, f *ec07Fixture) ecv12Extras {
	t.Helper()
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.q.DB().Exec(ctx, sql, args...); err != nil {
			t.Fatalf("fixture %q: %v", sql, err)
		}
	}
	var (
		tierID, orderID, chanID uuid.UUID
		venueID                 uuid.UUID
	)
	row := f.q.DB().QueryRow(ctx, `SELECT tier_id, order_id FROM tickets WHERE id = $1`, f.ticket2)
	if err := row.Scan(&tierID, &orderID); err != nil {
		t.Fatalf("ticket2: %v", err)
	}
	if err := f.q.DB().QueryRow(ctx, `SELECT venue_id FROM sessions WHERE id = $1`, f.sessID).Scan(&venueID); err != nil {
		t.Fatalf("venue: %v", err)
	}
	if err := f.q.DB().QueryRow(ctx, `SELECT channel_id FROM orders WHERE id = $1`, orderID).Scan(&chanID); err != nil {
		t.Fatalf("channel: %v", err)
	}
	x := ecv12Extras{otherSess: uuid.New()}
	var (
		issuance, testChan, testResv, testCS, testOrder, testTicket, testItem = uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
		inviteTicket, refundID                                                = uuid.New(), uuid.New()
	)
	t.Cleanup(func() {
		for _, sql := range []string{
			`DELETE FROM order_items WHERE order_id = '` + testOrder.String() + `'`,
			`DELETE FROM tickets WHERE id IN ('` + testTicket.String() + `','` + inviteTicket.String() + `')`,
			`DELETE FROM orders WHERE id = '` + testOrder.String() + `'`,
			`DELETE FROM checkout_sessions WHERE id = '` + testCS.String() + `'`,
			`DELETE FROM reservations WHERE id = '` + testResv.String() + `'`,
			`DELETE FROM sales_channels WHERE id = '` + testChan.String() + `'`,
			`DELETE FROM complimentary_issuances WHERE id = '` + issuance.String() + `'`,
			`DELETE FROM refunds WHERE id = '` + refundID.String() + `'`,
			`DELETE FROM sessions WHERE id = '` + x.otherSess.String() + `'`,
		} {
			if _, err := f.q.DB().Exec(context.Background(), sql); err != nil {
				t.Logf("cleanup %q: %v", sql, err)
			}
		}
	})

	// ticket2 is returned by the selling site: cancelled, 22.50 refunded outside.
	exec(`UPDATE tickets SET status = 'cancelled', refund_price = 2250 WHERE id = $1`, f.ticket2)
	exec(`UPDATE orders SET status = 'partially_refunded' WHERE id = $1`, orderID)
	exec(`INSERT INTO refunds (id, settlement, org_id, order_id, ticket_id, amount, currency, reason, requested_by,
	                           state, approved_at, succeeded_at)
	      VALUES ($1, 'external', $2, $3, $4, 2250, 'EUR', 'WC-REFUND-77', 'site', 'succeeded', now(), now())`,
		refundID, f.orgID, orderID, f.ticket2)

	// An invitation issued by the admin flow: no order, price from the category.
	exec(`INSERT INTO complimentary_issuances (id, org_id, session_id, tier_id, qty, batch_id)
	      VALUES ($1, $2, $3, $4, 1, $5)`, issuance, f.orgID, f.sessID, tierID, "ecv12-"+issuance.String())
	exec(`INSERT INTO tickets (id, session_id, tier_id, holder_email, complimentary_issuance_id, ordinal)
	      VALUES ($1, $2, $3, 'guest@example.test', $4, 1)`, inviteTicket, f.sessID, tierID, issuance)

	// A paid order of a TEST sales channel: in no figure, counted apart.
	exec(`INSERT INTO sales_channels (id, org_id, name, payment_mode, provider, is_test)
	      VALUES ($1, $2, $3, 'direct_merchant', 'stripe', true)`, testChan, f.orgID, "Ecv12 Test "+testChan.String()[:8])
	exec(`INSERT INTO reservations (id, org_id, channel_id, session_id, quantity, state, expires_at, converted_at)
	      VALUES ($1, $2, $3, $4, 1, 'converted', now() + interval '1 hour', now())`, testResv, f.orgID, testChan, f.sessID)
	exec(`INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state)
	      VALUES ($1, $2, $3, $4, 'completed')`, testCS, f.orgID, testChan, testResv)
	exec(`INSERT INTO orders (id, org_id, channel_id, event_id, session_id, checkout_session_id, reservation_id,
	                          source, status, currency, subtotal, discount, charge, total, buyer_name, buyer_email)
	      VALUES ($1, $2, $3, $4, $5, $6, $7, 'public_feed', 'paid', 'EUR', 2500, 0, 0, 2500, 'Test Buyer', 'test@example.test')`,
		testOrder, f.orgID, testChan, f.eventID, f.sessID, testCS, testResv)
	exec(`INSERT INTO tickets (id, checkout_session_id, session_id, tier_id, holder_email, order_id, ordinal)
	      VALUES ($1, $2, $3, $4, 'test@example.test', $5, 1)`, testTicket, testCS, f.sessID, tierID, testOrder)
	exec(`INSERT INTO order_items (id, order_id, ordinal, kind, tier_id, ticket_id, unit_price, discount, charge, total)
	      VALUES ($1, $2, 1, 'ticket', $3, $4, 2500, 0, 0, 2500)`, testItem, testOrder, tierID, testTicket)

	// A session in January 2027, outside the October-December range used below.
	exec(`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, currency, currency_source, status)
	      VALUES ($1, $2, $3, '2027-01-20T17:00:00Z', '2027-01-20T19:00:00Z', 50, 'EUR', 'override', 'scheduled')`,
		x.otherSess, f.eventID, venueID)
	return x
}

type ecv12Summary struct {
	Sessions []struct {
		ID         string `json:"id"`
		TestOrders int64  `json:"test_orders"`
		Cards      []struct {
			Currency        string `json:"currency"`
			Orders          int64  `json:"orders"`
			Sold            int64  `json:"sold"`
			Complimentary   int64  `json:"complimentary"`
			RefundedTickets int64  `json:"refunded_tickets"`
			ActiveTickets   int64  `json:"active_tickets"`
			Revenue         int64  `json:"revenue"`
			Refunded        int64  `json:"refunded"`
			Net             int64  `json:"net"`
			Breakdown       []struct {
				TierName  string  `json:"tier_name"`
				UnitPrice int64   `json:"unit_price"`
				PromoCode *string `json:"promo_code"`
				Tickets   int64   `json:"tickets"`
				Revenue   int64   `json:"revenue"`
			} `json:"breakdown"`
			Promos []struct {
				Code   string `json:"code"`
				Orders int64  `json:"orders"`
			} `json:"promos"`
		} `json:"cards"`
	} `json:"sessions"`
	Totals []struct {
		Currency string `json:"currency"`
		Revenue  int64  `json:"revenue"`
		Net      int64  `json:"net"`
	} `json:"totals"`
	TestOrders int64 `json:"test_orders"`
	Truncated  bool  `json:"truncated"`
}

func TestOrgSummary_CardsBreakdownTestOrdersAndRange(t *testing.T) {
	f := newEC07Fixture(t)
	addEcv12Rows(t, f)
	manager := f.member(t, "organizer")
	orgPath := "/v1/organizations/" + f.orgID.String()

	st, _, body := f.get(t, orgPath+"/summary?from=2026-10-01&to=2026-12-31", manager, nil)
	if st != http.StatusOK {
		t.Fatalf("summary: %d %s", st, body)
	}
	var got ecv12Summary
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, body)
	}
	if len(got.Sessions) != 1 || got.Sessions[0].ID != f.sessID.String() {
		t.Fatalf("the January session is outside the range, the December one is in: %s", body)
	}
	s := got.Sessions[0]
	if s.TestOrders != 1 || got.TestOrders != 1 {
		t.Errorf("test orders: session %d total %d", s.TestOrders, got.TestOrders)
	}
	if len(s.Cards) != 1 {
		t.Fatalf("cards: %s", body)
	}
	c := s.Cards[0]
	// Two tickets sold at 22.50, one of them returned outside; one invitation;
	// the test ticket is nowhere. Orders.total of the paid order is 45.00.
	if c.Currency != "EUR" || c.Orders != 1 || c.Sold != 2 || c.Complimentary != 1 || c.RefundedTickets != 1 || c.ActiveTickets != 2 {
		t.Errorf("counts: %+v", c)
	}
	if c.Revenue != 4500 || c.Refunded != 2250 || c.Net != 2250 {
		t.Errorf("money: %+v", c)
	}
	if len(c.Breakdown) != 1 || c.Breakdown[0].TierName != "Parterre" || c.Breakdown[0].UnitPrice != 2500 ||
		c.Breakdown[0].Tickets != 2 || c.Breakdown[0].Revenue != 4500 || c.Breakdown[0].PromoCode == nil ||
		!strings.HasPrefix(*c.Breakdown[0].PromoCode, "EC07-") {
		t.Errorf("breakdown: %+v", c.Breakdown)
	}
	if len(c.Promos) != 1 || c.Promos[0].Orders != 1 {
		t.Errorf("promos: %+v", c.Promos)
	}
	if len(got.Totals) != 1 || got.Totals[0].Revenue != 4500 || got.Totals[0].Net != 2250 {
		t.Errorf("totals: %+v", got.Totals)
	}

	// No range: both sessions exist, the empty January one has no card.
	st, _, body = f.get(t, orgPath+"/summary", manager, nil)
	var all ecv12Summary
	if st != http.StatusOK || json.Unmarshal(body, &all) != nil || len(all.Sessions) != 1 {
		t.Errorf("unbounded: %d %s", st, body)
	}
	// event_id of another organization's event: no sessions, not an error.
	st, _, body = f.get(t, orgPath+"/summary?event_id="+uuid.NewString(), manager, nil)
	var none ecv12Summary
	if st != http.StatusOK || json.Unmarshal(body, &none) != nil || len(none.Sessions) != 0 {
		t.Errorf("foreign event: %d %s", st, body)
	}
	st, _, body = f.get(t, orgPath+"/summary?from=nope", manager, nil)
	if st != http.StatusBadRequest || ec07ErrorCode(t, body) != "summary.invalid_range" {
		t.Errorf("bad range: %d %s", st, body)
	}
}

func TestAccountingXLSX_ThreeSheetsAuditAndAccess(t *testing.T) {
	f := newEC07Fixture(t)
	addEcv12Rows(t, f)
	manager := f.member(t, "organizer")
	orgPath := "/v1/organizations/" + f.orgID.String()
	url := orgPath + "/exports/accounting.xlsx?from=2026-10-01&to=2026-12-31&locale=ru"

	st, hdr, body := f.get(t, url, manager, nil)
	if st != http.StatusOK {
		t.Fatalf("accounting.xlsx: %d %s", st, body)
	}
	if ct := hdr.Get("Content-Type"); ct != xlsxexport.ContentType {
		t.Errorf("Content-Type: %q", ct)
	}
	if cd := hdr.Get("Content-Disposition"); cd != `attachment; filename="accounting_2026-10-01_2026-12-31.xlsx"` {
		t.Errorf("Content-Disposition: %q", cd)
	}
	sheets := xlsxtest.OpenBook(t, body)
	if len(sheets) != 3 || sheets[0].SheetName != "По сеансам" || sheets[1].SheetName != "По ценам" || sheets[2].SheetName != "Билеты" {
		t.Fatalf("sheets: %d", len(sheets))
	}

	bySession := sheets[0].Values()
	if len(bySession) != 2 {
		t.Fatalf("sheet 1 rows: %v", bySession)
	}
	if h := strings.Join(bySession[0], "|"); h != "Мероприятие|Сеанс|Валюта|Заказов|Продано|Пригласительных|Возвращено билетов|Активных билетов|Выручка|Возвращено|Нетто" {
		t.Errorf("sheet 1 header: %s", h)
	}
	if r := bySession[1]; r[2] != "EUR" || r[3] != "1" || r[4] != "2" || r[5] != "1" || r[6] != "1" || r[7] != "2" || r[8] != "45.00" || r[9] != "22.50" || r[10] != "22.50" {
		t.Errorf("sheet 1 row: %v", r)
	}

	byPrice := sheets[1].Values()
	if len(byPrice) != 2 || byPrice[1][2] != "Parterre" || byPrice[1][3] != "25.00" || !strings.HasPrefix(byPrice[1][5], "EC07-") || byPrice[1][6] != "2" {
		t.Errorf("sheet 2: %v", byPrice)
	}

	tickets := sheets[2].Values()
	// header + ticket1, ticket2, the invitation; the test ticket is absent.
	if len(tickets) != 4 {
		t.Fatalf("sheet 3 rows: %d\n%v", len(tickets), tickets)
	}
	if h := strings.Join(tickets[0], "|"); !strings.HasPrefix(h, "Билет|Заказ|Дата|Мероприятие|Сеанс|Покупатель") || !strings.HasSuffix(h, "Сумма возврата|Способ возврата|Ссылка на возврат|Тест") {
		t.Errorf("sheet 3 header: %s", h)
	}
	var refunded, invited []string
	for _, r := range tickets[1:] {
		switch {
		case r[11] == "cancelled":
			refunded = r
		case r[12] == "yes":
			invited = r
		}
		if r[16] != "no" {
			t.Errorf("Test column: %v", r)
		}
	}
	if refunded == nil || refunded[9] != "22.50" || refunded[13] != "22.50" || refunded[14] != "external" || refunded[15] != "WC-REFUND-77" {
		t.Errorf("refunded ticket row: %v", refunded)
	}
	if invited == nil || invited[5] != "" || invited[9] != "" {
		t.Errorf("invitation row (no buyer, no price paid): %v", invited)
	}
	for _, r := range tickets[1:] {
		for _, cell := range r {
			if strings.Contains(cell, "test@example.test") {
				t.Errorf("a test order leaked into the tickets sheet: %v", r)
			}
		}
	}

	// The download is audited.
	var n int
	err := f.q.DB().QueryRow(context.Background(),
		`SELECT count(*) FROM audit_events WHERE action = 'v1.org.accounting_export.download' AND resource_id = $1
		   AND metadata->>'from' = '2026-10-01' AND (metadata->>'tickets')::int = 3`, f.orgID.String()).Scan(&n)
	if err != nil || n != 1 {
		t.Errorf("audit rows = %d (%v)", n, err)
	}

	// English headers by default; a bad range is a 400; an API key is refused.
	st, _, body = f.get(t, orgPath+"/exports/accounting.xlsx?to=2026-12-31", manager, nil)
	if st != http.StatusOK || xlsxtest.OpenBook(t, body)[0].SheetName != "By sessions" {
		t.Errorf("default locale: %d", st)
	}
	st, _, body = f.get(t, orgPath+"/exports/accounting.xlsx?from=2026-12-31&to=2026-01-01", manager, nil)
	if st != http.StatusBadRequest || ec07ErrorCode(t, body) != "summary.invalid_range" {
		t.Errorf("bad range: %d %s", st, body)
	}
	creator, err := f.q.InsertUser(context.Background(), "ecv12-key-"+uuid.NewString()+"@example.test", "x", "en")
	if err != nil {
		t.Fatalf("InsertUser: %v", err)
	}
	f.userID = creator.ID
	key := f.key(t, f.orgID, "order.read", "session.read")
	st, _, body = f.get(t, url, key, nil)
	if st != http.StatusForbidden || ec07ErrorCode(t, body) != "accounting.api_key_forbidden" {
		t.Errorf("api key: %d %s", st, body)
	}
	agent := f.member(t, "agent")
	if st, _, _ := f.get(t, url, agent, nil); st != http.StatusForbidden {
		t.Errorf("agent: %d", st)
	}
	// Another organization's manager sees only its own (empty) workbook, and a
	// non-member is refused.
	other := newEC07Fixture(t)
	if st, _, _ := other.get(t, orgPath+"/exports/accounting.xlsx", other.member(t, "organizer"), nil); st != http.StatusForbidden && st != http.StatusNotFound {
		t.Errorf("foreign organization: %d", st)
	}
}

func TestOrgSummary_AgentAndStrangerAreRefused(t *testing.T) {
	f := newEC07Fixture(t)
	orgPath := "/v1/organizations/" + f.orgID.String()
	if st, _, _ := f.get(t, orgPath+"/summary", f.member(t, "agent"), nil); st != http.StatusForbidden {
		t.Errorf("agent: %d", st)
	}
	other := newEC07Fixture(t)
	if st, _, _ := f.get(t, orgPath+"/summary", other.member(t, "organizer"), nil); st != http.StatusForbidden && st != http.StatusNotFound {
		t.Errorf("stranger: %d", st)
	}
}
