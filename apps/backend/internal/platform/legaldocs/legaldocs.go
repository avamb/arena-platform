// Package legaldocs holds the organizer's terms of sale (line P, task P3a,
// plan docs/plans/hosted_page_legal_web_center_wave_plan_2026-10-13_ru.md
// section 4): the vocabulary of the versioned templates (legal_documents,
// migration 0163), the mini-parser that substitutes an organization's data
// into a template, and the renderer that stores what a buyer was shown
// (legal_renderings).
//
// The organizer never edits the text. A platform superadmin keeps one
// template per kind and language; the organizer's data (seller, promoter,
// refund policy, the seller's country) is substituted through an ALLOWLIST
// of placeholders (Placeholders). There is no code in a template: only
// {{key}}, {{#if key}} ... {{else}} ... {{/if}}, and a line-based markup of
// headings (## / ###), list items (- ) and paragraphs (blank lines). Every
// value and every piece of template text is HTML-escaped, so the result is
// HTML without scripts, styles or links, whoever wrote the template.
package legaldocs

// KindSaleTerms is the only document kind so far (CHECK legal_documents_kind_check).
const KindSaleTerms = "sale_terms"

// Kinds lists every document kind, in the CHECK's order.
var Kinds = []string{KindSaleTerms}

// Document statuses (CHECK legal_documents_status_check). A draft can be
// published; publishing retires the previous published version of the same
// kind and language. A published or retired version never changes.
const (
	StatusDraft     = "draft"
	StatusPublished = "published"
	StatusRetired   = "retired"
)

// Statuses lists every status, in the CHECK's order.
var Statuses = []string{StatusDraft, StatusPublished, StatusRetired}

// DefaultLocale is the language a missing translation falls back to.
const DefaultLocale = "en"

// Locales are the seven languages a template may be written in (CHECK
// legal_documents_locale_check): the languages of the letters and the PDF.
var Locales = []string{"en", "ru", "cs", "he", "es", "de", "fr"}

// Refund policy modes (CHECK organizations_refund_policy_mode_check,
// events_refund_policy_mode_check, legal_renderings_refund_mode_check).
// Names chosen in P3a: the plan of 2026-10-13 asks for "three ready modes"
// without naming them.
const (
	// RefundNone: the buyer cannot return or exchange a ticket. A
	// cancellation by the organizer is still refunded (the template says so
	// for every mode).
	RefundNone = "no_refund"
	// RefundUntilSalesEnd: the buyer may ask for a refund until ticket sales
	// for the session end.
	RefundUntilSalesEnd = "refund_until_sales_end"
	// RefundByLaw: refunds only in the cases and time limits the law of the
	// seller's country sets. It promises nothing beyond the law and denies
	// nothing the law grants, which is why it is the default.
	RefundByLaw = "refund_by_law"
)

// RefundModes lists every refund mode, in the CHECK's order.
var RefundModes = []string{RefundNone, RefundUntilSalesEnd, RefundByLaw}

// DefaultRefundMode applies when neither the event nor the organization
// chose a mode.
const DefaultRefundMode = RefundByLaw

// PlatformSellerName is the seller named in the terms of a merchant_of_record
// channel, where the platform sells as merchant of record. It equals
// delivery/templates.PlatformLegalName (a test keeps them equal) until the
// owner gives the platform's legal entity data.
const PlatformSellerName = "Arena Platform"

// KnownKind reports whether k is one of Kinds.
func KnownKind(k string) bool { return contains(Kinds, k) }

// KnownLocale reports whether l is one of Locales.
func KnownLocale(l string) bool { return contains(Locales, l) }

// KnownRefundMode reports whether m is one of RefundModes.
func KnownRefundMode(m string) bool { return contains(RefundModes, m) }

// ResolveRefundMode picks the mode the terms state: the event's, else the
// organization's, else DefaultRefundMode. An unknown stored value is
// skipped (the CHECK makes it impossible; this keeps the function total).
func ResolveRefundMode(event, org *string) string {
	for _, p := range []*string{event, org} {
		if p != nil && KnownRefundMode(*p) {
			return *p
		}
	}
	return DefaultRefundMode
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
