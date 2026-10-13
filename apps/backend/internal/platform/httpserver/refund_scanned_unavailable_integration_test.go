//go:build integration

// refund_scanned_unavailable_integration_test.go - the scan check answers 503
// when it cannot be made, and nothing is written.
package httpserver

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/htickets"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/refunds"
)

// brokenRefundEngine answers every scan check with an error.
func brokenRefundEngine(t *testing.T) *refunds.Engine {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	pool.Close()
	return refunds.New(refunds.Options{DB: pool})
}

func allowEveryRow(http.ResponseWriter, *http.Request, uuid.UUID, bool, string, string) bool {
	return true
}

// T1: the flat create and the approval answer 503 when the scan check cannot
// be made (no engine, or a check that fails), and write nothing.
func TestRefundScanned_FlatRoutesAnswer503WhenTheScanCheckFails(t *testing.T) {
	e := newScanEnv(t)
	ctx := auth.WithActor(context.Background(), auth.Actor{ID: uuid.NewString()})
	for name, engine := range map[string]*refunds.Engine{"no engine": nil, "failing check": brokenRefundEngine(t)} {
		h := e.srv.checkoutHandler().WithRowOrgAccess(hcheckout.RowOrgAccess(allowEveryRow)).WithRefundEngine(engine)
		router := chi.NewRouter()
		router.Post("/v1/refunds", h.HandleCreateRefund)
		router.Post("/v1/refunds/{id}/approve", h.HandleApproveRefund)
		body := fmt.Sprintf(`{"payment_intent_id":%q,"amount":5000,"currency":"EUR"}`, e.f.pay)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/refunds", strings.NewReader(body)).WithContext(ctx))
		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("%s: flat create %d %s; want 503", name, w.Code, w.Body.String())
		}
		if n := e.count(t, `SELECT count(*) FROM refunds WHERE payment_intent_id = $1`, e.f.pay); n != 0 {
			t.Fatalf("%s: the flat create wrote %d refund(s)", name, n)
		}
		var id uuid.UUID
		if err := e.srv.pgxPool.QueryRow(context.Background(), `INSERT INTO refunds (payment_intent_id, org_id, amount, currency, reason)
			VALUES ($1, $2, 5000, 'EUR', 't1') RETURNING id`, e.f.pay, e.org).Scan(&id); err != nil {
			t.Fatal(err)
		}
		w = httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/refunds/"+id.String()+"/approve", nil).WithContext(ctx))
		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("%s: approve %d %s; want 503", name, w.Code, w.Body.String())
		}
		var state string
		if err := e.srv.pgxPool.QueryRow(context.Background(), `SELECT state FROM refunds WHERE id = $1`, id).Scan(&state); err != nil || state != "requested" {
			t.Errorf("%s: refund is %q (%v) after a refused approval; want requested", name, state, err)
		}
		e.exec(t, `DELETE FROM refunds WHERE id = $1`, id)
	}
}

// T1: the cancellation's own scan guard answers 503 when its check fails, and
// the ticket stays active.
func TestRefundScanned_CancelGuardAnswers503WhenTheCheckFails(t *testing.T) {
	e := newScanEnv(t, "ticket.cancel", "refund.read")
	bg := context.Background()
	// Another transaction holds the exception table; the guard's read waits
	// and its deadline ends it with an error.
	lock, err := e.srv.pgxPool.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Rollback(bg) }()
	if _, err := lock.Exec(bg, `LOCK TABLE refund_scan_exceptions IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(bg, 500*time.Millisecond)
	defer cancel()
	_, cerr := e.srv.ticketsHandler().CancelTicketTx(ctx, htickets.CancelTicketParams{
		TicketID: e.f.tickets[0], Reason: "r", RefundMode: "manual", ActorType: "user", ScanGuard: true,
	})
	if cerr == nil || cerr.Status != http.StatusServiceUnavailable || cerr.Code != refunds.CodeEngineUnavailable {
		t.Fatalf("cancel with a failing scan check: %+v; want 503 %s", cerr, refunds.CodeEngineUnavailable)
	}
	_ = lock.Rollback(bg)
	if st, _ := e.f.ticket(t, 0); st != "active" {
		t.Fatalf("ticket is %q after a refused cancellation; want active", st)
	}
}

// T1: the flat create answers 503 when the scan check INSIDE its writing
// transaction fails (the pre-check passed), and nothing is written. The
// in-transaction check is held on a lock of scan_events and then cancelled.
func TestRefundScanned_FlatCreateAnswers503WhenTheInTransactionCheckFails(t *testing.T) {
	e := newScanEnv(t)
	ctx := context.Background()
	payLock, err := e.srv.pgxPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = payLock.Rollback(ctx) }()
	if err := refunds.LockPayment(ctx, payLock, e.f.pay); err != nil {
		t.Fatal(err)
	}
	type answer struct {
		status int
		body   map[string]any
	}
	done := make(chan answer, 1)
	go func() {
		st, out := e.flatCreate(t, 1000)
		done <- answer{st, out}
	}()
	waitFor := func(what, query string) int {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			var pid int
			err := e.srv.pgxPool.QueryRow(ctx, query).Scan(&pid)
			if err == nil {
				return pid
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: %v", what, err)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	// The pre-check is done once the create waits for the payment's lock.
	waitFor("the create never waited for the payment lock",
		`SELECT pid FROM pg_locks WHERE locktype = 'advisory' AND NOT granted LIMIT 1`)
	tableLock, err := e.srv.pgxPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tableLock.Rollback(ctx) }()
	if _, err := tableLock.Exec(ctx, `LOCK TABLE scan_events IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	if err := payLock.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	pid := waitFor("the in-transaction check never reached scan_events",
		`SELECT pid FROM pg_stat_activity WHERE wait_event_type = 'Lock' AND query ILIKE '%scan_events%' AND pid <> pg_backend_pid() LIMIT 1`)
	if _, err := e.srv.pgxPool.Exec(ctx, `SELECT pg_cancel_backend($1)`, pid); err != nil {
		t.Fatal(err)
	}
	got := <-done
	_ = tableLock.Rollback(ctx)
	if got.status != http.StatusServiceUnavailable {
		t.Fatalf("flat create whose in-transaction scan check failed: %d %v; want 503", got.status, got.body)
	}
	if n := e.count(t, `SELECT count(*) FROM refunds WHERE payment_intent_id = $1`, e.f.pay); n != 0 {
		t.Fatalf("the flat create wrote %d refund(s)", n)
	}
}
