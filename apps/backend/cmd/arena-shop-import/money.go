package main

import (
	"errors"
	"fmt"
	"strings"
)

// errEmptyMoney is returned for a blank amount; callers decide whether blank
// means "absent" (a compare-at price) or "invalid" (a price).
var errEmptyMoney = errors.New("empty amount")

// zeroDecimalCurrencies and threeDecimalCurrencies list the ISO 4217 codes
// whose minor unit is not 1/100. Everything else, ILS and EUR included, has
// two decimals.
var (
	zeroDecimalCurrencies = map[string]bool{
		"JPY": true, "KRW": true, "VND": true, "CLP": true, "ISK": true, "UGX": true, "XAF": true, "XOF": true,
	}
	threeDecimalCurrencies = map[string]bool{"KWD": true, "BHD": true, "OMR": true, "JOD": true, "TND": true}
)

func currencyExponent(currency string) int {
	switch c := strings.ToUpper(currency); {
	case zeroDecimalCurrencies[c]:
		return 0
	case threeDecimalCurrencies[c]:
		return 3
	}
	return 2
}

// ParseMinor converts a Shopify major-unit amount ("650.00", "12.5", "7")
// into minor units. This is the ONE place a major amount becomes a minor one
// in the importer: it works on the decimal string, never on a float, and it
// refuses an amount with more decimals than the currency has instead of
// rounding it silently.
func ParseMinor(raw, currency string) (int64, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, errEmptyMoney
	}
	exp := currencyExponent(currency)
	whole, frac, hasDot := strings.Cut(s, ".")
	if whole == "" && hasDot {
		whole = "0"
	}
	if whole == "" || len(whole) > 12 {
		return 0, fmt.Errorf("amount %q is not a valid number", raw)
	}
	if len(frac) > exp {
		// Trailing zeros beyond the currency's decimals are harmless ("5.000").
		if strings.Trim(frac[exp:], "0") != "" {
			return 0, fmt.Errorf("amount %q has more than %d decimals", raw, exp)
		}
		frac = frac[:exp]
	}
	for len(frac) < exp {
		frac += "0"
	}
	var minor int64
	for _, part := range []string{whole, frac} {
		for _, r := range part {
			if r < '0' || r > '9' {
				return 0, fmt.Errorf("amount %q is not a valid non-negative number", raw)
			}
			minor = minor*10 + int64(r-'0')
		}
	}
	return minor, nil
}

// FormatMinor renders minor units for the report ("650.00").
func FormatMinor(minor int64, currency string) string {
	exp := currencyExponent(currency)
	if exp == 0 {
		return fmt.Sprintf("%d", minor)
	}
	div := int64(1)
	for i := 0; i < exp; i++ {
		div *= 10
	}
	return fmt.Sprintf("%d.%0*d", minor/div, exp, minor%div)
}
