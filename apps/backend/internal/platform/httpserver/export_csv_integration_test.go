//go:build integration

// export_csv_integration_test.go — EC-07 (spec 35 §5.8): the organizer's CSV
// exports through the REAL router. One session with two tickets (one with a
// stored EAN-13 credential, one legacy without), a buyer whose name looks
// like a formula and whose phone starts with `+`, a promo code redeemed on
// the order. The file must come back the way a spreadsheet keeps it exact:
// BOM, `;`, CRLF, barcodes and phones as `="…"`, the name defused with `'`.
//
// Access: a manager (membership role organizer, empty roles claim — what the
// bot mints) gets the file, an agent is refused, a foreign organization sees
// a 404, and a file over the cap is refused with 413 before any byte.
//
// Run against a fresh migrated database (AGENTS.md CI-Integration recipe).
package httpserver

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/barcodes/ean13"
)

type ec07Fixture struct {
	*prom0113Fixture
	srv      *Server
	secret   string
	orgID    uuid.UUID
	eventID  uuid.UUID
	sessID   uuid.UUID
	promoID  uuid.UUID
	ticket1  uuid.UUID // stored EAN-13 credential, admitted at the gate
	ticket2  uuid.UUID // legacy: no credential, derived PlatformCode
	ticket2N int64     // its system_ticket_id
}

