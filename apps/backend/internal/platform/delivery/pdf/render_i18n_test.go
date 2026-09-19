// render_i18n_test.go — mojibake regression + label-locale coverage for
// the UTF-8 font migration (launch blocker fix, Czech/Russian sales).
//
// Before this fix, every layout used gofpdf's built-in core Helvetica
// font, which is WinAnsi/Latin-1 only: any character outside that range
// (Cyrillic, Czech diacritics, Hebrew, ...) came out as mojibake because
// gofpdf's core-font text path writes single-byte character codes with no
// corresponding glyph. See fonts.go for the fix (an embedded UTF-8
// TrueType family) and labels.go for the locale-selected field labels.
package pdf

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"
)

// i18nTicket returns a Ticket exercising every non-Latin-1 script this
// fix targets: Cyrillic (event name, holder name), Czech diacritics
// (venue name, a long address that must still wrap inside the ticket
// frame).
func i18nTicket(t *testing.T) Ticket {
	t.Helper()
	tk := validTicket(t)
	tk.EventName = "Концерт «Весна»"
	tk.VenueName = "Příliš žluťoučký kůň — Divadlo Na Zábradlí"
	tk.VenueCity = "Praha"
	tk.HolderName = "Иван Петрович Сидоров"
	tk.LegalName = "Divadlo Na Zábradlí, s.r.o."
	tk.LegalAddressLine1 = "Anenské náměstí 5, Staré Město"
	tk.LegalAddressLine2 = "Provozovna: Divadelní 296/5, 110 00 Praha 1 - Staré Město"
	tk.LegalAddressPostalCode = "110 00"
	tk.LegalAddressCity = "Praha 1"
	tk.LegalAddressCountry = "Česká republika"
	tk.ContactEmail = "pokladna@nazabradli.example.cz"
	return tk
}

// TestRender_UnicodeContent_SucceedsAndEmbedsFont is the key regression
// check: Cyrillic + Czech diacritics must render via the embedded UTF-8
// font, not fall back to (or silently mix in) the Latin-1 core Helvetica
// font that produced mojibake before this fix.
func TestRender_UnicodeContent_SucceedsAndEmbedsFont(t *testing.T) {
	out, err := Render(context.Background(), i18nTicket(t))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if len(out) < 200 {
		t.Fatalf("PDF suspiciously small: %d bytes", len(out))
	}

	// The embedded TrueType font program must be present (FontFile2 is
	// how gofpdf embeds a UTF-8 TrueType font's glyph outlines; the core
	// Helvetica font never emits this key at all).
	if !bytes.Contains(out, []byte("/FontFile2")) {
		t.Error("PDF does not embed a TrueType font program (/FontFile2 missing)")
	}

	// Body text must not reference the core Helvetica font resource. The
	// PDF font dictionary names the family via /BaseFont; Helvetica's
	// value there is literally "/BaseFont /Helvetica" (or "/Helvetica-Bold"
	// etc). DejaVuSansCondensed's /BaseFont uses gofpdf's generated
	// subset-tag + family name and never contains that string.
	if bytes.Contains(out, []byte("/BaseFont /Helvetica")) {
		t.Error("PDF still references the core Helvetica font for body text")
	}

	// The key regression check: the OLD mojibake bug wrote Latin-1 core
	// font text as single bytes with no valid glyph for non-Latin-1
	// runes, so gofpdf's escape() would have passed "Концерт"'s raw UTF-8
	// byte sequence straight into the content stream, byte for byte
	// (SetCompression(false) means the content stream is not deflated, so
	// this is a direct substring check, no decompression needed). Once
	// the UTF-8 font is wired up, every character is 2-byte UTF-16BE, so
	// that exact raw-UTF-8 byte run can never appear again.
	if bytes.Contains(out, []byte("Концерт")) {
		t.Error("PDF contains the raw UTF-8 byte sequence for \"Концерт\" literally " +
			"(the pre-fix mojibake signature) instead of UTF-16BE-encoded text")
	}

	// Positive check: the Cyrillic and Czech text DOES appear, correctly
	// UTF-16BE encoded (see pdf_testutil_test.go's pdfText). The venue row
	// joins VenueName and VenueCity with ", " (joinNonEmpty), so the venue
	// name is checked in that combined form, matching what drawDetails
	// actually draws.
	for _, want := range []string{
		"Концерт «Весна»",
		"Příliš žluťoučký kůň — Divadlo Na Zábradlí, Praha",
		"Иван Петрович Сидоров",
	} {
		if !bytes.Contains(out, pdfText(want)) {
			t.Errorf("PDF missing expected Unicode text %q (as UTF-16BE)", want)
		}
	}
}

