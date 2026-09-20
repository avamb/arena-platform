// layout.go — page geometry and drawing for the e-ticket.
//
// Every constant in ticketSpec is a port of the production WordPress design
// (bil24-ticket-mailer/templates/ticket.php + includes/class-btm-renderer.php),
// whose CSS is expressed in millimetres. gofpdf runs this document in POINTS,
// so each value is written as mm(x) to keep it readable against the original
// stylesheet — and so that a future edit can be diffed against it.
//
// The three constraints the original encodes, repeated here because breaking
// any of them is silent and expensive:
//
//   - ONE CODE BLOCK PER PHONE SCREEN. The page is 105x297 mm — half of A4
//     lengthwise, NOT A5 — and the code block is absolutely positioned at
//     bottomTop (140 mm) on every page. ONE PDF CARRIES A WHOLE ORDER, one
//     ticket per page, so the page before and the page after this one are
//     other people's live tickets, not blanks: the page length is the only
//     thing that keeps two consecutive tickets' codes further apart than a
//     fit-to-width viewport can show (a 21:9 phone tops out around 245 mm of
//     a 105 mm-wide page; consecutive codes sit ~290 mm apart). Making the
//     QR the primary, LARGE mark makes this MORE load-bearing, not less: a
//     60 mm QR resolves from further away, further off-axis and at a
//     coarser camera focus than the old 32 mm one did, so the neighbouring
//     ticket's QR drifting into frame is now a realistic mis-scan rather
//     than a theoretical one, and page length is what prevents it. The page
//     height equals A4's, so the sheet also prints at exactly 100% and "2
//     pages per sheet" tiles two tickets onto one A4.
//   - THE FLOWING TOP CAN NEVER PUSH THE BOTTOM. Everything above bottomTop
//     flows; the code block does not. A long title or a long address eats
//     its own slack, never the codes' position — which is precisely what
//     keeps consecutive pages' codes a FIXED distance apart no matter how
//     long the event names, venue addresses and footers of the tickets in
//     one order happen to run.
//   - LONG VALUES SHRINK, THEY DO NOT REFLOW. Title 13.5pt, 11.5pt past 55
//     characters; info values 10.5pt, 9pt past 28. Measured in RUNES, not
//     bytes — see runeLen.
package pdf

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/jung-kurt/gofpdf"
	"github.com/skip2/go-qrcode"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/barcodes/ean13"
)

// mmToPt converts millimetres to PDF points (1 mm = 72/25.4 pt).
const mmToPt = 72.0 / 25.4

// mm converts a millimetre measurement from the ported stylesheet into the
// points this document is laid out in.
func mm(v float64) float64 { return v * mmToPt }

// ── Palette ─────────────────────────────────────────────────────────────
//
// Ported from the original stylesheet. The ACCENT is deliberately not here:
// it lives on the Ticket (see DefaultAccentColor) so it can be set per
// organization later. Everything below is fixed page furniture.
var (
	colorBody     = rgb{r: 34, g: 51, b: 61}    // #22333D — body text, title, info values
	colorMuted    = rgb{r: 70, g: 85, b: 95}    // #46555f — venue, address, ticket number
	colorWeekday  = rgb{r: 122, g: 135, b: 144} // #7a8790 — the weekday under the date
	colorLabel    = rgb{r: 147, g: 160, b: 168} // #93a0a8 — info labels, footer
	colorHairline = rgb{r: 227, g: 231, b: 233} // #e3e7e9 — the rule above the info row
)

