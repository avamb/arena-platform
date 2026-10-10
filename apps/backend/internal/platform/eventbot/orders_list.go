package eventbot

// orders_list.go — "Orders" (EC-04, spec 35 §5.5): the organization's orders
// in three tabs (Latest, Paid, Unpaid), five rows a page, a free-text search,
// and an optional filter to one event or one date. The search is NOT worked
// out here: whatever the person types goes to the API's single `q` parameter
// as it is, and the server decides whether it is a barcode, an order number,
// an e-mail, a phone or a name (horders/search.go classifyQuery).
//
// Where the person is — tab, search text, page, the scope, and the ids of the
// rows on screen — lives in bot_dialogs under kind "orders" (dialogs.go), so a
// bot restart keeps it and the buttons of the message already in the chat go
// on working. A row button carries the row's INDEX on the page (paging.go),
// resolved against the stored ids; the order card (order_card.go) shares the
// dialog (steps "card" and "cancel").
//
// Buyer data: rows show a buyer's name, the card shows e-mail and phone. All
// of it is written to the private chat of the person who asked and nowhere
// else — the search text (which may be an e-mail or a phone) is stored in the
// dialog row, never logged.

import (
	"context"
	"log/slog"
	"strings"

	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"
)

const (
	ordersDialogKind = "orders"
	// Steps: the list is on screen (a typed text is a search), an order card
	// is, or the cancel word of an unpaid order is awaited.
	ordStepList   = "list"
	ordStepCard   = "card"
	ordStepCancel = "cancel"
)

// ordersDialog is the screens' state, stored as the bot_dialogs row's JSON.
type ordersDialog struct {
	OrgID uuid.UUID `json:"org_id"`
	Tab   string    `json:"tab"`
	Query string    `json:"query,omitempty"`
	Page  int       `json:"page"`
	// EventID or SessionID narrow the list to one event or one date; BackEvent
	// is the event card the list's "Back" returns to; ScopeName is the raw
	// (unescaped) words that name the scope on screen.
	EventID   *uuid.UUID  `json:"event_id,omitempty"`
	SessionID *uuid.UUID  `json:"session_id,omitempty"`
	BackEvent *uuid.UUID  `json:"back_event,omitempty"`
	ScopeName string      `json:"scope_name,omitempty"`
	IDs       []uuid.UUID `json:"ids,omitempty"`     // the orders on the page on screen, in order
	CardID    *uuid.UUID  `json:"card_id,omitempty"` // the order whose card (or cancel prompt) is open
	MsgID     int         `json:"msg_id,omitempty"`
}

func newOrdersDialog(orgID uuid.UUID) ordersDialog {
	return ordersDialog{OrgID: orgID, Tab: ordTabRecent, Page: 1}
}

// ordersScope is what a list is narrowed to when it is opened from an event
// or from a date.
type ordersScope struct {
	EventID, SessionID, BackEvent *uuid.UUID
	Name                          string // raw words, escaped at render
}

func (s ordersScope) apply(st *ordersDialog) {
	st.EventID, st.SessionID, st.BackEvent, st.ScopeName = s.EventID, s.SessionID, s.BackEvent, s.Name
}

func validOrdersTab(t string) bool {
	return t == ordTabRecent || t == ordTabPaid || t == ordTabUnpaid
}

// tabFromCode maps the one-letter code of a tab press to the tab.
func tabFromCode(code string) (string, bool) {
	switch code {
	case "r":
		return ordTabRecent, true
	case "p":
		return ordTabPaid, true
	case "u":
		return ordTabUnpaid, true
	}
	return "", false
}

func tabCode(tab string) string {
	switch tab {
	case ordTabPaid:
		return "p"
	case ordTabUnpaid:
		return "u"
	}
	return "r"
}

// ─── state in bot_dialogs ─────────────────────────────────────────────────────

// loadOrdersState reads the dialog, or starts the default one when there is
// none, it ran out (expired is then true, once) or it belongs to another
// organization.
func (b *Bot) loadOrdersState(ctx context.Context, tg int64, orgID uuid.UUID) (st ordersDialog, step string, expired bool) {
	step, found, expired, err := b.dialogs.Load(ctx, tg, ordersDialogKind, &st)
	if err != nil {
		b.logger.Error("eventbot: orders dialog load failed", slog.Int64("telegram_user_id", tg), slog.String("error", err.Error()))
		return newOrdersDialog(orgID), "", false
	}
	if !found || st.OrgID != orgID || !validOrdersTab(st.Tab) {
		return newOrdersDialog(orgID), "", expired
	}
	if st.Page < 1 {
		st.Page = 1
	}
	return st, step, false
}

