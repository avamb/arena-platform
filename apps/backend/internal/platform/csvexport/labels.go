package csvexport

import (
	"net/http"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/i18n"
)

// Column is a spreadsheet column with a localized label. The label is the
// only localized thing in a file: values stay as the API spells them.
type Column int

// The columns of the platform's exports (spec 35 §5.8).
const (
	ColOrder Column = iota
	ColOrderStatus
	ColDate
	ColBuyer
	ColEmail
	ColPhone
	ColCategory
	ColPrice
	ColCurrency
	ColBarcode
	ColTicketStatus
	ColEntered
	ColChannel
	ColPromoCode
	ColSession
	ColKind
	ColPlacesTotal
	ColPlacesAvailable
	ColPlacesHeld
	ColPlacesSold
	ColPlacesSoldUpstream
	ColPlacesUnavailable
	ColPaidTickets
	ColRevenue
	ColOnSale
	ColDiscount
)

// SupportedLocales are the header languages, in the order they are offered.
var SupportedLocales = []string{"en", "ru", "es"}

// DefaultLocale is the header language when the request names none.
const DefaultLocale = "en"

var labels = map[string]map[Column]string{
	"en": {
		ColOrder: "Order", ColOrderStatus: "Order status", ColDate: "Date",
		ColBuyer: "Buyer", ColEmail: "E-mail", ColPhone: "Phone",
		ColCategory: "Category", ColPrice: "Price", ColCurrency: "Currency",
		ColBarcode: "Barcode", ColTicketStatus: "Ticket status", ColEntered: "Entered",
		ColChannel: "Channel", ColPromoCode: "Promo code", ColSession: "Session",
		ColKind: "Kind", ColPlacesTotal: "Places", ColPlacesAvailable: "Available",
		ColPlacesHeld: "Held", ColPlacesSold: "Sold", ColPlacesSoldUpstream: "Sold elsewhere",
		ColPlacesUnavailable: "Unavailable", ColPaidTickets: "Paid tickets",
		ColRevenue: "Revenue", ColOnSale: "On sale", ColDiscount: "Discount",
	},
	"ru": {
		ColOrder: "Заказ", ColOrderStatus: "Статус заказа", ColDate: "Дата",
		ColBuyer: "Покупатель", ColEmail: "E-mail", ColPhone: "Телефон",
		ColCategory: "Категория", ColPrice: "Цена", ColCurrency: "Валюта",
		ColBarcode: "Штрихкод", ColTicketStatus: "Статус билета", ColEntered: "Вошёл",
		ColChannel: "Канал", ColPromoCode: "Промокод", ColSession: "Сеанс",
		ColKind: "Тип", ColPlacesTotal: "Мест", ColPlacesAvailable: "Свободно",
		ColPlacesHeld: "Удержано", ColPlacesSold: "Продано", ColPlacesSoldUpstream: "Продано вне системы",
		ColPlacesUnavailable: "Недоступно", ColPaidTickets: "Оплачено билетов",
		ColRevenue: "Выручка", ColOnSale: "В продаже", ColDiscount: "Скидка",
	},
	"es": {
		ColOrder: "Pedido", ColOrderStatus: "Estado del pedido", ColDate: "Fecha",
		ColBuyer: "Comprador", ColEmail: "E-mail", ColPhone: "Teléfono",
		ColCategory: "Categoría", ColPrice: "Precio", ColCurrency: "Moneda",
		ColBarcode: "Código de barras", ColTicketStatus: "Estado de la entrada", ColEntered: "Ha entrado",
		ColChannel: "Canal", ColPromoCode: "Código promocional", ColSession: "Sesión",
		ColKind: "Tipo", ColPlacesTotal: "Plazas", ColPlacesAvailable: "Libres",
		ColPlacesHeld: "Retenidas", ColPlacesSold: "Vendidas", ColPlacesSoldUpstream: "Vendidas fuera del sistema",
		ColPlacesUnavailable: "No disponibles", ColPaidTickets: "Entradas pagadas",
		ColRevenue: "Ingresos", ColOnSale: "En venta", ColDiscount: "Descuento",
	},
}

// Label is the column's label in locale, falling back to English for an
// unknown locale.
func Label(locale string, c Column) string {
	if l, ok := labels[locale][c]; ok {
		return l
	}
	return labels[DefaultLocale][c]
}

// Header renders the labels of cols in locale, ready for Writer.WriteHeader.
func Header(locale string, cols ...Column) []string {
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = Label(locale, c)
	}
	return out
}

// LocaleFromRequest picks the header language: `?locale=` first (the spelling
// the change-impact route uses), then the platform's `?lang=`, then
// Accept-Language, then English. An unknown tag is never an error.
func LocaleFromRequest(r *http.Request) string {
	lang := r.URL.Query().Get("locale")
	if lang == "" {
		lang = r.URL.Query().Get("lang")
	}
	return i18n.NegotiateLocale(r.Header.Get("Accept-Language"), lang, "", DefaultLocale, SupportedLocales)
}
