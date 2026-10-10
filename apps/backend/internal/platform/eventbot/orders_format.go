package eventbot

// orders_format.go — the pure parts of the Orders screens (spec 35 EC-04,
// EC-05): the one-glyph state of an order, the row of the list, the money of
// the card (struck through for a fully refunded order), the phone number as a
// call and a WhatsApp link ONLY when it is valid E.164, the plain-language
// reason an order is unpaid, and the paging arithmetic of a server-paged list.

import (
	"strconv"
	"strings"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

// Order statuses the API reports (orders.status, plus the refund states).
const (
	orderStatusPending     = "pending_payment"
	orderStatusPaid        = "paid"
	orderStatusExpired     = "expired"
	orderStatusCancelled   = "cancelled"
	orderStatusPartRefund  = "partially_refunded"
	orderStatusRefunded    = "refunded"
	orderStatusManualCheck = "manual_review"
)

// OrderChip is the one-glyph state of an order row: ✅ paid, ⏳ waiting for
// payment, ⌛ expired, ✖ cancelled, ↩ refunded (wholly or partly), ⚠ waiting
// for an operator. An unknown status shows a neutral dot, never a wrong word.
func OrderChip(status string) string {
	switch status {
	case orderStatusPaid:
		return "✅"
	case orderStatusPending:
		return "⏳"
	case orderStatusExpired:
		return "⌛"
	case orderStatusCancelled:
		return "✖"
	case orderStatusPartRefund, orderStatusRefunded:
		return "↩"
	case orderStatusManualCheck:
		return "⚠"
	default:
		return "·"
	}
}

// orderStatusKey is the message key of a status word; "" for an unknown one.
func orderStatusKey(status string) string {
	switch status {
	case orderStatusPaid, orderStatusPending, orderStatusExpired, orderStatusCancelled,
		orderStatusPartRefund, orderStatusRefunded, orderStatusManualCheck:
		return "bot.ord.st_" + status
	}
	return ""
}

// isPaidStatus reports whether the order's money arrived (and may have been
// returned since): the states with no "unpaid reason".
func isPaidStatus(status string) bool {
	switch status {
	case orderStatusPaid, orderStatusPartRefund, orderStatusRefunded:
		return true
	}
	return false
}

// orderCancellable reports whether the API will cancel the order: only one
// still waiting for payment (ordering.Cancel's guard).
func orderCancellable(status string) bool { return status == orderStatusPending }

// shortDay is "15.10" for a moment in the zone (UTC for an empty or unknown
// one).
func shortDay(t time.Time, tz string) string {
	full := FormatWhen(t, tz)
	if len(full) >= 5 {
		return full[:5]
	}
	return full
}

// OrderRowLabel is the text of a list row's button:
// "№1000000500 · ✅ · 47 EUR · Buyer Name · 15.10". The name is cut so the
// row stays readable on a phone; a missing name is simply left out.
func OrderRowLabel(o openapi.OrderListItem, loc string) string {
	parts := []string{
		"№" + strconv.FormatInt(o.SystemId, 10),
		OrderChip(o.Status),
		FormatMoney(int64(o.Total), o.Currency, loc),
	}
	if o.BuyerName != nil {
		if name := truncate(*o.BuyerName, 18); name != "" {
			parts = append(parts, name)
		}
	}
	parts = append(parts, shortDay(o.CreatedAt, o.SessionTimezone))
	return strings.Join(parts, " · ")
}

// OrderAmountHTML is the order's total for the card header. A fully refunded
// order shows it struck through (<s>), because the buyer got the money back;
// a partly refunded one stays as it is — the status word says so.
func OrderAmountHTML(status string, total int64, currency, loc string) string {
	amount := FormatMoney(total, currency, loc)
	if status == orderStatusRefunded {
		return "<s>" + amount + "</s>"
	}
	return amount
}

// PagesFor is how many pages total rows make (at least one).
func PagesFor(total int64, size int) int {
	if size <= 0 || total <= 0 {
		return 1
	}
	return int((total + int64(size) - 1) / int64(size))
}

// ─── phone ────────────────────────────────────────────────────────────────────

// NormalizeE164 turns what a buyer typed into "+34600111222" when — and only
// when — it is a valid E.164 number: a "+" (or the international prefix 00),
// a country code that does not start with 0, and 7 to 15 digits in all.
// Spaces, dots, dashes and parentheses are ignored; any other character, or a
// number without the international prefix, is not valid — a national number
// without its country code cannot be dialled or sent to WhatsApp from here.
func NormalizeE164(raw string) (string, bool) {
	s := strings.TrimSpace(raw)
	if strings.HasPrefix(s, "00") {
		s = "+" + s[2:]
	}
	if !strings.HasPrefix(s, "+") {
		return "", false
	}
	var digits strings.Builder
	for _, r := range s[1:] {
		switch {
		case r >= '0' && r <= '9':
			digits.WriteRune(r)
		case r == ' ' || r == '.' || r == '-' || r == '(' || r == ')':
		default:
			return "", false
		}
	}
	d := digits.String()
	if len(d) < 7 || len(d) > 15 || d[0] == '0' {
		return "", false
	}
	return "+" + d, true
}

// PhoneHTML is the buyer's phone for a card: the number as typed, and — only
// for a valid E.164 number — a call link (tel:) and a WhatsApp link (wa.me).
// plain is the same text without the links, for a client that refuses them.
func PhoneHTML(raw, callLabel, waLabel string) (html, plain string) {
	shown := Esc(strings.TrimSpace(raw))
	e164, ok := NormalizeE164(raw)
	if !ok {
		return shown, shown
	}
	html = shown + " · <a href=\"tel:" + e164 + "\">" + Esc(callLabel) + "</a> · <a href=\"https://wa.me/" + strings.TrimPrefix(e164, "+") + "\">" + Esc(waLabel) + "</a>"
	return html, shown
}

// ─── unpaid reason ────────────────────────────────────────────────────────────

// unpaidReasons are the values of OrderDetail.unpaid_reason.
var unpaidReasons = map[string]bool{
	"payment_failed": true, "payment_abandoned": true, "hold_expired": true,
	"cancelled": true, "awaiting_payment": true, "manual_review": true,
}

// UnpaidReasonKey is the message key that explains in plain words why an
// order is not paid. A paid, partly or wholly refunded order has none (""). An
// unpaid one whose reason the server left empty or a newer server added
// falls back to the status, then to a neutral sentence — never to a raw code.
func UnpaidReasonKey(status, reason string) string {
	if isPaidStatus(status) {
		return ""
	}
	if unpaidReasons[reason] {
		return "bot.ord.reason_" + reason
	}
	switch status {
	case orderStatusCancelled:
		return "bot.ord.reason_cancelled"
	case orderStatusPending:
		return "bot.ord.reason_awaiting_payment"
	case orderStatusExpired:
		return "bot.ord.reason_hold_expired"
	case orderStatusManualCheck:
		return "bot.ord.reason_manual_review"
	}
	return "bot.ord.reason_unknown"
}

// channelKindKey is the message key of the sales channel in words.
func channelKindKey(kind string) string {
	switch kind {
	case "site", "hosted_page", "widget":
		return "bot.ord.channel_" + kind
	}
	return "bot.ord.channel_other"
}

// deliveryKey is the message key of the e-mail delivery state; "" when
// there is nothing to say (no letter was ever queued).
func deliveryKey(state string) string {
	switch state {
	case "sent", "pending", "failed":
		return "bot.ord.delivery_" + state
	}
	return ""
}

// ticketStatusKey is the message key of a ticket's status word.
func ticketStatusKey(status string) string {
	switch status {
	case "active", "cancelled", "transferred":
		return "bot.ord.tk_" + status
	}
	return ""
}
