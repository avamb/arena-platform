package analytics

// ux_digest.go — analytics.ux_digest (spec 50 section 10, stage A8): every
// Monday at 09:00 Madrid time the ops chat gets the five steps with the
// largest losses and the five most frequent errors of the week that ended on
// Sunday, each with its change against the week before. The message carries
// counts and dictionary names only (flow, step, error code, screen): no
// person, no e-mail, no text a person typed.

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"
	_ "time/tzdata" // Europe/Madrid must resolve on a host without system tzdata

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/opsalert"
)

// UXDigestJobType is the worker_jobs.job_type of the weekly UX digest.
const UXDigestJobType = "analytics.ux_digest"

// DigestSize is how many steps and how many errors the digest lists.
const DigestSize = 5

// digestKeyMax cuts one name in the message (dictionary values are short, the
// cut only protects the chat from a pathological one).
const digestKeyMax = 60

// DigestLocation is the zone the digest schedule is kept in.
func DigestLocation() *time.Location {
	loc, err := time.LoadLocation("Europe/Madrid")
	if err != nil {
		// Unreachable with the embedded tzdata; UTC keeps the job alive.
		return time.UTC
	}
	return loc
}

// NextDigestTime is the first Monday 09:00 Madrid time strictly after now.
func NextDigestTime(now time.Time) time.Time {
	loc := DigestLocation()
	local := now.In(loc)
	day := time.Date(local.Year(), local.Month(), local.Day(), 9, 0, 0, 0, loc)
	for day.Weekday() != time.Monday || !day.After(now) {
		day = time.Date(day.Year(), day.Month(), day.Day()+1, 9, 0, 0, 0, loc)
	}
	return day.UTC()
}

// DigestWeek is the week the digest of a run at `at` covers: the seven days
// Monday to Sunday that ended before the Madrid Monday of `at`. Days are UTC
// days, like flow_daily.
func DigestWeek(at time.Time) Period {
	local := at.In(DigestLocation())
	back := (int(local.Weekday()) + 6) % 7 // days since Monday
	monday := time.Date(local.Year(), local.Month(), local.Day()-back, 0, 0, 0, 0, time.UTC)
	return Period{From: monday.AddDate(0, 0, -7), To: monday.AddDate(0, 0, -1)}
}

// DigestStep is one step of the loss ranking.
type DigestStep struct {
	Surface      string
	Flow         string
	Step         string
	Entered      int
	LeftHere     int
	PreviousLeft int
}

// UXDigest is the data of one weekly message.
type UXDigest struct {
	Week   Period
	Steps  []DigestStep
	Errors []ErrorStat
}

// RankLosses picks the n steps where the most people left a flow, most first
// (ties: higher drop-off, then name). The pseudo flow of errors outside any
// flow carries no steps and is skipped, as is a step nobody left.
func RankLosses(in []FlowStepCompare, n int) []DigestStep {
	var all []DigestStep
	for _, c := range in {
		if c.Current.Flow == FlowScreens || c.Current.LeftHere <= 0 {
			continue
		}
		all = append(all, DigestStep{
			Surface: c.Current.Surface, Flow: c.Current.Flow, Step: c.Current.Step,
			Entered: c.Current.Entered, LeftHere: c.Current.LeftHere, PreviousLeft: c.Previous.LeftHere,
		})
	}
	rate := func(s DigestStep) float64 {
		if s.Entered <= 0 {
			return 0
		}
		return float64(s.LeftHere) / float64(s.Entered)
	}
	sort.SliceStable(all, func(i, j int) bool {
		a, b := all[i], all[j]
		switch {
		case a.LeftHere != b.LeftHere:
			return a.LeftHere > b.LeftHere
		case rate(a) != rate(b):
			return rate(a) > rate(b)
		case a.Surface != b.Surface:
			return a.Surface < b.Surface
		case a.Flow != b.Flow:
			return a.Flow < b.Flow
		}
		return a.Step < b.Step
	})
	if len(all) > n {
		all = all[:n]
	}
	return all
}

// BuildUXDigest reads the week and the one before it.
func BuildUXDigest(ctx context.Context, q Querier, week Period) (UXDigest, error) {
	d := UXDigest{Week: week}
	flows, err := QueryFlows(ctx, q, "", week)
	if err != nil {
		return d, err
	}
	d.Steps = RankLosses(flows, DigestSize)
	if d.Errors, err = QueryErrors(ctx, q, "", week, DigestSize); err != nil {
		return d, err
	}
	return d, nil
}

func digestName(s string) string {
	return opsalert.Truncate(opsalert.ScrubEmails(s), digestKeyMax)
}

func changeText(cur, prev int) string {
	if prev == 0 {
		if cur == 0 {
			return "no change"
		}
		return "new"
	}
	pct := ChangePct(cur, prev)
	sign := "+"
	if *pct < 0 {
		sign = ""
	}
	return fmt.Sprintf("%s%.0f%%", sign, *pct)
}

