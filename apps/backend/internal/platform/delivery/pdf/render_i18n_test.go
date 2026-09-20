// render_i18n_test.go — mojibake regression + locale coverage.
//
// Before the UTF-8 font migration every layout used gofpdf's built-in core
// Helvetica, which is WinAnsi/Latin-1 only: Cyrillic, Czech diacritics and
// Hebrew all came out as mojibake because gofpdf's core-font text path writes
// single-byte character codes with no corresponding glyph. fonts.go is the
// fix (an embedded UTF-8 TrueType family); these tests keep it fixed.
//
// The locale tests below cover the ported chrome tables (labels.go): the
// string table, the month names in the form a date uses, the weekday names,
// and the per-locale join patterns.
package pdf

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
	"time"
)

// i18nTicket exercises every non-Latin-1 script this fix targets: Cyrillic
// (event name, holder name), Czech diacritics (venue name plus a long legal
// address that must still lay out inside the footer).
func i18nTicket(t *testing.T) Ticket {
	t.Helper()
	tk := validTicket(t)
	tk.EventName = "Концерт «Весна»"
	tk.VenueName = "Divadlo Na Zábradlí"
	tk.VenueAddress = "Anenské náměstí 5"
	tk.VenueCity = "Praha"
	tk.HolderName = "Иван Петрович Сидоров"
	tk.TierName = "Příliš žluťoučký kůň"
	tk.LegalName = "Divadlo Na Zábradlí, s.r.o."
	tk.LegalAddressLine1 = "Anenské náměstí 5, Staré Město"
	tk.LegalAddressLine2 = "Provozovna: Divadelní 296/5, 110 00 Praha 1"
	tk.LegalAddressPostalCode = "110 00"
	tk.LegalAddressCity = "Praha 1"
	tk.LegalAddressCountry = "Česká republika"
	tk.ContactEmail = "pokladna@nazabradli.example.cz"
	return tk
}

// TestRender_UnicodeContent_SucceedsAndEmbedsFont is the key regression
// check: Cyrillic + Czech diacritics must render via the embedded UTF-8
// font, not fall back to (or silently mix in) the Latin-1 core Helvetica
// that produced mojibake before the fix.
func TestRender_UnicodeContent_SucceedsAndEmbedsFont(t *testing.T) {
	out := render(t, i18nTicket(t))
	if len(out) < 200 {
		t.Fatalf("PDF suspiciously small: %d bytes", len(out))
	}

	// The embedded TrueType font program must be present (/FontFile2 is how
	// gofpdf embeds a UTF-8 TrueType font's glyph outlines; the core
	// Helvetica path never emits this key at all).
	if !bytes.Contains(out, []byte("/FontFile2")) {
		t.Error("PDF does not embed a TrueType font program (/FontFile2 missing)")
	}

	// Body text must not reference the core Helvetica font resource.
	if bytes.Contains(out, []byte("/BaseFont /Helvetica")) {
		t.Error("PDF still references the core Helvetica font for body text")
	}

	// THE regression check: the old mojibake bug wrote Latin-1 core-font text
	// as single bytes, so gofpdf's escape() passed "Концерт"'s raw UTF-8 byte
	// sequence straight into the content stream. With the UTF-8 font wired up
	// every character is 2-byte UTF-16BE, so that exact raw-UTF-8 run can
	// never appear again. (SetCompression(false) makes this a direct
	// substring check — no decompression needed.)
	if bytes.Contains(out, []byte("Концерт")) {
		t.Error("PDF contains the raw UTF-8 byte sequence for \"Концерт\" literally " +
			"(the pre-fix mojibake signature) instead of UTF-16BE-encoded text")
	}

	// Positive check: the Cyrillic and Czech text DOES appear, correctly
	// UTF-16BE encoded. The address line joins VenueAddress and VenueCity
	// with ", ", matching what drawWhenWhere actually draws. These are
	// PREFIX matches: the venue column and the info cells are narrow enough
	// that a long value wraps per word across several show operators, so
	// only the start of the first wrapped line is a contiguous run.
	for _, want := range []string{
		"Концерт «Весна»",
		"Divadlo Na Zábradlí",
		"Anenské náměstí 5",
		"Иван",
		"Příliš",
	} {
		if !bytes.Contains(out, pdfTextPrefix(want)) {
			t.Errorf("PDF missing expected Unicode text %q (as UTF-16BE)", want)
		}
	}
}

