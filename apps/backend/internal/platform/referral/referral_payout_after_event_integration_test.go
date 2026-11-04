//go:build integration

// referral_payout_after_event_integration_test.go — the payout rules of owner
// decision 03.11.2026 (spec 52 R-RF2, sections 5.1 and 6.2): the cashback
// waits for the end of the friend's event, a friend refunded before that
// cancels it, the organizer's Stripe balance is read first, and a refund
// Stripe holds pending or later cancels reaches the reward.
package referral_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/referral"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/refunds"
)

// moveSession puts the fixture's session at start (two hours long).
func (f *fixture) moveSession(t *testing.T, start time.Time) {
	t.Helper()
	f.exec(t, `UPDATE sessions SET start_at = $2, end_at = $2::timestamptz + interval '2 hours',
	                               sales_end_at = $2 WHERE id = $1`, f.session, start.UTC().Truncate(time.Minute))
}

type waitView struct {
	State, WaitReason string
	NextAttempt       *time.Time
	BalanceSince      *time.Time
}

func (f *fixture) waitOf(t *testing.T, id uuid.UUID) waitView {
	t.Helper()
	var v waitView
	var reason *string
	if err := f.pool.QueryRow(context.Background(), `SELECT state, wait_reason, next_attempt_at, balance_wait_since
		FROM referral_rewards WHERE id = $1`, id).Scan(&v.State, &reason, &v.NextAttempt, &v.BalanceSince); err != nil {
		t.Fatal(err)
	}
	if reason != nil {
		v.WaitReason = *reason
	}
	return v
}

// due makes a waiting reward's time come, as the sweep would find it later.
func (f *fixture) due(t *testing.T, id uuid.UUID) {
	t.Helper()
	f.exec(t, `UPDATE referral_rewards SET next_attempt_at = now() - interval '1 minute' WHERE id = $1`, id)
}

func (f *fixture) letters(t *testing.T, jobType string, id uuid.UUID) int {
	t.Helper()
	return f.count(t, `SELECT count(*) FROM worker_jobs WHERE job_type = $1 AND payload->>'reward_id' = $2`, jobType, id.String())
}

func (f *fixture) organizerLetters(t *testing.T, kind string) int {
	t.Helper()
	return f.count(t, `SELECT count(*) FROM worker_jobs WHERE job_type = 'referral.organizer_email'
		AND payload->>'org_id' = $1 AND payload->>'kind' = $2`, f.org.String(), kind)
}

func TestPayout_WaitsUntilTheFriendsSessionHasEnded(t *testing.T) {
	f := newFixture(t, 10, 10, 100)
	ctx := context.Background()
	start := time.Now().Add(48 * time.Hour)
	f.moveSession(t, start)
	code := f.issue(t)
	friend := f.friend(t, code, 3000, "friend-"+f.suffix+"@example.com")
	rw := f.rewardOf(t, friend)

	if err := f.svc.Payout(ctx, rw.ID); err != nil {
		t.Fatalf("Payout before the event: %v", err)
	}
	w := f.waitOf(t, rw.ID)
	if w.State != referral.StateDue || w.WaitReason != referral.WaitEvent || f.module.callCount() != 0 {
		t.Fatalf("before the event: %+v, %d refund calls; want due waiting_event and no refund", w, f.module.callCount())
	}
	wantAt := start.Truncate(time.Minute).Add(2*time.Hour + referral.PayoutGrace)
	if w.NextAttempt == nil || w.NextAttempt.Sub(wantAt).Abs() > time.Minute {
		t.Fatalf("next attempt %v; want the end of the session plus the grace (%v)", w.NextAttempt, wantAt)
	}
	// The sweep does not queue it before its time.
	f.exec(t, `DELETE FROM worker_jobs WHERE job_type = 'referral.payout' AND payload->>'reward_id' = $1`, rw.ID.String())
	f.exec(t, `UPDATE referral_rewards SET created_at = now() - interval '1 hour' WHERE id = $1`, rw.ID)
	if rep, err := f.svc.Sweep(ctx); err != nil || rep.Requeued != 0 {
		t.Fatalf("sweep before the event: %+v %v; want nothing requeued", rep, err)
	}

	// The session is moved to a later date: the payout moves with it.
	f.moveSession(t, start.Add(7*24*time.Hour))
	f.due(t, rw.ID)
	if err := f.svc.Payout(ctx, rw.ID); err != nil {
		t.Fatal(err)
	}
	if w := f.waitOf(t, rw.ID); w.State != referral.StateDue || w.NextAttempt == nil || w.NextAttempt.Before(start.Add(7*24*time.Hour)) {
		t.Fatalf("moved session: %+v; want due until the new date", w)
	}

	// The event took place and the grace passed: paid, the wait reason gone.
	f.moveSession(t, time.Now().Add(-26*time.Hour-2*time.Hour))
	f.due(t, rw.ID)
	if err := f.svc.Payout(ctx, rw.ID); err != nil {
		t.Fatal(err)
	}
	if got := f.rewardOf(t, friend); got.State != referral.StatePaid || got.Amount != 300 {
		t.Fatalf("after the event: %+v; want paid 300", got)
	}
	if w := f.waitOf(t, rw.ID); w.WaitReason != "" {
		t.Fatalf("a paid reward still says %q", w.WaitReason)
	}
}

