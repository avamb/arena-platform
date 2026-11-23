package metainsights

import (
	"errors"
	"math"
	"strconv"
	"strings"
)

// zeroDecimal lists the ISO 4217 currencies without minor units.
var zeroDecimal = map[string]bool{
	"BIF": true, "CLP": true, "DJF": true, "GNF": true, "ISK": true, "JPY": true, "KMF": true,
	"KRW": true, "PYG": true, "RWF": true, "UGX": true, "UYI": true, "VND": true, "VUV": true,
	"XAF": true, "XOF": true, "XPF": true,
}

// threeDecimal lists the ISO 4217 currencies with three minor digits.
var threeDecimal = map[string]bool{
	"BHD": true, "IQD": true, "JOD": true, "KWD": true, "LYD": true, "OMR": true, "TND": true,
}

// CurrencyExponent is the number of minor digits of an ISO 4217 currency:
// 0 for JPY, KRW, ISK and the other zero-decimal ones, 3 for BHD, KWD, OMR,
// JOD, TND, IQD and LYD, 2 for every other code.
func CurrencyExponent(currency string) int {
	c := strings.ToUpper(strings.TrimSpace(currency))
	switch {
	case zeroDecimal[c]:
		return 0
	case threeDecimal[c]:
		return 3
	}
	return 2
}

var errBadDecimal = errors.New("metainsights: not a decimal number")

// DecimalToMinor converts a decimal string of Meta ("12.34", "7", "-0.5")
// into minor units with exp digits, without floating point. Extra fraction
// digits are rounded half away from zero ("0.125" with exp 2 is 13). An
// empty string is 0. A value outside int64 is an error.
func DecimalToMinor(s string, exp int) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	if exp < 0 || exp > 6 {
		return 0, errBadDecimal
	}
	neg := false
	switch s[0] {
	case '-':
		neg = true
		s = s[1:]
	case '+':
		s = s[1:]
	}
	intPart, frac, _ := strings.Cut(s, ".")
	if intPart == "" && frac == "" {
		return 0, errBadDecimal
	}
	if !allDigits(intPart) || !allDigits(frac) {
		return 0, errBadDecimal
	}
	if intPart == "" {
		intPart = "0"
	}
	roundUp := false
	if len(frac) > exp {
		roundUp = frac[exp] >= '5'
		frac = frac[:exp]
	}
	frac += strings.Repeat("0", exp-len(frac))
	digits := strings.TrimLeft(intPart+frac, "0")
	if digits == "" {
		digits = "0"
	}
	n, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return 0, errBadDecimal
	}
	if roundUp {
		if n == math.MaxInt64 {
			return 0, errBadDecimal
		}
		n++
	}
	if neg {
		n = -n
	}
	return n, nil
}

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// parseCount reads a Meta counter ("123", sometimes "123.0"); the fraction
// is dropped. An empty or broken value is 0.
func parseCount(s string) int64 {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '.'); i >= 0 {
		s = s[:i]
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// FormatMinor writes minor units with the currency's exponent: 123456 EUR is
// "1234.56 EUR".
func FormatMinor(minor int64, currency string) string {
	exp := CurrencyExponent(currency)
	neg := minor < 0
	u := uint64(minor)
	if neg {
		u = uint64(-minor)
	}
	s := strconv.FormatUint(u, 10)
	if exp > 0 {
		if len(s) <= exp {
			s = strings.Repeat("0", exp-len(s)+1) + s
		}
		s = s[:len(s)-exp] + "." + s[len(s)-exp:]
	}
	if neg {
		s = "-" + s
	}
	if currency != "" {
		s += " " + strings.ToUpper(currency)
	}
	return s
}
