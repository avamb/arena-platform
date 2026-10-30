package shop

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/mail"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/geoslug"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/richtext"
)

// Limits shared by the service and its tests. They mirror the CHECK
// constraints of migration 0172.
const (
	MaxSlugLen        = 200
	MaxTitleLen       = 300
	MaxBodyHTMLLen    = 100000
	MaxPageBodyLen    = 200000
	MaxVariants       = 100
	MaxCheckoutLines  = 50
	MaxLineQty        = 99
	MaxShortTextLen   = 200
	MaxAddressLen     = 300
	MaxNoteLen        = 2000
	MaxAnnouncement   = 500
	MaxEmailLen       = 254
	MaxPhoneLen       = 50
	MaxPostalLen      = 30
	MaxCityLen        = 120
	MaxSKULen         = 100
	MaxImageAltLen    = 500
	MaxPageListLength = 100
)

// The rich-text profiles of a product description and a static page: the
// event description's tags and link schemes (richtext.ProfileDescription),
// with the shop's own length limits and no limit on visible text or links.
// A product description drops everything outside that list: images, tables,
// <section>, <style>, inline styles and classes (a Shopify body keeps its
// text, not its layout). A page also keeps img and tables (SanitizePageBody).
var (
	ProductBodyProfile = richtext.Profile{
		Name:    "shop_product_body",
		Tags:    richtext.ProfileDescription.Tags,
		Schemes: richtext.ProfileDescription.Schemes,
		MaxHTML: MaxBodyHTMLLen,
	}
	PageBodyProfile = richtext.Profile{
		Name:    "shop_page_body",
		Tags:    richtext.ProfileDescription.Tags,
		Schemes: richtext.ProfileDescription.Schemes,
		MaxHTML: MaxPageBodyLen,
	}
)

// SanitizeBody reduces body_html to the profile's rich-text subset; a text
// the sanitizer refuses is a 422 shop.invalid_body_html naming the reason.
//
// The page profile goes to SanitizePageBody, which also keeps pictures
// (https src only) and tables: a page is operator-authored content of a
// storefront, unlike a product description.
func SanitizeBody(in string, p richtext.Profile) (string, error) {
	if p.Name == PageBodyProfile.Name {
		return SanitizePageBody(in)
	}
	if strings.TrimSpace(in) == "" {
		return "", nil
	}
	out, err := richtext.Sanitize(in, p)
	if err != nil {
		e := invalid("shop.invalid_body_html", fmt.Sprintf("body_html was refused (%v); it is limited to %d characters of HTML", err, p.MaxHTML))
		var re *richtext.Error
		if errors.As(err, &re) {
			e.Details = map[string]any{"reason": re.Reason}
		}
		return "", e
	}
	return out, nil
}

var (
	currencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)
	countryPattern  = regexp.MustCompile(`^[A-Z]{2}$`)
	domainPattern   = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)
)

// NormalizeSlug lower-cases and trims a product or page slug and reports
// whether it is usable: 1-200 characters, no slash, '?', '#' or whitespace.
// Shopify handles may carry non-Latin letters, so those are kept.
func NormalizeSlug(raw string) (string, bool) {
	s := strings.ToLower(strings.TrimSpace(raw))
	n := utf8.RuneCountInString(s)
	if n == 0 || len(s) > MaxSlugLen || n > MaxSlugLen {
		return "", false
	}
	if strings.ContainsAny(s, "/?#") || strings.IndexFunc(s, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\f' || r == '\v'
	}) >= 0 {
		return "", false
	}
	return s, true
}

// SlugFromTitle derives a slug from a product title through the platform's
// one slug implementation; a title with nothing transliterable gives
// "product".
func SlugFromTitle(title string) string {
	s := geoslug.Slugify(title)
	if len(s) > MaxSlugLen {
		s = strings.Trim(s[:MaxSlugLen], "-")
	}
	if s == "" {
		return "product"
	}
	return s
}

// ValidCurrency reports an ISO 4217-shaped code (upper case).
func ValidCurrency(c string) bool { return currencyPattern.MatchString(c) }

// ValidCountry reports an ISO 3166-1 alpha-2-shaped code (upper case).
func ValidCountry(c string) bool { return countryPattern.MatchString(c) }

// NormalizeDomain lower-cases a storefront host and checks it is a bare host
// name (no scheme, path or port). "" means "no domain".
func NormalizeDomain(raw string) (string, bool) {
	d := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(raw)), ".")
	if d == "" {
		return "", true
	}
	if len(d) < 3 || len(d) > 253 || !domainPattern.MatchString(d) {
		return "", false
	}
	return d, true
}

// ValidEmail reports a single plain e-mail address.
func ValidEmail(e string) bool {
	if e == "" || len(e) > MaxEmailLen || strings.ContainsAny(e, " <>") {
		return false
	}
	a, err := mail.ParseAddress(e)
	return err == nil && a.Address == e
}

