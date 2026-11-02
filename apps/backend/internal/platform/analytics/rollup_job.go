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

// ScheduleNext inserts a pending job due at `at`.
func (s *PGRollupScheduler) ScheduleNext(ctx context.Context, at time.Time) error {
	if _, err := s.pool.Exec(ctx, `
INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
VALUES ($1, '{}', 3, 'pending', $2)`, RollupJobType, at); err != nil {
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
	// Now nil means time.Now.
	Now func() time.Time
}

// NewRollupHandler returns the handler of RollupJobType. Like the partitions
// job it queues the next run even when this run fails, so one bad
// hour cannot stop the cadence; the failure is returned afterwards.
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
	return func(ctx context.Context, _ []byte) error {
		res, err := o.Rollup.Run(ctx)
		if o.Scheduler != nil {
			if serr := o.Scheduler.ScheduleNext(ctx, o.Now().Add(o.Interval)); serr != nil && err == nil {
				err = serr
			}
		}
		if err != nil {
			return err
		}
		o.Logger.Info("analytics rollup complete",
			"people", res.People, "created", res.PeopleCreated, "merged", res.PeopleMerged,
			"sessions_written", res.SessionsWritten, "profiles_changed", res.ProfilesChanged,
			"flow_rows", res.FlowRows)
		return nil
	}
}

// ScheduleRollupJob enqueues the first run at arena-worker startup unless one
// is already pending or claimed. Every later run is self-scheduled.
func ScheduleRollupJob(ctx context.Context, pool *pgxpool.Pool) error {
	var cnt int64
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM worker_jobs WHERE job_type = $1 AND status IN ('pending', 'claimed')`,
		RollupJobType).Scan(&cnt); err != nil {
		return fmt.Errorf("analytics: check initial rollup job: %w", err)
	}
	if cnt > 0 {
		return nil
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
		 VALUES ($1, '{}', 3, 'pending', now())`, RollupJobType); err != nil {
		return fmt.Errorf("analytics: enqueue initial rollup job: %w", err)
	}
	return nil
}