// TestRender_UnicodeContent_LongAddressWraps guards the wrapping contract: a
// long Czech legal address must still lay out as a bounded set of footer
// lines on a single page, not error out or silently vanish.
func TestRender_UnicodeContent_LongAddressWraps(t *testing.T) {
	tk := i18nTicket(t)
	out := render(t, tk)
	if !bytes.Contains(out, pdfText(tk.LegalAddressLine2)) {
		t.Error("PDF missing the long Czech address line")
	}
	if got := bytes.Count(out, []byte("/Type /Page\n")); got > 1 {
		t.Errorf("expected a single-page PDF, found %d /Type /Page markers", got)
	}
}

// TestRender_EAN13Digits_StillRenderWithUnicodeContent guards that the
// barcode's human-readable digits — ASCII, drawn through the same UTF-8
// font — keep rendering on a ticket whose other fields are non-Latin-1.
func TestRender_EAN13Digits_StillRenderWithUnicodeContent(t *testing.T) {
	tk := i18nTicket(t)
	tk.EAN13 = "4006381333931"
	out := render(t, tk)
	for _, want := range []string{tk.EAN13[:1], tk.EAN13[1:7], tk.EAN13[7:13]} {
		if !bytes.Contains(out, pdfText(want)) {
			t.Errorf("PDF missing EAN-13 human-readable digit group %q", want)
		}
	}
}

// ──────────────────────────────────────────────────────────────────────
// Hebrew: honest glyph-coverage check, no bidi/shaping claims.
// ──────────────────────────────────────────────────────────────────────

// TestRender_Hebrew_GlyphsPresent renders a Hebrew event name and asserts
// the render succeeds. It does NOT assert correct visual right-to-left order
// or contextual shaping — gofpdf's RTL()/LTR() only reverses logical
// character order before laying out left-to-right advances; it is not a bidi
// or shaping engine. The narrower, honestly-reportable claim (together with
// TestDejaVuSansCondensed_HasHebrewGlyphs) is that the embedded font has real
// outlines for the Hebrew block, so Hebrew does not degrade to .notdef boxes.
func TestRender_Hebrew_GlyphsPresent(t *testing.T) {
	tk := validTicket(t)
	tk.EventName = "קונצרט"
	tk.HolderName = "דוד לוי"
	out := render(t, tk)
	if !bytes.Contains(out, pdfText(tk.EventName)) {
		t.Error("PDF missing Hebrew event name")
	}
	if !bytes.Contains(out, pdfText(tk.HolderName)) {
		t.Error("PDF missing Hebrew holder name")
	}
}

// TestDejaVuSansCondensed_HasHebrewGlyphs parses the embedded font's own
// 'cmap' table (format 4, platform 3/encoding 1 — the exact subtable
// gofpdf's utf8fontfile.go reads) to check, independently of gofpdf, whether
// the font maps the core Hebrew alphabet (Aleph U+05D0 .. Tav U+05EA) to real
// glyphs. This is the ground truth behind fonts.go's Hebrew caveat.
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
// The ported chrome tables.
// ──────────────────────────────────────────────────────────────────────

func TestStringsFor_KnownLocalesAndFallback(t *testing.T) {
	for _, locale := range SupportedLocales {
		t.Run(locale, func(t *testing.T) {
			got := stringsFor(locale)
			if got.Category == "" || got.TicketNo == "" || got.KeepNote == "" || got.NotFiscal == "" {
				t.Fatalf("locale %q has an incomplete string table: %+v", locale, got)
			}
		})
	}
	en := stringsFor("en")
	for _, locale := range []string{"", "xx", "he", "zh-Hans"} {
		if got := stringsFor(locale); got != en {
			t.Errorf("stringsFor(%q) should fall back to English", locale)
		}
	}
	// Case folding and region subtags.
	if stringsFor("RU") != stringsFor("ru") || stringsFor(" cs ") != stringsFor("cs") ||
		stringsFor("es-ES") != stringsFor("es") {
		t.Error("locale normalization (case, trim, region subtag) is broken")
	}
	// Spanish is a first-class locale now — the first two live clients are
	// in Spain — where it used to fall back to English.
	if stringsFor("es") == en {
		t.Error("es must have its own table, not the English fallback")
	}
}

