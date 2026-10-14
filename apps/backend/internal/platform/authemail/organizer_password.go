package authemail

// organizer_password.go renders the password letters an ORGANIZER receives:
// "forgot password", "your account was created" and "you are invited to an
// organization". Their link lands on the web event center
// (EVENT_CENTER_PUBLIC_URL), and the letter explains every step in plain
// words (owner decision 2026-10-14, text standard docs/text_style_standard_ru.md).
// Letters of platform users keep the console wording in authemail.go.

import (
	"bytes"
	"fmt"
	"html"
	"math"
	"strings"
	"time"
)

// organizerLetter is one letter before it becomes HTML or text.
type organizerLetter struct {
	lang       string
	subject    string
	title      string
	intro      []string // paragraphs before the steps
	stepsIntro string
	steps      []string
	button     string
	fallback   string   // "if the button does not work" line
	after      []string // validity, expired link, not expecting it
	footer     []string
}

// organizerLetterText holds the words of one language.
type organizerLetterText struct {
	resetSubject, resetTitle                string
	resetIntro                              string // %s = e-mail
	resetSteps                              []string
	resetButton, resetIgnore                string
	inviteSubjectOrg, inviteSubject         string // %s = organization
	setupSubject                            string
	inviteTitle                             string
	inviteIntroOrg, inviteIntro, setupIntro string // %s = organization
	whatIs                                  string
	setupSteps                              []string // %s in the last step = e-mail
	setupButton, setupIgnore                string
	hello, stepsIntro, fallback             string
	validity                                string // %[1]s = duration, %[2]s = date
	expired                                 string // %[1]s = forgot page, %[2]s = e-mail
	address, auto                           string // address: %s = event center
	months                                  [12]string
	dateLayout                              string // %[1]d day, %[2]s month, %[3]d year, %[4]s time
	hours, days                             func(n int) string
}

