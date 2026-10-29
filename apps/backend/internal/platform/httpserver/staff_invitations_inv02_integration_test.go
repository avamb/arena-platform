//go:build integration

// Staff invitations with seats on the plan, task INV-02 (spec 46, sections
// 3.1 and 3.2): the issue transaction through the real router, the
// per-session list, the cancellation back to each seat's origin, the
// request_id replay, roles, organization isolation and a race against the
// public seated hold.
package httpserver

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/bil24wire"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
)

type inv02Fx struct {
	t                     *testing.T
	srv                   *Server
	ts                    *httptest.Server
	pool                  *pgxpool.Pool
	secret                string
	o                     *inv01Org
	venue, event, session uuid.UUID
	plan, planVer         uuid.UUID
	seatTier, gaTier      uuid.UUID
	ownerID               uuid.UUID
	manager, agent        string
	// phone is this fixture's guest phone: customer_identities is GLOBALLY
	// unique on the normalized value, so a fixed literal would resolve to a
	// leftover customer of an earlier run (AGENTS.md).
	phone string
}

// inv02PhonePlaceholder is the phone inv02Body writes; f.do swaps it for
// the fixture's random one.
const inv02PhonePlaceholder = "+34600111222"

// newINV02Fx builds an organization with a hybrid session: eight seats
// A|1|1..A|1|8 (A|1|1..A|1|3 held back as the organizer's reserve) and a GA
// category with four places (the last two held back). start is the session
// start relative to now.
func newINV02Fx(t *testing.T, start time.Duration) *inv02Fx {
	t.Helper()
	srv, secret := productionIntegrationServer(t)
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	pool := srv.pgxPool
	ctx := context.Background()
	f := &inv02Fx{t: t, srv: srv, ts: ts, pool: pool, secret: secret}
	rnd := uuid.New()
	f.phone = fmt.Sprintf("+3469%07d", binary.BigEndian.Uint32(rnd[:4])%10_000_000)
	f.o = newINV01Org(t, ctx, pool, secret)
	if err := pool.QueryRow(ctx, `SELECT user_id FROM memberships WHERE org_id = $1 AND role = 'org_admin'`, f.o.org).Scan(&f.ownerID); err != nil {
		t.Fatalf("owner id: %v", err)
	}
	f.manager = f.member("organizer")
	f.agent = f.member("agent")
	f.venue, f.event, f.session = uuid.New(), uuid.New(), uuid.New()
	f.plan, f.planVer = uuid.New(), uuid.New()
	f.seatTier, f.gaTier = uuid.New(), uuid.New()
	t.Cleanup(f.cleanup)

	startAt := time.Now().UTC().Add(start)
	steps := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO venues (id, org_id, name, timezone) VALUES ($1, $2, 'INV02 Venue', 'Europe/Prague')`, []any{f.venue, f.o.org}},
		{`INSERT INTO events (id, org_id, name, status, visibility) VALUES ($1, $2, 'INV02 Event', 'draft', 'private')`, []any{f.event, f.o.org}},
		{`INSERT INTO seating_plans (id, venue_id, owner_org_id, name, plan_type, status)
		  VALUES ($1, $2, $3, 'INV02 Plan', 'assigned_seats', 'active')`, []any{f.plan, f.venue, f.o.org}},
		{`INSERT INTO seating_plan_versions (id, seating_plan_id, version_number, geometry, geometry_checksum, capacity_seated)
		  VALUES ($1, $2, 1, '{}'::jsonb, 'inv02', 8)`, []any{f.planVer, f.plan}},
		{`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, status,
		    admission_mode, currency, currency_source, seating_plan_version_id)
		  VALUES ($1, $2, $3, $4::timestamptz, $4::timestamptz + interval '2 hours', 100, 'scheduled', 'hybrid', 'EUR', 'override', $5)`,
			[]any{f.session, f.event, f.venue, startAt, f.planVer}},
		{`INSERT INTO ticket_tiers (id, session_id, name, pricing_mode, price_amount, currency, sort_order)
		  VALUES ($1, $2, 'Parterre', 'fixed', 2500, 'EUR', 0)`, []any{f.seatTier, f.session}},
		{`INSERT INTO ticket_tiers (id, session_id, name, pricing_mode, price_amount, currency, sort_order, capacity, unit_seq, is_open)
		  VALUES ($1, $2, 'Standing', 'fixed', 1500, 'EUR', 1, 4, 1, true)`, []any{f.gaTier, f.session}},
		{`INSERT INTO inventory_ledger (session_id, tier_id, capacity_total) VALUES ($1, NULL, 100)`, []any{f.session}},
		{`INSERT INTO session_seats (session_id, seat_key, sector_name, row_name, seat_number, tier_id, status, kind)
		  SELECT $1, 'A|1|' || n, 'A', '1', n::text, $2,
		         CASE WHEN n <= 3 THEN 'unavailable' ELSE 'available' END, 'seat'
		  FROM generate_series(1, 8) n`, []any{f.session, f.seatTier}},
		{`INSERT INTO session_seats (session_id, seat_key, sector_name, row_name, seat_number, tier_id, status, kind)
		  SELECT $1, 'ga|t1|' || lpad(n::text, 6, '0'), '', '', '', $2,
		         CASE WHEN n >= 3 THEN 'unavailable' ELSE 'available' END, 'ga_unit'
		  FROM generate_series(1, 4) n`, []any{f.session, f.gaTier}},
	}
	for i, s := range steps {
		if _, err := pool.Exec(ctx, s.sql, s.args...); err != nil {
			t.Fatalf("INV02 fixture step %d: %v", i, err)
		}
	}
	return f
}

func (f *inv02Fx) member(role string) string {
	f.t.Helper()
	ctx := context.Background()
	q := gen.New(f.pool)
	u, err := q.InsertUser(ctx, "inv02-"+role+"-"+uuid.NewString()+"@example.test", "x", "en")
	if err != nil {
		f.t.Fatalf("InsertUser: %v", err)
	}
	if _, err := q.InsertMembership(ctx, u.ID, f.o.org, role); err != nil {
		f.t.Fatalf("InsertMembership %s: %v", role, err)
	}
	tok, _, err := auth.IssueJWT(f.secret, u.ID, nil, nil, "arena-api", "arena-api", time.Hour)
	if err != nil {
		f.t.Fatalf("IssueJWT: %v", err)
	}
	return tok
}

// inv02SessionCleanupSQL removes everything the invitations of one session
// wrote (worker_jobs first: they are keyed by JSON payload, no FK).
var inv02SessionCleanupSQL = []string{
	`DELETE FROM outbox_events WHERE aggregate_id IN (
	   SELECT id::text FROM orders WHERE session_id = $1
	   UNION ALL SELECT id::text FROM tickets WHERE session_id = $1
	   UNION ALL SELECT checkout_session_id::text FROM orders WHERE session_id = $1)`,
	`DELETE FROM outbox WHERE aggregate_id IN (
	   SELECT id FROM orders WHERE session_id = $1
	   UNION ALL SELECT id FROM tickets WHERE session_id = $1)`,
	`DELETE FROM order_events WHERE order_id IN (SELECT id FROM orders WHERE session_id = $1)`,
	`DELETE FROM worker_jobs WHERE payload->>'checkout_session_id' IN (
	   SELECT cs.id::text FROM checkout_sessions cs JOIN reservations r ON r.id = cs.reservation_id WHERE r.session_id = $1)
	   OR payload->>'reservation_id' IN (SELECT id::text FROM reservations WHERE session_id = $1)
	   OR payload->>'ticket_id' IN (SELECT id::text FROM tickets WHERE session_id = $1)`,
	`DELETE FROM order_items WHERE order_id IN (SELECT id FROM orders WHERE session_id = $1)`,
	`DELETE FROM barcodes WHERE ticket_id IN (SELECT id FROM tickets WHERE session_id = $1)`,
	`DELETE FROM tickets WHERE session_id = $1`,
	`DELETE FROM orders WHERE session_id = $1`,
	`DELETE FROM invitation_batches WHERE session_id = $1`,
	`DELETE FROM checkout_sessions WHERE reservation_id IN (SELECT id FROM reservations WHERE session_id = $1)`,
	`DELETE FROM reservation_seats WHERE reservation_id IN (SELECT id FROM reservations WHERE session_id = $1)`,
	`DELETE FROM reservation_ga_items WHERE reservation_id IN (SELECT id FROM reservations WHERE session_id = $1)`,
	`DELETE FROM session_seats WHERE session_id = $1`,
	`DELETE FROM reservations WHERE session_id = $1`,
	`DELETE FROM inventory_ledger WHERE session_id = $1`,
	`DELETE FROM ticket_tiers WHERE session_id = $1`,
	`DELETE FROM sessions WHERE id = $1`,
}

// cleanup removes everything the session's invitations wrote, worker_jobs
// first (AGENTS.md: keyed by JSON payload, no FK to clean them up).
func (f *inv02Fx) cleanup() {
	ctx := context.Background()
	// The guests' customer rows: captured before the orders go, deleted
	// after them (orders.customer_id has no cascade).
	var customerIDs []uuid.UUID
	if rows, err := f.pool.Query(ctx, `SELECT customer_id FROM customer_org_links WHERE org_id = $1`, f.o.org); err == nil {
		for rows.Next() {
			var id uuid.UUID
			if rows.Scan(&id) == nil {
				customerIDs = append(customerIDs, id)
			}
		}
		rows.Close()
	}
	for _, sql := range inv02SessionCleanupSQL {
		if _, err := f.pool.Exec(ctx, sql, f.session); err != nil {
			f.t.Logf("INV02 cleanup: %v", err)
		}
	}
	for _, s := range []struct {
		sql string
		arg uuid.UUID
	}{
		{`DELETE FROM customer_org_links WHERE org_id = $1`, f.o.org},
		{`DELETE FROM outbox_events WHERE aggregate_id = $1::text`, f.o.org},
		{`DELETE FROM seating_plan_versions WHERE id = $1`, f.planVer},
		{`DELETE FROM seating_plans WHERE id = $1`, f.plan},
		{`DELETE FROM events WHERE id = $1`, f.event},
		{`DELETE FROM venues WHERE id = $1`, f.venue},
	} {
		if _, err := f.pool.Exec(ctx, s.sql, s.arg); err != nil {
			f.t.Logf("INV02 cleanup: %v", err)
		}
	}
	// Only customers no other organization knows (identities cascade).
	if len(customerIDs) > 0 {
		if _, err := f.pool.Exec(ctx, `
			DELETE FROM customers c
			WHERE  c.id = ANY($1::uuid[])
			  AND  NOT EXISTS (SELECT 1 FROM customer_org_links l WHERE l.customer_id = c.id)
			  AND  NOT EXISTS (SELECT 1 FROM orders o WHERE o.customer_id = c.id)`, customerIDs); err != nil {
			f.t.Logf("INV02 cleanup customers: %v", err)
		}
	}
}

func (f *inv02Fx) do(token, method, path, body string) (int, map[string]any) {
	f.t.Helper()
	body = strings.ReplaceAll(body, inv02PhonePlaceholder, f.phone)
	req, err := http.NewRequestWithContext(context.Background(), method, f.ts.URL+path, strings.NewReader(body))
	if err != nil {
		f.t.Fatalf("NewRequest: %v", err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Admin-Reason", "inv-02 integration test")
	resp, err := f.ts.Client().Do(req)
	if err != nil {
		f.t.Fatalf("%s %s: %v", method, path, err)
	}
	raw := integReadBody(f.t, resp)
	out := map[string]any{}
	_ = json.Unmarshal([]byte(raw), &out)
	out["_raw"] = raw
	return resp.StatusCode, out
}

func (f *inv02Fx) issuePath() string {
	return "/v1/organizations/" + f.o.org.String() + "/sessions/" + f.session.String() + "/invitations"
}

func (f *inv02Fx) issue(token, body string) (int, map[string]any) {
	return f.do(token, http.MethodPost, f.issuePath(), body)
}

func (f *inv02Fx) seat(key string) gen.SessionSeatRow {
	f.t.Helper()
	s, err := gen.New(f.pool).GetSessionSeatByKey(context.Background(), f.session, key)
	if err != nil {
		f.t.Fatalf("seat %s: %v", key, err)
	}
	return s
}

func inv02Body(requestID uuid.UUID, seats []string, gaTier *uuid.UUID, gaQty int, delivery, email string) string {
	b := map[string]any{
		"request_id": requestID.String(),
		"guest":      map[string]any{"name": "Ana López", "phone": inv02PhonePlaceholder, "locale": "es-ES"},
		"delivery":   delivery,
		"note":       "front row for the press",
	}
	if email != "" {
		b["guest"].(map[string]any)["email"] = email
	}
	if len(seats) > 0 {
		var s []map[string]string
		for _, k := range seats {
			s = append(s, map[string]string{"seat_key": k})
		}
		b["seats"] = s
	}
	if gaTier != nil {
		b["ga"] = []map[string]any{{"tier_id": gaTier.String(), "qty": gaQty}}
	}
	raw, _ := json.Marshal(b)
	return string(raw)
}

func TestINV02_IssueFromReserveAndSale_CancelReturnsEachSeatToItsOrigin(t *testing.T) {
	f := newINV02Fx(t, 30*24*time.Hour)
	ctx := context.Background()
	email := "guest-" + uuid.NewString()[:8] + "@example.test"

	code, body := f.issue(f.o.owner, inv02Body(uuid.New(), []string{"A|1|1", "A|1|5"}, &f.gaTier, 3, "guest_email", email))
	if code != http.StatusCreated {
		t.Fatalf("issue: %d %s", code, body["_raw"])
	}
	if body["tickets_status"] != "issuing" || body["replayed"] != false {
		t.Fatalf("issue answer: %s", body["_raw"])
	}
	// The billing preview (INV-07): a staff invitation on a fresh account
	// whose terms bill invitations free costs nothing.
	charge, _ := body["charge"].(map[string]any)
	if charge == nil || charge["mode"] != "free" || charge["tickets"] != float64(5) ||
		charge["amount_minor"] != float64(0) || charge["free_tickets"] != float64(5) {
		t.Fatalf("issue charge preview: %s", body["_raw"])
	}
	if fr, _ := body["from_reserve"].(float64); fr != 2 {
		t.Fatalf("from_reserve = %v, want 2 (A|1|1 and the third GA place): %s", body["from_reserve"], body["_raw"])
	}
	orderID := uuid.MustParse(body["order_id"].(string))

	// Every place is sold in the issue transaction itself.
	for _, k := range []string{"A|1|1", "A|1|5", "ga|t1|000001", "ga|t1|000002", "ga|t1|000003"} {
		if s := f.seat(k); s.Status != "sold" {
			t.Fatalf("%s is %s after the issue, want sold", k, s.Status)
		}
	}
	if s := f.seat("ga|t1|000004"); s.Status != "unavailable" {
		t.Fatalf("ga|t1|000004 is %s, the invitation must take free places first", s.Status)
	}

	// The order: complimentary, 0, internal channel, issued by the owner.
	var (
		source, status, issuedVia string
		total                     int64
		channel                   uuid.UUID
		issuedBy                  *uuid.UUID
		buyerEmail                *string
		meta                      []byte
		csID, resID               uuid.UUID
	)
	if err := f.pool.QueryRow(ctx, `
		SELECT source, status, total, channel_id, issued_via, issued_by, buyer_email, metadata, checkout_session_id, reservation_id
		FROM orders WHERE id = $1`, orderID).Scan(&source, &status, &total, &channel, &issuedVia, &issuedBy, &buyerEmail, &meta, &csID, &resID); err != nil {
		t.Fatalf("read order: %v", err)
	}
	if source != "complimentary" || status != "paid" || total != 0 || channel != f.o.internal || issuedVia != "staff" ||
		issuedBy == nil || *issuedBy != f.ownerID || buyerEmail == nil || *buyerEmail != email {
		t.Fatalf("order: source %s status %s total %d channel %s via %s by %v email %v", source, status, total, channel, issuedVia, issuedBy, buyerEmail)
	}
	if !strings.Contains(string(meta), `"delivery": "guest_email"`) && !strings.Contains(string(meta), `"delivery":"guest_email"`) {
		t.Fatalf("delivery mode not in metadata: %s", meta)
	}
	var reserveLines, saleLines, resState string
	if err := f.pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE seat_origin = 'unavailable')::text, count(*) FILTER (WHERE seat_origin = 'available')::text
		FROM order_items WHERE order_id = $1`, orderID).Scan(&reserveLines, &saleLines); err != nil {
		t.Fatalf("seat origins: %v", err)
	}
	if reserveLines != "2" || saleLines != "3" {
		t.Fatalf("seat_origin lines: reserve %s sale %s, want 2 and 3", reserveLines, saleLines)
	}
	if err := f.pool.QueryRow(ctx, `SELECT state FROM reservations WHERE id = $1`, resID).Scan(&resState); err != nil || resState != "converted" {
		t.Fatalf("reservation state %q (%v), want converted", resState, err)
	}
	var jobs int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM worker_jobs WHERE job_type = 'checkout.issue_tickets' AND payload->>'checkout_session_id' = $1`, csID.String()).Scan(&jobs); err != nil || jobs != 1 {
		t.Fatalf("issue_tickets jobs = %d (%v), want 1", jobs, err)
	}

	// No WordPress site hears about it: the order's channel is the internal
	// one, which has no bil24_wp subscriber.
	loader := bil24wire.NewPoolLoader(f.pool)
	_, refChannel, err := loader.OrderRef(ctx, orderID)
	if err != nil || refChannel != f.o.internal {
		t.Fatalf("OrderRef channel %s (%v), want the internal channel", refChannel, err)
	}
	if _, ok, err := loader.SubscriberByChannel(ctx, refChannel); err != nil || ok {
		t.Fatalf("the internal channel resolves a WordPress subscriber: ok=%v err=%v", ok, err)
	}

	// Cancelling before the tickets exist is refused, not half-done.
	cancelPath := "/v1/organizations/" + f.o.org.String() + "/invitations/" + orderID.String() + "/cancel"
	if code, body := f.do(f.manager, http.MethodPost, cancelPath, `{}`); code != http.StatusConflict {
		t.Fatalf("cancel before issuance: %d %s", code, body["_raw"])
	}

	// The worker's job: mint the tickets.
	cs, err := gen.New(f.pool).GetCheckoutSessionByID(ctx, csID)
	if err != nil {
		t.Fatalf("checkout session: %v", err)
	}
	tickets, err := f.srv.ticketsHandler().IssueTicketsForCheckout(ctx, cs)
	if err != nil || len(tickets) != 5 {
		t.Fatalf("IssueTicketsForCheckout: %d tickets, %v", len(tickets), err)
	}

	// The list shows it, issued by the owner, with every place.
	code, list := f.do(f.manager, http.MethodGet, f.issuePath(), "")
	if code != http.StatusOK {
		t.Fatalf("list: %d %s", code, list["_raw"])
	}
	items, _ := list["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("list items: %s", list["_raw"])
	}
	item := items[0].(map[string]any)
	if item["order_id"] != orderID.String() || item["delivery"] != "guest_email" || item["tickets_active"].(float64) != 5 ||
		len(item["places"].([]any)) != 5 || item["issued_by"].(map[string]any)["user_id"] != f.ownerID.String() {
		t.Fatalf("list item: %s", list["_raw"])
	}

	// An agent may not cancel; the manager cancels the owner's invitation.
	if code, body := f.do(f.agent, http.MethodPost, cancelPath, `{}`); code != http.StatusForbidden {
		t.Fatalf("agent cancel: %d %s", code, body["_raw"])
	}
	code, body = f.do(f.manager, http.MethodPost, cancelPath, `{"notify_guest": false}`)
	if code != http.StatusOK {
		t.Fatalf("manager cancel: %d %s", code, body["_raw"])
	}
	// A cancellation moves no money: the order stays paid, so the
	// invitation is still billed (owner rule: the charge stays).
	if body["order_status"] != "paid" || len(body["cancelled_ticket_ids"].([]any)) != 5 || body["notify_guest"] != false {
		t.Fatalf("cancel answer: %s", body["_raw"])
	}
	inv02AssertBilled(t, f, orderID, 5)
	want := map[string]string{
		"A|1|1": "unavailable", "A|1|5": "available",
		"ga|t1|000001": "available", "ga|t1|000002": "available", "ga|t1|000003": "unavailable",
	}
	for k, st := range want {
		if s := f.seat(k); s.Status != st {
			t.Fatalf("%s is %s after the cancel, want %s", k, s.Status, st)
		}
	}
	// A second cancel finds nothing active and changes nothing.
	code, body = f.do(f.o.owner, http.MethodPost, cancelPath, ``)
	if code != http.StatusOK || len(body["cancelled_ticket_ids"].([]any)) != 0 {
		t.Fatalf("repeat cancel: %d %s", code, body["_raw"])
	}
}

func TestINV02_SaleRestrictionsDoNotApply_CancelledSessionRefuses(t *testing.T) {
	// The session started an hour ago, so its sales ended too.
	f := newINV02Fx(t, -time.Hour)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `UPDATE ticket_tiers SET is_open = false, sale_window_end = now() - interval '1 day' WHERE session_id = $1`, f.session); err != nil {
		t.Fatalf("close categories: %v", err)
	}
	code, body := f.issue(f.manager, inv02Body(uuid.New(), []string{"A|1|2"}, &f.gaTier, 1, "pdf_only", ""))
	if code != http.StatusCreated {
		t.Fatalf("issue after start, sales end and in a closed category: %d %s", code, body["_raw"])
	}
	var email *string
	var customer *uuid.UUID
	if err := f.pool.QueryRow(ctx, `SELECT buyer_email, customer_id FROM orders WHERE id = $1`, body["order_id"]).Scan(&email, &customer); err != nil {
		t.Fatalf("order: %v", err)
	}
	if email != nil || customer != nil {
		t.Fatalf("pdf_only order carries a guest address or customer: %v %v", email, customer)
	}

	// A public hold on the same session is refused: the gate is only off for staff.
	if _, err := hcheckout.CreateSeatedHold(ctx, f.pool, gen.New(f.pool), hcheckout.SeatedHoldInput{
		OrgID: f.o.org, ChannelID: f.o.sales, SessionID: f.session, SeatKeys: []string{"A|1|6"}, ExpiresAt: time.Now().Add(10 * time.Minute),
	}); err == nil {
		t.Fatalf("a public hold after the sales end must be refused")
	}

	if _, err := f.pool.Exec(ctx, `UPDATE sessions SET status = 'cancelled' WHERE id = $1`, f.session); err != nil {
		t.Fatalf("cancel session: %v", err)
	}
	code, body = f.issue(f.o.owner, inv02Body(uuid.New(), []string{"A|1|7"}, nil, 0, "pdf_only", ""))
	if code != http.StatusUnprocessableEntity || !strings.Contains(body["_raw"].(string), "invitation.session_cancelled") {
		t.Fatalf("cancelled session: %d %s", code, body["_raw"])
	}
	if s := f.seat("A|1|7"); s.Status != "available" {
		t.Fatalf("a refused issue changed a seat: %s", s.Status)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE sessions SET deleted_at = now() WHERE id = $1`, f.session); err != nil {
		t.Fatalf("delete session: %v", err)
	}
	code, body = f.issue(f.o.owner, inv02Body(uuid.New(), []string{"A|1|7"}, nil, 0, "pdf_only", ""))
	if code != http.StatusNotFound {
		t.Fatalf("deleted session: %d %s", code, body["_raw"])
	}
}

