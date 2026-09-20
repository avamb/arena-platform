// ean13_symbol_test.go — encoder unit tests (independent decoder
// cross-check against known reference EAN-13 vectors), bar-geometry
// tests, and adaptive-sizing tests for the hand-drawn barcode symbol in
// ean13_symbol.go.
package pdf

import (
	"bytes"
	"context"
	"testing"

	"github.com/jung-kurt/gofpdf"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/barcodes/ean13"
)

// ── Independent decoder (deliberately NOT sharing code with
// ean13_symbol.go's tables) — cross-checks ean13Pattern by decoding its
// output back to 13 digits, including first-digit recovery from the L/G
// parity of the six left-hand groups. ────────────────────────────────────

var testLCode = map[string]int{
	"0001101": 0, "0011001": 1, "0010011": 2, "0111101": 3, "0100011": 4,
	"0110001": 5, "0101111": 6, "0111011": 7, "0110111": 8, "0001011": 9,
}

var testGCode = map[string]int{
	"0100111": 0, "0110011": 1, "0011011": 2, "0100001": 3, "0011101": 4,
	"0111001": 5, "0000101": 6, "0010001": 7, "0001001": 8, "0010111": 9,
}

var testRCode = map[string]int{
	"1110010": 0, "1100110": 1, "1101100": 2, "1000010": 3, "1011100": 4,
	"1001110": 5, "1010000": 6, "1000100": 7, "1001000": 8, "1110100": 9,
}

// testParityToFirstDigit maps a 6-character L/G string to the first digit
// it encodes — the reverse of ean13Parity.
var testParityToFirstDigit = map[string]int{
	"LLLLLL": 0, "LLGLGG": 1, "LLGGLG": 2, "LLGGGL": 3, "LGLLGG": 4,
	"LGGLLG": 5, "LGGGLL": 6, "LGLGLG": 7, "LGLGGL": 8, "LGGLGL": 9,
}

// decodeEAN13Pattern independently decodes a 95-module pattern back to its
// 13 digits: verifies the guards, decodes each of the six left groups
// against BOTH the L and G tables to recover the digit and which table
// matched, looks up the resulting L/G parity string to recover the first
// digit, then decodes the six right groups against the R table.
func decodeEAN13Pattern(t *testing.T, pattern string) string {
	t.Helper()
	if len(pattern) != 95 {
		t.Fatalf("pattern length = %d, want 95", len(pattern))
	}
	if pattern[0:3] != "101" {
		t.Fatalf("missing start guard: got %q", pattern[0:3])
	}
	if pattern[45:50] != "01010" {
		t.Fatalf("missing centre guard: got %q", pattern[45:50])
	}
	if pattern[92:95] != "101" {
		t.Fatalf("missing end guard: got %q", pattern[92:95])
	}

	parity := make([]byte, 6)
	leftDigits := make([]int, 6)
	for i := 0; i < 6; i++ {
		group := pattern[3+i*7 : 3+i*7+7]
		if d, ok := testLCode[group]; ok {
			parity[i] = 'L'
			leftDigits[i] = d
			continue
		}
		if d, ok := testGCode[group]; ok {
			parity[i] = 'G'
			leftDigits[i] = d
			continue
		}
		t.Fatalf("left group %d (%q) matches neither L nor G table", i, group)
	}
	firstDigit, ok := testParityToFirstDigit[string(parity)]
	if !ok {
		t.Fatalf("parity pattern %q matches no known first digit", string(parity))
	}

	rightDigits := make([]int, 6)
	for i := 0; i < 6; i++ {
		group := pattern[50+i*7 : 50+i*7+7]
		d, ok := testRCode[group]
		if !ok {
			t.Fatalf("right group %d (%q) matches no R-table entry", i, group)
		}
		rightDigits[i] = d
	}

	out := make([]byte, 0, 13)
	out = append(out, byte('0'+firstDigit))
	for _, d := range leftDigits {
		out = append(out, byte('0'+d))
	}
	for _, d := range rightDigits {
		out = append(out, byte('0'+d))
	}
	return string(out)
}

