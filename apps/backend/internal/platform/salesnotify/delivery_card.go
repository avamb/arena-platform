package salesnotify

// delivery_card.go — the delivery group's card of a paid shop order with its
// step buttons (spec 08_architecture/61 section 7b.2).
//
// The card is the courier form of FormatShopOrderPaid plus one status line
// (the current step, who set it and when, in the organization's time zone) and
// the buttons of the next steps. arena-worker sends it when the order is paid
// and remembers the message per chat (sales_notification_cards); whenever the
// step changes, from ANY surface (v1.shop.order.fulfillment_changed), or the
// order is refunded, the worker edits every copy in place, so the group always
// sees the current step and its feed is not cluttered. The sales bot in
// arena-bot receives the presses (package salesbot) and edits the pressed card
// at once from the answer of POST /v1/bot/shop-delivery/steps, which renders
// it with this same code.
//
// Callback data (at most 64 bytes, Telegram's limit): "sd:<op>:<lang>:<order
// uuid>", 45 bytes at most. op is p (packed), h (handed over: ask for the
// number), hn (handed over without a number), d (delivered), f (not delivered:
// show the reasons), r<c> (a reason: rn no answer, ra not home, rw wrong
// address, rr refused, ro other: ask for the text), b (back to the step
// buttons). lang is the organization's language the card was written in, so
// the bot asks its questions in the same language without a lookup.

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/shop"
)

// Button is one inline button: its label and its callback data.
type Button struct {
	Text string `json:"text"`
	Data string `json:"data"`
}

// DeliveryCard is the text of a delivery card (Telegram HTML) and its button
// rows (empty when nothing can be pressed any more).
type DeliveryCard struct {
	Text string     `json:"text"`
	Rows [][]Button `json:"rows"`
}

// CardState is everything a card shows.
type CardState struct {
	Shop ShopOrder
	// Last is the newest step change (nil when the order never moved).
	Last *gen.ShopOrderEventRow
	// Zone is the organization's time zone for the "· 14:20" of the status
	// line (nil = UTC); Now decides whether the date is printed too.
	Zone *time.Location
	Now  time.Time
}

// Card actions (the op of the callback data).
const (
	OpPacked          = "p"
	OpHandedOver      = "h"
	OpHandedOverPlain = "hn"
	OpDelivered       = "d"
	OpFailed          = "f"
	OpBack            = "b"
	// OpReasonPrefix + a reason letter (reasonCodes).
	OpReasonPrefix = "r"
	// OpReasonOther asks for the reason as text.
	OpReasonOther = "ro"
)

// cardActionPrefix starts every delivery-card callback.
const cardActionPrefix = "sd:"

// reasonCodes maps the letter of a reason button to the stored reason.
var reasonCodes = map[string]string{
	"n": shop.ReasonNoAnswer,
	"a": shop.ReasonNotHome,
	"w": shop.ReasonWrongAddress,
	"r": shop.ReasonRefused,
}

// reasonOrder is the order of the reason buttons (V-18), "o" (other) last.
var reasonOrder = []string{"n", "a", "w", "r"}

// stepOps maps a step to the op of its button.
var stepOps = map[string]string{
	shop.StepPacked: OpPacked, shop.StepHandedOver: OpHandedOver, shop.StepDelivered: OpDelivered, shop.StepFailed: OpFailed,
}

// CardAction is a decoded press of a card button.
type CardAction struct {
	Op      string
	Locale  string
	OrderID uuid.UUID
}

// EncodeCardAction builds the callback data of a card button.
func EncodeCardAction(op, locale string, orderID uuid.UUID) string {
	return cardActionPrefix + op + ":" + shopLocale(locale) + ":" + orderID.String()
}

// DecodeCardAction reads the callback data of a card button; ok is false for
// anything else.
func DecodeCardAction(data string) (CardAction, bool) {
	rest, ok := strings.CutPrefix(data, cardActionPrefix)
	if !ok {
		return CardAction{}, false
	}
	parts := strings.Split(rest, ":")
	if len(parts) != 3 || parts[0] == "" || len(parts[1]) < 2 || len(parts[1]) > 3 {
		return CardAction{}, false
	}
	id, err := uuid.Parse(parts[2])
	if err != nil {
		return CardAction{}, false
	}
	switch op := parts[0]; {
	case op == OpPacked, op == OpHandedOver, op == OpHandedOverPlain, op == OpDelivered, op == OpFailed, op == OpBack, op == OpReasonOther:
	case strings.HasPrefix(op, OpReasonPrefix) && reasonCodes[strings.TrimPrefix(op, OpReasonPrefix)] != "":
	default:
		return CardAction{}, false
	}
	return CardAction{Op: parts[0], Locale: parts[1], OrderID: id}, true
}

// ReasonOf is the stored reason of a reason op ("" for other ops).
func ReasonOf(op string) string {
	if !strings.HasPrefix(op, OpReasonPrefix) {
		return ""
	}
	return reasonCodes[strings.TrimPrefix(op, OpReasonPrefix)]
}

// StepLabel is the button and status label of a step in the language.
func StepLabel(locale, step string) string {
	return say(locale, "salesnotify.shop.step."+step, nil)
}

// ReasonLabel is a stored reason as people read it: a code of the buttons in
// the language, any other text as it was typed (escaped for HTML).
func ReasonLabel(locale, reason string) string {
	for _, code := range reasonCodes {
		if code == reason {
			return say(locale, "salesnotify.shop.reason."+reason, nil)
		}
	}
	return esc(clip(reason, 300))
}

