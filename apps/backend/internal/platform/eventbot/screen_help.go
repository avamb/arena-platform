package eventbot

// screen_help.go — "❓ What is here?" (2026-11-13): the help of ONE screen.
//
// A screen builds its buttons from ONE list of items (screenItems). The
// keyboard is rendered from that list, and so is the help: one line per item,
// in the order of the screen. An item has an id, and the id names its help line
// (bot.hp.i.<id>). A role never reads the line of a button it does not have,
// because the item is not in its list. A new item needs an id in helpItemIDs
// and a line in every language (TestScreenHelp_EveryItemHasALine).
//
// The press is "hp:<screen>" or "hp:<screen>:<event uuid>". It keeps nothing:
// the screen is built again from the real state, and the one button « Back
// returns to the screen itself. The list of events keeps its own dialog, so
// « Back shows the same filter, search and page.

import (
	"context"
	"strings"

	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

// helpPrefix is the callback prefix of the help screens.
const helpPrefix = "hp"

// The screens that have a help: the value is the first part after "hp:".
const (
	hpList     = "el"   // My events
	hpCard     = "ec"   // the card of one event
	hpSales    = "es"   // card group: Sales and orders
	hpEdit     = "ee"   // card group: Edit
	hpPromo    = "ep"   // card group: Promotion
	hpMore     = "em"   // card group: More
	hpOrders   = "ord"  // section: Orders and refunds
	hpPromoSec = "pro"  // section: Promotion
	hpDoor     = "door" // section: Entrance to the event
	hpSettings = "set"  // section: Settings
	hpShop     = "sh"   // the shop home
)

// screenItem is one control of a screen. Rows are its buttons (several rows for
// the rows of events, several buttons in a row for the two filter chips); an item
// without rows is a hint that has no button. Join puts the first row of the item
// into the row before it.
type screenItem struct {
	ID    string
	Rows  [][]button
	Label string // the bold label of the help line; the first button's text when empty
	Join  bool
}

type screenItems []screenItem

// add appends one item with one row; a nil row is a control the person does not
// have, so nothing is added.
func (s *screenItems) add(id string, row []button) {
	if row != nil {
		*s = append(*s, screenItem{ID: id, Rows: [][]button{row}})
	}
}

// addJoined is add, but the button shares the row of the item before it.
func (s *screenItems) addJoined(id string, row []button) {
	if row != nil {
		*s = append(*s, screenItem{ID: id, Rows: [][]button{row}, Join: true})
	}
}

// rows renders the keyboard of the list.
func (s screenItems) rows() [][]button {
	var out [][]button
	for _, it := range s {
		for i, row := range it.Rows {
			if i == 0 && it.Join && len(out) > 0 {
				out[len(out)-1] = append(out[len(out)-1], row...)
				continue
			}
			out = append(out, row)
		}
	}
	return out
}

// ids is the id of each item, in order.
func (s screenItems) ids() []string {
	out := make([]string, 0, len(s))
	for _, it := range s {
		out = append(out, it.ID)
	}
	return out
}

func (it screenItem) label() string {
	if it.Label != "" {
		return it.Label
	}
	if len(it.Rows) > 0 && len(it.Rows[0]) > 0 {
		return it.Rows[0][0].Text
	}
	return ""
}

// helpItemIDs is every id an item of a help screen may have.
var helpItemIDs = []string{
	"el.filters", "el.search", "el.event", "el.paging", "el.clear",
	"ec.publish", "ec.link", "ec.s", "ec.e", "ec.p", "ec.m",
	"gs.summary", "gs.orders", "gs.guests", "gs.csv", "gs.dates",
	"ge.edit", "ge.sessions", "ge.cat", "ge.materials", "ge.sample", "ge.text", "ge.promoter",
	"gp.promo", "gp.invite", "gp.links", "gp.referral",
	"gm.copy", "gm.publish", "gm.off", "gm.archive", "gm.delete",
	"so.orders", "so.refunds", "so.disputes",
	"sp.promo", "sp.invite", "sp.link", "sp.mail",
	"sd.scanner", "sd.guests",
	"ss.org", "ss.pay", "ss.team", "ss.team_add", "ss.promoter", "ss.venues", "ss.notif", "ss.lang", "ss.switch",
	"sh.orders", "sh.close", "sh.open", "sh.settings", "sh.web",
}

func helpItemKey(id string) string { return "bot.hp.i." + id }

// helpKeys lists the texts of the help screens.
var helpKeys = func() []string {
	keys := []string{"bot.hp.btn", "bot.hp.title", "bot.hp.path", "bot.hp.path_sep", "bot.hp.p_event", "bot.hp.l_search", "bot.hp.l_event"}
	for _, id := range helpItemIDs {
		keys = append(keys, helpItemKey(id))
	}
	return keys
}()

func init() { MessageKeys = append(MessageKeys, helpKeys...) }

// helpData is the callback of the help button of a screen.
func helpData(screen, arg string) string {
	if arg == "" {
		return helpPrefix + ":" + screen
	}
	return helpPrefix + ":" + screen + ":" + arg
}

// helpRow is the "❓ What is here?" button.
func (b *Bot) helpRow(loc, screen, arg string) []button {
	return b.mnBtn(loc, "bot.hp.btn", helpData(screen, arg))
}

// isEventsHelp reports the help of a screen of "My events": it belongs to the
// list's dialog, so the list keeps its filter, search and page.
func isEventsHelp(data string) bool {
	parts := strings.Split(data, ":")
	if len(parts) < 2 || parts[0] != helpPrefix {
		return false
	}
	switch parts[1] {
	case hpList, hpCard, hpSales, hpEdit, hpPromo, hpMore:
		return true
	}
	return false
}

// helpGroupScreen is the help screen of a card group, and the other way round.
func helpGroupScreen(group string) string {
	switch group {
	case cardGroupSales:
		return hpSales
	case cardGroupEdit:
		return hpEdit
	case cardGroupPromo:
		return hpPromo
	}
	return hpMore
}

func helpScreenGroup(screen string) (string, bool) {
	switch screen {
	case hpSales:
		return cardGroupSales, true
	case hpEdit:
		return cardGroupEdit, true
	case hpPromo:
		return cardGroupPromo, true
	case hpMore:
		return cardGroupMore, true
	}
	return "", false
}

// screenHelpText is the help: a title, one line for each item, and the way back.
func (b *Bot) screenHelpText(loc, name string, items screenItems, path []string) string {
	var sb strings.Builder
	sb.WriteString(b.texts.T(loc, "bot.hp.title", map[string]any{"Screen": Esc(name)}))
	sb.WriteString("\n")
	for _, it := range items {
		sb.WriteString("\n<b>" + Esc(it.label()) + "</b>: " + b.texts.T(loc, helpItemKey(it.ID), nil))
	}
	sep := b.texts.T(loc, "bot.hp.path_sep", nil)
	sb.WriteString("\n\n" + b.texts.T(loc, "bot.hp.path", map[string]any{"Path": Esc(strings.Join(path, sep))}))
	return clipMessage(sb.String(), maxMessageRunes)
}

// helpBack is the only button of a help screen.
func (b *Bot) helpBack(loc, back string) *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]button{
		{{Text: "« " + b.texts.T(loc, "bot.btn_back", nil), CallbackData: back}},
	}}
}

