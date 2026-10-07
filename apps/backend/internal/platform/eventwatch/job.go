package eventwatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// JobType is the self-scheduling worker job that runs the watcher.
const JobType = "events.change_watch"

// DefaultInterval is how often the watcher looks at the published events.
const DefaultInterval = 30 * time.Second

// DefaultStableFor is how long a difference must stay the same before it is
// announced. An event created through the bot is saved date by date, and the
// first date may already be published: announcing at once would announce half
// an event and then its "changes".
const DefaultStableFor = 60 * time.Second

// Announcer delivers a message to the operator. salesnotify.Dispatcher is the
// production one; it does nothing when no bot token is configured.
type Announcer interface {
	AnnounceEventChange(ctx context.Context, text string)
}

// Scheduler enqueues the next run.
type Scheduler interface {
	ScheduleNext(ctx context.Context, at time.Time) error
}

// PGScheduler schedules through worker_jobs, like every self-scheduling job.
type PGScheduler struct{ pool *pgxpool.Pool }

// NewPGScheduler wraps a pool.
func NewPGScheduler(pool *pgxpool.Pool) *PGScheduler { return &PGScheduler{pool: pool} }

// ScheduleNext inserts the next events.change_watch job.
func (s *PGScheduler) ScheduleNext(ctx context.Context, at time.Time) error {
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
		VALUES ($1, '{}', 3, 'pending', $2)`, JobType, at); err != nil {
		return fmt.Errorf("eventwatch: schedule next run: %w", err)
	}
	return nil
}

// ScheduleInitialJob seeds the queue once at worker start: a pending or
// claimed run means the chain is alive and nothing is added.
func ScheduleInitialJob(ctx context.Context, pool *pgxpool.Pool) error {
	var cnt int64
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM worker_jobs
		 WHERE job_type = $1 AND status IN ('pending', 'claimed')`, JobType).Scan(&cnt); err != nil {
		return fmt.Errorf("eventwatch: check initial job: %w", err)
	}
	if cnt > 0 {
		return nil
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
		VALUES ($1, '{}', 3, 'pending', now())`, JobType); err != nil {
		return fmt.Errorf("eventwatch: enqueue initial job: %w", err)
	}
	return nil
}

// Options configures a Watcher.
type Options struct {
	Pool           *pgxpool.Pool
	Announcer      Announcer
	Logger         *slog.Logger
	Interval       time.Duration
	StableFor      time.Duration
	TicketsBaseURL string
	Scheduler      Scheduler
}

// Watcher compares the published events with what the operator was told.
type Watcher struct {
	pool      *pgxpool.Pool
	announcer Announcer
	logger    *slog.Logger
	interval  time.Duration
	stableFor time.Duration
	base      string
	scheduler Scheduler
}

// NewWatcher builds a watcher; zero durations take the defaults.
func NewWatcher(o Options) *Watcher {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Interval <= 0 {
		o.Interval = DefaultInterval
	}
	if o.StableFor < 0 {
		o.StableFor = 0
	} else if o.StableFor == 0 {
		o.StableFor = DefaultStableFor
	}
	return &Watcher{
		pool: o.Pool, announcer: o.Announcer, logger: o.Logger, interval: o.Interval,
		stableFor: o.StableFor, base: o.TicketsBaseURL, scheduler: o.Scheduler,
	}
}

// NewHandler is the worker handler of events.change_watch. It always
// schedules the next run, even after a failed one, so a broken pass can never
// end the chain; the failure is logged and the job still succeeds.
func NewHandler(o Options) func(ctx context.Context, payload []byte) error {
	w := NewWatcher(o)
	return func(ctx context.Context, _ []byte) error {
		announced, err := w.RunOnce(ctx, time.Now().UTC())
		if err != nil {
			w.logger.Warn("event change watch failed", "error", err.Error())
		} else if announced > 0 {
			w.logger.Info("event change watch announced", "messages", announced)
		}
		if w.scheduler != nil {
			if serr := w.scheduler.ScheduleNext(ctx, time.Now().Add(w.interval)); serr != nil {
				return serr
			}
		}
		return nil
	}
}

type row struct {
	announced       *Snapshot
	announcedDigest string
	observedDigest  string
	observedAt      time.Time
}

// RunOnce is one pass; now is injectable for tests. It returns how many
// messages it sent.
func (w *Watcher) RunOnce(ctx context.Context, now time.Time) (int, error) {
	events, err := Load(ctx, w.pool)
	if err != nil {
		return 0, err
	}
	seeded, err := w.isSeeded(ctx)
	if err != nil {
		return 0, err
	}
	if !seeded {
		return 0, w.seed(ctx, events, now)
	}
	rows, err := w.rows(ctx)
	if err != nil {
		return 0, err
	}

	sent := 0
	for _, ev := range events {
		digest := Digest(ev.Snapshot)
		r, known := rows[ev.ID]

		switch {
		case !known:
			// First sight of a published event: start the clock, say nothing yet.
			if err := w.insertObserved(ctx, ev, digest, now); err != nil {
				return sent, err
			}

		case r.announced != nil && r.announcedDigest == digest:
			// Back to (or still at) what the operator was told.
			if r.observedDigest != digest {
				if err := w.setObserved(ctx, ev.ID, digest, now); err != nil {
					return sent, err
				}
			}

		case r.observedDigest != digest:
			// Still changing: restart the clock.
			if err := w.setObserved(ctx, ev.ID, digest, now); err != nil {
				return sent, err
			}

		case now.Sub(r.observedAt) < w.stableFor:
			// The same difference, but not for long enough yet.

		default:
			text := ""
			if r.announced == nil {
				text = NewEventText(ev, w.base)
			} else {
				text = ChangedText(*r.announced, ev, w.base)
			}
			// Record first, send second: a crash between the two loses one
			// message instead of repeating it forever.
			if err := w.setAnnounced(ctx, ev, digest, now); err != nil {
				return sent, err
			}
			if text != "" && w.announcer != nil {
				w.announcer.AnnounceEventChange(ctx, text)
				sent++
			}
		}
	}
	return sent, nil
}

func (w *Watcher) isSeeded(ctx context.Context) (bool, error) {
	var at time.Time
	err := w.pool.QueryRow(ctx, `SELECT seeded_at FROM event_watch_state WHERE id`).Scan(&at)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("eventwatch: read state: %w", err)
	}
	return true, nil
}

// seed records the events that exist right now as already announced, so
// switching the watcher on never announces the old catalogue.
func (w *Watcher) seed(ctx context.Context, events []Event, now time.Time) error {
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("eventwatch: begin seed: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, ev := range events {
		raw, err := json.Marshal(ev.Snapshot)
		if err != nil {
			return fmt.Errorf("eventwatch: marshal snapshot: %w", err)
		}
		d := Digest(ev.Snapshot)
		if _, err := tx.Exec(ctx, `
			INSERT INTO event_watch_snapshots
			       (event_id, org_id, announced, announced_digest, observed_digest, observed_at, announced_at)
			VALUES ($1::uuid, $2::uuid, $3, $4, $4, $5, $5)
			ON CONFLICT (event_id) DO NOTHING`, ev.ID, ev.OrgID, raw, d, now); err != nil {
			return fmt.Errorf("eventwatch: seed snapshot: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO event_watch_state (id, seeded_at) VALUES (true, $1)
		ON CONFLICT (id) DO NOTHING`, now); err != nil {
		return fmt.Errorf("eventwatch: seed state: %w", err)
	}
	return tx.Commit(ctx)
}

