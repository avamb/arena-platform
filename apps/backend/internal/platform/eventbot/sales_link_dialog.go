package eventbot

// sales_link_dialog.go — the "Links and QR" dialog of the bot (spec 43 §5.2,
// QL-8): the organizer picks where a link leads, where the code will be
// printed or posted, names the place in one message, and the bot answers with
// the QR image (PNG as a photo, SVG as a document), the short address and the
// page the code leads to. The rules (the target must belong to the
// organization, the address alphabet, the default utm labels, the page a code
// falls back to) are arena-api's (hsalelinks, salelinks): the bot checks only
// what saves a round trip and shows the API's refusal in plain words.
//
// State lives in ONE bot_dialogs row of kind "sales_link" (dialogs.go), saved
// BEFORE every reply, so a restart in the middle of a question loses nothing.
// Presses are "ql:<kind>:<arg>". A press that CHANGES or READS a link names it
// by its UUID (the button must keep meaning that link however many cards the
// chat holds) and works after the dialog ran out; a list of choices (events,
// dates) names a row by its INDEX on the stored page (paging.go):
//
//	ql:menu             the main menu entry (the list, filters reset)
//	ql:list             back to the list as it was    ql:new  a new link
//	ql:e:<event>        from an event card
//	ql:le:<event>       the list narrowed to one event (sales_link_list.go)
//	ql:s:<session>      from a date's card      ql:x           cancel
//	ql:t:<ev|ss|pg|oe|url>  the target choice   ql:tb          back to the target choice
//	ql:ep:<n> ql:eo:<i> events page / event     ql:sp:<n> ql:so:<i>  dates page / date
//	ql:pk:<kind>        the placement           ql:pb          back to the placement
//	ql:v:<link>         the card                ql:st:<link>   statistics
//	ql:qr:<link>        send the code again     ql:ar:<link> ql:ua:<link>  archive / restore
//	ql:rt:<link>        retarget (choice)       ql:rt:ok:<link>  confirm the retarget
//
// A retarget is confirmed with ONE press: it moves a printed code but touches
// no money and cancels nothing (spec 43 §5.2), so no typed word is asked.
// The list of links (QL-9) has its own presses, listed in sales_link_list.go.

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

const salesLinkDialogKind = "sales_link"

// The dialog's steps.
const (
	slStepTarget    = "target"           // the target choice is on screen
	slStepEvents    = "events"           // the list of events to choose from
	slStepSessions  = "sessions"         // the dates of the chosen event
	slStepURL       = "url"              // waits for a typed address
	slStepPlacement = "placement"        // the placement buttons
	slStepLabel     = "label"            // waits for the typed name of the place
	slStepConfirm   = "retarget_confirm" // a retarget waits for its press
)

// The dialog's modes.
const (
	slModeNew      = "new"
	slModeRetarget = "retarget"
)

const (
	maxLabelRunes = 120  // sales_links.label, as the API
	maxURLRunes   = 2048 // the longest address a url link may carry
	slPageSize    = listPageSize
	slMaxSessions = 30 // dates offered as chips, soonest first
)

// slTarget is the chosen target as the dialog keeps it. Name and When are
// only for the texts.
type slTarget struct {
	Kind       string     `json:"kind"`
	EventID    *uuid.UUID `json:"event_id,omitempty"`
	SessionID  *uuid.UUID `json:"session_id,omitempty"`
	PromoterID *uuid.UUID `json:"promoter_id,omitempty"`
	URL        string     `json:"url,omitempty"`
	Name       string     `json:"name,omitempty"`
	When       string     `json:"when,omitempty"`
}

// api is the body of the target in a create or patch request. A session link
// names its date alone: the server adds the event.
func (t slTarget) api() openapi.SalesLinkTarget {
	out := openapi.SalesLinkTarget{Kind: t.Kind}
	switch t.Kind {
	case slKindEvent:
		out.EventId = t.EventID
	case slKindSession:
		out.SessionId = t.SessionID
	case slKindPromoterPage:
		out.PromoterId = t.PromoterID
	case slKindURL:
		u := t.URL
		out.Url = &u
	}
	return out
}

// slEvent is the event the choice is about: "this event" and the page of its
// promoter come from it.
type slEvent struct {
	ID           uuid.UUID  `json:"id"`
	Name         string     `json:"name"`
	PromoterID   *uuid.UUID `json:"promoter_id,omitempty"`
	PromoterName string     `json:"promoter_name,omitempty"`
}

// slSess is one date on offer; the label is made when the list is read, in
// the venue's zone.
type slSess struct {
	ID    uuid.UUID `json:"id"`
	Label string    `json:"label"`
}

// salesLinkDialog is the dialog's state, stored as the bot_dialogs row's JSON.
type salesLinkDialog struct {
	OrgID uuid.UUID `json:"org_id"`
	MsgID int       `json:"msg_id,omitempty"`
	Mode  string    `json:"mode"`
	// LinkID/LinkLabel name the link a retarget is about, or the one just made.
	LinkID    *uuid.UUID `json:"link_id,omitempty"`
	LinkLabel string     `json:"link_label,omitempty"`
	Event     *slEvent   `json:"event,omitempty"`
	Target    *slTarget  `json:"target,omitempty"`
	Placement string     `json:"placement,omitempty"`
	// Page/IDs/Sessions are the choice on screen (events or dates).
	Page     int         `json:"page,omitempty"`
	IDs      []uuid.UUID `json:"ids,omitempty"`
	Sessions []slSess    `json:"sessions,omitempty"`
}

func newSalesLinkDialog(orgID uuid.UUID) salesLinkDialog {
	return salesLinkDialog{OrgID: orgID, Mode: slModeNew, Page: 1}
}

// canSalesLinks reports whether the person may make links: the owner, the
// manager and the platform operator hold sales_link.manage (the agent role
// holds none). The API still decides every call.
func canSalesLinks(id *Identity) bool { return canViewSales(id) }

// ─── state in bot_dialogs ─────────────────────────────────────────────────────

// loadSalesLinkState reads the dialog, or starts an empty one when there is
// none, it ran out (expired is then true, once) or it belongs to another
// organization. found is true only for a live dialog of this organization.
func (b *Bot) loadSalesLinkState(ctx context.Context, tg int64, orgID uuid.UUID) (st salesLinkDialog, step string, found, expired bool) {
	step, found, expired, err := b.dialogs.Load(ctx, tg, salesLinkDialogKind, &st)
	if err != nil {
		b.logger.Error("eventbot: sales link dialog load failed", slog.Int64("telegram_user_id", tg), slog.String("error", err.Error()))
		return newSalesLinkDialog(orgID), "", false, false
	}
	if !found || st.OrgID != orgID {
		return newSalesLinkDialog(orgID), "", false, expired
	}
	if st.Page < 1 {
		st.Page = 1
	}
	if st.Mode == "" {
		st.Mode = slModeNew
	}
	return st, step, true, false
}

