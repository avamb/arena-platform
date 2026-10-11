package refunds

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
)

// JobTypeBuyerEmail is the worker job that writes the buyer's letter about a
// refund operation (PAY-07). The handler lives in the delivery package,
// which owns templates, branding and the mail sender.
const JobTypeBuyerEmail = "refund.buyer_email"

// BuyerEmailPayload is the job payload: the operation whose accepted refunds
// the letter covers.
type BuyerEmailPayload struct {
	BatchID string `json:"batch_id"`
}

// buyerNoticeDelay lets the other refunds of an operation that is being
// driven right now reach their answer, so one letter covers them all.
const buyerNoticeDelay = "60 seconds"

// queueBuyerNotice asks for the letter of r's operation. It is best effort:
// a failure is logged and the next settle or repair pass asks again, and the
// job itself is idempotent (it claims the refunds it mails). A refund
// without an operation (the flat routes, the ticket-cancel route) sends no
// letter.
func (e *Engine) queueBuyerNotice(ctx context.Context, r Refund) {
	if r.BatchID == nil {
		return
	}
	payload, _ := json.Marshal(BuyerEmailPayload{BatchID: r.BatchID.String()})
	err := e.inTx(ctx, func(tx pgx.Tx) error {
		// One pending job per operation is enough: it reads whatever is
		// accepted and not yet mailed when it runs.
		_, err := tx.Exec(ctx, `
			INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
			SELECT $1, $2::jsonb, 5, 'pending', now() + $3::interval
			WHERE NOT EXISTS (
			    SELECT 1 FROM worker_jobs
			    WHERE job_type = $1 AND status = 'pending' AND payload->>'batch_id' = $4)`,
			JobTypeBuyerEmail, payload, buyerNoticeDelay, r.BatchID.String())
		return err
	})
	if err != nil {
		e.logger.Error("refunds: queueing the buyer letter failed", "refund_id", r.ID.String(), "error", err.Error())
	}
}