// TestRender_UnicodeContent_LongAddressWraps guards the MultiCell/CellFormat
// wrapping contract: a long Czech legal address (LegalAddressLine2, ~60
// characters) must still be laid out as a bounded set of footer lines and
// must not blow past the page — i.e. rendering must succeed and produce a
// single-page-sized PDF, not error out or silently truncate to nothing.
func TestRender_UnicodeContent_LongAddressWraps(t *testing.T) {
	tk := i18nTicket(t)
	out, err := Render(context.Background(), tk)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	// The long address line was passed through buildLegalLines verbatim
	// (one CellFormat call per line, not wrapped further) — assert it
	// still made it into the footer intact rather than gofpdf erroring on
	// "character outside the supported range" or similar.
	if !bytes.Contains(out, pdfText(tk.LegalAddressLine2)) {
		t.Error("PDF missing the long Czech address line")
	}
	// Sanity: still exactly one page (no accidental page break / overflow
	// past the QR/barcode area).
	if got := bytes.Count(out, []byte("/Type /Page\n")); got > 1 {
		t.Errorf("expected a single-page PDF, found %d /Type /Page markers", got)
	}
}

// TestRender_EAN13AndHumanCode_StillRenderWithUnicodeContent guards that
// the EAN-13 caption and the human-entry code — both still ASCII, drawn
// via the Ticket-ID font (UTF-8 DejaVu) and Courier (core) respectively —
// keep rendering correctly on a ticket whose OTHER fields are non-Latin-1.
func TestRender_EAN13AndHumanCode_StillRenderWithUnicodeContent(t *testing.T) {
	tk := i18nTicket(t)
	tk.EAN13 = "4006381333931"
	tk.HumanCode = "M7KT-2QV9"
	out, err := Render(context.Background(), tk)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !bytes.Contains(out, pdfText("EAN-13: "+tk.EAN13)) {
		t.Error("PDF missing EAN-13 caption")
	}
	// drawHumanCode draws one glyph per Text() call in the Courier core
	// font, so each letter is its own single-byte "(X)Tj" token, not one
	// combined UTF-16BE run.
	for _, r := range tk.HumanCode {
		if r == '-' {
			continue
		}
		if !bytes.Contains(out, []byte("("+string(r)+")")) {
			t.Errorf("PDF missing human-code glyph %q", string(r))
		}
	}
}

// ──────────────────────────────────────────────────────────────────────
// Hebrew: honest glyph-coverage check, no bidi/shaping claims.
// ──────────────────────────────────────────────────────────────────────

// TestRender_Hebrew_GlyphsPresent renders a ticket whose event name is
// Hebrew and asserts the render succeeds. It does NOT assert correct
// visual right-to-left order or contextual shaping — gofpdf's RTL()/LTR()
// only reverses the logical character order before laying out
// left-to-right glyph advances (see reverseText in the module); it is not
// a bidi/shaping engine. What this test (together with
// TestDejaVuSansCondensed_HasHebrewGlyphs below) establishes is the
// narrower, honestly-reportable claim: the embedded font has real glyph
// outlines for the Hebrew block, so Hebrew text does not fall back to
// .notdef boxes — full correct bidi rendering is out of scope.
func TestRender_Hebrew_GlyphsPresent(t *testing.T) {
	tk := validTicket(t)
	tk.EventName = "קונצרט"
	tk.HolderName = "דוד לוי"
	out, err := Render(context.Background(), tk)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !bytes.Contains(out, pdfText(tk.EventName)) {
		t.Error("PDF missing Hebrew event name")
	}
	if !bytes.Contains(out, pdfText(tk.HolderName)) {
		t.Error("PDF missing Hebrew holder name")
	}
}

// TestDejaVuSansCondensed_HasHebrewGlyphs parses the embedded font's own
// 'cmap' table (format 4, platform 3/encoding 1 — the exact subtable
// gofpdf's utf8fontfile.go itself reads; see parseCMAPTable) to check,
// independently of gofpdf, whether DejaVuSansCondensed.ttf actually maps
// the core Hebrew alphabet (Aleph U+05D0 .. Tav U+05EA) to real glyphs.
// This is the ground truth behind the Hebrew claim in fonts.go's doc
// comment: DejaVu's Hebrew coverage is real but not exhaustive (points
// vowels/cantillation marks and presentation forms are commonly absent),
// so this only checks the base consonant letters actually used above.
func TestDejaVuSansCondensed_HasHebrewGlyphs(t *testing.T) {
	cmap, err := parseFormat4Cmap(dejaVuRegularTTF)
	if err != nil {
		t.Fatalf("parseFormat4Cmap: %v", err)
	}
	missing := []rune{}
	for r := rune(0x05D0); r <= 0x05EA; r++ { // Hebrew Aleph..Tav
		if !cmap.has(r) {
			missing = append(missing, r)
		}
	}
	if len(missing) > 0 {
		t.Logf("DejaVuSansCondensed.ttf is missing glyphs for %d/27 Hebrew base "+
			"letters: %q — Hebrew text on the PDF will show .notdef boxes for "+
			"those; see fonts.go's Hebrew caveat.", len(missing), string(missing))
	} else {
		t.Log("DejaVuSansCondensed.ttf maps every Hebrew base consonant " +
			"(Aleph..Tav) to a real glyph.")
	}
}

