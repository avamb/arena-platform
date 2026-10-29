package salesnotify

import (
	"strings"
	"testing"
	"time"
)

func inviteSale(by Staff) Sale {
	return Sale{
		OrgName: "Actorre", EventName: "Hamlet", StartAt: time.Date(2026, 11, 20, 19, 0, 0, 0, time.UTC),
		TimeZone: "Europe/Madrid", OrderNumber: 1000000512, Source: "complimentary", ChannelName: "Invitations",
		Currency: "EUR", Total: 0, Categories: []CategoryCount{{Name: "Parterre", Count: 2}},
		Buyer:    Buyer{Name: "Ivan Petrov", Email: "ivan@example.com", Phone: "+34 600 000 000"},
		IssuedBy: by,
	}
}

func TestFormatSale_InvitationNamesWhoIssued(t *testing.T) {
	cases := []struct {
		name string
		by   Staff
		want string
	}{
		{"owner", Staff{Role: RoleOwner, Email: "boss@actorre.es"}, "🙋 Issued by: Owner · boss@actorre.es"},
		{"manager", Staff{Role: RoleManager, Email: "anna@actorre.es"}, "🙋 Issued by: Manager · anna@actorre.es"},
		{"operator", Staff{Role: RoleOperator, Email: "ops@arena.test"}, "🙋 Issued by: Platform operator · ops@arena.test"},
		{"api key", Staff{Role: RoleAPIKey, Label: "WP site <1>"}, "🙋 Issued by: API key · WP site &lt;1&gt;"},
		{"api key without a name", Staff{Role: RoleAPIKey}, "🙋 Issued by: API key"},
		{"left the organization", Staff{Email: "gone@actorre.es"}, "🙋 Issued by: gone@actorre.es"},
	}
	for _, c := range cases {
		msg := FormatSale(inviteSale(c.by))
		if !strings.Contains(msg, "\n\n"+c.want) {
			t.Errorf("%s: missing %q in\n%s", c.name, c.want, msg)
		}
		// The sale itself is announced as before, the block comes last.
		if !strings.Contains(msg, "Total: <b>0.00 EUR</b>") || !strings.Contains(msg, "✉️ ivan@example.com") ||
			!strings.HasSuffix(msg, strings.TrimPrefix(c.want, "")) {
			t.Errorf("%s: sale body changed or block not last:\n%s", c.name, msg)
		}
	}
}

func TestFormatSale_NoIssuerBlockForOrdinarySale(t *testing.T) {
	s := inviteSale(Staff{})
	s.Source = "public_feed"
	if msg := FormatSale(s); strings.Contains(msg, "Issued by") || strings.Contains(msg, "🙋") {
		t.Fatalf("an ordinary sale must not carry the block:\n%s", msg)
	}
	// With no buyer contact the block still follows a blank line.
	s = inviteSale(Staff{Role: RoleManager, Email: "a@b.es"})
	s.Buyer = Buyer{}
	if msg := FormatSale(s); !strings.HasSuffix(msg, "EUR</b>\n\n🙋 Issued by: Manager · a@b.es") {
		t.Fatalf("block without buyer:\n%s", msg)
	}
}

func TestFormatRefund_InvitationNamesWhoCancelled(t *testing.T) {
	r := Refund{
		OrgName: "Actorre", EventName: "Hamlet", StartAt: time.Date(2026, 11, 20, 19, 0, 0, 0, time.UTC),
		TimeZone: "Europe/Madrid", OrderNumber: 1000000512, TicketNumber: 77, Currency: "EUR", Amount: 0,
		Buyer: Buyer{Name: "Ivan Petrov"},
	}
	for _, c := range []struct {
		by   Staff
		want string
	}{
		{Staff{Role: RoleOwner, Email: "boss@actorre.es"}, "🙋 Cancelled by: Owner · boss@actorre.es"},
		{Staff{Role: RoleManager, Email: "anna@actorre.es"}, "🙋 Cancelled by: Manager · anna@actorre.es"},
		{Staff{Role: RoleOperator, Email: "ops@arena.test"}, "🙋 Cancelled by: Platform operator · ops@arena.test"},
		{Staff{Role: RoleAPIKey, Label: "WP site"}, "🙋 Cancelled by: API key · WP site"},
	} {
		r.CancelledBy = c.by
		msg := FormatRefund(r)
		if !strings.Contains(msg, "Ticket cancelled") || !strings.HasSuffix(msg, "\n\n"+c.want) {
			t.Errorf("want suffix %q in\n%s", c.want, msg)
		}
	}
	r.CancelledBy = Staff{}
	if msg := FormatRefund(r); strings.Contains(msg, "Cancelled by") {
		t.Fatalf("an ordinary refund must not carry the block:\n%s", msg)
	}
}
