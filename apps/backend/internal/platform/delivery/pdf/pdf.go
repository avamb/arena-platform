// Package pdf renders the e-ticket PDF for a single issued ticket.
//
// This package is intentionally a pure renderer: Render performs no IO and
// has no global state. The caller resolves every dependency (organization
// logo and event poster bytes via the media adapter, holder information from
// the order, session metadata, the ticket's EAN-13 credential) and passes
// them in through the Ticket struct.
//
// # The layout
//
// The page is a Go port of the ticket design the owner already ships in
// production from the Lampyris and Vino&Co WordPress sites
// (bil24-ticket-mailer: templates/ticket.php + includes/class-btm-renderer.php).
// That design is the authoritative spec; the constraints it encodes are
// repeated on the constants in layout.go so a future edit cannot break them
// by accident:
//
//   - 105x297 mm, half of A4 lengthwise — NOT A5. Two consecutive barcodes
//     stay a full page apart, so a fit-to-width phone viewport can never show
//     two tickets' codes at once and an entrance scanner cannot grab the
//     neighbouring ticket. Page height equals A4's, so it prints on A4 at
//     exactly 100% and "2 pages per sheet" tiles two tickets onto one sheet.
//   - The code block is ANCHORED at 140 mm from the top of every page. A long
//     title or a long venue address can never push it down.
//   - Long values SHRINK their font rather than reflow the page: the event
//     title drops from 13.5pt to 11.5pt past 55 characters, an info value
//     from 10.5pt to 9pt past 28.
//
// # Two deliberate departures from the WordPress original
//
//  1. A QR CODE is drawn above the barcode, carrying EXACTLY the barcode's
//     value — the EAN-13 digits, nothing else. Entrance control (MACS) reads
//     the QR, which scans far more reliably off a phone screen than a barcode
//     does; the barcode stays because the human-readable EAN-13 number
//     printed under it is what staff type in when a scanner fails. The QR
//     deliberately no longer encodes the ticket UUID, which is what the
//     pre-port arena layout did and which nothing could resolve.
//  2. The accent colour defaults to the Arena Sold Out indigo
//     (DefaultAccentColor) instead of the original's hardcoded Lampyris
//     yellow. It is a single field on Ticket so a later wave can set it per
//     organization — Lampyris' own tickets must be able to stay yellow once
//     they move onto arena.
//
// # Library choice
//
// The renderer uses github.com/jung-kurt/gofpdf, pinned in the repo's
// go.mod: it accepts in-memory image AND font sources
// (RegisterImageOptionsReader, AddUTF8FontFromBytes), and its API surface is
// small, mature and frozen. QR rasterisation uses
// github.com/skip2/go-qrcode, also pure Go and in-memory. The EAN-13 symbol
// is hand-drawn (ean13_symbol.go) — no barcode library.
//
// # Fonts and internationalisation
//
// The renderer registers one embedded UTF-8 TrueType family (DejaVu Sans
// Condensed, see fonts.go) instead of gofpdf's built-in Core 14 fonts
// (Helvetica et al., which are WinAnsi/Latin-1 only and render anything
// outside that range as mojibake — the pre-2026-09 bug). Never add a
// SetFont("Helvetica", ...) call back into this package.
//
// Ticket.Locale selects the CHROME language only (labels.go) — the printed
// field labels, the footer wording, and the month/weekday names the date is
// built from. Content values (event name, venue, holder, category) are
// organizer/buyer data and are never translated, which is why the font needs
// broad Unicode coverage independently of the locale.
//
// # Graceful degradation
//
// Both of the first live clients have neither an organizer logo nor an event
// poster, so every optional element simply disappears rather than leaving a
// hole: no poster means the date/venue column spans the full page width, no
// logo means the accent band sits at the very top, and a missing address,
// category, holder, order number or price drops that element without leaving
// a gap. The renderer deliberately omits any fiscal-receipt block; an
// e-ticket is not a tax document, and the footer says so.
package pdf

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg" // register JPEG decoder for image type-detection
	_ "image/png"  // register PNG decoder for image type-detection
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Format names a page layout. Both values render the SAME 105x297 mm page:
// the ported design is already the answer to "I want to print this", because
// its page height is A4's and it prints at exactly 100% scale (and tiles two
// tickets per sheet with "2 pages per sheet"). The constants are kept so the
// organizations.ticket_pdf_format flag and delivery.Payload.TicketPDFFormat
// keep resolving, and so an unknown value is still an error rather than a
// silent default.
type Format string

