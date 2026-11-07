// Package xlsxexport writes a one-sheet Excel workbook (.xlsx) from the same
// cells the CSV exports use (csvexport.Cell), so a row is rendered once and
// served in both formats.
//
// It exists because a CSV is opened by Excel with the separator of the
// regional settings: with English settings the ";" of csvexport is not one,
// and every row lands in column A ("sep=;" fixes that but breaks the BOM and
// with it the Cyrillic text). An .xlsx has no separator and no encoding to
// guess.
//
// The writer is deliberately small: a zip of six XML parts, inline strings
// (no shared-string table) and four cell styles. Rules:
//
//   - A Text column (barcode, order number, phone, ticket number) is text in
//     the FILE, not by a trick: every cell is an inline string with the "@"
//     number format (numFmt 49), and the column itself carries that style, so
//     a value typed or pasted into it later stays text too. Excel never shows
//     4.6E+12, never drops a leading zero, and is told not to flag the cells
//     as "number stored as text".
//   - A text cell is never a formula: inline strings are not evaluated, so
//     the CSV's injection prefix is not needed and not written.
//   - A numeric cell (money, a short count) in another column is a number.
//   - The header row is bold and frozen, the sheet has an autofilter over the
//     header and the data, and each column is as wide as its longest value
//     (within limits).
//   - Characters XML 1.0 cannot carry (control characters) are dropped and a
//     value is cut to Excel's 32 767 characters per cell.
//
// The output is deterministic: the same sheet gives the same bytes.
package xlsxexport

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/csvexport"
)

// ContentType is the media type of an .xlsx file.
const ContentType = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"

// MaxRows is Excel's row limit, the header included.
const MaxRows = 1_048_576

// Width limits of a column, in characters.
const (
	minWidth = 8
	maxWidth = 60
)

// maxCellUnits is Excel's limit of characters (UTF-16 units) in one cell.
const maxCellUnits = 32_767

// Cell styles (indexes into cellXfs of styles.xml).
const (
	styleDefault    = 0
	styleHeader     = 1 // bold
	styleText       = 2 // "@" text format
	styleHeaderText = 3 // bold, "@"
)

// zipTime is the fixed modification time of every part, so the bytes depend
// on the sheet only.
var zipTime = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// ErrTooManyRows is returned for a sheet Excel could not open.
var ErrTooManyRows = errors.New("xlsxexport: more rows than Excel holds")

// Column is one column of the sheet.
type Column struct {
	Header string
	// Text makes the column text: its cells and the column style use the "@"
	// format, so identifiers keep every digit.
	Text bool
}

// Sheet is the content of the workbook.
type Sheet struct {
	// Name is the tab name; characters Excel refuses are removed and it is
	// cut to 31 characters. Empty means "Sheet1".
	Name    string
	Columns []Column
	Rows    [][]csvexport.Cell
}

