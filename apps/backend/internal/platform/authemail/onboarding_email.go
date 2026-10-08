package authemail

// onboarding_email.go — the e-mails of the organizer application flow
// (08_architecture/34_onboarding_applications_ru.md §11): the confirm/continue
// link, reminders, "received", a request for details, the approval and the
// refusal. Producers enqueue an onboarding.email job in the same transaction as
// the state change (worker.EnqueueInTx); arena-worker renders it here. The
// link token is never logged.

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"log/slog"
	"net/url"
	"strings"

	emailadapter "github.com/abhteam/arena_new/apps/backend/internal/adapters/email"
)

// JobTypeOnboardingEmail is the worker_jobs.job_type of an application e-mail.
const JobTypeOnboardingEmail = "onboarding.email"

// Kinds of onboarding e-mail.
const (
	OnboardingKindConfirm       = "confirm"
	OnboardingKindResume        = "resume"
	OnboardingKindReminder      = "reminder"
	OnboardingKindReceived      = "received"
	OnboardingKindInfoRequested = "info_requested"
	OnboardingKindApproved      = "approved"
	OnboardingKindRejected      = "rejected"
	// OnboardingKindCode carries the six-digit code of the Telegram channel.
	OnboardingKindCode = "code"
)

// OnboardingEmailPayload is the JSON payload of an onboarding.email job.
type OnboardingEmailPayload struct {
	Kind          string `json:"kind"`
	ApplicationID string `json:"application_id"`
	Email         string `json:"email"`
	FirstName     string `json:"first_name,omitempty"`
	Locale        string `json:"locale,omitempty"`
	OrgName       string `json:"org_name,omitempty"`
	// Token is the raw resume token. NOT logged.
	Token   string `json:"token,omitempty"`
	Message string `json:"message,omitempty"`
	// Code is the six-digit e-mail confirmation code of the Telegram channel. NOT logged.
	Code   string   `json:"code,omitempty"`
	Fields []string `json:"fields,omitempty"`
}

// onboardingKindNeedsLink lists the kinds whose button opens the form.
var onboardingKindNeedsLink = map[string]bool{
	OnboardingKindConfirm: true, OnboardingKindResume: true,
	OnboardingKindReminder: true, OnboardingKindInfoRequested: true,
}

// HandleOnboardingEmail is the worker.HandlerFunc for onboarding.email.
func (h *Handler) HandleOnboardingEmail(ctx context.Context, payload []byte) error {
	var p OnboardingEmailPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return fmt.Errorf("authemail: decode onboarding payload: %w", err)
	}
	if p.Email == "" || p.Kind == "" {
		return fmt.Errorf("authemail: onboarding payload missing email or kind")
	}
	link := ""
	switch {
	case p.Kind == OnboardingKindCode:
		if p.Code == "" {
			return fmt.Errorf("authemail: onboarding code e-mail has no code")
		}
	case onboardingKindNeedsLink[p.Kind]:
		if p.Token == "" {
			return fmt.Errorf("authemail: onboarding %s e-mail has no token", p.Kind)
		}
		if h.siteURL == "" {
			return fmt.Errorf("authemail: onboarding e-mail needs ONBOARDING_SITE_URL")
		}
		link = h.siteURL + "/start/confirm?token=" + url.QueryEscape(p.Token) + "&lang=" + url.QueryEscape(normalizeOnboardingLocale(p.Locale))
	case p.Kind == OnboardingKindApproved:
		link = h.appPublicURL
	}
	subject, htmlBody, textBody, ok := renderOnboardingEmail(p, link)
	if !ok {
		return fmt.Errorf("authemail: unknown onboarding e-mail kind %q", p.Kind)
	}
	h.logger.Info("authemail: sending onboarding e-mail",
		slog.String("kind", p.Kind), slog.String("application_id", p.ApplicationID))
	if err := h.sender.Send(ctx, emailadapter.Message{To: p.Email, Subject: subject, HTMLBody: htmlBody, TextBody: textBody}); err != nil {
		return fmt.Errorf("authemail: send onboarding %s e-mail for %s: %w", p.Kind, p.ApplicationID, err)
	}
	return nil
}

func normalizeOnboardingLocale(l string) string {
	switch strings.ToLower(strings.TrimSpace(l)) {
	case "ru":
		return "ru"
	case "es":
		return "es"
	}
	return "en"
}

type onboardingText struct {
	Subject string
	Body    string
	Button  string
}

