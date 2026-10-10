package eventbot

// invite.go — "Invitations" (EC-12, spec 35 §6.3): free tickets for guests,
// issued from the bot. The dialog, kind "invite" in bot_dialogs (dialogs.go),
// walks
//
//	ev       the event (events that still have a date to come)
//	se       the date
//	ti       the category (general-admission ones; a seated hall is refused)
//	qty      how many tickets - at most 50 in one operation, more in several
//	rcpt     the guests, one per line "Name, e-mail" or just the e-mail
//	confirm  the counts and the guests, then ONE press that issues
//
// then one POST .../complimentary PER GUEST (qty 1 each): every guest gets an
// invitation of their own that can be annulled on its own, and a failure half
// way leaves the guests before it issued and says which were not. The batch id
// of each call is "<operation>-<index>", so a retry after a timeout, a double
// press or a bot restart replays the guests already issued (the API answers
// the first result and sends no second letter).
//
// The number of guests must EQUAL the quantity: the guests may arrive in
// several messages (each one accepted whole or not at all, see
// invite_recipients.go), a message with more guests than are still needed is
// refused, and the confirmation is shown only when the counts match. There are
// no anonymous tickets from the bot - an invitation goes to an address.
//
// Guests' e-mails are contact data: they are shown and typed only in the
// private chat (every entry point checks it), live in the dialog row like the
// orders search text, and are never logged. The list and the annulling are in
// invite_list.go. Presses are "iv:<what>":
//
//	iv:new  hub          iv:i  issue                iv:e:<event>  issue, event known
//	iv:ep:<n> iv:eo:<i>  events page / open row     iv:sp:<n> iv:so:<i>  dates
//	iv:to:<i>            category                   iv:q:<n>      quantity
//	iv:go                issue                      iv:bk         back one step

import (
	"context"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"
)

const (
	inviteDialogKind = "invite"

	invStepEvents  = "ev"
	invStepDates   = "se"
	invStepTiers   = "ti"
	invStepQty     = "qty"
	invStepRcpt    = "rcpt"
	invStepConfirm = "confirm"
	invStepList    = "list"
	invStepCard    = "card"
	invStepRevoke  = "revoke"

	// maxInviteTierRows bounds the category buttons of one date.
	maxInviteTierRows = 8

	// API error codes with their own words.
	invCodeSoldOut  = "tier.sold_out"
	invCodeCapacity = "complimentary.capacity_overflow"
	invCodeTooMany  = "complimentary.qty_too_large"
	invCodeRecipOne = "complimentary.invalid_recipient"
	invCodeTierReq  = "tier.required"
)

// inviteDialog is the state between two messages.
type inviteDialog struct {
	OrgID uuid.UUID `json:"org_id"`
	MsgID int       `json:"msg_id,omitempty"`

	// events picker
	EventIDs  []uuid.UUID `json:"event_ids,omitempty"`
	EventPage int         `json:"event_page,omitempty"`
	// the chosen event; Fixed when the person came from the event's card
	EventID   uuid.UUID `json:"event_id"`
	EventName string    `json:"event_name,omitempty"`
	Fixed     bool      `json:"fixed,omitempty"`

	// dates picker and the chosen date
	SessionIDs  []uuid.UUID `json:"session_ids,omitempty"`
	SessionPage int         `json:"session_page,omitempty"`
	SessionID   uuid.UUID   `json:"session_id"`
	When        string      `json:"when,omitempty"`

	// categories and the chosen one; Free is the places it still has
	TierIDs  []uuid.UUID `json:"tier_ids,omitempty"`
	TierID   uuid.UUID   `json:"tier_id"`
	TierName string      `json:"tier_name,omitempty"`
	Free     int64       `json:"free"`

	// the operation
	Qty   int               `json:"qty,omitempty"`
	Rcpt  []InviteRecipient `json:"rcpt,omitempty"`
	Batch string            `json:"batch,omitempty"`
	Done  []int64           `json:"done,omitempty"` // ticket number per guest, 0 = not issued yet

	// the issued list
	ListPage int         `json:"list_page,omitempty"`
	ListIDs  []uuid.UUID `json:"list_ids,omitempty"`
	Open     uuid.UUID   `json:"open"`
	// OpenGuest is who the open invitation is for, as the messages name them.
	OpenGuest string `json:"open_guest,omitempty"`
}

// canInvite reports whether the person may issue and annul invitations: the
// roles that hold complimentary.issue (owner, manager) and the operator.
func canInvite(id *Identity) bool { return canViewSales(id) }

// ─── state in bot_dialogs ─────────────────────────────────────────────────────

