// layout_test.go — the geometric contracts of the ported design: the
// anchored code block, the font-shrink rules, the machine-readable payload,
// and what the page looks like when the optional elements are absent (which
// is how both of the first live clients' tickets actually render).
package pdf

import (
	"bytes"
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/skip2/go-qrcode"
)

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

// TestBottomBlock_IsAnchoredRegardlessOfTitleLength is THE constraint of
// this design: the tear line and the codes under it sit at the same place on
// every page, whatever flows above them. If a long event name could push the
// block down, consecutive tickets' codes would stop being a full page apart
// and an entrance scanner could grab the neighbouring ticket.
func TestBottomBlock_IsAnchoredRegardlessOfTitleLength(t *testing.T) {
	short := validTicket(t)
	short.EventName = "Gala"

	long := validTicket(t)
	long.EventName = "An extraordinarily long event name that wraps across several " +
		"lines of the ticket and would push anything that merely flowed after it " +
		"much further down the page than the designer intended"

	shortOut, longOut := render(t, short), render(t, long)

	// The tear line is the only stroked straight line on the page.
	shortYs, longYs := strokedLineYs(shortOut), strokedLineYs(longOut)
	if len(shortYs) != 1 || len(longYs) != 1 {
		t.Fatalf("expected exactly one stroked line (the tear line), got %d and %d", len(shortYs), len(longYs))
	}
	wantY := mm(297) - mm(140) // PDF user space measures y from the bottom
	for name, got := range map[string]float64{"short title": shortYs[0], "long title": longYs[0]} {
		if diff := got - wantY; diff > 0.05 || diff < -0.05 {
			t.Errorf("%s: tear line at y=%.2f, want %.2f (140 mm from the top)", name, got, wantY)
		}
	}

	// The QR is the first image on a page that has neither logo nor poster;
	// here both are present, so it is the third. Compare the last placement,
	// which is the QR either way.
	shortImgs, longImgs := drawnImages(t, shortOut), drawnImages(t, longOut)
	if len(shortImgs) == 0 || len(longImgs) == 0 {
		t.Fatal("expected drawn images")
	}
	sq, lq := shortImgs[len(shortImgs)-1], longImgs[len(longImgs)-1]
	if sq != lq {
		t.Errorf("the QR moved with the title length: %+v vs %+v", sq, lq)
	}
	if diff := sq.w - mm(32); diff > 0.05 || diff < -0.05 {
		t.Errorf("QR edge = %.2fpt, want %.2fpt (32 mm)", sq.w, mm(32))
	}
	if diff := sq.x - (mm(105)-mm(32))/2; diff > 0.05 || diff < -0.05 {
		t.Errorf("QR is not centred: x=%.2f", sq.x)
	}
}

// TestBottomBlock_CodesStayCloseTogether guards the sizing argument written
// on ticketSpec: the QR and the barcode of ONE ticket may be read
// interchangeably (they carry the same value), but the nearest pair
// belonging to DIFFERENT tickets — one page apart — must stay further apart
// than a fit-to-width phone viewport can show (~245 mm on a 21:9 screen).
func TestBottomBlock_CodesStayCloseTogether(t *testing.T) {
	tk := validTicket(t)
	tk.OrgLogo, tk.PosterImage = nil, nil
	out := render(t, tk)

	imgs := drawnImages(t, out)
	if len(imgs) != 1 {
		t.Fatalf("expected exactly the QR image, got %d", len(imgs))
	}
	// The QR is the page's FIRST code; the barcode's bars start below it.
	qrTop := mm(297) - (imgs[0].y + imgs[0].h)
	barcodeTop := qrTop + ticketSpec.qrSize + ticketSpec.qrGapBelow

	// To scan two DIFFERENT tickets at once, a viewport would have to hold
	// this page's LAST code (the barcode) and the next page's FIRST code
	// (that page's QR) in full. Pages are exactly one page height apart.
	needed := (mm(297) + qrTop + ticketSpec.qrSize) - barcodeTop
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
