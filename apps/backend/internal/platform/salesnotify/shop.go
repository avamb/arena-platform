package salesnotify

// shop.go — the merchandise shop's announcements (spec 47 section 7, lane L5).
//
// A paid shop order (v1.shop.order.paid) and a refund of one
// (v1.shop.order.refunded) go to the chats whose subscription kinds include
// "shop": normally the organizer's logistics group, plus any operator chat
// that listens to shop sales. The message is the courier's form: the buyer,
// then the delivery details in the order shop.CourierFields prints them, then
// the goods, the shipping fee and the buyer's note, so the logistician can
// copy it straight into the courier's order form. Buyer name, phone and
// e-mail are shown on purpose (owner decision 2026-09-25: the organizer must
// be able to reach the buyer).
//
// Each announcement is claimed once in sales_notification_deliveries
// (shop_paid:<order id>, shop_refund:<order id>:<refunded so far>) because the
// outbox fan-out re-runs every leg when any other leg fails.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/outbox"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/shop"
)

// maxShopItemLines keeps a very large order inside Telegram's 4096-character
// limit: the rest is summarised in one line.
const maxShopItemLines = 30

// shopMessageBudget is how many characters the goods list may grow the
// message to: Telegram refuses a message over 4096 characters, and the
// rest is kept free for the closing lines and for the markup of the text.
// maxShopItemName cuts one very long product title.
const (
	shopMessageBudget = 3800
	maxShopItemName   = 120
)

// ShopOrder is one shop order as the announcement shows it.
type ShopOrder struct {
	OrgName string
	// Locale is the organization's language (organizations.default_locale);
	// the texts of the announcement and the country name follow it, "" is en.
	Locale string
	Order  gen.ShopOrderRow
	Items  []gen.ShopOrderItemRow
}

// shopMoney is an amount in the message's language: "450,00 ₪" in ru.
func shopMoney(amount int64, currency, locale string) string {
	return esc(formatShopMoney(amount, currency, locale))
}

// writeShopHeader renders the line both messages start with: the order.
func writeShopHeader(b *strings.Builder, loc string, o gen.ShopOrderRow) {
	fmt.Fprintf(b, "%s\n", say(loc, "salesnotify.shop.order_number", map[string]any{"Number": o.Number}))
}

// writeShopBuyer renders the buyer's name, phone and e-mail.
func writeShopBuyer(b *strings.Builder, o gen.ShopOrderRow) {
	if n := strings.TrimSpace(o.BuyerName); n != "" {
		fmt.Fprintf(b, "👤 %s\n", esc(n))
	}
	if p := strings.TrimSpace(o.BuyerPhone); p != "" {
		fmt.Fprintf(b, "📞 %s\n", esc(p))
	}
	if e := strings.TrimSpace(o.BuyerEmail); e != "" {
		fmt.Fprintf(b, "✉️ %s\n", esc(e))
	}
}

// writeCourierForm prints the courier form field by field in the courier
// form's order, then the two map links built from the full address. The
// country is named in the message's language.
func writeCourierForm(b *strings.Builder, loc string, o gen.ShopOrderRow) {
	c := shop.CourierFieldsOfIn(o, loc)
	lines := c.Lines()
	if len(lines) == 0 {
		return
	}
	fmt.Fprintf(b, "\n%s\n", say(loc, "salesnotify.shop.courier_form", nil))
	for _, l := range lines {
		fmt.Fprintf(b, "%s\n", esc(l))
	}
	if o.DeliveryKind == "pickup" || !c.HasAddress() {
		return
	}
	q := c.MapQuery()
	fmt.Fprintf(b, "\n%s\n", say(loc, "salesnotify.shop.map_google", map[string]any{"Url": esc(shop.GoogleMapsURL(q))}))
	fmt.Fprintf(b, "%s\n", say(loc, "salesnotify.shop.map_waze", map[string]any{"Url": esc(shop.WazeURL(q))}))
}

