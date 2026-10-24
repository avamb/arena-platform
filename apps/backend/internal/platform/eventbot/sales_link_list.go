package eventbot

// sales_link_list.go — the list of the organization's links behind the main
// menu's "Links and QR" (spec 43 §5.2, QL-9). Five links a page, each with its
// name, where it leads and its scans and orders; a status filter (active, the
// archive, all) and a narrowing to one event.
//
// The list has its OWN dialog row (kind "sales_link_list"), apart from the
// creation dialog (kind "sales_link", sales_link_dialog.go): a press on a
// link's card ends the creation dialog so a text typed next is not taken for
// the name of a place, and the list must survive that. It owns the same "ql:"
// presses, so a card button never ends it, takes no typed text, and a restart
// keeps the filter and the page.
//
// The API pages with a keyset cursor and only goes forward, so the state keeps
// the stack of cursors that led to the page on screen: Cursors[k] is the
// cursor that fetches page k+1. A press of "back" fetches again with the
// stored cursor. A row is named by its INDEX on the page (paging.go), resolved
// against the ids the screen stored; a press on a link's card names the link.
//
//	ql:menu            the list, filters reset      ql:list  the list as it was
//	ql:o:<i>           open row i                   ql:new   a new link
//	ql:lp:<n>          page n (next or previous)    ql:le:<event>  one event
//	ql:lf:<f>          status filter active|archived|all
//	ql:lev             choose the event to narrow to    ql:lx  all events
//	ql:fp:<n> ql:fo:<i>  that choice's page / event