// layoutSpec is the page geometry. All lengths are PDF points; every *LineH
// field is a line-height MULTIPLIER of its font size, and every *FS field is
// a font size in points.
type layoutSpec struct {
	pageW, pageH float64
	sideMargin   float64 // the original's .body left/right padding

	// Header band: the organizer's logo plate (or, failing that, a wordmark),
	// then the accent bar. logoBoxH bounds a logo that is not a wide plate —
	// the original hardcodes a 320x100 brand plate at 58 mm wide, which a
	// square logo would turn into a 58 mm-tall block that swallows the page.
	headerPadTop    float64
	headerPadBottom float64
	logoBoxW        float64
	logoBoxH        float64
	wordmarkFS      float64
	accentBarH      float64
	bodyPadTop      float64

	// Event title and the accent rule under it.
	titleFS            float64
	titleFSLong        float64
	titleLongThreshold int
	titleLineH         float64
	titleRuleGap       float64
	ruleH              float64
	ruleGapBelow       float64

	// Poster column. posterColW is the full column INCLUDING the gutter to
	// the text beside it; posterImgW/H is the box the image is fitted into.
	posterColW      float64
	posterImgW      float64
	posterImgH      float64
	posterTopGap    float64
	posterBottomGap float64

	// Date / weekday / venue column, right of the poster (or full width).
	dateFS     float64
	dateLineH  float64
	dowFS      float64
	dowLineH   float64
	whereGap   float64
	whereFS    float64
	whereLineH float64

	// Info row: small uppercase label above a bold value, in equal columns
	// across the full content width.
	infoTopGap             float64
	infoRuleH              float64
	infoCellPadTop         float64
	infoCellPadRight       float64
	infoLabelFS            float64
	infoLabelTracking      float64 // per-glyph letter-spacing (the original's 0.6pt)
	infoLabelGap           float64 // label baseline to the top of the value line
	infoValueFS            float64
	infoValueFSLong        float64
	infoValueLongThreshold int
	infoValueLineH         float64
	infoRowGap             float64

	// The anchored bottom block: QR, barcode, number, footer. There is no
	// tear line — the ported original had a dashed rule here, for a paper
	// ticket that would be torn at the gate; these tickets live on a phone
	// screen and nobody tears them, so the rule was removed rather than
	// kept as decoration.
	bottomTop float64
	// qrSize is the nominal QR edge; qrMinSize is how far fitQRSize may
	// shrink it when an unusually tall footer squeezes the block.
	qrSize         float64
	qrMinSize      float64
	qrGapBelow     float64
	ticketNoFS     float64
	ticketNoLineH  float64
	ticketNoGap    float64
	footPadTop     float64
	footSideMargin float64
	footFS         float64
	footLineH      float64
	bottomSafety   float64 // keep this much clear above the paper edge

	// EAN-13 barcode symbol geometry, consumed by drawEAN13Symbol
	// (ean13_symbol.go). eanBarH/eanGuardExtra are NOMINAL — that function
	// shrinks them (never below eanMinBarHeightPt) if a page ever leaves too
	// little room, and skips the symbol entirely rather than drawing a stub.
	eanModuleW    float64
	eanBarH       float64
	eanGuardExtra float64
	eanDigitFS    float64
	eanLeadGap    float64
}

// ticketSpec is the one page geometry this renderer has.
//
// # Which code is the primary one
//
// The QR is what the buyer holds up to the gate and what entrance control
// (MACS) actually reads, and a QR resolves off a phone screen — backlit,
// smudged, at an angle, at whatever brightness the buyer left the phone on —
// far more reliably than a barcode does. It is therefore drawn FIRST, CENTRED
// and LARGE (qrSize, 60 mm). The barcode is the secondary aid: deliberately
// narrower (~56 mm against the old 96 mm) and shorter, so that at a glance it
// reads as an accessory to the QR rather than as a second, competing mark.
// Its human-readable digits stay large enough to read aloud and type, because
// those digits — not the bars — are the real fallback when a scanner fails.
//
// Both codes carry EXACTLY the same value (the EAN-13 digits), so it never
// matters which one a scanner happens to catch.
//
// 13 numeric characters at the highest error-correction level fit in a
// version-1 symbol (21x21 modules), so at 60 mm a module is ~2.8 mm — an
// order of magnitude above the practical floor, with the error correction
// left at its maximum so a thumb-smudged or partly-glared code still
// resolves. The size is bounded not by legibility but by the block budget:
// the code block must still fit between bottomTop and the footer sitting
// above the bottom safety margin. fitQRSize gives back QR edge before
// anything is allowed to overflow, because a 40 mm QR still scans perfectly
// while a footer printed off the bottom of the page is simply broken.
var ticketSpec = layoutSpec{
	pageW: mm(105), pageH: mm(297),

	sideMargin: mm(8),

	headerPadTop:    mm(5),
	headerPadBottom: mm(4.6),
	logoBoxW:        mm(58),
	logoBoxH:        mm(20),
	wordmarkFS:      12,
	accentBarH:      mm(1.8),
	bodyPadTop:      mm(5),

	titleFS:            13.5,
	titleFSLong:        11.5,
	titleLongThreshold: 55,
	titleLineH:         1.28,
	titleRuleGap:       mm(2.6),
	ruleH:              mm(1.1),
	ruleGapBelow:       mm(3.6),

	posterColW:      mm(50.5),
	posterImgW:      mm(46),
	posterImgH:      mm(46 * 335.0 / 320.0), // the site's 320x335 poster standard
	posterTopGap:    mm(0.5),
	posterBottomGap: mm(2.5),

	dateFS:     11,
	dateLineH:  1.35,
	dowFS:      8.5,
	dowLineH:   1.35,
	whereGap:   mm(3.6),
	whereFS:    9.5,
	whereLineH: 1.45,

	infoTopGap:             mm(3.5),
	infoRuleH:              mm(0.3),
	infoCellPadTop:         mm(2.2),
	infoCellPadRight:       mm(2),
	infoLabelFS:            7.2,
	infoLabelTracking:      0.6,
	infoLabelGap:           2,
	infoValueFS:            10.5,
	infoValueFSLong:        9,
	infoValueLongThreshold: 28,
	infoValueLineH:         1.3,
	infoRowGap:             mm(1),

	bottomTop:      mm(140),
	qrSize:         mm(60),
	qrMinSize:      mm(40),
	qrGapBelow:     mm(7),
	ticketNoFS:     9.5,
	ticketNoLineH:  1.3,
	ticketNoGap:    mm(2.4),
	footPadTop:     mm(3),
	footSideMargin: mm(9),
	footFS:         7,
	footLineH:      1.55,
	bottomSafety:   mm(10),

	// The barcode is the SECONDARY mark, and these numbers are what make it
	// read that way. 0.465 mm modules put the 95-module symbol, its quiet
	// zones and the leading digit inside ~56 mm — a little over half the old
	// 96 mm, and narrower than the 60 mm QR above it, which is the whole
	// point: whatever the buyer's eye lands on first must be the QR.
	//
	// 0.465 mm is ~140% of the GS1 nominal X dimension (0.33 mm), so the
	// symbol is still comfortably above spec on paper at 100%; on a phone,
	// where the page is rendered at roughly two thirds of its printed width,
	// it lands near nominal. That is an acceptable trade now that the
	// barcode is no longer the mark a gate is expected to read — and the
	// human-readable digits below it (eanDigitFS 12pt, well above the 9.5pt
	// ticket-number line) stay the real manual fallback.
	//
	// eanBarH 15 mm truncates the symbol relative to its GS1-proportional
	// height for this width: deliberate, standard practice for a secondary
	// in-line barcode, and still 3 mm of shrink headroom above
	// eanMinBarHeightPt before drawEAN13Symbol would drop the symbol.
	eanModuleW:    mm(0.465),
	eanBarH:       mm(15),
	eanGuardExtra: mm(1.2),
	eanDigitFS:    12,
	eanLeadGap:    3,
}

