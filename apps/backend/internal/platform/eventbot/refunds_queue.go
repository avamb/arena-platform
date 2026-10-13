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
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

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
	queueDefaultFilter  = "review"
	codeNotRetryable    = "refund.not_retryable"
	codeAlreadyReplaced = "refund.already_replaced"
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

// queueRetryable says whether the row gets a "Retry" button: the server's own
// verdict (it provably moved no money and was not retried yet). The retry
// route still decides when the button is pressed.
func queueRetryable(it openapi.OrderRefundQueueItem) bool {
	return it.Retryable != nil && *it.Retryable
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
			b.askQueueRetry(ctx, chatID, &msgID, id, jwt, refundID)
			return
		}
		b.startQueueRetry(ctx, chatID, msgID, id, refundID)
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

// askQueueRetry is the first of two steps: it names the order, the ticket and
// the amount that would be sent again and sends nothing. The second step is a
// button that carries the refund's id.
func (b *Bot) askQueueRetry(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt string, refundID uuid.UUID) {
	loc := id.Locale()
	list := &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: "« " + b.texts.T(loc, "bot.btn_back", nil), CallbackData: "rq:l:" + queueDefaultFilter + ":1"}},
	}}
	it, err := b.arena.RefundGet(ctx, jwt, id.Current.OrgID, refundID)
	if err != nil {
		b.ecError(ctx, chatID, editMsgID, id, err, "rq:l:"+queueDefaultFilter+":1")
		return
	}
	if !queueRetryable(it) {
		// It changed since the list was drawn (retried, or accepted late).
		b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.rq.not_retryable", nil), list)
		return
	}
	amount := FormatMoney(it.Amount, it.Currency, loc)
	b.reply(ctx, chatID, editMsgID, b.queueRetryAskText(loc, it), &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: b.texts.T(loc, "bot.rq.retry_yes", map[string]any{"Amount": amount}), CallbackData: "rq:ry:" + refundID.String()}},
		{{Text: b.texts.T(loc, "bot.rq.retry_no", nil), CallbackData: "rq:l:" + queueDefaultFilter + ":1"}},
	}})
}

// queueRetryAskText is the retry question. A retry inherits the first
// refund's choices (Engine.Retry), so the question names them: whether the
// ticket is cancelled and whether the buyer gets the letter.
func (b *Bot) queueRetryAskText(loc string, it openapi.OrderRefundQueueItem) string {
	order, ticket := "—", "—"
	if it.OrderNumber != nil {
		order = "№" + strconv.FormatInt(*it.OrderNumber, 10)
	}
	if it.TicketNumber != nil {
		ticket = "№" + strconv.FormatInt(*it.TicketNumber, 10)
	}
	ticketLine := b.texts.T(loc, "bot.rq.retry_keeps_ticket", nil)
	// The retry takes the operation's choice (retry_cancels_ticket), which
	// can differ from the refund row's own flag; an older server that does
	// not send it falls back to the row.
	cancels := it.CancelTicket
	if it.RetryCancelsTicket != nil {
		cancels = *it.RetryCancelsTicket
	}
	if cancels {
		ticketLine = b.texts.T(loc, "bot.rq.retry_cancels_ticket", nil)
	}
	letterLine := b.texts.T(loc, "bot.rq.retry_letter", nil)
	if it.NotifyBuyer != nil && !*it.NotifyBuyer {
		letterLine = b.texts.T(loc, "bot.rq.retry_no_letter", nil)
	}
	return b.texts.T(loc, "bot.rq.retry_ask", map[string]any{
		"Order": order, "Tickets": ticket, "Amount": FormatMoney(it.Amount, it.Currency, loc),
		"TicketRule": ticketLine, "Letter": letterLine,
	})
}

