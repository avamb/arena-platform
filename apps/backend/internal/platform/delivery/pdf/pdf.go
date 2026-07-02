// Package pdf renders a printable e-ticket PDF for a single issued ticket.
//
// This package is intentionally a pure renderer: Render performs no IO and
// has no global state. The caller is responsible for resolving any
// dependencies (organization logo bytes via the media adapter, holder
// information from the order, session metadata in the venue's local
// timezone, ticket credential code, etc.) and passing them in via the
// Ticket struct.
//
// # Library choice
//
// The renderer uses github.com/jung-kurt/gofpdf, pinned in the repo's
// go.mod. The two libraries the feature spec proposes are:
//
//   - github.com/jung-kurt/gofpdf
//   - github.com/signintech/gopdf
//
// We chose gofpdf because:
//
//  1. gofpdf ships PDF Core 14 fonts (Helvetica, Times, Courier, Symbol,
//     ZapfDingbats) built in, while gopdf requires every text glyph to come
//     from an external TTF that the renderer must read from disk. The
//     "no IO" requirement of this feature is therefore satisfiable with
//     gofpdf out-of-the-box; gopdf would force us to embed a TTF via
//     //go:embed and burn ~250 KiB of font data into the binary just to
//     print one line of text.
//
//  2. gofpdf accepts in-memory image sources (RegisterImageOptionsReader),
//     so the org logo we receive as a []byte from the media adapter can be
//     embedded without writing it to a temp file.
//
//  3. gofpdf's API surface is small, mature, and frozen. Its single
//     dependency tree is itself a single non-transitive package
//     (no headless browser, no Cgo, no font shaper). The package is in
//     "low-activity maintenance" but the PDF/1.4 output it produces is
//     stable and renders correctly in every reader we target. We do not
//     need any of gopdf's newer features (PDF/A, form fields, encryption).
//
// QR code rasterisation uses github.com/skip2/go-qrcode, also a pure-Go
// in-memory implementation with no IO. The PNG bytes it returns are
// embedded into the PDF via RegisterImageOptionsReader the same way the
// org logo is.
//
// # Layout (US-Letter, portrait, 612x792 points)
//
//	┌────────────────────────────────────────────────────────────┐
//	│ [ORG LOGO]                                Arena Ticket     │
//	├────────────────────────────────────────────────────────────┤
//	│                                                            │
//	│  Event:    {event name}                                    │
//	│  Session:  {YYYY-MM-DD HH:MM} (venue local, {tz})          │
//	│  Venue:    {venue name}, {venue city}                      │
//	│  Tier:     {tier name}                                     │
//	│  Holder:   {holder name}                                   │
//	│                                                            │
//	│  Ticket ID: {ticket id printable}                          │
//	│                                                            │
//	│             ┌─────────────┐                                │
//	│             │             │                                │
//	│             │   QR CODE   │                                │
//	│             │             │                                │
//	│             └─────────────┘                                │
//	│                                                            │
//	│  {fine print}                                              │
//	│                                                            │
//	└────────────────────────────────────────────────────────────┘
//
// The renderer deliberately omits any fiscal-receipt block. Issuing a
// fiscal receipt (Russian Federation 54-FZ workflow) is a downstream
// integration that produces its own document; the e-ticket PDF is not
// a tax document.
package pdf

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg" // register JPEG decoder for logo type-detection
	_ "image/png"  // register PNG decoder for logo type-detection
	"strings"
	"time"

	"github.com/jung-kurt/gofpdf"
	"github.com/skip2/go-qrcode"
)

