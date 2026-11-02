//go:build integration

// referral_second_review_integration_test.go — the second review of "Refer a
// friend": a code only for a payment that can return a part of itself, the
// letter only for a refusal the provider really gave, and the one correction
// letter for a cashback announced as paid that did not stay paid.
package referral_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/referral"
)

func TestIssueCode_OnlyForAPaymentThatCanReturnAPart(t *testing.T) {
	f := newFixture(t, 10, 10, 100)
	ctx := context.Background()
	for _, provider := range []string{"flitt", "manual", "nosuchprovider"} {
		f.exec(t, `UPDATE payment_intents SET provider = $2 WHERE id = $1`, f.payment, provider)
		if _, _, err := referral.IssueCode(ctx, f.pool, f.referrerOrder); !errors.Is(err, referral.ErrNotEligible) {
			t.Fatalf("provider %q: IssueCode err = %v; want ErrNotEligible", provider, err)
		}
	}
	// An order with no payment arena took cannot carry a code either.
	f.exec(t, `UPDATE payment_intents SET provider = 'stripe', state = 'failed' WHERE id = $1`, f.payment)
	if _, _, err := referral.IssueCode(ctx, f.pool, f.referrerOrder); !errors.Is(err, referral.ErrNotEligible) {
		t.Fatalf("no succeeded payment: IssueCode err = %v; want ErrNotEligible", err)
	}
	f.exec(t, `UPDATE payment_intents SET state = 'succeeded' WHERE id = $1`, f.payment)
	if _, created, err := referral.IssueCode(ctx, f.pool, f.referrerOrder); err != nil || !created {
		t.Fatalf("stripe payment: IssueCode = created %v, %v; want a new code", created, err)
	}
}

func TestRecordFriendOrder_ReferrerProviderCannotReturnAPart_NotPossibleWithoutALetter(t *testing.T) {
	f := newFixture(t, 10, 10, 100)
	code := f.issue(t)
	// The referrer's provider changed after the code was issued.
	f.exec(t, `UPDATE payment_intents SET provider = 'flitt' WHERE id = $1`, f.payment)
	friend := f.friend(t, code, 3000, "friend-"+f.suffix+"@example.com")
	rw := f.rewardOf(t, friend)
	if rw.State != referral.StateNotPossible || rw.Reason != referral.ReasonProviderUnsupported {
		t.Fatalf("reward = %+v; want not_possible provider_unsupported", rw)
	}
	if n := len(f.jobs(t, referral.JobTypeCashbackEmail)); n != 0 {
		t.Fatalf("%d referrer letters queued for a provider that cannot pay; want none", n)
	}
	if f.module.callCount() != 0 {
		t.Fatal("money was sent through a provider that cannot return a part")
	}
}

func TestLetters_CorrectionWhenAPaidCashbackDoesNotStay_OnceAndNotWhileInReview(t *testing.T) {
	f := newFixture(t, 10, 10, 100)
	ctx := context.Background()
	code := f.issue(t)
	friend := f.friend(t, code, 3000, "friend-"+f.suffix+"@example.com")
	rw := f.rewardOf(t, friend)
	if err := f.svc.Payout(ctx, rw.ID); err != nil {
		t.Fatalf("Payout: %v", err)
	}
	jobs := f.jobs(t, referral.JobTypeCashbackEmail)
	if len(jobs) != 1 || !strings.Contains(jobs[0], `"kind": "sent"`) {
		t.Fatalf("queued letters = %v; want the one sent letter", jobs)
	}
	cs := &captureSender{}
	h := f.cashbackHandler(cs)
	if err := h(ctx, []byte(jobs[0])); err != nil || len(cs.all()) != 1 {
		t.Fatalf("sent letter: %v, %d mails", err, len(cs.all()))
	}

	// The provider cancels the refund after accepting it: a person looks. The
	// referrer is told nothing yet.
	f.exec(t, `UPDATE refunds SET state = 'manual_review', failure_code = 'reversed_after_success' WHERE org_id = $1`, f.org)
	if _, err := f.svc.Sweep(ctx); err != nil {
		t.Fatalf("sweep 1: %v", err)
	}
	if got := f.rewardOf(t, friend); got.State != referral.StateNotPossible || got.Reason != "manual_review" {
		t.Fatalf("reward = %+v; want not_possible manual_review", got)
	}
	if n := len(f.jobs(t, referral.JobTypeCashbackEmail)); n != 1 {
		t.Fatalf("%d letters queued while the refund is in review; want still 1", n)
	}

	// The review ends in a refusal: ONE correction.
	f.exec(t, `UPDATE refunds SET state = 'failed', failure_code = 'card_closed' WHERE org_id = $1`, f.org)
	if _, err := f.svc.Sweep(ctx); err != nil {
		t.Fatalf("sweep 2: %v", err)
	}
	jobs = f.jobs(t, referral.JobTypeCashbackEmail)
	if len(jobs) != 2 || !strings.Contains(jobs[1], `"kind": "unavailable"`) || !strings.Contains(jobs[1], `"correction": true`) {
		t.Fatalf("queued letters = %v; want the sent letter and one unavailable correction", jobs)
	}
	// A repeated sweep queues no second correction.
	if _, err := f.svc.Sweep(ctx); err != nil {
		t.Fatalf("sweep 3: %v", err)
	}
	if n := len(f.jobs(t, referral.JobTypeCashbackEmail)); n != 2 {
		t.Fatalf("%d letters queued after a settled sweep; want 2", n)
	}

	// The handler sends it once, even when the job is delivered twice.
	for i := 0; i < 2; i++ {
		if err := h(ctx, []byte(jobs[1])); err != nil {
			t.Fatalf("correction run %d: %v", i, err)
		}
	}
	if n := len(cs.all()); n != 2 {
		t.Fatalf("%d letters sent in all; want the paid one and one correction", n)
	}
	if f.count(t, `SELECT count(*) FROM referral_rewards WHERE id = $1 AND correction_sent_at IS NOT NULL`, rw.ID) != 1 {
		t.Error("correction_sent_at is not set")
	}
	// The first letter's job, delivered again, still sends nothing.
	if err := h(ctx, []byte(jobs[0])); err != nil || len(cs.all()) != 2 {
		t.Fatalf("redelivered first letter: %v, %d mails; want no new mail", err, len(cs.all()))
	}
}

func TestLetters_NoUnavailableLetterForARefundThePlatformRefused(t *testing.T) {
	f := newFixture(t, 10, 10, 100)
	ctx := context.Background()
	code := f.issue(t)
	// No active ticket left to take the money from: the engine is never asked.
	f.exec(t, `UPDATE tickets SET status = 'cancelled' WHERE order_id = $1`, f.referrerOrder)
	friend := f.friend(t, code, 3000, "friend-"+f.suffix+"@example.com")
	if err := f.svc.Payout(ctx, f.rewardOf(t, friend).ID); err != nil {
		t.Fatal(err)
	}
	rw := f.rewardOf(t, friend)
	if rw.State != referral.StateNotPossible || rw.Reason != "no_refundable_ticket" {
		t.Fatalf("reward = %+v; want not_possible no_refundable_ticket", rw)
	}
	if n := len(f.jobs(t, referral.JobTypeCashbackEmail)); n != 0 {
		t.Fatalf("%d letters queued for a refusal the platform made; want none", n)
	}
}
