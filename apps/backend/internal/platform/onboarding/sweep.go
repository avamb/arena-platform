package onboarding

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/authemail"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/opsalert"
)

// JobTypeSweep is the worker_jobs.job_type of the housekeeping job.
const JobTypeSweep = "onboarding.sweep"

// SweepInterval is the gap between runs. Nothing here is urgent.
const SweepInterval = 10 * time.Minute

const (
	sweepBatch = 100
	// queueReminderAfter / queueReminderEvery: an application waiting for the
	// operator longer than a day is mentioned again every 12 hours.
	queueReminderAfter = 24 * time.Hour
	queueReminderEvery = 12 * time.Hour
)

// reminderDays: the inactivity after which reminder number i is sent.
var reminderDays = []int{3, 14, 60}

// SweepResult counts what a run did.
type SweepResult struct {
	Expired, Reminded, Purged, QueueReminded int
}

// Sweep expires abandoned drafts, sends the 3/14/60-day reminders, erases the
// personal data of old expired and rejected applications and nudges the
// operator about applications that have waited a day.
func (s *Service) Sweep(ctx context.Context) (SweepResult, error) {
	var res SweepResult
	set, err := loadSettings(ctx, s.pool)
	if err != nil {
		return res, err
	}
	now := s.now()

	tag, err := s.pool.Exec(ctx, `
WITH gone AS (
  UPDATE onboarding_applications SET status = 'expired', updated_at = $1
  WHERE status = 'draft' AND expires_at < $1 AND purged_at IS NULL RETURNING id)
INSERT INTO onboarding_application_events (application_id, kind, actor_type)
SELECT id, 'expired', 'system' FROM gone`, now)
	if err != nil {
		return res, fmt.Errorf("onboarding: expire drafts: %w", err)
	}
	res.Expired = int(tag.RowsAffected())

	for stage, days := range reminderDays {
		ids, err := s.dueReminders(ctx, stage, days, now)
		if err != nil {
			return res, err
		}
		for _, id := range ids {
			if err := s.sendReminder(ctx, id, now); err != nil {
				s.logger.Warn("onboarding: reminder failed", slog.String("application_id", id.String()), slog.Any("error", err))
				continue
			}
			res.Reminded++
		}
	}

	rows, err := s.pool.Query(ctx, `
SELECT id FROM onboarding_applications
WHERE purged_at IS NULL AND status IN ('expired', 'rejected') AND updated_at < $1::timestamptz - make_interval(days => $2)
ORDER BY updated_at LIMIT $3`, now, set.PurgeAfterDays, sweepBatch)
	if err != nil {
		return res, err
	}
	var purge []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return res, err
		}
		purge = append(purge, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return res, err
	}
	for _, id := range purge {
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return res, err
		}
		if err := purgeRow(ctx, tx, id, now); err != nil {
			_ = tx.Rollback(ctx)
			return res, err
		}
		if err := addEvent(ctx, tx, id, "purged", ActorSystem, map[string]any{"retention": true}, nil); err != nil {
			_ = tx.Rollback(ctx)
			return res, err
		}
		if err := tx.Commit(ctx); err != nil {
			return res, err
		}
		res.Purged++
	}

	n, err := s.nudgeQueue(ctx, now)
	if err != nil {
		return res, err
	}
	res.QueueReminded = n
	return res, nil
}