// specFor maps a Format to its geometry. Both formats resolve to the same
// page — see the Format doc comment — but an unknown value is still an
// error rather than a silent default.
func specFor(f Format) (layoutSpec, error) {
	switch f {
	case FormatMobile, FormatA4Print:
		return ticketSpec, nil
	default:
		return layoutSpec{}, fmt.Errorf("%w: %q", ErrUnknownFormat, f)
	}
}

// contentW is the width between the side margins.
func (s layoutSpec) contentW() float64 { return s.pageW - 2*s.sideMargin }

// qrPixelSize is the raster size of the QR image handed to gofpdf. It has to
// stay ahead of the size the QR is DRAWN at (ticketSpec.qrSize, 60 mm): the
// PNG is always scaled DOWN into the page box, never up, so no resampling can
// soften a module edge and the symbol prints crisp at 300 dpi. go-qrcode
// rounds this up to a whole number of pixels per module, so the source image
// is module-aligned by construction. It was 512 while the QR was 32 mm; a
// 60 mm QR needs more, and the PNG compresses to a couple of kilobytes either
// way because it is two flat colours.
const qrPixelSize = 768

// newDoc builds the empty, page-less document every render (and every
// measuring pass) starts from.
//
// Deterministic: compression off, catalog sort on, timestamps pinned.
func newDoc(t Ticket, spec layoutSpec) *gofpdf.Fpdf {
	doc := gofpdf.NewCustom(&gofpdf.InitType{
		OrientationStr: "P",
		UnitStr:        "pt",
		Size:           gofpdf.SizeType{Wd: spec.pageW, Ht: spec.pageH},
	})
	registerFonts(doc)
	doc.SetMargins(spec.sideMargin, 0, spec.sideMargin)
	// gofpdf pads every cell by 1 mm on each side by default; the ported
	// stylesheet uses cellpadding=0 and positions text by its own margins, so
	// leaving the default in would offset every run by 1 mm from where the
	// geometry says it should be.
	doc.SetCellMargin(0)
	doc.SetAutoPageBreak(false, 0)
	doc.SetCompression(false) // deterministic byte output for tests
	// gofpdf emits resource dictionaries (fonts, images) by iterating Go
	// maps, whose order is randomized per process; sorting them keeps two
	// renders of the same Ticket byte-identical.
	doc.SetCatalogSort(true)
	// gofpdf treats a zero time as "use time.Now() at render time"; pin both
	// timestamps so the same Ticket always renders to the same bytes.
	pinned := pinnedTimestamp()
	doc.SetCreationDate(pinned)
	doc.SetModificationDate(pinned)
	doc.SetProducer("arena-ticketing", true)
	doc.SetCreator("arena-ticketing", true)
	// The document title carries the human-facing number, never the internal
	// UUID — a PDF title shows in the reader's window chrome and survives
	// into anything the buyer forwards.
	doc.SetTitle(fmt.Sprintf("Ticket %s", displayTicketNumber(t)), true)
	return doc
}

