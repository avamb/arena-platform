package eventbot

// order_card.go — the card of one order (EC-05, spec 35 §5.6) and the
// cancellation of an unpaid one. Everything on the card comes from ONE call,
// GET .../orders/{id} (tickets with their EAN-13 and entry time, delivery,
// payment, channel, the unpaid reason); only the event's name and the date,
// which that answer carries as ids, are taken from the list row of the same
// order (the list is the one place the API joins them).
//
// The card shows the buyer's e-mail and phone. They are written to the
// private chat of the person who pressed the button — a card asked for in a
// group would show no contacts — and are never logged: nothing here puts a
// buyer field in a log line or a callback payload.
//
// Cancelling is irreversible (the hold is released, the order is closed), so
// it is never one tap: the button only opens a prompt that asks for the
// localized cancel word, the same one the Sessions dialog asks for, and a
// wrong word cancels nothing. Resending the tickets (EC-13) and refunds (the
// money stage) are not built: there are no buttons for them yet — they join
// orderCardKeyboard when their stages land.

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

// maxOrderTickets is how many ticket lines one card prints before it says
// "and N more" (an order is a handful of tickets; the cap keeps the message
// under Telegram's limit for the odd 40-ticket group booking).
const maxOrderTickets = 12

// orderMeta is what the card borrows from the list row of its order.
type orderMeta struct {
	EventName string
	StartAt   time.Time
	TZ        string
}

// ─── the card ─────────────────────────────────────────────────────────────────

// showOrderCard loads one order and draws its card. note is a line to put
// above it ("Order cancelled").
func (b *Bot) showOrderCard(ctx context.Context, chatID int64, editMsgID *int, from *models.User, id *Identity, jwt string, st ordersDialog, orderID uuid.UUID, note string) {
	orgID := id.Current.OrgID
	detail, err := b.arena.GetOrder(ctx, jwt, orgID, orderID)
	if err != nil {
		b.ecError(ctx, chatID, editMsgID, id, err, "or:b")
		return
	}
	meta := b.orderMeta(ctx, jwt, orgID, detail)

	st.CardID = &orderID
	if editMsgID != nil {
		st.MsgID = *editMsgID
	}
	b.saveOrdersState(ctx, from.ID, st, ordStepCard)

	loc := id.Locale()
	private := chatID == from.ID
	html := note + b.orderCardText(loc, detail, meta, private, false)
	plain := note + b.orderCardText(loc, detail, meta, private, true)
	kb := b.orderCardKeyboard(loc, detail)
	if err := b.replyChecked(ctx, chatID, editMsgID, clipMessage(html, maxMessageRunes), kb); err != nil && html != plain {
		// A client that refuses the call/WhatsApp links still gets the card.
		b.reply(ctx, chatID, editMsgID, clipMessage(plain, maxMessageRunes), kb)
	}
}

// orderMeta finds the order's event name, date and zone in the list row of the
// same order (searched by its number inside its own date). A failure only
// costs the event lines, never the card.
func (b *Bot) orderMeta(ctx context.Context, jwt string, orgID uuid.UUID, d openapi.OrderDetail) *orderMeta {
	session := d.SessionId
	page, err := b.arena.ListOrders(ctx, jwt, orgID, OrderListQuery{
		Q: "#" + strconv.FormatInt(d.SystemId, 10), SessionID: &session, Limit: 50,
	})
	if err != nil {
		b.logger.Warn("eventbot: order meta lookup failed", slog.String("order_id", d.Id.String()), slog.String("error", routeOnly(err)))
		return nil
	}
	for _, o := range page.Orders {
		if o.Id == d.Id {
			return &orderMeta{EventName: o.EventName, StartAt: o.SessionStartAt, TZ: o.SessionTimezone}
		}
	}
	return nil
}