// StepButtons are the buttons of the steps that may follow step: the forward
// steps in one row, "not delivered" in its own.
func StepButtons(locale, step string, orderID uuid.UUID) [][]Button {
	var forward, failed []Button
	for _, next := range shop.NextSteps(step) {
		b := Button{Text: StepLabel(locale, next), Data: EncodeCardAction(stepOps[next], locale, orderID)}
		if next == shop.StepFailed {
			failed = append(failed, b)
		} else {
			forward = append(forward, b)
		}
	}
	var rows [][]Button
	if len(forward) > 0 {
		rows = append(rows, forward)
	}
	if len(failed) > 0 {
		rows = append(rows, failed)
	}
	return rows
}

// ReasonButtons are the reasons of "not delivered" (V-18), then "back".
func ReasonButtons(locale string, orderID uuid.UUID) [][]Button {
	var rows [][]Button
	for i := 0; i < len(reasonOrder); i += 2 {
		var row []Button
		for _, c := range reasonOrder[i:min(i+2, len(reasonOrder))] {
			row = append(row, Button{Text: say(locale, "salesnotify.shop.reason."+reasonCodes[c], nil),
				Data: EncodeCardAction(OpReasonPrefix+c, locale, orderID)})
		}
		rows = append(rows, row)
	}
	rows = append(rows, []Button{
		{Text: say(locale, "salesnotify.shop.reason.other", nil), Data: EncodeCardAction(OpReasonOther, locale, orderID)},
		{Text: say(locale, "salesnotify.shop.btn_back", nil), Data: EncodeCardAction(OpBack, locale, orderID)},
	})
	return rows
}

// TrackingButtons are what the card carries while the bot waits for the
// shipment number: "without a number" and "back".
func TrackingButtons(locale string, orderID uuid.UUID) [][]Button {
	return [][]Button{{
		{Text: say(locale, "salesnotify.shop.btn_no_tracking", nil), Data: EncodeCardAction(OpHandedOverPlain, locale, orderID)},
		{Text: say(locale, "salesnotify.shop.btn_back", nil), Data: EncodeCardAction(OpBack, locale, orderID)},
	}}
}

// cardTime is when a step was set, in the organization's zone: "14:20" today,
// "05.11 14:20" on another day.
func cardTime(at time.Time, st CardState) string {
	zone := st.Zone
	if zone == nil {
		zone = time.UTC
	}
	at = at.In(zone)
	now := st.Now
	if now.IsZero() {
		now = time.Now()
	}
	now = now.In(zone)
	if at.Year() == now.Year() && at.YearDay() == now.YearDay() {
		return at.Format("15:04") // allow:timeformat
	}
	return at.Format("02.01 15:04") // allow:timeformat
}

// StatusLine is the card's line about the current step: "📦 Packed · Ivan ·
// 14:20", the number of a handed-over parcel, the reason of a failed one. A
// refunded order says "do not ship" instead. "" for an order that waits and
// never moved.
func StatusLine(st CardState) string {
	o := st.Shop.Order
	loc := st.Shop.Locale
	if shop.OrderClosed(o) {
		return say(loc, "salesnotify.shop.card_closed", nil)
	}
	step := shop.CurrentStep(o)
	if st.Last == nil || st.Last.Step != step {
		if step == shop.StepWaiting || step == "" {
			return ""
		}
		return StepLabel(loc, step)
	}
	parts := []string{StepLabel(loc, step)}
	if step == shop.StepFailed && st.Last.Reason != "" {
		parts[0] += ": " + ReasonLabel(loc, st.Last.Reason)
	}
	if n := strings.TrimSpace(st.Last.ActorName); n != "" {
		parts = append(parts, esc(n))
	}
	parts = append(parts, cardTime(st.Last.CreatedAt, st))
	if step == shop.StepHandedOver {
		if t := strings.TrimSpace(o.TrackingNote); t != "" {
			parts = append(parts, say(loc, "salesnotify.shop.step_tracking", map[string]any{"Tracking": esc(clip(t, 200))}))
		}
	}
	return strings.Join(parts, " · ")
}

// RenderDeliveryCard is the card as the delivery group sees it now: the
// courier form, the status line and the buttons of the next steps (none for a
// refunded, cancelled or delivered order).
func RenderDeliveryCard(st CardState) DeliveryCard {
	text := FormatShopOrderPaid(st.Shop)
	if line := StatusLine(st); line != "" {
		text += "\n\n" + line
	}
	o := st.Shop.Order
	card := DeliveryCard{Text: text, Rows: [][]Button{}}
	if !shop.OrderClosed(o) && o.Status != shop.StatusPendingPayment && o.Status != shop.StatusManualReview {
		if rows := StepButtons(st.Shop.Locale, shop.CurrentStep(o), o.ID); rows != nil {
			card.Rows = rows
		}
	}
	return card
}

// FormatShopDeliveryFailed is what a sales chat hears about a parcel that did
// not arrive (spec 61 section 7b.4): the order number and the reason, no buyer
// data. The organizer decides what next (another delivery or a refund).
func FormatShopDeliveryFailed(s ShopOrder, reason string) string {
	o := s.Order
	loc := s.Locale
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", say(loc, "salesnotify.shop.failed_title", map[string]any{"Org": esc(s.OrgName)}))
	b.WriteString(say(loc, "salesnotify.shop.failed_body", map[string]any{"Number": o.Number, "Reason": ReasonLabel(loc, reason)}))
	return b.String()
}
