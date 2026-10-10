// sweep.go — refund.sweep (spec 36 §7), the self-scheduling worker job that
// finishes what a request could not. One pass:
//
//  1. STUCK: an engine refund still provider_pending 24 hours after it was
//     APPROVED (not created: a refund may wait days for its approval) goes
//     to manual_review — under the payment's advisory lock, and never while
//     a provider call of it is in flight (provider_attempted_at younger than
//     CallStaleAfter). A call that answers after the park still records the
//     provider's answer (drive.go apply, "late answer").
//  2. RETRY: one with no provider refund id whose last call started more
//     than a minute ago is called again — same idempotency key, so the
//     provider answers the first attempt's refund if there was one.
//  3. LOOKUP: one the provider accepted as pending is read back through the
//     module's RefundLookup every ten minutes; a refund the provider later
//     reports failed already cost its ticket, so it goes to manual_review
//     ("money did not go back"), never silently to failed.
//  4. REPAIR: an accepted refund whose ticket is still active (a crash
//     between the outcome commit and the cancellation) gets its ticket
//     cancelled; after maxRepairAttempts failures it is parked.
//  5. ALERTS: every engine refund in manual_review whose ops alert was not
//     sent yet (review_alerted_at) is announced once — ids, amount and the
//     failure code, never buyer data. Whichever process moved the row, the
//     alert comes from here (arena-worker holds the ops bot).
//
// A pass is BOUNDED (PAY-03 review M4/M5): a context deadline well under the
// worker's 5-minute stale-claim timeout, at most maxCalls provider calls,
// candidates ordered so the longest-waiting go first and at most one call
// per payment per pass, so one large batch cannot starve the others. It
// only touches rows the engine created (provider IS NOT NULL).
package refunds

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
)

// JobType is the worker_jobs.job_type of the sweep.
const JobType = "refund.sweep"

// Sweep timings and bounds (spec §7 and §14 question 3: 24 hours, ops bot).
const (
	DefaultSweepInterval = time.Minute
	LookupAfter          = 10 * time.Minute
	StuckAfter           = 24 * time.Hour
	// DefaultSweepPassTimeout keeps a pass well under the worker's 5-minute
	// stale-claim timeout, so a slow pass is never re-run concurrently.
	DefaultSweepPassTimeout = 3 * time.Minute
	// DefaultSweepMaxCalls caps the provider calls (retries + lookups) of
	// one pass.
	DefaultSweepMaxCalls = 40
	maxRepairAttempts    = 10
	sweepBatch           = 50
	lookupsPerPayment    = 3
)

// Notifier is the ops alert channel (opsalert.Notifier fits).
type Notifier interface {
	Send(ctx context.Context, text string) error
}

// SweepReport counts what one pass did.
type SweepReport struct {
	Stuck, Retried, LookedUp, Repaired, Alerted int
	// Calls is the number of provider calls the pass made.
	Calls int
}

// scopeSQL is the organization filter (tests only, see Options.SweepOrgs).
const scopeSQL = ` AND (cardinality($2::uuid[]) = 0 OR org_id = ANY($2::uuid[]))`

