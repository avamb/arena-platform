package eventbot

// categories.go — the "Categories" screens of a session (EC-15, spec 35 §6.6):
// the list of the date's ticket categories with their places, the card of one
// and what can be changed on it — closed/open, the price for NEW purchases,
// the sale window (a calendar) and the quantity. Reached from the date's card
// in the Sessions dialog (sessions_dialog.go) and from the event card of an
// event with a single date.
//
// The rules are arena-api's, the bot shows their answers: a category cannot go
// below the places sold or held (409 tier.quantity_below_used), a seated
// category's places come from the plan (409 tier.seated_category), a window
// must end after it starts. What the bot decides itself is what NOT to offer:
//
//   - no delete: a selling category is closed, never removed;
//   - a seated category has price, window and open/close, no quantity;
//   - a category that belongs to a chain (ticket_tier_chain: it hands its free
//     places to the next one by date or when its step sells out) keeps only its
//     price: opening, closing, resizing and moving its window by hand would
//     fight the automatic hand-over, so a note sends the organizer to the
//     event editor instead;
//   - a free or pay-what-you-want category has no price to type.
//
// A price change reaches NEW purchases only: carts already started keep the
// price they were quoted and orders already paid are never repriced (the
// server's rule, AB-48 step 10); the confirmation says so. When the category
// follows a price schedule, the window that is active now is the one changed
// (PUT price-schedule), otherwise the base price (PATCH price_amount).
//
// State lives in ONE bot_dialogs row of kind "category" (dialogs.go): the date,
// the list page, the category whose card is open, the calendar scratch. Presses
// are "ct:<kind>:<arg>"; a row of the list is named by its INDEX on the page
// (paging.go), anything that changes a category carries its UUID:
//
//	ct:e:<event>   the event's only date      ct:b   the list        ct:p:<n>   a page
//	ct:o:<i>       open row i                 ct:v:<id>  the card
//	ct:op:<id>     open (sell again)          ct:cl:<id> close (stop NEW sales)
//	ct:pr:<id>     ask a price                ct:ps:<id> confirm it
//	ct:qt:<id>     ask a quantity
//	ct:wn:<id>     the window screen          ct:ws:<id> / ct:we:<id>  calendar for its start / end
//	ct:wx:<id>     remove the limit being asked (start or end)
//	ct:cal:… ct:d:… ct:dc:… ct:noop   the wizard calendar's own presses

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

const categoryDialogKind = "category"

// The dialog's steps.
const (
	catStepList    = "list"
	catStepCard    = "card"
	catStepPrice   = "price"         // a price is awaited as text
	catStepPriceOK = "price_confirm" // a price waits for its press
	catStepQty     = "qty"           // a quantity is awaited as text
	catStepWindow  = "window"        // the window screen
	catStepWStart  = "wstart"        // calendar: the first day of sale
	catStepWEnd    = "wend"          // calendar: the last day of sale
)

// maxCategoryQuantity bounds a typed quantity; a bigger number is a typo.
const maxCategoryQuantity = 1_000_000

// categoryDialog is the screens' state, stored as the bot_dialogs row's JSON.
type categoryDialog struct {
	OrgID     uuid.UUID `json:"org_id"`
	EventID   uuid.UUID `json:"event_id"`
	SessionID uuid.UUID `json:"session_id"`
	EventName string    `json:"event_name"`
	// When is the date as the venue's clock shows it; Tz its zone; SalesEnd the
	// moment the whole date stops selling (RFC 3339), which caps every window.
	When     string `json:"when"`
	Tz       string `json:"tz,omitempty"`
	SalesEnd string `json:"sales_end,omitempty"`
	MsgID    int    `json:"msg_id,omitempty"`
	Page     int    `json:"page"`
	// IDs are the categories on the page on screen.
	IDs []uuid.UUID `json:"ids,omitempty"`
	// CardID names the category of the open card, question or confirmation.
	CardID *uuid.UUID `json:"card_id,omitempty"`
	// Pending is the price (minor units) waiting for its confirmation press.
	Pending int64 `json:"pending,omitempty"`
	// Cal is the throwaway draft the wizard's calendar runs on.
	Cal *Draft `json:"cal,omitempty"`
}

// canCategories reports whether the person may manage categories: the owner,
// the manager and the platform operator hold tier.read/tier.update (the agent
// role holds none). The API still decides every call.
func canCategories(id *Identity) bool { return canViewSales(id) }

// ─── what a category allows ───────────────────────────────────────────────────

// categoryCaps is what the bot offers on one category's card.
type categoryCaps struct {
	Toggle, Price, Window, Qty bool
	Chain, Seated, NotFixed    bool
}

// categoryCapsFor decides what to offer for t among its session's categories.
func categoryCapsFor(t openapi.TicketTierItem, all []openapi.TicketTierItem) categoryCaps {
	chain := t.NextTierId != nil || t.SellLimit != nil
	for _, o := range all {
		if o.NextTierId != nil && *o.NextTierId == t.Id {
			chain = true
		}
	}
	fixed := string(t.PricingMode) == "fixed"
	kind := ""
	if t.Kind != nil {
		kind = *t.Kind
	}
	return categoryCaps{
		Toggle: !chain, Price: fixed, Window: !chain, Qty: kind == "ga" && !chain,
		Chain: chain, Seated: kind == "seated", NotFixed: !fixed,
	}
}

// categoryOpen reports whether the category sells (an absent flag is open).
func categoryOpen(t openapi.TicketTierItem) bool { return t.IsOpen == nil || *t.IsOpen }

// categoryPriceNow is the price buyers pay now: the scheduled one when a window
// is active, else the base price.
func categoryPriceNow(t openapi.TicketTierItem) int64 {
	if t.CurrentPrice != nil {
		return *t.CurrentPrice
	}
	return t.PriceAmount
}

func int32or0(p *int32) int32 {
	if p == nil {
		return 0
	}
	return *p
}