import (
	"context"
	"log/slog"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

const salesLinkListDialogKind = "sales_link_list"

// The list's steps.
const (
	slStepList       = "list"        // the list of links is on screen
	slStepListEvents = "list_events" // the choice of the event to narrow to
)

// slFilters are the status filters, in the order they are shown.
var slFilters = []string{slStatusActive, slStatusArchived, "all"}

// slListWhereMax cuts an address shown in a row.
const slListWhereMax = 40

// salesLinkListState is the list's state, stored as the bot_dialogs row's JSON.
type salesLinkListState struct {
	OrgID   uuid.UUID   `json:"org_id"`
	MsgID   int         `json:"msg_id,omitempty"`
	Filter  string      `json:"lf,omitempty"` // active (default), archived or all
	Event   *slEvent    `json:"le,omitempty"` // only the links of this event
	Page    int         `json:"lp,omitempty"`
	Cursors []string    `json:"lc,omitempty"` // Cursors[k] fetches page k+1
	Next    string      `json:"ln,omitempty"` // the cursor of the page after this one
	IDs     []uuid.UUID `json:"li,omitempty"` // the links on screen, by row
	// PickPage/PickIDs are the event choice on screen.
	PickPage int         `json:"pp,omitempty"`
	PickIDs  []uuid.UUID `json:"pi,omitempty"`
}

// loadSalesLinkList reads the list's state. found is false when there is none,
// it ran out or it belongs to another organization; an empty state of this
// organization comes back for the caller to start from.
func (b *Bot) loadSalesLinkList(ctx context.Context, tg int64, orgID uuid.UUID) (ls salesLinkListState, step string, found bool) {
	step, found, _, err := b.dialogs.Load(ctx, tg, salesLinkListDialogKind, &ls)
	if err != nil {
		b.logger.Error("eventbot: sales link list load failed", slog.Int64("telegram_user_id", tg), slog.String("error", err.Error()))
		return salesLinkListState{OrgID: orgID}, "", false
	}
	if !found || ls.OrgID != orgID {
		return salesLinkListState{OrgID: orgID}, "", false
	}
	return ls, step, true
}

func (b *Bot) saveSalesLinkList(ctx context.Context, tg int64, ls salesLinkListState, step string) {
	orgID := ls.OrgID
	if err := b.dialogs.Save(ctx, tg, &orgID, salesLinkListDialogKind, step, ls, dialogTTL); err != nil {
		b.logger.Error("eventbot: sales link list save failed", slog.Int64("telegram_user_id", tg), slog.String("error", err.Error()))
	}
}

// leaveSalesLinkList ends the list: another screen was opened.
func (b *Bot) leaveSalesLinkList(ctx context.Context, tg int64) {
	if err := b.dialogs.Delete(ctx, tg, salesLinkListDialogKind); err != nil {
		b.logger.Warn("eventbot: sales link list clear failed", slog.Int64("telegram_user_id", tg), slog.String("error", err.Error()))
	}
}

// salesLinkListText never takes a text: the list has no typed question. It is
// registered so the router knows the kind, and the text goes on to whoever
// else waits for it.
func (b *Bot) salesLinkListText(context.Context, int64, *models.User, string) bool { return false }

// slListPosition is the page the list stands on and the cursor that fetches
// it; a state that lost its cursors starts again from the first page.
func slListPosition(ls salesLinkListState) (int, string) {
	page := max(ls.Page, 1)
	if page == 1 || page-1 >= len(ls.Cursors) {
		return 1, ""
	}
	return page, ls.Cursors[page-1]
}

// slListCallback handles the list's presses (and the entries into it).
func (b *Bot) slListCallback(ctx context.Context, chatID int64, msgID *int, from *models.User, id *Identity, jwt, kind, arg string) {
	loc := id.Locale()
	orgID := id.Current.OrgID
	ls, step, found := b.loadSalesLinkList(ctx, from.ID, orgID)

	switch kind {
	case "menu": // the main menu's entry: the list, filters reset
		b.slRenderList(ctx, chatID, msgID, id, jwt, salesLinkListState{OrgID: orgID}, 1, "")
		return
	case "list": // back to the list as the person left it
		b.slRenderCurrentList(ctx, chatID, msgID, id, jwt, ls)
		return
	case "new":
		b.slListNew(ctx, chatID, msgID, id, ls)
		return
	case "le":
		if eventID, err := uuid.Parse(arg); err == nil {
			b.slListForEvent(ctx, chatID, msgID, id, jwt, ls, eventID)
		}
		return
	}
	if !found {
		// The list ran out while the person was away: say so and offer the
		// menu, rather than act on a press made on rows that may be stale.
		b.reply(ctx, chatID, msgID, b.texts.T(loc, "bot.dialog_expired", nil), &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
			{{Text: b.texts.T(loc, "bot.ql.menu_btn", nil), CallbackData: "ql:menu"}},
			{{Text: b.texts.T(loc, "bot.btn_home", nil), CallbackData: "home"}},
		}})
		return
	}
	ls.MsgID = *msgID

	switch kind {
	case "lf":
		for _, f := range slFilters {
			if f == arg {
				ls.Filter, ls.Cursors = f, nil
				b.slRenderList(ctx, chatID, msgID, id, jwt, ls, 1, "")
				return
			}
		}
		b.slRenderCurrentList(ctx, chatID, msgID, id, jwt, ls)
	case "lx":
		ls.Event, ls.Cursors = nil, nil
		b.slRenderList(ctx, chatID, msgID, id, jwt, ls, 1, "")
	case "lev":
		ls.PickPage = 1
		b.slRenderFilterEvents(ctx, chatID, msgID, id, jwt, ls)
	case "fp":
		if step != slStepListEvents {
			b.slRenderCurrentList(ctx, chatID, msgID, id, jwt, ls)
			return
		}
		ls.PickPage = ParsePage(arg)
		b.slRenderFilterEvents(ctx, chatID, msgID, id, jwt, ls)
	case "fo":
		b.slFilterEventChosen(ctx, chatID, msgID, id, jwt, ls, step, arg)
	case "lp":
		b.slListPage(ctx, chatID, msgID, id, jwt, ls, ParsePage(arg))
	case "o":
		i, ok := ParseIndex(arg, len(ls.IDs))
		if step != slStepList || !ok {
			// The row belongs to a screen that is gone: draw the list again.
			b.slRenderCurrentList(ctx, chatID, msgID, id, jwt, ls)
			return
		}
		// The card names its link by UUID and needs no list state; the list
		// keeps its row for "back to the list".
		b.slShowCard(ctx, chatID, msgID, id, jwt, ls.IDs[i], "")
	}
}

func (b *Bot) slRenderCurrentList(ctx context.Context, chatID int64, msgID *int, id *Identity, jwt string, ls salesLinkListState) {
	page, cursor := slListPosition(ls)
	b.slRenderList(ctx, chatID, msgID, id, jwt, ls, page, cursor)
}

