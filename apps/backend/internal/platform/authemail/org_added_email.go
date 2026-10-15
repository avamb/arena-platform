package authemail

// org_added_email.go — the letter an EXISTING account receives when an owner
// adds it to an organization through the web channel of
// POST /v1/organizations/{org_id}/bot-invitations (channel=web, plan
// docs/plans/event_center_unified_plan_2026-10-14_ru.md stage 0, A2).
//
// A NEW account gets the ordinary org_invitation password letter
// (organizer_password.go) with its one-time setup link. An account that
// already exists has nothing to set up: the letter says it was added, names
// the web event center and carries NO token. Because arena cannot tell
// whether such an account ever chose a password (an account made by an earlier
// bot invitation holds a random one), the letter also says where to ask for
// a new password.
//
// The job is queued with worker.EnqueueInTx in the same transaction as the
// membership. The letter is built by organizerLetter, the same renderer as
// the organizer's other password letters, and goes through the same sender.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	emailadapter "github.com/abhteam/arena_new/apps/backend/internal/adapters/email"
)

// JobTypeOrgAddedEmail is the worker_jobs.job_type of an "added to an
// organization" letter for an existing account.
const JobTypeOrgAddedEmail = "auth.org_added_email"

// OrgAddedEmailPayload is the JSON payload of an auth.org_added_email job. It
// carries no token and no link.
type OrgAddedEmailPayload struct {
	// UserID is included in logs only.
	UserID string `json:"user_id"`
	// Email is the recipient address.
	Email string `json:"email"`
	// OrgName names the organization the person was added to.
	OrgName string `json:"org_name,omitempty"`
	// Locale selects the letter language (en default, ru, es).
	Locale string `json:"locale,omitempty"`
}

// orgAddedText holds the words of one language.
type orgAddedText struct {
	subjectOrg, subject         string // subjectOrg: %s = organization
	title                       string
	introOrg, intro, whatIs     string // introOrg: %s = organization
	hello, stepsIntro, fallback string
	steps                       []string // %s in the 2nd step = e-mail, %s in the 3rd = forgot page
	button, ignore              string
	address, auto               string // address: %s = event center
}

var orgAddedTexts = map[string]orgAddedText{
	"en": {
		subjectOrg: "%s added you to the Arena event center",
		subject:    "You were added to an organization in the Arena event center",
		title:      "You are in the team",
		introOrg:   "%s added you to its team in the Arena event center.",
		intro:      "You were added to an organization in the Arena event center.",
		whatIs:     "In the event center you see your events, sales and orders.",
		hello:      "Hello!",
		stepsIntro: "What to do:",
		fallback:   "If the button does not work, copy this link into the address bar of your browser:",
		steps: []string{
			"Press the “Open the event center” button below.",
			"Sign in. Your e-mail is %s. Use the password you already have.",
			"If you do not have a password yet, open %s, type your e-mail and ask for a new letter.",
		},
		button:  "Open the event center",
		ignore:  "If you were not expecting this letter, do nothing. Your password stays the same.",
		address: "The event center address: %s. Save it in your bookmarks.",
		auto:    "This letter was sent automatically. Please do not reply to it.",
	},
	"ru": {
		subjectOrg: "%s добавляет вас в ивент-центр Arena",
		subject:    "Вас добавили в организацию в ивент-центре Arena",
		title:      "Вы в команде",
		introOrg:   "Организация «%s» добавила вас в свою команду в ивент-центре Arena.",
		intro:      "Вас добавили в организацию в ивент-центре Arena.",
		whatIs:     "В ивент-центре вы видите свои мероприятия, продажи и заказы.",
		hello:      "Здравствуйте!",
		stepsIntro: "Что сделать:",
		fallback:   "Если кнопка не работает, скопируйте эту ссылку в адресную строку браузера:",
		steps: []string{
			"Нажмите кнопку «Открыть ивент-центр» ниже.",
			"Войдите. Ваш адрес: %s. Пароль тот же, что у вас уже есть.",
			"Если пароля ещё нет, откройте %s, введите свой адрес и запросите новое письмо.",
		},
		button:  "Открыть ивент-центр",
		ignore:  "Если вы не ждали этого письма, ничего не делайте. Ваш пароль останется прежним.",
		address: "Адрес ивент-центра: %s. Сохраните его в закладки.",
		auto:    "Письмо отправлено автоматически. Отвечать на него не нужно.",
	},
	"es": {
		subjectOrg: "%s te añade al centro de eventos de Arena",
		subject:    "Te han añadido a una organización en el centro de eventos de Arena",
		title:      "Ya estás en el equipo",
		introOrg:   "La organización «%s» te ha añadido a su equipo en el centro de eventos de Arena.",
		intro:      "Te han añadido a una organización en el centro de eventos de Arena.",
		whatIs:     "En el centro de eventos ves tus eventos, ventas y pedidos.",
		hello:      "¡Hola!",
		stepsIntro: "Qué hacer:",
		fallback:   "Si el botón no funciona, copia este enlace en la barra de direcciones del navegador:",
		steps: []string{
			"Pulsa el botón «Abrir el centro de eventos» de abajo.",
			"Entra. Tu correo es %s. Usa la contraseña que ya tienes.",
			"Si aún no tienes contraseña, abre %s, escribe tu correo y pide un correo nuevo.",
		},
		button:  "Abrir el centro de eventos",
		ignore:  "Si no esperabas este correo, no hagas nada. Tu contraseña sigue igual.",
		address: "La dirección del centro de eventos: %s. Guárdala en tus marcadores.",
		auto:    "Este correo se envió automáticamente. No hace falta responder.",
	},
}

