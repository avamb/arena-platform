// ean13_symbol.go — a hand-drawn, dependency-free EAN-13 barcode symbol for
// the ticket PDF (feature: scannable venue-entry barcode, spec §11).
//
// The MACS access-control export identifies a ticket by its EAN-13 number,
// and venue entry may be gated by laser/handheld barcode scanners reading
// that number straight off the printed/displayed ticket. Printing the
// EAN-13 as plain text (the pre-existing behaviour) is not scannable by
// those devices, so this file draws the actual GS1 EAN-13 symbol: guard
// bars, six left digits (L/G parity selected by the first digit), six
// right digits (R code), quiet zones, and the human-readable digits
// underneath in the conventional 1+6+6 grouping.
//
// There is no barcode library in go.mod and none is added — every module
// is a vector filled rectangle drawn with gofpdf's own Rect(...,"F"), pure
// black on pure white, no anti-aliasing tricks. ean13Pattern/ean13Bars are
// pure functions with no gofpdf dependency so the encoder itself can be
// tested against known reference vectors independently of PDF rendering
// (see ean13_symbol_test.go).
package pdf

import (
	"fmt"
	"strings"

	"github.com/jung-kurt/gofpdf"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/barcodes/ean13"
)

// eanMMToPt converts a length in millimetres to PDF points, matching the
// "pt" unit every layoutSpec geometry constant in this package is expressed
// in (see layout.go's gofpdf.InitType{UnitStr: "pt"}). It is layout.go's
// mmToPt under the name this file's constants were written against.
const eanMMToPt = mmToPt

// eanMinBarHeightPt is the scannability floor: below this the renderer
// skips the symbol entirely rather than drawing a barcode too short to
// trust at the gate (about 12 mm — well under the nominal 20 mm spec
// value, used only as a last-resort shrink target on a page whose other
// content leaves very little room, e.g. a wrapped multi-line headline plus
// seat rows plus a long legal address all at once).
const eanMinBarHeightPt = 12 * eanMMToPt

// eanQuietLeftModules / eanQuietRightModules are the minimum quiet-zone
// widths either side of the symbol, in modules, per the GS1 spec (left
// quiet zone is wider than right because it also has to clear the
// human-readable "number system" digit printed further to the left).
const (
	eanQuietLeftModules  = 11
	eanQuietRightModules = 7
)

// eanTextGapPt is the vertical gap between the bottom of the (tallest)
// guard bars and the baseline area of the human-readable digits.
const eanTextGapPt = 2.0

// eanTextDescentPt is a small buffer reserved below the human-readable
// digits' baseline for descenders, so the returned y cursor never sits
// flush against the printed digits.
const eanTextDescentPt = 3.0

// ── GS1 EAN-13 encoding tables ──────────────────────────────────────────

// ean13LCode / ean13GCode encode digits 0-9 as 7-module patterns for the
// six LEFT-hand digits: L is odd parity (used directly for UPC-A / the
// EAN-13 first-digit-0 case), G is even parity (its mirror). Which of the
// two a given left-hand digit position uses is selected by ean13Parity,
// keyed on the code's own first digit.
var ean13LCode = [10]string{
	"0001101", "0011001", "0010011", "0111101", "0100011",
	"0110001", "0101111", "0111011", "0110111", "0001011",
}

var ean13GCode = [10]string{
	"0100111", "0110011", "0011011", "0100001", "0011101",
	"0111001", "0000101", "0010001", "0001001", "0010111",
}

// ean13RCode encodes digits 0-9 for the six RIGHT-hand digits (always this
// one table — no parity selection on the right half).
var ean13RCode = [10]string{
	"1110010", "1100110", "1101100", "1000010", "1011100",
	"1001110", "1010000", "1000100", "1001000", "1110100",
}

// ean13Parity[firstDigit] is a 6-character 'L'/'G' string selecting, left
// to right, which table encodes each of the six LEFT-hand digits (i.e.
// digits 2-7, 1-indexed, of the 13-digit code) — the standard GS1
// first-digit parity table that lets a 13-digit number encode in a
// 12-module (UPC-A shaped) left+right symbol.
var ean13Parity = [10]string{
	"LLLLLL", "LLGLGG", "LLGGLG", "LLGGGL", "LGLLGG",
	"LGGLLG", "LGGGLL", "LGLGLG", "LGLGGL", "LGGLGL",
}

