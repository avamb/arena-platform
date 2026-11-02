package referral

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// JobTypeSweep is the self-scheduling safety-net job.
const JobTypeSweep = "referral.payout_sweep"

// SweepInterval is how often the sweep runs.
const SweepInterval = 10 * time.Minute

const (
	// dueGrace is how long a due reward may wait for its own payout job
	// before the sweep queues it again.
	dueGrace = 5 * time.Minute
	// reversalWindow is how far back a paid reward is watched for a friend who
	// got the money back or opened a dispute (spec 52 section 5.2).
	reversalWindow = "120 days"
	// reversalNoticeWindow bounds the retries of the organizer notice.
	reversalNoticeWindow = "14 days"
	sweepBatch           = 200
	// pauseAfterReversals: this many reversals under one code pause the code.
	pauseAfterReversals = 2
)

// SweepReport counts what one pass did.
type SweepReport struct {
	Requeued    int
	ReadBack    int
	Reversed    int
	Notified    int
	Paused      int
	Deactivated int
}

// Sweep is one pass. Each section runs even when an earlier one failed; the
// errors are joined.
//
//  1. due rewards older than dueGrace with no live payout job are queued again;
//  2. sent rewards (and those parked in manual review) are read back from
//     their refunds: succeeded becomes paid, a refusal becomes not_possible;
//  3. a friend who got the money back (the order is refunded) or opened a
//     dispute, after the cashback was paid, is recorded on the reward
//     (friend_reversal) and the organizer is told through the Notifier;
//  4. a code with two such friends is paused (the organizer can switch it on),
//     and a code whose referrer order is refunded or cancelled is deactivated.
func (s *Service) Sweep(ctx context.Context) (SweepReport, error) {
	var rep SweepReport
	var errs []error
	for _, step := range []struct {
		name string
		fn   func() error
	}{
		{"requeue", func() (err error) { rep.Requeued, err = s.requeueDue(ctx); return }},
		{"read back", func() (err error) { rep.ReadBack, err = s.readBackSent(ctx); return }},
		{"reversals", func() (err error) { rep.Reversed, err = s.markReversals(ctx); return }},
		{"notify", func() (err error) { rep.Notified, err = s.notifyReversals(ctx); return }},
		{"pause", func() (err error) { rep.Paused, err = s.pauseReversedCodes(ctx); return }},
		{"deactivate", func() (err error) { rep.Deactivated, err = s.deactivateDeadCodes(ctx); return }},
	} {
		if err := step.fn(); err != nil {
			errs = append(errs, fmt.Errorf("referral sweep %s: %w", step.name, err))
		}
	}
	return rep, errors.Join(errs...)
}

