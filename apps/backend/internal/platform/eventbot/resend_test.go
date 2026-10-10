package eventbot

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

func resendIdentity(role string, super bool) *Identity {
	return &Identity{Current: &Membership{Role: role}, Superadmin: super}
}

// The button is on the card of a paid order with an active ticket, for a role
// that may use it, in the private chat — and nowhere else.
func TestResendButton_OnlyWhereItCanBeUsed(t *testing.T) {
	t.Parallel()
	b := ecTestBot(t)
	has := func(id *Identity, status string, tickets, cancelled int, private bool) (bool, string) {
		d := orderDetail(t, status, "", tickets, "")
		for i := 0; i < cancelled && i < len(d.Tickets); i++ {
			d.Tickets[i].Status = "cancelled"
		}
		kb := b.withResendButton(b.orderCardKeyboard("en", d), id, d, private)
		for _, row := range kb.InlineKeyboard {
			for _, btn := range row {
				if strings.HasPrefix(btn.CallbackData, "or:r:s:") {
					return true, btn.CallbackData
				}
			}
		}
		return false, ""
	}
	owner, manager, agent := resendIdentity("org_admin", false), resendIdentity("organizer", false), resendIdentity("agent", false)

	for _, id := range []*Identity{owner, manager, resendIdentity("agent", true)} {
		if ok, data := has(id, "paid", 2, 0, true); !ok || len(data) > 64 {
			t.Errorf("a paid order must offer the button (%+v): %v %q", id.Current, ok, data)
		}
	}
	if ok, _ := has(manager, "partially_refunded", 2, 1, true); !ok {
		t.Error("a partially refunded order with an active ticket must offer the button")
	}
	for name, c := range map[string]struct {
		id        *Identity
		status    string
		tickets   int
		cancelled int
		private   bool
	}{
		"agent":                   {agent, "paid", 2, 0, true},
		"unpaid":                  {manager, "pending_payment", 1, 0, true},
		"expired":                 {manager, "expired", 0, 0, true},
		"cancelled":               {manager, "cancelled", 0, 0, true},
		"refunded":                {manager, "refunded", 2, 2, true},
		"no tickets":              {manager, "paid", 0, 0, true},
		"every ticket cancelled":  {manager, "paid", 2, 2, true},
		"group chat":              {manager, "paid", 2, 0, false},
		"no organization (empty)": {&Identity{}, "paid", 2, 0, true},
	} {
		if ok, _ := has(c.id, c.status, c.tickets, c.cancelled, c.private); ok {
			t.Errorf("%s: the button must not be offered", name)
		}
	}
}

// The button sits above Back/Home, below Cancel, and leaves the rest alone.
func TestResendButton_KeepsTheOtherRows(t *testing.T) {
	t.Parallel()
	b := ecTestBot(t)
	d := orderDetail(t, "paid", "", 1, "")
	plain := b.orderCardKeyboard("en", d)
	kb := b.withResendButton(plain, resendIdentity("organizer", false), d, true)
	if len(kb.InlineKeyboard) != len(plain.InlineKeyboard)+1 {
		t.Fatalf("rows %d -> %d, want one more", len(plain.InlineKeyboard), len(kb.InlineKeyboard))
	}
	last := kb.InlineKeyboard[len(kb.InlineKeyboard)-1]
	if last[0].CallbackData != "or:b" || last[1].CallbackData != "home" {
		t.Errorf("Back/Home must stay last: %+v", last)
	}
	if got := kb.InlineKeyboard[len(kb.InlineKeyboard)-2][0].CallbackData; got != "or:r:s:"+d.Id.String() {
		t.Errorf("the resend row = %q", got)
	}
	// A nil keyboard and an empty one pass through.
	if b.withResendButton(nil, resendIdentity("organizer", false), d, true) != nil {
		t.Error("nil keyboard must stay nil")
	}
}

func TestValidResendEmail_Bot(t *testing.T) {
	t.Parallel()
	for _, s := range []string{"a@b.co", "anna.nova+tag@example.com", "x_y@sub.example.org"} {
		if !validResendEmail(s) {
			t.Errorf("%q must be valid", s)
		}
	}
	for _, s := range []string{"", "plain", "a@b", "a@.com", "a@b.", "@example.com", "a b@example.com", "Name <a@example.com>",
		"a@example.com, b@example.com", "a@example.com;b@example.com", "\"q\"@example.com", "a@@example.com",
		strings.Repeat("a", 260) + "@example.com"} {
		if validResendEmail(s) {
			t.Errorf("%q must be invalid", s)
		}
	}
}