func (b *Bot) saveSalesLinkState(ctx context.Context, tg int64, st salesLinkDialog, step string) {
	orgID := st.OrgID
	if err := b.dialogs.Save(ctx, tg, &orgID, salesLinkDialogKind, step, st, dialogTTL); err != nil {
		b.logger.Error("eventbot: sales link dialog save failed", slog.Int64("telegram_user_id", tg), slog.String("error", err.Error()))
	}
}

// leaveSalesLink ends the dialog: whoever opens another screen is no longer
// naming a place, so a text typed next is not taken for the name of one.
func (b *Bot) leaveSalesLink(ctx context.Context, tg int64) {
	if err := b.dialogs.Delete(ctx, tg, salesLinkDialogKind); err != nil {
		b.logger.Warn("eventbot: sales link dialog clear failed", slog.Int64("telegram_user_id", tg), slog.String("error", err.Error()))
	}
}

// ─── entry points ─────────────────────────────────────────────────────────────

// salesLinkMenuRow is the main menu's button.
func (b *Bot) salesLinkMenuRow(loc string, id *Identity) []models.InlineKeyboardButton {
	if !canSalesLinks(id) {
		return nil
	}
	return []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.ql.menu_btn", nil), CallbackData: "ql:menu"}}
}

// salesLinkEventRow is the event card's button.
func (b *Bot) salesLinkEventRow(loc string, id *Identity, eventID uuid.UUID) []models.InlineKeyboardButton {
	if !canSalesLinks(id) {
		return nil
	}
	return []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.ql.btn", nil), CallbackData: "ql:e:" + eventID.String()}}
}

// salesLinkSessionButton is the date card's button (sessions_dialog.go). Its
// data keeps the "ql:" prefix, so the press ends the Sessions dialog and opens
// this one.
func (b *Bot) salesLinkSessionButton(loc string, sessionID uuid.UUID) Button {
	return Button{Label: b.texts.T(loc, "bot.ql.btn", nil), Data: "ql:s:" + sessionID.String()}
}

// salesLinkCallback handles every "ql:<data>" press.
func (b *Bot) salesLinkCallback(ctx context.Context, chatID int64, msgID int, from *models.User, data string) {
	id, jwt, ok := b.ecIdentity(ctx, chatID, &msgID, from)
	if !ok {
		return
	}
	loc := id.Locale()
	if !canSalesLinks(id) {
		b.reply(ctx, chatID, &msgID, b.texts.T(loc, "bot.ec.no_rights", nil), b.backKeyboard(loc, "home"))
		return
	}
	orgID := id.Current.OrgID
	kind, arg, _ := strings.Cut(data, ":")

	switch kind {
	case "noop": // the position button of a pager
		return
	case "menu", "list", "new", "le", "lf", "lp", "lx", "lev", "o", "fp", "fo":
		// The list of links keeps its own dialog (sales_link_list.go).
		b.slListCallback(ctx, chatID, &msgID, from, id, jwt, kind, arg)
		return
	case "e":
		if eventID, err := uuid.Parse(arg); err == nil {
			b.slStartEvent(ctx, chatID, &msgID, id, jwt, eventID)
		}
		return
	case "s":
		if sessionID, err := uuid.Parse(arg); err == nil {
			b.slStartSession(ctx, chatID, &msgID, id, jwt, sessionID)
		}
		return
	case "v", "st", "qr", "ar", "ua", "rt":
		b.slLinkPress(ctx, chatID, msgID, from, id, jwt, kind, arg)
		return
	}

	st, step, found, expired := b.loadSalesLinkState(ctx, from.ID, orgID)
	if kind == "x" {
		b.slCancel(ctx, chatID, &msgID, from, id, jwt, st, found)
		return
	}
	if expired || !found {
		// The dialog ran out while the person was away (or was ended by
		// another screen): say so and offer the menu, rather than act on a
		// press made on rows that may be stale.
		b.reply(ctx, chatID, &msgID, b.texts.T(loc, "bot.dialog_expired", nil), &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
			{{Text: b.texts.T(loc, "bot.ql.menu_btn", nil), CallbackData: "ql:menu"}},
			{{Text: b.texts.T(loc, "bot.btn_home", nil), CallbackData: "home"}},
		}})
		return
	}
	st.MsgID = msgID
	switch kind {
	case "t":
		b.slTargetPress(ctx, chatID, &msgID, id, jwt, st, arg)
	case "tb":
		b.slRenderTarget(ctx, chatID, &msgID, id, st, "")
	case "ep":
		st.Page = ParsePage(arg)
		b.slRenderEvents(ctx, chatID, &msgID, id, jwt, st)
	case "eo":
		b.slEventChosen(ctx, chatID, &msgID, id, jwt, st, arg)
	case "sp":
		st.Page = ParsePage(arg)
		b.slRenderSessions(ctx, chatID, &msgID, id, st, "")
	case "so":
		b.slSessionChosen(ctx, chatID, &msgID, id, st, arg)
	case "pk":
		b.slPlacementChosen(ctx, chatID, &msgID, id, st, step, arg)
	case "pb":
		if st.Target == nil {
			b.slRenderTarget(ctx, chatID, &msgID, id, st, "")
			return
		}
		b.slRenderPlacement(ctx, chatID, &msgID, id, st, "")
	}
}

// slStartEvent begins a new link for an event (the event card's button).
func (b *Bot) slStartEvent(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt string, eventID uuid.UUID) {
	ev, ok, err := b.slFindEvent(ctx, jwt, id.Current.OrgID, eventID)
	if err != nil {
		b.ecError(ctx, chatID, editMsgID, id, err, "home")
		return
	}
	if !ok {
		b.reply(ctx, chatID, editMsgID, b.texts.T(id.Locale(), "bot.ec.not_found", nil), b.backKeyboard(id.Locale(), "home"))
		return
	}
	st := newSalesLinkDialog(id.Current.OrgID)
	st.Event = &ev
	st.Target = &slTarget{Kind: slKindEvent, EventID: &ev.ID, Name: ev.Name}
	b.slRenderPlacement(ctx, chatID, editMsgID, id, st, "")
}