func TestINV02_RequestIDReplay_ValidationAndIsolation(t *testing.T) {
	f := newINV02Fx(t, 30*24*time.Hour)
	ctx := context.Background()
	rid := uuid.New()
	payload := inv02Body(rid, []string{"A|1|4"}, nil, 0, "my_email", "")

	code, first := f.issue(f.o.owner, payload)
	if code != http.StatusCreated {
		t.Fatalf("first issue: %d %s", code, first["_raw"])
	}
	code, again := f.issue(f.o.owner, payload)
	if code != http.StatusOK || again["replayed"] != true || again["order_id"] != first["order_id"] {
		t.Fatalf("replay: %d %s", code, again["_raw"])
	}
	var orders, reservations int
	if err := f.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM orders WHERE session_id = $1), (SELECT count(*) FROM reservations WHERE session_id = $1)`,
		f.session).Scan(&orders, &reservations); err != nil || orders != 1 || reservations != 1 {
		t.Fatalf("replay wrote rows: orders %d reservations %d (%v)", orders, reservations, err)
	}

	// Refusals.
	cases := []struct {
		token, body, code string
		status            int
	}{
		{f.o.owner, inv02Body(uuid.New(), nil, nil, 0, "pdf_only", ""), "invitation.empty", 400},
		{f.o.owner, inv02Body(uuid.New(), []string{"A|1|4"}, nil, 0, "pdf_only", ""), "invitation.seats_taken", 409},
		{f.o.owner, inv02Body(uuid.New(), []string{"Z|9|9"}, nil, 0, "pdf_only", ""), "invitation.unknown_seat", 400},
		{f.o.owner, inv02Body(uuid.New(), []string{"A|1|6"}, nil, 0, "guest_email", ""), "invitation.guest_email_required", 422},
		{f.o.owner, inv02Body(uuid.New(), nil, &f.gaTier, 51, "pdf_only", ""), "invitation.too_many", 400},
		{f.o.owner, inv02Body(uuid.New(), nil, &f.gaTier, 5, "pdf_only", ""), "invitation.not_enough_places", 409},
		{f.agent, inv02Body(uuid.New(), []string{"A|1|6"}, nil, 0, "pdf_only", ""), "", 403},
	}
	for i, c := range cases {
		code, body := f.issue(c.token, c.body)
		if code != c.status || !strings.Contains(body["_raw"].(string), c.code) {
			t.Fatalf("case %d: %d %s, want %d %s", i, code, body["_raw"], c.status, c.code)
		}
	}
	if code, body := f.issue(f.o.owner, inv02Body(uuid.New(), []string{"A|1|4"}, nil, 0, "pdf_only", "")); code != http.StatusConflict ||
		!strings.Contains(body["_raw"].(string), `"A|1|4"`) {
		t.Fatalf("seats_taken must name the seat: %d %s", code, body["_raw"])
	}

	// Another organization sees nothing of this one.
	other := newINV01Org(t, ctx, f.pool, f.secret)
	foreign := "/v1/organizations/" + other.org.String()
	if code, body := f.do(other.owner, http.MethodPost, foreign+"/sessions/"+f.session.String()+"/invitations",
		inv02Body(uuid.New(), []string{"A|1|6"}, nil, 0, "pdf_only", "")); code != http.StatusNotFound {
		t.Fatalf("foreign issue: %d %s", code, body["_raw"])
	}
	if code, body := f.do(other.owner, http.MethodGet, foreign+"/sessions/"+f.session.String()+"/invitations", ""); code != http.StatusNotFound {
		t.Fatalf("foreign list: %d %s", code, body["_raw"])
	}
	if code, body := f.do(other.owner, http.MethodPost, foreign+"/invitations/"+first["order_id"].(string)+"/cancel", `{}`); code != http.StatusNotFound {
		t.Fatalf("foreign cancel: %d %s", code, body["_raw"])
	}
	if code, body := f.do(other.owner, http.MethodPost, "/v1/organizations/"+f.o.org.String()+"/sessions/"+f.session.String()+"/invitations",
		inv02Body(uuid.New(), []string{"A|1|6"}, nil, 0, "pdf_only", "")); code != http.StatusForbidden && code != http.StatusNotFound {
		t.Fatalf("non-member issue: %d %s", code, body["_raw"])
	}
	if s := f.seat("A|1|6"); s.Status != "available" {
		t.Fatalf("a refused request took A|1|6: %s", s.Status)
	}
	// The same request_id on another session of the organization is a conflict.
	if code, body := f.do(f.o.owner, http.MethodPost, "/v1/organizations/"+f.o.org.String()+"/sessions/"+uuid.NewString()+"/invitations", payload); code != http.StatusConflict {
		t.Fatalf("request_id reused for another session: %d %s", code, body["_raw"])
	}
}

func TestINV02_ConcurrentInvitationAndPublicHold_OneWins(t *testing.T) {
	f := newINV02Fx(t, 30*24*time.Hour)
	ctx := context.Background()
	const workers = 16
	var (
		wg                          sync.WaitGroup
		mu                          sync.Mutex
		invWins, holdWins, refusals int
		failures                    []string
	)
	start := make(chan struct{})
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			if i%2 == 0 {
				code, body := f.issue(f.o.owner, inv02Body(uuid.New(), []string{"A|1|8"}, nil, 0, "pdf_only", ""))
				mu.Lock()
				defer mu.Unlock()
				switch code {
				case http.StatusCreated:
					invWins++
				case http.StatusConflict:
					refusals++
				default:
					failures = append(failures, body["_raw"].(string))
				}
				return
			}
			_, err := hcheckout.CreateSeatedHold(ctx, f.pool, gen.New(f.pool), hcheckout.SeatedHoldInput{
				OrgID: f.o.org, ChannelID: f.o.sales, SessionID: f.session, SeatKeys: []string{"A|1|8"},
				ExpiresAt: time.Now().Add(10 * time.Minute),
			})
			mu.Lock()
			defer mu.Unlock()
			var conflict *hcheckout.SeatConflictsError
			switch {
			case err == nil:
				holdWins++
			case errors.As(err, &conflict):
				refusals++
			default:
				failures = append(failures, err.Error())
			}
		}(i)
	}
	close(start)
	wg.Wait()
	if len(failures) > 0 {
		t.Fatalf("unexpected failures (deadlock or 5xx): %v", failures)
	}
	if invWins+holdWins != 1 || refusals != workers-1 {
		t.Fatalf("invitations %d holds %d refusals %d, want exactly one winner", invWins, holdWins, refusals)
	}
	s := f.seat("A|1|8")
	if (invWins == 1 && s.Status != "sold") || (holdWins == 1 && s.Status != "held") {
		t.Fatalf("A|1|8 is %s with invitation wins %d, hold wins %d", s.Status, invWins, holdWins)
	}
}