// activeWindow is the index of the window that is in force at now, or -1.
func activeWindow(ws []openapi.TierPriceWindow, now time.Time) int {
	for i, w := range ws {
		if !w.ValidFrom.After(now) && (w.ValidTo == nil || now.Before(*w.ValidTo)) {
			return i
		}
	}
	return -1
}

// withWindowPrice is the schedule with window i priced at amount.
func withWindowPrice(ws []openapi.TierPriceWindow, i int, amount int64) []openapi.TierPriceWindow {
	out := append([]openapi.TierPriceWindow(nil), ws...)
	out[i].PriceAmount = amount
	return out
}

// parseCategoryQuantity reads a whole number of places from 1 to
// maxCategoryQuantity.
func parseCategoryQuantity(raw string) (int32, bool) {
	s := strings.NewReplacer(" ", "", " ", "").Replace(strings.TrimSpace(raw))
	n, ok := digitsValue(s)
	if !ok || n < 1 || n > maxCategoryQuantity {
		return 0, false
	}
	return int32(n), true // #nosec G115 -- bounded above by maxCategoryQuantity
}

// categoryZone is the venue's zone, UTC when unknown.
func categoryZone(tz string) *time.Location {
	if tz != "" {
		if l, err := time.LoadLocation(tz); err == nil {
			return l
		}
	}
	return time.UTC
}

// windowEdge turns a calendar day into the instant a window edge falls on, in
// the venue's zone: the first day of sale starts at its midnight, the last day
// "inclusive" ends at the midnight after it (the wizard's own convention).
func windowEdge(iso, tz string, inclusiveEnd bool) (time.Time, bool) {
	day, err := time.ParseInLocation(isoLayout, iso, categoryZone(tz))
	if err != nil {
		return time.Time{}, false
	}
	if inclusiveEnd {
		day = day.AddDate(0, 0, 1)
	}
	return day, true
}

// windowEdgeLabel prints a window edge as the organizer typed it: a bare date
// for a midnight (the last day for an end), the date and clock otherwise.
func windowEdgeLabel(t time.Time, tz string, isEnd bool) string {
	local := t.In(categoryZone(tz))
	if local.Hour() == 0 && local.Minute() == 0 && local.Second() == 0 {
		if isEnd {
			local = local.AddDate(0, 0, -1)
		}
		// allow:timeformat: date shown in a chat, not a wire timestamp
		return local.Format("02.01.2006")
	}
	return FormatWhen(t, tz)
}

// ─── state in bot_dialogs ─────────────────────────────────────────────────────

// loadCategoryState reads the dialog. found is false when there is none or it
// belongs to another organization; expired is true exactly once after it ran
// out.
func (b *Bot) loadCategoryState(ctx context.Context, tg int64, orgID uuid.UUID) (st categoryDialog, step string, found, expired bool) {
	step, found, expired, err := b.dialogs.Load(ctx, tg, categoryDialogKind, &st)
	if err != nil {
		b.logger.Error("eventbot: category dialog load failed", slog.Int64("telegram_user_id", tg), slog.String("error", err.Error()))
		return categoryDialog{}, "", false, false
	}
	if found && st.OrgID != orgID {
		return categoryDialog{}, "", false, false
	}
	if st.Page < 1 {
		st.Page = 1
	}
	return st, step, found, expired
}

func (b *Bot) saveCategoryState(ctx context.Context, tg int64, st categoryDialog, step string) {
	orgID := st.OrgID
	if err := b.dialogs.Save(ctx, tg, &orgID, categoryDialogKind, step, st, dialogTTL); err != nil {
		b.logger.Error("eventbot: category dialog save failed", slog.Int64("telegram_user_id", tg), slog.String("error", err.Error()))
	}
}

// leaveCategories ends the dialog: whoever opens another screen is no longer
// answering a category question, so a text typed next is not taken for one.
func (b *Bot) leaveCategories(ctx context.Context, tg int64) {
	if err := b.dialogs.Delete(ctx, tg, categoryDialogKind); err != nil {
		b.logger.Warn("eventbot: category dialog clear failed", slog.Int64("telegram_user_id", tg), slog.String("error", err.Error()))
	}
}

// ctKeyboard turns wizard-style buttons into inline buttons whose callbacks
// carry the "ct:" prefix.
func ctKeyboard(rows [][]Button) *models.InlineKeyboardMarkup {
	out := make([][]models.InlineKeyboardButton, 0, len(rows))
	for _, r := range rows {
		line := make([]models.InlineKeyboardButton, 0, len(r))
		for _, bt := range r {
			line = append(line, models.InlineKeyboardButton{Text: bt.Label, CallbackData: "ct:" + bt.Data})
		}
		out = append(out, line)
	}
	return &models.InlineKeyboardMarkup{InlineKeyboard: out}
}

func (b *Bot) ctBtn(loc, key, data string, vals map[string]any) Button {
	return Button{Label: b.texts.T(loc, key, vals), Data: data}
}

func (b *Bot) ctHomeRow(loc string) []Button {
	return []Button{{Label: b.texts.T(loc, "bot.btn_home", nil), Data: "home"}}
}

// ─── entry points ─────────────────────────────────────────────────────────────

// categoryEventRow is the event card's button, for an event with one date.
func (b *Bot) categoryEventRow(loc string, id *Identity, eventID uuid.UUID) []models.InlineKeyboardButton {
	if !canCategories(id) {
		return nil
	}
	return []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.cat.btn", nil), CallbackData: "ct:e:" + eventID.String()}}
}