// slStartSession begins a new link for one date (the date card's button).
func (b *Bot) slStartSession(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt string, sessionID uuid.UUID) {
	orgID := id.Current.OrgID
	sum, err := b.arena.SessionSummary(ctx, jwt, orgID, sessionID)
	if err != nil {
		b.ecError(ctx, chatID, editMsgID, id, err, "home")
		return
	}
	ev, ok, err := b.slFindEvent(ctx, jwt, orgID, sum.Session.EventId)
	if err != nil {
		b.ecError(ctx, chatID, editMsgID, id, err, "home")
		return
	}
	if !ok {
		ev = slEvent{ID: sum.Session.EventId, Name: sum.Session.EventName}
	}
	tz := ""
	if sum.Session.VenueTimezone != nil {
		tz = *sum.Session.VenueTimezone
	}
	st := newSalesLinkDialog(orgID)
	st.Event = &ev
	st.Target = &slTarget{Kind: slKindSession, SessionID: &sessionID, EventID: &ev.ID, Name: ev.Name, When: FormatWhen(sum.Session.StartAt, tz)}
	b.slRenderPlacement(ctx, chatID, editMsgID, id, st, "")
}

// slFindEvent reads the organization's events and picks one.
func (b *Bot) slFindEvent(ctx context.Context, jwt string, orgID, eventID uuid.UUID) (slEvent, bool, error) {
	events, err := b.arena.ListEvents(ctx, jwt, orgID)
	if err != nil {
		return slEvent{}, false, err
	}
	for _, e := range events {
		if e.Id == eventID {
			return slEventOf(e), true, nil
		}
	}
	return slEvent{}, false, nil
}

func slEventOf(e openapi.EventItem) slEvent {
	ev := slEvent{ID: e.Id, Name: e.Name, PromoterID: e.PromoterId}
	if e.PromoterName != nil {
		ev.PromoterName = *e.PromoterName
	}
	return ev
}

// slCancel ends the dialog. A retarget goes back to the link's card; a new
// link goes home.
func (b *Bot) slCancel(ctx context.Context, chatID int64, msgID *int, from *models.User, id *Identity, jwt string, st salesLinkDialog, found bool) {
	b.leaveSalesLink(ctx, from.ID)
	if found && st.Mode == slModeRetarget && st.LinkID != nil {
		// The dialog ends before the card: a text typed next is not an address.
		b.slShowCard(ctx, chatID, msgID, id, jwt, *st.LinkID, b.texts.T(id.Locale(), "bot.ql.cancelled", nil)+"\n\n")
		return
	}
	b.showHome(ctx, chatID, msgID, from, "")
}

// ─── the target ───────────────────────────────────────────────────────────────

// slCancelRow is the row every question ends with.
func (b *Bot) slCancelRow(loc string) []models.InlineKeyboardButton {
	return []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.ql.cancel_btn", nil), CallbackData: "ql:x"}}
}

// slRenderTarget asks where the link leads. With an event in play the choices
// start from it; without one the person picks an event, the page of all
// events or an address of their own.
func (b *Bot) slRenderTarget(ctx context.Context, chatID int64, editMsgID *int, id *Identity, st salesLinkDialog, note string) {
	loc := id.Locale()
	st.Page, st.IDs, st.Sessions, st.Target = 1, nil, nil, nil
	if editMsgID != nil {
		st.MsgID = *editMsgID
	}
	b.saveSalesLinkState(ctx, id.Link.TelegramUserID, st, slStepTarget)

	var rows [][]models.InlineKeyboardButton
	text := note
	if st.Event != nil {
		text += b.texts.T(loc, "bot.ql.target_q_event", map[string]any{"Name": Esc(st.Event.Name)})
		rows = append(rows,
			[]models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.ql.t_event", nil), CallbackData: "ql:t:ev"}},
			[]models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.ql.t_session", nil), CallbackData: "ql:t:ss"}},
			[]models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.ql.t_pages", nil), CallbackData: "ql:t:pg"}},
			[]models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.ql.t_other_event", nil), CallbackData: "ql:t:oe"}},
		)
	} else {
		text += b.texts.T(loc, "bot.ql.target_q", nil)
		rows = append(rows,
			[]models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.ql.t_pick_event", nil), CallbackData: "ql:t:oe"}},
			[]models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.ql.t_pages", nil), CallbackData: "ql:t:pg"}},
		)
	}
	rows = append(rows,
		[]models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.ql.t_url", nil), CallbackData: "ql:t:url"}},
		b.slCancelRow(loc),
	)
	b.reply(ctx, chatID, editMsgID, text, &models.InlineKeyboardMarkup{InlineKeyboard: rows})
}

// slTargetPress handles "ql:t:<what>".
func (b *Bot) slTargetPress(ctx context.Context, chatID int64, msgID *int, id *Identity, jwt string, st salesLinkDialog, what string) {
	switch what {
	case "ev":
		if st.Event == nil {
			b.slRenderTarget(ctx, chatID, msgID, id, st, "")
			return
		}
		st.Target = &slTarget{Kind: slKindEvent, EventID: &st.Event.ID, Name: st.Event.Name}
		b.slTargetChosen(ctx, chatID, msgID, id, st)
	case "ss":
		if st.Event == nil {
			b.slRenderTarget(ctx, chatID, msgID, id, st, "")
			return
		}
		b.slLoadSessions(ctx, chatID, msgID, id, jwt, st)
	case "pg":
		t := &slTarget{Kind: slKindOrgPage, Name: id.Current.OrgName}
		if st.Event != nil && st.Event.PromoterID != nil {
			t = &slTarget{Kind: slKindPromoterPage, PromoterID: st.Event.PromoterID, Name: st.Event.PromoterName}
		}
		st.Target = t
		b.slTargetChosen(ctx, chatID, msgID, id, st)
	case "oe":
		st.Page = 1
		b.slRenderEvents(ctx, chatID, msgID, id, jwt, st)
	case "url":
		st.Page, st.IDs, st.Sessions, st.Target = 1, nil, nil, nil
		if msgID != nil {
			st.MsgID = *msgID
		}
		b.saveSalesLinkState(ctx, id.Link.TelegramUserID, st, slStepURL)
		b.reply(ctx, chatID, msgID, b.texts.T(id.Locale(), "bot.ql.url_q", nil), &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
			{{Text: "« " + b.texts.T(id.Locale(), "bot.btn_back", nil), CallbackData: "ql:tb"}},
			b.slCancelRow(id.Locale()),
		}})
	}
}

// slTargetChosen goes on from a chosen target: a new link asks where it will
// be placed, a retarget asks for its confirmation.
func (b *Bot) slTargetChosen(ctx context.Context, chatID int64, editMsgID *int, id *Identity, st salesLinkDialog) {
	if st.Mode == slModeRetarget {
		b.slRenderConfirm(ctx, chatID, editMsgID, id, st)
		return
	}
	b.slRenderPlacement(ctx, chatID, editMsgID, id, st, "")
}

