package eventbot

import (
	"strings"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/i18n"
)

// The retry question says what the retry really does: it inherits the first
// refund's choices, so a money-only refund does not cancel the ticket and a
// refund sent without a letter sends none.
func TestQueueRetryAsk_FollowsTheRefundsOwnChoices(t *testing.T) {
	bundle, err := i18n.NewBundle()
	if err != nil {
		t.Fatal(err)
	}
	b := &Bot{texts: NewTexts(bundle)}
	no, yes := false, true
	money := openapi.OrderRefundQueueItem{Amount: 1000, Currency: "EUR", CancelTicket: false, NotifyBuyer: &no}
	text := b.queueRetryAskText("en", money)
	if !strings.Contains(text, b.texts.T("en", "bot.rq.retry_keeps_ticket", nil)) || !strings.Contains(text, b.texts.T("en", "bot.rq.retry_no_letter", nil)) {
		t.Errorf("money-only refund without a letter: %q", text)
	}
	full := openapi.OrderRefundQueueItem{Amount: 1000, Currency: "EUR", CancelTicket: true, NotifyBuyer: &yes}
	text = b.queueRetryAskText("en", full)
	if !strings.Contains(text, b.texts.T("en", "bot.rq.retry_cancels_ticket", nil)) || !strings.Contains(text, b.texts.T("en", "bot.rq.retry_letter", nil)) {
		t.Errorf("full refund with a letter: %q", text)
	}
}

// A refused queue retry is named by its own reason: a malformed call and a
// provider that returns only the whole payment are not "changed meanwhile".
func TestQueueRetryErrorKey_NamesBadRequestAndPartialRefusal(t *testing.T) {
	b := &Bot{}
	if got := b.queueRetryErrorKey(&APIError{Status: 400, Code: "refund.invalid_request"}); got != "bot.rq.err_refused" {
		t.Errorf("400: %q; want bot.rq.err_refused", got)
	}
	if got := b.queueRetryErrorKey(&APIError{Status: 422, Code: "refund.partial_not_supported"}); got != "bot.rq.err_partial" {
		t.Errorf("422 partial: %q; want bot.rq.err_partial", got)
	}
	if got := b.queueRetryErrorKey(&APIError{Status: 422, Code: "refund.provider_not_supported"}); got != "bot.ref.err_unsupported" {
		t.Errorf("422 unsupported: %q; want bot.ref.err_unsupported", got)
	}
}

// The ticket line follows retry_cancels_ticket, the operation's choice that
// Engine.Retry inherits, not the refund row's own flag: a refund parked
// before any call gave its row flag up while its operation still cancels.
func TestQueueRetryAsk_FollowsTheOperationsTicketChoice(t *testing.T) {
	bundle, err := i18n.NewBundle()
	if err != nil {
		t.Fatal(err)
	}
	b := &Bot{texts: NewTexts(bundle)}
	yes := true
	parked := openapi.OrderRefundQueueItem{Amount: 1000, Currency: "EUR", CancelTicket: false, RetryCancelsTicket: &yes, NotifyBuyer: &yes}
	if text := b.queueRetryAskText("en", parked); !strings.Contains(text, b.texts.T("en", "bot.rq.retry_cancels_ticket", nil)) {
		t.Errorf("parked refund whose operation cancels the ticket: %q", text)
	}
}