// loadInvite reads the dialog. found is false when there is none, it belongs to
// another organization, or it ran out (expired is then true, once).
func (b *Bot) loadInvite(ctx context.Context, tg int64, orgID uuid.UUID) (st inviteDialog, step string, found, expired bool) {
	step, found, expired, err := b.dialogs.Load(ctx, tg, inviteDialogKind, &st)
	if err != nil {
		b.logger.Error("eventbot: invite dialog load failed", slog.Int64("telegram_user_id", tg), slog.String("error", err.Error()))
		return inviteDialog{}, "", false, false
	}
	if found && st.OrgID != orgID {
		return inviteDialog{}, "", false, false
	}
	return st, step, found, expired
}

func (b *Bot) saveInvite(ctx context.Context, tg int64, st inviteDialog, step string) {
	orgID := st.OrgID
	if err := b.dialogs.Save(ctx, tg, &orgID, inviteDialogKind, step, st, dialogTTL); err != nil {
		b.logger.Error("eventbot: invite dialog save failed", slog.Int64("telegram_user_id", tg), slog.String("error", err.Error()))
	}
}

// leaveInvite ends the dialog: whoever opens another screen is no longer
// typing guests, a quantity or an annul word.
func (b *Bot) leaveInvite(ctx context.Context, tg int64) {
	if err := b.dialogs.Delete(ctx, tg, inviteDialogKind); err != nil {
		b.logger.Warn("eventbot: invite dialog clear failed", slog.Int64("telegram_user_id", tg), slog.String("error", err.Error()))
	}
}

// ─── keyboards ────────────────────────────────────────────────────────────────

func (b *Bot) invButton(loc, key, data string) models.InlineKeyboardButton {
	return models.InlineKeyboardButton{Text: b.texts.T(loc, key, nil), CallbackData: data}
}

// invNav is the bottom row: back (one step, or to the given target) and home.
func (b *Bot) invNav(loc, backData string) []models.InlineKeyboardButton {
	return []models.InlineKeyboardButton{
		{Text: "« " + b.texts.T(loc, "bot.btn_back", nil), CallbackData: backData},
		{Text: b.texts.T(loc, "bot.btn_home", nil), CallbackData: "home"},
	}
}

func (b *Bot) invMarkup(rows ...[]models.InlineKeyboardButton) *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{InlineKeyboard: rows}
}

// ─── presses ──────────────────────────────────────────────────────────────────

// inviteGone answers a press (or a typed text) with no live dialog behind it.
// An expired one is reported once, like every dialog (spec 35 §4.1).
func (b *Bot) inviteGone(ctx context.Context, chatID int64, editMsgID *int, loc string, expired bool) {
	key := "bot.dialog_expired"
	if !expired {
		key = "bot.ec.not_found"
	}
	b.reply(ctx, chatID, editMsgID, b.texts.T(loc, key, nil), b.invMarkup(
		[]models.InlineKeyboardButton{b.invButton(loc, "bot.inv.btn", "iv:new")},
		[]models.InlineKeyboardButton{b.invButton(loc, "bot.btn_home", "home")},
	))
}