func TestPayout_FriendRefundedBeforeTheEventEndsCancelsTheCashback(t *testing.T) {
	f := newFixture(t, 10, 10, 100)
	ctx := context.Background()
	f.moveSession(t, time.Now().Add(72*time.Hour))
	code := f.issue(t)
	friend := f.friend(t, code, 3000, "friend-"+f.suffix+"@example.com")
	rw := f.rewardOf(t, friend)
	if err := f.svc.Payout(ctx, rw.ID); err != nil {
		t.Fatal(err)
	}
	// The friend gets the money back before the event; the referrer keeps
	// their ticket. The sweep cancels the cashback at once.
	f.exec(t, `UPDATE orders SET status = 'refunded' WHERE id = $1`, friend)
	rep, err := f.svc.Sweep(ctx)
	if err != nil || rep.Cancelled != 1 {
		t.Fatalf("sweep: %+v %v; want 1 cancelled", rep, err)
	}
	if got := f.rewardOf(t, friend); got.State != referral.StateCancelled || got.Reason != "friend_refunded" {
		t.Fatalf("reward %+v; want cancelled friend_refunded", got)
	}
	// After the event nothing is paid.
	f.moveSession(t, time.Now().Add(-4*24*time.Hour))
	if err := f.svc.Payout(ctx, rw.ID); err != nil {
		t.Fatal(err)
	}
	if f.module.callCount() != 0 || f.count(t, `SELECT count(*) FROM refunds WHERE org_id = $1`, f.org) != 0 {
		t.Fatal("a cashback left for a friend who got the money back")
	}
}

func TestPayout_FriendRefundedDuringTheGraceIsCancelledByThePayout(t *testing.T) {
	f := newFixture(t, 10, 10, 100)
	ctx := context.Background()
	f.moveSession(t, time.Now().Add(-4*time.Hour)) // ended two hours ago
	code := f.issue(t)
	friend := f.friend(t, code, 3000, "friend-"+f.suffix+"@example.com")
	rw := f.rewardOf(t, friend)
	if err := f.svc.Payout(ctx, rw.ID); err != nil {
		t.Fatal(err)
	}
	if w := f.waitOf(t, rw.ID); w.WaitReason != referral.WaitEvent {
		t.Fatalf("inside the grace: %+v", w)
	}
	f.exec(t, `UPDATE orders SET status = 'partially_refunded' WHERE id = $1`, friend)
	f.moveSession(t, time.Now().Add(-3*24*time.Hour))
	f.due(t, rw.ID)
	if err := f.svc.Payout(ctx, rw.ID); err != nil {
		t.Fatal(err)
	}
	if got := f.rewardOf(t, friend); got.State != referral.StateCancelled || got.Reason != "friend_refunded" || f.module.callCount() != 0 {
		t.Fatalf("reward %+v, %d calls; want cancelled friend_refunded and no refund", got, f.module.callCount())
	}
}