const (
	// FormatMobile is the default: the 105x297 mm phone-first page.
	FormatMobile Format = "mobile"
	// FormatA4Print is an alias of FormatMobile — see Format.
	FormatA4Print Format = "a4"
)

// DefaultAccentColor is the Arena Sold Out accent (apps/tickets-page's
// --asa-accent). It paints the band under the header and the rule under the
// event title.
//
// It is a Ticket field rather than a literal sprinkled through the layout
// precisely so a later wave can drive it from the organization record:
// Lampyris' tickets are yellow (#FCDC54) today on WordPress and must be able
// to stay yellow when they move onto arena.
const DefaultAccentColor = "#4f46e5"

// Ticket carries every datum the renderer needs. It is intentionally flat
// and serialisable: callers project it from their domain models (and resolve
// image bytes via the media adapter) before calling Render.
type Ticket struct {
	// TicketID is the canonical internal ticket identifier (UUID string).
	//
	// It is NEVER printed on the page, never reaches the PDF metadata, and —
	// since the QR carries the EAN-13 — is no longer encoded into any machine
	// -readable mark either. It is still required: it keys the in-document
	// image resource names, which gofpdf holds in a map and never writes into
	// the output.
	TicketID string

	// TicketNumber is the human-facing ticket number printed under the codes
	// and used as the PDF document title — tickets.system_ticket_id, the
	// platform's own bigint identity for the ticket. Empty falls back to a
	// short reference derived from TicketID; the full UUID is never printed
	// either way.
	TicketNumber string

	// OrderNumber is the buyer-facing order reference printed in the footer
	// ("Order 9096"). Empty drops the line.
	OrderNumber string

	// Locale selects the language of the printed CHROME — labels, footer
	// wording, month and weekday names. Supported values are
	// SupportedLocales; anything else falls back to DefaultLocale. Content
	// values are never translated.
	Locale string

	// AccentColor is the accent band / title rule colour as a CSS-style hex
	// string ("#4f46e5", with or without the leading '#', 3 or 6 digits).
	// Empty or unparseable falls back to DefaultAccentColor.
	AccentColor string

	// EventName is the human-readable event name — the page's headline.
	EventName string

	// SessionStart is the session start instant (UTC); SessionTZ is the
	// venue's IANA timezone name (for example "Europe/Prague"), used to print
	// SessionStart on the venue's own wall clock. An empty or unknown zone
	// falls back to UTC rather than failing.
	SessionStart time.Time
	SessionTZ    string

	// VenueName / VenueAddress / VenueCity compose the "where" block.
	// VenueName prints bold; address and city join with ", " on the line
	// under it, and the whole line disappears when both are empty.
	VenueName    string
	VenueAddress string
	VenueCity    string

	// TierName is the price tier / category name ("VIP", "Stalls"). Empty
	// drops the Category info cell.
	TierName string

	// SeatSector / SeatRow / SeatNumber are the denormalized seat coordinates
	// copied from tickets.seat_sector / seat_row / seat_number. All three are
	// empty for general-admission tickets, which then have no Seat info cell
	// at all. Any non-empty subset renders — a seated ticket that only knows
	// its row still prints "row 3" rather than nothing.
	SeatSector string
	SeatRow    string
	SeatNumber string

	// HolderName is the printed ticket holder name. Empty drops the cell.
	HolderName string

	// PriceMinor is the ticket price in MINOR units (the platform's storage
	// unit everywhere — see the AGENTS.md money-units gotcha). nil means "no
	// price known"; a zero value means a free ticket. Both drop the Price
	// cell entirely rather than printing "0".
	PriceMinor *int64
	// Currency is the ISO code appended to the formatted price ("CZK").
	Currency string
	// PriceLabel replaces the formatted amount outright when non-empty — the
	// ported `price_label_override`, used for a complimentary ticket where
	// the nominal category price would be misleading ("Invitation").
	PriceLabel string

	// OrgLogo is the organization logo image bytes (PNG or JPEG), drawn
	// centred in the header band. Empty (or undecodable) falls back to the
	// OrgName wordmark, and with neither the band collapses entirely.
	OrgLogo []byte

	// PosterImage is the event poster image bytes (PNG or JPEG), drawn at the
	// left of the date/venue block. Empty (or undecodable) means the block
	// spans the full page width — no placeholder, no reserved gap.
	PosterImage []byte

	// EAN13 is the platform-minted EAN-13 barcode number (the stored
	// ticket_credentials payload, minted via internal/platform/barcodes/mint).
	// It is the ONLY machine-readable value on the page: the QR encodes
	// exactly these digits and the barcode symbol encodes them again.
	//
	// Empty — or failing the GS1 check digit — draws NEITHER code and leaves
	// the rest of the page intact. That is the honest outcome for a legacy
	// ticket issued before the EAN-13 work and not yet caught up by the
	// tickets.backfill_ean13 job: such a ticket has nothing a gate scanner
	// could resolve, and a fake mark would be worse than none.
	EAN13 string

	// FinePrint replaces the footer's closing note. Empty uses the localized
	// default (defaultNoteFor: the ported "show the barcode / do not share"
	// note plus the "not a fiscal receipt" disclosure).
	FinePrint string

	// ── Organisation branding ─────────────────────────────────────────
	// All optional; empty fields are silently skipped so the renderer never
	// refuses to print a ticket because branding metadata is missing.

	// OrgName is the organizer's public display name, printed in the footer
	// ("Organizer: ...") and used as the header wordmark when there is no
	// logo image.
	OrgName string
	// OrgWebsiteURL is appended to the footer's organizer line when set.
	OrgWebsiteURL string

	// LegalName, LegalAddress* and ContactEmail compose the EU "commercial
	// communications" minimum-identification block in the footer. Empty
	// fields drop their line; all empty drops the block.
	LegalName              string
	LegalAddressLine1      string
	LegalAddressLine2      string
	LegalAddressPostalCode string
	LegalAddressCity       string
	LegalAddressCountry    string
	ContactEmail           string
}