// orderCardText is the card. plain leaves the call / WhatsApp links out
// (everything else is the same text). private is false in a group chat,
// where the buyer's contacts are not shown at all.
func (b *Bot) orderCardText(loc string, d openapi.OrderDetail, meta *orderMeta, private, plain bool) string {
	tz := ""
	if meta != nil {
		tz = meta.TZ
	}
	var sb strings.Builder

	statusWord := Esc(d.Status)
	if key := orderStatusKey(d.Status); key != "" {
		statusWord = b.texts.T(loc, key, nil)
	}
	sb.WriteString(b.texts.T(loc, "bot.ord.card_head", map[string]any{
		"Num": d.SystemId, "Chip": OrderChip(d.Status), "Status": statusWord,
		"Amount": OrderAmountHTML(d.Status, int64(d.Total), d.Currency, loc),
		"When":   Esc(FormatWhen(d.CreatedAt, tz)),
	}))

	if meta != nil && meta.EventName != "" {
		sb.WriteString(b.texts.T(loc, "bot.ord.card_event", map[string]any{
			"Name": Esc(meta.EventName), "Date": Esc(FormatWhen(meta.StartAt, meta.TZ)),
		}))
	}

	sb.WriteString(b.buyerBlock(loc, d, private, plain))

	if len(d.Tickets) > 0 {
		sb.WriteString(b.texts.T(loc, "bot.ord.tickets_head", map[string]any{"N": len(d.Tickets)}))
		for i, t := range d.Tickets {
			if i >= maxOrderTickets {
				sb.WriteString("\n" + b.texts.T(loc, "bot.ord.tickets_more", map[string]any{"N": len(d.Tickets) - i}))
				break
			}
			sb.WriteString("\n" + b.ticketLine(loc, t, tz))
		}
	} else if isPaidStatus(d.Status) {
		sb.WriteString(b.texts.T(loc, "bot.ord.no_tickets", nil))
	}

	channel := b.texts.T(loc, channelKindKey(d.Channel.Kind), nil)
	if name := strings.TrimSpace(d.Channel.Name); name != "" {
		channel += " (" + Esc(name) + ")"
	}
	sb.WriteString(b.texts.T(loc, "bot.ord.channel_line", map[string]any{"Channel": channel}))
	if d.Payment != nil && d.Payment.Provider != "" {
		sb.WriteString(b.texts.T(loc, "bot.ord.payment_line", map[string]any{"Provider": Esc(d.Payment.Provider)}))
	}
	if len(d.Tickets) > 0 {
		if key := deliveryKey(d.DeliveryState); key != "" {
			sb.WriteString("\n" + b.texts.T(loc, key, nil))
		}
	}

	if key := UnpaidReasonKey(d.Status, d.UnpaidReason); key != "" {
		sb.WriteString("\n\n" + b.texts.T(loc, key, nil))
		if msg := providerFailure(d.Payment); msg != "" {
			sb.WriteString("\n" + b.texts.T(loc, "bot.ord.reason_provider", map[string]any{"Msg": Esc(msg)}))
		}
	}
	return sb.String()
}

// providerFailure is the provider's own words for a failed payment: its
// message, else its code, cut to a line.
func providerFailure(p *openapi.OrderPaymentSummary) string {
	if p == nil {
		return ""
	}
	msg := ""
	switch {
	case p.FailureMessage != nil && strings.TrimSpace(*p.FailureMessage) != "":
		msg = *p.FailureMessage
	case p.FailureCode != nil:
		msg = *p.FailureCode
	}
	return cutRunes(strings.Join(strings.Fields(msg), " "), 200)
}

