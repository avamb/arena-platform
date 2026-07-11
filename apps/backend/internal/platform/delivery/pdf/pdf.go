// Package pdf renders the e-ticket PDF for a single issued ticket.
//
// This package is intentionally a pure renderer: Render performs no IO and
// has no global state. The caller is responsible for resolving any
// dependencies (organization logo bytes via the media adapter, holder
// information from the order, session metadata in the venue's local
// timezone, ticket credential code, etc.) and passing them in via the
// Ticket struct.
//
// # Layouts (SEAT-C4)
//
// The legacy US-Letter print layout is gone: nobody prints tickets, the
// ticket is consumed on a phone screen (owner decision 2026-07-10). The
// renderer supports two layouts behind one API, both fed by the same
// content-projection struct (Ticket) so the printed fields can never
// diverge between them:
//
//   - FormatMobile (default) — portrait phone aspect (396×702 pt ≈ 9:16).
//     The QR code is the hero: ≥55% of the page width, centered, high
//     error correction, with the human-readable credential code printed
//     directly under it in large letter-spaced monospace type (the
//     manual-entry fallback at the gate). Above the QR: event name in
//     large type, date+time (venue-local), venue name/city, and — for
//     seated tickets — Sector / Row / Seat as the most prominent rows.
//     Org branding header and legal footer (feature #290 fields) are
//     kept, scaled to the narrow page. No stub, no tear-off, no
//     duplicated blocks.
//
//   - FormatA4Print — A4 portrait for the organizers that still want a
//     printable ticket. Same content blocks scaled up, QR ≈70 mm with
//     the human code beneath, generous margins for home printers. Still
//     no stub/tear-off.
//
// Which format(s) the delivery email attaches is the organizer-level
// organizations.ticket_pdf_format flag ('mobile' | 'a4' | 'both'),
// threaded through delivery.Payload.TicketPDFFormat.
//
// # Library choice
//
// The renderer uses github.com/jung-kurt/gofpdf, pinned in the repo's
// go.mod: it ships the PDF Core 14 fonts built in (no font IO), accepts
// in-memory image sources (RegisterImageOptionsReader), and its API
// surface is small, mature, and frozen. QR code rasterisation uses
// github.com/skip2/go-qrcode, also a pure-Go in-memory implementation.
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
)

// Format selects one of the two SEAT-C4 page layouts. Both layouts render
// the same Ticket content projection.
type Format string

const (
	// FormatMobile is the default phone-aspect layout (396×702 pt) with
	// the QR code as the hero element.
	FormatMobile Format = "mobile"
	// FormatA4Print is the A4 portrait variant for organizers that still
	// want a printable ticket.
	FormatA4Print Format = "a4"
)

// Ticket carries every datum the PDF renderer needs. It is intentionally
// flat and serialisable: callers project this from their domain models
// (and resolve logo bytes via the media adapter) before calling Render.
// It is the single content-projection struct shared by both layouts —
// FormatMobile and FormatA4Print can never print diverging field sets.
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

	// SeatSector / SeatRow / SeatNumber are the denormalized seat
	// coordinates copied from tickets.seat_sector / seat_row / seat_number
	// (SEAT-C3, feature #311). All three are empty for general-admission
	// tickets, in which case drawDetails omits the Sector / Row / Seat
	// rows entirely. For assigned-seat tickets the renderer prints
	// dedicated Sector / Row / Seat rows as the most prominent rows of
	// the details block.
	SeatSector string
	SeatRow    string
	SeatNumber string

	// OrgLogo is the organization logo image bytes (PNG or JPEG).
	// Empty means "no logo available"; the renderer skips the logo slot
	// and prints the event name in its place. This is the resolved output
	// of the media adapter — the renderer never performs a media lookup.
	OrgLogo []byte

	// QRPayload is the string encoded into the QR code; this MUST be the
	// value of ticket_credentials.payload (static_qr) for the ticket
	// being rendered.
	QRPayload string

	// HumanCode is the SEAT-C4 human-readable manual-entry fallback
	// printed under the QR code in large letter-spaced monospace type.
	// The renderer prints exactly what it is given — callers pass the
	// grouped display form ("XXXX-XXXX", see humancode.Format). Empty
	// omits the code line (legacy credentials without a code).
	HumanCode string

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

// ErrUnknownFormat is returned by RenderFormat for a Format value other
// than FormatMobile or FormatA4Print.
var ErrUnknownFormat = errors.New("pdf: unknown render format")

// Render returns the bytes of a single-page e-ticket PDF in the default
// FormatMobile layout. See RenderFormat for the two-layout contract.
func Render(ctx context.Context, ticket Ticket) ([]byte, error) {
	return RenderFormat(ctx, ticket, FormatMobile)
}

// RenderFormat renders the ticket in the requested layout. The function
// is pure: it performs no network or filesystem IO, holds no global
// state, and returns the same bytes for the same (ticket, format) pair
// every time it is called.
//
// ctx is honoured for cancellation only — it is checked once before the
// (cheap) layout pass starts so that callers can abort a batch render
// before this ticket's work begins.
func RenderFormat(ctx context.Context, ticket Ticket, format Format) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validate(ticket); err != nil {
		return nil, err
	}
	spec, err := specFor(format)
	if err != nil {
		return nil, err
	}
	return renderWithSpec(ticket, spec)
}

// pinnedTimestamp is the fixed sentinel stamped into every render as the
// PDF creation/modification date. gofpdf substitutes time.Now() for a
// zero time, which would break the byte-determinism contract.
func pinnedTimestamp() time.Time {
	return time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
}

// validate enforces the must-have fields for a sane e-ticket render.
//
// Cosmetic fields (venue city, tier, holder name, human code) are not
// required — they are omitted or printed as the empty string if missing,
// which is preferable to refusing to issue a ticket because a tier name
// is blank.
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

// hasSeat reports whether the ticket carries denormalized seat
// coordinates. All three fields must be non-empty (trimmed) for the seat
// block to render — a partially populated set signals an issuance-time
// data bug rather than a legitimate seated ticket.
func hasSeat(t Ticket) bool {
	return strings.TrimSpace(t.SeatSector) != "" &&
		strings.TrimSpace(t.SeatRow) != "" &&
		strings.TrimSpace(t.SeatNumber) != ""
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