func newEC07Fixture(t *testing.T) *ec07Fixture {
	t.Helper()
	srv, secret := productionIntegrationServer(t)
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	// One server for the requests AND for the row-cap knob (exportMaxRows),
	// so the 413 test tunes the very server it talks to.
	base := &prom0113Fixture{ts: ts, client: ts.Client(), q: gen.New(srv.pgxPool)}
	f := &ec07Fixture{prom0113Fixture: base, srv: srv, secret: secret}
	ctx := context.Background()
	f.orgID = base.org(t, "Ec07")

	var (
		venueID, chanID, tierID = uuid.New(), uuid.New(), uuid.New()
		resvID, csID, orderID   = uuid.New(), uuid.New(), uuid.New()
		item1, item2, redID     = uuid.New(), uuid.New(), uuid.New()
		nonce                   = uuid.NewString()[:8]
	)
	f.eventID, f.sessID, f.promoID = uuid.New(), uuid.New(), uuid.New()
	f.ticket1, f.ticket2 = uuid.New(), uuid.New()

	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.q.DB().Exec(ctx, sql, args...); err != nil {
			t.Fatalf("fixture %q: %v", sql, err)
		}
	}
	t.Cleanup(func() {
		for _, step := range []struct {
			sql string
			arg uuid.UUID
		}{
			{`DELETE FROM promo_code_redemptions WHERE id = $1`, redID},
			{`DELETE FROM ticket_credentials WHERE ticket_id = $1`, f.ticket1},
			{`DELETE FROM order_items WHERE order_id = $1`, orderID},
			{`DELETE FROM tickets WHERE order_id = $1`, orderID},
			{`DELETE FROM orders WHERE id = $1`, orderID},
			{`DELETE FROM session_seats WHERE session_id = $1`, f.sessID},
			{`DELETE FROM checkout_sessions WHERE id = $1`, csID},
			{`DELETE FROM reservations WHERE id = $1`, resvID},
			{`DELETE FROM promo_codes WHERE id = $1`, f.promoID},
			{`DELETE FROM ticket_tiers WHERE id = $1`, tierID},
			{`DELETE FROM sales_channels WHERE id = $1`, chanID},
			{`DELETE FROM sessions WHERE id = $1`, f.sessID},
			{`DELETE FROM events WHERE id = $1`, f.eventID},
			{`DELETE FROM venues WHERE id = $1`, venueID},
			{`DELETE FROM memberships WHERE org_id = $1`, f.orgID},
			{`DELETE FROM organizations WHERE id = $1`, f.orgID},
		} {
			if _, err := f.q.DB().Exec(context.Background(), step.sql, step.arg); err != nil {
				t.Logf("cleanup %q: %v", step.sql, err)
			}
		}
	})

	exec(`INSERT INTO venues (id, org_id, name, timezone) VALUES ($1, $2, $3, 'Europe/Prague')`, venueID, f.orgID, "Ec07 Hall "+nonce)
	exec(`INSERT INTO events (id, org_id, name, slug, status) VALUES ($1, $2, $3, $4, 'published')`,
		f.eventID, f.orgID, "Ec07 Event "+nonce, "ec07-event-"+nonce)
	// 2026-12-24 17:30 UTC is 18:30 in Prague.
	exec(`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, currency, currency_source, status)
	      VALUES ($1, $2, $3, '2026-12-24T17:30:00Z', '2026-12-24T19:30:00Z', 100, 'EUR', 'override', 'scheduled')`,
		f.sessID, f.eventID, venueID)
	exec(`INSERT INTO sales_channels (id, org_id, name, payment_mode, provider)
	      VALUES ($1, $2, $3, 'direct_merchant', 'stripe')`, chanID, f.orgID, "Ec07 Site "+nonce)
	exec(`INSERT INTO ticket_tiers (id, session_id, name, pricing_mode, price_amount, currency, capacity)
	      VALUES ($1, $2, 'Parterre', 'fixed', 2500, 'EUR', 100)`, tierID, f.sessID)
	exec(`INSERT INTO promo_codes (id, org_id, code, discount_type, discount_value)
	      VALUES ($1, $2, $3, 'fixed_amount', 500)`, f.promoID, f.orgID, "EC07-"+strings.ToUpper(nonce))
	exec(`INSERT INTO reservations (id, org_id, channel_id, session_id, quantity, state, expires_at, converted_at)
	      VALUES ($1, $2, $3, $4, 2, 'converted', now() + interval '1 hour', now())`, resvID, f.orgID, chanID, f.sessID)
	exec(`INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state)
	      VALUES ($1, $2, $3, $4, 'completed')`, csID, f.orgID, chanID, resvID)
	exec(`INSERT INTO orders (id, org_id, channel_id, event_id, session_id, checkout_session_id, reservation_id,
	                          source, status, currency, subtotal, discount, charge, total, promo_code_id,
	                          buyer_name, buyer_email, buyer_phone, created_at)
	      VALUES ($1, $2, $3, $4, $5, $6, $7, 'public_feed', 'paid', 'EUR', 5000, 500, 0, 4500, $8,
	              '=HYPERLINK("http://evil.test")', $9, '+34600111222', '2026-10-01T10:15:00Z')`,
		orderID, f.orgID, chanID, f.eventID, f.sessID, csID, resvID, f.promoID, "ec07-"+nonce+"@example.test")
	exec(`INSERT INTO tickets (id, checkout_session_id, session_id, tier_id, holder_email, order_id, ordinal, used_at)
	      VALUES ($1, $2, $3, $4, 'x@example.test', $5, 1, now())`, f.ticket1, csID, f.sessID, tierID, orderID)
	exec(`INSERT INTO tickets (id, checkout_session_id, session_id, tier_id, holder_email, order_id, ordinal)
	      VALUES ($1, $2, $3, $4, 'x@example.test', $5, 2)`, f.ticket2, csID, f.sessID, tierID, orderID)
	exec(`INSERT INTO ticket_credentials (ticket_id, type, payload) VALUES ($1, 'ean13', '4600051000001')`, f.ticket1)
	exec(`INSERT INTO order_items (id, order_id, ordinal, kind, tier_id, ticket_id, unit_price, discount, charge, total)
	      VALUES ($1, $2, 1, 'ticket', $3, $4, 2500, 250, 0, 2250)`, item1, orderID, tierID, f.ticket1)
	exec(`INSERT INTO order_items (id, order_id, ordinal, kind, tier_id, ticket_id, unit_price, discount, charge, total)
	      VALUES ($1, $2, 2, 'ticket', $3, $4, 2500, 250, 0, 2250)`, item2, orderID, tierID, f.ticket2)
	exec(`INSERT INTO promo_code_redemptions (id, promo_code_id, order_id, discount_amount, order_amount, redeemed_at)
	      VALUES ($1, $2, $3, 500, 4500, '2026-10-01T10:16:00Z')`, redID, f.promoID, orderID)
	if err := f.q.DB().QueryRow(ctx, `SELECT system_ticket_id FROM tickets WHERE id = $1`, f.ticket2).Scan(&f.ticket2N); err != nil {
		t.Fatalf("system_ticket_id: %v", err)
	}
	return f
}

