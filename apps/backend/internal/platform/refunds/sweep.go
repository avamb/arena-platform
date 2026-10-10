// sweep.go — refund.sweep (spec 36 §7), the self-scheduling worker job that
// finishes what a request could not. One pass:
//
//  1. STUCK: an engine refund with no provider refund id still
//     provider_pending 24 hours after it was APPROVED (not created: a refund
//     may wait days for its approval) goes to manual_review — under the
//     payment's advisory lock, and never while a provider call of it is in
//     flight (provider_attempted_at younger than CallStaleAfter). One the
//     provider ACCEPTED as pending (it carries a provider refund id) is not
//     stuck — the provider is still working on it — and is parked only
//     after PendingParkAfter (7 days). A call that answers after the park
//     still records the provider's answer (drive.go apply, "late answer").
//  2. RETRY: one with no provider refund id whose last call started more
//     than a minute ago is called again — same idempotency key, so the
//     provider answers the first attempt's refund if there was one. NEVER
//     after StuckAfter since approval: a provider keeps an idempotency key
//     for about a day (Stripe: 24 hours), and a later re-POST with the same
//     key could create a SECOND refund (second review, item 2).
//  3. LOOKUP: one the provider accepted as pending is read back through the
//     module's RefundLookup every ten minutes (a GET by the provider's own
//     refund id, so no idempotency window applies) for up to
//     PendingParkAfter; a refund the provider later reports failed already
//     cost its ticket, so it goes to manual_review ("money did not go
//     back"), never silently to failed.
//  4. REPAIR: an accepted refund whose settlement is unfinished (no
//     settled_at: a crash between the outcome commit and the cancellation,
//     a failed cancellation, order projection or publish) is settled
//     again; after maxRepairAttempts failures it is parked.
//  5. ALERTS: every engine refund that owes an ops alert (alert_due_at: it
//     entered manual_review, or a late acceptance revived a failed refund)
//     is announced once — ids, amount and the failure code, never buyer
//     data. Whichever process moved the row, the alert comes from here
//     (arena-worker holds the ops bot).
//
// A pass is BOUNDED and a provider brownout cannot starve it (review M4/M5,
// second review item 5): every section runs under its own deadline derived
// from the job's context — park SweepPassTimeout/6, retry /2, lookups /6,
// repair /6, alerts a fresh 30 seconds — so the whole pass stays under the
// worker's 5-minute stale-claim timeout; the retry section also stops after
// maxUnknownPerPass calls without an answer; provider calls are capped at
// SweepMaxCalls; candidates are ordered so the longest-waiting go first and
// at most one call per payment is made per pass, so one large batch cannot
// starve the others; and a failing section never skips the ones after it.
// It only touches rows the engine created (provider IS NOT NULL).
package refunds

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/opsalert"
)

// JobType is the worker_jobs.job_type of the sweep.
const JobType = "refund.sweep"

