// Package csvexport is the single CSV writer of the platform
// (08_architecture/35_telegram_event_center_full_ru.md §5.1, EC-07).
//
// Every spreadsheet an organizer downloads — sales, summaries, promo code
// usage — goes through this writer, because the file is opened in Excel,
// LibreOffice or Numbers, and each of them rewrites a naive CSV:
//
//   - a 13-digit barcode becomes 4,60005E+12;
//   - a phone loses its leading zero or its "+", or becomes a formula;
//   - a buyer called `=HYPERLINK("http://…")` runs as a formula.
//
// The rules, all applied by the writer and never by a caller:
//
//   - UTF-8 with a BOM, separator `;`, CRLF line endings;
//   - every text value is quoted, an inner quote is doubled;
//   - a value that is digits only and at least ExcelTextDigits long, a
//     value with a leading zero, and a phone-shaped value starting with `+`
//     are written as Excel text: `="4600051000001"` — the spreadsheet shows
//     the digits exactly as stored;
//   - any other value starting with `=`, `+`, `-`, `@`, a TAB or a CR gets
//     a `'` prefix (CSV-injection defence);
//   - money is a decimal with a dot and two digits, the currency is its own
//     column; a date is `YYYY-MM-DD HH:MM` in the venue's zone.
//
// The writer streams: rows go to the underlying io.Writer as they are
// written, it never holds the file. Column labels are localized through
// Header / Labels (en, ru, es).
package csvexport

