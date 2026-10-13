package eventbot

import (
	"net/http"
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
	pid := "re_1"
	res := openapi.CreateOrderRefundsResponse{Refunds: []openapi.OrderRefundItem{
		{Amount: 2500, Currency: "EUR", State: "succeeded"},
		{Amount: 2500, Currency: "EUR", State: "provider_pending", ProviderRefundId: &pid},
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

// Round-2 review R2-7: a refund the server only queued (provider_pending with
// no provider id: it was not sent yet) is never shown as accepted.
func TestRefundResultText_QueuedIsNotAccepted(t *testing.T) {
	t.Parallel()
	b := ecTestBot(t)
	res := openapi.CreateOrderRefundsResponse{Refunds: []openapi.OrderRefundItem{
		{Amount: 2500, Currency: "EUR", State: "provider_pending"},
	}}
	for _, loc := range SupportedLocales {
		got := b.refundResultText(loc, refundDialog{Num: 7, Provider: "stripe"}, res)
		if strings.Contains(got, "Stripe") || !strings.Contains(got, "🕓") {
			t.Errorf("%s: a refund not sent yet reads as accepted:\n%s", loc, got)
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

func TestRefundErrorKey_MapsEveryAnswerOfTheCreateRoute(t *testing.T) {
	b := &Bot{}
	cases := []struct {
		status int
		code   string
		want   string
	}{
		{409, "refund.in_progress", "bot.ref.err_progress"},
		{409, "refund.seller_site_order", "bot.ref.site"},
		{409, "refund.ticket_not_refundable", "bot.ref.err_ticket"},
		{409, "refund.amount_exceeds_refundable", "bot.ref.err_changed"},
		{422, "refund.provider_not_supported", "bot.ref.err_unsupported"},
		{422, "refund.partial_not_supported", "bot.ref.err_unsupported"},
		{400, "refund.invalid_request", "bot.ref.err_changed"},
		{503, "refund.engine_unavailable", "bot.ref.err_unavailable"},
		{404, "refund.order_not_found", "bot.ref.err_changed"},
		{500, "refund.create_failed", ""}, // says nothing about the refund: never "nothing happened"
	}
	for _, c := range cases {
		if got := b.refundErrorKey(&APIError{Status: c.status, Code: c.code}); got != c.want {
			t.Errorf("%d %s -> %q; want %q", c.status, c.code, got, c.want)
		}
	}
}

func TestRefundTicketLabel_NamesTheTicketNotItsPosition(t *testing.T) {
	if got := (refundTicket{Number: 5511, Seat: "A / 3 / 12"}).label(0); got != "№5511 (A / 3 / 12)" {
		t.Errorf("label = %q", got)
	}
	if got := (refundTicket{}).label(1); got != "№2" {
		t.Errorf("a ticket without a number falls back to its position: %q", got)
	}
}

// Round-2 review F6: a route without a provider refund is explained for what
// it is, never as "everything was already refunded", and a refused call
// says nothing was sent instead of "taking longer".
func TestRefundRoutesAndRefusalsHaveTheirOwnTexts(t *testing.T) {
	t.Parallel()
	for route, want := range map[string]string{
		"arena_provider": "", "seller_site": "bot.ref.site", "unsupported": "bot.ref.unsupported",
		"none": "bot.ref.no_payment", "unknown_provider": "bot.ref.unknown_provider", "later": "bot.ref.unknown_provider",
	} {
		if got := refundRouteKey(route); got != want {
			t.Errorf("route %q: %q; want %q", route, got, want)
		}
	}
	b := ecTestBot(t)
	for _, st := range []int{http.StatusForbidden, http.StatusUnauthorized} {
		if got := b.refundErrorKey(&APIError{Status: st, Code: "permissions.denied"}); got != "bot.ref.err_forbidden" {
			t.Errorf("status %d: %q; want bot.ref.err_forbidden", st, got)
		}
	}
}

// Round-2 review F6: a refused queue retry is explained by its own code, and
// an unexpected 409 never reads "order № changed" without a number.
func TestQueueRetryRefusalsHaveTheirOwnTexts(t *testing.T) {
	t.Parallel()
	b := ecTestBot(t)
	for _, c := range []struct {
		status int
		code   string
		want   string
	}{
		{http.StatusConflict, "refund.not_retryable", "bot.rq.not_retryable"},
		{http.StatusConflict, "refund.already_replaced", "bot.rq.already_replaced"},
		{http.StatusConflict, "refund.in_progress", "bot.rq.err_progress"},
		{http.StatusConflict, "refund.ticket_not_refundable", "bot.rq.err_ticket"},
		{http.StatusConflict, "refund.over_budget", "bot.rq.err_refused"},
		{http.StatusNotFound, "refund.not_found", "bot.rq.err_refused"},
		{http.StatusForbidden, "permissions.denied", "bot.ref.err_forbidden"},
	} {
		if got := b.queueRetryErrorKey(&APIError{Status: c.status, Code: c.code}); got != c.want {
			t.Errorf("%d %s: %q; want %q", c.status, c.code, got, c.want)
		}
	}
}

// Round-2 review, scanned block item 2: the bot says the owners were told
// only when the server says a notice went out (or went out today).
func TestScannedRefusalNamesWhatWasDone(t *testing.T) {
	t.Parallel()
	for notice, want := range map[string]string{
		"queued": "bot.ref.err_scanned_notified", "throttled": "bot.ref.err_scanned_notified",
		"no_recipient": "bot.ref.err_scanned_nobody", "": "bot.ref.err_scanned",
	} {
		ae := &APIError{Status: http.StatusConflict, Code: "refund.ticket_scanned", OwnersNotice: notice}
		if got := scannedErrorKey(ae); got != want {
			t.Errorf("notice %q: %q; want %q", notice, got, want)
		}
	}
}