// categoriesOpen starts the dialog on one date of an event and shows its
// categories. The date is looked up in the event's own list, so a session that
// is not the event's (or the organization's) reads as not found.
func (b *Bot) categoriesOpen(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt string, eventID, sessionID uuid.UUID, eventName string) {
	loc := id.Locale()
	orgID := id.Current.OrgID
	sessions, err := b.arena.ListSessions(ctx, jwt, orgID, eventID)
	if err != nil {
		b.ecError(ctx, chatID, editMsgID, id, err, "home")
		return
	}
	var found *openapi.SessionItem
	for i := range sessions {
		if sessions[i].Id == sessionID {
			found = &sessions[i]
		}
	}
	if found == nil {
		b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.ec.not_found", nil), b.backKeyboard(loc, "home"))
		return
	}
	st := categoryDialog{OrgID: orgID, EventID: eventID, SessionID: sessionID, EventName: eventName, Page: 1}
	if sum, err := b.arena.SessionSummary(ctx, jwt, orgID, sessionID); err == nil && sum.Session.VenueTimezone != nil {
		st.Tz = *sum.Session.VenueTimezone
	}
	st.When = FormatWhen(found.StartAt, st.Tz)
	if !found.SalesEndAt.IsZero() {
		st.SalesEnd = found.SalesEndAt.UTC().Format(time.RFC3339)
	}
	b.categoryRenderList(ctx, chatID, editMsgID, id, jwt, st, "")
}

// categoryEventOpen is the event card's button: the categories of the event's
// only date. An event with several dates sends the person to the Sessions
// screen to pick one.
func (b *Bot) categoryEventOpen(ctx context.Context, chatID int64, editMsgID *int, from *models.User, id *Identity, jwt string, eventID uuid.UUID) {
	loc := id.Locale()
	orgID := id.Current.OrgID
	events, err := b.arena.ListEvents(ctx, jwt, orgID)
	if err != nil {
		b.ecError(ctx, chatID, editMsgID, id, err, "home")
		return
	}
	name, ok := "", false
	for _, e := range events {
		if e.Id == eventID {
			name, ok = e.Name, true
		}
	}
	if !ok {
		b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.ec.not_found", nil), b.backKeyboard(loc, "home"))
		return
	}
	sessions, err := b.arena.ListSessions(ctx, jwt, orgID, eventID)
	if err != nil {
		b.ecError(ctx, chatID, editMsgID, id, err, "home")
		return
	}
	var live []openapi.SessionItem
	for _, s := range sessions {
		if string(s.Status) != "cancelled" {
			live = append(live, s)
		}
	}
	if len(live) != 1 {
		b.sessionsOpen(ctx, chatID, editMsgID, from, eventID)
		return
	}
	b.categoriesOpen(ctx, chatID, editMsgID, id, jwt, eventID, live[0].Id, name)
}

// categoryCallback handles every "ct:<data>" press.
func (b *Bot) categoryCallback(ctx context.Context, chatID int64, msgID int, from *models.User, data string) {
	id, jwt, ok := b.ecIdentity(ctx, chatID, &msgID, from)
	if !ok {
		return
	}
	loc := id.Locale()
	if !canCategories(id) {
		b.reply(ctx, chatID, &msgID, b.texts.T(loc, "bot.ec.no_rights", nil), b.backKeyboard(loc, "home"))
		return
	}
	orgID := id.Current.OrgID
	kind, arg, _ := strings.Cut(data, ":")

	switch kind {
	case "e":
		eventID, err := uuid.Parse(arg)
		if err != nil {
			return
		}
		b.categoryEventOpen(ctx, chatID, &msgID, from, id, jwt, eventID)
		return
	case "home":
		b.leaveCategories(ctx, from.ID)
		b.showHome(ctx, chatID, &msgID, from, "")
		return
	}

	st, step, found, expired := b.loadCategoryState(ctx, from.ID, orgID)
	if !found {
		// The screen ran out (or a restart found none and the press is old): say
		// so once, and offer the way back rather than act on stale rows.
		key := "bot.cat.gone"
		if expired {
			key = "bot.dialog_expired"
		}
		b.reply(ctx, chatID, &msgID, b.texts.T(loc, key, nil), b.backKeyboard(loc, "events:1"))
		return
	}
	st.MsgID = msgID

	if isCalData(data) {
		if step == catStepWStart || step == catStepWEnd {
			b.categoryDateInput(ctx, chatID, &msgID, id, jwt, st, step, "", data)
		}
		return
	}

	switch kind {
	case "back":
		// Up one level: the date's own screen in the Sessions dialog.
		b.leaveCategories(ctx, from.ID)
		b.sessionsOpen(ctx, chatID, &msgID, from, st.EventID)
	case "b":
		b.categoryRenderList(ctx, chatID, &msgID, id, jwt, st, "")
	case "p":
		st.Page = ParsePage(arg)
		b.categoryRenderList(ctx, chatID, &msgID, id, jwt, st, "")
	case "o":
		i, ok := ParseIndex(arg, len(st.IDs))
		if !ok {
			// The row is gone from the stored page (a stale message): show the
			// list as it is now instead of opening the wrong category.
			b.categoryRenderList(ctx, chatID, &msgID, id, jwt, st, "")
			return
		}
		b.categoryShowCard(ctx, chatID, &msgID, id, jwt, st, st.IDs[i], "")
	case "v", "op", "cl", "pr", "ps", "qt", "wn", "ws", "we", "wx":
		tierID, err := uuid.Parse(arg)
		if err != nil {
			return
		}
		b.categoryTierPress(ctx, chatID, &msgID, id, jwt, st, step, kind, tierID)
	}
}

// ─── the list ─────────────────────────────────────────────────────────────────

// categoryPlaces is the places part of a category's line.
func (b *Bot) categoryPlaces(loc string, t openapi.TicketTierItem) string {
	if t.Quantity == nil {
		return b.texts.T(loc, "bot.cat.places_none", nil)
	}
	return b.texts.T(loc, "bot.cat.places", map[string]any{
		"Sold": int32or0(t.Sold), "Held": int32or0(t.Held), "Available": int32or0(t.Available), "Total": int32or0(t.Quantity),
	})
}