// TestEAN13Pattern_KnownVectors checks ean13Pattern against three
// well-known/published EAN-13 codes: two widely-cited reference barcodes
// and one in the platform's own "21" GS1-internal-use prefix range (spec
// §11, ean13.PlatformPrefix). Every vector is round-tripped through the
// independent decoder above, which must recover the exact 13 digits,
// including the first digit recovered purely from L/G parity.
func TestEAN13Pattern_KnownVectors(t *testing.T) {
	platformCode := ean13.Encode(ean13.PlatformPrefix, 4830197526)

	vectors := []string{
		"4006381333931", // widely-published reference EAN-13 (Kinder Überraschung)
		"5901234123457", // widely-used generic EAN-13 reference/test code
		platformCode,    // platform "21..." prefix range
	}
	for _, code := range vectors {
		t.Run(code, func(t *testing.T) {
			if !ean13.Valid(code) {
				t.Fatalf("test vector %q is not a valid EAN-13 (bad check digit)", code)
			}
			pattern, err := ean13Pattern(code)
			if err != nil {
				t.Fatalf("ean13Pattern: %v", err)
			}
			if len(pattern) != 95 {
				t.Fatalf("pattern length = %d, want 95", len(pattern))
			}
			for i, c := range pattern {
				if c != '0' && c != '1' {
					t.Fatalf("pattern[%d] = %q, want '0' or '1'", i, c)
				}
			}
			got := decodeEAN13Pattern(t, pattern)
			if got != code {
				t.Fatalf("round-trip decode = %q, want %q", got, code)
			}
		})
	}
}

func TestEAN13Pattern_RejectsBadShape(t *testing.T) {
	cases := []string{
		"",               // empty
		"123",            // too short
		"12345678901234", // too long (14)
		"400638133393X",  // non-digit
		"40063813339 1",  // embedded space
	}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			if _, err := ean13Pattern(in); err == nil {
				t.Fatalf("ean13Pattern(%q): expected error, got nil", in)
			}
		})
	}
}

// TestEAN13Pattern_PlatformPrefixStartsWithTwoOnes derives digit "2" and
// "1"'s bit groups directly from the spec's own L-code table (first digit
// "2" selects parity LLGGLG; the second-position digit "1" then uses that
// group's L or G code) as an extra spec-table cross-check independent of
// the decoder above.
func TestEAN13Pattern_PlatformPrefixStartsWithTwoOnes(t *testing.T) {
	code := ean13.Encode(ean13.PlatformPrefix, 4830197526) // "21" + 10 digits + check
	if code[0:2] != "21" {
		t.Fatalf("test setup: platform code %q does not start with GS1 prefix 21", code)
	}
	pattern, err := ean13Pattern(code)
	if err != nil {
		t.Fatalf("ean13Pattern: %v", err)
	}
	// First digit '2' selects parity "LLGGLG" (ean13Parity[2]); the second
	// digit is '1', drawn with an L code since parity[0]=='L'.
	wantFirstGroup := ean13LCode[1] // digit '1', L-coded
	if got := pattern[3:10]; got != wantFirstGroup {
		t.Errorf("first left group = %q, want %q (L-code for digit 1)", got, wantFirstGroup)
	}
}

// ── Bar geometry ─────────────────────────────────────────────────────────

