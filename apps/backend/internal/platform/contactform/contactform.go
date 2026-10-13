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
//     (settings.hosted_page.public_contact.email), read when the job runs so
//     an address the owner changed in the meantime wins; Reply-To is the
//     buyer, so the organizer answers with one click and the buyer never
//     learns the organizer's address from arena. A failed send returns the
//     error and the worker retries.
//   - Telegram: when the organization keeps contact_form_to_telegram on, the
//     message is posted to the organization's OWN sales-notification chats
//     (never the operator's). Best effort: a Telegram failure never retries
//     the e-mail.
//
// Every buyer-typed value is escaped for the HTML of the e-mail and of the
// Telegram message. Messages older than RetentionDays are deleted by every
// run.
package contactform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	emailadapter "github.com/abhteam/arena_new/apps/backend/internal/adapters/email"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/opsalert"
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
// (salesnotify.Dispatcher.AnnounceToOrg).
type Announcer interface {
	AnnounceToOrg(ctx context.Context, orgID, text string) int
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
	TelegramSent   bool
	OrgName        string
	OrgLocale      string
	ToTelegram     bool
	PublicContact  []byte
	ChannelDeleted bool
}

const loadMessageSQL = `
SELECT m.id, m.org_id, m.page_slug, m.sender_name, m.sender_email, m.message,
       m.email_sent_at IS NOT NULL, m.telegram_sent_at IS NOT NULL,
       o.name, o.default_locale, o.contact_form_to_telegram,
       sc.settings #> '{hosted_page,public_contact}', sc.deleted_at IS NOT NULL
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
	h.purge(ctx)

	var m message
	err = h.pool.QueryRow(ctx, loadMessageSQL, id).Scan(
		&m.ID, &m.OrgID, &m.PageSlug, &m.SenderName, &m.SenderEmail, &m.Body,
		&m.EmailSent, &m.TelegramSent, &m.OrgName, &m.OrgLocale, &m.ToTelegram,
		&m.PublicContact, &m.ChannelDeleted,
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
			to = contactEmail(m.PublicContact)
		}
		if to == "" {
			// The owner removed the address after the buyer wrote: nothing
			// to mail. The Telegram leg still runs.
			h.logger.Warn("contactform: channel has no public e-mail; message not mailed", slog.String("message_id", id.String()))
		} else {
			subject, htmlBody, textBody := RenderEmail(m.OrgLocale, m.OrgName, m.PageSlug, m.SenderName, m.SenderEmail, m.Body)
			if err := h.sender.Send(ctx, emailadapter.Message{
				To: to, ReplyTo: m.SenderEmail, Subject: subject, HTMLBody: htmlBody, TextBody: textBody,
			}); err != nil {
				return fmt.Errorf("contactform: send message %s: %w", id, err)
			}
		}
		if _, err := h.pool.Exec(ctx, `UPDATE organizer_contact_messages SET email_sent_at = now() WHERE id = $1`, id); err != nil {
			return fmt.Errorf("contactform: mark e-mail sent: %w", err)
		}
	}

	if !m.TelegramSent {
		if m.ToTelegram && h.announcer != nil {
			n := h.announcer.AnnounceToOrg(ctx, m.OrgID.String(), FormatTelegram(m.OrgName, m.PageSlug, m.SenderName, m.SenderEmail, m.Body))
			h.logger.Info("contactform: message posted to the organization's chats", slog.String("message_id", id.String()), slog.Int("chats", n))
		}
		if _, err := h.pool.Exec(ctx, `UPDATE organizer_contact_messages SET telegram_sent_at = now() WHERE id = $1`, id); err != nil {
			h.logger.Warn("contactform: mark telegram sent failed", slog.String("message_id", id.String()), slog.String("error", err.Error()))
		}
	}
	return nil
}

// purge deletes messages older than RetentionDays. Best effort.
func (h *Handler) purge(ctx context.Context) {
	if _, err := h.pool.Exec(ctx, `DELETE FROM organizer_contact_messages WHERE created_at < now() - make_interval(days => $1)`, RetentionDays); err != nil {
		h.logger.Warn("contactform: purge failed", slog.String("error", err.Error()))
	}
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

// emailWords is the text of the organizer's letter per language.
type emailWords struct {
	subject, intro, from, page, reply, footer string
}

var words = map[string]emailWords{
	"en": {
		subject: "Message from a buyer: %s",
		intro:   "A buyer wrote to you through the contact form of your Arena sales page.",
		from:    "From", page: "Sales page",
		reply:  "Reply to this e-mail to answer the buyer. Arena does not see your answer.",
		footer: "Arena Sold Out sent this message because the contact form of your sales page is on.",
	},
	"ru": {
		subject: "Сообщение от покупателя: %s",
		intro:   "Покупатель написал вам через форму связи на вашей странице продаж Arena.",
		from:    "От", page: "Страница продаж",
		reply:  "Чтобы ответить покупателю, ответьте на это письмо. Arena не видит ваш ответ.",
		footer: "Arena Sold Out отправила это письмо, потому что на вашей странице продаж включена форма связи.",
	},
	"es": {
		subject: "Mensaje de un comprador: %s",
		intro:   "Un comprador te escribió con el formulario de contacto de tu página de venta de Arena.",
		from:    "De", page: "Página de venta",
		reply:  "Responde a este correo para contestar al comprador. Arena no ve tu respuesta.",
		footer: "Arena Sold Out envió este mensaje porque el formulario de contacto de tu página de venta está activo.",
	},
	"cs": {
		subject: "Zpráva od kupujícího: %s",
		intro:   "Kupující vám napsal přes kontaktní formulář vaší prodejní stránky Arena.",
		from:    "Od", page: "Prodejní stránka",
		reply:  "Kupujícímu odpovíte odpovědí na tento e-mail. Arena vaši odpověď nevidí.",
		footer: "Arena Sold Out poslala tuto zprávu, protože kontaktní formulář vaší prodejní stránky je zapnutý.",
	},
	"he": {
		subject: "הודעה מקונה: %s",
		intro:   "קונה כתב לך דרך טופס יצירת הקשר בדף המכירה שלך ב-Arena.",
		from:    "מאת", page: "דף המכירה",
		reply:  "כדי לענות לקונה, השב למייל הזה. Arena לא רואה את התשובה שלך.",
		footer: "Arena Sold Out שלחה הודעה זו כי טופס יצירת הקשר בדף המכירה שלך פעיל.",
	},
}

func wordsFor(locale string) (emailWords, string) {
	l := strings.ToLower(strings.TrimSpace(locale))
	if i := strings.IndexAny(l, "-_"); i > 0 {
		l = l[:i]
	}
	if w, ok := words[l]; ok {
		return w, l
	}
	return words["en"], "en"
}

// RenderEmail builds the organizer's letter in the organization's language.
// Every buyer-typed value is HTML-escaped in the HTML body.
func RenderEmail(locale, orgName, pageSlug, senderName, senderEmail, body string) (subject, htmlBody, textBody string) {
	w, l := wordsFor(locale)
	name := strings.Join(strings.Fields(senderName), " ")
	subject = fmt.Sprintf(w.subject, name)
	dir := "ltr"
	if l == "he" {
		dir = "rtl"
	}
	esc := html.EscapeString
	htmlBody = `<!doctype html><html dir="` + dir + `"><body style="font-family:Arial,sans-serif;color:#222;line-height:1.5">` +
		`<p>` + esc(w.intro) + `</p>` +
		`<p><b>` + esc(w.from) + `:</b> ` + esc(name) + ` &lt;<span dir="ltr">` + esc(senderEmail) + `</span>&gt;<br>` +
		`<b>` + esc(w.page) + `:</b> ` + esc(orgName) + ` (/` + esc(pageSlug) + `)</p>` +
		`<div style="white-space:pre-wrap;border-left:3px solid #4f46e5;padding:8px 12px;background:#f6f6fb">` + esc(body) + `</div>` +
		`<p>` + esc(w.reply) + `</p>` +
		`<p style="font-size:12px;color:#888">` + esc(w.footer) + `</p></body></html>`
	textBody = strings.Join([]string{
		w.intro, "",
		w.from + ": " + name + " <" + senderEmail + ">",
		w.page + ": " + orgName + " (/" + pageSlug + ")", "",
		body, "",
		w.reply, "",
		w.footer,
	}, "\n")
	return subject, htmlBody, textBody
}

// FormatTelegram is the sales-chat message (English, like every message of
// the sales bot). Every value is escaped for Telegram's HTML parse mode.
func FormatTelegram(orgName, pageSlug, senderName, senderEmail, body string) string {
	esc := opsalert.EscapeHTML
	var b strings.Builder
	b.WriteString("✉️ <b>Message from a buyer</b>\n")
	b.WriteString("Sales page: " + esc(orgName) + " (/" + esc(pageSlug) + ")\n")
	b.WriteString("From: " + esc(strings.Join(strings.Fields(senderName), " ")) + " &lt;" + esc(senderEmail) + "&gt;\n\n")
	b.WriteString(clipEscaped(body, maxTelegramBody))
	b.WriteString("\n\nThe same message went to your public e-mail. Reply to the buyer from there.")
	return b.String()
}

// maxTelegramBody bounds the escaped message in a Telegram post (Telegram
// refuses texts over 4096 characters; escaping can make "<" five times longer).
const maxTelegramBody = 3000

// clipEscaped escapes s for Telegram HTML and stops before the escaped text
// grows past max characters, ending with "…" when it had to stop.
func clipEscaped(s string, max int) string {
	var b strings.Builder
	n := 0
	for _, r := range s {
		e := opsalert.EscapeHTML(string(r))
		l := utf8.RuneCountInString(e)
		if n+l > max {
			b.WriteString("…")
			break
		}
		b.WriteString(e)
		n += l
	}
	return b.String()
}
