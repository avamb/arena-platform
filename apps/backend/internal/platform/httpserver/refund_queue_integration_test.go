//go:build integration

package httpserver

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

// EC-V3 and EC-V5 (spec 62 §12.2): the order list finds an order by a part of
// its event's name and by the session date, and carries the tickets with the
// state of their letters; the refund queue lists a failed refund and an
// undelivered ticket letter of the organization and nobody else's.
func TestRefundQueueAndExpandedOrders_Integration(t *testing.T) {
	stub := newStubStripe(t)
	srv, _ := productionIntegrationServerCfg(t, func(c *config.Config) { c.StripeAPIBaseURL = stub.baseURL() })
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	pf := &prom0113Fixture{ts: ts, client: ts.Client(), q: gen.New(srv.pgxPool)}
	user, err := pf.q.InsertUser(context.Background(), "ecv5-"+uuid.NewString()+"@example.test", "x", "en")
	if err != nil {
		t.Fatal(err)
	}
	pf.userID = user.ID
	org := pf.org(t, "ECV5")
	other := pf.org(t, "ECV5OTHER")
	key := pf.key(t, org, "refund.read", "refund.create", "refund.approve", "order.read")
	otherKey := pf.key(t, other, "refund.read", "order.read")
	stub.refundHandler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"re_ecv5","status":"succeeded"}`)
	}
	stub.sessionPI = "pi_ecv5_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
	f := newPay03Order(t, srv.pgxPool, org, "stripe")
	base := "/v1/organizations/" + org.String()
	ctx := context.Background()

	// ── a clean organization has an empty queue
	st, out := pf.do(t, http.MethodGet, base+"/refunds/queue", key, "")
	if st != http.StatusOK || out["count"] != float64(0) {
		t.Fatalf("empty queue: %d %v", st, out)
	}

	// ── a failed refund and a failed ticket letter appear
	body := fmt.Sprintf(`{"items":[{"ticket_id":%q,"amount":1000},{"ticket_id":%q,"amount":500}],"reason":"queue test"}`,
		f.tickets[0], f.tickets[1])
	st, created := refundOrgPost(t, pf, base+"/orders/"+f.order.String()+"/refunds", key, "ecv5-"+uuid.NewString(), body)
	if st != http.StatusAccepted || len(created["refunds"].([]any)) != 2 {
		t.Fatalf("a partial refund of two tickets in one action: %d %v", st, created)
	}
	if _, err := srv.pgxPool.Exec(ctx, `UPDATE refunds SET state = 'failed', failure_code = 'provider_error'
		WHERE order_id = $1 AND ticket_id = $2`, f.order, f.tickets[0]); err != nil {
		t.Fatalf("fail a refund: %v", err)
	}
	if _, err := srv.pgxPool.Exec(ctx, `INSERT INTO delivery_jobs (ticket_id, recipient_email, status, last_error)
		VALUES ($1, 'buyer@example.com', 'failed', 'relay refused buyer@example.com')`, f.tickets[0]); err != nil {
		t.Fatalf("delivery job: %v", err)
	}
	st, out = pf.do(t, http.MethodGet, base+"/refunds/queue", key, "")
	if st != http.StatusOK {
		t.Fatalf("queue: %d %v", st, out)
	}
	refundRows := out["refunds"].([]any)
	if len(refundRows) != 1 || refundRows[0].(map[string]any)["state"] != "failed" || out["refunds_total"] != float64(1) {
		t.Fatalf("queue refunds = %v; want the one failed refund", refundRows)
	}
	letters := out["letters"].([]any)
	if len(letters) != 1 {
		t.Fatalf("queue letters = %v; want the failed ticket letter", letters)
	}
	l := letters[0].(map[string]any)
	if l["order_id"] != f.order.String() || l["kind"] != "ticket" || l["problem"] != "failed" ||
		l["tickets"] != float64(1) || strings.Contains(fmt.Sprint(l["reason"]), "@") {
		t.Fatalf("letter row = %v", l)
	}
	if out["count"] != float64(2) {
		t.Fatalf("count = %v; want 2", out["count"])
	}

	// ── another organization sees none of it
	if st, out = pf.do(t, http.MethodGet, base+"/refunds/queue", otherKey, ""); st != http.StatusForbidden && st != http.StatusNotFound {
		t.Fatalf("foreign org queue: %d %v; want a refusal", st, out)
	}
	otherQueue := "/v1/organizations/" + other.String() + "/refunds/queue"
	if st, out = pf.do(t, http.MethodGet, otherQueue, otherKey, ""); st != http.StatusOK || out["count"] != float64(0) {
		t.Fatalf("other org queue: %d %v; want empty", st, out)
	}

	// ── the order list: event-name search, session dates, tickets with letters
	var eventName string
	if err := srv.pgxPool.QueryRow(ctx, `SELECT e.name FROM events e JOIN orders o ON o.event_id = e.id WHERE o.id = $1`, f.order).Scan(&eventName); err != nil {
		t.Fatal(err)
	}
	var start time.Time
	if err := srv.pgxPool.QueryRow(ctx, `SELECT start_at FROM sessions WHERE id = $1`, f.session).Scan(&start); err != nil {
		t.Fatal(err)
	}
	list := func(q url.Values) []any {
		t.Helper()
		st, out := pf.do(t, http.MethodGet, base+"/orders?"+q.Encode(), key, "")
		if st != http.StatusOK {
			t.Fatalf("list %v: %d %v", q, st, out)
		}
		return out["orders"].([]any)
	}
	rows := list(url.Values{"q": {strings.ToLower(eventName[2:])}}) // "e abcd1234" -> a part, lower case
	if len(rows) != 1 || rows[0].(map[string]any)["id"] != f.order.String() {
		t.Fatalf("event-name search = %v; want the order", rows)
	}
	if rows := list(url.Values{"q": {"no such show"}}); len(rows) != 0 {
		t.Fatalf("unrelated search = %v; want none", rows)
	}
	if rows := list(url.Values{"session_from": {start.Add(-time.Hour).Format(time.RFC3339)}, "session_to": {start.Add(time.Hour).Format(time.RFC3339)}}); len(rows) != 1 {
		t.Fatalf("session range around the start = %v; want the order", rows)
	}
	if rows := list(url.Values{"session_from": {start.Add(time.Hour).Format(time.RFC3339)}}); len(rows) != 0 {
		t.Fatalf("session range after the start = %v; want none", rows)
	}
	row := list(url.Values{})[0].(map[string]any)
	tickets, _ := row["tickets"].([]any)
	if len(tickets) != 2 {
		t.Fatalf("tickets on the list row = %v; want 2", row["tickets"])
	}
	first := tickets[0].(map[string]any)
	if first["delivery_status"] != "failed" || first["recipient_email"] != "buyer@example.com" || first["price"] != float64(2500) {
		t.Fatalf("first ticket = %v", first)
	}
	if tickets[1].(map[string]any)["delivery_status"] != "none" {
		t.Fatalf("second ticket = %v; want no letter job", tickets[1])
	}
}
