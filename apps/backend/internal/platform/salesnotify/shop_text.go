package salesnotify

// shop_text.go — the shop announcements speak the ORGANIZATION's language
// (organizations.default_locale), not a fixed English: Einat Winery's
// logistician reads Russian. The texts are the salesnotify.shop.* keys of the
// shared i18n bundle (en/ru/cs/he/es); a language without a key falls back to
// English through go-i18n. The ticket-sale messages (messages.go) stay English.
//
// The bundle is built once per process on first use, the way contactform
// does it, so the Format* functions stay plain functions of their input.

import (
	"strings"
	"sync"

	goi18n "github.com/nicksnyder/go-i18n/v2/i18n"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/i18n"
)

var (
	shopBundleOnce sync.Once
	shopBundle     *i18n.Bundle
)

// shopLocale is the language of one rendering: the language subtag of the
// organization's locale, "en" when it is empty.
func shopLocale(locale string) string {
	l := strings.ToLower(strings.TrimSpace(locale))
	if i := strings.IndexAny(l, "-_"); i > 0 {
		l = l[:i]
	}
	if l == "" {
		return "en"
	}
	return l
}

// say renders one salesnotify.shop.* text. Values in data are used verbatim:
// callers escape user-supplied strings with esc first, because every message
// is sent with HTML parse mode. An unknown key answers the key itself.
func say(locale, id string, data map[string]any) string {
	shopBundleOnce.Do(func() {
		if b, err := i18n.NewBundle(); err == nil {
			shopBundle = b
		}
	})
	if shopBundle == nil {
		return id
	}
	var td any
	if len(data) > 0 {
		td = data
	}
	out, err := shopBundle.LocalizerFor(shopLocale(locale)).Localize(&goi18n.LocalizeConfig{MessageID: id, TemplateData: td})
	if err != nil || out == "" {
		return id
	}
	return out
}

// shopKeys lists every salesnotify.shop.* key, for the completeness test.
var shopKeys = []string{
	"salesnotify.shop.new_order",
	"salesnotify.shop.order_number",
	"salesnotify.shop.total",
	"salesnotify.shop.delivery_none",
	"salesnotify.shop.delivery_pickup",
	"salesnotify.shop.delivery_shipping",
	"salesnotify.shop.courier_form",
	"salesnotify.shop.map_google",
	"salesnotify.shop.map_waze",
	"salesnotify.shop.items",
	"salesnotify.shop.items_more",
	"salesnotify.shop.item_line",
	"salesnotify.shop.shipping",
	"salesnotify.shop.discount",
	"salesnotify.shop.note",
	"salesnotify.shop.courier_order",
	"salesnotify.shop.courier_pickup",
	"salesnotify.shop.courier_none",
	"salesnotify.shop.courier_item",
	"salesnotify.shop.courier_total",
	"salesnotify.shop.courier_note",
	"salesnotify.shop.courier_map_google",
	"salesnotify.shop.courier_map_waze",
	"salesnotify.shop.refund_title",
	"salesnotify.shop.refunded",
	"salesnotify.shop.refunded_so_far",
	"salesnotify.shop.review_title",
	"salesnotify.shop.review_paid",
	"salesnotify.shop.review_body",
	"salesnotify.shop.review_unconfirmed_amount",
	"salesnotify.shop.review_unconfirmed_body",
}