// slListNew starts a new link from the list. When the list is narrowed to an
// event the target choice starts from that event.
func (b *Bot) slListNew(ctx context.Context, chatID int64, msgID *int, id *Identity, ls salesLinkListState) {
	st := newSalesLinkDialog(id.Current.OrgID)
	st.Event = ls.Event
	b.slRenderTarget(ctx, chatID, msgID, id, st, "")
}

// slListForEvent opens the list narrowed to one event (the link card's "to
// this event's list"). The status filter the person had is kept.
func (b *Bot) slListForEvent(ctx context.Context, chatID int64, msgID *int, id *Identity, jwt string, ls salesLinkListState, eventID uuid.UUID) {
	ev, ok, err := b.slFindEvent(ctx, jwt, id.Current.OrgID, eventID)
	if err != nil {
		b.ecError(ctx, chatID, msgID, id, err, "home")
		return
	}
	if !ok {
		b.reply(ctx, chatID, msgID, b.texts.T(id.Locale(), "bot.ec.not_found", nil), b.backKeyboard(id.Locale(), "home"))
		return
	}
	ls.Event, ls.Cursors = &ev, nil
	b.slRenderList(ctx, chatID, msgID, id, jwt, ls, 1, "")
}

// slListPage moves one page. Forward uses the cursor the API handed out for
// the page on screen; back uses the one stored when that page was fetched. Any
// other number is a press of an older screen, and the current page is drawn.
func (b *Bot) slListPage(ctx context.Context, chatID int64, msgID *int, id *Identity, jwt string, ls salesLinkListState, want int) {
	cur, _ := slListPosition(ls)
	switch {
	case want == cur+1 && ls.Next != "":
		b.slRenderList(ctx, chatID, msgID, id, jwt, ls, want, ls.Next)
	case want == cur-1 && want >= 1 && want-1 < len(ls.Cursors):
		b.slRenderList(ctx, chatID, msgID, id, jwt, ls, want, ls.Cursors[want-1])
	default:
		b.slRenderCurrentList(ctx, chatID, msgID, id, jwt, ls)
	}
}

// slWhere says in a few words where a link leads.
func (b *Bot) slWhere(loc string, l openapi.SalesLink, events map[uuid.UUID]string) string {
	name := func() string {
		if l.Target.EventId != nil {
			if n := events[*l.Target.EventId]; n != "" {
				return Esc(truncate(n, slListWhereMax))
			}
		}
		return "—"
	}
	switch l.Target.Kind {
	case slKindEvent:
		return b.texts.T(loc, "bot.ql.w_event", map[string]any{"Name": name()})
	case slKindSession:
		return b.texts.T(loc, "bot.ql.w_session", map[string]any{"Name": name()})
	case slKindPromoterPage:
		return b.texts.T(loc, "bot.ql.w_promoter", nil)
	case slKindURL:
		addr := ""
		if l.Target.Url != nil {
			addr = *l.Target.Url
			if u, err := url.Parse(addr); err == nil && u.Host != "" {
				addr = u.Host
			}
		}
		return b.texts.T(loc, "bot.ql.w_url", map[string]any{"URL": Esc(truncate(addr, slListWhereMax))})
	}
	return b.texts.T(loc, "bot.ql.w_org", nil)
}

// slEventNames gives the names of the events the page's links point at: the
// narrowed event is known, any other costs one read of the event list.
func (b *Bot) slEventNames(ctx context.Context, jwt string, orgID uuid.UUID, ls salesLinkListState, items []openapi.SalesLink) map[uuid.UUID]string {
	names := map[uuid.UUID]string{}
	if ls.Event != nil {
		names[ls.Event.ID] = ls.Event.Name
	}
	missing := false
	for _, l := range items {
		if l.Target.EventId != nil {
			if _, ok := names[*l.Target.EventId]; !ok {
				missing = true
			}
		}
	}
	if !missing {
		return names
	}
	if events, err := b.arena.ListEvents(ctx, jwt, orgID); err == nil {
		for _, e := range events {
			names[e.Id] = e.Name
		}
	}
	return names
}

