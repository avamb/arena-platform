// i18n.go — locale negotiation and description-key translation helpers
// for the Bil24 compat gateway. Feature #478, spec section 6.
//
// The Bil24-supported locales are en, ru, he, cs. The negotiation
// function reduces a BCP-47 tag to its primary subtag and matches
// against that set; a channel-scoped default_locale acts as the
// mid-tier fallback, with hard fallback to "en".
//
//	ru-RU → ru
//	en-GB → en
//	he-IL → he
//	cs-CZ → cs
//	klingon or "" → channelDefault → "en"
//
// The Localize helper wraps go-i18n's Localizer + MessageID + template
// data lookup into a single call that always returns a non-empty
// string: on miss it returns the caller's english fallback. This lets
// per-command handlers emit description-key + english pairs and rely on
// the localization boundary to substitute the target-language text
// without special-casing missing bundles / missing keys in every branch.
//
// This package must NOT import internal/platform/httpserver — the
// dependency direction (feature #188) is HTTP → adapter. It IS allowed
// to import internal/platform/i18n which is a peer package under
// platform/.

package bil24compat

import (
	"strings"

	goi18n "github.com/nicksnyder/go-i18n/v2/i18n"
)

// Bil24SupportedLocales is the set of locales the Bil24 compat gateway
// serves description text in. Spec section 6.
var Bil24SupportedLocales = []string{"en", "ru", "he", "cs"}

// Bil24DefaultLocale is the final hard fallback used when neither the
// request locale nor the channel default_locale resolves to a supported
// locale.
const Bil24DefaultLocale = "en"

// NegotiateBil24Locale returns the best-matching Bil24 locale for the
// request. Priority:
//
//  1. reqLocale (the wire `locale` field, e.g. "ru-RU" from the WP
//     plugin) reduced to its primary subtag if that subtag is supported.
//  2. channelDefault (sales_channels.default_locale for the resolved
//     `fid`), reduced the same way.
//  3. Bil24DefaultLocale ("en").
//
// Both inputs may be empty; the fallback chain always yields a
// supported locale (guaranteed non-empty).
func NegotiateBil24Locale(reqLocale, channelDefault string) string {
	if l := canonicalBil24(reqLocale); l != "" {
		return l
	}
	if l := canonicalBil24(channelDefault); l != "" {
		return l
	}
	return Bil24DefaultLocale
}

// canonicalBil24 reduces the tag to its primary subtag and checks
// membership in Bil24SupportedLocales. Returns "" when not supported.
func canonicalBil24(tag string) string {
	tag = strings.ToLower(strings.TrimSpace(tag))
	if tag == "" {
		return ""
	}
	if dash := strings.IndexByte(tag, '-'); dash > 0 {
		tag = tag[:dash]
	}
	if under := strings.IndexByte(tag, '_'); under > 0 {
		tag = tag[:under]
	}
	for _, s := range Bil24SupportedLocales {
		if s == tag {
			return tag
		}
	}
	return ""
}

// LocalizeDescription resolves a bil24.* message key against the
// supplied Localizer, substituting the provided template data. When
// loc is nil, or the key is absent, or the lookup fails for any other
// reason, the english fallback is returned unchanged so the wire byte
// surface never becomes an empty string.
//
// The english argument SHOULD match the en.toml value for the key so
// that missing-bundle deployments keep byte-identical descriptions.
// The four spec buckets already define these fallbacks (see
// error_mapping.go and Desc* constants below).
func LocalizeDescription(loc *goi18n.Localizer, key, english string, params map[string]any) string {
	if loc == nil || key == "" {
		return english
	}
	var td interface{}
	if len(params) > 0 {
		td = params
	}
	msg, err := loc.Localize(&goi18n.LocalizeConfig{
		MessageID:    key,
		TemplateData: td,
	})
	if err != nil || msg == "" {
		return english
	}
	return msg
}

// Bil24DescriptionKeys is the canonical list of bil24.* message IDs
// the compat gateway may surface. Spec section 6. Kept as a package
// symbol so completeness tests (every key present in every locale
// bundle) can iterate it without a text-file scrape.
var Bil24DescriptionKeys = []string{
	"bil24.ok",
	"bil24.seat_taken",
	"bil24.category_sold_out",
	"bil24.session_expired",
	"bil24.sales_closed",
	"bil24.promo_invalid",
	// Spec §7.6 (feature #491): ADD_PROMO_CODES / CHECK_KDP name the exact
	// reason a code was refused, because the WordPress checkout renders the
	// description verbatim next to the promo input.
	"bil24.promo_not_found",
	"bil24.promo_expired",
	"bil24.promo_not_yet_valid",
	"bil24.promo_not_applicable",
	"bil24.promo_min_order",
	"bil24.hold_expired",
	"bil24.order_not_found",
	"bil24.currency_mismatch",
	"bil24.pricing_mode_unsupported",
	"bil24.line_wrong_session",
	"bil24.order_cancelled",
	"bil24.use_refund_ticket",
	"bil24.unknown_command",
	"bil24.invalid_request",
	"bil24.not_found",
	"bil24.unauthorized",
	"bil24.internal",
}
