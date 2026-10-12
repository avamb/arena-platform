package refunds

import (
	"context"
	"encoding/json"
	"fmt"
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
// hours, not every pass for ever. Only jobs that FAILED (exhausted their
// attempts) after the refund settled count against buyerNoticeMaxJobs: a job
// that found nothing to mail yet (the refund was still pending) or that sent
// the letter is not a failed send. Once the limit is reached the operator is
// alerted once (alertLostBuyerNotices) and the sweep stops queueing.
const (
	buyerNoticeSettleGrace  = 3 * time.Minute
	buyerNoticeRequeueAfter = 15 * time.Minute
	buyerNoticeMaxJobs      = 6
)

// failedBuyerJobsSQL counts the letter jobs of r's operation that failed
// after r settled ($3 the job type).
const failedBuyerJobsSQL = `(SELECT count(*) FROM worker_jobs j
	WHERE j.job_type = $3 AND j.payload->>'batch_id' = r.batch_id::text
	  AND j.status = 'failed' AND j.created_at >= r.settled_at)`

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
			  AND  r.buyer_notified_at IS NULL AND r.buyer_notice_given_up_at IS NULL
			  AND  (cardinality($2::uuid[]) = 0 OR r.org_id = ANY($2::uuid[]))
			  AND  NOT EXISTS (SELECT 1 FROM worker_jobs j
			         WHERE j.job_type = $3 AND j.payload->>'batch_id' = r.batch_id::text
			           AND (j.status IN ('pending', 'claimed') OR j.created_at > $4))
			  AND  `+failedBuyerJobsSQL+` < $5
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

// AlertLostBuyerNotices exposes the alert section to tests.
func (e *Engine) AlertLostBuyerNotices(ctx context.Context, n Notifier) (int, error) {
	return e.alertLostBuyerNotices(ctx, n, alertsPerPass)
}

// lostNotice is one operation whose letter was given up.
type lostNotice struct {
	batch, org, order string
	refunds           int
}

// alertLostBuyerNotices tells the operator, once per operation, that the
// buyer's letter could not be sent after buyerNoticeMaxJobs failed jobs: the
// money went back but the buyer was not told by e-mail. Only a confirmed
// delivery marks the refunds (buyer_notice_given_up_at), so an alert Telegram
// did not take is sent again by the next pass. The text carries ids only.
func (e *Engine) alertLostBuyerNotices(ctx context.Context, n Notifier, limit int) (int, error) {
	var lost []lostNotice
	err := e.inTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT r.batch_id::text, r.org_id::text, COALESCE(r.order_id::text, ''), count(*)
			FROM   refunds r
			JOIN   refund_batches b ON b.id = r.batch_id AND b.notify_buyer
			WHERE  r.state = 'succeeded' AND r.settled_at IS NOT NULL
			  AND  r.buyer_notified_at IS NULL AND r.buyer_notice_given_up_at IS NULL
			  AND  (cardinality($2::uuid[]) = 0 OR r.org_id = ANY($2::uuid[]))
			  AND  NOT EXISTS (SELECT 1 FROM worker_jobs j
			         WHERE j.job_type = $3 AND j.payload->>'batch_id' = r.batch_id::text
			           AND j.status IN ('pending', 'claimed'))
			  AND  `+failedBuyerJobsSQL+` >= $1
			GROUP  BY r.batch_id, r.org_id, r.order_id
			LIMIT  $4`,
			buyerNoticeMaxJobs, e.scopeOrgs(), JobTypeBuyerEmail, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var l lostNotice
			if err := rows.Scan(&l.batch, &l.org, &l.order, &l.refunds); err != nil {
				return err
			}
			lost = append(lost, l)
		}
		return rows.Err()
	})
	if err != nil {
		return 0, err
	}
	sent := 0
	for _, l := range lost {
		text := fmt.Sprintf("Refund letter to the buyer was NOT sent: refund operation %s of order %s (organization %s), %d refund(s). The money went back, but the e-mail failed %d times and is no longer retried. Check the mail provider and tell the buyer another way.",
			l.batch, l.order, l.org, l.refunds, buyerNoticeMaxJobs)
		if derr := deliver(ctx, n, text); derr != nil {
			e.logger.Warn("refunds: lost buyer letter alert not delivered; the next pass retries it", "batch_id", l.batch, "error", derr.Error())
			return sent, nil
		}
		if err := e.inTx(ctx, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE refunds SET buyer_notice_given_up_at = now()
				WHERE batch_id = $1::uuid AND state = 'succeeded' AND buyer_notified_at IS NULL AND buyer_notice_given_up_at IS NULL`, l.batch)
			return err
		}); err != nil {
			return sent, err
		}
		e.logger.Error("refunds: buyer letter given up after repeated failures", "batch_id", l.batch, "org_id", l.org)
		sent++
	}
	return sent, nil
}
