// layout_test.go — the geometric contracts of the ported design: the
// anchored code block, the font-shrink rules, the machine-readable payload,
// and what the page looks like when the optional elements are absent (which
// is how both of the first live clients' tickets actually render).
package pdf

import (
	"bytes"
	"context"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/skip2/go-qrcode"
)

// noArt strips the organizer logo and the event poster. Both of the first
// live clients have neither, so this — not validTicket — is what their
// tickets actually look like.
func noArt(tk Ticket) Ticket {
	tk.OrgLogo, tk.PosterImage = nil, nil
	return tk
}

// textOpAt locates the text-show operator that drew s and returns the
// position gofpdf emitted for it, in PDF user space (y from the page
// bottom). gofpdf writes "BT <x> <y> Td (<text>)Tj ET", so the coordinates
// sit immediately before the token pdfText builds.
func textOpAt(t *testing.T, out []byte, s string) (x, y float64) {
	t.Helper()
	idx := bytes.Index(out, pdfText(s))
	if idx < 0 {
		t.Fatalf("no text operator drew %q", s)
	}
	start := bytes.LastIndex(out[:idx], []byte("BT "))
	if start < 0 {
		t.Fatalf("no BT before the text operator for %q", s)
	}
	fields := strings.Fields(string(out[start+3 : idx]))
	for i, f := range fields {
		if f == "Td" && i >= 2 {
			return mustParse(t, fields[i-2]), mustParse(t, fields[i-1])
		}
	}
	t.Fatalf("no Td operator before %q (fields %v)", s, fields)
	return 0, 0
}

func mustParse(t *testing.T, s string) float64 {
	t.Helper()
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return v
}

func render(t *testing.T, tk Ticket) []byte {
	t.Helper()
	out, err := Render(context.Background(), tk)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	return out
}

// ── The anchored bottom block ───────────────────────────────────────────

// theQR returns the QR's drawn box. It is the LAST image on the page: the
// logo and the poster, when present, are drawn before it.
func theQR(t *testing.T, out []byte) box {
	t.Helper()
	imgs := drawnBoxes(t, out)
	if len(imgs) == 0 {
		t.Fatal("no image drawn — expected at least the QR")
	}
	return imgs[len(imgs)-1]
}

// barcodeBars returns the filled rectangles that make up the EAN-13 symbol:
// every bar shares the top edge the symbol was drawn at, which is exactly one
// qrGapBelow under the QR.
func barcodeBars(t *testing.T, out []byte, qr box) []box {
	t.Helper()
	want := qr.bottom() + ticketSpec.qrGapBelow
	bars := []box{}
	for _, r := range filledRects(t, out) {
		if diff := r.top - want; diff < 0.05 && diff > -0.05 {
			bars = append(bars, r)
		}
	}
	if len(bars) == 0 {
		t.Fatalf("no barcode bars found at y=%.2f", want)
	}
	return bars
}

// barcodeFootprint is how much page the barcode actually occupies: the bars,
// plus the quiet zones that must stay blank either side of them, plus the
// human-readable leading digit printed to the left of the left quiet zone.
func barcodeFootprint(t *testing.T, bars []box, leadW float64) (left, right, top, bottom float64) {
	t.Helper()
	left, right = bars[0].x, bars[0].right()
	top, bottom = bars[0].top, bars[0].bottom()
	for _, b := range bars {
		if b.x < left {
			left = b.x
		}
		if b.right() > right {
			right = b.right()
		}
		if b.bottom() > bottom {
			bottom = b.bottom()
		}
	}
	left -= float64(eanQuietLeftModules)*ticketSpec.eanModuleW + ticketSpec.eanLeadGap + leadW
	right += float64(eanQuietRightModules) * ticketSpec.eanModuleW
	return left, right, top, bottom
}

// leadDigitWidth measures the human-readable leading digit at the size
// drawEAN13Symbol prints it.
func leadDigitWidth(t *testing.T, code string) float64 {
	t.Helper()
	doc := newTestPDFForEAN(t)
	doc.SetFont(fontFamily, "", ticketSpec.eanDigitFS)
	return doc.GetStringWidth(code[:1])
}

