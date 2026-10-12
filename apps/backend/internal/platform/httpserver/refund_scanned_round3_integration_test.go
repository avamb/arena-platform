//go:build integration

// refund_scanned_round3_integration_test.go - review round 2 of the
// scanned-ticket refund block: an admitted scan event counts, two first
// attempts at once notify once, a whole-payment refund is stopped at the
// claim, the ticket-cancel route refuses money for a scanned ticket before it
// cancels anything, and the "first scanned ticket" is always the same one.
package httpserver

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/refunds"
)

type scanEnv struct {
	srv   *Server
	pf    *prom0113Fixture
	org   uuid.UUID
	key   string
	f     *pay03Fixture
	calls *atomic.Int32
}

func newScanEnv(t *testing.T, scopes ...string) *scanEnv {
	t.Helper()
	stub := newStubStripe(t)
	srv, _ := productionIntegrationServerCfg(t, func(c *config.Config) { c.StripeAPIBaseURL = stub.baseURL() })
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	pf := &prom0113Fixture{ts: ts, client: ts.Client(), q: gen.New(srv.pgxPool)}
	ctx := context.Background()
	user, err := pf.q.InsertUser(ctx, "scan3-"+uuid.NewString()+"@example.test", "x", "en")
	if err != nil {
		t.Fatal(err)
	}
	pf.userID = user.ID
	org := pf.org(t, "SCAN3")
	if len(scopes) == 0 {
		scopes = []string{"refund.read", "refund.create", "refund.approve"}
	}
	key := pf.key(t, org, scopes...)
	stub.sessionPI = "pi_scan3_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
	calls := &atomic.Int32{}
	stub.refundHandler = func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":"re_%s","status":"succeeded"}`, uuid.NewString()[:8])
	}
	f := newPay03Order(t, srv.pgxPool, org, "stripe")
	t.Cleanup(func() {
		c := context.Background()
		_, _ = srv.pgxPool.Exec(c, `DELETE FROM scan_events WHERE ticket_id = ANY($1)`, []uuid.UUID{f.tickets[0], f.tickets[1]})
		_, _ = srv.pgxPool.Exec(c, `DELETE FROM refund_scan_blocks WHERE org_id = $1`, org)
		_, _ = srv.pgxPool.Exec(c, `DELETE FROM worker_jobs WHERE job_type = $1 AND payload->>'org_id' = $2`, refunds.JobTypeScannedAlertEmail, org.String())
		_, _ = srv.pgxPool.Exec(c, `DELETE FROM outbox_events WHERE payload->>'org_id' = $1`, org.String())
	})
	return &scanEnv{srv: srv, pf: pf, org: org, key: key, f: f, calls: calls}
}

func (e *scanEnv) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := e.srv.pgxPool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

func (e *scanEnv) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := e.srv.pgxPool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// scanEvent records an admitted gate scan of ticket i, as the scanner of org
// writes it (used_at is left unset, as when its stamping failed).
func (e *scanEnv) scanEvent(t *testing.T, org uuid.UUID, i int) {
	t.Helper()
	e.exec(t, `INSERT INTO scan_events (org_id, session_id, ticket_id, credential_code, scanned_at, result)
		VALUES ($1, $2, $3, $4, now(), 'admitted')`, org, e.f.session, e.f.tickets[i], "scan3-"+uuid.NewString())
}

func (e *scanEnv) createRefund(t *testing.T, i int) (int, map[string]any) {
	t.Helper()
	body := fmt.Sprintf(`{"items":[{"ticket_id":%q}],"reason":"scan3"}`, e.f.tickets[i])
	return refundOrgPost(t, e.pf, "/v1/organizations/"+e.org.String()+"/orders/"+e.f.order.String()+"/refunds", e.key, "scan3-"+uuid.NewString(), body)
}

// Item 9: an admitted scan event of the ticket's own organization blocks the
// refund even when used_at was never stamped; another organization's scan
// event does not.
func TestRefundScanned_AdmittedScanEventCounts(t *testing.T) {
	e := newScanEnv(t)
	other := e.pf.org(t, "SCAN3-OTHER")
	e.scanEvent(t, other, 1)
	e.scanEvent(t, e.org, 0)
	if st, out := e.createRefund(t, 0); st != http.StatusConflict || prom0113Code(out) != refunds.CodeTicketScanned {
		t.Fatalf("refund of a ticket with an admitted scan event: %d %v; want 409 %s", st, out, refunds.CodeTicketScanned)
	}
	if st, out := e.createRefund(t, 1); st != http.StatusAccepted {
		t.Fatalf("another organization's scan event blocked the refund: %d %v", st, out)
	}
}

