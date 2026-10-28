package shop

import (
	"strings"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// CourierFields are an order's delivery details in the order a courier form
// asks for them (spec 47 section 7): name, phone, city, address line 1,
// address line 2, postal code. It is the ONE place that list lives: the
// courier text of the order card (CourierText), the Telegram announcement of
// a paid order (salesnotify) and the e-mail to the buyer all read it, so a
// change to the form's order is made here and nowhere else.
type CourierFields struct {
	Name     string
	Phone    string
	City     string
	Address1 string
	Address2 string
	Postal   string
}

// CourierFieldsOf reads the delivery details of an order. The recipient's
// name is the shipping name, falling back to the buyer's.
func CourierFieldsOf(o gen.ShopOrderRow) CourierFields {
	name := strings.TrimSpace(o.ShipName)
	if name == "" {
		name = strings.TrimSpace(o.BuyerName)
	}
	return CourierFields{
		Name:     name,
		Phone:    strings.TrimSpace(o.BuyerPhone),
		City:     strings.TrimSpace(o.ShipCity),
		Address1: strings.TrimSpace(o.ShipAddress1),
		Address2: strings.TrimSpace(o.ShipAddress2),
		Postal:   strings.TrimSpace(o.ShipPostal),
	}
}

// Lines is every non-empty field on a line of its own, address line 2
// separate from line 1.
func (c CourierFields) Lines() []string {
	return nonEmpty(c.Name, c.Phone, c.City, c.Address1, c.Address2, c.Postal)
}

// JoinedLines is the same with the two address lines on one line
// ("Herzl 1, apt 4"), which is how the courier text prints the address.
func (c CourierFields) JoinedLines() []string {
	addr := c.Address1
	if c.Address2 != "" {
		addr = strings.TrimSpace(addr + ", " + c.Address2)
	}
	return nonEmpty(c.Name, c.Phone, c.City, addr, c.Postal)
}

// ItemLabel is an order line's name: the product, with its variant in
// brackets unless it is the single default variant.
func ItemLabel(it gen.ShopOrderItemRow) string {
	title := it.ProductTitle
	if v := strings.TrimSpace(it.VariantTitle); v != "" && v != "Default Title" {
		title += " (" + v + ")"
	}
	return title
}

func nonEmpty(parts ...string) []string {
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