// startQueueRetry sends the retry OFF the bot's single update worker: the
// server drives the new refund before it answers, which may take minutes,
// and the bot must keep answering everyone meanwhile. A second press while
// the same retry is under way only says so (the server would answer the same
// operation anyway: its key is derived from the refund).
func (b *Bot) startQueueRetry(ctx context.Context, chatID int64, msgID int, id *Identity, refundID uuid.UUID) {
	loc := id.Locale()
	if _, busy := b.retrying.LoadOrStore(refundID, struct{}{}); busy {
		b.reply(ctx, chatID, nil, b.texts.T(loc, "bot.rq.still_sending", nil), nil)
		return
	}
	b.reply(ctx, chatID, &msgID, b.texts.T(loc, "bot.ref.sending", nil), nil)
	started := b.goBackground("queue retry", func() {
		defer b.retrying.Delete(refundID)
		wctx, cancel := context.WithTimeout(detached(ctx), refundSendWindow)
		defer cancel()
		b.doQueueRetry(wctx, chatID, msgID, id, refundID)
	}, func() {
		// Whether the retry went out is unknown: say so, and let the queue
		// show its state (a second press replays the same operation).
		cctx, cancel := context.WithTimeout(detached(ctx), 30*time.Second)
		defer cancel()
		b.reply(cctx, chatID, &msgID, b.texts.T(loc, "bot.ref.err_timeout", nil), &models.InlineKeyboardMarkup{
			InlineKeyboard: [][]models.InlineKeyboardButton{{{Text: "« " + b.texts.T(loc, "bot.btn_back", nil),
				CallbackData: "rq:l:" + queueDefaultFilter + ":1"}}}})
	})
	if !started {
		// The bot is shutting down: nothing was sent.
		b.retrying.Delete(refundID)
		b.reply(ctx, chatID, &msgID, b.texts.T(loc, "bot.ref.err_unavailable", nil), nil)
	}
}

// codePartialNotSupported: the provider returns only the whole payment at
// once and part of it is already back.
const codePartialNotSupported = "refund.partial_not_supported"

// queueRetryErrorKey picks the text for a refused retry.
func (b *Bot) queueRetryErrorKey(ae *APIError) string {
	switch {
	case ae.Status == http.StatusConflict && ae.Code == codeNotRetryable:
		return "bot.rq.not_retryable"
	case ae.Status == http.StatusConflict && ae.Code == codeAlreadyReplaced:
		return "bot.rq.already_replaced"
	case ae.Status == http.StatusConflict && ae.Code == codeRefundProgress:
		return "bot.rq.err_progress"
	case ae.Status == http.StatusConflict && ae.Code == codeRefundNotFit:
		return "bot.rq.err_ticket"
	case ae.Status == http.StatusConflict && ae.Code == codeRefundScanned:
		return scannedErrorKey(ae)
	case ae.Status == http.StatusConflict:
		// Any other refusal: the refund or its order changed. Never "order
		// № changed" without a number.
		return "bot.rq.err_refused"
	case ae.Status == http.StatusNotFound || ae.Status == http.StatusBadRequest:
		return "bot.rq.err_refused"
	case ae.Status == http.StatusUnprocessableEntity && ae.Code == codePartialNotSupported:
		return "bot.rq.err_partial"
	}
	return b.refundErrorKey(ae)
}

func (b *Bot) doQueueRetry(ctx context.Context, chatID int64, msgID int, id *Identity, refundID uuid.UUID) {
	loc := id.Locale()
	edit := &msgID
	back := &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: "« " + b.texts.T(loc, "bot.btn_back", nil), CallbackData: "rq:l:" + queueDefaultFilter + ":1"}},
		{{Text: b.texts.T(loc, "bot.btn_home", nil), CallbackData: "home"}},
	}}
	var (
		res openapi.CreateOrderRefundsResponse
		err error
	)
	// An answer that never came is asked for again: the retry operation has a
	// key derived from the refund, so this reads the one already started.
	// Every attempt carries a freshly minted token.
	for attempt := 0; attempt <= refundReplayAttempts; attempt++ {
		jwt, merr := b.minter.Mint(id.Link.UserID)
		if merr != nil {
			err = merr
			break
		}
		res, err = b.arena.RetryRefund(ctx, jwt, id.Current.OrgID, refundID)
		var ae *APIError
		if err == nil || errors.As(err, &ae) || ctx.Err() != nil {
			break
		}
		select {
		case <-ctx.Done():
		case <-time.After(refundReplayPause):
		}
	}
	rctx, cancel := context.WithTimeout(detached(ctx), 30*time.Second)
	defer cancel()
	var ae *APIError
	switch {
	case err == nil:
		text := b.texts.T(loc, "bot.rq.retry_done", nil) + b.refundLines(loc, Esc(b.texts.T(loc, "bot.rq.provider_word", nil)), res.Refunds)
		b.reply(rctx, chatID, edit, text, back)
	case errors.As(err, &ae):
		key := b.queueRetryErrorKey(ae)
		if key == "" {
			key = "bot.ref.err_timeout"
		}
		b.reply(rctx, chatID, edit, b.texts.T(loc, key, map[string]any{"Num": "—", "Provider": Esc(b.texts.T(loc, "bot.rq.provider_word", nil))}), back)
	default:
		// No answer at all: the retry may be under way. Never "failed".
		b.logger.Warn("eventbot: refund retry got no answer", slog.String("error", err.Error()))
		b.reply(rctx, chatID, edit, b.texts.T(loc, "bot.ref.err_timeout", nil), back)
	}
}