// inviteCallback handles every "iv:<data>" press.
func (b *Bot) inviteCallback(ctx context.Context, chatID int64, msgID int, from *models.User, data string) {
	id, jwt, ok := b.ecIdentity(ctx, chatID, &msgID, from)
	if !ok {
		return
	}
	loc := id.Locale()
	if !canInvite(id) {
		b.reply(ctx, chatID, &msgID, b.texts.T(loc, "bot.ec.no_rights", nil), b.backKeyboard(loc, "home"))
		return
	}
	if chatID != from.ID {
		b.reply(ctx, chatID, &msgID, b.texts.T(loc, "bot.inv.private_only", nil), b.backKeyboard(loc, "home"))
		return
	}
	orgID := id.Current.OrgID
	kind, arg, _ := strings.Cut(data, ":")

	// Entries that start a dialog of their own.
	switch kind {
	case "new":
		b.leaveInvite(ctx, from.ID)
		b.showInviteHub(ctx, chatID, &msgID, id)
		return
	case "i":
		b.showInviteEvents(ctx, chatID, &msgID, id, jwt, inviteDialog{OrgID: orgID}, 1)
		return
	case "e":
		eventID, err := uuid.Parse(arg)
		if err != nil {
			return
		}
		b.showInviteDates(ctx, chatID, &msgID, id, jwt, inviteDialog{OrgID: orgID, EventID: eventID, Fixed: true}, 1)
		return
	case "l":
		b.showInviteList(ctx, chatID, &msgID, id, jwt, inviteDialog{OrgID: orgID}, 1, "")
		return
	}

	st, step, found, expired := b.loadInvite(ctx, from.ID, orgID)
	if !found {
		b.inviteGone(ctx, chatID, &msgID, loc, expired)
		return
	}
	switch kind {
	case "ep":
		b.showInviteEvents(ctx, chatID, &msgID, id, jwt, st, ParsePage(arg))
	case "eo":
		i, ok := ParseIndex(arg, len(st.EventIDs))
		if !ok || step != invStepEvents {
			b.showInviteEvents(ctx, chatID, &msgID, id, jwt, st, st.EventPage)
			return
		}
		st.EventID, st.Fixed = st.EventIDs[i], false
		b.showInviteDates(ctx, chatID, &msgID, id, jwt, st, 1)
	case "sp":
		b.showInviteDates(ctx, chatID, &msgID, id, jwt, st, ParsePage(arg))
	case "so":
		i, ok := ParseIndex(arg, len(st.SessionIDs))
		if !ok || step != invStepDates {
			b.showInviteDates(ctx, chatID, &msgID, id, jwt, st, st.SessionPage)
			return
		}
		st.SessionID = st.SessionIDs[i]
		b.showInviteTiers(ctx, chatID, &msgID, id, jwt, st)
	case "to":
		i, ok := ParseIndex(arg, len(st.TierIDs))
		if !ok || step != invStepTiers {
			b.showInviteTiers(ctx, chatID, &msgID, id, jwt, st)
			return
		}
		b.chooseInviteTier(ctx, chatID, &msgID, id, jwt, st, st.TierIDs[i])
	case "q":
		n, ok := ParseInviteQty(arg)
		if !ok || step != invStepQty {
			return
		}
		b.takeInviteQty(ctx, chatID, &msgID, from.ID, loc, st, n)
	case "go":
		if step != invStepConfirm {
			return
		}
		b.sendInvitations(ctx, chatID, &msgID, from.ID, id, jwt, st)
	case "bk":
		b.inviteBack(ctx, chatID, &msgID, from, id, jwt, st, step)
	default:
		b.inviteListCallback(ctx, chatID, &msgID, id, jwt, st, step, kind, arg)
	}
}

// inviteBack goes one step back.
func (b *Bot) inviteBack(ctx context.Context, chatID int64, editMsgID *int, from *models.User, id *Identity, jwt string, st inviteDialog, step string) {
	loc := id.Locale()
	switch step {
	case invStepDates:
		if st.Fixed {
			b.leaveInvite(ctx, id.Link.TelegramUserID)
			b.showEventCard(ctx, chatID, editMsgID, from, st.EventID, false)
			return
		}
		b.showInviteEvents(ctx, chatID, editMsgID, id, jwt, st, st.EventPage)
	case invStepTiers:
		b.showInviteDates(ctx, chatID, editMsgID, id, jwt, st, st.SessionPage)
	case invStepQty:
		b.showInviteTiers(ctx, chatID, editMsgID, id, jwt, st)
	case invStepRcpt:
		st.Rcpt, st.Done = nil, nil
		b.showInviteQty(ctx, chatID, editMsgID, id.Link.TelegramUserID, loc, st, "")
	case invStepConfirm:
		st.Rcpt, st.Done, st.Batch = nil, nil, ""
		b.showInviteRecipients(ctx, chatID, editMsgID, id.Link.TelegramUserID, loc, st, "")
	case invStepList:
		b.leaveInvite(ctx, id.Link.TelegramUserID)
		b.showInviteHub(ctx, chatID, editMsgID, id)
	case invStepCard, invStepRevoke:
		b.showInviteList(ctx, chatID, editMsgID, id, jwt, st, st.ListPage, "")
	default:
		b.leaveInvite(ctx, id.Link.TelegramUserID)
		b.showInviteHub(ctx, chatID, editMsgID, id)
	}
}

// ─── hub ──────────────────────────────────────────────────────────────────────

func (b *Bot) showInviteHub(ctx context.Context, chatID int64, editMsgID *int, id *Identity) {
	loc := id.Locale()
	b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.inv.hub", map[string]any{
		"Org": Esc(id.Current.OrgName), "Max": inviteMaxOperation,
	}), b.invMarkup(
		[]models.InlineKeyboardButton{b.invButton(loc, "bot.inv.issue_btn", "iv:i")},
		[]models.InlineKeyboardButton{b.invButton(loc, "bot.inv.list_btn", "iv:l")},
		[]models.InlineKeyboardButton{b.invButton(loc, "bot.btn_home", "home")},
	))
}

// ─── events ───────────────────────────────────────────────────────────────────