func (b *Bot) saveOrdersState(ctx context.Context, tg int64, st ordersDialog, step string) {
	orgID := st.OrgID
	if err := b.dialogs.Save(ctx, tg, &orgID, ordersDialogKind, step, st, dialogTTL); err != nil {
		b.logger.Error("eventbot: orders dialog save failed", slog.Int64("telegram_user_id", tg), slog.String("error", err.Error()))
	}
}

// leaveOrders ends the dialog: whoever opens another screen is no longer
// searching, so a text typed next is not taken for a search or a cancel word.
func (b *Bot) leaveOrders(ctx context.Context, tg int64) {
	if err := b.dialogs.Delete(ctx, tg, ordersDialogKind); err != nil {
		b.logger.Warn("eventbot: orders dialog clear failed", slog.Int64("telegram_user_id", tg), slog.String("error", err.Error()))
	}
}

// ─── entry ────────────────────────────────────────────────────────────────────

// showOrdersFresh opens the list on the Latest tab with no search, narrowed
// to the scope (the zero scope: every order of the organization).
func (b *Bot) showOrdersFresh(ctx context.Context, chatID int64, editMsgID *int, from *models.User, scope ordersScope) {
	id, jwt, ok := b.ecIdentity(ctx, chatID, editMsgID, from)
	if !ok {
		return
	}
	if !b.ordersAllowed(ctx, chatID, editMsgID, id) {
		return
	}
	st := newOrdersDialog(id.Current.OrgID)
	scope.apply(&st)
	b.renderOrders(ctx, chatID, editMsgID, id, jwt, st, "")
}

// ordersAllowed answers the "no rights" screen to a role that may not read
// orders (the buttons are not shown to it either).
func (b *Bot) ordersAllowed(ctx context.Context, chatID int64, editMsgID *int, id *Identity) bool {
	if canViewSales(id) {
		return true
	}
	loc := id.Locale()
	b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.ec.no_rights", nil), b.backKeyboard(loc, "home"))
	return false
}

// ─── the list ─────────────────────────────────────────────────────────────────