// TestFormatShowTime_PerLocale pins the ported date tables and the join
// patterns: Czech and German put a period after the day, Spanish links the
// three parts with "de", and the Slavic months are in the genitive the date
// grammar requires ("5 декабря", not "декабрь").
func TestFormatShowTime_PerLocale(t *testing.T) {
	// 2026-12-05 is a Saturday.
	session := time.Date(2026, 12, 5, 18, 30, 0, 0, time.UTC)
	cases := []struct {
		locale, date, weekday string
	}{
		{"en", "5 December 2026", "Saturday"},
		{"ru", "5 декабря 2026", "суббота"},
		{"cs", "5. prosince 2026", "sobota"},
		{"es", "5 de diciembre de 2026", "sábado"},
		{"de", "5. Dezember 2026", "Samstag"},
		{"fr", "5 décembre 2026", "samedi"},
		{"it", "5 dicembre 2026", "sabato"},
		{"pl", "5 grudnia 2026", "sobota"},
		{"uk", "5 грудня 2026", "субота"},
		{"xx", "5 December 2026", "Saturday"}, // unknown falls back to en
	}
	for _, tc := range cases {
		t.Run(tc.locale, func(t *testing.T) {
			date, clock, weekday := formatShowTime(session, "", tc.locale)
			if date != tc.date {
				t.Errorf("date = %q want %q", date, tc.date)
			}
			if weekday != tc.weekday {
				t.Errorf("weekday = %q want %q", weekday, tc.weekday)
			}
			if clock != "18:30" {
				t.Errorf("clock = %q want 18:30 (24-hour in every locale)", clock)
			}
		})
	}
}

func TestFormatShowTime_VenueLocalClock(t *testing.T) {
	session := time.Date(2026, 5, 12, 18, 30, 0, 0, time.UTC)

	_, clock, _ := formatShowTime(session, "Europe/Moscow", "en")
	if clock != "21:30" { // Moscow is UTC+3 year-round
		t.Errorf("clock = %q, want the venue-local 21:30", clock)
	}
	// A date can roll over into the next day in the venue's zone.
	date, clock, weekday := formatShowTime(
		time.Date(2026, 5, 12, 23, 30, 0, 0, time.UTC), "Europe/Moscow", "en")
	if date != "13 May 2026" || clock != "02:30" || weekday != "Wednesday" {
		t.Errorf("got %q %q %q, want the venue-local next day", date, clock, weekday)
	}
	// An empty or bogus zone falls back to UTC and never panics.
	for _, tz := range []string{"", "Not/A/Real/Zone"} {
		_, clock, _ := formatShowTime(session, tz, "en")
		if clock != "18:30" {
			t.Errorf("tz %q: clock = %q, want the UTC fallback 18:30", tz, clock)
		}
	}
}

// TestRender_LocaleSelectsPrintedChrome asserts the locale actually changes
// what is drawn: each locale's own composed date, its own weekday name, its
// own "Ticket #" prefix and its own uppercase CATEGORY label — and never
// another locale's date form.
func TestRender_LocaleSelectsPrintedChrome(t *testing.T) {
	for _, locale := range SupportedLocales {
		t.Run(locale, func(t *testing.T) {
			tk := validTicket(t)
			// No poster: the date column spans the full width, so even the
			// long Spanish form lands as a single show operator.
			tk.OrgLogo, tk.PosterImage = nil, nil
			tk.Locale = locale
			tk.SessionStart = time.Date(2026, 12, 5, 18, 30, 0, 0, time.UTC)
			tk.SessionTZ = ""
			out := render(t, tk)

			date, _, weekday := formatShowTime(tk.SessionStart, "", locale)
			s := stringsFor(locale)
			for _, want := range []string{date, weekday, s.TicketNo + " " + tk.TicketNumber} {
				if !bytes.Contains(out, pdfText(want)) {
					t.Errorf("%s PDF missing %q", locale, want)
				}
			}
			if !pdfHasTracked(out, strings.ToUpper(s.Category)) {
				t.Errorf("%s PDF missing its own CATEGORY label", locale)
			}
			for _, other := range SupportedLocales {
				otherDate, _, _ := formatShowTime(tk.SessionStart, "", other)
				if other == locale || otherDate == date {
					continue
				}
				if bytes.Contains(out, pdfText(otherDate)) {
					t.Errorf("%s PDF also contains the %s date %q", locale, other, otherDate)
				}
			}
		})
	}
}

