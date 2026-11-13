package eventbot

// event_card_groups.go — the grouped event card (2026-11-13). The card keeps
// FIVE buttons: the link for buyers, and four group screens (sales and
// orders, edit, promotion, more). Every action the card had before is now one
// press deeper at most, and every old callback still works: only the screen
// that carries a button moved.
//
//	ec:g:<event>:<s|e|p|m>  a group screen (stateless: a title, buttons, the way back)
//	ec:lk:<event>           the buyers' link of the event and its QR code in one press
//	ec:gl:<event>           the guest list: the single live date's list at once, else the dates
//
// A group screen ends the delete question like any "ec:" press that is not a
// status button (ecCallback). The dangerous actions (take off sale, archive,
// delete) sit only inside "More", and keep their own confirmations.

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"unicode/utf8"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
	catalogdomain "github.com/abhteam/arena_new/apps/backend/internal/domain/catalog"
)

// The group screens, as the last part of "ec:g:<event>:<group>".
const (
	cardGroupSales = "s"
	cardGroupEdit  = "e"
	cardGroupPromo = "p"
	cardGroupMore  = "m"

	// cardLinkKind marks the link button in the order of the card's buttons.
	cardLinkKind = "lk"

	// cardLinkPlacement is the placement of the sales link the QR code of an
	// event page rests on.
	cardLinkPlacement = "other"
)

// cardGroupKeys lists the bot.ec.g.* keys of the grouped card.
var cardGroupKeys = []string{
	"bot.ec.g.link_btn", "bot.ec.g.sales_btn", "bot.ec.g.edit_btn", "bot.ec.g.promo_btn", "bot.ec.g.more_btn",
	"bot.ec.g.cat_btn", "bot.ec.g.guests_btn", "bot.ec.g.link_more_btn", "bot.ec.g.screen",
	"bot.ec.g.s_text", "bot.ec.g.e_text", "bot.ec.g.p_text", "bot.ec.g.m_text",
	"bot.ec.g.link_text", "bot.ec.g.link_text_noqr", "bot.ec.g.link_text_nocode", "bot.ec.g.link_none", "bot.ec.g.ql_btn",
}

// maxSalesLinkPages bounds the walk through the links of one event.
const maxSalesLinkPages = 100

// keyedMutex is one mutex per key; the zero value is ready. An entry lives
// only while somebody holds or waits for it.
type keyedMutex struct {
	mu sync.Mutex
	m  map[uuid.UUID]*keyedMutexEntry
}

type keyedMutexEntry struct {
	mu   sync.Mutex
	refs int
}

// lock takes the mutex of key and returns the function that releases it.
func (k *keyedMutex) lock(key uuid.UUID) func() {
	k.mu.Lock()
	if k.m == nil {
		k.m = map[uuid.UUID]*keyedMutexEntry{}
	}
	e := k.m[key]
	if e == nil {
		e = &keyedMutexEntry{}
		k.m[key] = e
	}
	e.refs++
	k.mu.Unlock()
	e.mu.Lock()
	return func() {
		e.mu.Unlock()
		k.mu.Lock()
		e.refs--
		if e.refs == 0 {
			delete(k.m, key)
		}
		k.mu.Unlock()
	}
}

func init() { MessageKeys = append(MessageKeys, cardGroupKeys...) }

// cardFacts is what the buttons depend on besides the person's role.
type cardFacts struct {
	Status     string // events.status
	SalesState string // the event list's sales state
	HasLink    bool   // the event has a public link for buyers
	Dates      int    // dates in the summary; 0 when the summary is not known
	SingleLive bool   // the summary shows exactly one date and it is not cancelled
}

// cardFactsOf reads the facts of one event; sum is nil when the person may not
// read figures or the call failed.
func cardFactsOf(ev openapi.EventItem, link string, sum *openapi.EventSummary) cardFacts {
	f := cardFacts{Status: string(ev.Status), SalesState: ev.SalesState, HasLink: link != ""}
	if sum != nil {
		f.Dates = len(sum.Sessions)
		f.SingleLive = len(sum.Sessions) == 1 && sum.Sessions[0].Status != "cancelled"
	}
	return f
}

// past reports an event that is over or archived: its figures matter more than
// its link.
func (f cardFacts) past() bool {
	return f.SalesState == salesStateArchived || f.Status == evsStatusArchived
}

