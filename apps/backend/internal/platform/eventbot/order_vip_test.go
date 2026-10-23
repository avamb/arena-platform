package eventbot

import (
	"strings"
	"testing"
)

// V1: one button per active ticket for the roles that hold ticket.update, the
// press carries the WANTED value, and a marked ticket shows "VIP" on its line.
func TestVIPButtons_PerActiveTicketWithTheWantedValue(t *testing.T) {
	t.Parallel()
	b := ecTestBot(t)
	d := orderDetail(t, "paid", "", 3, "")
	d.Tickets[0].Vip = true
	d.Tickets[2].Status = "cancelled"

	var got []string
	kb := b.withVIPButtons(b.orderCardKeyboard("en", d), resendIdentity("organizer", false), d)
	for _, row := range kb.InlineKeyboard {
		for _, btn := range row {
			if strings.HasPrefix(btn.CallbackData, "or:vip:") {
				if len(btn.CallbackData) > 64 {
					t.Errorf("callback data too long: %d bytes", len(btn.CallbackData))
				}
				got = append(got, btn.CallbackData)
			}
		}
	}
	want := []string{"or:vip:0:" + d.Tickets[0].Id.String(), "or:vip:1:" + d.Tickets[1].Id.String()}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("buttons = %v, want %v (marked -> clear, unmarked -> mark, cancelled -> none)", got, want)
	}
	last := kb.InlineKeyboard[len(kb.InlineKeyboard)-1]
	if last[len(last)-1].CallbackData != "home" {
		t.Error("the Back / Home row must stay last")
	}
}

func TestVIPButtons_NotForAnAgentOrAnEmptyIdentity(t *testing.T) {
	t.Parallel()
	b := ecTestBot(t)
	d := orderDetail(t, "paid", "", 2, "")
	for name, id := range map[string]*Identity{"agent": resendIdentity("agent", false), "none": {}} {
		kb := b.withVIPButtons(b.orderCardKeyboard("en", d), id, d)
		for _, row := range kb.InlineKeyboard {
			for _, btn := range row {
				if strings.HasPrefix(btn.CallbackData, "or:vip:") {
					t.Errorf("%s: got a VIP button %q", name, btn.CallbackData)
				}
			}
		}
	}
}

func TestVIPButtons_StayInPlaceAndShortAndMarkTheLine(t *testing.T) {
	t.Parallel()
	for _, data := range []string{"or:vip:1:00000000-0000-0000-0000-000000000000", "iv:vip:0"} {
		if !inPlacePress(data, nil, "") {
			t.Errorf("%s must redraw its own message", data)
		}
	}
	b := ecTestBot(t)
	d := orderDetail(t, "paid", "", 2, "")
	d.Tickets[1].Vip = true
	if strings.Contains(b.ticketLine("en", d.Tickets[0], ""), "VIP") {
		t.Error("an unmarked ticket line mentions VIP")
	}
	if !strings.Contains(b.ticketLine("en", d.Tickets[1], ""), "VIP") {
		t.Error("a marked ticket line does not show VIP")
	}
}
