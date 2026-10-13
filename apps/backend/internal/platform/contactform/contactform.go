// Package contactform delivers the messages buyers send through the "Write
// to the organizer" form of the hosted sales page (HP contact form). The
// public route (httpserver/hcontact) stores each message in
// organizer_contact_messages (migration 0148) and queues one worker job; this
// package is that job.
//
// One job, two legs, each sent at most once (email_sent_at /
// telegram_sent_at):
//
//   - e-mail to the channel's public contact e-mail
//     (settings.hosted_page.public_contact.email), else to the organization's
//     own contact_email, else to its owner's account e-mail
//     (gen.OwnerRecipientSQL); the page shows neither. Read when the job runs so
//     an address the owner changed in the meantime wins, and checked again
//     with sellerinfo.NormalizeEmail (a value written around the API is never
//     mailed). Reply-To is the buyer, so the organizer answers with one click
//     and the buyer never learns the organizer's address from arena. A
//     failed send returns the error and the worker retries. email_mailed
//     records whether a letter really went out.
//   - Telegram: when the organization keeps contact_form_to_telegram on, the
//     message is posted to the organization's OWN sales-notification chats
//     (never the operator's). The post says whether the e-mail went out.
//     telegram_sent_at is set only when at least one chat took the post or
//     the organization has no chat; otherwise the job returns an error and
//     the worker retries the Telegram leg alone.
//
// Every buyer-typed value is escaped for the HTML of the e-mail and of the
// Telegram message. The words of the organizer's letter are contact.email.*
// keys of the shared i18n catalog. Messages older than RetentionDays are
// deleted by the self-scheduling job PurgeJobType (NewPurgeHandler).
package contactform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	goi18n "github.com/nicksnyder/go-i18n/v2/i18n"

	emailadapter "github.com/abhteam/arena_new/apps/backend/internal/adapters/email"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/i18n"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/opsalert"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/sellerinfo"
)

// JobType is the worker job that delivers one message.
const JobType = "contact.organizer_message"

// RetentionDays is how long a message is kept.
const RetentionDays = 90

// Payload is the job's JSON.
type Payload struct {
	MessageID string `json:"message_id"`
}

// Announcer posts a text to an organization's own sales chats
// (salesnotify.Dispatcher.AnnounceToOrg). chats is how many chats the
// organization has, delivered how many took the text; err means the chats
// could not even be listed.
type Announcer interface {
	AnnounceToOrg(ctx context.Context, orgID, text string) (chats, delivered int, err error)
}

// Options configures the handler.
type Options struct {
	Pool      *pgxpool.Pool
	Sender    emailadapter.Sender
	Announcer Announcer // nil: no Telegram leg
	Logger    *slog.Logger
}

// Handler is the job handler. Construct with New.
type Handler struct {
	pool      *pgxpool.Pool
	sender    emailadapter.Sender
	announcer Announcer
	logger    *slog.Logger
}

// New builds the handler.
func New(o Options) *Handler {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	return &Handler{pool: o.Pool, sender: o.Sender, announcer: o.Announcer, logger: o.Logger}
}

// message is one stored row plus what the job needs about its organization.
type message struct {
	ID             uuid.UUID
	OrgID          uuid.UUID
	PageSlug       string
	SenderName     string
	SenderEmail    string
	Body           string
	EmailSent      bool
	EmailMailed    bool
	TelegramSent   bool
	OrgName        string
	OrgLocale      string
	ToTelegram     bool
	PublicContact  []byte
	OrgEmail       string
	OwnerEmail     string
	ChannelDeleted bool
}

const loadMessageSQL = `
SELECT m.id, m.org_id, m.page_slug, m.sender_name, m.sender_email, m.message,
       m.email_sent_at IS NOT NULL, m.email_mailed, m.telegram_sent_at IS NOT NULL,
       o.name, o.default_locale, o.contact_form_to_telegram,
       sc.settings #> '{hosted_page,public_contact}', COALESCE(o.contact_email, ''),
       COALESCE(` + gen.OwnerRecipientSQL + `, ''), sc.deleted_at IS NOT NULL
FROM organizer_contact_messages m
JOIN organizations o ON o.id = m.org_id
JOIN sales_channels sc ON sc.id = m.channel_id
WHERE m.id = $1`

