// Package adminnotify tells every platform administrator (an active user with
// the platform role platform_superadmin) about an event personally: one e-mail
// per administrator and one Telegram message per live bot link, sent by the
// event-center bot (EVENTS_TELEGRAM_BOT_TOKEN). Fanout only enqueues worker
// jobs inside the caller's transaction; the caller owns de-duplication.
package adminnotify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	emailadapter "github.com/abhteam/arena_new/apps/backend/internal/adapters/email"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/opsalert"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/salesnotify"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/worker"
)

const (
	// JobTypeTelegram is the worker_jobs.job_type of one personal Telegram
	// message to a platform admin.
	JobTypeTelegram = "admin_notify.telegram"
	// JobTypeEmail is the worker_jobs.job_type of one e-mail to a platform admin.
	JobTypeEmail = "admin_notify.email"

	jobAttempts = 5

	defaultButton = "Открыть"
	defaultIcon   = "🆕"
)

// Recipient is one platform superadmin with the live Telegram chats.
type Recipient struct {
	UserID uuid.UUID
	Email  string
	Chats  []int64
}

// Message is what an administrator is told. WhatToDo is a full sentence
// ("Что делать: ..."); it may be empty.
type Message struct {
	// Kind names the notice in the e-mail job (such as
	// KindMetaConnectionBroken); empty for the platform-admin notices.
	Kind string
	// Icon starts the Telegram message; empty means the default.
	Icon        string
	Title       string
	Details     []string
	Link        string
	ButtonLabel string
	WhatToDo    string
}

// Options tunes Fanout.
type Options struct {
	// Telegram is true when the worker can send Telegram messages.
	Telegram bool
	Logger   *slog.Logger
}

// TelegramPayload is the payload of an admin_notify.telegram job.
type TelegramPayload struct {
	ChatID int64  `json:"chat_id"`
	Text   string `json:"text"`
}

// EmailPayload is the payload of an admin_notify.email job.
type EmailPayload struct {
	Kind        string   `json:"kind,omitempty"`
	Email       string   `json:"email"`
	UserID      string   `json:"user_id,omitempty"`
	Title       string   `json:"title"`
	Details     []string `json:"details,omitempty"`
	Link        string   `json:"link,omitempty"`
	ButtonLabel string   `json:"button_label,omitempty"`
	WhatToDo    string   `json:"what_to_do,omitempty"`
}

// Recipients lists the active platform superadmins with their live Telegram links.
func Recipients(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}) ([]Recipient, error) {
	rows, err := q.Query(ctx, `
SELECT u.id, u.email, coalesce((SELECT array_agg(l.telegram_user_id ORDER BY l.telegram_user_id)
                                FROM bot_telegram_links l WHERE l.user_id = u.id AND l.revoked_at IS NULL), '{}')
FROM users u
WHERE u.deactivated_at IS NULL
  AND (EXISTS (SELECT 1 FROM user_roles ur JOIN roles r ON r.id = ur.role_id
               WHERE ur.user_id = u.id AND ur.org_id IS NULL AND r.org_id IS NULL AND r.name = 'platform_superadmin')
    OR EXISTS (SELECT 1 FROM memberships m WHERE m.user_id = u.id AND m.role = 'platform_superadmin' AND m.status = 'active'))
ORDER BY u.email`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Recipient
	for rows.Next() {
		var a Recipient
		if err := rows.Scan(&a.UserID, &a.Email, &a.Chats); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// TelegramText renders the Telegram (HTML) message.
func TelegramText(m Message) string {
	e := opsalert.EscapeHTML
	icon := m.Icon
	if icon == "" {
		icon = defaultIcon
	}
	lines := []string{icon + " <b>" + e(m.Title) + "</b>"}
	for _, d := range m.Details {
		lines = append(lines, e(d))
	}
	if m.WhatToDo != "" {
		lines = append(lines, "", e(m.WhatToDo))
	}
	if m.Link != "" {
		lines = append(lines, m.Link)
	}
	return strings.Join(lines, "\n")
}

// Fanout queues one e-mail job per administrator and one Telegram job per live
// chat in the caller's transaction. It answers how many administrators there
// are; 0 means nothing was queued and the caller may fall back to another channel.
func Fanout(ctx context.Context, tx interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}, msg Message, opts Options) (int, error) {
	admins, err := Recipients(ctx, tx)
	if err != nil {
		return 0, err
	}
	return FanoutTo(ctx, tx, admins, msg, opts)
}

// FanoutTo queues one e-mail job per recipient and one Telegram job per live
// chat in the caller's transaction, and answers how many recipients there
// are. Without opts.Telegram only e-mails go, with a warning.
func FanoutTo(ctx context.Context, tx interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, recipients []Recipient, msg Message, opts Options) (int, error) {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	if len(recipients) == 0 {
		return 0, nil
	}
	text := TelegramText(msg)
	skipped := false
	for _, a := range recipients {
		if _, err := worker.EnqueueInTx(ctx, tx, JobTypeEmail, EmailPayload{
			Kind: msg.Kind, Email: a.Email, UserID: a.UserID.String(), Title: msg.Title, Details: msg.Details,
			Link: msg.Link, ButtonLabel: msg.ButtonLabel, WhatToDo: msg.WhatToDo,
		}, jobAttempts); err != nil {
			return 0, fmt.Errorf("adminnotify: queue e-mail: %w", err)
		}
		for _, chat := range a.Chats {
			if !opts.Telegram {
				skipped = true
				continue
			}
			if _, err := worker.EnqueueInTx(ctx, tx, JobTypeTelegram, TelegramPayload{ChatID: chat, Text: text}, jobAttempts); err != nil {
				return 0, fmt.Errorf("adminnotify: queue Telegram message: %w", err)
			}
		}
	}
	if skipped {
		logger.Warn("adminnotify: EVENTS_TELEGRAM_BOT_TOKEN is not set on the worker, the message goes by e-mail only", "kind", msg.Kind)
	}
	return len(recipients), nil
}

// TelegramSender is the part of the Telegram client the handler needs.
type TelegramSender interface {
	Send(ctx context.Context, chatID, text string) error
}

// NewTelegramHandler is the worker handler of admin_notify.telegram (and of the
// legacy onboarding.admin_telegram, which has the same payload). A nil sender
// (no EVENTS_TELEGRAM_BOT_TOKEN) drops the job with a warning.
func NewTelegramHandler(sender TelegramSender, logger *slog.Logger) func(context.Context, []byte) error {
	if logger == nil {
		logger = slog.Default()
	}
	return func(ctx context.Context, payload []byte) error {
		var p TelegramPayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("adminnotify: decode Telegram payload: %w", err)
		}
		if p.ChatID == 0 || p.Text == "" {
			return fmt.Errorf("adminnotify: Telegram payload missing chat or text")
		}
		if sender == nil {
			logger.Warn("adminnotify: Telegram message dropped, EVENTS_TELEGRAM_BOT_TOKEN is not set")
			return nil
		}
		err := sender.Send(ctx, fmt.Sprint(p.ChatID), p.Text)
		var perm *salesnotify.PermanentError
		if errors.As(err, &perm) {
			// The admin blocked the bot or deleted the chat: retrying is pointless.
			logger.Warn("adminnotify: Telegram message refused", slog.Any("error", err))
			return nil
		}
		return err
	}
}

// NewEmailHandler is the worker handler of admin_notify.email. A nil sender
// drops the job with a warning.
func NewEmailHandler(sender emailadapter.Sender, logger *slog.Logger) func(context.Context, []byte) error {
	if logger == nil {
		logger = slog.Default()
	}
	return func(ctx context.Context, payload []byte) error {
		var p EmailPayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("adminnotify: decode e-mail payload: %w", err)
		}
		if p.Email == "" || strings.TrimSpace(p.Title) == "" {
			return fmt.Errorf("adminnotify: e-mail payload missing address or title")
		}
		if sender == nil {
			logger.Warn("adminnotify: e-mail dropped, no e-mail sender")
			return nil
		}
		subject, htmlBody, textBody := RenderEmail(p)
		if err := sender.Send(ctx, emailadapter.Message{To: p.Email, Subject: subject, HTMLBody: htmlBody, TextBody: textBody}); err != nil {
			return fmt.Errorf("adminnotify: send e-mail: %w", err)
		}
		return nil
	}
}