// Sweep timings and bounds (spec §7 and §14 question 3: 24 hours, ops bot).
const (
	DefaultSweepInterval = time.Minute
	LookupAfter          = 10 * time.Minute
	// StuckAfter: a refund the provider has not taken is never re-POSTed
	// once its FIRST attempt (never attempted: its approval) is this old,
	// and is parked instead. Stripe keeps an idempotency key for 24 hours
	// from the first request; the hour of margin covers a POST that lands a
	// call timeout after the check (third review, M2).
	StuckAfter = 23 * time.Hour
	// PendingParkAfter: a refund the provider accepted as pending is looked
	// up for this long before it is parked for a human.
	PendingParkAfter = 7 * 24 * time.Hour
	// DefaultSweepPassTimeout sizes a pass's sections (see the file comment)
	// so that a pass, with every section's overrun (SweepWorstCase, about
	// 290 s with the defaults), stays under the worker's 5-minute
	// stale-claim timeout and is never re-run concurrently. It was 3
	// minutes until the fifth review (LOW a) counted the settle tails: the
	// worst case was then about 350 s.
	DefaultSweepPassTimeout = 2 * time.Minute
	// DefaultSweepMaxCalls caps the provider calls (retries + lookups) of
	// one pass.
	DefaultSweepMaxCalls = 40
	// maxUnknownPerPass: the retry section stops after this many calls that
	// ended without an answer — a provider in a brownout is not hammered.
	maxUnknownPerPass  = 5
	alertSectionBudget = 30 * time.Second
	// alertsPerPass keeps one pass well inside Telegram's group limit of
	// about 20 messages a minute.
	alertsPerPass = 15
	// alertLease: how long a claimed alert is reserved for the pass that
	// sends it; a pass that died mid-send releases it this way.
	alertLease        = 2 * time.Minute
	maxRepairAttempts = 10
	// MaxAlertAttempts: an owed alert Telegram refused this many times, and
	// owed for longer than alertGiveUpAfter, is given up (logged), so a
	// message Telegram can never accept does not cost a call every pass.
	MaxAlertAttempts  = 20
	alertGiveUpAfter  = 24 * time.Hour
	sweepBatch        = 50
	lookupsPerPayment = 3
)

// Notifier is the ops alert channel (opsalert.Notifier fits).
type Notifier interface {
	Send(ctx context.Context, text string) error
}

// ConfirmingNotifier reports whether a message was actually delivered
// (opsalert.TelegramNotifier's SendConfirmed). When the notifier has it,
// an alert is cleared only after a confirmed delivery and retried by the
// next pass otherwise (third review, M1); a plain Notifier's Send error is
// treated the same way.
type ConfirmingNotifier interface {
	SendConfirmed(ctx context.Context, text string) error
}

func deliver(ctx context.Context, n Notifier, text string) error {
	if c, ok := n.(ConfirmingNotifier); ok {
		return c.SendConfirmed(ctx, text)
	}
	return n.Send(ctx, text)
}

// SweepReport counts what one pass did.
type SweepReport struct {
	Stuck, Retried, LookedUp, Repaired, Alerted int
	// Calls is the number of provider calls the pass made; Unknown how many
	// of the retries ended without an answer.
	Calls, Unknown int
}

// scopeSQL is the organization filter (tests only, see Options.SweepOrgs).
const scopeSQL = ` AND (cardinality($2::uuid[]) = 0 OR org_id = ANY($2::uuid[]))`

// Sweep runs one bounded pass. now is the clock (tests move it). Every
// section runs, whatever an earlier one did; their errors are joined.
func (e *Engine) Sweep(ctx context.Context, now time.Time, notifier Notifier) (SweepReport, error) {
	var (
		rep  SweepReport
		errs []error
	)
	t := e.sweepPassTimeout
	budget := e.sweepMaxCalls
	section := func(d time.Duration, name string, fn func(ctx context.Context) error) {
		sctx, cancel := context.WithTimeout(ctx, d)
		defer cancel()
		if err := fn(sctx); err != nil {
			errs = append(errs, fmt.Errorf("refunds: sweep %s: %w", name, err))
		}
	}

	section(t/6, "park", func(ctx context.Context) error {
		n, err := e.parkStuck(ctx, now)
		rep.Stuck = n
		return err
	})
	section(t/2, "retry", func(ctx context.Context) error {
		return e.retryPass(ctx, now, &rep, &budget)
	})
	section(t/6, "lookup", func(ctx context.Context) error {
		return e.lookupPass(ctx, now, &rep, &budget)
	})
	section(t/6, "repair", func(ctx context.Context) error {
		n, err := e.repair(ctx, now)
		rep.Repaired = n
		return err
	})
	if notifier != nil {
		// A fresh context: alerts are sent even when the job's own context
		// is nearly spent.
		actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), alertSectionBudget)
		n, err := e.alertReviews(actx, notifier)
		cancel()
		rep.Alerted = n
		if err != nil {
			errs = append(errs, fmt.Errorf("refunds: sweep alerts: %w", err))
		}
	}
	return rep, errors.Join(errs...)
}