// Handle implements the worker handler signature.
func (h *Handler) Handle(ctx context.Context, payload []byte) error {
	var p Payload
	if err := json.Unmarshal(payload, &p); err != nil {
		return fmt.Errorf("contactform: decode payload: %w", err)
	}
	id, err := uuid.Parse(p.MessageID)
	if err != nil {
		return fmt.Errorf("contactform: bad message id: %w", err)
	}

	var m message
	err = h.pool.QueryRow(ctx, loadMessageSQL, id).Scan(
		&m.ID, &m.OrgID, &m.PageSlug, &m.SenderName, &m.SenderEmail, &m.Body,
		&m.EmailSent, &m.EmailMailed, &m.TelegramSent, &m.OrgName, &m.OrgLocale, &m.ToTelegram,
		&m.PublicContact, &m.OrgEmail, &m.OwnerEmail, &m.ChannelDeleted,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		// Purged or never committed: nothing to deliver.
		h.logger.Info("contactform: message is gone", slog.String("message_id", id.String()))
		return nil
	}
	if err != nil {
		return fmt.Errorf("contactform: load message: %w", err)
	}

	if !m.EmailSent {
		to := ""
		if !m.ChannelDeleted {
			to = recipient(m.PublicContact, m.OrgEmail, m.OwnerEmail)
		}
		mailed := false
		if to == "" {
			// The owner removed the address after the buyer wrote, or the
			// stored value is not an address: nothing to mail. The Telegram
			// leg still runs and says so.
			h.logger.Warn("contactform: channel has no usable public e-mail; message not mailed", slog.String("message_id", id.String()))
		} else {
			subject, htmlBody, textBody := RenderEmail(m.OrgLocale, m.OrgName, m.PageSlug, m.SenderName, m.SenderEmail, m.Body)
			if err := h.sender.Send(ctx, emailadapter.Message{
				To: to, ReplyTo: m.SenderEmail, Subject: subject, HTMLBody: htmlBody, TextBody: textBody,
			}); err != nil {
				return fmt.Errorf("contactform: send message %s: %w", id, err)
			}
			mailed = true
		}
		if _, err := h.pool.Exec(ctx, `UPDATE organizer_contact_messages SET email_sent_at = now(), email_mailed = $2 WHERE id = $1`, id, mailed); err != nil {
			return fmt.Errorf("contactform: mark e-mail sent: %w", err)
		}
		m.EmailMailed = mailed
	}

	if !m.TelegramSent {
		if m.ToTelegram && h.announcer != nil {
			text := FormatTelegram(m.OrgName, m.PageSlug, m.SenderName, m.SenderEmail, m.Body, m.EmailMailed)
			chats, delivered, err := h.announcer.AnnounceToOrg(ctx, m.OrgID.String(), text)
			if err != nil {
				return fmt.Errorf("contactform: post message %s to the sales chats: %w", id, err)
			}
			if chats > 0 && delivered == 0 {
				return fmt.Errorf("contactform: no sales chat took message %s (%d chats)", id, chats)
			}
			h.logger.Info("contactform: message posted to the organization's chats", slog.String("message_id", id.String()),
				slog.Int("chats", chats), slog.Int("delivered", delivered))
		}
		if _, err := h.pool.Exec(ctx, `UPDATE organizer_contact_messages SET telegram_sent_at = now() WHERE id = $1`, id); err != nil {
			h.logger.Warn("contactform: mark telegram sent failed", slog.String("message_id", id.String()), slog.String("error", err.Error()))
		}
	}
	return nil
}

// PurgeJobType is the self-scheduling job that deletes old messages.
const PurgeJobType = "contact.purge"

// PurgeInterval is how often the purge runs.
const PurgeInterval = time.Hour

