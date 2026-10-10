//go:build integration

// engine_review_integration_test.go — the PAY-03 review findings, each
// pinned against a live database (same fixture and fake module as
// engine_integration_test.go).
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

func succeeded(req payments.RefundRequest) (payments.RefundResult, error) {
	return payments.RefundResult{ProviderRefundID: "re_" + req.IdempotencyKey, Status: payments.RefundSucceeded}, nil
}

// TestEngine_RefusalAfterAnUnknownOutcomeGoesToManualReview (review H1): the
// first call ended without an answer, so it may have returned the money. A
// later refusal — a configuration error, a decline, a deactivated config —
// proves nothing about that first call: the refund goes to manual_review
// with an alert, never to failed (which would free the ticket for a second
// refund of money that may already be back).
func TestEngine_RefusalAfterAnUnknownOutcomeGoesToManualReview(t *testing.T) {
	pool := testPool(t)
	cases := []struct {
		name     string
		second   func(m *fakeModule)
		wantCode string
	}{
		{"config error", func(m *fakeModule) {
			m.buildErrs = []error{&refunds.ConfigError{Code: "payment.provider_not_configured", Message: "no config"}}
		}, "payment.provider_not_configured"},
		{"declined", func(m *fakeModule) {
			m.answers = []func(payments.RefundRequest) (payments.RefundResult, error){declined}
		}, "card_closed"},
		{"deactivated config", func(m *fakeModule) {
			m.buildErrs = []error{&refunds.ConfigError{Code: "payment.provider_inactive", Message: "the payment provider config is inactive"}}
		}, "payment.provider_inactive"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, pool, "pay03fake")
			m := &fakeModule{partial: true, answers: []func(payments.RefundRequest) (payments.RefundResult, error){timeout}}
			e := f.engine(m, true)
			res, err := e.CreateBatch(context.Background(), f.batch("k-h1", true, refunds.Item{TicketID: f.tickets[0]}))
			if err != nil {
				t.Fatal(err)
			}
			id := res.Refunds[0].ID
			if res.Refunds[0].State != refunds.StateProviderPending {
				t.Fatalf("after the timeout: %s", res.Refunds[0].State)
			}
			c.second(m)
			f.staleMarker(t, id)
			n := &recordingNotifier{}
			if _, err := e.Sweep(context.Background(), time.Now(), n); err != nil {
				t.Fatal(err)
			}
			got, _ := e.GetRefund(context.Background(), id)
			if got.State != refunds.StateManualReview || deref(got.FailureCode) != c.wantCode {
				t.Fatalf("refund = %s / %s; want manual_review with %s", got.State, deref(got.FailureCode), c.wantCode)
			}
			if st, _ := f.ticketStatus(t, f.tickets[0]); st != "active" {
				t.Fatalf("ticket = %s; nothing was accepted", st)
			}
			if len(n.texts) != 1 || !strings.Contains(n.texts[0], id.String()) {
				t.Fatalf("alerts = %v; want one naming the refund", n.texts)
			}
			// The ticket stays blocked for a second refund while a human looks.
			if _, err := e.CreateBatch(context.Background(), f.batch("k-h1-again", true, refunds.Item{TicketID: f.tickets[0]})); refusalCode(err) != refunds.CodeInProgress {
				t.Fatalf("second refund of the ticket: %v; want %s", err, refunds.CodeInProgress)
			}
		})
	}
}