// retryPass calls again the refunds whose last call ended without an answer.
func (e *Engine) retryPass(ctx context.Context, now time.Time, rep *SweepReport, budget *int) error {
	retry, err := e.selectIDs(ctx, `SELECT id FROM (
		SELECT id, provider_attempted_at, created_at,
		       row_number() OVER (PARTITION BY payment_intent_id
		                          ORDER BY provider_attempted_at NULLS FIRST, created_at) AS rn
		FROM refunds
		WHERE settlement = 'provider' AND provider IS NOT NULL AND state = 'provider_pending'
		  AND provider_refund_id IS NULL AND COALESCE(provider_attempted_at, created_at) <= $1
		  AND COALESCE(first_attempted_at, approved_at, created_at) > $3`+scopeSQL+`) c
		WHERE rn = 1 ORDER BY provider_attempted_at NULLS FIRST, created_at LIMIT `+strconv.Itoa(sweepBatch),
		now.Add(-CallStaleAfter), e.scopeOrgs(), now.Add(-StuckAfter))
	if err != nil {
		return err
	}
	for _, id := range retry {
		if *budget <= 0 || rep.Unknown >= maxUnknownPerPass || ctx.Err() != nil {
			break
		}
		r, called, derr := e.drive(ctx, id)
		if derr != nil {
			if called {
				*budget--
				rep.Calls++
			}
			rep.Unknown++
			e.logger.Error("refunds: sweep retry failed", "refund_id", id.String(), "error", derr.Error())
			continue
		}
		if !called {
			// Not claimable (in flight elsewhere, answered meanwhile):
			// skipped, not an unanswered call (third review, L1).
			continue
		}
		*budget--
		rep.Calls++
		if r.State == StateProviderPending && r.ProviderRefundID == nil {
			rep.Unknown++
			continue
		}
		rep.Retried++
	}
	return nil
}

