// layout.go — per-format page geometry and drawing for the SEAT-C4
// two-layout renderer (FormatMobile / FormatA4Print).
//
// Both layouts draw the same content blocks in the same order — org
// branding header, event headline, labelled detail rows (with the seat
// rows most prominent), hero QR code, human code, ticket id, legal
// footer, fine print — differing only in the layoutSpec geometry. The
// content itself always comes from the single Ticket projection struct,
// so the layouts cannot diverge in what they print.
package pdf

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/jung-kurt/gofpdf"
	"github.com/skip2/go-qrcode"
)

// layoutSpec is the per-format page geometry. All values are PDF points.
type layoutSpec struct {
	pageW, pageH float64
	margin       float64

	headerH    float64 // header band height (logo + wordmark)
	headerFS   float64 // wordmark font size
	siteFS     float64 // website line font size
	logoW      float64 // logo slot width (height auto from aspect)
	headlineFS float64 // event-name headline font size

	detailFS   float64 // regular detail row font size
	seatFS     float64 // seat rows font size (most prominent row)
	detailRowH float64 // regular detail row height
	seatRowH   float64 // seat row height
	labelW     float64 // label column BASELINE width — a floor, not a fixed value; computeLabelWidth grows it to fit the active locale's widest label, never below this

	qrSize float64 // QR square edge
	codeFS float64 // human-code font size (letter-spaced mono)
	idFS   float64 // ticket-id line font size

	// EAN-13 barcode symbol geometry (ean13_symbol.go). eanBarH/eanGuardExtra
	// are NOMINAL — drawEAN13Symbol shrinks them (never below
	// eanMinBarHeightPt) when a page's other content leaves too little
	// vertical room before the footer.
	eanModuleW    float64 // module width, pt (GS1 allows ~0.30-0.40mm at 100%)
	eanBarH       float64 // nominal bar height, pt (GS1 floor ≈ 20mm)
	eanGuardExtra float64 // nominal extra height of the 3 guard-bar groups, pt (~1.5mm)
	eanDigitFS    float64 // font size for the human-readable digits beneath the symbol
	eanLeadGap    float64 // gap between the leading digit and the left quiet zone, pt

	legalFS     float64 // legal footer font size
	legalLineH  float64 // legal footer line height
	finePrintFS float64 // fine print font size
	finePrintH  float64 // vertical space reserved for the fine print
	blockGap    float64 // vertical gap between major blocks
}

// mobileSpec is the default phone-aspect layout: 396×702 pt ≈ 9:16.
// qrSize/pageW = 232/396 ≈ 0.586, satisfying the ≥55%-of-page-width
// acceptance bound for the hero QR.
// Row heights, blockGap, finePrintH and legalLineH below were tightened
// (from an earlier, more generous draft) to free enough vertical room for
// the EAN-13 barcode symbol to render at its full nominal size even in the
// worst realistic combination this layout has to survive: a wrapped
// 2-line headline + all three seat rows + a full 6-line legal address
// block (exercised by the Czech sample in render_i18n_test.go / the
// mandatory visual-QA sample set) — before this pass, that exact
// combination left only ~33pt above the footer, below
// eanMinBarHeightPt+textBlockH, so the symbol silently skipped itself on
// every seated Czech/Russian ticket with a full legal footer. None of
// these numbers are asserted on by any test; they are free to keep tuning.
var mobileSpec = layoutSpec{
	pageW: 396, pageH: 702,
	margin:     24,
	headerH:    54,
	headerFS:   14,
	siteFS:     8,
	logoW:      80,
	headlineFS: 18,
	detailFS:   10,
	seatFS:     14,
	detailRowH: 12,
	seatRowH:   16,
	labelW:     58,
	qrSize:     232,
	codeFS:     16,
	idFS:       7,

	eanModuleW:    0.33 * eanMMToPt, // ≈0.935pt (0.33mm)
	eanBarH:       20 * eanMMToPt,   // ≈56.69pt (GS1 20mm floor)
	eanGuardExtra: 1.5 * eanMMToPt,  // ≈4.25pt
	eanDigitFS:    7,
	eanLeadGap:    3,

	legalFS:     6,
	legalLineH:  7,
	finePrintFS: 6,
	finePrintH:  26,
	blockGap:    8,
}