// showInviteEvents lists the events that still have a date to come (not
// archived), the soonest first, five to a page.
func (b *Bot) showInviteEvents(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt string, st inviteDialog, page int) {
	loc := id.Locale()
	events, err := b.arena.ListEvents(ctx, jwt, st.OrgID)
	if err != nil {
		b.ecError(ctx, chatID, editMsgID, id, err, "iv:new")
		return
	}
	rows := events[:0:0]
	for _, e := range events {
		if e.SalesState != salesStateArchived && string(e.Status) != "archived" && e.NextSessionAt != nil {
			rows = append(rows, e)
		}
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].NextSessionAt.Before(*rows[j].NextSessionAt) })
	pages := PagesFor(int64(len(rows)), listPageSize)
	if page < 1 {
		page = 1
	}
	if page > pages {
		page = pages
	}
	lo := (page - 1) * listPageSize
	hi := lo + listPageSize
	if hi > len(rows) {
		hi = len(rows)
	}
	st.EventIDs, st.EventPage, st.MsgID = st.EventIDs[:0], page, msgOf(editMsgID)
	if len(rows) == 0 {
		b.saveInvite(ctx, id.Link.TelegramUserID, st, invStepEvents)
		b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.inv.ev_empty", nil), b.invMarkup(b.invNav(loc, "iv:new")))
		return
	}
	kb := make([][]models.InlineKeyboardButton, 0, listPageSize+3)
	for i, e := range rows[lo:hi] {
		st.EventIDs = append(st.EventIDs, e.Id)
		kb = append(kb, []models.InlineKeyboardButton{{Text: eventRowLabel(e, evFilterRun), CallbackData: ItemCallback("iv:eo", i)}})
	}
	if nav := PagerRow(Pager{Prefix: "iv:ep", Page: page, Pages: pages},
		b.texts.T(loc, "bot.btn_prev", nil), b.texts.T(loc, "bot.btn_next", nil)); nav != nil {
		kb = append(kb, nav)
	}
	kb = append(kb, b.invNav(loc, "iv:new"))
	b.saveInvite(ctx, id.Link.TelegramUserID, st, invStepEvents)
	b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.inv.ev_title", map[string]any{
		"Org": Esc(id.Current.OrgName), "Page": page, "Pages": pages,
	}), b.invMarkup(kb...))
}

// msgOf is the id of the message being edited, or 0.
func msgOf(editMsgID *int) int {
	if editMsgID == nil {
		return 0
	}
	return *editMsgID
}

// ─── dates ────────────────────────────────────────────────────────────────────

// showInviteDates lists the dates of the chosen event that have not ended and
// are not cancelled, soonest first.
func (b *Bot) showInviteDates(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt string, st inviteDialog, page int) {
	loc := id.Locale()
	back := "iv:bk"
	sum, err := b.arena.EventSummary(ctx, jwt, st.OrgID, st.EventID)
	if err != nil {
		b.leaveInvite(ctx, id.Link.TelegramUserID)
		b.ecError(ctx, chatID, editMsgID, id, err, "iv:new")
		return
	}
	st.EventName = sum.Event.Name
	now := time.Now()
	type dateRow struct {
		id    uuid.UUID
		label string
	}
	var rows []dateRow
	sessions := sum.Sessions
	sort.SliceStable(sessions, func(i, j int) bool { return sessions[i].StartAt.Before(sessions[j].StartAt) })
	for _, s := range sessions {
		if s.Status == "cancelled" || !s.EndAt.After(now) {
			continue
		}
		rows = append(rows, dateRow{s.Id, "📅 " + shortWhen(s) + " · " + b.texts.T(loc, "bot.inv.free_label", map[string]any{"N": s.Places.Ga.Available})})
	}
	pages := PagesFor(int64(len(rows)), listPageSize)
	if page < 1 {
		page = 1
	}
	if page > pages {
		page = pages
	}
	lo := (page - 1) * listPageSize
	hi := lo + listPageSize
	if hi > len(rows) {
		hi = len(rows)
	}
	st.SessionIDs, st.SessionPage, st.MsgID = st.SessionIDs[:0], page, msgOf(editMsgID)
	if len(rows) == 0 {
		b.saveInvite(ctx, id.Link.TelegramUserID, st, invStepDates)
		b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.inv.se_empty", map[string]any{"Name": Esc(st.EventName)}), b.invMarkup(b.invNav(loc, back)))
		return
	}
	kb := make([][]models.InlineKeyboardButton, 0, listPageSize+3)
	for i, r := range rows[lo:hi] {
		st.SessionIDs = append(st.SessionIDs, r.id)
		kb = append(kb, []models.InlineKeyboardButton{{Text: r.label, CallbackData: ItemCallback("iv:so", i)}})
	}
	if nav := PagerRow(Pager{Prefix: "iv:sp", Page: page, Pages: pages},
		b.texts.T(loc, "bot.btn_prev", nil), b.texts.T(loc, "bot.btn_next", nil)); nav != nil {
		kb = append(kb, nav)
	}
	kb = append(kb, b.invNav(loc, back))
	b.saveInvite(ctx, id.Link.TelegramUserID, st, invStepDates)
	b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.inv.se_title", map[string]any{
		"Name": Esc(st.EventName), "Page": page, "Pages": pages,
	}), b.invMarkup(kb...))
}

