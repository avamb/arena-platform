package templates

import (
	"strings"
	"testing"
)

func referralBuyerData() Data {
	return Data{
		RecipientEmail: "ref@example.com",
		HolderName:     "Ana",
		EventName:      "Gala",
		Branding:       Branding{OrgName: "Org", LegalName: "Org SL", ContactEmail: "info@org.example"},
		Accent:         DefaultAccentColor,
		Referral:       &ReferralData{Code: "ANA-7K2P", Amount: "5.00 EUR", Order: "1042"},
	}
}

func referralOrganizerData(reversal string, paused bool) Data {
	return Data{
		EventName: "Gala",
		Branding:  Branding{OrgName: "Org"},
		Referral:  &ReferralData{Code: "ANA-7K2P", Amount: "5.00 EUR", Order: "1043", Reversal: reversal, CodePaused: paused},
	}
}

// noTicketWord is the stem each language uses for a ticket. The cashback
// letters speak of a card and an order, never of a ticket: the referrer may
// have bought anything, and the only place the word may stand is the
// platform's own unsubscribe note.
var noTicketWord = map[string][]string{
	"en": {"ticket"},
	"ru": {"билет"},
	"cs": {"vstupenk"},
	"es": {"entrada"},
	"de": {"ticket"},
	"fr": {"billet"},
	"he": {"כרטיסים"}, // the singular also means a payment card
}

func TestReferralBuyerLetters_RenderInEveryLocaleWithTheirFacts(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{TemplateKindReferralCashbackSent, TemplateKindReferralCashbackUnavailable} {
		for _, loc := range SupportedLocales {
			out, err := r.Render(kind, loc, referralBuyerData())
			if err != nil {
				t.Fatalf("%s/%s: %v", kind, loc, err)
			}
			if r.ResolveLocale(kind, loc) != loc {
				t.Errorf("%s/%s: no template for the locale", kind, loc)
			}
			for _, want := range []string{"ANA-7K2P", "5.00 EUR", "1042"} {
				if !strings.Contains(out.HTMLBody, want) || !strings.Contains(out.TextBody, want) {
					t.Errorf("%s/%s: %q missing from a part", kind, loc, want)
				}
			}
			if strings.TrimSpace(out.Subject) == "" {
				t.Errorf("%s/%s: empty subject", kind, loc)
			}
			all := out.Subject + "\n" + out.HTMLBody + "\n" + out.TextBody
			if strings.Contains(all, "<no value>") || strings.Contains(all, "{{") {
				t.Errorf("%s/%s: unrendered value", kind, loc)
			}
			// No ticket word outside the unsubscribe note.
			body := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(all, unsubscribeNotes[loc], ""), "&#39;", "'"))
			for _, w := range noTicketWord[loc] {
				if strings.Contains(body, w) {
					t.Errorf("%s/%s: the letter speaks of a ticket (%q)", kind, loc, w)
				}
			}
			// The title is the h1 and the part says it in the text part too.
			if strings.Count(out.HTMLBody, "<h1") != 1 {
				t.Errorf("%s/%s: want exactly one heading", kind, loc)
			}
		}
	}
}

// The "sent" letter promises the bank delay and nothing else.
func TestReferralCashbackSent_StatesTheBankDelayAndPromisesNothingElse(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	delays := map[string]string{
		"en": "5–10 business days", "ru": "5–10 рабочих дней", "cs": "5–10 pracovních dnů",
		"es": "5 a 10 días hábiles", "de": "5–10 Werktage", "fr": "5 à 10 jours ouvrables", "he": "5 עד 10 ימי עסקים",
	}
	for _, loc := range SupportedLocales {
		out, err := r.Render(TemplateKindReferralCashbackSent, loc, referralBuyerData())
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.TextBody, delays[loc]) {
			t.Errorf("%s: the bank delay %q is missing:\n%s", loc, delays[loc], out.TextBody)
		}
	}
}