// TestEngine_ConfigReadBlipIsUnknownNotFailed (review H1): when the
// configuration cannot be READ (the module source answers a plain error, as
// hcheckout.RefundModuleSource does for a database blip), the outcome is
// unknown — the refund stays provider_pending and is retried, and a later
// success settles it.
func TestEngine_ConfigReadBlipIsUnknownNotFailed(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	m := &fakeModule{partial: true, buildErrs: []error{errors.New("failed to load payment provider configs"), nil}}
	e := f.engine(m, true)
	res, err := e.CreateBatch(context.Background(), f.batch("k-blip", true, refunds.Item{TicketID: f.tickets[0]}))
	if err != nil {
		t.Fatal(err)
	}
	r := res.Refunds[0]
	if r.State != refunds.StateProviderPending || r.ProviderRefundID != nil || m.callCount() != 0 {
		t.Fatalf("after the blip: %s calls %d; want provider_pending, no provider call", r.State, m.callCount())
	}
	f.staleMarker(t, r.ID)
	if rep, err := e.Sweep(context.Background(), time.Now(), nil); err != nil || rep.Retried != 1 {
		t.Fatalf("sweep: %+v %v", rep, err)
	}
	got, _ := e.GetRefund(context.Background(), r.ID)
	if got.State != refunds.StateSucceeded {
		t.Fatalf("after the retry: %s; want succeeded", got.State)
	}
	if st, _ := f.ticketStatus(t, f.tickets[0]); st != "cancelled" {
		t.Fatalf("ticket = %s; want cancelled", st)
	}
}

// TestEngine_AlreadyRefundedGoesToManualReview (review M2): a refusal that
// itself says the money may be back (Stripe charge_already_refunded) is
// never a plain failure, even on the first attempt.
func TestEngine_AlreadyRefundedGoesToManualReview(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	m := &fakeModule{partial: true, answers: []func(payments.RefundRequest) (payments.RefundResult, error){
		func(payments.RefundRequest) (payments.RefundResult, error) {
			return payments.RefundResult{}, &payments.RefundDeclinedError{Code: "charge_already_refunded", Message: "Charge has already been refunded.", NeedsReview: true}
		}}}
	res, err := f.engine(m, true).CreateBatch(context.Background(), f.batch("k-already", true, refunds.Item{TicketID: f.tickets[0]}))
	if err != nil {
		t.Fatal(err)
	}
	if r := res.Refunds[0]; r.State != refunds.StateManualReview || deref(r.FailureCode) != "charge_already_refunded" {
		t.Fatalf("refund = %s / %s; want manual_review", r.State, deref(r.FailureCode))
	}
}