// Ticket carries every datum the PDF renderer needs. It is intentionally
// flat and serialisable: callers project this from their domain models
// (and resolve logo bytes via the media adapter) before calling Render.
type Ticket struct {
	// TicketID is the printable canonical ticket identifier (UUID string).
	TicketID string

	// EventName is the human-readable event name.
	EventName string

	// SessionStart is the session start instant (UTC).
	SessionStart time.Time
	// SessionTZ is the IANA timezone name of the venue
	// (for example "Europe/Moscow"). Used to format SessionStart in the
	// venue's local time on the printed ticket.
	SessionTZ string

	// VenueName is the venue's display name.
	VenueName string
	// VenueCity is the venue's city.
	VenueCity string

	// TierName is the price tier / category name (for example "VIP", "Stalls").
	TierName string

	// HolderName is the printed ticket holder name.
	HolderName string

	// OrgLogo is the organization logo image bytes (PNG or JPEG).
	// Empty means "no logo available"; the renderer skips the logo slot
	// and prints the event name in its place. This is the resolved output
	// of the media adapter — the renderer never performs a media lookup.
	OrgLogo []byte

	// QRPayload is the string encoded into the QR code; this MUST be the
	// value of ticket_credentials.code for the ticket being rendered.
	QRPayload string

	// FinePrint is the small disclaimer printed at the bottom of the
	// ticket. Empty string falls back to DefaultFinePrint.
	FinePrint string

	// ── Organisation branding (feature #290, T-3) ─────────────────────
	// These are the same branding fields that appear in the email
	// header/footer. They are rendered into the PDF header (org name
	// and website right of the logo) and footer (legal identification
	// block above the FinePrint). All are optional; empty fields are
	// silently skipped so the renderer never refuses to print a ticket
	// because branding metadata is missing.

	// OrgName is the public organisation display name printed in the
	// header next to the logo. Empty omits the wordmark.
	OrgName string
	// OrgWebsiteURL is rendered as a small line under OrgName in the
	// header. Empty omits the URL.
	OrgWebsiteURL string

	// LegalName is the registered juridical name printed in the footer
	// legal block. Empty omits the entire footer block.
	LegalName string
	// LegalAddressLine1/2, PostalCode/City, Country compose the footer
	// address. Each is rendered on its own line; empty values are
	// suppressed.
	LegalAddressLine1      string
	LegalAddressLine2      string
	LegalAddressPostalCode string
	LegalAddressCity       string
	LegalAddressCountry    string
	// ContactEmail is the public contact rendered in the footer.
	ContactEmail string
}

// DefaultFinePrint is the disclaimer printed when Ticket.FinePrint is empty.
//
// It deliberately mentions only resale, non-refundability and that the QR
// code is the only proof of admission. It does NOT include any
// fiscal-receipt language.
const DefaultFinePrint = "This e-ticket is valid only when the QR code above scans successfully at the venue. " +
	"Possession of this PDF without a successful QR scan grants no admission rights. " +
	"Resale outside the organizer's official channels may invalidate the ticket. " +
	"This document is not a fiscal receipt."

// ErrInvalidTicket is returned by Render when the Ticket is missing data
// that is required to produce a usable PDF (an empty QR payload, ticket
// id, or event name).
var ErrInvalidTicket = errors.New("pdf: ticket missing required fields")

