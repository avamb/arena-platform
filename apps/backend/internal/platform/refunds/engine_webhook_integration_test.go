//go:build integration

// engine_webhook_integration_test.go — a provider's refund webhook applied to
// an accepted (pending) refund (PAY-05).
package refunds_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/refunds"
)

func pendingRefund(t *testing.T, f *fixture, key string) (*refunds.Engine, refunds.Refund) {
	t.Helper()
	e, r, _ := pendingRefundWith(t, f, key)
	return e, r
}

func pendingRefundWith(t *testing.T, f *fixture, key string) (*refunds.Engine, refunds.Refund, *fakeModule) {
	t.Helper()
	m := &fakeModule{partial: true, answers: []func(payments.RefundRequest) (payments.RefundResult, error){pending}}
	e := f.engine(m, true)
	res, err := e.CreateBatch(context.Background(), f.batch(key, true, refunds.Item{TicketID: f.tickets[0]}))
	if err != nil {
		t.Fatalf("CreateBatch: %v", err)
	}
	r := res.Refunds[0]
	if r.State != refunds.StateProviderPending || r.ProviderRefundID == nil {
		t.Fatalf("refund = %+v; want provider_pending with a provider id", r)
	}
	return e, r, m
}

func TestEngine_RefundWebhookSucceededSettlesAPendingRefund(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	e, r, m := pendingRefundWith(t, f, "k-hook-ok")
	ctx := context.Background()
	m.lookup = payments.RefundResult{Status: payments.RefundSucceeded}

	found, err := e.ApplyProviderRefund(ctx, f.org, "pay03fake", *r.ProviderRefundID,
		payments.RefundResult{ProviderRefundID: *r.ProviderRefundID, Status: payments.RefundSucceeded})
	if err != nil || !found {
		t.Fatalf("ApplyProviderRefund = %v %v; want found", found, err)
	}
	got, _ := e.GetRefund(ctx, r.ID)
	if got.State != refunds.StateSucceeded {
		t.Fatalf("state = %s; want succeeded", got.State)
	}
	// A replay of the same delivery changes nothing.
	if found, err := e.ApplyProviderRefund(ctx, f.org, "pay03fake", *r.ProviderRefundID,
		payments.RefundResult{Status: payments.RefundSucceeded}); err != nil || !found {
		t.Fatalf("replay = %v %v", found, err)
	}
	again, _ := e.GetRefund(ctx, r.ID)
	if again.State != refunds.StateSucceeded {
		t.Fatalf("after replay: %s", again.State)
	}
}

func TestEngine_RefundWebhookFailureGoesToManualReview(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	e, r, m := pendingRefundWith(t, f, "k-hook-fail")
	ctx := context.Background()
	m.lookup = payments.RefundResult{Status: payments.RefundFailed, FailureCode: "insufficient_funds"}

	if found, err := e.ApplyProviderRefund(ctx, f.org, "pay03fake", *r.ProviderRefundID,
		payments.RefundResult{Status: payments.RefundFailed, FailureCode: "insufficient_funds"}); err != nil || !found {
		t.Fatalf("ApplyProviderRefund = %v %v", found, err)
	}
	got, _ := e.GetRefund(ctx, r.ID)
	if got.State != refunds.StateManualReview {
		t.Fatalf("state = %s; want manual_review (the ticket is already cancelled)", got.State)
	}
	// A later "succeeded" cannot silently overwrite the operator's queue.
	m.lookup = payments.RefundResult{Status: payments.RefundSucceeded}
	if _, err := e.ApplyProviderRefund(ctx, f.org, "pay03fake", *r.ProviderRefundID,
		payments.RefundResult{Status: payments.RefundSucceeded}); err != nil {
		t.Fatal(err)
	}
	if again, _ := e.GetRefund(ctx, r.ID); again.State != refunds.StateManualReview {
		t.Fatalf("a late success moved a manual_review row to %s", again.State)
	}
}