// a4Spec is the printable A4 portrait variant (595.28×841.89 pt) with
// generous home-printer margins. qrSize = 198.43 pt ≈ 70 mm.
//
// Row heights, blockGap, finePrintH and legalLineH were tightened the same
// way as mobileSpec (see its comment) — A4's larger absolute page size does
// NOT mean proportionally more slack: every block below scales up with it
// (bigger fonts, bigger row heights, bigger QR), so the same worst-case
// combination (wrapped headline + seat rows + a full legal block) left the
// EAN-13 symbol only ~23pt of room here too before this pass.
var a4Spec = layoutSpec{
	pageW: 595.28, pageH: 841.89,
	margin:     56.7, // 2 cm
	headerH:    80,
	headerFS:   18,
	siteFS:     10,
	logoW:      120,
	headlineFS: 24,
	detailFS:   12,
	seatFS:     16,
	detailRowH: 16,
	seatRowH:   19,
	labelW:     80,
	qrSize:     198.43, // ≈ 70 mm
	codeFS:     20,
	idFS:       9,

	eanModuleW:    0.4 * eanMMToPt, // ≈1.134pt (0.4mm — A4 has more room)
	eanBarH:       24 * eanMMToPt,  // ≈68.03pt
	eanGuardExtra: 1.5 * eanMMToPt, // ≈4.25pt
	eanDigitFS:    9,
	eanLeadGap:    4,

	legalFS:     8,
	legalLineH:  9,
	finePrintFS: 8,
	finePrintH:  32,
	blockGap:    10,
}

// specFor maps a Format to its layoutSpec.
func specFor(f Format) (layoutSpec, error) {
	switch f {
	case FormatMobile:
		return mobileSpec, nil
	case FormatA4Print:
		return a4Spec, nil
	default:
		return layoutSpec{}, fmt.Errorf("%w: %q", ErrUnknownFormat, f)
	}
}