// Render returns the bytes of a single-page PDF e-ticket. The function is
// pure: it performs no network or filesystem IO, holds no global state,
// and returns the same bytes for the same input every time it is called.
//
// ctx is honoured for cancellation only — it is checked once before the
// (cheap) layout pass starts so that callers can abort a batch render
// before this ticket's work begins.
func Render(ctx context.Context, ticket Ticket) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validate(ticket); err != nil {
		return nil, err
	}

	const (
		pageW  = 612.0 // US Letter width  in points
		pageH  = 792.0 // US Letter height in points
		margin = 54.0  // 0.75 inch margin
	)

	pdf := gofpdf.New("P", "pt", "Letter", "")
	pdf.SetMargins(margin, margin, margin)
	pdf.SetAutoPageBreak(false, margin)
	pdf.SetCompression(false) // deterministic byte output for tests
	// gofpdf emits resource dictionaries (fonts, images) by iterating Go
	// maps, whose order is randomized per process; sorting them keeps two
	// renders of the same Ticket byte-identical.
	pdf.SetCatalogSort(true)
	// gofpdf treats a zero time as "use time.Now() at render time". To keep
	// Render's output deterministic (the same Ticket renders to byte-identical
	// PDFs) we pin both timestamps to a fixed sentinel.
	pinned := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	pdf.SetCreationDate(pinned)
	pdf.SetModificationDate(pinned)
	pdf.SetProducer("arena-ticketing", true)
	pdf.SetCreator("arena-ticketing", true)
	pdf.SetTitle(fmt.Sprintf("Ticket %s", ticket.TicketID), true)
	pdf.AddPage()

	// ── Header band ───────────────────────────────────────────────────
	drawHeader(pdf, ticket, margin, pageW)

	// ── Body: labelled detail lines ───────────────────────────────────
	bodyTop := margin + 110.0
	drawDetails(pdf, ticket, margin, bodyTop)

	// ── QR code (centered) ────────────────────────────────────────────
	qrPNG, err := qrcode.Encode(ticket.QRPayload, qrcode.Medium, 512)
	if err != nil {
		return nil, fmt.Errorf("pdf: encode qr: %w", err)
	}
	const qrSize = 180.0
	qrX := (pageW - qrSize) / 2
	qrY := bodyTop + 180.0
	pdf.RegisterImageOptionsReader(
		"qr-"+ticket.TicketID,
		gofpdf.ImageOptions{ImageType: "PNG", ReadDpi: false},
		bytes.NewReader(qrPNG),
	)
	pdf.ImageOptions(
		"qr-"+ticket.TicketID,
		qrX, qrY, qrSize, qrSize,
		false, gofpdf.ImageOptions{ImageType: "PNG"},
		0, "",
	)

	// ── Ticket ID under the QR ────────────────────────────────────────
	pdf.SetFont("Helvetica", "", 10)
	pdf.SetXY(margin, qrY+qrSize+8)
	pdf.CellFormat(pageW-2*margin, 14,
		"Ticket ID: "+ticket.TicketID, "", 0, "C", false, 0, "")

	// ── Footer: legal-identification block ────────────────────────────
	// EU "commercial communications" minimum identification: the
	// organisation's legal name + registered address + a contact
	// channel. Drawn above the fine-print disclaimer.
	footerY := pageH - margin - 44
	legalLines := buildLegalLines(ticket)
	if len(legalLines) > 0 {
		// Reserve ~10pt per legal line above the fine print.
		legalHeight := float64(len(legalLines)) * 10
		legalTop := footerY - legalHeight - 6
		pdf.SetFont("Helvetica", "", 8)
		pdf.SetTextColor(102, 102, 102)
		pdf.SetXY(margin, legalTop)
		for _, ln := range legalLines {
			pdf.CellFormat(pageW-2*margin, 10, ln, "", 2, "C", false, 0, "")
		}
		pdf.SetTextColor(0, 0, 0)
	}

	// ── Fine print ────────────────────────────────────────────────────
	fine := ticket.FinePrint
	if strings.TrimSpace(fine) == "" {
		fine = DefaultFinePrint
	}
	pdf.SetFont("Helvetica", "I", 8)
	pdf.SetXY(margin, footerY)
	pdf.MultiCell(pageW-2*margin, 10, fine, "", "C", false)

	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, fmt.Errorf("pdf: output: %w", err)
	}
	return buf.Bytes(), nil
}

// validate enforces the must-have fields for a sane e-ticket render.
//
// Cosmetic fields (venue city, tier, holder name) are not required —
// they are printed as the empty string if missing, which is preferable
// to refusing to issue a ticket because a tier name is blank.
func validate(t Ticket) error {
	switch {
	case strings.TrimSpace(t.TicketID) == "":
		return fmt.Errorf("%w: ticket_id", ErrInvalidTicket)
	case strings.TrimSpace(t.EventName) == "":
		return fmt.Errorf("%w: event_name", ErrInvalidTicket)
	case strings.TrimSpace(t.QRPayload) == "":
		return fmt.Errorf("%w: qr_payload", ErrInvalidTicket)
	case t.SessionStart.IsZero():
		return fmt.Errorf("%w: session_start", ErrInvalidTicket)
	}
	return nil
}