// dueReminders lists confirmed drafts that have been quiet for `days` and have
// received exactly `stage` reminders so far.
func (s *Service) dueReminders(ctx context.Context, stage, days int, now time.Time) ([]uuid.UUID, error) {
	rows, err := s.pool.Query(ctx, `
SELECT id FROM onboarding_applications
WHERE status = 'draft' AND purged_at IS NULL AND email_confirmed_at IS NOT NULL
  AND reminders_sent = $1 AND last_activity_at < $2::timestamptz - make_interval(days => $3)
  AND (last_reminder_at IS NULL OR last_reminder_at < $2::timestamptz - interval '1 day')
ORDER BY last_activity_at LIMIT $4`, stage, now, days, sweepBatch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Service) sendReminder(ctx context.Context, id uuid.UUID, now time.Time) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	app, err := scanApplication(tx.QueryRow(ctx,
		`SELECT `+appColumns+` FROM onboarding_applications WHERE id = $1 AND status = 'draft' AND purged_at IS NULL FOR UPDATE`, id))
	if app, err = orNotFound(app, err); err != nil {
		return err
	}
	resume, hash, err := newToken()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
UPDATE onboarding_applications SET resume_token_hash = $2, resume_token_expires_at = $3,
  reminders_sent = reminders_sent + 1, last_reminder_at = $4 WHERE id = $1`,
		id, hash, now.Add(resumeLinkTTL), now); err != nil {
		return err
	}
	if err := s.queueEmail(ctx, tx, authemail.OnboardingKindReminder, app, resume, nil, ""); err != nil {
		return err
	}
	if err := addEvent(ctx, tx, id, "reminder_sent", ActorSystem, map[string]any{"number": app.RemindersSent + 1}, nil); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// nudgeQueue tells the operator about applications that have waited more than
// a day, at most once per queueReminderEvery for each.
func (s *Service) nudgeQueue(ctx context.Context, now time.Time) (int, error) {
	rows, err := s.pool.Query(ctx, `
SELECT a.id, coalesce(a.org_name, ''), coalesce(a.country, ''), a.submitted_at
FROM onboarding_applications a
WHERE a.status = 'pending_approval' AND a.purged_at IS NULL AND a.submitted_at < $1
  AND NOT EXISTS (SELECT 1 FROM onboarding_application_events e
                  WHERE e.application_id = a.id AND e.kind = 'queue_reminder' AND e.created_at > $2)
ORDER BY a.submitted_at LIMIT 20`, now.Add(-queueReminderAfter), now.Add(-queueReminderEvery))
	if err != nil {
		return 0, err
	}
	type waiting struct {
		id          uuid.UUID
		org, ctry   string
		submittedAt time.Time
	}
	var list []waiting
	for rows.Next() {
		var w waiting
		if err := rows.Scan(&w.id, &w.org, &w.ctry, &w.submittedAt); err != nil {
			rows.Close()
			return 0, err
		}
		list = append(list, w)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(list) == 0 {
		return 0, nil
	}
	lines := []string{fmt.Sprintf("⏰ <b>Заявки ждут решения больше суток: %d</b>", len(list))}
	for _, w := range list {
		hours := int(now.Sub(w.submittedAt).Hours())
		line := fmt.Sprintf("• %s (%s), %d ч", opsalert.EscapeHTML(w.org), opsalert.EscapeHTML(w.ctry), hours)
		if l := s.link(w.id); l != "" {
			line += " " + l
		}
		lines = append(lines, line)
		if err := addEvent(ctx, s.pool, w.id, "queue_reminder", ActorSystem, nil, nil); err != nil {
			return 0, err
		}
	}
	s.send(ctx, strings.Join(lines, "\n"))
	return len(list), nil
}

// ─── worker wiring ───────────────────────────────────────────────────────────

// NewSweepHandler returns the worker handler for onboarding.sweep. It
// reschedules itself even when a run fails, so a transient error cannot stop
// the cadence.
func NewSweepHandler(svc *Service, pool *pgxpool.Pool, logger *slog.Logger) func(ctx context.Context, payload []byte) error {
	if logger == nil {
		logger = slog.Default()
	}
	return func(ctx context.Context, _ []byte) error {
		res, runErr := svc.Sweep(ctx)
		if _, err := pool.Exec(ctx, `INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
VALUES ($1, '{}', 3, 'pending', $2)`, JobTypeSweep, time.Now().Add(SweepInterval)); err != nil {
			return fmt.Errorf("onboarding: schedule next sweep: %w", err)
		}
		if runErr != nil {
			return runErr
		}
		logger.Info("onboarding sweep complete", "expired", res.Expired, "reminded", res.Reminded,
			"purged", res.Purged, "queue_reminded", res.QueueReminded)
		return nil
	}
}

// ScheduleInitialSweep enqueues the first sweep when none is pending.
func ScheduleInitialSweep(ctx context.Context, pool *pgxpool.Pool) error {
	var cnt int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM worker_jobs WHERE job_type = $1 AND status IN ('pending', 'claimed')`,
		JobTypeSweep).Scan(&cnt); err != nil {
		return fmt.Errorf("onboarding: check initial sweep job: %w", err)
	}
	if cnt > 0 {
		return nil
	}
	if _, err := pool.Exec(ctx, `INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
VALUES ($1, '{}', 3, 'pending', now())`, JobTypeSweep); err != nil {
		return fmt.Errorf("onboarding: enqueue initial sweep job: %w", err)
	}
	return nil
}
