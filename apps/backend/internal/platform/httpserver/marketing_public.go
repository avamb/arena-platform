package httpserver

// marketing_public.go — the two links of the marketing consent (spec 51-1):
//
//   - GET /v1/public/marketing/confirm?token= confirms the address (double
//     opt-in) and 303s to the page the buyer checked out on, with
//     arena_marketing=confirmed (or =invalid).
//   - GET /v1/public/marketing/withdraw?token= shows a one-button page;
//     POST to the same address withdraws the consent. A GET never withdraws:
//     mail scanners open every link of a letter by themselves. The form posts
//     multipart/form-data, which the JSON content-type guard lets through.
//
// No JWT: the token is the credential. The withdraw token is a withdraw-only
// capability; the page names the organization, never the address.

import (
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/marketing"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/salesubscribe"
)

func (s *Server) marketingVisit(r *http.Request) marketing.Visit {
	return marketing.Visit{ClientIP: httputil.TrustedClientIP(r, 0), UserAgent: r.UserAgent()}
}

func (s *Server) handleMarketingConfirm(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if s.pgxPool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "The service is temporarily unavailable. Try again in a minute.", r))
		return
	}
	fallback := strings.TrimRight(strings.TrimSpace(s.cfg.PublicTicketsBaseURL), "/")
	l, err := marketing.Confirm(r.Context(), s.pgxPool, r.URL.Query().Get("token"), s.marketingVisit(r))
	if err != nil {
		if !errors.Is(err, marketing.ErrTokenInvalid) {
			s.logger.Error("marketing: confirm failed", slog.String("error", err.Error()))
		}
		if fallback == "" {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"marketing.link_invalid", "This link was already used or has expired. Tick the box again on your next order.", r))
			return
		}
		http.Redirect(w, r, marketingLanding(fallback, "invalid"), http.StatusSeeOther) //nolint:gosec // G710: the configured tickets page
		return
	}
	target := salesubscribe.CleanPageURL(l.PageURL)
	if target == "" {
		target = fallback
	}
	if target == "" {
		writeMarketingPage(w, l.Locale, "confirmed", l.OrgName, "")
		return
	}
	// The page was checked by the return-URL policy when the checkout began.
	http.Redirect(w, r, marketingLanding(target, "confirmed"), http.StatusSeeOther) //nolint:gosec // G710: validated at checkout
}

func marketingLanding(raw, value string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	q := u.Query()
	q.Set("arena_marketing", value)
	u.RawQuery = q.Encode()
	return u.String()
}

func (s *Server) handleMarketingWithdrawPage(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if s.pgxPool == nil {
		writeMarketingPage(w, "", "invalid", "", "")
		return
	}
	u, err := marketing.LookupUnsubscribe(r.Context(), s.pgxPool, token)
	if err != nil {
		if !errors.Is(err, marketing.ErrTokenInvalid) {
			s.logger.Error("marketing: withdraw lookup failed", slog.String("error", err.Error()))
		}
		writeMarketingPage(w, r.URL.Query().Get("lang"), "invalid", "", "")
		return
	}
	if u.Withdrawn {
		writeMarketingPage(w, u.Locale, "done", u.OrgName, "")
		return
	}
	writeMarketingPage(w, u.Locale, "ask", u.OrgName, "?token="+url.QueryEscape(strings.TrimSpace(token)))
}

func (s *Server) handleMarketingWithdraw(w http.ResponseWriter, r *http.Request) {
	if s.pgxPool == nil {
		writeMarketingPage(w, "", "invalid", "", "")
		return
	}
	u, err := marketing.Withdraw(r.Context(), s.pgxPool, r.URL.Query().Get("token"), s.marketingVisit(r))
	if err != nil {
		if !errors.Is(err, marketing.ErrTokenInvalid) {
			s.logger.Error("marketing: withdraw failed", slog.String("error", err.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"marketing.withdraw_failed", "We could not stop the mailings. Try again in a minute.", r))
			return
		}
		writeMarketingPage(w, "", "invalid", "", "")
		return
	}
	writeMarketingPage(w, u.Locale, "done", u.OrgName, "")
}