// categoryLine is one category of the list.
func (b *Bot) categoryLine(loc string, t openapi.TicketTierItem) string {
	chip := "●"
	if !categoryOpen(t) {
		chip = "✕"
	}
	price := b.texts.T(loc, "bot.cat.price_free", nil)
	switch string(t.PricingMode) {
	case "fixed":
		price = FormatMoney(categoryPriceNow(t), t.Currency, loc)
	case "pwyw":
		price = b.texts.T(loc, "bot.cat.price_pwyw", nil)
	}
	return b.texts.T(loc, "bot.cat.line", map[string]any{
		"Chip": chip, "Name": Esc(t.Name), "Price": Esc(price), "Value": b.categoryPlaces(loc, t),
	})
}

// categoryRenderList loads the date's categories, draws one page and stores
// the ids of the rows it shows.
func (b *Bot) categoryRenderList(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt string, st categoryDialog, note string) {
	loc := id.Locale()
	tiers, err := b.arena.ListTiers(ctx, jwt, st.OrgID, st.EventID, st.SessionID)
	if err != nil {
		b.ecError(ctx, chatID, editMsgID, id, err, "home")
		return
	}
	total := int64(len(tiers))
	pages := PagesFor(total, listPageSize)
	if st.Page < 1 {
		st.Page = 1
	}
	if st.Page > pages {
		st.Page = pages
	}
	start := (st.Page - 1) * listPageSize
	end := start + listPageSize
	if end > len(tiers) {
		end = len(tiers)
	}
	pageItems := tiers[start:end]
	st.IDs = st.IDs[:0]
	for _, t := range pageItems {
		st.IDs = append(st.IDs, t.Id)
	}
	st.CardID, st.Cal, st.Pending = nil, nil, 0
	if editMsgID != nil {
		st.MsgID = *editMsgID
	}
	b.saveCategoryState(ctx, id.Link.TelegramUserID, st, catStepList)

	text := note + b.texts.T(loc, "bot.cat.list_title", map[string]any{
		"Name": Esc(st.EventName), "When": Esc(st.When), "Total": total, "Page": st.Page, "Pages": pages,
	})
	if len(pageItems) == 0 {
		text += "\n\n" + b.texts.T(loc, "bot.cat.list_empty", nil)
	} else {
		for _, t := range pageItems {
			text += "\n\n" + b.categoryLine(loc, t)
		}
		text += "\n\n" + b.texts.T(loc, "bot.cat.list_hint", nil)
	}
	var rows [][]Button
	for i, t := range pageItems {
		chip := "●"
		if !categoryOpen(t) {
			chip = "✕"
		}
		rows = append(rows, []Button{{Label: chip + " " + truncate(t.Name, 44), Data: ItemCallback("o", i)}})
	}
	if nav := PagerRow(Pager{Prefix: "p", Page: st.Page, Pages: pages}, b.texts.T(loc, "bot.btn_prev", nil), b.texts.T(loc, "bot.btn_next", nil)); nav != nil {
		line := make([]Button, 0, len(nav))
		for _, bt := range nav {
			data := bt.CallbackData
			if data == calNoop {
				data = "noop"
			}
			line = append(line, Button{Label: bt.Text, Data: data})
		}
		rows = append(rows, line)
	}
	rows = append(rows,
		[]Button{{Label: "« " + b.texts.T(loc, "bot.btn_back", nil), Data: "back"}},
		b.ctHomeRow(loc))
	b.reply(ctx, chatID, editMsgID, clipMessage(text, maxMessageRunes), ctKeyboard(rows))
}

// ─── the card ─────────────────────────────────────────────────────────────────

// categoryFind picks one category out of a fresh list.
func (b *Bot) categoryFind(ctx context.Context, jwt string, st categoryDialog, tierID uuid.UUID) (openapi.TicketTierItem, []openapi.TicketTierItem, bool, error) {
	tiers, err := b.arena.ListTiers(ctx, jwt, st.OrgID, st.EventID, st.SessionID)
	if err != nil {
		return openapi.TicketTierItem{}, nil, false, err
	}
	for _, t := range tiers {
		if t.Id == tierID {
			return t, tiers, true, nil
		}
	}
	return openapi.TicketTierItem{}, tiers, false, nil
}

// categoryShowCard reads the category afresh and draws its card.
func (b *Bot) categoryShowCard(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt string, st categoryDialog, tierID uuid.UUID, note string) {
	t, tiers, found, err := b.categoryFind(ctx, jwt, st, tierID)
	if err != nil {
		b.ecError(ctx, chatID, editMsgID, id, err, "events:1")
		return
	}
	if !found {
		b.categoryRenderList(ctx, chatID, editMsgID, id, jwt, st, b.texts.T(id.Locale(), "bot.cat.gone_one", nil)+"\n\n")
		return
	}
	b.categoryRenderCard(ctx, chatID, editMsgID, id, st, t, tiers, note)
}

// categoryWindowLabel is the category's sale window as the card says it.
func (b *Bot) categoryWindowLabel(loc string, t openapi.TicketTierItem, tz string) string {
	switch {
	case t.SaleWindowStart != nil && t.SaleWindowEnd != nil:
		return b.texts.T(loc, "bot.cat.window_both", map[string]any{
			"Min": Esc(windowEdgeLabel(*t.SaleWindowStart, tz, false)), "Max": Esc(windowEdgeLabel(*t.SaleWindowEnd, tz, true)),
		})
	case t.SaleWindowStart != nil:
		return b.texts.T(loc, "bot.cat.window_from", map[string]any{"Date": Esc(windowEdgeLabel(*t.SaleWindowStart, tz, false))})
	case t.SaleWindowEnd != nil:
		return b.texts.T(loc, "bot.cat.window_until", map[string]any{"Date": Esc(windowEdgeLabel(*t.SaleWindowEnd, tz, true))})
	}
	return b.texts.T(loc, "bot.cat.window_none", nil)
}

