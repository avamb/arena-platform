//go:build integration

// event_status_http_integration_test.go — EC-10 on the server: the status
// route (publish, take off sale, archive; every real move audited; a foreign
// organization's event is the route's own 404), the delete-impact dry run and
// the guard that refuses to delete an event that has sold anything. Goes
// through the REAL router with organization API keys.
//
// Run against a migrated database (the tests clean up their own rows).
package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/apikeys"
)

type evStatusFixture struct {
	t       *testing.T
	ts      *httptest.Server
	pool    *pgxpool.Pool
	q       *gen.Queries
	orgID   uuid.UUID
	key     string // may publish and delete
	readKey string // event.read only
	venueID uuid.UUID
	channel uuid.UUID
	feed    string
	feedID  uuid.UUID
	suffix  string
}

func newEvStatusFixture(t *testing.T) *evStatusFixture {
	t.Helper()
	srv, _ := productionIntegrationServer(t)
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	pool := srv.pgxPool
	f := &evStatusFixture{t: t, ts: ts, pool: pool, q: gen.New(pool), suffix: uuid.NewString()[:8]}
	ctx := context.Background()

	user, err := f.q.InsertUser(ctx, "ev-status-"+uuid.NewString()+"@example.test", "x", "en")
	if err != nil {
		t.Fatalf("InsertUser: %v", err)
	}
	org, err := f.q.InsertOrganization(ctx, "EV Status "+f.suffix, "ev-status-"+f.suffix, "EE", "en", 1200)
	if err != nil {
		t.Fatalf("InsertOrganization: %v", err)
	}
	f.orgID = org.ID
	f.venueID, f.channel = uuid.New(), uuid.New()
	f.exec(`INSERT INTO venues (id, org_id, name, timezone) VALUES ($1, $2, $3, 'Europe/Madrid')`, f.venueID, org.ID, "EV Venue "+f.suffix)
	f.exec(`INSERT INTO sales_channels (id, org_id, name) VALUES ($1, $2, $3)`, f.channel, org.ID, "EV Channel "+f.suffix)
	f.feed = "evstatus-" + uuid.NewString()
	tok, err := f.q.InsertFeedToken(ctx, f.feed, f.channel, "ev status")
	if err != nil {
		t.Fatalf("InsertFeedToken: %v", err)
	}
	f.feedID = tok.ID

	issue := func(scopes ...string) string {
		_, key, err := apikeys.Issue(ctx, apikeys.NewStoreFromQueries(f.q), apikeys.IssueInput{
			OrgID: org.ID, Name: "ev-status-" + uuid.NewString()[:6], Scopes: scopes, CreatedBy: user.ID,
		})
		if err != nil {
			t.Fatalf("apikeys.Issue: %v", err)
		}
		return key
	}
	f.key = issue("event.read", "event.publish", "event.delete")
	f.readKey = issue("event.read")

	t.Cleanup(func() {
		c := context.Background()
		sessions := `(SELECT s.id FROM sessions s JOIN events e ON e.id = s.event_id WHERE e.org_id = $1)`
		for _, sql := range []string{
			`DELETE FROM outbox_events WHERE payload->>'org_id' = $1::text`,
			`DELETE FROM order_items WHERE order_id IN (SELECT id FROM orders WHERE org_id = $1)`,
			`DELETE FROM tickets WHERE session_id IN ` + sessions,
			`DELETE FROM orders WHERE org_id = $1`,
			`DELETE FROM checkout_sessions WHERE org_id = $1`,
			`DELETE FROM reservations WHERE org_id = $1`,
			`DELETE FROM event_publications WHERE event_id IN (SELECT id FROM events WHERE org_id = $1)`,
			`DELETE FROM agent_feed_tokens WHERE sales_channel_id IN (SELECT id FROM sales_channels WHERE org_id = $1)`,
			`DELETE FROM ticket_tiers WHERE session_id IN ` + sessions,
			`DELETE FROM api_keys WHERE org_id = $1`,
			`DELETE FROM audit_events WHERE metadata->>'org_id' = $1::text`,
			`DELETE FROM sales_channels WHERE org_id = $1`,
			`DELETE FROM sessions WHERE id IN ` + sessions,
			`DELETE FROM events WHERE org_id = $1`,
			`DELETE FROM venues WHERE org_id = $1`,
			`DELETE FROM organizations WHERE id = $1`,
		} {
			if _, err := pool.Exec(c, sql, org.ID); err != nil {
				t.Logf("ev status cleanup: %s: %v", sql, err)
			}
		}
		_, _ = pool.Exec(c, `DELETE FROM users WHERE id = $1`, user.ID)
	})
	return f
}

