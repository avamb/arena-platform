package eventbot

import (
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

// sentLetters gives every ticket of d a sent letter to the address to.
func sentLetters(d openapi.OrderDetail, to string) []openapi.OrderDeliveryEntry {
	at := time.Date(2099, 1, 15, 19, 32, 0, 0, time.UTC)
	var out []openapi.OrderDeliveryEntry
	for _, tk := range d.Tickets {
		addr := to
		out = append(out, openapi.OrderDeliveryEntry{TicketId: tk.Id, Status: "sent", SentAt: &at, RecipientEmail: &addr})
	}
	return out
}

func letters(statuses ...string) openapi.OrderDetail {
	var d openapi.OrderDetail
	email := "buyer@example.test"
	d.BuyerEmail = &email
	for i, s := range statuses {
		e := openapi.OrderDeliveryEntry{Status: s}
		if s == "sent" {
			at := time.Date(2099, 1, 15, 19, 30+i, 0, 0, time.UTC)
			e.SentAt = &at
		}
		if s == "failed" {
			msg := "  550 mailbox\n unavailable  "
			e.LastError = &msg
		}
		d.Delivery = append(d.Delivery, e)
	}
	return d
}

func TestTicketLetterStatus_Folds(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in    []string
		state string
		done  int
		total int
	}{
		{nil, letterNone, 0, 0},
		{[]string{"none", "skipped", "disabled"}, letterNone, 0, 0},
		{[]string{"pending", "processing"}, letterQueued, 0, 2},
		{[]string{"sent", "sent"}, letterSent, 2, 2},
		{[]string{"sent", "pending"}, letterPartialQueued, 1, 2},
		{[]string{"sent", "failed", "sent", "sent"}, letterPartial, 3, 4},
		{[]string{"failed"}, letterFailed, 0, 1},
		{[]string{"failed", "pending"}, letterQueued, 0, 2},
		{[]string{"opened", "delivered"}, letterDelivered, 2, 2},
		{[]string{"opened", "opened"}, letterOpened, 2, 2},
	}
	for i, c := range cases {
		got := ticketLetterStatus(letters(c.in...))
		if got.State != c.state || got.Done != c.done || got.Total != c.total {
			t.Errorf("case %d %v: got %s %d/%d, want %s %d/%d", i, c.in, got.State, got.Done, got.Total, c.state, c.done, c.total)
		}
	}
	// The latest send is the time, the failure is one clean line.
	s := ticketLetterStatus(letters("sent", "failed", "sent"))
	if s.At == nil || s.At.Minute() != 32 {
		t.Errorf("time = %v, want the latest send", s.At)
	}
	if s.Why != "550 mailbox unavailable" || !s.failed() {
		t.Errorf("why = %q failed=%v", s.Why, s.failed())
	}
	if s.To != "buyer@example.test" {
		t.Errorf("to = %q, want the buyer's address when the job has none", s.To)
	}
}

func TestRefundLetterStatus(t *testing.T) {
	t.Parallel()
	var d openapi.OrderDetail
	if s := refundLetterStatus(d); s.State != "" {
		t.Errorf("no refund letter must say nothing, got %q", s.State)
	}
	at := time.Date(2099, 1, 16, 10, 0, 0, 0, time.UTC)
	for in, want := range map[string]string{"off": letterOff, "queued": letterQueued, "partial": letterPartialQueued, "sent": letterSent, "failed": letterFailed, "weird": ""} {
		d.RefundLetter = &openapi.OrderRefundLetter{State: in, Refunds: 2, Sent: 1, At: &at}
		if got := refundLetterStatus(d).State; got != want {
			t.Errorf("%s -> %q, want %q", in, got, want)
		}
	}
}

// The card's lines, in the venue's zone, and the address only in a private chat.
func TestOrderCard_LetterLines(t *testing.T) {
	t.Parallel()
	b := ecTestBot(t)
	d := orderDetail(t, "partially_refunded", "", 2, "")
	d.Delivery = sentLetters(d, "anna.tickets@example.test")
	msg := "550 mailbox full"
	d.Delivery[1].Status, d.Delivery[1].SentAt, d.Delivery[1].LastError = "failed", nil, &msg
	at := time.Date(2099, 1, 16, 10, 0, 0, 0, time.UTC)
	d.RefundLetter = &openapi.OrderRefundLetter{State: "sent", Refunds: 1, Sent: 1, At: &at}

	got := b.orderCardText("ru", d, testMeta, true, false)
	for _, want := range []string{
		"⚠️📧 Письмо с билетами: отправлено 1 из 2. Остальные Arena отправить не смогла. Причина: 550 mailbox full",
		"📧 Письмо о возврате: отправлено 16.01.2099 11:00 на anna@example.test.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("card lacks %q:\n%s", want, got)
		}
	}
	group := b.orderCardText("ru", d, testMeta, false, false)
	if strings.Contains(group, "@example.test") {
		t.Errorf("a group chat must not see an address:\n%s", group)
	}

	// A failed letter renames the resend button; a sent one keeps its name.
	kb := relabelButton(b.withResendButton(b.orderCardKeyboard("ru", d), resendIdentity("org_admin", false), d, true),
		"or:r:s:"+d.Id.String(), b.texts.T("ru", "bot.ord.letter_retry_btn", nil))
	if !keyboardHasText(kb, "📧 Отправить письмо ещё раз") {
		t.Errorf("the retry label is missing: %+v", kb)
	}
}

func keyboardHasText(kb *models.InlineKeyboardMarkup, text string) bool {
	if kb == nil {
		return false
	}
	for _, row := range kb.InlineKeyboard {
		for _, btn := range row {
			if btn.Text == text {
				return true
			}
		}
	}
	return false
}

func TestOrderRowLabel_MarksAFailedLetter(t *testing.T) {
	t.Parallel()
	o := listItem(t, "paid", 4700, "Buyer Name")
	failed := letterFailed
	o.DeliveryState = &failed
	if got := OrderRowLabel(o, "en"); !strings.Contains(got, "✅ · ⚠️📧 · 47 EUR") {
		t.Errorf("row = %q, want the failed-letter mark", got)
	}
	sent := letterSent
	o.DeliveryState = &sent
	if got := OrderRowLabel(o, "en"); strings.Contains(got, "📧") {
		t.Errorf("a sent letter must not be marked: %q", got)
	}
}
