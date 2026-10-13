//go:build integration

// refund_reject_race_integration_test.go - the flat reject route rejects only
// a refund that is still requested, under the payment's refund lock.
package httpserver

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/refunds"
)

// A refund that moves on (here: to the provider) between the reject route's
// read and its write is not rejected: the route answers 409, the row keeps
// its state, and the scan exception it spent stays spent, so the scanned
// ticket cannot be refunded a second time on it.
func TestRefundReject_RowThatMovedOnIsNotRejected(t *testing.T) {
	e := newScanEnv(t)
	creator := e.pf.key(t, e.org, "refund.read", "refund.create")
	e.dropExceptionsOnCleanup(t)
	ctx := context.Background()
	e.exec(t, `UPDATE tickets SET used_at = now() - interval '1 hour' WHERE id = $1`, e.f.tickets[0])
	e.grantException(t, 0)
	st, out := e.ticketRefundAs(t, creator, 0)
	if st != http.StatusAccepted {
		t.Fatalf("ticket refund on the exception: %d %v", st, out)
	}
	id := out["refunds"].([]any)[0].(map[string]any)["id"].(string)

	// Hold the payment's refund lock, as the engine does while it sends.
	holder, err := e.srv.pgxPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Rollback(ctx) }()
	if err := refunds.LockPayment(ctx, holder, e.f.pay); err != nil {
		t.Fatal(err)
	}
	type answer struct {
		status int
		body   map[string]any
	}
	done := make(chan answer, 1)
	go func() {
		st, out := e.pf.do(t, http.MethodPost, "/v1/refunds/"+id+"/reject", e.key, "")
		done <- answer{st, out}
	}()
	// The reject read 'requested' and now waits for the lock.
	deadline := time.Now().Add(10 * time.Second)
	for e.count(t, `SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND NOT granted`) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the reject never waited for the payment's refund lock")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := holder.Exec(ctx, `UPDATE refunds SET state = 'provider_pending', provider = 'stripe', provider_attempts = 1,
		approved_at = now() WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if err := holder.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	got := <-done
	if got.status != http.StatusConflict || prom0113Code(got.body) != "refund.invalid_state" {
		t.Fatalf("reject of a refund that moved on: %d %v; want 409 refund.invalid_state", got.status, got.body)
	}
	if n := e.count(t, `SELECT count(*) FROM refunds WHERE id = $1 AND state = 'provider_pending'`, id); n != 1 {
		t.Fatal("the refund that moved on was rejected")
	}
	if by := e.exceptionUsedBy(t, 0); by != id {
		t.Fatalf("exception used by %q; want it kept by the refund that moved on (%s)", by, id)
	}
}
