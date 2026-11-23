package metainsights

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
	_ "time/tzdata" // Europe/Madrid and the ad account zones must resolve on a host without system tzdata

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DefaultSweepInterval is how often meta.insights_sweep looks for due
// organizations.
const DefaultSweepInterval = 10 * time.Minute

// Due rules.
const (
	// NightlyStaleAfter: the nightly run is due once the last success is
	// older than this...
	NightlyStaleAfter = 20 * time.Hour
	// ...and the Madrid local hour is in [NightFromHour, NightToHour).
	NightFromHour = 3
	NightToHour   = 7
	// RetryAfter is the shortest gap between two runs the sweep starts for
	// one organization, so a run that keeps failing (no access, throttled)
	// is not repeated every 10 minutes. META_INSIGHTS_EVERY below it
	// shortens it.
	RetryAfter = time.Hour
)

var madrid = loadMadrid()

// loadMadrid resolves Europe/Madrid from the embedded tzdata; UTC is a last
// resort that never happens with time/tzdata linked in.
func loadMadrid() *time.Location {
	loc, err := time.LoadLocation("Europe/Madrid")
	if err != nil {
		return time.UTC
	}
	return loc
}

// Due decides whether the sweep queues a sync of an organization whose
// selected ad account is selectedAccount, and in which mode:
//   - backfill when the stored history belongs to another account or the
//     90-day backfill never finished;
//   - scheduled (the nightly run) when the last success is older than 20
//     hours and the Madrid local hour is 3 to 6;
//   - interval when every (META_INSIGHTS_EVERY, 0 = off) has passed since
//     the last success.
//
// No mode is due within RetryAfter (or every, when shorter) of the last run.
func Due(st State, selectedAccount string, now time.Time, every time.Duration) (mode string, due bool) {
	gap := RetryAfter
	if every > 0 && every < gap {
		gap = every
	}
	if st.LastRunAt != nil && now.Sub(*st.LastRunAt) < gap {
		return "", false
	}
	if st.AdAccountID != selectedAccount || st.BackfillDoneAt == nil {
		return ModeBackfill, true
	}
	h := now.In(madrid).Hour()
	if (st.LastSuccessAt == nil || now.Sub(*st.LastSuccessAt) > NightlyStaleAfter) && h >= NightFromHour && h < NightToHour {
		return ModeScheduled, true
	}
	if every > 0 && (st.LastSuccessAt == nil || now.Sub(*st.LastSuccessAt) >= every) {
		return ModeInterval, true
	}
	return "", false
}

// ScheduleInitialJob seeds the sweep once at worker start: a pending or
// claimed sweep means the chain is alive and nothing is added.
func ScheduleInitialJob(ctx context.Context, pool *pgxpool.Pool) error {
	var cnt int64
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM worker_jobs WHERE job_type = $1 AND status IN ('pending', 'claimed')`, SweepJobType).Scan(&cnt); err != nil {
		return fmt.Errorf("metainsights: check initial job: %w", err)
	}
	if cnt > 0 {
		return nil
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
		VALUES ($1, '{}', 3, 'pending', now())`, SweepJobType); err != nil {
		return fmt.Errorf("metainsights: enqueue initial job: %w", err)
	}
	return nil
}

// Sweep queues a sync for every organization with an ACTIVE connection, a
// token and a selected ad account that is Due. It reads only the database
// and never queues a second sync for an organization that has one waiting.
// It returns how many syncs it queued.
func (s *Syncer) Sweep(ctx context.Context, now time.Time) (int, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT c.org_id, a.meta_id,
		       st.ad_account_id, st.backfill_done_at, st.last_run_at, st.last_success_at
		  FROM org_meta_connections c
		  JOIN org_meta_assets a ON a.org_id = c.org_id AND a.kind = 'ad_account' AND a.selected
		  JOIN organizations o   ON o.id = c.org_id AND o.deleted_at IS NULL
		  LEFT JOIN meta_insights_state st ON st.org_id = c.org_id
		 WHERE c.status = 'active' AND c.access_token_enc IS NOT NULL
		 ORDER BY c.org_id`)
	if err != nil {
		return 0, err
	}
	type cand struct {
		org  uuid.UUID
		mode string
	}
	var due []cand
	for rows.Next() {
		var (
			org     uuid.UUID
			account string
			acct    *string
			st      State
		)
		if err := rows.Scan(&org, &account, &acct, &st.BackfillDoneAt, &st.LastRunAt, &st.LastSuccessAt); err != nil {
			rows.Close()
			return 0, err
		}
		if acct != nil {
			st.AdAccountID = *acct
		}
		if mode, ok := Due(st, account, now, s.every); ok {
			due = append(due, cand{org, mode})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	queued := 0
	for _, c := range due {
		ok, err := enqueueSync(ctx, s.pool, c.org, c.mode)
		if err != nil {
			return queued, err
		}
		if ok {
			queued++
		}
	}
	return queued, nil
}

// NewSweepHandler is the worker handler of meta.insights_sweep. It always
// schedules the next sweep, even after a failed pass, so the chain never
// ends; a failed pass is logged and the job still succeeds.
func (s *Syncer) NewSweepHandler() func(ctx context.Context, payload []byte) error {
	return func(ctx context.Context, _ []byte) error {
		if n, err := s.Sweep(ctx, time.Now().UTC()); err != nil {
			s.logger.Warn("metainsights: sweep failed", "error", err.Error())
		} else if n > 0 {
			s.logger.Info("metainsights: sweep queued syncs", "syncs", n)
		}
		if _, err := s.pool.Exec(ctx, `
			INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
			VALUES ($1, '{}', 3, 'pending', $2)`, SweepJobType, time.Now().Add(s.interval)); err != nil {
			return fmt.Errorf("metainsights: schedule next sweep: %w", err)
		}
		return nil
	}
}

// NewSyncHandler is the worker handler of meta.insights_sync. A bad payload
// is dropped (logged); a database failure is returned so the job retries; a
// Graph failure is stored by Run and the job succeeds.
func (s *Syncer) NewSyncHandler() func(ctx context.Context, payload []byte) error {
	return func(ctx context.Context, payload []byte) error {
		var p syncPayload
		if err := json.Unmarshal(payload, &p); err != nil {
			s.logger.Warn("metainsights: sync payload is not valid JSON, dropped")
			return nil
		}
		org, err := uuid.Parse(p.OrgID)
		if err != nil {
			s.logger.Warn("metainsights: sync payload has no valid org_id, dropped")
			return nil
		}
		switch p.Mode {
		case ModeBackfill, ModeScheduled, ModeInterval, ModeManual:
		default:
			p.Mode = ModeScheduled
		}
		res, err := s.Run(ctx, org, p.Mode, time.Now().UTC())
		if err != nil {
			return fmt.Errorf("metainsights: sync: %w", err)
		}
		if res.ErrorCode != "" {
			s.logger.Info("metainsights: sync ended without data", "org_id", org.String(), "code", res.ErrorCode)
		}
		return nil
	}
}
