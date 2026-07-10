// errors.go defines the ValidationError type surfaced by SVG import.
// Each error class from §6 has a stable Code so callers (HTTP layer,
// audit log, i18n) can key off it deterministically.
package seating

import (
	"fmt"
	"strings"
)

// Error codes for SVG-import validation failures. Callers can rely on
// these string values remaining stable — they are part of the audit /
// UI contract.
const (
	ErrCanvasTooLarge          = "canvas_too_large"
	ErrCanvasMissing           = "canvas_missing"
	ErrInvalidSVG              = "invalid_svg"
	ErrSeatNotCircle           = "seat_not_circle"
	ErrRowMissingTitle         = "row_missing_title"
	ErrRowMissingSectorLabel   = "row_missing_sector_label"
	ErrSeatMissingNumber       = "seat_missing_number"
	ErrSeatColorUnmatched      = "seat_color_unmatched"
	ErrDuplicateSeat           = "duplicate_seat"
	ErrEmptyRow                = "empty_row"
	ErrEmptySection            = "empty_section"
	ErrPriceCategoryMissing    = "price_category_missing"
	ErrPriceCategoryEmpty      = "price_category_empty"
	ErrPriceCategoryUnlabelled = "price_category_unlabelled"
)

// WarningCode is emitted for §6 rules that are advisory rather than
// hard-fail. Currently: missing Legend group (rule 6).
const (
	WarnLegendMissing = "legend_missing"
)

// ValidationError is a single §6 rule violation. Element identifies the
// offending SVG element (id, label, or a synthetic descriptor) so the
// error message is actionable.
type ValidationError struct {
	Code    string
	Element string
	Detail  string
}

func (e ValidationError) Error() string {
	if e.Element == "" && e.Detail == "" {
		return e.Code
	}
	if e.Detail == "" {
		return fmt.Sprintf("%s: %s", e.Code, e.Element)
	}
	if e.Element == "" {
		return fmt.Sprintf("%s: %s", e.Code, e.Detail)
	}
	return fmt.Sprintf("%s: %s: %s", e.Code, e.Element, e.Detail)
}

// ValidationErrors is the batch return type from ImportSVG — the
// importer accumulates every §6 violation instead of aborting on the
// first, so the client can present the full list in one round-trip.
type ValidationErrors []ValidationError

func (v ValidationErrors) Error() string {
	if len(v) == 0 {
		return "seating: no validation errors"
	}
	parts := make([]string, len(v))
	for i, e := range v {
		parts[i] = e.Error()
	}
	return "seating: " + strings.Join(parts, "; ")
}

// HasCode reports whether v contains at least one ValidationError with
// the given Code — convenience for table-driven tests and for callers
// that want to key branching off a specific rule violation.
func (v ValidationErrors) HasCode(code string) bool {
	for _, e := range v {
		if e.Code == code {
			return true
		}
	}
	return false
}