// slTargetText describes a target in a sentence of the texts.
func (b *Bot) slTargetText(loc string, t slTarget) string {
	switch t.Kind {
	case slKindEvent:
		return b.texts.T(loc, "bot.ql.tgt_event", map[string]any{"Name": Esc(t.Name)})
	case slKindSession:
		return b.texts.T(loc, "bot.ql.tgt_session", map[string]any{"Name": Esc(t.Name), "When": Esc(t.When)})
	case slKindURL:
		return b.texts.T(loc, "bot.ql.tgt_url", map[string]any{"URL": Esc(t.URL)})
	}
	return b.texts.T(loc, "bot.ql.tgt_page", map[string]any{"Name": Esc(t.Name)})
}

// ─── choosing an event or a date ──────────────────────────────────────────────

// slRenderEvents lists the organization's events that are not archived or
// canceled, five to a page. A draft stays on the list: its code leads to the
// page with all events until the event is published. The stored ids are what a press by index is resolved against.
func (b *Bot) slRenderEvents(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt string, st salesLinkDialog) {
	loc := id.Locale()
	all, err := b.arena.ListEvents(ctx, jwt, id.Current.OrgID)
	if err != nil {
		b.ecError(ctx, chatID, editMsgID, id, err, "home")
		return
	}
	events := slSellableEvents(all)
	pages := PagesFor(int64(len(events)), slPageSize)
	if st.Page < 1 {
		st.Page = 1
	}
	if st.Page > pages {
		st.Page = pages
	}
	start := (st.Page - 1) * slPageSize
	end := min(start+slPageSize, len(events))
	page := events[start:end]
	st.IDs = st.IDs[:0]
	for _, e := range page {
		st.IDs = append(st.IDs, e.Id)
	}
	st.Sessions = nil
	if editMsgID != nil {
		st.MsgID = *editMsgID
	}
	b.saveSalesLinkState(ctx, id.Link.TelegramUserID, st, slStepEvents)

	var rows [][]models.InlineKeyboardButton
	text := b.texts.T(loc, "bot.ql.events_q", map[string]any{"Page": st.Page, "Pages": pages})
	if len(page) == 0 {
		text = b.texts.T(loc, "bot.ql.events_none", nil)
	}
	for i, e := range page {
		rows = append(rows, []models.InlineKeyboardButton{{Text: truncate(e.Name, 48), CallbackData: ItemCallback("ql:eo", i)}})
	}
	if nav := b.slPager(loc, Pager{Prefix: "ql:ep", Page: st.Page, Pages: pages}); nav != nil {
		rows = append(rows, nav)
	}
	rows = append(rows,
		[]models.InlineKeyboardButton{{Text: "« " + b.texts.T(loc, "bot.btn_back", nil), CallbackData: "ql:tb"}},
		b.slCancelRow(loc),
	)
	b.reply(ctx, chatID, editMsgID, text, &models.InlineKeyboardMarkup{InlineKeyboard: rows})
}

// slPager is paging.go's row with the position button kept inside the "ql:"
// prefix: a bare "noop" press would end the dialog.
func (b *Bot) slPager(loc string, p Pager) []models.InlineKeyboardButton {
	row := PagerRow(p, b.texts.T(loc, "bot.btn_prev", nil), b.texts.T(loc, "bot.btn_next", nil))
	for i := range row {
		if row[i].CallbackData == calNoop {
			row[i].CallbackData = "ql:noop"
		}
	}
	return row
}

// slSellableEvents drops the events that can no longer be sold.
func slSellableEvents(all []openapi.EventItem) []openapi.EventItem {
	var events []openapi.EventItem
	for _, e := range all {
		if s := string(e.Status); s != "archived" && s != "cancelled" {
			events = append(events, e)
		}
	}
	return events
}

// slEventChosen takes the event the person pressed and goes back to the
// target choice, now with that event in play.
func (b *Bot) slEventChosen(ctx context.Context, chatID int64, msgID *int, id *Identity, jwt string, st salesLinkDialog, arg string) {
	i, ok := ParseIndex(arg, len(st.IDs))
	if !ok {
		b.slRenderEvents(ctx, chatID, msgID, id, jwt, st)
		return
	}
	ev, found, err := b.slFindEvent(ctx, jwt, id.Current.OrgID, st.IDs[i])
	if err != nil {
		b.ecError(ctx, chatID, msgID, id, err, "home")
		return
	}
	if !found {
		b.slRenderEvents(ctx, chatID, msgID, id, jwt, st)
		return
	}
	st.Event = &ev
	b.slRenderTarget(ctx, chatID, msgID, id, st, "")
}

// slLoadSessions reads the event's dates that are not cancelled, with each
// one's label in its venue's zone, and shows the first page.
func (b *Bot) slLoadSessions(ctx context.Context, chatID int64, msgID *int, id *Identity, jwt string, st salesLinkDialog) {
	orgID := id.Current.OrgID
	sessions, err := b.arena.ListSessions(ctx, jwt, orgID, st.Event.ID)
	if err != nil {
		b.ecError(ctx, chatID, msgID, id, err, "home")
		return
	}
	// The list is oldest first and holds the past too: keep the sessions
	// that can still sell, and cut AFTER that, so a long series still reaches
	// its coming dates.
	now := time.Now()
	var coming []openapi.SessionItem
	for _, s := range sessions {
		if string(s.Status) == "cancelled" || !s.StartAt.After(now) || (!s.SalesEndAt.IsZero() && !s.SalesEndAt.After(now)) {
			continue
		}
		if len(coming) >= slMaxSessions {
			break
		}
		coming = append(coming, s)
	}
	// One venue in practice: the first session's zone labels them all. A
	// failed read leaves the labels in UTC.
	tz := ""
	if len(coming) > 0 {
		if sum, err := b.arena.SessionSummary(ctx, jwt, orgID, coming[0].Id); err == nil && sum.Session.VenueTimezone != nil {
			tz = *sum.Session.VenueTimezone
		}
	}
	st.Sessions = nil
	for _, s := range coming {
		st.Sessions = append(st.Sessions, slSess{ID: s.Id, Label: FormatWhen(s.StartAt, tz)})
	}
	st.Page = 1
	b.slRenderSessions(ctx, chatID, msgID, id, st, "")
}

