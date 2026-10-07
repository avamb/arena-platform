package authemail

// bot_invitation.go — the e-mail that carries a Telegram event-center bot
// invitation (08_architecture/28_telegram_event_center_bot_ru.md §3.3).
//
// POST /v1/organizations/{org_id}/bot-invitations enqueues a bot.invitation_email
// job in the same transaction as the membership and the invitation row
// (worker.EnqueueInTx); arena-worker renders it here. The link is a Telegram
// deep link, https://t.me/<bot>?start=inv_<code>, built from the configured
// bot username — never from a request header — and the code is never logged.

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"log/slog"
	"strings"
	"time"

	emailadapter "github.com/abhteam/arena_new/apps/backend/internal/adapters/email"
)

// JobTypeBotInvitationEmail is the worker_jobs.job_type of a bot invitation
// e-mail.
const JobTypeBotInvitationEmail = "bot.invitation_email"

// BotInvitationStartPrefix is prepended to the code in the deep link's start
// parameter, so the bot can tell an invitation from any other /start payload.
const BotInvitationStartPrefix = "inv_"

// BotInvitationEmailPayload is the JSON payload of a bot.invitation_email job.
type BotInvitationEmailPayload struct {
	// UserID is included in logs only.
	UserID string `json:"user_id"`
	// Email is the recipient address.
	Email string `json:"email"`
	// Code is the one-time invitation code (base64url, ≤ 43 chars). NOT logged.
	Code string `json:"code"`
	// ExpiresAt is rendered in the e-mail body.
	ExpiresAt time.Time `json:"expires_at"`
	// OrgName names the inviting organization.
	OrgName string `json:"org_name,omitempty"`
	// Role is the bot-level role the person is invited as: owner or manager.
	Role string `json:"role,omitempty"`
	// Locale selects the e-mail language (en default, ru, es).
	Locale string `json:"locale,omitempty"`
}

// BotDeepLink builds the Telegram deep link that opens the bot with the
// invitation code as its /start payload.
func BotDeepLink(botUsername, code string) string {
	return "https://t.me/" + strings.TrimPrefix(strings.TrimSpace(botUsername), "@") +
		"?start=" + BotInvitationStartPrefix + code
}

// HandleBotInvitationEmail is the worker.HandlerFunc for bot.invitation_email.
//
// A handler built without BotUsername cannot produce a working link and
// returns an error, so the job retries (and surfaces in last_error) instead
// of mailing a dead link.
func (h *Handler) HandleBotInvitationEmail(ctx context.Context, payload []byte) error {
	var p BotInvitationEmailPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return fmt.Errorf("authemail: decode bot invitation payload: %w", err)
	}
	if p.Email == "" || p.Code == "" {
		return fmt.Errorf("authemail: bot invitation payload missing email or code")
	}
	if h.botUsername == "" {
		return fmt.Errorf("authemail: bot invitation e-mail needs EVENTS_TELEGRAM_BOT_USERNAME")
	}

	link := BotDeepLink(h.botUsername, p.Code)
	subject, htmlBody, textBody := renderBotInvitationEmail(p.Locale, p.OrgName, p.Role, link, p.ExpiresAt)

	h.logger.Info("authemail: sending bot invitation email",
		slog.String("user_id", p.UserID),
		slog.String("role", p.Role),
		// code and link intentionally omitted
	)
	if err := h.sender.Send(ctx, emailadapter.Message{
		To:       p.Email,
		Subject:  subject,
		HTMLBody: htmlBody,
		TextBody: textBody,
	}); err != nil {
		return fmt.Errorf("authemail: send bot invitation email to user %s: %w", p.UserID, err)
	}
	h.logger.Info("authemail: bot invitation email delivered", slog.String("user_id", p.UserID))
	return nil
}

// renderBotInvitationEmail returns subject, HTML body and text body of the
// invitation in the requested locale (en default, ru, es).
func renderBotInvitationEmail(locale, orgName, role, link string, expiresAt time.Time) (subject, htmlBody, textBody string) {
	safeLink := html.EscapeString(link)
	expiry := expiresAt.UTC().Format(time.RFC3339)
	orgName = strings.Join(strings.Fields(orgName), " ")

	switch normalizeLocale(locale) {
	case "ru":
		roleWord := "менеджера"
		if role == "owner" {
			roleWord = "владельца"
		}
		if orgName != "" {
			subject = orgName + ": приглашение в бот мероприятий Arena"
		} else {
			subject = "Приглашение в бот мероприятий Arena"
		}
		intro := "Вас пригласили заводить мероприятия и смотреть продажи"
		if orgName != "" {
			intro += " организации «" + orgName + "»"
		}
		intro += " в Telegram-боте Arena (роль: " + roleWord + ")."
		htmlBody = fmt.Sprintf(botInviteHTMLRu, html.EscapeString(intro), expiry, safeLink, safeLink)
		textBody = strings.Join([]string{
			subject, "",
			intro,
			"Откройте ссылку в Telegram и нажмите «Start»:",
			link, "",
			"Ссылка одноразовая и действует до " + expiry + ".",
			"Если вы не ждали этого письма, просто проигнорируйте его.",
			"", "Arena Platform — автоматическое сообщение",
		}, "\n")
	case "es":
		roleWord := "gestor"
		if role == "owner" {
			roleWord = "propietario"
		}
		if orgName != "" {
			subject = orgName + ": invitación al bot de eventos de Arena"
		} else {
			subject = "Invitación al bot de eventos de Arena"
		}
		intro := "Te han invitado a crear eventos y seguir las ventas"
		if orgName != "" {
			intro += " de la organización «" + orgName + "»"
		}
		intro += " en el bot de Arena en Telegram (rol: " + roleWord + ")."
		htmlBody = fmt.Sprintf(botInviteHTMLEs, html.EscapeString(intro), expiry, safeLink, safeLink)
		textBody = strings.Join([]string{
			subject, "",
			intro,
			"Abre el enlace en Telegram y pulsa «Start»:",
			link, "",
			"El enlace es de un solo uso y es válido hasta " + expiry + ".",
			"Si no esperabas este correo, simplemente ignóralo.",
			"", "Arena Platform — mensaje automático",
		}, "\n")
	default:
		roleWord := "manager"
		if role == "owner" {
			roleWord = "owner"
		}
		if orgName != "" {
			subject = orgName + ": your invitation to the Arena events bot"
		} else {
			subject = "Your invitation to the Arena events bot"
		}
		intro := "You have been invited to create events and follow sales"
		if orgName != "" {
			intro += " of " + orgName
		}
		intro += " in the Arena Telegram bot (role: " + roleWord + ")."
		htmlBody = fmt.Sprintf(botInviteHTMLEn, html.EscapeString(intro), expiry, safeLink, safeLink)
		textBody = strings.Join([]string{
			subject, "",
			intro,
			"Open this link in Telegram and press Start:",
			link, "",
			"The link works once and is valid until " + expiry + ".",
			"If you were not expecting this e-mail, you can safely ignore it.",
			"", "Arena Platform - automated message",
		}, "\n")
	}
	return
}