func TestEAN13Bars_StructureAndGuardHeight(t *testing.T) {
	code := ean13.Encode(ean13.PlatformPrefix, 4830197526)
	const moduleW, barH, guardExtra = 1.0, 60.0, 5.0
	bars, err := ean13Bars(code, moduleW, barH, guardExtra)
	if err != nil {
		t.Fatalf("ean13Bars: %v", err)
	}
	if len(bars) == 0 {
		t.Fatal("expected a non-empty bar list for a valid code")
	}
	// Every bar must be fully within the 95-module frame (x=0 is the left
	// edge of the start guard — the quiet zone is NOT part of this frame,
	// so no bar should ever start before x=0 or extend past x=95*moduleW).
	for i, b := range bars {
		if b.x < 0 {
			t.Errorf("bar %d starts before the symbol frame: x=%v", i, b.x)
		}
		if b.x+b.width > 95*moduleW+1e-9 {
			t.Errorf("bar %d extends past the symbol frame: x+w=%v", i, b.x+b.width)
		}
		if b.height != barH && b.height != barH+guardExtra {
			t.Errorf("bar %d has an unexpected height %v (want %v or %v)", i, b.height, barH, barH+guardExtra)
		}
	}
	// The very first bar is the start guard: it must begin at x=0 and be
	// guard-height (taller than an ordinary data bar).
	first := bars[0]
	if first.x != 0 {
		t.Errorf("first bar does not start at x=0: got %v", first.x)
	}
	if first.height != barH+guardExtra {
		t.Errorf("start guard bar height = %v, want %v (barH+guardExtra)", first.height, barH+guardExtra)
	}
	// The very last bar is the end guard: it must end exactly at x=95*moduleW
	// and be guard-height too.
	last := bars[len(bars)-1]
	if last.x+last.width != 95*moduleW {
		t.Errorf("last bar does not end at the symbol's right edge: got %v want %v", last.x+last.width, 95*moduleW)
	}
	if last.height != barH+guardExtra {
		t.Errorf("end guard bar height = %v, want %v (barH+guardExtra)", last.height, barH+guardExtra)
	}
}

func TestEAN13Bars_InvalidShapePropagatesError(t *testing.T) {
	if _, err := ean13Bars("not-a-code", 1, 1, 1); err == nil {
		t.Fatal("expected error for a non-digit code")
	}
}

// ── Adaptive sizing (fitEANBarHeight) ───────────────────────────────────

func TestFitEANBarHeight(t *testing.T) {
	const nominalBarH, nominalGuardExtra, textBlockH = 56.69, 4.25, 12.0

	t.Run("nominal size fits", func(t *testing.T) {
		barH, guardExtra, ok := fitEANBarHeight(nominalBarH, nominalGuardExtra, textBlockH, 200)
		if !ok {
			t.Fatal("expected ok=true with ample room")
		}
		if barH != nominalBarH || guardExtra != nominalGuardExtra {
			t.Errorf("got (%v, %v), want nominal (%v, %v)", barH, guardExtra, nominalBarH, nominalGuardExtra)
		}
	})

	t.Run("shrinks guard extra first when tight", func(t *testing.T) {
		// Room for the nominal bar height plus a 1pt guard nub plus text,
		// but not the full nominal guardExtra.
		available := nominalBarH + 1.0 + textBlockH + 0.5
		barH, guardExtra, ok := fitEANBarHeight(nominalBarH, nominalGuardExtra, textBlockH, available)
		if !ok {
			t.Fatal("expected ok=true")
		}
		if barH != nominalBarH {
			t.Errorf("bar height should stay nominal while only guardExtra shrinks: got %v", barH)
		}
		if guardExtra >= nominalGuardExtra {
			t.Errorf("guardExtra should have shrunk below nominal %v, got %v", nominalGuardExtra, guardExtra)
		}
	})

	t.Run("shrinks bar height down to the scannability floor", func(t *testing.T) {
		available := eanMinBarHeightPt + 1.0 + textBlockH
		barH, _, ok := fitEANBarHeight(nominalBarH, nominalGuardExtra, textBlockH, available)
		if !ok {
			t.Fatal("expected ok=true just above the floor")
		}
		if barH < eanMinBarHeightPt {
			t.Errorf("bar height %v fell below the floor %v", barH, eanMinBarHeightPt)
		}
		if barH >= nominalBarH {
			t.Errorf("expected a shrunk bar height, got %v (nominal %v)", barH, nominalBarH)
		}
	})

	t.Run("skips entirely below the scannability floor", func(t *testing.T) {
		_, _, ok := fitEANBarHeight(nominalBarH, nominalGuardExtra, textBlockH, eanMinBarHeightPt-1)
		if ok {
			t.Fatal("expected ok=false when even the floor height cannot fit")
		}
	})

	t.Run("skips entirely with negative/zero room", func(t *testing.T) {
		_, _, ok := fitEANBarHeight(nominalBarH, nominalGuardExtra, textBlockH, -5)
		if ok {
			t.Fatal("expected ok=false with no room at all")
		}
	})
}

