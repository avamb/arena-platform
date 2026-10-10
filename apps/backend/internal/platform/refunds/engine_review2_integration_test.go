//go:build integration

// engine_review2_integration_test.go — the findings of the SECOND PAY-03
// review, each pinned against a live database (same fixture and fake module
// as engine_integration_test.go).
package refunds_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/refunds"
)

// rawRefund inserts an engine refund row of pay directly — for scenarios
// (hundreds of overdue rows, many payments) the order fixture cannot build.
// set is extra SQL assignments applied right after the insert.
func (f *fixture) rawRefund(t *testing.T, pay uuid.UUID, amount int64, state, set string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := f.pool.QueryRow(context.Background(), `INSERT INTO refunds
		(payment_intent_id, org_id, amount, currency, reason, state, settlement, provider, origin, approved_at)
		VALUES ($1, $2, $3, 'EUR', 'raw', $4, 'provider', 'pay03fake', 'arena', now()) RETURNING id`,
		pay, f.org, amount, state).Scan(&id); err != nil {
		t.Fatalf("raw refund: %v", err)
	}
	if set != "" {
		f.exec(t, `UPDATE refunds SET `+set+` WHERE id = $1`, id)
	}
	return id
}

// extraPayment adds another succeeded payment of the fixture's organization.
func (f *fixture) extraPayment(t *testing.T) uuid.UUID {
	t.Helper()
	ref := "cs_pay03x_" + uuid.NewString()
	pi, err := gen.New(f.pool).InsertPaymentIntent(context.Background(), nil, f.org, "pay03fake", &ref, 1000, "EUR", "succeeded", nil, nil)
	if err != nil {
		t.Fatalf("extra payment: %v", err)
	}
	return pi.ID
}

func (f *fixture) refundState(t *testing.T, id uuid.UUID) refunds.Refund {
	t.Helper()
	r, err := f.engine(&fakeModule{}, false).GetRefund(context.Background(), id)
	if err != nil {
		t.Fatalf("get refund: %v", err)
	}
	return r
}

