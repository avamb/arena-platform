package opswatchdog

import (
	"fmt"
	"strings"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/opsalert"
)

// saleLine is one paid order rendered for the sales-feed check.
type saleLine struct {
	OrgName     string
	EventTitle  string
	OrderNumber string // orders.system_id, stringified
	TicketCount int
	Total       int64
	Currency    string
	Source      string // orders.source, mapped to a human channel label
}

// sourceLabel maps orders.source to the human "widget vs gateway" label the
// spec asks for.
func sourceLabel(source string) string {
	switch source {
	case "bil24_gateway":
		return "gateway"
	case "public_feed", "checkout_api":
		return "widget"
	case "complimentary":
		return "complimentary"
	default:
		return source
	}
}

// formatSaleMessage renders one sale as a standalone Telegram message.
func formatSaleMessage(s saleLine) string {
	return fmt.Sprintf(
		"🎟 <b>sale</b>\norg: %s\nevent: %s\norder: #%s\nchannel: %s\ntickets: %d\ntotal: %s",
		opsalert.EscapeHTML(s.OrgName),
		opsalert.EscapeHTML(s.EventTitle),
		opsalert.EscapeHTML(s.OrderNumber),
		opsalert.EscapeHTML(sourceLabel(s.Source)),
		s.TicketCount,
		opsalert.EscapeHTML(opsalert.FormatMinorUnits(s.Total, s.Currency)),
	)
}

// formatSalesDigest renders more than digestThreshold sales from one run as
// a single batched message instead of one message per order.
func formatSalesDigest(sales []saleLine) string {
	var b strings.Builder
	fmt.Fprintf(&b, "🎟 <b>%d sales</b>", len(sales))
	totals := map[string]int64{}
	tickets := 0
	for _, s := range sales {
		totals[s.Currency] += s.Total
		tickets += s.TicketCount
	}
	b.WriteString(fmt.Sprintf("\ntickets: %d", tickets))
	for cur, amt := range totals {
		b.WriteString(fmt.Sprintf("\ntotal (%s): %s", opsalert.EscapeHTML(cur), opsalert.EscapeHTML(opsalert.FormatMinorUnits(amt, cur))))
	}
	b.WriteString("\norders: ")
	nums := make([]string, 0, len(sales))
	for _, s := range sales {
		nums = append(nums, "#"+s.OrderNumber)
	}
	b.WriteString(opsalert.EscapeHTML(strings.Join(nums, ", ")))
	return b.String()
}

// formatDeadLetterMessage renders one dead-lettered job/event as a
// standalone HIGH-severity message. lastError must already be scrubbed and
// truncated by the caller.
func formatDeadLetterMessage(kind, id, jobOrEventType, lastError string) string {
	msg := fmt.Sprintf(
		"🔶 <b>HIGH</b> dead letter (%s)\nid: %s\ntype: %s",
		opsalert.EscapeHTML(kind), opsalert.EscapeHTML(id), opsalert.EscapeHTML(jobOrEventType),
	)
	if lastError != "" {
		msg += "\nlast_error: " + opsalert.EscapeHTML(lastError)
	}
	return msg
}

// formatDeadLetterDigest renders more than digestThreshold dead letters from
// one run as a single batched message.
func formatDeadLetterDigest(kind string, count int, ids []string) string {
	return fmt.Sprintf(
		"🔶 <b>HIGH</b> %d new dead letters (%s)\nids: %s",
		count, opsalert.EscapeHTML(kind), opsalert.EscapeHTML(strings.Join(ids, ", ")),
	)
}

// formatRefundMessage renders one new refund row.
func formatRefundMessage(orgName string, amount int64, currency, settlement, state string) string {
	return fmt.Sprintf(
		"💸 <b>refund</b>\norg: %s\namount: %s\nsettlement: %s\nstate: %s",
		opsalert.EscapeHTML(orgName),
		opsalert.EscapeHTML(opsalert.FormatMinorUnits(amount, currency)),
		opsalert.EscapeHTML(settlement),
		opsalert.EscapeHTML(state),
	)
}

// formatRefundDigest batches more than digestThreshold refunds.
func formatRefundDigest(count int) string {
	return fmt.Sprintf("💸 <b>%d refunds</b> recorded this run", count)
}

// formatHeartbeat renders the once-daily "still alive" digest.
func formatHeartbeat(salesByCurrency map[string]saleTotal, openAlerts int) string {
	var b strings.Builder
	b.WriteString("📋 <b>watchdog alive</b> — last 24h")
	if len(salesByCurrency) == 0 {
		b.WriteString("\nno sales")
	}
	for cur, t := range salesByCurrency {
		fmt.Fprintf(&b, "\n%s: %d orders, %s", opsalert.EscapeHTML(cur), t.Count, opsalert.EscapeHTML(opsalert.FormatMinorUnits(t.Sum, cur)))
	}
	fmt.Fprintf(&b, "\nopen alerts: %d", openAlerts)
	return b.String()
}

// saleTotal aggregates paid-order counts/sums per currency for the
// heartbeat digest.
type saleTotal struct {
	Count int
	Sum   int64
}

// formatStartupMessage renders the one-time "watchdog started" message.
func formatStartupMessage(version, commit string) string {
	msg := "🟢 <b>ops watchdog started</b>"
	if version != "" {
		msg += "\nversion: " + opsalert.EscapeHTML(version)
	}
	if commit != "" {
		msg += "\ncommit: " + opsalert.EscapeHTML(commit)
	}
	return msg
}