var organizerTexts = map[string]organizerLetterText{
	"en": {
		resetSubject: "Arena: choose a new password",
		resetTitle:   "Choose a new password",
		resetIntro:   "We received a request for a new password for the Arena event center. The request is for %s.",
		resetSteps: []string{
			"Press the \u201cChoose a new password\u201d button below.",
			"Type the new password twice. Use at least 8 characters.",
			"Press \u201cSave password\u201d. Then sign in with the new password.",
		},
		resetButton:      "Choose a new password",
		resetIgnore:      "If you did not ask for a new password, do nothing. Your password stays the same.",
		inviteSubjectOrg: "%s invites you to the Arena event center",
		inviteSubject:    "You are invited to the Arena event center",
		setupSubject:     "Arena: your event center account is ready",
		inviteTitle:      "Welcome to the Arena event center",
		inviteIntroOrg:   "%s invites you to its team in the Arena event center.",
		inviteIntro:      "You are invited to an organization in the Arena event center.",
		setupIntro:       "We created an account for you in the Arena event center.",
		whatIs:           "In the event center you see your events, sales and orders.",
		setupSteps: []string{
			"Press the \u201cChoose a password\u201d button below.",
			"Think of a password with at least 8 characters. Type it twice.",
			"Press \u201cSave password\u201d.",
			"Sign in. Your e-mail is %s. Your password is the one you chose.",
		},
		setupButton: "Choose a password",
		setupIgnore: "If you were not expecting this letter, do nothing. Nobody can sign in without the password.",
		hello:       "Hello!",
		stepsIntro:  "What to do:",
		fallback:    "If the button does not work, copy this link into the address bar of your browser:",
		validity:    "The link works once. It is valid for %[1]s, until %[2]s.",
		expired:     "If the link no longer works, open %[1]s, type %[2]s and ask for a new letter.",
		address:     "The event center address: %s. Save it in your bookmarks.",
		auto:        "This letter was sent automatically. Please do not reply to it.",
		months: [12]string{"January", "February", "March", "April", "May", "June", "July",
			"August", "September", "October", "November", "December"},
		dateLayout: "%[2]s %[1]d, %[3]d, %[4]s UTC",
		hours:      func(n int) string { return pluralTwo(n, "hour", "hours") },
		days:       func(n int) string { return pluralTwo(n, "day", "days") },
	},
	"ru": {
		resetSubject: "Arena: новый пароль",
		resetTitle:   "Новый пароль",
		resetIntro:   "Мы получили запрос на новый пароль для ивент-центра Arena. Запрос пришёл для адреса %s.",
		resetSteps: []string{
			"Нажмите кнопку «Задать новый пароль» ниже.",
			"Введите новый пароль два раза. Пароль должен быть не короче 8 символов.",
			"Нажмите «Сохранить пароль». Затем войдите с новым паролем.",
		},
		resetButton:      "Задать новый пароль",
		resetIgnore:      "Если вы не просили новый пароль, ничего не делайте. Ваш пароль останется прежним.",
		inviteSubjectOrg: "%s приглашает вас в ивент-центр Arena",
		inviteSubject:    "Приглашение в ивент-центр Arena",
		setupSubject:     "Arena: ваш вход в ивент-центр готов",
		inviteTitle:      "Добро пожаловать в ивент-центр Arena",
		inviteIntroOrg:   "Организация «%s» приглашает вас в свою команду в ивент-центре Arena.",
		inviteIntro:      "Вас пригласили в организацию в ивент-центре Arena.",
		setupIntro:       "Мы создали для вас вход в ивент-центр Arena.",
		whatIs:           "В ивент-центре вы видите свои мероприятия, продажи и заказы.",
		setupSteps: []string{
			"Нажмите кнопку «Задать пароль» ниже.",
			"Придумайте пароль не короче 8 символов. Введите его два раза.",
			"Нажмите «Сохранить пароль».",
			"Войдите. Ваш адрес: %s. Пароль — тот, который вы задали.",
		},
		setupButton: "Задать пароль",
		setupIgnore: "Если вы не ждали этого письма, ничего не делайте. Без пароля никто не сможет войти.",
		hello:       "Здравствуйте!",
		stepsIntro:  "Что сделать:",
		fallback:    "Если кнопка не работает, скопируйте эту ссылку в адресную строку браузера:",
		validity:    "Ссылка работает один раз. Она действует %[1]s, до %[2]s.",
		expired:     "Если ссылка уже не работает, откройте %[1]s, введите %[2]s и запросите новое письмо.",
		address:     "Адрес ивент-центра: %s. Сохраните его в закладки.",
		auto:        "Письмо отправлено автоматически. Отвечать на него не нужно.",
		months: [12]string{"января", "февраля", "марта", "апреля", "мая", "июня", "июля",
			"августа", "сентября", "октября", "ноября", "декабря"},
		dateLayout: "%[1]d %[2]s %[3]d, %[4]s по UTC",
		hours:      func(n int) string { return plural(n, "час", "часа", "часов") },
		days:       func(n int) string { return plural(n, "день", "дня", "дней") },
	},
	"es": {
		resetSubject: "Arena: elige una contraseña nueva",
		resetTitle:   "Elige una contraseña nueva",
		resetIntro:   "Hemos recibido una solicitud de contraseña nueva para el centro de eventos de Arena. La solicitud es para %s.",
		resetSteps: []string{
			"Pulsa el botón «Elegir contraseña nueva» de abajo.",
			"Escribe la contraseña nueva dos veces. Usa al menos 8 caracteres.",
			"Pulsa «Guardar contraseña». Después entra con la contraseña nueva.",
		},
		resetButton:      "Elegir contraseña nueva",
		resetIgnore:      "Si no pediste una contraseña nueva, no hagas nada. Tu contraseña sigue igual.",
		inviteSubjectOrg: "%s te invita al centro de eventos de Arena",
		inviteSubject:    "Invitación al centro de eventos de Arena",
		setupSubject:     "Arena: tu acceso al centro de eventos está listo",
		inviteTitle:      "Bienvenido al centro de eventos de Arena",
		inviteIntroOrg:   "La organización «%s» te invita a su equipo en el centro de eventos de Arena.",
		inviteIntro:      "Te han invitado a una organización en el centro de eventos de Arena.",
		setupIntro:       "Hemos creado tu acceso al centro de eventos de Arena.",
		whatIs:           "En el centro de eventos ves tus eventos, ventas y pedidos.",
		setupSteps: []string{
			"Pulsa el botón «Elegir contraseña» de abajo.",
			"Piensa una contraseña de al menos 8 caracteres. Escríbela dos veces.",
			"Pulsa «Guardar contraseña».",
			"Entra. Tu correo es %s. Tu contraseña es la que elegiste.",
		},
		setupButton: "Elegir contraseña",
		setupIgnore: "Si no esperabas este correo, no hagas nada. Nadie puede entrar sin la contraseña.",
		hello:       "¡Hola!",
		stepsIntro:  "Qué hacer:",
		fallback:    "Si el botón no funciona, copia este enlace en la barra de direcciones del navegador:",
		validity:    "El enlace funciona una sola vez. Es válido durante %[1]s, hasta el %[2]s.",
		expired:     "Si el enlace ya no funciona, abre %[1]s, escribe %[2]s y pide un correo nuevo.",
		address:     "La dirección del centro de eventos: %s. Guárdala en tus marcadores.",
		auto:        "Este correo se envió automáticamente. No hace falta responder.",
		months: [12]string{"enero", "febrero", "marzo", "abril", "mayo", "junio", "julio",
			"agosto", "septiembre", "octubre", "noviembre", "diciembre"},
		dateLayout: "%[1]d de %[2]s de %[3]d, %[4]s UTC",
		hours:      func(n int) string { return pluralTwo(n, "hora", "horas") },
		days:       func(n int) string { return pluralTwo(n, "día", "días") },
	},
}