// TestBottomBlock_IsAnchoredRegardlessOfTitleLength is THE constraint of
// this design: the codes sit at the same place on every page, whatever flows
// above them. One PDF carries a whole order, one ticket per page, so if a
// long event name could push the block down, consecutive tickets' codes would
// stop being a full page apart and an entrance scanner could grab the
// neighbouring ticket's.
func TestBottomBlock_IsAnchoredRegardlessOfTitleLength(t *testing.T) {
	short := validTicket(t)
	short.EventName = "Gala"

	long := validTicket(t)
	long.EventName = "An extraordinarily long event name that wraps across several " +
		"lines of the ticket and would push anything that merely flowed after it " +
		"much further down the page than the designer intended"

	shortQR := theQR(t, render(t, short))
	longQR := theQR(t, render(t, long))

	if shortQR != longQR {
		t.Errorf("the QR moved with the title length: %+v vs %+v", shortQR, longQR)
	}
	if diff := shortQR.top - ticketSpec.bottomTop; diff > 0.05 || diff < -0.05 {
		t.Errorf("the QR starts at %.2fpt, want the anchor %.2fpt (140 mm from the top)",
			shortQR.top, ticketSpec.bottomTop)
	}
	if diff := shortQR.x - (ticketSpec.pageW-shortQR.w)/2; diff > 0.05 || diff < -0.05 {
		t.Errorf("the QR is not centred: x=%.2f, w=%.2f", shortQR.x, shortQR.w)
	}
}

// TestBottomBlock_HasNoTearLine pins the owner's third change. The ported
// WordPress design had a dashed rule above the code block, for a paper ticket
// somebody would tear at the gate; these are read off a phone and nobody
// tears them. It was the only stroked line on the page, so its absence is
// exactly "no stroked line and no dash pattern anywhere".
func TestBottomBlock_HasNoTearLine(t *testing.T) {
	dashOpRe := regexp.MustCompile(`\[[0-9. ]*\] [0-9.]+ d`)
	for _, tk := range []Ticket{validTicket(t), noArt(validTicket(t))} {
		out := render(t, tk)
		if ys := strokedLineYs(out); len(ys) != 0 {
			t.Errorf("the tear line is back: %d stroked lines at %v", len(ys), ys)
		}
		if m := dashOpRe.Find(out); m != nil {
			t.Errorf("a dash pattern is still set (%q) — only the tear line ever used one", m)
		}
	}
}

// TestCodeBlock_QRIsPrimaryBarcodeIsSecondary is the owner's first two
// changes, as geometry. Entrance control (MACS) scans the QR, and a QR reads
// off a phone screen far more reliably than a barcode does, so the QR is the
// mark the buyer holds up: centred, and big. The barcode is demoted to an
// aid — narrower, shorter, below — while its human-readable digits stay large
// enough to read aloud and type, because those digits are the manual fallback
// when a scanner fails.
func TestCodeBlock_QRIsPrimaryBarcodeIsSecondary(t *testing.T) {
	tk := noArt(validTicket(t))
	out := render(t, tk)

	qr := theQR(t, out)
	if qr.w < mm(50) {
		t.Errorf("the QR is %.1f mm across; the owner asked for 50 mm or more", qr.w/mmToPt)
	}
	if diff := qr.w - qr.h; diff > 0.05 || diff < -0.05 {
		t.Errorf("the QR is not square: %.2f x %.2f", qr.w, qr.h)
	}

	bars := barcodeBars(t, out, qr)
	left, right, top, bottom := barcodeFootprint(t, bars, leadDigitWidth(t, tk.EAN13))
	barW, barH := right-left, bottom-top

	// Narrower AND shorter than the QR, in both directions, so that whatever
	// the buyer's eye lands on first is the QR.
	if barW >= qr.w {
		t.Errorf("the barcode (%.1f mm wide) is not narrower than the QR (%.1f mm)",
			barW/mmToPt, qr.w/mmToPt)
	}
	if barH >= qr.h {
		t.Errorf("the barcode (%.1f mm tall) is not shorter than the QR (%.1f mm)",
			barH/mmToPt, qr.h/mmToPt)
	}
	// The owner asked for roughly 55-65 mm; the bounds are loose enough that
	// a deliberate tweak passes and a regression back to the old full-width
	// (~96 mm) symbol does not.
	if barW < mm(50) || barW > mm(68) {
		t.Errorf("the barcode is %.1f mm wide, want roughly 55-65 mm", barW/mmToPt)
	}
	// The bars must still be tall enough to scan.
	if barH < eanMinBarHeightPt {
		t.Errorf("the bars are %.1f mm tall, below the %.1f mm scannability floor",
			barH/mmToPt, eanMinBarHeightPt/mmToPt)
	}
	// ...and the digits under them must stay the legible manual fallback:
	// larger than the ticket-number line they sit above.
	if ticketSpec.eanDigitFS <= ticketSpec.ticketNoFS {
		t.Errorf("the human-readable digits (%.1fpt) are no larger than the ticket number (%.1fpt)",
			ticketSpec.eanDigitFS, ticketSpec.ticketNoFS)
	}
	if !usesFontSize(out, ticketSpec.eanDigitFS) {
		t.Errorf("the human-readable digits were not drawn at %.1fpt", ticketSpec.eanDigitFS)
	}
}

