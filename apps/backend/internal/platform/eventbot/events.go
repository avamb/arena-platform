package eventbot

import (
	"context"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

// eventsPageSize is how many events one screen lists (spec 28 §5.1: 5).
const eventsPageSize = 5

// maxSessionsPerCard bounds the summary calls one event card makes.
const maxSessionsPerCard = 8

// SortEvents orders events for the list: upcoming ones (a date still ahead,
// or no date yet) first by their first date ascending, then past ones by
// their last date descending — the operator's next event is always on top.
func SortEvents(events []openapi.EventItem, now time.Time) (upcoming, past []openapi.EventItem) {
	for _, e := range events {
		if e.LastSessionAt == nil || !e.LastSessionAt.Before(now) {
			upcoming = append(upcoming, e)
		} else {
			past = append(past, e)
		}
	}
	sort.SliceStable(upcoming, func(i, j int) bool {
		a, b := upcoming[i].FirstSessionAt, upcoming[j].FirstSessionAt
		switch {
		case a == nil && b == nil:
			return upcoming[i].Name < upcoming[j].Name
		case a == nil:
			return false
		case b == nil:
			return true
		default:
			return a.Before(*b)
		}
	})
	sort.SliceStable(past, func(i, j int) bool {
		return past[i].LastSessionAt.After(*past[j].LastSessionAt)
	})
	return upcoming, past
}

// PageOf slices a list for page (1-based) of size; pages is the page count
// (at least 1). An out-of-range page is clamped.
func PageOf[T any](items []T, page, size int) (out []T, pageOut, pages int) {
	if size <= 0 {
		size = eventsPageSize
	}
	pages = (len(items) + size - 1) / size
	if pages == 0 {
		pages = 1
	}
	if page < 1 {
		page = 1
	}
	if page > pages {
		page = pages
	}
	start := (page - 1) * size
	end := start + size
	if start > len(items) {
		start = len(items)
	}
	if end > len(items) {
		end = len(items)
	}
	return items[start:end], page, pages
}

func (b *Bot) statusText(locale string, status string) string {
	switch status {
	case "draft":
		return b.texts.T(locale, "bot.status_draft", nil)
	case "published":
		return b.texts.T(locale, "bot.status_published", nil)
	case "archived":
		return b.texts.T(locale, "bot.status_archived", nil)
	case "cancelled":
		return b.texts.T(locale, "bot.status_cancelled", nil)
	default:
		return Esc(status)
	}
}

// sessionLine renders one date of an event from its summary; when the
// summary is unavailable it still names the date so the card is never empty.
func (b *Bot) sessionLine(ctx context.Context, jwt, loc string, orgID uuid.UUID, s openapi.SessionItem) string {
	summary, err := b.arena.SessionSummary(ctx, jwt, orgID, s.Id)
	if err != nil {
		b.logger.Warn("eventbot: session summary failed", slog.String("session_id", s.Id.String()), slog.String("error", err.Error()))
		when := FormatWhen(s.StartAt, "")
		if string(s.Status) == "cancelled" {
			when += " (" + b.texts.T(loc, "bot.session_cancelled", nil) + ")"
		}
		return "<b>" + Esc(when) + "</b>"
	}
	tz := ""
	if summary.Session.VenueTimezone != nil {
		tz = *summary.Session.VenueTimezone
	}
	when := FormatWhen(summary.Session.StartAt, tz)
	if summary.Session.Status == "cancelled" {
		when += " (" + b.texts.T(loc, "bot.session_cancelled", nil) + ")"
	}
	venue := ""
	if summary.Session.VenueName != nil {
		venue = Esc(*summary.Session.VenueName)
	}
	places := SummaryPlaces(summary)
	// Once a ticket has been cancelled or refunded "sold 2" is not the whole
	// story: say how many were issued, how many came back and how many are
	// still valid.
	ticketsLine := ""
	if c := summary.Tickets.Cancelled; c > 0 {
		ticketsLine = b.texts.T(loc, "bot.session_tickets_line", map[string]any{
			"Issued":    summary.Tickets.Active + summary.Tickets.Cancelled + summary.Tickets.Transferred,
			"Cancelled": c,
			"Active":    summary.Tickets.Active,
		})
	}
	return b.texts.T(loc, "bot.session_line", map[string]any{
		"Tickets":   ticketsLine,
		"When":      Esc(when),
		"Venue":     venue,
		"Sold":      places.Sold,
		"Total":     places.Total,
		"Available": places.Available,
		"Held":      places.Held,
		"Money":     b.moneyText(loc, summary),
	})
}

// PlaceTotals is the seat and GA counts of a session added together.
type PlaceTotals struct {
	Sold, Total, Available, Held int64
}

// SummaryPlaces adds up the summary's GA and seated places. Sold includes
// tickets sold upstream (in the system a session was imported from).
func SummaryPlaces(s openapi.SessionSummary) PlaceTotals {
	var t PlaceTotals
	for _, c := range []openapi.SessionPlaceCounts{s.Places.Ga, s.Places.Seats} {
		t.Sold += c.Sold + c.SoldUpstream
		t.Total += c.Total
		t.Available += c.Available
		t.Held += c.Held
	}
	return t
}

func (b *Bot) moneyText(loc string, s openapi.SessionSummary) string {
	var parts []string
	for _, m := range s.Money {
		if m.PaidOrders == 0 && m.Paid == 0 {
			continue
		}
		parts = append(parts, b.texts.T(loc, "bot.money_line", map[string]any{
			"Paid":   FormatMoney(m.Paid, m.Currency, loc),
			"Orders": m.PaidOrders,
		}))
	}
	if len(parts) == 0 {
		return b.texts.T(loc, "bot.money_none", nil)
	}
	return strings.Join(parts, "; ")
}

// replyAPIError turns an arena-api failure into a message: a 403/404 on an
// organization the account used to belong to means the access was removed.
func (b *Bot) replyAPIError(ctx context.Context, chatID int64, editMsgID *int, id *Identity, err error) {
	loc := id.Locale()
	if IsAPIError(err, http.StatusForbidden) || IsAPIError(err, http.StatusNotFound) {
		b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.access_lost", nil), nil)
		return
	}
	b.logger.Error("eventbot: arena-api call failed", slog.Int64("telegram_user_id", id.Link.TelegramUserID), slog.String("error", err.Error()))
	b.reply(ctx, chatID, editMsgID, b.texts.T(loc, "bot.error_generic", nil), b.backKeyboard(loc, "home"))
}