// drawFlow draws everything ABOVE the anchored code block — the header band,
// the title and its accent rule, the poster / date / venue column and the
// info rows — and returns the y cursor below the last of them.
//
// It is one function because the layout has to be able to MEASURE the
// flowing block (see distributeFlowSlack) with exactly the drawing code that
// will later produce it, into a throwaway document.
func drawFlow(doc *gofpdf.Fpdf, t Ticket, spec layoutSpec, s ticketStrings, accent rgb) float64 {
	y := drawHeader(doc, t, spec, accent)
	y = drawTitle(doc, t, spec, accent, y+spec.bodyPadTop)
	y = drawWhenWhere(doc, t, spec, y)
	return drawInfo(doc, t, spec, s, y)
}

// renderWithSpec draws the page and returns the finished PDF bytes.
func renderWithSpec(t Ticket, spec layoutSpec) ([]byte, error) {
	spec = distributeFlowSlack(t, spec)

	doc := newDoc(t, spec)
	doc.AddPage()

	str := stringsFor(t.Locale)
	drawFlow(doc, t, spec, str, accentColor(t))
	if err := drawBottom(doc, t, spec, str); err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	if err := doc.Output(&buf); err != nil {
		return nil, fmt.Errorf("pdf: output: %w", err)
	}
	return buf.Bytes(), nil
}

// flowGapMaxScale caps how far distributeFlowSlack may OPEN the flowing
// block's gaps. Three times the ported rhythm is already a very airy page;
// past that the details stop reading as one block and start reading as
// unrelated fragments, which is worse than the white it would have absorbed.
//
// flowGapMinScale is the matching floor for CLOSING them. A third of the
// nominal rhythm is tight but still legibly separated, and it is only ever
// reached by a ticket that would otherwise print its holder's name on top of
// the QR.
const (
	flowGapMaxScale = 3.0
	flowGapMinScale = 0.35
)

// flowGutterTarget is the white distributeFlowSlack AIMS to leave between the
// last line of the details and the top of the code block when the ticket has
// room to spare. flowGutterMin is the hard floor it will compress the details
// to reach when they do not: a 60 mm QR is read by pointing a camera at it,
// and text crowding — let alone overprinting — its top edge is a scan
// failure, not a cosmetic blemish.
var (
	flowGutterTarget = mm(14)
	flowGutterMin    = mm(4)
)

// distributeFlowSlack returns a copy of spec whose flowing-block gaps have
// been scaled so the details end a comfortable distance above the anchored
// code block — opened up when the ticket has little to say, closed down when
// it has too much.
//
// The anchor is NOT negotiable (see the package comment: it is what keeps
// every ticket's codes one full page from its neighbours' in an order-wide
// PDF). So a ticket with little above it — no poster, a one-line title, no
// seat — used to leave a 60 mm dead band in the middle of the page, and both
// of the first live clients' tickets look exactly like that, making it the
// common case rather than the exception. At the other end, a ticket with a
// poster AND a five-line title AND a wrapped address used to run straight
// into the code block and have its last line clipped by the QR's opaque
// raster. Rather than move the anchor or invent filler, the surplus (or the
// deficit) is taken from the gaps the design already has, in proportion to
// their nominal sizes, so the page keeps its rhythm and only its breathing
// changes.
//
// A ticket that already lands between the two gutters gets no adjustment at
// all, after a single measuring pass.
//
// The measurement is EXACT rather than iterative. Nothing in the flowing
// block wraps on y — auto page break is off and every MultiCell width is
// independent of the cursor — so the block's end is an AFFINE function of the
// gap scale. One measuring pass at the nominal scale plus one at the relevant
// limit determine it, and the answer is then arithmetic.
func distributeFlowSlack(t Ticket, spec layoutSpec) layoutSpec {
	s := stringsFor(t.Locale)
	accent := accentColor(t)
	measure := func(sp layoutSpec) float64 {
		scratch := newDoc(t, sp)
		scratch.AddPage()
		return drawFlow(scratch, t, sp, s, accent)
	}

	atNominal := measure(spec)
	switch {
	case atNominal < spec.bottomTop-flowGutterTarget:
		return fitFlowGaps(spec, measure, atNominal, spec.bottomTop-flowGutterTarget, flowGapMaxScale)
	case atNominal > spec.bottomTop-flowGutterMin:
		return fitFlowGaps(spec, measure, atNominal, spec.bottomTop-flowGutterMin, flowGapMinScale)
	default:
		return spec
	}
}

// fitFlowGaps solves for the gap scale that lands the flowing block's end on
// target, given its measured end at the nominal scale and one more measurement
// at limitScale (above 1 to expand, below 1 to contract). The result is
// clamped to the [1, limitScale] interval, so the returned spec is never
// scaled further than the caller allowed and never scaled the wrong way.
func fitFlowGaps(spec layoutSpec, measure func(layoutSpec) float64, atNominal, target, limitScale float64) layoutSpec {
	atLimit := measure(scaleFlowGaps(spec, limitScale))
	// end(k) = atNominal + (k-1)*slope, and slope > 0 whenever the block has
	// any gaps at all to give or take.
	slope := (atLimit - atNominal) / (limitScale - 1)
	if slope <= 0 {
		return spec
	}
	k := 1 + (target-atNominal)/slope
	lo, hi := limitScale, 1.0
	if limitScale > 1 {
		lo, hi = 1.0, limitScale
	}
	if k < lo {
		k = lo
	}
	if k > hi {
		k = hi
	}
	return scaleFlowGaps(spec, k)
}

