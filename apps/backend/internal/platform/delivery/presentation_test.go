package delivery

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// presentation_test.go — the render-time resolution rules, without a
// database. The live-DB counterpart is presentation_integration_test.go.

func strPtr(s string) *string { return &s }

// fakePresentationQuerier records its calls and answers with a canned row
// or error.
type fakePresentationQuerier struct {
	row   gen.TicketPresentationRow
	err   error
	calls int
}

func (f *fakePresentationQuerier) GetTicketPresentationByID(context.Context, uuid.UUID) (gen.TicketPresentationRow, error) {
	f.calls++
	return f.row, f.err
}

func fullPresentationRow() gen.TicketPresentationRow {
	start := time.Date(2026, 10, 3, 19, 0, 0, 0, time.UTC)
	return gen.TicketPresentationRow{
		SystemTicketID: 1000000517,
		EventName:      strPtr("Resolved Event"),
		SessionStartAt: &start,
		VenueName:      strPtr("Resolved Venue"),
		VenueCity:      strPtr("Tallinn"),
		VenueTimezone:  strPtr("Europe/Tallinn"),
		TierName:       strPtr("Resolved Category"),
		HolderName:     strPtr("Resolved Holder"),
	}
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestResolvePresentation_FillsEveryEmptyField is the defect itself: the
// enqueuers send {ticket_id, locale} and nothing else, so everything the
// buyer reads has to come from this lookup.
func TestResolvePresentation_FillsEveryEmptyField(t *testing.T) {
	q := &fakePresentationQuerier{row: fullPresentationRow()}
	p := Payload{TicketID: uuid.New().String(), Locale: "en"}

	resolvePresentation(context.Background(), q, uuid.New(), &p, quietLogger())

	if q.calls != 1 {
		t.Fatalf("expected exactly one lookup, got %d", q.calls)
	}
	checks := map[string]struct{ got, want string }{
		"event_name":    {p.EventName, "Resolved Event"},
		"venue_name":    {p.VenueName, "Resolved Venue"},
		"venue_city":    {p.VenueCity, "Tallinn"},
		"session_tz":    {p.SessionTZ, "Europe/Tallinn"},
		"tier_name":     {p.TierName, "Resolved Category"},
		"holder_name":   {p.HolderName, "Resolved Holder"},
		"ticket_number": {p.TicketNumber, "1000000517"},
	}
	for field, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %q; want %q", field, c.got, c.want)
		}
	}
	if want := time.Date(2026, 10, 3, 19, 0, 0, 0, time.UTC); !p.SessionStart.Equal(want) {
		t.Errorf("session_start = %v; want %v", p.SessionStart, want)
	}
}

// TestResolvePresentation_PayloadHintWins protects every existing
// enqueuer and test that DOES populate the hints: a resolved value may
// only fill a gap, never overwrite.
func TestResolvePresentation_PayloadHintWins(t *testing.T) {
	q := &fakePresentationQuerier{row: fullPresentationRow()}
	hinted := time.Date(2030, 1, 2, 3, 4, 0, 0, time.UTC)
	p := Payload{
		TicketID:     uuid.New().String(),
		EventName:    "Hinted Event",
		SessionStart: hinted,
		SessionTZ:    "Europe/Prague",
		VenueName:    "Hinted Venue",
		VenueCity:    "Prague",
		TierName:     "Hinted Category",
		HolderName:   "Hinted Holder",
		TicketNumber: "42",
	}

	resolvePresentation(context.Background(), q, uuid.New(), &p, quietLogger())

	if q.calls != 0 {
		t.Errorf("a fully hinted payload must not query at all; got %d calls", q.calls)
	}
	if p.EventName != "Hinted Event" || p.VenueName != "Hinted Venue" ||
		p.VenueCity != "Prague" || p.SessionTZ != "Europe/Prague" ||
		p.TierName != "Hinted Category" || p.HolderName != "Hinted Holder" ||
		p.TicketNumber != "42" || !p.SessionStart.Equal(hinted) {
		t.Errorf("resolution overwrote a payload hint: %+v", p)
	}
}