// ErrInvalidTicket is returned by Render when the Ticket is missing data
// required to produce a usable page.
var ErrInvalidTicket = errors.New("pdf: ticket missing required fields")

// ErrUnknownFormat is returned by RenderFormat for a Format value other than
// FormatMobile or FormatA4Print.
var ErrUnknownFormat = errors.New("pdf: unknown render format")

// Render returns the bytes of a single-page e-ticket PDF. See RenderFormat.
func Render(ctx context.Context, ticket Ticket) ([]byte, error) {
	return RenderFormat(ctx, ticket, FormatMobile)
}

// RenderFormat renders the ticket in the requested layout. The function is
// pure: it performs no network or filesystem IO and holds no global state.
//
// It is also byte-deterministic — the same (ticket, format) pair renders to
// the same bytes — with ONE known exception: when a ticket carries a logo
// and a poster whose source images have the SAME pixel width, the two image
// objects can swap places in the output. gofpdf's catalog-sort orders image
// objects by pixel width (putimages) and breaks a tie with Go's randomized
// map iteration. The two documents are identical in content and in every
// drawn position; only the order of two objects inside the file differs. Do
// not build a content hash over the raw bytes without accounting for it.
//
// ctx is honoured for cancellation only — it is checked once before the
// (cheap) layout pass starts so callers can abort a batch render before this
// ticket's work begins.
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

// pinnedTimestamp is the fixed sentinel stamped into every render as the PDF
// creation/modification date. gofpdf substitutes time.Now() for a zero time,
// which would break the byte-determinism contract.
func pinnedTimestamp() time.Time {
	return time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
}

// validate enforces the must-have fields for a sane render.
//
// Everything else — venue, category, holder, price, order number, poster,
// logo, and even the EAN-13 itself — is optional and simply disappears when
// absent. Refusing to issue a ticket because a category name is blank would
// be worse than printing the ticket without it.
func validate(t Ticket) error {
	switch {
	case strings.TrimSpace(t.TicketID) == "":
		return fmt.Errorf("%w: ticket_id", ErrInvalidTicket)
	case strings.TrimSpace(t.EventName) == "":
		return fmt.Errorf("%w: event_name", ErrInvalidTicket)
	case t.SessionStart.IsZero():
		return fmt.Errorf("%w: session_start", ErrInvalidTicket)
	}
	return nil
}