// slRenderSessions draws one page of the stored dates.
func (b *Bot) slRenderSessions(ctx context.Context, chatID int64, editMsgID *int, id *Identity, st salesLinkDialog, note string) {
	loc := id.Locale()
	if st.Event == nil {
		b.slRenderTarget(ctx, chatID, editMsgID, id, st, "")
		return
	}
	pages := PagesFor(int64(len(st.Sessions)), slPageSize)
	if st.Page < 1 {
		st.Page = 1
	}
	if st.Page > pages {
		st.Page = pages
	}
	if editMsgID != nil {
		st.MsgID = *editMsgID
	}
	b.saveSalesLinkState(ctx, id.Link.TelegramUserID, st, slStepSessions)

	text := note + b.texts.T(loc, "bot.ql.sessions_q", map[string]any{"Name": Esc(st.Event.Name), "Page": st.Page, "Pages": pages})
	if len(st.Sessions) == 0 {
		text = note + b.texts.T(loc, "bot.ql.sessions_none", map[string]any{"Name": Esc(st.Event.Name)})
	}
	start := (st.Page - 1) * slPageSize
	end := min(start+slPageSize, len(st.Sessions))
	var rows [][]models.InlineKeyboardButton
	for i := start; i < end; i++ {
		rows = append(rows, []models.InlineKeyboardButton{{Text: st.Sessions[i].Label, CallbackData: ItemCallback("ql:so", i)}})
	}
	if nav := b.slPager(loc, Pager{Prefix: "ql:sp", Page: st.Page, Pages: pages}); nav != nil {
		rows = append(rows, nav)
	}
	rows = append(rows,
		[]models.InlineKeyboardButton{{Text: "« " + b.texts.T(loc, "bot.btn_back", nil), CallbackData: "ql:tb"}},
		b.slCancelRow(loc),
	)
	b.reply(ctx, chatID, editMsgID, text, &models.InlineKeyboardMarkup{InlineKeyboard: rows})
}

// slSessionChosen takes the date the person pressed (its index in the whole
// stored list).
func (b *Bot) slSessionChosen(ctx context.Context, chatID int64, msgID *int, id *Identity, st salesLinkDialog, arg string) {
	i, ok := ParseIndex(arg, len(st.Sessions))
	if !ok || st.Event == nil {
		b.slRenderSessions(ctx, chatID, msgID, id, st, "")
		return
	}
	s := st.Sessions[i]
	sid := s.ID
	st.Target = &slTarget{Kind: slKindSession, SessionID: &sid, EventID: &st.Event.ID, Name: st.Event.Name, When: s.Label}
	b.slTargetChosen(ctx, chatID, msgID, id, st)
}

// ─── the placement and the name ───────────────────────────────────────────────

// slRenderPlacement asks where the code will be put.
func (b *Bot) slRenderPlacement(ctx context.Context, chatID int64, editMsgID *int, id *Identity, st salesLinkDialog, note string) {
	loc := id.Locale()
	if st.Target == nil {
		b.slRenderTarget(ctx, chatID, editMsgID, id, st, "")
		return
	}
	st.Page, st.IDs, st.Sessions = 1, nil, nil
	if editMsgID != nil {
		st.MsgID = *editMsgID
	}
	b.saveSalesLinkState(ctx, id.Link.TelegramUserID, st, slStepPlacement)

	var rows [][]models.InlineKeyboardButton
	var row []models.InlineKeyboardButton
	for _, p := range slPlacements {
		row = append(row, models.InlineKeyboardButton{Text: b.texts.T(loc, "bot.ql.pl_"+p, nil), CallbackData: "ql:pk:" + p})
		if len(row) == 2 {
			rows = append(rows, row)
			row = nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	rows = append(rows,
		[]models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.ql.change_target_btn", nil), CallbackData: "ql:tb"}},
		b.slCancelRow(loc),
	)
	text := note + b.texts.T(loc, "bot.ql.placement_q", map[string]any{"Target": b.slTargetText(loc, *st.Target)})
	b.reply(ctx, chatID, editMsgID, text, &models.InlineKeyboardMarkup{InlineKeyboard: rows})
}

// slPlacementChosen takes the placement and asks for the name of the place.
func (b *Bot) slPlacementChosen(ctx context.Context, chatID int64, msgID *int, id *Identity, st salesLinkDialog, step, kind string) {
	valid := false
	for _, p := range slPlacements {
		valid = valid || p == kind
	}
	if !valid || st.Target == nil || (step != slStepPlacement && step != slStepLabel) {
		b.slRenderTarget(ctx, chatID, msgID, id, st, "")
		return
	}
	st.Placement = kind
	b.slRenderLabelAsk(ctx, chatID, msgID, id, st, "")
}

func (b *Bot) slRenderLabelAsk(ctx context.Context, chatID int64, editMsgID *int, id *Identity, st salesLinkDialog, prefix string) {
	loc := id.Locale()
	if editMsgID != nil {
		st.MsgID = *editMsgID
	}
	b.saveSalesLinkState(ctx, id.Link.TelegramUserID, st, slStepLabel)
	b.reply(ctx, chatID, editMsgID, prefix+b.texts.T(loc, "bot.ql.label_q", map[string]any{
		"Place": b.texts.T(loc, "bot.ql.pl_"+st.Placement, nil),
	}), &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: "« " + b.texts.T(loc, "bot.btn_back", nil), CallbackData: "ql:pb"}},
		b.slCancelRow(loc),
	}})
}

// ─── typed text ───────────────────────────────────────────────────────────────

// parseSalesLinkLabel reads the name of the place. The second result is the
// message key of the reason when the text is refused.
func parseSalesLinkLabel(raw string) (string, string) {
	s := strings.Join(strings.Fields(raw), " ")
	if s == "" {
		return "", "bot.ql.err_label_empty"
	}
	if utf8.RuneCountInString(s) > maxLabelRunes {
		return "", "bot.ql.err_label_long"
	}
	return s, ""
}

// parseSalesLinkURL reads a typed address: http or https, no spaces. The API
// checks the rest (host, credentials).
func parseSalesLinkURL(raw string) (string, string) {
	s := strings.TrimSpace(raw)
	low := strings.ToLower(s)
	hasScheme := strings.HasPrefix(low, "http://") || strings.HasPrefix(low, "https://")
	if !hasScheme || strings.ContainsAny(s, " \t\r\n") || len(s) < 11 {
		return "", "bot.ql.err_url"
	}
	if utf8.RuneCountInString(s) > maxURLRunes {
		return "", "bot.ql.err_url_long"
	}
	return s, ""
}

