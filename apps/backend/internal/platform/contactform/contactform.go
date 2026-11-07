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
//     (gen.OwnerRecipientSQL); the page shows neither. A message from a
//     promoter's page or about a promoter's event (Payload.ToOwner, decided
//     by the send route) goes to the owner's account e-mail only (owner
//     decision 2026-10-14), never to the public e-mail or the promoter's. Read when the job runs so
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
	"net/url"
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

// Topics of the site form (spec 62 section 10.2). The topic is also the
// Turnstile widget action of the form.
const (
	TopicContact      = "contact"
	TopicPrivateEvent = "private_event"
)

// ValidTopic reports whether t is one of the allow-listed topics.
func ValidTopic(t string) bool { return t == TopicContact || t == TopicPrivateEvent }

// MaxRecipientsPerTopic bounds how many addresses one topic of a channel's
// contact form is mailed to. Checked when the channel settings are written
// (ValidateRecipients) and again when they are read (RecipientsFor), so a
// public form can never fan one message out to a mailing list.
const MaxRecipientsPerTopic = 10

// ValidateRecipients checks a channel's settings.contact_form.recipients
// object as it is written: every topic lists at most MaxRecipientsPerTopic
// addresses. An absent object is valid. It returns a readable message, or ""
// when the object is acceptable.
func ValidateRecipients(raw []byte) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return "settings.contact_form.recipients must be an object mapping a topic to a list of addresses"
	}
	for topic, v := range m {
		if list, ok := v.([]any); ok && len(list) > MaxRecipientsPerTopic {
			return fmt.Sprintf("settings.contact_form.recipients.%s may list at most %d addresses", topic, MaxRecipientsPerTopic)
		}
	}
	return ""
}

