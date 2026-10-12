//go:build integration

package refunds_test

import (
	"context"
	"errors"
	"strings"
	"sync"
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

// Round-3 review R3-8: an event for a refund arena already has is read back
// before anything moves; a read that keeps failing (a 404 included) is
// retried by the provider only MaxDashboardConfirmAttempts times, then the
// refund is left as it is, a record is written and the operator alerted.
func TestEngine_KnownRefundReadBackFailuresAreBounded(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	f.cleanProviderEvents(t)
	e, r, m := pendingRefundWith(t, f, "k-r4-known")
	ctx := context.Background()
	m.lookupErr = payments.ErrRefundNotFound
	for i := 1; i < refunds.MaxDashboardConfirmAttempts; i++ {
		if found, err := e.ApplyProviderRefund(ctx, f.org, "pay03fake", *r.ProviderRefundID,
			payments.RefundResult{Status: payments.RefundSucceeded}); err == nil || !found || errors.Is(err, refunds.ErrProviderEventGivenUp) {
			t.Fatalf("delivery %d = %v %v; want found with a retryable error", i, found, err)
		}
	}
	if _, err := e.ApplyProviderRefund(ctx, f.org, "pay03fake", *r.ProviderRefundID,
		payments.RefundResult{Status: payments.RefundSucceeded}); !errors.Is(err, refunds.ErrProviderEventGivenUp) {
		t.Fatalf("last delivery: %v; want ErrProviderEventGivenUp", err)
	}
	if got, _ := e.GetRefund(ctx, r.ID); got.State != refunds.StateProviderPending {
		t.Fatalf("state = %s; an unconfirmed event moved the refund", got.State)
	}
	if n := f.unresolved(t, refunds.UnresolvedKnownUnconfirmed); n != 1 {
		t.Fatalf("%d records; want 1", n)
	}
	n := &recordingNotifier{}
	if _, err := e.Sweep(ctx, time.Now(), n); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, txt := range n.texts {
		if strings.Contains(txt, r.ID.String()) && strings.Contains(txt, "left as it was") {
			found = true
		}
	}
	if !found {
		t.Fatalf("alerts = %q; want one naming arena refund %s", n.texts, r.ID)
	}
}

// R3-8: an event that names no payment and cannot be read back is bounded too.
func TestEngine_EventWithoutPaymentReadBackFailuresAreBounded(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	f.cleanProviderEvents(t)
	m := &fakeModule{lookupErr: errors.New("provider unreachable")}
	e := f.engine(m, true)
	ctx := context.Background()
	ev := payments.NormalizedEvent{ProviderRefundID: "re_r4_nopay_" + uuid.NewString()[:8], RefundStatus: payments.RefundSucceeded}
	for i := 1; i < refunds.MaxDashboardConfirmAttempts; i++ {
		if _, err := e.RecordDashboardRefund(ctx, f.org, "pay03fake", ev, nil); err == nil {
			t.Fatalf("delivery %d: no error", i)
		}
	}
	if res, err := e.RecordDashboardRefund(ctx, f.org, "pay03fake", ev, nil); err != nil || !res.Unconfirmed {
		t.Fatalf("last delivery = %+v %v; want acknowledged", res, err)
	}
	if n := f.unresolved(t, refunds.UnresolvedNoPayment); n != 1 {
		t.Fatalf("%d records; want 1", n)
	}
}

// Round-3 review R3-7: a payment that only knows its checkout id (no charge
// reference stored) is resolved before a refund event that names the charge
// is judged foreign; when it cannot be resolved the event leaves a record
// and alerts the operator instead of being dropped as not ours.
func TestEngine_DashboardRefundOfAPaymentWithoutChargeRef(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	f.cleanProviderEvents(t)
	ctx := context.Background()
	cs := f.paymentRef(t)

	// Unresolvable: recorded, alerted, nothing applied.
	bad := &fakeModule{resolveErr: &payments.RefundDeclinedError{Code: "resource_missing"}}
	ev := payments.NormalizedEvent{ProviderRefundID: "re_r4_nocref_" + uuid.NewString()[:8],
		ProviderChargeRef: "pi_from_" + cs, RefundStatus: payments.RefundSucceeded}
	res, err := f.engine(bad, true).RecordDashboardRefund(ctx, f.org, "pay03fake", ev, nil)
	if err != nil || !res.Unconfirmed || res.Created {
		t.Fatalf("unresolvable = %+v %v; want acknowledged, unconfirmed, nothing written", res, err)
	}
	if n := f.unresolved(t, refunds.UnresolvedNoChargeRef); n != 1 {
		t.Fatalf("%d records; want 1", n)
	}

	// Resolvable: the reference is stored and the refund is recorded.
	good := &fakeModule{lookup: payments.RefundResult{Status: payments.RefundSucceeded, AmountMinor: 5000, Currency: "eur"}}
	ev.ProviderRefundID = "re_r4_cref_" + uuid.NewString()[:8]
	res, err = f.engine(good, true).RecordDashboardRefund(ctx, f.org, "pay03fake", ev, nil)
	if err != nil || !res.Created {
		t.Fatalf("resolvable = %+v %v; want the refund recorded", res, err)
	}
	var stored string
	_ = f.pool.QueryRow(ctx, `SELECT COALESCE(provider_charge_ref, '') FROM payment_intents WHERE id = $1`, f.payment).Scan(&stored)
	if stored != "pi_from_"+cs {
		t.Fatalf("provider_charge_ref = %q; want the resolved reference stored", stored)
	}
}

// Parallel redeliveries of the event that settles a pending refund settle it
// once: one cancellation and one v1.ticket.refunded for its ticket.
func TestEngine_ParallelRefundEventsSettleOnce(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	pub := &publishCounter{}
	m := &fakeModule{partial: true, answers: []func(payments.RefundRequest) (payments.RefundResult, error){pending}}
	e := f.engineWith(m, true, func(o *refunds.Options) { o.PublishRefunded = pub.fn })
	ctx := context.Background()
	created, err := e.CreateBatch(ctx, f.batch("k-r4-parallel", true, refunds.Item{TicketID: f.tickets[0]}))
	if err != nil || created.Refunds[0].ProviderRefundID == nil {
		t.Fatalf("CreateBatch: %v", err)
	}
	r := created.Refunds[0]
	m.lookup = payments.RefundResult{Status: payments.RefundSucceeded}
	const n = 6
	errs := make(chan error, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := e.ApplyProviderRefund(ctx, f.org, "pay03fake", *r.ProviderRefundID, payments.RefundResult{Status: payments.RefundSucceeded})
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("a parallel delivery failed: %v", err)
		}
	}
	if got, _ := e.GetRefund(ctx, r.ID); got.State != refunds.StateSucceeded {
		t.Fatalf("state = %s; want succeeded", got.State)
	}
	if st, _ := f.ticketStatus(t, f.tickets[0]); st != "cancelled" {
		t.Fatalf("ticket = %s; want cancelled", st)
	}
	if got := pub.get(f.tickets[0]); got != 1 || pub.total() != 1 {
		t.Fatalf("v1.ticket.refunded published %d time(s) (total %d); want once", got, pub.total())
	}
}
