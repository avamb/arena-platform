package delivery

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
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

func int64Ptr(v int64) *int64 { return &v }

// posterRowMediaID is the poster id fullPresentationRow resolves to — the
// value COALESCE(sessions.poster_media_id, events.poster_media_id) would
// have produced.
var posterRowMediaID = uuid.MustParse("8f14e45f-ea72-4f0b-9a33-3f0a7c2dd001")

func fullPresentationRow() gen.TicketPresentationRow {
	start := time.Date(2026, 10, 3, 19, 0, 0, 0, time.UTC)
	poster := posterRowMediaID
	return gen.TicketPresentationRow{
		SystemTicketID: 1000000517,
		OrderNumber:    int64Ptr(1000000499),
		EventName:      strPtr("Resolved Event"),
		SessionStartAt: &start,
		VenueName:      strPtr("Resolved Venue"),
		VenueAddress:   strPtr("Estonia pst 4"),
		VenueCity:      strPtr("Tallinn"),
		VenueTimezone:  strPtr("Europe/Tallinn"),
		TierName:       strPtr("Resolved Category"),
		HolderName:     strPtr("Resolved Holder"),
		PriceMinor:     int64Ptr(1890),
		PriceCurrency:  strPtr("EUR"),
		PosterMediaID:  &poster,
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
		"event_name":      {p.EventName, "Resolved Event"},
		"venue_name":      {p.VenueName, "Resolved Venue"},
		"venue_address":   {p.VenueAddress, "Estonia pst 4"},
		"venue_city":      {p.VenueCity, "Tallinn"},
		"session_tz":      {p.SessionTZ, "Europe/Tallinn"},
		"tier_name":       {p.TierName, "Resolved Category"},
		"holder_name":     {p.HolderName, "Resolved Holder"},
		"ticket_number":   {p.TicketNumber, "1000000517"},
		"order_number":    {p.OrderNumber, "1000000499"},
		"currency":        {p.Currency, "EUR"},
		"poster_media_id": {p.PosterMediaID, posterRowMediaID.String()},
	}
	for field, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %q; want %q", field, c.got, c.want)
		}
	}
	if p.PriceMinor == nil || *p.PriceMinor != 1890 {
		t.Errorf("price_minor = %v; want 1890", p.PriceMinor)
	}
	if want := time.Date(2026, 10, 3, 19, 0, 0, 0, time.UTC); !p.SessionStart.Equal(want) {
		t.Errorf("session_start = %v; want %v", p.SessionStart, want)
	}
}

// TestApplyPresentation_ComplimentaryLeavesThePriceUnset — an invitation
// discounts its whole subtotal, so every order item totals 0. The ticket
// must carry NO price at all: "0 EUR" printed on a gift reads as a
// pricing bug, and the renderer drops the cell only when the amount is
// unset or zero.
func TestApplyPresentation_ComplimentaryLeavesThePriceUnset(t *testing.T) {
	row := fullPresentationRow()
	row.PriceMinor = int64Ptr(0)

	p := Payload{TicketID: uuid.New().String(), Template: TemplateInvitation}
	applyPresentation(&p, row)

	if p.PriceMinor != nil {
		t.Errorf("price_minor = %d; want nil for a zero-total (complimentary) ticket", *p.PriceMinor)
	}
	// Everything else still resolves — an invitation is a real ticket.
	if p.OrderNumber != "1000000499" || p.EventName != "Resolved Event" {
		t.Errorf("a complimentary ticket must still resolve the rest: %+v", p)
	}
}

// TestApplyPresentation_TicketWithoutAnOrderHasNoPriceOrOrderNumber — a
// legacy ticket, or one issued by the admin complimentary path, has no
// orders row behind it at all. Every order-derived column is NULL and the
// page simply omits those lines.
func TestApplyPresentation_TicketWithoutAnOrderHasNoPriceOrOrderNumber(t *testing.T) {
	p := Payload{TicketID: uuid.New().String()}
	applyPresentation(&p, gen.TicketPresentationRow{
		SystemTicketID: 5150,
		EventName:      strPtr("Orderless Event"),
	})

	if p.OrderNumber != "" {
		t.Errorf("order_number = %q; want empty", p.OrderNumber)
	}
	if p.PriceMinor != nil || p.Currency != "" {
		t.Errorf("price must stay unset: minor=%v currency=%q", p.PriceMinor, p.Currency)
	}
	if p.PosterMediaID != "" {
		t.Errorf("poster_media_id = %q; want empty", p.PosterMediaID)
	}
}