func TestMaskEmail_Bot(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"anna@example.com":       "a***@e******.com",
		"a@b.co":                 "a@b.co",
		"boris@mail.example.org": "b****@m***.example.org",
		"nonsense":               "***",
		"юля@пример.рф":          "ю**@п*****.рф",
	} {
		if got := maskEmail(in); got != want {
			t.Errorf("maskEmail(%q) = %q, want %q", in, got, want)
		}
	}
}

// The address the letter goes to when none is typed: the buyer's, else the
// first ACTIVE holder's.
func TestOrderAddress(t *testing.T) {
	t.Parallel()
	d := orderDetail(t, "paid", "", 2, "")
	if got := orderAddress(d); got != "anna@example.test" {
		t.Errorf("buyer address = %q", got)
	}
	d.BuyerEmail = nil
	if got := orderAddress(d); got != "" {
		t.Errorf("no buyer and no holder = %q", got)
	}
	holder := "holder@example.test"
	d.Tickets[0].Status = "cancelled"
	d.Tickets[0].HolderEmail = ptr("cancelled@example.test")
	d.Tickets[1].HolderEmail = &holder
	if got := orderAddress(d); got != holder {
		t.Errorf("holder address = %q, want the active ticket's", got)
	}
	if got := activeTickets(d); got != 1 {
		t.Errorf("activeTickets = %d, want 1", got)
	}
}

func ptr(s string) *string { return &s }

// Every resend text renders in every supported language with the values it is
// given — the order number, the count and the address — and the one-time text
// names the 24 hours.
func TestResendTexts_RenderInEveryLanguage(t *testing.T) {
	t.Parallel()
	b := ecTestBot(t)
	vars := map[string]any{"Num": 1000000500, "N": 3, "Email": "a***@e******.com"}
	for _, loc := range SupportedLocales {
		for _, key := range resendKeys {
			got := b.texts.T(loc, key, vars)
			if got == "" || got == key || strings.Contains(got, "<no value>") {
				t.Errorf("%s %s rendered %q", loc, key, got)
			}
		}
		for _, key := range []string{"bot.resend.choose", "bot.resend.choose_noaddr", "bot.resend.other_ask", "bot.resend.confirm_same",
			"bot.resend.confirm_other", "bot.resend.done_same", "bot.resend.done_other", "bot.resend.site", "bot.resend.not_paid", "bot.resend.no_tickets"} {
			if got := b.texts.T(loc, key, vars); !strings.Contains(got, "1000000500") {
				t.Errorf("%s %s lacks the order number: %q", loc, key, got)
			}
		}
		for _, key := range []string{"bot.resend.same_btn", "bot.resend.choose", "bot.resend.confirm_same", "bot.resend.confirm_other", "bot.resend.done_same", "bot.resend.done_other"} {
			if got := b.texts.T(loc, key, vars); !strings.Contains(got, "a***@e******.com") {
				t.Errorf("%s %s lacks the address: %q", loc, key, got)
			}
		}
		for _, key := range []string{"bot.resend.other_ask", "bot.resend.confirm_other"} {
			if got := b.texts.T(loc, key, vars); !strings.Contains(got, "24") {
				t.Errorf("%s %s must name the 24 hours: %q", loc, key, got)
			}
		}
		// The warning says the order's own address does not change.
		if ask := b.texts.T(loc, "bot.resend.other_ask", vars); ask == b.texts.T("en", "bot.resend.other_ask", vars) && loc != "en" {
			t.Errorf("%s other_ask is a copy of English", loc)
		}
	}
}

// Presses stay inside Telegram's 64-byte callback_data.
func TestResendCallbacks_FitTelegram(t *testing.T) {
	t.Parallel()
	for _, data := range []string{"or:r:s:" + uuid.NewString(), "or:r:a", "or:r:o", "or:r:go", "or:r:b"} {
		if len(data) > 64 {
			t.Errorf("%q is %d bytes", data, len(data))
		}
	}
}
