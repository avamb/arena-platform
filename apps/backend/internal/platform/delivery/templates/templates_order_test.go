package templates

import (
	"strings"
	"testing"
)

func orderData() Data {
	return Data{
		RecipientEmail: "buyer@example.com",
		HolderName:     "Anna",
		EventName:      "Jazz Night",
		SessionStart:   "2026-11-14 20:00 (CET)",
		VenueName:      "Sala Clamores, Madrid",
		Tickets: []TicketLine{
			{Number: "253", Tier: "Front", Seat: "Row 1 · Seat 4", Price: "25.00 EUR"},
			{Number: "254", Tier: "Front", Seat: "Row 1 · Seat 5", Price: "25.00 EUR"},
		},
		Payment: &PaymentData{
			OrderNumber: "1001900724",
			PaidAt:      "2026-10-07 16:32 (CEST)",
			Subtotal:    "50.00 EUR",
			Discount:    "5.00 EUR",
			ServiceFee:  "0.45 EUR",
			Total:       "45.45 EUR",
		},
		Branding: Branding{
			OrgName:      "ABH Team",
			WebsiteURL:   "https://abh.example.com",
			LogoURL:      "https://api.example.com/v1/media-files/logo",
			LogoAlt:      "ABH Team",
			LegalName:    "ABH TEAM OU",
			ContactEmail: "contact@abh.example.com",
		},
	}
}

// One letter carries every ticket of the order and the payment block, in every
// shipped language, in the design of the site e-mails.
func TestRender_Ticket_OrderLetter_AllLocales(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	for _, loc := range SupportedLocales {
		out, err := r.Render(TemplateKindTicket, loc, orderData())
		if err != nil {
			t.Fatalf("%s: %v", loc, err)
		}
		for _, want := range []string{
			"253", "254", // both tickets
			"1001900724",             // the order number
			"50.00 EUR", "45.45 EUR", // subtotal and total
			"5.00 EUR", "0.45 EUR", // discount and service fee
			"2026-10-07 16:32 (CEST)",     // the payment time
			"Jazz Night", "Sala Clamores", // the event card
			`max-width:560px`, `#f2f4f5`, // the site design
			`src="https://api.example.com/v1/media-files/logo"`,
			"background:#4f46e5", // the accent band, the Arena default
		} {
			if !strings.Contains(out.HTMLBody, want) {
				t.Errorf("%s: html missing %q", loc, want)
			}
		}
		for _, want := range []string{"253", "254", "1001900724", "45.45 EUR", "Jazz Night"} {
			if !strings.Contains(out.TextBody, want) {
				t.Errorf("%s: text missing %q", loc, want)
			}
		}
		if strings.Contains(out.HTMLBody, "ZgotmplZ") {
			t.Errorf("%s: html/template refused a value (ZgotmplZ)", loc)
		}
		if !strings.Contains(out.Subject, "Jazz Night") {
			t.Errorf("%s: subject %q lacks the event name", loc, out.Subject)
		}
	}
}

// The subject and the greeting say "tickets" for an order of several and keep
// the singular for one.
func TestRender_Ticket_SubjectFollowsTheTicketCount(t *testing.T) {
	r, _ := New()
	many := orderData()
	one := orderData()
	one.Tickets = one.Tickets[:1]
	for _, loc := range SupportedLocales {
		m, err := r.Render(TemplateKindTicket, loc, many)
		if err != nil {
			t.Fatal(err)
		}
		o, err := r.Render(TemplateKindTicket, loc, one)
		if err != nil {
			t.Fatal(err)
		}
		if m.Subject == o.Subject {
			t.Errorf("%s: the subject must differ between 1 and 2 tickets, both %q", loc, m.Subject)
		}
	}
}

// Without a payment block (an invitation, a free order, unknown money) nothing
// of it is printed; without Tickets the single-ticket fields still work.
func TestRender_Ticket_NoPaymentBlockAndLegacySingleTicket(t *testing.T) {
	r, _ := New()
	d := orderData()
	d.Payment = nil
	d.Tickets = nil
	d.TicketID = "777"
	d.TierName = "VIP"
	d.SeatRow = "3"
	out, err := r.Render(TemplateKindTicket, "en", d)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.HTMLBody, "Total paid") || strings.Contains(out.TextBody, "Total paid") {
		t.Error("a payment block was printed without payment data")
	}
	for _, want := range []string{"No. 777", "VIP", "Row 3"} {
		if !strings.Contains(out.HTMLBody, want) {
			t.Errorf("legacy single ticket: html missing %q", want)
		}
	}
}

// A Hebrew letter is right-to-left end to end, the accent border sits on the
// right and the price column on the left.
func TestRender_Ticket_Hebrew_IsRTL(t *testing.T) {
	r, _ := New()
	out, err := r.Render(TemplateKindTicket, "he", orderData())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`dir="rtl"`, "border-right:4px solid", "text-align:left;"} {
		if !strings.Contains(out.HTMLBody, want) {
			t.Errorf("he: html missing %q", want)
		}
	}
}

// An organization's own accent colour reaches the band; an empty one is the
// Arena default.
func TestRender_AccentColour(t *testing.T) {
	r, _ := New()
	d := orderData()
	d.Accent = "#FCDC54"
	out, err := r.Render(TemplateKindTicket, "en", d)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.HTMLBody, "background:#FCDC54;height:3px") {
		t.Errorf("accent colour not applied: %s", out.HTMLBody)
	}
}

// The change and cancel letters wear the same frame and still say what changed.
func TestRender_ChangeAndCancel_WearTheSiteDesign(t *testing.T) {
	r, _ := New()
	d := orderData()
	d.Change = ChangeData{Date: true, OldStart: "2026-11-14 20:00", NewStart: "2026-11-21 20:00", ContactEmail: "o@example.com", Message: "Sorry"}
	for _, kind := range []string{TemplateKindChange, TemplateKindCancel} {
		for _, loc := range SupportedLocales {
			out, err := r.Render(kind, loc, d)
			if err != nil {
				t.Fatalf("%s/%s: %v", kind, loc, err)
			}
			for _, want := range []string{`max-width:560px`, "background:#4f46e5", "border-left:3px solid #4f46e5", "o@example.com"} {
				if loc == "he" && want == "border-left:3px solid #4f46e5" {
					continue
				}
				if !strings.Contains(out.HTMLBody, want) {
					t.Errorf("%s/%s: html missing %q", kind, loc, want)
				}
			}
		}
	}
}

func TestFormatSeat(t *testing.T) {
	cases := []struct{ loc, sec, row, num, want string }{
		{"en", "A", "3", "5", "Sector A · Row 3 · Seat 5"},
		{"ru", "", "3", "5", "Ряд 3 · Место 5"},
		{"de", "", "", "", ""},
		{"xx", "B", "", "", "Sector B"}, // unknown locale -> English
		{"cs-CZ", "", "", "7", "Místo 7"},
	}
	for _, c := range cases {
		if got := FormatSeat(c.loc, c.sec, c.row, c.num); got != c.want {
			t.Errorf("FormatSeat(%q,%q,%q,%q) = %q, want %q", c.loc, c.sec, c.row, c.num, got, c.want)
		}
	}
}