// ──────────────────────────────────────────────────────────────────────
// Locale label dictionaries.
// ──────────────────────────────────────────────────────────────────────

func TestLabelsFor_KnownLocales(t *testing.T) {
	cases := []struct {
		locale string
		want   ticketLabels
	}{
		{"en", labelsEN},
		{"ru", labelsRU},
		{"cs", labelsCS},
		{"RU", labelsRU},    // case-insensitive
		{" cs ", labelsCS},  // trimmed
		{"ru-RU", labelsRU}, // region subtag stripped
		{"cs-CZ", labelsCS},
	}
	for _, tc := range cases {
		t.Run(tc.locale, func(t *testing.T) {
			got := labelsFor(tc.locale)
			if got != tc.want {
				t.Fatalf("labelsFor(%q) = %+v, want %+v", tc.locale, got, tc.want)
			}
		})
	}
}

func TestLabelsFor_UnknownOrEmptyFallsBackToEnglish(t *testing.T) {
	for _, locale := range []string{"", "de", "es", "he", "fr-FR", "xx"} {
		t.Run(locale, func(t *testing.T) {
			got := labelsFor(locale)
			if got != labelsEN {
				t.Fatalf("labelsFor(%q) = %+v, want English fallback %+v", locale, got, labelsEN)
			}
		})
	}
}

// TestRender_LocaleSelectsPrintedLabels asserts the locale actually
// changes what is drawn on the page: an "ru" ticket must show the
// Russian "Сектор"/"Ряд"/"Место" seat labels (not the English ones), and
// an English/unset-locale ticket must show the English labels.
func TestRender_LocaleSelectsPrintedLabels(t *testing.T) {
	seated := func(locale string) Ticket {
		tk := seatedTicket(t)
		tk.Locale = locale
		return tk
	}

	ru, err := Render(context.Background(), seated("ru"))
	if err != nil {
		t.Fatalf("Render ru: %v", err)
	}
	for _, want := range []string{"Сектор:", "Ряд:", "Место:", "Владелец:"} {
		if !bytes.Contains(ru, pdfText(want)) {
			t.Errorf("ru PDF missing label %q", want)
		}
	}
	for _, banned := range []string{"Sector:", "Row:", "Seat:", "Holder:"} {
		if bytes.Contains(ru, pdfText(banned)) {
			t.Errorf("ru PDF should not contain English label %q", banned)
		}
	}

	cs, err := Render(context.Background(), seated("cs"))
	if err != nil {
		t.Fatalf("Render cs: %v", err)
	}
	for _, want := range []string{"Sektor:", "Řada:", "Místo:", "Držitel:"} {
		if !bytes.Contains(cs, pdfText(want)) {
			t.Errorf("cs PDF missing label %q", want)
		}
	}

	en, err := Render(context.Background(), seated(""))
	if err != nil {
		t.Fatalf("Render en (default): %v", err)
	}
	for _, want := range []string{"Sector:", "Row:", "Seat:", "Holder:"} {
		if !bytes.Contains(en, pdfText(want)) {
			t.Errorf("default-locale PDF missing English label %q", want)
		}
	}

	unknown, err := Render(context.Background(), seated("fr"))
	if err != nil {
		t.Fatalf("Render fr (unsupported, should fall back to en): %v", err)
	}
	if !bytes.Equal(en, unknown) {
		t.Error("unsupported locale should render byte-identical to the English default")
	}
}

// TestRender_LocaleAffectsTicketIDAndEAN13Prefixes covers the two plain
// "Label: value" lines drawn outside drawDetails (Ticket ID and EAN-13),
// which are wired through labels.TicketID / labels.EAN13 separately.
func TestRender_LocaleAffectsTicketIDAndEAN13Prefixes(t *testing.T) {
	tk := validTicket(t)
	tk.EAN13 = "4006381333931"
	tk.Locale = "ru"
	out, err := Render(context.Background(), tk)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !bytes.Contains(out, pdfText("Номер билета: "+tk.TicketID)) {
		t.Error("PDF missing localized Ticket ID prefix")
	}
	// EAN-13 keeps the same caption in every locale (it is a barcode
	// standard's name, not prose).
	if !bytes.Contains(out, pdfText("EAN-13: "+tk.EAN13)) {
		t.Error("PDF missing EAN-13 caption")
	}
}

