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

// paymentRef is the fixture payment's provider reference (its cs_… id).
func (f *fixture) paymentRef(t *testing.T) string {
	t.Helper()
	var ref string
	if err := f.pool.QueryRow(context.Background(), `SELECT provider_payment_id FROM payment_intents WHERE id = $1`, f.payment).Scan(&ref); err != nil {
		t.Fatal(err)
	}
	return ref
}

func (f *fixture) cleanProviderEvents(t *testing.T) {
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM audit_events WHERE resource_type = 'provider_refund' AND metadata->>'org_id' = $1::text`, f.org)
	})
}

func (f *fixture) unresolved(t *testing.T, kind string) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_events
		WHERE action = 'v1.refund.provider_event_unresolved' AND metadata->>'org_id' = $1::text AND metadata->>'kind' = $2`,
		f.org, kind).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Round-3 review R3-1: a dashboard refund of an arena payment that cannot be
// read back after MaxDashboardConfirmAttempts deliveries leaves a visible
// record and the sweep sends the operator ONE alert for it.
func TestEngine_UnconfirmedDashboardRefundAlertsTheOperator(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	f.cleanProviderEvents(t)
	m := &fakeModule{lookupErr: errors.New("provider unreachable")}
	e := f.engine(m, true)
	ctx := context.Background()
	ev := payments.NormalizedEvent{ProviderRefundID: "re_r4_unconf_" + uuid.NewString()[:8],
		ProviderChargeRef: f.paymentRef(t), RefundStatus: payments.RefundSucceeded}
	for i := 1; i < refunds.MaxDashboardConfirmAttempts; i++ {
		if _, err := e.RecordDashboardRefund(ctx, f.org, "pay03fake", ev, nil); err == nil {
			t.Fatalf("delivery %d: no error; want one so the provider retries", i)
		}
	}
	res, err := e.RecordDashboardRefund(ctx, f.org, "pay03fake", ev, nil)
	if err != nil || !res.Unconfirmed {
		t.Fatalf("last delivery = %+v %v; want acknowledged as unconfirmed", res, err)
	}
	if n := f.unresolved(t, refunds.UnresolvedUnconfirmed); n != 1 {
		t.Fatalf("%d unresolved records; want 1", n)
	}
	// A late redelivery writes no second record.
	_, _ = e.RecordDashboardRefund(ctx, f.org, "pay03fake", ev, nil)
	if n := f.unresolved(t, refunds.UnresolvedUnconfirmed); n != 1 {
		t.Fatalf("%d unresolved records after a redelivery; want 1", n)
	}
	n := &recordingNotifier{}
	if _, err := e.Sweep(ctx, time.Now(), n); err != nil {
		t.Fatal(err)
	}
	if len(n.texts) != 1 || !strings.Contains(n.texts[0], ev.ProviderRefundID) || !strings.Contains(n.texts[0], "could not be read back") {
		t.Fatalf("alerts = %q; want one naming the provider refund", n.texts)
	}
	n2 := &recordingNotifier{}
	if _, err := e.Sweep(ctx, time.Now(), n2); err != nil {
		t.Fatal(err)
	}
	if len(n2.texts) != 0 {
		t.Fatalf("the alert was sent again: %q", n2.texts)
	}
}