// buyerBlock is the buyer's name, e-mail and phone. The phone carries a call
// link and a WhatsApp link only when it is a valid E.164 number.
func (b *Bot) buyerBlock(loc string, d openapi.OrderDetail, private, plain bool) string {
	name := strings.TrimSpace(deref(d.BuyerName))
	email := strings.TrimSpace(deref(d.BuyerEmail))
	phone := strings.TrimSpace(deref(d.BuyerPhone))
	if !private {
		return b.texts.T(loc, "bot.ord.contacts_private", nil)
	}
	title := "<b>" + Esc(name) + "</b>"
	if name == "" {
		title = b.texts.T(loc, "bot.ord.buyer_unknown", nil)
	}
	var lines strings.Builder
	if email != "" {
		lines.WriteString("\n✉ " + Esc(email))
	}
	if phone != "" {
		html, text := PhoneHTML(phone, b.texts.T(loc, "bot.ord.phone_call", nil), b.texts.T(loc, "bot.ord.phone_wa", nil))
		if plain {
			html = text
		}
		lines.WriteString("\n📞 " + html)
	}
	return b.texts.T(loc, "bot.ord.card_buyer", map[string]any{"Name": title, "Contacts": lines.String()})
}

// ticketLine is one ticket: category, what was paid for it, the EAN-13 (in
// <code>, so a tap copies it), the seat, its status and the door entry.
func (b *Bot) ticketLine(loc string, t openapi.OrderTicketSummary, tz string) string {
	tier := "—"
	if t.TierName != nil && strings.TrimSpace(*t.TierName) != "" {
		tier = Esc(*t.TierName)
	}
	seat := ""
	if t.SeatLabel != nil && strings.TrimSpace(*t.SeatLabel) != "" {
		seat = b.texts.T(loc, "bot.ord.seat", map[string]any{"Label": Esc(*t.SeatLabel)})
	}
	status := Esc(t.Status)
	if key := ticketStatusKey(t.Status); key != "" {
		status = b.texts.T(loc, key, nil)
	}
	entered := ""
	if t.UsedAt != nil {
		entered = b.texts.T(loc, "bot.ord.entered", map[string]any{"When": Esc(FormatWhen(*t.UsedAt, tz))})
	}
	return b.texts.T(loc, "bot.ord.ticket_line", map[string]any{
		"Tier": tier, "Price": FormatMoney(t.Price, t.Currency, loc), "Code": Esc(t.Barcode),
		"Seat": seat, "Status": status, "Entered": entered,
	})
}

// orderCardKeyboard is the card's buttons: "Cancel unpaid order" for an order
// that is still waiting for payment, then Back to the list and Home. (Resend
// tickets, EC-13, and Refund, the money stage, are added here when they exist.)
func (b *Bot) orderCardKeyboard(loc string, d openapi.OrderDetail) *models.InlineKeyboardMarkup {
	var rows [][]models.InlineKeyboardButton
	if orderCancellable(d.Status) {
		rows = append(rows, []models.InlineKeyboardButton{{
			Text: b.texts.T(loc, "bot.ord.cancel_btn", nil), CallbackData: "or:c:" + d.Id.String(),
		}})
	}
	rows = append(rows, []models.InlineKeyboardButton{
		{Text: "« " + b.texts.T(loc, "bot.btn_back", nil), CallbackData: "or:b"},
		{Text: b.texts.T(loc, "bot.btn_home", nil), CallbackData: "home"},
	})
	return &models.InlineKeyboardMarkup{InlineKeyboard: rows}
}

// ─── cancelling an unpaid order ───────────────────────────────────────────────

// askCancelOrder opens the prompt that asks for the cancel word. Nothing is
// cancelled by it; an order that is not waiting for payment any more only
// shows its card again, with the reason.
func (b *Bot) askCancelOrder(ctx context.Context, chatID int64, editMsgID *int, from *models.User, id *Identity, jwt string, st ordersDialog, orderID uuid.UUID) {
	loc := id.Locale()
	detail, err := b.arena.GetOrder(ctx, jwt, id.Current.OrgID, orderID)
	if err != nil {
		b.ecError(ctx, chatID, editMsgID, id, err, "or:b")
		return
	}
	if !orderCancellable(detail.Status) {
		b.showOrderCard(ctx, chatID, editMsgID, from, id, jwt, st, orderID, b.texts.T(loc, "bot.ord.cancel_not_possible", nil)+"\n\n")
		return
	}
	st.CardID = &orderID
	if editMsgID != nil {
		st.MsgID = *editMsgID
	}
	b.saveOrdersState(ctx, from.ID, st, ordStepCancel)
	b.showCancelPrompt(ctx, chatID, editMsgID, loc, detail, "")
}