func TestPayout_CancelledSessionPaysNothingWhileTheFriendWaits(t *testing.T) {
	f := newFixture(t, 10, 10, 100)
	ctx := context.Background()
	code := f.issue(t)
	friend := f.friend(t, code, 3000, "friend-"+f.suffix+"@example.com")
	rw := f.rewardOf(t, friend)
	f.exec(t, `UPDATE sessions SET status = 'cancelled' WHERE id = $1`, f.session)
	if err := f.svc.Payout(ctx, rw.ID); err != nil {
		t.Fatal(err)
	}
	if w := f.waitOf(t, rw.ID); w.State != referral.StateDue || w.WaitReason != referral.WaitSessionCancelled || f.module.callCount() != 0 {
		t.Fatalf("cancelled session: %+v; want due session_cancelled and no refund", w)
	}
	// Nobody refunded the friend for 60 days after the planned date.
	f.moveSession(t, time.Now().Add(-61*24*time.Hour))
	f.due(t, rw.ID)
	if err := f.svc.Payout(ctx, rw.ID); err != nil {
		t.Fatal(err)
	}
	if got := f.rewardOf(t, friend); got.State != referral.StateCancelled || got.Reason != referral.ReasonSessionCancelled {
		t.Fatalf("reward %+v; want cancelled session_cancelled", got)
	}
}

