package sellerinfo

import "strings"

// idLabels are the words printed before the seller's registration and tax
// numbers in ticket letters and on the PDF ticket (HP-4b). The tickets page
// carries the same words for its five languages (tickets-page lib/i18n.ts).
type idLabels struct {
	Registration string
	Tax          string // a tax number of an unknown or "other" scheme
	VAT          string // eu_vat, gb_vat, il_vat
	EIN          string // us_ein
}

var idLabelsByLocale = map[string]idLabels{
	"en": {Registration: "Registration no.", Tax: "Tax ID", VAT: "VAT ID", EIN: "EIN"},
	"ru": {Registration: "Регистрационный номер", Tax: "Налоговый номер", VAT: "Номер плательщика НДС", EIN: "EIN"},
	"cs": {Registration: "Registrační číslo", Tax: "DIČ", VAT: "DIČ", EIN: "EIN"},
	"he": {Registration: "מספר רישום", Tax: "מזהה מס", VAT: "מספר עוסק במע״מ", EIN: "EIN"},
	"es": {Registration: "N.º de registro", Tax: "ID fiscal", VAT: "ID de IVA", EIN: "EIN"},
	"de": {Registration: "Registernummer", Tax: "Steuernummer", VAT: "USt-IdNr.", EIN: "EIN"},
	"fr": {Registration: "N° d'immatriculation", Tax: "Identifiant fiscal", VAT: "N° de TVA", EIN: "EIN"},
	"it": {Registration: "N. di registrazione", Tax: "Codice fiscale", VAT: "Partita IVA", EIN: "EIN"},
	"pl": {Registration: "Numer rejestrowy", Tax: "NIP", VAT: "NIP UE", EIN: "EIN"},
	"uk": {Registration: "Реєстраційний номер", Tax: "Податковий номер", VAT: "Номер платника ПДВ", EIN: "EIN"},
}

// IDsLine is the seller's identification line of a letter footer and a PDF
// ticket: "Registration no. 123 · VAT ID CZ123" in the given language
// (anything unknown falls back to English, a region subtag is ignored).
// Each number appears only when it is given; with neither the line is "".
func IDsLine(locale, registration, taxID, scheme string) string {
	lang := strings.ToLower(strings.TrimSpace(locale))
	if i := strings.IndexAny(lang, "-_"); i > 0 {
		lang = lang[:i]
	}
	l, ok := idLabelsByLocale[lang]
	if !ok {
		l = idLabelsByLocale["en"]
	}
	var parts []string
	if v := strings.TrimSpace(registration); v != "" {
		parts = append(parts, l.Registration+" "+v)
	}
	if v := strings.TrimSpace(taxID); v != "" {
		label := l.Tax
		switch strings.ToLower(strings.TrimSpace(scheme)) {
		case "eu_vat", "gb_vat", "il_vat":
			label = l.VAT
		case "us_ein":
			label = l.EIN
		}
		parts = append(parts, label+" "+v)
	}
	return strings.Join(parts, " · ")
}