// Sweep runs one bounded pass. now is the clock (tests move it).
func (e *Engine) Sweep(ctx context.Context, now time.Time, notifier Notifier) (SweepReport, error) {
	ctx, cancel := context.WithTimeout(ctx, e.sweepPassTimeout)
	defer cancel()
	var rep SweepReport
	budget := e.sweepMaxCalls

	stuck, err := e.parkStuck(ctx, now)
	if err != nil {
		return rep, fmt.Errorf("refunds: park stuck refunds: %w", err)
	}
	rep.Stuck = stuck

	retry, err := e.selectIDs(ctx, `SELECT id FROM (
		SELECT id, provider_attempted_at, created_at,
		       row_number() OVER (PARTITION BY payment_intent_id
		                          ORDER BY provider_attempted_at NULLS FIRST, created_at) AS rn
		FROM refunds
		WHERE settlement = 'provider' AND provider IS NOT NULL AND state = 'provider_pending'
		  AND provider_refund_id IS NULL AND COALESCE(provider_attempted_at, created_at) <= $1`+scopeSQL+`) c
		WHERE rn = 1 ORDER BY provider_attempted_at NULLS FIRST, created_at LIMIT `+strconv.Itoa(sweepBatch),
		now.Add(-CallStaleAfter), e.scopeOrgs())
	if err != nil {
		return rep, fmt.Errorf("refunds: select retries: %w", err)
	}
	for _, id := range retry {
		if budget <= 0 || ctx.Err() != nil {
			break
		}
		budget--
		rep.Calls++
		if _, derr := e.Drive(ctx, id); derr != nil {
			e.logger.Error("refunds: sweep retry failed", "refund_id", id.String(), "error", derr.Error())
			continue
		}
		rep.Retried++
	}

	lookup, err := e.selectIDs(ctx, `SELECT id FROM (
		SELECT id, updated_at,
		       row_number() OVER (PARTITION BY payment_intent_id ORDER BY updated_at) AS rn
		FROM refunds
		WHERE settlement = 'provider' AND provider IS NOT NULL AND state = 'provider_pending'
		  AND provider_refund_id IS NOT NULL AND updated_at <= $1`+scopeSQL+`) c
		WHERE rn <= `+strconv.Itoa(lookupsPerPayment)+` ORDER BY updated_at LIMIT `+strconv.Itoa(sweepBatch),
		now.Add(-LookupAfter), e.scopeOrgs())
	if err != nil {
		return rep, fmt.Errorf("refunds: select lookups: %w", err)
	}
	for _, id := range lookup {
		if budget <= 0 || ctx.Err() != nil {
			break
		}
		budget--
		rep.Calls++
		if lerr := e.lookup(ctx, id); lerr != nil {
			e.logger.Error("refunds: sweep lookup failed", "refund_id", id.String(), "error", lerr.Error())
			continue
		}
		rep.LookedUp++
	}

	repaired, err := e.repair(ctx, now)
	if err != nil {
		return rep, fmt.Errorf("refunds: repair: %w", err)
	}
	rep.Repaired = repaired

	if notifier != nil {
		alerted, err := e.alertReviews(ctx, notifier)
		if err != nil {
			return rep, fmt.Errorf("refunds: alerts: %w", err)
		}
		rep.Alerted = alerted
	}
	return rep, nil
}

func (e *Engine) selectIDs(ctx context.Context, sql string, args ...any) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	err := e.inTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, sql, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id uuid.UUID
			if err := rows.Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return rows.Err()
	})
	return ids, err
}

func (e *Engine) refundAndPayment(ctx context.Context, id uuid.UUID) (Refund, Payment, error) {
	var (
		r   Refund
		pay Payment
	)
	err := e.inTx(ctx, func(tx pgx.Tx) error {
		var err error
		if r, err = getRefund(ctx, tx, id, false); err != nil {
			return err
		}
		pay, err = getPayment(ctx, tx, r.PaymentIntentID)
		return err
	})
	return r, pay, err
}

// parkStuck moves engine refunds pending for longer than StuckAfter SINCE
// THEIR APPROVAL to manual_review, one row at a time under its payment's
// advisory lock, skipping any whose provider call is in flight.
func (e *Engine) parkStuck(ctx context.Context, now time.Time) (int, error) {
	const stuckWhere = `settlement = 'provider' AND provider IS NOT NULL AND state = 'provider_pending'
		AND COALESCE(approved_at, created_at) <= $1
		AND (provider_attempted_at IS NULL OR provider_attempted_at <= now() - $3::interval)`
	ids, err := e.selectIDs(ctx, `SELECT id FROM refunds WHERE `+stuckWhere+scopeSQL+
		` ORDER BY created_at LIMIT `+strconv.Itoa(sweepBatch), now.Add(-StuckAfter), e.scopeOrgs(), intervalText(CallStaleAfter))
	if err != nil {
		return 0, err
	}
	parked := 0
	for _, id := range ids {
		if ctx.Err() != nil {
			break
		}
		var moved *Refund
		err := e.inTx(ctx, func(tx pgx.Tx) error {
			var piID *uuid.UUID
			if err := tx.QueryRow(ctx, `SELECT payment_intent_id FROM refunds WHERE id = $1`, id).Scan(&piID); err != nil || piID == nil {
				return err
			}
			if err := LockPayment(ctx, tx, *piID); err != nil {
				return err
			}
			r, err := scanRefund(tx.QueryRow(ctx, `UPDATE refunds
				SET state = 'manual_review', updated_at = now(),
				    failure_code = COALESCE(failure_code, 'stuck_provider_pending'),
				    failure_reason = COALESCE(failure_reason, 'the provider did not confirm this refund within 24 hours of its approval')
				WHERE id = $4 AND `+stuckWhere+scopeSQL+`
				RETURNING `+refundColumns, now.Add(-StuckAfter), e.scopeOrgs(), intervalText(CallStaleAfter), id))
			if errors.Is(err, pgx.ErrNoRows) {
				return nil // changed meanwhile (answered, or a call started)
			}
			if err != nil {
				return err
			}
			moved = &r
			return e.writeAudit(ctx, tx, audit.Event{ActorType: "system", Action: "v1.refund.manual_review",
				ResourceType: "refund", ResourceID: r.ID.String(),
				Metadata: map[string]any{"reason": "stuck_provider_pending", "order_id": uuidString(r.OrderID),
					"amount": r.Amount, "currency": r.Currency, "provider": deref(r.Provider)}})
		})
		if err != nil {
			return parked, err
		}
		if moved != nil {
			parked++
			e.logReview(*moved)
		}
	}
	return parked, nil
}

