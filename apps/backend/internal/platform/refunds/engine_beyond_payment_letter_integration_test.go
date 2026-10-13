//go:build integration

package refunds_test

import (
	"context"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/refunds"
)

// A refund an operator records as beyond the payment (the buyer was refunded
// twice) is announced to nobody: the buyer gets no letter about it, neither
// from the letter pass nor from the job's claim.
func TestEngine_BeyondPaymentRefundGetsNoBuyerLetter(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	ctx := context.Background()
	e := f.engine(&fakeModule{partial: true}, true)
	res, err := e.CreateBatch(ctx, f.batch("k-beyond-letter", true, refunds.Item{TicketID: f.tickets[0]}))
	if err != nil {
		t.Fatalf("CreateBatch: %v", err)
	}
	r := res.Refunds[0]
	// The refund was accepted late, after another refund had taken its
	// place: it waits for an operator, unsettled and unannounced.
	f.exec(t, `UPDATE refunds SET state = 'manual_review', failure_code = 'late_acceptance_over_budget', settled_at = NULL,
		buyer_notified_at = NULL WHERE id = $1`, r.ID)
	f.exec(t, `DELETE FROM worker_jobs WHERE job_type = $1 AND payload->>'batch_id' = $2::text`, refunds.JobTypeBuyerEmail, res.BatchID)
	f.exec(t, `INSERT INTO refunds (payment_intent_id, org_id, order_id, amount, currency, reason, state, settlement,
		provider, provider_refund_id, provider_status, succeeded_at, settled_at)
		SELECT id, org_id, $2, amount, currency, 'whole payment', 'succeeded', 'provider', 'pay03fake', 're_whole_'||gen_random_uuid(), 'succeeded', now(), now()
		FROM payment_intents WHERE id = $1`, f.payment, f.order)
	if _, err := e.Resolve(ctx, refunds.ResolveInput{OrgID: f.org, RefundID: r.ID, Outcome: refunds.ResolveRefundedBeyondPayment,
		Note: "dashboard shows both", Actor: refunds.Actor{Type: "user", ID: f.org.String()}}); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if _, err := e.RequeueBuyerNotices(ctx, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	// The letter job's claim takes exactly these rows.
	var claimable int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM refunds
		WHERE id = $1 AND buyer_notified_at IS NULL AND state = 'succeeded' AND settled_at IS NOT NULL`, r.ID).Scan(&claimable); err != nil {
		t.Fatal(err)
	}
	if claimable != 0 {
		t.Fatalf("the beyond-payment refund is still waiting for a buyer letter")
	}
	var requeued int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM worker_jobs WHERE job_type = $1 AND payload->>'batch_id' = $2::text
		AND created_at > (SELECT updated_at FROM refunds WHERE id = $3)`, refunds.JobTypeBuyerEmail, res.BatchID, r.ID).Scan(&requeued); err != nil {
		t.Fatal(err)
	}
	if requeued != 0 {
		t.Fatalf("the letter pass queued %d letter job(s) for a refund recorded beyond the payment", requeued)
	}
}
