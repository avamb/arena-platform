package eventbot

// event_summary.go — the "Summary" screen of a session or of a whole event
// (EC-06, spec 35 §5.7): tickets, orders, places, the money of each currency
// down to the net, and the figures of every category. The card shows the
// headline numbers; this screen is the money page an organizer opens at the
// end of a sale, with the exports right under it.

import (
	"context"
	"strings"

	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

// summaryView is what the screen draws, whichever summary it came from.
type summaryView struct {
	Title   string // event name
	Scope   string // the date and venue, or "all dates (N)"
	Figures salesFigures
	// Back is the callback the "Back" button returns to.
	Back string
	// Buttons are the export buttons of the screen.
	Buttons [][]models.InlineKeyboardButton
}

// summaryText renders the screen.
func (b *Bot) summaryText(loc string, v summaryView) string {
	f := v.Figures
	var sb strings.Builder
	sb.WriteString(b.texts.T(loc, "bot.ec.sum_title", map[string]any{"Name": Esc(v.Title), "Scope": v.Scope}))
	sb.WriteString("\n\n")
	sb.WriteString(b.texts.T(loc, "bot.ec.sum_tickets", map[string]any{
		"Active": f.Tickets.Active, "Cancelled": f.Tickets.Cancelled, "Used": f.Tickets.Used, "Transferred": f.Tickets.Transferred,
	}))
	var paid, pending int64
	for _, m := range f.Money {
		paid += m.PaidOrders
		pending += m.PendingOrders
	}
	sb.WriteString("\n" + b.texts.T(loc, "bot.ec.sum_orders", map[string]any{"Paid": paid, "Pending": pending}))
	sb.WriteString("\n" + b.texts.T(loc, "bot.ec.sum_places", map[string]any{
		"Sold": f.Places.Sold, "Available": f.Places.Available, "Held": f.Places.Held,
		"Unavailable": f.Places.Unavailable, "Total": f.Places.Total,
	}))
	if f.Comp.Tickets > 0 {
		sb.WriteString("\n" + b.texts.T(loc, "bot.ec.comp_line", map[string]any{"N": f.Comp.Tickets}))
	}
	shown := 0
	for _, m := range f.Money {
		if !hasMoney(m) && m.Pending == 0 {
			continue
		}
		count, _, external := refundTotals(f.Refunds, m.Currency)
		pendingText, externalText := "", ""
		if m.Pending > 0 {
			pendingText = FormatMoney(m.Pending, m.Currency, loc)
		}
		if external > 0 {
			externalText = FormatMoney(external, m.Currency, loc)
		}
		sb.WriteString("\n\n" + b.texts.T(loc, "bot.ec.sum_money", map[string]any{
			"Paid": FormatMoney(m.Paid, m.Currency, loc), "Orders": m.PaidOrders,
			"Discount": FormatMoney(m.Discount, m.Currency, loc), "Charge": FormatMoney(m.ServiceCharge, m.Currency, loc),
			"RefundCount": count, "Refunded": FormatMoney(m.Refunded, m.Currency, loc), "External": externalText,
			"Net": FormatMoney(m.Net, m.Currency, loc), "Pending": pendingText, "PendingOrders": m.PendingOrders,
		}))
		shown++
	}
	if shown == 0 {
		sb.WriteString("\n\n" + b.texts.T(loc, "bot.money_none", nil))
	}
	if t := b.tiersText(loc, f.Tiers); t != "" {
		sb.WriteString("\n\n" + t)
	}
	return clipMessage(sb.String(), maxMessageRunes)
}

func (b *Bot) summaryKeyboard(loc string, v summaryView) *models.InlineKeyboardMarkup {
	rows := append([][]models.InlineKeyboardButton{}, v.Buttons...)
	rows = append(rows, []models.InlineKeyboardButton{
		{Text: "« " + b.texts.T(loc, "bot.btn_back", nil), CallbackData: v.Back},
		{Text: b.texts.T(loc, "bot.btn_home", nil), CallbackData: "home"},
	})
	return &models.InlineKeyboardMarkup{InlineKeyboard: rows}
}

// showEventSummary is the summary of a whole event (the card's "Summary").
func (b *Bot) showEventSummary(ctx context.Context, chatID int64, editMsgID *int, from *models.User, eventID uuid.UUID) {
	id, jwt, ok := b.ecIdentity(ctx, chatID, editMsgID, from)
	if !ok {
		return
	}
	loc := id.Locale()
	back := "ec:o:" + eventID.String()
	if !canViewSales(id) {
		b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.ec.no_rights", nil), b.backKeyboard(loc, back))
		return
	}
	sum, err := b.arena.EventSummary(ctx, jwt, id.Current.OrgID, eventID)
	if err != nil {
		b.ecError(ctx, chatID, editMsgID, id, err, "el:b")
		return
	}
	v := summaryView{
		Title:   sum.Event.Name,
		Scope:   b.texts.T(loc, "bot.ec.scope_all", map[string]any{"N": len(sum.Sessions)}),
		Figures: figuresFromEvent(sum),
		Back:    back,
		Buttons: [][]models.InlineKeyboardButton{{
			{Text: b.texts.T(loc, "bot.ec.csv_btn", nil), CallbackData: "ec:ce:" + eventID.String()},
		}},
	}
	b.reply(ctx, chatID, editMsgID, b.summaryText(loc, v), b.summaryKeyboard(loc, v))
}

// showSessionSummary is the summary of one date. back is the callback of the
// screen the person came from; "" returns to the event card with its dates
// expanded (where the per-date buttons live).
func (b *Bot) showSessionSummary(ctx context.Context, chatID int64, editMsgID *int, from *models.User, sessionID uuid.UUID, back string) {
	id, jwt, ok := b.ecIdentity(ctx, chatID, editMsgID, from)
	if !ok {
		return
	}
	loc := id.Locale()
	if !canViewSales(id) {
		b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.ec.no_rights", nil), b.backKeyboard(loc, "el:b"))
		return
	}
	sum, err := b.arena.SessionSummary(ctx, jwt, id.Current.OrgID, sessionID)
	if err != nil {
		b.ecError(ctx, chatID, editMsgID, id, err, "el:b")
		return
	}
	if back == "" {
		back = "ec:o:" + sum.Session.EventId.String() + ":1"
	}
	v := summaryView{
		Title:   sum.Session.EventName,
		Scope:   b.sessionScope(loc, sum),
		Figures: figuresFromSession(sum),
		Back:    back,
		Buttons: [][]models.InlineKeyboardButton{{
			{Text: b.texts.T(loc, "bot.ec.csv_btn", nil), CallbackData: "ec:cs:" + sessionID.String()},
			{Text: b.texts.T(loc, "bot.ec.summary_csv_btn", nil), CallbackData: "ec:cm:" + sessionID.String()},
		}},
	}
	b.reply(ctx, chatID, editMsgID, b.summaryText(loc, v), b.summaryKeyboard(loc, v))
}

// sessionScope is "15.10.2026 20:00 Venue" for a session summary.
func (b *Bot) sessionScope(loc string, sum openapi.SessionSummary) string {
	tz := ""
	if sum.Session.VenueTimezone != nil {
		tz = *sum.Session.VenueTimezone
	}
	scope := "<b>" + Esc(FormatWhen(sum.Session.StartAt, tz)) + "</b>"
	if sum.Session.Status == "cancelled" {
		scope += " (" + b.texts.T(loc, "bot.session_cancelled", nil) + ")"
	}
	if v := venueOf(sum.Session.VenueName); v != "" {
		scope += " " + v
	}
	return scope
}