// TestEngine_ApprovedLateIsNotParkedEarly (review H2): a refund requested on
// day 1 and approved on day 3 has waited for the provider for minutes, not
// days — the 24 hours are counted from the approval.
func TestEngine_ApprovedLateIsNotParkedEarly(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	m := &fakeModule{partial: true, answers: []func(payments.RefundRequest) (payments.RefundResult, error){timeout}}
	e := f.engine(m, true)
	in := f.batch("k-late-approve", true, refunds.Item{TicketID: f.tickets[0]})
	in.Approved = false
	res, err := e.CreateBatch(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	id := res.Refunds[0].ID
	f.exec(t, `UPDATE refunds SET created_at = now() - interval '48 hours', requested_at = now() - interval '48 hours' WHERE id = $1`, id)

	// Day 3: approved (the flat approve route's two engine steps).
	ctx := context.Background()
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := refunds.LockPayment(ctx, tx, f.payment); err != nil {
		t.Fatal(err)
	}
	if _, err := refunds.MarkApprovedTx(ctx, tx, id, "pay03fake"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Drive(ctx, id); err != nil {
		t.Fatal(err)
	}
	f.staleMarker(t, id)
	rep, err := e.Sweep(ctx, time.Now(), &recordingNotifier{})
	if err != nil || rep.Stuck != 0 {
		t.Fatalf("sweep: %+v %v; a refund approved minutes ago is not stuck", rep, err)
	}
	if got, _ := e.GetRefund(ctx, id); got.State != refunds.StateProviderPending {
		t.Fatalf("state = %s; want still provider_pending", got.State)
	}
}

// TestEngine_ParkRacingAnInFlightCallKeepsTheAcceptance (review H2): the
// sweep never parks a refund whose call is in flight, and when the row is
// parked anyway (an operator, or a call that outlived its marker) the
// provider's acceptance is still recorded and the ticket cancelled.
func TestEngine_ParkRacingAnInFlightCallKeepsTheAcceptance(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	m := &fakeModule{partial: true, answers: []func(payments.RefundRequest) (payments.RefundResult, error){succeeded}}
	e := f.engine(m, true)
	var parkedDuringCall int
	var sweepErr error
	m.beforeRefund = func(req payments.RefundRequest) bool {
		// Old enough to be "stuck" by the clock — but the call is in flight.
		f.exec(t, `UPDATE refunds SET created_at = now() - interval '25 hours', approved_at = now() - interval '25 hours',
			first_attempted_at = now() - interval '24 hours' WHERE id = $1`, req.IdempotencyKey)
		rep, err := e.Sweep(context.Background(), time.Now(), nil)
		parkedDuringCall, sweepErr = rep.Stuck, err
		// Parked regardless, as an operator would.
		f.exec(t, `UPDATE refunds SET state = 'manual_review', failure_code = 'operator_hold' WHERE id = $1`, req.IdempotencyKey)
		return true
	}
	res, err := e.CreateBatch(context.Background(), f.batch("k-race-park", true, refunds.Item{TicketID: f.tickets[0]}))
	if err != nil {
		t.Fatal(err)
	}
	if sweepErr != nil || parkedDuringCall != 0 {
		t.Fatalf("sweep during the call: parked %d err %v; an in-flight call must not be parked", parkedDuringCall, sweepErr)
	}
	r := res.Refunds[0]
	if r.State != refunds.StateSucceeded || deref(r.ProviderRefundID) != "re_"+r.ID.String() {
		t.Fatalf("refund = %s / %s; the provider's acceptance must be recorded", r.State, deref(r.ProviderRefundID))
	}
	if st, link := f.ticketStatus(t, f.tickets[0]); st != "cancelled" || link == nil || *link != r.ID {
		t.Fatalf("ticket = %s link %v; Stripe accepted, the ticket must be cancelled", st, link)
	}
	var lateAudit int
	_ = f.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_events WHERE action = 'v1.refund.provider_result'
		AND resource_id = $1 AND (metadata->>'late_answer')::boolean`, r.ID.String()).Scan(&lateAudit)
	if lateAudit != 1 {
		t.Fatalf("late-answer audit rows = %d; want 1", lateAudit)
	}
}

// TestEngine_SweepCapsCallsAndServesEveryPayment (review M4/M5): one pass
// makes at most SweepMaxCalls provider calls, one per payment, and a refund
// never tried goes before one already retried.
func TestEngine_SweepCapsCallsAndServesEveryPayment(t *testing.T) {
	pool := testPool(t)
	f1, f2 := newFixture(t, pool, "pay03fake"), newFixture(t, pool, "pay03fake")
	m := &fakeModule{partial: true, answers: []func(payments.RefundRequest) (payments.RefundResult, error){timeout}}
	scope := func(max int) func(o *refunds.Options) {
		return func(o *refunds.Options) { o.SweepOrgs, o.SweepMaxCalls = []uuid.UUID{f1.org, f2.org}, max }
	}
	// f1: two refunds of one payment — the first timed out, the second was
	// never called (the claim saw the first in flight). f2: one timed out.
	r1, err := f1.engine(m, true).CreateBatch(context.Background(), f1.batch("k-fair", true,
		refunds.Item{TicketID: f1.tickets[0]}, refunds.Item{TicketID: f1.tickets[1]}))
	if err != nil {
		t.Fatal(err)
	}
	r2, err := f2.engine(m, true).CreateBatch(context.Background(), f2.batch("k-fair", true, refunds.Item{TicketID: f2.tickets[0]}))
	if err != nil {
		t.Fatal(err)
	}
	a, b, c := r1.Refunds[0], r1.Refunds[1], r2.Refunds[0]
	if a.ProviderAttempts != 1 || b.ProviderAttempts != 0 || c.ProviderAttempts != 1 {
		t.Fatalf("attempts %d/%d/%d; want 1/0/1", a.ProviderAttempts, b.ProviderAttempts, c.ProviderAttempts)
	}
	f1.staleMarker(t, a.ID)
	f2.staleMarker(t, c.ID)
	// b was never called; a refund that young is still its creator's to
	// drive, so age it past CallStaleAfter too.
	f1.exec(t, `UPDATE refunds SET created_at = now() - interval '2 minutes' WHERE id = $1`, b.ID)

	before := m.callCount()
	rep, err := f1.engineWith(m, true, scope(1)).Sweep(context.Background(), time.Now(), nil)
	if err != nil || rep.Calls != 1 || m.callCount()-before != 1 {
		t.Fatalf("capped pass: %+v %v calls %d; want exactly one call", rep, err, m.callCount()-before)
	}

	// The never-tried refund went first.
	if got, _ := f1.engine(m, true).GetRefund(context.Background(), b.ID); got.ProviderAttempts != 1 {
		t.Fatalf("the capped pass called something else first: b attempts %d", got.ProviderAttempts)
	}
	f1.staleMarker(t, a.ID)
	f1.staleMarker(t, b.ID)
	f2.staleMarker(t, c.ID)
	before = m.callCount()
	rep, err = f1.engineWith(m, true, scope(10)).Sweep(context.Background(), time.Now(), nil)
	if err != nil || rep.Calls != 2 || m.callCount()-before != 2 {
		t.Fatalf("pass: %+v %v; want one call per payment (two payments)", rep, err)
	}
	e := f1.engine(m, true)
	gotB, _ := e.GetRefund(context.Background(), b.ID)
	if gotB.ProviderAttempts == 0 {
		t.Fatalf("the never-tried refund b was starved: %+v", gotB)
	}
	gotC, _ := e.GetRefund(context.Background(), c.ID)
	if gotC.ProviderAttempts < 2 {
		t.Fatalf("payment 2's refund was starved: attempts %d", gotC.ProviderAttempts)
	}
}

// TestEngine_SweepPassDeadlineStopsCalling (review M4/M5): a pass stops
// calling the provider once its deadline passed, and the answer of the call
// that was in flight is still recorded.
func TestEngine_SweepPassDeadlineStopsCalling(t *testing.T) {
	pool := testPool(t)
	f1, f2 := newFixture(t, pool, "pay03fake"), newFixture(t, pool, "pay03fake")
	m := &fakeModule{partial: true, answers: []func(payments.RefundRequest) (payments.RefundResult, error){timeout}}
	r1, err := f1.engine(m, true).CreateBatch(context.Background(), f1.batch("k-deadline", true, refunds.Item{TicketID: f1.tickets[0]}))
	if err != nil {
		t.Fatal(err)
	}
	r2, err := f2.engine(m, true).CreateBatch(context.Background(), f2.batch("k-deadline", true, refunds.Item{TicketID: f2.tickets[0]}))
	if err != nil {
		t.Fatal(err)
	}
	f1.staleMarker(t, r1.Refunds[0].ID)
	f2.staleMarker(t, r2.Refunds[0].ID)
	m.answers = []func(payments.RefundRequest) (payments.RefundResult, error){succeeded}
	m.beforeRefund = func(payments.RefundRequest) bool {
		time.Sleep(400 * time.Millisecond) // a slow provider
		return true
	}
	e := f1.engineWith(m, true, func(o *refunds.Options) {
		o.SweepOrgs, o.SweepPassTimeout = []uuid.UUID{f1.org, f2.org}, 200*time.Millisecond
	})
	before := m.callCount()
	_, _ = e.Sweep(context.Background(), time.Now(), nil) // the pass may end with its deadline error
	if calls := m.callCount() - before; calls != 1 {
		t.Fatalf("calls = %d; the pass must stop calling after its deadline", calls)
	}
	won := 0
	for _, id := range []uuid.UUID{r1.Refunds[0].ID, r2.Refunds[0].ID} {
		if got, _ := e.GetRefund(context.Background(), id); got.State == refunds.StateSucceeded {
			won++
		}
	}
	if won != 1 {
		t.Fatalf("succeeded refunds = %d; the in-flight call's acceptance must be recorded despite the deadline", won)
	}
}

// TestEngine_RepairGivesUpAfterTheCapWithAnAlert (review M4/M5): a ticket
// cancellation that keeps failing is not retried forever — after the cap
// the refund is parked in manual_review and an operator is alerted.
func TestEngine_RepairGivesUpAfterTheCapWithAnAlert(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	m := &fakeModule{partial: true}
	e := f.engine(m, false) // no canceller: every cancellation "fails"
	res, err := e.CreateBatch(context.Background(), f.batch("k-repair-cap", true, refunds.Item{TicketID: f.tickets[0]}))
	if err != nil || res.Refunds[0].State != refunds.StateSucceeded {
		t.Fatalf("CreateBatch: %v", err)
	}
	id := res.Refunds[0].ID
	f.exec(t, `UPDATE refunds SET updated_at = now() - interval '2 minutes' WHERE id = $1`, id)
	n := &recordingNotifier{}
	if rep, err := e.Sweep(context.Background(), time.Now(), n); err != nil || rep.Repaired != 0 {
		t.Fatalf("first repair: %+v %v", rep, err)
	}
	var attempts int
	_ = f.pool.QueryRow(context.Background(), `SELECT repair_attempts FROM refunds WHERE id = $1`, id).Scan(&attempts)
	if attempts != 1 {
		t.Fatalf("repair_attempts = %d; want 1", attempts)
	}
	// An immediate second pass leaves it alone (the attempt bumped its clock).
	if _, err := e.Sweep(context.Background(), time.Now(), n); err != nil {
		t.Fatal(err)
	}
	_ = f.pool.QueryRow(context.Background(), `SELECT repair_attempts FROM refunds WHERE id = $1`, id).Scan(&attempts)
	if attempts != 1 {
		t.Fatalf("repair_attempts = %d after an immediate pass; want 1", attempts)
	}
	// At the cap.
	f.exec(t, `UPDATE refunds SET repair_attempts = 10, repair_attempted_at = now() - interval '2 minutes' WHERE id = $1`, id)
	if _, err := e.Sweep(context.Background(), time.Now(), n); err != nil {
		t.Fatal(err)
	}
	got, _ := e.GetRefund(context.Background(), id)
	if got.State != refunds.StateManualReview || deref(got.FailureCode) != "ticket_cancellation_failed" {
		t.Fatalf("refund = %s / %s; want parked", got.State, deref(got.FailureCode))
	}
	if len(n.texts) != 1 || !strings.Contains(n.texts[0], id.String()) || !strings.Contains(n.texts[0], "ticket_cancellation_failed") {
		t.Fatalf("alerts = %v", n.texts)
	}
}

// TestPGScheduler_NeverForksASecondChain (review M4/M5): scheduling the
// next sweep while one is already pending adds nothing.
func TestPGScheduler_NeverForksASecondChain(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	countPending := func() int {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM worker_jobs WHERE job_type = $1 AND status = 'pending'`, refunds.JobType).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	start := countPending()
	t.Cleanup(func() {
		if start == 0 {
			_, _ = pool.Exec(ctx, `DELETE FROM worker_jobs WHERE job_type = $1 AND status = 'pending'`, refunds.JobType)
		}
	})
	s := refunds.NewPGScheduler(pool)
	for i := 0; i < 3; i++ {
		if err := s.ScheduleNext(ctx, time.Now().Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	want := start
	if want == 0 {
		want = 1
	}
	if got := countPending(); got != want {
		t.Fatalf("pending sweeps = %d; want %d", got, want)
	}
}
