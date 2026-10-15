//go:build integration

// macs_scan_events_integration_test.go — task M1: MACS posts its door scans
// back to arena through POST /v1/macs/scan-events, signed with the secret of
// the organization's MACS subscriber. Real router, real database, real
// PGEventsWriter: the scans land in scan_events with source='macs', the
// first admission stamps tickets.used_at, every accepted scan has its
// v1.ticket.scanned outbox row, a replay writes nothing, a foreign ticket is
// never admitted, the session summary counts the admissions by source, and a
// MACS-admitted ticket can no longer be refunded.
package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/barcodes/ean13"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hmacsscan"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/refunds"
)

type macsScanEnv struct {
	*scanEnv
	subID  uuid.UUID
	secret string
}

func newMACSScanEnv(t *testing.T) *macsScanEnv {
	t.Helper()
	e := newScanEnv(t)
	ctx := context.Background()
	secret := "whsec_m1_" + uuid.NewString()
	var subID uuid.UUID
	if err := e.srv.pgxPool.QueryRow(ctx, `INSERT INTO webhook_subscribers (site_url, callback_url, signing_secret, event_types, active, kind, org_id)
		VALUES ('', 'https://macs.example.test/api/_wh/tickets', $1, '{}', true, 'macs', $2) RETURNING id`, secret, e.org).Scan(&subID); err != nil {
		t.Fatalf("subscriber: %v", err)
	}
	// Registered after newScanEnv, so it runs first: the scans reference the
	// subscriber, which must outlive them.
	t.Cleanup(func() {
		c := context.Background()
		_, _ = e.srv.pgxPool.Exec(c, `DELETE FROM scan_events WHERE subscriber_id = $1`, subID)
		_, _ = e.srv.pgxPool.Exec(c, `DELETE FROM webhook_subscribers WHERE id = $1`, subID)
	})
	return &macsScanEnv{scanEnv: e, subID: subID, secret: secret}
}

func (m *macsScanEnv) post(t *testing.T, sig string, body []byte) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, m.pf.ts.URL+"/v1/macs/scan-events", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(hmacsscan.HeaderSubscriberID, m.subID.String())
	req.Header.Set(hmacsscan.HeaderSignature, sig)
	resp, err := m.pf.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func (m *macsScanEnv) signedPost(t *testing.T, scans []map[string]any) (int, map[string]any, []byte, string) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"scans": scans})
	if err != nil {
		t.Fatal(err)
	}
	sig := hmacsscan.SignatureHeader(m.secret, body)
	st, out := m.post(t, sig, body)
	return st, out, body, sig
}

