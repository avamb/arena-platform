//go:build integration

// session_tickets_scans_integration_test.go — task M2: the order card lists the
// last scans of each ticket (both sources, newest first), the new
// GET .../sessions/{session_id}/tickets lists a session's tickets with their
// entry and filters "has not come yet", and the sales CSV carries the first
// entry's time, source and scanner. Real router, real database.
package httpserver

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/apikeys"
)

func asSlice(t *testing.T, v any) []map[string]any {
	t.Helper()
	raw, _ := v.([]any)
	out := make([]map[string]any, len(raw))
	for i, r := range raw {
		m, ok := r.(map[string]any)
		if !ok {
			t.Fatalf("element %d is %T, want an object", i, r)
		}
		out[i] = m
	}
	return out
}

func ticketNumbers(items []map[string]any) []int64 {
	out := make([]int64, len(items))
	for i, it := range items {
		n, _ := it["system_ticket_id"].(float64)
		out[i] = int64(n)
	}
	return out
}

func TestSessionTickets_ScansTwinSources(t *testing.T) {
	f := newECOrdersFixture(t)
	ctx := context.Background()
	run := uuid.NewString()[:8]

	// Two extra tickets of the session: an invitation nobody scanned (no
	// order) and a cancelled ticket of the paid order.
	invite, cancelled := uuid.New(), uuid.New()
	var tierID, csID uuid.UUID
	if err := f.pool.QueryRow(ctx, `SELECT tier_id, checkout_session_id FROM tickets WHERE id = $1`, f.ticket1).Scan(&tierID, &csID); err != nil {
		t.Fatalf("read tier: %v", err)
	}
	for _, s := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO tickets (id, checkout_session_id, session_id, tier_id, holder_email, ordinal)
		  VALUES ($1, $2, $3, $4, $5, 2)`, []any{invite, csID, f.sessionA, tierID, "guest-" + run + "@example.com"}},
		{`INSERT INTO tickets (id, checkout_session_id, session_id, tier_id, order_id, ordinal, status, cancelled_at)
		  VALUES ($1, $2, $3, $4, $5, 3, 'cancelled', now())`, []any{cancelled, csID, f.sessionA, tierID, f.paidOrder}},
	} {
		if _, err := f.pool.Exec(ctx, s.sql, s.args...); err != nil {
			t.Fatalf("extra ticket: %v", err)
		}
	}

	// A MACS subscriber of the organization (its scans reference it).
	var subID uuid.UUID
	if err := f.pool.QueryRow(ctx, `INSERT INTO webhook_subscribers (site_url, callback_url, signing_secret, event_types, active, kind, org_id)
		VALUES ('', 'https://macs.example.test/api/_wh/tickets', 'whsec_m2', '{}', true, 'macs', $1) RETURNING id`, f.orgA).Scan(&subID); err != nil {
		t.Fatalf("subscriber: %v", err)
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = f.pool.Exec(c, `DELETE FROM scan_events WHERE org_id = ANY($1)`, []uuid.UUID{f.orgA, f.orgB})
		_, _ = f.pool.Exec(c, `DELETE FROM api_keys WHERE org_id = $1`, f.orgA)
		_, _ = f.pool.Exec(c, `DELETE FROM webhook_subscribers WHERE id = $1`, subID)
		_, _ = f.pool.Exec(c, `DELETE FROM order_items WHERE ticket_id = $1`, cancelled)
		_, _ = f.pool.Exec(c, `DELETE FROM tickets WHERE id = ANY($1)`, []uuid.UUID{invite, cancelled})
	})

	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	// Ticket 1: the arena scanner turned it away, MACS let it in later.
	// used_at is left unset, as when its stamping failed: the admitted scan
	// row alone makes it entered.
	if _, err := f.pool.Exec(ctx, `INSERT INTO scan_events (org_id, session_id, ticket_id, credential_code, scanned_at, gate, result, source)
		VALUES ($1, $2, $3, $4, $5, 'North', 'denied', 'arena_scanner')`,
		f.orgA, f.sessionA, f.ticket1, "m2-a-"+run, base); err != nil {
		t.Fatalf("arena scan: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO scan_events (org_id, session_id, ticket_id, credential_code, scanned_at, gate, result, source, scanner_label, external_scan_id, subscriber_id)
		VALUES ($1, $2, $3, $4, $5, 'Gate 2', 'admitted', 'macs', 'Scanner 7', $6, $7)`,
		f.orgA, f.sessionA, f.ticket1, "m2-b-"+run, base.Add(30*time.Minute), "m2-"+run, subID); err != nil {
		t.Fatalf("macs scan: %v", err)
	}
	// Another organization's scan of the same ticket must not show.
	if _, err := f.pool.Exec(ctx, `INSERT INTO scan_events (org_id, session_id, ticket_id, credential_code, scanned_at, gate, result)
		VALUES ($1, NULL, $2, $3, $4, 'Foreign', 'admitted')`, f.orgB, f.ticket1, "m2-c-"+run, base.Add(40*time.Minute)); err != nil {
		t.Fatalf("foreign scan: %v", err)
	}

	// ── the order card ────────────────────────────────────────────────────
	code, card := f.get("/v1/organizations/"+f.orgA.String()+"/orders/"+f.paidOrder.String(), f.managerTok)
	if code != http.StatusOK {
		t.Fatalf("order card: %d %v", code, card)
	}
	byID := map[string]map[string]any{}
	for _, tk := range asSlice(t, card["tickets"]) {
		byID[tk["id"].(string)] = tk
	}
	t1 := byID[f.ticket1.String()]
	scans := asSlice(t, t1["scans"])
	if len(scans) != 2 || scans[0]["source"] != "macs" || scans[0]["result"] != "admitted" ||
		scans[0]["scanner_label"] != "Scanner 7" || scans[0]["gate"] != "Gate 2" ||
		scans[1]["source"] != "arena_scanner" || scans[1]["result"] != "denied" || scans[1]["gate"] != "North" {
		t.Fatalf("ticket 1 scans = %v", scans)
	}
	if t1["used_at"] != base.Add(30*time.Minute).Format(time.RFC3339) {
		t.Errorf("ticket 1 used_at = %v, want the admitted scan %s", t1["used_at"], base.Add(30*time.Minute).Format(time.RFC3339))
	}
	t2 := byID[f.ticket2.String()]
	if s := asSlice(t, t2["scans"]); len(s) != 0 || t2["scans"] == nil {
		t.Errorf("ticket 2 scans = %v, want an empty list", t2["scans"])
	}
	if t2["used_at"] == nil {
		t.Errorf("ticket 2 (barcode scanned) lost its used_at")
	}

	// ── the session list ──────────────────────────────────────────────────
	path := "/v1/organizations/" + f.orgA.String() + "/sessions/" + f.sessionA.String() + "/tickets"
	code, all := f.get(path, f.managerTok)
	if code != http.StatusOK || all["total"] != float64(4) {
		t.Fatalf("all tickets: %d %v", code, all)
	}
	if all["venue_timezone"] != "Europe/Madrid" {
		t.Errorf("venue_timezone = %v", all["venue_timezone"])
	}

	code, no := f.get(path+"?entered=false", f.managerTok)
	items := asSlice(t, no["items"])
	if code != http.StatusOK || no["total"] != float64(1) || len(items) != 1 {
		t.Fatalf("not entered: %d %v", code, no)
	}
	if items[0]["ticket_id"] != invite.String() || items[0]["entered"] != false || items[0]["order_id"] != nil ||
		items[0]["buyer_email"] != "guest-"+run+"@example.com" || items[0]["last_scan"] != nil {
		t.Errorf("the not-entered ticket = %v (want the invitation, no order, no scan; the cancelled ticket is not listed)", items[0])
	}

	code, yes := f.get(path+"?entered=true", f.managerTok)
	items = asSlice(t, yes["items"])
	if code != http.StatusOK || yes["total"] != float64(2) || len(items) != 2 {
		t.Fatalf("entered: %d %v", code, yes)
	}
	var first map[string]any
	for _, it := range items {
		if it["ticket_id"] == f.ticket1.String() {
			first = it
		}
		if it["entered"] != true || it["entered_at"] == nil {
			t.Errorf("an entered ticket reads %v", it)
		}
	}
	ls, _ := first["last_scan"].(map[string]any)
	if ls["source"] != "macs" || ls["scanner_label"] != "Scanner 7" || ls["gate"] != "Gate 2" {
		t.Errorf("ticket 1 last_scan = %v (a foreign organization's scan must not show)", first["last_scan"])
	}
	if first["order_number"] != float64(f.paidSystemID) || first["buyer_name"] != "Anna Nováková" {
		t.Errorf("ticket 1 order/buyer = %v / %v", first["order_number"], first["buyer_name"])
	}

	// Paging: two at a time, in ticket-number order.
	_, p1 := f.get(path+"?limit=2", f.managerTok)
	_, p2 := f.get(path+"?limit=2&offset=2", f.managerTok)
	n1, n2 := ticketNumbers(asSlice(t, p1["items"])), ticketNumbers(asSlice(t, p2["items"]))
	if len(n1) != 2 || len(n2) != 2 || p1["has_more"] != true || p2["has_more"] != false || n1[1] >= n2[0] {
		t.Errorf("paging: %v %v has_more %v/%v", n1, n2, p1["has_more"], p2["has_more"])
	}

	// ── refusals ──────────────────────────────────────────────────────────
	if code, out := f.get(path+"?entered=maybe", f.managerTok); code != http.StatusBadRequest {
		t.Errorf("entered=maybe: %d %v", code, out)
	}
	// Organization B's manager, session A under B's path: the route's own 404.
	pathB := "/v1/organizations/" + f.orgB.String() + "/sessions/" + f.sessionA.String() + "/tickets"
	if code, out := f.get(pathB, f.managerTok); code != http.StatusNotFound {
		t.Errorf("org B read org A's session tickets: %d %v", code, out)
	}
	var managerID uuid.UUID
	if err := f.pool.QueryRow(ctx, `SELECT user_id FROM memberships WHERE org_id = $1 AND role = 'organizer' LIMIT 1`, f.orgA).Scan(&managerID); err != nil {
		t.Fatalf("find the manager: %v", err)
	}
	_, rawKey, err := apikeys.Issue(ctx, apikeys.NewStoreFromQueries(gen.New(f.pool)), apikeys.IssueInput{
		OrgID: f.orgA, Name: "m2-" + run, Scopes: []string{"order.read"}, CreatedBy: managerID,
	})
	if err != nil {
		t.Fatalf("apikeys.Issue: %v", err)
	}
	resp := integDoRequest(t, f.ts.Client(), http.MethodGet, f.ts.URL+path, rawKey, "")
	raw := integReadBody(t, resp)
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(raw, "orders.session_tickets_api_key_forbidden") ||
		strings.Contains(raw, "guest-"+run) {
		t.Errorf("API key read: %d %s", resp.StatusCode, raw)
	}

	// ── the sales CSV: first entry's time, source, scanner and gate ───────
	resp = integDoRequest(t, f.ts.Client(), http.MethodGet,
		f.ts.URL+"/v1/organizations/"+f.orgA.String()+"/sessions/"+f.sessionA.String()+"/sales.csv", f.managerTok, "")
	csv := integReadBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("sales.csv: %d %s", resp.StatusCode, csv)
	}
	madrid, err := time.LoadLocation("Europe/Madrid")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(strings.TrimPrefix(csv, "\uFEFF"), "\r\n"), "\r\n")
	if !strings.Contains(lines[0], `"Entered";"Entry time";"Entry source";"Scanner / gate";"Channel"`) {
		t.Fatalf("csv header = %s", lines[0])
	}
	var row1 string
	for _, l := range lines[1:] {
		if strings.Contains(l, `"Parterre"`) && strings.Contains(l, "macs") {
			row1 = l
		}
	}
	want := `"yes";"` + base.Add(30*time.Minute).In(madrid).Format("2006-01-02 15:04") + `";"macs";"Scanner 7 / Gate 2"`
	if !strings.Contains(row1, want) {
		t.Errorf("ticket 1 csv row = %s, want it to contain %s", row1, want)
	}
}
