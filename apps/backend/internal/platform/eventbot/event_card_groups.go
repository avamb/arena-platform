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
	"bot.ec.g.link_text", "bot.ec.g.link_text_noqr", "bot.ec.g.link_none",
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
	var rows [][]models.InlineKeyboardButton
	if cardCanPublish(id, f) {
		rows = append(rows, b.mnBtn(loc, "bot.evs.publish_btn", "ec:ev:"+eventID.String()+":"+evsActPublish))
	}
	order := []string{cardLinkKind, cardGroupSales, cardGroupEdit, cardGroupPromo, cardGroupMore}
	if f.past() {
		order = []string{cardGroupSales, cardLinkKind, cardGroupEdit, cardGroupPromo, cardGroupMore}
	}
	for _, kind := range order {
		if row := b.cardTopButton(loc, id, eventID, kind, f); row != nil {
			rows = append(rows, row)
		}
	}
	return rows
}

// cardTopButton is one of the five buttons, or nil when it has nothing to open.
func (b *Bot) cardTopButton(loc string, id *Identity, eventID uuid.UUID, kind string, f cardFacts) []models.InlineKeyboardButton {
	if kind == cardLinkKind {
		if !f.HasLink || !canSalesLinks(id) {
			return nil
		}
		return b.mnBtn(loc, "bot.ec.g.link_btn", "ec:"+cardLinkKind+":"+eventID.String())
	}
	if len(b.cardGroupRows(loc, id, eventID, kind, f)) == 0 {
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
	ev := eventID.String()
	var rows [][]models.InlineKeyboardButton
	add := func(row []models.InlineKeyboardButton) {
		if row != nil {
			rows = append(rows, row)
		}
	}
	switch group {
	case cardGroupSales:
		if !canViewSales(id) {
			return nil
		}
		add(b.mnBtn(loc, "bot.ec.summary_btn", "ec:es:"+ev))
		add(b.mnBtn(loc, "bot.ord.btn", "or:e:"+ev))
		add(b.mnBtn(loc, "bot.ec.g.guests_btn", "ec:gl:"+ev))
		add(b.mnBtn(loc, "bot.ec.csv_btn", "ec:ce:"+ev))
		if f.Dates > 1 {
			add([]models.InlineKeyboardButton{{
				Text: b.texts.T(loc, "bot.ec.dates_btn", map[string]any{"N": f.Dates}), CallbackData: "ec:o:" + ev + ":1",
			}})
		}
	case cardGroupEdit:
		add(b.mnBtn(loc, "bot.wz.edit_btn", "wz:edit:"+ev))
		add(b.mnBtn(loc, "bot.ses.open_btn", "ses:list:"+ev))
		if f.SingleLive { // the categories of an event with one live date (EC-15)
			if row := b.categoryEventRow(loc, id, eventID); row != nil {
				row[0].Text = b.texts.T(loc, "bot.ec.g.cat_btn", nil)
				add(row)
			}
		}
		add(b.materialsEventRow(loc, eventID))
		add(b.mnBtn(loc, "bot.sample_btn", "sample:"+ev))
		if canViewSales(id) { // the owner, the manager and the operator write the text of the e-mail (RT-06)
			add(b.mnBtn(loc, "bot.tf.event_btn", "tf:n:"+ev))
		}
		add(b.promoterEventRow(loc, id, eventID))
	case cardGroupPromo:
		add(b.promoEventRow(loc, id, eventID))
		if canInvite(id) {
			add(b.mnBtn(loc, "bot.inv.btn", "iv:e:"+ev))
		}
		add(b.referralEventRow(loc, id, eventID))
	case cardGroupMore:
		add(b.mnBtn(loc, "bot.wz.copy_btn", "wz:copy:"+ev))
		for _, row := range b.eventStatusRows(loc, id, f.Status, eventID) {
			for _, btn := range row {
				rows = append(rows, []models.InlineKeyboardButton{btn})
			}
		}
	}
	return rows
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
	textKey := "bot.ec.g.link_text"
	if !b.sendEventQR(ctx, chatID, jwt, orgID, *event) {
		textKey = "bot.ec.g.link_text_noqr"
	}
	rows := [][]models.InlineKeyboardButton{b.mnBtn(loc, "bot.ec.g.link_more_btn", "ql:e:"+eventID.String())}
	rows = append(rows, b.cardBackRows(loc, eventID)...)
	b.reply(ctx, chatID, editMsgID, b.texts.T(loc, textKey, map[string]any{"Name": Esc(event.Name), "URL": Esc(url)}), &models.InlineKeyboardMarkup{InlineKeyboard: rows})
}

// sendEventQR sends the QR code of the event page as an image. The code rests
// on one sales link of the event (placement "other"): the one that is already
// there, or a new one. It reports whether the image went out.
func (b *Bot) sendEventQR(ctx context.Context, chatID int64, jwt string, orgID uuid.UUID, ev openapi.EventItem) bool {
	link, err := b.eventSalesLink(ctx, jwt, orgID, ev)
	if err != nil {
		b.logger.Warn("eventbot: event sales link failed", slog.String("event_id", ev.Id.String()), slog.String("error", err.Error()))
		return false
	}
	png, err := b.arena.SalesLinkQR(ctx, jwt, orgID, link.Id, "png", 1024)
	if err != nil {
		b.logger.Warn("eventbot: event qr failed", slog.String("link_id", link.Id.String()), slog.String("error", err.Error()))
		return false
	}
	if _, err := b.tg.SendPhoto(ctx, &tgbot.SendPhotoParams{
		ChatID:    chatID,
		Photo:     &models.InputFileUpload{Filename: link.Code + ".png", Data: bytes.NewReader(png)},
		Caption:   Esc(ev.Name),
		ParseMode: models.ParseModeHTML,
	}); err != nil {
		b.logger.Warn("eventbot: send event qr failed", slog.Int64("chat_id", chatID), slog.String("error", unwrapURLError(err).Error()))
		return false
	}
	return true
}

// eventSalesLink returns the active link of the event with the placement
// "other", or makes one named after the event.
func (b *Bot) eventSalesLink(ctx context.Context, jwt string, orgID uuid.UUID, ev openapi.EventItem) (openapi.SalesLink, error) {
	eventID := ev.Id
	list, err := b.arena.ListSalesLinks(ctx, jwt, orgID, SalesLinkQuery{EventID: &eventID, Status: slStatusActive, Limit: 50})
	if err != nil {
		return openapi.SalesLink{}, err
	}
	for _, l := range list.Items {
		if l.Target.Kind == slKindEvent && l.Target.EventId != nil && *l.Target.EventId == eventID && l.PlacementKind == cardLinkPlacement {
			return l, nil
		}
	}
	label := strings.Join(strings.Fields(ev.Name), " ")
	if utf8.RuneCountInString(label) > maxLabelRunes {
		label = string([]rune(label)[:maxLabelRunes])
	}
	if label == "" {
		label = ev.Id.String()
	}
	return b.arena.CreateSalesLink(ctx, jwt, orgID, SalesLinkCreate{
		Target: openapi.SalesLinkTarget{Kind: slKindEvent, EventId: &eventID}, Label: label, PlacementKind: cardLinkPlacement,
	})
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
