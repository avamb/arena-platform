package refunds

import (
	"context"
	"encoding/json"
	"time"

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
// a failure is logged, and refund.sweep (requeueBuyerNotices) asks again for
// any operation whose settled refunds are still unmailed with no job on its
// way. The job itself is idempotent (it claims the refunds it mails). A
// refund without an operation (the flat routes, the ticket-cancel route)
// sends no letter, and neither does an operation created with
// notify_buyer=false.
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
			WHERE EXISTS (SELECT 1 FROM refund_batches WHERE id = $4::text::uuid AND notify_buyer)
			  AND NOT EXISTS (
			    SELECT 1 FROM worker_jobs
			    WHERE job_type = $1 AND status = 'pending' AND payload->>'batch_id' = $4::text)`,
			JobTypeBuyerEmail, payload, buyerNoticeDelay, r.BatchID.String())
		return err
	})
	if err != nil {
		e.logger.Error("refunds: queueing the buyer letter failed", "refund_id", r.ID.String(), "error", err.Error())
	}
}

// Re-queue limits: a letter that keeps failing is tried a few more times over
// hours, not every pass for ever.
const (
	buyerNoticeSettleGrace  = 3 * time.Minute
	buyerNoticeRequeueAfter = 15 * time.Minute
	buyerNoticeMaxJobs      = 6
)

// RequeueBuyerNotices exposes the sweep section to tests. It queues the letter again for operations whose accepted,
// settled refunds are still unmailed while no job is on its way: the job
// exhausted its attempts during a mail outage, the first enqueue failed after
// settling committed, or a worker died between claiming the refunds and
// sending. finish is the only other place that queues the letter and it does
// not run again for a settled refund, so without this pass such a letter is
// lost for good.
func (e *Engine) RequeueBuyerNotices(ctx context.Context, now time.Time) (int, error) {
	return e.requeueBuyerNotices(ctx, now)
}

func (e *Engine) requeueBuyerNotices(ctx context.Context, now time.Time) (int, error) {
	var queued int
	err := e.inTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT DISTINCT r.batch_id
			FROM   refunds r
			JOIN   refund_batches b ON b.id = r.batch_id AND b.notify_buyer
			WHERE  r.state = 'succeeded' AND r.settled_at IS NOT NULL AND r.settled_at < $1
			  AND  r.buyer_notified_at IS NULL
			  AND  (cardinality($2::uuid[]) = 0 OR r.org_id = ANY($2::uuid[]))
			  AND  NOT EXISTS (SELECT 1 FROM worker_jobs j
			         WHERE j.job_type = $3 AND j.payload->>'batch_id' = r.batch_id::text
			           AND (j.status IN ('pending', 'claimed') OR j.created_at > $4))
			  AND  (SELECT count(*) FROM worker_jobs j
			         WHERE j.job_type = $3 AND j.payload->>'batch_id' = r.batch_id::text) < $5
			LIMIT 50`,
			now.Add(-buyerNoticeSettleGrace), e.scopeOrgs(), JobTypeBuyerEmail, now.Add(-buyerNoticeRequeueAfter), buyerNoticeMaxJobs)
		if err != nil {
			return err
		}
		var batches []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			batches = append(batches, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, id := range batches {
			payload, _ := json.Marshal(BuyerEmailPayload{BatchID: id})
			if _, err := tx.Exec(ctx, `INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
				VALUES ($1, $2::jsonb, 5, 'pending', now())`, JobTypeBuyerEmail, payload); err != nil {
				return err
			}
			queued++
		}
		return nil
	})
	return queued, err
}