func TestEngine_RefundWebhookStaysInsideTheOrganizationAndProvider(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	e, r := pendingRefund(t, f, "k-hook-scope")
	ctx := context.Background()
	ok := payments.RefundResult{Status: payments.RefundSucceeded}

	if found, err := e.ApplyProviderRefund(ctx, uuid.New(), "pay03fake", *r.ProviderRefundID, ok); err != nil || found {
		t.Fatalf("another organization = %v %v; want not found", found, err)
	}
	if found, err := e.ApplyProviderRefund(ctx, f.org, "other", *r.ProviderRefundID, ok); err != nil || found {
		t.Fatalf("another provider = %v %v; want not found", found, err)
	}
	if found, err := e.ApplyProviderRefund(ctx, f.org, "pay03fake", "re_unknown", ok); err != nil || found {
		t.Fatalf("unknown refund id = %v %v; want not found", found, err)
	}
	if got, _ := e.GetRefund(ctx, r.ID); got.State != refunds.StateProviderPending {
		t.Fatalf("state = %s; nothing may have moved it", got.State)
	}
}

// The event body is a hint: the refund is read back from the provider before
// anything moves, and a read that fails is an error (the provider redelivers).
func TestEngine_RefundWebhookReadsTheRefundBack(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	e, r, m := pendingRefundWith(t, f, "k-hook-readback")
	ctx := context.Background()

	m.lookup = payments.RefundResult{Status: payments.RefundPending}
	if _, err := e.ApplyProviderRefund(ctx, f.org, "pay03fake", *r.ProviderRefundID,
		payments.RefundResult{Status: payments.RefundSucceeded}); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.GetRefund(ctx, r.ID); got.State != refunds.StateProviderPending {
		t.Fatalf("an event the provider does not confirm moved the refund to %s", got.State)
	}
	m.lookupErr = errors.New("provider unreachable")
	if found, err := e.ApplyProviderRefund(ctx, f.org, "pay03fake", *r.ProviderRefundID,
		payments.RefundResult{Status: payments.RefundSucceeded}); err == nil || !found {
		t.Fatalf("unreadable refund = %v %v; want found with an error", found, err)
	}
	if got, _ := e.GetRefund(ctx, r.ID); got.State != refunds.StateProviderPending {
		t.Fatalf("state = %s after an unreadable refund; want provider_pending", got.State)
	}
}

// A refund that already SUCCEEDED and is later reported failed or canceled by
// the provider goes to manual_review with an alert owed; an event the provider
// does not confirm changes nothing.
func TestEngine_RefundWebhookReversalOfASucceededRefund(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	e, r, m := pendingRefundWith(t, f, "k-hook-reversed")
	ctx := context.Background()
	m.lookup = payments.RefundResult{Status: payments.RefundSucceeded}
	if _, err := e.ApplyProviderRefund(ctx, f.org, "pay03fake", *r.ProviderRefundID,
		payments.RefundResult{Status: payments.RefundSucceeded}); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.GetRefund(ctx, r.ID); got.State != refunds.StateSucceeded {
		t.Fatalf("state = %s; want succeeded", got.State)
	}

	// The provider still says succeeded: a "failed" event is not believed.
	if _, err := e.ApplyProviderRefund(ctx, f.org, "pay03fake", *r.ProviderRefundID,
		payments.RefundResult{Status: payments.RefundFailed}); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.GetRefund(ctx, r.ID); got.State != refunds.StateSucceeded {
		t.Fatalf("an unconfirmed failure moved a succeeded refund to %s", got.State)
	}

	// The provider confirms the reversal.
	m.lookup = payments.RefundResult{Status: payments.RefundFailed, FailureCode: "canceled"}
	if found, err := e.ApplyProviderRefund(ctx, f.org, "pay03fake", *r.ProviderRefundID,
		payments.RefundResult{Status: payments.RefundFailed}); err != nil || !found {
		t.Fatalf("ApplyProviderRefund = %v %v", found, err)
	}
	got, _ := e.GetRefund(ctx, r.ID)
	if got.State != refunds.StateManualReview || got.FailureCode == nil || *got.FailureCode != "reversed_after_success" ||
		got.FailureReason == nil || !strings.Contains(*got.FailureReason, "canceled") {
		t.Fatalf("refund = %s %v %v; want manual_review reversed_after_success naming the provider's code", got.State, got.FailureCode, got.FailureReason)
	}
	var alertOwed bool
	if err := f.pool.QueryRow(ctx, `SELECT alert_due_at IS NOT NULL FROM refunds WHERE id = $1`, r.ID).Scan(&alertOwed); err != nil {
		t.Fatal(err)
	}
	if !alertOwed {
		t.Error("a reversed refund owes no alert")
	}
}

