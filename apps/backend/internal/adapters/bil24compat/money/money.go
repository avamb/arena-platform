// Package money is the ONE place where the Bil24-compatible gateway converts
// between the two money representations it has to live with (design authority:
// 08_architecture/20_bil24_gateway_money_units_spec_ru.md, spec W1-M).
//
//   - On the WIRE (every /compat/bil24/* command, the bil24_wp webhooks and the
//     MACS order.paid / ticket.refunded payloads) money is a JSON number in
//     MAJOR currency units with at most two decimals: 525, 5.25, 18.9. That is
//     what real Bil24 sends and what the WordPress plugins write straight into
//     WooCommerce prices.
//   - In the DATABASE and the domain money is a bigint in MINOR units. Every
//     calculation — channel fee_percent, promo discounts, cart sums — is done
//     in minor units and converted exactly once, when the response is encoded.
//
// No other package may open-code a "/ 100" or "* 100" money conversion; a
// static guardrail (tests/staticanalysis) enforces that for hbil24, macs and
// bil24wire.
package money

import (
	"math"
	"strings"
)

// DefaultScale is the minor-units-per-major-unit factor of every currency the
// wave-1 channels sell in (CZK, ILS, EUR — ISO-4217 exponent 2).
const DefaultScale int64 = 100

// zeroExponent lists the ISO-4217 currencies whose minor unit IS the major
// unit (exponent 0). Wave-1 channels never sell in these, but ScaleFor exists
// so that supporting one later is a table edit, not a rewrite of every call
// site.
var zeroExponent = map[string]int64{
	"BIF": 1, "CLP": 1, "DJF": 1, "GNF": 1, "ISK": 1, "JPY": 1,
	"KMF": 1, "KRW": 1, "PYG": 1, "RWF": 1, "UGX": 1, "UYI": 1,
	"VND": 1, "VUV": 1, "XAF": 1, "XOF": 1, "XPF": 1, "HUF": 1,
}

// ScaleFor returns how many minor units make one major unit of the given
// ISO-4217 alphabetic code. Unknown or empty codes fall back to DefaultScale,
// which is the right answer for every currency arena has ever priced in.
//
// HUF is listed as exponent 0 on purpose: ISO-4217 gives it 2, but every
// Hungarian ticketing integration (Bil24 included) prices in whole forints.
func ScaleFor(currency string) int64 {
	if s, ok := zeroExponent[strings.ToUpper(strings.TrimSpace(currency))]; ok {
		return s
	}
	return DefaultScale
}

// Major converts a minor-unit amount to the float major units the wire uses,
// at the default scale. The result is rounded to two decimals so that float
// division artefacts (18.899999999999999) never reach the JSON encoder.
func Major(minor int64) float64 { return MajorScale(minor, DefaultScale) }

// MajorScale is Major with an explicit scale (see ScaleFor).
func MajorScale(minor int64, scale int64) float64 {
	if scale <= 1 {
		return float64(minor)
	}
	v := float64(minor) / float64(scale)
	return math.Round(v*float64(scale)) / float64(scale)
}

// MajorPtr converts an optional minor-unit amount, preserving nil.
func MajorPtr(minor *int64) *float64 {
	if minor == nil {
		return nil
	}
	v := Major(*minor)
	return &v
}

// Minor converts a major-unit wire amount into the integer minor units arena
// stores, rounding half AWAY FROM ZERO (math.Round) so the classic
// 24.999999 → 2499 truncation artefact cannot happen.
func Minor(major float64) int64 { return MinorScale(major, DefaultScale) }

// MinorScale is Minor with an explicit scale (see ScaleFor).
func MinorScale(major float64, scale int64) int64 {
	if scale <= 1 {
		return int64(math.Round(major))
	}
	return int64(math.Round(major * float64(scale)))
}

// MinorPtr converts an optional major-unit wire amount, preserving nil.
func MinorPtr(major *float64) *int64 {
	if major == nil {
		return nil
	}
	v := Minor(*major)
	return &v
}

// RoundMinor collapses a minor-unit amount that was computed in floating point
// (a percentage fee, a prorated share) back onto whole minor units, again
// rounding half away from zero. Callers keep their arithmetic in minor units
// and hand the result here before encoding it with Major.
func RoundMinor(minor float64) int64 { return int64(math.Round(minor)) }