// salesLinkText consumes the text of a question in progress and reports
// whether it was taken.
func (b *Bot) salesLinkText(ctx context.Context, chatID int64, from *models.User, text string) bool {
	var st salesLinkDialog
	step, found, expired, err := b.dialogs.Load(ctx, from.ID, salesLinkDialogKind, &st)
	if err != nil {
		b.logger.Error("eventbot: sales link dialog load failed", slog.Int64("telegram_user_id", from.ID), slog.String("error", err.Error()))
		return false
	}
	if expired {
		// The lapse is told only for a dialog that was waiting for TYPED text
		// (the name of the place or an address). A lapsed card or choice
		// screen takes no text, so the text goes on to whoever else wants it.
		if step != slStepLabel && step != slStepURL {
			return false
		}
		loc := NormalizeLocale(from.LanguageCode)
		if id, _, err := b.resolveIdentity(ctx, from.ID); err == nil {
			loc = id.Locale()
		}
		b.send(ctx, chatID, b.texts.T(loc, "bot.dialog_expired", nil), &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
			{{Text: b.texts.T(loc, "bot.ql.menu_btn", nil), CallbackData: "ql:menu"}},
			{{Text: b.texts.T(loc, "bot.btn_home", nil), CallbackData: "home"}},
		}})
		return true
	}
	if !found || (step != slStepLabel && step != slStepURL) {
		return false
	}
	id, jwt, err := b.resolveIdentity(ctx, from.ID)
	if err != nil || id.Current == nil || id.Current.OrgID != st.OrgID || !canSalesLinks(id) {
		b.leaveSalesLink(ctx, from.ID)
		return false
	}
	loc := id.Locale()
	var edit *int
	if st.MsgID != 0 {
		m := st.MsgID
		edit = &m
	}
	if step == slStepURL {
		url, errKey := parseSalesLinkURL(text)
		if errKey != "" {
			b.reply(ctx, chatID, edit, b.texts.T(loc, errKey, nil)+"\n\n"+b.texts.T(loc, "bot.ql.url_q", nil), &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
				{{Text: "« " + b.texts.T(loc, "bot.btn_back", nil), CallbackData: "ql:tb"}},
				b.slCancelRow(loc),
			}})
			return true
		}
		st.Target = &slTarget{Kind: slKindURL, URL: url}
		b.slTargetChosen(ctx, chatID, edit, id, st)
		return true
	}
	label, errKey := parseSalesLinkLabel(text)
	if errKey != "" {
		b.slRenderLabelAsk(ctx, chatID, edit, id, st, b.texts.T(loc, errKey, nil)+"\n\n")
		return true
	}
	b.slCreate(ctx, chatID, edit, id, jwt, st, label)
	return true
}

// slCreate makes the link and answers with the code, the address and the
// card. A refusal the bot has words for goes back to the question that can
// fix it.
func (b *Bot) slCreate(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt string, st salesLinkDialog, label string) {
	loc := id.Locale()
	if st.Target == nil || st.Placement == "" {
		b.slRenderTarget(ctx, chatID, editMsgID, id, st, "")
		return
	}
	link, err := b.arena.CreateSalesLink(ctx, jwt, id.Current.OrgID, SalesLinkCreate{
		Target: st.Target.api(), Label: label, PlacementKind: st.Placement,
	})
	if err != nil {
		switch APIErrorCode(err) {
		case "sales_link.invalid_target":
			b.slRenderTarget(ctx, chatID, editMsgID, id, st, b.texts.T(loc, "bot.ql.err_target", nil)+"\n\n")
			return
		case "sales_link.invalid_url":
			b.slRenderTarget(ctx, chatID, editMsgID, id, st, b.texts.T(loc, "bot.ql.err_url_refused", nil)+"\n\n")
			return
		case "sales_link.invalid_label":
			b.slRenderLabelAsk(ctx, chatID, editMsgID, id, st, b.texts.T(loc, "bot.ql.err_label_long", nil)+"\n\n")
			return
		}
		b.ecError(ctx, chatID, editMsgID, id, err, "home")
		return
	}
	// The link exists. Its card needs no state (every button carries the
	// link's UUID), so the dialog ends here: it would only sit for 30 minutes
	// and take for an answer a text meant for something else.
	b.leaveSalesLink(ctx, id.Link.TelegramUserID)

	b.slSendQR(ctx, chatID, jwt, id.Current.OrgID, loc, link)
	b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.ql.created", nil)+"\n\n"+b.slCardText(loc, link), b.slCardKeyboard(loc, link))
}

// ─── the code itself ──────────────────────────────────────────────────────────

// slSendQR sends the code: the PNG as a photo (what a phone shows at once) and
// the SVG as a document (what a printer wants). A failure is told in one line
// with the way out and never blocks the card.
func (b *Bot) slSendQR(ctx context.Context, chatID int64, jwt string, orgID uuid.UUID, loc string, link openapi.SalesLink) {
	failed := false
	png, err := b.arena.SalesLinkQR(ctx, jwt, orgID, link.Id, "png", 1024)
	if err != nil {
		b.logger.Warn("eventbot: sales link qr (png) failed", slog.String("link_id", link.Id.String()), slog.String("error", err.Error()))
		failed = true
	} else if _, err := b.tg.SendPhoto(ctx, &tgbot.SendPhotoParams{
		ChatID:    chatID,
		Photo:     &models.InputFileUpload{Filename: link.Code + ".png", Data: bytes.NewReader(png)},
		Caption:   b.texts.T(loc, "bot.ql.qr_caption", map[string]any{"Label": Esc(link.Label)}),
		ParseMode: models.ParseModeHTML,
	}); err != nil {
		b.logger.Warn("eventbot: send sales link photo failed", slog.Int64("chat_id", chatID), slog.String("error", unwrapURLError(err).Error()))
		failed = true
	}
	svg, err := b.arena.SalesLinkQR(ctx, jwt, orgID, link.Id, "svg", 0)
	if err != nil {
		b.logger.Warn("eventbot: sales link qr (svg) failed", slog.String("link_id", link.Id.String()), slog.String("error", err.Error()))
		failed = true
	} else if _, err := b.tg.SendDocument(ctx, &tgbot.SendDocumentParams{
		ChatID:    chatID,
		Document:  &models.InputFileUpload{Filename: link.Code + ".svg", Data: bytes.NewReader(svg)},
		Caption:   b.texts.T(loc, "bot.ql.svg_caption", nil),
		ParseMode: models.ParseModeHTML,
	}); err != nil {
		b.logger.Warn("eventbot: send sales link svg failed", slog.Int64("chat_id", chatID), slog.String("error", unwrapURLError(err).Error()))
		failed = true
	}
	if failed {
		b.send(ctx, chatID, b.texts.T(loc, "bot.ql.qr_failed", nil), nil)
	}
}

// ─── the card ─────────────────────────────────────────────────────────────────