// plural picks the Russian form for n: one (1, 21), few (2-4, 22-24) or many.
func plural(n int, one, few, many string) string {
	m10, m100 := n%10, n%100
	word := many
	switch {
	case m10 == 1 && m100 != 11:
		word = one
	case m10 >= 2 && m10 <= 4 && (m100 < 12 || m100 > 14):
		word = few
	}
	return fmt.Sprintf("%d %s", n, word)
}

// pluralTwo is the English and Spanish rule: one for 1, other for the rest.
func pluralTwo(n int, one, other string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, other)
}

// linkDuration says how long the link still lives, rounded to whole hours or,
// from two days on, whole days. Never less than one hour.
func linkDuration(t organizerLetterText, expiresAt, now time.Time) string {
	hours := int(math.Round(expiresAt.Sub(now).Hours()))
	if hours >= 48 {
		return t.days(int(math.Round(float64(hours) / 24)))
	}
	if hours < 1 {
		hours = 1
	}
	return t.hours(hours)
}

// linkDate prints the end of the link in UTC: the letter does not know the
// reader's time zone, so it names the zone instead of guessing.
func linkDate(t organizerLetterText, at time.Time) string {
	at = at.UTC()
	clock := at.Format("15:04") // allow:timeformat
	return fmt.Sprintf(t.dateLayout, at.Day(), t.months[at.Month()-1], at.Year(), clock)
}

// organizerLocale folds a stored locale to one of the letter languages.
func organizerLocale(locale string) string {
	l := strings.ToLower(strings.TrimSpace(locale))
	if i := strings.IndexAny(l, "-_"); i > 0 {
		l = l[:i]
	}
	if _, ok := organizerTexts[l]; ok {
		return l
	}
	return "en"
}

