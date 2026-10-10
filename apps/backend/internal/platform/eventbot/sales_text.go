package eventbot

// sales_text.go — how the figures of a summary read in a chat message. The
// event card (EC-03) and the summary screen (EC-06) show the same numbers
// from two sources — GET .../events/{id}/summary and GET .../sessions/{id}/
// summary — so both are first turned into one salesFigures and rendered by
// the functions below. Money is minor units in the API and goes through
// FormatMoney here; no buyer data appears on these screens (spec 35 §2.9).

import (
	"strings"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

const (
	// maxTierLines, maxPromoLines and maxSessionLines bound a message: a
	// Telegram text is 4096 characters at most, and an event may have dozens
	// of categories, codes and dates.
	maxTierLines    = 10
	maxPromoLines   = 6
	maxSessionLines = maxSessionsPerCard
	// maxMessageRunes is the budget of one screen's text, under Telegram's
	// 4096 with room for the entities.
	maxMessageRunes = 3800
)

// placeSum is the places of a session or an event, seats and GA added up.
type placeSum struct {
	Sold, Total, Available, Held, Unavailable int64
}

func sumPlaces(counts ...openapi.SessionPlaceCounts) placeSum {
	var p placeSum
	for _, c := range counts {
		p.Sold += c.Sold
		p.Total += c.Total
		p.Available += c.Available
		p.Held += c.Held
		p.Unavailable += c.Unavailable
	}
	return p
}

// tierFigures is one row of the category table.
type tierFigures struct {
	Name     string
	Currency string
	IsOpen   bool
	Price    int64
	Revenue  int64
	Sold     int64
	Places   int64
}

// salesFigures is what a card or a summary screen draws.
type salesFigures struct {
	Places  placeSum
	Money   []openapi.SummaryMoneyItem
	Refunds []openapi.SummaryRefundsItem
	Tickets openapi.SummaryTickets
	Entered openapi.SummaryEntered
	Comp    openapi.SummaryComplimentary
	Promos  []openapi.SummaryPromoItem
	Tiers   []tierFigures
}

func figuresFromEvent(s openapi.EventSummary) salesFigures {
	f := salesFigures{
		Places:  sumPlaces(s.Places.Ga, s.Places.Seats),
		Money:   s.Money,
		Refunds: s.Refunds,
		Tickets: s.Tickets,
		Entered: s.Entered,
		Comp:    s.Complimentary,
		Promos:  s.Promos,
	}
	for _, t := range s.Tiers {
		f.Tiers = append(f.Tiers, tierFigures{Name: t.Name, Currency: t.Currency, IsOpen: t.IsOpen,
			Price: t.PriceAmount, Revenue: t.PaidRevenue, Sold: t.Places.Sold, Places: t.Places.Total})
	}
	return f
}

func figuresFromSession(s openapi.SessionSummary) salesFigures {
	f := salesFigures{
		Places:  sumPlaces(s.Places.Ga, s.Places.Seats),
		Tickets: openapi.SummaryTickets(s.Tickets),
		Entered: s.Entered,
		Comp:    s.Complimentary,
	}
	for _, m := range s.Money {
		f.Money = append(f.Money, openapi.SummaryMoneyItem(m))
	}
	for _, r := range s.Refunds {
		f.Refunds = append(f.Refunds, openapi.SummaryRefundsItem(r))
	}
	for _, p := range s.Promos {
		f.Promos = append(f.Promos, openapi.SummaryPromoItem(p))
	}
	for _, t := range s.Tiers {
		f.Tiers = append(f.Tiers, tierFigures{Name: t.Name, Currency: t.Currency, IsOpen: t.IsOpen,
			Price: t.PriceAmount, Revenue: t.PaidRevenue, Sold: t.Places.Sold, Places: t.Places.Total})
	}
	return f
}

// refundTotals is how many refunds succeeded in a currency and for how much
// (arena-driven and outside-the-system ones both); external is the part of
// the amount a selling site returned itself.
func refundTotals(refunds []openapi.SummaryRefundsItem, currency string) (count, amount, external int64) {
	for _, r := range refunds {
		if r.Currency != currency || r.State != "succeeded" {
			continue
		}
		count += r.Refunds
		amount += r.Amount
		if r.Settlement == "external" {
			external += r.Amount
		}
	}
	return count, amount, external
}

// moneyBlock is the card's money for one currency: what was paid, then the
// refunds and the net (spec 35 §5.4: "Refunds: N for X, net Y").
func (b *Bot) moneyBlock(loc string, m openapi.SummaryMoneyItem, refunds []openapi.SummaryRefundsItem) string {
	count, _, _ := refundTotals(refunds, m.Currency)
	return b.texts.T(loc, "bot.money_line", map[string]any{
		"Paid": FormatMoney(m.Paid, m.Currency, loc), "Orders": m.PaidOrders,
	}) + "\n" + b.texts.T(loc, "bot.ec.refunds_line", map[string]any{
		"Count": count, "Amount": FormatMoney(m.Refunded, m.Currency, loc), "Net": FormatMoney(m.Net, m.Currency, loc),
	})
}

// hasMoney reports whether a currency's figures say anything.
func hasMoney(m openapi.SummaryMoneyItem) bool {
	return m.PaidOrders > 0 || m.Paid > 0 || m.Refunded > 0
}

// totalsText is the numbers block of the event card: places, money by
// currency, the door count, invitations and promo codes.
func (b *Bot) totalsText(loc string, f salesFigures) string {
	var lines []string
	lines = append(lines, b.texts.T(loc, "bot.ec.tot_places", map[string]any{
		"Sold": f.Places.Sold, "Total": f.Places.Total, "Available": f.Places.Available, "Held": f.Places.Held,
	}))
	money := 0
	for _, m := range f.Money {
		if hasMoney(m) {
			lines = append(lines, b.moneyBlock(loc, m, f.Refunds))
			money++
		}
	}
	if money == 0 {
		lines = append(lines, b.texts.T(loc, "bot.money_none", nil))
	}
	if f.Entered.Total > 0 {
		lines = append(lines, b.texts.T(loc, "bot.ec.entered_line", map[string]any{"Used": f.Entered.Used, "Total": f.Entered.Total}))
	}
	if f.Comp.Tickets > 0 {
		lines = append(lines, b.texts.T(loc, "bot.ec.comp_line", map[string]any{"N": f.Comp.Tickets}))
	}
	if len(f.Promos) > 0 {
		lines = append(lines, b.texts.T(loc, "bot.ec.promos_head", nil))
		for i, p := range f.Promos {
			if i >= maxPromoLines {
				lines = append(lines, b.texts.T(loc, "bot.ec.more", map[string]any{"N": len(f.Promos) - i}))
				break
			}
			lines = append(lines, b.texts.T(loc, "bot.ec.promo_line", map[string]any{
				"Code": Esc(p.Code), "Orders": p.Orders, "Discount": FormatMoney(p.Discount, p.Currency, loc),
			}))
		}
	}
	return strings.Join(lines, "\n")
}

// tiersText is the category table: name, list price, sold of places, revenue.
func (b *Bot) tiersText(loc string, tiers []tierFigures) string {
	if len(tiers) == 0 {
		return ""
	}
	lines := []string{b.texts.T(loc, "bot.ec.tiers_head", nil)}
	for i, t := range tiers {
		if i >= maxTierLines {
			lines = append(lines, b.texts.T(loc, "bot.ec.more", map[string]any{"N": len(tiers) - i}))
			break
		}
		closed := ""
		if !t.IsOpen {
			closed = b.texts.T(loc, "bot.ec.tier_closed", nil)
		}
		lines = append(lines, b.texts.T(loc, "bot.ec.tier_line", map[string]any{
			"Name": Esc(truncate(t.Name, 40)), "Closed": closed, "Price": FormatMoney(t.Price, t.Currency, loc),
			"Sold": t.Sold, "Places": t.Places, "Revenue": FormatMoney(t.Revenue, t.Currency, loc),
		}))
	}
	return strings.Join(lines, "\n")
}

// moneyLines is one line per currency that has figures — the short form the
// dates list of a card uses.
func (b *Bot) moneyLines(loc string, money []openapi.SummaryMoneyItem) string {
	var parts []string
	for _, m := range money {
		if hasMoney(m) {
			parts = append(parts, b.texts.T(loc, "bot.money_line", map[string]any{
				"Paid": FormatMoney(m.Paid, m.Currency, loc), "Orders": m.PaidOrders,
			}))
		}
	}
	if len(parts) == 0 {
		return b.texts.T(loc, "bot.money_none", nil)
	}
	return strings.Join(parts, "; ")
}

// sessionWhen is "15.10.2026 20:00" in the venue's zone, with the cancelled
// note when the session is.
func (b *Bot) sessionWhen(loc string, s openapi.EventSummarySession) string {
	tz := ""
	if s.VenueTimezone != nil {
		tz = *s.VenueTimezone
	}
	when := FormatWhen(s.StartAt, tz)
	if s.Status == "cancelled" {
		when += " (" + b.texts.T(loc, "bot.session_cancelled", nil) + ")"
	}
	return when
}

func venueOf(name *string) string {
	if name == nil {
		return ""
	}
	return Esc(*name)
}

// sessionEntry is one date of an event in the dates list: when and where,
// places, tickets returned and the money.
func (b *Bot) sessionEntry(loc string, s openapi.EventSummarySession) string {
	p := sumPlaces(s.Places.Ga, s.Places.Seats)
	ticketsLine := ""
	if c := s.Tickets.Cancelled; c > 0 {
		ticketsLine = b.texts.T(loc, "bot.session_tickets_line", map[string]any{
			"Issued": s.Tickets.Active + c + s.Tickets.Transferred, "Cancelled": c, "Active": s.Tickets.Active,
		})
	}
	return b.texts.T(loc, "bot.session_line", map[string]any{
		"Tickets": ticketsLine, "When": Esc(b.sessionWhen(loc, s)), "Venue": venueOf(s.VenueName),
		"Sold": p.Sold, "Total": p.Total, "Available": p.Available, "Held": p.Held,
		"Money": b.moneyLines(loc, s.Money),
	})
}

// shortWhen is the date and time of a session for a button label.
func shortWhen(s openapi.EventSummarySession) string {
	tz := ""
	if s.VenueTimezone != nil {
		tz = *s.VenueTimezone
	}
	full := FormatWhen(s.StartAt, tz)
	// "15.10.2026 20:00" -> "15.10 20:00"
	if len(full) >= 16 {
		return full[:5] + full[10:]
	}
	return full
}

// clipMessage keeps a screen's text under the Telegram limit, cutting at a
// line end (every tag the screens use opens and closes on one line, so the
// cut cannot leave one open).
func clipMessage(s string, maxRunes int) string {
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	cut := string(r[:maxRunes])
	if i := strings.LastIndex(cut, "\n"); i > 0 {
		cut = cut[:i]
	}
	return cut + "\n…"
}