// scaleFlowGaps multiplies every vertical gap in the flowing block by k. It
// deliberately touches only the GAPS — never a font size, a line height or a
// rule thickness — so a distributed page is the ported design with more air,
// not a different design.
func scaleFlowGaps(spec layoutSpec, k float64) layoutSpec {
	out := spec
	for _, g := range []*float64{
		&out.headerPadBottom,
		&out.bodyPadTop,
		&out.titleRuleGap,
		&out.ruleGapBelow,
		&out.posterBottomGap,
		&out.whereGap,
		&out.infoTopGap,
		&out.infoCellPadTop,
		&out.infoRowGap,
	} {
		*g *= k
	}
	return out
}

// setText / setFill are the two colour setters, kept as helpers so no call
// site has to spell out a literal triple.
func setText(doc *gofpdf.Fpdf, c rgb) { doc.SetTextColor(c.r, c.g, c.b) }
func setFill(doc *gofpdf.Fpdf, c rgb) { doc.SetFillColor(c.r, c.g, c.b) }

// drawHeader draws the organizer's logo plate (centred, as the original does
// — the plate carries the organizer's own colours and renders as-is on
// white), then the full-bleed accent bar under it. It returns the y cursor
// below the bar.
//
// Degradation, in order: a decodable logo wins; failing that the organizer's
// name is set as a centred wordmark, so the band is never an empty box; and
// with neither the band collapses to nothing and the accent bar becomes the
// very first thing on the page. Both of the first live clients land in the
// middle case.
func drawHeader(doc *gofpdf.Fpdf, t Ticket, spec layoutSpec, accent rgb) float64 {
	bandH := 0.0
	switch info, ok := decodeImageInfo(t.OrgLogo); {
	case ok:
		w, h := fitBox(info.w, info.h, spec.logoBoxW, spec.logoBoxH)
		name := "logo-" + t.TicketID
		doc.RegisterImageOptionsReader(
			name,
			gofpdf.ImageOptions{ImageType: info.typ, ReadDpi: false},
			bytes.NewReader(t.OrgLogo),
		)
		doc.ImageOptions(
			name, (spec.pageW-w)/2, spec.headerPadTop, w, h,
			false, gofpdf.ImageOptions{ImageType: info.typ}, 0, "",
		)
		bandH = spec.headerPadTop + h + spec.headerPadBottom
	default:
		if wordmark := strings.TrimSpace(t.OrgName); wordmark != "" {
			doc.SetFont(fontFamily, "B", spec.wordmarkFS)
			setText(doc, colorBody)
			doc.SetXY(spec.sideMargin, spec.headerPadTop)
			doc.MultiCell(spec.contentW(), spec.wordmarkFS*1.3, wordmark, "", "C", false)
			bandH = doc.GetY() + spec.headerPadBottom
		}
	}
	setFill(doc, accent)
	doc.Rect(0, bandH, spec.pageW, spec.accentBarH, "F")
	return bandH + spec.accentBarH
}

// drawTitle prints the event name and the accent rule under it, returning
// the y cursor below the rule. The name wraps; past titleLongThreshold runes
// it wraps at the smaller size instead, so a long name costs one extra line
// rather than pushing the whole page down.
func drawTitle(doc *gofpdf.Fpdf, t Ticket, spec layoutSpec, accent rgb, y float64) float64 {
	fs := spec.titleFS
	if runeLen(t.EventName) > spec.titleLongThreshold {
		fs = spec.titleFSLong
	}
	doc.SetFont(fontFamily, "B", fs)
	setText(doc, colorBody)
	doc.SetXY(spec.sideMargin, y)
	doc.MultiCell(spec.contentW(), fs*spec.titleLineH, t.EventName, "", "L", false)

	ruleY := doc.GetY() + spec.titleRuleGap
	setFill(doc, accent)
	doc.Rect(spec.sideMargin, ruleY, spec.contentW(), spec.ruleH, "F")
	return ruleY + spec.ruleH + spec.ruleGapBelow
}

