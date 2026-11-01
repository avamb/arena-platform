package analytics

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PartitionsJobType is the worker_jobs.job_type of the monthly partition
// maintenance (spec 50 section 6).
const PartitionsJobType = "analytics.partitions"

// Partition policy: three months ahead are always ready, raw events are kept
// 13 months (the partition of an older month is dropped whole).
const (
	PartitionsAhead      = 3
	RetentionMonths      = 13
	PartitionsInterval   = 6 * time.Hour
	partitionLockTimeout = "5s"
)

var partitionNameRe = regexp.MustCompile(`^events_y(\d{4})m(\d{2})$`)

// MonthStart returns the first instant of t's UTC month.
func MonthStart(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// PartitionName is the table name of the partition holding t's month.
func PartitionName(t time.Time) string {
	m := MonthStart(t)
	return fmt.Sprintf("events_y%04dm%02d", m.Year(), int(m.Month()))
}

// PartitionsWanted lists the first instants of the months that must have a
// partition at now: the current one and PartitionsAhead more.
func PartitionsWanted(now time.Time) []time.Time {
	first := MonthStart(now)
	out := make([]time.Time, 0, PartitionsAhead+1)
	for i := 0; i <= PartitionsAhead; i++ {
		out = append(out, first.AddDate(0, i, 0))
	}
	return out
}

// RetentionCutoff is the first month that is still kept at now: a partition
// of an earlier month is dropped.
func RetentionCutoff(now time.Time) time.Time {
	return MonthStart(now).AddDate(0, -RetentionMonths, 0)
}

// ParsePartitionName reads the month out of a partition table name.
func ParsePartitionName(name string) (time.Time, bool) {
	m := partitionNameRe.FindStringSubmatch(name)
	if m == nil {
		return time.Time{}, false
	}
	y, _ := strconv.Atoi(m[1])
	mo, _ := strconv.Atoi(m[2])
	if mo < 1 || mo > 12 {
		return time.Time{}, false
	}
	return time.Date(y, time.Month(mo), 1, 0, 0, 0, 0, time.UTC), true
}

// PartitionsToDrop picks, among existing partition table names, the ones
// older than the retention at now. A name that does not follow the
// events_yYYYYmMM convention is never touched.
func PartitionsToDrop(existing []string, now time.Time) []string {
	cutoff := RetentionCutoff(now)
	var out []string
	for _, n := range existing {
		if m, ok := ParsePartitionName(n); ok && m.Before(cutoff) {
			out = append(out, n)
		}
	}
	return out
}

// PartitionDB is the part of the pool the maintenance needs.
type PartitionDB interface {
	BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// MaintainPartitions creates the missing partitions and drops the expired
// ones. It is idempotent. It returns the created and dropped table names.
func MaintainPartitions(ctx context.Context, db PartitionDB, now time.Time) (created, dropped []string, err error) {
	existing, err := listPartitions(ctx, db)
	if err != nil {
		return nil, nil, err
	}
	have := make(map[string]bool, len(existing))
	for _, n := range existing {
		have[n] = true
	}
	tx, err := db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, nil, fmt.Errorf("analytics: begin partition tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "SET LOCAL lock_timeout = '"+partitionLockTimeout+"'"); err != nil {
		return nil, nil, fmt.Errorf("analytics: set lock timeout: %w", err)
	}
	for _, m := range PartitionsWanted(now) {
		name := PartitionName(m)
		if have[name] {
			continue
		}
		// Names and bounds come from time arithmetic, never from input.
		q := fmt.Sprintf(
			"CREATE TABLE IF NOT EXISTS analytics.%s PARTITION OF analytics.events FOR VALUES FROM ('%s') TO ('%s')",
			name, partitionBound(m), partitionBound(m.AddDate(0, 1, 0)))
		if _, err := tx.Exec(ctx, q); err != nil {
			return nil, nil, fmt.Errorf("analytics: create partition %s: %w", name, err)
		}
		created = append(created, name)
	}
	for _, name := range PartitionsToDrop(existing, now) {
		if _, err := tx.Exec(ctx, "DROP TABLE IF EXISTS analytics."+name); err != nil {
			return nil, nil, fmt.Errorf("analytics: drop partition %s: %w", name, err)
		}
		dropped = append(dropped, name)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, nil, fmt.Errorf("analytics: commit partitions: %w", err)
	}
	return created, dropped, nil
}

func listPartitions(ctx context.Context, db PartitionDB) ([]string, error) {
	rows, err := db.Query(ctx, `
		SELECT c.relname
		  FROM pg_inherits i
		  JOIN pg_class c ON c.oid = i.inhrelid
		  JOIN pg_class p ON p.oid = i.inhparent
		  JOIN pg_namespace n ON n.oid = p.relnamespace
		 WHERE n.nspname = 'analytics' AND p.relname = 'events'`)
	if err != nil {
		return nil, fmt.Errorf("analytics: list partitions: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("analytics: scan partition: %w", err)
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

// PartitionsScheduler enqueues the next maintenance run.
type PartitionsScheduler interface {
	ScheduleNext(ctx context.Context, at time.Time) error
}

// PGPartitionsScheduler is the worker_jobs-backed scheduler.
type PGPartitionsScheduler struct{ pool *pgxpool.Pool }

// NewPGPartitionsScheduler wraps a pool.
func NewPGPartitionsScheduler(pool *pgxpool.Pool) *PGPartitionsScheduler {
	return &PGPartitionsScheduler{pool: pool}
}

// ScheduleNext inserts a pending job due at `at`.
func (s *PGPartitionsScheduler) ScheduleNext(ctx context.Context, at time.Time) error {
	const q = `
		INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
		VALUES ($1, '{}', 3, 'pending', $2)`
	if _, err := s.pool.Exec(ctx, q, PartitionsJobType, at); err != nil {
		return fmt.Errorf("analytics: schedule next partition run: %w", err)
	}
	return nil
}

// PartitionsOptions configure the handler.
type PartitionsOptions struct {
	DB        PartitionDB
	Scheduler PartitionsScheduler
	Logger    *slog.Logger
	Interval  time.Duration
	// Now nil means time.Now.
	Now func() time.Time
}

// NewPartitionsHandler returns the handler of job_type PartitionsJobType. It
// has the worker.HandlerFunc signature. It always schedules the next run, even
// when this one fails, so one bad night cannot stop the maintenance for good:
// the failure is returned after the next run is queued.
func NewPartitionsHandler(o PartitionsOptions) func(ctx context.Context, payload []byte) error {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Interval <= 0 {
		o.Interval = PartitionsInterval
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return func(ctx context.Context, _ []byte) error {
		created, dropped, err := MaintainPartitions(ctx, o.DB, o.Now())
		if o.Scheduler != nil {
			if serr := o.Scheduler.ScheduleNext(ctx, o.Now().Add(o.Interval)); serr != nil && err == nil {
				err = serr
			}
		}
		if err != nil {
			return err
		}
		o.Logger.Info("analytics partitions maintained", "created", created, "dropped", dropped)
		return nil
	}
}

// SchedulePartitionsJob enqueues the first run at arena-worker startup unless
// one is already pending or claimed. Every later run is self-scheduled.
func SchedulePartitionsJob(ctx context.Context, pool *pgxpool.Pool) error {
	var cnt int64
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM worker_jobs WHERE job_type = $1 AND status IN ('pending', 'claimed')`,
		PartitionsJobType).Scan(&cnt); err != nil {
		return fmt.Errorf("analytics: check initial partitions job: %w", err)
	}
	if cnt > 0 {
		return nil
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
		 VALUES ($1, '{}', 3, 'pending', now())`, PartitionsJobType); err != nil {
		return fmt.Errorf("analytics: enqueue initial partitions job: %w", err)
	}
	return nil
}

// partitionBound spells a month boundary as a SQL timestamptz literal for the
// DDL of a partition (DDL cannot take bind parameters). The value comes from
// time arithmetic on a UTC month start, never from input.
func partitionBound(m time.Time) string {
	return m.UTC().Format("2006-01-02 15:04:05+00") // allow:timeformat DDL literal, not a wire timestamp
}
