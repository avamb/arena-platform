package salesnotify

import (
	"fmt"
	"strings"
)

// shopCurrencySymbols are the currencies a shop message prints as a symbol.
// Any other code keeps its ISO letters after the number.
var shopCurrencySymbols = map[string]string{
	"ILS": "₪", "EUR": "€", "USD": "$", "GBP": "£", "CZK": "Kč", "GEL": "₾",
}

// moneyFormat is the number style of one language.
type moneyFormat struct {
	group   string
	decimal string
}

// shopMoneyFormats: en and he print "1,234.50"; ru, es and cs print
// "1 234,50" with a no-break space as the group separator so a figure never
// wraps.
var shopMoneyFormats = map[string]moneyFormat{
	"en": {group: ",", decimal: "."},
	"he": {group: ",", decimal: "."},
	"ru": {group: "\u00a0", decimal: ","},
	"es": {group: "\u00a0", decimal: ","},
	"cs": {group: "\u00a0", decimal: ","},
}

// formatShopMoney renders an amount in minor units for a message in locale:
// "450,00 ₪" for ru, "450.00 ₪" for en, "1 234,50 €" for es. The symbol
// follows the number after a no-break space. An unknown currency keeps its
// ISO code in the symbol's place; an unknown language prints as English.
func formatShopMoney(amount int64, currency, locale string) string {
	f, ok := shopMoneyFormats[shopLocale(locale)]
	if !ok {
		f = shopMoneyFormats["en"]
	}
	neg := amount < 0
	if neg {
		amount = -amount
	}
	digits := fmt.Sprintf("%d", amount/100)
	var grouped strings.Builder
	for i, r := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			grouped.WriteString(f.group)
		}
		grouped.WriteRune(r)
	}
	out := fmt.Sprintf("%s%s%02d", grouped.String(), f.decimal, amount%100)
	if neg {
		out = "-" + out
	}
	cur := strings.ToUpper(strings.TrimSpace(currency))
	if sym, ok := shopCurrencySymbols[cur]; ok {
		cur = sym
	}
	if cur == "" {
		return out
	}
	return out + "\u00a0" + cur
}
