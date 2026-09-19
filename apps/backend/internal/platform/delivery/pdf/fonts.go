package pdf

import (
	_ "embed"

	"github.com/jung-kurt/gofpdf"
)

// fontFamily is the single UTF-8 TrueType font family used for every piece
// of body text drawn on the e-ticket PDF (headline, detail labels/values,
// legal footer, fine print). Using one constant everywhere means the font
// can never drift between the two SEAT-C4 layouts or between a label and
// its value.
//
// # Why DejaVu Sans Condensed
//
// gofpdf's built-in Core 14 fonts (Helvetica et al.) are WinAnsi/Latin-1
// only: any character outside that range (Cyrillic "Концерт", Czech
// diacritics "Příliš žluťoučký kůň", Greek, most of Hebrew) came out as
// mojibake because the core-font code path emits raw single-byte character
// codes with no corresponding glyph in the reader's Latin-1 fallback.
//
// DejaVuSansCondensed{,-Bold,-Oblique}.ttf ship inside the gofpdf module
// itself (font/ directory) specifically to back AddUTF8FontFromBytes, so no
// network fetch or extra dependency is needed. DejaVu covers Latin
// Extended-A/B, Cyrillic, Greek and a good chunk of Hebrew — see the
// Hebrew caveat on RenderFormat's doc comment and the render_i18n_test.go
// TestRender_Hebrew_GlyphsPresent test for what "a good chunk" means in
// practice (glyphs render; no bidi/shaping).
//
// The three TTFs plus fonts/LICENSE-DejaVu.txt are vendored verbatim from
// github.com/jung-kurt/gofpdf@v1.16.2's font/ directory (Bitstream Vera
// license, permissive, redistribution explicitly allowed). If a future
// ticket needs a script DejaVu does not cover (e.g. CJK, Arabic shaping),
// add that family's TTFs the same way — embed the bytes, register a new
// AddUTF8FontFromBytes family constant, and select it per-Ticket instead of
// widening this one.
const fontFamily = "DejaVuSansCondensed"

//go:embed fonts/DejaVuSansCondensed.ttf
var dejaVuRegularTTF []byte

//go:embed fonts/DejaVuSansCondensed-Bold.ttf
var dejaVuBoldTTF []byte

//go:embed fonts/DejaVuSansCondensed-Oblique.ttf
var dejaVuObliqueTTF []byte

// registerFonts loads the embedded UTF-8 TrueType family onto a freshly
// constructed *gofpdf.Fpdf. Must be called once per document, before the
// first SetFont(fontFamily, ...) call. Regular/Bold/Italic ("", "B", "I")
// are registered; no BoldItalic variant is needed because no layout block
// currently combines the two styles.
func registerFonts(pdf *gofpdf.Fpdf) {
	pdf.AddUTF8FontFromBytes(fontFamily, "", dejaVuRegularTTF)
	pdf.AddUTF8FontFromBytes(fontFamily, "B", dejaVuBoldTTF)
	pdf.AddUTF8FontFromBytes(fontFamily, "I", dejaVuObliqueTTF)
}
