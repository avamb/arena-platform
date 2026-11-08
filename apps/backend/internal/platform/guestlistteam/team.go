// Package guestlistteam sends the CURRENT guest list of a session to the
// organization's whole team before the session (owner request 2026-11-08,
// migration 0211): 24 hours and 3 hours before start_at, by e-mail to every
// active owner (membership role org_admin) and manager (organizer), and as a
// document to the organization's own sales Telegram groups.
//
// It is separate from package guestlist's per-person subscription
// (guest_list.schedule, migration 0187), which a member turns on for
// themselves: a member who has that subscription is left out of the e-mail
// here, so nobody gets the same list twice. The file is built by
// guestlist.Build at send time, so it holds every ticket with its status as of
// that moment.
//
// Rules:
//
//   - each (session, kind) is sent at most once: the guest_list_org_sends row
//     is inserted BEFORE anything is built or sent (ON CONFLICT DO NOTHING),
//     so a crash or a failed send never repeats a list;
//   - the 24h list goes only while now is before start-3h, the 3h list only
//     before start (see Due);
//   - a cancelled or deleted session, a session without tickets, an archived
//     organization and an organization that turned the switch off
//     (organizations.guest_list_auto_send) get nothing;
//   - Telegram: only sales_notification_subscriptions rows of THIS
//     organization that are allowed; the operator rows (org_id NULL) never
//     receive a buyer file.
package guestlistteam

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/email"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/guestlist"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/salesnotify"
)

// JobType is the self-scheduling worker job.
const JobType = "guest_list.team_send"

// DefaultInterval is how often the job looks at the clock.
const DefaultInterval = 5 * time.Minute

// The two kinds, as stored in guest_list_org_sends.kind.
const (
	Kind24h = "24h"
	Kind3h  = "3h"
)

// batch bounds the sessions one pass looks at.
const batch = 200

// Due says which kinds are due for a session starting at start. The 24h list
// is due from start-24h and only until start-3h (after that the 3h list
// replaces it); the 3h list from start-3h until start.
func Due(now, start time.Time) []string {
	var out []string
	if !now.Before(start.Add(-24*time.Hour)) && now.Before(start.Add(-3*time.Hour)) {
		out = append(out, Kind24h)
	}
	if !now.Before(start.Add(-3*time.Hour)) && now.Before(start) {
		out = append(out, Kind3h)
	}
	return out
}

// DocumentSender posts a file to a Telegram chat (salesnotify.TelegramSender).
type DocumentSender interface {
	SendDocument(ctx context.Context, chatID, filename, contentType string, data []byte, caption string) error
}

// ChatStore records what happened to a group (salesnotify.PGStore).
type ChatStore interface {
	UpdateChatID(ctx context.Context, subscriptionID, chatID string) error
	RecordDelivery(ctx context.Context, subscriptionID, lastError string) error
}

// Options configures the job.
type Options struct {
	Pool     *pgxpool.Pool
	Sender   email.Sender
	Docs     DocumentSender // nil: no Telegram
	Chats    ChatStore
	Logger   *slog.Logger
	Interval time.Duration
}

// Job is the worker job.
type Job struct {
	pool     *pgxpool.Pool
	sender   email.Sender
	docs     DocumentSender
	chats    ChatStore
	logger   *slog.Logger
	interval time.Duration
}

// New builds the job; a zero interval takes the default.
func New(o Options) *Job {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Interval <= 0 {
		o.Interval = DefaultInterval
	}
	return &Job{pool: o.Pool, sender: o.Sender, docs: o.Docs, chats: o.Chats, logger: o.Logger, interval: o.Interval}
}

// ScheduleInitialJob seeds the queue once at worker start; a pending or
// claimed run means the chain is alive.
func ScheduleInitialJob(ctx context.Context, pool *pgxpool.Pool) error {
	var cnt int64
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM worker_jobs
		 WHERE job_type = $1 AND status IN ('pending', 'claimed')`, JobType).Scan(&cnt); err != nil {
		return fmt.Errorf("guestlistteam: check initial job: %w", err)
	}
	if cnt > 0 {
		return nil
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
		VALUES ($1, '{}', 3, 'pending', now())`, JobType); err != nil {
		return fmt.Errorf("guestlistteam: enqueue initial job: %w", err)
	}
	return nil
}

