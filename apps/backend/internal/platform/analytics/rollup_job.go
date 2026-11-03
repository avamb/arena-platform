package analytics

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErasureResult counts what ErasePerson removed.
type ErasureResult struct {
	Identities int
	Events     int64
	Sessions   int64
	// Applications is how many onboarding applications lost their aid.
	Applications int64
}

// ErasePerson deletes everything analytics holds about one person (spec 50
// section 11): the raw events written under any of their identifiers, then
// the person row, which takes the identities, the sessions and the profile
// with it (ON DELETE CASCADE). It runs in one transaction.
//
// The visitor id kept on a not-yet-purged onboarding application is cleared
// as well, so the next rollup cannot rebuild the history's link. A live
// application still makes the rollup create a bare person from its e-mail
// hash: that row holds no behaviour, only what the application itself keeps.
// contact_codes are CRM data and stay.
func ErasePerson(ctx context.Context, db interface {
	BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error)
}, personID uuid.UUID) (ErasureResult, error) {
	var res ErasureResult
	tx, err := db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return res, fmt.Errorf("analytics: begin erase tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Wait for a running rollup (and keep it out) until this transaction ends,
	// so a concurrent join cannot restore what is deleted below.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, RollupLockKey); err != nil {
		return res, fmt.Errorf("analytics: erase lock: %w", err)
	}
	pid := personID.String()
	rows, err := tx.Query(ctx, `SELECT kind, value FROM analytics.identities WHERE person_id = $1::uuid`, pid)
	if err != nil {
		return res, fmt.Errorf("analytics: read identities: %w", err)
	}
	var kinds, values, aids []string
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			rows.Close()
			return res, err
		}
		kinds, values = append(kinds, k), append(values, v)
		if k == IDKindAID {
			aids = append(aids, v)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return res, err
	}
	res.Identities = len(kinds)

	if len(kinds) > 0 {
		tag, err := tx.Exec(ctx, `
DELETE FROM analytics.events
 WHERE (id_kind, id_value) IN (SELECT * FROM unnest($1::text[], $2::text[]))`, kinds, values)
		if err != nil {
			return res, fmt.Errorf("analytics: delete events: %w", err)
		}
		res.Events = tag.RowsAffected()
	}
	if len(aids) > 0 {
		tag, err := tx.Exec(ctx, `UPDATE onboarding_applications SET aid = NULL WHERE aid = ANY($1::text[])`, aids)
		if err != nil {
			return res, fmt.Errorf("analytics: clear application aid: %w", err)
		}
		res.Applications = tag.RowsAffected()
	}
	tag, err := tx.Exec(ctx, `DELETE FROM analytics.sessions WHERE person_id = $1::uuid`, pid)
	if err != nil {
		return res, err
	}
	res.Sessions = tag.RowsAffected()
	// Identities and the profile cascade from the person row.
	if _, err := tx.Exec(ctx, `DELETE FROM analytics.people WHERE id = $1::uuid`, pid); err != nil {
		return res, fmt.Errorf("analytics: delete person: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return res, err
	}
	return res, nil
}

// ─── worker wiring ─────────────────────────────────────────────────────────

// RollupScheduler enqueues the next run.
type RollupScheduler interface {
	ScheduleNext(ctx context.Context, at time.Time) error
}

// PGRollupScheduler is the worker_jobs-backed scheduler.
type PGRollupScheduler struct{ pool *pgxpool.Pool }

// NewPGRollupScheduler wraps a pool.
func NewPGRollupScheduler(pool *pgxpool.Pool) *PGRollupScheduler {
	return &PGRollupScheduler{pool: pool}
}

// ScheduleNext inserts a pending job due at `at`, unless one is already
// pending (the running job itself is claimed), so a retry or a restart never
// forks a second chain of runs.
func (s *PGRollupScheduler) ScheduleNext(ctx context.Context, at time.Time) error {
	if _, err := s.pool.Exec(ctx, `
INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
SELECT $1, '{}', 3, 'pending', $2
WHERE  NOT EXISTS (SELECT 1 FROM worker_jobs WHERE job_type = $1 AND status = 'pending')`,
		RollupJobType, at); err != nil {
		return fmt.Errorf("analytics: schedule next rollup: %w", err)
	}
	return nil
}

// RollupHandlerOptions configure the handler.
type RollupHandlerOptions struct {
	Rollup    *Rollup
	Scheduler RollupScheduler
	Logger    *slog.Logger
	Interval  time.Duration
	// Failures counts a failed run and every person that could not be
	// recomputed (arena_analytics_rollup_failed_total); nil is fine.
	Failures Counter
	// Now nil means time.Now.
	Now func() time.Time
}

// NewRollupHandler returns the handler of RollupJobType. The next run is
// queued first, and the handler returns nil even when the run failed: the
// failure is logged and counted and the next run retries it, so a worker retry
// never forks a second chain (the scheduler also refuses to queue a run while
// one is queued). Only a failure to queue the next run is returned.
func NewRollupHandler(o RollupHandlerOptions) func(ctx context.Context, payload []byte) error {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Interval <= 0 {
		o.Interval = RollupInterval
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	fail := func(n float64) {
		if o.Failures != nil {
			o.Failures.Add(n)
		}
	}
	return func(ctx context.Context, _ []byte) error {
		if o.Scheduler != nil {
			if err := o.Scheduler.ScheduleNext(ctx, o.Now().Add(o.Interval)); err != nil {
				o.Logger.Error("analytics rollup: next run not scheduled", "error", err.Error())
				return err
			}
		}
		res, err := o.Rollup.Run(ctx)
		switch {
		case err != nil:
			fail(1)
			o.Logger.Error("analytics rollup failed, retrying at the next run", "error", err.Error())
		case res.Skipped:
			o.Logger.Info("analytics rollup skipped: another run holds the lock")
		default:
			if res.PersonFailures > 0 {
				fail(float64(res.PersonFailures))
			}
			o.Logger.Info("analytics rollup complete",
				"people", res.People, "recomputed", res.Recomputed, "full_pass", res.FullPass,
				"created", res.PeopleCreated, "merged", res.PeopleMerged, "merge_conflicts", res.MergeConflicts,
				"person_failures", res.PersonFailures, "sessions_written", res.SessionsWritten,
				"profiles_changed", res.ProfilesChanged, "flow_rows", res.FlowRows)
		}
		return nil
	}
}

// ScheduleRollupJob enqueues the first run at arena-worker startup unless one
// is already pending or claimed, in ONE statement, so two workers starting
// together cannot both insert. Every later run is self-scheduled.
func ScheduleRollupJob(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, `
INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
SELECT $1, '{}', 3, 'pending', now()
WHERE  NOT EXISTS (SELECT 1 FROM worker_jobs WHERE job_type = $1 AND status IN ('pending', 'claimed'))`,
		RollupJobType); err != nil {
		return fmt.Errorf("analytics: enqueue initial rollup job: %w", err)
	}
	return nil
}
