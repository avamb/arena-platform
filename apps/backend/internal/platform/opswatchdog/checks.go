package opswatchdog

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/opsalert"
)

// loadCursor returns the stored (ts, id) cursor for key, defaulting to "now,
// empty" (and persisting that default) when no row exists yet — this is
// what guarantees a first run never replays history (see
// EnsureInitialCursors; this is the same fallback for the rare case a check
// runs before that seeding, or a new cursor key ships later).
func (r *runner) loadCursor(ctx context.Context, key string) (time.Time, string, error) {
	c, err := r.cursors.Get(ctx, key)
	if err != nil {
		return time.Time{}, "", err
	}
	if c == nil || c.TS == nil {
		now := r.clk.Now()
		if err := r.cursors.Set(ctx, key, Cursor{TS: &now, ID: ""}); err != nil {
			return time.Time{}, "", err
		}
		return now, "", nil
	}
	return *c.TS, c.ID, nil
}

func (r *runner) saveCursor(ctx context.Context, key string, ts time.Time, id string) error {
	return r.cursors.Set(ctx, key, Cursor{TS: &ts, ID: id})
}

// ─────────────────────────────────────────────────────────────────────────
// (a) Sales feed
// ─────────────────────────────────────────────────────────────────────────

func (r *runner) checkSalesFeed(ctx context.Context) error {
	ts, id, err := r.loadCursor(ctx, cursorSalesFeed)
	if err != nil {
		return fmt.Errorf("sales_feed: load cursor: %w", err)
	}

	const q = `
		SELECT o.id::text, o.system_id, org.name, ev.name, o.source, o.currency,
		       o.total, o.paid_at,
		       (SELECT count(*) FROM tickets t WHERE t.order_id = o.id AND t.status = 'active')
		  FROM orders o
		  JOIN organizations org ON org.id = o.org_id
		  JOIN events ev ON ev.id = o.event_id
		 WHERE o.status = 'paid'
		   AND o.paid_at IS NOT NULL
		   AND (o.paid_at > $1 OR (o.paid_at = $1 AND o.id::text > $2))
		 ORDER BY o.paid_at ASC, o.id ASC
		 LIMIT $3
	`
	rows, err := r.pool.Query(ctx, q, ts, id, checkRowLimit)
	if err != nil {
		return fmt.Errorf("sales_feed: query: %w", err)
	}
	defer rows.Close()

	var sales []saleLine
	var lastTS time.Time
	var lastID string
	for rows.Next() {
		var s saleLine
		var orderID string
		var systemID int64
		var paidAt time.Time
		if err := rows.Scan(&orderID, &systemID, &s.OrgName, &s.EventTitle, &s.Source,
			&s.Currency, &s.Total, &paidAt, &s.TicketCount); err != nil {
			return fmt.Errorf("sales_feed: scan: %w", err)
		}
		s.OrderNumber = fmt.Sprintf("%d", systemID)
		sales = append(sales, s)
		lastTS, lastID = paidAt, orderID
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("sales_feed: iterate: %w", err)
	}
	if len(sales) == 0 {
		return nil
	}

	if len(sales) > digestThreshold {
		if err := r.notifier.Send(ctx, formatSalesDigest(sales)); err != nil {
			r.logger.Warn("ops.watchdog: sales digest notify failed", "error", err.Error())
		}
	} else {
		for _, s := range sales {
			if err := r.notifier.Send(ctx, formatSaleMessage(s)); err != nil {
				r.logger.Warn("ops.watchdog: sale notify failed", "error", err.Error())
			}
		}
	}

	return r.saveCursor(ctx, cursorSalesFeed, lastTS, lastID)
}

// ─────────────────────────────────────────────────────────────────────────
// (b) Paid but no tickets issued
// ─────────────────────────────────────────────────────────────────────────