// ─── categories ───────────────────────────────────────────────────────────────

// showInviteTiers lists the general-admission categories of the chosen date
// with their free places. A date with a seating plan is refused: a free
// ticket there would take a place from the ledger but no seat, and the seat
// would stay on sale.
func (b *Bot) showInviteTiers(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt string, st inviteDialog) {
	loc := id.Locale()
	back := "iv:bk"
	sum, err := b.arena.SessionSummary(ctx, jwt, st.OrgID, st.SessionID)
	if err != nil {
		b.ecError(ctx, chatID, editMsgID, id, err, "iv:new")
		return
	}
	tz := ""
	if sum.Session.VenueTimezone != nil {
		tz = *sum.Session.VenueTimezone
	}
	st.EventID, st.EventName, st.When = sum.Session.EventId, sum.Session.EventName, FormatWhen(sum.Session.StartAt, tz)
	st.MsgID = msgOf(editMsgID)
	st.TierIDs = st.TierIDs[:0]
	vars := map[string]any{"Name": Esc(st.EventName), "When": Esc(st.When)}
	if sum.Session.HasSeatingPlan {
		b.saveInvite(ctx, id.Link.TelegramUserID, st, invStepTiers)
		b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.inv.ti_seated", vars), b.invMarkup(b.invNav(loc, back)))
		return
	}
	var kb [][]models.InlineKeyboardButton
	for _, t := range sum.Tiers {
		if t.Kind != "ga" || len(st.TierIDs) >= maxInviteTierRows {
			continue
		}
		label := truncate(t.Name, 30) + " · " + b.texts.T(loc, "bot.inv.free_label", map[string]any{"N": t.Places.Available})
		kb = append(kb, []models.InlineKeyboardButton{{Text: label, CallbackData: ItemCallback("iv:to", len(st.TierIDs))}})
		st.TierIDs = append(st.TierIDs, t.Id)
	}
	if len(kb) == 0 {
		b.saveInvite(ctx, id.Link.TelegramUserID, st, invStepTiers)
		b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.inv.ti_empty", vars), b.invMarkup(b.invNav(loc, back)))
		return
	}
	kb = append(kb, b.invNav(loc, back))
	b.saveInvite(ctx, id.Link.TelegramUserID, st, invStepTiers)
	b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.inv.ti_title", vars), b.invMarkup(kb...))
}

// chooseInviteTier takes the category, re-reading its free places so the
// number the next screen quotes is the current one.
func (b *Bot) chooseInviteTier(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt string, st inviteDialog, tierID uuid.UUID) {
	loc := id.Locale()
	sum, err := b.arena.SessionSummary(ctx, jwt, st.OrgID, st.SessionID)
	if err != nil {
		b.ecError(ctx, chatID, editMsgID, id, err, "iv:new")
		return
	}
	for _, t := range sum.Tiers {
		if t.Id != tierID {
			continue
		}
		if t.Places.Available <= 0 {
			b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.inv.ti_full", map[string]any{"Tier": Esc(t.Name)}), b.invMarkup(b.invNav(loc, "iv:bk")))
			return
		}
		st.TierID, st.TierName, st.Free = t.Id, t.Name, t.Places.Available
		st.Qty, st.Rcpt, st.Done, st.Batch = 0, nil, nil, ""
		b.showInviteQty(ctx, chatID, editMsgID, id.Link.TelegramUserID, loc, st, "")
		return
	}
	b.showInviteTiers(ctx, chatID, editMsgID, id, jwt, st)
}

// ─── quantity ─────────────────────────────────────────────────────────────────