// mayCreateBuyersLink reports whether "Link for buyers" may make a NEW sales
// link: only for a published event that is not over (on sale, sales not open
// yet, sold out). A draft, a cancelled, an archived or a past event never gets
// a new link from that press.
func mayCreateBuyersLink(status, salesState string) bool {
	return status == evsStatusPublish && salesState != salesStateArchived
}

func cardGroupData(eventID uuid.UUID, group string) string {
	return "ec:g:" + eventID.String() + ":" + group
}

// cardCanPublish reports a draft the person may publish.
func cardCanPublish(id *Identity, f cardFacts) bool {
	return canChangeEventStatus(id) && catalogdomain.IsValidEventTransition(f.Status, evsStatusPublish)
}

// cardRows is the card's buttons, one per row. A draft that may be published
// starts with "Publish". An event on sale starts with the link, a past or
// archived one with its sales. The rest follow in the same order, and a button
// with nothing behind it is not shown.
func (b *Bot) cardRows(loc string, id *Identity, eventID uuid.UUID, f cardFacts) [][]models.InlineKeyboardButton {
	return b.cardItems(loc, id, eventID, f).rows()
}

// cardItems is the list of the card's buttons: the keyboard and the help of the
// card are both made from it.
func (b *Bot) cardItems(loc string, id *Identity, eventID uuid.UUID, f cardFacts) screenItems {
	var items screenItems
	if cardCanPublish(id, f) {
		items.add("ec.publish", b.mnBtn(loc, "bot.evs.publish_btn", "ec:ev:"+eventID.String()+":"+evsActPublish))
	}
	order := []string{cardLinkKind, cardGroupSales, cardGroupEdit, cardGroupPromo, cardGroupMore}
	if f.past() {
		order = []string{cardGroupSales, cardLinkKind, cardGroupEdit, cardGroupPromo, cardGroupMore}
	}
	for _, kind := range order {
		itemID := "ec." + kind
		if kind == cardLinkKind {
			itemID = "ec.link"
		}
		items.add(itemID, b.cardTopButton(loc, id, eventID, kind, f))
	}
	return items
}

// cardTopButton is one of the five buttons, or nil when it has nothing to open.
func (b *Bot) cardTopButton(loc string, id *Identity, eventID uuid.UUID, kind string, f cardFacts) []models.InlineKeyboardButton {
	if kind == cardLinkKind {
		if !f.HasLink || !canSalesLinks(id) {
			return nil
		}
		return b.mnBtn(loc, "bot.ec.g.link_btn", "ec:"+cardLinkKind+":"+eventID.String())
	}
	if len(b.cardGroupItems(loc, id, eventID, kind, f)) == 0 {
		return nil
	}
	return b.mnBtn(loc, cardGroupTitleKey(kind), cardGroupData(eventID, kind))
}

// cardGroupTitleKey is the button text of a group, which is also its title.
func cardGroupTitleKey(group string) string {
	switch group {
	case cardGroupSales:
		return "bot.ec.g.sales_btn"
	case cardGroupEdit:
		return "bot.ec.g.edit_btn"
	case cardGroupPromo:
		return "bot.ec.g.promo_btn"
	}
	return "bot.ec.g.more_btn"
}

// cardGroupTextKey is the short sentence of a group screen.
func cardGroupTextKey(group string) string {
	switch group {
	case cardGroupSales:
		return "bot.ec.g.s_text"
	case cardGroupEdit:
		return "bot.ec.g.e_text"
	case cardGroupPromo:
		return "bot.ec.g.p_text"
	}
	return "bot.ec.g.m_text"
}

// cardGroupRows is the buttons inside one group, for this role and this event.
// Each carries the callback it always had, so an old message keeps working.
func (b *Bot) cardGroupRows(loc string, id *Identity, eventID uuid.UUID, group string, f cardFacts) [][]models.InlineKeyboardButton {
	return b.cardGroupItems(loc, id, eventID, group, f).rows()
}

// statusItemIDs maps the status buttons of the More group to their help ids.
var statusItemIDs = map[string]string{
	evsActPublish: "gm.publish", evsActOff: "gm.off", evsActArchive: "gm.archive", evsActDelete: "gm.delete",
}