// slRenderList reads one page of links (page is 1-based, cursor fetches it)
// and draws it with its filters. The state is stored before the reply.
func (b *Bot) slRenderList(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt string, ls salesLinkListState, page int, cursor string) {
	loc := id.Locale()
	orgID := id.Current.OrgID
	if ls.Filter == "" {
		ls.Filter = slStatusActive
	}
	q := SalesLinkQuery{Status: ls.Filter, Limit: slPageSize, Cursor: cursor}
	if ls.Event != nil {
		q.EventID = &ls.Event.ID
	}
	res, err := b.arena.ListSalesLinks(ctx, jwt, orgID, q)
	if err != nil {
		b.ecError(ctx, chatID, editMsgID, id, err, "home")
		return
	}
	if len(res.Items) == 0 && page > 1 {
		// The page emptied while the person was away (links were archived):
		// start again from the first.
		b.slRenderList(ctx, chatID, editMsgID, id, jwt, ls, 1, "")
		return
	}

	// A text typed next is not the name of a place or an address.
	b.leaveSalesLink(ctx, id.Link.TelegramUserID)
	// The cursors that lead to this page: the stack below it, then its own.
	keep := min(page-1, len(ls.Cursors))
	ls.Cursors = append(append([]string(nil), ls.Cursors[:keep]...), cursor)
	ls.Page, ls.Next = page, res.NextCursor
	ls.IDs = ls.IDs[:0]
	for _, l := range res.Items {
		ls.IDs = append(ls.IDs, l.Id)
	}
	ls.PickIDs, ls.PickPage = nil, 0
	if editMsgID != nil {
		ls.MsgID = *editMsgID
	}
	b.saveSalesLinkList(ctx, id.Link.TelegramUserID, ls, slStepList)

	var sb strings.Builder
	sb.WriteString(b.texts.T(loc, "bot.ql.list_head", map[string]any{
		"Filter": Esc(b.texts.T(loc, "bot.ql.f_"+ls.Filter, nil)), "Page": page,
	}))
	if ls.Event != nil {
		sb.WriteString("\n" + b.texts.T(loc, "bot.ql.list_event", map[string]any{"Name": Esc(truncate(ls.Event.Name, 60))}))
	}
	if len(res.Items) == 0 {
		key := "bot.ql.list_empty"
		switch {
		case ls.Event != nil:
			key = "bot.ql.list_empty_event"
		case ls.Filter == slStatusArchived:
			key = "bot.ql.list_empty_archived"
		}
		sb.WriteString("\n\n" + b.texts.T(loc, key, nil))
	} else {
		names := b.slEventNames(ctx, jwt, orgID, ls, res.Items)
		sb.WriteString("\n")
		for i, l := range res.Items {
			line := b.texts.T(loc, "bot.ql.list_row", map[string]any{
				"N": (page-1)*slPageSize + i + 1, "Label": Esc(truncate(l.Label, 60)), "Where": b.slWhere(loc, l, names),
				"Scans": l.ScanCount, "Orders": l.OrdersCount,
			})
			if l.Status == slStatusArchived {
				line += " " + b.texts.T(loc, "bot.ql.list_mark_archived", nil)
			}
			sb.WriteString("\n" + line)
		}
		sb.WriteString("\n\n" + b.texts.T(loc, "bot.ql.list_hint", nil))
	}
	b.reply(ctx, chatID, editMsgID, sb.String(), &models.InlineKeyboardMarkup{InlineKeyboard: b.slListKeyboard(loc, ls, page, len(res.Items))})
}