// textOK reports a string within max runes.
func textOK(s string, max int) bool { return utf8.RuneCountInString(s) <= max && len(s) <= 4*max }

// NewOrderToken is the buyer's secret handle on an order: 32 random bytes,
// hex (the shape shop_orders_token_check expects).
func NewOrderToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("shop: order token: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// RefundedStatus is the order status that follows from how much of it went
// back: refunded when all of it, partially_refunded when some, else the
// current status.
func RefundedStatus(current string, total, refunded int64) string {
	switch {
	case refunded <= 0:
		return current
	case refunded >= total:
		return StatusRefunded
	default:
		return StatusPartiallyRefunded
	}
}

// refundStatus is the status an order takes when accepted (succeeded) of
// its total went back: refunded when all of it did; partially_refunded when
// some did (an order parked in manual_review stays there until all of it
// went back); and the status it had before any refund when nothing did (a
// refund that was reversed). An order that was never paid keeps its status.
func refundStatus(o gen.ShopOrderRow, accepted int64) string {
	base := o.Status
	switch o.Status {
	case StatusPaid, StatusShipped, StatusManualReview:
	case StatusRefunded, StatusPartiallyRefunded:
		base = StatusPaid
		switch {
		case o.ShippedAt != nil:
			base = StatusShipped
		case strings.Contains(string(o.Metadata), `"review_reason"`):
			base = StatusManualReview
		}
	default:
		return o.Status
	}
	switch {
	case accepted >= o.Total && accepted > 0:
		return StatusRefunded
	case accepted > 0 && base == StatusManualReview:
		return StatusManualReview
	case accepted > 0:
		return StatusPartiallyRefunded
	default:
		return base
	}
}

// PublicStatus maps an order status to the buyer-facing checkout status:
// pending, paid, expired or cancelled. A pending order whose hold has passed
// already reads as expired, before the sweep flips it.
func PublicStatus(o gen.ShopOrderRow, holdPassed bool) string {
	switch o.Status {
	case StatusPendingPayment:
		if holdPassed {
			return "expired"
		}
		return "pending"
	case StatusPaid, StatusShipped, StatusRefunded, StatusPartiallyRefunded, StatusManualReview:
		return "paid"
	case StatusExpired:
		return "expired"
	default:
		return "cancelled"
	}
}

// CourierText is the order's delivery details one per line, in the order a
// courier form asks for them (spec 47 section 7, until the logistician's own
// list arrives): name, phone, city, address, postal code, contents, total,
// comment. Empty fields are left out; money is printed in major units with the
// currency code.
func CourierText(o gen.ShopOrderRow, items []gen.ShopOrderItemRow) string {
	var b strings.Builder
	line := func(s string) {
		if s = strings.TrimSpace(s); s != "" {
			b.WriteString(s)
			b.WriteByte('\n')
		}
	}
	for _, l := range CourierFieldsOf(o).JoinedLines() {
		line(l)
	}
	for _, it := range items {
		line(fmt.Sprintf("%s x %d", ItemLabel(it), it.Qty))
	}
	line(FormatMinor(o.Total, o.Currency))
	line(o.DeliveryNote)
	return strings.TrimRight(b.String(), "\n")
}

// zeroDecimalCurrencies have no minor unit: arena's minor unit of a yen is a
// yen (the Stripe adapter documents the same rule).
var zeroDecimalCurrencies = map[string]bool{
	"BIF": true, "CLP": true, "DJF": true, "GNF": true, "ISK": true, "JPY": true, "KMF": true,
	"KRW": true, "MGA": true, "PYG": true, "RWF": true, "UGX": true, "VND": true,
	"VUV": true, "XAF": true, "XOF": true, "XPF": true,
}

// threeDecimalCurrencies keep three decimals.
var threeDecimalCurrencies = map[string]bool{
	"BHD": true, "JOD": true, "KWD": true, "OMR": true, "TND": true,
}

// CurrencyExponent is the number of decimals of a currency's major unit: 0
// for the yen, 3 for the dinar, 2 for everything else.
func CurrencyExponent(currency string) int {
	c := strings.ToUpper(strings.TrimSpace(currency))
	switch {
	case zeroDecimalCurrencies[c]:
		return 0
	case threeDecimalCurrencies[c]:
		return 3
	default:
		return 2
	}
}

// FormatMinor prints minor units as a major-unit amount with the decimals of
// the currency and its code ("2500", "ILS" -> "25.00 ILS", "2500", "JPY" ->
// "2500 JPY"). For display only.
func FormatMinor(amount int64, currency string) string {
	sign := ""
	if amount < 0 {
		sign, amount = "-", -amount
	}
	exp := CurrencyExponent(currency)
	if exp == 0 {
		return fmt.Sprintf("%s%d %s", sign, amount, currency)
	}
	unit := int64(1)
	for i := 0; i < exp; i++ {
		unit *= 10
	}
	return fmt.Sprintf("%s%d.%0*d %s", sign, amount/unit, exp, amount%unit, currency)
}
