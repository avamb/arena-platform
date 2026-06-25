// line_category.go captures the canonical event-report line categories as
// pure domain identifiers.
//
// The aggregation worker in internal/platform/reporting emits one
// event_report_lines row per category, and the delivery worker in
// internal/platform/reportdelivery renders one summary block per category.
// Keeping the identifiers and the canonical order centralised here ensures
// the two sides cannot drift.
package reporting

// Line-category identifiers. The string values are the canonical wire-format
// stored in the event_report_lines.category column; do NOT change them
// without a data migration.
const (
	// CategorySales is the gross/net sale aggregation for the event.
	CategorySales = "sales"
	// CategoryRefunds is the refund aggregation for the event.
	CategoryRefunds = "refunds"
	// CategoryComplimentary is the complimentary-grant aggregation.
	CategoryComplimentary = "complimentary"
	// CategoryScans is the scan-event aggregation.
	CategoryScans = "scans"
	// CategoryCommissions is derived from sales as gross - net
	// (= platform_fee + provider_fee).
	CategoryCommissions = "commissions"
	// CategoryPayouts is derived from sales as net (organiser payable).
	CategoryPayouts = "payouts"
)

// AllLineCategories is the canonical insert/render order of report line
// categories. Workers iterate this slice to guarantee deterministic output.
var AllLineCategories = []string{
	CategorySales,
	CategoryRefunds,
	CategoryComplimentary,
	CategoryScans,
	CategoryCommissions,
	CategoryPayouts,
}

// IsKnownLineCategory reports whether the given string is one of the
// canonical event-report line categories.
func IsKnownLineCategory(category string) bool {
	for _, c := range AllLineCategories {
		if c == category {
			return true
		}
	}
	return false
}
