// Package guestlist is the organizer's guest list of a session (owner
// decision 2026-11-02, spec 51 section 4.4, migration 0188).
//
// The list is a CSV of the session's ACTIVE tickets, one row per ticket,
// sorted by the guest's name: guest, category, seat, ticket number, barcode,
// VIP, entered and entry time, order, e-mail, phone, invitation. It is built
// here once and served two ways: on demand by
// GET /v1/organizations/{org_id}/sessions/{session_id}/guest-list.csv (the bot
// button and the web event center), and automatically by the
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
	csvexport.ColInvitation,
}

// Querier is the part of gen.Queries the list reads.
type Querier interface {
	CountGuestListRows(ctx context.Context, sessionID, orgID uuid.UUID) (int64, error)
	ListGuestListRows(ctx context.Context, sessionID, orgID uuid.UUID, limit int32) ([]gen.GuestListRow, error)
}

// File is a built guest list.
type File struct {
	Body []byte
	Rows int
}

// Build reads the active tickets of a session of the organization and writes
// the file with headers in locale. loc is the venue's zone for the entry time.
// A session with more than maxRows tickets is ErrTooManyRows; maxRows <= 0
// means MaxRows.
func Build(ctx context.Context, q Querier, orgID, sessionID uuid.UUID, locale string, loc *time.Location, maxRows int64) (File, error) {
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
	var buf bytes.Buffer
	w := csvexport.NewWriter(&buf)
	if err := w.WriteHeader(csvexport.Header(locale, Columns...)); err != nil {
		return File{}, err
	}
	for _, r := range rows {
		if err := w.WriteRow(Cells(r, loc)...); err != nil {
			return File{}, err
		}
	}
	if err := w.Flush(); err != nil {
		return File{}, err
	}
	return File{Body: buf.Bytes(), Rows: len(rows)}, nil
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

// FileName is `guests_<slug>_<YYYY-MM-DD>.csv`, the date being the session's
// day in the venue's zone. Only [a-z0-9._-] survive.
func FileName(slug string, start time.Time, loc *time.Location) string {
	s := strings.ToLower(strings.TrimSpace(slug))
	var b strings.Builder
	dash := false
	for _, r := range s {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-'
		if ok {
			b.WriteRune(r)
			dash = false
			continue
		}
		if !dash {
			b.WriteByte('-')
			dash = true
		}
	}
	s = strings.Trim(b.String(), "-.")
	if s == "" {
		s = "session"
	}
	if len(s) > 80 {
		s = s[:80]
	}
	if loc == nil {
		loc = time.UTC
	}
	// allow:timeformat: a calendar date in a file name, in the venue's zone.
	return "guests_" + s + "_" + start.In(loc).Format("2006-01-02") + ".csv"
}
