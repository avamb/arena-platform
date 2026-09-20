package pdf

// pdf_testutil_test.go — shared helpers for asserting on the raw PDF bytes.
//
// The renderer sets SetCompression(false), so the CONTENT stream (text and
// vector operators) is plain, greppable bytes. Embedded images keep their own
// compression: a PNG's IDAT payload is copied verbatim into the PDF object,
// which is what pngIDAT below exploits to identify exactly which QR was
// embedded.
//
// Two encoding subtleties make a naive `bytes.Contains(out, []byte("Seat"))`
// wrong, and both have bitten this package before:
//
//  1. Once a layout registers a UTF-8 TrueType font (fonts.go) and calls
//     SetFont(fontFamily, ...), gofpdf writes text as UTF-16BE (two bytes per
//     rune, no BOM) before emitting the "(...)Tj" operator — see
//     AddUTF8FontFromBytes / Fpdf.Text / Fpdf.CellFormat in
//     github.com/jung-kurt/gofpdf@v1.16.2. pdfText mirrors that encoding.
//  2. The info labels are letter-spaced, and gofpdf has no character-spacing
//     operator, so drawTracked emits ONE show operator per glyph. A tracked
//     label never appears as a single run; pdfHasTracked checks its glyphs.

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"regexp"
	"strconv"
	"testing"
)

// utf16beEscaped replicates gofpdf's utf8toutf16(s, false) + escape(...)
// pipeline: UTF-16BE code units (2 bytes each, big-endian, no BOM) with
// backslash / parentheses / CR escaped for the PDF string-literal syntax.
// Only handles runes within the Basic Multilingual Plane (U+0000-U+FFFF, no
// surrogate pairs) — sufficient for every label/value this package prints,
// and exactly what gofpdf's own converter handles too (see util.go's
// utf8toutf16, which only special-cases 1/2/3-byte UTF-8 sequences).
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

// pdfText wraps utf16beEscaped in the "(...)" PDF string-literal delimiters,
// matching the "(Sector:)"-style tokens in the raw content stream.
func pdfText(s string) []byte {
	out := []byte{'('}
	out = append(out, utf16beEscaped(s)...)
	out = append(out, ')')
	return out
}

// pdfTextPrefix is pdfText without the closing delimiter: it matches a show
// operator whose text STARTS with s. Needed for anything the renderer wraps
// (the footer note, a long value in a narrow info cell), where the drawn run
// is one line of the string rather than the whole of it.
func pdfTextPrefix(s string) []byte {
	out := []byte{'('}
	return append(out, utf16beEscaped(s)...)
}

// pdfHasTracked reports whether every glyph of s was drawn as its own show
// operator — how drawTracked emits a letter-spaced info label.
func pdfHasTracked(out []byte, s string) bool {
	for _, r := range s {
		if r == ' ' {
			continue
		}
		if !bytes.Contains(out, pdfText(string(r))) {
			return false
		}
	}
	return true
}

// usesFontSize reports whether the document ever selected the given font
// size. gofpdf emits font selection as "BT /F<n> <size> Tf ET" with the size
// at two decimals (fpdf.go's SetFontSize/SetFont).
func usesFontSize(out []byte, size float64) bool {
	return bytes.Contains(out, []byte(fmt.Sprintf("%.2f Tf", size)))
}

// mediaBox returns the page's declared width and height in points.
var mediaBoxRe = regexp.MustCompile(`/MediaBox \[0 0 ([0-9.]+) ([0-9.]+)\]`)

func mediaBox(t *testing.T, out []byte) (w, h float64) {
	t.Helper()
	m := mediaBoxRe.FindSubmatch(out)
	if m == nil {
		t.Fatal("no /MediaBox in the rendered PDF")
	}
	return mustFloat(t, m[1]), mustFloat(t, m[2])
}

// strokedLineYs returns the y coordinate (in PDF user space, measured from
// the page BOTTOM) of every straight stroked line in the document. gofpdf's
// Line emits "%.2f %.2f m %.2f %.2f l S".
var lineOpRe = regexp.MustCompile(`([0-9.]+) ([0-9.]+) m ([0-9.]+) ([0-9.]+) l S`)

func strokedLineYs(out []byte) []float64 {
	ys := []float64{}
	for _, m := range lineOpRe.FindAllSubmatch(out, -1) {
		v, err := strconv.ParseFloat(string(m[2]), 64)
		if err != nil {
			continue
		}
		ys = append(ys, v)
	}
	return ys
}

// placement is one drawn image: its size and its lower-left corner, in PDF
// user space (y from the page bottom).
type placement struct{ w, h, x, y float64 }

// drawnImages returns every image placement in the document. gofpdf's
// ImageOptions emits "q %.5f 0 0 %.5f %.5f %.5f cm /I<id> Do Q", where the
// id is the image's SHA-1 checksum in hex (ImageInfoType.i), not an index.
var imageOpRe = regexp.MustCompile(`q ([0-9.]+) 0 0 ([0-9.]+) ([0-9.]+) ([0-9.]+) cm /I[0-9a-f]+ Do Q`)

func drawnImages(t *testing.T, out []byte) []placement {
	t.Helper()
	got := []placement{}
	for _, m := range imageOpRe.FindAllSubmatch(out, -1) {
		got = append(got, placement{
			w: mustFloat(t, m[1]), h: mustFloat(t, m[2]),
			x: mustFloat(t, m[3]), y: mustFloat(t, m[4]),
		})
	}
	return got
}

func mustFloat(t *testing.T, b []byte) float64 {
	t.Helper()
	v, err := strconv.ParseFloat(string(b), 64)
	if err != nil {
		t.Fatalf("parse %q: %v", b, err)
	}
	return v
}

// pngIDAT concatenates a PNG's IDAT chunk payloads — the exact byte run
// gofpdf's parsePNG copies into the PDF image object, so a test can assert
// that one specific PNG (say, the QR of a specific payload) is or is not
// embedded in the document.
func pngIDAT(t *testing.T, png []byte) []byte {
	t.Helper()
	const sigLen = 8
	if len(png) < sigLen {
		t.Fatalf("not a PNG: %d bytes", len(png))
	}
	var data []byte
	for p := sigLen; p+8 <= len(png); {
		length := int(binary.BigEndian.Uint32(png[p : p+4]))
		typ := string(png[p+4 : p+8])
		body := p + 8
		if body+length > len(png) {
			break
		}
		if typ == "IDAT" {
			data = append(data, png[body:body+length]...)
		}
		p = body + length + 4 // + CRC
	}
	if len(data) == 0 {
		t.Fatal("PNG has no IDAT data")
	}
	return data
}