// RecipientsFor reads the addresses a topic is mailed to from a channel's
// settings (sales_channels.settings.contact_form.recipients, a map topic ->
// list of addresses). raw is that settings.contact_form.recipients object.
// Only valid addresses survive, lower-cased and without duplicates; the order
// is kept, and at most MaxRecipientsPerTopic are returned. An unmapped topic
// gives nil, which means "the usual chain".
func RecipientsFor(raw []byte, topic string) []string {
	if len(raw) == 0 {
		return nil
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	var list []any
	switch v := m[topic].(type) {
	case []any:
		list = v
	case string:
		list = []any{v}
	}
	seen := map[string]bool{}
	var out []string
	for _, item := range list {
		s, _ := item.(string)
		to, ok := sellerinfo.NormalizeEmail(s)
		if !ok || seen[to] {
			continue
		}
		seen[to] = true
		out = append(out, to)
		if len(out) == MaxRecipientsPerTopic {
			break
		}
	}
	return out
}

// RetentionDays is how long a message is kept.
const RetentionDays = 90

// Payload is the job's JSON.
type Payload struct {
	MessageID string `json:"message_id"`
	// ToOwner: the message goes to the organization's owner only. A job
	// queued before the field existed reads false and keeps the old chain.
	ToOwner bool `json:"to_owner,omitempty"`
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
	// Ops hears about a message that reached nobody (no usable address and
	// no sales chat). Ids only, never the buyer's words or an address.
	Ops    opsalert.Notifier
	Logger *slog.Logger
}

// Handler is the job handler. Construct with New.
type Handler struct {
	pool      *pgxpool.Pool
	sender    emailadapter.Sender
	announcer Announcer
	ops       opsalert.Notifier
	logger    *slog.Logger
}

// New builds the handler.
func New(o Options) *Handler {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	return &Handler{pool: o.Pool, sender: o.Sender, announcer: o.Announcer, ops: o.Ops, logger: o.Logger}
}

// EmailLeg is what the e-mail leg did, as the Telegram copy tells it.
type EmailLeg int

const (
	// EmailMailed: the letter went out.
	EmailMailed EmailLeg = iota
	// EmailNoAddress: there is no usable address to mail.
	EmailNoAddress
	// EmailRetrying: sending failed and the worker will try again.
	EmailRetrying
)

// message is one stored row plus what the job needs about its organization.
type message struct {
	ID              uuid.UUID
	OrgID           uuid.UUID
	PageSlug        string
	SenderName      string
	SenderEmail     string
	Body            string
	EmailSent       bool
	EmailMailed     bool
	TelegramSent    bool
	OrgName         string
	OrgLocale       string
	ToTelegram      bool
	PublicContact   []byte
	OrgEmail        string
	OwnerEmail      string
	ChannelDeleted  bool
	Topic           string
	LastName        string
	Phone           string
	DoneTo          []string
	TopicRecipients []byte
	OrgSenderEmail  string
	OrgSenderStatus string
	SiteOrigins     []byte
}

const loadMessageSQL = `
SELECT m.id, m.org_id, m.page_slug, m.sender_name, m.sender_email, m.message,
       m.email_sent_at IS NOT NULL, m.email_mailed, m.telegram_sent_at IS NOT NULL,
       o.name, o.default_locale, o.contact_form_to_telegram,
       sc.settings #> '{hosted_page,public_contact}', COALESCE(o.contact_email, ''),
       COALESCE(` + gen.OwnerRecipientSQL + `, ''), sc.deleted_at IS NOT NULL,
       m.topic, m.last_name, m.phone, m.email_done_to, sc.settings #> '{contact_form,recipients}',
       COALESCE(o.sender_email, ''), COALESCE(o.sender_verification_status, ''), sc.settings #> '{site,origins}'
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
		&m.Topic, &m.LastName, &m.Phone, &m.DoneTo, &m.TopicRecipients,
		&m.OrgSenderEmail, &m.OrgSenderStatus, &m.SiteOrigins,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		// Purged or never committed: nothing to deliver.
		h.logger.Info("contactform: message is gone", slog.String("message_id", id.String()))
		return nil
	}
	if err != nil {
		return fmt.Errorf("contactform: load message: %w", err)
	}

	// The e-mail leg. A failed send does not stop the Telegram leg: the
	// organizer gets the copy in the chat now, and the worker retries the
	// e-mail alone (its error is returned at the end).
	leg := EmailNoAddress
	if m.EmailMailed {
		leg = EmailMailed
	}
	var sendErr error
	if !m.EmailSent {
		var targets []string
		switch {
		case p.ToOwner:
			// The owner is looked up through the organization, not the
			// channel, so a deleted channel does not lose the message.
			if to := recipient(nil, m.OwnerEmail); to != "" {
				targets = []string{to}
			}
		case !m.ChannelDeleted:
			// A topic the channel maps to addresses goes to exactly those
			// (spec 62 section 10.2); otherwise the usual chain decides.
			targets = RecipientsFor(m.TopicRecipients, m.Topic)
			if len(targets) == 0 {
				if to := recipient(m.PublicContact, m.OrgEmail, m.OwnerEmail); to != "" {
					targets = []string{to}
				}
			}
		}
		fullName := FullName(m.SenderName, m.LastName)
		mailed := false
		if len(targets) == 0 {
			// The owner removed the address after the buyer wrote, or the
			// stored value is not an address: nothing to mail. The Telegram
			// leg still runs and says so.
			h.logger.Warn("contactform: no usable address; message not mailed", slog.String("message_id", id.String()))
		} else {
			subject, htmlBody, textBody := RenderEmailTopic(m.OrgLocale, m.OrgName, pageRef(m.PageSlug, m.SiteOrigins), fullName, m.SenderEmail, m.Phone, m.Topic, m.Body)
			from, fromName := organizerFrom(m.OrgSenderEmail, m.OrgSenderStatus, m.OrgName)
			done := map[string]bool{}
			for _, d := range m.DoneTo {
				done[d] = true
			}
			var failed []string
			for _, to := range targets {
				if done[to] {
					continue
				}
				if err := h.sender.Send(ctx, emailadapter.Message{
					To: to, From: from, FromName: fromName, ReplyTo: m.SenderEmail, Subject: subject, HTMLBody: htmlBody, TextBody: textBody,
				}); err != nil {
					failed = append(failed, err.Error())
					continue
				}
				// Recorded at once: a retry after a later address failed
				// must not mail this one again.
				if _, err := h.pool.Exec(ctx, `UPDATE organizer_contact_messages SET email_done_to = array_append(email_done_to, $2) WHERE id = $1`, id, to); err != nil {
					return fmt.Errorf("contactform: record mailed address: %w", err)
				}
			}
			if len(failed) > 0 {
				sendErr = fmt.Errorf("contactform: send message %s: %d of %d addresses failed: %s", id, len(failed), len(targets), failed[0])
			} else {
				mailed = true
			}
		}
		if sendErr != nil {
			leg = EmailRetrying
			// Visible to the operator: the row keeps the last failure, the log
			// names the message (ids only), and a job that runs out of retries
			// reaches the ops watchdog as a dead letter.
			h.logger.Error("contactform: e-mail leg failed, the worker will retry",
				slog.String("message_id", id.String()), slog.String("org_id", m.OrgID.String()), slog.String("topic", m.Topic))
			if _, err := h.pool.Exec(ctx, `UPDATE organizer_contact_messages SET email_last_error = $2 WHERE id = $1`, id,
				clipError(opsalert.ScrubEmails(sendErr.Error()))); err != nil {
				h.logger.Warn("contactform: record e-mail failure failed", slog.String("message_id", id.String()))
			}
		} else {
			if _, err := h.pool.Exec(ctx, `UPDATE organizer_contact_messages SET email_sent_at = now(), email_mailed = $2, email_last_error = '' WHERE id = $1`, id, mailed); err != nil {
				return fmt.Errorf("contactform: mark e-mail sent: %w", err)
			}
			m.EmailMailed = mailed
			leg = EmailNoAddress
			if mailed {
				leg = EmailMailed
			}
		}
	}

	if !m.TelegramSent {
		posted := false
		if m.ToTelegram && h.announcer != nil {
			text := FormatTelegram(m.OrgName, pageRef(m.PageSlug, m.SiteOrigins), FullName(m.SenderName, m.LastName), m.SenderEmail, m.Body, leg)
			chats, delivered, err := h.announcer.AnnounceToOrg(ctx, m.OrgID.String(), text)
			if err != nil {
				return errors.Join(sendErr, fmt.Errorf("contactform: post message %s to the sales chats: %w", id, err))
			}
			if chats > 0 && delivered == 0 {
				return errors.Join(sendErr, fmt.Errorf("contactform: no sales chat took message %s (%d chats)", id, chats))
			}
			posted = delivered > 0
			h.logger.Info("contactform: message posted to the organization's chats", slog.String("message_id", id.String()),
				slog.Int("chats", chats), slog.Int("delivered", delivered))
		}
		if leg == EmailNoAddress && !posted {
			h.reachedNobody(ctx, m)
		}
		if _, err := h.pool.Exec(ctx, `UPDATE organizer_contact_messages SET telegram_sent_at = now() WHERE id = $1`, id); err != nil {
			h.logger.Warn("contactform: mark telegram sent failed", slog.String("message_id", id.String()), slog.String("error", err.Error()))
		}
	}
	return sendErr
}

// reachedNobody reports a message that went neither by e-mail nor to a chat:
// the form accepted it, but nobody will read it. An error log and an ops
// alert with ids only - never the buyer's words or an address.
func (h *Handler) reachedNobody(ctx context.Context, m message) {
	h.logger.Error("contactform: message reached nobody (no usable address, no sales chat)",
		slog.String("message_id", m.ID.String()), slog.String("org_id", m.OrgID.String()))
	if h.ops == nil {
		return
	}
	text := "Contact form: a buyer's message reached nobody.\nOrganization: " + opsalert.EscapeHTML(m.OrgName) +
		" (" + m.OrgID.String() + ")\nMessage: " + m.ID.String() +
		"\nThe organization has no usable e-mail address and no sales chat. Give it an address, then read the message in organizer_contact_messages."
	if err := h.ops.Send(ctx, text); err != nil {
		h.logger.Warn("contactform: ops alert failed", slog.String("message_id", m.ID.String()), slog.String("error", err.Error()))
	}
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

// FullName joins a first and a last name.
func FullName(first, last string) string {
	return strings.Join(strings.Fields(first+" "+last), " ")
}

// clipError keeps an error text within the column (300 characters).
func clipError(s string) string {
	if r := []rune(s); len(r) > 200 {
		return string(r[:200])
	}
	return s
}

// RenderEmail builds the organizer's letter in the organization's language.
// Every buyer-typed value is HTML-escaped in the HTML body.
func RenderEmail(locale, orgName, pageSlug, senderName, senderEmail, body string) (subject, htmlBody, textBody string) {
	return RenderEmailTopic(locale, orgName, pageSlug, senderName, senderEmail, "", "", body)
}

// RenderEmailTopic is RenderEmail for the site form: the sender's phone and
// the topic ride along in the sender line and the page line (symbols, not
// words, so the letter needs no new catalog keys). An empty message prints a
// dash.
func RenderEmailTopic(locale, orgName, pageSlug, senderName, senderEmail, phone, topic, body string) (subject, htmlBody, textBody string) {
	l := baseLang(locale)
	loc := localizer(l)
	name := strings.Join(strings.Fields(senderName), " ")
	subject = say(loc, "contact.email.subject", map[string]string{"Name": name})
	intro, from, page := say(loc, "contact.email.intro", nil), say(loc, "contact.email.from", nil), say(loc, "contact.email.page", nil)
	reply, footer := say(loc, "contact.email.reply", nil), say(loc, "contact.email.footer", nil)
	phone = strings.Join(strings.Fields(phone), " ")
	pageTag := ""
	if topic != "" {
		pageTag = " \u00b7 " + topic
	}
	if strings.TrimSpace(body) == "" {
		body = "\u2014"
	}
	dir := "ltr"
	if l == "he" {
		dir = "rtl"
	}
	esc := html.EscapeString
	htmlBody = `<!doctype html><html dir="` + dir + `"><body style="font-family:Arial,sans-serif;color:#222;line-height:1.5">` +
		`<p>` + esc(intro) + `</p>` +
		`<p><b>` + esc(from) + `:</b> ` + esc(name) + ` &lt;<span dir="ltr">` + esc(senderEmail) + `</span>&gt;<br>` +
		phoneHTML(phone, esc) +
		`<b>` + esc(page) + `:</b> ` + esc(orgName) + ` ` + esc(pageParen(pageSlug)) + esc(pageTag) + `</p>` +
		`<div style="white-space:pre-wrap;border-left:3px solid #4f46e5;padding:8px 12px;background:#f6f6fb">` + esc(body) + `</div>` +
		`<p>` + esc(reply) + `</p>` +
		`<p style="font-size:12px;color:#888">` + esc(footer) + `</p></body></html>`
	textBody = strings.Join([]string{
		intro, "",
		from + ": " + name + " <" + senderEmail + ">" + phoneText(phone),
		strings.TrimSpace(page+": "+orgName+" "+pageParen(pageSlug)) + pageTag, "",
		body, "",
		reply, "",
		footer,
	}, "\n")
	return subject, htmlBody, textBody
}

// FormatTelegram is the sales-chat message (English, like every message of
// the sales bot). Every value is escaped for Telegram's HTML parse mode, and
// the whole text stays within telegramMaxUTF16: the buyer's words are cut,
// never the lines around them. leg says what the e-mail leg did.
func FormatTelegram(orgName, pageSlug, senderName, senderEmail, body string, leg EmailLeg) string {
	esc := opsalert.EscapeHTML
	head := "✉️ <b>Message from a buyer</b>\n" +
		strings.TrimSpace("Sales page: "+esc(orgName)+" "+esc(pageParen(pageSlug))) + "\n" +
		"From: " + esc(strings.Join(strings.Fields(senderName), " ")) + " &lt;" + esc(senderEmail) + "&gt;\n\n"
	tail := "\n\nThe same message went to your e-mail. Reply to the buyer from there."
	switch leg {
	case EmailNoAddress:
		tail = "\n\nThis message was not e-mailed: Arena has no e-mail address to send it to. To answer the buyer, write to the address above."
	case EmailRetrying:
		tail = "\n\nThe e-mail copy could not be sent yet. Arena Sold Out keeps trying. To answer the buyer now, write to the address above."
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

func phoneHTML(phone string, esc func(string) string) string {
	if phone == "" {
		return ""
	}
	return `\u260e <span dir="ltr">` + esc(phone) + `</span><br>`
}

func phoneText(phone string) string {
	if phone == "" {
		return ""
	}
	return "\n\u260e " + phone
}

// siteFormMarker is the page_slug the static-site form stores (hsite/contact.go):
// not a page, only a marker that the message came from the channel's site.
const siteFormMarker = "feed-site"

// pageRef turns a stored page_slug into the page reference the letters print:
// a slug stays a slug, the site marker becomes the host of the site's first
// origin in parentheses ("" when the channel has none). RenderEmailTopic and
// FormatTelegram take this value in their pageSlug argument.
func pageRef(pageSlug string, originsJSON []byte) string {
	if pageSlug != siteFormMarker {
		return pageSlug
	}
	var origins []string
	if err := json.Unmarshal(originsJSON, &origins); err != nil {
		return ""
	}
	for _, o := range origins {
		if u, err := url.Parse(strings.TrimSpace(o)); err == nil && u.Host != "" {
			return "(" + u.Host + ")"
		}
	}
	return ""
}

// pageParen is the parenthesised page reference: "(/slug)", "(host)" or "".
func pageParen(ref string) string {
	switch {
	case ref == "":
		return ""
	case strings.HasPrefix(ref, "("):
		return ref
	}
	return "(/" + ref + ")"
}

// organizerFrom is the From of the organizer-facing letter: the organization's
// own sender only when it is verified, like the ticket letters. An empty From
// leaves the platform default to the e-mail adapter.
func organizerFrom(senderEmail, status, orgName string) (from, fromName string) {
	if status == "verified" && senderEmail != "" {
		from = senderEmail
	}
	return from, orgName
}
