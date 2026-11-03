//go:build integration

// referral_review_integration_test.go — the review fixes of "Refer a friend":
// no code for an event without live dates, a friend order of another event,
// the friends limit under parallel payments, the percentages the code keeps,
// manual_review rewards, a paid reward whose refund was later reversed, and
// the surfaces that must not see a personal code.
package referral_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/referral"
)

func TestIssueCode_NoLiveSessionIsRefused(t *testing.T) {
	f := newFixture(t, 10, 10, 100)
	f.exec(t, "UPDATE sessions SET status = 'cancelled' WHERE id = $1", f.session)
	if _, _, err := referral.IssueCode(context.Background(), f.pool, f.referrerOrder); !errors.Is(err, referral.ErrNoSessions) {
		t.Fatalf("event without a live date: err = %v; want ErrNoSessions", err)
	}
	if n := f.count(t, "SELECT count(*) FROM promo_codes WHERE org_id = $1 AND origin = 'referral'", f.org); n != 0 {
		t.Fatalf("%d personal codes written for an event with no live date", n)
	}
}

func TestRecordFriendOrder_OtherEventIsCancelled(t *testing.T) {
	f := newFixture(t, 10, 10, 100)
	code := f.issue(t)
	other := uuid.New()
	f.exec(t, "INSERT INTO events (id, org_id, name, status, visibility) VALUES ($1, $2, 'Other', 'published', 'public')", other, f.org)
	id := uuid.New()
	pc := code.PromoCodeID
	f.addOrder(t, id, "paid", 3000, "friend-"+f.suffix+"@example.com", "Friend", &pc)
	f.exec(t, "UPDATE orders SET event_id = $2 WHERE id = $1", id, other)
	f.record(t, id)
	if rw := f.rewardOf(t, id); rw.State != referral.StateCancelled || rw.Reason != "other_event" || rw.Amount != 0 {
		t.Fatalf("reward = %+v; want cancelled other_event with nothing owed", rw)
	}
}

func TestRecordFriendOrder_MaxFriendsHoldsUnderParallelPayments(t *testing.T) {
	f := newFixture(t, 10, 10, 100) // the programme allows 5 friends
	code := f.issue(t)
	const n = 8
	ids := make([]uuid.UUID, n)
	for i := range ids {
		ids[i] = uuid.New()
		pc := code.PromoCodeID
		f.addOrder(t, ids[i], "paid", 1000, "friend"+string(rune('a'+i))+"-"+f.suffix+"@example.com", "Friend", &pc)
	}
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for _, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx := context.Background()
			tx, err := f.pool.BeginTx(ctx, pgx.TxOptions{})
			if err != nil {
				errs <- err
				return
			}
			defer func() { _ = tx.Rollback(ctx) }()
			ord, err := gen.New(tx).GetOrderByID(ctx, id, f.org)
			if err != nil {
				errs <- err
				return
			}
			if err := referral.RecordFriendOrderTx(ctx, tx, ord); err != nil {
				errs <- err
				return
			}
			errs <- tx.Commit(ctx)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("parallel record: %v", err)
		}
	}
	if got := f.count(t, "SELECT count(*) FROM referral_rewards WHERE promo_code_id = $1 AND state = 'due'", code.PromoCodeID); got != 5 {
		t.Errorf("%d due rewards; want exactly the 5 places of the code", got)
	}
	if got := f.count(t, "SELECT count(*) FROM referral_rewards WHERE promo_code_id = $1 AND state = 'capped' AND reason = 'max_friends'", code.PromoCodeID); got != n-5 {
		t.Errorf("%d rewards capped by max_friends; want %d", got, n-5)
	}
}

func TestRecordFriendOrder_TheCodeKeepsItsPercentsWhateverTheProgrammeBecomes(t *testing.T) {
	f := newFixture(t, 10, 10, 100)
	code := f.issue(t)
	if code.CashbackPct != 10 {
		t.Fatalf("code = %+v; want 10 %% cashback", code)
	}
	f.exec(t, "UPDATE referral_programs SET cashback_pct = 50, cashback_cap_pct = 20 WHERE id = $1", f.program)
	friend := f.friend(t, code, 3000, "friend-"+f.suffix+"@example.com")
	rw := f.rewardOf(t, friend)
	if rw.State != referral.StateDue || rw.Amount != 300 {
		t.Fatalf("reward = %+v; want due 300 (10 %% of 3000, as the code promised)", rw)
	}
	var cashbackPct, capPct int
	if err := f.pool.QueryRow(context.Background(), "SELECT cashback_pct, cap_pct FROM referral_rewards WHERE id = $1", rw.ID).Scan(&cashbackPct, &capPct); err != nil {
		t.Fatal(err)
	}
	if cashbackPct != 10 || capPct != 100 {
		t.Fatalf("reward copies %d/%d; want the code's 10/100", cashbackPct, capPct)
	}
}

func (f *fixture) providerCode(t *testing.T, friend uuid.UUID) string {
	t.Helper()
	var c *string
	if err := f.pool.QueryRow(context.Background(), "SELECT provider_code FROM referral_rewards WHERE friend_order_id = $1", friend).Scan(&c); err != nil {
		t.Fatal(err)
	}
	if c == nil {
		return ""
	}
	return *c
}

