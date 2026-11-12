package authemail

import (
	"html"
	"strings"
)

const adminNewApplicationAction = "Что делать: откройте заявку в админке, проверьте данные и одобрите её, отклоните или запросите уточнения."

// renderAdminNewApplication renders the letter to a platform admin about a new
// or re-submitted application. The text is Russian and lives here, not in the
// i18n catalogs: only platform staff get it.
func renderAdminNewApplication(p OnboardingEmailPayload, link string) (subject, htmlBody, textBody string, ok bool) {
	title := strings.TrimSpace(p.Title)
	if title == "" {
		title = "Новая заявка на онбординг"
	}
	org := strings.Join(strings.Fields(p.OrgName), " ")
	subject = title
	if org != "" {
		subject += ": " + org
	}

	var b strings.Builder
	b.WriteString(`<!DOCTYPE html><html lang="ru"><head><meta charset="UTF-8"><title>Arena</title></head>`)
	b.WriteString(`<body style="font-family:sans-serif;max-width:600px;margin:0 auto;padding:20px;color:#1a1a2e">`)
	b.WriteString(`<h1 style="color:#1a1a2e;font-size:20px">` + html.EscapeString(title) + `</h1><p style="line-height:1.5">`)
	for i, d := range p.Details {
		if i > 0 {
			b.WriteString("<br>")
		}
		b.WriteString(html.EscapeString(d))
	}
	b.WriteString(`</p><p style="line-height:1.5">` + html.EscapeString(adminNewApplicationAction) + `</p>`)
	if link != "" {
		b.WriteString(`<p style="margin:24px 0"><a href="` + html.EscapeString(link) + `" style="background:#4f46e5;color:#fff;padding:12px 24px;border-radius:4px;text-decoration:none;font-weight:bold;display:inline-block">Открыть заявку</a></p>`)
		b.WriteString(`<p style="font-size:12px;color:#666;word-break:break-all">` + html.EscapeString(link) + `</p>`)
	}
	b.WriteString(`<hr><p style="font-size:11px;color:#999">Arena Platform</p></body></html>`)

	text := []string{title, ""}
	text = append(text, p.Details...)
	text = append(text, "", adminNewApplicationAction)
	if link != "" {
		text = append(text, "", "Открыть заявку: "+link)
	}
	text = append(text, "", "Arena Platform")
	return subject, b.String(), strings.Join(text, "\n"), true
}