func TestDefaultNoteFor_DisclosesNonFiscalityInEveryLocale(t *testing.T) {
	for _, locale := range SupportedLocales {
		t.Run(locale, func(t *testing.T) {
			s := stringsFor(locale)
			note := defaultNoteFor(locale)
			if !strings.Contains(note, s.KeepNote) {
				t.Error("the note must carry the ported entrance instruction")
			}
			if !strings.Contains(note, s.NotFiscal) {
				t.Error("the note must disclose that the document is not a fiscal receipt")
			}
			for _, banned := range []string{"кассовый чек", "tax invoice", "fiscal receipt valid"} {
				if strings.Contains(note, banned) {
					t.Errorf("note contains the banned fragment %q", banned)
				}
			}
		})
	}
	if defaultNoteFor("xx") != defaultNoteFor("en") {
		t.Error("an unknown locale should fall back to the English note")
	}
}

func TestRender_UnsetFinePrint_PrintsTheLocalizedNote(t *testing.T) {
	for _, locale := range []string{"en", "ru", "cs", "es"} {
		t.Run(locale, func(t *testing.T) {
			tk := validTicket(t)
			tk.Locale = locale
			out := render(t, tk)
			// The note wraps across footer lines, each drawn as its own show
			// operator, so the opening words are matched as a PREFIX of
			// whatever the first wrapped line turned out to be.
			words := strings.Fields(stringsFor(locale).KeepNote)
			opening := strings.Join(words[:3], " ")
			if !bytes.Contains(out, pdfTextPrefix(opening)) {
				t.Errorf("PDF missing the localized closing note (looked for %q)", opening)
			}
		})
	}
}

func TestBuildLegalLines_LocalizedContactLabel(t *testing.T) {
	tk := Ticket{LegalName: "X s.r.o.", ContactEmail: "hi@x.example"}
	cases := []struct {
		locale string
		want   string
	}{
		{"en", "Contact: hi@x.example"},
		{"ru", "Контакт: hi@x.example"},
		{"cs", "Kontakt: hi@x.example"},
		{"es", "Contacto: hi@x.example"},
	}
	for _, tc := range cases {
		t.Run(tc.locale, func(t *testing.T) {
			got := buildLegalLines(tk, stringsFor(tc.locale).Contact)
			if last := got[len(got)-1]; last != tc.want {
				t.Errorf("got %q want %q", last, tc.want)
			}
		})
	}
}

func TestRender_FooterContactLabelIsLocalized(t *testing.T) {
	cases := []struct {
		locale string
		want   string
		banned []string
	}{
		{"ru", "Контакт: hi@x.example", []string{"Contact:", "Kontakt:"}},
		{"cs", "Kontakt: hi@x.example", []string{"Contact:", "Контакт:"}},
		{"es", "Contacto: hi@x.example", []string{"Contact:", "Контакт:"}},
	}
	for _, tc := range cases {
		t.Run(tc.locale, func(t *testing.T) {
			tk := validTicket(t)
			tk.Locale = tc.locale
			tk.LegalName = "X s.r.o."
			tk.ContactEmail = "hi@x.example"
			out := render(t, tk)
			if !bytes.Contains(out, pdfText(tc.want)) {
				t.Errorf("PDF missing localized contact line %q", tc.want)
			}
			for _, banned := range tc.banned {
				if bytes.Contains(out, pdfText(banned+" hi@x.example")) {
					t.Errorf("PDF should not contain a different locale's contact prefix %q", banned)
				}
			}
		})
	}
}

// ──────────────────────────────────────────────────────────────────────
// Minimal format-4 'cmap' parser — mirrors gofpdf's own subtable selection
// (utf8fontfile.go: platform 3/encoding 1, or platform 0, format 4) so
// TestDejaVuSansCondensed_HasHebrewGlyphs answers the same question gofpdf
// itself would ask of the font, independent of gofpdf's internals.
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