// TestCodeBlock_QuietZonesStayBlank guards the GS1 requirement that survives
// the barcode's demotion: narrowing the symbol narrows its quiet zones with
// it, and anything drawn into them stops a scanner finding the symbol's
// edges. The leading human-readable digit is deliberately printed to the LEFT
// of the left quiet zone, not inside it.
func TestCodeBlock_QuietZonesStayBlank(t *testing.T) {
	tk := noArt(validTicket(t))
	out := render(t, tk)

	qr := theQR(t, out)
	bars := barcodeBars(t, out, qr)
	barsLeft, barsRight := bars[0].x, bars[0].right()
	barsBottom := bars[0].bottom()
	for _, b := range bars {
		if b.x < barsLeft {
			barsLeft = b.x
		}
		if b.right() > barsRight {
			barsRight = b.right()
		}
		if b.bottom() > barsBottom {
			barsBottom = b.bottom()
		}
	}
	quietL := barsLeft - float64(eanQuietLeftModules)*ticketSpec.eanModuleW
	quietR := barsRight + float64(eanQuietRightModules)*ticketSpec.eanModuleW

	inQuietZone := func(x, top float64) bool {
		if top < bars[0].top-1 || top > barsBottom+1 {
			return false
		}
		return (x >= quietL && x < barsLeft) || (x > barsRight && x <= quietR)
	}
	for _, r := range filledRects(t, out) {
		if inQuietZone(r.x, r.top) || inQuietZone(r.right(), r.top) {
			t.Errorf("a filled rectangle intrudes into a quiet zone: %+v", r)
		}
	}
	for _, b := range textBaselines(t, out) {
		if inQuietZone(b.x, b.top) {
			t.Errorf("text is drawn inside a quiet zone at x=%.2f", b.x)
		}
	}
}

// TestCodeBlock_FitsThePageWithoutOverflow is the counterweight to making the
// QR big: the block is anchored, so everything it grew into has to come out
// of the room below it. The worst realistic ticket carries a full legal
// identification block AND a long organizer note, which together wrap the
// footer to a dozen lines.
func TestCodeBlock_FitsThePageWithoutOverflow(t *testing.T) {
	long := noArt(validTicket(t))
	long.LegalName = "Actorre Producciones Sociedad Limitada"
	long.LegalAddressLine1 = "Calle Mayor 14, 2 B"
	long.LegalAddressLine2 = "Edificio Las Palmeras"
	long.LegalAddressPostalCode = "03140"
	long.LegalAddressCity = "Guardamar del Segura"
	long.LegalAddressCountry = "Espana"
	long.ContactEmail = "hola@actorre.es"
	long.FinePrint = strings.Repeat(
		"Show the barcode at the entrance and do not share this ticket publicly. ", 4)

	for name, tk := range map[string]Ticket{
		"typical":     noArt(validTicket(t)),
		"with art":    validTicket(t),
		"long footer": long,
	} {
		t.Run(name, func(t *testing.T) {
			out := render(t, tk)
			if low := lowestInk(t, out); low > ticketSpec.pageH {
				t.Errorf("the page overflows by %.1f mm", (low-ticketSpec.pageH)/mmToPt)
			}
			// The QR never yields past its floor, and the barcode is never
			// squeezed below the scannability floor either — a page this
			// tight should shrink the QR, not drop the symbol.
			qr := theQR(t, out)
			if qr.w < ticketSpec.qrMinSize-0.05 {
				t.Errorf("the QR shrank to %.1f mm, below the %.1f mm floor",
					qr.w/mmToPt, ticketSpec.qrMinSize/mmToPt)
			}
			if bars := barcodeBars(t, out, qr); len(bars) != 30 {
				t.Errorf("expected the full 30-bar EAN-13 symbol, got %d bars", len(bars))
			}
		})
	}
}