// lookupPass reads accepted-but-pending refunds back from their provider.
func (e *Engine) lookupPass(ctx context.Context, now time.Time, rep *SweepReport, budget *int) error {
	lookup, err := e.selectIDs(ctx, `SELECT id FROM (
		SELECT id, updated_at,
		       row_number() OVER (PARTITION BY payment_intent_id ORDER BY updated_at) AS rn
		FROM refunds
		WHERE settlement = 'provider' AND provider IS NOT NULL AND state = 'provider_pending'
		  AND provider_refund_id IS NOT NULL AND updated_at <= $1
		  AND COALESCE(approved_at, created_at) > $3`+scopeSQL+`) c
		WHERE rn <= `+strconv.Itoa(lookupsPerPayment)+` ORDER BY updated_at LIMIT `+strconv.Itoa(sweepBatch),
		now.Add(-LookupAfter), e.scopeOrgs(), now.Add(-PendingParkAfter))
	if err != nil {
		return err
	}
	for _, id := range lookup {
		if *budget <= 0 || ctx.Err() != nil {
			break
		}
		*budget--
		rep.Calls++
		if lerr := e.lookup(ctx, id); lerr != nil {
			e.logger.Error("refunds: sweep lookup failed", "refund_id", id.String(), "error", lerr.Error())
			continue
		}
		rep.LookedUp++
	}
	return nil
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

// stuckWhere selects the refunds parkStuck parks. $1 = now - StuckAfter
// (measured from the FIRST attempt, else the approval), $2 = organization
// scope, $3 = CallStaleAfter, $4 = now - PendingParkAfter (from approval).
const stuckWhere = `settlement = 'provider' AND provider IS NOT NULL AND state = 'provider_pending'
	AND ((provider_refund_id IS NULL AND COALESCE(first_attempted_at, approved_at, created_at) <= $1
	      AND (provider_attempted_at IS NULL OR provider_attempted_at <= now() - $3::interval))
	  OR (provider_refund_id IS NOT NULL AND COALESCE(approved_at, created_at) <= $4))` + scopeSQL

// parkStuck moves overdue engine refunds (stuckWhere) to manual_review, one
// row at a time under its payment's advisory lock, skipping any whose
// provider call is in flight.
func (e *Engine) parkStuck(ctx context.Context, now time.Time) (int, error) {
	args := []any{now.Add(-StuckAfter), e.scopeOrgs(), intervalText(CallStaleAfter), now.Add(-PendingParkAfter)}
	ids, err := e.selectIDs(ctx, `SELECT id FROM refunds WHERE `+stuckWhere+
		` ORDER BY created_at LIMIT `+strconv.Itoa(sweepBatch), args...)
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
				SET state = 'manual_review', updated_at = now(), `+reviewAlertSQL+`,
				    failure_code = CASE WHEN provider_refund_id IS NULL THEN 'stuck_provider_pending' ELSE 'provider_pending_too_long' END,
				    cancel_ticket = CASE WHEN provider_refund_id IS NULL AND provider_attempts = 0 THEN false ELSE cancel_ticket END,
				    failure_reason = CASE WHEN provider_refund_id IS NULL AND provider_attempts = 0
				        THEN 'this refund was approved more than 23 hours ago but never sent to the provider: no call was made, so no money moved through it. It no longer counts against the payment and no longer holds its ticket, and arena will never send it. The ticket is unchanged. If the buyer is still owed this money, create a new refund of the ticket or order; otherwise nothing else needs doing'
				        WHEN provider_refund_id IS NULL
				        THEN 'the provider did not confirm this refund within 23 hours of its first attempt, and it may not be sent again (idempotency window); last answer: ' || COALESCE(failure_code, 'none')
				        ELSE 'the provider accepted this refund but has not completed it within 7 days' END
				WHERE id = $5 AND `+stuckWhere+`
				RETURNING `+refundColumns, append(args, id)...))
			if errors.Is(err, pgx.ErrNoRows) {
				return nil // changed meanwhile (answered, or a call started)
			}
			if err != nil {
				return err
			}
			moved = &r
			return e.writeAudit(ctx, tx, audit.Event{ActorType: "system", Action: "v1.refund.manual_review",
				ResourceType: "refund", ResourceID: r.ID.String(),
				Metadata: map[string]any{"reason": deref(r.FailureCode), "order_id": uuidString(r.OrderID),
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

// repairWhere selects accepted refunds (alias r) whose after-acceptance
// steps are not all done: settled_at is stamped only when every due ticket
// cancellation of the refund's scope went through, the order projection is
// in place and — once it succeeded — the v1.ticket.refunded publish is
// claimed (settle.go finish). Which tickets a refund covers is decided by
// refundScope alone; this query keeps no second copy of those rules (fourth
// review, H-1 and M-a: a copy drifted, and a refund whose projection or
// publish failed after its tickets were cancelled was never retried).
// $1 = now - CallStaleAfter, $2 = scope.
const repairWhere = `r.settlement = 'provider' AND r.provider IS NOT NULL AND r.settled_at IS NULL
	AND (r.state = 'succeeded' OR (r.state = 'provider_pending' AND r.provider_refund_id IS NOT NULL))
	AND COALESCE(r.repair_attempted_at, r.updated_at) <= $1
	AND (cardinality($2::uuid[]) = 0 OR r.org_id = ANY($2::uuid[]))`

// repair finishes accepted refunds whose settlement a crash, a failure or an
// expired deadline left unfinished (a ticket still active, the order not
// projected, the publish not claimed), with a bounded number of attempts
// per refund. Each attempt is decided and
// counted under the payment's advisory lock (second review, item 7); the
// cancellation itself runs in the canceller's own transaction afterwards,
// and the v1.ticket.refunded publish is claimed once (settle.go publish).
func (e *Engine) repair(ctx context.Context, now time.Time) (int, error) {
	args := []any{now.Add(-CallStaleAfter), e.scopeOrgs()}
	ids, err := e.selectIDs(ctx, `SELECT r.id FROM refunds r WHERE `+repairWhere+`
		ORDER BY COALESCE(r.repair_attempted_at, r.updated_at) LIMIT `+strconv.Itoa(sweepBatch), args...)
	if err != nil {
		return 0, err
	}
	repaired := 0
	for _, id := range ids {
		if ctx.Err() != nil {
			break
		}
		attempts := 0
		if err := e.inTx(ctx, func(tx pgx.Tx) error {
			var piID *uuid.UUID
			if err := tx.QueryRow(ctx, `SELECT payment_intent_id FROM refunds WHERE id = $1`, id).Scan(&piID); err != nil || piID == nil {
				return err
			}
			if err := LockPayment(ctx, tx, *piID); err != nil {
				return err
			}
			err := tx.QueryRow(ctx, `UPDATE refunds r SET repair_attempts = repair_attempts + 1, repair_attempted_at = now()
				WHERE r.id = $3 AND `+repairWhere+` RETURNING repair_attempts`, append(args, id)...).Scan(&attempts)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil // another pass repaired it meanwhile
			}
			return err
		}); err != nil {
			return repaired, err
		}
		if attempts == 0 {
			continue
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
		// The same steps as the first settle; v1.ticket.refunded is published
		// at most once (publish claims it).
		if e.finish(ctx, ctx, r, pay) {
			repaired++
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
		u, err := scanRefund(tx.QueryRow(ctx, `UPDATE refunds SET state = 'manual_review', updated_at = now(), `+reviewAlertSQL+`,
			failure_code = 'ticket_cancellation_failed',
			failure_reason = 'the provider returned the money but finishing the refund kept failing (cancelling its tickets, moving the order, or announcing the refund to the sites); check the tickets and the order by hand'
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

// alertReviews sends one ops alert per engine refund that owes one
// (alert_due_at), at most alertsPerPass per pass. Ids, amount, provider and
// code only — never the buyer — with every value HTML-escaped (Telegram's
// HTML parse mode refuses a stray < or &). Each alert is leased first
// (alert_lease_until, so a concurrent pass skips it), sent, and only a
// CONFIRMED delivery clears alert_due_at (third review, M1). A failed
// delivery counts against its row (alert_attempts) and the leases are
// released; rows with fewer failures go first, so a message Telegram keeps
// refusing never blocks the alerts behind it, and one refused
// MaxAlertAttempts times and owed for longer than alertGiveUpAfter is given
// up with an error log (fourth review, LOW 2). The first failure still ends
// the section: Telegram may be down, and the rest wait for the next pass.
func (e *Engine) alertReviews(ctx context.Context, n Notifier) (int, error) {
	var (
		rows  []Refund
		due   []time.Time
		fails []int
	)
	err := e.inTx(ctx, func(tx pgx.Tx) error {
		res, err := tx.Query(ctx, `UPDATE refunds SET alert_lease_until = now() + $2::interval
			WHERE id IN (SELECT id FROM refunds
			             WHERE settlement = 'provider' AND provider IS NOT NULL AND alert_due_at IS NOT NULL
			               AND (alert_lease_until IS NULL OR alert_lease_until < now())
			               AND (cardinality($1::uuid[]) = 0 OR org_id = ANY($1::uuid[]))
			             ORDER BY alert_attempts, alert_due_at LIMIT `+strconv.Itoa(alertsPerPass)+` FOR UPDATE SKIP LOCKED)
			RETURNING alert_attempts, alert_due_at, `+refundColumns, e.scopeOrgs(), intervalText(alertLease))
		if err != nil {
			return err
		}
		defer res.Close()
		for res.Next() {
			var (
				at   time.Time
				fail int
			)
			r, err := scanRefundWith(res, &fail, &at)
			if err != nil {
				return err
			}
			rows, due, fails = append(rows, r), append(due, at), append(fails, fail)
		}
		return res.Err()
	})
	if err != nil {
		return 0, err
	}
	// RETURNING does not keep the subquery's order: fewest failures first,
	// then the oldest.
	order := make([]int, len(rows))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		x, y := order[a], order[b]
		if fails[x] != fails[y] {
			return fails[x] < fails[y]
		}
		return due[x].Before(due[y])
	})
	sent := 0
	for k, i := range order {
		r := rows[i]
		if derr := deliver(ctx, n, alertText(r)); derr != nil {
			e.logger.Warn("refunds: ops alert not delivered; the next pass retries it", "refund_id", r.ID.String(), "error", derr.Error())
			if ctx.Err() == nil {
				// The section's own deadline is not the message's fault.
				e.alertFailed(ctx, r, due[i])
			}
			rest := make([]Refund, 0, len(order)-k)
			for _, j := range order[k:] {
				rest = append(rest, rows[j])
			}
			e.releaseAlerts(ctx, rest)
			break
		}
		e.alertDelivered(ctx, r, due[i])
		sent++
	}
	return sent, nil
}