// The "unavailable" letter sends the referrer to the organizer by the ticket
// letters' contact rule: the public e-mail, else the page's contact form, and
// never a private address.
func TestReferralCashbackUnavailable_PointsAtTheOrganizerContact(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	for _, loc := range SupportedLocales {
		d := referralBuyerData()
		out, err := r.Render(TemplateKindReferralCashbackUnavailable, loc, d)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Count(out.TextBody, "info@org.example") < 2 || !strings.Contains(out.HTMLBody, `href="mailto:info@org.example"`) {
			t.Errorf("%s: the public e-mail is not offered in the body and the footer", loc)
		}

		d.Branding.ContactEmail = ""
		d.Branding.ContactFormURL = "https://tickets.example.com/org#contact"
		form, err := r.Render(TemplateKindReferralCashbackUnavailable, loc, d)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Count(form.TextBody, d.Branding.ContactFormURL) < 2 || strings.Count(form.HTMLBody, `href="`+d.Branding.ContactFormURL+`"`) < 2 {
			t.Errorf("%s: the contact form link is not offered in the body and the footer", loc)
		}

		d.Branding.ContactFormURL = ""
		none, err := r.Render(TemplateKindReferralCashbackUnavailable, loc, d)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(none.HTMLBody, "mailto:") || strings.Contains(none.HTMLBody, "#contact") {
			t.Errorf("%s: a contact is printed although the letter has none", loc)
		}
	}
	// The sent letter has no contact paragraph of its own (the footer has it).
	sent, _ := r.Render(TemplateKindReferralCashbackSent, "en", referralBuyerData())
	if strings.Count(sent.TextBody, "info@org.example") != 1 {
		t.Errorf("the sent letter repeats the contact:\n%s", sent.TextBody)
	}
}

func TestReferralReversalOrganizer_SaysWhatHappenedAndThatTheCashbackStays(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	for _, loc := range SupportedLocales {
		subjects := map[string]string{}
		for _, reversal := range []string{ReferralReversalRefunded, ReferralReversalDisputed} {
			for _, paused := range []bool{false, true} {
				out, err := r.Render(TemplateKindReferralReversalOrganizer, loc, referralOrganizerData(reversal, paused))
				if err != nil {
					t.Fatalf("%s/%s: %v", loc, reversal, err)
				}
				all := out.Subject + "\n" + out.HTMLBody + "\n" + out.TextBody
				if strings.Contains(all, "<no value>") || strings.Contains(all, "{{") {
					t.Errorf("%s/%s: unrendered value", loc, reversal)
				}
				for _, want := range []string{"ANA-7K2P", "5.00 EUR", "1043"} {
					if !strings.Contains(out.TextBody, want) || !strings.Contains(out.HTMLBody, want) {
						t.Errorf("%s/%s: %q missing from a part", loc, reversal, want)
					}
				}
				if !strings.Contains(out.Subject, "1043") {
					t.Errorf("%s/%s: the subject lacks the order number: %q", loc, reversal, out.Subject)
				}
				// The pause sentence is the only text that differs by paused.
				pausedHere := strings.Count(out.TextBody, "ANA-7K2P") == 2
				if pausedHere != paused {
					t.Errorf("%s/%s paused=%v: the code appears %d times in the text part", loc, reversal, paused, strings.Count(out.TextBody, "ANA-7K2P"))
				}
				if !paused {
					subjects[reversal] = out.Subject
				}
				// An organizer letter carries no buyer unsubscribe note and no buyer data.
				if strings.Contains(all, unsubscribeNotes[loc]) {
					t.Errorf("%s/%s: the organizer letter carries the buyer note", loc, reversal)
				}
			}
		}
		if subjects[ReferralReversalRefunded] == subjects[ReferralReversalDisputed] {
			t.Errorf("%s: a refund and a dispute share one subject %q", loc, subjects[ReferralReversalRefunded])
		}
	}
}

func TestReferralReversalOrganizer_RTLLocaleIsRightToLeft(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{TemplateKindReferralCashbackSent, TemplateKindReferralCashbackUnavailable, TemplateKindReferralReversalOrganizer} {
		d := referralBuyerData()
		if kind == TemplateKindReferralReversalOrganizer {
			d = referralOrganizerData(ReferralReversalRefunded, false)
		}
		out, err := r.Render(kind, "he", d)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.HTMLBody, `<html lang="he" dir="rtl">`) {
			t.Errorf("%s/he: the page is not marked right-to-left", kind)
		}
	}
}