// marketingPageTexts: title, "ask" text, button, "done" text, "invalid"
// text, "confirmed" text. %s is the organization.
var marketingPageTexts = map[string][6]string{
	"en": {"Mailings", "Stop the news and offers from %s?", "Stop the mailings",
		"You will no longer get news and offers from %s. Letters about your orders and tickets still arrive.",
		"This link is not valid. Copy it from the letter again.", "Thank you! The subscription to %s is confirmed."},
	"ru": {"Рассылка", "Больше не получать новости и предложения от %s?", "Отказаться от рассылки",
		"Вы больше не будете получать новости и предложения от %s. Письма о заказах и билеты по-прежнему придут.",
		"Ссылка не работает. Скопируйте её из письма ещё раз.", "Спасибо! Подписка на %s подтверждена."},
	"es": {"Correos", "¿Dejar de recibir novedades y ofertas de %s?", "Dejar de recibir correos",
		"Ya no recibirás novedades ni ofertas de %s. Los correos sobre tus pedidos y las entradas seguirán llegando.",
		"Este enlace no es válido. Cópialo otra vez del correo.", "¡Gracias! Tu suscripción a %s está confirmada."},
	"cs": {"Novinky", "Už nedostávat novinky a nabídky od %s?", "Ukončit zasílání novinek",
		"Novinky a nabídky od %s už vám nepřijdou. E-maily o objednávkách a vstupenky dál chodí.",
		"Tento odkaz neplatí. Zkopírujte ho z e-mailu znovu.", "Děkujeme! Odběr novinek %s je potvrzen."},
	"de": {"Mailings", "Keine Neuigkeiten und Angebote mehr von %s erhalten?", "Mailings beenden",
		"Sie erhalten keine Neuigkeiten und Angebote mehr von %s. E-Mails zu Bestellungen und Tickets kommen weiterhin.",
		"Dieser Link ist ungültig. Kopieren Sie ihn noch einmal aus der E-Mail.", "Danke! Ihr Abonnement bei %s ist bestätigt."},
	"fr": {"Envois", "Ne plus recevoir les nouvelles et offres de %s ?", "Arrêter les envois",
		"Vous ne recevrez plus les nouvelles et offres de %s. Les e-mails sur vos commandes et vos billets continuent d’arriver.",
		"Ce lien n’est pas valide. Copiez-le à nouveau depuis l’e-mail.", "Merci ! Votre abonnement à %s est confirmé."},
	"he": {"דיוור", "להפסיק לקבל עדכונים והצעות מ־%s?", "להפסקת הדיוור",
		"לא תקבלו יותר עדכונים והצעות מ־%s. מיילים על הזמנות וכרטיסים ימשיכו להגיע.",
		"הקישור לא תקף. העתיקו אותו שוב מהמייל.", "תודה! ההרשמה ל־%s אושרה."},
}

func marketingPageLang(raw string) string {
	l := strings.ToLower(strings.TrimSpace(raw))
	if i := strings.IndexAny(l, "-_"); i > 0 {
		l = l[:i]
	}
	if _, ok := marketingPageTexts[l]; ok {
		return l
	}
	return "en"
}

// writeMarketingPage renders the small page of the withdraw link. state is
// "ask", "done", "invalid" or "confirmed"; action is the form's query string.
func writeMarketingPage(w http.ResponseWriter, locale, state, orgName, action string) {
	lang := marketingPageLang(locale)
	t := marketingPageTexts[lang]
	org := html.EscapeString(orgName)
	var body string
	switch state {
	case "ask":
		body = fmt.Sprintf(`<p>%s</p><form method="post" enctype="multipart/form-data" action="%s"><button type="submit">%s</button></form>`,
			fmt.Sprintf(html.EscapeString(t[1]), org), html.EscapeString(action), html.EscapeString(t[2]))
	case "done":
		body = "<p>" + fmt.Sprintf(html.EscapeString(t[3]), org) + "</p>"
	case "confirmed":
		body = "<p>" + fmt.Sprintf(html.EscapeString(t[5]), org) + "</p>"
	default:
		body = "<p>" + html.EscapeString(t[4]) + "</p>"
	}
	dir := "ltr"
	if lang == "he" {
		dir = "rtl"
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'")
	_, _ = fmt.Fprintf(w, `<!DOCTYPE html><html lang="%s" dir="%s"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>%s</title>`+
		`<style>body{font-family:Arial,Helvetica,sans-serif;background:#f2f4f5;color:#22333D;margin:0;padding:48px 16px}main{max-width:480px;margin:0 auto;background:#fff;border-radius:8px;padding:28px}button{background:#4f46e5;color:#fff;border:0;border-radius:6px;padding:12px 22px;font-size:15px;cursor:pointer}</style>`+
		`</head><body><main><h1>%s</h1>%s</main></body></html>`, lang, dir, html.EscapeString(t[0]), html.EscapeString(t[0]), body)
}