// Item 3: ten first attempts at once journal ten attempts and notify ONCE.
func TestRefundScanned_ConcurrentFirstAttemptsNotifyOnce(t *testing.T) {
	e := newScanEnv(t)
	e.exec(t, `UPDATE tickets SET used_at = now() WHERE id = $1`, e.f.tickets[0])
	engine := e.srv.refundEngine()
	actor := refunds.Actor{Type: "user", ID: uuid.NewString()}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			engine.RecordScannedBlock(context.Background(), e.org, e.f.tickets[0], actor, "test")
		}()
	}
	wg.Wait()
	if n := e.count(t, `SELECT count(*) FROM worker_jobs WHERE job_type = $1 AND payload->>'org_id' = $2`, refunds.JobTypeScannedAlertEmail, e.org.String()); n != 1 {
		t.Fatalf("owner e-mail jobs = %d; want 1", n)
	}
	if n := e.count(t, `SELECT attempts FROM refund_scan_blocks WHERE ticket_id = $1`, e.f.tickets[0]); n != 10 {
		t.Fatalf("attempts = %d; want 10", n)
	}
}

// Item 5: a refund of the whole payment (no ticket of its own), queued before
// a ticket of that payment was scanned, is stopped at the claim.
func TestRefundScanned_WholePaymentRefundStoppedAtClaim(t *testing.T) {
	e := newScanEnv(t)
	e.exec(t, `UPDATE tickets SET used_at = now() WHERE id = $1`, e.f.tickets[1])
	var id uuid.UUID
	if err := e.srv.pgxPool.QueryRow(context.Background(), `INSERT INTO refunds (payment_intent_id, org_id, amount, currency, reason,
		state, settlement, provider, origin, cancel_ticket)
		VALUES ($1, $2, 5000, 'EUR', 'whole payment', 'provider_pending', 'provider', 'stripe', 'arena', false) RETURNING id`,
		e.f.pay, e.org).Scan(&id); err != nil {
		t.Fatal(err)
	}
	got, err := e.srv.refundEngine().Drive(context.Background(), id)
	if err != nil {
		t.Fatalf("drive: %v", err)
	}
	if got.State != "failed" || got.FailureCode == nil || *got.FailureCode != "ticket_scanned" || e.calls.Load() != 0 {
		t.Fatalf("whole-payment refund = %s/%v with %d provider call(s); want failed/ticket_scanned and none", got.State, got.FailureCode, e.calls.Load())
	}
}

// Item 4: the ticket-cancel route refuses money for a scanned ticket BEFORE
// it cancels anything, and journals the attempt; without money it cancels.
func TestRefundScanned_TicketCancelRouteRefusesMoneyFirst(t *testing.T) {
	e := newScanEnv(t, "ticket.cancel", "refund.read")
	e.exec(t, `UPDATE tickets SET used_at = now() WHERE id = ANY($1)`, []uuid.UUID{e.f.tickets[0], e.f.tickets[1]})
	for _, mode := range []string{"manual", "automatic"} {
		st, out := e.pf.do(t, http.MethodPost, "/v1/tickets/"+e.f.tickets[0].String()+"/cancel", e.key,
			fmt.Sprintf(`{"reason":"scan3","refund_mode":%q}`, mode))
		if st != http.StatusConflict || prom0113Code(out) != refunds.CodeTicketScanned {
			t.Fatalf("cancel with refund_mode %s: %d %v; want 409 %s", mode, st, out, refunds.CodeTicketScanned)
		}
	}
	if st, _ := e.f.ticket(t, 0); st != "active" {
		t.Fatalf("the ticket is %q after a refused cancellation; want active", st)
	}
	if n := e.count(t, `SELECT count(*) FROM refunds WHERE payment_intent_id = $1`, e.f.pay); n != 0 {
		t.Fatalf("a refused cancellation wrote %d refund(s)", n)
	}
	if n := e.count(t, `SELECT attempts FROM refund_scan_blocks WHERE ticket_id = $1`, e.f.tickets[0]); n != 2 {
		t.Fatalf("journaled attempts = %d; want 2", n)
	}
	if st, out := e.pf.do(t, http.MethodPost, "/v1/tickets/"+e.f.tickets[1].String()+"/cancel", e.key,
		`{"reason":"scan3","refund_mode":"none"}`); st != http.StatusOK {
		t.Fatalf("cancel without money: %d %v; want 200", st, out)
	}
}

// Item 7: the scanned ticket a whole-payment check names is always the same
// one (the lowest ticket number), so the once-a-day notice is keyed stably.
// A guard: without the ORDER BY the pick depends on the plan.
func TestRefundScanned_FirstScannedTicketIsStable(t *testing.T) {
	e := newScanEnv(t)
	e.exec(t, `UPDATE tickets SET used_at = now() WHERE id = ANY($1)`, []uuid.UUID{e.f.tickets[0], e.f.tickets[1]})
	var want uuid.UUID
	if err := e.srv.pgxPool.QueryRow(context.Background(), `SELECT id FROM tickets WHERE id = ANY($1)
		ORDER BY system_ticket_id NULLS LAST, id LIMIT 1`, []uuid.UUID{e.f.tickets[0], e.f.tickets[1]}).Scan(&want); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		got, found, err := e.srv.refundEngine().ScannedTicketOfPayment(context.Background(), e.f.pay)
		if err != nil || !found || got != want {
			t.Fatalf("pick %d = %s (%v, %v); want %s", i, got, found, err, want)
		}
	}
}
