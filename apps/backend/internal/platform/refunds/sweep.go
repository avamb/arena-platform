// sweep.go — refund.sweep (spec 36 §7), the self-scheduling worker job that
// finishes what a request could not:
//
//  1. STUCK: an engine refund still provider_pending 24 hours after it was
//     created goes to manual_review, with one ops alert (no buyer data).
//  2. RETRY: one with no provider refund id whose last call started more
//     than a minute ago is called again — same idempotency key, so the
//     provider answers the first attempt's refund if there was one.
//  3. LOOKUP: one the provider accepted as pending is read back through the
//     module's RefundLookup every ten minutes; a refund the provider later
//     reports failed already cost its ticket, so it goes to manual_review
//     ("money did not go back"), never silently to failed.
//  4. REPAIR: an accepted refund whose ticket is still active (a crash
//     between the outcome commit and the cancellation) gets its ticket
//     cancelled.
//
// It only touches rows the engine created (provider IS NOT NULL).
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

// Sweep timings (spec §7 and §14 question 3: 24 hours, ops bot).
const (
	DefaultSweepInterval = time.Minute
	LookupAfter          = 10 * time.Minute
	StuckAfter           = 24 * time.Hour
	sweepBatch           = 50
)

// Notifier is the ops alert channel (opsalert.Notifier fits).
type Notifier interface {
	Send(ctx context.Context, text string) error
}

// SweepReport counts what one pass did.
type SweepReport struct {
	Stuck, Retried, LookedUp, Repaired int
}

// Sweep runs one pass. now is the clock (tests move it).
func (e *Engine) Sweep(ctx context.Context, now time.Time, notifier Notifier) (SweepReport, error) {
	var rep SweepReport
	stuck, err := e.parkStuck(ctx, now)
	if err != nil {
		return rep, fmt.Errorf("refunds: park stuck refunds: %w", err)
	}
	rep.Stuck = len(stuck)
	for _, r := range stuck {
		e.alertStuck(ctx, notifier, r)
	}

	retry, err := e.selectIDs(ctx, `SELECT id FROM refunds
		WHERE settlement = 'provider' AND provider IS NOT NULL AND state = 'provider_pending'
		  AND provider_refund_id IS NULL AND COALESCE(provider_attempted_at, created_at) <= $1 AND (cardinality($2::uuid[]) = 0 OR org_id = ANY($2::uuid[]))
		ORDER BY created_at LIMIT `+strconv.Itoa(sweepBatch), now.Add(-CallStaleAfter), e.scopeOrgs())
	if err != nil {
		return rep, fmt.Errorf("refunds: select retries: %w", err)
	}
	for _, id := range retry {
		if _, derr := e.Drive(ctx, id); derr != nil {
			e.logger.Error("refunds: sweep retry failed", "refund_id", id.String(), "error", derr.Error())
			continue
		}
		rep.Retried++
	}

	lookup, err := e.selectIDs(ctx, `SELECT id FROM refunds
		WHERE settlement = 'provider' AND provider IS NOT NULL AND state = 'provider_pending'
		  AND provider_refund_id IS NOT NULL AND updated_at <= $1 AND (cardinality($2::uuid[]) = 0 OR org_id = ANY($2::uuid[]))
		ORDER BY updated_at LIMIT `+strconv.Itoa(sweepBatch), now.Add(-LookupAfter), e.scopeOrgs())
	if err != nil {
		return rep, fmt.Errorf("refunds: select lookups: %w", err)
	}
	for _, id := range lookup {
		if lerr := e.lookup(ctx, id); lerr != nil {
			e.logger.Error("refunds: sweep lookup failed", "refund_id", id.String(), "error", lerr.Error())
			continue
		}
		rep.LookedUp++
	}

	repair, err := e.selectIDs(ctx, `SELECT r.id FROM refunds r
		WHERE r.settlement = 'provider' AND r.provider IS NOT NULL
		  AND (r.state = 'succeeded' OR (r.state = 'provider_pending' AND r.provider_refund_id IS NOT NULL))
		  AND ((r.cancel_ticket AND EXISTS (SELECT 1 FROM tickets t WHERE t.id = r.ticket_id AND t.status = 'active'))
		       OR (r.ticket_id IS NULL AND EXISTS (
		            SELECT 1 FROM payment_intents p JOIN tickets t ON t.checkout_session_id = p.checkout_session_id
		            WHERE p.id = r.payment_intent_id AND r.amount >= p.amount AND t.status = 'active')
		           AND NOT EXISTS (SELECT 1 FROM tickets t2 WHERE t2.refund_id = r.id)))
		  AND r.updated_at <= $1 AND (cardinality($2::uuid[]) = 0 OR r.org_id = ANY($2::uuid[]))
		ORDER BY r.updated_at LIMIT `+strconv.Itoa(sweepBatch), now.Add(-CallStaleAfter), e.scopeOrgs())
	if err != nil {
		return rep, fmt.Errorf("refunds: select repairs: %w", err)
	}
	for _, id := range repair {
		r, pay, gerr := e.refundAndPayment(ctx, id)
		if gerr != nil {
			e.logger.Error("refunds: sweep repair load failed", "refund_id", id.String(), "error", gerr.Error())
			continue
		}
		e.cancelTickets(ctx, r, pay)
		e.projectOrder(ctx, r, pay)
		rep.Repaired++
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

// parkStuck moves engine refunds pending for longer than StuckAfter to
// manual_review.
func (e *Engine) parkStuck(ctx context.Context, now time.Time) ([]Refund, error) {
	var out []Refund
	err := e.inTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `UPDATE refunds
			SET state = 'manual_review', updated_at = now(),
			    failure_code = COALESCE(failure_code, 'stuck_provider_pending'),
			    failure_reason = COALESCE(failure_reason, 'the provider did not confirm this refund within 24 hours')
			WHERE id IN (SELECT id FROM refunds
			             WHERE settlement = 'provider' AND provider IS NOT NULL AND state = 'provider_pending'
			               AND created_at <= $1 AND (cardinality($2::uuid[]) = 0 OR org_id = ANY($2::uuid[])) ORDER BY created_at LIMIT `+strconv.Itoa(sweepBatch)+`
			             FOR UPDATE SKIP LOCKED)
			RETURNING `+refundColumns, now.Add(-StuckAfter), e.scopeOrgs())
		if err != nil {
			return err
		}
		if out, err = scanRefunds(rows); err != nil {
			return err
		}
		for _, r := range out {
			if err := e.writeAudit(ctx, tx, audit.Event{ActorType: "system", Action: "v1.refund.manual_review",
				ResourceType: "refund", ResourceID: r.ID.String(),
				Metadata: map[string]any{"reason": "stuck_provider_pending", "order_id": uuidString(r.OrderID),
					"amount": r.Amount, "currency": r.Currency, "provider": deref(r.Provider)}}); err != nil {
				return err
			}
		}
		return nil
	})
	return out, err
}