// cardGroupItems is the list of one group's buttons: the keyboard and the help
// of the group are both made from it.
func (b *Bot) cardGroupItems(loc string, id *Identity, eventID uuid.UUID, group string, f cardFacts) screenItems {
	ev := eventID.String()
	var items screenItems
	switch group {
	case cardGroupSales:
		if !canViewSales(id) {
			return nil
		}
		items.add("gs.summary", b.mnBtn(loc, "bot.ec.summary_btn", "ec:es:"+ev))
		items.add("gs.orders", b.mnBtn(loc, "bot.ord.btn", "or:e:"+ev))
		items.add("gs.guests", b.mnBtn(loc, "bot.ec.g.guests_btn", "ec:gl:"+ev))
		items.add("gs.csv", b.mnBtn(loc, "bot.ec.csv_btn", "ec:ce:"+ev))
		if f.Dates > 1 {
			items.add("gs.dates", []models.InlineKeyboardButton{{
				Text: b.texts.T(loc, "bot.ec.dates_btn", map[string]any{"N": f.Dates}), CallbackData: "ec:o:" + ev + ":1",
			}})
		}
	case cardGroupEdit:
		items.add("ge.edit", b.mnBtn(loc, "bot.wz.edit_btn", "wz:edit:"+ev))
		items.add("ge.sessions", b.mnBtn(loc, "bot.ses.open_btn", "ses:list:"+ev))
		if f.SingleLive { // the categories of an event with one live date (EC-15)
			if row := b.categoryEventRow(loc, id, eventID); row != nil {
				row[0].Text = b.texts.T(loc, "bot.ec.g.cat_btn", nil)
				items.add("ge.cat", row)
			}
		}
		items.add("ge.materials", b.materialsEventRow(loc, eventID))
		items.add("ge.sample", b.mnBtn(loc, "bot.sample_btn", "sample:"+ev))
		if canViewSales(id) { // the owner, the manager and the operator write the text of the e-mail (RT-06)
			items.add("ge.text", b.mnBtn(loc, "bot.tf.event_btn", "tf:n:"+ev))
		}
		items.add("ge.promoter", b.promoterEventRow(loc, id, eventID))
	case cardGroupPromo:
		items.add("gp.promo", b.promoEventRow(loc, id, eventID))
		if canInvite(id) {
			items.add("gp.invite", b.mnBtn(loc, "bot.inv.btn", "iv:e:"+ev))
		}
		if canSalesLinks(id) { // the "QR / link" dialog also for an event that has no public link yet
			items.add("gp.links", b.mnBtn(loc, "bot.ec.g.ql_btn", "ql:e:"+ev))
		}
		items.add("gp.referral", b.referralEventRow(loc, id, eventID))
	case cardGroupMore:
		items.add("gm.copy", b.mnBtn(loc, "bot.wz.copy_btn", "wz:copy:"+ev))
		for _, row := range b.eventStatusRows(loc, id, f.Status, eventID) {
			for _, btn := range row {
				act := strings.TrimPrefix(btn.CallbackData, "ec:ev:"+ev+":")
				items.add(statusItemIDs[act], []models.InlineKeyboardButton{btn})
			}
		}
	}
	return items
}

// cardEvent finds one event in the organization's list.
func (b *Bot) cardEvent(ctx context.Context, jwt string, orgID, eventID uuid.UUID) (*openapi.EventItem, error) {
	events, err := b.arena.ListEvents(ctx, jwt, orgID)
	if err != nil {
		return nil, err
	}
	for i := range events {
		if events[i].Id == eventID {
			return &events[i], nil
		}
	}
	return nil, &APIError{Status: 404, Code: "event.not_found"}
}

// cardSummary reads the summary for a person who may see figures; nil on a
// refusal or a failed call (the card then drops what depends on it).
func (b *Bot) cardSummary(ctx context.Context, id *Identity, jwt string, eventID uuid.UUID) *openapi.EventSummary {
	if !canViewSales(id) {
		return nil
	}
	s, err := b.arena.EventSummary(ctx, jwt, id.Current.OrgID, eventID)
	if err != nil {
		b.logger.Warn("eventbot: event summary failed", slog.String("event_id", eventID.String()), slog.String("error", err.Error()))
		return nil
	}
	return &s
}

// cardBackRows is the last rows of a screen under the card: back to the card,
// and the menu.
func (b *Bot) cardBackRows(loc string, eventID uuid.UUID) [][]models.InlineKeyboardButton {
	return [][]models.InlineKeyboardButton{
		{{Text: "« " + b.texts.T(loc, "bot.btn_back", nil), CallbackData: "ec:o:" + eventID.String()}},
		b.mnHomeRow(loc),
	}
}