// alertText is the ops message of one owed alert.
func alertText(r Refund) string {
	esc := opsalert.EscapeHTML
	if r.State == StateManualReview {
		text := fmt.Sprintf("Refund %s (%d %s via %s) needs manual review: %s. Order %s.",
			r.ID, r.Amount, esc(r.Currency), esc(deref(r.Provider)), esc(deref(r.FailureCode)), uuidString(r.OrderID))
		switch deref(r.FailureCode) {
		case failureBudgetTaken, failureBudgetAfterUnanswered, failureLateOverBudget:
			if r.TicketID != nil {
				// Sixth review: nothing cancels this ticket automatically.
				text += fmt.Sprintf(" Ticket %s is STILL VALID. Check the provider dashboard; if the buyer's money for it is back, cancel it without a refund: POST /v1/tickets/%s/cancel with refund_mode=none.",
					r.TicketID, r.TicketID)
			}
		}
		return text
	}
	return fmt.Sprintf("Refund %s (%d %s via %s) was accepted by the provider late, after it had been parked or marked failed: accepted_late. Its ticket is cancelled; check for a manual duplicate refund. Order %s.",
		r.ID, r.Amount, esc(r.Currency), esc(deref(r.Provider)), uuidString(r.OrderID))
}

// alertWriteTimeout bounds each bookkeeping write of the alert section, on a
// context of its own: a delivered alert must be marked even when the
// section's deadline has just passed, or it is sent again.
const alertWriteTimeout = 5 * time.Second