// buildLegalLines composes the footer legal-identification block from the
// branding fields on the Ticket. Returns an empty slice when none of the
// legal fields are populated, in which case the renderer omits the block
// (the closing note still prints).
//
// contactLabel is the localized "Contact" word, resolved once by the caller
// from the ticket's locale so every line agrees.
//
// Lines: LegalName, address-line1, address-line2, "<postal> <city>",
// country, "<contactLabel>: <email>".
func buildLegalLines(t Ticket, contactLabel string) []string {
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
		out = append(out, contactLabel+": "+v)
	}
	return out
}

// seatValue composes the Seat info cell's value from the denormalized seat
// coordinates, using the locale's own words for "row" and "seat" the way the
// ported design does ("A, row 3, seat 12" / "A, ряд 3, место 12").
//
// Unlike the pre-port layout — which drew three separate rows and required
// ALL THREE coordinates to be present before drawing any of them — each part
// is independent: a ticket that knows only its row still says so.
func seatValue(t Ticket, s ticketStrings) string {
	parts := make([]string, 0, 3)
	if v := strings.TrimSpace(t.SeatSector); v != "" {
		parts = append(parts, v)
	}
	if v := strings.TrimSpace(t.SeatRow); v != "" {
		parts = append(parts, s.RowWord+" "+v)
	}
	if v := strings.TrimSpace(t.SeatNumber); v != "" {
		parts = append(parts, s.SeatWord+" "+v)
	}
	return strings.Join(parts, ", ")
}

// priceValue renders the Price info cell's value, or "" to drop the cell.
//
// PriceLabel wins outright (the complimentary-ticket override). Otherwise a
// nil PriceMinor means "unknown" and a zero one means "free" — neither
// prints, because "0 EUR" on a ticket reads as a pricing bug rather than as
// a gift.
func priceValue(t Ticket) string {
	if v := strings.TrimSpace(t.PriceLabel); v != "" {
		return v
	}
	if t.PriceMinor == nil || *t.PriceMinor == 0 {
		return ""
	}
	return formatMoneyMinor(*t.PriceMinor, t.Currency)
}

// formatMoneyMinor formats a minor-unit amount the way the ported design
// does: no decimals at all when the amount is whole, exactly two otherwise,
// thousands separated by a space, then the currency code.
//
//	60000, "CZK"  -> "600 CZK"
//	 1895, "EUR"  -> "18.95 EUR"
//	150000, "CZK" -> "1 500 CZK"
func formatMoneyMinor(minor int64, currency string) string {
	sign := ""
	if minor < 0 {
		sign = "-"
		minor = -minor
	}
	units, cents := minor/100, minor%100
	out := sign + groupThousands(units)
	if cents != 0 {
		out += fmt.Sprintf(".%02d", cents)
	}
	if c := strings.TrimSpace(currency); c != "" {
		out += " " + c
	}
	return out
}

// groupThousands renders a non-negative integer with a space every three
// digits ("1 500").
func groupThousands(v int64) string {
	digits := strconv.FormatInt(v, 10)
	if len(digits) <= 3 {
		return digits
	}
	var b strings.Builder
	lead := len(digits) % 3
	if lead == 0 {
		lead = 3
	}
	b.WriteString(digits[:lead])
	for i := lead; i < len(digits); i += 3 {
		b.WriteByte(' ')
		b.WriteString(digits[i : i+3])
	}
	return b.String()
}

// displayTicketNumber returns the number printed on the page (and used as
// the PDF document title) for t. Never the raw UUID.
func displayTicketNumber(t Ticket) string {
	return DisplayNumber(t.TicketNumber, t.TicketID)
}

// DisplayNumber is the buyer-facing ticket number rule, exported so the
// e-mail body and the PDF can never disagree about what the buyer is told
// their ticket number is: the resolved ticketNumber (tickets.system_ticket_id)
// when there is one, otherwise a short reference derived from ticketID. The
// full UUID is never the answer.
func DisplayNumber(ticketNumber, ticketID string) string {
	if n := strings.TrimSpace(ticketNumber); n != "" {
		return n
	}
	return shortTicketRef(ticketID)
}