// showCardGroup draws "ec:g:<event>:<group>".
func (b *Bot) showCardGroup(ctx context.Context, chatID int64, editMsgID *int, from *models.User, eventID uuid.UUID, group string) {
	id, jwt, ok := b.ecIdentity(ctx, chatID, editMsgID, from)
	if !ok {
		return
	}
	loc := id.Locale()
	switch group {
	case cardGroupSales, cardGroupEdit, cardGroupPromo, cardGroupMore:
	default:
		return
	}
	event, err := b.cardEvent(ctx, jwt, id.Current.OrgID, eventID)
	if err != nil {
		b.ecError(ctx, chatID, editMsgID, id, err, "el:b")
		return
	}
	var sum *openapi.EventSummary
	if group == cardGroupSales || group == cardGroupEdit {
		sum = b.cardSummary(ctx, id, jwt, eventID)
	}
	rows := b.cardGroupRows(loc, id, eventID, group, cardFactsOf(*event, "", sum))
	if len(rows) == 0 {
		b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.ec.no_rights", nil), b.cardKeyboard(loc, "ec:o:"+eventID.String()))
		return
	}
	text := b.texts.T(loc, "bot.ec.g.screen", map[string]any{
		"Name": Esc(event.Name), "Title": b.texts.T(loc, cardGroupTitleKey(group), nil), "Text": b.texts.T(loc, cardGroupTextKey(group), nil),
	})
	rows = append(rows, b.helpRow(loc, helpGroupScreen(group), eventID.String()))
	rows = append(rows, b.cardBackRows(loc, eventID)...)
	b.reply(ctx, chatID, editMsgID, text, &models.InlineKeyboardMarkup{InlineKeyboard: rows})
}

// ─── the buyers' link and its QR code ─────────────────────────────────────────

// sendBuyersLink answers "ec:lk:<event>": the QR code as an image, and the
// link in the message, in one press.
func (b *Bot) sendBuyersLink(ctx context.Context, chatID int64, editMsgID *int, from *models.User, eventID uuid.UUID) {
	id, jwt, ok := b.ecIdentity(ctx, chatID, editMsgID, from)
	if !ok {
		return
	}
	loc := id.Locale()
	card := "ec:o:" + eventID.String()
	if !canSalesLinks(id) {
		b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.ec.no_rights", nil), b.cardKeyboard(loc, card))
		return
	}
	orgID := id.Current.OrgID
	event, err := b.cardEvent(ctx, jwt, orgID, eventID)
	if err != nil {
		b.ecError(ctx, chatID, editMsgID, id, err, "el:b")
		return
	}
	url := b.eventLink(ctx, jwt, orgID, *event)
	if url == "" {
		b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.ec.g.link_none", nil), b.cardKeyboard(loc, card))
		return
	}
	// A new sales link is made only for a published event that is not over
	// (on sale, sales not open yet, sold out): an organizer announces the link
	// before sales open. A past event reuses a link it already has, or shows
	// the page link only.
	create := mayCreateBuyersLink(string(event.Status), string(event.SalesState))
	textKey := "bot.ec.g.link_text"
	switch sent, noCode := b.sendEventQR(ctx, chatID, jwt, orgID, *event, create); {
	case noCode:
		textKey = "bot.ec.g.link_text_nocode"
	case !sent:
		textKey = "bot.ec.g.link_text_noqr"
	}
	rows := [][]models.InlineKeyboardButton{b.mnBtn(loc, "bot.ec.g.link_more_btn", "ql:e:"+eventID.String())}
	rows = append(rows, b.cardBackRows(loc, eventID)...)
	b.reply(ctx, chatID, editMsgID, b.texts.T(loc, textKey, map[string]any{"Name": Esc(event.Name), "URL": Esc(url)}), &models.InlineKeyboardMarkup{InlineKeyboard: rows})
}