// repair cancels the tickets of accepted refunds a crash left active, with
// a bounded number of attempts per refund.
func (e *Engine) repair(ctx context.Context, now time.Time) (int, error) {
	ids, err := e.selectIDs(ctx, `SELECT r.id FROM refunds r
		WHERE r.settlement = 'provider' AND r.provider IS NOT NULL
		  AND (r.state = 'succeeded' OR (r.state = 'provider_pending' AND r.provider_refund_id IS NOT NULL))
		  AND ((r.cancel_ticket AND EXISTS (SELECT 1 FROM tickets t WHERE t.id = r.ticket_id AND t.status = 'active'))
		       OR (r.ticket_id IS NULL AND EXISTS (
		            SELECT 1 FROM payment_intents p JOIN tickets t ON t.checkout_session_id = p.checkout_session_id
		            WHERE p.id = r.payment_intent_id AND r.amount >= p.amount AND t.status = 'active')
		           AND NOT EXISTS (SELECT 1 FROM tickets t2 WHERE t2.refund_id = r.id)))
		  AND COALESCE(r.repair_attempted_at, r.updated_at) <= $1
		  AND (cardinality($2::uuid[]) = 0 OR r.org_id = ANY($2::uuid[]))
		ORDER BY COALESCE(r.repair_attempted_at, r.updated_at) LIMIT `+strconv.Itoa(sweepBatch),
		now.Add(-CallStaleAfter), e.scopeOrgs())
	if err != nil {
		return 0, err
	}
	repaired := 0
	for _, id := range ids {
		if ctx.Err() != nil {
			break
		}
		var attempts int
		if err := e.inTx(ctx, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `UPDATE refunds SET repair_attempts = repair_attempts + 1, repair_attempted_at = now()
				WHERE id = $1 RETURNING repair_attempts`, id).Scan(&attempts)
		}); err != nil {
			return repaired, err
		}
		r, pay, err := e.refundAndPayment(ctx, id)
		if err != nil {
			e.logger.Error("refunds: sweep repair load failed", "refund_id", id.String(), "error", err.Error())
			continue
		}
		if attempts > maxRepairAttempts {
			if err := e.parkRepair(ctx, r); err != nil {
				return repaired, err
			}
			continue
		}
		ticketIDs, ok := e.cancelTickets(ctx, r, pay)
		e.projectOrder(ctx, r, pay)
		if ok {
			repaired++
			if r.State == StateSucceeded {
				// The first settle never got this far: v1.ticket.refunded is
				// published here, once (consumers dedup by ticket).
				e.publish(ctx, r, pay, ticketIDs)
			}
		}
	}
	return repaired, nil
}

// parkRepair parks an accepted refund whose ticket cancellation keeps
// failing: the money went back, the ticket still admits — a human acts.
func (e *Engine) parkRepair(ctx context.Context, r Refund) error {
	var moved *Refund
	err := e.inTx(ctx, func(tx pgx.Tx) error {
		if err := LockPayment(ctx, tx, r.PaymentIntentID); err != nil {
			return err
		}
		u, err := scanRefund(tx.QueryRow(ctx, `UPDATE refunds SET state = 'manual_review', updated_at = now(),
			failure_code = 'ticket_cancellation_failed',
			failure_reason = 'the provider returned the money but the ticket could not be cancelled; cancel it by hand'
			WHERE id = $1 AND state IN ('succeeded', 'provider_pending') RETURNING `+refundColumns, r.ID))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		moved = &u
		return e.writeAudit(ctx, tx, audit.Event{ActorType: "system", Action: "v1.refund.manual_review",
			ResourceType: "refund", ResourceID: u.ID.String(),
			Metadata: map[string]any{"reason": "ticket_cancellation_failed", "order_id": uuidString(u.OrderID),
				"amount": u.Amount, "currency": u.Currency}})
	})
	if err == nil && moved != nil {
		e.logReview(*moved)
	}
	return err
}