func (f *evStatusFixture) exec(sql string, args ...any) {
	f.t.Helper()
	if _, err := f.pool.Exec(context.Background(), sql, args...); err != nil {
		f.t.Fatalf("fixture %q: %v", sql, err)
	}
}

// event inserts an event with one dated session and one priced category, so
// the publish gate passes, and publishes it to the fixture's feed.
func (f *evStatusFixture) event(status string) (eventID, sessionID uuid.UUID) {
	f.t.Helper()
	eventID, sessionID = uuid.New(), uuid.New()
	f.exec(`INSERT INTO events (id, org_id, name, status, visibility, slug) VALUES ($1, $2, $3, $4, 'public', $5)`,
		eventID, f.orgID, "EV "+status+" "+f.suffix, status, "ev-"+uuid.NewString()[:8])
	f.exec(`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, status, currency, currency_source)
	        VALUES ($1, $2, $3, $4, $4::timestamptz + interval '2 hours', 5, 'scheduled', 'EUR', 'override')`,
		sessionID, eventID, f.venueID, time.Now().UTC().Add(30*24*time.Hour).Truncate(time.Hour))
	f.exec(`INSERT INTO ticket_tiers (id, session_id, name, pricing_mode, price_amount, currency, capacity, unit_seq, is_open)
	        VALUES ($1, $2, 'Standing', 'fixed', 2500, 'EUR', 5, 1, true)`, uuid.New(), sessionID)
	if _, err := f.q.PublishEvent(context.Background(), eventID, f.feedID, nil); err != nil {
		f.t.Fatalf("PublishEvent: %v", err)
	}
	return eventID, sessionID
}

// sell puts an order of the given status on the event, with one ticket when
// withTicket is set.
func (f *evStatusFixture) sell(eventID, sessionID uuid.UUID, status string, withTicket bool) {
	f.t.Helper()
	res, cs, order := uuid.New(), uuid.New(), uuid.New()
	f.exec(`INSERT INTO reservations (id, org_id, channel_id, session_id, quantity, state, expires_at, converted_at)
	        VALUES ($1, $2, $3, $4, 1, 'converted', now() + interval '1 hour', now())`, res, f.orgID, f.channel, sessionID)
	f.exec(`INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state) VALUES ($1, $2, $3, $4, 'completed')`, cs, f.orgID, f.channel, res)
	f.exec(`INSERT INTO orders (id, org_id, channel_id, event_id, session_id, checkout_session_id, reservation_id,
	                            source, status, currency, subtotal, discount, charge, total, buyer_email)
	        VALUES ($1, $2, $3, $4, $5, $6, $7, 'public_feed', $8, 'EUR', 2500, 0, 0, 2500, 'ev-buyer@example.test')`,
		order, f.orgID, f.channel, eventID, sessionID, cs, res, status)
	if withTicket {
		f.exec(`INSERT INTO tickets (id, checkout_session_id, session_id, holder_email, order_id) VALUES ($1, $2, $3, 'ev-buyer@example.test', $4)`,
			uuid.New(), cs, sessionID, order)
	}
}

func (f *evStatusFixture) do(key, method, path, body string) (int, map[string]any) {
	f.t.Helper()
	resp := integDoRequest(f.t, f.ts.Client(), method, f.ts.URL+path, key, body)
	raw := integReadBody(f.t, resp)
	out := map[string]any{}
	if strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			f.t.Fatalf("%s %s: decode %q: %v", method, path, raw, err)
		}
	}
	return resp.StatusCode, out
}

func (f *evStatusFixture) base(eventID uuid.UUID) string {
	return "/v1/organizations/" + f.orgID.String() + "/events/" + eventID.String()
}

func (f *evStatusFixture) status(eventID uuid.UUID) string {
	f.t.Helper()
	var s string
	if err := f.pool.QueryRow(context.Background(), `SELECT status FROM events WHERE id = $1`, eventID).Scan(&s); err != nil {
		f.t.Fatal(err)
	}
	return s
}

func (f *evStatusFixture) deleted(eventID uuid.UUID) bool {
	f.t.Helper()
	var d bool
	if err := f.pool.QueryRow(context.Background(), `SELECT deleted_at IS NOT NULL FROM events WHERE id = $1`, eventID).Scan(&d); err != nil {
		f.t.Fatal(err)
	}
	return d
}

