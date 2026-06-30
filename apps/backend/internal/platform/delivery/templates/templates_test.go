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
		Branding: Branding{
			OrgName:      PlatformOrgName,
			LogoURL:      PlatformLogoURL,
			LogoAlt:      PlatformOrgName,
			LegalName:    PlatformLegalName,
			ContactEmail: PlatformContactEmail,
		},
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

// ──────────────────────────────────────────────────────────────────────────────
// Feature #290 (T-3) Branding hooks on ticket emails
// ──────────────────────────────────────────────────────────────────────────────

func TestRender_Branding_HeaderUsesOrgFields(t *testing.T) {
	r, _ := New()
	data := Data{
		TicketID: "id", EventName: "E", RecipientEmail: "x@y.z",
		Branding: Branding{
			OrgName:    "Globe Theatre",
			WebsiteURL: "https://globe.example.com",
			LogoURL:    "https://media.example.com/o/abc/logo.png?sig=xyz",
			LogoAlt:    "Globe Theatre",
			LegalName:  "Globe Theatre Ltd",
		},
	}
	for _, kind := range []string{TemplateKindTicket, TemplateKindInvitation} {
		for _, loc := range SupportedLocales {
			out, err := r.Render(kind, loc, data)
			if err != nil {
				t.Fatalf("%s/%s: %v", kind, loc, err)
			}
			if !strings.Contains(out.HTMLBody, "Globe Theatre") {
				t.Errorf("%s/%s: header missing OrgName", kind, loc)
			}
			if !strings.Contains(out.HTMLBody, "https://globe.example.com") {
				t.Errorf("%s/%s: header missing WebsiteURL", kind, loc)
			}
			if !strings.Contains(out.HTMLBody, `src="https://media.example.com/o/abc/logo.png?sig=xyz"`) {
				t.Errorf("%s/%s: header missing logo <img src>", kind, loc)
			}
			if !strings.Contains(out.TextBody, "Globe Theatre") {
				t.Errorf("%s/%s: text body missing OrgName", kind, loc)
			}
		}
	}
}

func TestRender_Branding_FooterCarriesLegalIdentification(t *testing.T) {
	r, _ := New()
	data := Data{
		TicketID: "id", EventName: "E", RecipientEmail: "x@y.z",
		Branding: Branding{
			OrgName:                "Globe Theatre",
			LogoURL:                PlatformLogoURL,
			LogoAlt:                "Globe Theatre",
			LegalName:              "Globe Theatre Ltd",
			LegalAddressLine1:      "21 New Globe Walk",
			LegalAddressLine2:      "Suite 4",
			LegalAddressPostalCode: "SE1 9DT",
			LegalAddressCity:       "London",
			LegalAddressCountry:    "GB",
			ContactEmail:           "hello@globe.example.com",
		},
	}
	for _, kind := range []string{TemplateKindTicket, TemplateKindInvitation} {
		for _, loc := range SupportedLocales {
			out, err := r.Render(kind, loc, data)
			if err != nil {
				t.Fatalf("%s/%s: %v", kind, loc, err)
			}
			for _, want := range []string{
				"Globe Theatre Ltd",
				"21 New Globe Walk",
				"Suite 4",
				"SE1 9DT",
				"London",
				"GB",
				"hello@globe.example.com",
			} {
				if !strings.Contains(out.HTMLBody, want) {
					t.Errorf("%s/%s: footer HTML missing %q", kind, loc, want)
				}
				if !strings.Contains(out.TextBody, want) {
					t.Errorf("%s/%s: footer text missing %q", kind, loc, want)
				}
			}
		}
	}
}

func TestRender_Branding_PlatformFallbackLogo(t *testing.T) {
	r, _ := New()
	// When the org has no logo_media_id the worker substitutes the
	// platform logo URL. The header <img src> must point at it.
	out, err := r.Render(TemplateKindTicket, "en", Data{
		TicketID: "id", EventName: "E", RecipientEmail: "x@y.z",
		Branding: Branding{
			OrgName:      PlatformOrgName,
			LogoURL:      PlatformLogoURL,
			LegalName:    PlatformLegalName,
			ContactEmail: PlatformContactEmail,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.HTMLBody, `src="`+PlatformLogoURL+`"`) {
		t.Errorf("header missing platform fallback logo src: %q", out.HTMLBody)
	}
	if !strings.Contains(out.HTMLBody, PlatformLegalName) {
		t.Errorf("footer missing platform legal name: %q", out.HTMLBody)
	}
}

func TestRender_Branding_LogoOmittedWhenURLEmpty(t *testing.T) {
	// Defensive: when LogoURL is empty (worker bug — should never happen
	// in production) the <img> tag is suppressed by the {{with}} guard
	// rather than emitting src="".
	r, _ := New()
	out, err := r.Render(TemplateKindTicket, "en", Data{
		TicketID: "id", EventName: "E", RecipientEmail: "x@y.z",
		Branding: Branding{OrgName: "X", LegalName: "X Ltd"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.HTMLBody, `src=""`) {
		t.Errorf("emitted empty <img src=''>: %q", out.HTMLBody)
	}
}

func TestRender_Branding_OptionalFooterFieldsOmitted(t *testing.T) {
	// When only LegalName is set, the address lines and contact email
	// must not leak as empty lines or dangling labels.
	r, _ := New()
	out, err := r.Render(TemplateKindTicket, "en", Data{
		TicketID: "id", EventName: "E", RecipientEmail: "x@y.z",
		Branding: Branding{
			OrgName:   PlatformOrgName,
			LogoURL:   PlatformLogoURL,
			LegalName: "Solo Ltd",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.HTMLBody, "Contact:") {
		t.Errorf("HTML leaked Contact label with no email: %q", out.HTMLBody)
	}
	if strings.Contains(out.TextBody, "Contact:") {
		t.Errorf("text leaked Contact label with no email: %q", out.TextBody)
	}
}

func TestRender_Branding_HTMLEscapingAppliesToFooter(t *testing.T) {
	// Legal-name and address fields are user-controlled; they must be
	// auto-escaped in the HTML body.
	r, _ := New()
	out, err := r.Render(TemplateKindTicket, "en", Data{
		TicketID: "id", EventName: "E", RecipientEmail: "x@y.z",
		Branding: Branding{
			OrgName:           PlatformOrgName,
			LogoURL:           PlatformLogoURL,
			LegalName:         "<script>x</script>",
			LegalAddressLine1: "Evil & Co.",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.HTMLBody, "<script>x</script>") {
		t.Errorf("LegalName not escaped: %q", out.HTMLBody)
	}
	if !strings.Contains(out.HTMLBody, "Evil &amp; Co.") {
		t.Errorf("LegalAddressLine1 not HTML-escaped: %q", out.HTMLBody)
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