// TestFitQRSize_YieldsBeforeAnythingOverflows pins the priority order inside
// the code block: the QR is drawn at its nominal size whenever the nominal
// barcode fits under it, gives back edge — never below qrMinSize — when an
// unusually tall footer squeezes the block, and stops yielding at the floor
// because a 40 mm QR still scans while a footer printed off the page does
// not.
func TestFitQRSize_YieldsBeforeAnythingOverflows(t *testing.T) {
	below := ticketSpec.qrGapBelow + ticketSpec.eanBarH + ticketSpec.eanGuardExtra +
		eanTextBlockH(ticketSpec)

	if got := fitQRSize(ticketSpec, ticketSpec.qrSize+below+mm(20)); got != ticketSpec.qrSize {
		t.Errorf("with room to spare the QR should stay nominal (%.2f), got %.2f", ticketSpec.qrSize, got)
	}
	tight := ticketSpec.qrSize - mm(8) + below
	if got := fitQRSize(ticketSpec, tight); got >= ticketSpec.qrSize || got < ticketSpec.qrMinSize {
		t.Errorf("a squeezed block should shrink the QR into [%.2f, %.2f), got %.2f",
			ticketSpec.qrMinSize, ticketSpec.qrSize, got)
	}
	if got := fitQRSize(ticketSpec, 0); got != ticketSpec.qrMinSize {
		t.Errorf("the QR must stop yielding at %.2f, got %.2f", ticketSpec.qrMinSize, got)
	}
}

// TestBottomBlock_CodesStayCloseTogether guards the sizing argument written
// on ticketSpec: the QR and the barcode of ONE ticket may be read
// interchangeably (they carry the same value), but the nearest pair belonging
// to DIFFERENT tickets — one page apart, since one PDF now carries a whole
// order — must stay further apart than a fit-to-width phone viewport can show
// (~245 mm on a 21:9 screen). A big QR makes this MORE important, not less:
// it resolves from further away and further off-axis than the old 32 mm one.
func TestBottomBlock_CodesStayCloseTogether(t *testing.T) {
	out := render(t, noArt(validTicket(t)))

	qr := theQR(t, out)
	bars := barcodeBars(t, out, qr)
	_, _, barcodeTop, _ := barcodeFootprint(t, bars, leadDigitWidth(t, validTicket(t).EAN13))

	// To scan two DIFFERENT tickets at once, a viewport would have to hold
	// this page's LAST code (the barcode) and the next page's FIRST code
	// (that page's QR) in full. Pages are exactly one page height apart.
	needed := (ticketSpec.pageH + qr.bottom()) - barcodeTop
	const viewport = 245 // mm shown by a fit-to-width 21:9 phone at 105 mm wide
	if needed <= mm(viewport) {
		t.Errorf("a %.0f mm viewport would show two different tickets' codes at once "+
			"(it takes only %.1f mm to hold both)", float64(viewport), needed/mmToPt)
	}
}

// TestBottomBlock_NoEAN13DrawsNeitherCode pins the honest-degradation rule:
// a legacy ticket with no (or a checksum-invalid) EAN-13 gets no QR and no
// barcode at all, rather than a mark that looks scannable and resolves to
// nothing. The rest of the page still prints.
func TestBottomBlock_NoEAN13DrawsNeitherCode(t *testing.T) {
	for _, code := range []string{"", "1234567890123" /* bad check digit */} {
		t.Run("ean="+code, func(t *testing.T) {
			tk := validTicket(t)
			tk.OrgLogo, tk.PosterImage = nil, nil
			tk.EAN13 = code
			out := render(t, tk)
			if imgs := drawnImages(t, out); len(imgs) != 0 {
				t.Errorf("expected no image at all, got %+v", imgs)
			}
			if !bytes.Contains(out, pdfText("Ticket # "+tk.TicketNumber)) {
				t.Error("the ticket number must still print without a code")
			}
		})
	}
}

// ── The machine-readable payload ────────────────────────────────────────

