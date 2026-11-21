package delivery

import (
	"context"
	"strings"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/email"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/delivery/templates"
)

func TestOrgLetterStyle_ApplySender_VerifiedSenderOwnsTheFrom(t *testing.T) {
	s := orgLetterStyle{Name: " Vino&Co ", SenderEmail: " no-reply@vinoandco.events ", SenderStatus: "verified"}
	msg := email.Message{To: "buyer@example.com"}
	s.applySender(&msg)
	if msg.From != "no-reply@vinoandco.events" {
		t.Fatalf("From = %q, want the organization's verified sender", msg.From)
	}
	if msg.FromName != "Vino&Co" {
		t.Fatalf("FromName = %q, want the organization's name", msg.FromName)
	}
}

func TestOrgLetterStyle_ApplySender_UnverifiedSenderLeavesThePlatformFrom(t *testing.T) {
	for _, status := range []string{"", "pending", "failed"} {
		s := orgLetterStyle{Name: "Vino&Co", SenderEmail: "no-reply@vinoandco.events", SenderStatus: status}
		msg := email.Message{To: "buyer@example.com"}
		s.applySender(&msg)
		if msg.From != "" {
			t.Fatalf("status %q: From = %q, want empty (the adapter's platform default)", status, msg.From)
		}
		if msg.FromName != "Vino&Co" {
			t.Fatalf("status %q: FromName = %q", status, msg.FromName)
		}
	}
}

func TestOrgLetterStyle_ApplySender_KeepsANameAlreadySet(t *testing.T) {
	s := orgLetterStyle{Name: "Vino&Co", SenderEmail: "no-reply@vinoandco.events", SenderStatus: "verified"}
	msg := email.Message{FromName: "Promoter brand"}
	s.applySender(&msg)
	if msg.FromName != "Promoter brand" {
		t.Fatalf("FromName = %q, want the name the caller set", msg.FromName)
	}
}

// The header of the three growth letters takes the organization's accent
// (and its name when there is no logo), like the ticket letter.
func TestOrgLetterStyle_Branding_AccentAndNameWithoutALogo(t *testing.T) {
	s := orgLetterStyle{Name: "Vino&Co", Accent: "#822121"}
	b := s.branding(context.Background(), nil, nil)
	if b.OrgName != "Vino&Co" || b.LogoAlt != "Vino&Co" {
		t.Fatalf("branding = %+v", b)
	}
	if b.Accent != "#822121" {
		t.Fatalf("Accent = %q, want the organization's primary colour", b.Accent)
	}
	r, err := templates.New()
	if err != nil {
		t.Fatal(err)
	}
	out, err := r.Render(templates.TemplateKindMarketingConfirm, "ru", templates.Data{
		RecipientEmail: "buyer@example.com",
		Branding:       b,
		Marketing:      templates.MarketingConfirmData{ConfirmURL: "https://api.example/c", WithdrawURL: "https://api.example/w", FromSite: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.HTMLBody, "background:#822121") {
		t.Fatal("the confirmation button and band do not carry the organization's accent")
	}
	if strings.Contains(out.HTMLBody, templates.DefaultAccentColor) {
		t.Fatal("the platform accent is still in the letter")
	}
	if !strings.Contains(out.HTMLBody, "Вы подписались на сайте") || strings.Contains(out.HTMLBody, "При оформлении заказа") {
		t.Fatal("a subscription left on the website must not be described as a checkout consent")
	}
	if !strings.Contains(out.TextBody, "Вы подписались на сайте") {
		t.Fatal("the text part does not name the website")
	}
}

// A logo URL in the branding puts the image into the header instead of the
// name, in every letter kind that shares the header.
func TestGrowthLetterTemplates_LogoHeader(t *testing.T) {
	r, err := templates.New()
	if err != nil {
		t.Fatal(err)
	}
	b := templates.Branding{OrgName: "Vino&Co", LogoAlt: "Vino&Co", LogoURL: "https://media.example/logo.png", Accent: "#822121"}
	for _, kind := range []string{templates.TemplateKindMarketingConfirm, templates.TemplateKindCheckoutReminder, templates.TemplateKindLatePayment, templates.TemplateKindMyTicketsLink} {
		for _, loc := range templates.SupportedLocales {
			out, err := r.Render(kind, loc, templates.Data{RecipientEmail: "buyer@example.com", Branding: b,
				Marketing:    templates.MarketingConfirmData{ConfirmURL: "https://api.example/c", WithdrawURL: "https://api.example/w"},
				MyTickets:    templates.MyTicketsLinkData{URL: "https://site.example/my-tickets#t=x"},
				CartReminder: templates.CartReminderData{URL: "https://api.example/r"},
			})
			if err != nil {
				t.Fatalf("%s/%s: %v", kind, loc, err)
			}
			if !strings.Contains(out.HTMLBody, `<img src="https://media.example/logo.png"`) {
				t.Fatalf("%s/%s: the header does not show the organization's logo", kind, loc)
			}
		}
	}
}