// sendEventQR sends the QR code of the event page as an image. The code rests
// on one sales link of the event (placement "other"): the one that is already
// there, or a new one when create is true. It reports whether the image went
// out, and noCode when there was no link and none was to be made.
func (b *Bot) sendEventQR(ctx context.Context, chatID int64, jwt string, orgID uuid.UUID, ev openapi.EventItem, create bool) (sent, noCode bool) {
	link, found, err := b.eventSalesLink(ctx, jwt, orgID, ev, create)
	if err != nil {
		b.logger.Warn("eventbot: event sales link failed", slog.String("event_id", ev.Id.String()), slog.String("error", err.Error()))
		return false, false
	}
	if !found {
		return false, true
	}
	png, err := b.arena.SalesLinkQR(ctx, jwt, orgID, link.Id, "png", 1024)
	if err != nil {
		b.logger.Warn("eventbot: event qr failed", slog.String("link_id", link.Id.String()), slog.String("error", err.Error()))
		return false, false
	}
	if _, err := b.tg.SendPhoto(ctx, &tgbot.SendPhotoParams{
		ChatID:    chatID,
		Photo:     &models.InputFileUpload{Filename: link.Code + ".png", Data: bytes.NewReader(png)},
		Caption:   Esc(ev.Name),
		ParseMode: models.ParseModeHTML,
	}); err != nil {
		b.logger.Warn("eventbot: send event qr failed", slog.Int64("chat_id", chatID), slog.String("error", unwrapURLError(err).Error()))
		return false, false
	}
	return true, false
}

// eventSalesLink returns the active link of the event with the placement
// "other". Without one it makes a link named after the event when create is
// true, else it reports found = false and creates nothing. The find-and-create
// is serialized per event, and the search reads ALL pages of the event's active
// links, so a double press makes one link.
func (b *Bot) eventSalesLink(ctx context.Context, jwt string, orgID uuid.UUID, ev openapi.EventItem, create bool) (openapi.SalesLink, bool, error) {
	eventID := ev.Id
	defer b.linkLocks.lock(eventID)()
	cursor := ""
	for page := 0; page < maxSalesLinkPages; page++ {
		list, listErr := b.arena.ListSalesLinks(ctx, jwt, orgID, SalesLinkQuery{EventID: &eventID, Status: slStatusActive, Limit: 50, Cursor: cursor})
		if listErr != nil {
			return openapi.SalesLink{}, false, listErr
		}
		for _, l := range list.Items {
			if l.Target.Kind == slKindEvent && l.Target.EventId != nil && *l.Target.EventId == eventID && l.PlacementKind == cardLinkPlacement {
				return l, true, nil
			}
		}
		if list.NextCursor == "" || list.NextCursor == cursor {
			break
		}
		cursor = list.NextCursor
	}
	if !create {
		return openapi.SalesLink{}, false, nil
	}
	label := strings.Join(strings.Fields(ev.Name), " ")
	if utf8.RuneCountInString(label) > maxLabelRunes {
		label = string([]rune(label)[:maxLabelRunes])
	}
	if label == "" {
		label = ev.Id.String()
	}
	created, err := b.arena.CreateSalesLink(ctx, jwt, orgID, SalesLinkCreate{
		Target: openapi.SalesLinkTarget{Kind: slKindEvent, EventId: &eventID}, Label: label, PlacementKind: cardLinkPlacement,
	})
	if err != nil {
		return openapi.SalesLink{}, false, err
	}
	return created, true, nil
}

// ─── the guest list ───────────────────────────────────────────────────────────

// cardGuestList answers "ec:gl:<event>": an event with one live date sends that
// date's list at once, an event with several opens its dates (the list is a
// date's, ses:gl).
func (b *Bot) cardGuestList(ctx context.Context, chatID int64, editMsgID *int, from *models.User, eventID uuid.UUID) {
	id, jwt, ok := b.ecIdentity(ctx, chatID, editMsgID, from)
	if !ok {
		return
	}
	loc := id.Locale()
	if !canViewSales(id) {
		b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.ec.no_rights", nil), b.cardKeyboard(loc, "ec:o:"+eventID.String()))
		return
	}
	sessions, err := b.arena.ListSessions(ctx, jwt, id.Current.OrgID, eventID)
	if err != nil {
		b.ecError(ctx, chatID, editMsgID, id, err, "ec:o:"+eventID.String())
		return
	}
	var live []openapi.SessionItem
	for _, s := range sessions {
		if string(s.Status) != "cancelled" {
			live = append(live, s)
		}
	}
	if len(live) == 1 {
		b.sendGuestList(ctx, chatID, from, live[0].Id)
		return
	}
	b.sessionsOpen(ctx, chatID, editMsgID, from, eventID)
}