// renderWithSpec draws the shared content blocks with the given geometry
// and returns the finished PDF bytes. Deterministic: compression off,
// catalog sort on, creation/modification dates pinned.
func renderWithSpec(ticket Ticket, spec layoutSpec) ([]byte, error) {
	pdf := gofpdf.NewCustom(&gofpdf.InitType{
		OrientationStr: "P",
		UnitStr:        "pt",
		Size:           gofpdf.SizeType{Wd: spec.pageW, Ht: spec.pageH},
	})
	registerFonts(pdf)
	labels := labelsFor(ticket.Locale)
	// Computed up front (before anything is drawn) so the EAN-13 block
	// below knows exactly how much vertical room it has before it would
	// collide with the footer — the footer's own position never moves, so
	// content above it must fit, not the other way round.
	legalLines := buildLegalLines(ticket, labels.Contact)
	footerY := spec.pageH - spec.margin - spec.finePrintH
	footerTop := footerY
	if len(legalLines) > 0 {
		legalHeight := float64(len(legalLines)) * spec.legalLineH
		footerTop = footerY - legalHeight - 6
	}
	pdf.SetMargins(spec.margin, spec.margin, spec.margin)
	pdf.SetAutoPageBreak(false, spec.margin)
	pdf.SetCompression(false) // deterministic byte output for tests
	// gofpdf emits resource dictionaries (fonts, images) by iterating Go
	// maps, whose order is randomized per process; sorting them keeps two
	// renders of the same Ticket byte-identical.
	pdf.SetCatalogSort(true)
	// gofpdf treats a zero time as "use time.Now() at render time". To keep
	// the render deterministic (the same Ticket renders to byte-identical
	// PDFs) we pin both timestamps to a fixed sentinel.
	pinned := pinnedTimestamp()
	pdf.SetCreationDate(pinned)
	pdf.SetModificationDate(pinned)
	pdf.SetProducer("arena-ticketing", true)
	pdf.SetCreator("arena-ticketing", true)
	pdf.SetTitle(fmt.Sprintf("Ticket %s", ticket.TicketID), true)
	pdf.AddPage()

	// ── Header band: org branding ─────────────────────────────────────
	drawHeader(pdf, ticket, spec)

	// ── Event headline + labelled detail rows ─────────────────────────
	y := spec.margin + spec.headerH + spec.blockGap
	y = drawHeadline(pdf, ticket, spec, y)
	labelW := computeLabelWidth(pdf, labels, spec)
	y = drawDetails(pdf, ticket, spec.margin, y, labelW, spec, labels)

	// ── Hero QR code (centered, high error correction) ────────────────
	qrPNG, err := qrcode.Encode(ticket.QRPayload, qrcode.High, 512)
	if err != nil {
		return nil, fmt.Errorf("pdf: encode qr: %w", err)
	}
	qrX := (spec.pageW - spec.qrSize) / 2
	qrY := y + spec.blockGap
	pdf.RegisterImageOptionsReader(
		"qr-"+ticket.TicketID,
		gofpdf.ImageOptions{ImageType: "PNG", ReadDpi: false},
		bytes.NewReader(qrPNG),
	)
	pdf.ImageOptions(
		"qr-"+ticket.TicketID,
		qrX, qrY, spec.qrSize, spec.qrSize,
		false, gofpdf.ImageOptions{ImageType: "PNG"},
		0, "",
	)
	y = qrY + spec.qrSize

	// ── Human code directly under the QR (manual-entry fallback) ──────
	if code := strings.TrimSpace(ticket.HumanCode); code != "" {
		y = drawHumanCode(pdf, code, spec, y+spec.blockGap)
	}

	// ── EAN-13 scannable barcode symbol under the QR/code block (spec §11):
	// guard bars + data bars + quiet zones + human-readable digits in the
	// conventional 1+6+6 grouping, replacing the old plain-text caption so
	// venue-entry laser/handheld scanners fed by the MACS access-control
	// export can actually read the ticket ─────────────────────────────
	if ean := strings.TrimSpace(ticket.EAN13); ean != "" {
		idLineH := spec.idFS + 4
		// The ticket-id line (drawn right after this block, at y+6) plus a
		// small buffer must still fit above footerTop, so the symbol's own
		// ceiling is footerTop minus that reservation — never the raw
		// footerTop itself.
		ceiling := footerTop - 4 - 6 - idLineH
		y = drawEAN13Symbol(pdf, ean, spec, y+spec.blockGap/2, ceiling)
	}

	// ── Ticket ID under the QR/code block ─────────────────────────────
	pdf.SetFont(fontFamily, "", spec.idFS)
	pdf.SetTextColor(102, 102, 102)
	pdf.SetXY(spec.margin, y+6)
	pdf.CellFormat(spec.pageW-2*spec.margin, spec.idFS+4,
		labels.TicketID+": "+ticket.TicketID, "", 0, "C", false, 0, "")
	pdf.SetTextColor(0, 0, 0)

	// ── Footer: legal-identification block ────────────────────────────
	// EU "commercial communications" minimum identification: the
	// organisation's legal name + registered address + a contact
	// channel. Drawn above the fine-print disclaimer. legalLines/footerY
	// were computed up front, before any drawing, so the EAN-13 block
	// above could size itself against the same footerTop boundary.
	if len(legalLines) > 0 {
		legalHeight := float64(len(legalLines)) * spec.legalLineH
		legalTop := footerY - legalHeight - 6
		pdf.SetFont(fontFamily, "", spec.legalFS)
		pdf.SetTextColor(102, 102, 102)
		pdf.SetXY(spec.margin, legalTop)
		for _, ln := range legalLines {
			pdf.CellFormat(spec.pageW-2*spec.margin, spec.legalLineH, ln, "", 2, "C", false, 0, "")
		}
		pdf.SetTextColor(0, 0, 0)
	}

	// ── Fine print ────────────────────────────────────────────────────
	fine := ticket.FinePrint
	if strings.TrimSpace(fine) == "" {
		fine = defaultFinePrintFor(ticket.Locale)
	}
	pdf.SetFont(fontFamily, "I", spec.finePrintFS)
	pdf.SetXY(spec.margin, footerY)
	pdf.MultiCell(spec.pageW-2*spec.margin, spec.finePrintFS+2, fine, "", "C", false)

	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, fmt.Errorf("pdf: output: %w", err)
	}
	return buf.Bytes(), nil
}

