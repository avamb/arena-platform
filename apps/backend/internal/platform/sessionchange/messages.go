package sessionchange

import "strings"

// DefaultMessage is the text the organizer is offered, in the ORGANIZER's own
// language, to put under the standard wording of the letter their buyers get.
// It is a suggestion shown and editable in every client (bot, event centers,
// admin), never inserted silently: whatever the organizer leaves in the field
// is what is sent. It promises nothing about money — refunds are a separate
// conversation between organizer and buyer.
//
// kind is "change" (the session moved) or "cancel". An unknown locale falls
// back to English.
func DefaultMessage(kind, locale string) string {
	table, ok := defaultMessages[normalizeLocale(locale)]
	if !ok {
		table = defaultMessages["en"]
	}
	if kind == "cancel" {
		return table[1]
	}
	return table[0]
}

// KindForDefault maps change kinds to the DefaultMessage kind.
func KindForDefault(kinds []string) string {
	if Has(kinds, KindCancelled) {
		return "cancel"
	}
	return "change"
}

func normalizeLocale(locale string) string {
	locale = strings.ToLower(strings.TrimSpace(locale))
	if i := strings.IndexAny(locale, "-_"); i > 0 {
		locale = locale[:i]
	}
	return locale
}

// defaultMessages: [0] = moved, [1] = cancelled.
var defaultMessages = map[string][2]string{
	"en": {
		"We have changed the details of this event. Your ticket stays valid for the new date and place. We apologise for the inconvenience.",
		"Unfortunately this event has been cancelled. We apologise for the inconvenience. Please write to us and we will tell you what happens next.",
	},
	"ru": {
		"Мы изменили условия мероприятия. Ваш билет остаётся действительным на новую дату и место. Приносим извинения за неудобства.",
		"К сожалению, мероприятие отменено. Приносим извинения за неудобства. Напишите нам, и мы подскажем, что делать дальше.",
	},
	"cs": {
		"Změnili jsme podmínky akce. Vaše vstupenka zůstává platná pro nový termín a místo. Omlouváme se za nepříjemnosti.",
		"Akce bohužel byla zrušena. Omlouváme se za nepříjemnosti. Napište nám a my vám sdělíme, jak postupovat dál.",
	},
	"de": {
		"Wir haben die Details der Veranstaltung geändert. Ihre Eintrittskarte bleibt für den neuen Termin und Ort gültig. Wir entschuldigen uns für die Unannehmlichkeiten.",
		"Die Veranstaltung wurde leider abgesagt. Wir entschuldigen uns für die Unannehmlichkeiten. Schreiben Sie uns, dann sagen wir Ihnen, wie es weitergeht.",
	},
	"es": {
		"Hemos cambiado los detalles del evento. Su entrada sigue siendo válida para la nueva fecha y lugar. Pedimos disculpas por las molestias.",
		"Lamentablemente el evento ha sido cancelado. Pedimos disculpas por las molestias. Escríbanos y le diremos qué pasos seguir.",
	},
	"fr": {
		"Nous avons modifié les détails de l'événement. Votre billet reste valable pour la nouvelle date et le nouveau lieu. Nous nous excusons pour la gêne occasionnée.",
		"Malheureusement, l'événement a été annulé. Nous nous excusons pour la gêne occasionnée. Écrivez-nous et nous vous indiquerons la suite.",
	},
	"he": {
		"שינינו את פרטי האירוע. הכרטיס שלכם נשאר תקף למועד ולמקום החדשים. אנו מתנצלים על אי הנוחות.",
		"לצערנו האירוע בוטל. אנו מתנצלים על אי הנוחות. כתבו לנו ונסביר מה השלבים הבאים.",
	},
}