// Build returns the workbook as bytes.
func Build(s Sheet) ([]byte, error) {
	var buf bytes.Buffer
	if err := Write(&buf, s); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Write writes the workbook to w.
func Write(w io.Writer, s Sheet) error { return WriteBook(w, []Sheet{s}) }

// BuildBook returns a workbook of several sheets as bytes, in the order
// given; the first one is the sheet Excel opens on.
func BuildBook(sheets []Sheet) ([]byte, error) {
	var buf bytes.Buffer
	if err := WriteBook(&buf, sheets); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// WriteBook writes a workbook of one or more sheets to w. Tab names are
// made unique (a repeat gets " 2", " 3", ...), each sheet has its own frozen
// header and autofilter.
func WriteBook(w io.Writer, sheets []Sheet) error {
	if len(sheets) == 0 {
		return errors.New("xlsxexport: a workbook needs at least one sheet")
	}
	names := make([]string, len(sheets))
	used := map[string]bool{}
	for i, s := range sheets {
		if len(s.Rows)+1 > MaxRows {
			return ErrTooManyRows
		}
		if len(s.Columns) == 0 {
			return errors.New("xlsxexport: a sheet needs at least one column")
		}
		name := SheetName(s.Name)
		for n := 2; used[strings.ToLower(name)]; n++ {
			suffix := " " + strconv.Itoa(n)
			base := []rune(SheetName(s.Name))
			if len(base)+len(suffix) > 31 {
				base = base[:31-len(suffix)]
			}
			name = string(base) + suffix
		}
		used[strings.ToLower(name)] = true
		names[i] = name
	}
	zw := zip.NewWriter(w)
	type part struct {
		name string
		body []byte
	}
	parts := []part{
		{"[Content_Types].xml", []byte(contentTypesFor(len(sheets)))},
		{"_rels/.rels", []byte(rootRels)},
		{"xl/workbook.xml", workbookFor(names, sheets)},
		{"xl/_rels/workbook.xml.rels", []byte(workbookRelsFor(len(sheets)))},
		{"xl/styles.xml", []byte(styles)},
	}
	for i, s := range sheets {
		parts = append(parts, part{"xl/worksheets/sheet" + strconv.Itoa(i+1) + ".xml", sheet(s, i == 0)})
	}
	for _, p := range parts {
		f, err := zw.CreateHeader(&zip.FileHeader{Name: p.name, Method: zip.Deflate, Modified: zipTime})
		if err != nil {
			return err
		}
		if _, err := f.Write(p.body); err != nil {
			return err
		}
	}
	return zw.Close()
}

// sheet renders xl/worksheets/sheet1.xml. The data goes into its own buffer
// first, because the column widths come before it in the file.
func sheet(s Sheet, selected bool) []byte {
	ncol := len(s.Columns)
	widths := make([]int, ncol)
	var data bytes.Buffer
	data.WriteString(`<sheetData>`)
	data.WriteString(`<row r="1">`)
	for i, c := range s.Columns {
		st := styleHeader
		if c.Text {
			st = styleHeaderText
		}
		v := clean(c.Header)
		widths[i] = max(widths[i], displayWidth(v)+2) // bold and the filter arrow
		writeString(&data, ref(i, 1), st, v)
	}
	data.WriteString(`</row>`)
	for ri, row := range s.Rows {
		r := ri + 2
		data.WriteString(`<row r="`)
		data.WriteString(strconv.Itoa(r))
		data.WriteString(`">`)
		for i := 0; i < ncol && i < len(row); i++ {
			cell := row[i]
			v := clean(cell.Value())
			if v == "" {
				continue
			}
			widths[i] = max(widths[i], displayWidth(v))
			switch {
			case s.Columns[i].Text:
				writeString(&data, ref(i, r), styleText, v)
			case cell.Numeric():
				writeNumber(&data, ref(i, r), v)
			default:
				writeString(&data, ref(i, r), styleDefault, v)
			}
		}
		data.WriteString(`</row>`)
	}
	data.WriteString(`</sheetData>`)

	last := ref(ncol-1, len(s.Rows)+1)
	var b bytes.Buffer
	b.WriteString(xmlHeader)
	b.WriteString(`<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">`)
	b.WriteString(`<dimension ref="A1:` + last + `"/>`)
	if selected {
		b.WriteString(`<sheetViews><sheetView tabSelected="1" workbookViewId="0">`)
	} else {
		b.WriteString(`<sheetViews><sheetView workbookViewId="0">`)
	}
	b.WriteString(`<pane ySplit="1" topLeftCell="A2" activePane="bottomLeft" state="frozen"/>`)
	b.WriteString(`<selection pane="bottomLeft" activeCell="A2" sqref="A2"/>`)
	b.WriteString(`</sheetView></sheetViews>`)
	b.WriteString(`<sheetFormatPr defaultRowHeight="15"/>`)
	b.WriteString(`<cols>`)
	for i, c := range s.Columns {
		w := min(max(widths[i]+1, minWidth), maxWidth)
		n := strconv.Itoa(i + 1)
		b.WriteString(`<col min="` + n + `" max="` + n + `" width="` + strconv.Itoa(w) + `" customWidth="1"`)
		if c.Text {
			b.WriteString(` style="` + strconv.Itoa(styleText) + `"`)
		}
		b.WriteString(`/>`)
	}
	b.WriteString(`</cols>`)
	b.Write(data.Bytes())
	b.WriteString(`<autoFilter ref="A1:` + last + `"/>`)
	b.WriteString(`<pageMargins left="0.7" right="0.7" top="0.75" bottom="0.75" header="0.3" footer="0.3"/>`)
	var text []string
	for i, c := range s.Columns {
		if c.Text {
			col := colName(i)
			text = append(text, col+"1:"+col+strconv.Itoa(MaxRows))
		}
	}
	if len(text) > 0 {
		b.WriteString(`<ignoredErrors><ignoredError sqref="` + strings.Join(text, " ") + `" numberStoredAsText="1"/></ignoredErrors>`)
	}
	b.WriteString(`</worksheet>`)
	return b.Bytes()
}

func writeString(b *bytes.Buffer, r string, style int, v string) {
	b.WriteString(`<c r="` + r + `"`)
	if style != styleDefault {
		b.WriteString(` s="` + strconv.Itoa(style) + `"`)
	}
	b.WriteString(` t="inlineStr"><is><t`)
	if v != strings.TrimSpace(v) {
		b.WriteString(` xml:space="preserve"`)
	}
	b.WriteString(`>`)
	escape(b, v)
	b.WriteString(`</t></is></c>`)
}

func writeNumber(b *bytes.Buffer, r, v string) {
	if _, err := strconv.ParseFloat(v, 64); err != nil {
		// Not a number after all: keep it as text rather than corrupt the file.
		writeString(b, r, styleDefault, v)
		return
	}
	b.WriteString(`<c r="` + r + `"><v>` + v + `</v></c>`)
}

// escape writes v with the five XML entities replaced. clean has already
// removed every character XML cannot carry.
func escape(b *bytes.Buffer, v string) {
	for _, r := range v {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '"':
			b.WriteString("&quot;")
		case '\r':
			b.WriteString("&#13;")
		default:
			b.WriteRune(r)
		}
	}
}

// clean drops the characters XML 1.0 cannot carry and cuts the value to
// Excel's per-cell limit.
func clean(v string) string {
	ok := true
	for _, r := range v {
		if !xmlChar(r) {
			ok = false
			break
		}
	}
	if ok && len(v) <= maxCellUnits {
		return v
	}
	var b strings.Builder
	units := 0
	for _, r := range v {
		if !xmlChar(r) {
			continue
		}
		n := 1
		if r >= 0x10000 {
			n = 2
		}
		if units+n > maxCellUnits {
			break
		}
		units += n
		b.WriteRune(r)
	}
	return b.String()
}

func xmlChar(r rune) bool {
	switch {
	case r == utf8.RuneError:
		return false
	case r == '\t' || r == '\n' || r == '\r':
		return true
	case r < 0x20:
		return false
	case r <= 0xD7FF:
		return true
	case r >= 0xE000 && r <= 0xFFFD:
		return true
	case r >= 0x10000 && r <= 0x10FFFF:
		return true
	}
	return false
}

// displayWidth is the widest line of v in characters.
func displayWidth(v string) int {
	w := 0
	for _, line := range strings.Split(v, "\n") {
		w = max(w, utf8.RuneCountInString(line))
	}
	return w
}

// colName is the column letters of a zero-based index: 0 -> A, 26 -> AA.
func colName(i int) string {
	var b []byte
	for i++; i > 0; i = (i - 1) / 26 {
		b = append([]byte{byte('A' + (i-1)%26)}, b...)
	}
	return string(b)
}

func ref(col, row int) string { return colName(col) + strconv.Itoa(row) }

// SheetName makes a tab name Excel accepts: no []:*?/\, no leading or
// trailing apostrophe, at most 31 characters, "Sheet1" when nothing is left.
func SheetName(name string) string {
	var b strings.Builder
	n := 0
	for _, r := range strings.TrimSpace(name) {
		if strings.ContainsRune(`[]:*?/\`, r) || !xmlChar(r) || r < 0x20 {
			continue
		}
		if n == 31 {
			break
		}
		b.WriteRune(r)
		n++
	}
	s := strings.Trim(b.String(), "' ")
	if s == "" {
		return "Sheet1"
	}
	return s
}

func workbookFor(names []string, sheets []Sheet) []byte {
	var b bytes.Buffer
	b.WriteString(xmlHeader)
	b.WriteString(`<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">`)
	b.WriteString(`<bookViews><workbookView/></bookViews><sheets>`)
	for i, name := range names {
		n := strconv.Itoa(i + 1)
		b.WriteString(`<sheet name="`)
		escape(&b, name)
		b.WriteString(`" sheetId="` + n + `" r:id="rId` + n + `"/>`)
	}
	b.WriteString(`</sheets>`)
	// The autofilter's range of each sheet, as Excel itself records it.
	b.WriteString(`<definedNames>`)
	for i, name := range names {
		b.WriteString(`<definedName name="_xlnm._FilterDatabase" localSheetId="` + strconv.Itoa(i) + `" hidden="1">`)
		escape(&b, "'"+strings.ReplaceAll(name, "'", "''")+"'!$A$1:$"+colName(len(sheets[i].Columns)-1)+"$"+strconv.Itoa(len(sheets[i].Rows)+1))
		b.WriteString(`</definedName>`)
	}
	b.WriteString(`</definedNames></workbook>`)
	return b.Bytes()
}

const xmlHeader = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n"

func contentTypesFor(n int) string {
	var b strings.Builder
	b.WriteString(xmlHeader + `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
		`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>` +
		`<Default Extension="xml" ContentType="application/xml"/>` +
		`<Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>`)
	for i := 1; i <= n; i++ {
		b.WriteString(`<Override PartName="/xl/worksheets/sheet` + strconv.Itoa(i) + `.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>`)
	}
	b.WriteString(`<Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/></Types>`)
	return b.String()
}

const rootRels = xmlHeader + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
	`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/>` +
	`</Relationships>`

func workbookRelsFor(n int) string {
	var b strings.Builder
	b.WriteString(xmlHeader + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">`)
	for i := 1; i <= n; i++ {
		id := strconv.Itoa(i)
		b.WriteString(`<Relationship Id="rId` + id + `" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet` + id + `.xml"/>`)
	}
	b.WriteString(`<Relationship Id="rId` + strconv.Itoa(n+1) + `" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/></Relationships>`)
	return b.String()
}

// styles holds the four cell styles: default, bold header, "@" text and
// bold "@" text (numFmt 49 is Excel's built-in text format).
const styles = xmlHeader + `<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">` +
	`<fonts count="2">` +
	`<font><sz val="11"/><name val="Calibri"/><family val="2"/></font>` +
	`<font><b/><sz val="11"/><name val="Calibri"/><family val="2"/></font>` +
	`</fonts>` +
	`<fills count="2"><fill><patternFill patternType="none"/></fill><fill><patternFill patternType="gray125"/></fill></fills>` +
	`<borders count="1"><border><left/><right/><top/><bottom/><diagonal/></border></borders>` +
	`<cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs>` +
	`<cellXfs count="4">` +
	`<xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"/>` +
	`<xf numFmtId="0" fontId="1" fillId="0" borderId="0" xfId="0" applyFont="1"/>` +
	`<xf numFmtId="49" fontId="0" fillId="0" borderId="0" xfId="0" applyNumberFormat="1"/>` +
	`<xf numFmtId="49" fontId="1" fillId="0" borderId="0" xfId="0" applyFont="1" applyNumberFormat="1"/>` +
	`</cellXfs>` +
	`<cellStyles count="1"><cellStyle name="Normal" xfId="0" builtinId="0"/></cellStyles>` +
	`</styleSheet>`
