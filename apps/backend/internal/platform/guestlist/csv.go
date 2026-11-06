// Package guestlist is the organizer's guest list of a session (owner
// decision 2026-11-02, spec 51 section 4.4, migration 0187).
//
// The list holds the session's ACTIVE tickets, one row per ticket, sorted by
// the guest's name: guest, category, seat, ticket number, barcode, VIP,
// entered and entry time, order, e-mail, phone, invitation. The rows are built
// here once (Cells) and written as an Excel workbook (.xlsx, what people get)
// or a CSV (for imports). It is served two ways: on demand by
// GET /v1/organizations/{org_id}/sessions/{session_id}/guest-list.xlsx (the
// bot button and the web event center; guest-list.csv is the import file),
// and automatically by the
// guest_list.schedule job 24 hours and 3 hours before the start and at doors
// open, to every owner and manager who ticked "send me the guest list"
// (guest_list_subscriptions). The automatic list goes to the person's private
// chat with the event center bot, or by e-mail to a person who is not linked
// to the bot. It never goes to a group chat: the file holds buyers' names,
// e-mails and phones.
package guestlist

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/barcodes/ean13"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/csvexport"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/geoslug"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/xlsxexport"
)

// MaxRows is the largest list built; a session with more active tickets is
// refused (the file would not be read on a phone, and the cap keeps one
// request from reading a whole hall twice).
const MaxRows = 50_000

// ErrTooManyRows is returned when the session has more than MaxRows tickets.
var ErrTooManyRows = errors.New("guestlist: too many rows")

// Columns are the columns of the guest list, in the owner's order (owner
// decision 2026-11-02: "entry plus contacts").
var Columns = []csvexport.Column{
	csvexport.ColGuest, csvexport.ColCategory, csvexport.ColSeat,
	csvexport.ColTicket, csvexport.ColBarcode, csvexport.ColVIP,
	csvexport.ColEntered, csvexport.ColEnteredAt,
	csvexport.ColOrder, csvexport.ColEmail, csvexport.ColPhone,
	csvexport.ColInvitation, csvexport.ColTest,
}

// Querier is the part of gen.Queries the list reads.
type Querier interface {
	CountGuestListRows(ctx context.Context, sessionID, orgID uuid.UUID) (int64, error)
	ListGuestListRows(ctx context.Context, sessionID, orgID uuid.UUID, limit int32) ([]gen.GuestListRow, error)
}

// Format is the file format of a list.
type Format string

// The two formats. XLSX is what people get (the bot, the letter, the event
// center's main button): Excel opens it the same way whatever its regional
// settings. CSV stays for imports into other systems.
const (
	XLSX Format = "xlsx"
	CSV  Format = "csv"
)

// ContentType is the media type of a file in this format.
func (f Format) ContentType() string {
	if f == CSV {
		return "text/csv; charset=utf-8"
	}
	return xlsxexport.ContentType
}

// File is a built guest list.
type File struct {
	Body        []byte
	Rows        int
	ContentType string
}

// textColumns are the columns an XLSX keeps as text: identifiers whose
// digits a spreadsheet would otherwise turn into a number (a 13-digit barcode
// into 4.6E+12, a phone's leading zero dropped).
var textColumns = map[csvexport.Column]bool{
	csvexport.ColTicket: true, csvexport.ColBarcode: true,
	csvexport.ColOrder: true, csvexport.ColPhone: true,
}

// Build reads the active tickets of a session of the organization and writes
// the file in format with headers in locale. loc is the venue's zone for the
// entry time. A session with more than maxRows tickets is ErrTooManyRows;
// maxRows <= 0 means MaxRows. Both formats are written from the same Cells.
func Build(ctx context.Context, q Querier, orgID, sessionID uuid.UUID, locale string, loc *time.Location, maxRows int64, format Format) (File, error) {
	if maxRows <= 0 {
		maxRows = MaxRows
	}
	n, err := q.CountGuestListRows(ctx, sessionID, orgID)
	if err != nil {
		return File{}, err
	}
	if n > maxRows {
		return File{}, ErrTooManyRows
	}
	rows, err := q.ListGuestListRows(ctx, sessionID, orgID, int32(min(maxRows, MaxRows))) //nolint:gosec // bounded by MaxRows
	if err != nil {
		return File{}, err
	}
	header := csvexport.Header(locale, Columns...)
	var body []byte
	if format == CSV {
		body, err = buildCSV(header, rows, loc)
	} else {
		body, err = buildXLSX(locale, header, rows, loc)
	}
	if err != nil {
		return File{}, err
	}
	return File{Body: body, Rows: len(rows), ContentType: format.ContentType()}, nil
}

