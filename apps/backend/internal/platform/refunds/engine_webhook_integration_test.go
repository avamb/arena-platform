//go:build integration

// engine_webhook_integration_test.go — a provider's refund webhook applied to
// an accepted (pending) refund (PAY-05).
package refunds_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/refunds"
)

func pendingRefund(t *testing.T, f *fixture, key string) (*refunds.Engine, refunds.Refund) {
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
	return e, r
}

func TestEngine_RefundWebhookSucceededSettlesAPendingRefund(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	e, r := pendingRefund(t, f, "k-hook-ok")
	ctx := context.Background()

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
	e, r := pendingRefund(t, f, "k-hook-fail")
	ctx := context.Background()

	if found, err := e.ApplyProviderRefund(ctx, f.org, "pay03fake", *r.ProviderRefundID,
		payments.RefundResult{Status: payments.RefundFailed, FailureCode: "insufficient_funds"}); err != nil || !found {
		t.Fatalf("ApplyProviderRefund = %v %v", found, err)
	}
	got, _ := e.GetRefund(ctx, r.ID)
	if got.State != refunds.StateManualReview {
		t.Fatalf("state = %s; want manual_review (the ticket is already cancelled)", got.State)
	}
	// A later "succeeded" cannot silently overwrite the operator's queue.
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