// TestResolvePresentation_PayloadHintWins protects every existing
// enqueuer and test that DOES populate the hints: a resolved value may
// only fill a gap, never overwrite.
func TestResolvePresentation_PayloadHintWins(t *testing.T) {
	q := &fakePresentationQuerier{row: fullPresentationRow()}
	hinted := time.Date(2030, 1, 2, 3, 4, 0, 0, time.UTC)
	p := fullyHintedPayload(hinted)

	resolvePresentation(context.Background(), q, uuid.New(), &p, quietLogger())

	if q.calls != 0 {
		t.Errorf("a fully hinted payload must not query at all; got %d calls", q.calls)
	}
	if p.EventName != "Hinted Event" || p.VenueName != "Hinted Venue" ||
		p.VenueAddress != "Hinted Street 1" ||
		p.VenueCity != "Prague" || p.SessionTZ != "Europe/Prague" ||
		p.TierName != "Hinted Category" || p.HolderName != "Hinted Holder" ||
		p.TicketNumber != "42" || p.OrderNumber != "4242" ||
		p.PriceMinor == nil || *p.PriceMinor != 60000 || p.Currency != "CZK" ||
		p.PosterMediaID != hintedPosterMediaID ||
		!p.SessionStart.Equal(hinted) {
		t.Errorf("resolution overwrote a payload hint: %+v", p)
	}
}

// hintedPosterMediaID is a media id no fixture row resolves to, so a test
// asserting it survived is really asserting the hint won.
const hintedPosterMediaID = "0f3b6d2e-1111-4c3a-9b22-aaaaaaaaaaaa"

// fullyHintedPayload is a payload in which an enqueuer has populated
// EVERY field resolvePresentation could fill — the case that must skip
// the lookup entirely.
func fullyHintedPayload(start time.Time) Payload {
	return Payload{
		TicketID:      uuid.New().String(),
		EventName:     "Hinted Event",
		SessionStart:  start,
		SessionTZ:     "Europe/Prague",
		VenueName:     "Hinted Venue",
		VenueAddress:  "Hinted Street 1",
		VenueCity:     "Prague",
		TierName:      "Hinted Category",
		HolderName:    "Hinted Holder",
		TicketNumber:  "42",
		OrderNumber:   "4242",
		PriceMinor:    int64Ptr(60000),
		Currency:      "CZK",
		PosterMediaID: hintedPosterMediaID,
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

// ─────────────────────────────────────────────────────────────────────────────
// Poster bytes
// ─────────────────────────────────────────────────────────────────────────────

// recordingMediaResolver answers with a canned payload and remembers what
// it was asked for — including whether the caller bounded the fetch.
type recordingMediaResolver struct {
	bytes []byte
	err   error

	calls       int
	lastID      string
	hadDeadline bool
}

func (r *recordingMediaResolver) ResolveLogo(ctx context.Context, mediaID string) ([]byte, string, error) {
	r.calls++
	r.lastID = mediaID
	_, r.hadDeadline = ctx.Deadline()
	return r.bytes, "", r.err
}

// tinyPNG encodes a w×h opaque PNG. Distinct sizes matter: gofpdf keys its
// image resources by content, so two images of identical pixel dimensions
// in one document make the output non-deterministic (AGENTS.md).
func tinyPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		for y := 0; y < h; y++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 7 % 256), G: uint8(y * 11 % 256), B: 0x80, A: 0xFF})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode test png: %v", err)
	}
	return buf.Bytes()
}

