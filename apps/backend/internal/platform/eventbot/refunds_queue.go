package eventbot

// refunds_queue.go — the "Refunds" queue (EC-23, spec 35 §8): the refunds of
// the organization by state, with what the person can do next. It reads
// GET .../refunds and shows the server's states; nothing is worked out here.
//
// The screens keep no dialog row: a list is re-read on every press, and a
// press that CHANGES something carries the refund's own UUID, so an old
// message in the chat always means the same refund.
//
//	rq:l:<filter>:<page>   the list (filters review, failed, pending, done)
//	rq:rt:<refund id>      "send it again?" — the question
//	rq:ry:<refund id>      yes: POST .../refunds/{id}/retry
//
// "Open order" is the Orders card's own press, or:v:<order id>.
//
// A retry starts a NEW refund for the same ticket and amount; the server
// refuses (409 refund.not_retryable) anything that may already have moved
// money, so the button is only a request, never a promise. Closing a refund
// by hand is a platform operator's job and is not offered here.

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

// queueFilters maps the filter chip to the refund state the API filters on.
var queueFilters = []struct{ key, state string }{
	{"review", "manual_review"},
	{"failed", "failed"},
	{"pending", "provider_pending"},
	{"done", "succeeded"},
}

const (
	queueDefaultFilter = "review"
	codeNotRetryable   = "refund.not_retryable"
)

func queueState(filter string) (string, bool) {
	for _, f := range queueFilters {
		if f.key == filter {
			return f.state, true
		}
	}
	return "", false
}

// queueChip is the mark in front of a refund line.
func queueChip(state string) string {
	switch state {
	case "succeeded":
		return "✅"
	case "provider_pending":
		return "⏳"
	case "failed":
		return "❌"
	case "manual_review":
		return "⚠️"
	}
	return "•"
}

// queueRetryable says whether the row gets a "Retry" button. The server has
// the last word; this only keeps the button off rows that can never qualify.
func queueRetryable(it openapi.OrderRefundQueueItem) bool {
	switch it.State {
	case "failed":
		return it.TicketId != nil
	case "manual_review":
		return it.TicketId != nil && it.ProviderRefundId == nil
	}
	return false
}

// queueMenuRow is the main menu's row for the queue.
func (b *Bot) queueMenuRow(loc string, id *Identity) []models.InlineKeyboardButton {
	if !canRefund(id) {
		return nil
	}
	return []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.rq.btn", nil), CallbackData: "rq:l:" + queueDefaultFilter + ":1"}}
}

// queueCallback handles every "rq:<data>" press.
func (b *Bot) queueCallback(ctx context.Context, chatID int64, msgID int, from *models.User, data string) {
	id, jwt, ok := b.ecIdentity(ctx, chatID, &msgID, from)
	if !ok {
		return
	}
	loc := id.Locale()
	if !canRefund(id) {
		b.reply(ctx, chatID, &msgID, b.texts.T(loc, "bot.ec.no_rights", nil), b.backKeyboard(loc, "home"))
		return
	}
	parts := strings.Split(data, ":")
	switch parts[0] {
	case "l":
		filter, page := queueDefaultFilter, 1
		if len(parts) > 1 {
			filter = parts[1]
		}
		if len(parts) > 2 {
			page = ParsePage(parts[2])
		}
		b.showQueue(ctx, chatID, &msgID, id, jwt, filter, page)
	case "rt", "ry":
		if len(parts) < 2 {
			return
		}
		refundID, err := uuid.Parse(parts[1])
		if err != nil {
			return
		}
		if parts[0] == "rt" {
			b.askQueueRetry(ctx, chatID, &msgID, loc, refundID)
			return
		}
		b.doQueueRetry(ctx, chatID, &msgID, id, jwt, refundID)
	}
}

