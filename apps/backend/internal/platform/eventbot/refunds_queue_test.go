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
