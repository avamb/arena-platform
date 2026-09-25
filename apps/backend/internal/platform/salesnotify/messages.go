package salesnotify

import (
	"fmt"
	"strings"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/opsalert"
)

// Buyer is who bought, as the order recorded it. Organizers see it in their
// own group so they can reach the buyer (owner decision 2026-09-25); any
// field may be empty.
type Buyer struct {
	Name  string
	Email string
	Phone string
}

// Sale is one paid order, as the notification shows it.
type Sale struct {
	OrgID       string
	OrgName     string
	EventName   string
	VenueName   string
	StartAt     time.Time
	TimeZone    string
	OrderNumber int64
	Source      string
	ChannelName string
	Currency    string
	Total       int64
	PromoCode   string
	Categories  []CategoryCount
	Buyer       Buyer
}

// CategoryCount is how many units of one category an order bought.
type CategoryCount struct {
	Name  string `json:"name"`
	Count int    `json:"n"`
}

// Tickets is the number of units in the order.
func (s Sale) Tickets() int {
	n := 0
	for _, c := range s.Categories {
		n += c.Count
	}
	return n
}

// Refund is one refunded or cancelled ticket.
type Refund struct {
	OrgID        string
	OrgName      string
	EventName    string
	VenueName    string
	StartAt      time.Time
	TimeZone     string
	OrderNumber  int64 // 0 = unknown
	TicketNumber int64
	Currency     string
	Amount       int64 // 0 = cancelled without money back
	Buyer        Buyer
}

// sourceLabel names where the sale came from.
func sourceLabel(source string) string {
	switch source {
	case "bil24_gateway":
		return "website"
	case "public_feed", "checkout_api":
		return "widget"
	case "complimentary":
		return "invitation"
	default:
		return source
	}
}

// humanTimeLayout is how a session start reads in a chat message.
const humanTimeLayout = "Mon 02 Jan 2006, 15:04"

// sessionTime renders the session start in the venue's own zone, the way
// the buyer's ticket shows it; an unknown zone falls back to UTC, labelled.
func sessionTime(t time.Time, tz string) string {
	if tz != "" {
		if loc, err := time.LoadLocation(tz); err == nil {
			// allow:timeformat: human-readable Telegram text, not a wire timestamp
			return t.In(loc).Format(humanTimeLayout)
		}
	}
	// allow:timeformat: human-readable Telegram text, not a wire timestamp
	return t.UTC().Format(humanTimeLayout) + " UTC"
}

func esc(s string) string { return opsalert.EscapeHTML(s) }

func money(amount int64, currency string) string {
	return opsalert.FormatMinorUnits(amount, strings.TrimSpace(currency))
}

// writeEvent renders the event block: name, then when and where on lines
// of their own.
func writeEvent(b *strings.Builder, name string, start time.Time, tz, venue string) {
	fmt.Fprintf(b, "<b>%s</b>\n", esc(name))
	fmt.Fprintf(b, "📅 %s\n", esc(sessionTime(start, tz)))
	if venue != "" {
		fmt.Fprintf(b, "📍 %s\n", esc(venue))
	}
}

// writeBuyer renders the buyer block, preceded by a blank line; nothing when
// the order recorded no contact at all.
func writeBuyer(b *strings.Builder, buyer Buyer) {
	if buyer.Name == "" && buyer.Email == "" && buyer.Phone == "" {
		return
	}
	b.WriteString("\n")
	if buyer.Name != "" {
		fmt.Fprintf(b, "\n👤 %s", esc(buyer.Name))
	}
	if buyer.Email != "" {
		fmt.Fprintf(b, "\n✉️ %s", esc(buyer.Email))
	}
	if buyer.Phone != "" {
		fmt.Fprintf(b, "\n📞 %s", esc(buyer.Phone))
	}
}

// FormatSale renders one sale.
func FormatSale(s Sale) string {
	var b strings.Builder
	fmt.Fprintf(&b, "🎟 <b>New sale</b> — %s\n\n", esc(s.OrgName))
	writeEvent(&b, s.EventName, s.StartAt, s.TimeZone, s.VenueName)
	src := sourceLabel(s.Source)
	if s.ChannelName != "" {
		src += " (" + s.ChannelName + ")"
	}
	fmt.Fprintf(&b, "\n🧾 Order #%d · %s\n", s.OrderNumber, esc(src))
	if len(s.Categories) == 0 {
		fmt.Fprintf(&b, "🎫 %d ticket(s)\n", s.Tickets())
	}
	for _, c := range s.Categories {
		fmt.Fprintf(&b, "🎫 %d × %s\n", c.Count, esc(c.Name))
	}
	if s.PromoCode != "" {
		fmt.Fprintf(&b, "🏷 Promo code: <code>%s</code>\n", esc(s.PromoCode))
	}
	fmt.Fprintf(&b, "💰 Total: <b>%s</b>", esc(money(s.Total, s.Currency)))
	writeBuyer(&b, s.Buyer)
	return b.String()
}

// FormatRefund renders one refunded ticket; one cancelled without money
// back says so instead of showing a zero amount.
func FormatRefund(r Refund) string {
	var b strings.Builder
	title := "Refund"
	if r.Amount == 0 {
		title = "Ticket cancelled"
	}
	fmt.Fprintf(&b, "↩️ <b>%s</b> — %s\n\n", title, esc(r.OrgName))
	writeEvent(&b, r.EventName, r.StartAt, r.TimeZone, r.VenueName)
	if r.OrderNumber != 0 {
		fmt.Fprintf(&b, "\n🧾 Order #%d · ticket #%d\n", r.OrderNumber, r.TicketNumber)
	} else {
		fmt.Fprintf(&b, "\n🧾 Ticket #%d\n", r.TicketNumber)
	}
	if r.Amount == 0 {
		b.WriteString("💸 No money returned")
	} else {
		fmt.Fprintf(&b, "💸 Refunded: <b>%s</b>", esc(money(r.Amount, r.Currency)))
	}
	writeBuyer(&b, r.Buyer)
	return b.String()
}
