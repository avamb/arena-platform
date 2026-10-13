//go:build integration

package refunds_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/refunds"
)

// E-L1: one sweep pass has ONE alert budget. A pass whose review alerts use it
// up sends no provider-event alert; that alert goes out on the next pass.
func TestEngine_ProviderEventAlertsShareThePassBudget(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	f.cleanProviderEvents(t)
	ctx := context.Background()
	e := f.engine(&fakeModule{lookupErr: errors.New("provider unreachable")}, true)
	ev := payments.NormalizedEvent{ProviderRefundID: "re_budget_" + uuid.NewString()[:8],
		ProviderChargeRef: f.paymentRef(t), RefundStatus: payments.RefundSucceeded}
	for i := 0; i < refunds.MaxDashboardConfirmAttempts; i++ {
		_, _ = e.RecordDashboardRefund(ctx, f.org, "pay03fake", ev, nil)
	}
	if n := f.unresolved(t, refunds.UnresolvedUnconfirmed); n != 1 {
		t.Fatalf("%d unresolved records; want 1", n)
	}
	for i := 0; i < 15; i++ {
		f.rawRefund(t, f.payment, 1, "manual_review", `failure_code = 'budget_probe', alert_due_at = now()`)
	}
	first := &recordingNotifier{}
	if _, err := e.Sweep(ctx, time.Now(), first); err != nil {
		t.Fatal(err)
	}
	if len(first.texts) != 15 {
		t.Fatalf("first pass sent %d alerts; want the budget of 15", len(first.texts))
	}
	for _, text := range first.texts {
		if strings.Contains(text, ev.ProviderRefundID) {
			t.Fatalf("the provider event was alerted beyond the pass budget: %q", text)
		}
	}
	second := &recordingNotifier{}
	if _, err := e.Sweep(ctx, time.Now(), second); err != nil {
		t.Fatal(err)
	}
	if len(second.texts) != 1 || !strings.Contains(second.texts[0], ev.ProviderRefundID) {
		t.Fatalf("second pass alerts = %q; want the provider event", second.texts)
	}
}