// onboardingTexts[locale][kind]. {name} {org} {message} are replaced.
var onboardingTexts = map[string]map[string]onboardingText{
	"en": {
		OnboardingKindConfirm:       {"Confirm your e-mail to continue your Arena application", "Hello{name}, thank you for starting an application to sell tickets with Arena. Confirm your e-mail address with the button below. You can leave and come back later: your answers are saved, and this link brings you back to the form.", "Confirm and continue"},
		OnboardingKindResume:        {"Continue your Arena application", "Hello{name}, here is the link to continue your application. Your answers are saved.", "Continue the application"},
		OnboardingKindReminder:      {"Your Arena application is waiting for you", "Hello{name}, you started an application to sell tickets with Arena but have not finished it. Your answers are saved and it takes only a few minutes to complete.", "Finish the application"},
		OnboardingKindReceived:      {"We received your Arena application", "Hello{name}, we received the application{org}. Our team checks every organizer by hand, usually within one or two working days. We will write to you as soon as there is a decision or a question.", ""},
		OnboardingKindInfoRequested: {"We need a few details for your Arena application", "Hello{name}, to continue with your application we need some details:\n\n{message}\n\nOpen the form with the button below and update the highlighted answers.", "Update the application"},
		OnboardingKindApproved:      {"Your Arena application is approved", "Hello{name}, good news: your application{org} is approved and your workspace is ready. If this is your first Arena account, a separate e-mail brings a link to set your password. Then sign in to create your first event.", "Sign in to Arena"},
		OnboardingKindCode:          {"Your Arena confirmation code", "Hello{name}, enter this code in the Telegram chat to confirm your e-mail address. It is valid for 15 minutes. If you did not start an application, ignore this e-mail.", ""},
		OnboardingKindRejected:      {"About your Arena application", "Hello{name}, thank you for your interest in Arena. We are not able to approve the application{org} at this time.{message} If you think this is a mistake, simply reply to this e-mail.", ""},
	},
	"ru": {
		OnboardingKindConfirm:       {"Подтвердите e-mail, чтобы продолжить заявку в Arena", "Здравствуйте{name}, спасибо, что начали заявку на продажу билетов через Arena. Подтвердите адрес кнопкой ниже. Заявку можно оставить и вернуться позже: ответы сохранены, а эта ссылка возвращает вас в анкету.", "Подтвердить и продолжить"},
		OnboardingKindResume:        {"Продолжить заявку в Arena", "Здравствуйте{name}, вот ссылка, чтобы продолжить заявку. Ваши ответы сохранены.", "Продолжить заявку"},
		OnboardingKindReminder:      {"Ваша заявка в Arena ждёт вас", "Здравствуйте{name}, вы начали заявку на продажу билетов через Arena, но не закончили её. Ответы сохранены, осталось несколько минут.", "Закончить заявку"},
		OnboardingKindReceived:      {"Мы получили вашу заявку в Arena", "Здравствуйте{name}, мы получили заявку{org}. Команда проверяет каждого организатора вручную, обычно за один-два рабочих дня. Мы напишем вам, как только будет решение или вопрос.", ""},
		OnboardingKindInfoRequested: {"Нужны уточнения по вашей заявке в Arena", "Здравствуйте{name}, чтобы продолжить с заявкой, нам нужны уточнения:\n\n{message}\n\nОткройте анкету кнопкой ниже и обновите отмеченные ответы.", "Обновить заявку"},
		OnboardingKindApproved:      {"Ваша заявка в Arena одобрена", "Здравствуйте{name}, хорошие новости: заявка{org} одобрена, рабочее пространство готово. Если это ваш первый аккаунт в Arena, отдельным письмом придёт ссылка для создания пароля. После этого войдите и создайте первое мероприятие.", "Войти в Arena"},
		OnboardingKindCode:          {"Код подтверждения Arena", "Здравствуйте{name}, введите этот код в чате Telegram, чтобы подтвердить адрес e-mail. Код действует 15 минут. Если вы не подавали заявку, просто проигнорируйте письмо.", ""},
		OnboardingKindRejected:      {"О вашей заявке в Arena", "Здравствуйте{name}, спасибо за интерес к Arena. Сейчас мы не можем одобрить заявку{org}.{message} Если вы считаете, что это ошибка, просто ответьте на это письмо.", ""},
	},
	"es": {
		OnboardingKindConfirm:       {"Confirma tu correo para continuar con tu solicitud en Arena", "Hola{name}, gracias por iniciar una solicitud para vender entradas con Arena. Confirma tu correo con el botón de abajo. Puedes dejarlo y volver más tarde: tus respuestas quedan guardadas y este enlace te devuelve al formulario.", "Confirmar y continuar"},
		OnboardingKindResume:        {"Continúa tu solicitud en Arena", "Hola{name}, aquí tienes el enlace para continuar con tu solicitud. Tus respuestas están guardadas.", "Continuar la solicitud"},
		OnboardingKindReminder:      {"Tu solicitud en Arena te está esperando", "Hola{name}, empezaste una solicitud para vender entradas con Arena pero no la terminaste. Tus respuestas están guardadas y solo faltan unos minutos.", "Terminar la solicitud"},
		OnboardingKindReceived:      {"Hemos recibido tu solicitud en Arena", "Hola{name}, hemos recibido la solicitud{org}. Nuestro equipo revisa a cada organizador a mano, normalmente en uno o dos días laborables. Te escribiremos en cuanto haya una decisión o una pregunta.", ""},
		OnboardingKindInfoRequested: {"Necesitamos algunos datos para tu solicitud en Arena", "Hola{name}, para continuar con tu solicitud necesitamos algunos datos:\n\n{message}\n\nAbre el formulario con el botón de abajo y actualiza las respuestas marcadas.", "Actualizar la solicitud"},
		OnboardingKindApproved:      {"Tu solicitud en Arena está aprobada", "Hola{name}, buenas noticias: la solicitud{org} está aprobada y tu espacio de trabajo está listo. Si es tu primera cuenta en Arena, otro correo te enviará un enlace para crear tu contraseña. Después entra y crea tu primer evento.", "Entrar en Arena"},
		OnboardingKindCode:          {"Tu código de confirmación de Arena", "Hola{name}, introduce este código en el chat de Telegram para confirmar tu correo. Es válido durante 15 minutos. Si no iniciaste una solicitud, ignora este correo.", ""},
		OnboardingKindRejected:      {"Sobre tu solicitud en Arena", "Hola{name}, gracias por tu interés en Arena. Por ahora no podemos aprobar la solicitud{org}.{message} Si crees que es un error, responde a este correo.", ""},
	},
}