func (m *macsScanEnv) systemID(t *testing.T, ticket uuid.UUID) int64 {
	t.Helper()
	var id int64
	if err := m.srv.pgxPool.QueryRow(context.Background(), `SELECT system_ticket_id FROM tickets WHERE id = $1`, ticket).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// storedBarcode gives ticket a platform barcode, as issuance does.
func (m *macsScanEnv) storedBarcode(t *testing.T, ticket uuid.UUID) string {
	t.Helper()
	code, err := ean13.Random()
	if err != nil {
		t.Fatal(err)
	}
	m.exec(t, `INSERT INTO barcodes (authority_id, external_ref, ticket_id)
		VALUES ((SELECT id FROM barcode_authorities WHERE type = 'platform' ORDER BY created_at LIMIT 1), $1, $2)`, code, ticket)
	t.Cleanup(func() {
		_, _ = m.srv.pgxPool.Exec(context.Background(), `DELETE FROM barcodes WHERE external_ref = $1`, code)
	})
	return code
}

func resultsOf(t *testing.T, out map[string]any) []map[string]any {
	t.Helper()
	raw, _ := out["results"].([]any)
	res := make([]map[string]any, len(raw))
	for i, r := range raw {
		res[i], _ = r.(map[string]any)
	}
	return res
}

func TestMACSScanEvents_AdmitsReplaysAndBlocksRefunds(t *testing.T) {
	m := newMACSScanEnv(t)
	ctx := context.Background()
	t0, t1 := m.f.tickets[0], m.f.tickets[1]
	id0 := m.systemID(t, t0)
	code1 := m.storedBarcode(t, t1)
	base := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	at := func(min int) string { return base.Add(time.Duration(min) * time.Minute).Format(time.RFC3339) }
	run := uuid.NewString()[:8]
	sid := func(s string) string { return "m1-" + run + "-" + s }

	scans := []map[string]any{
		// Sent first but later in time: the earlier A3 must stamp used_at.
		{"scan_id": sid("a1"), "system_ticket_id": id0, "result": "admitted", "scanned_at": at(10), "scanner_id": "dev-2", "scanner_name": "Scanner 2", "gate": "Main"},
		{"scan_id": sid("a2"), "barcode": code1, "result": "admitted", "scanned_at": at(0), "scanner_id": "dev-1", "scanner_name": "Scanner 1", "gate": "Side"},
		{"scan_id": sid("a3"), "system_ticket_id": id0, "result": "admitted", "scanned_at": at(5), "scanner_id": "dev-2", "scanner_name": "Scanner 2", "gate": "Main"},
		{"scan_id": sid("a4"), "barcode": code1, "result": "duplicate", "scanned_at": at(1), "scanner_id": "dev-1", "scanner_name": "Scanner 1", "gate": "Side"},
		// The same scan id twice in one batch, and the same physical scan
		// (barcode and second) under another id: both duplicates.
		{"scan_id": sid("a2"), "barcode": code1, "result": "admitted", "scanned_at": at(0)},
		{"scan_id": sid("a6"), "barcode": code1, "result": "admitted", "scanned_at": at(0)},
		// A barcode and an id of different tickets.
		{"scan_id": sid("a7"), "system_ticket_id": id0, "barcode": code1, "result": "admitted", "scanned_at": at(2)},
	}
	st, out, body, sig := m.signedPost(t, scans)
	if st != http.StatusAccepted {
		t.Fatalf("batch: %d %v", st, out)
	}
	res := resultsOf(t, out)
	want := []struct {
		status string
		ticket uuid.UUID
		first  bool
		code   string
	}{
		{hmacsscan.StatusAccepted, t0, false, ""},
		{hmacsscan.StatusAccepted, t1, true, ""},
		{hmacsscan.StatusAccepted, t0, true, ""},
		{hmacsscan.StatusAccepted, t1, false, ""},
		{hmacsscan.StatusDuplicate, t1, false, ""},
		{hmacsscan.StatusDuplicate, t1, false, ""},
		{hmacsscan.StatusRejected, uuid.Nil, false, hmacsscan.CodeTicketMismatch},
	}
	if len(res) != len(want) {
		t.Fatalf("results: %v", out)
	}
	for i, w := range want {
		r := res[i]
		if r["status"] != w.status || r["first_admission"] != w.first || (w.code != "" && r["error_code"] != w.code) {
			t.Errorf("scan %d: %v, want %+v", i, r, w)
		}
		if w.ticket != uuid.Nil && r["ticket_id"] != w.ticket.String() {
			t.Errorf("scan %d ticket: %v, want %s", i, r["ticket_id"], w.ticket)
		}
	}
	if out["accepted"].(float64) != 4 || out["duplicates"].(float64) != 2 || out["rejected"].(float64) != 1 {
		t.Fatalf("counters: %v", out)
	}

	// used_at is the earliest admission, set once.
	for _, c := range []struct {
		ticket uuid.UUID
		want   string
	}{{t0, at(5)}, {t1, at(0)}} {
		var used time.Time
		if err := m.srv.pgxPool.QueryRow(ctx, `SELECT used_at FROM tickets WHERE id = $1`, c.ticket).Scan(&used); err != nil {
			t.Fatal(err)
		}
		if used.UTC().Format(time.RFC3339) != c.want {
			t.Errorf("used_at of %s: %s, want %s", c.ticket, used.UTC().Format(time.RFC3339), c.want)
		}
	}

	rows := m.count(t, `SELECT count(*) FROM scan_events WHERE subscriber_id = $1 AND source = 'macs' AND org_id = $2`, m.subID, m.org)
	if rows != 4 {
		t.Fatalf("scan_events rows: %d, want 4", rows)
	}
	if n := m.count(t, `SELECT count(*) FROM scan_events WHERE subscriber_id = $1 AND result = 'denied' AND external_scan_id = $2`, m.subID, sid("a4")); n != 1 {
		t.Fatalf("a MACS duplicate verdict is stored as denied: %d", n)
	}
	if n := m.count(t, `SELECT count(*) FROM scan_events WHERE external_scan_id = $1 AND scanner_label = 'Scanner 2' AND device_id = 'dev-2' AND gate = 'Main'`, sid("a3")); n != 1 {
		t.Fatalf("scanner fields of a3 not stored")
	}
	aggs := []string{t0.String(), t1.String()}
	outboxRows := func() int {
		return m.count(t, `SELECT count(*) FROM outbox_events WHERE event_type = 'v1.ticket.scanned' AND aggregate_type = 'ticket'
			AND aggregate_id = ANY($1::text[]) AND payload->>'source' = 'macs'`, aggs)
	}
	if n := outboxRows(); n != 4 {
		t.Fatalf("v1.ticket.scanned rows: %d, want 4", n)
	}
	auditRows := func() int {
		return m.count(t, `SELECT count(*) FROM audit_events WHERE actor_type = 'macs_subscriber' AND actor_id = $1 AND action = 'v1.ticket.admitted'`, m.subID)
	}
	if n := auditRows(); n != 2 {
		t.Fatalf("audit rows of first admissions: %d, want 2", n)
	}

	// The same batch again: 202, every scan a duplicate, nothing written.
	st, out = m.post(t, sig, body)
	if st != http.StatusAccepted || out["accepted"].(float64) != 0 || out["duplicates"].(float64) != 6 {
		t.Fatalf("replay: %d %v", st, out)
	}
	if m.count(t, `SELECT count(*) FROM scan_events WHERE subscriber_id = $1`, m.subID) != 4 || outboxRows() != 4 || auditRows() != 2 {
		t.Fatal("a replayed batch wrote rows")
	}

	// A wrong signature is 401 and writes nothing.
	if st, out := m.post(t, hmacsscan.SignatureHeader("not-the-secret", body), body); st != http.StatusUnauthorized || prom0113Code(out) != "macs.invalid_signature" {
		t.Fatalf("bad signature: %d %v", st, out)
	}

	// The session summary counts the MACS admissions.
	readKey := m.pf.key(t, m.org, "order.read")
	st, sum := m.pf.do(t, http.MethodGet, fmt.Sprintf("/v1/organizations/%s/sessions/%s/summary", m.org, m.f.session), readKey, "")
	if st != http.StatusOK {
		t.Fatalf("summary: %d %v", st, sum)
	}
	entered, _ := sum["entered"].(map[string]any)
	bySource, _ := entered["by_source"].([]any)
	var macs map[string]any
	for _, s := range bySource {
		if r, _ := s.(map[string]any); r["source"] == "macs" {
			macs = r
		}
	}
	if macs == nil || macs["tickets"].(float64) != 2 || macs["scans"].(float64) != 3 || entered["used"].(float64) != 2 {
		t.Fatalf("summary entered: %v", entered)
	}

	// Decision M1: a MACS admission blocks the refund like any gate scan.
	if st, out := m.createRefund(t, 0); st != http.StatusConflict || prom0113Code(out) != refunds.CodeTicketScanned {
		t.Fatalf("refund of a MACS-admitted ticket: %d %v; want 409 %s", st, out, refunds.CodeTicketScanned)
	}
	tx, err := m.srv.pgxPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	// Clear used_at: the admitted scan_events row alone must still count.
	if _, err := tx.Exec(ctx, `UPDATE tickets SET used_at = NULL WHERE id = $1`, t1); err != nil {
		t.Fatal(err)
	}
	if scanned, err := refunds.TicketScannedTx(ctx, tx, t1); err != nil || !scanned {
		t.Fatalf("TicketScannedTx on a MACS admission without used_at: %v %v", scanned, err)
	}
}

// A subscriber admits only tickets of its own organization: another
// organization's ticket, by id or by barcode, is rejected per scan as not
// found, gets no scan row and keeps its used_at empty.
func TestMACSScanEvents_ForeignTicketIsRejectedPerScan(t *testing.T) {
	m := newMACSScanEnv(t)
	other := m.pf.org(t, "M1-OTHER")
	foreign := newPay03Order(t, m.srv.pgxPool, other, "stripe")
	t.Cleanup(func() {
		_, _ = m.srv.pgxPool.Exec(context.Background(), `DELETE FROM scan_events WHERE ticket_id = ANY($1)`, []uuid.UUID{foreign.tickets[0], foreign.tickets[1]})
	})
	fid := m.systemID(t, foreign.tickets[0])
	legacy := ean13.PlatformCode(m.systemID(t, foreign.tickets[1]))
	own := m.systemID(t, m.f.tickets[0])
	run := uuid.NewString()[:8]
	at := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	st, out, _, _ := m.signedPost(t, []map[string]any{
		{"scan_id": "m1f-" + run + "-1", "system_ticket_id": fid, "result": "admitted", "scanned_at": at},
		{"scan_id": "m1f-" + run + "-2", "barcode": legacy, "result": "admitted", "scanned_at": at},
		{"scan_id": "m1f-" + run + "-3", "system_ticket_id": own, "result": "admitted", "scanned_at": at},
		{"scan_id": "m1f-" + run + "-4", "system_ticket_id": int64(9_000_000_000_000), "result": "admitted", "scanned_at": at},
	})
	if st != http.StatusAccepted {
		t.Fatalf("batch: %d %v", st, out)
	}
	res := resultsOf(t, out)
	for i, want := range []string{hmacsscan.CodeTicketNotFound, hmacsscan.CodeTicketNotFound, "", hmacsscan.CodeTicketNotFound} {
		if want == "" {
			if res[i]["status"] != hmacsscan.StatusAccepted {
				t.Errorf("own ticket: %v", res[i])
			}
			continue
		}
		if res[i]["status"] != hmacsscan.StatusRejected || res[i]["error_code"] != want || res[i]["ticket_id"] != nil {
			t.Errorf("scan %d: %v, want rejected %s", i, res[i], want)
		}
	}
	if n := m.count(t, `SELECT count(*) FROM scan_events WHERE ticket_id = ANY($1)`, []uuid.UUID{foreign.tickets[0], foreign.tickets[1]}); n != 0 {
		t.Fatalf("foreign ticket got %d scan rows", n)
	}
	if n := m.count(t, `SELECT count(*) FROM tickets WHERE id = ANY($1) AND used_at IS NOT NULL`, []uuid.UUID{foreign.tickets[0], foreign.tickets[1]}); n != 0 {
		t.Fatalf("foreign ticket stamped used: %d", n)
	}
}