// Round-3 review R3-3: a reversal the provider confirmed moved no money, so
// the reversed refund no longer counts against the payment.
func TestEngine_ReversedRefundLeavesThePaymentsBudget(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	e, r, m := pendingRefundWith(t, f, "k-hook-reversed-budget")
	ctx := context.Background()
	m.lookup = payments.RefundResult{Status: payments.RefundSucceeded}
	if _, err := e.ApplyProviderRefund(ctx, f.org, "pay03fake", *r.ProviderRefundID, payments.RefundResult{Status: payments.RefundSucceeded}); err != nil {
		t.Fatal(err)
	}
	m.lookup = payments.RefundResult{Status: payments.RefundFailed, FailureCode: "canceled"}
	if _, err := e.ApplyProviderRefund(ctx, f.org, "pay03fake", *r.ProviderRefundID, payments.RefundResult{Status: payments.RefundFailed}); err != nil {
		t.Fatal(err)
	}
	// Half of the payment went back through a refund that names no ticket.
	f.exec(t, `INSERT INTO refunds (payment_intent_id, org_id, order_id, amount, currency, reason, state, settlement,
		provider, provider_refund_id, provider_status, succeeded_at, settled_at)
		SELECT id, org_id, $2, 2500, currency, 'order level', 'succeeded', 'provider', 'pay03fake', 're_half_'||gen_random_uuid(), 'succeeded', now(), now()
		FROM payment_intents WHERE id = $1`, f.payment, f.order)
	m.answers = append(m.answers, succeeded)
	if _, err := e.CreateBatch(ctx, f.batch("k-after-reversal", true, refunds.Item{TicketID: f.tickets[1]})); err != nil {
		t.Fatalf("refund of the other ticket after a reversal: %v (the reversed refund still counts)", err)
	}
}

// Round-3 review R3-9: what to do with a confirmed failure is decided on the
// row as it stands under the lock, not as it stood before the read-back: a
// refund another delivery settled meanwhile is reversed, not left as is.
func TestEngine_RefundWebhookDecidesOnTheLockedRow(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	e, r, m := pendingRefundWith(t, f, "k-hook-locked-row")
	ctx := context.Background()
	m.lookup = payments.RefundResult{Status: payments.RefundFailed, FailureCode: "canceled"}
	m.onLookup = func() {
		// Another delivery recorded the refund as succeeded while this one
		// was reading it back.
		f.exec(t, `UPDATE refunds SET state = 'succeeded', provider_status = 'succeeded', succeeded_at = now(), settled_at = now() WHERE id = $1`, r.ID)
	}
	if _, err := e.ApplyProviderRefund(ctx, f.org, "pay03fake", *r.ProviderRefundID, payments.RefundResult{Status: payments.RefundFailed}); err != nil {
		t.Fatal(err)
	}
	got, _ := e.GetRefund(ctx, r.ID)
	if got.State != refunds.StateManualReview || got.FailureCode == nil || *got.FailureCode != "reversed_after_success" {
		t.Fatalf("refund = %s %v; want the confirmed failure applied to the settled row as a reversal", got.State, got.FailureCode)
	}
}