// TestQRPayload_IsTheEAN13AndNeverTheUUID is the owner's explicit
// requirement: entrance control reads the QR, the QR scans more reliably off
// a phone screen than a barcode, and both marks must resolve to the SAME
// value so it does not matter which one a scanner happens to catch. The
// ticket UUID — which the pre-port renderer encoded — must appear nowhere in
// the document, in any form.
func TestQRPayload_IsTheEAN13AndNeverTheUUID(t *testing.T) {
	tk := validTicket(t)
	tk.OrgLogo, tk.PosterImage = nil, nil
	out := render(t, tk)

	wantQR, err := qrcode.Encode(tk.EAN13, qrcode.High, qrPixelSize)
	if err != nil {
		t.Fatalf("encode expected qr: %v", err)
	}
	if !bytes.Contains(out, pngIDAT(t, wantQR)) {
		t.Error("the embedded QR is not the one encoding the EAN-13 digits")
	}

	uuidQR, err := qrcode.Encode(tk.TicketID, qrcode.High, qrPixelSize)
	if err != nil {
		t.Fatalf("encode uuid qr: %v", err)
	}
	if bytes.Contains(out, pngIDAT(t, uuidQR)) {
		t.Error("the document still embeds a QR encoding the ticket UUID")
	}

	// And the UUID must not be printed either — neither as raw bytes
	// (metadata, operators) nor as the UTF-16BE text the layout emits.
	assertNoUUID(t, out, tk.TicketID)
	if bytes.Contains(out, []byte(strings.ReplaceAll(tk.TicketID, "-", ""))) {
		t.Error("the dash-stripped ticket UUID appears in the document")
	}
	// The human-facing number is what the buyer gets instead.
	if !bytes.Contains(out, pdfText("Ticket # "+tk.TicketNumber)) {
		t.Error("the page does not print the human-facing ticket number")
	}
}

// TestBarcodeAndQR_CarryTheSameDigits cross-checks the second half of the
// same requirement: the barcode's own human-readable digits (drawn by
// drawEAN13Symbol in the conventional 1+6+6 grouping) spell exactly the
// value the QR encodes.
func TestBarcodeAndQR_CarryTheSameDigits(t *testing.T) {
	tk := validTicket(t)
	out := render(t, tk)
	for _, group := range []string{tk.EAN13[:1], tk.EAN13[1:7], tk.EAN13[7:13]} {
		if !bytes.Contains(out, pdfText(group)) {
			t.Errorf("PDF missing EAN-13 digit group %q", group)
		}
	}
}

// ── Font-shrink rules ───────────────────────────────────────────────────

func TestTitle_ShrinksPastTheLengthBudget(t *testing.T) {
	short := validTicket(t)
	short.EventName = strings.Repeat("a", ticketSpec.titleLongThreshold)
	out := render(t, short)
	if !usesFontSize(out, ticketSpec.titleFS) {
		t.Errorf("a %d-character title should print at %vpt", ticketSpec.titleLongThreshold, ticketSpec.titleFS)
	}
	if usesFontSize(out, ticketSpec.titleFSLong) {
		t.Errorf("a %d-character title should not shrink", ticketSpec.titleLongThreshold)
	}

	long := validTicket(t)
	long.EventName = strings.Repeat("a", ticketSpec.titleLongThreshold+1)
	out = render(t, long)
	if !usesFontSize(out, ticketSpec.titleFSLong) {
		t.Errorf("a %d-character title should shrink to %vpt", ticketSpec.titleLongThreshold+1, ticketSpec.titleFSLong)
	}
	if usesFontSize(out, ticketSpec.titleFS) {
		t.Error("the shrunk title must not also be drawn at the full size")
	}
}

// TestInfoValue_ShrinksPastTheLengthBudget uses a ticket with exactly ONE
// info cell so the assertion cannot be satisfied by some other cell's value.
func TestInfoValue_ShrinksPastTheLengthBudget(t *testing.T) {
	base := func(category string) Ticket {
		tk := validTicket(t)
		tk.OrgLogo, tk.PosterImage = nil, nil
		tk.HolderName, tk.PriceMinor = "", nil
		tk.SeatSector, tk.SeatRow, tk.SeatNumber = "", "", ""
		tk.TierName = category
		return tk
	}

	out := render(t, base(strings.Repeat("v", ticketSpec.infoValueLongThreshold)))
	if !usesFontSize(out, ticketSpec.infoValueFS) {
		t.Errorf("a %d-character value should print at %vpt", ticketSpec.infoValueLongThreshold, ticketSpec.infoValueFS)
	}
	if usesFontSize(out, ticketSpec.infoValueFSLong) {
		t.Error("a value at the budget should not shrink")
	}

	out = render(t, base(strings.Repeat("v", ticketSpec.infoValueLongThreshold+1)))
	if !usesFontSize(out, ticketSpec.infoValueFSLong) {
		t.Errorf("a %d-character value should shrink to %vpt", ticketSpec.infoValueLongThreshold+1, ticketSpec.infoValueFSLong)
	}
	if usesFontSize(out, ticketSpec.infoValueFS) {
		t.Error("the shrunk value must not also be drawn at the full size")
	}
}