func buildCSV(header []string, rows []gen.GuestListRow, loc *time.Location) ([]byte, error) {
	var buf bytes.Buffer
	w := csvexport.NewWriter(&buf)
	if err := w.WriteHeader(header); err != nil {
		return nil, err
	}
	for _, r := range rows {
		if err := w.WriteRow(Cells(r, loc)...); err != nil {
			return nil, err
		}
	}
	if err := w.Flush(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func buildXLSX(locale string, header []string, rows []gen.GuestListRow, loc *time.Location) ([]byte, error) {
	cols := make([]xlsxexport.Column, len(Columns))
	for i, c := range Columns {
		cols[i] = xlsxexport.Column{Header: header[i], Text: textColumns[c]}
	}
	cells := make([][]csvexport.Cell, len(rows))
	for i, r := range rows {
		cells[i] = Cells(r, loc)
	}
	return xlsxexport.Build(xlsxexport.Sheet{Name: SheetName(locale), Columns: cols, Rows: cells})
}

// Cells renders one ticket. The barcode is the stored EAN-13 credential; a
// ticket that predates it gets the retired PlatformCode derived from its
// number, exactly as the sales export does.
func Cells(r gen.GuestListRow, loc *time.Location) []csvexport.Cell {
	barcode := ean13.PlatformCode(r.SystemTicketID)
	if r.Barcode != nil && *r.Barcode != "" {
		barcode = *r.Barcode
	}
	var order csvexport.Cell
	if r.OrderNumber != nil {
		order = csvexport.Number(*r.OrderNumber)
	}
	return []csvexport.Cell{
		csvexport.Text(r.GuestName),
		csvexport.TextPtr(r.TierName),
		csvexport.Text(SeatLabel(r.SeatSector, r.SeatRow, r.SeatNumber)),
		csvexport.Number(r.SystemTicketID),
		csvexport.Text(barcode),
		csvexport.YesNo(r.VIP),
		csvexport.YesNo(r.EnteredAt != nil),
		csvexport.DateTimePtr(r.EnteredAt, loc),
		order,
		csvexport.TextPtr(r.Email),
		csvexport.TextPtr(r.Phone),
		csvexport.YesNo(r.Invitation),
		csvexport.YesNo(r.IsTest),
	}
}

// SeatLabel joins the seat parts that exist ("A / 3 / 12"); empty for a
// general-admission ticket.
func SeatLabel(sector, row, number *string) string {
	var parts []string
	for _, p := range []*string{sector, row, number} {
		if p != nil && strings.TrimSpace(*p) != "" {
			parts = append(parts, strings.TrimSpace(*p))
		}
	}
	return strings.Join(parts, " / ")
}

// Location resolves a venue's IANA zone; an unknown or empty zone is UTC.
func Location(tz string) *time.Location {
	if tz == "" {
		return time.UTC
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return time.UTC
	}
	return loc
}

// FileName is `guests_<event>_<YYYY-MM-DD>.<format>`: the event's name
// transliterated to a slug (geoslug: Cyrillic to Latin, accents folded), else
// its page slug, else "event"; the date is the session's day in the venue's
// zone. Only [a-z0-9-] survive, so the name needs no quoting anywhere.
func FileName(eventName, eventSlug string, start time.Time, loc *time.Location, format Format) string {
	s := safeSlug(geoslug.Slugify(eventName))
	if s == "" {
		s = safeSlug(eventSlug)
	}
	if s == "" {
		s = "event"
	}
	if loc == nil {
		loc = time.UTC
	}
	ext := "xlsx"
	if format == CSV {
		ext = "csv"
	}
	// allow:timeformat: a calendar date in a file name, in the venue's zone.
	return "guests_" + s + "_" + start.In(loc).Format("2006-01-02") + "." + ext
}

// safeSlug keeps [a-z0-9] and single dashes, at most 60 characters, cut at a
// dash when there is one.
func safeSlug(raw string) string {
	var b strings.Builder
	dash := true // no leading dash
	for _, r := range strings.ToLower(strings.TrimSpace(raw)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
			continue
		}
		if !dash {
			b.WriteByte('-')
			dash = true
		}
	}
	s := strings.Trim(b.String(), "-")
	if len(s) > 60 {
		s = s[:60]
		if i := strings.LastIndexByte(s, '-'); i > 20 {
			s = s[:i]
		}
	}
	return strings.Trim(s, "-")
}