// Purge deletes messages older than RetentionDays and returns how many.
func Purge(ctx context.Context, pool *pgxpool.Pool) (int64, error) {
	tag, err := pool.Exec(ctx, `DELETE FROM organizer_contact_messages WHERE created_at < now() - make_interval(days => $1)`, RetentionDays)
	if err != nil {
		return 0, fmt.Errorf("contactform: purge: %w", err)
	}
	return tag.RowsAffected(), nil
}

// NewPurgeHandler returns the worker handler for PurgeJobType. It schedules
// the next run even when this one fails, so an error cannot stop the cadence.
func NewPurgeHandler(pool *pgxpool.Pool, logger *slog.Logger) func(ctx context.Context, payload []byte) error {
	if logger == nil {
		logger = slog.Default()
	}
	return func(ctx context.Context, _ []byte) error {
		n, runErr := Purge(ctx, pool)
		if _, err := pool.Exec(ctx, `INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
VALUES ($1, '{}', 3, 'pending', $2)`, PurgeJobType, time.Now().Add(PurgeInterval)); err != nil {
			return fmt.Errorf("contactform: schedule next purge: %w", err)
		}
		if runErr != nil {
			return runErr
		}
		if n > 0 {
			logger.Info("contactform: old messages deleted", slog.Int64("deleted", n))
		}
		return nil
	}
}

