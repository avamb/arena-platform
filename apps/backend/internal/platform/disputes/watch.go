package disputes

// watch.go — the self-scheduling dispute.watch job (spec 51.3 §8): tells the
// organization when a dispute opens, 7, 3 and 1 day before its deadline,
// when the deadline passed and when the dispute closed; tells the operator
// when one opens and when one expired unanswered (no buyer data). Each
// notice is claimed in payment_dispute_notices BEFORE it is sent: a crash
// loses a message rather than repeating it.

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/email"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/opsalert"
)

// JobWatch is the self-scheduling watch.
const JobWatch = "dispute.watch"

// WatchInterval is how often the watch runs.
const WatchInterval = 5 * time.Minute

// Announcer posts to the organization's own sales chats (salesnotify).
type Announcer interface {
	AnnounceToOrg(ctx context.Context, orgID, text string) (chats, delivered int, err error)
}

// Notifier configures the watch's outputs; any of them may be nil.
type Notifier struct {
	Announcer Announcer
	Ops       opsalert.Notifier
	Sender    email.Sender
	// EventCenterURL is the web event center's public base, for links.
	EventCenterURL string
}

// ScheduleWatch seeds the chain once at worker start.
func ScheduleWatch(ctx context.Context, pool *pgxpool.Pool) error {
	var cnt int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM worker_jobs WHERE job_type = $1 AND status IN ('pending','claimed')`, JobWatch).Scan(&cnt); err != nil {
		return err
	}
	if cnt > 0 {
		return nil
	}
	_, err := pool.Exec(ctx, `INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
		VALUES ($1, '{}', 3, 'pending', now())`, JobWatch)
	return err
}

// WatchHandler is the worker handler of dispute.watch. It schedules the
// next run first, so a failing pass never ends the chain.
func (s *Service) WatchHandler(n Notifier) func(ctx context.Context, payload []byte) error {
	return func(ctx context.Context, _ []byte) error {
		if _, err := s.db.Exec(ctx, `INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
			SELECT $1, '{}', 3, 'pending', $2
			WHERE NOT EXISTS (SELECT 1 FROM worker_jobs WHERE job_type = $1 AND status = 'pending')`,
			JobWatch, time.Now().Add(WatchInterval)); err != nil {
			return fmt.Errorf("disputes: schedule next watch: %w", err)
		}
		if told, err := s.RunWatch(ctx, n, s.now().UTC()); err != nil {
			s.logger.Warn("dispute watch failed", slog.String("error", err.Error()))
		} else if told > 0 {
			s.logger.Info("dispute watch told organizers", slog.Int("notices", told))
		}
		return nil
	}
}

// RunWatch is one pass; now is injectable for tests. It returns how many
// notices it claimed.
func (s *Service) RunWatch(ctx context.Context, n Notifier, now time.Time) (int, error) {
	told := 0
	// Deadlines that passed: the dispute is expired.
	if _, err := s.db.Exec(ctx, `UPDATE payment_disputes SET state = 'expired', outcome = 'expired',
			closed_at = COALESCE(closed_at, $1), updated_at = $1
		WHERE state IN ('needs_response','draft_ready') AND due_by IS NOT NULL AND due_by < $1`, now); err != nil {
		return 0, err
	}
	if _, err := s.failStaleEmails(ctx); err != nil {
		return 0, err
	}
	rows, err := s.db.Query(ctx, `SELECT `+disputeColumns+` FROM payment_disputes
		WHERE closed_at IS NULL OR closed_at > $1::timestamptz - interval '7 days'
		ORDER BY created_at LIMIT 500`, now)
	if err != nil {
		return 0, err
	}
	var list []Dispute
	for rows.Next() {
		d, err := scanDispute(rows)
		if err != nil {
			rows.Close()
			return 0, err
		}
		list = append(list, d)
	}
	rows.Close()
	for _, d := range list {
		for _, key := range noticesDue(d, now) {
			claimed, err := s.claimNotice(ctx, d.ID, key)
			if err != nil {
				return told, err
			}
			if !claimed {
				continue
			}
			told++
			s.deliverNotice(ctx, n, d, key, now)
		}
	}
	return told, nil
}

// noticesDue lists the notices a dispute should have by now.
func noticesDue(d Dispute, now time.Time) []string {
	var keys []string
	keys = append(keys, "opened")
	if d.Open() && d.DueBy != nil {
		left := d.DueBy.Sub(now)
		for _, days := range []int{7, 3, 1} {
			if left <= time.Duration(days)*24*time.Hour {
				keys = append(keys, fmt.Sprintf("due_%d", days))
			}
		}
	}
	switch d.State {
	case StateExpired:
		keys = append(keys, "expired")
	case StateClosed:
		keys = append(keys, "closed")
	}
	return keys
}

func (s *Service) claimNotice(ctx context.Context, id uuid.UUID, key string) (bool, error) {
	tag, err := s.db.Exec(ctx, `INSERT INTO payment_dispute_notices (dispute_id, notice_key) VALUES ($1, $2)
		ON CONFLICT DO NOTHING`, id, key)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// deliverNotice sends one notice. A reminder whose deadline was already
// that close at the opening is not sent separately (the opening says it).
func (s *Service) deliverNotice(ctx context.Context, n Notifier, d Dispute, key string, now time.Time) {
	info, err := s.noticeInfo(ctx, d)
	if err != nil {
		s.logger.Warn("dispute watch: notice facts", slog.String("dispute_id", d.ID.String()), slog.String("error", err.Error()))
		return
	}
	if strings.HasPrefix(key, "due_") {
		var opened time.Time
		_ = s.db.QueryRow(ctx, `SELECT sent_at FROM payment_dispute_notices WHERE dispute_id = $1 AND notice_key = 'opened'`, d.ID).Scan(&opened)
		var days int
		_, _ = fmt.Sscanf(key, "due_%d", &days)
		if !opened.IsZero() && d.DueBy != nil && d.DueBy.Sub(opened) <= time.Duration(days)*24*time.Hour && now.Sub(opened) < time.Hour {
			return
		}
	}
	text := noticeText(key, d, info, n.EventCenterURL)
	if n.Announcer != nil {
		if _, _, err := n.Announcer.AnnounceToOrg(ctx, d.OrgID.String(), text); err != nil {
			s.logger.Warn("dispute watch: chat notice failed", slog.String("dispute_id", d.ID.String()), slog.String("error", err.Error()))
		}
	}
	if n.Ops != nil && (key == "opened" || key == "expired") {
		_ = n.Ops.Send(ctx, opsText(key, d, info))
	}
	if n.Sender != nil && (key == "opened" || key == "due_3" || key == "due_1" || key == "expired" || key == "closed") {
		to, err := OwnerEmails(ctx, s.db, d.OrgID, key == "due_1")
		if err != nil {
			return
		}
		subject, body := noticeEmail(key, d, info, n.EventCenterURL)
		for _, addr := range to {
			if err := n.Sender.Send(ctx, email.Message{To: addr, Subject: subject, TextBody: body, HTMLBody: plainToHTML(body)}); err != nil {
				s.logger.Warn("dispute watch: e-mail notice failed", slog.String("dispute_id", d.ID.String()), slog.String("error", err.Error()))
			}
		}
	}
}

type noticeFacts struct {
	OrgName, Locale, EventName, Timezone string
	OrderNumber                          int64
	HasPackage                           bool
	Threshold                            int64
}

func (s *Service) noticeInfo(ctx context.Context, d Dispute) (noticeFacts, error) {
	var nf noticeFacts
	err := s.db.QueryRow(ctx, `SELECT o.name, COALESCE(o.default_locale,'en'),
			COALESCE(e.name,''), COALESCE(v.timezone,''), COALESCE(ord.system_id, 0),
			EXISTS (SELECT 1 FROM payment_dispute_packages p WHERE p.dispute_id = $1)
		FROM payment_disputes d
		JOIN organizations o ON o.id = d.org_id
		LEFT JOIN orders ord ON ord.id = d.order_id
		LEFT JOIN sessions s ON s.id = ord.session_id
		LEFT JOIN events e ON e.id = s.event_id
		LEFT JOIN venues v ON v.id = s.venue_id
		WHERE d.id = $1`, d.ID).Scan(&nf.OrgName, &nf.Locale, &nf.EventName, &nf.Timezone, &nf.OrderNumber, &nf.HasPackage)
	if err != nil {
		return nf, err
	}
	if st, err := settingsFor(ctx, s.db, d.OrgID); err == nil {
		nf.Threshold = st.Threshold(d.Currency)
	}
	return nf, nil
}

func cardLink(base string, d Dispute) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return ""
	}
	return fmt.Sprintf("%s/o/%s/disputes/%s", base, d.OrgID, d.ID)
}
