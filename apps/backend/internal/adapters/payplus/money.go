// money.go — the ONE place PayPlus amounts cross between arena's minor units
// (orders.total, agorot / cents) and PayPlus's decimal major units ("12.5").
//
// Every currency PayPlus clears for Israeli merchants (ILS, USD, EUR, GBP)
// has two decimals, so the exponent is fixed at 2. Parsing is exact decimal
// string arithmetic, never float: 0.29 must become 29, not 28.999….
package payplus

import (
	"fmt"
	"strconv"
	"strings"
)

// MinorToMajor renders minor units as PayPlus's decimal major amount without
// trailing zeros: 1250 → "12.5", 1200 → "12", 1205 → "12.05".
func MinorToMajor(minor int64) string {
	sign := ""
	if minor < 0 {
		sign = "-"
		minor = -minor
	}
	whole, frac := minor/100, minor%100
	switch {
	case frac == 0:
		return fmt.Sprintf("%s%d", sign, whole)
	case frac%10 == 0:
		return fmt.Sprintf("%s%d.%d", sign, whole, frac/10)
	default:
		return fmt.Sprintf("%s%d.%02d", sign, whole, frac)
	}
}

// MajorToMinor parses a PayPlus amount ("12", "12.5", "12.05", "1e1") into
// minor units. More than two decimals are rounded half away from zero, which
// only a float-printing sender produces ("12.049999999999999").
func MajorToMinor(major string) (int64, bool) {
	s := strings.TrimSpace(major)
	if s == "" {
		return 0, false
	}
	if strings.ContainsAny(s, "eE") {
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return 0, false
		}
		s = strconv.FormatFloat(f, 'f', -1, 64)
	}
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(strings.TrimPrefix(s, "-"), "+")
	whole, frac, _ := strings.Cut(s, ".")
	if whole == "" {
		whole = "0"
	}
	if !digits(whole) || (frac != "" && !digits(frac)) {
		return 0, false
	}
	w, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return 0, false
	}
	frac += "000"
	cents, _ := strconv.ParseInt(frac[:2], 10, 64)
	v := w*100 + cents
	if frac[2] >= '5' {
		v++
	}
	if neg {
		v = -v
	}
	return v, true
}

func digits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}
