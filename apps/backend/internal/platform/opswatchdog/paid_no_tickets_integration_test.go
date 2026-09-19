//go:build integration

// paid_no_tickets_integration_test.go is the integration test the task
// mandates for the "paid but no tickets issued" check family: seed a paid
// order without tickets older than the threshold, expect exactly one
// CRITICAL; a second run must not duplicate it; fixing the condition
// (issuing a ticket) must produce a resolved message.
package opswatchdog_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/opswatchdog"
)

// recordingNotifier records every message Send was called with. Safe for
// concurrent use since the watchdog's checks run sequentially within one
// handler invocation, but a mutex costs nothing and rules out surprises.
type recordingNotifier struct {
	mu   sync.Mutex
	sent []string
}

func (r *recordingNotifier) Send(_ context.Context, text string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, text)
	return nil
}

func (r *recordingNotifier) messages() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.sent))
	copy(out, r.sent)
	return out
}

func countContaining(msgs []string, substr string) int {
	n := 0
	for _, m := range msgs {
		if strings.Contains(m, substr) {
			n++
		}
	}
	return n
}

func quietTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func connectWatchdogTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping live DB integration test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestPaidNoTickets_LiveDB_RaiseDedupResolve(t *testing.T) {
	pool := connectWatchdogTestDB(t)
	ctx := context.Background()

	f := newWDFixture(t, ctx, pool)
	defer f.cleanup()

	paidAt := time.Now().UTC().Add(-10 * time.Minute) // older than the 3-minute threshold
	orderID := f.seedOrder(t, ctx, "paid", &paidAt)
	csID := f.checkoutSessionOf(t, ctx, orderID)

	var systemID int64
	if err := pool.QueryRow(ctx, `SELECT system_id FROM orders WHERE id=$1`, orderID).Scan(&systemID); err != nil {
		t.Fatalf("read system_id: %v", err)
	}
	orderMarker := fmt.Sprintf("order: %d", systemID)

	notifier := &recordingNotifier{}
	handler := opswatchdog.NewHandler(opswatchdog.Options{
		Pool:     pool,
		Notifier: notifier,
		Logger:   quietTestLogger(),
		// No Scheduler: this test drives the handler directly, run by run,
		// and must not leave worker_jobs rows behind.
	})

	// ---- Run 1: expect exactly one CRITICAL for this order -----------------
	if err := handler(ctx, nil); err != nil {
		t.Fatalf("run 1: %v", err)
	}
	msgs := notifier.messages()
	if got := countContaining(msgs, orderMarker); got != 1 {
		t.Fatalf("run 1: messages mentioning %q = %d, want 1\nall messages:\n%s",
			orderMarker, got, strings.Join(msgs, "\n---\n"))
	}
	if !strings.Contains(oneContaining(msgs, orderMarker), "CRITICAL") {
		t.Errorf("run 1: expected message to be CRITICAL severity, got: %s", oneContaining(msgs, orderMarker))
	}
	if !strings.Contains(oneContaining(msgs, orderMarker), "no tickets issued") {
		t.Errorf("run 1: expected message about no tickets issued, got: %s", oneContaining(msgs, orderMarker))
	}

	// ---- Run 2: condition still holds, but must NOT duplicate the alert ----
	if err := handler(ctx, nil); err != nil {
		t.Fatalf("run 2: %v", err)
	}
	msgs = notifier.messages()
	if got := countContaining(msgs, orderMarker); got != 1 {
		t.Fatalf("run 2: messages mentioning %q = %d, want still 1 (no duplicate within the re-notify window)\nall messages:\n%s",
			orderMarker, got, strings.Join(msgs, "\n---\n"))
	}

	// ---- Fix the condition: issue a ticket for the order --------------------
	f.seedTicket(t, ctx, orderID, csID)

	// ---- Run 3: the alert must resolve --------------------------------------
	if err := handler(ctx, nil); err != nil {
		t.Fatalf("run 3: %v", err)
	}
	msgs = notifier.messages()
	resolvedCount := 0
	for _, m := range msgs {
		if strings.Contains(m, "resolved") && strings.Contains(m, "paid_no_tickets:"+orderID.String()) {
			resolvedCount++
		}
	}
	if resolvedCount != 1 {
		t.Fatalf("run 3: resolved messages for order = %d, want 1\nall messages:\n%s",
			resolvedCount, strings.Join(msgs, "\n---\n"))
	}

	// The store itself must agree: the alert is resolved.
	store := opswatchdog.NewPGAlertStore(pool)
	row, err := store.Get(ctx, "paid_no_tickets:"+orderID.String())
	if err != nil {
		t.Fatalf("Get alert: %v", err)
	}
	if row == nil {
		t.Fatal("expected an ops_alerts row to exist")
	}
	if row.ResolvedAt == nil {
		t.Fatal("expected ops_alerts row to be resolved")
	}

	// ---- Run 4: the ticket stops being 'active' — it is cancelled, refunded
	// or scanned at the door. The order still HAS its ticket, so the alert
	// must stay resolved. The check used to look for an 'active' ticket only
	// and re-raised a CRITICAL for every such order.
	if _, err := pool.Exec(ctx, `UPDATE tickets SET status = 'cancelled' WHERE order_id = $1`, orderID); err != nil {
		t.Fatalf("cancel ticket: %v", err)
	}
	before := countContaining(notifier.messages(), orderMarker)
	if err := handler(ctx, nil); err != nil {
		t.Fatalf("run 4: %v", err)
	}
	if after := countContaining(notifier.messages(), orderMarker); after != before {
		t.Fatalf("run 4: a cancelled ticket re-raised the alert (%d -> %d messages)", before, after)
	}

	cleanupOpsAlerts(t, ctx, pool, "paid_no_tickets:"+orderID.String())
}

// oneContaining returns the first message containing substr, or "" if none.
func oneContaining(msgs []string, substr string) string {
	for _, m := range msgs {
		if strings.Contains(m, substr) {
			return m
		}
	}
	return ""
}
