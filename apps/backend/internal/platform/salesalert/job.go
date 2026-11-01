package salesalert

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/salesstate"
)

// JobType is the self-scheduling worker job that runs the watch.
const JobType = "sales_state.watch"

// DefaultInterval is how often the watch looks at the sessions.
const DefaultInterval = time.Minute

// StaleAfter is how long the sites may go without taking a state change
// before the owner is told (owner decision 2026-10-31: 15 minutes).
const StaleAfter = 15 * time.Minute

// staleHorizon: an older undelivered change is history, never announced.
const staleHorizon = 24 * time.Hour

// factsBatch bounds one ListSessionSalesFacts call.
const factsBatch = 500

// Announcer posts to the organization's own sales chats.
// salesnotify.Dispatcher is the production one; without a bot token it has
// no chats.
type Announcer interface {
	AnnounceToOrg(ctx context.Context, orgID, text string) (chats, delivered int, err error)
}

// Options configures the watch.
type Options struct {
	Pool      *pgxpool.Pool
	Announcer Announcer
	Logger    *slog.Logger
	Interval  time.Duration
}

// Watcher compares the sessions with what the organizers were told.
type Watcher struct {
	pool      *pgxpool.Pool
	announcer Announcer
	logger    *slog.Logger
	interval  time.Duration
}

// NewWatcher builds a watch; a zero interval takes the default.
func NewWatcher(o Options) *Watcher {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Interval <= 0 {
		o.Interval = DefaultInterval
	}
	return &Watcher{pool: o.Pool, announcer: o.Announcer, logger: o.Logger, interval: o.Interval}
}

// ScheduleInitialJob seeds the queue once at worker start: a pending or
// claimed run means the chain is alive and nothing is added.
func ScheduleInitialJob(ctx context.Context, pool *pgxpool.Pool) error {
	var cnt int64
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM worker_jobs
		 WHERE job_type = $1 AND status IN ('pending', 'claimed')`, JobType).Scan(&cnt); err != nil {
		return fmt.Errorf("salesalert: check initial job: %w", err)
	}
	if cnt > 0 {
		return nil
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
		VALUES ($1, '{}', 3, 'pending', now())`, JobType); err != nil {
		return fmt.Errorf("salesalert: enqueue initial job: %w", err)
	}
	return nil
}

// NewHandler is the worker handler of sales_state.watch. It always schedules
// the next run, even after a failed pass, so a broken pass never ends the
// chain; the failure is logged and the job still succeeds.
func NewHandler(o Options) func(ctx context.Context, payload []byte) error {
	w := NewWatcher(o)
	return func(ctx context.Context, _ []byte) error {
		if told, err := w.RunOnce(ctx, time.Now().UTC()); err != nil {
			w.logger.Warn("sales state watch failed", "error", err.Error())
		} else if told > 0 {
			w.logger.Info("sales state watch told organizers", "alerts", told)
		}
		if _, err := w.pool.Exec(ctx, `
			INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
			VALUES ($1, '{}', 3, 'pending', $2)`, JobType, time.Now().Add(w.interval)); err != nil {
			return fmt.Errorf("salesalert: schedule next run: %w", err)
		}
		return nil
	}
}

