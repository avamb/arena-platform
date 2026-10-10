package eventbot

// events_list.go — "My events" (EC-02, spec 35 §5.3): the list with the
// filters "Running" (the default) and "Archive", a state chip on every row
// (taken from the server's sales_state — the bot never works the state out
// itself), five rows a page, and a text search over the names of the events
// already loaded (an organization has tens of events, so the filter is local).
//
// Where the person is — the filter, the search text, the page, and the ids of
// the rows on screen — lives in bot_dialogs under kind "events" (dialogs.go),
// so a restart or a deploy leaves the list as it was and the buttons of the
// message already in the chat keep working. A row button carries the row's
// INDEX on the page (paging.go), resolved against the stored ids.

import (
	"context"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

const (
	eventsDialogKind = "events"
	// The dialog is "list" while the list is the screen on show (a typed text
	// is then a search) and "card" while an event card is.
	eventsStepList = "list"
	eventsStepCard = "card"

	evFilterRun = "run"
	evFilterArc = "arc"

	// maxQueryRunes bounds a search text; a longer one is cut.
	maxQueryRunes = 60
)

// The sales states the server reports (hcatalog/sales_state.go).
const (
	salesStateOnSale   = "on_sale"
	salesStateUpcoming = "upcoming"
	salesStateSoldOut  = "sold_out"
	salesStateArchived = "archived"
)

// eventsDialog is the list's state, stored as the bot_dialogs row's JSON.
type eventsDialog struct {
	OrgID  uuid.UUID   `json:"org_id"`
	Filter string      `json:"filter"`
	Query  string      `json:"query,omitempty"`
	Page   int         `json:"page"`
	IDs    []uuid.UUID `json:"ids,omitempty"` // the events on the page on screen, in order
	MsgID  int         `json:"msg_id,omitempty"`
}

func newEventsDialog(orgID uuid.UUID) eventsDialog {
	return eventsDialog{OrgID: orgID, Filter: evFilterRun, Page: 1}
}

// ─── pure parts ───────────────────────────────────────────────────────────────

// EventChip is the one-glyph state of a row: ● running, ○ soon, ✕ sold out,
// ✓ archive. An empty or unknown state (the server could not work it out)
// shows a neutral dot rather than a wrong answer.
func EventChip(salesState string) string {
	switch salesState {
	case salesStateOnSale:
		return "●"
	case salesStateUpcoming:
		return "○"
	case salesStateSoldOut:
		return "✕"
	case salesStateArchived:
		return "✓"
	default:
		return "·"
	}
}

// EventInFilter reports whether the event belongs to the filter: the archive
// holds the archived events, "Running" everything else (selling, soon, sold
// out, and an event whose state is unknown).
func EventInFilter(e openapi.EventItem, filter string) bool {
	archived := e.SalesState == salesStateArchived
	if filter == evFilterArc {
		return archived
	}
	return !archived
}

// foldName lowers a name for matching; ё and е are one letter to a Russian
// typist.
func foldName(s string) string {
	return strings.ReplaceAll(strings.ToLower(s), "ё", "е")
}

// NameMatches is the search: every word of the query occurs in the name,
// case-insensitively, in any order. An empty query matches everything.
func NameMatches(name, query string) bool {
	words := strings.Fields(foldName(query))
	if len(words) == 0 {
		return true
	}
	folded := foldName(name)
	for _, w := range words {
		if !strings.Contains(folded, w) {
			return false
		}
	}
	return true
}

// FilterEvents keeps the events of the filter whose name matches the query,
// in list order: running events by their next date (the soonest first, those
// without a date last), the archive by its last date, the newest first.
func FilterEvents(events []openapi.EventItem, filter, query string) []openapi.EventItem {
	out := make([]openapi.EventItem, 0, len(events))
	for _, e := range events {
		if EventInFilter(e, filter) && NameMatches(e.Name, query) {
			out = append(out, e)
		}
	}
	if filter == evFilterArc {
		sort.SliceStable(out, func(i, j int) bool {
			return laterFirst(out[i].LastSessionAt, out[j].LastSessionAt, out[i].Name, out[j].Name)
		})
		return out
	}
	sort.SliceStable(out, func(i, j int) bool {
		return soonerFirst(nextDate(out[i]), nextDate(out[j]), out[i].Name, out[j].Name)
	})
	return out
}

// nextDate is the date a running event is listed by: the next session that
// has not ended, else the first one.
func nextDate(e openapi.EventItem) *time.Time {
	if e.NextSessionAt != nil {
		return e.NextSessionAt
	}
	return e.FirstSessionAt
}

func soonerFirst(a, b *time.Time, nameA, nameB string) bool {
	switch {
	case a == nil && b == nil:
		return nameA < nameB
	case a == nil:
		return false
	case b == nil:
		return true
	case a.Equal(*b):
		return nameA < nameB
	default:
		return a.Before(*b)
	}
}

func laterFirst(a, b *time.Time, nameA, nameB string) bool {
	switch {
	case a == nil && b == nil:
		return nameA < nameB
	case a == nil:
		return false
	case b == nil:
		return true
	case a.Equal(*b):
		return nameA < nameB
	default:
		return a.After(*b)
	}
}

// CountByFilter is how many events each filter holds (for the button labels).
func CountByFilter(events []openapi.EventItem) (running, archive int) {
	for _, e := range events {
		if EventInFilter(e, evFilterArc) {
			archive++
		} else {
			running++
		}
	}
	return running, archive
}

// ─── state in bot_dialogs ─────────────────────────────────────────────────────

// loadEventsState reads the list's state, or starts the default one when
// there is none, it ran out (expired is then true, once) or it belongs to
// another organization.
func (b *Bot) loadEventsState(ctx context.Context, tg int64, orgID uuid.UUID) (st eventsDialog, step string, expired bool) {
	step, found, expired, err := b.dialogs.Load(ctx, tg, eventsDialogKind, &st)
	if err != nil {
		b.logger.Error("eventbot: events dialog load failed", slog.Int64("telegram_user_id", tg), slog.String("error", err.Error()))
		return newEventsDialog(orgID), "", false
	}
	if !found || st.OrgID != orgID || (st.Filter != evFilterRun && st.Filter != evFilterArc) {
		return newEventsDialog(orgID), "", expired
	}
	if st.Page < 1 {
		st.Page = 1
	}
	return st, step, false
}

func (b *Bot) saveEventsState(ctx context.Context, tg int64, st eventsDialog, step string) {
	orgID := st.OrgID
	if err := b.dialogs.Save(ctx, tg, &orgID, eventsDialogKind, step, st, dialogTTL); err != nil {
		b.logger.Error("eventbot: events dialog save failed", slog.Int64("telegram_user_id", tg), slog.String("error", err.Error()))
	}
}

// leaveEvents ends the list's dialog: whoever opens another screen is no
// longer searching, so a text they type next is not taken for a search.
func (b *Bot) leaveEvents(ctx context.Context, tg int64) {
	if err := b.dialogs.Delete(ctx, tg, eventsDialogKind); err != nil {
		b.logger.Warn("eventbot: events dialog clear failed", slog.Int64("telegram_user_id", tg), slog.String("error", err.Error()))
	}
}

// isEventsCallback reports whether a callback prefix belongs to the screens
// that keep the list's dialog alive (the list, an event card and what opens
// from it); a press of anything else leaves the dialog.
func isEventsCallback(prefix string) bool {
	switch prefix {
	case "el", "ec", "events", "event", "noop":
		return true
	}
	return false
}

// isOrdersCallback reports whether a callback prefix belongs to the Orders
// screens (the paging button's "noop" keeps them alive too); a press of
// anything else ends their search / cancel-word dialog.
func isOrdersCallback(prefix string) bool {
	return prefix == "or" || prefix == "noop"
}

// ─── screens ──────────────────────────────────────────────────────────────────

// showEvents is the list at the page asked for (page <= 0: where the person
// was), keeping the stored filter and search. The home menu and /events open
// the list fresh instead (showEventsFresh).
func (b *Bot) showEvents(ctx context.Context, chatID int64, editMsgID *int, from *models.User, page int) {
	id, jwt, err := b.resolveIdentity(ctx, from.ID)
	if err != nil {
		b.replyIdentityError(ctx, chatID, from, err)
		return
	}
	if id.Current == nil {
		b.showOrgChooserFor(ctx, chatID, editMsgID, id)
		return
	}
	st, _, _ := b.loadEventsState(ctx, from.ID, id.Current.OrgID)
	if page > 0 {
		st.Page = page
	}
	b.renderEvents(ctx, chatID, editMsgID, id, jwt, st, "")
}

// showEventsFresh opens the list with the default filter and no search.
func (b *Bot) showEventsFresh(ctx context.Context, chatID int64, editMsgID *int, from *models.User, note string) {
	id, jwt, err := b.resolveIdentity(ctx, from.ID)
	if err != nil {
		b.replyIdentityError(ctx, chatID, from, err)
		return
	}
	if id.Current == nil {
		b.showOrgChooserFor(ctx, chatID, editMsgID, id)
		return
	}
	b.renderEvents(ctx, chatID, editMsgID, id, jwt, newEventsDialog(id.Current.OrgID), note)
}

// renderEvents loads the events, applies the state's filter and search, and
// draws one page. It stores the state — with the ids of the rows it shows —
// before answering, so a press on this very message finds them.
func (b *Bot) renderEvents(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt string, st eventsDialog, note string) {
	loc := id.Locale()
	events, err := b.arena.ListEvents(ctx, jwt, st.OrgID)
	if err != nil {
		b.replyAPIError(ctx, chatID, editMsgID, id, err)
		return
	}
	if len(events) == 0 {
		b.leaveEvents(ctx, id.Link.TelegramUserID)
		b.reply(ctx, chatID, editMsgID, note+b.texts.T(loc, "bot.events_empty", nil), b.backKeyboard(loc, "home"))
		return
	}
	filtered := FilterEvents(events, st.Filter, st.Query)
	items, page, pages := PageOf(filtered, st.Page, listPageSize)
	st.Page = page
	st.IDs = st.IDs[:0]
	for _, e := range items {
		st.IDs = append(st.IDs, e.Id)
	}
	if editMsgID != nil {
		st.MsgID = *editMsgID
	}
	b.saveEventsState(ctx, id.Link.TelegramUserID, st, eventsStepList)

	running, archive := CountByFilter(events)
	filterLabel := b.texts.T(loc, "bot.ec.filter_run", map[string]any{"N": running})
	if st.Filter == evFilterArc {
		filterLabel = b.texts.T(loc, "bot.ec.filter_arc", map[string]any{"N": archive})
	}
	search := ""
	if st.Query != "" {
		search = "\n" + b.texts.T(loc, "bot.ec.search_line", map[string]any{"Query": Esc(st.Query), "N": len(filtered)})
	}
	text := note + b.texts.T(loc, "bot.ec.list_title", map[string]any{
		"Org": Esc(id.Current.OrgName), "Filter": filterLabel, "Page": page, "Pages": pages,
		"Search": search, "Legend": b.texts.T(loc, "bot.ec.legend", nil),
	})
	if len(filtered) == 0 {
		text += "\n\n" + b.emptyListText(loc, st)
	}

	mark := func(label, filter string) string {
		if st.Filter == filter {
			return "✔ " + label
		}
		return label
	}
	rows := [][]models.InlineKeyboardButton{{
		{Text: mark(b.texts.T(loc, "bot.ec.filter_run", map[string]any{"N": running}), evFilterRun), CallbackData: "el:f:" + evFilterRun},
		{Text: mark(b.texts.T(loc, "bot.ec.filter_arc", map[string]any{"N": archive}), evFilterArc), CallbackData: "el:f:" + evFilterArc},
	}}
	for i, e := range items {
		rows = append(rows, []models.InlineKeyboardButton{{Text: eventRowLabel(e, st.Filter), CallbackData: ItemCallback("el:o", i)}})
	}
	if nav := PagerRow(Pager{Prefix: "el:p", Page: page, Pages: pages},
		b.texts.T(loc, "bot.btn_prev", nil), b.texts.T(loc, "bot.btn_next", nil)); nav != nil {
		rows = append(rows, nav)
	}
	if st.Query != "" {
		rows = append(rows, []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.ec.clear_search_btn", nil), CallbackData: "el:x"}})
	}
	rows = append(rows, []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.btn_home", nil), CallbackData: "home"}})
	b.reply(ctx, chatID, editMsgID, text, &models.InlineKeyboardMarkup{InlineKeyboard: rows})
}

func (b *Bot) emptyListText(loc string, st eventsDialog) string {
	switch {
	case st.Query != "":
		return b.texts.T(loc, "bot.ec.search_none", map[string]any{"Query": Esc(st.Query)})
	case st.Filter == evFilterArc:
		return b.texts.T(loc, "bot.ec.empty_arc", nil)
	default:
		return b.texts.T(loc, "bot.ec.empty_run", nil)
	}
}

// eventRowLabel is the text of a row's button: the state chip, a draft mark,
// the date the event is listed by, the name, and "×N" for several dates.
func eventRowLabel(e openapi.EventItem, filter string) string {
	label := EventChip(e.SalesState) + " "
	if e.Status == openapi.EventItemStatusDraft {
		label += "📝 "
	}
	date := nextDate(e)
	if filter == evFilterArc && e.LastSessionAt != nil {
		date = e.LastSessionAt
	}
	if date != nil {
		// allow:timeformat: day.month label on a chat button, not a wire timestamp
		label += date.UTC().Format("02.01") + " · "
	}
	label += truncate(e.Name, 36)
	if e.SessionCount > 1 {
		label += " ×" + strconv.Itoa(e.SessionCount)
	}
	return label
}

// ─── presses and text ─────────────────────────────────────────────────────────

// eventsCallback handles every "el:<data>" press of the list.
func (b *Bot) eventsCallback(ctx context.Context, chatID int64, msgID int, from *models.User, data string) {
	id, jwt, err := b.resolveIdentity(ctx, from.ID)
	if err != nil {
		b.replyIdentityError(ctx, chatID, from, err)
		return
	}
	if id.Current == nil {
		b.showOrgChooserFor(ctx, chatID, &msgID, id)
		return
	}
	loc := id.Locale()
	orgID := id.Current.OrgID
	kind, arg, _ := strings.Cut(data, ":")
	if kind == "new" {
		b.renderEvents(ctx, chatID, &msgID, id, jwt, newEventsDialog(orgID), "")
		return
	}
	st, _, expired := b.loadEventsState(ctx, from.ID, orgID)
	note := ""
	if expired {
		// The list ran out while the person was away: say so once, and start
		// the default list rather than act on a press made on stale rows.
		b.renderEvents(ctx, chatID, &msgID, id, jwt, newEventsDialog(orgID), b.texts.T(loc, "bot.dialog_expired", nil)+"\n\n")
		return
	}
	switch kind {
	case "b":
	case "f":
		if arg != evFilterRun && arg != evFilterArc {
			return
		}
		st.Filter, st.Page = arg, 1
	case "p":
		st.Page = ParsePage(arg)
	case "x":
		st.Query, st.Page = "", 1
	case "o":
		i, ok := ParseIndex(arg, len(st.IDs))
		if !ok {
			// The row is gone from the stored page (a stale message): show
			// the list as it is now instead of opening the wrong event.
			b.renderEvents(ctx, chatID, &msgID, id, jwt, st, note)
			return
		}
		b.showEventCard(ctx, chatID, &msgID, from, st.IDs[i], false)
		return
	default:
		return
	}
	b.renderEvents(ctx, chatID, &msgID, id, jwt, st, note)
}

// eventsText takes a typed text as the search while the list is on screen.
// It reports whether the text was taken. An expired list is reported once,
// like every dialog (spec 35 §4.1).
func (b *Bot) eventsText(ctx context.Context, chatID int64, from *models.User, text string) bool {
	var st eventsDialog
	step, found, expired, err := b.dialogs.Load(ctx, from.ID, eventsDialogKind, &st)
	if err != nil {
		b.logger.Error("eventbot: events dialog load failed", slog.Int64("telegram_user_id", from.ID), slog.String("error", err.Error()))
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
	if !found || step != eventsStepList {
		return false
	}
	id, jwt, err := b.resolveIdentity(ctx, from.ID)
	if err != nil || id.Current == nil || id.Current.OrgID != st.OrgID {
		b.leaveEvents(ctx, from.ID)
		return false
	}
	st.Query = cutRunes(strings.Join(strings.Fields(text), " "), maxQueryRunes)
	st.Page = 1
	var edit *int
	if st.MsgID != 0 {
		edit = &st.MsgID
	}
	b.renderEvents(ctx, chatID, edit, id, jwt, st, "")
	return true
}

// cutRunes shortens s to at most n runes.
func cutRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