// alertReviews sends one ops alert per engine refund in manual_review that
// has not been announced. Ids, amount, provider and code only — never the
// buyer. The row is marked BEFORE the send, so a crash loses one message
// rather than repeating it.
func (e *Engine) alertReviews(ctx context.Context, n Notifier) (int, error) {
	var rows []Refund
	err := e.inTx(ctx, func(tx pgx.Tx) error {
		res, err := tx.Query(ctx, `UPDATE refunds SET review_alerted_at = now()
			WHERE id IN (SELECT id FROM refunds
			             WHERE settlement = 'provider' AND provider IS NOT NULL AND state = 'manual_review'
			               AND review_alerted_at IS NULL AND (cardinality($1::uuid[]) = 0 OR org_id = ANY($1::uuid[]))
			             ORDER BY updated_at LIMIT `+strconv.Itoa(sweepBatch)+` FOR UPDATE SKIP LOCKED)
			RETURNING `+refundColumns, e.scopeOrgs())
		if err != nil {
			return err
		}
		rows, err = scanRefunds(res)
		return err
	})
	if err != nil {
		return 0, err
	}
	for _, r := range rows {
		text := fmt.Sprintf("Refund %s (%d %s via %s) needs manual review: %s. Order %s.",
			r.ID, r.Amount, r.Currency, deref(r.Provider), deref(r.FailureCode), uuidString(r.OrderID))
		if err := n.Send(ctx, text); err != nil {
			e.logger.Warn("refunds: ops alert failed", "refund_id", r.ID.String(), "error", err.Error())
		}
	}
	return len(rows), nil
}

