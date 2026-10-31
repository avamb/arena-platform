package hshop

import (
	"strconv"
	"strings"

	goi18n "github.com/nicksnyder/go-i18n/v2/i18n"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/i18n"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/shop"
)

// paymentDescriptionKey is the catalog key of the purpose line the payment
// provider's hosted page shows ("Einat Winery, order 1000001790").
const paymentDescriptionKey = "shop.payment.description"

// paymentDescription builds that line in the buyer's language. The number is
// the order's system_id, never its UUID. Without a bundle, with an empty or
// unsupported locale, or when the lookup fails, it is the English line (a
// locale outside the catalogs falls back to English inside the bundle).
func paymentDescription(b *i18n.Bundle, locale, org string, number int64) string {
	n := strconv.FormatInt(number, 10)
	fallback := org + ", order " + n
	if b == nil {
		return fallback
	}
	tag := strings.ToLower(strings.TrimSpace(locale))
	if i := strings.IndexAny(tag, "-_"); i > 0 {
		tag = tag[:i]
	}
	if tag == "" {
		tag = "en"
	}
	msg, err := b.LocalizerFor(tag).Localize(&goi18n.LocalizeConfig{
		MessageID:    paymentDescriptionKey,
		TemplateData: map[string]any{"Org": org, "Number": n},
	})
	if err != nil || msg == "" {
		return fallback
	}
	return msg
}

// reviewMessageKey is the catalog key of the line the buyer's page shows for a
// parked order, by cause.
var reviewMessageKey = map[string]string{
	shop.ReviewPaymentUnconfirmed: "shop.public.review_payment_unconfirmed",
	shop.ReviewLatePaymentNoStock: "shop.public.review_late_payment_no_stock",
}

// reviewMessageFallback is the English line when the catalog cannot answer.
var reviewMessageFallback = map[string]string{
	shop.ReviewPaymentUnconfirmed: "We are checking your payment and will write to you.",
	shop.ReviewLatePaymentNoStock: "Your payment arrived. We are checking the availability of your goods and will write to you.",
}

// reviewMessageFor is that line in the buyer's language (the locale the order
// was placed in; an unsupported or empty one is English).
func reviewMessageFor(b *i18n.Bundle, locale *string, reason string) string {
	fallback, ok := reviewMessageFallback[reason]
	if !ok {
		return ""
	}
	if b == nil {
		return fallback
	}
	tag := ""
	if locale != nil {
		tag = strings.ToLower(strings.TrimSpace(*locale))
	}
	if i := strings.IndexAny(tag, "-_"); i > 0 {
		tag = tag[:i]
	}
	if tag == "" {
		tag = "en"
	}
	msg, err := b.LocalizerFor(tag).Localize(&goi18n.LocalizeConfig{MessageID: reviewMessageKey[reason]})
	if err != nil || msg == "" {
		return fallback
	}
	return msg
}