import (
	"bufio"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Separator is the field separator: `;`, what Excel expects in the locales
// the platform sells in (a comma is the decimal separator there).
const Separator = ';'

// ExcelTextDigits is the length from which a digits-only value is written as
// Excel text so it is never rendered in scientific notation. Excel keeps 15
// significant digits but already switches to 1,23E+11 at 12 digits in a
// default-width column; 10 is the owner's margin (plan v3 §5.1).
const ExcelTextDigits = 10

// bom is the UTF-8 byte-order mark Excel needs to read the file as UTF-8
// instead of the machine's code page.
const bom = "\xEF\xBB\xBF"

// eol is the CRLF line ending of RFC 4180 and of Excel.
const eol = "\r\n"

// DateTimeLayout is the human date format of every date column: the venue's
// local time, minutes, no zone suffix (the zone is implied by the venue).
const DateTimeLayout = "2006-01-02 15:04"

// Cell is one value of a row. Build it with the typed constructors; the
// zero Cell is an empty field.
type Cell struct {
	raw  string
	kind cellKind
}

type cellKind uint8

const (
	kindEmpty   cellKind = iota
	kindText             // quoted, with the Excel-text and injection rules
	kindNumeric          // written bare: money and plain small numbers
)

// Text is a free-form value: a name, an e-mail, a status, a barcode, a
// phone. The Excel-text and injection rules are applied when it is written.
func Text(s string) Cell {
	if s == "" {
		return Cell{}
	}
	return Cell{raw: s, kind: kindText}
}

// TextPtr is Text for an optional value; nil is an empty field.
func TextPtr(s *string) Cell {
	if s == nil {
		return Cell{}
	}
	return Text(*s)
}

// Number is an identifier or a count: an order number, a quantity. A short
// number is written bare, so a count column sums in a spreadsheet; a long one
// (an order number of ten digits) goes through the Excel-text rule and keeps
// every digit.
func Number(n int64) Cell {
	s := strconv.FormatInt(n, 10)
	if IsExcelText(s) {
		return Text(s)
	}
	return Cell{raw: s, kind: kindNumeric}
}

// Money is an amount in minor units, written as a decimal with a dot and two
// digits (`1890` -> `18.90`). The currency belongs in its own column.
func Money(minor int64) Cell {
	return Cell{raw: FormatMoney(minor), kind: kindNumeric}
}

// MoneyPtr is Money for an optional amount; nil is an empty field.
func MoneyPtr(minor *int64) Cell {
	if minor == nil {
		return Cell{}
	}
	return Money(*minor)
}

// FormatMoney renders minor units as a decimal with exactly two digits.
func FormatMoney(minor int64) string {
	sign := ""
	if minor < 0 {
		sign, minor = "-", -minor
	}
	cents := minor % 100
	frac := strconv.FormatInt(cents, 10)
	if cents < 10 {
		frac = "0" + frac
	}
	return sign + strconv.FormatInt(minor/100, 10) + "." + frac
}

// DateTime is an instant rendered in loc (the venue's zone) as
// `YYYY-MM-DD HH:MM`. A nil loc means UTC. The zero time is an empty field.
func DateTime(t time.Time, loc *time.Location) Cell {
	if t.IsZero() {
		return Cell{}
	}
	if loc == nil {
		loc = time.UTC
	}
	// A human spreadsheet column in the venue's local time, by design
	// without a zone suffix (spec 35 §5.1).
	// allow:timeformat: not an API timestamp, a spreadsheet date column.
	return Cell{raw: t.In(loc).Format(DateTimeLayout), kind: kindText}
}

// DateTimePtr is DateTime for an optional instant; nil is an empty field.
func DateTimePtr(t *time.Time, loc *time.Location) Cell {
	if t == nil {
		return Cell{}
	}
	return DateTime(*t, loc)
}

// YesNo is a flag written as the API's own words, `yes` / `no` — never a
// localized word, so a column filters the same way whatever the header
// language (spec 35 §5.1 keeps enum values as the API spells them).
func YesNo(b bool) Cell {
	if b {
		return Text("yes")
	}
	return Text("no")
}

// Writer streams CSV rows to an io.Writer. It is not safe for concurrent use.
type Writer struct {
	bw      *bufio.Writer
	started bool
	err     error
}

// NewWriter wraps w. The BOM is written with the first row.
func NewWriter(w io.Writer) *Writer {
	return &Writer{bw: bufio.NewWriter(w)}
}

// WriteHeader writes the column labels as the first row. Labels are quoted
// like any text; use Header to localize them.
func (w *Writer) WriteHeader(labels []string) error {
	cells := make([]Cell, len(labels))
	for i, l := range labels {
		cells[i] = Cell{raw: l, kind: kindText}
	}
	return w.WriteRow(cells...)
}

// WriteRow writes one row. A Writer that has failed keeps returning the
// first error and writes nothing more.
func (w *Writer) WriteRow(cells ...Cell) error {
	if w.err != nil {
		return w.err
	}
	if !w.started {
		w.started = true
		if _, err := w.bw.WriteString(bom); err != nil {
			return w.fail(err)
		}
	}
	for i, c := range cells {
		if i > 0 {
			if err := w.bw.WriteByte(Separator); err != nil {
				return w.fail(err)
			}
		}
		if _, err := w.bw.WriteString(encode(c)); err != nil {
			return w.fail(err)
		}
	}
	if _, err := w.bw.WriteString(eol); err != nil {
		return w.fail(err)
	}
	return nil
}

// Flush pushes buffered rows to the underlying writer. Call it after a
// batch of rows (so a streaming response actually streams) and at the end.
func (w *Writer) Flush() error {
	if w.err != nil {
		return w.err
	}
	if err := w.bw.Flush(); err != nil {
		return w.fail(err)
	}
	return nil
}

func (w *Writer) fail(err error) error {
	w.err = err
	return err
}

// encode renders one cell by the package rules.
func encode(c Cell) string {
	switch c.kind {
	case kindEmpty:
		return `""`
	case kindNumeric:
		return c.raw
	}
	v := c.raw
	if IsExcelText(v) {
		// `="…"` is a formula that evaluates to the literal text, so the
		// spreadsheet never reinterprets the digits. The guard admits only
		// digits, a leading `+`, spaces, dashes and parentheses, so no quote
		// or separator can appear inside.
		return `="` + v + `"`
	}
	if needsInjectionPrefix(v) {
		v = "'" + v
	}
	return `"` + strings.ReplaceAll(v, `"`, `""`) + `"`
}

// IsExcelText reports whether a value is written as `="…"`: digits only and
// at least ExcelTextDigits long (a barcode, a long order number), digits
// with a leading zero (a local phone, a zero-padded code — a lone `0` is a
// number, not a padded one), or a phone-shaped value starting with `+`
// (`+34 600 111 222`).
func IsExcelText(v string) bool {
	if v == "" {
		return false
	}
	if v[0] == '+' {
		return isPhoneShaped(v[1:])
	}
	if !isDigits(v) {
		return false
	}
	return (v[0] == '0' && len(v) > 1) || len(v) >= ExcelTextDigits
}

func isDigits(v string) bool {
	if v == "" {
		return false
	}
	for _, r := range v {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// isPhoneShaped accepts what follows a `+` in a phone number: digits with
// optional spaces, dashes, dots and parentheses, at least one digit.
func isPhoneShaped(rest string) bool {
	digits := 0
	for _, r := range rest {
		switch {
		case r >= '0' && r <= '9':
			digits++
		case r == ' ' || r == '-' || r == '.' || r == '(' || r == ')':
		default:
			return false
		}
	}
	return digits > 0
}

// needsInjectionPrefix reports whether a spreadsheet would read the value
// as a formula or a command: it starts with `=`, `+`, `-`, `@`, TAB or CR.
// Leading spaces are skipped first — Excel trims them before deciding.
func needsInjectionPrefix(v string) bool {
	trimmed := strings.TrimLeftFunc(v, func(r rune) bool { return r == ' ' || unicode.Is(unicode.Zs, r) })
	if trimmed == "" {
		return false
	}
	switch trimmed[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return true
	}
	return false
}