// A refund parked in manual_review is not a refusal: the reward says
// manual_review whatever failure code the engine recorded, the code is kept
// apart, no "could not be returned" letter goes out, and when the operator
// closes the refund the sweep moves the reward on.
func TestSweep_ManualReviewRefundKeepsTheReasonSendsNoLetterAndFollowsTheOperator(t *testing.T) {
	f := newFixture(t, 10, 10, 100)
	f.module.answers = []func(payments.RefundRequest) (payments.RefundResult, error){pendingAnswer}
	ctx := context.Background()
	code := f.issue(t)
	friend := f.friend(t, code, 3000, "friend-"+f.suffix+"@example.com")
	if err := f.svc.Payout(ctx, f.rewardOf(t, friend).ID); err != nil {
		t.Fatal(err)
	}
	f.exec(t, "UPDATE refunds SET state = 'manual_review', failure_code = 'budget_exceeded_after_unanswered_call' WHERE org_id = $1", f.org)
	if _, err := f.svc.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	rw := f.rewardOf(t, friend)
	if rw.State != referral.StateNotPossible || rw.Reason != "manual_review" || f.providerCode(t, friend) != "budget_exceeded_after_unanswered_call" {
		t.Fatalf("reward = %+v code %q; want not_possible manual_review with the engine's code apart", rw, f.providerCode(t, friend))
	}
	if n := len(f.jobs(t, referral.JobTypeCashbackEmail)); n != 0 {
		t.Fatalf("%d letters queued for a refund waiting for a person", n)
	}
	// The operator closes it as returned.
	f.exec(t, "UPDATE refunds SET state = 'succeeded', succeeded_at = now() WHERE org_id = $1", f.org)
	if _, err := f.svc.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if rw := f.rewardOf(t, friend); rw.State != referral.StatePaid {
		t.Fatalf("reward = %+v; want paid after the operator closed the refund as returned", rw)
	}
	if jobs := f.jobs(t, referral.JobTypeCashbackEmail); len(jobs) != 1 {
		t.Fatalf("%d letters; want the one \"sent\" letter", len(jobs))
	}
}

func TestSweep_ManualReviewClosedAsNotRefundedSendsTheUnavailableLetter(t *testing.T) {
	f := newFixture(t, 10, 10, 100)
	f.module.answers = []func(payments.RefundRequest) (payments.RefundResult, error){pendingAnswer}
	ctx := context.Background()
	code := f.issue(t)
	friend := f.friend(t, code, 3000, "friend-"+f.suffix+"@example.com")
	if err := f.svc.Payout(ctx, f.rewardOf(t, friend).ID); err != nil {
		t.Fatal(err)
	}
	f.exec(t, "UPDATE refunds SET state = 'manual_review', failure_code = 'x' WHERE org_id = $1", f.org)
	if _, err := f.svc.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	f.exec(t, "UPDATE refunds SET state = 'failed', failure_code = 'card_closed' WHERE org_id = $1", f.org)
	if _, err := f.svc.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	rw := f.rewardOf(t, friend)
	if rw.State != referral.StateNotPossible || rw.Reason != "card_closed" {
		t.Fatalf("reward = %+v; want not_possible card_closed", rw)
	}
	jobs := f.jobs(t, referral.JobTypeCashbackEmail)
	if len(jobs) != 1 || !strings.Contains(jobs[0], "unavailable") {
		t.Fatalf("letters = %v; want one \"unavailable\" letter", jobs)
	}
}

// A reward is paid, then the provider (or an operator) turns the refund into
// manual_review (reversed after success): the sweep takes it back to
// not_possible/manual_review and sends no second letter.
func TestSweep_PaidRewardWhoseRefundWasReversedGoesBackToManualReview(t *testing.T) {
	f := newFixture(t, 10, 10, 100)
	f.module.answers = []func(payments.RefundRequest) (payments.RefundResult, error){pendingAnswer}
	ctx := context.Background()
	code := f.issue(t)
	friend := f.friend(t, code, 3000, "friend-"+f.suffix+"@example.com")
	if err := f.svc.Payout(ctx, f.rewardOf(t, friend).ID); err != nil {
		t.Fatal(err)
	}
	f.exec(t, "UPDATE refunds SET state = 'succeeded', succeeded_at = now() WHERE org_id = $1", f.org)
	if _, err := f.svc.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if rw := f.rewardOf(t, friend); rw.State != referral.StatePaid {
		t.Fatalf("reward = %+v; want paid first", rw)
	}
	f.exec(t, "UPDATE refunds SET state = 'manual_review', failure_code = 'reversed_after_success' WHERE org_id = $1", f.org)
	if _, err := f.svc.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	rw := f.rewardOf(t, friend)
	if rw.State != referral.StateNotPossible || rw.Reason != "manual_review" || f.providerCode(t, friend) != "reversed_after_success" {
		t.Fatalf("reward = %+v code %q; want not_possible manual_review reversed_after_success", rw, f.providerCode(t, friend))
	}
	if n := len(f.jobs(t, referral.JobTypeCashbackEmail)); n != 1 {
		t.Fatalf("%d letters; want only the first \"sent\" one", n)
	}
}

// A personal code of an event is an unknown code to every surface but the
// widget's checkout: the gateway, the REST checkout and promo-validate read
// manual codes only.
func TestPromoLookups_ReferralCodeOnlyForTheWidget(t *testing.T) {
	f := newFixture(t, 10, 10, 100)
	code := f.issue(t)
	ctx := context.Background()
	q := gen.New(f.pool)
	if _, err := q.GetPromoCodeByCode(ctx, f.org, code.Code); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("GetPromoCodeByCode: err = %v; want no rows", err)
	}
	if _, err := q.GetPromoCodeByCodeCI(ctx, f.org, code.Code); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("GetPromoCodeByCodeCI: err = %v; want no rows", err)
	}
	row, err := q.GetPromoCodeByCodeCIAnyOrigin(ctx, f.org, code.Code)
	if err != nil || row.ID != code.PromoCodeID {
		t.Errorf("GetPromoCodeByCodeCIAnyOrigin = (%v, %v); want the personal code", row.ID, err)
	}
}