func (b *Bot) showInviteQty(ctx context.Context, chatID int64, editMsgID *int, tg int64, loc string, st inviteDialog, note string) {
	st.MsgID = msgOf(editMsgID)
	b.saveInvite(ctx, tg, st, invStepQty)
	limit := int64(inviteMaxOperation)
	if st.Free < limit {
		limit = st.Free
	}
	var quick []models.InlineKeyboardButton
	for _, n := range []int64{1, 2, 5, 10, 20} {
		if n <= limit {
			quick = append(quick, models.InlineKeyboardButton{Text: invNum(n), CallbackData: "iv:q:" + invNum(n)})
		}
	}
	rows := [][]models.InlineKeyboardButton{}
	if len(quick) > 0 {
		rows = append(rows, quick)
	}
	rows = append(rows, b.invNav(loc, "iv:bk"))
	b.reply(ctx, chatID, editMsgID, note+b.texts.T(loc, "bot.inv.qty_ask", map[string]any{
		"Name": Esc(st.EventName), "When": Esc(st.When), "Tier": Esc(st.TierName), "N": st.Free, "Max": inviteMaxOperation,
	}), b.invMarkup(rows...))
}

// takeInviteQty accepts a quantity (a press or a typed number) when it fits
// the operation limit and the places left.
func (b *Bot) takeInviteQty(ctx context.Context, chatID int64, editMsgID *int, tg int64, loc string, st inviteDialog, n int) {
	switch {
	case n > inviteMaxOperation:
		b.showInviteQty(ctx, chatID, editMsgID, tg, loc, st, b.texts.T(loc, "bot.inv.qty_over_max", map[string]any{"Max": inviteMaxOperation})+"\n\n")
		return
	case int64(n) > st.Free:
		b.showInviteQty(ctx, chatID, editMsgID, tg, loc, st, b.texts.T(loc, "bot.inv.qty_over_free", map[string]any{"N": st.Free, "Tier": Esc(st.TierName)})+"\n\n")
		return
	}
	if st.Qty != n {
		st.Rcpt, st.Done, st.Batch = nil, nil, ""
	}
	st.Qty = n
	b.showInviteRecipients(ctx, chatID, editMsgID, tg, loc, st, "")
}

// invNum prints a number.
func invNum(n int64) string { return strconv.FormatInt(n, 10) }

// ─── recipients ───────────────────────────────────────────────────────────────

// guestLine is one guest as the screens print it (HTML-safe).
func guestLine(r InviteRecipient) string {
	if r.Name == "" {
		return Esc(r.Email)
	}
	return Esc(r.Name) + " · " + Esc(r.Email)
}

func guestLines(rs []InviteRecipient) string {
	var sb strings.Builder
	for i, r := range rs {
		if i > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(invNum(int64(i+1)) + ". " + guestLine(r))
	}
	return sb.String()
}

func (b *Bot) showInviteRecipients(ctx context.Context, chatID int64, editMsgID *int, tg int64, loc string, st inviteDialog, note string) {
	st.MsgID = msgOf(editMsgID)
	b.saveInvite(ctx, tg, st, invStepRcpt)
	text := note + b.texts.T(loc, "bot.inv.rcpt_ask", map[string]any{
		"N": st.Qty, "Left": st.Qty - len(st.Rcpt), "Tier": Esc(st.TierName),
	})
	if len(st.Rcpt) > 0 {
		text += "\n\n" + b.texts.T(loc, "bot.inv.rcpt_got", map[string]any{"Got": len(st.Rcpt), "N": st.Qty, "List": guestLines(st.Rcpt)})
	}
	b.reply(ctx, chatID, editMsgID, text, b.invMarkup(b.invNav(loc, "iv:bk")))
}

// takeInviteRecipients reads a message of guests into the operation.
func (b *Bot) takeInviteRecipients(ctx context.Context, chatID int64, editMsgID *int, tg int64, loc string, st inviteDialog, text string) {
	got, bad := ParseInviteRecipients(text, st.Rcpt)
	if len(bad) > 0 {
		b.showInviteRecipients(ctx, chatID, editMsgID, tg, loc, st, b.badLinesNote(loc, bad)+"\n\n")
		return
	}
	if len(got) == 0 {
		b.showInviteRecipients(ctx, chatID, editMsgID, tg, loc, st, "")
		return
	}
	left := st.Qty - len(st.Rcpt)
	if len(got) > left {
		b.showInviteRecipients(ctx, chatID, editMsgID, tg, loc, st, b.texts.T(loc, "bot.inv.rcpt_too_many", map[string]any{
			"Sent": len(got), "Left": left,
		})+"\n\n")
		return
	}
	st.Rcpt = append(st.Rcpt, got...)
	if len(st.Rcpt) < st.Qty {
		b.showInviteRecipients(ctx, chatID, editMsgID, tg, loc, st, "")
		return
	}
	st.Batch = strings.ReplaceAll(uuid.NewString(), "-", "")
	st.Done = make([]int64, len(st.Rcpt))
	b.showInviteConfirm(ctx, chatID, editMsgID, tg, loc, st, "")
}