func (f *evStatusFixture) publications(eventID uuid.UUID) int {
	f.t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM event_publications WHERE event_id = $1`, eventID).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

// auditMoves lists "from>to" of the status audit rows of the event, oldest first.
func (f *evStatusFixture) auditMoves(eventID uuid.UUID) []string {
	f.t.Helper()
	rows, err := f.pool.Query(context.Background(),
		`SELECT metadata->>'from_status', metadata->>'to_status' FROM audit_events
		  WHERE action = 'v1.event.status_update' AND resource_id = $1 ORDER BY occurred_at, id`, eventID.String())
	if err != nil {
		f.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var from, to string
		if err := rows.Scan(&from, &to); err != nil {
			f.t.Fatal(err)
		}
		out = append(out, from+">"+to)
	}
	return out
}

func (f *evStatusFixture) feedHas(eventID uuid.UUID) bool {
	f.t.Helper()
	resp := integDoRequest(f.t, f.ts.Client(), http.MethodGet, f.ts.URL+"/v1/public/feeds/"+f.feed+"/events", "", "")
	return strings.Contains(integReadBody(f.t, resp), eventID.String())
}

func evCode(m map[string]any) string {
	if e, ok := m["error"].(map[string]any); ok {
		if c, ok := e["code"].(string); ok {
			return c
		}
	}
	return ""
}

func evDetail(m map[string]any, key string) float64 {
	if e, ok := m["error"].(map[string]any); ok {
		if d, ok := e["details"].(map[string]any); ok {
			if v, ok := d[key].(float64); ok {
				return v
			}
		}
	}
	return -1
}

// publish -> take off sale -> publish again -> archive: each real move is
// audited, a repeat is a silent no-op, sold tickets and the event's
// publications are never touched, and the feed shows the event only while it
// is published.
func TestEventStatusHTTP_Lifecycle_AuditedAndSalesUntouched(t *testing.T) {
	f := newEvStatusFixture(t)
	eventID, sessionID := f.event("draft")
	f.sell(eventID, sessionID, "paid", true)
	post := func(to string) (int, map[string]any) {
		return f.do(f.key, http.MethodPost, f.base(eventID)+"/status", `{"status":"`+to+`"}`)
	}

	if st, body := post("published"); st != http.StatusOK {
		t.Fatalf("publish: %d %v", st, body)
	}
	if !f.feedHas(eventID) {
		t.Error("a published event must be in its feed")
	}
	if st, _ := post("published"); st != http.StatusOK {
		t.Errorf("publishing twice is a no-op, got %d", st)
	}

	// Take off sale.
	if st, body := post("draft"); st != http.StatusOK {
		t.Fatalf("take off sale: %d %v", st, body)
	}
	if got := f.status(eventID); got != "draft" {
		t.Errorf("status after taking off sale = %q", got)
	}
	if f.feedHas(eventID) {
		t.Error("an event taken off sale must leave its feed")
	}
	if n := f.publications(eventID); n != 1 {
		t.Errorf("take off sale must keep the event's publications, have %d", n)
	}
	var tickets, orders int
	_ = f.pool.QueryRow(context.Background(), `SELECT count(*) FROM tickets WHERE session_id = $1 AND status = 'active'`, sessionID).Scan(&tickets)
	_ = f.pool.QueryRow(context.Background(), `SELECT count(*) FROM orders WHERE event_id = $1 AND status = 'paid'`, eventID).Scan(&orders)
	if tickets != 1 || orders != 1 {
		t.Errorf("sold tickets and paid orders must be untouched, have tickets=%d orders=%d", tickets, orders)
	}

	// Back on sale, the same channels carry it again.
	if st, body := post("published"); st != http.StatusOK {
		t.Fatalf("publish again: %d %v", st, body)
	}
	if !f.feedHas(eventID) {
		t.Error("publishing again restores the feed")
	}

	// A draft cannot be archived; a published event can, and it is final.
	other, _ := f.event("draft")
	if st, body := f.do(f.key, http.MethodPost, f.base(other)+"/status", `{"status":"archived"}`); st != http.StatusUnprocessableEntity || evCode(body) != "event.invalid_transition" {
		t.Errorf("draft -> archived: %d %v", st, body)
	}
	if st, body := post("archived"); st != http.StatusOK {
		t.Fatalf("archive: %d %v", st, body)
	}
	if st, body := post("draft"); st != http.StatusUnprocessableEntity || evCode(body) != "event.invalid_transition" {
		t.Errorf("archived -> draft must be refused: %d %v", st, body)
	}
	if st, body := post("published"); st != http.StatusUnprocessableEntity {
		t.Errorf("archived -> published must be refused: %d %v", st, body)
	}

	want := []string{"draft>published", "published>draft", "draft>published", "published>archived"}
	if got := f.auditMoves(eventID); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("audit moves = %v, want %v", got, want)
	}
}

// The publish gate still holds: no date, no priced category, no publication.
func TestEventStatusHTTP_PublishGate(t *testing.T) {
	f := newEvStatusFixture(t)
	empty := uuid.New()
	f.exec(`INSERT INTO events (id, org_id, name, status, visibility) VALUES ($1, $2, $3, 'draft', 'public')`, empty, f.orgID, "EV empty "+f.suffix)
	if st, body := f.do(f.key, http.MethodPost, f.base(empty)+"/status", `{"status":"published"}`); st != http.StatusUnprocessableEntity || evCode(body) != "event.publish_requires_session" {
		t.Errorf("no date: %d %v", st, body)
	}
	if got := f.status(empty); got != "draft" {
		t.Errorf("a refused publish must not change the status, have %q", got)
	}
	if moves := f.auditMoves(empty); len(moves) != 0 {
		t.Errorf("a refused move must not be audited: %v", moves)
	}
}

// The dry run and the delete agree: any order that was ever paid, or any
// ticket, blocks the delete with 409 event.has_paid_orders and the counts, and
// leaves the event untouched; unpaid orders do not.
func TestEventDeleteHTTP_GuardAndImpact(t *testing.T) {
	f := newEvStatusFixture(t)

	// Never sold: allowed, then really deleted.
	free, _ := f.event("published")
	st, body := f.do(f.key, http.MethodGet, f.base(free)+"/delete-impact", "")
	if st != http.StatusOK || body["can_delete"] != true || body["blocked"] != "" || body["paid_orders"] != float64(0) || body["tickets"] != float64(0) || body["sessions"] != float64(1) || body["status"] != "published" || body["can_archive"] != true {
		t.Fatalf("impact of a never-sold event: %d %v", st, body)
	}

	// Orders that never paid do not count.
	unpaid, unpaidSession := f.event("published")
	for _, s := range []string{"pending_payment", "cancelled", "expired", "abandoned"} {
		f.sell(unpaid, unpaidSession, s, false)
	}
	st, body = f.do(f.key, http.MethodGet, f.base(unpaid)+"/delete-impact", "")
	if st != http.StatusOK || body["can_delete"] != true || body["paid_orders"] != float64(0) {
		t.Fatalf("unpaid orders must not block: %d %v", st, body)
	}

	// Every ever-paid status blocks.
	for _, paidStatus := range []string{"paid", "partially_refunded", "refunded"} {
		ev, sess := f.event("published")
		f.sell(ev, sess, paidStatus, true)
		f.sell(ev, sess, paidStatus, false)
		st, body = f.do(f.key, http.MethodGet, f.base(ev)+"/delete-impact", "")
		if st != http.StatusOK || body["can_delete"] != false || body["blocked"] != "has_paid_orders" || body["paid_orders"] != float64(2) || body["tickets"] != float64(1) || body["can_archive"] != true {
			t.Errorf("%s: impact = %d %v", paidStatus, st, body)
		}
		st, body = f.do(f.key, http.MethodDelete, f.base(ev), "")
		if st != http.StatusConflict || evCode(body) != "event.has_paid_orders" {
			t.Errorf("%s: delete = %d %v", paidStatus, st, body)
		}
		if evDetail(body, "paid_orders") != 2 || evDetail(body, "tickets") != 1 || evDetail(body, "sessions") != 1 {
			t.Errorf("%s: the 409 must carry the counts: %v", paidStatus, body)
		}
		if f.deleted(ev) || f.status(ev) != "published" {
			t.Errorf("%s: a refused delete must change nothing", paidStatus)
		}
		// Archive stays possible, and an archived event with sales still cannot be deleted.
		if st, body = f.do(f.key, http.MethodPost, f.base(ev)+"/status", `{"status":"archived"}`); st != http.StatusOK {
			t.Errorf("%s: archive after the refusal: %d %v", paidStatus, st, body)
		}
		st, body = f.do(f.key, http.MethodGet, f.base(ev)+"/delete-impact", "")
		if st != http.StatusOK || body["can_delete"] != false || body["can_archive"] != false || body["status"] != "archived" {
			t.Errorf("%s: impact of an archived event with sales = %d %v", paidStatus, st, body)
		}
		if st, body = f.do(f.key, http.MethodDelete, f.base(ev), ""); st != http.StatusConflict {
			t.Errorf("%s: an archived event with sales is not deletable: %d %v", paidStatus, st, body)
		}
	}

	// An issued ticket blocks even when its order never paid (a data-fix leftover).
	ticketOnly, ticketOnlySession := f.event("published")
	f.sell(ticketOnly, ticketOnlySession, "pending_payment", true)
	if st, body = f.do(f.key, http.MethodDelete, f.base(ticketOnly), ""); st != http.StatusConflict {
		t.Errorf("an issued ticket must block the delete: %d %v", st, body)
	}

	// The never-sold and the unpaid-only events delete, audited.
	for _, ev := range []uuid.UUID{free, unpaid} {
		if st, body = f.do(f.key, http.MethodDelete, f.base(ev), ""); st != http.StatusOK || body["deleted"] != true {
			t.Errorf("delete of a never-sold event: %d %v", st, body)
		}
		if !f.deleted(ev) {
			t.Error("the event must be soft-deleted")
		}
		var audits int
		_ = f.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_events WHERE action = 'v1.event.delete' AND resource_id = $1`, ev.String()).Scan(&audits)
		if audits != 1 {
			t.Errorf("a delete must be audited once, have %d", audits)
		}
		// A deleted event is gone for the dry run too.
		if st, _ = f.do(f.key, http.MethodGet, f.base(ev)+"/delete-impact", ""); st != http.StatusNotFound {
			t.Errorf("impact of a deleted event = %d, want 404", st)
		}
	}
}

