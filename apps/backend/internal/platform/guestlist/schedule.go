package guestlist

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/email"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// JobType is the self-scheduling worker job that writes the due deliveries
// and mails the e-mail ones.
const JobType = "guest_list.schedule"

// DefaultInterval is how often the job looks at the clock.
const DefaultInterval = time.Minute

// emailBatch bounds the letters one pass sends.
const emailBatch = 20

// Slots, in the order a session reaches them.
const (
	SlotH24   = "h24"
	SlotH3    = "h3"
	SlotDoors = "doors"
)

// Short reasons stored in guest_list_deliveries.last_error. They never carry
// data from the file.
const (
	ReasonSessionGone = "session_gone"
	ReasonNoTickets   = "no_tickets"
	ReasonTooMany     = "too_many_rows"
	ReasonBuildFailed = "build_failed"
	ReasonSendFailed  = "send_failed"
	ReasonNoSender    = "no_sender"
)

// Options configures the job.
type Options struct {
	Pool     *pgxpool.Pool
	Sender   email.Sender
	Logger   *slog.Logger
	Interval time.Duration
}

// Scheduler writes due deliveries and mails the e-mail ones.
type Scheduler struct {
	pool     *pgxpool.Pool
	sender   email.Sender
	logger   *slog.Logger
	interval time.Duration
}

// NewScheduler builds the job; a zero interval takes the default.
func NewScheduler(o Options) *Scheduler {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Interval <= 0 {
		o.Interval = DefaultInterval
	}
	return &Scheduler{pool: o.Pool, sender: o.Sender, logger: o.Logger, interval: o.Interval}
}

// ScheduleInitialJob seeds the queue once at worker start: a pending or
// claimed run means the chain is alive and nothing is added.
func ScheduleInitialJob(ctx context.Context, pool *pgxpool.Pool) error {
	var cnt int64
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM worker_jobs
		 WHERE job_type = $1 AND status IN ('pending', 'claimed')`, JobType).Scan(&cnt); err != nil {
		return fmt.Errorf("guestlist: check initial job: %w", err)
	}
	if cnt > 0 {
		return nil
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
		VALUES ($1, '{}', 3, 'pending', now())`, JobType); err != nil {
		return fmt.Errorf("guestlist: enqueue initial job: %w", err)
	}
	return nil
}

// NewHandler is the worker handler of guest_list.schedule. It queues the next
// run BEFORE the work, so a pass that fails or panics never ends the chain;
// a failure is logged and the job still succeeds.
func NewHandler(o Options) func(ctx context.Context, payload []byte) error {
	s := NewScheduler(o)
	return func(ctx context.Context, _ []byte) error {
		if err := scheduleNext(ctx, s.pool, time.Now().Add(s.interval)); err != nil {
			return err
		}
		written, mailed, err := s.RunOnce(ctx, time.Now().UTC())
		if err != nil {
			s.logger.Warn("guest list schedule failed", slog.String("error", err.Error()))
		}
		if written > 0 || mailed > 0 {
			s.logger.Info("guest list schedule", slog.Int64("due", written), slog.Int("mailed", mailed))
		}
		return nil
	}
}

// scheduleNext queues the next run unless one is pending. The advisory lock
// serializes two runs that would otherwise both see no pending row under READ
// COMMITTED and fork a second chain.
func scheduleNext(ctx context.Context, pool *pgxpool.Pool, at time.Time) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("guestlist: schedule next run: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, JobType); err != nil {
		return fmt.Errorf("guestlist: schedule next run: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
		SELECT $1, '{}', 3, 'pending', $2
		WHERE NOT EXISTS (SELECT 1 FROM worker_jobs WHERE job_type = $1 AND status = 'pending')`,
		JobType, at); err != nil {
		return fmt.Errorf("guestlist: schedule next run: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("guestlist: schedule next run: %w", err)
	}
	return nil
}

// RunOnce is one pass: write the deliveries whose moment came, then mail the
// e-mail ones. now is injectable for tests. Telegram deliveries are left for
// the bot, which claims them through POST /v1/bot/guest-list-deliveries/claim.
func (s *Scheduler) RunOnce(ctx context.Context, now time.Time) (written int64, mailed int, err error) {
	q := gen.New(s.pool)
	written, err = q.ScheduleGuestListDeliveries(ctx, now, gen.GuestListGrace)
	if err != nil {
		return 0, 0, err
	}
	mailed, err = s.mail(ctx, q)
	return written, mailed, err
}

func (s *Scheduler) mail(ctx context.Context, q *gen.Queries) (int, error) {
	if s.sender == nil || email.IsDevOnly(s.sender) {
		// No production sender: e-mail rows wait. They expire 2 hours after
		// their moment, so a later configured sender does not mail old lists.
		return 0, nil
	}
	ds, err := q.ClaimGuestListEmailDeliveries(ctx, emailBatch)
	if err != nil {
		return 0, err
	}
	sent := 0
	for _, d := range ds {
		outcome, reason := s.mailOne(ctx, q, d)
		var r *string
		if reason != "" {
			r = &reason
		}
		if _, ferr := q.FinishGuestListDelivery(ctx, d.ID, d.Attempt, outcome, r); ferr != nil {
			return sent, ferr
		}
		if outcome == gen.GuestListSent {
			sent++
		}
	}
	return sent, nil
}

// mailOne builds and sends one letter. The person is told nothing when the
// list cannot be built: the bot's button and the event center still serve it.
func (s *Scheduler) mailOne(ctx context.Context, q *gen.Queries, d gen.GuestListDelivery) (outcome, reason string) {
	head, err := q.GetExportSessionHeader(ctx, d.SessionID, d.OrgID)
	if errors.Is(err, pgx.ErrNoRows) {
		return gen.GuestListSkipped, ReasonSessionGone
	}
	if err != nil {
		return gen.GuestListFailed, ReasonBuildFailed
	}
	loc := Location(d.SessionTimezone)
	locale := NormalizeLocale(d.Locale)
	file, err := Build(ctx, q, d.OrgID, d.SessionID, locale, loc, MaxRows, XLSX)
	switch {
	case errors.Is(err, ErrTooManyRows):
		return gen.GuestListSkipped, ReasonTooMany
	case err != nil:
		return gen.GuestListFailed, ReasonBuildFailed
	case file.Rows == 0:
		return gen.GuestListSkipped, ReasonNoTickets
	}
	slug := ""
	if head.EventSlug != nil {
		slug = *head.EventSlug
	}
	letter := RenderEmail(locale, d.Slot, d.EventName, d.SessionStartAt, loc, file.Rows)
	msg := email.Message{
		To:       d.Email,
		Subject:  letter.Subject,
		HTMLBody: letter.HTML,
		TextBody: letter.Text,
		Attachments: []email.Attachment{{
			Filename:    FileName(head.EventName, slug, d.SessionStartAt, loc, XLSX),
			ContentType: file.ContentType,
			Data:        file.Body,
		}},
	}
	if err := s.sender.Send(ctx, msg); err != nil {
		// The address and the file never reach the log.
		s.logger.Warn("guest list e-mail not sent", slog.String("delivery_id", d.ID.String()))
		return gen.GuestListFailed, ReasonSendFailed
	}
	return gen.GuestListSent, ""
}
