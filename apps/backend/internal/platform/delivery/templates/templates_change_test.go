package templates

import (
	"html"
	"strings"
	"testing"
)

const (
	testPhone   = "+34 600 123 456"
	testMessage = "Doors open at 19:30.\nPlease bring ID."
)

func changeTestData() Data {
	return Data{
		TicketID:       "TN-1042",
		RecipientEmail: "fan@example.com",
		HolderName:     "Lena",
		EventName:      "Symphonic Night",
		SessionStart:   "2026-11-04 20:00 (Europe/Madrid)",
		VenueName:      "Teatro Nuevo",
		TierName:       "Parkett A",
		SeatSector:     "S1",
		SeatRow:        "7",
		SeatNumber:     "12",
		Branding: Branding{
			OrgName:      PlatformOrgName,
			LogoURL:      PlatformLogoURL,
			LogoAlt:      PlatformOrgName,
			LegalName:    PlatformLegalName,
			ContactEmail: PlatformContactEmail,
		},
		Change: ChangeData{
			Date:         true,
			Venue:        true,
			OldStart:     "2026-10-28 20:00 (Europe/Madrid)",
			NewStart:     "2026-11-04 20:00 (Europe/Madrid)",
			OldVenue:     "Sala Vieja",
			NewVenue:     "Teatro Nuevo",
			Message:      testMessage,
			ContactName:  "Familia Teatro",
			ContactEmail: "organizer@example.org",
			ContactPhone: testPhone,
		},
	}
}

var refundFragments = []string{
	"refund", "reembols", "rembours", "erstatt", "rückerstattung",
	"возврат", "vrácen", "החזר",
}

func assertNoRefundPromise(t *testing.T, label string, out Rendered) {
	t.Helper()
	for part, s := range map[string]string{
		"subject": out.Subject, "html": out.HTMLBody, "text": out.TextBody,
	} {
		low := strings.ToLower(s)
		for _, frag := range refundFragments {
			if strings.Contains(low, frag) {
				t.Errorf("%s/%s: contains refund wording %q", label, part, frag)
			}
		}
	}
}

func TestRender_ChangeAndCancel_FullyPopulated(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	data := changeTestData()
	for _, loc := range SupportedLocales {
		change, err := r.Render(TemplateKindChange, loc, data)
		if err != nil {
			t.Fatalf("change/%s: %v", loc, err)
		}
		cancel, err := r.Render(TemplateKindCancel, loc, data)
		if err != nil {
			t.Fatalf("cancel/%s: %v", loc, err)
		}
		for kind, out := range map[string]Rendered{"change": change, "cancel": cancel} {
			label := kind + "/" + loc
			if out.Subject == "" || out.HTMLBody == "" || out.TextBody == "" {
				t.Errorf("%s: empty subject/html/text", label)
			}
			if !strings.Contains(out.Subject, data.EventName) {
				t.Errorf("%s: subject missing event name: %q", label, out.Subject)
			}
			for part, s := range map[string]string{"html": out.HTMLBody, "text": out.TextBody} {
				if !strings.Contains(s, "Please bring ID.") {
					t.Errorf("%s/%s: organizer message missing", label, part)
				}
				if !strings.Contains(s, data.Change.ContactEmail) {
					t.Errorf("%s/%s: contact email missing", label, part)
				}
				// html/template escapes "+" as "&#43;" — compare decoded text.
				if !strings.Contains(html.UnescapeString(s), testPhone) {
					t.Errorf("%s/%s: contact phone missing", label, part)
				}
				if !strings.Contains(s, data.Change.ContactName) {
					t.Errorf("%s/%s: contact name missing", label, part)
				}
				if !strings.Contains(s, data.TicketID) {
					t.Errorf("%s/%s: ticket id missing", label, part)
				}
			}
			if !strings.Contains(out.HTMLBody, `lang="`+loc+`"`) {
				t.Errorf("%s: html missing lang attribute", label)
			}
			if !strings.Contains(out.HTMLBody, "white-space:pre-line") {
				t.Errorf("%s: organizer message must keep line breaks in html", label)
			}
			if loc == "he" && !strings.Contains(out.HTMLBody, `dir="rtl"`) {
				t.Errorf("%s: expected dir=\"rtl\"", label)
			}
			assertNoRefundPromise(t, label, out)
		}

		// change: both parts changed -> all four values shown, generic subject.
		for part, s := range map[string]string{"html": change.HTMLBody, "text": change.TextBody} {
			for _, want := range []string{
				data.Change.OldStart, data.Change.NewStart,
				data.Change.OldVenue, data.Change.NewVenue,
			} {
				if !strings.Contains(s, want) {
					t.Errorf("change/%s/%s: missing %q", loc, part, want)
				}
			}
		}
		// cancel: the cancelled session's start is named; no was->now rows.
		for part, s := range map[string]string{"html": cancel.HTMLBody, "text": cancel.TextBody} {
			if !strings.Contains(s, data.SessionStart) {
				t.Errorf("cancel/%s/%s: session start missing", loc, part)
			}
			if strings.Contains(s, data.Change.OldStart) {
				t.Errorf("cancel/%s/%s: must not show the old start", loc, part)
			}
		}
	}
}