// badLinesNote names every refused line: its number, what was typed (cut) and why.
func (b *Bot) badLinesNote(loc string, bad []InviteLineProblem) string {
	var sb strings.Builder
	for i, p := range bad {
		if i > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(b.texts.T(loc, "bot.inv.bad_line", map[string]any{
			"Line": p.Line, "Text": Esc(cutRunes(p.Text, 50)), "Reason": b.texts.T(loc, "bot.inv.reason_"+p.Reason, nil),
		}))
	}
	return b.texts.T(loc, "bot.inv.rcpt_bad", map[string]any{"List": sb.String()})
}

// ─── confirmation ─────────────────────────────────────────────────────────────

func (b *Bot) showInviteConfirm(ctx context.Context, chatID int64, editMsgID *int, tg int64, loc string, st inviteDialog, note string) {
	st.MsgID = msgOf(editMsgID)
	b.saveInvite(ctx, tg, st, invStepConfirm)
	b.reply(ctx, chatID, editMsgID, clipMessage(note+b.texts.T(loc, "bot.inv.confirm", map[string]any{
		"Name": Esc(st.EventName), "When": Esc(st.When), "Tier": Esc(st.TierName), "N": len(st.Rcpt), "List": guestLines(st.Rcpt),
	}), maxMessageRunes), b.invMarkup(
		[]models.InlineKeyboardButton{b.invButton(loc, "bot.inv.send_btn", "iv:go")},
		b.invNav(loc, "iv:bk"),
	))
}

// ─── issuing ──────────────────────────────────────────────────────────────────

// sendInvitations issues one invitation per guest that has none yet and shows
// the outcome. A refusal for want of places ends the operation (the guests
// issued before it stay issued); any other failure keeps the dialog so the
// same press can be repeated - the guests already done are not issued again.
func (b *Bot) sendInvitations(ctx context.Context, chatID int64, editMsgID *int, tg int64, id *Identity, jwt string, st inviteDialog) {
	loc := id.Locale()
	if len(st.Done) != len(st.Rcpt) {
		st.Done = make([]int64, len(st.Rcpt))
	}
	var failure error
	for i, r := range st.Rcpt {
		if st.Done[i] != 0 {
			continue
		}
		res, err := b.arena.IssueInvitation(ctx, jwt, st.OrgID, InvitationInput{
			SessionID: st.SessionID, TierID: st.TierID, Email: r.Email, Name: r.Name,
			BatchID: "tg-" + st.Batch + "-" + invNum(int64(i)),
		})
		if err != nil {
			failure = err
			break
		}
		num := int64(-1) // issued, number not reported
		if len(res.Tickets) > 0 && res.Tickets[0].SystemTicketID > 0 {
			num = res.Tickets[0].SystemTicketID
		}
		st.Done[i] = num
	}
	issued, notIssued := inviteOutcome(st)
	if failure == nil {
		b.leaveInvite(ctx, tg)
		b.reply(ctx, chatID, editMsgID, clipMessage(b.texts.T(loc, "bot.inv.done", map[string]any{
			"N": len(issued), "List": b.issuedLines(issued),
		}), maxMessageRunes), b.invMarkup(
			[]models.InlineKeyboardButton{b.invButton(loc, "bot.inv.list_btn", "iv:l")},
			[]models.InlineKeyboardButton{b.invButton(loc, "bot.inv.issue_btn", "iv:i")},
			[]models.InlineKeyboardButton{b.invButton(loc, "bot.btn_home", "home")},
		))
		return
	}

	why, final := b.inviteFailureWhy(loc, failure, id)
	text := b.texts.T(loc, "bot.inv.partial", map[string]any{
		"Done": len(issued), "N": len(st.Rcpt), "Why": why,
	})
	if len(issued) > 0 {
		text += "\n\n" + b.texts.T(loc, "bot.inv.partial_done", map[string]any{"List": b.issuedLines(issued)})
	}
	text += "\n\n" + b.texts.T(loc, "bot.inv.partial_left", map[string]any{"List": guestLines(notIssued)})
	if final {
		b.leaveInvite(ctx, tg)
		b.reply(ctx, chatID, editMsgID, clipMessage(text, maxMessageRunes), b.invMarkup(
			[]models.InlineKeyboardButton{b.invButton(loc, "bot.inv.list_btn", "iv:l")},
			[]models.InlineKeyboardButton{b.invButton(loc, "bot.btn_home", "home")},
		))
		return
	}
	st.MsgID = msgOf(editMsgID)
	b.saveInvite(ctx, tg, st, invStepConfirm)
	b.reply(ctx, chatID, editMsgID, clipMessage(text, maxMessageRunes), b.invMarkup(
		[]models.InlineKeyboardButton{b.invButton(loc, "bot.inv.retry_btn", "iv:go")},
		b.invNav(loc, "iv:bk"),
	))
}