// categoryCardText is the card: state, price, places, window and the notes
// that explain what is not offered.
func (b *Bot) categoryCardText(loc string, st categoryDialog, t openapi.TicketTierItem, all []openapi.TicketTierItem) string {
	caps := categoryCapsFor(t, all)
	stateKey := "bot.cat.state_open"
	if !categoryOpen(t) {
		stateKey = "bot.cat.state_closed"
	}
	var sb strings.Builder
	sb.WriteString(b.texts.T(loc, "bot.cat.card_head", map[string]any{
		"Name": Esc(t.Name), "State": b.texts.T(loc, stateKey, nil), "When": Esc(st.When),
	}))
	if caps.NotFixed {
		key := "bot.cat.note_free"
		if string(t.PricingMode) == "pwyw" {
			key = "bot.cat.note_pwyw"
		}
		sb.WriteString("\n" + b.texts.T(loc, key, nil))
	} else {
		sb.WriteString("\n" + b.texts.T(loc, "bot.cat.card_price", map[string]any{"Price": Esc(FormatMoney(categoryPriceNow(t), t.Currency, loc))}))
		if t.NextPriceChangeAt != nil {
			sb.WriteString("\n" + b.texts.T(loc, "bot.cat.card_next_price", map[string]any{"When": Esc(FormatWhen(*t.NextPriceChangeAt, st.Tz))}))
		}
	}
	sb.WriteString("\n" + b.texts.T(loc, "bot.cat.card_places", map[string]any{"Value": b.categoryPlaces(loc, t)}))
	sb.WriteString("\n" + b.texts.T(loc, "bot.cat.card_window", map[string]any{"Value": b.categoryWindowLabel(loc, t, st.Tz)}))
	if st.SalesEnd != "" {
		if end, err := time.Parse(time.RFC3339, st.SalesEnd); err == nil {
			sb.WriteString("\n" + b.texts.T(loc, "bot.cat.card_session_end", map[string]any{"When": Esc(FormatWhen(end, st.Tz))}))
		}
	}
	if caps.Chain {
		next := ""
		if t.NextTierId != nil {
			for _, o := range all {
				if o.Id == *t.NextTierId {
					next = o.Name
				}
			}
		}
		key := "bot.cat.note_chain_target"
		if next != "" {
			key = "bot.cat.note_chain"
		}
		sb.WriteString("\n\n" + b.texts.T(loc, key, map[string]any{"Name": Esc(next)}))
	}
	if caps.Seated {
		sb.WriteString("\n\n" + b.texts.T(loc, "bot.cat.note_seated", nil))
	}
	return sb.String()
}

func (b *Bot) categoryRenderCard(ctx context.Context, chatID int64, editMsgID *int, id *Identity, st categoryDialog, t openapi.TicketTierItem, all []openapi.TicketTierItem, note string) {
	loc := id.Locale()
	st.CardID, st.Cal, st.Pending = &t.Id, nil, 0
	if editMsgID != nil {
		st.MsgID = *editMsgID
	}
	b.saveCategoryState(ctx, id.Link.TelegramUserID, st, catStepCard)

	caps := categoryCapsFor(t, all)
	tid := t.Id.String()
	var rows [][]Button
	var line []Button
	if caps.Toggle {
		if categoryOpen(t) {
			line = append(line, b.ctBtn(loc, "bot.cat.close_btn", "cl:"+tid, nil))
		} else {
			line = append(line, b.ctBtn(loc, "bot.cat.open_btn", "op:"+tid, nil))
		}
	}
	if caps.Price {
		line = append(line, b.ctBtn(loc, "bot.cat.price_btn", "pr:"+tid, nil))
	}
	if len(line) > 0 {
		rows = append(rows, line)
	}
	line = nil
	if caps.Window {
		line = append(line, b.ctBtn(loc, "bot.cat.window_btn", "wn:"+tid, nil))
	}
	if caps.Qty {
		line = append(line, b.ctBtn(loc, "bot.cat.qty_btn", "qt:"+tid, nil))
	}
	if len(line) > 0 {
		rows = append(rows, line)
	}
	rows = append(rows, []Button{{Label: "« " + b.texts.T(loc, "bot.btn_back", nil), Data: "b"}}, b.ctHomeRow(loc))
	b.reply(ctx, chatID, editMsgID, clipMessage(note+b.categoryCardText(loc, st, t, all), maxMessageRunes), ctKeyboard(rows))
}

// ─── presses that name a category ─────────────────────────────────────────────