// member mints a JWT whose only authority is a membership row of role.
func (f *ec07Fixture) member(t *testing.T, role string) string {
	t.Helper()
	ctx := context.Background()
	user, err := f.q.InsertUser(ctx, "ec07-"+role+"-"+uuid.NewString()+"@example.test", "x", "en")
	if err != nil {
		t.Fatalf("InsertUser: %v", err)
	}
	if _, err := f.q.InsertMembership(ctx, user.ID, f.orgID, role); err != nil {
		t.Fatalf("InsertMembership(%s): %v", role, err)
	}
	tok, _, err := auth.IssueJWT(f.secret, user.ID, nil, nil, "arena-api", "arena-api", time.Hour)
	if err != nil {
		t.Fatalf("IssueJWT: %v", err)
	}
	return tok
}

// get fetches path with the bearer and returns the status, headers and raw body.
func (f *ec07Fixture) get(t *testing.T, path, bearer string, header map[string]string) (int, http.Header, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, f.ts.URL+path, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := f.client.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp.StatusCode, resp.Header, body
}

func ec07ErrorCode(t *testing.T, body []byte) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	return prom0113Code(m)
}

// ec07Lines splits a CSV file the way the format promises: BOM first, CRLF
// records, no bare LF.
func ec07Lines(t *testing.T, body []byte) []string {
	t.Helper()
	s := string(body)
	if !strings.HasPrefix(s, "\xEF\xBB\xBF") {
		t.Fatalf("no UTF-8 BOM: %q", s[:min(len(s), 10)])
	}
	s = strings.TrimPrefix(s, "\xEF\xBB\xBF")
	if !strings.HasSuffix(s, "\r\n") {
		t.Fatalf("file does not end with CRLF: %q", s)
	}
	if strings.Contains(strings.ReplaceAll(s, "\r\n", ""), "\n") {
		t.Fatalf("bare LF inside the file: %q", s)
	}
	return strings.Split(strings.TrimSuffix(s, "\r\n"), "\r\n")
}

func TestExportCSV_SessionSalesFileKeepsBarcodesPhonesAndNamesExact(t *testing.T) {
	f := newEC07Fixture(t)
	manager := f.member(t, "organizer")
	orgPath := "/v1/organizations/" + f.orgID.String()

	st, hdr, body := f.get(t, orgPath+"/sessions/"+f.sessID.String()+"/sales.csv?locale=ru", manager, nil)
	if st != http.StatusOK {
		t.Fatalf("manager sales.csv: %d %s", st, body)
	}
	if ct := hdr.Get("Content-Type"); ct != "text/csv; charset=utf-8" {
		t.Errorf("Content-Type: %q", ct)
	}
	if cd := hdr.Get("Content-Disposition"); !strings.HasPrefix(cd, `attachment; filename="sales_ec07-event-`) || !strings.HasSuffix(cd, `.csv"`) {
		t.Errorf("Content-Disposition: %q", cd)
	}
	lines := ec07Lines(t, body)
	if len(lines) != 3 {
		t.Fatalf("lines: %d\n%s", len(lines), body)
	}
	if lines[0] != `"Заказ";"Статус заказа";"Дата";"Покупатель";"E-mail";"Телефон";"Категория";"Цена";"Валюта";"Штрихкод";"Статус билета";"Вошёл";"Канал";"Промокод"` {
		t.Errorf("header: %s", lines[0])
	}
	// Ticket 1: stored EAN-13, admitted. Ticket 2: derived legacy code, not admitted.
	legacy := ean13.PlatformCode(f.ticket2N)
	for i, want := range []string{
		`="4600051000001";"active";"yes"`,
		`="` + legacy + `";"active";"no"`,
	} {
		if !strings.Contains(lines[i+1], want) {
			t.Errorf("row %d lacks %s: %s", i+1, want, lines[i+1])
		}
	}
	row := lines[1]
	for _, want := range []string{
		`"paid";"2026-10-01 12:15"`,           // order date in Prague (UTC+2 on 2026-10-01)
		`"'=HYPERLINK(""http://evil.test"")"`, // the name is defused, quotes doubled
		`="+34600111222"`,                     // the phone keeps its plus
		`"Parterre";22.50;"EUR"`,              // the price paid for THAT ticket, not the list price
		`;"Ec07 Site `,                        // the channel
		`;"EC07-`,                             // the promo code
	} {
		if !strings.Contains(row, want) {
			t.Errorf("row lacks %s: %s", want, row)
		}
	}
	if strings.Contains(row, "25.00") {
		t.Errorf("row carries the list price instead of the paid one: %s", row)
	}
	// The order number is a 10-digit system id: written as Excel text.
	if !strings.HasPrefix(row, `="`) {
		t.Errorf("order number is not Excel text: %s", row)
	}

	// English header by default, Spanish through Accept-Language.
	st, _, body = f.get(t, orgPath+"/sessions/"+f.sessID.String()+"/sales.csv", manager, nil)
	if st != http.StatusOK || !strings.HasPrefix(ec07Lines(t, body)[0], `"Order";"Order status";"Date"`) {
		t.Errorf("default header: %d %s", st, ec07Lines(t, body)[0])
	}
	st, _, body = f.get(t, orgPath+"/sessions/"+f.sessID.String()+"/sales.csv", manager, map[string]string{"Accept-Language": "es-ES,es;q=0.9"})
	if st != http.StatusOK || !strings.HasPrefix(ec07Lines(t, body)[0], `"Pedido";"Estado del pedido";"Fecha"`) {
		t.Errorf("es header: %d %s", st, ec07Lines(t, body)[0])
	}
}