// drawWhenWhere draws the poster (when there is one) and, beside it, the
// date / time / weekday / venue / address column. It returns the y cursor
// below whichever of the two is taller.
//
// With no poster the text column starts at the left margin and spans the
// full content width — no placeholder frame, no reserved gutter. Both of the
// first live clients have no poster, so this is the common case, not the
// exception.
func drawWhenWhere(doc *gofpdf.Fpdf, t Ticket, spec layoutSpec, y float64) float64 {
	textX, textW := spec.sideMargin, spec.contentW()
	bottom := y

	if info, ok := decodeImageInfo(t.PosterImage); ok {
		w, h := fitBox(info.w, info.h, spec.posterImgW, spec.posterImgH)
		name := "poster-" + t.TicketID
		doc.RegisterImageOptionsReader(
			name,
			gofpdf.ImageOptions{ImageType: info.typ, ReadDpi: false},
			bytes.NewReader(t.PosterImage),
		)
		doc.ImageOptions(
			name, spec.sideMargin, y+spec.posterTopGap, w, h,
			false, gofpdf.ImageOptions{ImageType: info.typ}, 0, "",
		)
		bottom = y + spec.posterTopGap + h + spec.posterBottomGap
		textX = spec.sideMargin + spec.posterColW
		textW = spec.pageW - spec.sideMargin - textX
	}

	date, clock, weekday := formatShowTime(t.SessionStart, t.SessionTZ, t.Locale)

	doc.SetFont(fontFamily, "B", spec.dateFS)
	setText(doc, colorBody)
	doc.SetXY(textX, y)
	doc.MultiCell(textW, spec.dateFS*spec.dateLineH, date+"\n"+clock, "", "L", false)

	doc.SetFont(fontFamily, "", spec.dowFS)
	setText(doc, colorWeekday)
	doc.SetXY(textX, doc.GetY())
	doc.MultiCell(textW, spec.dowFS*spec.dowLineH, weekday, "", "L", false)

	venue := strings.TrimSpace(t.VenueName)
	address := joinNonEmpty(", ", t.VenueAddress, t.VenueCity)
	if venue != "" || address != "" {
		wy := doc.GetY() + spec.whereGap
		setText(doc, colorMuted)
		if venue != "" {
			doc.SetFont(fontFamily, "B", spec.whereFS)
			doc.SetXY(textX, wy)
			doc.MultiCell(textW, spec.whereFS*spec.whereLineH, venue, "", "L", false)
			wy = doc.GetY()
		}
		if address != "" {
			doc.SetFont(fontFamily, "", spec.whereFS)
			doc.SetXY(textX, wy)
			doc.MultiCell(textW, spec.whereFS*spec.whereLineH, address, "", "L", false)
		}
	}

	if ty := doc.GetY(); ty > bottom {
		bottom = ty
	}
	return bottom
}

// infoCell is one label/value pair in the info row.
type infoCell struct{ label, value string }

// infoRows assembles the info block row by row, skipping every cell whose
// value is empty: a general-admission ticket has no Seat cell, a free or
// unpriced ticket no Price cell, an unnamed holder no Holder cell. With no
// cells at all the block draws nothing — not even its hairline rule.
//
// Category / Seat / Price share ONE row of equal columns, exactly as the
// ported design lays them out. The holder gets a row of its own rather than
// a fourth column: an 89 mm content width split four ways leaves 22 mm per
// cell, and a personal name wraps to three lines in that — the arena
// addition must not degrade the row the original already tuned.
func infoRows(t Ticket, s ticketStrings) [][]infoCell {
	top := make([]infoCell, 0, 3)
	for _, c := range []infoCell{
		{label: s.Category, value: strings.TrimSpace(t.TierName)},
		{label: s.Seat, value: seatValue(t, s)},
		{label: s.Price, value: priceValue(t)},
	} {
		if c.value != "" {
			top = append(top, c)
		}
	}
	rows := make([][]infoCell, 0, 2)
	if len(top) > 0 {
		rows = append(rows, top)
	}
	if holder := strings.TrimSpace(t.HolderName); holder != "" {
		rows = append(rows, []infoCell{{label: s.Holder, value: holder}})
	}
	return rows
}

// infoCells flattens infoRows — "everything the info block will print".
func infoCells(t Ticket, s ticketStrings) []infoCell {
	out := []infoCell{}
	for _, row := range infoRows(t, s) {
		out = append(out, row...)
	}
	return out
}

// drawInfo draws the hairline rule and the info cells under the date/venue
// block, and returns the y cursor below them.
func drawInfo(doc *gofpdf.Fpdf, t Ticket, spec layoutSpec, s ticketStrings, y float64) float64 {
	rows := infoRows(t, s)
	if len(rows) == 0 {
		return y
	}
	y += spec.infoTopGap
	setFill(doc, colorHairline)
	doc.Rect(spec.sideMargin, y, spec.contentW(), spec.infoRuleH, "F")
	y += spec.infoRuleH

	for i, row := range rows {
		if i > 0 {
			y += spec.infoRowGap
		}
		y = drawInfoRow(doc, row, spec, y)
	}
	return y
}