func (b *Bot) categoryTierPress(ctx context.Context, chatID int64, msgID *int, id *Identity, jwt string, st categoryDialog, step, kind string, tierID uuid.UUID) {
	loc := id.Locale()
	t, tiers, found, err := b.categoryFind(ctx, jwt, st, tierID)
	if err != nil {
		b.ecError(ctx, chatID, msgID, id, err, "events:1")
		return
	}
	if !found {
		b.categoryRenderList(ctx, chatID, msgID, id, jwt, st, b.texts.T(loc, "bot.cat.gone_one", nil)+"\n\n")
		return
	}
	caps := categoryCapsFor(t, tiers)
	switch kind {
	case "v":
		b.categoryRenderCard(ctx, chatID, msgID, id, st, t, tiers, "")
	case "op", "cl":
		if !caps.Toggle {
			b.categoryRenderCard(ctx, chatID, msgID, id, st, t, tiers, "")
			return
		}
		open := kind == "op"
		updated, err := b.arena.PatchTier(ctx, jwt, st.OrgID, st.EventID, st.SessionID, tierID, map[string]any{"is_open": open})
		if err != nil {
			b.categoryFail(ctx, chatID, msgID, id, st, t, tiers, err)
			return
		}
		doneKey := "bot.cat.closed_done"
		if open {
			doneKey = "bot.cat.opened_done"
		}
		b.categoryAfterWrite(ctx, chatID, msgID, id, jwt, st, updated, b.texts.T(loc, doneKey, nil)+"\n\n")
	case "pr":
		if !caps.Price {
			b.categoryRenderCard(ctx, chatID, msgID, id, st, t, tiers, "")
			return
		}
		st.CardID, st.Pending = &t.Id, 0
		b.saveCategoryState(ctx, id.Link.TelegramUserID, st, catStepPrice)
		b.reply(ctx, chatID, msgID, b.texts.T(loc, "bot.cat.q_price", map[string]any{
			"Name": Esc(t.Name), "Price": Esc(FormatMoney(categoryPriceNow(t), t.Currency, loc)), "Currency": Esc(strings.ToUpper(t.Currency)),
		}), ctKeyboard([][]Button{b.ctCardBack(loc, t.Id)}))
	case "ps":
		// Only the confirmation the dialog is waiting for, for THIS category.
		if step != catStepPriceOK || st.CardID == nil || *st.CardID != t.Id || st.Pending < 1 || !caps.Price {
			b.categoryRenderCard(ctx, chatID, msgID, id, st, t, tiers, "")
			return
		}
		b.categoryApplyPrice(ctx, chatID, msgID, id, jwt, st, t, tiers, st.Pending)
	case "qt":
		if !caps.Qty {
			b.categoryRenderCard(ctx, chatID, msgID, id, st, t, tiers, "")
			return
		}
		st.CardID = &t.Id
		b.saveCategoryState(ctx, id.Link.TelegramUserID, st, catStepQty)
		b.reply(ctx, chatID, msgID, b.texts.T(loc, "bot.cat.q_qty", map[string]any{
			"Name": Esc(t.Name), "Value": b.categoryPlaces(loc, t), "Limit": int32or0(t.Sold) + int32or0(t.Held),
		}), ctKeyboard([][]Button{b.ctCardBack(loc, t.Id)}))
	case "wn", "ws", "we", "wx":
		if !caps.Window {
			b.categoryRenderCard(ctx, chatID, msgID, id, st, t, tiers, "")
			return
		}
		b.categoryWindowPress(ctx, chatID, msgID, id, jwt, st, step, kind, t, tiers)
	}
}

// ctCardBack is the one "back to the card" row of a question.
func (b *Bot) ctCardBack(loc string, tierID uuid.UUID) []Button {
	return []Button{b.ctBtn(loc, "bot.cat.cancel_btn", "v:"+tierID.String(), nil)}
}

// categoryAfterWrite re-reads the list after a write and shows the card with
// the note, so the card is never drawn from the answer of the write alone.
func (b *Bot) categoryAfterWrite(ctx context.Context, chatID int64, msgID *int, id *Identity, jwt string, st categoryDialog, updated openapi.TicketTierItem, note string) {
	b.categoryShowCard(ctx, chatID, msgID, id, jwt, st, updated.Id, note)
}

// categoryErrKey maps a refusal to the message that says plainly what to do.
// "" means the API's answer has no words of its own.
func categoryErrKey(err error) string {
	switch APIErrorCode(err) {
	case tierCodeBelowUsed:
		return "bot.cat.err_qty_below"
	case tierCodeSeated:
		return "bot.cat.err_qty_seated"
	case tierCodeInvalidWindow:
		return "bot.cat.err_window"
	case tierCodeInvalidCap, tierCodeCapRequired:
		return "bot.cat.err_qty"
	case tierCodeInvalidPrice:
		return "bot.cat.err_price"
	}
	return ""
}

// categoryFail answers a refused write: a refusal the bot has words for keeps
// the card with the reason above it, anything else is the generic screen.
func (b *Bot) categoryFail(ctx context.Context, chatID int64, msgID *int, id *Identity, st categoryDialog, t openapi.TicketTierItem, tiers []openapi.TicketTierItem, err error) {
	loc := id.Locale()
	key := categoryErrKey(err)
	if key == "" {
		b.ecError(ctx, chatID, msgID, id, err, "events:1")
		return
	}
	floor := int32or0(t.Sold) + int32or0(t.Held)
	b.categoryRenderCard(ctx, chatID, msgID, id, st, t, tiers, b.texts.T(loc, key, map[string]any{"Limit": floor})+"\n\n")
}

// ─── price ────────────────────────────────────────────────────────────────────

// categoryApplyPrice writes the new price: the active scheduled window when
// the category follows a schedule that covers now, the base price otherwise.
func (b *Bot) categoryApplyPrice(ctx context.Context, chatID int64, msgID *int, id *Identity, jwt string, st categoryDialog, t openapi.TicketTierItem, tiers []openapi.TicketTierItem, amount int64) {
	loc := id.Locale()
	sched, err := b.arena.TierScheduleOf(ctx, jwt, st.OrgID, st.EventID, st.SessionID, t.Id)
	if err != nil {
		b.ecError(ctx, chatID, msgID, id, err, "events:1")
		return
	}
	doneKey := "bot.cat.price_saved"
	if i := activeWindow(sched.Windows, time.Now()); i >= 0 {
		err = b.arena.PutTierSchedule(ctx, jwt, st.OrgID, st.EventID, st.SessionID, t.Id, withWindowPrice(sched.Windows, i, amount))
		doneKey = "bot.cat.price_saved_window"
	} else {
		_, err = b.arena.PatchTier(ctx, jwt, st.OrgID, st.EventID, st.SessionID, t.Id, map[string]any{"price_amount": amount})
	}
	if err != nil {
		b.categoryFail(ctx, chatID, msgID, id, st, t, tiers, err)
		return
	}
	note := b.texts.T(loc, doneKey, map[string]any{"Price": Esc(FormatMoney(amount, t.Currency, loc))}) + "\n\n"
	b.categoryShowCard(ctx, chatID, msgID, id, jwt, st, t.Id, note)
}

// ─── window ───────────────────────────────────────────────────────────────────

