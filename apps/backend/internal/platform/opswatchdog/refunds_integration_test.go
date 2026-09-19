//go:build integration

// refunds_integration_test.go is the integration test for the refunds
// check family: a new refund row must be reported exactly once (INFO),
// never replayed, never duplicated.
package opswatchdog_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/opswatchdog"
)

func TestRefundsFeed_LiveDB_ReportedOnce(t *testing.T) {
	pool := connectWatchdogTestDB(t)
	ctx := context.Background()

	f := newWDFixture(t, ctx, pool)
	defer f.cleanup()

	paidAt := time.Now().UTC()
	orderID := f.seedOrder(t, ctx, "paid", &paidAt)
	csID := f.checkoutSessionOf(t, ctx, orderID)
	ticketID := f.seedTicket(t, ctx, orderID, csID)

	refundID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO refunds (id, org_id, amount, currency, state, settlement, order_id, ticket_id)
		VALUES ($1, $2, 500, 'EUR', 'succeeded', 'external', $3, $4)`,
		refundID, f.orgID, orderID, ticketID,
	); err != nil {
		t.Fatalf("seed refund: %v", err)
	}

	notifier := &recordingNotifier{}
	handler := opswatchdog.NewHandler(opswatchdog.Options{
		Pool:     pool,
		Notifier: notifier,
		Logger:   quietTestLogger(),
	})

	if err := handler(ctx, nil); err != nil {
		t.Fatalf("run 1: %v", err)
	}
	orgMarker := f.orgID.String()[:8] // part of "Watchdog Org <suffix>"
	msgs := notifier.messages()
	refundMsgs := 0
	for _, m := range msgs {
		if strings.Contains(m, "refund") && strings.Contains(m, orgMarker) {
			refundMsgs++
		}
	}
	if refundMsgs != 1 {
		t.Fatalf("run 1: refund messages for org = %d, want 1\nall:\n%s", refundMsgs, strings.Join(msgs, "\n---\n"))
	}

	if err := handler(ctx, nil); err != nil {
		t.Fatalf("run 2: %v", err)
	}
	msgs = notifier.messages()
	refundMsgs = 0
	for _, m := range msgs {
		if strings.Contains(m, "refund") && strings.Contains(m, orgMarker) {
			refundMsgs++
		}
	}
	if refundMsgs != 1 {
		t.Fatalf("run 2: refund messages for org = %d, want still 1 (no replay)\nall:\n%s", refundMsgs, strings.Join(msgs, "\n---\n"))
	}
}
