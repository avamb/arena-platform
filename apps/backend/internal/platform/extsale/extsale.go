// Package extsale holds the rules of a session that a partner box office
// sells (sessions.sales_mode = 'external', migration 0220, spec 63 §14): the
// check of the partner's address and name, the conversion of the display
// price "from" between major and minor units, and the lock rules of a change
// of the sales mode. It is pure (no database, no HTTP), so the organizer API,
// the event-bundle import and the feed share ONE set of rules.
package extsale

import (
	"errors"
	"math"
	"net"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/shop"
)

const (
	// MaxURLLen is the longest partner address accepted.
	MaxURLLen = 2048
	// MaxSellerNameRunes is the longest box office name accepted.
	MaxSellerNameRunes = 80
	// maxPriceMajor bounds the display price so the minor value fits bigint.
	maxPriceMajor = 1e9
)

// Error codes of the partner block (422) and of the mode change (409).
const (
	CodeInvalidURL        = "session.invalid_external_url"
	CodeInvalidSellerName = "session.invalid_external_seller_name"
	CodeInvalidPrice      = "session.invalid_external_price"
	CodeInvalidCurrency   = "session.invalid_external_currency"
	CodeInvalidSalesMode  = "session.invalid_sales_mode"
	CodeExternalMissing   = "session.external_required"
	CodeExternalUnused    = "session.external_not_applicable"
	CodeSalesModeLocked   = "session.sales_mode_locked"
	CodeSoldExternally    = "session.sold_externally"
)

// platformHosts are the platform's own sites: a partner box office is never
// one of them (nor a subdomain of one).
var platformHosts = []string{"arenasoldout.com"}

var currencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)

// FieldError is a refusal of the partner block: Code is the error code,
// Field the dotted name of the offending field.
type FieldError struct {
	Code    string
	Field   string
	Message string
}

// Error implements error.
func (e *FieldError) Error() string { return e.Code + ": " + e.Message }

// Input is the partner block as a caller wrote it. PriceFrom is in major
// units; a nil PriceFrom and an empty Currency mean "no display price".
type Input struct {
	URL        string
	SellerName string
	PriceFrom  *float64
	Currency   string
}

// Sale is the checked partner block, ready for gen.SetSessionSalesMode.
type Sale struct {
	URL            string
	SellerName     string
	PriceFromMinor *int64
	Currency       *string
}