// renderOrgAddedEmail builds the letter. base is the event center origin; the
// letter carries the sign-in address and no token.
func renderOrgAddedEmail(locale, orgName, email, base string) (subject, htmlBody, textBody string) {
	lang := organizerLocale(locale)
	t := orgAddedTexts[lang]
	orgName = strings.Join(strings.Fields(orgName), " ")
	loginURL := base + "/login"
	forgot := base + "/forgot-password"

	l := organizerLetter{lang: lang, title: t.title, stepsIntro: t.stepsIntro, fallback: t.fallback, button: t.button}
	if orgName != "" {
		l.subject = fmt.Sprintf(t.subjectOrg, orgName)
		l.intro = []string{t.hello, fmt.Sprintf(t.introOrg, orgName), t.whatIs}
	} else {
		l.subject = t.subject
		l.intro = []string{t.hello, t.intro, t.whatIs}
	}
	l.steps = []string{t.steps[0], fmt.Sprintf(t.steps[1], email), fmt.Sprintf(t.steps[2], forgot)}
	l.after = []string{t.ignore}
	l.footer = []string{fmt.Sprintf(t.address, base), t.auto}
	return l.subject, l.html(loginURL), l.text(loginURL)
}

// HandleOrgAddedEmail is the worker.HandlerFunc for auth.org_added_email. A
// handler built without EventCenterURL cannot name the address and returns an
// error, so the job retries instead of mailing a letter without it.
func (h *Handler) HandleOrgAddedEmail(ctx context.Context, payload []byte) error {
	var p OrgAddedEmailPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return fmt.Errorf("authemail: decode org added payload: %w", err)
	}
	if p.Email == "" {
		return fmt.Errorf("authemail: org added payload missing email")
	}
	if h.eventCenter == "" {
		return fmt.Errorf("authemail: org added e-mail needs EVENT_CENTER_PUBLIC_URL")
	}
	subject, htmlBody, textBody := renderOrgAddedEmail(p.Locale, p.OrgName, p.Email, h.eventCenter)

	h.logger.Info("authemail: sending org added email", slog.String("user_id", p.UserID))
	if err := h.sender.Send(ctx, emailadapter.Message{
		To:       p.Email,
		Subject:  subject,
		HTMLBody: htmlBody,
		TextBody: textBody,
	}); err != nil {
		return fmt.Errorf("authemail: send org added email to user %s: %w", p.UserID, err)
	}
	h.logger.Info("authemail: org added email delivered", slog.String("user_id", p.UserID))
	return nil
}