// TestFitTrackedLabel_NeverOverflowsItsColumn carries forward the lesson of
// the label-overlap defect the pre-port layout had: a locale whose word is
// longer than English's must shed its letter-spacing, then its size, rather
// than run into the neighbouring column.
func TestFitTrackedLabel_NeverOverflowsItsColumn(t *testing.T) {
	doc := newTestPDFForEAN(t)
	const nominal, tracking = 7.2, 0.6

	t.Run("nominal fits a wide column", func(t *testing.T) {
		fs, tr := fitTrackedLabel(doc, "CATEGORY", nominal, tracking, 200)
		if fs != nominal || tr != tracking {
			t.Errorf("got (%v, %v), want the nominal (%v, %v)", fs, tr, nominal, tracking)
		}
	})

	t.Run("drops the tracking before the size", func(t *testing.T) {
		doc.SetFont(fontFamily, "B", nominal)
		bare := doc.GetStringWidth("CATEGORY")
		fs, tr := fitTrackedLabel(doc, "CATEGORY", nominal, tracking, bare+0.5)
		if fs != nominal {
			t.Errorf("font size should stay nominal while only the tracking is dropped, got %v", fs)
		}
		if tr != 0 {
			t.Errorf("tracking should have been dropped, got %v", tr)
		}
	})

	t.Run("shrinks down to the floor and never past it", func(t *testing.T) {
		for _, label := range []string{"CATEGORY", "КАТЕГОРИЯ", "KATEGORIE", "TITULAR"} {
			for _, maxW := range []float64{30, 12, 3} {
				fs, tr := fitTrackedLabel(doc, label, nominal, tracking, maxW)
				if fs < infoLabelMinFS {
					t.Errorf("%s in %vpt: font size %v fell below the floor", label, maxW, fs)
				}
				doc.SetFont(fontFamily, "B", fs)
				w := doc.GetStringWidth(label) + float64(runeLen(label)-1)*tr
				if fs > infoLabelMinFS && w > maxW {
					t.Errorf("%s in %vpt: still %vpt wide at size %v", label, maxW, w, fs)
				}
			}
		}
	})
}

// ── Distributing the white between the details and the codes ────────────

// flowEnd is how far down the page the flowing block reaches: the lowest ink
// that is still above the anchored code block.
func flowEnd(t *testing.T, out []byte) float64 {
	t.Helper()
	end := 0.0
	consider := func(y float64) {
		if y < ticketSpec.bottomTop && y > end {
			end = y
		}
	}
	for _, b := range append(filledRects(t, out), drawnBoxes(t, out)...) {
		consider(b.bottom())
	}
	for _, b := range textBaselines(t, out) {
		consider(b.top)
	}
	return end
}

// TestFlowSlack_SparseTicketLeavesNoDeadBand is the owner's complaint about
// the rendered sample: a ticket with no poster, a one-line title and three
// info values ended its details less than half way down the page and left a
// 60 mm white band before the anchored code block. The anchor cannot move —
// it is what keeps every ticket's codes one page from its neighbours' — so
// the surplus is given back to the gaps the design already has instead.
func TestFlowSlack_SparseTicketLeavesNoDeadBand(t *testing.T) {
	tk := noArt(validTicket(t))
	tk.EventName = "Gala"

	gutter := ticketSpec.bottomTop - flowEnd(t, render(t, tk))
	if gutter > flowGutterTarget+mm(2) {
		t.Errorf("a sparse ticket still leaves a %.1f mm band above the codes; "+
			"the distribution should close it to about %.1f mm",
			gutter/mmToPt, flowGutterTarget/mmToPt)
	}
	if gutter < flowGutterMin {
		t.Errorf("the details were pushed to within %.1f mm of the QR", gutter/mmToPt)
	}
}

