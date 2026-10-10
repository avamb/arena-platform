package eventbot

// ec_callbacks.go — the router of the event-center buttons of spec 35 stage 1.
// Two prefixes: "el:" is the events list (events_list.go: filter, page, open
// a row, clear the search) and "ec:" is everything on and under an event's
// card:
//
//	ec:o:<event>[:1]   the card (":1" = dates expanded)
//	ec:es:<event>      summary of the event      ec:ss:<session>  of one date
//	ec:ce:<event>      sales CSV of the event    ec:cs:<session>  of one date
//	ec:cm:<session>    summary CSV of one date
//	ec:nt              the Notifications screen
//
// The ids are in the payload (a UUID plus its prefix is 43 bytes, inside
// Telegram's 64), because a button on a card must keep meaning THAT event
// however many other cards the chat holds; lists, which cannot carry ids for
// five rows, use indexes (paging.go).

import (
	"context"
	"strings"

	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"
)

// ecCallback handles every "ec:<data>" press.
func (b *Bot) ecCallback(ctx context.Context, chatID int64, msgID int, from *models.User, data string) {
	kind, arg, _ := strings.Cut(data, ":")
	if kind == "nt" {
		if id, _, ok := b.ecIdentity(ctx, chatID, &msgID, from); ok {
			b.showNotifications(ctx, chatID, &msgID, id)
		}
		return
	}
	idPart, flag, _ := strings.Cut(arg, ":")
	target, err := uuid.Parse(idPart)
	if err != nil {
		return
	}
	switch kind {
	case "o":
		b.showEventCard(ctx, chatID, &msgID, from, target, flag == "1")
	case "es":
		b.showEventSummary(ctx, chatID, &msgID, from, target)
	case "ss":
		b.showSessionSummary(ctx, chatID, &msgID, from, target, "")
	case csvSessionSales, csvSessionSummary, csvEventSales:
		b.sendCSV(ctx, chatID, from, kind, target)
	}
}