// ── Render-level integration: valid / invalid / empty EAN13 ────────────

// eanRenderTicket returns a plain ASCII-only ticket (no seat rows, short
// legal block) so these tests isolate the EAN-13 behaviour from the
// tight-layout adaptive-sizing path exercised elsewhere.
func eanRenderTicket(t *testing.T) Ticket {
	t.Helper()
	return validTicket(t)
}

func TestRender_ValidEAN13_DrawsHumanReadableDigitGroups(t *testing.T) {
	tk := eanRenderTicket(t)
	tk.EAN13 = ean13.Encode(ean13.PlatformPrefix, 9988776655)
	out, err := Render(context.Background(), tk)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for _, want := range []string{tk.EAN13[:1], tk.EAN13[1:7], tk.EAN13[7:13]} {
		if !bytes.Contains(out, pdfText(want)) {
			t.Errorf("PDF missing EAN-13 human-readable digit group %q", want)
		}
	}
}

func TestRender_InvalidEAN13_DrawsNothingNoError(t *testing.T) {
	tk := eanRenderTicket(t)
	tk.EAN13 = "1234567890123" // wrong check digit — ean13.Valid rejects it
	if ean13.Valid(tk.EAN13) {
		t.Fatalf("test setup: %q is unexpectedly a valid EAN-13", tk.EAN13)
	}
	out, err := Render(context.Background(), tk)
	if err != nil {
		t.Fatalf("Render must not error on an invalid EAN13: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("expected non-empty PDF")
	}
	if bytes.Contains(out, pdfText(tk.EAN13[1:7])) {
		t.Error("PDF should not print any EAN-13 digit grouping for an invalid code")
	}
}

func TestRender_EmptyEAN13_DrawsNothingNoError(t *testing.T) {
	tk := eanRenderTicket(t)
	tk.EAN13 = ""
	out, err := Render(context.Background(), tk)
	if err != nil {
		t.Fatalf("Render must not error with an empty EAN13: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("expected non-empty PDF")
	}
	// Rest of the layout (ticket number, footer) must still be intact.
	if !bytes.Contains(out, pdfText(stringsFor("en").TicketNo+" "+displayTicketNumber(tk))) {
		t.Error("PDF missing Ticket ID line when EAN13 is empty")
	}
}

// newTestPDFForEAN returns a freshly constructed, font-registered
// *gofpdf.Fpdf with a page already added — the minimum drawEAN13Symbol
// needs to call GetStringWidth/Rect/Text without a full renderWithSpec
// pass.
func newTestPDFForEAN(t *testing.T) *gofpdf.Fpdf {
	t.Helper()
	pdf := gofpdf.NewCustom(&gofpdf.InitType{
		OrientationStr: "P",
		UnitStr:        "pt",
		Size:           gofpdf.SizeType{Wd: ticketSpec.pageW, Ht: ticketSpec.pageH},
	})
	registerFonts(pdf)
	pdf.AddPage()
	return pdf
}

func TestDrawEAN13Symbol_ZeroCeiling_DrawsNothing(t *testing.T) {
	// A direct unit check of the "skip gracefully, no placeholder" contract:
	// with footerLimit==y (no room at all) drawEAN13Symbol must return y
	// unchanged.
	code := ean13.Encode(ean13.PlatformPrefix, 1122334455)
	pdfDoc := newTestPDFForEAN(t)
	const y = 100.0
	got := drawEAN13Symbol(pdfDoc, code, ticketSpec, y, y)
	if got != y {
		t.Errorf("expected y unchanged (%v) when there is no room, got %v", y, got)
	}
}

func TestDrawEAN13Symbol_InvalidCode_DrawsNothing(t *testing.T) {
	pdfDoc := newTestPDFForEAN(t)
	const y = 100.0
	got := drawEAN13Symbol(pdfDoc, "1234567890123", ticketSpec, y, y+500)
	if got != y {
		t.Errorf("expected y unchanged (%v) for an invalid code, got %v", y, got)
	}
}