// TestFlowSlack_CrowdedTicketNeverRunsIntoTheCodes is the same mechanism in
// the other direction, and it is the one that matters for scanning: the QR is
// an opaque raster, so a detail line that overran the anchor used to be
// CLIPPED by it — a poster plus a five-line title plus a wrapped address did
// exactly that. The gaps give the space back rather than the page losing a
// line.
func TestFlowSlack_CrowdedTicketNeverRunsIntoTheCodes(t *testing.T) {
	tk := validTicket(t) // poster AND logo
	tk.EventName = "An extraordinarily long event name that wraps across several lines " +
		"of the ticket and would push anything that merely flowed after it much " +
		"further down the page"
	tk.VenueAddress = "Avinguda Joan Fuster 12, Poligon Industrial El Raval, apartat de correus 1184"
	tk.HolderName = "Maria Jose Fernandez de la Guardia"

	out := render(t, tk)
	end := flowEnd(t, out)
	if end > ticketSpec.bottomTop-flowGutterMin+0.05 {
		t.Errorf("the details reach %.1f mm, within %.1f mm of the code block at %.1f mm",
			end/mmToPt, (ticketSpec.bottomTop-end)/mmToPt, ticketSpec.bottomTop/mmToPt)
	}
	// And the QR is still exactly where it was, at its nominal size: the
	// flowing block absorbed the pressure, the anchor did not move.
	qr := theQR(t, out)
	if diff := qr.top - ticketSpec.bottomTop; diff > 0.05 || diff < -0.05 {
		t.Errorf("the QR moved to %.2f; the anchor is %.2f", qr.top, ticketSpec.bottomTop)
	}
}

// TestScaleFlowGaps_TouchesOnlyGaps pins what the distribution is allowed to
// change. Scaling a font size, a line height or a rule thickness would make a
// sparse ticket a DIFFERENT design rather than the same one with more air.
func TestScaleFlowGaps_TouchesOnlyGaps(t *testing.T) {
	got := scaleFlowGaps(ticketSpec, 2)
	for name, pair := range map[string][2]float64{
		"titleFS":        {ticketSpec.titleFS, got.titleFS},
		"titleLineH":     {ticketSpec.titleLineH, got.titleLineH},
		"ruleH":          {ticketSpec.ruleH, got.ruleH},
		"accentBarH":     {ticketSpec.accentBarH, got.accentBarH},
		"infoValueFS":    {ticketSpec.infoValueFS, got.infoValueFS},
		"infoValueLineH": {ticketSpec.infoValueLineH, got.infoValueLineH},
		"posterImgH":     {ticketSpec.posterImgH, got.posterImgH},
		"bottomTop":      {ticketSpec.bottomTop, got.bottomTop},
		"qrSize":         {ticketSpec.qrSize, got.qrSize},
		"footFS":         {ticketSpec.footFS, got.footFS},
	} {
		if pair[0] != pair[1] {
			t.Errorf("%s changed from %v to %v — scaling must touch gaps only", name, pair[0], pair[1])
		}
	}
	if got.ruleGapBelow != 2*ticketSpec.ruleGapBelow {
		t.Errorf("ruleGapBelow = %v, want twice %v", got.ruleGapBelow, ticketSpec.ruleGapBelow)
	}
	if got.infoTopGap != 2*ticketSpec.infoTopGap {
		t.Errorf("infoTopGap = %v, want twice %v", got.infoTopGap, ticketSpec.infoTopGap)
	}
}

// ── Graceful degradation ────────────────────────────────────────────────

// TestNoPoster_DetailColumnSpansTheFullWidth is the live-client case: with
// no poster there must be no reserved gutter and no placeholder frame — the
// date/venue column simply starts at the left margin.
func TestNoPoster_DetailColumnSpansTheFullWidth(t *testing.T) {
	withPoster := validTicket(t)
	noPoster := validTicket(t)
	noPoster.PosterImage = nil

	date, _, _ := formatShowTime(withPoster.SessionStart, withPoster.SessionTZ, withPoster.Locale)

	xWith, _ := textOpAt(t, render(t, withPoster), date)
	xWithout, _ := textOpAt(t, render(t, noPoster), date)

	if diff := xWith - (ticketSpec.sideMargin + ticketSpec.posterColW); diff > 0.05 || diff < -0.05 {
		t.Errorf("with a poster the date should start at x=%.2f, got %.2f",
			ticketSpec.sideMargin+ticketSpec.posterColW, xWith)
	}
	if diff := xWithout - ticketSpec.sideMargin; diff > 0.05 || diff < -0.05 {
		t.Errorf("without a poster the date should start at the left margin x=%.2f, got %.2f",
			ticketSpec.sideMargin, xWithout)
	}
}