// renderOnboardingEmail returns the subject and both bodies, ok=false for an
// unknown kind.
func renderOnboardingEmail(p OnboardingEmailPayload, link string) (subject, htmlBody, textBody string, ok bool) {
	locale := normalizeOnboardingLocale(p.Locale)
	t, found := onboardingTexts[locale][p.Kind]
	if !found {
		return "", "", "", false
	}
	name := ""
	if n := strings.TrimSpace(p.FirstName); n != "" {
		name = " " + n
	}
	org := ""
	if o := strings.Join(strings.Fields(p.OrgName), " "); o != "" {
		org = " «" + o + "»"
	}
	message := ""
	if m := strings.TrimSpace(p.Message); m != "" {
		if p.Kind == OnboardingKindRejected {
			message = " " + m
		} else {
			message = m
		}
	}
	body := strings.NewReplacer("{name}", name, "{org}", org, "{message}", message).Replace(t.Body)

	codeBlock := ""
	if p.Code != "" {
		codeBlock = `<p style="font-size:30px;letter-spacing:8px;font-weight:bold;margin:24px 0">` + html.EscapeString(p.Code) + `</p>`
	}
	var b strings.Builder
	b.WriteString(`<!DOCTYPE html><html lang="` + locale + `"><head><meta charset="UTF-8"><title>Arena</title></head>`)
	b.WriteString(`<body style="font-family:sans-serif;max-width:600px;margin:0 auto;padding:20px;color:#1a1a2e">`)
	b.WriteString(`<h1 style="color:#1a1a2e;font-size:20px">` + html.EscapeString(t.Subject) + `</h1>`)
	for _, para := range strings.Split(body, "\n\n") {
		b.WriteString(`<p style="line-height:1.5">` + strings.ReplaceAll(html.EscapeString(strings.TrimSpace(para)), "\n", "<br>") + `</p>`)
	}
	b.WriteString(codeBlock)
	if t.Button != "" && link != "" {
		b.WriteString(`<p style="margin:24px 0"><a href="` + html.EscapeString(link) + `" style="background:#4f46e5;color:#fff;padding:12px 24px;border-radius:4px;text-decoration:none;font-weight:bold;display:inline-block">` +
			html.EscapeString(t.Button) + `</a></p>`)
		b.WriteString(`<p style="font-size:12px;color:#666;word-break:break-all">` + html.EscapeString(link) + `</p>`)
	}
	b.WriteString(`<hr><p style="font-size:11px;color:#999">Arena Platform</p></body></html>`)

	text := []string{t.Subject, "", body}
	if p.Code != "" {
		text = append(text, "", p.Code)
	}
	if t.Button != "" && link != "" {
		text = append(text, "", t.Button+": "+link)
	}
	text = append(text, "", "Arena Platform")
	return t.Subject, b.String(), strings.Join(text, "\n"), true
}
