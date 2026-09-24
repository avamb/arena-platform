package salesnotify

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/opsalert"
)

// Sale is one paid order, as the notification shows it. It never carries
// the buyer's name, e-mail or phone.
type Sale struct {
	ID          string // orders.id — cursor tiebreak
	At          time.Time
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

// Refund is one succeeded refund.
type Refund struct {
	ID           string
	At           time.Time
	OrgID        string
	OrgName      string
	EventName    string
	VenueName    string
	StartAt      *time.Time
	TimeZone     string
	OrderNumber  int64 // 0 = unknown
	TicketNumber int64 // 0 = the whole order / unknown
	Currency     string
	Amount       int64
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

// humanTimeLayout is how a session start reads in a chat message.
const humanTimeLayout = "Mon 02 Jan 2006, 15:04"

func esc(s string) string { return opsalert.EscapeHTML(s) }

func money(amount int64, currency string) string {
	return opsalert.FormatMinorUnits(amount, strings.TrimSpace(currency))
}

// FormatSale renders one sale.
func FormatSale(s Sale) string {
	var b strings.Builder
	fmt.Fprintf(&b, "🎟 <b>New sale</b> · %s\n", esc(s.OrgName))
	fmt.Fprintf(&b, "<b>%s</b>\n", esc(s.EventName))
	when := sessionTime(s.StartAt, s.TimeZone)
	if s.VenueName != "" {
		when += " · " + s.VenueName
	}
	fmt.Fprintf(&b, "%s\n", esc(when))
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

// FormatRefund renders one refund.
func FormatRefund(r Refund) string {
	var b strings.Builder
	fmt.Fprintf(&b, "↩️ <b>Refund</b> · %s\n", esc(r.OrgName))
	if r.EventName != "" {
		fmt.Fprintf(&b, "<b>%s</b>\n", esc(r.EventName))
	}
	if r.StartAt != nil {
		when := sessionTime(*r.StartAt, r.TimeZone)
		if r.VenueName != "" {
			when += " · " + r.VenueName
		}
		fmt.Fprintf(&b, "%s\n", esc(when))
	}
	switch {
	case r.OrderNumber != 0 && r.TicketNumber != 0:
		fmt.Fprintf(&b, "Order #%d · ticket #%d\n", r.OrderNumber, r.TicketNumber)
	case r.OrderNumber != 0:
		fmt.Fprintf(&b, "Order #%d\n", r.OrderNumber)
	}
	fmt.Fprintf(&b, "Amount: <b>%s</b>", esc(money(r.Amount, r.Currency)))
	return b.String()
}

// FormatDigest summarises a burst for one chat in a single message.
func FormatDigest(sales []Sale, refunds []Refund) string {
	var b strings.Builder
	if len(sales) > 0 {
		tickets := 0
		totals := map[string]int64{}
		for _, s := range sales {
			tickets += s.Tickets()
			totals[strings.TrimSpace(s.Currency)] += s.Total
		}
		fmt.Fprintf(&b, "🎟 <b>%d new sales</b>, %d tickets", len(sales), tickets)
		for _, cur := range sortedKeys(totals) {
			fmt.Fprintf(&b, "\nTotal: <b>%s</b>", esc(money(totals[cur], cur)))
		}
		nums := make([]string, 0, len(sales))
		for _, s := range sales {
			nums = append(nums, fmt.Sprintf("#%d", s.OrderNumber))
		}
		fmt.Fprintf(&b, "\nOrders: %s", strings.Join(nums, ", "))
	}
	if len(refunds) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		totals := map[string]int64{}
		for _, r := range refunds {
			totals[strings.TrimSpace(r.Currency)] += r.Amount
		}
		fmt.Fprintf(&b, "↩️ <b>%d refunds</b>", len(refunds))
		for _, cur := range sortedKeys(totals) {
			fmt.Fprintf(&b, "\nAmount: <b>%s</b>", esc(money(totals[cur], cur)))
		}
	}
	return b.String()
}

func sortedKeys(m map[string]int64) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