// categoryWindowPress handles the window screen and its two calendars.
func (b *Bot) categoryWindowPress(ctx context.Context, chatID int64, msgID *int, id *Identity, jwt string, st categoryDialog, step, kind string, t openapi.TicketTierItem, tiers []openapi.TicketTierItem) {
	loc := id.Locale()
	switch kind {
	case "wn":
		st.CardID, st.Cal = &t.Id, nil
		b.saveCategoryState(ctx, id.Link.TelegramUserID, st, catStepWindow)
		tid := t.Id.String()
		b.reply(ctx, chatID, msgID, b.texts.T(loc, "bot.cat.window_screen", map[string]any{
			"Name": Esc(t.Name), "Value": b.categoryWindowLabel(loc, t, st.Tz),
		}), ctKeyboard([][]Button{
			{b.ctBtn(loc, "bot.cat.window_from_btn", "ws:"+tid, nil), b.ctBtn(loc, "bot.cat.window_until_btn", "we:"+tid, nil)},
			b.ctCardBack(loc, t.Id),
		}))
	case "ws", "we":
		st.CardID = &t.Id
		st.Cal = &Draft{Version: draftSchemaVersion, Step: stSDate, Sessions: []DraftSession{{Timezone: st.Tz}}}
		next := catStepWStart
		if kind == "we" {
			next = catStepWEnd
		}
		b.categoryShowCalendar(ctx, chatID, msgID, id, st, t, next, "")
	case "wx":
		// "Remove the limit" of the calendar being asked.
		if step != catStepWStart && step != catStepWEnd {
			b.categoryRenderCard(ctx, chatID, msgID, id, st, t, tiers, "")
			return
		}
		b.categoryApplyWindow(ctx, chatID, msgID, id, jwt, st, t, tiers, step, "")
	}
}

// categoryShowCalendar draws the date question of one window edge.
func (b *Bot) categoryShowCalendar(ctx context.Context, chatID int64, msgID *int, id *Identity, st categoryDialog, t openapi.TicketTierItem, step, prefix string) {
	loc := id.Locale()
	b.saveCategoryState(ctx, id.Link.TelegramUserID, st, step)
	key, current := "bot.cat.q_wstart", ""
	if step == catStepWEnd {
		key = "bot.cat.q_wend"
	}
	switch {
	case step == catStepWStart && t.SaleWindowStart != nil:
		current = windowEdgeLabel(*t.SaleWindowStart, st.Tz, false)
	case step == catStepWEnd && t.SaleWindowEnd != nil:
		current = windowEdgeLabel(*t.SaleWindowEnd, st.Tz, true)
	}
	if current == "" {
		current = b.texts.T(loc, "bot.cat.window_none", nil)
	}
	question := b.texts.T(loc, key, map[string]any{"Name": Esc(t.Name), "Value": Esc(current)})
	if step == catStepWEnd && st.SalesEnd != "" {
		if end, err := time.Parse(time.RFC3339, st.SalesEnd); err == nil {
			question += "\n" + b.texts.T(loc, "bot.cat.cap_note", map[string]any{"When": Esc(FormatWhen(end, st.Tz))})
		}
	}
	nav := func(rows ...[]Button) [][]Button {
		return append(rows,
			[]Button{b.ctBtn(loc, "bot.cat.window_remove_btn", "wx:"+t.Id.String(), nil)},
			[]Button{b.ctBtn(loc, "bot.cat.cancel_btn", "wn:"+t.Id.String(), nil)})
	}
	scr := b.wizard.dateQuestion(loc, st.Cal, prefix, question, nav)
	b.reply(ctx, chatID, msgID, scr.Text, ctKeyboard(scr.Buttons))
}

// categoryDateInput applies a calendar press or a typed date at one of the two
// window questions; a chosen day is saved at once.
func (b *Bot) categoryDateInput(ctx context.Context, chatID int64, msgID *int, id *Identity, jwt string, st categoryDialog, step, text, data string) {
	loc := id.Locale()
	if st.Cal == nil || st.CardID == nil {
		return
	}
	t, tiers, found, err := b.categoryFind(ctx, jwt, st, *st.CardID)
	if err != nil {
		b.ecError(ctx, chatID, msgID, id, err, "events:1")
		return
	}
	if !found {
		b.categoryRenderList(ctx, chatID, msgID, id, jwt, st, b.texts.T(loc, "bot.cat.gone_one", nil)+"\n\n")
		return
	}
	out, handled, note := b.wizard.dateInput(loc, st.Cal, text, data)
	if handled {
		b.categoryShowCalendar(ctx, chatID, msgID, id, st, t, step, noteLine(note))
		return
	}
	iso, ok := ParseDate(out)
	if !ok {
		b.categoryShowCalendar(ctx, chatID, msgID, id, st, t, step, b.texts.T(loc, "bot.wz.err_date", nil)+"\n\n")
		return
	}
	b.categoryApplyWindow(ctx, chatID, msgID, id, jwt, st, t, tiers, step, iso)
}

// categoryApplyWindow saves one edge of the window (iso "" removes it).
func (b *Bot) categoryApplyWindow(ctx context.Context, chatID int64, msgID *int, id *Identity, jwt string, st categoryDialog, t openapi.TicketTierItem, tiers []openapi.TicketTierItem, step, iso string) {
	loc := id.Locale()
	key := "sale_window_start"
	if step == catStepWEnd {
		key = "sale_window_end"
	}
	var value any
	if iso != "" {
		edge, ok := windowEdge(iso, st.Tz, step == catStepWEnd)
		if !ok {
			b.categoryShowCalendar(ctx, chatID, msgID, id, st, t, step, b.texts.T(loc, "bot.wz.err_date", nil)+"\n\n")
			return
		}
		value = edge.UTC().Format(time.RFC3339)
	}
	updated, err := b.arena.PatchTier(ctx, jwt, st.OrgID, st.EventID, st.SessionID, t.Id, map[string]any{key: value})
	if err != nil {
		if categoryErrKey(err) == "bot.cat.err_window" {
			// The other edge is in the way: say so and keep the question open.
			b.categoryShowCalendar(ctx, chatID, msgID, id, st, t, step, b.texts.T(loc, "bot.cat.err_window", nil)+"\n\n")
			return
		}
		b.categoryFail(ctx, chatID, msgID, id, st, t, tiers, err)
		return
	}
	doneKey := "bot.cat.window_saved"
	if iso == "" {
		doneKey = "bot.cat.window_removed"
	}
	b.categoryAfterWrite(ctx, chatID, msgID, id, jwt, st, updated, b.texts.T(loc, doneKey, nil)+"\n\n")
}

