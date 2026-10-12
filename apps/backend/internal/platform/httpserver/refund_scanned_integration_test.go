//go:build integration

// refund_scanned_integration_test.go - a ticket that was already scanned at
// the gate is never refunded (owner decision 2026-10-12): refused on the
// organizer API, the flat routes and in the engine's claim, with the options
// saying why, a journal row, ONE owner e-mail and ONE sales-bot event per
// ticket and actor per day, and the money and the ticket left untouched.
package httpserver

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/email"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/delivery"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/refunds"
)

type scannedMailbox struct {
	mu   sync.Mutex
	msgs []email.Message
}

func (m *scannedMailbox) Send(_ context.Context, msg email.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.msgs = append(m.msgs, msg)
	return nil
}

func TestRefundScanned_HardBlockEverywhere(t *testing.T) {
	stub := newStubStripe(t)
	srv, _ := productionIntegrationServerCfg(t, func(c *config.Config) { c.StripeAPIBaseURL = stub.baseURL() })
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	pf := &prom0113Fixture{ts: ts, client: ts.Client(), q: gen.New(srv.pgxPool)}
	ctx := context.Background()
	ownerEmail := "scan-" + uuid.NewString() + "@example.test"
	user, err := pf.q.InsertUser(ctx, ownerEmail, "x", "ru")
	if err != nil {
		t.Fatal(err)
	}
	pf.userID = user.ID
	org := pf.org(t, "SCANNED")
	if _, err := srv.pgxPool.Exec(ctx, `INSERT INTO memberships (user_id, org_id, role, status) VALUES ($1, $2, 'org_admin', 'active')`, user.ID, org); err != nil {
		t.Fatal(err)
	}
	keyA := pf.key(t, org, "refund.read", "refund.create", "refund.approve")
	keyB := pf.key(t, org, "refund.read", "refund.create", "refund.approve")
	stub.sessionPI = "pi_scan_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
	var providerCalls int
	var callMu sync.Mutex
	stub.refundHandler = func(w http.ResponseWriter, r *http.Request) {
		callMu.Lock()
		providerCalls++
		callMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"re_scan","status":"succeeded"}`)
	}
	f := newPay03Order(t, srv.pgxPool, org, "stripe")
	base := "/v1/organizations/" + org.String()
	t.Cleanup(func() {
		c := context.Background()
		_, _ = srv.pgxPool.Exec(c, `DELETE FROM refund_scan_blocks WHERE org_id = $1`, org)
		_, _ = srv.pgxPool.Exec(c, `DELETE FROM worker_jobs WHERE job_type = $1 AND payload->>'org_id' = $2`, refunds.JobTypeScannedAlertEmail, org.String())
		_, _ = srv.pgxPool.Exec(c, `DELETE FROM outbox_events WHERE event_type = $1 AND payload->>'org_id' = $2`, refunds.EventRefundBlockedScanned, org.String())
		_, _ = srv.pgxPool.Exec(c, `DELETE FROM barcodes WHERE ticket_id = ANY($1)`, []uuid.UUID{f.tickets[0], f.tickets[1]})
		_, _ = srv.pgxPool.Exec(c, `DELETE FROM memberships WHERE org_id = $1`, org)
	})

	// ticket 0 is used (used_at), ticket 1 only has a scanned LEGACY barcode
	if _, err := srv.pgxPool.Exec(ctx, `UPDATE tickets SET used_at = now() WHERE id = $1`, f.tickets[0]); err != nil {
		t.Fatal(err)
	}
	var authority uuid.UUID
	if err := srv.pgxPool.QueryRow(ctx, `SELECT id FROM barcode_authorities WHERE type = 'legacy_bil24' LIMIT 1`).Scan(&authority); err != nil {
		if err := srv.pgxPool.QueryRow(ctx, `INSERT INTO barcode_authorities (type, label) VALUES ('legacy_bil24', 'legacy test') RETURNING id`).Scan(&authority); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := srv.pgxPool.Exec(ctx, `INSERT INTO barcodes (authority_id, external_ref, ticket_id, status, scanned_at)
		VALUES ($1, $2, $3, 'scanned', now())`, authority, "legacy-"+uuid.NewString(), f.tickets[1]); err != nil {
		t.Fatal(err)
	}

	count := func(sql string, args ...any) int {
		var n int
		if err := srv.pgxPool.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	jobs := func() int {
		return count(`SELECT count(*) FROM worker_jobs WHERE job_type = $1 AND payload->>'org_id' = $2`, refunds.JobTypeScannedAlertEmail, org.String())
	}
	events := func() int {
		return count(`SELECT count(*) FROM outbox_events WHERE event_type = $1 AND payload->>'org_id' = $2`, refunds.EventRefundBlockedScanned, org.String())
	}

	// ── the options say why, for both kinds of scan
	st, opts := pf.do(t, http.MethodGet, base+"/orders/"+f.order.String()+"/refund-options", keyA, "")
	if st != http.StatusOK {
		t.Fatalf("options: %d %v", st, opts)
	}
	for _, tk := range opts["tickets"].([]any) {
		m := tk.(map[string]any)
		if m["not_refundable_reason"] != "ticket_scanned" || m["refundable"] != float64(0) {
			t.Fatalf("a scanned ticket in the options: %v; want refundable 0 and reason ticket_scanned", m)
		}
	}

	// ── create is refused with its own code; nothing is written, nothing is sent
	body := func(i int) string {
		return fmt.Sprintf(`{"items":[{"ticket_id":%q}],"reason":"scan test"}`, f.tickets[i])
	}
	for attempt, key := range []string{keyA, keyA, keyB} {
		st, out := refundOrgPost(t, pf, base+"/orders/"+f.order.String()+"/refunds", key, "scan-"+uuid.NewString(), body(0))
		if st != http.StatusConflict || prom0113Code(out) != "refund.ticket_scanned" {
			t.Fatalf("attempt %d: %d %v; want 409 refund.ticket_scanned", attempt, st, out)
		}
	}
	if n := count(`SELECT count(*) FROM refunds WHERE order_id = $1`, f.order); n != 0 {
		t.Fatalf("a refused refund left %d row(s)", n)
	}
	callMu.Lock()
	if providerCalls != 0 {
		t.Fatalf("the provider was called %d time(s)", providerCalls)
	}
	callMu.Unlock()
	st, out := refundOrgPost(t, pf, base+"/orders/"+f.order.String()+"/refunds", keyA, "scan-"+uuid.NewString(), body(1))
	if st != http.StatusConflict || prom0113Code(out) != "refund.ticket_scanned" {
		t.Fatalf("legacy-barcode ticket: %d %v; want 409 refund.ticket_scanned", st, out)
	}

	// ── antispam: one notice per ticket and actor, the attempts are counted
	var attempts int
	if err := srv.pgxPool.QueryRow(ctx, `SELECT attempts FROM refund_scan_blocks WHERE ticket_id = $1 AND actor_key LIKE 'api_key:%'
		ORDER BY attempts DESC LIMIT 1`, f.tickets[0]).Scan(&attempts); err != nil || attempts != 2 {
		t.Fatalf("attempts of the first actor on ticket 0 = %d (%v); want 2", attempts, err)
	}
	// ticket 0: actors A and B; ticket 1: actor A -> three notices, not four attempts' worth
	if jobs() != 3 || events() != 3 {
		t.Fatalf("e-mail jobs = %d, sales-bot events = %d; want 3 and 3 (one per ticket and actor)", jobs(), events())
	}
	if n := count(`SELECT count(*) FROM audit_events WHERE action = 'v1.refund.blocked_scanned' AND resource_id = ANY($1)`,
		[]string{f.tickets[0].String(), f.tickets[1].String()}); n < 4 {
		t.Fatalf("journal rows = %d; every attempt must be journaled (4)", n)
	}

	// ── the owners' e-mail, in the owner's language
	box := &scannedMailbox{}
	handler := delivery.NewScannedAlertHandler(delivery.ScannedAlertOptions{Sender: box, DB: srv.pgxPool})
	var payload []byte
	if err := srv.pgxPool.QueryRow(ctx, `SELECT payload::text FROM worker_jobs WHERE job_type = $1 AND payload->>'org_id' = $2
		AND payload->>'ticket_id' = $3 LIMIT 1`, refunds.JobTypeScannedAlertEmail, org.String(), f.tickets[0].String()).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if err := handler(ctx, payload); err != nil {
		t.Fatalf("alert handler: %v", err)
	}
	if len(box.msgs) != 1 || box.msgs[0].To != ownerEmail || !strings.Contains(box.msgs[0].Subject, "Возврат заблокирован") {
		t.Fatalf("owner mail = %+v; want one Russian notice to %s", box.msgs, ownerEmail)
	}

	// ── the flat routes refuse it too
	st, out = pf.do(t, http.MethodPost, "/v1/refunds", keyA, fmt.Sprintf(`{"payment_intent_id":%q,"amount":5000,"currency":"EUR"}`, f.pay))
	if st != http.StatusConflict || prom0113Code(out) != "refund.ticket_scanned" {
		t.Fatalf("flat create: %d %v; want 409 refund.ticket_scanned", st, out)
	}

	// ── a refund asked for before the scan is stopped at the claim
	var queued uuid.UUID
	if err := srv.pgxPool.QueryRow(ctx, `INSERT INTO refunds (payment_intent_id, org_id, order_id, ticket_id, amount, currency, reason,
		state, settlement, provider, origin, cancel_ticket)
		VALUES ($1, $2, $3, $4, 2500, 'EUR', 'queued before the scan', 'provider_pending', 'provider', 'stripe', 'arena', true)
		RETURNING id`, f.pay, org, f.order, f.tickets[0]).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	got, err := srv.refundEngine().Drive(ctx, queued)
	if err != nil {
		t.Fatalf("drive: %v", err)
	}
	if got.State != "failed" || got.FailureCode == nil || *got.FailureCode != "ticket_scanned" {
		t.Fatalf("the queued refund is %s/%v; want failed/ticket_scanned", got.State, got.FailureCode)
	}
	callMu.Lock()
	if providerCalls != 0 {
		t.Fatalf("the claim let the provider be called %d time(s)", providerCalls)
	}
	callMu.Unlock()
	if st, _ := f.ticket(t, 0); st != "active" {
		t.Fatalf("the ticket is %q; a blocked refund must leave it untouched", st)
	}
}