// alertStuck tells the operator. Ids, amount and provider only — never the
// buyer.
func (e *Engine) alertStuck(ctx context.Context, n Notifier, r Refund) {
	if n == nil {
		return
	}
	text := fmt.Sprintf("Refund %s (%d %s via %s) is in manual review: no confirmation from the provider. Order %s.",
		r.ID, r.Amount, r.Currency, deref(r.Provider), uuidString(r.OrderID))
	if err := n.Send(ctx, text); err != nil {
		e.logger.Warn("refunds: ops alert failed", "refund_id", r.ID.String(), "error", err.Error())
	}
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
			// did not go back after all. An operator decides.
			sql = `UPDATE refunds SET state = 'manual_review', provider_status = $2, failure_code = $3,
			       failure_reason = $4, updated_at = now() WHERE id = $1 RETURNING ` + refundColumns
			code := res.FailureCode
			if code == "" {
				code = "failed_after_acceptance"
			}
			args = append(args, truncate(code, 100), truncate("the provider reported the refund failed after accepting it: "+res.FailureMessage, 500))
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
	if changed && updated.State == StateSucceeded {
		e.observe(outcomeSucceeded)
		e.settle(ctx, updated, pay)
	}
	return nil
}

func (e *Engine) touch(ctx context.Context, id uuid.UUID) error {
	return e.inTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE refunds SET updated_at = now() WHERE id = $1 AND state = 'provider_pending'`, id)
		return err
	})
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

// ScheduleNext inserts the next sweep job.
func (s *PGScheduler) ScheduleNext(ctx context.Context, at time.Time) error {
	if _, err := s.pool.Exec(ctx, `INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
		VALUES ($1, '{}', 3, 'pending', $2)`, JobType, at); err != nil {
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

// NewSweepHandler returns the worker handler. It always schedules the next
// run, even after a failed pass, so one bad row cannot stop the sweep.
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
			if err == nil && (rep.Stuck+rep.Retried+rep.LookedUp+rep.Repaired) > 0 {
				o.Logger.Info("refund sweep complete", "stuck", rep.Stuck, "retried", rep.Retried,
					"looked_up", rep.LookedUp, "repaired", rep.Repaired)
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

// scopeOrgs is the organization filter of a sweep pass: empty (every
// organization) in production; tests set Options.SweepOrgs so a pass never
// touches another test package's refunds in a shared database.
func (e *Engine) scopeOrgs() []uuid.UUID {
	if e.sweepOrgs == nil {
		return []uuid.UUID{}
	}
	return e.sweepOrgs
}
