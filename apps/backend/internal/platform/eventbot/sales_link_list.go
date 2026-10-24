package eventbot

// sales_link_list.go — the list of the organization's links behind the main
// menu's "Links and QR" (spec 43 §5.2, QL-9). Five links a page, each with its
// name, where it leads and its scans and orders; a status filter (active, the
// archive, all) and a narrowing to one event. The state lives in the same
// bot_dialogs row as the creation dialog (sales_link_dialog.go), under step
// "list", so a restart keeps the filter and the page.
//
// The API pages with a keyset cursor and only goes forward, so the dialog keeps
// the stack of cursors that led to the page on screen: Cursors[k] is the cursor
// that fetches page k+1. A press of "back" fetches again with the stored
// cursor. A row is named by its INDEX on the page (paging.go), resolved against
// the ids the screen stored; a press on a link's card names the link itself.
//
//	ql:menu            the list, filters reset      ql:list  the list as it was
//	ql:o:<i>           open row i                   ql:new   a new link
//	ql:lp:<n>          page n (next or previous)    ql:le:<event>  one event
//	ql:lf:<f>          status filter active|archived|all
//	ql:lev             choose the event to narrow to    ql:lx  all events
//	ql:fp:<n> ql:fo:<i>  that choice's page / event

import (
	"context"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

// The list's steps.
const (
	slStepList       = "list"        // the list of links is on screen
	slStepListEvents = "list_events" // the choice of the event to narrow to
)

// slFilters are the status filters, in the order they are shown.
var slFilters = []string{slStatusActive, slStatusArchived, "all"}

// slListWhereMax cuts an address shown in a row.
const slListWhereMax = 40

// carryList copies the list's own state to a dialog that starts something
// else (a retarget), so "back to the list" lands where the person left.
func (dst *salesLinkDialog) carryList(src salesLinkDialog) {
	dst.LFilter, dst.LEvent, dst.LPage, dst.LCursors, dst.LNext, dst.LIDs =
		src.LFilter, src.LEvent, src.LPage, src.LCursors, src.LNext, src.LIDs
}

// slListPosition is the page the list stands on and the cursor that fetches
// it; a state that lost its cursors starts again from the first page.
func slListPosition(st salesLinkDialog) (int, string) {
	page := max(st.LPage, 1)
	if page == 1 || page-1 >= len(st.LCursors) {
		return 1, ""
	}
	return page, st.LCursors[page-1]
}

// slListBack is "back to the list": the list as it was, or a fresh one when
// the dialog ran out.
func (b *Bot) slListBack(ctx context.Context, chatID int64, msgID *int, from *models.User, id *Identity, jwt string) {
	st, _, _, _ := b.loadSalesLinkState(ctx, from.ID, id.Current.OrgID)
	page, cursor := slListPosition(st)
	b.slRenderList(ctx, chatID, msgID, id, jwt, st, page, cursor, "")
}

// slListNew starts a new link from the list. When the list is narrowed to an
// event the target choice starts from that event.
func (b *Bot) slListNew(ctx context.Context, chatID int64, msgID *int, from *models.User, id *Identity) {
	st, _, _, _ := b.loadSalesLinkState(ctx, from.ID, id.Current.OrgID)
	st.Mode, st.LinkID, st.LinkLabel, st.Placement = slModeNew, nil, "", ""
	st.Event = st.LEvent
	b.slRenderTarget(ctx, chatID, msgID, id, st, "")
}

// slListForEvent opens the list narrowed to one event (the link card's "to
// this event's list"). The status filter the person had is kept.
func (b *Bot) slListForEvent(ctx context.Context, chatID int64, msgID *int, from *models.User, id *Identity, jwt string, eventID uuid.UUID) {
	ev, ok, err := b.slFindEvent(ctx, jwt, id.Current.OrgID, eventID)
	if err != nil {
		b.ecError(ctx, chatID, msgID, id, err, "home")
		return
	}
	if !ok {
		b.reply(ctx, chatID, msgID, b.texts.T(id.Locale(), "bot.ec.not_found", nil), b.backKeyboard(id.Locale(), "home"))
		return
	}
	st, _, _, _ := b.loadSalesLinkState(ctx, from.ID, id.Current.OrgID)
	st.LEvent, st.LCursors = &ev, nil
	b.slRenderList(ctx, chatID, msgID, id, jwt, st, 1, "", "")
}

// slListPress handles the presses that act on the stored list state.
func (b *Bot) slListPress(ctx context.Context, chatID int64, msgID *int, id *Identity, jwt string, st salesLinkDialog, step, kind, arg string) {
	switch kind {
	case "lf":
		for _, f := range slFilters {
			if f == arg {
				st.LFilter, st.LCursors = f, nil
				b.slRenderList(ctx, chatID, msgID, id, jwt, st, 1, "", "")
				return
			}
		}
		b.slRenderCurrentList(ctx, chatID, msgID, id, jwt, st)
	case "lx":
		st.LEvent, st.LCursors = nil, nil
		b.slRenderList(ctx, chatID, msgID, id, jwt, st, 1, "", "")
	case "lev":
		st.Page = 1
		b.slRenderFilterEvents(ctx, chatID, msgID, id, jwt, st)
	case "fp":
		if step != slStepListEvents {
			b.slRenderCurrentList(ctx, chatID, msgID, id, jwt, st)
			return
		}
		st.Page = ParsePage(arg)
		b.slRenderFilterEvents(ctx, chatID, msgID, id, jwt, st)
	case "fo":
		b.slFilterEventChosen(ctx, chatID, msgID, id, jwt, st, step, arg)
	case "lp":
		b.slListPage(ctx, chatID, msgID, id, jwt, st, ParsePage(arg))
	case "o":
		i, ok := ParseIndex(arg, len(st.LIDs))
		if step != slStepList || !ok {
			// The row belongs to a screen that is gone: draw the list again.
			b.slRenderCurrentList(ctx, chatID, msgID, id, jwt, st)
			return
		}
		b.slShowCard(ctx, chatID, msgID, id, jwt, st.LIDs[i], "")
	}
}

func (b *Bot) slRenderCurrentList(ctx context.Context, chatID int64, msgID *int, id *Identity, jwt string, st salesLinkDialog) {
	page, cursor := slListPosition(st)
	b.slRenderList(ctx, chatID, msgID, id, jwt, st, page, cursor, "")
}

// slListPage moves one page. Forward uses the cursor the API handed out for
// the page on screen; back uses the one stored when that page was fetched. Any
// other number is a press of an older screen, and the current page is drawn.
func (b *Bot) slListPage(ctx context.Context, chatID int64, msgID *int, id *Identity, jwt string, st salesLinkDialog, want int) {
	cur, _ := slListPosition(st)
	switch {
	case want == cur+1 && st.LNext != "":
		b.slRenderList(ctx, chatID, msgID, id, jwt, st, want, st.LNext, "")
	case want == cur-1 && want >= 1 && want-1 < len(st.LCursors):
		b.slRenderList(ctx, chatID, msgID, id, jwt, st, want, st.LCursors[want-1], "")
	default:
		b.slRenderCurrentList(ctx, chatID, msgID, id, jwt, st)
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
func (b *Bot) slEventNames(ctx context.Context, jwt string, orgID uuid.UUID, st salesLinkDialog, items []openapi.SalesLink) map[uuid.UUID]string {
	names := map[uuid.UUID]string{}
	if st.LEvent != nil {
		names[st.LEvent.ID] = st.LEvent.Name
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
func (b *Bot) slRenderList(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt string, st salesLinkDialog, page int, cursor, note string) {
	loc := id.Locale()
	orgID := id.Current.OrgID
	if st.LFilter == "" {
		st.LFilter = slStatusActive
	}
	q := SalesLinkQuery{Status: st.LFilter, Limit: slPageSize, Cursor: cursor}
	if st.LEvent != nil {
		q.EventID = &st.LEvent.ID
	}
	res, err := b.arena.ListSalesLinks(ctx, jwt, orgID, q)
	if err != nil {
		b.ecError(ctx, chatID, editMsgID, id, err, "home")
		return
	}
	if len(res.Items) == 0 && page > 1 {
		// The page emptied while the person was away (links were archived):
		// start again from the first.
		b.slRenderList(ctx, chatID, editMsgID, id, jwt, st, 1, "", note)
		return
	}

	// The cursors that lead to this page: the stack below it, then its own.
	keep := min(page-1, len(st.LCursors))
	st.LCursors = append(append([]string(nil), st.LCursors[:keep]...), cursor)
	st.LPage, st.LNext = page, res.NextCursor
	st.LIDs = st.LIDs[:0]
	for _, l := range res.Items {
		st.LIDs = append(st.LIDs, l.Id)
	}
	st.Mode, st.Target, st.Placement, st.Event, st.Sessions, st.IDs, st.Page = slModeNew, nil, "", nil, nil, nil, 1
	if editMsgID != nil {
		st.MsgID = *editMsgID
	}
	b.saveSalesLinkState(ctx, id.Link.TelegramUserID, st, slStepList)

	var sb strings.Builder
	sb.WriteString(note)
	sb.WriteString(b.texts.T(loc, "bot.ql.list_head", map[string]any{
		"Filter": Esc(b.texts.T(loc, "bot.ql.f_"+st.LFilter, nil)), "Page": page,
	}))
	if st.LEvent != nil {
		sb.WriteString("\n" + b.texts.T(loc, "bot.ql.list_event", map[string]any{"Name": Esc(truncate(st.LEvent.Name, 60))}))
	}
	if len(res.Items) == 0 {
		key := "bot.ql.list_empty"
		switch {
		case st.LEvent != nil:
			key = "bot.ql.list_empty_event"
		case st.LFilter == slStatusArchived:
			key = "bot.ql.list_empty_archived"
		}
		sb.WriteString("\n\n" + b.texts.T(loc, key, nil))
	} else {
		names := b.slEventNames(ctx, jwt, orgID, st, res.Items)
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
	b.reply(ctx, chatID, editMsgID, sb.String(), &models.InlineKeyboardMarkup{InlineKeyboard: b.slListKeyboard(loc, st, page, len(res.Items))})
}

// slListKeyboard is the numbers of the rows, the pager, the filters and the
// way to a new link.
func (b *Bot) slListKeyboard(loc string, st salesLinkDialog, page, rows int) [][]models.InlineKeyboardButton {
	var kb [][]models.InlineKeyboardButton
	var nums []models.InlineKeyboardButton
	for i := 0; i < rows; i++ {
		nums = append(nums, models.InlineKeyboardButton{Text: strconv.Itoa((page-1)*slPageSize + i + 1), CallbackData: ItemCallback("ql:o", i)})
	}
	if len(nums) > 0 {
		kb = append(kb, nums)
	}
	if nav := b.slListPager(loc, page, st.LNext != ""); nav != nil {
		kb = append(kb, nav)
	}
	var filters []models.InlineKeyboardButton
	for _, f := range slFilters {
		label := b.texts.T(loc, "bot.ql.f_"+f, nil)
		if f == st.LFilter {
			label = "✔ " + label
		}
		filters = append(filters, models.InlineKeyboardButton{Text: label, CallbackData: "ql:lf:" + f})
	}
	kb = append(kb, filters)
	if st.LEvent != nil {
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
func (b *Bot) slRenderFilterEvents(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt string, st salesLinkDialog) {
	loc := id.Locale()
	events, err := b.arena.ListEvents(ctx, jwt, id.Current.OrgID)
	if err != nil {
		b.ecError(ctx, chatID, editMsgID, id, err, "home")
		return
	}
	pages := PagesFor(int64(len(events)), slPageSize)
	st.Page = min(max(st.Page, 1), pages)
	start := (st.Page - 1) * slPageSize
	page := events[start:min(start+slPageSize, len(events))]
	st.IDs = st.IDs[:0]
	for _, e := range page {
		st.IDs = append(st.IDs, e.Id)
	}
	if editMsgID != nil {
		st.MsgID = *editMsgID
	}
	b.saveSalesLinkState(ctx, id.Link.TelegramUserID, st, slStepListEvents)

	text := b.texts.T(loc, "bot.ql.list_pick_event", map[string]any{"Page": st.Page, "Pages": pages})
	if len(page) == 0 {
		text = b.texts.T(loc, "bot.ql.events_none", nil)
	}
	var rows [][]models.InlineKeyboardButton
	for i, e := range page {
		rows = append(rows, []models.InlineKeyboardButton{{Text: truncate(e.Name, 48), CallbackData: ItemCallback("ql:fo", i)}})
	}
	if nav := b.slPager(loc, Pager{Prefix: "ql:fp", Page: st.Page, Pages: pages}); nav != nil {
		rows = append(rows, nav)
	}
	rows = append(rows, []models.InlineKeyboardButton{{Text: "« " + b.texts.T(loc, "bot.btn_back", nil), CallbackData: "ql:list"}})
	b.reply(ctx, chatID, editMsgID, text, &models.InlineKeyboardMarkup{InlineKeyboard: rows})
}

// slFilterEventChosen narrows the list to the event the person pressed.
func (b *Bot) slFilterEventChosen(ctx context.Context, chatID int64, msgID *int, id *Identity, jwt string, st salesLinkDialog, step, arg string) {
	i, ok := ParseIndex(arg, len(st.IDs))
	if step != slStepListEvents || !ok {
		b.slRenderCurrentList(ctx, chatID, msgID, id, jwt, st)
		return
	}
	ev, found, err := b.slFindEvent(ctx, jwt, id.Current.OrgID, st.IDs[i])
	if err != nil {
		b.ecError(ctx, chatID, msgID, id, err, "home")
		return
	}
	if !found {
		b.slRenderFilterEvents(ctx, chatID, msgID, id, jwt, st)
		return
	}
	st.LEvent, st.LCursors = &ev, nil
	b.slRenderList(ctx, chatID, msgID, id, jwt, st, 1, "", "")
}
