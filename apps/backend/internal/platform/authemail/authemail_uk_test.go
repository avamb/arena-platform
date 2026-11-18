package authemail

import (
	"strings"
	"testing"
	"time"
)

func TestUkrainianAccountLetters(t *testing.T) {
	exp := time.Date(2026, 12, 1, 10, 0, 0, 0, time.UTC)
	const link = "https://app.example.test/x?token=abc&email=a%40b.test"

	type letter struct{ subject, html, text string }
	cases := map[string]letter{}
	s, h, x := renderVerificationEmail("uk", link, exp)
	cases["verify"] = letter{s, h, x}
	s, h, x = renderPasswordResetEmail("uk", link, exp)
	cases["reset"] = letter{s, h, x}
	s, h, x = renderAccountSetupEmail("uk", PurposeAccountSetup, "", link, exp)
	cases["setup"] = letter{s, h, x}
	s, h, x = renderAccountSetupEmail("uk", PurposeOrgInvitation, "Vino & Co", link, exp)
	cases["invite"] = letter{s, h, x}

	for name, l := range cases {
		if !strings.Contains(l.html, `lang="uk"`) || !strings.Contains(l.html, "&amp;email=") {
			t.Errorf("%s: html is not uk or link not escaped: %s", name, l.html)
		}
		if !strings.Contains(l.text, link) || !strings.Contains(l.text, "автоматичне повідомлення") {
			t.Errorf("%s: text wrong: %s", name, l.text)
		}
		if strings.ContainsAny(l.subject+l.text, "ыэёъ") {
			t.Errorf("%s: Russian letters in Ukrainian copy", name)
		}
		if strings.Contains(l.subject, "ArenaSoldOut.com") == false {
			t.Errorf("%s: brand missing in subject %q", name, l.subject)
		}
	}
	if !strings.Contains(cases["invite"].subject, "Vino & Co") {
		t.Errorf("invite subject lost org name: %q", cases["invite"].subject)
	}
	// Other locales still get English.
	if s, _, _ := renderPasswordResetEmail("ru", link, exp); !strings.HasPrefix(s, "Reset") {
		t.Errorf("non-uk reset must stay English, got %q", s)
	}
}