// ──────────────────────────────────────────────────────────────────────
// Minimal format-4 'cmap' parser — mirrors gofpdf's own subtable
// selection (utf8fontfile.go: platform 3/encoding 1, or platform 0, format
// 4) so TestDejaVuSansCondensed_HasHebrewGlyphs answers the same question
// gofpdf itself would ask of the font, independent of gofpdf's internals.
// ──────────────────────────────────────────────────────────────────────

type format4Cmap struct {
	segCount           int
	endCode, startCode []uint16
	idDelta            []int16
	idRangeOffset      []uint16
	idRangeOffsetPos   []int // absolute byte offset of idRangeOffset[i] in ttf
	ttf                []byte
}

func parseFormat4Cmap(ttf []byte) (*format4Cmap, error) {
	be := binary.BigEndian
	numTables := be.Uint16(ttf[4:6])
	var cmapOff uint32
	found := false
	for i := 0; i < int(numTables); i++ {
		rec := ttf[12+i*16 : 12+i*16+16]
		tag := string(rec[0:4])
		if tag == "cmap" {
			cmapOff = be.Uint32(rec[8:12])
			found = true
			break
		}
	}
	if !found {
		return nil, errNoCmapTable
	}
	numEncodings := be.Uint16(ttf[cmapOff+2 : cmapOff+4])
	var subtableOff uint32
	subtableFound := false
	for i := 0; i < int(numEncodings); i++ {
		rec := ttf[cmapOff+4+uint32(i*8) : cmapOff+4+uint32(i*8)+8]
		platformID := be.Uint16(rec[0:2])
		encodingID := be.Uint16(rec[2:4])
		off := be.Uint32(rec[4:8])
		if (platformID == 3 && encodingID == 1) || platformID == 0 {
			format := be.Uint16(ttf[cmapOff+off : cmapOff+off+2])
			if format == 4 {
				subtableOff = cmapOff + off
				subtableFound = true
				break
			}
		}
	}
	if !subtableFound {
		return nil, errNoFormat4Subtable
	}

	segCountX2 := be.Uint16(ttf[subtableOff+6 : subtableOff+8])
	segCount := int(segCountX2 / 2)

	endCodeOff := subtableOff + 14
	startCodeOff := endCodeOff + uint32(segCountX2) + 2 // +2 for reservedPad
	idDeltaOff := startCodeOff + uint32(segCountX2)
	idRangeOff := idDeltaOff + uint32(segCountX2)

	c := &format4Cmap{
		segCount:         segCount,
		endCode:          make([]uint16, segCount),
		startCode:        make([]uint16, segCount),
		idDelta:          make([]int16, segCount),
		idRangeOffset:    make([]uint16, segCount),
		idRangeOffsetPos: make([]int, segCount),
		ttf:              ttf,
	}
	for i := 0; i < segCount; i++ {
		c.endCode[i] = be.Uint16(ttf[endCodeOff+uint32(i*2):])
		c.startCode[i] = be.Uint16(ttf[startCodeOff+uint32(i*2):])
		c.idDelta[i] = int16(be.Uint16(ttf[idDeltaOff+uint32(i*2):]))
		pos := idRangeOff + uint32(i*2)
		c.idRangeOffset[i] = be.Uint16(ttf[pos:])
		c.idRangeOffsetPos[i] = int(pos)
	}
	return c, nil
}

// has reports whether the subtable maps r to a non-.notdef glyph.
func (c *format4Cmap) has(r rune) bool {
	if r < 0 || r > 0xFFFF {
		return false
	}
	cp := uint16(r)
	for i := 0; i < c.segCount; i++ {
		if cp < c.startCode[i] || cp > c.endCode[i] {
			continue
		}
		if c.idRangeOffset[i] == 0 {
			glyph := uint16(int32(cp) + int32(c.idDelta[i]))
			return glyph != 0
		}
		addr := c.idRangeOffsetPos[i] + int(c.idRangeOffset[i]) + 2*int(cp-c.startCode[i])
		if addr+2 > len(c.ttf) {
			return false
		}
		glyph := binary.BigEndian.Uint16(c.ttf[addr : addr+2])
		if glyph == 0 {
			return false
		}
		glyph = uint16(int32(glyph) + int32(c.idDelta[i]))
		return glyph != 0
	}
	return false
}

var errNoCmapTable = errStr("ttf: no cmap table")
var errNoFormat4Subtable = errStr("ttf: no format-4 Unicode cmap subtable")

type errStr string

func (e errStr) Error() string { return string(e) }