// FormatShopOrderPaid renders a paid shop order for the logistics chat, in
// the organization's language (s.Locale).
func FormatShopOrderPaid(s ShopOrder) string {
	o := s.Order
	loc := s.Locale
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", say(loc, "salesnotify.shop.new_order", map[string]any{"Org": esc(s.OrgName)}))
	writeShopHeader(&b, loc, o)
	fmt.Fprintf(&b, "%s\n", say(loc, "salesnotify.shop.total", map[string]any{"Amount": shopMoney(o.Total, o.Currency, loc)}))
	writeShopBuyer(&b, o)
	switch o.DeliveryKind {
	case "none":
		// A file or a service: nothing travels, so no courier form either.
		fmt.Fprintf(&b, "%s\n", say(loc, "salesnotify.shop.delivery_none", nil))
	case "pickup":
		fmt.Fprintf(&b, "%s\n", say(loc, "salesnotify.shop.delivery_pickup", nil))
		writeCourierForm(&b, loc, o)
	default:
		fmt.Fprintf(&b, "%s\n", say(loc, "salesnotify.shop.delivery_shipping", nil))
		writeCourierForm(&b, loc, o)
	}

	if len(s.Items) > 0 {
		fmt.Fprintf(&b, "\n%s\n", say(loc, "salesnotify.shop.items", nil))
	}
	// The closing lines are composed first, so the goods can be cut to what
	// is left of Telegram's 4096 characters (SA-37): an order of many
	// long-named lines used to be refused by Telegram and never announced.
	var tail strings.Builder
	if o.ShippingFee > 0 {
		fmt.Fprintf(&tail, "%s\n", say(loc, "salesnotify.shop.shipping", map[string]any{"Amount": shopMoney(o.ShippingFee, o.Currency, loc)}))
	}
	if o.Discount > 0 {
		fmt.Fprintf(&tail, "%s\n", say(loc, "salesnotify.shop.discount", map[string]any{"Amount": shopMoney(o.Discount, o.Currency, loc)}))
	}
	if note := strings.TrimSpace(o.DeliveryNote); note != "" {
		fmt.Fprintf(&tail, "\n%s\n", say(loc, "salesnotify.shop.note", map[string]any{"Note": esc(clip(note, 600))}))
	}
	used := utf8.RuneCountInString(b.String()) + utf8.RuneCountInString(tail.String())
	for i, it := range s.Items {
		line := say(loc, "salesnotify.shop.item_line", map[string]any{
			"Qty": it.Qty, "Name": esc(clip(shopItemName(it), maxShopItemName)), "Price": shopMoney(it.UnitPriceMinor, o.Currency, loc),
		}) + "\n"
		more := ""
		if i < len(s.Items)-1 {
			more = say(loc, "salesnotify.shop.items_more", map[string]any{"Count": len(s.Items) - i - 1}) + "\n"
		}
		if i == maxShopItemLines || used+utf8.RuneCountInString(line)+utf8.RuneCountInString(more) > shopMessageBudget {
			fmt.Fprintf(&b, "%s\n", say(loc, "salesnotify.shop.items_more", map[string]any{"Count": len(s.Items) - i}))
			break
		}
		b.WriteString(line)
		used += utf8.RuneCountInString(line)
	}
	b.WriteString(tail.String())
	return strings.TrimRight(b.String(), "\n")
}

// FormatShopOrderRefund renders money returned on a shop order. refundedNow
// is the amount of this refund, 0 when the event did not say.
func FormatShopOrderRefund(s ShopOrder, refundedNow int64) string {
	o := s.Order
	loc := s.Locale
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", say(loc, "salesnotify.shop.refund_title", map[string]any{"Org": esc(s.OrgName)}))
	writeShopHeader(&b, loc, o)
	if refundedNow > 0 {
		fmt.Fprintf(&b, "%s\n", say(loc, "salesnotify.shop.refunded", map[string]any{"Amount": shopMoney(refundedNow, o.Currency, loc)}))
	}
	fmt.Fprintf(&b, "%s\n", say(loc, "salesnotify.shop.refunded_so_far", map[string]any{
		"Amount": shopMoney(o.RefundedMinor, o.Currency, loc), "Total": shopMoney(o.Total, o.Currency, loc),
	}))
	writeShopBuyer(&b, o)
	return strings.TrimRight(b.String(), "\n")
}