// Handler is the worker handler. It queues the next run BEFORE the work, so a
// failing pass never ends the chain; a failure is logged and the job
// succeeds.
func (j *Job) Handler() func(ctx context.Context, payload []byte) error {
	return func(ctx context.Context, _ []byte) error {
		if err := j.scheduleNext(ctx, time.Now().Add(j.interval)); err != nil {
			return err
		}
		n, err := j.RunOnce(ctx, time.Now().UTC())
		if err != nil {
			j.logger.Warn("guest list team send failed", slog.String("error", err.Error()))
		}
		if n > 0 {
			j.logger.Info("guest list team send", slog.Int("lists", n))
		}
		return nil
	}
}

func (j *Job) scheduleNext(ctx context.Context, at time.Time) error {
	tx, err := j.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("guestlistteam: schedule next run: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, JobType); err != nil {
		return fmt.Errorf("guestlistteam: schedule next run: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
		SELECT $1, '{}', 3, 'pending', $2
		WHERE NOT EXISTS (SELECT 1 FROM worker_jobs WHERE job_type = $1 AND status = 'pending')`,
		JobType, at); err != nil {
		return fmt.Errorf("guestlistteam: schedule next run: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("guestlistteam: schedule next run: %w", err)
	}
	return nil
}

type candidate struct {
	SessionID  uuid.UUID
	OrgID      uuid.UUID
	EventName  string
	EventSlug  *string
	StartAt    time.Time
	Timezone   *string
	Locale     string
	OrgName    string
	SenderMail string
	SenderStat string
}

func (j *Job) candidates(ctx context.Context, now time.Time) ([]candidate, error) {
	rows, err := j.pool.Query(ctx, `
		SELECT s.id, e.org_id, e.name, e.slug, s.start_at, v.timezone,
		       COALESCE(o.default_locale, 'en'), o.name,
		       COALESCE(o.sender_email, ''), COALESCE(o.sender_verification_status, '')
		FROM      sessions s
		JOIN      events e ON e.id = s.event_id
		JOIN      organizations o ON o.id = e.org_id
		LEFT JOIN venues v ON v.id = s.venue_id
		WHERE s.deleted_at IS NULL AND s.status <> 'cancelled'
		  AND e.deleted_at IS NULL AND o.deleted_at IS NULL
		  AND o.guest_list_auto_send
		  AND s.start_at > $1 AND s.start_at <= $1 + interval '24 hours'
		  AND (SELECT count(*) FROM guest_list_org_sends x WHERE x.session_id = s.id) < 2
		  AND EXISTS (SELECT 1 FROM tickets t WHERE t.session_id = s.id)
		ORDER BY s.start_at
		LIMIT $2`, now, batch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.SessionID, &c.OrgID, &c.EventName, &c.EventSlug, &c.StartAt, &c.Timezone,
			&c.Locale, &c.OrgName, &c.SenderMail, &c.SenderStat); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// RunOnce is one pass at the given moment; it answers how many lists went
// out (a list counts once however many recipients it reached).
func (j *Job) RunOnce(ctx context.Context, now time.Time) (int, error) {
	cs, err := j.candidates(ctx, now)
	if err != nil {
		return 0, err
	}
	sent := 0
	var firstErr error
	for _, c := range cs {
		for _, kind := range Due(now, c.StartAt) {
			ok, err := j.sendOne(ctx, c, kind)
			if err != nil && firstErr == nil {
				firstErr = err
			}
			if ok {
				sent++
			}
		}
	}
	return sent, firstErr
}

type recipient struct{ Email string }

func (j *Job) recipients(ctx context.Context, orgID uuid.UUID) ([]recipient, error) {
	rows, err := j.pool.Query(ctx, `
		SELECT DISTINCT lower(btrim(u.email))
		FROM   memberships m
		JOIN   users u ON u.id = m.user_id
		WHERE  m.org_id = $1 AND m.status = 'active' AND m.role IN ('org_admin', 'organizer')
		  AND  u.deactivated_at IS NULL AND u.anonymized_at IS NULL
		  AND  btrim(u.email) <> ''
		  AND  NOT EXISTS (SELECT 1 FROM guest_list_subscriptions gs
		                    WHERE gs.org_id = $1 AND gs.user_id = u.id)
		ORDER  BY 1`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []recipient
	for rows.Next() {
		var r recipient
		if err := rows.Scan(&r.Email); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

type chat struct{ ID, ChatID string }

// chats are the allowed Telegram groups of THIS organization. The query
// names org_id = $1 and never reads the operator rows (org_id NULL).
func (j *Job) groups(ctx context.Context, orgID uuid.UUID) ([]chat, error) {
	rows, err := j.pool.Query(ctx, `
		SELECT id::text, chat_id
		FROM   sales_notification_subscriptions
		WHERE  org_id = $1 AND allowed
		ORDER  BY created_at, id`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []chat
	for rows.Next() {
		var c chat
		if err := rows.Scan(&c.ID, &c.ChatID); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// sendOne claims (session, kind) and sends. It answers whether a list was
// built and offered to at least one channel.
func (j *Job) sendOne(ctx context.Context, c candidate, kind string) (bool, error) {
	emailOK := j.sender != nil && !email.IsDevOnly(j.sender)
	docsOK := j.docs != nil
	if !emailOK && !docsOK {
		return false, nil // nothing could carry it; leave the slot open
	}
	tag, err := j.pool.Exec(ctx, `
		INSERT INTO guest_list_org_sends (session_id, kind, org_id) VALUES ($1, $2, $3)
		ON CONFLICT (session_id, kind) DO NOTHING`, c.SessionID, kind, c.OrgID)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() != 1 {
		return false, nil // already claimed by an earlier pass or another worker
	}
	finish := func(outcome string, tickets, es, ef, cs, cf int) {
		_, ferr := j.pool.Exec(ctx, `
			UPDATE guest_list_org_sends
			   SET finished_at = now(), outcome = $3, tickets = $4,
			       emails_sent = $5, emails_failed = $6, chats_sent = $7, chats_failed = $8
			 WHERE session_id = $1 AND kind = $2`, c.SessionID, kind, outcome, tickets, es, ef, cs, cf)
		if ferr != nil {
			j.logger.Warn("guest list team send: could not record outcome", slog.String("session_id", c.SessionID.String()))
		}
	}

	loc := guestlist.Location(deref(c.Timezone))
	locale := guestlist.NormalizeLocale(c.Locale)
	q := gen.New(j.pool)
	file, err := guestlist.Build(ctx, q, c.OrgID, c.SessionID, locale, loc, guestlist.MaxRows, guestlist.XLSX)
	switch {
	case errors.Is(err, guestlist.ErrTooManyRows):
		finish("too_many_rows", 0, 0, 0, 0, 0)
		return false, nil
	case err != nil:
		finish("build_failed", 0, 0, 0, 0, 0)
		return false, err
	case file.Rows == 0:
		finish("no_tickets", 0, 0, 0, 0, 0)
		return false, nil
	}
	slug := ""
	if c.EventSlug != nil {
		slug = *c.EventSlug
	}
	name := guestlist.FileName(c.EventName, slug, c.StartAt, loc, guestlist.XLSX)
	letter := Render(locale, kind, c.EventName, c.StartAt, loc, file.Counts)

	var es, ef, cs, cf int
	if emailOK {
		rs, rerr := j.recipients(ctx, c.OrgID)
		if rerr != nil {
			j.logger.Warn("guest list team send: recipients", slog.String("error", rerr.Error()))
		}
		for _, r := range rs {
			msg := email.Message{
				To: r.Email, FromName: strings.TrimSpace(c.OrgName),
				Subject: letter.Subject, HTMLBody: letter.HTML, TextBody: letter.Text,
				Attachments: []email.Attachment{{Filename: name, ContentType: file.ContentType, Data: file.Body}},
			}
			if c.SenderStat == "verified" && strings.TrimSpace(c.SenderMail) != "" {
				msg.From = strings.TrimSpace(c.SenderMail)
			}
			if serr := j.sender.Send(ctx, msg); serr != nil {
				ef++
				j.logger.Warn("guest list team e-mail not sent", slog.String("session_id", c.SessionID.String()))
				continue
			}
			es++
		}
	}
	if docsOK {
		gs, gerr := j.groups(ctx, c.OrgID)
		if gerr != nil {
			j.logger.Warn("guest list team send: groups", slog.String("error", gerr.Error()))
		}
		for _, g := range gs {
			if j.sendDocument(ctx, g, name, file.ContentType, file.Body, letter.Caption) {
				cs++
			} else {
				cf++
			}
		}
	}
	finish("sent", file.Rows, es, ef, cs, cf)
	return es+cs > 0, nil
}

// sendDocument posts the file to one group, following a supergroup upgrade
// once, and records the group's state like the sales notifications do.
func (j *Job) sendDocument(ctx context.Context, g chat, name, ctype string, body []byte, caption string) bool {
	chatID := g.ChatID
	err := j.docs.SendDocument(ctx, chatID, name, ctype, body, caption)
	var mig *salesnotify.MigratedError
	if errors.As(err, &mig) {
		chatID = mig.NewChatID
		if j.chats != nil {
			_ = j.chats.UpdateChatID(ctx, g.ID, chatID)
		}
		err = j.docs.SendDocument(ctx, chatID, name, ctype, body, caption)
	}
	if j.chats != nil {
		reason := ""
		if err != nil {
			reason = err.Error()
			if len(reason) > 200 {
				reason = reason[:200]
			}
		}
		_ = j.chats.RecordDelivery(ctx, g.ID, reason)
	}
	return err == nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// Letter is a rendered team guest list message.
type Letter struct {
	Subject string
	HTML    string
	Text    string
	Caption string // the Telegram caption: plain text, under 1024 characters
}

type texts struct {
	subject, greeting, intro, when24, when3, counts, valid, in, out, gone, total, note string
}

var words = map[string]texts{
	"en": {
		subject:  "Guest list: %s, %s",
		greeting: "Hello,",
		intro:    "The current guest list for %s, %s is attached (%s).",
		when24:   "24 hours before the start", when3: "3 hours before the start",
		counts: "Tickets: %d.", valid: "Valid", in: "Checked in", out: "Checked out", gone: "Refunded / cancelled", total: "Total",
		note: "The list is built at the moment of sending: later sales, refunds and entries are not in it. The owner can switch these letters off in the event center, under the organization settings.",
	},
	"ru": {
		subject:  "Список гостей: %s, %s",
		greeting: "Здравствуйте!",
		intro:    "Во вложении актуальный список гостей: %s, %s (%s).",
		when24:   "за 24 часа до начала", when3: "за 3 часа до начала",
		counts: "Билетов: %d.", valid: "Действителен", in: "Вошёл", out: "Вышел", gone: "Возврат / отмена", total: "Всего",
		note: "Список собран в момент отправки: более поздние продажи, возвраты и входы в него не попали. Владелец может отключить эти письма в ивент-центре, в настройках организации.",
	},
	"es": {
		subject:  "Lista de invitados: %s, %s",
		greeting: "Hola:",
		intro:    "Adjuntamos la lista de invitados actual de %s, %s (%s).",
		when24:   "24 horas antes del inicio", when3: "3 horas antes del inicio",
		counts: "Entradas: %d.", valid: "Válida", in: "Ha entrado", out: "Ha salido", gone: "Devuelta / cancelada", total: "Total",
		note: "La lista se genera en el momento del envío: las ventas, devoluciones y entradas posteriores no aparecen. El propietario puede desactivar estos correos en el centro de eventos, en los ajustes de la organización.",
	},
}

// Render builds the letter: event, session date and time on the venue's
// clock, the counts per status. counts index is the MACS status 0..3.
func Render(locale, kind, eventName string, start time.Time, loc *time.Location, counts [4]int) Letter {
	t, ok := words[locale]
	if !ok {
		t = words["en"]
	}
	event := strings.Join(strings.Fields(eventName), " ")
	when := guestlist.When(start, loc)
	slot := t.when24
	if kind == Kind3h {
		slot = t.when3
	}
	total := counts[0] + counts[1] + counts[2] + counts[3]
	breakdown := fmt.Sprintf("%s: %d. %s: %d. %s: %d. %s: %d.",
		t.valid, counts[0], t.in, counts[1], t.out, counts[2], t.gone, counts[3])
	lines := []string{
		t.greeting,
		fmt.Sprintf(t.intro, event, when, slot),
		fmt.Sprintf(t.counts, total) + " " + breakdown,
		t.note,
	}
	var hb strings.Builder
	hb.WriteString(`<!doctype html><html><body style="font-family:Arial,sans-serif;font-size:15px;line-height:1.5;color:#111">`)
	for _, l := range lines {
		hb.WriteString("<p>" + html.EscapeString(l) + "</p>")
	}
	hb.WriteString("</body></html>")
	caption := fmt.Sprintf("%s, %s\n%s", event, when, fmt.Sprintf(t.counts, total)+" "+breakdown)
	if r := []rune(caption); len(r) > 1000 {
		caption = string(r[:1000])
	}
	return Letter{
		Subject: fmt.Sprintf(t.subject, event, when),
		HTML:    hb.String(),
		Text:    strings.Join(lines, "\n\n"),
		Caption: caption,
	}
}
