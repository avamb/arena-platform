package salesnotify

// shop.go — the merchandise shop's announcements (spec 47 section 7, spec 61
// section 7a).
//
// A paid shop order (v1.shop.order.paid), a refund of one
// (v1.shop.order.refunded) and an order parked for review
// (v1.shop.order.manual_review) go to two kinds of chat:
//
//   - shop_delivery, the DELIVERY group: the courier's form. The buyer, then
//     the delivery details in the order shop.CourierFields prints them, then
//     the goods, the shipping fee and the buyer's note, so the logistician can
//     copy it straight into the courier's order form. Buyer name, phone and
//     e-mail are shown on purpose (owner decision 2026-09-25: the organizer
//     must be able to reach the buyer). An order with nothing to deliver
//     (delivery_kind none) and an order that is not paid yet are not sent
//     there: a refund says "do not ship" or "already shipped".
//   - shop_sale, the SALES chat: the fact only (number, amount, goods), never
//     a name, a phone, an e-mail or an address.
//
// A chat with both kinds gets the card alone. Nothing goes out for an
// organization whose shop mode is none (orgmodules.ShopMode).
//
// Each announcement is claimed once in sales_notification_deliveries, the card
// and the fact under their own keys (shop_paid:<order id> and
// shop_sale_paid:<order id>, shop_refund:<order id>:<refunded so far> and
// shop_sale_refund:..., shop_review:<order id>:<cause> for the fact), because
// the outbox fan-out re-runs every leg when any other leg fails.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/orgmodules"
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
	// ShopCountry is the shop's own country (shop_settings.country_iso2, upper
	// case ISO 3166-1 alpha-2, "" when unset). Only the order card's courier
	// text reads it: a parcel inside that country does not print it.
	ShopCountry string
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
// country is named in the message's language. It is the ONE writer of the
// form: the Telegram announcement (HTML, with a heading, every country) and
// the order card's courier text (plain, no heading, the shop's own country
// left out) differ only in the courierStyle they pass.
func writeCourierForm(b *strings.Builder, loc string, o gen.ShopOrderRow, st courierStyle) {
	c := shop.CourierFieldsOfIn(o, loc)
	q := c.MapQuery() // the map always gets the country, whatever is printed
	if st.hidesCountry(c.CountryCode) {
		c.Country = ""
	}
	lines := c.Lines()
	if len(lines) == 0 {
		return
	}
	if st.heading {
		fmt.Fprintf(b, "\n%s\n", say(loc, "salesnotify.shop.courier_form", nil))
	}
	for _, l := range lines {
		fmt.Fprintf(b, "%s\n", st.esc(l))
	}
	if o.DeliveryKind == "pickup" || !c.HasAddress() {
		return
	}
	fmt.Fprintf(b, "\n%s\n", say(loc, st.mapKey("google"), map[string]any{"Url": st.esc(shop.GoogleMapsURL(q))}))
	fmt.Fprintf(b, "%s\n", say(loc, st.mapKey("waze"), map[string]any{"Url": st.esc(shop.WazeURL(q))}))
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
		writeCourierForm(&b, loc, o, telegramCourierStyle)
	default:
		fmt.Fprintf(&b, "%s\n", say(loc, "salesnotify.shop.delivery_shipping", nil))
		writeCourierForm(&b, loc, o, telegramCourierStyle)
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

// shopItemLines renders the goods as "Name ×qty" entries, cut to what is left
// of the message budget: used is the length of everything already in the
// message. The lines left out are counted in a closing "and N more" entry.
func shopItemLines(loc string, items []gen.ShopOrderItemRow, used int) []string {
	var out []string
	for i, it := range items {
		line := say(loc, "salesnotify.shop.sale_item", map[string]any{
			"Name": esc(clip(shopItemName(it), maxShopItemName)), "Qty": it.Qty,
		})
		more := ""
		if i < len(items)-1 {
			more = say(loc, "salesnotify.shop.items_more", map[string]any{"Count": len(items) - i - 1})
		}
		if i == maxShopItemLines || used+utf8.RuneCountInString(line)+utf8.RuneCountInString(more)+2 > shopMessageBudget {
			out = append(out, say(loc, "salesnotify.shop.items_more", map[string]any{"Count": len(items) - i}))
			break
		}
		out = append(out, line)
		used += utf8.RuneCountInString(line) + 2
	}
	return out
}

// FormatShopSale renders the FACT of a paid shop order for a sales chat: the
// order number, the total and the goods. No buyer name, phone, e-mail or
// address, whatever the order holds.
func FormatShopSale(s ShopOrder) string {
	o := s.Order
	loc := s.Locale
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", say(loc, "salesnotify.shop.sale_title", map[string]any{"Org": esc(s.OrgName)}))
	fmt.Fprintf(&b, "%s · %s", say(loc, "salesnotify.shop.order_number", map[string]any{"Number": o.Number}),
		say(loc, "salesnotify.shop.total", map[string]any{"Amount": shopMoney(o.Total, o.Currency, loc)}))
	if len(s.Items) > 0 {
		head := say(loc, "salesnotify.shop.sale_items", map[string]any{"Count": len(s.Items), "List": ""})
		lines := shopItemLines(loc, s.Items, utf8.RuneCountInString(b.String())+utf8.RuneCountInString(head))
		fmt.Fprintf(&b, "\n%s", say(loc, "salesnotify.shop.sale_items", map[string]any{"Count": len(s.Items), "List": strings.Join(lines, ", ")}))
	}
	return b.String()
}

// shopRefundedFully reports whether everything the buyer paid has gone back.
func shopRefundedFully(o gen.ShopOrderRow) bool {
	return o.Status == "refunded" || (o.Total > 0 && o.RefundedMinor >= o.Total)
}

// FormatShopSaleRefund renders a refund for a sales chat: the order number,
// the amount of this refund (refundedNow, 0 when the event did not say) and
// whether the order is refunded fully or partly. No buyer data.
func FormatShopSaleRefund(s ShopOrder, refundedNow int64) string {
	o := s.Order
	loc := s.Locale
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", say(loc, "salesnotify.shop.refund_title", map[string]any{"Org": esc(s.OrgName)}))
	writeShopHeader(&b, loc, o)
	if refundedNow > 0 {
		fmt.Fprintf(&b, "%s\n", say(loc, "salesnotify.shop.refunded", map[string]any{"Amount": shopMoney(refundedNow, o.Currency, loc)}))
	}
	if shopRefundedFully(o) {
		fmt.Fprintf(&b, "%s\n", say(loc, "salesnotify.shop.sale_refund_full", nil))
	} else {
		fmt.Fprintf(&b, "%s\n", say(loc, "salesnotify.shop.sale_refund_part", map[string]any{
			"Amount": shopMoney(o.RefundedMinor, o.Currency, loc), "Total": shopMoney(o.Total, o.Currency, loc),
		}))
	}
	return strings.TrimRight(b.String(), "\n")
}

// FormatShopDeliveryRefund renders a refund for the delivery group: the order
// number and the goods, and what to do with the parcel. An order not shipped
// yet says "do not ship" (the whole order refunded: stop the shipment, part of
// it: wait for the organizer), a shipped one says "already shipped". The buyer
// is named so the logistician knows which parcel it is.
func FormatShopDeliveryRefund(s ShopOrder, refundedNow int64) string {
	o := s.Order
	loc := s.Locale
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", say(loc, "salesnotify.shop.refund_title", map[string]any{"Org": esc(s.OrgName)}))
	writeShopHeader(&b, loc, o)
	switch {
	case o.ShippedAt != nil:
		fmt.Fprintf(&b, "%s\n", say(loc, "salesnotify.shop.refund_already_shipped", nil))
	case shopRefundedFully(o):
		fmt.Fprintf(&b, "%s\n", say(loc, "salesnotify.shop.refund_stop_shipment", nil))
	default:
		fmt.Fprintf(&b, "%s\n", say(loc, "salesnotify.shop.refund_do_not_ship", nil))
	}
	if refundedNow > 0 {
		fmt.Fprintf(&b, "%s\n", say(loc, "salesnotify.shop.refunded", map[string]any{"Amount": shopMoney(refundedNow, o.Currency, loc)}))
	}
	fmt.Fprintf(&b, "%s\n", say(loc, "salesnotify.shop.refunded_so_far", map[string]any{
		"Amount": shopMoney(o.RefundedMinor, o.Currency, loc), "Total": shopMoney(o.Total, o.Currency, loc),
	}))
	writeShopBuyer(&b, o)
	if len(s.Items) > 0 {
		fmt.Fprintf(&b, "\n%s\n", say(loc, "salesnotify.shop.items", nil))
		for _, l := range shopItemLines(loc, s.Items, utf8.RuneCountInString(b.String())) {
			fmt.Fprintf(&b, "%s\n", l)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// FormatShopOrderReview renders the notice for an order parked in
// manual_review, for the sales chat (the delivery group hears nothing: the
// order is not paid, there is nothing to ship). It carries no buyer data.
// There are two causes and the text never mixes them up (SB-02):
//
//   - late_payment_no_stock: the payment arrived after the hold ended and found
//     the goods gone. The money IS taken ("Paid"); the organizer decides
//     (ship late, or refund).
//   - payment_unconfirmed: the provider's report is not trusted (the amount
//     differs, the payment was reversed before arena confirmed it). The money
//     may NOT have arrived, so the message says "not confirmed", has no
//     "Paid" line and asks to wait.
//
// The operator gets their own ops alert from the watchdog, not this one.
func FormatShopOrderReview(s ShopOrder) string {
	o := s.Order
	loc := s.Locale
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", say(loc, "salesnotify.shop.review_title", map[string]any{"Org": esc(s.OrgName)}))
	writeShopHeader(&b, loc, o)
	if o.ReviewReason != nil && *o.ReviewReason == shop.ReviewPaymentUnconfirmed {
		fmt.Fprintf(&b, "%s\n", say(loc, "salesnotify.shop.review_unconfirmed_amount", map[string]any{"Amount": shopMoney(o.Total, o.Currency, loc)}))
		fmt.Fprintf(&b, "\n%s", say(loc, "salesnotify.shop.review_unconfirmed_body", map[string]any{"Number": o.Number}))
		return b.String()
	}
	fmt.Fprintf(&b, "%s\n", say(loc, "salesnotify.shop.review_paid", map[string]any{"Amount": shopMoney(o.Total, o.Currency, loc)}))
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

// RouteShop splits the subscriptions that hear a shop trigger for orgID into
// the sales chats (the fact) and the delivery groups (the card). The per-chat
// switches apply as for tickets: paid and review follow on_order_paid, a
// refund follows on_ticket_refunded. A review notice goes to the sales chats
// only. Otherwise a chat with both kinds is a delivery group and gets the card
// alone; ships says whether the order has anything to deliver, and when it has
// not (a file or a service) the delivery groups hear nothing, so a chat with
// both kinds falls back to the fact.
func RouteShop(subs []Subscription, orgID string, trigger Trigger, ships bool) (sale, delivery []Subscription) {
	for _, s := range subs {
		if s.OrgID != "" && s.OrgID != orgID {
			continue
		}
		on := s.OnOrderPaid
		if trigger == TriggerShopOrderRefunded {
			on = s.OnTicketRefunded
		}
		if !on {
			continue
		}
		toDelivery := trigger != TriggerShopOrderReview && ships && s.Hears(KindShopDelivery)
		switch {
		case toDelivery:
			delivery = append(delivery, s)
		case s.Hears(KindShopSale):
			sale = append(sale, s)
		}
	}
	return sale, delivery
}

// shopAllowed reports whether the shop announcements go out for the order's
// organization: its shop mode is full or orders only. A refund is the one
// event that may arrive after the order has closed (the refund of the last
// open order closes it), so a refund of an order that was still waiting to be
// shipped counts as orders only.
func (d *Dispatcher) shopAllowed(ctx context.Context, orgID string, trigger Trigger, o gen.ShopOrderRow) (bool, error) {
	mode, err := d.store.ShopMode(ctx, orgID)
	if err != nil {
		return false, err
	}
	if mode != orgmodules.ModeNone {
		return true, nil
	}
	return trigger == TriggerShopOrderRefunded && o.ShippedAt == nil && o.DeliveryKind != "none" && o.RefundedMinor > 0, nil
}

// handleShop announces one shop event: it loads the order, checks the shop
// mode, routes the sales chats and the delivery groups, and claims the card
// and the fact separately, each under a key that includes what makes the
// announcement new.
func (d *Dispatcher) handleShop(ctx context.Context, ev outbox.Event) {
	id := payloadString(ev.Payload, "order_id", ev.AggregateID)
	orgID := payloadString(ev.Payload, "org_id", "")
	order, err := d.store.ShopOrder(ctx, orgID, id)
	if err != nil {
		if !errors.Is(err, ErrNotFound) {
			d.logger.Warn("sales.notify: could not build notification",
				"event_type", ev.EventType, "aggregate_id", ev.AggregateID, "error", err.Error())
		}
		return
	}

	var (
		trigger          Trigger
		saleKey, fullKey string
		saleText         string
		fullText         string
	)
	switch ev.EventType {
	case EventShopOrderManualReview:
		trigger = TriggerShopOrderReview
		// The cause is part of the key: an order parked as an unconfirmed
		// payment that is then parked again as a late payment (the payment
		// succeeded, the goods were gone) is a new announcement.
		reason := ""
		if order.Order.ReviewReason != nil {
			reason = *order.Order.ReviewReason
		}
		saleKey, saleText = "shop_review:"+id+":"+reason, FormatShopOrderReview(order)
	case EventShopOrderPaid:
		trigger = TriggerShopOrderPaid
		saleKey, saleText = "shop_sale_paid:"+id, FormatShopSale(order)
		fullKey, fullText = "shop_paid:"+id, FormatShopOrderPaid(order)
	default:
		trigger = TriggerShopOrderRefunded
		// The amount "refunded so far" is part of the key: two refunds of one
		// order are two announcements, a retry of one is not.
		so := order.Order.RefundedMinor
		if v, ok := payloadInt(ev.Payload, "refunded_minor"); ok {
			so = v
		}
		now, _ := payloadInt(ev.Payload, "refunded_now")
		saleKey, saleText = fmt.Sprintf("shop_sale_refund:%s:%d", id, so), FormatShopSaleRefund(order, now)
		fullKey, fullText = fmt.Sprintf("shop_refund:%s:%d", id, so), FormatShopDeliveryRefund(order, now)
	}

	ok, err := d.shopAllowed(ctx, orgID, trigger, order.Order)
	if err != nil {
		d.logger.Warn("sales.notify: shop mode", "org_id", orgID, "error", err.Error())
		return
	}
	if !ok {
		return
	}
	subs, err := d.store.Subscriptions(ctx)
	if err != nil {
		d.logger.Warn("sales.notify: load subscriptions", "error", err.Error())
		return
	}
	sale, delivery := RouteShop(subs, orgID, trigger, order.Order.DeliveryKind != "none")
	// None of the shop messages carries the events-bot footer: the chats are
	// a courier's and a sales desk's, and "create your events from Telegram"
	// is noise there. Ticket sales keep it (compose).
	d.announce(ctx, delivery, fullKey, fullText)
	d.announce(ctx, sale, saleKey, saleText)
}

// announce claims key once and sends text to every target.
func (d *Dispatcher) announce(ctx context.Context, targets []Subscription, key, text string) {
	if len(targets) == 0 {
		return
	}
	first, err := d.store.Claim(ctx, key)
	if err != nil {
		d.logger.Warn("sales.notify: claim", "key", key, "error", err.Error())
		return
	}
	if !first {
		return // an outbox retry
	}
	for _, sub := range targets {
		_ = d.deliver(ctx, sub, text)
	}
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

// ShopMode implements Store: the shop mode of the organization today.
func (s *PGStore) ShopMode(ctx context.Context, orgID string) (orgmodules.Mode, error) {
	org, err := uuid.Parse(orgID)
	if err != nil {
		return orgmodules.ModeNone, ErrNotFound
	}
	return orgmodules.ShopMode(ctx, gen.New(s.pool), org, time.Now())
}