func (r *runner) checkPaidNoTickets(ctx context.Context) error {
	cutoff := r.clk.Now().Add(-paidNoTicketsAge)

	const q = `
		SELECT o.id::text, o.system_id, org.name, ev.name, o.paid_at
		  FROM orders o
		  JOIN organizations org ON org.id = o.org_id
		  JOIN events ev ON ev.id = o.event_id
		 WHERE o.status = 'paid'
		   AND o.paid_at IS NOT NULL
		   AND o.paid_at < $1
		   AND NOT EXISTS (
		         SELECT 1 FROM tickets t WHERE t.order_id = o.id AND t.status = 'active'
		       )
		 ORDER BY o.paid_at ASC
		 LIMIT $2
	`
	rows, err := r.pool.Query(ctx, q, cutoff, checkRowLimit)
	if err != nil {
		return fmt.Errorf("paid_no_tickets: query: %w", err)
	}
	defer rows.Close()

	var current []AlertInput
	for rows.Next() {
		var orderID, orgName, eventName string
		var systemID int64
		var paidAt time.Time
		if err := rows.Scan(&orderID, &systemID, &orgName, &eventName, &paidAt); err != nil {
			return fmt.Errorf("paid_no_tickets: scan: %w", err)
		}
		current = append(current, AlertInput{
			Fingerprint: fpPaidNoTickets + ":" + orderID,
			Severity:    "critical",
			Title:       "order paid but no tickets issued",
			Details: map[string]any{
				"order":     fmt.Sprintf("%d", systemID),
				"org":       orgName,
				"event":     eventName,
				"paid_at":   paidAt.Format(time.RFC3339),
				"age_check": paidNoTicketsAge.String(),
			},
		})
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("paid_no_tickets: iterate: %w", err)
	}

	return r.engine.Sync(ctx, fpPaidNoTickets, current, nil)
}

// ─────────────────────────────────────────────────────────────────────────
// (c) Payment succeeded but checkout not completed / manual_review
// ─────────────────────────────────────────────────────────────────────────

func (r *runner) checkPaymentNotCompleted(ctx context.Context) error {
	cutoff := r.clk.Now().Add(-paymentNotCompletedAge)

	const q = `
		SELECT pi.id::text, org.name, pi.amount, pi.currency, pi.succeeded_at
		  FROM payment_intents pi
		  JOIN organizations org ON org.id = pi.org_id
		  LEFT JOIN checkout_sessions cs ON cs.id = pi.checkout_session_id
		 WHERE pi.state = 'succeeded'
		   AND pi.succeeded_at IS NOT NULL
		   AND pi.succeeded_at < $1
		   AND (cs.id IS NULL OR cs.state <> 'completed')
		 ORDER BY pi.succeeded_at ASC
		 LIMIT $2
	`
	rows, err := r.pool.Query(ctx, q, cutoff, checkRowLimit)
	if err != nil {
		return fmt.Errorf("payment_not_completed: query: %w", err)
	}
	defer rows.Close()

	var current []AlertInput
	for rows.Next() {
		var piID, orgName, currency string
		var amount int64
		var succeededAt time.Time
		if err := rows.Scan(&piID, &orgName, &amount, &currency, &succeededAt); err != nil {
			return fmt.Errorf("payment_not_completed: scan: %w", err)
		}
		current = append(current, AlertInput{
			Fingerprint: fpPaymentNotDone + ":" + piID,
			Severity:    "critical",
			Title:       "payment succeeded but checkout not completed",
			Details: map[string]any{
				"org":          orgName,
				"amount":       opsalert.FormatMinorUnits(amount, currency),
				"succeeded_at": succeededAt.Format(time.RFC3339),
			},
		})
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("payment_not_completed: iterate: %w", err)
	}

	return r.engine.Sync(ctx, fpPaymentNotDone, current, nil)
}

func (r *runner) checkManualReview(ctx context.Context) error {
	const q = `
		SELECT 'checkout_session' AS kind, cs.id::text, org.name, cs.total, cs.currency
		  FROM checkout_sessions cs JOIN organizations org ON org.id = cs.org_id
		 WHERE cs.state = 'manual_review'
		UNION ALL
		SELECT 'order' AS kind, o.id::text, org.name, o.total, o.currency
		  FROM orders o JOIN organizations org ON org.id = o.org_id
		 WHERE o.status = 'manual_review'
		UNION ALL
		SELECT 'payment_intent' AS kind, pi.id::text, org.name, pi.amount, pi.currency
		  FROM payment_intents pi JOIN organizations org ON org.id = pi.org_id
		 WHERE pi.state = 'manual_review'
		LIMIT $1
	`
	rows, err := r.pool.Query(ctx, q, checkRowLimit)
	if err != nil {
		return fmt.Errorf("manual_review: query: %w", err)
	}
	defer rows.Close()

	var current []AlertInput
	for rows.Next() {
		var kind, id, orgName, currency string
		var amount *int64
		if err := rows.Scan(&kind, &id, &orgName, &amount, &currency); err != nil {
			return fmt.Errorf("manual_review: scan: %w", err)
		}
		details := map[string]any{"kind": kind, "org": orgName}
		if amount != nil {
			details["amount"] = opsalert.FormatMinorUnits(*amount, currency)
		}
		current = append(current, AlertInput{
			Fingerprint: fpManualReview + ":" + kind + ":" + id,
			Severity:    "critical",
			Title:       "requires manual review (late payment / stuck checkout)",
			Details:     details,
		})
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("manual_review: iterate: %w", err)
	}

	return r.engine.Sync(ctx, fpManualReview, current, nil)
}