func TestExportCSV_EventSummaryAndPromoFiles(t *testing.T) {
	f := newEC07Fixture(t)
	manager := f.member(t, "organizer")
	orgPath := "/v1/organizations/" + f.orgID.String()

	st, hdr, body := f.get(t, orgPath+"/events/"+f.eventID.String()+"/sales.csv?locale=ru", manager, nil)
	if st != http.StatusOK {
		t.Fatalf("event sales.csv: %d %s", st, body)
	}
	lines := ec07Lines(t, body)
	if len(lines) != 3 || !strings.HasSuffix(lines[0], `;"Промокод";"Сеанс"`) {
		t.Errorf("event header/rows: %q", lines)
	}
	if !strings.HasSuffix(lines[1], `;"2026-12-24 18:30"`) {
		t.Errorf("session start in the venue's zone: %s", lines[1])
	}
	if cd := hdr.Get("Content-Disposition"); !strings.Contains(cd, `filename="sales_ec07-event-`) {
		t.Errorf("Content-Disposition: %q", cd)
	}

	st, hdr, body = f.get(t, orgPath+"/sessions/"+f.sessID.String()+"/summary.csv?locale=ru", manager, nil)
	if st != http.StatusOK {
		t.Fatalf("summary.csv: %d %s", st, body)
	}
	lines = ec07Lines(t, body)
	if len(lines) != 2 {
		t.Fatalf("summary lines: %q", lines)
	}
	if lines[0] != `"Категория";"Тип";"Цена";"Валюта";"Мест";"Свободно";"Удержано";"Продано";"Продано вне системы";"Недоступно";"Оплачено билетов";"Выручка";"В продаже"` {
		t.Errorf("summary header: %s", lines[0])
	}
	if lines[1] != `"Parterre";"ga";25.00;"EUR";0;0;0;0;0;0;2;45.00;"yes"` {
		t.Errorf("summary row: %s", lines[1])
	}
	if cd := hdr.Get("Content-Disposition"); !strings.Contains(cd, `filename="summary_ec07-event-`) {
		t.Errorf("Content-Disposition: %q", cd)
	}

	st, hdr, body = f.get(t, orgPath+"/promo-codes/"+f.promoID.String()+"/redemptions.csv?locale=ru", manager, nil)
	if st != http.StatusOK {
		t.Fatalf("redemptions.csv: %d %s", st, body)
	}
	lines = ec07Lines(t, body)
	if len(lines) != 2 || lines[0] != `"Заказ";"Статус заказа";"Дата";"Покупатель";"E-mail";"Скидка";"Валюта"` {
		t.Errorf("redemptions: %q", lines)
	}
	if !strings.Contains(lines[1], `"paid";"2026-10-01 12:16";"'=HYPERLINK(""http://evil.test"")";`) || !strings.HasSuffix(lines[1], `;5.00;"EUR"`) {
		t.Errorf("redemption row: %s", lines[1])
	}
	if cd := hdr.Get("Content-Disposition"); !strings.Contains(cd, `filename="promo_ec07-`) {
		t.Errorf("Content-Disposition: %q", cd)
	}
}