func (w *Watcher) rows(ctx context.Context) (map[string]row, error) {
	rs, err := w.pool.Query(ctx, `
		SELECT event_id::text, announced, COALESCE(announced_digest, ''), observed_digest, observed_at
		  FROM event_watch_snapshots`)
	if err != nil {
		return nil, fmt.Errorf("eventwatch: read snapshots: %w", err)
	}
	defer rs.Close()
	out := map[string]row{}
	for rs.Next() {
		var id string
		var raw []byte
		var r row
		if err := rs.Scan(&id, &raw, &r.announcedDigest, &r.observedDigest, &r.observedAt); err != nil {
			return nil, fmt.Errorf("eventwatch: scan snapshot: %w", err)
		}
		if len(raw) > 0 {
			var s Snapshot
			if err := json.Unmarshal(raw, &s); err != nil {
				return nil, fmt.Errorf("eventwatch: decode snapshot of %s: %w", id, err)
			}
			r.announced = &s
		}
		out[id] = r
	}
	return out, rs.Err()
}

func (w *Watcher) insertObserved(ctx context.Context, ev Event, digest string, now time.Time) error {
	if _, err := w.pool.Exec(ctx, `
		INSERT INTO event_watch_snapshots (event_id, org_id, observed_digest, observed_at)
		VALUES ($1::uuid, $2::uuid, $3, $4)
		ON CONFLICT (event_id) DO NOTHING`, ev.ID, ev.OrgID, digest, now); err != nil {
		return fmt.Errorf("eventwatch: record new event: %w", err)
	}
	return nil
}

func (w *Watcher) setObserved(ctx context.Context, eventID, digest string, now time.Time) error {
	if _, err := w.pool.Exec(ctx, `
		UPDATE event_watch_snapshots
		   SET observed_digest = $2, observed_at = $3, updated_at = now()
		 WHERE event_id = $1::uuid`, eventID, digest, now); err != nil {
		return fmt.Errorf("eventwatch: record observation: %w", err)
	}
	return nil
}

func (w *Watcher) setAnnounced(ctx context.Context, ev Event, digest string, now time.Time) error {
	raw, err := json.Marshal(ev.Snapshot)
	if err != nil {
		return fmt.Errorf("eventwatch: marshal snapshot: %w", err)
	}
	if _, err := w.pool.Exec(ctx, `
		UPDATE event_watch_snapshots
		   SET announced = $2, announced_digest = $3, observed_digest = $3,
		       observed_at = $4, announced_at = $4, updated_at = now()
		 WHERE event_id = $1::uuid`, ev.ID, raw, digest, now); err != nil {
		return fmt.Errorf("eventwatch: record announcement: %w", err)
	}
	return nil
}
