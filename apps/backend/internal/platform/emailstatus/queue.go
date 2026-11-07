package emailstatus

// queue.go — the undelivered buyer e-mails of one organization (EC-V5, spec 62
// §12.2): the web event center's refund queue lists, next to the failed
// refunds, every order whose buyer most likely has no letter, so the organizer
// can send it again. Same definition of "no letter" as FailedLetterOrders
// (the orders list's letter_failed), plus a ticket job that sits unsent.

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// JobStuckAfter is how long a ticket delivery job may stay pending or
// processing before the queue calls it stuck: the worker picks a job within
// seconds, a few retries stay inside the first minutes.
const JobStuckAfter = 20 * time.Minute

// jobStuckSQL is JobStuckAfter as a Postgres interval.
const jobStuckSQL = "20 minutes"

// queueWindow bounds how far back the queue looks, so old history of a
// long-lived organization does not grow the scan or the list forever.
const queueWindow = "60 days"

// Problems of an UndeliveredLetter.
const (
	ProblemFailed = "failed" // the relay or the buyer's server refused it
	ProblemStuck  = "stuck"  // accepted, but no delivery was ever reported, or the job is stuck
)

// UndeliveredLetter is one order with a letter the buyer most likely lacks.
type UndeliveredLetter struct {
	OrderID     uuid.UUID
	OrderNumber int64
	EventName   string
	// Kind is "ticket" for the order's ticket letters, else the kind of the
	// email_letters row (change, cancel, refund, ...).
	Kind    string
	Problem string
	// Tickets counts the tickets whose letter is affected (0 for non-ticket kinds).
	Tickets   int
	Recipient string
	Reason    string
	Since     time.Time
}

const undeliveredSQL = `
SELECT * FROM (
  SELECT o.id AS order_id, o.system_id, e.name AS event_name, 'ticket' AS kind,
         CASE WHEN bool_or(dj.status = 'failed' OR l.status = 'failed') THEN 'failed' ELSE 'stuck' END AS problem,
         count(DISTINCT dj.ticket_id)::int AS tickets,
         COALESCE(max(dj.recipient_email), max(l.recipient), max(o.buyer_email), '') AS recipient,
         COALESCE(max(dj.last_error) FILTER (WHERE dj.status = 'failed'), max(l.failure_reason), '') AS reason,
         min(COALESCE(l.failed_at, l.sent_at, dj.updated_at)) AS since
  FROM   orders o
  JOIN   events e ON e.id = o.event_id
  JOIN   order_items oi ON oi.order_id = o.id
  JOIN   delivery_jobs dj ON dj.ticket_id = oi.ticket_id
  LEFT JOIN email_letters l ON l.id = dj.email_letter_id
  WHERE  o.org_id = $1 AND NOT o.is_test
    AND  o.created_at > now() - '` + queueWindow + `'::interval
    AND  (dj.status = 'failed'
          OR (dj.status IN ('pending', 'processing') AND dj.updated_at < now() - '` + jobStuckSQL + `'::interval)
          OR (dj.status = 'sent' AND (l.status = 'failed' OR ` + stuckExpr + `)))
  GROUP  BY o.id, o.system_id, e.name
  UNION ALL
  SELECT o.id, o.system_id, e.name, l.kind,
         CASE WHEN l.status = 'failed' THEN 'failed' ELSE 'stuck' END,
         0, l.recipient, COALESCE(l.failure_reason, ''), COALESCE(l.failed_at, l.sent_at)
  FROM   email_letters l
  JOIN   orders o ON o.id = l.order_id AND o.org_id = $1
  JOIN   events e ON e.id = o.event_id
  WHERE  NOT o.is_test
    AND  l.sent_at > now() - '` + queueWindow + `'::interval
    AND  l.kind IN ('change', 'cancel', 'refund', 'referral_cashback', 'referral_unavailable')
    AND  (l.status = 'failed' OR ` + stuckExpr + `)
    AND  NOT EXISTS (
           SELECT 1 FROM email_letters n
           WHERE  n.order_id = o.id AND n.kind = l.kind AND n.status <> 'sending'
             AND  date_trunc('second', n.sent_at) > date_trunc('second', l.sent_at))
) q
ORDER BY since DESC, order_id
LIMIT $2`

// UndeliveredLetters lists, newest first, up to limit orders of the
// organization whose buyer most likely has no letter. Test orders are left out.
func UndeliveredLetters(ctx context.Context, db DB, orgID uuid.UUID, limit int) ([]UndeliveredLetter, error) {
	rows, err := db.Query(ctx, undeliveredSQL, orgID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UndeliveredLetter
	for rows.Next() {
		var u UndeliveredLetter
		if err := rows.Scan(&u.OrderID, &u.OrderNumber, &u.EventName, &u.Kind, &u.Problem,
			&u.Tickets, &u.Recipient, &u.Reason, &u.Since); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