// TestNoLogo_FallsBackToTheWordmarkThenToNothing walks the three header
// states. Neither of the first live clients has a logo, so the middle one is
// the common case, not the exception.
func TestNoLogo_FallsBackToTheWordmarkThenToNothing(t *testing.T) {
	logo := validTicket(t)
	logo.PosterImage = nil
	withLogo := render(t, logo)
	if got := len(drawnImages(t, withLogo)); got != 2 { // logo plate + QR
		t.Errorf("expected the logo and the QR, got %d images", got)
	}

	wordmark := validTicket(t)
	wordmark.OrgLogo, wordmark.PosterImage = nil, nil
	out := render(t, wordmark)
	if got := len(drawnImages(t, out)); got != 1 { // QR only
		t.Errorf("expected only the QR, got %d images", got)
	}
	if !bytes.Contains(out, pdfText(wordmark.OrgName)) {
		t.Error("with no logo the organizer name should be set as the header wordmark")
	}

	bare := validTicket(t)
	bare.OrgLogo, bare.PosterImage, bare.OrgName = nil, nil, ""
	bareOut := render(t, bare)
	if len(bareOut) == 0 {
		t.Fatal("a ticket with no branding at all must still render")
	}
	// With neither a plate nor a name the band collapses: the accent bar is
	// the very first thing drawn, at the top of the page.
	if !bytes.Contains(bareOut, []byte("re f")) {
		t.Error("expected the accent bar to be drawn as a filled rectangle")
	}
}

func TestBadImageBytesAreSkippedNotFatal(t *testing.T) {
	tk := validTicket(t)
	tk.OrgLogo = []byte("this is not an image at all")
	tk.PosterImage = []byte("nor is this")
	out := render(t, tk)
	if got := len(drawnImages(t, out)); got != 1 {
		t.Errorf("corrupt images should be treated as absent, leaving only the QR; got %d", got)
	}
}

// TestMissingOptionalsDropTheirElement covers the rest of the degradation
// rules in one pass: no address, no category, no price, no order number.
func TestMissingOptionalsDropTheirElement(t *testing.T) {
	tk := Ticket{
		TicketID:     "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		TicketNumber: "7",
		EventName:    "Taller de teatro",
		SessionStart: time.Date(2026, 10, 16, 17, 0, 0, 0, time.UTC),
		SessionTZ:    "Europe/Madrid",
		VenueName:    "Teatro Estudio",
		Locale:       "es",
		EAN13:        "2100000000302",
	}
	out := render(t, tk)
	s := stringsFor("es")

	// No info cells at all: no category, no seat, no price, no holder — so
	// the block draws nothing, not even its hairline rule. Exactly one
	// filled rule remains on the page (the accent bar under the header) plus
	// the accent title rule; the info rule would be a third.
	if got := infoCells(tk, s); len(got) != 0 {
		t.Errorf("expected no info cells, got %+v", got)
	}
	if bytes.Contains(out, pdfText(s.Order+" ")) {
		t.Error("an order line printed for a ticket with no order number")
	}
	if bytes.Contains(out, pdfText(s.Organizer+": ")) {
		t.Error("an organizer line printed for a ticket with no organizer name")
	}
	// The venue prints, its (absent) address does not leave a stray comma.
	if !bytes.Contains(out, pdfText("Teatro Estudio")) {
		t.Error("the venue name is missing")
	}
	for _, banned := range []string{", ", "Teatro Estudio, "} {
		if bytes.Contains(out, pdfText(banned)) {
			t.Errorf("an empty address left the separator %q on the page", banned)
		}
	}
	// The closing note is the one footer line that always prints.
	words := strings.Fields(s.KeepNote)
	if !bytes.Contains(out, pdfTextPrefix(strings.Join(words[:3], " "))) {
		t.Error("the closing footer note is missing")
	}
}

// ── Footer ──────────────────────────────────────────────────────────────

func TestFooter_OrderAndOrganizerLines(t *testing.T) {
	tk := validTicket(t)
	tk.OrgWebsiteURL = "lampyris.cz"
	out := render(t, tk)
	if !bytes.Contains(out, pdfText("Order 9096")) {
		t.Error("footer missing the order line")
	}
	if !bytes.Contains(out, pdfText("Organizer: Lampyris · lampyris.cz")) {
		t.Error("footer missing the organizer line")
	}
}

func TestFooter_CustomFinePrintReplacesTheDefaultNote(t *testing.T) {
	tk := validTicket(t)
	tk.FinePrint = "Bring photo ID."
	out := render(t, tk)
	if !bytes.Contains(out, pdfText("Bring photo ID.")) {
		t.Error("the organizer's own note did not print")
	}
	if bytes.Contains(out, pdfText(stringsFor("en").KeepNote)) {
		t.Error("the default note should have been replaced")
	}
}
