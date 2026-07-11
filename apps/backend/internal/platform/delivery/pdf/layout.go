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
	labelW     float64 // label column width

	qrSize float64 // QR square edge
	codeFS float64 // human-code font size (letter-spaced mono)
	idFS   float64 // ticket-id line font size

	legalFS     float64 // legal footer font size
	legalLineH  float64 // legal footer line height
	finePrintFS float64 // fine print font size
	finePrintH  float64 // vertical space reserved for the fine print
	blockGap    float64 // vertical gap between major blocks
}

// mobileSpec is the default phone-aspect layout: 396×702 pt ≈ 9:16.
// qrSize/pageW = 232/396 ≈ 0.586, satisfying the ≥55%-of-page-width
// acceptance bound for the hero QR.
var mobileSpec = layoutSpec{
	pageW: 396, pageH: 702,
	margin:      24,
	headerH:     54,
	headerFS:    14,
	siteFS:      8,
	logoW:       80,
	headlineFS:  18,
	detailFS:    10,
	seatFS:      14,
	detailRowH:  14,
	seatRowH:    18,
	labelW:      58,
	qrSize:      232,
	codeFS:      20,
	idFS:        7,
	legalFS:     6,
	legalLineH:  8,
	finePrintFS: 6,
	finePrintH:  34,
	blockGap:    12,
}

// a4Spec is the printable A4 portrait variant (595.28×841.89 pt) with
// generous home-printer margins. qrSize = 198.43 pt ≈ 70 mm.
var a4Spec = layoutSpec{
	pageW: 595.28, pageH: 841.89,
	margin:      56.7, // 2 cm
	headerH:     80,
	headerFS:    18,
	siteFS:      10,
	logoW:       120,
	headlineFS:  24,
	detailFS:    12,
	seatFS:      16,
	detailRowH:  18,
	seatRowH:    22,
	labelW:      80,
	qrSize:      198.43, // ≈ 70 mm
	codeFS:      24,
	idFS:        9,
	legalFS:     8,
	legalLineH:  10,
	finePrintFS: 8,
	finePrintH:  44,
	blockGap:    18,
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
	y = drawDetails(pdf, ticket, spec.margin, y, spec)

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

	// ── Ticket ID under the QR/code block ─────────────────────────────
	pdf.SetFont("Helvetica", "", spec.idFS)
	pdf.SetTextColor(102, 102, 102)
	pdf.SetXY(spec.margin, y+6)
	pdf.CellFormat(spec.pageW-2*spec.margin, spec.idFS+4,
		"Ticket ID: "+ticket.TicketID, "", 0, "C", false, 0, "")
	pdf.SetTextColor(0, 0, 0)

	// ── Footer: legal-identification block ────────────────────────────
	// EU "commercial communications" minimum identification: the
	// organisation's legal name + registered address + a contact
	// channel. Drawn above the fine-print disclaimer.
	footerY := spec.pageH - spec.margin - spec.finePrintH
	legalLines := buildLegalLines(ticket)
	if len(legalLines) > 0 {
		legalHeight := float64(len(legalLines)) * spec.legalLineH
		legalTop := footerY - legalHeight - 6
		pdf.SetFont("Helvetica", "", spec.legalFS)
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
		fine = DefaultFinePrint
	}
	pdf.SetFont("Helvetica", "I", spec.finePrintFS)
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
	pdf.SetFont("Helvetica", "B", spec.headerFS)
	pdf.SetXY(spec.margin, spec.margin+spec.headerFS/2)
	pdf.CellFormat(spec.pageW-2*spec.margin, spec.headerFS+4, orgName, "", 0, "R", false, 0, "")

	if site := strings.TrimSpace(t.OrgWebsiteURL); site != "" {
		pdf.SetFont("Helvetica", "", spec.siteFS)
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
	pdf.SetFont("Helvetica", "B", spec.headlineFS)
	pdf.SetXY(spec.margin, y)
	pdf.MultiCell(spec.pageW-2*spec.margin, spec.headlineFS+4, t.EventName, "", "L", false)
	return pdf.GetY() + spec.blockGap/2
}

// drawDetails renders the labelled detail block and returns the y cursor
// below it.
//
// SEAT-C3 (feature #311): for tickets carrying denormalized seat
// coordinates (SeatSector / SeatRow / SeatNumber all populated together),
// three additional rows — Sector / Row / Seat — are inserted after Tier.
// SEAT-C4 draws them in the seat font size (the most prominent rows of
// the block, per the mobile-first spec). GA tickets skip the seat block
// entirely.
func drawDetails(pdf *gofpdf.Fpdf, t Ticket, x, y float64, spec layoutSpec) float64 {
	type row struct {
		label, value string
		seat         bool
	}
	rows := []row{
		{label: "Session", value: formatSessionInVenueTZ(t.SessionStart, t.SessionTZ)},
		{label: "Venue", value: joinNonEmpty(", ", t.VenueName, t.VenueCity)},
		{label: "Tier", value: t.TierName},
	}
	if hasSeat(t) {
		rows = append(rows,
			row{label: "Sector", value: t.SeatSector, seat: true},
			row{label: "Row", value: t.SeatRow, seat: true},
			row{label: "Seat", value: t.SeatNumber, seat: true},
		)
	}
	rows = append(rows, row{label: "Holder", value: t.HolderName})

	for _, r := range rows {
		fs, rh := spec.detailFS, spec.detailRowH
		valueStyle := ""
		if r.seat {
			fs, rh = spec.seatFS, spec.seatRowH
			valueStyle = "B"
		}
		pdf.SetXY(x, y)
		pdf.SetFont("Helvetica", "B", fs)
		pdf.CellFormat(spec.labelW, rh, r.label+":", "", 0, "L", false, 0, "")
		pdf.SetFont("Helvetica", valueStyle, fs)
		pdf.CellFormat(spec.pageW-x-spec.margin-spec.labelW, rh, r.value, "", 0, "L", false, 0, "")
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