// drawHeader prints the org logo (if present) on the left and the org
// wordmark (falling back to "Arena E-Ticket") on the right of the header
// band, followed by a divider line.
func drawHeader(pdf *gofpdf.Fpdf, t Ticket, spec layoutSpec) {
	if len(t.OrgLogo) > 0 {
		imgType, ok := detectImageType(t.OrgLogo)
		if ok {
			name := "logo-" + t.TicketID
			pdf.RegisterImageOptionsReader(
				name,
				gofpdf.ImageOptions{ImageType: imgType, ReadDpi: false},
				bytes.NewReader(t.OrgLogo),
			)
			// Width-constrained slot — gofpdf preserves aspect when one
			// dimension is 0.
			pdf.ImageOptions(
				name,
				spec.margin, spec.margin, spec.logoW, 0,
				false, gofpdf.ImageOptions{ImageType: imgType},
				0, "",
			)
		}
	}

	// Right-aligned org branding wordmark stack. When OrgName is empty
	// we fall back to the generic "Arena E-Ticket" label so the header
	// always carries an identifier.
	orgName := strings.TrimSpace(t.OrgName)
	if orgName == "" {
		orgName = "Arena E-Ticket"
	}
	pdf.SetFont(fontFamily, "B", spec.headerFS)
	pdf.SetXY(spec.margin, spec.margin+spec.headerFS/2)
	pdf.CellFormat(spec.pageW-2*spec.margin, spec.headerFS+4, orgName, "", 0, "R", false, 0, "")

	if site := strings.TrimSpace(t.OrgWebsiteURL); site != "" {
		pdf.SetFont(fontFamily, "", spec.siteFS)
		pdf.SetTextColor(85, 85, 85)
		pdf.SetXY(spec.margin, spec.margin+spec.headerFS/2+spec.headerFS+6)
		pdf.CellFormat(spec.pageW-2*spec.margin, spec.siteFS+2, site, "", 0, "R", false, 0, "")
		pdf.SetTextColor(0, 0, 0)
	}

	// Divider underneath the header band.
	y := spec.margin + spec.headerH
	pdf.SetLineWidth(0.5)
	pdf.Line(spec.margin, y, spec.pageW-spec.margin, y)
}

// drawHeadline prints the event name in large type above the detail
// block and returns the y cursor below it. Long names wrap.
func drawHeadline(pdf *gofpdf.Fpdf, t Ticket, spec layoutSpec, y float64) float64 {
	pdf.SetFont(fontFamily, "B", spec.headlineFS)
	pdf.SetXY(spec.margin, y)
	pdf.MultiCell(spec.pageW-2*spec.margin, spec.headlineFS+4, t.EventName, "", "L", false)
	return pdf.GetY() + spec.blockGap/2
}

// labelColumnPaddingPt is the breathing room added after the widest
// label+colon string before the value column starts.
const labelColumnPaddingPt = 6.0

// labelColumnMaxFraction caps the label column at this fraction of the
// row's content width, so a locale with an unexpectedly long label
// dictionary cannot collapse the value column to nothing — the label
// simply prints up against the cap rather than the column growing without
// bound and pushing the value off the page.
const labelColumnMaxFraction = 0.45