// ScheduleInitialPurge enqueues the first purge when none is pending.
func ScheduleInitialPurge(ctx context.Context, pool *pgxpool.Pool) error {
	var cnt int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM worker_jobs WHERE job_type = $1 AND status IN ('pending', 'claimed')`,
		PurgeJobType).Scan(&cnt); err != nil {
		return fmt.Errorf("contactform: check initial purge job: %w", err)
	}
	if cnt > 0 {
		return nil
	}
	if _, err := pool.Exec(ctx, `INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
VALUES ($1, '{}', 3, 'pending', now())`, PurgeJobType); err != nil {
		return fmt.Errorf("contactform: enqueue initial purge job: %w", err)
	}
	return nil
}

// recipient is the channel's public e-mail when it is a valid address, else
// the first valid private address in order - the organization's own
// contact_email, then its owner's account e-mail - else "". A private address
// is never shown to the buyer unless the organizer answers from it.
func recipient(raw []byte, private ...string) string {
	if to, ok := sellerinfo.NormalizeEmail(contactEmail(raw)); ok {
		return to
	}
	for _, p := range private {
		if to, ok := sellerinfo.NormalizeEmail(p); ok {
			return to
		}
	}
	return ""
}

// contactEmail reads public_contact.email from the channel settings.
func contactEmail(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	var c struct {
		Email any `json:"email"`
	}
	if json.Unmarshal(raw, &c) != nil {
		return ""
	}
	s, _ := c.Email.(string)
	return strings.TrimSpace(s)
}

var (
	bundleOnce sync.Once
	bundle     *i18n.Bundle
)

// localizer returns the catalog localizer for locale, nil when the catalog
// could not be loaded (the embedded files are checked by the i18n tests).
func localizer(locale string) *goi18n.Localizer {
	bundleOnce.Do(func() {
		b, err := i18n.NewBundle()
		if err == nil {
			bundle = b
		}
	})
	if bundle == nil {
		return nil
	}
	return bundle.LocalizerFor(locale)
}

// say is one contact.email.* text; English when the language lacks it.
func say(loc *goi18n.Localizer, id string, data map[string]string) string {
	if loc == nil {
		return id
	}
	out, err := loc.Localize(&goi18n.LocalizeConfig{MessageID: id, TemplateData: data})
	if err != nil {
		return id
	}
	return out
}

// baseLang is the language subtag of locale, lower case.
func baseLang(locale string) string {
	l := strings.ToLower(strings.TrimSpace(locale))
	if i := strings.IndexAny(l, "-_"); i > 0 {
		l = l[:i]
	}
	return l
}

// RenderEmail builds the organizer's letter in the organization's language.
// Every buyer-typed value is HTML-escaped in the HTML body.
func RenderEmail(locale, orgName, pageSlug, senderName, senderEmail, body string) (subject, htmlBody, textBody string) {
	l := baseLang(locale)
	loc := localizer(l)
	name := strings.Join(strings.Fields(senderName), " ")
	subject = say(loc, "contact.email.subject", map[string]string{"Name": name})
	intro, from, page := say(loc, "contact.email.intro", nil), say(loc, "contact.email.from", nil), say(loc, "contact.email.page", nil)
	reply, footer := say(loc, "contact.email.reply", nil), say(loc, "contact.email.footer", nil)
	dir := "ltr"
	if l == "he" {
		dir = "rtl"
	}
	esc := html.EscapeString
	htmlBody = `<!doctype html><html dir="` + dir + `"><body style="font-family:Arial,sans-serif;color:#222;line-height:1.5">` +
		`<p>` + esc(intro) + `</p>` +
		`<p><b>` + esc(from) + `:</b> ` + esc(name) + ` &lt;<span dir="ltr">` + esc(senderEmail) + `</span>&gt;<br>` +
		`<b>` + esc(page) + `:</b> ` + esc(orgName) + ` (/` + esc(pageSlug) + `)</p>` +
		`<div style="white-space:pre-wrap;border-left:3px solid #4f46e5;padding:8px 12px;background:#f6f6fb">` + esc(body) + `</div>` +
		`<p>` + esc(reply) + `</p>` +
		`<p style="font-size:12px;color:#888">` + esc(footer) + `</p></body></html>`
	textBody = strings.Join([]string{
		intro, "",
		from + ": " + name + " <" + senderEmail + ">",
		page + ": " + orgName + " (/" + pageSlug + ")", "",
		body, "",
		reply, "",
		footer,
	}, "\n")
	return subject, htmlBody, textBody
}

// FormatTelegram is the sales-chat message (English, like every message of
// the sales bot). Every value is escaped for Telegram's HTML parse mode, and
// the whole text stays within telegramMaxUTF16: the buyer's words are cut,
// never the lines around them. mailed says whether the e-mail leg really
// sent the letter.
func FormatTelegram(orgName, pageSlug, senderName, senderEmail, body string, mailed bool) string {
	esc := opsalert.EscapeHTML
	head := "✉️ <b>Message from a buyer</b>\n" +
		"Sales page: " + esc(orgName) + " (/" + esc(pageSlug) + ")\n" +
		"From: " + esc(strings.Join(strings.Fields(senderName), " ")) + " &lt;" + esc(senderEmail) + "&gt;\n\n"
	tail := "\n\nThe same message went to your e-mail. Reply to the buyer from there."
	if !mailed {
		tail = "\n\nThis message was not e-mailed: the sales page and the organization have no e-mail address. To answer the buyer, write to the address above."
	}
	budget := telegramMaxUTF16 - utf16Len(head) - utf16Len(tail)
	if budget < 0 {
		budget = 0
	}
	return head + clipEscaped(body, budget) + tail
}

// telegramMaxUTF16 is Telegram's limit on one message, counted in UTF-16
// code units. The escaped text is counted, which is never shorter than what
// Telegram counts after parsing the HTML, so the post always fits.
const telegramMaxUTF16 = 4096

// utf16Len is the length of s in UTF-16 code units.
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		n += utf16.RuneLen(r)
	}
	return n
}

// clipEscaped escapes s for Telegram HTML and stops before the escaped text
// grows past max UTF-16 code units, ending with "…" (one unit, counted in
// max) when it had to stop.
func clipEscaped(s string, max int) string {
	if all := opsalert.EscapeHTML(s); utf16Len(all) <= max {
		return all
	}
	var b strings.Builder
	n := 0
	for _, r := range s {
		e := opsalert.EscapeHTML(string(r))
		l := utf16Len(e)
		if n+l > max-1 {
			break
		}
		b.WriteString(e)
		n += l
	}
	if max > 0 {
		b.WriteString("…")
	}
	return b.String()
}
