package eventbot

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

func refundOptions(provider string, partial bool, refundable ...int64) openapi.OrderRefundOptions {
	opts := openapi.OrderRefundOptions{
		OrderId: uuid.New(), Provider: provider, Currency: "EUR", PartialRefund: partial, Route: refundRouteArena,
	}
	for _, r := range refundable {
		opts.Tickets = append(opts.Tickets, openapi.OrderRefundTicketOption{TicketId: openapi_types.UUID(uuid.New()), Refundable: r})
	}
	return opts
}

// The dialog offers exactly what the server says is refundable: a ticket with
// nothing left, or one already in a refund, is not a choice.
func TestRefundDialog_OffersOnlyRefundableTickets(t *testing.T) {
	t.Parallel()
	opts := refundOptions("stripe", true, 2500, 0, 1800)
	opts.Tickets[2].InProgress = true
	d := newRefundDialog(uuid.New(), 1000000500, opts)
	if len(d.Tickets) != 1 || d.Tickets[0].Refundable != 2500 || !d.Tickets[0].Sel {
		t.Fatalf("tickets = %+v; want only the 2500 one, selected", d.Tickets)
	}
}

func TestRefundDialog_TotalFollowsTheSelectionAndAPartialAmount(t *testing.T) {
	t.Parallel()
	d := newRefundDialog(uuid.New(), 1, refundOptions("stripe", true, 2500, 2500, 1000))
	if got := d.total(); got != 6000 {
		t.Fatalf("total = %d; want 6000", got)
	}
	d.Tickets[1].Sel = false
	d.Tickets[0].Amount = 1000
	if got := d.total(); got != 2000 {
		t.Fatalf("total = %d; want 2000 (a part of the first ticket plus the third)", got)
	}
	d.Tickets[0].Amount = 9999 // more than refundable is never sent
	if got := d.Tickets[0].amountOf(); got != 2500 {
		t.Fatalf("amountOf = %d; want the refundable 2500", got)
	}
}

func TestRefundWord_AcceptsTheLocalWordAndEnglish(t *testing.T) {
	t.Parallel()
	b := ecTestBot(t)
	for _, c := range []struct {
		loc, text string
		want      bool
	}{
		{"en", "refund", true}, {"en", " REFUND! ", true}, {"ru", "вернуть", true}, {"ru", "REFUND", true},
		{"es", "devolver", true}, {"en", "yes", false}, {"ru", "да", false}, {"en", "", false},
	} {
		if got := b.isRefundWord(c.loc, c.text); got != c.want {
			t.Errorf("isRefundWord(%q, %q) = %v; want %v", c.loc, c.text, got, c.want)
		}
	}
}

func TestRefundResultText_NamesEveryOutcome(t *testing.T) {
	t.Parallel()
	b := ecTestBot(t)
	why := "card closed"
	res := openapi.CreateOrderRefundsResponse{Refunds: []openapi.OrderRefundItem{
		{Amount: 2500, Currency: "EUR", State: "succeeded"},
		{Amount: 2500, Currency: "EUR", State: "provider_pending"},
		{Amount: 2500, Currency: "EUR", State: "failed", FailureReason: &why},
		{Amount: 2500, Currency: "EUR", State: "manual_review"},
	}}
	got := b.refundResultText("en", refundDialog{Num: 1000000500, Provider: "stripe"}, res)
	for _, want := range []string{"1000000500", "returned", "Stripe", "failed: card closed", "manual check"} {
		if !strings.Contains(got, want) {
			t.Errorf("result text lacks %q:\n%s", want, got)
		}
	}
}

// The button is on a paid order's card, in a private chat, for a role that may refund.
func TestRefundButton_OnlyWhereItCanBeUsed(t *testing.T) {
	t.Parallel()
	b := ecTestBot(t)
	has := func(id *Identity, status string, private bool) bool {
		d := orderDetail(t, status, "", 2, "")
		kb := b.withRefundButton(b.orderCardKeyboard("en", d), id, d, private)
		for _, row := range kb.InlineKeyboard {
			for _, btn := range row {
				if strings.HasPrefix(btn.CallbackData, "or:f:s:") {
					if len(btn.CallbackData) > 64 {
						t.Errorf("callback %q is longer than Telegram allows", btn.CallbackData)
					}
					return true
				}
			}
		}
		return false
	}
	owner, manager, agent := resendIdentity("org_admin", false), resendIdentity("organizer", false), resendIdentity("agent", false)
	if !has(owner, "paid", true) || !has(manager, "partially_refunded", true) {
		t.Error("a paid order must offer the button to the owner and the manager")
	}
	if has(agent, "paid", true) || has(manager, "paid", false) || has(manager, "pending_payment", true) || has(manager, "refunded", true) {
		t.Error("the button must not appear for an agent, in a group chat, or on an unpaid or fully refunded order")
	}
}