// shortTicketRefLen is how many hex digits of the ticket UUID the
// no-system-id fallback keeps. Eight is short enough to read aloud and wide
// enough (4.3e9 values) that two tickets of one event colliding is not a
// practical concern — and it is a fallback for legacy rows only: every
// ticket issued since migration 0088 has a system_ticket_id.
const shortTicketRefLen = 8

// shortTicketRef condenses a ticket UUID into a short, human-quotable
// reference — its first shortTicketRefLen hex digits, uppercased, dashes
// removed. The result is deliberately NOT the UUID.
func shortTicketRef(id string) string {
	hex := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(id), "-", ""))
	if len(hex) > shortTicketRefLen {
		hex = hex[:shortTicketRefLen]
	}
	return hex
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

// runeLen is the character (not byte) length used by every font-shrink
// threshold in the layout. The ported original measured with mb_strlen for
// the same reason: "Концерт" is 7 characters and 14 bytes, and a byte-length
// budget would shrink every Cyrillic title for no reason.
func runeLen(s string) int { return utf8.RuneCountInString(s) }

// rgb is a parsed 8-bit colour.
type rgb struct{ r, g, b int }

// parseHexColor parses a CSS-style hex colour ("#4f46e5", "4f46e5", "#abc").
// Anything unparseable returns fallback — a bad accent value must never stop
// a ticket from printing.
func parseHexColor(s string, fallback rgb) rgb {
	h := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "#"))
	if len(h) == 3 {
		h = string([]byte{h[0], h[0], h[1], h[1], h[2], h[2]})
	}
	if len(h) != 6 {
		return fallback
	}
	v, err := strconv.ParseUint(h, 16, 32)
	if err != nil {
		return fallback
	}
	return rgb{r: int(v>>16) & 0xFF, g: int(v>>8) & 0xFF, b: int(v) & 0xFF}
}

// accentColor resolves the ticket's accent, falling back to
// DefaultAccentColor.
func accentColor(t Ticket) rgb {
	return parseHexColor(t.AccentColor, parseHexColor(DefaultAccentColor, rgb{}))
}

// imageInfo is what the layout needs to know about an in-memory image before
// it can place it: gofpdf's RegisterImageOptionsReader must be told the
// format explicitly, and the layout needs the pixel aspect ratio to fit the
// image inside its box without distorting it.
type imageInfo struct {
	typ  string // "PNG" or "JPEG", as gofpdf's ImageOptions.ImageType wants it
	w, h int    // pixel dimensions
}

// decodeImageInfo peeks at an in-memory image. ok is false for anything that
// is not a decodable PNG or JPEG, which every call site treats as "there is
// no image" rather than as an error.
func decodeImageInfo(b []byte) (imageInfo, bool) {
	if len(b) < 8 {
		return imageInfo{}, false
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return imageInfo{}, false
	}
	switch format {
	case "png":
		return imageInfo{typ: "PNG", w: cfg.Width, h: cfg.Height}, true
	case "jpeg":
		return imageInfo{typ: "JPEG", w: cfg.Width, h: cfg.Height}, true
	}
	return imageInfo{}, false
}

// SupportsImage reports whether b is an image this renderer can actually
// draw — a decodable PNG or JPEG.
//
// It exists so a caller that FETCHES artwork (the delivery worker pulling
// an event poster out of the media store) can ask the renderer itself
// instead of trusting a media_objects.content_type column or duplicating
// the format list. A false answer means "there is no image here", never an
// error: OrgLogo and PosterImage are both optional, and the page prints
// without them.
func SupportsImage(b []byte) bool {
	_, ok := decodeImageInfo(b)
	return ok
}

// fitBox scales a (w,h) pixel image into a boxW x boxH box, preserving the
// aspect ratio (letterboxing rather than cropping). The ported WordPress
// renderer centre-CROPS the poster to a fixed 320:335 ratio before handing it
// to mPDF; this package has no image pipeline and will not re-encode
// organizer artwork, so it fits inside the same box instead — the poster
// column's width is what the layout depends on, and a letterboxed poster is
// honest where a stretched one is not.
func fitBox(imgW, imgH int, boxW, boxH float64) (w, h float64) {
	if imgW <= 0 || imgH <= 0 {
		return 0, 0
	}
	scale := boxW / float64(imgW)
	if s := boxH / float64(imgH); s < scale {
		scale = s
	}
	return float64(imgW) * scale, float64(imgH) * scale
}
