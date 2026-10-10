package eventbot

// event_card.go — the card of one event (EC-03, spec 35 §5.4). Its numbers
// come from ONE call, GET .../events/{id}/summary: places, money by currency
// with refunds and net, the door count, invitations, promo codes and the
// category table. An event with several dates shows the totals of the whole
// event first and keeps its dates in a list the person expands; a single-date
// event names that date at the top instead.
//
// A role that may not read sales (or an API that fails the call) still gets
// the card as it used to be — name, status, link and the dates — so the
// edit, sample ticket and sessions buttons never disappear with the figures.

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

// membershipRoleManager is the membership role of an organization manager.
const membershipRoleManager = "organizer"

// canViewSales reports whether the person may read sales figures, summaries
// and exports (order.read + session.read): the owner, the manager, and the
// platform operator working through the bot. Buttons for these are shown
// only to them.
func canViewSales(id *Identity) bool {
	if id == nil || id.Current == nil {
		return false
	}
	return id.Superadmin || id.Current.Role == membershipRoleOwner || id.Current.Role == membershipRoleManager
}

// ecIdentity resolves who is asking and in which organization for an
// event-center screen, answering the standard "who are you / which
// organization" screens itself when it cannot.
func (b *Bot) ecIdentity(ctx context.Context, chatID int64, editMsgID *int, from *models.User) (*Identity, string, bool) {
	id, jwt, err := b.resolveIdentity(ctx, from.ID)
	if err != nil {
		b.replyIdentityError(ctx, chatID, from, err)
		return nil, "", false
	}
	if id.Current == nil {
		b.showOrgChooserFor(ctx, chatID, editMsgID, id)
		return nil, "", false
	}
	return id, jwt, true
}

// ecError answers a failed API call of an event-center screen: a missing row
// (or one of another organization — the API answers both 404) is "not
// found", a refused one is "no rights", anything else a logged generic error.
// back is the callback the single button returns to.
func (b *Bot) ecError(ctx context.Context, chatID int64, editMsgID *int, id *Identity, err error, back string) {
	loc := id.Locale()
	key := "bot.error_generic"
	switch {
	case IsAPIError(err, http.StatusNotFound):
		key = "bot.ec.not_found"
	case IsAPIError(err, http.StatusForbidden):
		key = "bot.ec.no_rights"
	default:
		b.logger.Error("eventbot: arena-api call failed", slog.Int64("telegram_user_id", id.Link.TelegramUserID), slog.String("error", err.Error()))
	}
	b.reply(ctx, chatID, editMsgID, b.texts.T(loc, key, nil), b.backKeyboard(loc, back))
}

// showEvent is the card with its dates collapsed; the page argument is the
// old list page and is not used any more (the list keeps its own state).
func (b *Bot) showEvent(ctx context.Context, chatID int64, editMsgID *int, from *models.User, eventID uuid.UUID, _ int) {
	b.showEventCard(ctx, chatID, editMsgID, from, eventID, false)
}

