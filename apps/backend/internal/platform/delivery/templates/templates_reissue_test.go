package templates

import (
	"html"
	"strings"
	"testing"
)

// reissueWords spells out, per locale, the subject prefix, a sentence of the
// typo text, a sentence of the "at your request" text and the closing
// sentence of the replacement letter (spec 63, decision R-19).
var reissueWords = map[string][4]string{
	"en": {"Your new tickets — ", "There was a typo in the e-mail address given at purchase", "At your request we have issued new tickets", "Your seats and payment have not changed."},
	"ru": {"Ваши новые билеты — ", "При покупке в адресе электронной почты была опечатка", "По вашей просьбе мы выпустили новые билеты", "Места и оплата не изменились."},
	"cs": {"Vaše nové vstupenky — ", "Při nákupu byl v e-mailové adrese překlep", "Na vaši žádost jsme vystavili nové vstupenky", "Místa a platba se nezměnily."},
	"de": {"Ihre neuen Tickets — ", "Beim Kauf enthielt die E-Mail-Adresse einen Tippfehler", "Auf Ihren Wunsch haben wir neue Tickets", "Plätze und Zahlung bleiben unverändert."},
	"es": {"Tus nuevas entradas — ", "Al comprar, la dirección de correo electrónico tenía un error tipográfico", "A petición tuya, hemos emitido nuevas entradas", "Tus asientos y tu pago no han cambiado."},
	"fr": {"Vos nouveaux billets — ", "Lors de l'achat, l'adresse e-mail contenait une faute de frappe", "À votre demande, nous avons émis de nouveaux billets", "Vos places et votre paiement n'ont pas changé."},
	"he": {"הכרטיסים החדשים שלך — ", "בעת הרכישה הייתה שגיאת הקלדה בכתובת הדוא״ל", "לבקשתך הנפקנו כרטיסים חדשים", "המקומות והתשלום לא השתנו."},
}

func TestReissueWords_CoverEverySupportedLocale(t *testing.T) {
	for _, loc := range SupportedLocales {
		if _, ok := reissueWords[loc]; !ok {
			t.Errorf("no replacement letter words for supported locale %q", loc)
		}
	}
}

// A ticket letter that carries Data.Reissue explains why new tickets came, in
// every language and in both bodies; an ordinary letter never does.
func TestRender_ReissueLetter(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	for _, loc := range SupportedLocales {
		w := reissueWords[loc]
		for _, typo := range []bool{true, false} {
			data := orderData()
			data.Reissue = &ReissueData{Typo: typo, OrderNumber: "4711"}
			out, err := r.Render(TemplateKindTicket, loc, data)
			if err != nil {
				t.Fatalf("%s: %v", loc, err)
			}
			subject := html.UnescapeString(out.Subject)
			if !strings.HasPrefix(subject, w[0]) || !strings.Contains(subject, data.EventName) {
				t.Errorf("%s: subject %q", loc, subject)
			}
			want, notWant := w[1], w[2]
			if !typo {
				want, notWant = w[2], w[1]
			}
			for part, body := range map[string]string{"html": out.HTMLBody, "text": out.TextBody} {
				body = html.UnescapeString(body)
				for _, s := range []string{want, w[3], "4711"} {
					if !strings.Contains(body, s) {
						t.Errorf("%s %s typo=%v: %q missing", loc, part, typo, s)
					}
				}
				if strings.Contains(body, notWant) {
					t.Errorf("%s %s typo=%v: the other text was printed", loc, part, typo)
				}
			}
		}

		out, err := r.Render(TemplateKindTicket, loc, orderData())
		if err != nil {
			t.Fatalf("%s: %v", loc, err)
		}
		all := html.UnescapeString(out.Subject + out.HTMLBody + out.TextBody)
		for _, s := range w {
			if strings.Contains(all, s) {
				t.Errorf("%s: ordinary letter carries the replacement text %q", loc, s)
			}
		}
	}
}

// invitationReissueSubjects is the subject prefix of a replaced invitation.
var invitationReissueSubjects = map[string]string{
	"en": "Your new invitation — ",
	"ru": "Ваше новое приглашение — ",
	"cs": "Vaše nová pozvánka — ",
	"de": "Ihre neue Einladung — ",
	"es": "Tu nueva invitación — ",
	"fr": "Votre nouvelle invitation — ",
	"he": "ההזמנה החדשה שלך — ",
}

// A replaced invitation explains itself in every language and both bodies;
// an ordinary invitation is byte for byte what it was.
func TestRender_ReissueInvitation(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	for _, loc := range SupportedLocales {
		prefix := invitationReissueSubjects[loc]
		if prefix == "" {
			t.Fatalf("no invitation reissue subject for %q", loc)
		}
		plain, err := r.Render(TemplateKindInvitation, loc, orderData())
		if err != nil {
			t.Fatalf("%s: %v", loc, err)
		}
		typo := orderData()
		typo.Reissue = &ReissueData{Typo: true}
		a, err := r.Render(TemplateKindInvitation, loc, typo)
		if err != nil {
			t.Fatalf("%s: %v", loc, err)
		}
		req := orderData()
		req.Reissue = &ReissueData{}
		b, err := r.Render(TemplateKindInvitation, loc, req)
		if err != nil {
			t.Fatalf("%s: %v", loc, err)
		}
		if !strings.HasPrefix(html.UnescapeString(a.Subject), prefix) || strings.HasPrefix(html.UnescapeString(plain.Subject), prefix) {
			t.Errorf("%s: subjects %q / %q", loc, a.Subject, plain.Subject)
		}
		for part, pair := range map[string][3]string{
			"html": {plain.HTMLBody, a.HTMLBody, b.HTMLBody},
			"text": {plain.TextBody, a.TextBody, b.TextBody},
		} {
			if len(pair[1]) <= len(pair[0]) || len(pair[2]) <= len(pair[0]) || pair[1] == pair[2] {
				t.Errorf("%s %s: the replacement block is missing or does not depend on the reason", loc, part)
			}
		}
	}
}