// lookup reads one accepted-but-pending refund back from its provider.
func (e *Engine) lookup(ctx context.Context, id uuid.UUID) error {
	r, pay, err := e.refundAndPayment(ctx, id)
	if err != nil {
		return err
	}
	if r.State != StateProviderPending || r.ProviderRefundID == nil {
		return nil
	}
	desc, hasModule := e.modules.Descriptor(deref(r.Provider))
	var looker payments.RefundLookup
	if hasModule && desc.Capabilities.RefundLookup {
		if module, berr := e.modules.Build(ctx, r.OrgID, deref(r.Provider)); berr == nil {
			looker, _ = module.(payments.RefundLookup)
		}
	}
	if looker == nil {
		// No way to ask: wait for the webhook (PAY-05) or the 24-hour park.
		return e.touch(ctx, id)
	}
	callCtx, cancel := context.WithTimeout(ctx, e.callTimeout)
	defer cancel()
	res, err := looker.RefundStatus(callCtx, *r.ProviderRefundID)
	if err != nil {
		e.observe(outcomeUnknown)
		return e.touch(ctx, id)
	}
	var updated Refund
	changed := false
	err = e.inTx(ctx, func(tx pgx.Tx) error {
		if err := LockPayment(ctx, tx, pay.ID); err != nil {
			return err
		}
		cur, err := getRefund(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if cur.State != StateProviderPending {
			updated = cur
			return nil
		}
		var sql string
		args := []any{id, string(res.Status)}
		switch res.Status {
		case payments.RefundSucceeded:
			sql = `UPDATE refunds SET state = 'succeeded', succeeded_at = now(), provider_status = $2, updated_at = now()
			       WHERE id = $1 RETURNING ` + refundColumns
		case payments.RefundFailed:
			// The ticket was cancelled when the provider accepted; the money
			// did not go back after all. An operator decides — and is told
			// (alertReviews), PAY-03 review M1.
			sql = `UPDATE refunds SET state = 'manual_review', provider_status = $2, failure_code = $3,
			       failure_reason = $4, updated_at = now() WHERE id = $1 RETURNING ` + refundColumns
			code := res.FailureCode
			if code == "" {
				code = "failed_after_acceptance"
			}
			args = append(args, truncate(code, 100), truncate("the provider reported the refund failed after accepting it (the ticket is already cancelled, the money did not go back): "+res.FailureMessage, 500))
		default:
			sql = `UPDATE refunds SET provider_status = $2, updated_at = now() WHERE id = $1 RETURNING ` + refundColumns
		}
		if updated, err = scanRefund(tx.QueryRow(ctx, sql, args...)); err != nil {
			return err
		}
		changed = updated.State != cur.State
		if !changed {
			return nil
		}
		return e.writeAudit(ctx, tx, audit.Event{ActorType: "system", Action: "v1.refund.provider_status",
			ResourceType: "refund", ResourceID: id.String(),
			Metadata: map[string]any{"state": updated.State, "provider_status": string(res.Status),
				"order_id": uuidString(updated.OrderID), "amount": updated.Amount, "currency": updated.Currency}})
	})
	if err != nil {
		return err
	}
	switch {
	case changed && updated.State == StateSucceeded:
		e.observe(outcomeSucceeded)
		e.settle(ctx, updated, pay)
	case changed && updated.State == StateManualReview:
		e.logReview(updated)
	}
	return nil
}

func (e *Engine) touch(ctx context.Context, id uuid.UUID) error {
	return e.inTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE refunds SET updated_at = now() WHERE id = $1 AND state = 'provider_pending'`, id)
		return err
	})
}

// scopeOrgs is the organization filter of a sweep pass: empty (every
// organization) in production; tests set Options.SweepOrgs so a pass never
// touches another test package's refunds in a shared database.
func (e *Engine) scopeOrgs() []uuid.UUID {
	if e.sweepOrgs == nil {
		return []uuid.UUID{}
	}
	return e.sweepOrgs
}

// ─────────────────────────────────────────────────────────────────────────────
// Worker job plumbing (the reservationexpiry / opswatchdog pattern)
// ─────────────────────────────────────────────────────────────────────────────

// Scheduler enqueues the next sweep.
type Scheduler interface {
	ScheduleNext(ctx context.Context, at time.Time) error
}

// PGScheduler enqueues the next run into worker_jobs.
type PGScheduler struct{ pool *pgxpool.Pool }

// NewPGScheduler builds a PGScheduler.
func NewPGScheduler(pool *pgxpool.Pool) *PGScheduler { return &PGScheduler{pool: pool} }

// ScheduleNext inserts the next sweep job — unless one is already pending.
// A pass re-run after a stale claim (or two passes racing) must not grow a
// second chain of sweeps: there is only ever one pending refund.sweep.
func (s *PGScheduler) ScheduleNext(ctx context.Context, at time.Time) error {
	if _, err := s.pool.Exec(ctx, `INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
		SELECT $1, '{}', 3, 'pending', $2
		WHERE NOT EXISTS (SELECT 1 FROM worker_jobs WHERE job_type = $1 AND status = 'pending')`, JobType, at); err != nil {
		return fmt.Errorf("refunds: schedule next sweep: %w", err)
	}
	return nil
}

// SweepOptions wires the job handler.
type SweepOptions struct {
	Engine    *Engine
	Notifier  Notifier
	Scheduler Scheduler
	Interval  time.Duration
	Logger    *slog.Logger
	Now       func() time.Time
}

// NewSweepHandler returns the worker handler. It schedules the next run
// exactly once per pass, even after a failed pass, so one bad row cannot
// stop the sweep and a pass never forks a second chain.
func NewSweepHandler(o SweepOptions) func(ctx context.Context, payload []byte) error {
	if o.Interval <= 0 {
		o.Interval = DefaultSweepInterval
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return func(ctx context.Context, _ []byte) error {
		var passErr error
		if o.Engine == nil {
			passErr = errors.New("refunds: sweep has no engine")
		} else {
			rep, err := o.Engine.Sweep(ctx, o.Now(), o.Notifier)
			passErr = err
			if err == nil && (rep.Stuck+rep.Retried+rep.LookedUp+rep.Repaired+rep.Alerted) > 0 {
				o.Logger.Info("refund sweep complete", "stuck", rep.Stuck, "retried", rep.Retried,
					"looked_up", rep.LookedUp, "repaired", rep.Repaired, "alerted", rep.Alerted, "calls", rep.Calls)
			}
		}
		if passErr != nil {
			o.Logger.Error("refund sweep pass failed", "error", passErr.Error())
		}
		if o.Scheduler != nil {
			if err := o.Scheduler.ScheduleNext(ctx, o.Now().Add(o.Interval)); err != nil {
				return fmt.Errorf("refunds: schedule next run: %w", err)
			}
		}
		return nil
	}
}

// ScheduleInitialJob seeds the queue once at worker start-up when no sweep
// is pending or claimed.
func ScheduleInitialJob(ctx context.Context, pool *pgxpool.Pool) error {
	var n int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM worker_jobs WHERE job_type = $1 AND status IN ('pending', 'claimed')`,
		JobType).Scan(&n); err != nil {
		return fmt.Errorf("refunds: check initial sweep job: %w", err)
	}
	if n > 0 {
		return nil
	}
	if _, err := pool.Exec(ctx, `INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
		VALUES ($1, '{}', 3, 'pending', now())`, JobType); err != nil {
		return fmt.Errorf("refunds: enqueue initial sweep job: %w", err)
	}
	return nil
}