// Another organization's key, a key without the permission, and a wrong org id
// get nothing: 404 for a foreign event, 403 without the permission, and the
// event is never touched.
func TestEventStatusHTTP_OrgIsolationAndPermissions(t *testing.T) {
	a := newEvStatusFixture(t)
	b := newEvStatusFixture(t)
	eventID, sessionID := a.event("published")
	a.sell(eventID, sessionID, "paid", true)
	draft, _ := a.event("draft")

	foreign := func(method, suffix, body string) (int, map[string]any) {
		// B's key against A's organization and A's event.
		return b.do(b.key, method, "/v1/organizations/"+a.orgID.String()+"/events/"+eventID.String()+suffix, body)
	}
	for _, c := range []struct{ method, suffix, body string }{
		{http.MethodPost, "/status", `{"status":"draft"}`},
		{http.MethodDelete, "", ""},
		{http.MethodGet, "/delete-impact", ""},
	} {
		if st, body := foreign(c.method, c.suffix, c.body); st == http.StatusOK || st == http.StatusConflict || st == http.StatusUnprocessableEntity {
			t.Errorf("foreign org %s %s: %d %v", c.method, c.suffix, st, body)
		}
	}
	// B's own org id with A's event id is the route's 404.
	own := "/v1/organizations/" + b.orgID.String() + "/events/" + eventID.String()
	for _, c := range []struct{ method, suffix, body string }{
		{http.MethodPost, "/status", `{"status":"draft"}`},
		{http.MethodDelete, "", ""},
		{http.MethodGet, "/delete-impact", ""},
	} {
		if st, body := b.do(b.key, c.method, own+c.suffix, c.body); st != http.StatusNotFound || evCode(body) != "event.not_found" {
			t.Errorf("foreign event via own org %s %s: %d %v", c.method, c.suffix, st, body)
		}
	}
	if a.status(eventID) != "published" || a.deleted(eventID) {
		t.Error("the foreign calls must not touch the event")
	}

	// A key without event.publish / event.delete is refused.
	for _, c := range []struct{ method, path, body string }{
		{http.MethodPost, a.base(draft) + "/status", `{"status":"published"}`},
		{http.MethodDelete, a.base(draft), ""},
		{http.MethodGet, a.base(draft) + "/delete-impact", ""},
	} {
		if st, body := a.do(a.readKey, c.method, c.path, c.body); st != http.StatusForbidden {
			t.Errorf("read-only key %s %s: %d %v", c.method, c.path, st, body)
		}
	}
	if a.status(draft) != "draft" || a.deleted(draft) {
		t.Error("a refused call must not touch the event")
	}
}