// TestResolvePresentation_PartialHintsKeepTheirValues covers the mixed
// case — one hint set, the rest resolved.
func TestResolvePresentation_PartialHintsKeepTheirValues(t *testing.T) {
	q := &fakePresentationQuerier{row: fullPresentationRow()}
	p := Payload{TicketID: uuid.New().String(), EventName: "Hinted Event"}

	resolvePresentation(context.Background(), q, uuid.New(), &p, quietLogger())

	if q.calls != 1 {
		t.Fatalf("expected one lookup, got %d", q.calls)
	}
	if p.EventName != "Hinted Event" {
		t.Errorf("event_name = %q; want the hint to survive", p.EventName)
	}
	if p.VenueName != "Resolved Venue" {
		t.Errorf("venue_name = %q; want the resolved value", p.VenueName)
	}
}

// TestResolvePresentation_LookupFailureIsNonFatal — a missing venue must
// never hold up a ticket e-mail.
func TestResolvePresentation_LookupFailureIsNonFatal(t *testing.T) {
	for name, err := range map[string]error{
		"no rows":       pgx.ErrNoRows,
		"database down": errors.New("connection refused"),
	} {
		t.Run(name, func(t *testing.T) {
			q := &fakePresentationQuerier{err: err}
			p := Payload{TicketID: uuid.New().String()}
			resolvePresentation(context.Background(), q, uuid.New(), &p, quietLogger())
			if p.EventName != "" || p.VenueName != "" || p.TicketNumber != "" {
				t.Errorf("a failed lookup must leave the payload untouched: %+v", p)
			}
		})
	}
}

// TestApplyPresentation_NullColumnsAreSkipped — a GA ticket has no tier,
// a venue may have no city or timezone, a gateway order no buyer name.
func TestApplyPresentation_NullColumnsAreSkipped(t *testing.T) {
	p := Payload{TicketID: uuid.New().String()}
	applyPresentation(&p, gen.TicketPresentationRow{
		SystemTicketID: 7,
		EventName:      strPtr("Only The Event"),
	})

	if p.EventName != "Only The Event" {
		t.Errorf("event_name = %q", p.EventName)
	}
	if p.TicketNumber != "7" {
		t.Errorf("ticket_number = %q; want %q", p.TicketNumber, "7")
	}
	if p.VenueName != "" || p.VenueCity != "" || p.SessionTZ != "" ||
		p.TierName != "" || p.HolderName != "" || !p.SessionStart.IsZero() {
		t.Errorf("NULL columns must stay empty, not become %+v", p)
	}
}

// TestApplyPresentation_BlankStringsAreTreatedAsNull — an orders row with
// a whitespace-only buyer_name must not print a blank Holder row's worth
// of spaces.
func TestApplyPresentation_BlankStringsAreTreatedAsNull(t *testing.T) {
	p := Payload{TicketID: uuid.New().String()}
	applyPresentation(&p, gen.TicketPresentationRow{HolderName: strPtr("   ")})
	if p.HolderName != "" {
		t.Errorf("holder_name = %q; want empty", p.HolderName)
	}
}

// TestNeedsPresentation_SkipsWhenEverythingIsHinted keeps the query off
// the hot path for a caller that already knows everything.
func TestNeedsPresentation_SkipsWhenEverythingIsHinted(t *testing.T) {
	full := Payload{
		EventName:    "E",
		SessionStart: time.Now(),
		SessionTZ:    "UTC",
		VenueName:    "V",
		VenueCity:    "C",
		TierName:     "T",
		HolderName:   "H",
		TicketNumber: "1",
	}
	if needsPresentation(full) {
		t.Error("a fully populated payload should not need resolution")
	}
	missing := full
	missing.TierName = ""
	if !needsPresentation(missing) {
		t.Error("a payload missing the category should need resolution")
	}
}