// computeLabelWidth measures every label string drawDetails actually
// draws for the active locale — both at the regular detail-row font size
// (Session/Venue/Tier/Holder) and at the larger seat-row font size
// (Sector/Row/Seat, drawn bigger/bolder as the most prominent rows) —
// and returns the label column width wide enough to fit the widest one
// with labelColumnPaddingPt of breathing room, capped at
// labelColumnMaxFraction of the content width and never narrower than the
// format's spec.labelW baseline (so the already-tuned English column
// width is unaffected).
//
// This is what fixes the cs-locale overlap where "Místo konání:" (Venue,
// Czech) was wider than the old fixed labelW and overprinted its own
// value ("Místo konání:Divadlo…", first letter of the value lost under
// the label). Must be called AFTER registerFonts — it reads
// pdf.GetStringWidth against the currently loaded font — and it leaves
// the pdf's font state set to its last measurement; callers must
// SetFont again before drawing.
func computeLabelWidth(pdf *gofpdf.Fpdf, labels ticketLabels, spec layoutSpec) float64 {
	regular := []string{labels.Session, labels.Venue, labels.Tier, labels.Holder}
	seatLabels := []string{labels.Sector, labels.Row, labels.Seat}

	widest := 0.0
	measure := func(strs []string, fs float64) {
		pdf.SetFont(fontFamily, "B", fs)
		for _, s := range strs {
			if w := pdf.GetStringWidth(s + ":"); w > widest {
				widest = w
			}
		}
	}
	measure(regular, spec.detailFS)
	measure(seatLabels, spec.seatFS)

	want := widest + labelColumnPaddingPt
	if maxW := labelColumnMaxFraction * (spec.pageW - 2*spec.margin); want > maxW {
		want = maxW
	}
	if want < spec.labelW {
		want = spec.labelW
	}
	return want
}

// drawDetails renders the labelled detail block and returns the y cursor
// below it. labelW is the column width computed by computeLabelWidth for
// the ticket's active locale (never spec.labelW directly — that field is
// only the format's baseline/minimum).
//
// SEAT-C3 (feature #311): for tickets carrying denormalized seat
// coordinates (SeatSector / SeatRow / SeatNumber all populated together),
// three additional rows — Sector / Row / Seat — are inserted after Tier.
// SEAT-C4 draws them in the seat font size (the most prominent rows of
// the block, per the mobile-first spec). GA tickets skip the seat block
// entirely.
func drawDetails(pdf *gofpdf.Fpdf, t Ticket, x, y, labelW float64, spec layoutSpec, labels ticketLabels) float64 {
	type row struct {
		label, value string
		seat         bool
	}
	rows := []row{
		{label: labels.Session, value: formatSessionInVenueTZ(t.SessionStart, t.SessionTZ, t.Locale)},
		{label: labels.Venue, value: joinNonEmpty(", ", t.VenueName, t.VenueCity)},
		{label: labels.Tier, value: t.TierName},
	}
	if hasSeat(t) {
		rows = append(rows,
			row{label: labels.Sector, value: t.SeatSector, seat: true},
			row{label: labels.Row, value: t.SeatRow, seat: true},
			row{label: labels.Seat, value: t.SeatNumber, seat: true},
		)
	}
	rows = append(rows, row{label: labels.Holder, value: t.HolderName})

	for _, r := range rows {
		fs, rh := spec.detailFS, spec.detailRowH
		valueStyle := ""
		if r.seat {
			fs, rh = spec.seatFS, spec.seatRowH
			valueStyle = "B"
		}
		pdf.SetXY(x, y)
		pdf.SetFont(fontFamily, "B", fs)
		pdf.CellFormat(labelW, rh, r.label+":", "", 0, "L", false, 0, "")
		pdf.SetFont(fontFamily, valueStyle, fs)
		pdf.CellFormat(spec.pageW-x-spec.margin-labelW, rh, r.value, "", 0, "L", false, 0, "")
		y += rh
	}
	return y
}

// drawHumanCode prints the human-readable credential code centered in
// large letter-spaced monospace type (the manual-entry fallback at the
// gate) and returns the y cursor below it. gofpdf has no character
// spacing operator, so the spacing is drawn glyph by glyph: Courier is
// monospaced (600/1000 em advance), which makes per-glyph x positions
// exact and deterministic.
func drawHumanCode(pdf *gofpdf.Fpdf, code string, spec layoutSpec, y float64) float64 {
	const courierAdvance = 0.6 // Courier glyph advance as a fraction of the font size
	glyphW := spec.codeFS * courierAdvance
	gap := spec.codeFS * 0.25
	runes := []rune(code)
	total := float64(len(runes))*glyphW + float64(len(runes)-1)*gap
	x := (spec.pageW - total) / 2
	baseline := y + spec.codeFS

	pdf.SetFont("Courier", "B", spec.codeFS)
	for _, r := range runes {
		pdf.Text(x, baseline, string(r))
		x += glyphW + gap
	}
	return baseline + 4
}
