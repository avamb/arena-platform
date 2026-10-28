package shop

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/mail"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/geoslug"
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
	case StatusPaid, StatusShipped, StatusRefunded, StatusPartiallyRefunded:
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

// FormatMinor prints minor units as a major-unit amount with two decimals and
// the currency code ("2500", "ILS" -> "25.00 ILS"). For display only.
func FormatMinor(amount int64, currency string) string {
	sign := ""
	if amount < 0 {
		sign, amount = "-", -amount
	}
	return fmt.Sprintf("%s%d.%02d %s", sign, amount/100, amount%100, currency)
}