// drawHeader prints the org logo (if present) on the left and the
// "Arena Ticket" wordmark on the right of the header band.
func drawHeader(pdf *gofpdf.Fpdf, t Ticket, margin, pageW float64) {
	const headerHeight = 80.0

	if len(t.OrgLogo) > 0 {
		imgType, ok := detectImageType(t.OrgLogo)
		if ok {
			name := "logo-" + t.TicketID
			pdf.RegisterImageOptionsReader(
				name,
				gofpdf.ImageOptions{ImageType: imgType, ReadDpi: false},
				bytes.NewReader(t.OrgLogo),
			)
			// Box: 120pt wide, 60pt tall — gofpdf preserves aspect when one dim is 0.
			pdf.ImageOptions(
				name,
				margin, margin, 120, 0,
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
	pdf.SetFont("Helvetica", "B", 18)
	pdf.SetXY(margin, margin+18)
	pdf.CellFormat(pageW-2*margin, 22, orgName, "", 0, "R", false, 0, "")

	if site := strings.TrimSpace(t.OrgWebsiteURL); site != "" {
		pdf.SetFont("Helvetica", "", 10)
		pdf.SetTextColor(85, 85, 85)
		pdf.SetXY(margin, margin+42)
		pdf.CellFormat(pageW-2*margin, 12, site, "", 0, "R", false, 0, "")
		pdf.SetTextColor(0, 0, 0)
	}

	// Divider underneath the header band.
	y := margin + headerHeight + 8
	pdf.SetLineWidth(0.5)
	pdf.Line(margin, y, pageW-margin, y)
}

// buildLegalLines composes the footer legal-identification block from
// the branding fields on the Ticket. Returns an empty slice when none
// of the legal fields are populated, in which case the renderer omits
// the block entirely (the FinePrint disclaimer still prints).
//
// Lines: LegalName, address-line1, address-line2, "<postal> <city>",
// country, "Contact: <email>".
func buildLegalLines(t Ticket) []string {
	out := []string{}
	if name := strings.TrimSpace(t.LegalName); name != "" {
		out = append(out, name)
	}
	if v := strings.TrimSpace(t.LegalAddressLine1); v != "" {
		out = append(out, v)
	}
	if v := strings.TrimSpace(t.LegalAddressLine2); v != "" {
		out = append(out, v)
	}
	postal := strings.TrimSpace(t.LegalAddressPostalCode)
	city := strings.TrimSpace(t.LegalAddressCity)
	switch {
	case postal != "" && city != "":
		out = append(out, postal+" "+city)
	case postal != "":
		out = append(out, postal)
	case city != "":
		out = append(out, city)
	}
	if v := strings.TrimSpace(t.LegalAddressCountry); v != "" {
		out = append(out, v)
	}
	if v := strings.TrimSpace(t.ContactEmail); v != "" {
		out = append(out, "Contact: "+v)
	}
	return out
}

// drawDetails renders the labelled detail block.
func drawDetails(pdf *gofpdf.Fpdf, t Ticket, x, y float64) {
	rows := [][2]string{
		{"Event", t.EventName},
		{"Session", formatSessionInVenueTZ(t.SessionStart, t.SessionTZ)},
		{"Venue", joinNonEmpty(", ", t.VenueName, t.VenueCity)},
		{"Tier", t.TierName},
		{"Holder", t.HolderName},
	}

	const (
		labelW  = 80.0
		rowH    = 18.0
		valueFS = 12.0
	)
	for i, r := range rows {
		ry := y + float64(i)*rowH
		pdf.SetXY(x, ry)
		pdf.SetFont("Helvetica", "B", valueFS)
		pdf.CellFormat(labelW, rowH, r[0]+":", "", 0, "L", false, 0, "")
		pdf.SetFont("Helvetica", "", valueFS)
		pdf.CellFormat(380, rowH, r[1], "", 0, "L", false, 0, "")
	}
}

// formatSessionInVenueTZ converts the UTC session start into the venue's
// local clock time and returns a "YYYY-MM-DD HH:MM (ZoneName)" string.
//
// If tz is empty or LoadLocation fails, the time is rendered in UTC and
// the zone label is "UTC" — the renderer never panics on a bad zone.
func formatSessionInVenueTZ(t time.Time, tz string) string {
	loc := time.UTC
	zoneLabel := "UTC"
	if strings.TrimSpace(tz) != "" {
		if l, err := time.LoadLocation(tz); err == nil {
			loc = l
			zoneLabel = tz
		}
	}
	local := t.In(loc)
	// Human-facing PDF ticket text rendered in the venue-local timezone;
	// allow:timeformat: deliberately not an RFC3339 API timestamp.
	return fmt.Sprintf("%s (%s)", local.Format("2006-01-02 15:04"), zoneLabel)
}

// joinNonEmpty joins the non-empty arguments with sep.
func joinNonEmpty(sep string, parts ...string) string {
	kept := parts[:0]
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, sep)
}

// detectImageType peeks at the leading bytes of an in-memory image to
// decide whether it is PNG or JPEG. gofpdf's RegisterImageOptionsReader
// needs to be told the format explicitly; auto-decoding via image.Decode
// gives us a robust answer without re-encoding.
func detectImageType(b []byte) (string, bool) {
	if len(b) < 8 {
		return "", false
	}
	_, format, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return "", false
	}
	switch format {
	case "png":
		return "PNG", true
	case "jpeg":
		return "JPEG", true
	}
	return "", false
}