func (b *Bot) showCancelPrompt(ctx context.Context, chatID int64, editMsgID *int, loc string, d openapi.OrderDetail, note string) {
	text := note + b.texts.T(loc, "bot.ord.cancel_ask", map[string]any{
		"Num": d.SystemId, "Amount": FormatMoney(int64(d.Total), d.Currency, loc), "Word": b.sesCancelWord(loc),
	})
	b.reply(ctx, chatID, editMsgID, text, &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: "« " + b.texts.T(loc, "bot.btn_back", nil), CallbackData: "or:v:" + d.Id.String()}},
		{{Text: b.texts.T(loc, "bot.btn_home", nil), CallbackData: "home"}},
	}})
}

// cancelWordTyped handles the text typed while the cancel word is awaited.
// Only the word cancels; anything else leaves the order untouched and asks
// again.
func (b *Bot) cancelWordTyped(ctx context.Context, chatID int64, editMsgID *int, from *models.User, id *Identity, jwt string, st ordersDialog, text string) {
	loc := id.Locale()
	orgID := id.Current.OrgID
	if st.CardID == nil {
		b.leaveOrders(ctx, from.ID)
		return
	}
	orderID := *st.CardID
	if !b.sesIsCancelWord(loc, text) {
		detail, err := b.arena.GetOrder(ctx, jwt, orgID, orderID)
		if err != nil {
			b.ecError(ctx, chatID, editMsgID, id, err, "or:b")
			return
		}
		b.showCancelPrompt(ctx, chatID, editMsgID, loc, detail, b.texts.T(loc, "bot.ord.cancel_wrong", map[string]any{"Word": b.sesCancelWord(loc)})+"\n\n")
		return
	}
	err := b.arena.CancelOrder(ctx, jwt, orgID, orderID, "cancelled from the Telegram bot")
	switch {
	case err == nil:
		b.showOrderCard(ctx, chatID, editMsgID, from, id, jwt, st, orderID, b.texts.T(loc, "bot.ord.cancel_done", nil)+"\n\n")
	case IsAPIError(err, http.StatusConflict):
		// Paid, expired or cancelled by somebody else in the meantime.
		b.showOrderCard(ctx, chatID, editMsgID, from, id, jwt, st, orderID, b.texts.T(loc, "bot.ord.cancel_not_possible", nil)+"\n\n")
	default:
		b.ecError(ctx, chatID, editMsgID, id, err, "or:b")
	}
}

// ─── plumbing ─────────────────────────────────────────────────────────────────

// replyChecked is reply that reports whether the text reached the chat, so a
// caller with a plainer version of it can retry.
func (b *Bot) replyChecked(ctx context.Context, chatID int64, editMsgID *int, text string, kb *models.InlineKeyboardMarkup) error {
	if editMsgID != nil {
		params := &tgbot.EditMessageTextParams{ChatID: chatID, MessageID: *editMsgID, Text: text, ParseMode: models.ParseModeHTML}
		if kb != nil {
			params.ReplyMarkup = kb
		}
		_, err := b.tg.EditMessageText(ctx, params)
		if err == nil || strings.Contains(err.Error(), "message is not modified") {
			return nil
		}
	}
	return b.sendChecked(ctx, chatID, text, kb)
}

// routeOnly is an error's text for a log line. API errors carry a status and a
// code, never the request's query (arena_client.go routeOf).
func routeOnly(err error) string {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.Code
	}
	return err.Error()
}