// helpCallback handles every "hp:<screen>[:<event>]" press. The screen is built
// again from the state it has now.
func (b *Bot) helpCallback(ctx context.Context, chatID int64, msgID int, from *models.User, data string) {
	screen, arg, _ := strings.Cut(data, ":")
	if screen == hpShop {
		b.shopHelp(ctx, chatID, msgID, from)
		return
	}
	id, jwt, ok := b.ecIdentity(ctx, chatID, &msgID, from)
	if !ok {
		return
	}
	loc := id.Locale()
	menu := b.texts.T(loc, menuButtonKey, nil)
	events := b.texts.T(loc, "bot.btn_events", nil)
	var (
		name  string
		items screenItems
		path  []string
		back  string
	)
	switch screen {
	case hpList:
		list, err := b.arena.ListEvents(ctx, jwt, id.Current.OrgID)
		if err != nil {
			b.ecError(ctx, chatID, &msgID, id, err, "el:b")
			return
		}
		st, _, _ := b.loadEventsState(ctx, from.ID, id.Current.OrgID)
		items, _, _, _, _ = b.eventsListItems(loc, st, list)
		name, path, back = events, []string{menu, events}, "el:b"
	case hpCard, hpSales, hpEdit, hpPromo, hpMore:
		eventID, err := uuid.Parse(arg)
		if err != nil {
			return
		}
		event, err := b.cardEvent(ctx, jwt, id.Current.OrgID, eventID)
		if err != nil {
			b.ecError(ctx, chatID, &msgID, id, err, "el:b")
			return
		}
		pick := b.texts.T(loc, "bot.hp.p_event", nil)
		if group, isGroup := helpScreenGroup(screen); isGroup {
			var sum *openapi.EventSummary
			if group == cardGroupSales || group == cardGroupEdit {
				sum = b.cardSummary(ctx, id, jwt, eventID)
			}
			items = b.cardGroupItems(loc, id, eventID, group, cardFactsOf(*event, "", sum))
			if len(items) == 0 { // the group screen's own refusal (showCardGroup)
				b.reply(ctx, chatID, &msgID, b.texts.T(loc, "bot.ec.no_rights", nil), b.cardKeyboard(loc, "ec:o:"+eventID.String()))
				return
			}
			name = b.texts.T(loc, cardGroupTitleKey(group), nil)
			path, back = []string{menu, events, pick, name}, "ec:g:"+eventID.String()+":"+group
			break
		}
		url := b.eventLink(ctx, jwt, id.Current.OrgID, *event)
		items = b.cardItems(loc, id, eventID, cardFactsOf(*event, url, b.cardSummary(ctx, id, jwt, eventID)))
		name, path, back = event.Name, []string{menu, events, pick}, "ec:o:"+eventID.String()
	case hpOrders, hpPromoSec, hpDoor, hpSettings:
		if b.sectionHelpRefused(loc, id, screen) {
			b.reply(ctx, chatID, &msgID, b.texts.T(loc, "bot.ec.no_rights", nil), b.backKeyboard(loc, "home"))
			return
		}
		var sec string
		switch screen {
		case hpOrders:
			items, sec = b.ordersSectionItems(ctx, loc, id, jwt), "orders_btn"
		case hpPromoSec:
			items, sec = b.promoSectionItems(loc, id), "promo_btn"
		case hpDoor:
			items, sec = b.doorSectionItems(loc, id), "door_btn"
		default:
			items, sec = b.settingsSectionItems(ctx, loc, id, jwt), "settings_btn"
		}
		name = b.texts.T(loc, "bot.mn."+sec, nil)
		path, back = []string{menu, name}, menuSectionPrefix+":"+screen
	default:
		return
	}
	if len(items) == 0 {
		b.reply(ctx, chatID, &msgID, b.texts.T(loc, "bot.ec.no_rights", nil), b.backKeyboard(loc, "home"))
		return
	}
	b.reply(ctx, chatID, &msgID, b.screenHelpText(loc, name, items, path), b.helpBack(loc, back))
}

// sectionHelpRefused applies the gates of the section screens
// (menuSectionCallback) to their help: a role that cannot open the screen gets
// the refusal of the screen, never a help text.
func (b *Bot) sectionHelpRefused(loc string, id *Identity, screen string) bool {
	switch screen {
	case hpOrders:
		return !canViewSales(id)
	case hpPromoSec:
		return len(b.promoSectionItems(loc, id)) == 0
	}
	return false
}

// shopHelp is the help of the shop home.
func (b *Bot) shopHelp(ctx context.Context, chatID int64, msgID int, from *models.User) {
	id, jwt, ok := b.shopIdentity(ctx, chatID, &msgID, from)
	if !ok {
		return
	}
	loc := id.Locale()
	settings, err := b.shopHomeSettings(ctx, jwt, id)
	if err != nil {
		b.shopError(ctx, chatID, &msgID, id, err, "sh:home")
		return
	}
	name := b.texts.T(loc, "bot.sh.btn", nil)
	path := []string{b.texts.T(loc, menuButtonKey, nil), name}
	b.reply(ctx, chatID, &msgID, b.screenHelpText(loc, name, b.shopHomeItems(loc, settings), path), b.helpBack(loc, "sh:home"))
}
