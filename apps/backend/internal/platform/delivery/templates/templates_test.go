package templates

import (
	"reflect"
	"strings"
	"testing"
)

func TestNew_ParsesAllEmbeddedFiles(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got := r.KnownTemplates()
	want := []string{
		"invitation.de", "invitation.en", "invitation.es", "invitation.he",
		"ticket.de", "ticket.en", "ticket.es", "ticket.he",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("KnownTemplates mismatch:\n got=%v\nwant=%v", got, want)
	}
}

func TestSupportedLocalesMatchEmbeddedFiles(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	for _, loc := range SupportedLocales {
		for _, kind := range []string{TemplateKindTicket, TemplateKindInvitation} {
			if r.ResolveLocale(kind, loc) != loc {
				t.Errorf("ResolveLocale(%q,%q): expected %q to be present",
					kind, loc, loc)
			}
		}
	}
}

func TestResolveLocale_FallbacksToEnglish(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	cases := []string{"", "fr", "klingon", "EN-US", "  ", "ZH-Hant-TW"}
	for _, in := range cases {
		got := r.ResolveLocale(TemplateKindTicket, in)
		// "EN-US" should normalize to "en" — a hit, not a fallback.
		if strings.EqualFold(strings.SplitN(in, "-", 2)[0], "en") {
			if got != "en" {
				t.Errorf("ResolveLocale(%q)=%q want en", in, got)
			}
			continue
		}
		if got != DefaultLocale {
			t.Errorf("ResolveLocale(%q)=%q want %q", in, got, DefaultLocale)
		}
	}
}

func TestRender_AllLocalesEmitNonEmptyOutput(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	data := Data{
		TicketID:       "11111111-2222-3333-4444-555555555555",
		RecipientEmail: "fan@example.com",
		HolderName:     "Lena",
		EventName:      "Symphonic Night",
		SessionStart:   "2026-09-12 20:00 (Europe/Berlin)",
		VenueName:      "Philharmonie Berlin",
		TierName:       "Parkett A",
	}
	for _, kind := range []string{TemplateKindTicket, TemplateKindInvitation} {
		for _, loc := range SupportedLocales {
			out, err := r.Render(kind, loc, data)
			if err != nil {
				t.Fatalf("Render(%s,%s): %v", kind, loc, err)
			}
			if out.Subject == "" {
				t.Errorf("%s/%s: empty subject", kind, loc)
			}
			if !strings.Contains(out.HTMLBody, data.EventName) {
				t.Errorf("%s/%s: HTMLBody missing event name", kind, loc)
			}
			if !strings.Contains(out.TextBody, data.EventName) {
				t.Errorf("%s/%s: TextBody missing event name", kind, loc)
			}
			if !strings.Contains(out.TextBody, data.TicketID) {
				t.Errorf("%s/%s: TextBody missing ticket id", kind, loc)
			}
			// HTML body must declare the locale on <html lang="..">
			if !strings.Contains(out.HTMLBody, `lang="`+loc+`"`) {
				t.Errorf("%s/%s: HTMLBody missing lang attribute", kind, loc)
			}
		}
	}
}

func TestRender_HebrewIsRTL(t *testing.T) {
	r, _ := New()
	for _, kind := range []string{TemplateKindTicket, TemplateKindInvitation} {
		out, err := r.Render(kind, "he", Data{
			TicketID: "t", EventName: "Concert", RecipientEmail: "a@b.c",
		})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.HTMLBody, `dir="rtl"`) {
			t.Errorf("%s/he: expected dir=\"rtl\" in HTML body", kind)
		}
	}
}

func TestRender_HTMLEscapingAppliesToData(t *testing.T) {
	r, _ := New()
	out, err := r.Render(TemplateKindTicket, "en", Data{
		TicketID: "t1", EventName: "<script>alert(1)</script>",
		RecipientEmail: "x@y.z",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.HTMLBody, "<script>alert(1)</script>") {
		t.Errorf("HTMLBody not escaped: %q", out.HTMLBody)
	}
	if !strings.Contains(out.HTMLBody, "&lt;script&gt;") {
		t.Errorf("HTMLBody missing escaped form: %q", out.HTMLBody)
	}
	// Text body intentionally NOT escaped — it stays literal for the
	// plain-text MIME part.
	if !strings.Contains(out.TextBody, "<script>alert(1)</script>") {
		t.Errorf("TextBody should preserve literal text: %q", out.TextBody)
	}
}

func TestRender_UnknownKindErrors(t *testing.T) {
	r, _ := New()
	_, err := r.Render("nope", "en", Data{TicketID: "t", EventName: "E", RecipientEmail: "x"})
	if err == nil {
		t.Fatal("expected error for unknown kind")
	}
}

func TestRender_OptionalFieldsOmitted(t *testing.T) {
	r, _ := New()
	out, err := r.Render(TemplateKindTicket, "en", Data{
		TicketID: "id-1", EventName: "Show", RecipientEmail: "x@y.z",
	})
	if err != nil {
		t.Fatal(err)
	}
	// With no HolderName the greeting must NOT contain a stray dangling
	// name space — the {{with}} guard suppresses the leading space.
	if strings.Contains(out.TextBody, "Hello ,") {
		t.Errorf("greeting did not collapse empty name: %q", out.TextBody)
	}
	// Optional rows should be absent when their inputs are empty.
	if strings.Contains(out.HTMLBody, "Session:") || strings.Contains(out.HTMLBody, "Venue:") {
		t.Errorf("optional rows leaked when inputs empty: %q", out.HTMLBody)
	}
}

func TestRender_LocaleFallbackProducesEnglish(t *testing.T) {
	r, _ := New()
	out, err := r.Render(TemplateKindTicket, "klingon", Data{
		TicketID: "id", EventName: "E", RecipientEmail: "x@y.z",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.HTMLBody, `lang="en"`) {
		t.Errorf("fallback did not produce English: %q", out.HTMLBody)
	}
}

func TestNormalize(t *testing.T) {
	cases := map[string]string{
		"":          "",
		"en":        "en",
		"EN":        "en",
		"  En  ":    "en",
		"en-US":     "en",
		"zh-Hant":   "zh",
		"HE":        "he",
		"de-DE-x-y": "de",
	}
	for in, want := range cases {
		if got := normalize(in); got != want {
			t.Errorf("normalize(%q)=%q want %q", in, got, want)
		}
	}
}