// showQueue draws one page of the filter.
func (b *Bot) showQueue(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt, filter string, page int) {
	loc := id.Locale()
	state, ok := queueState(filter)
	if !ok {
		filter = queueDefaultFilter
		state, _ = queueState(filter)
	}
	if page < 1 {
		page = 1
	}
	q, err := b.arena.RefundQueue(ctx, jwt, id.Current.OrgID, state, listPageSize, (page-1)*listPageSize)
	if err != nil {
		b.ecError(ctx, chatID, editMsgID, id, err, "home")
		return
	}
	pages := (q.Total + listPageSize - 1) / listPageSize
	if pages < 1 {
		pages = 1
	}
	if page > pages {
		// The list shrank (a refund was dealt with elsewhere): show the last page.
		b.showQueue(ctx, chatID, editMsgID, id, jwt, filter, pages)
		return
	}

	var sb strings.Builder
	sb.WriteString(b.texts.T(loc, "bot.rq.title", map[string]any{"Filter": b.texts.T(loc, "bot.rq.f_"+filter, nil), "Count": q.Total}))
	var rows [][]models.InlineKeyboardButton
	var chips []models.InlineKeyboardButton
	for _, f := range queueFilters {
		label := b.texts.T(loc, "bot.rq.f_"+f.key, nil)
		if f.key == filter {
			label = "• " + label
		}
		chips = append(chips, models.InlineKeyboardButton{Text: label, CallbackData: "rq:l:" + f.key + ":1"})
	}
	rows = append(rows, chips[:2], chips[2:])

	if len(q.Refunds) == 0 {
		sb.WriteString("\n\n" + b.texts.T(loc, "bot.rq.empty", nil))
	}
	for _, it := range q.Refunds {
		order := "—"
		if it.OrderNumber != nil {
			order = "№" + strconv.FormatInt(*it.OrderNumber, 10)
		}
		event := ""
		if it.EventName != nil {
			event = " · " + Esc(*it.EventName)
		}
		sb.WriteString("\n\n" + b.texts.T(loc, "bot.rq.row", map[string]any{
			"Chip": queueChip(it.State), "Order": order, "Amount": FormatMoney(it.Amount, it.Currency, loc), "Event": event,
		}))
		if why := queueWhy(it); why != "" {
			sb.WriteString("\n" + Esc(clipRunes(why, 160)))
		}
		var row []models.InlineKeyboardButton
		if queueRetryable(it) {
			row = append(row, models.InlineKeyboardButton{
				Text: b.texts.T(loc, "bot.rq.retry_btn", map[string]any{"Order": order}), CallbackData: "rq:rt:" + it.Id.String(),
			})
		}
		if it.OrderId != nil {
			row = append(row, models.InlineKeyboardButton{
				Text: b.texts.T(loc, "bot.rq.order_btn", map[string]any{"Order": order}), CallbackData: "or:v:" + it.OrderId.String(),
			})
		}
		if len(row) > 0 {
			rows = append(rows, row)
		}
	}
	if pr := PagerRow(Pager{Prefix: "rq:l:" + filter, Page: page, Pages: pages},
		b.texts.T(loc, "bot.btn_prev", nil), b.texts.T(loc, "bot.btn_next", nil)); pr != nil {
		rows = append(rows, pr)
	}
	rows = append(rows, []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.btn_home", nil), CallbackData: "home"}})
	b.reply(ctx, chatID, editMsgID, clipMessage(sb.String(), maxMessageRunes), &models.InlineKeyboardMarkup{InlineKeyboard: rows})
}

// queueWhy is the one line that explains a refund that needs attention.
func queueWhy(it openapi.OrderRefundQueueItem) string {
	switch it.State {
	case "failed", "manual_review":
		return strings.TrimSpace(deref(it.FailureReason))
	}
	return ""
}

func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func (b *Bot) askQueueRetry(ctx context.Context, chatID int64, editMsgID *int, loc string, refundID uuid.UUID) {
	b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.rq.retry_ask", nil), &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: b.texts.T(loc, "bot.rq.retry_yes", nil), CallbackData: "rq:ry:" + refundID.String()}},
		{{Text: "« " + b.texts.T(loc, "bot.btn_back", nil), CallbackData: "rq:l:" + queueDefaultFilter + ":1"}},
	}})
}

func (b *Bot) doQueueRetry(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt string, refundID uuid.UUID) {
	loc := id.Locale()
	back := &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: "« " + b.texts.T(loc, "bot.btn_back", nil), CallbackData: "rq:l:" + queueDefaultFilter + ":1"}},
		{{Text: b.texts.T(loc, "bot.btn_home", nil), CallbackData: "home"}},
	}}
	res, err := b.arena.RetryRefund(ctx, jwt, id.Current.OrgID, refundID)
	switch {
	case err == nil:
		text := b.texts.T(loc, "bot.rq.retry_done", nil) + b.refundLines(loc, Esc(b.texts.T(loc, "bot.rq.provider_word", nil)), res.Refunds)
		b.reply(ctx, chatID, editMsgID, text, back)
	case IsAPIError(err, http.StatusConflict) && APIErrorCode(err) == codeNotRetryable:
		b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.rq.not_retryable", nil), back)
	case IsAPIError(err, http.StatusConflict):
		b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.ref.err_progress", map[string]any{"Num": "—"}), back)
	default:
		b.ecError(ctx, chatID, editMsgID, id, err, "rq:l:"+queueDefaultFilter+":1")
	}
}