// renderOrganizerPasswordEmail builds the organizer letter for purpose.
// base is the event center origin, link the full link with the token.
func renderOrganizerPasswordEmail(locale, purpose, orgName, email, base, link string, expiresAt, now time.Time) (subject, htmlBody, textBody string) {
	lang := organizerLocale(locale)
	t := organizerTexts[lang]
	orgName = strings.Join(strings.Fields(orgName), " ")
	forgot := base + "/forgot-password"

	l := organizerLetter{lang: lang, stepsIntro: t.stepsIntro, fallback: t.fallback}
	validity := fmt.Sprintf(t.validity, linkDuration(t, expiresAt, now), linkDate(t, expiresAt))
	expired := fmt.Sprintf(t.expired, forgot, email)

	switch purpose {
	case PurposeAccountSetup, PurposeOrgInvitation:
		switch {
		case purpose == PurposeOrgInvitation && orgName != "":
			l.subject = fmt.Sprintf(t.inviteSubjectOrg, orgName)
			l.intro = []string{t.hello, fmt.Sprintf(t.inviteIntroOrg, orgName), t.whatIs}
		case purpose == PurposeOrgInvitation:
			l.subject = t.inviteSubject
			l.intro = []string{t.hello, t.inviteIntro, t.whatIs}
		default:
			l.subject = t.setupSubject
			l.intro = []string{t.hello, t.setupIntro, t.whatIs}
		}
		l.title = t.inviteTitle
		l.steps = make([]string, len(t.setupSteps))
		copy(l.steps, t.setupSteps)
		l.steps[len(l.steps)-1] = fmt.Sprintf(l.steps[len(l.steps)-1], email)
		l.button = t.setupButton
		l.after = []string{validity, expired, t.setupIgnore}
	default:
		l.subject = t.resetSubject
		l.title = t.resetTitle
		l.intro = []string{t.hello, fmt.Sprintf(t.resetIntro, email)}
		l.steps = t.resetSteps
		l.button = t.resetButton
		l.after = []string{validity, expired, t.resetIgnore}
	}
	l.footer = []string{fmt.Sprintf(t.address, base), t.auto}
	return l.subject, l.html(link), l.text(link)
}

func (l organizerLetter) html(link string) string {
	esc := html.EscapeString
	var b bytes.Buffer
	fmt.Fprintf(&b, "<!DOCTYPE html>\n<html lang=\"%s\">\n<head><meta charset=\"UTF-8\"><title>%s</title></head>\n", l.lang, esc(l.title))
	b.WriteString(`<body style="font-family:sans-serif;max-width:600px;margin:0 auto;padding:20px;color:#1a1a2e;line-height:1.5">` + "\n")
	fmt.Fprintf(&b, "  <h1 style=\"color:#1a1a2e\">%s</h1>\n", esc(l.title))
	for _, p := range l.intro {
		fmt.Fprintf(&b, "  <p>%s</p>\n", esc(p))
	}
	fmt.Fprintf(&b, "  <p><strong>%s</strong></p>\n  <ol>\n", esc(l.stepsIntro))
	for _, s := range l.steps {
		fmt.Fprintf(&b, "    <li>%s</li>\n", esc(s))
	}
	b.WriteString("  </ol>\n")
	fmt.Fprintf(&b, `  <p style="margin:24px 0">
    <a href="%s"
       style="background:#4f46e5;color:#fff;padding:12px 24px;border-radius:6px;text-decoration:none;font-weight:bold;display:inline-block">
      %s
    </a>
  </p>
`, esc(link), esc(l.button))
	fmt.Fprintf(&b, "  <p style=\"font-size:13px;color:#555\">%s<br>\n    <span style=\"word-break:break-all\">%s</span>\n  </p>\n", esc(l.fallback), esc(link))
	for _, p := range l.after {
		fmt.Fprintf(&b, "  <p style=\"font-size:13px;color:#555\">%s</p>\n", esc(p))
	}
	b.WriteString("  <hr>\n")
	for _, p := range l.footer {
		fmt.Fprintf(&b, "  <p style=\"font-size:12px;color:#888\">%s</p>\n", esc(p))
	}
	b.WriteString("</body>\n</html>")
	return b.String()
}

func (l organizerLetter) text(link string) string {
	var b bytes.Buffer
	b.WriteString(l.title + "\n\n")
	for _, p := range l.intro {
		b.WriteString(p + "\n\n")
	}
	b.WriteString(l.stepsIntro + "\n")
	for i, s := range l.steps {
		fmt.Fprintf(&b, "%d. %s\n", i+1, s)
	}
	fmt.Fprintf(&b, "\n%s:\n%s\n\n", l.button, link)
	for _, p := range l.after {
		b.WriteString(p + "\n")
	}
	b.WriteString("\n--\n")
	for _, p := range l.footer {
		b.WriteString(p + "\n")
	}
	return b.String()
}