// slCardText is the card of a link: its name and place, the short address to
// print, and the page the code leads to right now.
func (b *Bot) slCardText(loc string, l openapi.SalesLink) string {
	var sb strings.Builder
	place := b.texts.T(loc, "bot.ql.pl_"+l.PlacementKind, nil)
	sb.WriteString(b.texts.T(loc, "bot.ql.card_head", map[string]any{"Label": Esc(l.Label), "Place": Esc(place)}))
	sb.WriteString("\n" + b.texts.T(loc, "bot.ql.card_short", map[string]any{"URL": Esc(l.ShortUrl)}))
	if l.DestinationUrl != "" {
		sb.WriteString("\n" + b.texts.T(loc, "bot.ql.card_dest", map[string]any{"URL": Esc(l.DestinationUrl)}))
	} else {
		sb.WriteString("\n" + b.texts.T(loc, "bot.ql.card_no_dest", nil))
	}
	switch {
	case l.Status == slStatusArchived:
		sb.WriteString("\n\n" + b.texts.T(loc, "bot.ql.card_archived", nil))
	case l.DestinationDegraded:
		sb.WriteString("\n\n" + b.texts.T(loc, "bot.ql.card_degraded", nil))
	}
	sb.WriteString("\n\n" + b.texts.T(loc, "bot.ql.card_counts", map[string]any{"Scans": l.ScanCount, "Orders": l.OrdersCount}))
	return sb.String()
}

// slCardKeyboard is the card's buttons. Every one carries the link's UUID, so
// a card in the chat keeps meaning its link.
func (b *Bot) slCardKeyboard(loc string, l openapi.SalesLink) *models.InlineKeyboardMarkup {
	lid := l.Id.String()
	archive := models.InlineKeyboardButton{Text: b.texts.T(loc, "bot.ql.archive_btn", nil), CallbackData: "ql:ar:" + lid}
	if l.Status == slStatusArchived {
		archive = models.InlineKeyboardButton{Text: b.texts.T(loc, "bot.ql.unarchive_btn", nil), CallbackData: "ql:ua:" + lid}
	}
	rows := [][]models.InlineKeyboardButton{
		{{Text: b.texts.T(loc, "bot.ql.stats_btn", nil), CallbackData: "ql:st:" + lid}},
		{{Text: b.texts.T(loc, "bot.ql.retarget_btn", nil), CallbackData: "ql:rt:" + lid}, archive},
		{{Text: b.texts.T(loc, "bot.ql.qr_again_btn", nil), CallbackData: "ql:qr:" + lid}},
	}
	if l.Target.EventId != nil {
		rows = append(rows, []models.InlineKeyboardButton{
			{Text: b.texts.T(loc, "bot.ql.list_event_btn", nil), CallbackData: "ql:le:" + l.Target.EventId.String()},
		})
	}
	rows = append(rows, []models.InlineKeyboardButton{
		{Text: b.texts.T(loc, "bot.ql.list_btn", nil), CallbackData: "ql:list"},
		{Text: b.texts.T(loc, "bot.btn_home", nil), CallbackData: "home"},
	})
	return &models.InlineKeyboardMarkup{InlineKeyboard: rows}
}

// slShowCard reads the link afresh and draws its card. A link that is not the
// organization's reads as gone.
func (b *Bot) slShowCard(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt string, linkID uuid.UUID, note string) {
	link, ok := b.slGetLink(ctx, chatID, editMsgID, id, jwt, linkID)
	if !ok {
		return
	}
	b.reply(ctx, chatID, editMsgID, note+b.slCardText(id.Locale(), link), b.slCardKeyboard(id.Locale(), link))
}

// slGetLink reads one link and answers the standard screens itself when it
// cannot: a link of another organization, or one that is gone, is "gone".
func (b *Bot) slGetLink(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt string, linkID uuid.UUID) (openapi.SalesLink, bool) {
	link, err := b.arena.GetSalesLink(ctx, jwt, id.Current.OrgID, linkID)
	if err == nil {
		return link, true
	}
	if APIErrorCode(err) == "sales_link.not_found" {
		loc := id.Locale()
		b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.ql.gone", nil), &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
			{{Text: b.texts.T(loc, "bot.ql.menu_btn", nil), CallbackData: "ql:menu"}},
			{{Text: b.texts.T(loc, "bot.btn_home", nil), CallbackData: "home"}},
		}})
		return openapi.SalesLink{}, false
	}
	b.ecError(ctx, chatID, editMsgID, id, err, "home")
	return openapi.SalesLink{}, false
}

// slLinkPress handles the presses that name a link by its UUID: the card, the
// statistics, the code again, the archive and the retarget. They work after
// the dialog ran out; only the retarget's confirmation needs the question the
// dialog remembers.
func (b *Bot) slLinkPress(ctx context.Context, chatID int64, msgID int, from *models.User, id *Identity, jwt, kind, arg string) {
	loc := id.Locale()
	orgID := id.Current.OrgID
	confirm := false
	if kind == "rt" && strings.HasPrefix(arg, "ok:") {
		confirm, arg = true, strings.TrimPrefix(arg, "ok:")
	}
	linkID, err := uuid.Parse(arg)
	if err != nil {
		return
	}
	if !confirm {
		// A press on a link's card ends whatever question was open (the name
		// of a place, an address), so the next text is not taken for its answer.
		// The retarget's confirmation keeps the dialog: it needs the question.
		b.leaveSalesLink(ctx, from.ID)
	}
	switch kind {
	case "v":
		b.slShowCard(ctx, chatID, &msgID, id, jwt, linkID, "")
	case "st":
		b.slShowStats(ctx, chatID, &msgID, id, jwt, linkID)
	case "qr":
		link, ok := b.slGetLink(ctx, chatID, &msgID, id, jwt, linkID)
		if !ok {
			return
		}
		b.slSendQR(ctx, chatID, jwt, orgID, loc, link)
		b.reply(ctx, chatID, &msgID, b.slCardText(loc, link), b.slCardKeyboard(loc, link))
	case "ar", "ua":
		status, done := slStatusArchived, "bot.ql.archived_done"
		if kind == "ua" {
			status, done = slStatusActive, "bot.ql.unarchived_done"
		}
		link, err := b.arena.UpdateSalesLink(ctx, jwt, orgID, linkID, SalesLinkPatch{Status: &status})
		if err != nil {
			if APIErrorCode(err) == "sales_link.not_found" {
				b.slShowCard(ctx, chatID, &msgID, id, jwt, linkID, "")
				return
			}
			b.ecError(ctx, chatID, &msgID, id, err, "home")
			return
		}
		b.reply(ctx, chatID, &msgID, b.texts.T(loc, done, nil)+"\n\n"+b.slCardText(loc, link), b.slCardKeyboard(loc, link))
	case "rt":
		if confirm {
			b.slRetargetConfirmed(ctx, chatID, &msgID, from, id, jwt, linkID)
			return
		}
		b.slRetargetStart(ctx, chatID, &msgID, id, jwt, linkID)
	}
}

// ─── statistics ───────────────────────────────────────────────────────────────

// slStatsDay is a day as the API names it (UTC).
func slStatsDay(t time.Time) string {
	// allow:timeformat: the API's calendar-day key, not a wire timestamp
	return t.UTC().Format("2006-01-02")
}