// RenderEmail renders the staff letter (Russian, the look of the onboarding
// letter to administrators). A letter with a Kind goes to an organizer and
// signs with the brand ArenaSoldOut.com (owner decision 2026-11-12); the
// staff letters keep their old signature.
func RenderEmail(p EmailPayload) (subject, htmlBody, textBody string) {
	title := strings.TrimSpace(p.Title)
	button := strings.TrimSpace(p.ButtonLabel)
	if button == "" {
		button = defaultButton
	}
	brand, signature := "Arena", "Arena Platform"
	if p.Kind != "" {
		brand, signature = "ArenaSoldOut.com", "ArenaSoldOut.com"
	}
	var b strings.Builder
	b.WriteString(`<!DOCTYPE html><html lang="ru"><head><meta charset="UTF-8"><title>` + brand + `</title></head>`)
	b.WriteString(`<body style="font-family:sans-serif;max-width:600px;margin:0 auto;padding:20px;color:#1a1a2e">`)
	b.WriteString(`<h1 style="color:#1a1a2e;font-size:20px">` + html.EscapeString(title) + `</h1>`)
	if len(p.Details) > 0 {
		b.WriteString(`<p style="line-height:1.5">`)
		for i, d := range p.Details {
			if i > 0 {
				b.WriteString("<br>")
			}
			b.WriteString(html.EscapeString(d))
		}
		b.WriteString(`</p>`)
	}
	if p.WhatToDo != "" {
		b.WriteString(`<p style="line-height:1.5">` + html.EscapeString(p.WhatToDo) + `</p>`)
	}
	if p.Link != "" {
		b.WriteString(`<p style="margin:24px 0"><a href="` + html.EscapeString(p.Link) + `" style="background:#4f46e5;color:#fff;padding:12px 24px;border-radius:4px;text-decoration:none;font-weight:bold;display:inline-block">` + html.EscapeString(button) + `</a></p>`)
		b.WriteString(`<p style="font-size:12px;color:#666;word-break:break-all">` + html.EscapeString(p.Link) + `</p>`)
	}
	b.WriteString(`<hr><p style="font-size:11px;color:#999">` + signature + `</p></body></html>`)

	text := []string{title, ""}
	text = append(text, p.Details...)
	if p.WhatToDo != "" {
		text = append(text, "", p.WhatToDo)
	}
	if p.Link != "" {
		text = append(text, "", button+": "+p.Link)
	}
	text = append(text, "", signature)
	return title, b.String(), strings.Join(text, "\n")
}