// approveNow moves a requested refund to provider_pending the way the flat
// approve route does (lock, MarkApprovedTx, commit) without driving it.
func (f *fixture) approveNow(t *testing.T, id uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := refunds.LockPayment(ctx, tx, f.payment); err != nil {
		t.Fatal(err)
	}
	if _, err := refunds.MarkApprovedTx(ctx, tx, id, "pay03fake"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

// TestEngine_OverdueRowsAreNeverRePosted (item 2): Stripe keeps an
// idempotency key for 24 hours, so a refund approved longer ago than
// StuckAfter must never be POSTed again — even when parkStuck (50 rows per
// pass) has not reached it yet.
func TestEngine_OverdueRowsAreNeverRePosted(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	m := &fakeModule{partial: true}
	e := f.engine(m, true)
	ids := make([]uuid.UUID, 0, 120)
	for i := 0; i < 120; i++ {
		ids = append(ids, f.rawRefund(t, f.payment, 1, "provider_pending",
			`approved_at = now() - interval '25 hours', created_at = now() - interval '25 hours',
			 provider_attempted_at = now() - interval '2 hours', provider_attempts = 1, provider_status = 'unknown_outcome'`))
	}
	for pass, wantStuck := range []int{50, 50, 20} {
		rep, err := e.Sweep(context.Background(), time.Now(), nil)
		if err != nil || rep.Stuck != wantStuck {
			t.Fatalf("pass %d: %+v %v; want %d parked", pass, rep, err, wantStuck)
		}
		if m.callCount() != 0 || rep.Calls != 0 {
			t.Fatalf("pass %d: %d provider calls; an overdue refund must never be re-POSTed", pass, m.callCount())
		}
	}
	// A direct Drive (a request) is refused by the claim as well. Reset one
	// row to provider_pending to prove it.
	f.exec(t, `UPDATE refunds SET state = 'provider_pending' WHERE id = $1`, ids[0])
	if _, err := e.Drive(context.Background(), ids[0]); err != nil {
		t.Fatal(err)
	}
	if m.callCount() != 0 {
		t.Fatalf("Drive re-POSTed an overdue refund")
	}
}

// TestEngine_InterleavedAttemptsJudgeTheLockedRow (item 3): attempt A
// outlives its marker; attempt B records an unknown outcome; attempt C is
// in flight when A's ConfigError lands. A must be judged on the LOCKED row
// (B's unknown outcome) — manual_review, the ticket stays blocked — and
// C's acceptance must still cancel the ticket.
func TestEngine_InterleavedAttemptsJudgeTheLockedRow(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	ctx := context.Background()
	m := &fakeModule{partial: true}
	e := f.engine(m, true)
	in := f.batch("k-interleave", true, refunds.Item{TicketID: f.tickets[0]})
	in.Approved = false
	res, err := e.CreateBatch(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	id := res.Refunds[0].ID
	f.approveNow(t, id)

	cInFlight, release := make(chan struct{}), make(chan struct{})
	cDone := make(chan refunds.Refund, 1)
	m.answers = []func(payments.RefundRequest) (payments.RefundResult, error){
		timeout, // B
		func(req payments.RefundRequest) (payments.RefundResult, error) { // C
			close(cInFlight)
			<-release
			return succeeded(req)
		},
	}
	var builds atomic.Int32
	m.onBuild = func() error {
		if builds.Add(1) != 1 {
			return nil // B's and C's builds
		}
		// A is slow: its marker goes stale, B runs and records unknown.
		f.staleMarker(t, id)
		if _, err := e.Drive(ctx, id); err != nil {
			t.Errorf("B: %v", err)
		}
		if got := f.refundState(t, id); deref(got.ProviderStatus) != "unknown_outcome" {
			t.Errorf("after B: provider_status %q", deref(got.ProviderStatus))
		}
		// C starts and is in flight when A's answer arrives.
		f.staleMarker(t, id)
		go func() {
			r, err := e.Drive(ctx, id)
			if err != nil {
				t.Errorf("C: %v", err)
			}
			cDone <- r
		}()
		<-cInFlight
		return &refunds.ConfigError{Code: "payment.provider_inactive", Message: "the payment provider config is inactive"}
	}
	a, err := e.Drive(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if a.State != refunds.StateManualReview {
		t.Fatalf("A recorded %s; judged on the locked row it must be manual_review, never failed", a.State)
	}
	// The amount is not freed: the ticket cannot be refunded a second time.
	if _, err := e.CreateBatch(ctx, f.batch("k-interleave-2", true, refunds.Item{TicketID: f.tickets[0]})); refusalCode(err) != refunds.CodeInProgress {
		t.Fatalf("second refund of the ticket: %v; want %s", err, refunds.CodeInProgress)
	}
	close(release)
	c := <-cDone
	if c.State != refunds.StateSucceeded || deref(c.ProviderRefundID) != "re_"+id.String() {
		t.Fatalf("C: %s / %s; the acceptance must be recorded", c.State, deref(c.ProviderRefundID))
	}
	if st, link := f.ticketStatus(t, f.tickets[0]); st != "cancelled" || link == nil || *link != id {
		t.Fatalf("ticket = %s link %v; want cancelled by the accepted refund", st, link)
	}
}

// TestEngine_LateAcceptanceRevivesAFailedRefundWithAnAlert (item 3): a row
// that went to failed while a call was in flight — and whose ticket a
// SECOND refund already took — is revived by the provider's late
// acceptance without colliding with the second refund, audited as a late
// answer, and an ops alert says to check for a double refund.
func TestEngine_LateAcceptanceRevivesAFailedRefundWithAnAlert(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	ctx := context.Background()
	m := &fakeModule{partial: true, answers: []func(payments.RefundRequest) (payments.RefundResult, error){pending, succeeded}}
	e := f.engine(m, true)
	var first atomic.Bool
	var second refunds.BatchResult
	m.beforeRefund = func(req payments.RefundRequest) bool {
		if first.CompareAndSwap(false, true) { // the outer call only; the second refund's call skips this
			f.exec(t, `UPDATE refunds SET state = 'failed', failed_at = now(), failure_code = 'operator' WHERE id = $1`, req.IdempotencyKey)
			var err error
			second, err = e.CreateBatch(ctx, f.batch("k-revive-2", true, refunds.Item{TicketID: f.tickets[0]}))
			if err != nil {
				t.Errorf("second refund: %v", err)
			}
		}
		return true
	}
	res, err := e.CreateBatch(ctx, f.batch("k-revive", true, refunds.Item{TicketID: f.tickets[0]}))
	if err != nil {
		t.Fatal(err)
	}
	r := res.Refunds[0]
	if r.State != refunds.StateSucceeded || deref(r.ProviderRefundID) != "re_"+r.ID.String() {
		t.Fatalf("revived refund = %s / %s; want succeeded with the provider id", r.State, deref(r.ProviderRefundID))
	}
	if r.CancelTicket {
		t.Fatalf("the revived refund kept cancel_ticket while the second refund owns the cancellation")
	}
	if len(second.Refunds) != 1 || second.Refunds[0].State != refunds.StateProviderPending {
		t.Fatalf("second refund = %+v", second.Refunds)
	}
	if st, _ := f.ticketStatus(t, f.tickets[0]); st != "cancelled" {
		t.Fatalf("ticket = %s; want cancelled", st)
	}
	var late int
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE action = 'v1.refund.provider_result'
		AND resource_id = $1 AND (metadata->>'late_answer')::boolean`, r.ID.String()).Scan(&late)
	if late != 1 {
		t.Fatalf("late-answer audit rows = %d", late)
	}
	n := &recordingNotifier{}
	if _, err := e.Sweep(ctx, time.Now(), n); err != nil {
		t.Fatal(err)
	}
	if len(n.texts) != 1 || !strings.Contains(n.texts[0], r.ID.String()) || !strings.Contains(n.texts[0], "accepted_after_failure") {
		t.Fatalf("alerts = %v; want one about the revived refund", n.texts)
	}
}

// TestEngine_SecondEntryIntoManualReviewAlertsAgain (item 4): park → alert
// → a late PENDING acceptance takes the row out of manual_review → the
// lookup reports the refund failed → manual_review again, and a SECOND
// alert fires.
func TestEngine_SecondEntryIntoManualReviewAlertsAgain(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	ctx := context.Background()
	m := &fakeModule{partial: true, answers: []func(payments.RefundRequest) (payments.RefundResult, error){pending}}
	e := f.engine(m, true)
	n := &recordingNotifier{}
	m.beforeRefund = func(req payments.RefundRequest) bool {
		// The call outlives its marker and the approval is a day old: the
		// sweep parks the row and alerts while the call is in flight.
		f.exec(t, `UPDATE refunds SET approved_at = now() - interval '25 hours', created_at = now() - interval '25 hours',
			provider_attempted_at = now() - interval '2 minutes' WHERE id = $1`, req.IdempotencyKey)
		rep, err := e.Sweep(ctx, time.Now(), n)
		if err != nil || rep.Stuck != 1 || rep.Alerted != 1 {
			t.Errorf("park during the call: %+v %v", rep, err)
		}
		return true
	}
	res, err := e.CreateBatch(ctx, f.batch("k-realert", true, refunds.Item{TicketID: f.tickets[0]}))
	if err != nil {
		t.Fatal(err)
	}
	id := res.Refunds[0].ID
	got := f.refundState(t, id)
	if got.State != refunds.StateProviderPending || got.ProviderRefundID == nil {
		t.Fatalf("after the late pending acceptance: %s", got.State)
	}
	var alerted *time.Time
	_ = f.pool.QueryRow(ctx, `SELECT review_alerted_at FROM refunds WHERE id = $1`, id).Scan(&alerted)
	if alerted != nil {
		t.Fatalf("review_alerted_at must be cleared when the row leaves manual_review")
	}
	if len(n.texts) != 1 {
		t.Fatalf("first alert: %v", n.texts)
	}
	f.exec(t, `UPDATE refunds SET updated_at = now() - interval '11 minutes' WHERE id = $1`, id)
	m.lookup = payments.RefundResult{Status: payments.RefundFailed, FailureCode: "insufficient_funds"}
	if _, err := e.Sweep(ctx, time.Now(), n); err != nil {
		t.Fatal(err)
	}
	if got := f.refundState(t, id); got.State != refunds.StateManualReview {
		t.Fatalf("after the failed lookup: %s", got.State)
	}
	if len(n.texts) != 2 || !strings.Contains(n.texts[1], "insufficient_funds") {
		t.Fatalf("alerts = %v; the second entry into manual_review must alert again", n.texts)
	}
}

// TestEngine_ProviderBrownoutDoesNotStarveRepairAndAlerts (item 5): ten
// payments whose calls never answer must not eat the pass — the retry
// section stops after its own deadline or maxUnknownPerPass, and the
// repair and the alerts still happen.
func TestEngine_ProviderBrownoutDoesNotStarveRepairAndAlerts(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	ctx := context.Background()
	m := &fakeModule{partial: true}
	// A repair candidate: accepted, the ticket left active (no canceller).
	res, err := f.engine(m, false).CreateBatch(ctx, f.batch("k-brownout", true, refunds.Item{TicketID: f.tickets[0]}))
	if err != nil || res.Refunds[0].State != refunds.StateSucceeded {
		t.Fatalf("repair candidate: %v", err)
	}
	f.exec(t, `UPDATE refunds SET updated_at = now() - interval '2 minutes' WHERE id = $1`, res.Refunds[0].ID)
	review := f.rawRefund(t, f.payment, 1, "manual_review", `failure_code = 'brownout_probe', alert_due_at = now()`)
	retry := `approved_at = now() - interval '10 minutes', provider_attempted_at = now() - interval '2 minutes',
		provider_attempts = 1, provider_status = 'unknown_outcome'`
	for i := 0; i < 10; i++ {
		f.rawRefund(t, f.extraPayment(t), 100, "provider_pending", retry)
	}
	m.answers = []func(payments.RefundRequest) (payments.RefundResult, error){
		func(payments.RefundRequest) (payments.RefundResult, error) {
			time.Sleep(150 * time.Millisecond)
			return payments.RefundResult{}, fmt.Errorf("context deadline exceeded")
		},
	}
	n := &recordingNotifier{}
	e := f.engineWith(m, true, func(o *refunds.Options) { o.SweepPassTimeout = 2400 * time.Millisecond })
	rep, _ := e.Sweep(ctx, time.Now(), n)
	if rep.Calls > 5 || rep.Calls == 0 || rep.Unknown != rep.Calls {
		t.Fatalf("slow brownout pass: %+v; want a few calls, all unknown, within the retry budget", rep)
	}
	if rep.Repaired != 1 || rep.Alerted != 1 || len(n.texts) != 1 || !strings.Contains(n.texts[0], review.String()) {
		t.Fatalf("pass %+v alerts %v; repair and alerts must still run", rep, n.texts)
	}
	// Fast timeouts: the pass stops at maxUnknownPerPass, not at ten.
	f.exec(t, `UPDATE refunds SET provider_attempted_at = now() - interval '2 minutes'
		WHERE org_id = $1 AND state = 'provider_pending' AND provider_refund_id IS NULL`, f.org)
	m.answers = []func(payments.RefundRequest) (payments.RefundResult, error){timeout}
	rep, _ = f.engine(m, true).Sweep(ctx, time.Now(), n)
	if rep.Calls != 5 || rep.Unknown != 5 {
		t.Fatalf("fast brownout pass: %+v; want exactly 5 unanswered calls", rep)
	}
}

// TestEngine_DeterministicModuleErrorsGoStraightToReview (item 6): an
// error that proves the provider was never reached and can never change —
// an unknown or declared-only module, an amount the module refuses before
// any network call — goes to manual_review with an alert at once instead of
// being retried every minute for a day.
func TestEngine_DeterministicModuleErrorsGoStraightToReview(t *testing.T) {
	pool := testPool(t)
	cases := []struct {
		name     string
		setup    func(m *fakeModule)
		wantCode string
	}{
		{"unknown module", func(m *fakeModule) {
			m.buildErrs = []error{fmt.Errorf("%w: %q", payments.ErrUnknownProvider, "gone")}
		}, "payment_module_unavailable"},
		{"declared only", func(m *fakeModule) {
			m.buildErrs = []error{fmt.Errorf("%w: gone", payments.ErrModuleNotImplemented)}
		}, "payment_module_unavailable"},
		{"amount refused before the network", func(m *fakeModule) {
			m.answers = []func(payments.RefundRequest) (payments.RefundResult, error){
				func(payments.RefundRequest) (payments.RefundResult, error) {
					return payments.RefundResult{}, fmt.Errorf("%w: KWD amounts must be a multiple of 10", payments.ErrRefundAmountInvalid)
				}}
		}, "amount_invalid"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, pool, "pay03fake")
			m := &fakeModule{partial: true}
			c.setup(m)
			e := f.engine(m, true)
			res, err := e.CreateBatch(context.Background(), f.batch("k-det", true, refunds.Item{TicketID: f.tickets[0]}))
			if err != nil {
				t.Fatal(err)
			}
			r := res.Refunds[0]
			if r.State != refunds.StateManualReview || deref(r.FailureCode) != c.wantCode || r.ProviderAttempts != 1 {
				t.Fatalf("refund = %s / %s attempts %d; want manual_review %s on the first attempt",
					r.State, deref(r.FailureCode), r.ProviderAttempts, c.wantCode)
			}
			n := &recordingNotifier{}
			if rep, err := e.Sweep(context.Background(), time.Now(), n); err != nil || rep.Calls != 0 || len(n.texts) != 1 {
				t.Fatalf("sweep: %+v %v alerts %v; want no retry and one alert", rep, err, n.texts)
			}
			if st, _ := f.ticketStatus(t, f.tickets[0]); st != "active" {
				t.Fatalf("ticket = %s", st)
			}
		})
	}
}

// TestEngine_ConcurrentRepairsPublishOnce (item 7): two sweep passes
// repairing the same refund at the same time publish v1.ticket.refunded
// exactly once.
func TestEngine_ConcurrentRepairsPublishOnce(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	ctx := context.Background()
	m := &fakeModule{partial: true}
	res, err := f.engine(m, false).CreateBatch(ctx, f.batch("k-publish-once", true, refunds.Item{TicketID: f.tickets[0]}))
	if err != nil || res.Refunds[0].State != refunds.StateSucceeded {
		t.Fatalf("CreateBatch: %v", err)
	}
	f.exec(t, `UPDATE refunds SET updated_at = now() - interval '2 minutes' WHERE id = $1`, res.Refunds[0].ID)
	var mu sync.Mutex
	published := 0
	e := f.engineWith(m, true, func(o *refunds.Options) {
		o.PublishRefunded = func(context.Context, []string, string, string, string, int64) {
			mu.Lock()
			published++
			mu.Unlock()
		}
	})
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := e.Sweep(ctx, time.Now(), nil); err != nil {
				t.Errorf("sweep: %v", err)
			}
		}()
	}
	wg.Wait()
	if published != 1 {
		t.Fatalf("published %d times; want exactly once", published)
	}
	if st, _ := f.ticketStatus(t, f.tickets[0]); st != "cancelled" {
		t.Fatalf("ticket = %s", st)
	}
}

// TestEngine_AcceptedPendingIsLookedUpNotParked (item 8): a refund the
// provider accepted as pending is not "stuck" after 24 hours — it keeps
// being looked up — and is parked with an alert only after 7 days.
func TestEngine_AcceptedPendingIsLookedUpNotParked(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	ctx := context.Background()
	m := &fakeModule{partial: true, answers: []func(payments.RefundRequest) (payments.RefundResult, error){pending}}
	m.lookup = payments.RefundResult{Status: payments.RefundPending}
	e := f.engine(m, true)
	res, err := e.CreateBatch(ctx, f.batch("k-slow-pending", true, refunds.Item{TicketID: f.tickets[0]}))
	if err != nil {
		t.Fatal(err)
	}
	id := res.Refunds[0].ID
	f.exec(t, `UPDATE refunds SET approved_at = now() - interval '3 days', created_at = now() - interval '3 days',
		updated_at = now() - interval '11 minutes' WHERE id = $1`, id)
	n := &recordingNotifier{}
	rep, err := e.Sweep(ctx, time.Now(), n)
	if err != nil || rep.Stuck != 0 || rep.LookedUp != 1 || len(n.texts) != 0 {
		t.Fatalf("3 days pending: %+v %v alerts %v; want looked up, not parked", rep, err, n.texts)
	}
	f.exec(t, `UPDATE refunds SET approved_at = now() - interval '8 days', created_at = now() - interval '8 days',
		updated_at = now() - interval '11 minutes' WHERE id = $1`, id)
	rep, err = e.Sweep(ctx, time.Now(), n)
	if err != nil || rep.Stuck != 1 || rep.LookedUp != 0 {
		t.Fatalf("8 days pending: %+v %v; want parked", rep, err)
	}
	if got := f.refundState(t, id); got.State != refunds.StateManualReview || deref(got.FailureCode) != "provider_pending_too_long" {
		t.Fatalf("refund = %s / %s", got.State, deref(got.FailureCode))
	}
	if len(n.texts) != 1 || !strings.Contains(n.texts[0], "provider_pending_too_long") {
		t.Fatalf("alerts = %v", n.texts)
	}
}
