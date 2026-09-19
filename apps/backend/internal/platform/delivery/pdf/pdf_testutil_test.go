package pdf

// pdf_testutil_test.go — shared test helpers for asserting on the raw PDF
// content-stream bytes.
//
// Before the UTF-8 font migration, gofpdf's core Helvetica/Courier fonts
// wrote text operators as plain Latin-1 bytes, so a test could grep the
// output for a literal "(Sector:)" and find it verbatim. Once a layout
// registers a UTF-8 TrueType font (see fonts.go) and calls
// pdf.SetFont(fontFamily, ...), gofpdf switches that text to UTF-16BE (two
// bytes per rune, no byte-order mark) before writing the "(...)Tj"
// operator — see AddUTF8FontFromBytes / Fpdf.Text / Fpdf.CellFormat in
// github.com/jung-kurt/gofpdf@v1.16.2/fpdf.go. A test that still expects
// literal ASCII bytes would now find nothing, not because the label is
// missing but because it is correctly Unicode-encoded.
//
// pdfText mirrors that encoding so tests can assert "this text was drawn
// with the UTF-8 font" the same way the old tests asserted "this text was
// drawn at all".

// utf16beEscaped replicates gofpdf's utf8toutf16(s, false) + escape(...)
// pipeline: UTF-16BE code units (2 bytes each, big-endian, no BOM) with
// backslash / parentheses / CR escaped for the PDF string-literal syntax.
// Only handles runes within the Basic Multilingual Plane (U+0000-U+FFFF,
// no surrogate pairs) — sufficient for every label/value this package
// prints (Latin Extended, Cyrillic, Hebrew, Greek all fit in the BMP), and
// it is exactly what gofpdf's own converter handles too (see
// util.go:utf8toutf16, which only special-cases 1/2/3-byte UTF-8
// sequences).
func utf16beEscaped(s string) []byte {
	var out []byte
	for _, r := range s {
		for _, b := range [2]byte{byte(r >> 8), byte(r)} {
			switch b {
			case '\\', '(', ')':
				out = append(out, '\\', b)
			case '\r':
				out = append(out, '\\', 'r')
			default:
				out = append(out, b)
			}
		}
	}
	return out
}

// pdfText wraps utf16beEscaped in the "(...)" PDF string-literal
// delimiters, matching the "(Sector:)"-style tokens the pre-UTF8-font
// tests searched the raw content stream for.
func pdfText(s string) []byte {
	out := []byte{'('}
	out = append(out, utf16beEscaped(s)...)
	out = append(out, ')')
	return out
}
