package eventbot

// series.go - the event card's "Tour" line and button (spec 63, L-2). The
// button opens the organization's tours: pressing one binds the event to it,
// "Unbind" clears the bind. The rules (a tour of another organization or an
// archived one is refused, 422 event.invalid_series) live in arena-api. The
// list is read again on every press and a tour is addressed by its place in
// that list, because a callback is 64 bytes and holds an event id already.

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"
)

// seriesEventLine is the event card's tour line; "" when the event has no tour
// or the read failed (the card never waits on it).
func (b *Bot) seriesEventLine(ctx context.Context, loc, jwt string, orgID, eventID uuid.UUID) string {
	_, name, err := b.arena.EventSeries(ctx, jwt, orgID, eventID)
	if err != nil || name == "" {
		return ""
	}
	return "\n" + b.texts.T(loc, "bot.ser.event_line", map[string]any{"Name": Esc(name)})
}

// seriesEventRow is the card's button for a role that may edit events.
func (b *Bot) seriesEventRow(loc string, id *Identity, eventID uuid.UUID) []models.InlineKeyboardButton {
	if !canViewSales(id) {
		return nil
	}
	return []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.ser.event_btn", nil), CallbackData: "sr:e:" + eventID.String()}}
}

// seriesCallback handles every "sr:<data>" press: e:<event> opens the list,
// s:<event>:<n> binds the n-th tour, u:<event> unbinds.
func (b *Bot) seriesCallback(ctx context.Context, chatID int64, msgID int, from *models.User, data string) {
	id, jwt, ok := b.ecIdentity(ctx, chatID, &msgID, from)
	if !ok {
		return
	}
	loc := id.Locale()
	if !canViewSales(id) {
		b.reply(ctx, chatID, &msgID, b.texts.T(loc, "bot.ec.no_rights", nil), b.backKeyboard(loc, "home"))
		return
	}
	parts := strings.Split(data, ":")
	if len(parts) < 2 {
		return
	}
	eventID, err := uuid.Parse(parts[1])
	if err != nil {
		return
	}
	orgID := id.Current.OrgID
	switch parts[0] {
	case "e":
		b.seriesRender(ctx, chatID, &msgID, id, jwt, eventID, "")
	case "u":
		if err := b.arena.LinkEventSeries(ctx, jwt, orgID, eventID.String(), ""); err != nil {
			b.ecError(ctx, chatID, &msgID, id, err, "ec:o:"+eventID.String())
			return
		}
		b.seriesRender(ctx, chatID, &msgID, id, jwt, eventID, b.texts.T(loc, "bot.ser.unbound", nil)+"\n\n")
	case "s":
		if len(parts) < 3 {
			return
		}
		items, err := b.arena.ActiveSeries(ctx, jwt, orgID)
		if err != nil {
			b.ecError(ctx, chatID, &msgID, id, err, "ec:o:"+eventID.String())
			return
		}
		n, convErr := strconv.Atoi(parts[2])
		if convErr != nil || n < 0 || n >= len(items) {
			// A stale row: show the list as it is now rather than bind the wrong tour.
			b.seriesRender(ctx, chatID, &msgID, id, jwt, eventID, b.texts.T(loc, "bot.ser.gone", nil)+"\n\n")
			return
		}
		if err := b.arena.LinkEventSeries(ctx, jwt, orgID, eventID.String(), items[n].ID); err != nil {
			if IsAPIError(err, http.StatusUnprocessableEntity) {
				b.seriesRender(ctx, chatID, &msgID, id, jwt, eventID, b.texts.T(loc, "bot.ser.refused", nil)+"\n\n")
				return
			}
			b.ecError(ctx, chatID, &msgID, id, err, "ec:o:"+eventID.String())
			return
		}
		b.seriesRender(ctx, chatID, &msgID, id, jwt, eventID, b.texts.T(loc, "bot.ser.saved", map[string]any{"Name": Esc(items[n].Name)})+"\n\n")
	}
}

// seriesRender shows the tours of the organization for one event.
func (b *Bot) seriesRender(ctx context.Context, chatID int64, editMsgID *int, id *Identity, jwt string, eventID uuid.UUID, prefix string) {
	loc := id.Locale()
	orgID := id.Current.OrgID
	back := "ec:o:" + eventID.String()
	items, err := b.arena.ActiveSeries(ctx, jwt, orgID)
	if err != nil {
		b.ecError(ctx, chatID, editMsgID, id, err, back)
		return
	}
	curID, curName, err := b.arena.EventSeries(ctx, jwt, orgID, eventID)
	if err != nil {
		b.ecError(ctx, chatID, editMsgID, id, err, back)
		return
	}
	text, rows := b.seriesScreen(loc, items, eventID, curID, curName)
	b.reply(ctx, chatID, editMsgID, prefix+text, &models.InlineKeyboardMarkup{InlineKeyboard: rows})
}

// seriesScreen builds the text and the keyboard of the tour list.
func (b *Bot) seriesScreen(loc string, items []SeriesItem, eventID uuid.UUID, curID, curName string) (string, [][]models.InlineKeyboardButton) {
	ev := eventID.String()
	var text string
	if curName != "" {
		text = b.texts.T(loc, "bot.ser.list_now", map[string]any{"Name": Esc(curName)})
	} else {
		text = b.texts.T(loc, "bot.ser.list_none_now", nil)
	}
	rows := [][]models.InlineKeyboardButton{}
	if len(items) == 0 {
		text += "\n\n" + b.texts.T(loc, "bot.ser.empty", nil)
	} else {
		text += "\n\n" + b.texts.T(loc, "bot.ser.list_hint", nil)
	}
	for i, s := range items {
		mark := ""
		if s.ID == curID {
			mark = "✔ "
		}
		rows = append(rows, []models.InlineKeyboardButton{{Text: mark + truncate(s.Name, 48), CallbackData: "sr:s:" + ev + ":" + strconv.Itoa(i)}})
	}
	if curID != "" {
		rows = append(rows, []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.ser.unbind_btn", nil), CallbackData: "sr:u:" + ev}})
	}
	rows = append(rows, []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.ser.back_btn", nil), CallbackData: "ec:o:" + ev}})
	return text, rows
}

// seriesKeys lists every bot.ser.* key; the locale test checks them in every
// catalog together with MessageKeys.
var seriesKeys = []string{
	"bot.ser.wz_ask", "bot.ser.wz_label", "bot.ser.warn_not_linked",
	"bot.ser.event_line", "bot.ser.event_btn", "bot.ser.list_now", "bot.ser.list_none_now",
	"bot.ser.list_hint", "bot.ser.empty", "bot.ser.unbind_btn", "bot.ser.back_btn",
	"bot.ser.saved", "bot.ser.unbound", "bot.ser.refused", "bot.ser.gone",
}

func init() {
	MessageKeys = append(MessageKeys, seriesKeys...)
}