// ─────────────────────────────────────────────────────────────────────────
// (d) Dead letters — worker_dead_letter, outbox_events, delivery_jobs
// ─────────────────────────────────────────────────────────────────────────

func (r *runner) checkDeadLetters(ctx context.Context) error {
	var errs []error
	if err := r.checkWorkerDeadLetters(ctx); err != nil {
		errs = append(errs, err)
	}
	if err := r.checkOutboxDeadLetters(ctx); err != nil {
		errs = append(errs, err)
	}
	if err := r.checkDeliveryFailures(ctx); err != nil {
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

func (r *runner) checkWorkerDeadLetters(ctx context.Context) error {
	ts, id, err := r.loadCursor(ctx, cursorDeadLetterWorker)
	if err != nil {
		return fmt.Errorf("dead_letter_worker_jobs: load cursor: %w", err)
	}
	const q = `
		SELECT id::text, job_type, attempts, last_error, failed_at
		  FROM worker_dead_letter
		 WHERE failed_at > $1 OR (failed_at = $1 AND id::text > $2)
		 ORDER BY failed_at ASC, id ASC
		 LIMIT $3
	`
	rows, err := r.pool.Query(ctx, q, ts, id, checkRowLimit)
	if err != nil {
		return fmt.Errorf("dead_letter_worker_jobs: query: %w", err)
	}
	defer rows.Close()

	var ids []string
	var lastTS time.Time
	var lastID string
	count := 0
	for rows.Next() {
		var rowID, jobType string
		var lastError *string
		var attempts int
		var failedAt time.Time
		if err := rows.Scan(&rowID, &jobType, &attempts, &lastError, &failedAt); err != nil {
			return fmt.Errorf("dead_letter_worker_jobs: scan: %w", err)
		}
		count++
		lastTS, lastID = failedAt, rowID
		errText := ""
		if lastError != nil {
			errText = opsalert.Truncate(opsalert.ScrubEmails(*lastError), 200)
		}
		if count <= digestThreshold {
			if err := r.notifier.Send(ctx, formatDeadLetterMessage("worker_jobs", rowID, jobType, errText)); err != nil {
				r.logger.Warn("ops.watchdog: dead-letter notify failed", "error", err.Error())
			}
		}
		ids = append(ids, rowID)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("dead_letter_worker_jobs: iterate: %w", err)
	}
	if count == 0 {
		return nil
	}
	if count > digestThreshold {
		if err := r.notifier.Send(ctx, formatDeadLetterDigest("worker_jobs", count, ids)); err != nil {
			r.logger.Warn("ops.watchdog: dead-letter digest notify failed", "error", err.Error())
		}
	}
	return r.saveCursor(ctx, cursorDeadLetterWorker, lastTS, lastID)
}

func (r *runner) checkOutboxDeadLetters(ctx context.Context) error {
	ts, id, err := r.loadCursor(ctx, cursorDeadLetterOutbox)
	if err != nil {
		return fmt.Errorf("dead_letter_outbox_events: load cursor: %w", err)
	}
	const q = `
		SELECT id::text, event_type, last_error, dead_lettered_at
		  FROM outbox_events
		 WHERE dead_lettered_at IS NOT NULL
		   AND (dead_lettered_at > $1 OR (dead_lettered_at = $1 AND id::text > $2))
		 ORDER BY dead_lettered_at ASC, id ASC
		 LIMIT $3
	`
	rows, err := r.pool.Query(ctx, q, ts, id, checkRowLimit)
	if err != nil {
		return fmt.Errorf("dead_letter_outbox_events: query: %w", err)
	}
	defer rows.Close()

	var ids []string
	var lastTS time.Time
	var lastID string
	count := 0
	for rows.Next() {
		var rowID, eventType string
		var lastError *string
		var deadAt time.Time
		if err := rows.Scan(&rowID, &eventType, &lastError, &deadAt); err != nil {
			return fmt.Errorf("dead_letter_outbox_events: scan: %w", err)
		}
		count++
		lastTS, lastID = deadAt, rowID
		errText := ""
		if lastError != nil {
			errText = opsalert.Truncate(opsalert.ScrubEmails(*lastError), 200)
		}
		if count <= digestThreshold {
			if err := r.notifier.Send(ctx, formatDeadLetterMessage("outbox_events", rowID, eventType, errText)); err != nil {
				r.logger.Warn("ops.watchdog: dead-letter notify failed", "error", err.Error())
			}
		}
		ids = append(ids, rowID)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("dead_letter_outbox_events: iterate: %w", err)
	}
	if count == 0 {
		return nil
	}
	if count > digestThreshold {
		if err := r.notifier.Send(ctx, formatDeadLetterDigest("outbox_events", count, ids)); err != nil {
			r.logger.Warn("ops.watchdog: dead-letter digest notify failed", "error", err.Error())
		}
	}
	return r.saveCursor(ctx, cursorDeadLetterOutbox, lastTS, lastID)
}

func (r *runner) checkDeliveryFailures(ctx context.Context) error {
	ts, id, err := r.loadCursor(ctx, cursorDeadLetterDelivery)
	if err != nil {
		return fmt.Errorf("dead_letter_delivery_jobs: load cursor: %w", err)
	}
	// recipient_email is deliberately NOT selected — it is PII and must
	// never reach a Telegram message.
	const q = `
		SELECT id::text, ticket_id::text, last_error, updated_at
		  FROM delivery_jobs
		 WHERE status = 'failed'
		   AND (updated_at > $1 OR (updated_at = $1 AND id::text > $2))
		 ORDER BY updated_at ASC, id ASC
		 LIMIT $3
	`
	rows, err := r.pool.Query(ctx, q, ts, id, checkRowLimit)
	if err != nil {
		return fmt.Errorf("dead_letter_delivery_jobs: query: %w", err)
	}
	defer rows.Close()

	var ids []string
	var lastTS time.Time
	var lastID string
	count := 0
	for rows.Next() {
		var rowID, ticketID string
		var lastError *string
		var updatedAt time.Time
		if err := rows.Scan(&rowID, &ticketID, &lastError, &updatedAt); err != nil {
			return fmt.Errorf("dead_letter_delivery_jobs: scan: %w", err)
		}
		count++
		lastTS, lastID = updatedAt, rowID
		errText := ""
		if lastError != nil {
			errText = opsalert.Truncate(opsalert.ScrubEmails(*lastError), 200)
		}
		if count <= digestThreshold {
			if err := r.notifier.Send(ctx, formatDeadLetterMessage("delivery_jobs (ticket email)", rowID, "ticket:"+ticketID, errText)); err != nil {
				r.logger.Warn("ops.watchdog: dead-letter notify failed", "error", err.Error())
			}
		}
		ids = append(ids, rowID)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("dead_letter_delivery_jobs: iterate: %w", err)
	}
	if count == 0 {
		return nil
	}
	if count > digestThreshold {
		if err := r.notifier.Send(ctx, formatDeadLetterDigest("delivery_jobs", count, ids)); err != nil {
			r.logger.Warn("ops.watchdog: dead-letter digest notify failed", "error", err.Error())
		}
	}
	return r.saveCursor(ctx, cursorDeadLetterDelivery, lastTS, lastID)
}

// ─────────────────────────────────────────────────────────────────────────
// (e) Lag — pending worker_jobs age, undelivered outbox_events backlog
// ─────────────────────────────────────────────────────────────────────────

func (r *runner) checkLag(ctx context.Context) error {
	now := r.clk.Now()
	var current []AlertInput

	var oldestPending *time.Time
	if err := r.pool.QueryRow(ctx,
		`SELECT min(scheduled_at) FROM worker_jobs WHERE status = 'pending'`,
	).Scan(&oldestPending); err != nil {
		return fmt.Errorf("lag: worker_jobs query: %w", err)
	}
	if oldestPending != nil && now.Sub(*oldestPending) > pendingJobLagThreshold {
		current = append(current, AlertInput{
			Fingerprint: fpLag + ":worker_jobs",
			Severity:    "warn",
			Title:       "oldest pending worker_jobs row is stuck",
			Details: map[string]any{
				"oldest_pending_at": oldestPending.Format(time.RFC3339),
				"age":               now.Sub(*oldestPending).String(),
			},
		})
	}

	var outboxBacklog int64
	if err := r.pool.QueryRow(ctx,
		`SELECT count(*) FROM outbox_events WHERE processed_at IS NULL AND dead_lettered_at IS NULL`,
	).Scan(&outboxBacklog); err != nil {
		return fmt.Errorf("lag: outbox_events query: %w", err)
	}
	if outboxBacklog >= outboxBacklogThreshold {
		current = append(current, AlertInput{
			Fingerprint: fpLag + ":outbox_backlog",
			Severity:    "warn",
			Title:       "outbox_events backlog above threshold",
			Details: map[string]any{
				"backlog":   outboxBacklog,
				"threshold": outboxBacklogThreshold,
			},
		})
	}

	return r.engine.Sync(ctx, fpLag, current, nil)
}

// ─────────────────────────────────────────────────────────────────────────
// (f) Refunds
// ─────────────────────────────────────────────────────────────────────────

func (r *runner) checkRefundsFeed(ctx context.Context) error {
	ts, id, err := r.loadCursor(ctx, cursorRefundsFeed)
	if err != nil {
		return fmt.Errorf("refunds_feed: load cursor: %w", err)
	}

	const q = `
		SELECT r.id::text, org.name, r.amount, r.currency, r.settlement, r.state, r.requested_at
		  FROM refunds r
		  JOIN organizations org ON org.id = r.org_id
		 WHERE r.requested_at > $1 OR (r.requested_at = $1 AND r.id::text > $2)
		 ORDER BY r.requested_at ASC, r.id ASC
		 LIMIT $3
	`
	rows, err := r.pool.Query(ctx, q, ts, id, checkRowLimit)
	if err != nil {
		return fmt.Errorf("refunds_feed: query: %w", err)
	}
	defer rows.Close()

	count := 0
	var lastTS time.Time
	var lastID string
	for rows.Next() {
		var rowID, orgName, currency, settlement, state string
		var amount int64
		var requestedAt time.Time
		if err := rows.Scan(&rowID, &orgName, &amount, &currency, &settlement, &state, &requestedAt); err != nil {
			return fmt.Errorf("refunds_feed: scan: %w", err)
		}
		count++
		lastTS, lastID = requestedAt, rowID
		if count <= digestThreshold {
			if err := r.notifier.Send(ctx, formatRefundMessage(orgName, amount, currency, settlement, state)); err != nil {
				r.logger.Warn("ops.watchdog: refund notify failed", "error", err.Error())
			}
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("refunds_feed: iterate: %w", err)
	}
	if count == 0 {
		return nil
	}
	if count > digestThreshold {
		if err := r.notifier.Send(ctx, formatRefundDigest(count)); err != nil {
			r.logger.Warn("ops.watchdog: refund digest notify failed", "error", err.Error())
		}
	}
	return r.saveCursor(ctx, cursorRefundsFeed, lastTS, lastID)
}

// ─────────────────────────────────────────────────────────────────────────
// (g) Heartbeat — once-daily "still alive" digest
// ─────────────────────────────────────────────────────────────────────────

func (r *runner) checkHeartbeat(ctx context.Context) error {
	now := r.clk.Now().UTC()
	if now.Hour() < r.heartbeatHourUTC {
		return nil
	}

	c, err := r.cursors.Get(ctx, cursorHeartbeat)
	if err != nil {
		return fmt.Errorf("heartbeat: load cursor: %w", err)
	}
	// allow:timeformat: date-only comparison, not a wire/API timestamp.
	today := now.Format("2006-01-02")
	// allow:timeformat: date-only comparison, not a wire/API timestamp.
	if c != nil && c.TS != nil && c.TS.UTC().Format("2006-01-02") == today {
		return nil // already sent today's heartbeat
	}

	rows, err := r.pool.Query(ctx,
		`SELECT currency, count(*), sum(total) FROM orders
		  WHERE status = 'paid' AND paid_at IS NOT NULL AND paid_at > $1
		  GROUP BY currency`,
		now.Add(-24*time.Hour),
	)
	if err != nil {
		return fmt.Errorf("heartbeat: sales query: %w", err)
	}
	totals := map[string]saleTotal{}
	for rows.Next() {
		var cur string
		var cnt int
		var sum int64
		if err := rows.Scan(&cur, &cnt, &sum); err != nil {
			rows.Close()
			return fmt.Errorf("heartbeat: scan sales: %w", err)
		}
		totals[cur] = saleTotal{Count: cnt, Sum: sum}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("heartbeat: iterate sales: %w", err)
	}
	rows.Close()

	var openAlerts int64
	if err := r.pool.QueryRow(ctx, `SELECT count(*) FROM ops_alerts WHERE resolved_at IS NULL`).Scan(&openAlerts); err != nil {
		return fmt.Errorf("heartbeat: open alerts query: %w", err)
	}

	if err := r.notifier.Send(ctx, formatHeartbeat(totals, int(openAlerts))); err != nil {
		r.logger.Warn("ops.watchdog: heartbeat notify failed", "error", err.Error())
	}

	return r.saveCursor(ctx, cursorHeartbeat, now, "")
}