// alertDelivered clears the owed alert — only if no NEW alert was owed
// meanwhile (alert_due_at unchanged since the lease).
func (e *Engine) alertDelivered(ctx context.Context, r Refund, due time.Time) {
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), alertWriteTimeout)
	defer cancel()
	if err := e.inTx(wctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(wctx, `UPDATE refunds SET review_alerted_at = now(), alert_lease_until = NULL, alert_attempts = 0,
			alert_due_at = CASE WHEN alert_due_at = $2 THEN NULL ELSE alert_due_at END WHERE id = $1`, r.ID, due)
		return err
	}); err != nil {
		e.logger.Error("refunds: marking an alert delivered failed", "refund_id", r.ID.String(), "error", err.Error())
	}
}

// alertFailed counts a refused delivery against its row and releases it.
// An alert refused MaxAlertAttempts times and owed for longer than
// alertGiveUpAfter is given up: Telegram is evidently up (the failures are
// spread over a day of passes that delivered other alerts first) and will
// never take this message.
func (e *Engine) alertFailed(ctx context.Context, r Refund, due time.Time) {
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), alertWriteTimeout)
	defer cancel()
	var (
		attempts int
		gaveUp   bool
	)
	if err := e.inTx(wctx, func(tx pgx.Tx) error {
		return tx.QueryRow(wctx, `UPDATE refunds SET alert_lease_until = NULL, alert_attempts = alert_attempts + 1,
			alert_due_at = CASE WHEN alert_attempts + 1 >= $3 AND alert_due_at = $2 AND alert_due_at < now() - $4::interval
			                    THEN NULL ELSE alert_due_at END
			WHERE id = $1 RETURNING alert_attempts, alert_due_at IS NULL`,
			r.ID, due, MaxAlertAttempts, intervalText(alertGiveUpAfter)).Scan(&attempts, &gaveUp)
	}); err != nil {
		e.logger.Warn("refunds: counting an undelivered alert failed; its lease expires", "refund_id", r.ID.String(), "error", err.Error())
		return
	}
	if gaveUp {
		e.logger.Error("refunds: ops alert given up after repeated refused deliveries; the refund still needs a human",
			"event", "refund_alert_given_up", "refund_id", r.ID.String(), "order_id", uuidString(r.OrderID),
			"attempts", attempts, "failure_code", deref(r.FailureCode))
	}
}