func (s *Service) requeueDue(ctx context.Context) (int, error) {
	tag, err := s.pool.Exec(ctx, `INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
		SELECT $1, jsonb_build_object('reward_id', r.id::text), $2, 'pending', now()
		FROM   referral_rewards r
		WHERE  r.state = 'due' AND r.created_at < now() - $3::interval
		  AND  (r.next_attempt_at IS NULL OR r.next_attempt_at <= now())
		  AND  NOT EXISTS (SELECT 1 FROM worker_jobs j
		                    WHERE j.job_type = $1 AND j.status IN ('pending', 'claimed')
		                      AND j.payload->>'reward_id' = r.id::text)
		ORDER  BY r.created_at LIMIT $4`,
		JobTypePayout, payoutMaxAttempts, fmt.Sprintf("%d seconds", int(dueGrace.Seconds())), sweepBatch)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

func (s *Service) readBackSent(ctx context.Context) (int, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+gen.ReferralRewardColumns+` FROM referral_rewards
		WHERE refund_batch_id IS NOT NULL
		  AND (state = 'sent' OR (state = 'not_possible' AND reason = 'manual_review'))
		ORDER BY updated_at LIMIT $1`, sweepBatch)
	if err != nil {
		return 0, err
	}
	var list []gen.ReferralRewardRow
	for rows.Next() {
		r, err := gen.ScanReferralRewardRow(rows)
		if err != nil {
			rows.Close()
			return 0, err
		}
		list = append(list, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	changed := 0
	var errs []error
	for _, rw := range list {
		before := rw.State
		if err := s.settleBatch(ctx, rw, *rw.RefundBatchID, []string{rw.State}); err != nil {
			errs = append(errs, err)
			continue
		}
		now, err := gen.New(s.pool).GetReferralRewardByID(ctx, rw.ID)
		if err == nil && now.State != before {
			changed++
		}
	}
	return changed, errors.Join(errs...)
}

// markReversals records friends who took the money back after the cashback
// was paid or sent. A refunded order wins over a later dispute: the first
// reversal found stays.
func (s *Service) markReversals(ctx context.Context) (int, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE referral_rewards r
		SET friend_reversal = CASE o.status WHEN 'refunded' THEN 'refunded' ELSE 'partially_refunded' END,
		    updated_at = now()
		FROM orders o
		WHERE o.id = r.friend_order_id AND r.state IN ('paid', 'sent') AND r.friend_reversal IS NULL
		  AND r.created_at > now() - $1::interval AND o.status IN ('refunded', 'partially_refunded')`,
		reversalWindow)
	if err != nil {
		return 0, err
	}
	n := int(tag.RowsAffected())
	ok, err := disputesTable(ctx, s.pool)
	if err != nil || !ok {
		return n, err
	}
	// An inquiry is not a dispute (spec 52 section 5.2).
	tag, err = s.pool.Exec(ctx, `UPDATE referral_rewards r
		SET friend_reversal = 'disputed', updated_at = now()
		WHERE r.state IN ('paid', 'sent') AND r.friend_reversal IS NULL
		  AND r.created_at > now() - $1::interval
		  AND EXISTS (SELECT 1 FROM payment_disputes d WHERE d.order_id = r.friend_order_id AND NOT d.is_inquiry)`,
		reversalWindow)
	if err != nil {
		s.logger.Warn("referral: dispute scan failed", "error", err.Error())
		return n, nil
	}
	return n + int(tag.RowsAffected()), nil
}

// notifyReversals tells the organizer once about each recorded reversal. A
// Notifier that is not wired yet leaves the reward unmarked, so the notice
// goes out as soon as one is.
func (s *Service) notifyReversals(ctx context.Context) (int, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+gen.ReferralRewardColumns+` FROM referral_rewards
		WHERE friend_reversal IS NOT NULL AND reversal_notified_at IS NULL
		  AND updated_at > now() - $1::interval
		ORDER BY updated_at LIMIT $2`, reversalNoticeWindow, sweepBatch)
	if err != nil {
		return 0, err
	}
	var list []gen.ReferralRewardRow
	for rows.Next() {
		r, err := gen.ScanReferralRewardRow(rows)
		if err != nil {
			rows.Close()
			return 0, err
		}
		list = append(list, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	sent := 0
	var errs []error
	for _, rw := range list {
		err := s.notifier.FriendReversed(ctx, rw)
		if errors.Is(err, ErrNotWired) {
			continue
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if _, err := s.pool.Exec(ctx, `UPDATE referral_rewards SET reversal_notified_at = now() WHERE id = $1`, rw.ID); err != nil {
			errs = append(errs, err)
			continue
		}
		sent++
	}
	return sent, errors.Join(errs...)
}

// pauseReversedCodes pauses a code with pauseAfterReversals friends who took
// the money back (spec 52 section 5.2 item 3). 'paused' is the status the
// organizer can switch on again.
func (s *Service) pauseReversedCodes(ctx context.Context) (int, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE promo_codes pc SET status = 'paused', updated_at = now()
		WHERE pc.origin = 'referral' AND pc.status = 'active'
		  AND (SELECT count(*) FROM referral_rewards r
		        WHERE r.promo_code_id = pc.id AND r.friend_reversal IS NOT NULL) >= $1`, pauseAfterReversals)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// deactivateDeadCodes switches off the codes whose referrer order is fully
// refunded or cancelled: the cashback has nowhere to go (spec 52 section 3.2).
func (s *Service) deactivateDeadCodes(ctx context.Context) (int, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE promo_codes pc SET status = 'inactive', updated_at = now()
		FROM orders o
		WHERE pc.origin = 'referral' AND pc.status IN ('active', 'paused')
		  AND o.id = pc.referrer_order_id AND o.status IN ('refunded', 'cancelled')`)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// Scheduler enqueues the next sweep.
type Scheduler interface {
	ScheduleNext(ctx context.Context, at time.Time) error
}

// PGScheduler enqueues the next sweep into worker_jobs, unless one is pending:
// a pass run twice must not grow a second chain.
type PGScheduler struct{ pool *pgxpool.Pool }

// NewPGScheduler builds a PGScheduler.
func NewPGScheduler(pool *pgxpool.Pool) *PGScheduler { return &PGScheduler{pool: pool} }

// ScheduleNext implements Scheduler.
func (p *PGScheduler) ScheduleNext(ctx context.Context, at time.Time) error {
	_, err := p.pool.Exec(ctx, `INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
		SELECT $1, '{}', 3, 'pending', $2
		WHERE NOT EXISTS (SELECT 1 FROM worker_jobs WHERE job_type = $1 AND status = 'pending')`, JobTypeSweep, at)
	return err
}

// ScheduleInitialJob seeds the queue once at worker start-up when no sweep is
// pending or claimed.
func ScheduleInitialJob(ctx context.Context, pool *pgxpool.Pool) error {
	var n int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM worker_jobs WHERE job_type = $1 AND status IN ('pending', 'claimed')`,
		JobTypeSweep).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	_, err := pool.Exec(ctx, `INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
		VALUES ($1, '{}', 3, 'pending', now())`, JobTypeSweep)
	return err
}

// NewSweepHandler returns the worker handler of JobTypeSweep. It schedules the
// next pass before it works, once per pass, so a failing pass does not end the
// chain.
func NewSweepHandler(s *Service, sched Scheduler) func(ctx context.Context, payload []byte) error {
	return func(ctx context.Context, _ []byte) error {
		if sched != nil {
			if err := sched.ScheduleNext(context.WithoutCancel(ctx), s.now().Add(SweepInterval)); err != nil {
				return fmt.Errorf("referral: schedule next sweep: %w", err)
			}
		}
		rep, err := s.Sweep(ctx)
		if (rep != SweepReport{}) {
			s.logger.Info("referral sweep complete", "requeued", rep.Requeued, "read_back", rep.ReadBack,
				"reversed", rep.Reversed, "notified", rep.Notified, "paused", rep.Paused, "deactivated", rep.Deactivated)
		}
		if err != nil {
			// The next pass is already queued; do not make the worker retry
			// this one and run two.
			s.logger.Error("referral sweep pass failed", "error", err.Error())
		}
		return nil
	}
}
