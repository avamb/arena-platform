package authemail

// bot_invitation_web.go — the bot invitation also names the web event
// center (spec 08_architecture/37, owner decision 2026-10-14: links to the
// event center in the organizer's letters and in the bot). The person signs
// in there through the bot's "Open in browser" button, so the letter needs
// no password step. Without EVENT_CENTER_PUBLIC_URL the letter is unchanged.

import (
	"html"
	"strings"
)

// botInviteWebLine is the sentence about the web, per locale.
func botInviteWebLine(locale string) string {
	switch normalizeLocale(locale) {
	case "ru":
		return "Работать можно и в браузере, в веб-ивент-центре. Войдите туда кнопкой «🌐 Открыть в браузере» в боте. Адрес:"
	case "es":
		return "También puedes trabajar en el navegador, en el centro de eventos web. Entra con el botón «🌐 Abrir en el navegador» del bot. Dirección:"
	default:
		return "You can also work in the browser, in the web event center. Sign in there with the «🌐 Open in browser» button of the bot. Address:"
	}
}

// withEventCenterLine adds the web paragraph to a rendered bot invitation.
// It goes before the closing rule of the HTML body and before the footer of
// the text body.
func withEventCenterLine(locale, eventCenter, htmlBody, textBody string) (string, string) {
	if eventCenter == "" {
		return htmlBody, textBody
	}
	line := botInviteWebLine(locale)
	safe := html.EscapeString(eventCenter)
	para := `<p style="font-size:13px;color:#333">` + html.EscapeString(line) +
		` <a href="` + safe + `">` + safe + `</a></p>` + "\n  <hr>"
	htmlBody = strings.Replace(htmlBody, "<hr>", para, 1)

	lines := strings.Split(textBody, "\n")
	if n := len(lines); n >= 2 {
		tail := append([]string{line, eventCenter, ""}, lines[n-2:]...)
		lines = append(lines[:n-2], tail...)
	}
	return htmlBody, strings.Join(lines, "\n")
}