// releaseAlerts gives undelivered alerts back to the next pass.
func (e *Engine) releaseAlerts(ctx context.Context, rows []Refund) {
	if len(rows) == 0 {
		return
	}
	ids := make([]uuid.UUID, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), alertWriteTimeout)
	defer cancel()
	if err := e.inTx(wctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(wctx, `UPDATE refunds SET alert_lease_until = NULL WHERE id = ANY($1::uuid[])`, ids)
		return err
	}); err != nil {
		e.logger.Warn("refunds: releasing undelivered alerts failed; their lease expires", "error", err.Error())
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
	callCtx, cancel := context.WithTimeout(ctx, e.callTimeout)
	defer cancel()
	var looker payments.RefundLookup
	if hasModule && desc.Capabilities.RefundLookup {
		if module, berr := e.modules.Build(callCtx, r.OrgID, deref(r.Provider)); berr == nil {
			looker, _ = module.(payments.RefundLookup)
		}
	}
	if looker == nil {
		// No way to ask: wait for the webhook (PAY-05) or the 7-day park.
		return e.touch(ctx, id)
	}
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
			sql = `UPDATE refunds SET state = 'succeeded', succeeded_at = now(), provider_status = $2, settled_at = NULL, updated_at = now()
			       WHERE id = $1 RETURNING ` + refundColumns
		case payments.RefundFailed:
			// The ticket was cancelled when the provider accepted; the money
			// did not go back after all. An operator decides — and is told
			// (alertReviews), review M1.
			sql = `UPDATE refunds SET state = 'manual_review', provider_status = $2, failure_code = $3,
			       failure_reason = $4, ` + reviewAlertSQL + `, updated_at = now() WHERE id = $1 RETURNING ` + refundColumns
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
			if (rep.Stuck + rep.Retried + rep.LookedUp + rep.Repaired + rep.Alerted + rep.Unknown) > 0 {
				o.Logger.Info("refund sweep complete", "stuck", rep.Stuck, "retried", rep.Retried,
					"looked_up", rep.LookedUp, "repaired", rep.Repaired, "alerted", rep.Alerted,
					"calls", rep.Calls, "unknown", rep.Unknown)
			}
		}
		if passErr != nil {
			o.Logger.Error("refund sweep pass failed", "error", passErr.Error())
		}
		if o.Scheduler != nil {
			if err := o.Scheduler.ScheduleNext(context.WithoutCancel(ctx), o.Now().Add(o.Interval)); err != nil {
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