// TestResolvePoster_ReturnsTheArtworkAndBoundsTheFetch is the happy path:
// the bytes reach the renderer, the media store is asked for exactly the
// resolved id, and the call carries a deadline of its own.
func TestResolvePoster_ReturnsTheArtworkAndBoundsTheFetch(t *testing.T) {
	art := tinyPNG(t, 24, 31)
	r := &recordingMediaResolver{bytes: art}

	got := resolvePoster(context.Background(), r, posterRowMediaID.String(), quietLogger())

	if !bytes.Equal(got, art) {
		t.Errorf("resolvePoster returned %d bytes; want the %d-byte poster", len(got), len(art))
	}
	if r.calls != 1 || r.lastID != posterRowMediaID.String() {
		t.Errorf("media store asked %d times for %q", r.calls, r.lastID)
	}
	if !r.hadDeadline {
		t.Error("the poster fetch must be bounded in time; the context carried no deadline")
	}
}

// TestResolvePoster_DegradesToNoPoster covers every way the artwork can
// fail to arrive. None of them may produce an error: the ticket ships
// without a poster instead.
func TestResolvePoster_DegradesToNoPoster(t *testing.T) {
	oversized := make([]byte, maxPosterBytes+1)
	copy(oversized, tinyPNG(t, 8, 9))

	cases := map[string]struct {
		resolver  *recordingMediaResolver
		mediaID   string
		wantCalls int
	}{
		"no poster on the event": {&recordingMediaResolver{bytes: tinyPNG(t, 8, 9)}, "", 0},
		"media row vanished":     {&recordingMediaResolver{err: ErrLogoNotFound}, posterRowMediaID.String(), 1},
		"media store outage":     {&recordingMediaResolver{err: errors.New("s3: connection refused")}, posterRowMediaID.String(), 1},
		"empty object":           {&recordingMediaResolver{}, posterRowMediaID.String(), 1},
		"not an image":           {&recordingMediaResolver{bytes: []byte("%PDF-1.4\nnot a poster at all")}, posterRowMediaID.String(), 1},
		"svg poster":             {&recordingMediaResolver{bytes: []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`)}, posterRowMediaID.String(), 1},
		"print master":           {&recordingMediaResolver{bytes: oversized}, posterRowMediaID.String(), 1},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := resolvePoster(context.Background(), c.resolver, c.mediaID, quietLogger()); got != nil {
				t.Errorf("want no poster, got %d bytes", len(got))
			}
			if c.resolver.calls != c.wantCalls {
				t.Errorf("media store calls = %d; want %d", c.resolver.calls, c.wantCalls)
			}
		})
	}
}

// TestResolvePoster_NoMediaResolverWired — arena-worker builds the
// delivery handler without a MediaResolver today, which must degrade to a
// poster-less ticket rather than panic.
func TestResolvePoster_NoMediaResolverWired(t *testing.T) {
	if got := resolvePoster(context.Background(), nil, posterRowMediaID.String(), quietLogger()); got != nil {
		t.Errorf("want no poster without a media resolver, got %d bytes", len(got))
	}
}

// TestNeedsPresentation_SkipsWhenEverythingIsHinted keeps the query off
// the hot path for a caller that already knows everything.
func TestNeedsPresentation_SkipsWhenEverythingIsHinted(t *testing.T) {
	full := fullyHintedPayload(time.Now())
	if needsPresentation(full) {
		t.Error("a fully populated payload should not need resolution")
	}
	for name, clear := range map[string]func(*Payload){
		"category":     func(p *Payload) { p.TierName = "" },
		"order number": func(p *Payload) { p.OrderNumber = "" },
		"address":      func(p *Payload) { p.VenueAddress = "" },
		"price":        func(p *Payload) { p.PriceMinor = nil },
		"poster":       func(p *Payload) { p.PosterMediaID = "" },
	} {
		t.Run(name, func(t *testing.T) {
			missing := full
			clear(&missing)
			if !needsPresentation(missing) {
				t.Errorf("a payload missing the %s should need resolution", name)
			}
		})
	}
}