// FormatUXDigest renders the message (Telegram HTML parse mode, English like
// the other ops messages).
func FormatUXDigest(d UXDigest) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<b>UX digest, %s - %s</b>\n", d.Week.From.Format("2 Jan"), d.Week.To.Format("2 Jan 2006")) // allow:timeformat
	b.WriteString("Days are UTC. Change is against the week before.\n")

	b.WriteString("\n<b>Where people leave most</b>\n")
	if len(d.Steps) == 0 {
		b.WriteString("No exits recorded.\n")
	}
	for i, s := range d.Steps {
		drop := ""
		if s.Entered > 0 {
			drop = fmt.Sprintf(" (%.0f%% of %d)", float64(s.LeftHere)/float64(s.Entered)*100, s.Entered)
		}
		fmt.Fprintf(&b, "%d. %s / %s / %s: %d left%s, was %d, %s\n", i+1,
			opsalert.EscapeHTML(digestName(s.Surface)), opsalert.EscapeHTML(digestName(s.Flow)),
			opsalert.EscapeHTML(digestName(s.Step)), s.LeftHere, drop, s.PreviousLeft, changeText(s.LeftHere, s.PreviousLeft))
	}

	b.WriteString("\n<b>Most frequent errors</b>\n")
	if len(d.Errors) == 0 {
		b.WriteString("No errors recorded.\n")
	}
	for i, e := range d.Errors {
		what := e.Key
		if e.Place != "" {
			what += " at " + e.Place
		}
		if what == "" {
			what = "(no name)"
		}
		fmt.Fprintf(&b, "%d. %s %s [%s]: %d, was %d, %s\n", i+1, e.Kind,
			opsalert.EscapeHTML(digestName(what)), e.Surface, e.Count, e.Previous, changeText(e.Count, e.Previous))
	}
	return strings.TrimRight(b.String(), "\n")
}

// ─── worker wiring ─────────────────────────────────────────────────────────

// UXDigestScheduler enqueues the next digest.
type UXDigestScheduler interface {
	ScheduleNext(ctx context.Context, at time.Time) error
}

// PGUXDigestScheduler is the worker_jobs-backed scheduler.
type PGUXDigestScheduler struct{ pool *pgxpool.Pool }

// NewPGUXDigestScheduler wraps a pool.
func NewPGUXDigestScheduler(pool *pgxpool.Pool) *PGUXDigestScheduler {
	return &PGUXDigestScheduler{pool: pool}
}

// ScheduleNext inserts a pending job due at `at` unless one is already
// pending, so a restart or a retry never forks a second chain.
func (s *PGUXDigestScheduler) ScheduleNext(ctx context.Context, at time.Time) error {
	const q = `
INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
SELECT $1, '{}', 3, 'pending', $2
WHERE  NOT EXISTS (SELECT 1 FROM worker_jobs WHERE job_type = $1 AND status = 'pending')`
	if _, err := s.pool.Exec(ctx, q, UXDigestJobType, at); err != nil {
		return fmt.Errorf("analytics: schedule next ux digest: %w", err)
	}
	return nil
}

// UXDigestHandlerOptions configure the handler.
type UXDigestHandlerOptions struct {
	DB        Querier
	Notifier  opsalert.Notifier
	Scheduler UXDigestScheduler
	Logger    *slog.Logger
	// Now nil means time.Now.
	Now func() time.Time
}

// NewUXDigestHandler returns the handler of UXDigestJobType. The next Monday
// is queued BEFORE the work, so a digest that fails every retry does not end
// the chain. With no Notifier the digest is only logged.
func NewUXDigestHandler(o UXDigestHandlerOptions) func(ctx context.Context, payload []byte) error {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Notifier == nil {
		o.Notifier = opsalert.NewNoop(o.Logger)
	}
	return func(ctx context.Context, _ []byte) error {
		now := o.Now()
		if o.Scheduler != nil {
			if err := o.Scheduler.ScheduleNext(ctx, NextDigestTime(now)); err != nil {
				return err
			}
		}
		d, err := BuildUXDigest(ctx, o.DB, DigestWeek(now))
		if err != nil {
			return err
		}
		if err := o.Notifier.Send(ctx, FormatUXDigest(d)); err != nil {
			return fmt.Errorf("analytics: send ux digest: %w", err)
		}
		o.Logger.Info("analytics ux digest sent", "steps", len(d.Steps), "errors", len(d.Errors))
		return nil
	}
}

// ScheduleUXDigestJob enqueues the first digest at arena-worker startup, at
// the next Monday 09:00 Madrid time, unless one is already pending or
// claimed. Every later digest is self-scheduled.
func ScheduleUXDigestJob(ctx context.Context, pool *pgxpool.Pool) error {
	const q = `
INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
SELECT $1, '{}', 3, 'pending', $2
WHERE  NOT EXISTS (SELECT 1 FROM worker_jobs WHERE job_type = $1 AND status IN ('pending', 'claimed'))`
	if _, err := pool.Exec(ctx, q, UXDigestJobType, NextDigestTime(time.Now())); err != nil {
		return fmt.Errorf("analytics: enqueue initial ux digest job: %w", err)
	}
	return nil
}