// ─── typed answers ────────────────────────────────────────────────────────────

// categoryText consumes the text of a question in progress (a price, a
// quantity or a date). It reports whether the text was taken.
func (b *Bot) categoryText(ctx context.Context, chatID int64, from *models.User, text string) bool {
	var st categoryDialog
	step, found, expired, err := b.dialogs.Load(ctx, from.ID, categoryDialogKind, &st)
	if err != nil {
		b.logger.Error("eventbot: category dialog load failed", slog.Int64("telegram_user_id", from.ID), slog.String("error", err.Error()))
		return false
	}
	if expired {
		loc := NormalizeLocale(from.LanguageCode)
		if id, _, err := b.resolveIdentity(ctx, from.ID); err == nil {
			loc = id.Locale()
		}
		b.send(ctx, chatID, b.texts.T(loc, "bot.dialog_expired", nil), &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
			{{Text: b.texts.T(loc, "bot.btn_events", nil), CallbackData: "el:new"}},
			{{Text: b.texts.T(loc, "bot.btn_home", nil), CallbackData: "home"}},
		}})
		return true
	}
	if !found || (step != catStepPrice && step != catStepQty && step != catStepWStart && step != catStepWEnd) || st.CardID == nil {
		return false
	}
	id, jwt, err := b.resolveIdentity(ctx, from.ID)
	if err != nil || id.Current == nil || id.Current.OrgID != st.OrgID || !canCategories(id) {
		b.leaveCategories(ctx, from.ID)
		return false
	}
	loc := id.Locale()
	var edit *int
	if st.MsgID != 0 {
		edit = &st.MsgID
	}
	if step == catStepWStart || step == catStepWEnd {
		b.categoryDateInput(ctx, chatID, edit, id, jwt, st, step, text, "")
		return true
	}
	t, tiers, ok, err := b.categoryFind(ctx, jwt, st, *st.CardID)
	if err != nil {
		b.ecError(ctx, chatID, edit, id, err, "events:1")
		return true
	}
	if !ok {
		b.categoryRenderList(ctx, chatID, edit, id, jwt, st, b.texts.T(loc, "bot.cat.gone_one", nil)+"\n\n")
		return true
	}
	caps := categoryCapsFor(t, tiers)
	back := ctKeyboard([][]Button{b.ctCardBack(loc, t.Id)})
	switch step {
	case catStepPrice:
		amount, okAmount := parsePromoAmount(text)
		if !caps.Price || !okAmount {
			b.reply(ctx, chatID, edit, b.texts.T(loc, "bot.cat.err_price", nil)+"\n\n"+b.texts.T(loc, "bot.cat.q_price", map[string]any{
				"Name": Esc(t.Name), "Price": Esc(FormatMoney(categoryPriceNow(t), t.Currency, loc)), "Currency": Esc(strings.ToUpper(t.Currency)),
			}), back)
			return true
		}
		st.Pending = amount
		b.saveCategoryState(ctx, from.ID, st, catStepPriceOK)
		b.reply(ctx, chatID, edit, b.texts.T(loc, "bot.cat.price_confirm", map[string]any{
			"Name": Esc(t.Name), "Old": Esc(FormatMoney(categoryPriceNow(t), t.Currency, loc)), "New": Esc(FormatMoney(amount, t.Currency, loc)),
		}), ctKeyboard([][]Button{
			{b.ctBtn(loc, "bot.cat.price_save_btn", "ps:"+t.Id.String(), nil)},
			b.ctCardBack(loc, t.Id),
		}))
	case catStepQty:
		n, okQty := parseCategoryQuantity(text)
		if !caps.Qty || !okQty {
			b.reply(ctx, chatID, edit, b.texts.T(loc, "bot.cat.err_qty", nil)+"\n\n"+b.texts.T(loc, "bot.cat.q_qty", map[string]any{
				"Name": Esc(t.Name), "Value": b.categoryPlaces(loc, t), "Limit": int32or0(t.Sold) + int32or0(t.Held),
			}), back)
			return true
		}
		_, err := b.arena.PatchTier(ctx, jwt, st.OrgID, st.EventID, st.SessionID, t.Id, map[string]any{"capacity": n})
		if err != nil {
			if key := categoryErrKey(err); key != "" {
				// Say what the category cannot give up, from a fresh read: the
				// places may have sold since the question was asked.
				fresh, _, _, _ := b.categoryFind(ctx, jwt, st, t.Id)
				floor := int32or0(fresh.Sold) + int32or0(fresh.Held)
				b.reply(ctx, chatID, edit, b.texts.T(loc, key, map[string]any{"Limit": floor})+"\n\n"+b.texts.T(loc, "bot.cat.q_qty", map[string]any{
					"Name": Esc(t.Name), "Value": b.categoryPlaces(loc, fresh), "Limit": floor,
				}), back)
				return true
			}
			b.ecError(ctx, chatID, edit, id, err, "events:1")
			return true
		}
		b.categoryShowCard(ctx, chatID, edit, id, jwt, st, t.Id, b.texts.T(loc, "bot.cat.qty_saved", map[string]any{"Total": n})+"\n\n")
	}
	return true
}