// ValidateURL checks a partner box office address: https only, a host that is
// a name (no IP literal, no bare host), not a platform host, at most
// MaxURLLen characters. It returns the trimmed address.
func ValidateURL(raw string) (string, *FieldError) {
	s := strings.TrimSpace(raw)
	bad := func(msg string) (string, *FieldError) {
		return "", &FieldError{Code: CodeInvalidURL, Field: "external.url", Message: msg}
	}
	if s == "" {
		return bad("url is required")
	}
	if len(s) > MaxURLLen {
		return bad("url must not be longer than 2048 characters")
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return bad("url must be a valid address")
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return bad("url must start with https://")
	}
	if u.User != nil {
		return bad("url must not contain a user name or password")
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host == "" || net.ParseIP(host) != nil {
		return bad("url must use a host name, not an IP address")
	}
	if !strings.Contains(host, ".") {
		return bad("url must use a public host name")
	}
	for _, p := range platformHosts {
		if host == p || strings.HasSuffix(host, "."+p) {
			return bad("url must lead to a partner box office, not to this platform")
		}
	}
	// The scheme is case-insensitive (RFC 3986 §3.1) but the database CHECK
	// is LIKE 'https://%': store it in lower case. Only the scheme is
	// rewritten, the rest of the address stays as the caller wrote it.
	s = "https" + s[len(u.Scheme):]
	return s, nil
}

// ValidateSellerName trims the box office name and checks it is 1-80 characters.
func ValidateSellerName(raw string) (string, *FieldError) {
	s := strings.TrimSpace(raw)
	n := utf8.RuneCountInString(s)
	if n < 1 || n > MaxSellerNameRunes {
		return "", &FieldError{Code: CodeInvalidSellerName, Field: "external.seller_name",
			Message: "seller_name must be 1 to 80 characters"}
	}
	return s, nil
}

// MinorFromMajor converts a major-unit price to minor units with the
// currency's own number of decimals (the converter of the shop and the
// tiers). ok is false for a negative, non-finite or absurdly large price.
func MinorFromMajor(major float64, currency string) (int64, bool) {
	if math.IsNaN(major) || math.IsInf(major, 0) || major < 0 || major > maxPriceMajor {
		return 0, false
	}
	return int64(math.Round(major * math.Pow10(shop.CurrencyExponent(currency)))), true
}

// MajorFromMinor converts minor units to a major-unit number for the wire.
func MajorFromMinor(minor int64, currency string) float64 {
	exp := shop.CurrencyExponent(currency)
	return float64(minor) / math.Pow10(exp)
}

// View is the partner block as the API and the feed show it.
type View struct {
	URL        string   `json:"url"`
	SellerName string   `json:"seller_name"`
	PriceFrom  *float64 `json:"price_from"`
	Currency   *string  `json:"currency"`
}

// ViewFromRow returns the partner block of a session row, or nil for a
// session the platform sells itself.
func ViewFromRow(s gen.SessionRow) *View {
	if s.SalesMode != gen.SessionSalesModeExternal || s.ExternalTicketURL == nil {
		return nil
	}
	v := &View{URL: *s.ExternalTicketURL}
	if s.ExternalSellerName != nil {
		v.SellerName = *s.ExternalSellerName
	}
	if s.DisplayPriceFromMinor != nil && s.DisplayCurrency != nil {
		cur := strings.TrimSpace(*s.DisplayCurrency)
		p := MajorFromMinor(*s.DisplayPriceFromMinor, cur)
		v.PriceFrom = &p
		v.Currency = &cur
	}
	return v
}

// Normalize checks the whole partner block and converts the price to minor
// units. The price and its currency go together or not at all.
func Normalize(in Input) (Sale, *FieldError) {
	u, ferr := ValidateURL(in.URL)
	if ferr != nil {
		return Sale{}, ferr
	}
	name, ferr := ValidateSellerName(in.SellerName)
	if ferr != nil {
		return Sale{}, ferr
	}
	sale := Sale{URL: u, SellerName: name}
	cur := strings.ToUpper(strings.TrimSpace(in.Currency))
	switch {
	case in.PriceFrom == nil && cur == "":
		return sale, nil
	case in.PriceFrom == nil:
		return Sale{}, &FieldError{Code: CodeInvalidPrice, Field: "external.price_from",
			Message: "price_from and currency are given together"}
	case cur == "":
		return Sale{}, &FieldError{Code: CodeInvalidCurrency, Field: "external.currency",
			Message: "price_from and currency are given together"}
	}
	if !currencyPattern.MatchString(cur) {
		return Sale{}, &FieldError{Code: CodeInvalidCurrency, Field: "external.currency",
			Message: "currency must be a three-letter ISO 4217 code"}
	}
	minor, ok := MinorFromMajor(*in.PriceFrom, cur)
	if !ok {
		return Sale{}, &FieldError{Code: CodeInvalidPrice, Field: "external.price_from",
			Message: "price_from must be a number from 0 to 1000000000"}
	}
	sale.PriceFromMinor = &minor
	sale.Currency = &cur
	return sale, nil
}

// ErrSalesModeLocked says a session cannot change its sales mode any more.
var ErrSalesModeLocked = errors.New("extsale: sales mode is locked")

// CheckSwitch decides a change of the sales mode from `from` to `to` by the
// lock rules of spec 63 §14.1:
//   - platform -> external only while the session has no orders, no holds, no
//     seats and no tiers;
//   - external -> platform only while it has no seats and no tiers.
//
// Staying in the same mode is always allowed. The caller reads the facts
// inside the transaction that holds the session row lock.
func CheckSwitch(from, to string, f gen.SessionSalesModeLockFacts) error {
	if from == to {
		return nil
	}
	switch to {
	case gen.SessionSalesModeExternal:
		if f.HasOrders || f.HasHolds || f.HasSeats || f.HasTiers {
			return ErrSalesModeLocked
		}
	case gen.SessionSalesModePlatform:
		if f.HasSeats || f.HasTiers {
			return ErrSalesModeLocked
		}
	}
	return nil
}

// ValidMode reports whether s is a sales mode.
func ValidMode(s string) bool {
	return s == gen.SessionSalesModePlatform || s == gen.SessionSalesModeExternal
}
