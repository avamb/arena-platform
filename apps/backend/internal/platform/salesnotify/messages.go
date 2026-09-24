package salesnotify

import (
	"fmt"
	"strings"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/opsalert"
)

// Sale is one paid order, as the notification shows it. It never carries
// the buyer's name, e-mail or phone.
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

func whenWhere(start time.Time, tz, venue string) string {
	s := sessionTime(start, tz)
	if venue != "" {
		s += " · " + venue
	}
	return s
}

// FormatSale renders one sale.
func FormatSale(s Sale) string {
	var b strings.Builder
	fmt.Fprintf(&b, "🎟 <b>New sale</b> · %s\n", esc(s.OrgName))
	fmt.Fprintf(&b, "<b>%s</b>\n", esc(s.EventName))
	fmt.Fprintf(&b, "%s\n", esc(whenWhere(s.StartAt, s.TimeZone, s.VenueName)))
	src := sourceLabel(s.Source)
	if s.ChannelName != "" {
		src += " · " + s.ChannelName
	}
	fmt.Fprintf(&b, "Order #%d · %s\n", s.OrderNumber, esc(src))
	parts := make([]string, 0, len(s.Categories))
	for _, c := range s.Categories {
		parts = append(parts, fmt.Sprintf("%s × %d", c.Name, c.Count))
	}
	fmt.Fprintf(&b, "Tickets: %d", s.Tickets())
	if len(parts) > 0 {
		fmt.Fprintf(&b, " (%s)", esc(strings.Join(parts, ", ")))
	}
	b.WriteString("\n")
	if s.PromoCode != "" {
		fmt.Fprintf(&b, "Promo code: %s\n", esc(s.PromoCode))
	}
	fmt.Fprintf(&b, "Total: <b>%s</b>", esc(money(s.Total, s.Currency)))
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
	fmt.Fprintf(&b, "↩️ <b>%s</b> · %s\n", title, esc(r.OrgName))
	fmt.Fprintf(&b, "<b>%s</b>\n", esc(r.EventName))
	fmt.Fprintf(&b, "%s\n", esc(whenWhere(r.StartAt, r.TimeZone, r.VenueName)))
	if r.OrderNumber != 0 {
		fmt.Fprintf(&b, "Order #%d · ticket #%d\n", r.OrderNumber, r.TicketNumber)
	} else {
		fmt.Fprintf(&b, "Ticket #%d\n", r.TicketNumber)
	}
	if r.Amount == 0 {
		b.WriteString("No money returned")
	} else {
		fmt.Fprintf(&b, "Amount: <b>%s</b>", esc(money(r.Amount, r.Currency)))
	}
	return b.String()
}