// showEventCard draws the card; expanded lists every date of a multi-date
// event under the totals.
func (b *Bot) showEventCard(ctx context.Context, chatID int64, editMsgID *int, from *models.User, eventID uuid.UUID, expanded bool) {
	id, jwt, ok := b.ecIdentity(ctx, chatID, editMsgID, from)
	if !ok {
		return
	}
	loc := id.Locale()
	orgID := id.Current.OrgID
	events, err := b.arena.ListEvents(ctx, jwt, orgID)
	if err != nil {
		b.replyAPIError(ctx, chatID, editMsgID, id, err)
		return
	}
	var event *openapi.EventItem
	for i := range events {
		if events[i].Id == eventID {
			event = &events[i]
			break
		}
	}
	if event == nil {
		b.renderEvents(ctx, chatID, editMsgID, id, jwt, newEventsDialog(orgID), "")
		return
	}
	// The list's state stays, so "Back" returns to the same filter, search
	// and page; the dialog is now on a card, where a typed text is no search.
	st, _, _ := b.loadEventsState(ctx, from.ID, orgID)
	b.saveEventsState(ctx, from.ID, st, eventsStepCard)

	link := ""
	if url := b.eventLink(ctx, jwt, orgID, *event); url != "" {
		link = "\n" + b.texts.T(loc, "bot.wz.link_line", map[string]any{"URL": url})
	}
	head := b.texts.T(loc, "bot.event_card", map[string]any{
		"Name": Esc(event.Name), "Status": EventChip(event.SalesState) + " " + b.statusText(loc, string(event.Status)), "Link": link,
	})

	head += b.promoterEventLine(loc, *event) // who promotes it (EC-14)

	var sum *openapi.EventSummary
	if canViewSales(id) {
		s, err := b.arena.EventSummary(ctx, jwt, orgID, eventID)
		if err != nil {
			b.logger.Warn("eventbot: event summary failed", slog.String("event_id", eventID.String()), slog.String("error", err.Error()))
		} else {
			sum = &s
		}
	}

	var sb strings.Builder
	sb.WriteString(head)
	var rows [][]models.InlineKeyboardButton
	if sum == nil {
		b.writePlainDates(ctx, &sb, jwt, loc, orgID, eventID)
	} else {
		rows = b.writeCardFigures(&sb, loc, eventID, *sum, expanded)
	}
	rows = append(rows,
		[]models.InlineKeyboardButton{
			{Text: b.texts.T(loc, "bot.wz.edit_btn", nil), CallbackData: "wz:edit:" + eventID.String()},
			{Text: b.texts.T(loc, "bot.wz.copy_btn", nil), CallbackData: "wz:copy:" + eventID.String()},
		},
		[]models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.sample_btn", nil), CallbackData: "sample:" + eventID.String()}},
		b.sessionsOrdersRow(loc, id, eventID),
	)
	if row := b.promoEventRow(loc, id, eventID); row != nil { // promo codes of the event (EC-11)
		rows = append(rows, row)
	}
	if row := b.promoterEventRow(loc, id, eventID); row != nil { // the event's promoter (EC-14)
		rows = append(rows, row)
	}
	// Publish, take off sale, archive, delete: what the status allows (EC-10).
	rows = append(rows, b.eventStatusRows(loc, id, string(event.Status), eventID)...)
	if canInvite(id) {
		// Free tickets for guests (EC-12), starting at this event's dates.
		rows = append(rows, []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.inv.btn", nil), CallbackData: "iv:e:" + eventID.String()}})
	}
	rows = append(rows, []models.InlineKeyboardButton{
		{Text: "« " + b.texts.T(loc, "bot.btn_back", nil), CallbackData: "el:b"},
		{Text: b.texts.T(loc, "bot.btn_home", nil), CallbackData: "home"},
	})
	b.reply(ctx, chatID, editMsgID, clipMessage(sb.String(), maxMessageRunes), &models.InlineKeyboardMarkup{InlineKeyboard: rows})
}

// writePlainDates is the card without figures: the dates, one summary call
// each when the person may read them (sessionLine tolerates a refusal).
func (b *Bot) writePlainDates(ctx context.Context, sb *strings.Builder, jwt, loc string, orgID, eventID uuid.UUID) {
	sessions, err := b.arena.ListSessions(ctx, jwt, orgID, eventID)
	if err != nil {
		b.logger.Warn("eventbot: sessions list failed", slog.String("event_id", eventID.String()), slog.String("error", err.Error()))
		return
	}
	if len(sessions) == 0 {
		sb.WriteString("\n\n" + b.texts.T(loc, "bot.event_no_sessions", nil))
		return
	}
	for i, s := range sessions {
		if i >= maxSessionLines {
			sb.WriteString("\n…")
			break
		}
		sb.WriteString("\n\n")
		sb.WriteString(b.sessionLine(ctx, jwt, loc, orgID, s))
	}
}

// writeCardFigures writes the figures part of the card and returns its
// buttons (summary, export, and the dates toggle with the per-date rows).
func (b *Bot) writeCardFigures(sb *strings.Builder, loc string, eventID uuid.UUID, sum openapi.EventSummary, expanded bool) [][]models.InlineKeyboardButton {
	sessions := sum.Sessions
	multi := len(sessions) > 1
	sb.WriteString("\n\n")
	switch {
	case len(sessions) == 0:
		sb.WriteString(b.texts.T(loc, "bot.event_no_sessions", nil))
	case multi:
		sb.WriteString(b.texts.T(loc, "bot.ec.all_dates", map[string]any{"N": len(sessions)}))
		if next, ok := nextSession(sessions, time.Now()); ok {
			sb.WriteString("\n" + b.texts.T(loc, "bot.ec.next_line", map[string]any{"When": Esc(b.sessionWhen(loc, next))}))
		}
	default:
		sb.WriteString(b.texts.T(loc, "bot.ec.when_line", map[string]any{
			"When": Esc(b.sessionWhen(loc, sessions[0])), "Venue": venueOf(sessions[0].VenueName),
		}))
	}
	f := figuresFromEvent(sum)
	if len(sessions) > 0 {
		sb.WriteString("\n" + b.totalsText(loc, f))
		if t := b.tiersText(loc, f.Tiers); t != "" {
			sb.WriteString("\n\n" + t)
		}
	}

	rows := [][]models.InlineKeyboardButton{{
		{Text: b.texts.T(loc, "bot.ec.summary_btn", nil), CallbackData: "ec:es:" + eventID.String()},
		{Text: b.texts.T(loc, "bot.ec.csv_btn", nil), CallbackData: "ec:ce:" + eventID.String()},
	}}
	if !multi {
		return rows
	}
	if !expanded {
		return append(rows, []models.InlineKeyboardButton{{
			Text: b.texts.T(loc, "bot.ec.dates_btn", map[string]any{"N": len(sessions)}), CallbackData: "ec:o:" + eventID.String() + ":1",
		}})
	}
	for i, s := range sessions {
		if i >= maxSessionLines {
			sb.WriteString("\n\n" + b.texts.T(loc, "bot.ec.more", map[string]any{"N": len(sessions) - i}))
			break
		}
		sb.WriteString("\n\n" + b.sessionEntry(loc, s))
		when := shortWhen(s)
		rows = append(rows, []models.InlineKeyboardButton{
			{Text: "📊 " + when, CallbackData: "ec:ss:" + s.Id.String()},
			{Text: "⬇ " + when, CallbackData: "ec:cs:" + s.Id.String()},
		})
	}
	return append(rows, []models.InlineKeyboardButton{{
		Text: b.texts.T(loc, "bot.ec.dates_hide_btn", nil), CallbackData: "ec:o:" + eventID.String() + ":0",
	}})
}

// nextSession is the first date that has not ended and is not cancelled.
func nextSession(sessions []openapi.EventSummarySession, now time.Time) (openapi.EventSummarySession, bool) {
	for _, s := range sessions {
		if s.Status != "cancelled" && s.EndAt.After(now) {
			return s, true
		}
	}
	return openapi.EventSummarySession{}, false
}

// sessionsOrdersRow is the card's "Sessions" button, with "Orders" (the
// orders of this event) beside it for a role that may read orders.
func (b *Bot) sessionsOrdersRow(loc string, id *Identity, eventID uuid.UUID) []models.InlineKeyboardButton {
	row := []models.InlineKeyboardButton{{Text: b.texts.T(loc, "bot.ses.open_btn", nil), CallbackData: "ses:list:" + eventID.String()}}
	if canViewSales(id) {
		row = append(row, models.InlineKeyboardButton{Text: b.texts.T(loc, "bot.ord.btn", nil), CallbackData: "or:e:" + eventID.String()})
	}
	return row
}