func TestPayout_LowStripeBalanceKeepsItDueAndTellsTheOrganizerOnce(t *testing.T) {
	f := newFixture(t, 10, 10, 100)
	ctx := context.Background()
	f.module.setBalance(299, nil) // the cashback is 300
	code := f.issue(t)
	friend := f.friend(t, code, 3000, "friend-"+f.suffix+"@example.com")
	rw := f.rewardOf(t, friend)
	if err := f.svc.Payout(ctx, rw.ID); err != nil {
		t.Fatal(err)
	}
	w := f.waitOf(t, rw.ID)
	if w.State != referral.StateDue || w.WaitReason != referral.WaitBalance || w.BalanceSince == nil || f.module.callCount() != 0 {
		t.Fatalf("low balance: %+v, %d refund calls; want due waiting_balance, clock started, no refund", w, f.module.callCount())
	}
	if w.NextAttempt == nil || time.Until(*w.NextAttempt) < 3*time.Hour {
		t.Fatalf("next balance read at %v; want hours away", w.NextAttempt)
	}
	if _, err := f.svc.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	notices := f.notifier.noticesOf(referral.NoticeBalance)
	if len(notices) != 1 || notices[0].Count != 1 || notices[0].AmountMinor != 300 || notices[0].Currency != "EUR" ||
		f.organizerLetters(t, referral.NoticeBalance) != 1 {
		t.Fatalf("notices %+v, letters %d; want one notice and one letter: 1 cashback needs 300 EUR",
			notices, f.organizerLetters(t, referral.NoticeBalance))
	}
	// Another attempt, still too little: no second notice.
	since := w.BalanceSince
	f.due(t, rw.ID)
	if err := f.svc.Payout(ctx, rw.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(f.notifier.noticesOf(referral.NoticeBalance)); n != 1 || f.organizerLetters(t, referral.NoticeBalance) != 1 {
		t.Fatalf("%d notices after a retry; want still one", n)
	}
	if w := f.waitOf(t, rw.ID); w.BalanceSince == nil || !w.BalanceSince.Equal(*since) {
		t.Fatalf("the 30-day clock restarted: %v -> %v", since, w.BalanceSince)
	}
	// The organizer topped the balance up: paid.
	f.module.setBalance(10_000, nil)
	f.due(t, rw.ID)
	if err := f.svc.Payout(ctx, rw.ID); err != nil {
		t.Fatal(err)
	}
	if got := f.rewardOf(t, friend); got.State != referral.StatePaid {
		t.Fatalf("after the top-up: %+v; want paid", got)
	}
}

func TestPayout_BalanceReadFailureIsUnknownNotARefusal(t *testing.T) {
	f := newFixture(t, 10, 10, 100)
	ctx := context.Background()
	f.module.setBalance(0, errors.New("stripe: 503"))
	code := f.issue(t)
	friend := f.friend(t, code, 3000, "friend-"+f.suffix+"@example.com")
	rw := f.rewardOf(t, friend)
	if err := f.svc.Payout(ctx, rw.ID); err != nil {
		t.Fatal(err)
	}
	if w := f.waitOf(t, rw.ID); w.State != referral.StateDue || w.WaitReason != referral.WaitBalanceUnknown || f.module.callCount() != 0 {
		t.Fatalf("unknown balance: %+v; want due balance_unknown and no refund", w)
	}
	if _, err := f.svc.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(f.notifier.noticesOf(referral.NoticeBalance)); n != 0 {
		t.Fatalf("%d balance notices for a balance nobody could read", n)
	}
}

func TestPayout_ThirtyDaysWithoutBalanceIsNotPossibleWithLetters(t *testing.T) {
	f := newFixture(t, 10, 10, 100)
	ctx := context.Background()
	f.module.setBalance(0, nil)
	code := f.issue(t)
	friend := f.friend(t, code, 3000, "friend-"+f.suffix+"@example.com")
	rw := f.rewardOf(t, friend)
	if err := f.svc.Payout(ctx, rw.ID); err != nil {
		t.Fatal(err)
	}
	f.exec(t, `UPDATE referral_rewards SET balance_wait_since = now() - interval '31 days' WHERE id = $1`, rw.ID)
	f.due(t, rw.ID)
	if err := f.svc.Payout(ctx, rw.ID); err != nil {
		t.Fatal(err)
	}
	if got := f.rewardOf(t, friend); got.State != referral.StateNotPossible || got.Reason != referral.ReasonBalanceTimeout {
		t.Fatalf("after 30 days: %+v; want not_possible balance_timeout", got)
	}
	if f.module.callCount() != 0 {
		t.Fatal("a refund was sent although the balance never covered it")
	}
	// The referrer: "could not be returned, contact the organizer".
	if n := f.count(t, `SELECT count(*) FROM worker_jobs WHERE job_type = 'referral.cashback_email'
		AND payload->>'reward_id' = $1 AND payload->>'kind' = 'unavailable'`, rw.ID.String()); n != 1 {
		t.Fatalf("%d unavailable letters to the referrer; want 1", n)
	}
	// The organizer: settle with them yourself. Once.
	for i := 0; i < 2; i++ {
		if _, err := f.svc.Sweep(ctx); err != nil {
			t.Fatal(err)
		}
	}
	unpaid := f.notifier.noticesOf(referral.NoticeUnpaid)
	if len(unpaid) != 1 || unpaid[0].Reason != referral.ReasonBalanceTimeout || unpaid[0].Code != code.Code ||
		unpaid[0].FriendOrderNumber == 0 || f.organizerLetters(t, referral.NoticeUnpaid) != 1 {
		t.Fatalf("unpaid notices %+v, letters %d; want one of each", unpaid, f.organizerLetters(t, referral.NoticeUnpaid))
	}
	// Arena issues no promo code in its place (owner decision 03.11).
	if n := f.count(t, `SELECT count(*) FROM promo_codes WHERE org_id = $1 AND id <> $2`, f.org, code.PromoCodeID); n != 0 {
		t.Fatalf("%d promo codes written in place of the cashback", n)
	}
}

func TestPayout_StripeHoldsTheCashbackThenCancelsIt(t *testing.T) {
	f := newFixture(t, 10, 10, 100)
	ctx := context.Background()
	f.module.answers = []func(payments.RefundRequest) (payments.RefundResult, error){pendingAnswer}
	code := f.issue(t)
	friend := f.friend(t, code, 3000, "friend-"+f.suffix+"@example.com")
	rw := f.rewardOf(t, friend)
	if err := f.svc.Payout(ctx, rw.ID); err != nil {
		t.Fatal(err)
	}
	if got := f.rewardOf(t, friend); got.State != referral.StateSent {
		t.Fatalf("pending at Stripe: %+v; want sent", got)
	}
	// A fresh pending refund is a card refund on its way: no notice yet.
	if _, err := f.svc.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(f.notifier.noticesOf(referral.NoticePending)); n != 0 {
		t.Fatalf("%d pending notices for a refund sent a moment ago", n)
	}
	// Still pending eight days later: the organizer was told once, the
	// refund engine did NOT park it for a person (Stripe holds it up to 45
	// days), and the reward is still sent.
	f.exec(t, `UPDATE refunds SET approved_at = now() - interval '8 days', created_at = now() - interval '8 days',
	                              updated_at = now() - interval '1 day' WHERE org_id = $1`, f.org)
	for i := 0; i < 2; i++ {
		if _, err := f.svc.Sweep(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(f.notifier.noticesOf(referral.NoticePending)); n != 1 || f.organizerLetters(t, referral.NoticePending) != 1 {
		t.Fatalf("%d pending notices, %d letters; want one each", n, f.organizerLetters(t, referral.NoticePending))
	}
	if _, err := f.engine.Sweep(ctx, time.Now(), nil); err != nil {
		t.Fatalf("refund sweep: %v", err)
	}
	if st := f.refundState(t); st != refunds.StateProviderPending {
		t.Fatalf("refund %s after 8 days; want still provider_pending", st)
	}
	if got := f.rewardOf(t, friend); got.State != referral.StateSent {
		t.Fatalf("reward %+v while Stripe holds the refund; want sent", got)
	}

	// Day 45: Stripe cancels the refund (failure_reason insufficient_funds).
	var providerRefundID string
	if err := f.pool.QueryRow(ctx, `SELECT provider_refund_id FROM refunds WHERE org_id = $1`, f.org).Scan(&providerRefundID); err != nil {
		t.Fatal(err)
	}
	found, err := f.engine.ApplyProviderRefund(ctx, f.org, "stripe", providerRefundID,
		payments.RefundResult{ProviderRefundID: providerRefundID, Status: payments.RefundFailed, FailureCode: "insufficient_funds"})
	if err != nil || !found {
		t.Fatalf("ApplyProviderRefund: found=%v err=%v", found, err)
	}
	if st := f.refundState(t); st != refunds.StateFailed {
		t.Fatalf("refund %s after Stripe cancelled it; want failed (a cashback cancels no ticket)", st)
	}
	if _, err := f.svc.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if got := f.rewardOf(t, friend); got.State != referral.StateNotPossible || got.Reason != "insufficient_funds" {
		t.Fatalf("reward %+v; want not_possible insufficient_funds", got)
	}
	if n := f.count(t, `SELECT count(*) FROM worker_jobs WHERE job_type = 'referral.cashback_email'
		AND payload->>'reward_id' = $1 AND payload->>'kind' = 'unavailable'`, rw.ID.String()); n != 1 {
		t.Fatalf("%d unavailable letters to the referrer; want 1", n)
	}
	if _, err := f.svc.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if unpaid := f.notifier.noticesOf(referral.NoticeUnpaid); len(unpaid) != 1 || unpaid[0].Reason != "insufficient_funds" ||
		f.organizerLetters(t, referral.NoticeUnpaid) != 1 {
		t.Fatalf("unpaid notices %+v; want one, the provider's refusal", unpaid)
	}
}

func (f *fixture) refundState(t *testing.T) string {
	t.Helper()
	var st string
	if err := f.pool.QueryRow(context.Background(), `SELECT state FROM refunds WHERE org_id = $1`, f.org).Scan(&st); err != nil {
		t.Fatal(err)
	}
	return st
}