// drawInfoRow draws one row of equal-width cells — small uppercase label
// above a bold value — and returns the y cursor below the tallest of them.
func drawInfoRow(doc *gofpdf.Fpdf, row []infoCell, spec layoutSpec, y float64) float64 {
	colW := spec.contentW() / float64(len(row))
	cellW := colW - spec.infoCellPadRight
	top := y + spec.infoCellPadTop
	bottom := top

	for i, c := range row {
		x := spec.sideMargin + float64(i)*colW

		label := strings.ToUpper(c.label)
		fs, tracking := fitTrackedLabel(doc, label, spec.infoLabelFS, spec.infoLabelTracking, cellW)
		setText(doc, colorLabel)
		// gofpdf's own baseline rule for a cell of height h is
		// top + 0.5*h + 0.3*fontSize; Text() takes the baseline directly, so
		// the label's is placed explicitly to sit on the same optical line.
		drawTracked(doc, label, x, top+fs, fs, tracking)

		valueFS := spec.infoValueFS
		if runeLen(c.value) > spec.infoValueLongThreshold {
			valueFS = spec.infoValueFSLong
		}
		doc.SetFont(fontFamily, "B", valueFS)
		setText(doc, colorBody)
		doc.SetXY(x, top+fs+spec.infoLabelGap)
		doc.MultiCell(cellW, valueFS*spec.infoValueLineH, c.value, "", "L", false)
		if b := doc.GetY(); b > bottom {
			bottom = b
		}
	}
	return bottom
}

// infoLabelMinFS is the floor fitTrackedLabel will shrink a label to.
const infoLabelMinFS = 5.5

// fitTrackedLabel picks the font size and per-glyph tracking that keep label
// inside maxW: the nominal pair first, then the same size with the tracking
// dropped, then progressively smaller sizes down to infoLabelMinFS.
//
// This is the surviving lesson of the label-overlap defect the pre-port
// layout had: a locale whose word happens to be longer than English's ("Místo
// konání" for Venue) must never overprint its neighbour. The stacked cell
// this design uses makes an overlap far less likely than the old side-by-side
// label column did — the label has the whole cell width to itself — but a
// four-cell row on a 105 mm page is narrow enough that it is still worth
// measuring rather than assuming.
//
// Leaves the document's font state set to its last measurement; every caller
// re-sets the font before drawing.
func fitTrackedLabel(doc *gofpdf.Fpdf, label string, fs, tracking, maxW float64) (float64, float64) {
	n := float64(runeLen(label))
	width := func(size, tr float64) float64 {
		doc.SetFont(fontFamily, "B", size)
		w := doc.GetStringWidth(label)
		if n > 1 {
			w += (n - 1) * tr
		}
		return w
	}
	if width(fs, tracking) <= maxW {
		return fs, tracking
	}
	if width(fs, 0) <= maxW {
		return fs, 0
	}
	for size := fs - 0.4; size >= infoLabelMinFS; size -= 0.4 {
		if width(size, 0) <= maxW {
			return size, 0
		}
	}
	return infoLabelMinFS, 0
}

// drawTracked prints s with per-glyph letter-spacing at the given baseline.
// gofpdf exposes no character-spacing operator, so the spacing is drawn
// glyph by glyph — which means the text lands in the content stream as one
// show operator per glyph rather than a single run (tests that search the
// raw bytes have to look for the glyphs, not the word).
func drawTracked(doc *gofpdf.Fpdf, s string, x, baseline, fs, tracking float64) {
	doc.SetFont(fontFamily, "B", fs)
	for _, r := range s {
		g := string(r)
		doc.Text(x, baseline, g)
		x += doc.GetStringWidth(g) + tracking
	}
}

// eanTextBlockH is the vertical room drawEAN13Symbol needs below the bars
// for the human-readable digits (gap + cap height + descender buffer).
func eanTextBlockH(spec layoutSpec) float64 {
	return eanTextGapPt + spec.eanDigitFS + eanTextDescentPt
}

// fitQRSize returns the QR edge to draw given `available` pt of room between
// the top of the code block and the highest y the codes may reach.
//
// The QR is the primary mark, so it is drawn at its nominal size whenever the
// page can hold the nominal barcode under it, and shrinks — never below
// spec.qrMinSize — only when an unusually tall footer (a full legal
// identification block plus a wrapped organizer note) would otherwise push
// the block off the bottom of the page. Below qrMinSize it stops yielding and
// drawEAN13Symbol's own shrink-then-skip takes over: a 40 mm QR that scans is
// worth more than a barcode nobody will point a camera at.
func fitQRSize(spec layoutSpec, available float64) float64 {
	below := spec.qrGapBelow + spec.eanBarH + spec.eanGuardExtra + eanTextBlockH(spec)
	size := available - below
	if size > spec.qrSize {
		size = spec.qrSize
	}
	if size < spec.qrMinSize {
		size = spec.qrMinSize
	}
	return size
}

