package shop

import (
	"net/url"
	"strings"

	"golang.org/x/text/language"
	"golang.org/x/text/language/display"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// CourierFields are an order's delivery details in the order a courier form
// asks for them (spec 47 section 7): name, phone, address line 1 (street and
// number), address line 2 (entrance, floor, intercom), city, postal code,
// country. The street comes BEFORE the city because that is how a postal
// address reads. It is the ONE place that list lives: the courier text of the
// order card (CourierText), the Telegram announcement of a paid order
// (salesnotify) and the e-mail to the buyer all read it, so a change to the
// form's order is made here and nowhere else.
type CourierFields struct {
	Name     string
	Phone    string
	Address1 string
	Address2 string
	City     string
	Postal   string
	// Country is the English display name of the ship country ("Israel"); the
	// shop's messages are English. CountryCode is the ISO 3166-1 alpha-2 code
	// it came from, upper case.
	Country     string
	CountryCode string
}

// CourierFieldsOf reads the delivery details of an order. The recipient's
// name is the shipping name, falling back to the buyer's.
func CourierFieldsOf(o gen.ShopOrderRow) CourierFields { return CourierFieldsOfIn(o, "en") }

// CourierFieldsOfIn is CourierFieldsOf with the country named in locale
// ("ru": "Израиль"). A locale the name tables do not know gives English.
func CourierFieldsOfIn(o gen.ShopOrderRow, locale string) CourierFields {
	name := strings.TrimSpace(o.ShipName)
	if name == "" {
		name = strings.TrimSpace(o.BuyerName)
	}
	code := strings.ToUpper(strings.TrimSpace(o.ShipCountry))
	return CourierFields{
		Name:        name,
		Phone:       strings.TrimSpace(o.BuyerPhone),
		Address1:    strings.TrimSpace(o.ShipAddress1),
		Address2:    strings.TrimSpace(o.ShipAddress2),
		City:        strings.TrimSpace(o.ShipCity),
		Postal:      strings.TrimSpace(o.ShipPostal),
		Country:     CountryNameIn(code, locale),
		CountryCode: code,
	}
}

// CountryName is the English name of an ISO 3166-1 alpha-2 code ("IL" ->
// "Israel"). An empty or unknown code gives the code itself (or "").
func CountryName(iso2 string) string { return CountryNameIn(iso2, "en") }

// CountryNameIn is the name of an ISO 3166-1 alpha-2 code in locale ("IL",
// "ru" -> "Израиль"), from the CLDR tables of golang.org/x/text. A code that
// is empty or unknown gives itself (or ""); a locale that is empty, invalid
// or without tables gives English.
func CountryNameIn(iso2, locale string) string {
	iso2 = strings.ToUpper(strings.TrimSpace(iso2))
	if iso2 == "" {
		return ""
	}
	r, err := language.ParseRegion(iso2)
	if err != nil {
		return iso2
	}
	tag, err := language.Parse(strings.ReplaceAll(strings.TrimSpace(locale), "_", "-"))
	if err != nil || tag == language.Und {
		tag = language.English
	}
	if name := display.Regions(tag).Name(r); name != "" {
		return name
	}
	if name := display.English.Regions().Name(r); name != "" {
		return name
	}
	return iso2
}

// Lines is every non-empty field on a line of its own, address line 2
// separate from line 1.
func (c CourierFields) Lines() []string {
	return nonEmpty(c.Name, c.Phone, c.Address1, c.Address2, c.City, c.Postal, c.Country)
}

// JoinedLines is the same with the two address lines on one line
// ("Herzl 1, apt 4"), which is how the courier text prints the address.
func (c CourierFields) JoinedLines() []string {
	return nonEmpty(c.Name, c.Phone, c.street(), c.City, c.Postal, c.Country)
}

func (c CourierFields) street() string {
	if c.Address2 == "" {
		return c.Address1
	}
	return strings.TrimSpace(c.Address1 + ", " + c.Address2)
}

// HasAddress reports whether there is anywhere to send a courier: a street or
// a city.
func (c CourierFields) HasAddress() bool { return c.Address1 != "" || c.City != "" }

// MapQuery is the address as one search string for a map: street, city,
// postal code, country. Address line 2 (entrance, floor, intercom) is left
// out on purpose, it only confuses a geocoder.
func (c CourierFields) MapQuery() string {
	return strings.Join(nonEmpty(c.Address1, c.City, c.Postal, c.Country), ", ")
}

// GoogleMapsURL is a Google Maps search for the address. No geocoding call is
// made and no coordinates are stored: the map app resolves the text itself.
// TODO geocode: when shop_orders gets ship_lat/ship_lng, link to
// "...&query=<lat>,<lng>" instead and keep the text search as the fallback.
func GoogleMapsURL(query string) string {
	return "https://www.google.com/maps/search/?api=1&query=" + url.QueryEscape(query)
}

// WazeURL opens Waze (what Israeli couriers use) and starts navigation to the
// address. TODO geocode: with coordinates use "ul?ll=<lat>,<lng>&navigate=yes".
func WazeURL(query string) string {
	return "https://waze.com/ul?q=" + url.QueryEscape(query) + "&navigate=yes"
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
