package hshop

import (
	"strconv"
	"strings"

	goi18n "github.com/nicksnyder/go-i18n/v2/i18n"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/i18n"
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