func TestExportCSV_AccessAndRowCap(t *testing.T) {
	f := newEC07Fixture(t)
	manager, agent := f.member(t, "organizer"), f.member(t, "agent")
	orgPath := "/v1/organizations/" + f.orgID.String()
	sales := orgPath + "/sessions/" + f.sessID.String() + "/sales.csv"

	// An agent holds neither order.read nor promo.read: 403 at the gate.
	for _, path := range []string{
		sales,
		orgPath + "/events/" + f.eventID.String() + "/sales.csv",
		orgPath + "/sessions/" + f.sessID.String() + "/summary.csv",
		orgPath + "/promo-codes/" + f.promoID.String() + "/redemptions.csv",
	} {
		if st, _, body := f.get(t, path, agent, nil); st != http.StatusForbidden {
			t.Errorf("agent %s: %d %s, want 403", path, st, body)
		}
	}

	// A member of ANOTHER organization: its own membership passes the
	// permission gate, the org check refuses it before any row is read.
	other := f.prom0113Fixture.org(t, "Ec07Other")
	t.Cleanup(func() {
		_, _ = f.q.DB().Exec(context.Background(), `DELETE FROM memberships WHERE org_id = $1`, other)
		_, _ = f.q.DB().Exec(context.Background(), `DELETE FROM organizations WHERE id = $1`, other)
	})
	otherUser, err := f.q.InsertUser(context.Background(), "ec07-other-"+uuid.NewString()+"@example.test", "x", "en")
	if err != nil {
		t.Fatalf("InsertUser: %v", err)
	}
	if _, err := f.q.InsertMembership(context.Background(), otherUser.ID, other, "organizer"); err != nil {
		t.Fatalf("InsertMembership: %v", err)
	}
	otherTok, _, err := auth.IssueJWT(f.secret, otherUser.ID, nil, nil, "arena-api", "arena-api", time.Hour)
	if err != nil {
		t.Fatalf("IssueJWT: %v", err)
	}
	if st, _, _ := f.get(t, sales, otherTok, nil); st != http.StatusForbidden {
		t.Errorf("foreign member on this org's path: %d, want 403", st)
	}
	// The same rows addressed through the OTHER organization's path: 404,
	// never the file and never a hint that the session exists.
	otherPath := "/v1/organizations/" + other.String()
	for _, p := range []struct{ path, code string }{
		{otherPath + "/sessions/" + f.sessID.String() + "/sales.csv", "session.not_found"},
		{otherPath + "/events/" + f.eventID.String() + "/sales.csv", "event.not_found"},
		{otherPath + "/sessions/" + f.sessID.String() + "/summary.csv", "session.not_found"},
		{otherPath + "/promo-codes/" + f.promoID.String() + "/redemptions.csv", "promo.not_found"},
	} {
		st, _, body := f.get(t, p.path, otherTok, nil)
		if st != http.StatusNotFound || ec07ErrorCode(t, body) != p.code {
			t.Errorf("%s: %d %s, want 404 %s", p.path, st, body, p.code)
		}
	}

	// A bogus id is the route's 400, an unknown one its 404.
	if st, _, _ := f.get(t, orgPath+"/sessions/not-a-uuid/sales.csv", manager, nil); st != http.StatusBadRequest {
		t.Errorf("bad uuid: %d, want 400", st)
	}
	if st, _, _ := f.get(t, orgPath+"/sessions/"+uuid.NewString()+"/sales.csv", manager, nil); st != http.StatusNotFound {
		t.Errorf("unknown session: %d, want 404", st)
	}

	// The cap: two tickets against a cap of one is 413 before any byte.
	f.srv.exportMaxRows = 1
	t.Cleanup(func() { f.srv.exportMaxRows = 0 })
	st, hdr, body := f.get(t, sales, manager, nil)
	if st != http.StatusRequestEntityTooLarge || ec07ErrorCode(t, body) != "export.too_many_rows" {
		t.Fatalf("over the cap: %d %s", st, body)
	}
	if !strings.HasPrefix(hdr.Get("Content-Type"), "application/json") {
		t.Errorf("413 must be the JSON envelope, got %q", hdr.Get("Content-Type"))
	}
	// The summary has no cap (bounded by the categories) and still answers.
	if st, _, _ := f.get(t, orgPath+"/sessions/"+f.sessID.String()+"/summary.csv", manager, nil); st != http.StatusOK {
		t.Errorf("summary under a cap of one: %d", st)
	}
	f.srv.exportMaxRows = 2
	if st, _, _ := f.get(t, sales, manager, nil); st != http.StatusOK {
		t.Errorf("exactly at the cap: %d, want 200", st)
	}
}