// slScanSums adds up the scans of today and of the last seven days (today
// included) from the per-day series, which the API counts in UTC days.
func slScanSums(days []openapi.SalesLinkDayStats, now time.Time) (today, week int64) {
	todayKey := slStatsDay(now)
	weekFrom := slStatsDay(now.AddDate(0, 0, -6))
	for _, d := range days {
		if d.Day == todayKey {
			today += d.Hits
		}
		if d.Day >= weekFrom && d.Day <= todayKey {
			week += d.Hits
		}
	}
	return today, week
}

// slShowStats draws the numbers of one link.
func (b *Bot) slShowStats(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt string, linkID uuid.UUID) {
	loc := id.Locale()
	link, ok := b.slGetLink(ctx, chatID, editMsgID, id, jwt, linkID)
	if !ok {
		return
	}
	stats, err := b.arena.SalesLinkStats(ctx, jwt, id.Current.OrgID, linkID, "", "")
	if err != nil {
		b.ecError(ctx, chatID, editMsgID, id, err, "home")
		return
	}
	today, week := slScanSums(stats.ByDay, time.Now())
	var sb strings.Builder
	sb.WriteString(b.texts.T(loc, "bot.ql.stats_head", map[string]any{"Label": Esc(link.Label)}))
	sb.WriteString("\n\n" + b.texts.T(loc, "bot.ql.stats_scans", map[string]any{"Total": stats.ScansTotal, "Week": week, "Today": today}))
	sb.WriteString("\n" + b.texts.T(loc, "bot.ql.stats_utc", nil))
	if stats.SuspectTotal > 0 {
		sb.WriteString("\n" + b.texts.T(loc, "bot.ql.stats_bots", map[string]any{"Count": stats.SuspectTotal}))
	}
	sb.WriteString("\n\n" + b.texts.T(loc, "bot.ql.stats_orders", map[string]any{"Orders": stats.Orders.Count, "Tickets": stats.Orders.Tickets}))
	if len(stats.Orders.Revenue) == 0 {
		sb.WriteString("\n" + b.texts.T(loc, "bot.ql.stats_no_revenue", nil))
	} else {
		parts := make([]string, 0, len(stats.Orders.Revenue))
		for _, r := range stats.Orders.Revenue {
			parts = append(parts, FormatMoney(r.AmountMinor, r.Currency, loc))
		}
		sb.WriteString("\n" + b.texts.T(loc, "bot.ql.stats_revenue", map[string]any{"Amount": Esc(strings.Join(parts, ", "))}))
	}
	b.reply(ctx, chatID, editMsgID, sb.String(), &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: "« " + b.texts.T(loc, "bot.btn_back", nil), CallbackData: "ql:v:" + linkID.String()}},
		{{Text: b.texts.T(loc, "bot.btn_home", nil), CallbackData: "home"}},
	}})
}

// ─── retarget ─────────────────────────────────────────────────────────────────

// slRetargetStart opens the target choice for an existing link.
func (b *Bot) slRetargetStart(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt string, linkID uuid.UUID) {
	loc := id.Locale()
	orgID := id.Current.OrgID
	link, ok := b.slGetLink(ctx, chatID, editMsgID, id, jwt, linkID)
	if !ok {
		return
	}
	if link.Status == slStatusArchived {
		// A code in the archive leads to the page of all events whatever its
		// target says: restore it first, then move it.
		b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.ql.rt_archived", nil), b.slCardKeyboard(loc, link))
		return
	}
	st := newSalesLinkDialog(orgID)
	st.Mode, st.LinkID, st.LinkLabel = slModeRetarget, &link.Id, link.Label
	if link.Target.EventId != nil {
		if ev, found, err := b.slFindEvent(ctx, jwt, orgID, *link.Target.EventId); err == nil && found {
			st.Event = &ev
		}
	}
	b.slRenderTarget(ctx, chatID, editMsgID, id, st, b.texts.T(loc, "bot.ql.rt_head", map[string]any{"Label": Esc(link.Label)})+"\n\n")
}

// slRenderConfirm asks the one press that moves the code.
func (b *Bot) slRenderConfirm(ctx context.Context, chatID int64, editMsgID *int, id *Identity, st salesLinkDialog) {
	loc := id.Locale()
	if st.LinkID == nil || st.Target == nil {
		b.slRenderTarget(ctx, chatID, editMsgID, id, st, "")
		return
	}
	if editMsgID != nil {
		st.MsgID = *editMsgID
	}
	b.saveSalesLinkState(ctx, id.Link.TelegramUserID, st, slStepConfirm)
	b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.ql.rt_confirm", map[string]any{
		"Label": Esc(st.LinkLabel), "Target": b.slTargetText(loc, *st.Target),
	}), &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: b.texts.T(loc, "bot.ql.rt_yes_btn", nil), CallbackData: "ql:rt:ok:" + st.LinkID.String()}},
		{{Text: "« " + b.texts.T(loc, "bot.btn_back", nil), CallbackData: "ql:tb"}},
		b.slCancelRow(loc),
	}})
}

// slRetargetConfirmed moves the code. It needs the confirmation the dialog is
// waiting for, for THIS link: an old button, or one of an older question,
// moves nothing.
func (b *Bot) slRetargetConfirmed(ctx context.Context, chatID int64, editMsgID *int, from *models.User, id *Identity, jwt string, linkID uuid.UUID) {
	loc := id.Locale()
	orgID := id.Current.OrgID
	st, step, found, _ := b.loadSalesLinkState(ctx, from.ID, orgID)
	if !found || step != slStepConfirm || st.LinkID == nil || *st.LinkID != linkID || st.Target == nil {
		b.slShowCard(ctx, chatID, editMsgID, id, jwt, linkID, "")
		return
	}
	target := st.Target.api()
	link, err := b.arena.UpdateSalesLink(ctx, jwt, orgID, linkID, SalesLinkPatch{Target: &target})
	if err != nil {
		switch APIErrorCode(err) {
		case "sales_link.invalid_target":
			b.slRenderTarget(ctx, chatID, editMsgID, id, st, b.texts.T(loc, "bot.ql.err_target", nil)+"\n\n")
			return
		case "sales_link.invalid_url":
			b.slRenderTarget(ctx, chatID, editMsgID, id, st, b.texts.T(loc, "bot.ql.err_url_refused", nil)+"\n\n")
			return
		case "sales_link.not_found":
			b.slShowCard(ctx, chatID, editMsgID, id, jwt, linkID, "")
			return
		}
		b.ecError(ctx, chatID, editMsgID, id, err, "home")
		return
	}
	b.leaveSalesLink(ctx, from.ID)
	b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.ql.rt_done", map[string]any{"Label": Esc(link.Label)})+"\n\n"+b.slCardText(loc, link), b.slCardKeyboard(loc, link))
}