// ean13Pattern returns the 95-module black/white bit pattern for a
// syntactically well-formed 13-digit EAN-13 code: '1' marks a black bar
// module, '0' a white/space module, left to right. Structure: start guard
// "101" (3) + six left digits (L/G, 7 modules each = 42) + centre guard
// "01010" (5) + six right digits (R, 7 modules each = 42) + end guard
// "101" (3) = 95 modules total.
//
// ean13Pattern only checks the input SHAPE (13 ASCII digits) — callers
// MUST validate the GS1 check digit with ean13.Valid first; a
// shape-valid-but-checksum-invalid code still produces a pattern (a
// scanner would simply reject the checksum), which is why drawEAN13Symbol
// gates on ean13.Valid before ever calling this.
func ean13Pattern(code string) (string, error) {
	if len(code) != 13 {
		return "", fmt.Errorf("pdf: ean13 pattern: want 13 digits, got %d", len(code))
	}
	digits := make([]int, 13)
	for i, r := range code {
		if r < '0' || r > '9' {
			return "", fmt.Errorf("pdf: ean13 pattern: non-digit rune %q at position %d", r, i)
		}
		digits[i] = int(r - '0')
	}
	parity := ean13Parity[digits[0]]

	var b strings.Builder
	b.Grow(95)
	b.WriteString("101") // start guard
	for i := 0; i < 6; i++ {
		d := digits[1+i]
		if parity[i] == 'L' {
			b.WriteString(ean13LCode[d])
		} else {
			b.WriteString(ean13GCode[d])
		}
	}
	b.WriteString("01010") // centre guard
	for i := 0; i < 6; i++ {
		b.WriteString(ean13RCode[digits[7+i]])
	}
	b.WriteString("101") // end guard
	return b.String(), nil
}

// ean13Bar is one filled bar (a maximal run of consecutive black modules)
// in a local coordinate frame: x=0 at the left edge of module 0 (the first
// module of the start guard, i.e. the quiet zone is NOT part of this
// frame), y=0 at the top of the bars — every bar shares the same top, only
// the three guard-bar groups (start/centre/end) extend further down.
type ean13Bar struct {
	x      float64 // left edge, pt
	width  float64 // pt
	height float64 // pt, measured down from the shared top
}

// ean13GuardModule reports whether module index i (0-based, 0..94) belongs
// to one of the three fixed guard-bar groups (start 0-2, centre 45-49, end
// 92-94) rather than a data digit.
func ean13GuardModule(i int) bool {
	return (i >= 0 && i <= 2) || (i >= 45 && i <= 49) || (i >= 92 && i <= 94)
}

// ean13Bars computes the filled bar rectangles for a validated 13-digit
// EAN-13 code. moduleW is the width of one module; barH is the height of
// an ordinary (non-guard) bar; guardExtra is how much taller the guard-bar
// groups are drawn, extending below the ordinary bars (both kinds share
// the same top, per GS1 convention — guard bars run a little past the data
// bars to make the symbol's start/centre/end easy to find by eye).
//
// This is a pure function with no gofpdf dependency, so tests can assert
// directly on the bar list (count, x positions, guard-vs-data heights)
// without rendering a PDF at all.
func ean13Bars(code string, moduleW, barH, guardExtra float64) ([]ean13Bar, error) {
	pattern, err := ean13Pattern(code)
	if err != nil {
		return nil, err
	}
	var bars []ean13Bar
	for i := 0; i < len(pattern); {
		if pattern[i] != '1' {
			i++
			continue
		}
		start := i
		for i < len(pattern) && pattern[i] == '1' {
			i++
		}
		runLen := i - start
		// A run's modules are either ALL inside a guard zone or ALL outside
		// it — the fixed guard patterns (101 / 01010 / 101) never share a
		// run of consecutive black modules with an adjacent data digit,
		// because every digit code both starts and ends the guard
		// boundaries land on a zero module. Checking the first module of
		// the run is therefore sufficient, but checking every module keeps
		// this correct even if that invariant is ever violated by a future
		// change to the tables above.
		guard := true
		for m := start; m < i; m++ {
			if !ean13GuardModule(m) {
				guard = false
				break
			}
		}
		h := barH
		if guard {
			h += guardExtra
		}
		bars = append(bars, ean13Bar{
			x:      float64(start) * moduleW,
			width:  float64(runLen) * moduleW,
			height: h,
		})
	}
	return bars, nil
}

