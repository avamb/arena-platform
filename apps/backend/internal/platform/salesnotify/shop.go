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

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/outbox"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/shop"
)

// maxShopItemLines keeps a very large order inside Telegram's 4096-character
// limit: the rest is summarised in one line.
const maxShopItemLines = 30

// ShopOrder is one shop order as the announcement shows it.
type ShopOrder struct {
	OrgName string
	Order   gen.ShopOrderRow
	Items   []gen.ShopOrderItemRow
}

func shopMoney(amount int64, currency string) string {
	return esc(money(amount, currency))
}

// writeShopHeader renders the lines both messages start with: the order and
// the buyer.
func writeShopHeader(b *strings.Builder, o gen.ShopOrderRow) {
	fmt.Fprintf(b, "🧾 Order #%d\n", o.Number)
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

// FormatShopOrderPaid renders a paid shop order for the logistics chat.
func FormatShopOrderPaid(s ShopOrder) string {
	o := s.Order
	var b strings.Builder
	fmt.Fprintf(&b, "🛍 <b>New shop order</b> — %s\n\n", esc(s.OrgName))
	writeShopHeader(&b, o)
	fmt.Fprintf(&b, "💰 Total: <b>%s</b>\n", shopMoney(o.Total, o.Currency))
	writeShopBuyer(&b, o)
	if o.DeliveryKind == "pickup" {
		b.WriteString("🏬 Delivery: pickup\n")
	} else {
		b.WriteString("🚚 Delivery: shipping\n")
	}

	// The courier form, field by field and in the courier form's order.
	if lines := shop.CourierFieldsOf(o).Lines(); len(lines) > 0 {
		b.WriteString("\n📦 <b>Courier form</b>\n")
		for _, l := range lines {
			fmt.Fprintf(&b, "%s\n", esc(l))
		}
	}

	if len(s.Items) > 0 {
		b.WriteString("\n🛒 <b>Items</b>\n")
	}
	for i, it := range s.Items {
		if i == maxShopItemLines {
			fmt.Fprintf(&b, "… and %d more line(s)\n", len(s.Items)-maxShopItemLines)
			break
		}
		fmt.Fprintf(&b, "%d × %s — %s each\n", it.Qty, esc(shopItemName(it)), shopMoney(it.UnitPriceMinor, o.Currency))
	}
	if o.ShippingFee > 0 {
		fmt.Fprintf(&b, "🚚 Shipping: %s\n", shopMoney(o.ShippingFee, o.Currency))
	}
	if o.Discount > 0 {
		fmt.Fprintf(&b, "🏷 Discount: -%s\n", shopMoney(o.Discount, o.Currency))
	}
	if note := strings.TrimSpace(o.DeliveryNote); note != "" {
		fmt.Fprintf(&b, "\n📝 Buyer's note: %s\n", esc(clip(note, 600)))
	}
	return strings.TrimRight(b.String(), "\n")
}

// FormatShopOrderRefund renders money returned on a shop order. refundedNow
// is the amount of this refund, 0 when the event did not say.
func FormatShopOrderRefund(s ShopOrder, refundedNow int64) string {
	o := s.Order
	var b strings.Builder
	fmt.Fprintf(&b, "↩️ <b>Shop refund</b> — %s\n\n", esc(s.OrgName))
	writeShopHeader(&b, o)
	if refundedNow > 0 {
		fmt.Fprintf(&b, "💸 Refunded: <b>%s</b>\n", shopMoney(refundedNow, o.Currency))
	}
	fmt.Fprintf(&b, "💸 Refunded so far: %s of %s\n", shopMoney(o.RefundedMinor, o.Currency), shopMoney(o.Total, o.Currency))
	writeShopBuyer(&b, o)
	return strings.TrimRight(b.String(), "\n")
}

func shopItemName(it gen.ShopOrderItemRow) string { return shop.ItemLabel(it) }

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// composeShop loads the order a shop event names and renders the message.
func (d *Dispatcher) composeShop(ctx context.Context, ev outbox.Event) (key string, trigger Trigger, text, orgID string, err error) {
	id := payloadString(ev.Payload, "order_id", ev.AggregateID)
	orgID = payloadString(ev.Payload, "org_id", "")
	order, err := d.store.ShopOrder(ctx, orgID, id)
	if err != nil {
		return "", 0, "", "", err
	}
	if ev.EventType == EventShopOrderPaid {
		return "shop_paid:" + id, TriggerShopOrderPaid, d.withFooter(FormatShopOrderPaid(order)), orgID, nil
	}
	// The amount "refunded so far" is part of the key: two refunds of one
	// order are two announcements, a retry of one is not.
	so := order.Order.RefundedMinor
	if v, ok := payloadInt(ev.Payload, "refunded_minor"); ok {
		so = v
	}
	now, _ := payloadInt(ev.Payload, "refunded_now")
	key = fmt.Sprintf("shop_refund:%s:%d", id, so)
	return key, TriggerShopOrderRefunded, d.withFooter(FormatShopOrderRefund(order, now)), orgID, nil
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
	var name string
	if err := s.pool.QueryRow(ctx, `SELECT name FROM organizations WHERE id = $1`, org).Scan(&name); err != nil {
		return ShopOrder{}, err
	}
	return ShopOrder{OrgName: name, Order: row, Items: items}, nil
}