// FormatShopOrderReview renders the notice for a payment that arrived after
// the order's hold ended and found its goods gone. The money is taken and the
// order is parked: the organizer decides (ship late, or refund). The operator
// gets their own ops alert from the watchdog, not this one.
func FormatShopOrderReview(s ShopOrder) string {
	o := s.Order
	loc := s.Locale
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", say(loc, "salesnotify.shop.review_title", map[string]any{"Org": esc(s.OrgName)}))
	writeShopHeader(&b, loc, o)
	fmt.Fprintf(&b, "%s\n", say(loc, "salesnotify.shop.review_paid", map[string]any{"Amount": shopMoney(o.Total, o.Currency, loc)}))
	writeShopBuyer(&b, o)
	fmt.Fprintf(&b, "\n%s", say(loc, "salesnotify.shop.review_body", map[string]any{"Number": o.Number}))
	return b.String()
}

func shopItemName(it gen.ShopOrderItemRow) string { return shop.ItemLabel(it) }

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// composeShop loads the order a shop event names and renders the message.
// None of the shop messages carries the events-bot footer: the logistics chat
// is a courier's, not an organizer's, and "create your events from Telegram"
// is noise there. Ticket sales keep it (salesnotify.go).
func (d *Dispatcher) composeShop(ctx context.Context, ev outbox.Event) (key string, trigger Trigger, text, orgID string, err error) {
	id := payloadString(ev.Payload, "order_id", ev.AggregateID)
	orgID = payloadString(ev.Payload, "org_id", "")
	order, err := d.store.ShopOrder(ctx, orgID, id)
	if err != nil {
		return "", 0, "", "", err
	}
	if ev.EventType == EventShopOrderManualReview {
		return "shop_review:" + id, TriggerShopOrderReview, FormatShopOrderReview(order), orgID, nil
	}
	if ev.EventType == EventShopOrderPaid {
		return "shop_paid:" + id, TriggerShopOrderPaid, FormatShopOrderPaid(order), orgID, nil
	}
	// The amount "refunded so far" is part of the key: two refunds of one
	// order are two announcements, a retry of one is not.
	so := order.Order.RefundedMinor
	if v, ok := payloadInt(ev.Payload, "refunded_minor"); ok {
		so = v
	}
	now, _ := payloadInt(ev.Payload, "refunded_now")
	key = fmt.Sprintf("shop_refund:%s:%d", id, so)
	return key, TriggerShopOrderRefunded, FormatShopOrderRefund(order, now), orgID, nil
}

// ShopOrder implements Store for the shop. An order of another organization
// or one that does not exist is ErrNotFound.
func (s *PGStore) ShopOrder(ctx context.Context, orgID, orderID string) (ShopOrder, error) {
	oid, err1 := uuid.Parse(orderID)
	org, err2 := uuid.Parse(orgID)
	if err1 != nil || err2 != nil {
		return ShopOrder{}, ErrNotFound
	}
	q := gen.New(s.pool)
	row, err := q.GetShopOrder(ctx, oid, org)
	if errors.Is(err, pgx.ErrNoRows) {
		return ShopOrder{}, ErrNotFound
	}
	if err != nil {
		return ShopOrder{}, err
	}
	items, err := q.ListShopOrderItems(ctx, []uuid.UUID{oid})
	if err != nil {
		return ShopOrder{}, err
	}
	var name, locale string
	if err := s.pool.QueryRow(ctx, `SELECT name, COALESCE(default_locale, '') FROM organizations WHERE id = $1`, org).Scan(&name, &locale); err != nil {
		return ShopOrder{}, err
	}
	return ShopOrder{OrgName: name, Locale: locale, Order: row, Items: items}, nil
}