// RunOnce is one pass; now is injectable for tests. It returns how many
// alerts it raised. The very first pass of a database records what is
// already sold out or stale without telling anybody.
func (w *Watcher) RunOnce(ctx context.Context, now time.Time) (int, error) {
	var seeded bool
	if err := w.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM sales_alert_watch_state)`).Scan(&seeded); err != nil {
		return 0, err
	}
	told, err := w.soldOut(ctx, now, !seeded)
	if err != nil {
		return told, err
	}
	n, err := w.staleSites(ctx, now, !seeded)
	told += n
	if err != nil {
		return told, err
	}
	if !seeded {
		if _, err := w.pool.Exec(ctx, `INSERT INTO sales_alert_watch_state (id) VALUES (1) ON CONFLICT DO NOTHING`); err != nil {
			return told, err
		}
	}
	return told, nil
}

// IsSoldOut is the rule of a sell-out the organizer is told about: set by
// hand, or no free place and no unpaid hold left on a session that has
// places. A "coming soon" set by hand is never a sell-out.
func IsSoldOut(f gen.SessionSalesFactsRow, now time.Time) (sold, manual bool) {
	switch f.Manual {
	case salesstate.SoldOut:
		return true, true
	case "":
		facts := salesstate.FactsFromRow(f)
		return salesstate.Effective("", facts, now) == salesstate.SoldOut && f.HeldPlaces == 0, false
	}
	return false, false
}

func (w *Watcher) soldOut(ctx context.Context, now time.Time, silent bool) (int, error) {
	rows, err := w.pool.Query(ctx, `
		SELECT s.id FROM sessions s
		WHERE  s.deleted_at IS NULL AND s.status <> 'cancelled' AND s.end_at > $1`, now)
	if err != nil {
		return 0, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return 0, err
	}
	noticed := map[uuid.UUID]bool{}
	nrows, err := w.pool.Query(ctx, `SELECT session_id FROM session_soldout_notices`)
	if err != nil {
		return 0, err
	}
	nids, err := pgx.CollectRows(nrows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return 0, err
	}
	for _, id := range nids {
		noticed[id] = true
	}

	q := gen.New(w.pool)
	told := 0
	for start := 0; start < len(ids); start += factsBatch {
		end := min(start+factsBatch, len(ids))
		facts, err := q.ListSessionSalesFacts(ctx, ids[start:end], now)
		if err != nil {
			return told, err
		}
		for id, f := range facts {
			sold, manual := IsSoldOut(f, now)
			switch {
			case sold && !noticed[id]:
				ok, err := w.tellSoldOut(ctx, id, manual, silent)
				if err != nil {
					return told, err
				}
				if ok {
					told++
				}
			case !sold && noticed[id]:
				// Places came back: the next sell-out is told again.
				if _, err := w.pool.Exec(ctx, `DELETE FROM session_soldout_notices WHERE session_id = $1`, id); err != nil {
					return told, err
				}
			}
		}
	}
	return told, nil
}

// tellSoldOut records the notice and queues the e-mail in one transaction,
// then posts to the chats: a crash between the two loses a chat message
// rather than repeating it. It reports whether it told anybody.
func (w *Watcher) tellSoldOut(ctx context.Context, sessionID uuid.UUID, manual, silent bool) (bool, error) {
	var orgID uuid.UUID
	if err := w.pool.QueryRow(ctx, `
		SELECT e.org_id FROM sessions s JOIN events e ON e.id = s.event_id WHERE s.id = $1`, sessionID).Scan(&orgID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	actor := ""
	if manual {
		_ = w.pool.QueryRow(ctx, `
			SELECT actor_name FROM session_sales_state_changes
			WHERE session_id = $1 AND new_state = 'sold_out'
			ORDER BY created_at DESC LIMIT 1`, sessionID).Scan(&actor)
	}
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `
		INSERT INTO session_soldout_notices (session_id, org_id, manual) VALUES ($1, $2, $3)
		ON CONFLICT (session_id) DO NOTHING`, sessionID, orgID, manual)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 0 {
		return false, nil // another pass told it
	}
	if !silent {
		if err := enqueueEmail(ctx, tx, AlertEmailPayload{
			Kind: KindSoldOut, OrgID: orgID.String(), SessionID: sessionID.String(), Manual: manual, Actor: actor,
		}); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	if silent {
		return false, nil
	}
	if w.announcer != nil {
		in, err := LoadSessionInfo(ctx, w.pool, orgID, sessionID)
		if err != nil {
			w.logger.Warn("sales state watch: session info for a sell-out", "session_id", sessionID.String(), "error", err.Error())
			return true, nil
		}
		if _, _, err := w.announcer.AnnounceToOrg(ctx, orgID.String(), SoldOutText(in, manual, actor)); err != nil {
			w.logger.Warn("sales state watch: sell-out chat message", "session_id", sessionID.String(), "error", err.Error())
		}
	}
	return true, nil
}

type staleChange struct {
	id, sessionID, orgID, eventID uuid.UUID
	state                         string
	at                            time.Time
}

func (w *Watcher) staleSites(ctx context.Context, now time.Time, silent bool) (int, error) {
	rows, err := w.pool.Query(ctx, `
		SELECT c.id, c.session_id, c.org_id, c.event_id, c.new_state, c.created_at
		FROM   session_sales_state_changes c
		JOIN   outbox_events o ON o.id = c.outbox_event_id
		WHERE  c.site_alerted_at IS NULL
		  AND  o.processed_at IS NULL
		  AND  c.created_at <= $1 AND c.created_at > $2
		  AND  c.id = (SELECT l.id FROM session_sales_state_changes l
		                WHERE l.session_id = c.session_id ORDER BY l.created_at DESC, l.id DESC LIMIT 1)
		ORDER  BY c.created_at`, now.Add(-StaleAfter), now.Add(-staleHorizon))
	if err != nil {
		return 0, err
	}
	var changes []staleChange
	for rows.Next() {
		var c staleChange
		if err := rows.Scan(&c.id, &c.sessionID, &c.orgID, &c.eventID, &c.state, &c.at); err != nil {
			rows.Close()
			return 0, err
		}
		changes = append(changes, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	q := gen.New(w.pool)
	told := 0
	for _, c := range changes {
		subs, err := q.ListWPSubscribersForEvent(ctx, c.eventID)
		if err != nil {
			return told, err
		}
		tag, err := w.pool.Exec(ctx, `
			UPDATE session_sales_state_changes SET site_alerted_at = $2
			WHERE session_id = $1 AND site_alerted_at IS NULL AND created_at <= $3`, c.sessionID, now, c.at)
		if err != nil {
			return told, err
		}
		if tag.RowsAffected() == 0 || silent || len(subs) == 0 {
			continue // told already, the first pass, or no site sells the event
		}
		told++
		chats := 0
		if w.announcer != nil {
			if in, err := LoadSessionInfo(ctx, w.pool, c.orgID, c.sessionID); err == nil {
				minutes := int(now.Sub(c.at) / time.Minute)
				n, delivered, aerr := w.announcer.AnnounceToOrg(ctx, c.orgID.String(), SiteStaleText(in, c.state, minutes))
				if aerr != nil {
					w.logger.Warn("sales state watch: stale-site chat message", "session_id", c.sessionID.String(), "error", aerr.Error())
				}
				if delivered > 0 {
					chats = n
				}
			}
		}
		if chats > 0 {
			continue
		}
		// No chat took it: the owners get an e-mail instead.
		payload := AlertEmailPayload{Kind: KindSiteStale, OrgID: c.orgID.String(), SessionID: c.sessionID.String(), State: c.state, Sites: len(subs)}
		if err := enqueueEmail(ctx, w.pool, payload); err != nil {
			return told, err
		}
	}
	return told, nil
}

// execer is a pool or a transaction.
type execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

func enqueueEmail(ctx context.Context, db execer, p AlertEmailPayload) error {
	body, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, `
		INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
		VALUES ($1, $2::jsonb, 5, 'pending', now())`, JobTypeAlertEmail, body)
	return err
}
