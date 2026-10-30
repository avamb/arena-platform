package salesnotify

// shop_courier.go — the courier text of a shop order (GET .../shop/orders/{id}/
// courier-text, the event center's "Copy for the courier" button).
//
// It is the SAME courier form the Telegram announcement prints (writeCourierForm
// in shop.go, names and order from shop.CourierFields, localized country, the
// Google Maps and Waze links, the same money format), only in plain text: no
// HTML, no emoji, no heading, and a parcel inside the shop's own country does
// not print the country. There is deliberately no second implementation in the
// shop package: the old shop.CourierText had English only, "250.00 ILS", no
// order number, no buyer note and no map links (UX-run findings C-05, E-06,
// E-11).

import (
	"fmt"
	"strings"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/shop"
)

// courierStyle is what differs between the two renderings of the courier form.
type courierStyle struct {
	// html escapes user text for Telegram's HTML parse mode; false is plain.
	html bool
	// heading prints the "Courier form" title above the lines.
	heading bool
	// plainKeys picks the emoji-free texts of the map links.
	plainKeys bool
	// shopCountry, when not empty, is the ISO code of the country that is NOT
	// printed (the shop's own).
	shopCountry string
}

// telegramCourierStyle is the announcement: HTML, a heading, every country.
var telegramCourierStyle = courierStyle{html: true, heading: true}

func (st courierStyle) esc(s string) string {
	if st.html {
		return esc(s)
	}
	return s
}

func (st courierStyle) hidesCountry(code string) bool {
	return st.shopCountry != "" && strings.EqualFold(st.shopCountry, code)
}

func (st courierStyle) mapKey(kind string) string {
	if st.plainKeys {
		return "salesnotify.shop.courier_map_" + kind
	}
	return "salesnotify.shop.map_" + kind
}

// FormatShopCourierText renders the courier text of an order in the
// organization's language (s.Locale): the order number, the recipient, the
// address, the map links, the goods, the total and the buyer's note.
func FormatShopCourierText(s ShopOrder) string {
	o := s.Order
	loc := s.Locale
	st := courierStyle{plainKeys: true, shopCountry: strings.ToUpper(strings.TrimSpace(s.ShopCountry))}
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", say(loc, "salesnotify.shop.courier_order", map[string]any{"Number": o.Number}))
	switch o.DeliveryKind {
	case "none":
		// A file or a service: nothing travels, only who bought it.
		fmt.Fprintf(&b, "%s\n", say(loc, "salesnotify.shop.courier_none", nil))
		writeCourierBuyer(&b, loc, o)
	case "pickup":
		fmt.Fprintf(&b, "%s\n", say(loc, "salesnotify.shop.courier_pickup", nil))
		writeCourierForm(&b, loc, o, st)
	default:
		writeCourierForm(&b, loc, o, st)
	}
	if len(s.Items) > 0 {
		b.WriteByte('\n')
	}
	for _, it := range s.Items {
		fmt.Fprintf(&b, "%s\n", say(loc, "salesnotify.shop.courier_item", map[string]any{"Name": shopItemName(it), "Qty": it.Qty}))
	}
	fmt.Fprintf(&b, "%s\n", say(loc, "salesnotify.shop.courier_total", map[string]any{"Amount": formatShopMoney(o.Total, o.Currency, loc)}))
	if note := strings.TrimSpace(o.DeliveryNote); note != "" {
		fmt.Fprintf(&b, "%s\n", say(loc, "salesnotify.shop.courier_note", map[string]any{"Note": note}))
	}
	return strings.TrimRight(b.String(), "\n")
}

// writeCourierBuyer prints the recipient's name and phone for an order that
// has no address.
func writeCourierBuyer(b *strings.Builder, loc string, o gen.ShopOrderRow) {
	c := shop.CourierFieldsOfIn(o, loc)
	for _, l := range []string{c.Name, c.Phone} {
		if l != "" {
			fmt.Fprintf(b, "%s\n", l)
		}
	}
}
