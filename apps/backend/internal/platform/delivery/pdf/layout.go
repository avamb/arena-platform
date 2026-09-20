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
//     bottomTop (140 mm) on every page. Two consecutive tickets' codes stay a
//     full page apart, further than a fit-to-width viewport can show even on
//     a 21:9 phone, so an entrance scanner cannot grab the neighbouring
//     ticket. The page height equals A4's, so the sheet also prints at exactly
//     100% and "2 pages per sheet" tiles two tickets onto one A4.
//   - THE FLOWING TOP CAN NEVER PUSH THE BOTTOM. Everything above the tear
//     line flows; the bottom block does not. A long title or a long address
//     eats its own slack, never the barcode's position.
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
	colorTear     = rgb{r: 185, g: 194, b: 200} // #b9c2c8 — the dashed tear line
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

	// The anchored bottom block: tear line, QR, barcode, number, footer.
	bottomTop      float64
	tearLineW      float64
	tearDashOn     float64
	tearDashOff    float64
	tearGapBelow   float64
	qrSize         float64
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
// The QR sizing (qrSize) deserves its own note. 13 numeric characters at the
// highest error-correction level fit in a version-1 symbol — 21x21 modules —
// so at 32 mm each module is ~1.5 mm, several times the practical floor for a
// phone screen or a printed sheet, with the error correction left at its
// maximum so a thumb-smudged or partly-glared code still resolves. 32 mm is
// also small enough to keep the QR and the barcode within ~60 mm of each
// other: the two codes of ONE ticket may be read interchangeably (they carry
// the same value), but the nearest pair belonging to DIFFERENT tickets stays
// ~295 mm apart, still well beyond what a fit-to-width phone viewport shows.
// Growing the QR much past this would start eating that margin.
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
	tearLineW:      mm(0.4),
	tearDashOn:     2,
	tearDashOff:    2,
	tearGapBelow:   mm(3.5),
	qrSize:         mm(32),
	qrGapBelow:     mm(4),
	ticketNoFS:     9.5,
	ticketNoLineH:  1.3,
	ticketNoGap:    mm(2.4),
	footPadTop:     mm(3),
	footSideMargin: mm(9),
	footFS:         7,
	footLineH:      1.55,
	bottomSafety:   mm(10),

	// 0.78 mm modules put the whole symbol plus its quiet zones and the
	// leading digit inside ~96 mm, i.e. roughly 4.5 mm clear of each paper
	// edge — wide bars scan off a phone screen far better than nominal ones,
	// which is the whole reason the original stretches the barcode to the
	// content width too.
	eanModuleW:    mm(0.78),
	eanBarH:       mm(18),
	eanGuardExtra: mm(1.5),
	eanDigitFS:    20,
	eanLeadGap:    4,
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

// qrPixelSize is the raster size of the QR image handed to gofpdf. It is
// deliberately far larger than the 32 mm it is drawn at: the PNG is scaled
// DOWN into the page box, so every module lands on a whole number of source
// pixels and no resampling can soften a module edge.
const qrPixelSize = 512

// renderWithSpec draws the page and returns the finished PDF bytes.
// Deterministic: compression off, catalog sort on, timestamps pinned.
func renderWithSpec(t Ticket, spec layoutSpec) ([]byte, error) {
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
	doc.AddPage()

	str := stringsFor(t.Locale)
	accent := accentColor(t)

	y := drawHeader(doc, t, spec, accent)
	y = drawTitle(doc, t, spec, accent, y+spec.bodyPadTop)
	y = drawWhenWhere(doc, t, spec, y)
	drawInfo(doc, t, spec, str, y)
	if err := drawBottom(doc, t, spec, str); err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	if err := doc.Output(&buf); err != nil {
		return nil, fmt.Errorf("pdf: output: %w", err)
	}
	return buf.Bytes(), nil
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

// drawBottom draws the anchored block: tear line, QR, barcode symbol with
// its human-readable digits, the ticket number, and the footer.
//
// The block's top is spec.bottomTop on every page, independent of everything
// above it — that is the whole point of the design. The QR sits directly
// under the tear line because it is what entrance control actually scans;
// the barcode follows immediately, with its digits beneath, because those
// digits are the manual-entry fallback when a scanner fails.
func drawBottom(doc *gofpdf.Fpdf, t Ticket, spec layoutSpec, s ticketStrings) error {
	y := spec.bottomTop

	doc.SetDrawColor(colorTear.r, colorTear.g, colorTear.b)
	doc.SetLineWidth(spec.tearLineW)
	doc.SetDashPattern([]float64{spec.tearDashOn, spec.tearDashOff}, 0)
	doc.Line(spec.sideMargin, y, spec.pageW-spec.sideMargin, y)
	doc.SetDashPattern(nil, 0)
	y += spec.tearGapBelow

	footLines := footerLines(doc, t, spec, s)
	// Everything below the barcode has a fixed height, so the symbol's
	// ceiling is known before it is drawn. drawEAN13Symbol shrinks or skips
	// itself against this rather than overprinting the footer.
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
		name := "qr-" + t.TicketID
		doc.RegisterImageOptionsReader(
			name,
			gofpdf.ImageOptions{ImageType: "PNG", ReadDpi: false},
			bytes.NewReader(qrPNG),
		)
		doc.ImageOptions(
			name, (spec.pageW-spec.qrSize)/2, y, spec.qrSize, spec.qrSize,
			false, gofpdf.ImageOptions{ImageType: "PNG"}, 0, "",
		)
		y += spec.qrSize + spec.qrGapBelow

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