// renderOrders loads one page for the state and draws it. It stores the state
// — with the ids of the rows it shows — before answering, so a press on this
// very message finds them.
func (b *Bot) renderOrders(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt string, st ordersDialog, note string) {
	loc := id.Locale()
	query := OrderListQuery{Tab: st.Tab, Q: st.Query, SessionID: st.SessionID, EventID: st.EventID, Limit: listPageSize}
	fetch := func(page int) (OrderPage, error) {
		query.Offset = (page - 1) * listPageSize
		return b.arena.ListOrders(ctx, jwt, st.OrgID, query)
	}
	res, err := fetch(st.Page)
	if err == nil {
		// A page past the end (rows were cancelled, a stale button): the last.
		if pages := PagesFor(res.TotalCount, listPageSize); st.Page > pages {
			st.Page = pages
			res, err = fetch(st.Page)
		}
	}
	if err != nil {
		b.ecError(ctx, chatID, editMsgID, id, err, "home")
		return
	}
	pages := PagesFor(res.TotalCount, listPageSize)
	st.IDs = st.IDs[:0]
	for _, o := range res.Orders {
		st.IDs = append(st.IDs, o.Id)
	}
	st.CardID = nil
	if editMsgID != nil {
		st.MsgID = *editMsgID
	}
	b.saveOrdersState(ctx, id.Link.TelegramUserID, st, ordStepList)

	scope := ""
	if st.ScopeName != "" {
		scope = b.texts.T(loc, "bot.ord.scope_line", map[string]any{"Name": Esc(st.ScopeName)})
	}
	search := ""
	if st.Query != "" {
		search = "\n" + b.texts.T(loc, "bot.ec.search_line", map[string]any{"Query": Esc(st.Query), "N": res.TotalCount})
	}
	text := note + b.texts.T(loc, "bot.ord.list_title", map[string]any{
		"Org": Esc(id.Current.OrgName), "Scope": scope, "Tab": b.texts.T(loc, "bot.ord.tab_"+st.Tab, nil),
		"Total": res.TotalCount, "Page": st.Page, "Pages": pages, "Search": search,
		"Legend": b.texts.T(loc, "bot.ord.legend", nil),
	})
	if len(res.Orders) == 0 {
		text += "\n\n" + b.emptyOrdersText(loc, st)
	}

	mark := func(tab string) string {
		label := b.texts.T(loc, "bot.ord.tab_"+tab, nil)
		if st.Tab == tab {
			return "✔ " + label
		}
		return label
	}
	rows := [][]models.InlineKeyboardButton{{
		{Text: mark(ordTabRecent), CallbackData: "or:t:r"},
		{Text: mark(ordTabPaid), CallbackData: "or:t:p"},
		{Text: mark(ordTabUnpaid), CallbackData: "or:t:u"},
	}}
	for i, o := range res.Orders {
		rows = append(rows, []models.InlineKeyboardButton{{Text: OrderRowLabel(o, loc), CallbackData: ItemCallback("or:o", i)}})
	}
	if nav := PagerRow(Pager{Prefix: "or:p", Page: st.Page, Pages: pages},
		b.texts.T(loc, "bot.btn_prev", nil), b.texts.T(loc, "bot.btn_next", nil)); nav != nil {
		rows = append(rows, nav)
	}
	if st.Query != "" {
		rows = append(rows, []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.ec.clear_search_btn", nil), CallbackData: "or:x"}})
	}
	if st.ScopeName != "" {
		rows = append(rows, []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.ord.scope_all_btn", nil), CallbackData: "or:a"}})
	}
	last := []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.btn_home", nil), CallbackData: "home"}}
	if st.BackEvent != nil {
		last = append([]models.InlineKeyboardButton{{Text: "« " + b.texts.T(loc, "bot.btn_back", nil), CallbackData: "ec:o:" + st.BackEvent.String()}}, last...)
	}
	rows = append(rows, last)
	b.reply(ctx, chatID, editMsgID, clipMessage(text, maxMessageRunes), &models.InlineKeyboardMarkup{InlineKeyboard: rows})
}

func (b *Bot) emptyOrdersText(loc string, st ordersDialog) string {
	if st.Query != "" {
		return b.texts.T(loc, "bot.ord.search_none", map[string]any{"Query": Esc(st.Query)})
	}
	return b.texts.T(loc, "bot.ord.empty_"+st.Tab, nil)
}

// ─── presses ──────────────────────────────────────────────────────────────────

// ordersCallback handles every "or:<data>" press:
//
//	or:new            the list, fresh            or:e:<event>  of one event
//	or:s:<session>    of one date                or:a          drop the scope
//	or:t:<r|p|u>      tab                        or:p:<n>      page
//	or:x              clear the search           or:b          back to the list
//	or:o:<i>          open row i of the page     or:v:<order>  the card of an order
//	or:c:<order>      cancel an unpaid order (asks for the word)
func (b *Bot) ordersCallback(ctx context.Context, chatID int64, msgID int, from *models.User, data string) {
	id, jwt, ok := b.ecIdentity(ctx, chatID, &msgID, from)
	if !ok {
		return
	}
	if !b.ordersAllowed(ctx, chatID, &msgID, id) {
		return
	}
	loc := id.Locale()
	orgID := id.Current.OrgID
	kind, arg, _ := strings.Cut(data, ":")

	switch kind {
	case "r":
		// "Resend tickets" (EC-13): its own dialog, so the list behind the card stays.
		b.resendCallback(ctx, chatID, msgID, from, arg)
		return
	case "new":
		b.renderOrders(ctx, chatID, &msgID, id, jwt, newOrdersDialog(orgID), "")
		return
	case "e", "s":
		target, err := uuid.Parse(arg)
		if err != nil {
			return
		}
		scope, ok := b.scopeFor(ctx, chatID, &msgID, id, jwt, kind, target)
		if !ok {
			return
		}
		st := newOrdersDialog(orgID)
		scope.apply(&st)
		b.renderOrders(ctx, chatID, &msgID, id, jwt, st, "")
		return
	case "v", "c":
		target, err := uuid.Parse(arg)
		if err != nil {
			return
		}
		// The ids are in the payload: a card keeps meaning THAT order however
		// many other cards the chat holds, so an expired dialog does not stop
		// them (it only loses the list behind the card).
		st, _, _ := b.loadOrdersState(ctx, from.ID, orgID)
		if kind == "v" {
			b.showOrderCard(ctx, chatID, &msgID, from, id, jwt, st, target, "")
		} else {
			b.askCancelOrder(ctx, chatID, &msgID, from, id, jwt, st, target)
		}
		return
	}

	st, _, expired := b.loadOrdersState(ctx, from.ID, orgID)
	if expired {
		// The screen ran out while the person was away: say so once, and
		// start the default list rather than act on a press made on stale rows.
		b.renderOrders(ctx, chatID, &msgID, id, jwt, newOrdersDialog(orgID), b.texts.T(loc, "bot.dialog_expired", nil)+"\n\n")
		return
	}
	switch kind {
	case "b":
	case "t":
		tab, ok := tabFromCode(arg)
		if !ok {
			return
		}
		st.Tab, st.Page = tab, 1
	case "p":
		st.Page = ParsePage(arg)
	case "x":
		st.Query, st.Page = "", 1
	case "a":
		ordersScope{}.apply(&st)
		st.Page = 1
	case "o":
		i, ok := ParseIndex(arg, len(st.IDs))
		if !ok {
			// The row is gone from the stored page (a stale message): show
			// the list as it is now instead of opening the wrong order.
			b.renderOrders(ctx, chatID, &msgID, id, jwt, st, "")
			return
		}
		b.showOrderCard(ctx, chatID, &msgID, from, id, jwt, st, st.IDs[i], "")
		return
	default:
		return
	}
	b.renderOrders(ctx, chatID, &msgID, id, jwt, st, "")
}

// scopeFor names what a scoped list is about ("Swan Lake", "Swan Lake · 15.10.2026
// 20:00") and the event card its Back returns to. A foreign or unknown id is
// the usual "not found".
func (b *Bot) scopeFor(ctx context.Context, chatID int64, msgID *int, id *Identity, jwt, kind string, target uuid.UUID) (ordersScope, bool) {
	orgID := id.Current.OrgID
	if kind == "s" {
		sum, err := b.arena.SessionSummary(ctx, jwt, orgID, target)
		if err != nil {
			b.ecError(ctx, chatID, msgID, id, err, "home")
			return ordersScope{}, false
		}
		tz := ""
		if sum.Session.VenueTimezone != nil {
			tz = *sum.Session.VenueTimezone
		}
		event := sum.Session.EventId
		return ordersScope{
			SessionID: &target, BackEvent: &event,
			Name: sum.Session.EventName + " · " + FormatWhen(sum.Session.StartAt, tz),
		}, true
	}
	events, err := b.arena.ListEvents(ctx, jwt, orgID)
	if err != nil {
		b.ecError(ctx, chatID, msgID, id, err, "home")
		return ordersScope{}, false
	}
	for _, e := range events {
		if e.Id == target {
			event := target
			return ordersScope{EventID: &event, BackEvent: &event, Name: e.Name}, true
		}
	}
	b.reply(ctx, chatID, msgID, b.texts.T(id.Locale(), "bot.ec.not_found", nil), b.backKeyboard(id.Locale(), "home"))
	return ordersScope{}, false
}

// ─── typed text ───────────────────────────────────────────────────────────────

// ordersText takes a typed text while an Orders screen is open: as the search
// while the list is on screen, as the cancel word while that is awaited. It
// reports whether the text was taken. An expired screen is reported once,
// like every dialog (spec 35 §4.1).
func (b *Bot) ordersText(ctx context.Context, chatID int64, from *models.User, text string) bool {
	var st ordersDialog
	step, found, expired, err := b.dialogs.Load(ctx, from.ID, ordersDialogKind, &st)
	if err != nil {
		b.logger.Error("eventbot: orders dialog load failed", slog.Int64("telegram_user_id", from.ID), slog.String("error", err.Error()))
		return false
	}
	if expired {
		loc := NormalizeLocale(from.LanguageCode)
		if id, _, err := b.resolveIdentity(ctx, from.ID); err == nil {
			loc = id.Locale()
		}
		b.send(ctx, chatID, b.texts.T(loc, "bot.dialog_expired", nil), &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
			{{Text: b.texts.T(loc, "bot.ord.btn", nil), CallbackData: "or:new"}},
			{{Text: b.texts.T(loc, "bot.btn_home", nil), CallbackData: "home"}},
		}})
		return true
	}
	if !found || (step != ordStepList && step != ordStepCancel) {
		return false
	}
	id, jwt, err := b.resolveIdentity(ctx, from.ID)
	if err != nil || id.Current == nil || id.Current.OrgID != st.OrgID || !canViewSales(id) {
		b.leaveOrders(ctx, from.ID)
		return false
	}
	var edit *int
	if st.MsgID != 0 {
		edit = &st.MsgID
	}
	if step == ordStepCancel {
		b.cancelWordTyped(ctx, chatID, edit, from, id, jwt, st, text)
		return true
	}
	st.Query = cutRunes(strings.Join(strings.Fields(text), " "), maxQueryRunes)
	st.Page = 1
	b.renderOrders(ctx, chatID, edit, id, jwt, st, "")
	return true
}