// Positional arguments: [1]=intro (escaped), [2]=expiry, [3]=link (href),
// [4]=link (display).
const botInviteHTMLEn = `<!DOCTYPE html>
<html lang="en">
<head><meta charset="UTF-8"><title>Arena events bot</title></head>
<body style="font-family:sans-serif;max-width:600px;margin:0 auto;padding:20px;color:#1a1a2e">
  <h1 style="color:#1a1a2e">Arena events bot</h1>
  <p>%s</p>
  <p>Open the link in Telegram and press <strong>Start</strong>.
     The link works once and is valid until <strong>%s</strong>.</p>
  <p style="margin:24px 0">
    <a href="%s"
       style="background:#1a73e8;color:#fff;padding:12px 24px;border-radius:4px;text-decoration:none;font-weight:bold;display:inline-block">
      Open the bot
    </a>
  </p>
  <p style="font-size:12px;color:#666">
    If the button does not work, copy and paste this link into Telegram:<br>
    <span style="word-break:break-all">%s</span>
  </p>
  <p style="font-size:11px;color:#999">
    If you were not expecting this e-mail, you can safely ignore it.
  </p>
  <hr>
  <p style="font-size:11px;color:#999">Arena Platform &mdash; automated message</p>
</body>
</html>`

const botInviteHTMLEs = `<!DOCTYPE html>
<html lang="es">
<head><meta charset="UTF-8"><title>Bot de eventos de Arena</title></head>
<body style="font-family:sans-serif;max-width:600px;margin:0 auto;padding:20px;color:#1a1a2e">
  <h1 style="color:#1a1a2e">Bot de eventos de Arena</h1>
  <p>%s</p>
  <p>Abre el enlace en Telegram y pulsa <strong>Start</strong>.
     El enlace es de un solo uso y es válido hasta <strong>%s</strong>.</p>
  <p style="margin:24px 0">
    <a href="%s"
       style="background:#1a73e8;color:#fff;padding:12px 24px;border-radius:4px;text-decoration:none;font-weight:bold;display:inline-block">
      Abrir el bot
    </a>
  </p>
  <p style="font-size:12px;color:#666">
    Si el botón no funciona, copia y pega este enlace en Telegram:<br>
    <span style="word-break:break-all">%s</span>
  </p>
  <p style="font-size:11px;color:#999">
    Si no esperabas este correo, simplemente ignóralo.
  </p>
  <hr>
  <p style="font-size:11px;color:#999">Arena Platform &mdash; mensaje automático</p>
</body>
</html>`

const botInviteHTMLRu = `<!DOCTYPE html>
<html lang="ru">
<head><meta charset="UTF-8"><title>Бот мероприятий Arena</title></head>
<body style="font-family:sans-serif;max-width:600px;margin:0 auto;padding:20px;color:#1a1a2e">
  <h1 style="color:#1a1a2e">Бот мероприятий Arena</h1>
  <p>%s</p>
  <p>Откройте ссылку в Telegram и нажмите <strong>Start</strong>.
     Ссылка одноразовая и действует до <strong>%s</strong>.</p>
  <p style="margin:24px 0">
    <a href="%s"
       style="background:#1a73e8;color:#fff;padding:12px 24px;border-radius:4px;text-decoration:none;font-weight:bold;display:inline-block">
      Открыть бота
    </a>
  </p>
  <p style="font-size:12px;color:#666">
    Если кнопка не работает, скопируйте и вставьте эту ссылку в Telegram:<br>
    <span style="word-break:break-all">%s</span>
  </p>
  <p style="font-size:11px;color:#999">
    Если вы не ждали этого письма, просто проигнорируйте его.
  </p>
  <hr>
  <p style="font-size:11px;color:#999">Arena Platform &mdash; автоматическое сообщение</p>
</body>
</html>`