func TestRender_ChangeAndCancel_PhoneHiddenWhenEmpty(t *testing.T) {
	r, _ := New()
	data := changeTestData()
	data.Change.ContactPhone = ""
	for _, kind := range []string{TemplateKindChange, TemplateKindCancel} {
		for _, loc := range SupportedLocales {
			out, err := r.Render(kind, loc, data)
			if err != nil {
				t.Fatalf("%s/%s: %v", kind, loc, err)
			}
			for part, s := range map[string]string{
				"html": out.HTMLBody, "text": out.TextBody, "subject": out.Subject,
			} {
				if strings.Contains(html.UnescapeString(s), testPhone) {
					t.Errorf("%s/%s/%s: phone shown although hidden", kind, loc, part)
				}
			}
			if !strings.Contains(out.TextBody, data.Change.ContactEmail) {
				t.Errorf("%s/%s: contact email missing", kind, loc)
			}
		}
	}
}

func TestRender_ChangeAndCancel_MessageIsEscapedInHTML(t *testing.T) {
	r, _ := New()
	data := changeTestData()
	data.Change.Message = "<script>alert(1)</script>"
	for _, kind := range []string{TemplateKindChange, TemplateKindCancel} {
		for _, loc := range SupportedLocales {
			out, err := r.Render(kind, loc, data)
			if err != nil {
				t.Fatalf("%s/%s: %v", kind, loc, err)
			}
			if strings.Contains(out.HTMLBody, "<script>alert(1)</script>") {
				t.Errorf("%s/%s: organizer message not escaped in html", kind, loc)
			}
			if !strings.Contains(out.HTMLBody, "&lt;script&gt;") {
				t.Errorf("%s/%s: escaped message missing in html", kind, loc)
			}
			if !strings.Contains(out.TextBody, "<script>alert(1)</script>") {
				t.Errorf("%s/%s: text body should keep the message literally", kind, loc)
			}
		}
	}
}

func TestRender_ChangeAndCancel_EmptyMessageOmitsBlock(t *testing.T) {
	r, _ := New()
	data := changeTestData()
	data.Change.Message = ""
	for _, kind := range []string{TemplateKindChange, TemplateKindCancel} {
		for _, loc := range SupportedLocales {
			out, err := r.Render(kind, loc, data)
			if err != nil {
				t.Fatalf("%s/%s: %v", kind, loc, err)
			}
			if strings.Contains(out.HTMLBody, "white-space:pre-line") {
				t.Errorf("%s/%s: message block rendered for an empty message", kind, loc)
			}
		}
	}
}

func TestRender_Change_VenueOnlyHasNoDateRow(t *testing.T) {
	r, _ := New()
	data := changeTestData()
	data.Change.Date = false
	data.Change.OldStart = "2026-10-28 20:00 (Europe/Madrid)"
	data.Change.NewStart = "2026-12-24 21:30 (Europe/Madrid)"
	data.SessionStart = "2026-11-04 20:00 (Europe/Madrid)" // unchanged session time
	for _, loc := range SupportedLocales {
		out, err := r.Render(TemplateKindChange, loc, data)
		if err != nil {
			t.Fatalf("change/%s: %v", loc, err)
		}
		for part, s := range map[string]string{"html": out.HTMLBody, "text": out.TextBody} {
			if !strings.Contains(s, data.Change.OldVenue) || !strings.Contains(s, data.Change.NewVenue) {
				t.Errorf("change/%s/%s: venue rows missing", loc, part)
			}
			if strings.Contains(s, data.Change.OldStart) || strings.Contains(s, data.Change.NewStart) {
				t.Errorf("change/%s/%s: date rows rendered for a venue-only change", loc, part)
			}
			if !strings.Contains(s, data.SessionStart) {
				t.Errorf("change/%s/%s: unchanged session start missing", loc, part)
			}
		}
		assertNoRefundPromise(t, "change-venue/"+loc, out)
	}
}

func TestRender_Change_DateOnlyHasNoVenueRow(t *testing.T) {
	r, _ := New()
	data := changeTestData()
	data.Change.Venue = false
	data.Change.OldVenue = "Sala Vieja"
	for _, loc := range SupportedLocales {
		out, err := r.Render(TemplateKindChange, loc, data)
		if err != nil {
			t.Fatalf("change/%s: %v", loc, err)
		}
		for part, s := range map[string]string{"html": out.HTMLBody, "text": out.TextBody} {
			if strings.Contains(s, data.Change.OldVenue) {
				t.Errorf("change/%s/%s: old venue rendered for a date-only change", loc, part)
			}
			if !strings.Contains(s, data.Change.OldStart) || !strings.Contains(s, data.Change.NewStart) {
				t.Errorf("change/%s/%s: date rows missing", loc, part)
			}
			if !strings.Contains(s, data.VenueName) {
				t.Errorf("change/%s/%s: unchanged venue missing", loc, part)
			}
		}
	}
}

func TestRender_Change_SubjectsDifferByWhatChanged(t *testing.T) {
	r, _ := New()
	for _, loc := range SupportedLocales {
		subjects := map[string]bool{}
		for _, c := range []struct{ date, venue bool }{{true, false}, {false, true}, {true, true}} {
			data := changeTestData()
			data.Change.Date, data.Change.Venue = c.date, c.venue
			out, err := r.Render(TemplateKindChange, loc, data)
			if err != nil {
				t.Fatalf("change/%s: %v", loc, err)
			}
			subjects[out.Subject] = true
		}
		if len(subjects) != 3 {
			t.Errorf("change/%s: expected 3 distinct subjects, got %v", loc, subjects)
		}
	}
}