// inviteOutcome splits the guests into issued ones (with their numbers) and
// those still without an invitation.
func inviteOutcome(st inviteDialog) (issued []issuedGuest, left []InviteRecipient) {
	for i, r := range st.Rcpt {
		if i < len(st.Done) && st.Done[i] != 0 {
			issued = append(issued, issuedGuest{Num: st.Done[i], Guest: r})
		} else {
			left = append(left, r)
		}
	}
	return issued, left
}

type issuedGuest struct {
	Num   int64
	Guest InviteRecipient
}

// issuedLines prints the issued guests with their ticket numbers.
func (b *Bot) issuedLines(rs []issuedGuest) string {
	var sb strings.Builder
	for i, r := range rs {
		if i >= 25 {
			sb.WriteString("\n…")
			break
		}
		if i > 0 {
			sb.WriteByte('\n')
		}
		if r.Num > 0 {
			sb.WriteString("№" + invNum(r.Num) + " · ")
		}
		sb.WriteString(guestLine(r.Guest))
	}
	return sb.String()
}

// inviteFailureWhy words an API refusal. final is true when repeating the
// press cannot help (no places, bad input), false for a transient failure.
func (b *Bot) inviteFailureWhy(loc string, err error, id *Identity) (why string, final bool) {
	switch {
	case APIErrorCode(err) == invCodeSoldOut || APIErrorCode(err) == invCodeCapacity:
		return b.texts.T(loc, "bot.inv.why_full", nil), true
	case APIErrorCode(err) == invCodeTooMany, APIErrorCode(err) == invCodeRecipOne, APIErrorCode(err) == invCodeTierReq:
		return b.texts.T(loc, "bot.inv.why_rejected", nil), true
	case IsAPIError(err, http.StatusForbidden):
		return b.texts.T(loc, "bot.ec.no_rights", nil), true
	case IsAPIError(err, http.StatusNotFound):
		return b.texts.T(loc, "bot.inv.why_gone", nil), true
	}
	b.logger.Error("eventbot: issuing an invitation failed", slog.Int64("telegram_user_id", id.Link.TelegramUserID), slog.String("error", err.Error()))
	return b.texts.T(loc, "bot.inv.why_error", nil), false
}

// ─── typed text ───────────────────────────────────────────────────────────────

// inviteText takes a typed text while an Invitations step waits for one: the
// quantity, the guests, the annul word. It reports whether the text was
// taken. A dialog that ran out is reported once, like every dialog.
func (b *Bot) inviteText(ctx context.Context, chatID int64, from *models.User, text string) bool {
	var probe inviteDialog
	step, found, expired, err := b.dialogs.Load(ctx, from.ID, inviteDialogKind, &probe)
	if err != nil {
		b.logger.Error("eventbot: invite dialog load failed", slog.Int64("telegram_user_id", from.ID), slog.String("error", err.Error()))
		return false
	}
	if expired {
		loc := NormalizeLocale(from.LanguageCode)
		if id, _, err := b.resolveIdentity(ctx, from.ID); err == nil {
			loc = id.Locale()
		}
		b.inviteGone(ctx, chatID, nil, loc, true)
		return true
	}
	if !found || (step != invStepQty && step != invStepRcpt && step != invStepRevoke) {
		return false
	}
	id, jwt, err := b.resolveIdentity(ctx, from.ID)
	if err != nil || id.Current == nil || id.Current.OrgID != probe.OrgID || !canInvite(id) {
		b.leaveInvite(ctx, from.ID)
		return false
	}
	loc := id.Locale()
	var edit *int
	if probe.MsgID != 0 {
		edit = &probe.MsgID
	}
	if chatID != from.ID {
		b.reply(ctx, chatID, edit, b.texts.T(loc, "bot.inv.private_only", nil), b.backKeyboard(loc, "home"))
		return true
	}
	switch step {
	case invStepQty:
		n, ok := ParseInviteQty(text)
		if !ok {
			b.showInviteQty(ctx, chatID, edit, from.ID, loc, probe, b.texts.T(loc, "bot.inv.qty_bad", nil)+"\n\n")
			return true
		}
		b.takeInviteQty(ctx, chatID, edit, from.ID, loc, probe, n)
	case invStepRcpt:
		b.takeInviteRecipients(ctx, chatID, edit, from.ID, loc, probe, text)
	case invStepRevoke:
		b.inviteRevokeWord(ctx, chatID, edit, from.ID, id, jwt, probe, text)
	}
	return true
}