// slListKeyboard is the numbers of the rows, the pager, the filters and the
// way to a new link.
func (b *Bot) slListKeyboard(loc string, ls salesLinkListState, page, rows int) [][]models.InlineKeyboardButton {
	var kb [][]models.InlineKeyboardButton
	var nums []models.InlineKeyboardButton
	for i := 0; i < rows; i++ {
		nums = append(nums, models.InlineKeyboardButton{Text: strconv.Itoa((page-1)*slPageSize + i + 1), CallbackData: ItemCallback("ql:o", i)})
	}
	if len(nums) > 0 {
		kb = append(kb, nums)
	}
	if nav := b.slListPager(loc, page, ls.Next != ""); nav != nil {
		kb = append(kb, nav)
	}
	var filters []models.InlineKeyboardButton
	for _, f := range slFilters {
		label := b.texts.T(loc, "bot.ql.f_"+f, nil)
		if f == ls.Filter {
			label = "✔ " + label
		}
		filters = append(filters, models.InlineKeyboardButton{Text: label, CallbackData: "ql:lf:" + f})
	}
	kb = append(kb, filters)
	if ls.Event != nil {
		kb = append(kb, []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.ql.list_all_events_btn", nil), CallbackData: "ql:lx"}})
	} else {
		kb = append(kb, []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.ql.list_pick_btn", nil), CallbackData: "ql:lev"}})
	}
	return append(kb,
		[]models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.ql.list_new_btn", nil), CallbackData: "ql:new"}},
		[]models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.btn_home", nil), CallbackData: "home"}},
	)
}

// slListPager is the "‹ 2 ›" row of a list that only knows its next page: the
// number alone, because the API does not say how many pages there are.
func (b *Bot) slListPager(loc string, page int, hasNext bool) []models.InlineKeyboardButton {
	if page <= 1 && !hasNext {
		return nil
	}
	var row []models.InlineKeyboardButton
	if page > 1 {
		row = append(row, models.InlineKeyboardButton{Text: "‹ " + b.texts.T(loc, "bot.btn_prev", nil), CallbackData: pageCallback("ql:lp", page-1)})
	}
	row = append(row, models.InlineKeyboardButton{Text: strconv.Itoa(page), CallbackData: "ql:noop"})
	if hasNext {
		row = append(row, models.InlineKeyboardButton{Text: b.texts.T(loc, "bot.btn_next", nil) + " ›", CallbackData: pageCallback("ql:lp", page+1)})
	}
	return row
}

// ─── narrowing to one event ───────────────────────────────────────────────────

// slRenderFilterEvents lists the organization's events, five to a page, to
// narrow the list to one of them.
func (b *Bot) slRenderFilterEvents(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt string, ls salesLinkListState) {
	loc := id.Locale()
	events, err := b.arena.ListEvents(ctx, jwt, id.Current.OrgID)
	if err != nil {
		b.ecError(ctx, chatID, editMsgID, id, err, "home")
		return
	}
	pages := PagesFor(int64(len(events)), slPageSize)
	ls.PickPage = min(max(ls.PickPage, 1), pages)
	start := (ls.PickPage - 1) * slPageSize
	page := events[start:min(start+slPageSize, len(events))]
	ls.PickIDs = ls.PickIDs[:0]
	for _, e := range page {
		ls.PickIDs = append(ls.PickIDs, e.Id)
	}
	if editMsgID != nil {
		ls.MsgID = *editMsgID
	}
	b.saveSalesLinkList(ctx, id.Link.TelegramUserID, ls, slStepListEvents)

	text := b.texts.T(loc, "bot.ql.list_pick_event", map[string]any{"Page": ls.PickPage, "Pages": pages})
	if len(page) == 0 {
		text = b.texts.T(loc, "bot.ql.events_none", nil)
	}
	var rows [][]models.InlineKeyboardButton
	for i, e := range page {
		rows = append(rows, []models.InlineKeyboardButton{{Text: truncate(e.Name, 48), CallbackData: ItemCallback("ql:fo", i)}})
	}
	if nav := b.slPager(loc, Pager{Prefix: "ql:fp", Page: ls.PickPage, Pages: pages}); nav != nil {
		rows = append(rows, nav)
	}
	rows = append(rows, []models.InlineKeyboardButton{{Text: "« " + b.texts.T(loc, "bot.btn_back", nil), CallbackData: "ql:list"}})
	b.reply(ctx, chatID, editMsgID, text, &models.InlineKeyboardMarkup{InlineKeyboard: rows})
}

// slFilterEventChosen narrows the list to the event the person pressed.
func (b *Bot) slFilterEventChosen(ctx context.Context, chatID int64, msgID *int, id *Identity, jwt string, ls salesLinkListState, step, arg string) {
	i, ok := ParseIndex(arg, len(ls.PickIDs))
	if step != slStepListEvents || !ok {
		b.slRenderCurrentList(ctx, chatID, msgID, id, jwt, ls)
		return
	}
	ev, found, err := b.slFindEvent(ctx, jwt, id.Current.OrgID, ls.PickIDs[i])
	if err != nil {
		b.ecError(ctx, chatID, msgID, id, err, "home")
		return
	}
	if !found {
		b.slRenderFilterEvents(ctx, chatID, msgID, id, jwt, ls)
		return
	}
	ls.Event, ls.Cursors = &ev, nil
	b.slRenderList(ctx, chatID, msgID, id, jwt, ls, 1, "")
}