// drawBottom draws the anchored block: the QR, the barcode symbol with its
// human-readable digits, the ticket number, and the footer.
//
// The block's top is spec.bottomTop on every page, independent of everything
// above it — that is the whole point of the design, and with one PDF now
// carrying a whole order it is what keeps every ticket's codes exactly one
// page apart from its neighbours'.
//
// Order of marks, largest first: the QR is centred at the very top of the
// block because it is the thing the buyer holds up and the thing entrance
// control reads. The barcode follows, narrower and shorter, as a secondary
// aid — and its human-readable digits beneath it are the manual-entry
// fallback staff type when a scanner fails, which is why they are set well
// above the ticket-number line's size.
//
// There is no tear line: the ported original's dashed rule belonged to a
// paper ticket somebody would tear at the gate, and these are read off a
// phone.
func drawBottom(doc *gofpdf.Fpdf, t Ticket, spec layoutSpec, s ticketStrings) error {
	y := spec.bottomTop

	footLines := footerLines(doc, t, spec, s)
	// Everything below the barcode has a fixed height, so the ceiling both
	// codes share is known before either is drawn. The QR sizes itself
	// against it (fitQRSize) and drawEAN13Symbol shrinks or skips itself
	// against it, rather than either overprinting the footer.
	footerH := spec.footPadTop + float64(len(footLines))*spec.footFS*spec.footLineH
	ceiling := spec.pageH - spec.bottomSafety - footerH -
		spec.ticketNoGap - spec.ticketNoFS*spec.ticketNoLineH

	// The QR and the barcode carry EXACTLY the same value — the EAN-13
	// digits — so a scanner that reads either one resolves the same ticket.
	// An absent or checksum-invalid number draws neither: a ticket with
	// nothing a gate can resolve is better off saying so than carrying a
	// mark that looks scannable and is not.
	if code := strings.TrimSpace(t.EAN13); ean13.Valid(code) {
		qrPNG, err := qrcode.Encode(code, qrcode.High, qrPixelSize)
		if err != nil {
			return fmt.Errorf("pdf: encode qr: %w", err)
		}
		qrSize := fitQRSize(spec, ceiling-y)
		name := "qr-" + t.TicketID
		doc.RegisterImageOptionsReader(
			name,
			gofpdf.ImageOptions{ImageType: "PNG", ReadDpi: false},
			bytes.NewReader(qrPNG),
		)
		doc.ImageOptions(
			name, (spec.pageW-qrSize)/2, y, qrSize, qrSize,
			false, gofpdf.ImageOptions{ImageType: "PNG"}, 0, "",
		)
		y += qrSize + spec.qrGapBelow

		setText(doc, colorBody)
		y = drawEAN13Symbol(doc, code, spec, y, ceiling)
	}

	y += spec.ticketNoGap
	doc.SetFont(fontFamily, "", spec.ticketNoFS)
	setText(doc, colorMuted)
	doc.SetXY(spec.sideMargin, y)
	doc.CellFormat(spec.contentW(), spec.ticketNoFS*spec.ticketNoLineH,
		s.TicketNo+" "+displayTicketNumber(t), "", 0, "C", false, 0, "")
	y += spec.ticketNoFS*spec.ticketNoLineH + spec.footPadTop

	doc.SetFont(fontFamily, "", spec.footFS)
	setText(doc, colorLabel)
	for _, line := range footLines {
		doc.SetXY(spec.footSideMargin, y)
		doc.CellFormat(spec.pageW-2*spec.footSideMargin, spec.footFS*spec.footLineH,
			line, "", 0, "C", false, 0, "")
		y += spec.footFS * spec.footLineH
	}
	setText(doc, colorBody)
	return nil
}

// footerLines assembles the centred footer, already wrapped to the footer's
// own width so drawBottom knows its exact height before it places the
// barcode above it:
//
//	Order <number>
//	Organizer: <name> · <website>
//	<legal identification block>
//	<closing note>
//
// Every line but the last is optional. The closing note is the only one that
// wraps, so it is split here with the footer font already selected.
//
// The legal block is the EU "commercial communications" minimum
// identification — the WordPress original has no equivalent, but the
// pre-port arena layout did and dropping a compliance element while porting
// a visual design would be the wrong kind of faithful.
func footerLines(doc *gofpdf.Fpdf, t Ticket, spec layoutSpec, s ticketStrings) []string {
	out := []string{}
	if order := strings.TrimSpace(t.OrderNumber); order != "" {
		out = append(out, s.Order+" "+order)
	}
	if org := strings.TrimSpace(t.OrgName); org != "" {
		out = append(out, s.Organizer+": "+joinNonEmpty(" · ", org, t.OrgWebsiteURL))
	}
	out = append(out, buildLegalLines(t, s.Contact)...)

	note := strings.TrimSpace(t.FinePrint)
	if note == "" {
		note = defaultNoteFor(t.Locale)
	}
	doc.SetFont(fontFamily, "", spec.footFS)
	for _, line := range doc.SplitLines([]byte(note), spec.pageW-2*spec.footSideMargin) {
		out = append(out, string(line))
	}
	return out
}