// fitEANBarHeight decides the actual (barH, guardExtra) to draw given only
// `available` pt of vertical room before the next fixed element (the
// ticket-id line, then the footer). It never returns a bar height below
// eanMinBarHeightPt — the caller (drawEAN13Symbol) treats a false ok as
// "skip the symbol entirely, draw nothing" rather than a placeholder box.
//
// Degrades in two steps, cheapest first:
//  1. Nominal size fits — use it unchanged.
//  2. Shrink the (purely cosmetic) guard-bar extension to a minimal 1pt
//     nub, keeping the nominal bar height.
//  3. Shrink the bar height itself down to, but never below,
//     eanMinBarHeightPt.
func fitEANBarHeight(nominalBarH, nominalGuardExtra, textBlockH, available float64) (barH, guardExtra float64, ok bool) {
	if available >= nominalBarH+nominalGuardExtra+textBlockH {
		return nominalBarH, nominalGuardExtra, true
	}
	const minGuardExtra = 1.0
	if available >= nominalBarH+minGuardExtra+textBlockH {
		return nominalBarH, minGuardExtra, true
	}
	shrunk := available - minGuardExtra - textBlockH
	if shrunk < eanMinBarHeightPt {
		return 0, 0, false
	}
	return shrunk, minGuardExtra, true
}

// drawEAN13Symbol draws the scannable EAN-13 barcode symbol — guard bars,
// data bars, quiet zones kept pure white, and the human-readable digits
// beneath in the conventional 1+6+6 grouping — centred on the page at y,
// PROVIDED code passes ean13.Valid (checksum, not just shape) and there is
// at least eanMinBarHeightPt of usable room before footerLimit (the
// highest y the caller can afford this block plus the ticket-id line that
// follows it to reach, before it would overlap the legal/fine-print
// footer). An invalid code, or a valid code with no room at all, draws
// NOTHING — no empty box, no placeholder — and returns y unchanged so the
// rest of the layout (ticket id, footer) prints exactly as it would have
// without a barcode.
//
// Bars are pure black filled rectangles on the page's white background
// (pdf.Rect(...,"F"), no stroke, no grey, no anti-aliasing trick) — never
// rasterised, never given a grey fill. The quiet zones
// (eanQuietLeftModules/eanQuietRightModules) are left untouched: no bar,
// no text, nothing drawn there at all.
func drawEAN13Symbol(pdf *gofpdf.Fpdf, code string, spec layoutSpec, y, footerLimit float64) float64 {
	if !ean13.Valid(code) {
		return y
	}
	textBlockH := eanTextGapPt + spec.eanDigitFS + eanTextDescentPt
	barH, guardExtra, ok := fitEANBarHeight(spec.eanBarH, spec.eanGuardExtra, textBlockH, footerLimit-y)
	if !ok {
		return y
	}
	bars, err := ean13Bars(code, spec.eanModuleW, barH, guardExtra)
	if err != nil || len(bars) == 0 {
		return y
	}

	pdf.SetFont(fontFamily, "", spec.eanDigitFS)
	lead, left, right := code[:1], code[1:7], code[7:13]
	leadW := pdf.GetStringWidth(lead)
	leftW := pdf.GetStringWidth(left)
	rightW := pdf.GetStringWidth(right)

	quietLeft := float64(eanQuietLeftModules) * spec.eanModuleW
	quietRight := float64(eanQuietRightModules) * spec.eanModuleW
	symbolW := 95 * spec.eanModuleW
	totalW := leadW + spec.eanLeadGap + quietLeft + symbolW + quietRight
	startX := (spec.pageW - totalW) / 2
	// originX is the left edge of module 0 (start guard) — the frame
	// ean13Bars' x/width coordinates are relative to.
	originX := startX + leadW + spec.eanLeadGap + quietLeft

	pdf.SetFillColor(0, 0, 0)
	for _, bar := range bars {
		pdf.Rect(originX+bar.x, y, bar.width, bar.height, "F")
	}

	maxBarH := barH + guardExtra
	baselineY := y + maxBarH + eanTextGapPt + spec.eanDigitFS
	pdf.SetTextColor(0, 0, 0)
	pdf.Text(startX, baselineY, lead)
	leftHalfX := originX + 3*spec.eanModuleW
	leftHalfW := 42 * spec.eanModuleW
	pdf.Text(leftHalfX+(leftHalfW-leftW)/2, baselineY, left)
	rightHalfX := originX + 50*spec.eanModuleW
	rightHalfW := 42 * spec.eanModuleW
	pdf.Text(rightHalfX+(rightHalfW-rightW)/2, baselineY, right)

	return baselineY + eanTextDescentPt
}
