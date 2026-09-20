//go:build integration

// presentation_integration_test.go — live-DB proof that a ticket.deliver
// job carrying NOTHING but {ticket_id, locale} (which is exactly what
// every production enqueuer sends) still renders the real event name,
// venue, category, holder and the venue-local session time, and that the
// buyer's PDF never contains the internal ticket UUID.
//
// Found in production 2026-09-20 on the first live client: the PDF title
// read "Arena Event", Venue / Category / Holder printed as bare labels
// with nothing after them, the session time was UTC, and the ticket
// number line was the raw UUID.
//
// Unlike delivery_integration_test.go this file does NOT use
// internal/tests/pgtest (testcontainers, which cannot start on the
// Windows dev host). It talks to whatever DATABASE_URL names and skips
// when that is unset, the same pattern the gen package's live-DB tests
// use. Point it at a FRESH scratch database (AGENTS.md CI-Integration
// recipe), never at the shared dev stand.
package delivery

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// ─────────────────────────────────────────────────────────────────────────────
// Fixture
// ─────────────────────────────────────────────────────────────────────────────

// presentationSeed holds the ids and the values the rendered artifacts are
// asserted against.
type presentationSeed struct {
	TicketID       uuid.UUID
	DeliveryJobID  uuid.UUID
	OrderID        uuid.UUID
	SessionID      uuid.UUID
	EventID        uuid.UUID
	RecipientEmail string

	EventName    string
	VenueName    string
	VenueAddress string
	VenueCity    string
	VenueTZ      string
	TierName     string
	HolderName   string
	SessionStart time.Time
	SystemID     int64
	// OrderSystemID is orders.system_id — the "Order <n>" line in the PDF
	// footer.
	OrderSystemID int64
	// PriceMinor is the order item's total: what the buyer paid for THIS
	// ticket, in minor units, after its share of the order discount and of
	// the service charge.
	PriceMinor int64
	Currency   string
	// EAN13 is the ticket's barcode credential — present on every ticket
	// issued since feature #502, and what makes the layout draw its QR.
	EAN13 string
	// EventPosterID / SessionPosterID are media_objects rows the fixture
	// creates but does NOT attach; the individual tests attach whichever
	// one they are about, so the default seed has no poster at all.
	EventPosterID   uuid.UUID
	SessionPosterID uuid.UUID
}

// tallinnCitySlug is seeded by migration 0006_geo together with its
// English i18n_text row, so the fixture can rely on it existing in a
// freshly migrated database without inserting geo rows of its own.
const tallinnCitySlug = "tallinn"

// seedPresentationTicket builds the full FK chain a real purchase
// produces — organization → event → venue(city, timezone) → session →
// channel → reservation → checkout session → customer → order → tier →
// ticket → delivery job — with distinctive values so an assertion cannot
// accidentally pass on a fallback string.
//
// The returned cleanup deletes the rows in FK order; every test defers it
// so a run against a shared database leaves nothing behind.
func seedPresentationTicket(ctx context.Context, t *testing.T, pool *pgxpool.Pool) (presentationSeed, func()) {
	t.Helper()

	// Randomised per run: orders.system_id is globally unique and the
	// org slug / venue name carry active unique indexes, so a leftover
	// from an interrupted run must not be able to collide (AGENTS.md).
	nonce := uuid.New().String()[:8]

	orgID := uuid.New()
	evtID := uuid.New()
	venueID := uuid.New()
	sessID := uuid.New()
	chanID := uuid.New()
	resvID := uuid.New()
	csID := uuid.New()
	custID := uuid.New()
	orderID := uuid.New()
	tierID := uuid.New()
	tktID := uuid.New()
	djID := uuid.New()
	itemID := uuid.New()
	evtPosterID := uuid.New()
	sessPosterID := uuid.New()

	seed := presentationSeed{
		TicketID:       tktID,
		DeliveryJobID:  djID,
		OrderID:        orderID,
		SessionID:      sessID,
		EventID:        evtID,
		RecipientEmail: fmt.Sprintf("buyer-%s@example.com", nonce),
		EventName:      "Podzimní Koncert " + nonce,
		VenueName:      "Estonia Concert Hall " + nonce,
		VenueAddress:   "Estonia puiestee 4 " + nonce,
		VenueCity:      "Tallinn",
		VenueTZ:        "Europe/Tallinn",
		TierName:       "Balcony Left " + nonce,
		HolderName:     "Jana Nováková " + nonce,
		// 19:00 UTC on 2026-10-03 is 22:00 in Europe/Tallinn (EEST, still
		// UTC+3 until the last Sunday of October), so "local" and "UTC"
		// cannot be confused in the assertions below.
		SessionStart: time.Date(2026, 10, 3, 19, 0, 0, 0, time.UTC),
		// 1890 subtotal + 95 service charge on this one unit: the ticket
		// must print what the buyer PAID (19.85), never the tier's list
		// price (18.90).
		PriceMinor:      1985,
		Currency:        "EUR",
		EAN13:           "2100000000302",
		EventPosterID:   evtPosterID,
		SessionPosterID: sessPosterID,
	}

	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seedPresentationTicket: %s\n  error: %v", strings.TrimSpace(sql)[:min(70, len(strings.TrimSpace(sql)))], err)
		}
	}

	exec(`INSERT INTO organizations (id, name, slug, country) VALUES ($1, $2, $3, 'EE')`,
		orgID, "Presentation Org "+nonce, "presentation-org-"+nonce)

	exec(`INSERT INTO events (id, org_id, name) VALUES ($1, $2, $3)`, evtID, orgID, seed.EventName)

	exec(`INSERT INTO venues (id, org_id, name, city_id, timezone, address_line1)
	      VALUES ($1, $2, $3, (SELECT id FROM cities WHERE slug = $4), $5, $6)`,
		venueID, orgID, seed.VenueName, tallinnCitySlug, seed.VenueTZ, seed.VenueAddress)

	// Two poster rows, attached by nobody yet: the tests that care attach
	// the one they are about, so the default fixture renders poster-less.
	// media_objects requires a positive byte_size and a 64-hex checksum.
	for _, poster := range []struct {
		id        uuid.UUID
		ownerType string
		ownerID   uuid.UUID
	}{
		{evtPosterID, "event_poster", evtID},
		{sessPosterID, "session_poster", sessID},
	} {
		exec(`INSERT INTO media_objects (id, org_id, owner_type, owner_id, storage_backend,
		                                 storage_key, content_type, byte_size, checksum_sha256)
		      VALUES ($1, $2, $3, $4, 'local', $5, 'image/png', 1024, repeat('a', 64))`,
			poster.id, orgID, poster.ownerType, poster.ownerID,
			fmt.Sprintf("media/%s/%s.png", nonce, poster.id))
	}

	exec(`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, currency, currency_source)
	      VALUES ($1, $2, $3, $4::timestamptz, $4::timestamptz + interval '2 hours', 100, 'EUR', 'override')`,
		sessID, evtID, venueID, seed.SessionStart)

	exec(`INSERT INTO sales_channels (id, org_id, name, payment_mode, provider)
	      VALUES ($1, $2, $3, 'direct_merchant', 'stripe')`, chanID, orgID, "Channel "+nonce)

	exec(`INSERT INTO reservations (id, org_id, channel_id, session_id, quantity, state, expires_at, converted_at)
	      VALUES ($1, $2, $3, $4, 1, 'converted', now() + interval '1 hour', now())`,
		resvID, orgID, chanID, sessID)

	exec(`INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state)
	      VALUES ($1, $2, $3, $4, 'completed')`, csID, orgID, chanID, resvID)

	exec(`INSERT INTO customers (id, display_name) VALUES ($1, $2)`,
		custID, "Never Used Fallback "+nonce)

	// buyer_name is the holder source; the customers.display_name above
	// exists only to prove the COALESCE prefers the order's own value.
	exec(`INSERT INTO orders (id, org_id, channel_id, event_id, session_id, customer_id,
	                          checkout_session_id, reservation_id, source, status,
	                          currency, subtotal, discount, charge, total, buyer_name, buyer_email)
	      VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'public_feed', 'paid',
	              'EUR', 1890, 0, 95, 1985, $9, $10)`,
		orderID, orgID, chanID, evtID, sessID, custID, csID, resvID, seed.HolderName, seed.RecipientEmail)

	// price_amount is deliberately the LIST price and differs from what the
	// buyer paid, so an assertion on the printed price cannot pass by
	// accidentally reading the tier.
	exec(`INSERT INTO ticket_tiers (id, session_id, name, pricing_mode, price_amount, currency)
	      VALUES ($1, $2, $3, 'fixed', 1890, 'EUR')`, tierID, sessID, seed.TierName)

	exec(`INSERT INTO tickets (id, checkout_session_id, session_id, tier_id, holder_email, order_id)
	      VALUES ($1, $2, $3, $4, $5, $6)`,
		tktID, csID, sessID, tierID, seed.RecipientEmail, orderID)

	// One order item per unit, ticket_id backfilled exactly as
	// IssueTicketsForCheckout does it. total = unit_price - discount +
	// charge, the amount actually paid for this seat.
	exec(`INSERT INTO order_items (id, order_id, ordinal, kind, tier_id, ticket_id,
	                               unit_price, discount, charge, total)
	      VALUES ($1, $2, 1, 'ticket', $3, $4, 1890, 0, 95, $5)`,
		itemID, orderID, tierID, tktID, seed.PriceMinor)

	exec(`INSERT INTO delivery_jobs (id, ticket_id, recipient_email) VALUES ($1, $2, $3)`,
		djID, tktID, seed.RecipientEmail)

	// Every ticket issued since feature #502 carries an EAN-13 credential,
	// and it is what makes the page draw its QR — so the image assertions
	// below can tell "the full layout rendered" from "the minimal fallback
	// PDF did". The digits are a valid GS1 code; the renderer draws
	// neither code for one that fails the check digit.
	exec(`INSERT INTO ticket_credentials (ticket_id, type, payload) VALUES ($1, 'ean13', $2)`,
		tktID, seed.EAN13)

	if err := pool.QueryRow(ctx,
		`SELECT system_ticket_id FROM tickets WHERE id = $1`, tktID,
	).Scan(&seed.SystemID); err != nil {
		t.Fatalf("seedPresentationTicket: read system_ticket_id: %v", err)
	}
	if seed.SystemID <= 0 {
		t.Fatalf("seedPresentationTicket: system_ticket_id = %d; want a positive sequence value", seed.SystemID)
	}
	if err := pool.QueryRow(ctx,
		`SELECT system_id FROM orders WHERE id = $1`, orderID,
	).Scan(&seed.OrderSystemID); err != nil {
		t.Fatalf("seedPresentationTicket: read orders.system_id: %v", err)
	}
	if seed.OrderSystemID <= 0 {
		t.Fatalf("seedPresentationTicket: orders.system_id = %d; want a positive sequence value", seed.OrderSystemID)
	}

	cleanup := func() {
		// FK order: worker_jobs → credentials/jobs → order items → ticket →
		// order → tier → checkout/reservation → session → venue/event/media
		// → customer → org.
		//
		// worker_jobs carries no FK to any of these rows — its ticket id
		// lives inside the JSON payload — so it is swept by hand first
		// (AGENTS.md). This fixture does not enqueue one itself, but a
		// handler change that starts enqueueing must not be able to leak a
		// row into a shared database, where the next package to drain the
		// queue generically fails on an unknown job type.
		for _, stmt := range []struct {
			sql  string
			args []any
		}{
			{`DELETE FROM worker_jobs WHERE payload->>'ticket_id' = $1`, []any{tktID.String()}},
			{`DELETE FROM ticket_credentials WHERE ticket_id = $1`, []any{tktID}},
			{`DELETE FROM delivery_jobs WHERE ticket_id = $1`, []any{tktID}},
			{`DELETE FROM order_items WHERE order_id = $1`, []any{orderID}},
			{`DELETE FROM tickets WHERE id = $1`, []any{tktID}},
			{`DELETE FROM orders WHERE id = $1`, []any{orderID}},
			{`DELETE FROM ticket_tiers WHERE id = $1`, []any{tierID}},
			{`DELETE FROM checkout_sessions WHERE id = $1`, []any{csID}},
			{`DELETE FROM reservations WHERE id = $1`, []any{resvID}},
			{`UPDATE sessions SET poster_media_id = NULL WHERE id = $1`, []any{sessID}},
			{`UPDATE events   SET poster_media_id = NULL WHERE id = $1`, []any{evtID}},
			{`DELETE FROM sessions WHERE id = $1`, []any{sessID}},
			{`DELETE FROM sales_channels WHERE id = $1`, []any{chanID}},
			{`DELETE FROM events WHERE id = $1`, []any{evtID}},
			{`DELETE FROM venues WHERE id = $1`, []any{venueID}},
			{`DELETE FROM media_objects WHERE id = ANY($1)`, []any{[]uuid.UUID{evtPosterID, sessPosterID}}},
			{`DELETE FROM customers WHERE id = $1`, []any{custID}},
			{`DELETE FROM organizations WHERE id = $1`, []any{orgID}},
		} {
			if _, err := pool.Exec(ctx, stmt.sql, stmt.args...); err != nil {
				t.Logf("seedPresentationTicket cleanup: %s: %v", stmt.sql, err)
			}
		}
	}
	return seed, cleanup
}

// presentationPool connects to DATABASE_URL, skipping when unset.
func presentationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping live DB integration test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// runDeliveryWithPayload invokes the real worker handler against a real
// SMTP capture server, with the given payload, and returns the captured
// message.
func runDeliveryWithPayload(ctx context.Context, t *testing.T, pool *pgxpool.Pool, p Payload) string {
	t.Helper()
	return runDeliveryWithMedia(ctx, t, pool, p, nil)
}

// runDeliveryWithMedia is runDeliveryWithPayload with a MediaResolver
// wired, which is what the poster needs: the handler resolves the poster
// media id from the ticket's rows and then asks the media store for its
// bytes, exactly as it does for the org logo.
func runDeliveryWithMedia(ctx context.Context, t *testing.T, pool *pgxpool.Pool, p Payload, media MediaResolver) string {
	t.Helper()
	smtp := newSMTPCaptureServer(t)
	queries := gen.New(pool)
	handler := NewHandler(HandlerOptions{
		TicketQueries:      queries,
		DeliveryJobQueries: queries,
		CredentialQueries:  queries,
		Sender:             buildSMTPSender(smtp.Addr),
		Media:              media,
		Logger:             testLogger(),
	})
	body := mustJSON(t, p)
	if err := handler(ctx, body); err != nil {
		t.Fatalf("delivery handler: %v", err)
	}
	select {
	case raw := <-smtp.Captured:
		return string(raw)
	case <-time.After(10 * time.Second):
		t.Fatal("SMTP capture server received no message within 10s")
		return ""
	}
}

// storedTicketPDF reads back the PDF the handler generated and persisted
// as the ticket's 'pdf' credential — the exact bytes attached to the
// e-mail, without having to unpick the MIME envelope.
func storedTicketPDF(ctx context.Context, t *testing.T, pool *pgxpool.Pool, ticketID uuid.UUID) []byte {
	t.Helper()
	var encoded string
	if err := pool.QueryRow(ctx,
		`SELECT payload FROM ticket_credentials WHERE ticket_id = $1 AND type = 'pdf'`, ticketID,
	).Scan(&encoded); err != nil {
		t.Fatalf("read stored pdf credential: %v", err)
	}
	out, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode stored pdf credential: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("stored pdf credential decoded to zero bytes")
	}
	return out
}

// ─────────────────────────────────────────────────────────────────────────────
// Tests
// ─────────────────────────────────────────────────────────────────────────────

// TestDeliveryPresentation_HintlessPayloadRendersRealValues is the
// regression test for the defect: the payload the production enqueuers
// actually send.
func TestDeliveryPresentation_HintlessPayloadRendersRealValues(t *testing.T) {
	pool := presentationPool(t)
	ctx := context.Background()
	seed, cleanup := seedPresentationTicket(ctx, t, pool)
	defer cleanup()

	raw := runDeliveryWithPayload(ctx, t, pool, Payload{
		TicketID: seed.TicketID.String(),
		Locale:   "en",
	})
	doc := storedTicketPDF(ctx, t, pool, seed.TicketID)

	// ── PDF: the real values, not "Arena Event" and blank rows ────────
	for label, want := range map[string]string{
		"event name": seed.EventName,
		"venue":      seed.VenueName,
		"venue city": seed.VenueCity,
		"category":   seed.TierName,
		"holder":     seed.HolderName,
	} {
		if !pdfContainsText(doc, want) {
			t.Errorf("PDF does not print the %s %q", label, want)
		}
	}
	if pdfContainsText(doc, "Arena Event") {
		t.Error("PDF still prints the \"Arena Event\" placeholder title")
	}

	// ── PDF: the venue-local session time ─────────────────────────────
	// The ported layout prints the date, the clock and the weekday as
	// three separate lines and never names the zone; the ONLY way UTC and
	// Europe/Tallinn differ on the page is the clock (19:00 vs 22:00) and,
	// here, the weekday and date they fall on.
	for _, want := range []string{"3 October 2026", "22:00", "Saturday"} {
		if !pdfContainsText(doc, want) {
			t.Errorf("PDF does not print %q of the venue-local session time", want)
		}
	}
	if pdfContainsText(doc, "19:00") {
		t.Error("PDF still prints the session time in UTC")
	}

	// ── PDF: the human-facing number, never the UUID ──────────────────
	wantNumber := fmt.Sprintf("%d", seed.SystemID)
	if !pdfContainsText(doc, "Ticket # "+wantNumber) {
		t.Errorf("PDF does not print the ticket number line for system_ticket_id %s", wantNumber)
	}
	assertPDFHasNoUUID(t, doc, seed.TicketID.String())

	// ── E-mail body: the same values ──────────────────────────────────
	body := emailTextBody(t, raw)
	for label, want := range map[string]string{
		"event name":    seed.EventName,
		"venue":         seed.VenueName + ", " + seed.VenueCity,
		"category":      seed.TierName,
		"holder":        seed.HolderName,
		"local time":    "2026-10-03 22:00 (Europe/Tallinn)",
		"ticket number": wantNumber,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("e-mail body does not carry the %s %q\n--- body ---\n%s", label, want, body)
		}
	}
	if strings.Contains(body, "Arena Event") {
		t.Error("e-mail body still carries the \"Arena Event\" placeholder")
	}
	if strings.Contains(body, seed.TicketID.String()) {
		t.Error("e-mail body leaks the internal ticket UUID")
	}
}

// TestDeliveryPresentation_PayloadHintOverridesResolvedValue proves the
// resolution only fills gaps, so an enqueuer that does populate a hint
// (and every existing test that does) keeps control.
func TestDeliveryPresentation_PayloadHintOverridesResolvedValue(t *testing.T) {
	pool := presentationPool(t)
	ctx := context.Background()
	seed, cleanup := seedPresentationTicket(ctx, t, pool)
	defer cleanup()

	const hintedEvent = "Hinted Event Name"
	const hintedTier = "Hinted Category"
	runDeliveryWithPayload(ctx, t, pool, Payload{
		TicketID:  seed.TicketID.String(),
		Locale:    "en",
		EventName: hintedEvent,
		TierName:  hintedTier,
	})
	doc := storedTicketPDF(ctx, t, pool, seed.TicketID)

	if !pdfContainsText(doc, hintedEvent) {
		t.Errorf("PDF does not print the hinted event name %q", hintedEvent)
	}
	if pdfContainsText(doc, seed.EventName) {
		t.Errorf("PDF printed the resolved event name %q over the payload hint", seed.EventName)
	}
	if !pdfContainsText(doc, hintedTier) {
		t.Errorf("PDF does not print the hinted category %q", hintedTier)
	}
	// The un-hinted fields still resolve.
	if !pdfContainsText(doc, seed.VenueName) {
		t.Errorf("PDF does not print the resolved venue %q", seed.VenueName)
	}
	if !pdfContainsText(doc, seed.VenueAddress+", "+seed.VenueCity) {
		t.Errorf("PDF does not print the resolved venue address line")
	}
}

// TestDeliveryPresentation_VenueWithoutTimezoneFallsBackToUTC — the
// fallback must be UTC, not a refusal to render.
func TestDeliveryPresentation_VenueWithoutTimezoneFallsBackToUTC(t *testing.T) {
	pool := presentationPool(t)
	ctx := context.Background()
	seed, cleanup := seedPresentationTicket(ctx, t, pool)
	defer cleanup()

	if _, err := pool.Exec(ctx,
		`UPDATE venues SET timezone = NULL WHERE name = $1`, seed.VenueName,
	); err != nil {
		t.Fatalf("clear venue timezone: %v", err)
	}

	runDeliveryWithPayload(ctx, t, pool, Payload{
		TicketID: seed.TicketID.String(),
		Locale:   "en",
	})
	doc := storedTicketPDF(ctx, t, pool, seed.TicketID)

	// 19:00 UTC, the stored instant itself — and the day before the
	// Tallinn-local clock would have put it.
	for _, want := range []string{"3 October 2026", "19:00", "Saturday"} {
		if !pdfContainsText(doc, want) {
			t.Errorf("PDF does not fall back to %q when the venue has no timezone", want)
		}
	}
	if pdfContainsText(doc, "22:00") {
		t.Error("PDF printed a venue-local time for a venue that has no timezone")
	}
	if !pdfContainsText(doc, seed.VenueName) {
		t.Errorf("a venue without a timezone must still print its name %q", seed.VenueName)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Order number, address, price, poster
// ─────────────────────────────────────────────────────────────────────────────

// posterStore is a MediaResolver backed by a map, so a test can prove
// WHICH media id the handler asked for.
type posterStore struct {
	byID  map[string][]byte
	err   error
	asked []string
}

func (s *posterStore) ResolveLogo(_ context.Context, mediaID string) ([]byte, string, error) {
	s.asked = append(s.asked, mediaID)
	if s.err != nil {
		return nil, "", s.err
	}
	b, ok := s.byID[mediaID]
	if !ok {
		return nil, "", ErrLogoNotFound
	}
	return b, "https://media.example.test/" + mediaID, nil
}

// pdfImageCount counts the image XObjects in an uncompressed document.
// The QR is always one of them, so "has a poster" is two.
func pdfImageCount(doc []byte) int {
	return bytes.Count(doc, []byte("/Subtype /Image"))
}

// attachPoster points the given column at a media row.
func attachPoster(ctx context.Context, t *testing.T, pool *pgxpool.Pool, table string, rowID, mediaID uuid.UUID) {
	t.Helper()
	// table is a test-controlled literal ("events" / "sessions"), never
	// caller input.
	if _, err := pool.Exec(ctx,
		fmt.Sprintf(`UPDATE %s SET poster_media_id = $2 WHERE id = $1`, table), rowID, mediaID,
	); err != nil {
		t.Fatalf("attach poster to %s: %v", table, err)
	}
}

// TestDeliveryPresentation_HintlessPayloadRendersOrderAddressPriceAndPoster
// is the second half of the same defect: four more fields the ported
// layout draws that nothing populated, so a live ticket printed no order
// number, no street address, no price and no artwork.
func TestDeliveryPresentation_HintlessPayloadRendersOrderAddressPriceAndPoster(t *testing.T) {
	pool := presentationPool(t)
	ctx := context.Background()
	seed, cleanup := seedPresentationTicket(ctx, t, pool)
	defer cleanup()

	attachPoster(ctx, t, pool, "events", seed.EventID, seed.EventPosterID)
	// 300×314: deliberately not the QR's raster width, because gofpdf
	// orders image objects by width and breaks ties with Go's randomised
	// map iteration (AGENTS.md / RenderFormat's determinism contract).
	art := tinyPNG(t, 300, 314)
	media := &posterStore{byID: map[string][]byte{seed.EventPosterID.String(): art}}

	runDeliveryWithMedia(ctx, t, pool, Payload{
		TicketID: seed.TicketID.String(),
		Locale:   "en",
	}, media)
	doc := storedTicketPDF(ctx, t, pool, seed.TicketID)

	if want := fmt.Sprintf("Order %d", seed.OrderSystemID); !pdfContainsText(doc, want) {
		t.Errorf("PDF footer does not print %q", want)
	}
	if want := seed.VenueAddress + ", " + seed.VenueCity; !pdfContainsText(doc, want) {
		t.Errorf("PDF does not print the venue address line %q", want)
	}
	// 1985 minor units, the order item's total — NOT the tier's 1890 list
	// price, which is what a naive join would have printed.
	if !pdfContainsText(doc, "19.85 EUR") {
		t.Error("PDF does not print the price the buyer actually paid (19.85 EUR)")
	}
	if pdfContainsText(doc, "18.90 EUR") {
		t.Error("PDF printed the tier's list price instead of the amount paid")
	}
	if got := pdfImageCount(doc); got != 2 {
		t.Errorf("PDF carries %d image(s); want 2 (the QR and the poster)", got)
	}
	if len(media.asked) != 1 || media.asked[0] != seed.EventPosterID.String() {
		t.Errorf("media store was asked for %v; want exactly the event poster %s",
			media.asked, seed.EventPosterID)
	}
}

// TestDeliveryPresentation_SessionPosterOverridesTheEventPoster — migration
// 0082's documented resolution order: the session's own artwork wins.
func TestDeliveryPresentation_SessionPosterOverridesTheEventPoster(t *testing.T) {
	pool := presentationPool(t)
	ctx := context.Background()
	seed, cleanup := seedPresentationTicket(ctx, t, pool)
	defer cleanup()

	attachPoster(ctx, t, pool, "events", seed.EventID, seed.EventPosterID)
	attachPoster(ctx, t, pool, "sessions", seed.SessionID, seed.SessionPosterID)

	media := &posterStore{byID: map[string][]byte{
		seed.EventPosterID.String():   tinyPNG(t, 300, 314),
		seed.SessionPosterID.String(): tinyPNG(t, 280, 293),
	}}

	runDeliveryWithMedia(ctx, t, pool, Payload{
		TicketID: seed.TicketID.String(),
		Locale:   "en",
	}, media)
	doc := storedTicketPDF(ctx, t, pool, seed.TicketID)

	if len(media.asked) != 1 || media.asked[0] != seed.SessionPosterID.String() {
		t.Fatalf("media store was asked for %v; want only the session poster %s",
			media.asked, seed.SessionPosterID)
	}
	if got := pdfImageCount(doc); got != 2 {
		t.Errorf("PDF carries %d image(s); want 2 (the QR and the session poster)", got)
	}
}

// TestDeliveryPresentation_ComplimentaryTicketPrintsNoPrice — an
// invitation discounts its whole subtotal, so its order item totals 0.
// The page must carry no price cell at all: "0.00 EUR" on a gift reads as
// a pricing bug.
func TestDeliveryPresentation_ComplimentaryTicketPrintsNoPrice(t *testing.T) {
	pool := presentationPool(t)
	ctx := context.Background()
	seed, cleanup := seedPresentationTicket(ctx, t, pool)
	defer cleanup()

	if _, err := pool.Exec(ctx,
		`UPDATE orders SET source = 'complimentary', discount = subtotal, charge = 0, total = 0
		 WHERE id = $1`, seed.OrderID,
	); err != nil {
		t.Fatalf("turn the order into an invitation: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE order_items SET discount = unit_price, charge = 0, total = 0 WHERE order_id = $1`,
		seed.OrderID,
	); err != nil {
		t.Fatalf("zero the invitation's order items: %v", err)
	}

	runDeliveryWithPayload(ctx, t, pool, Payload{
		TicketID: seed.TicketID.String(),
		Locale:   "en",
		Template: TemplateInvitation,
	})
	doc := storedTicketPDF(ctx, t, pool, seed.TicketID)

	// "Price" is the en label of the cell; with no amount the cell — label
	// included — is not drawn at all.
	if pdfContainsText(doc, "Price") {
		t.Error("an invitation must not print a price cell")
	}
	for _, unwanted := range []string{"0 EUR", "0.00 EUR", "18.90 EUR", "19.85 EUR"} {
		if pdfContainsText(doc, unwanted) {
			t.Errorf("an invitation printed the amount %q", unwanted)
		}
	}
	// It is still a real ticket: everything else resolves.
	if !pdfContainsText(doc, seed.EventName) {
		t.Error("the invitation lost its event name")
	}
	if want := fmt.Sprintf("Order %d", seed.OrderSystemID); !pdfContainsText(doc, want) {
		t.Errorf("the invitation lost its order number %q", want)
	}
}

// TestDeliveryPresentation_BadPosterStillDelivers — a poster that cannot
// be fetched, or that is not an image the renderer can draw, must cost
// the buyer nothing but the artwork.
func TestDeliveryPresentation_BadPosterStillDelivers(t *testing.T) {
	for name, media := range map[string]*posterStore{
		"media store outage": {err: errors.New("s3: connection refused")},
		"media row vanished": {byID: map[string][]byte{}},
		"not an image":       nil, // filled in below, needs the seeded id
	} {
		t.Run(name, func(t *testing.T) {
			pool := presentationPool(t)
			ctx := context.Background()
			seed, cleanup := seedPresentationTicket(ctx, t, pool)
			defer cleanup()

			attachPoster(ctx, t, pool, "events", seed.EventID, seed.EventPosterID)
			if media == nil {
				media = &posterStore{byID: map[string][]byte{
					seed.EventPosterID.String(): []byte("%PDF-1.4\nan organizer uploaded the print pdf"),
				}}
			}

			raw := runDeliveryWithMedia(ctx, t, pool, Payload{
				TicketID: seed.TicketID.String(),
				Locale:   "en",
			}, media)
			doc := storedTicketPDF(ctx, t, pool, seed.TicketID)

			if got := pdfImageCount(doc); got != 1 {
				t.Errorf("PDF carries %d image(s); want 1 (the QR alone — no poster)", got)
			}
			// The e-mail went out regardless, with the rest of the ticket intact.
			if body := emailTextBody(t, raw); !strings.Contains(body, seed.EventName) {
				t.Errorf("the e-mail lost its event name when the poster failed")
			}
			if !pdfContainsText(doc, seed.TierName) {
				t.Error("the PDF lost its category when the poster failed")
			}
		})
	}
}

// TestGetTicketPresentationByID_ResolvesEveryColumn exercises the query
// wrapper directly, so a column that silently stops resolving is caught
// at the SQL level and not only through a rendered PDF.
func TestGetTicketPresentationByID_ResolvesEveryColumn(t *testing.T) {
	pool := presentationPool(t)
	ctx := context.Background()
	seed, cleanup := seedPresentationTicket(ctx, t, pool)
	defer cleanup()

	attachPoster(ctx, t, pool, "events", seed.EventID, seed.EventPosterID)

	row, err := gen.New(pool).GetTicketPresentationByID(ctx, seed.TicketID)
	if err != nil {
		t.Fatalf("GetTicketPresentationByID: %v", err)
	}
	if row.SystemTicketID != seed.SystemID {
		t.Errorf("system_ticket_id = %d; want %d", row.SystemTicketID, seed.SystemID)
	}
	want := map[string]string{
		"event_name":     seed.EventName,
		"venue_name":     seed.VenueName,
		"venue_address":  seed.VenueAddress,
		"venue_city":     seed.VenueCity,
		"venue_timezone": seed.VenueTZ,
		"tier_name":      seed.TierName,
		"holder_name":    seed.HolderName,
		"price_currency": seed.Currency,
	}
	got := map[string]*string{
		"event_name":     row.EventName,
		"venue_name":     row.VenueName,
		"venue_address":  row.VenueAddress,
		"venue_city":     row.VenueCity,
		"venue_timezone": row.VenueTimezone,
		"tier_name":      row.TierName,
		"holder_name":    row.HolderName,
		"price_currency": row.PriceCurrency,
	}
	for field, w := range want {
		switch g := got[field]; {
		case g == nil:
			t.Errorf("%s is NULL; want %q", field, w)
		case *g != w:
			t.Errorf("%s = %q; want %q", field, *g, w)
		}
	}
	if row.SessionStartAt == nil || !row.SessionStartAt.UTC().Equal(seed.SessionStart) {
		t.Errorf("session_start_at = %v; want %v", row.SessionStartAt, seed.SessionStart)
	}
	if row.OrderNumber == nil || *row.OrderNumber != seed.OrderSystemID {
		t.Errorf("order_number = %v; want %d", row.OrderNumber, seed.OrderSystemID)
	}
	if row.PriceMinor == nil || *row.PriceMinor != seed.PriceMinor {
		t.Errorf("price_minor = %v; want %d (order_items.total, not the tier's list price)",
			row.PriceMinor, seed.PriceMinor)
	}
	if row.PosterMediaID == nil || *row.PosterMediaID != seed.EventPosterID {
		t.Errorf("poster_media_id = %v; want the event poster %s", row.PosterMediaID, seed.EventPosterID)
	}

	// The session's own poster wins once it is set (migration 0082).
	attachPoster(ctx, t, pool, "sessions", seed.SessionID, seed.SessionPosterID)
	row, err = gen.New(pool).GetTicketPresentationByID(ctx, seed.TicketID)
	if err != nil {
		t.Fatalf("GetTicketPresentationByID after session override: %v", err)
	}
	if row.PosterMediaID == nil || *row.PosterMediaID != seed.SessionPosterID {
		t.Errorf("poster_media_id = %v; want the session override %s", row.PosterMediaID, seed.SessionPosterID)
	}
}

// TestGetTicketPresentationByID_TicketWithoutAnOrderOrAddress proves the
// LEFT JOINs still hold: an orderless ticket on a venue-less session must
// resolve what it has and answer NULL for the rest — never ErrNoRows and
// never a join that drops the row.
func TestGetTicketPresentationByID_TicketWithoutAnOrderOrAddress(t *testing.T) {
	pool := presentationPool(t)
	ctx := context.Background()
	seed, cleanup := seedPresentationTicket(ctx, t, pool)
	defer cleanup()

	if _, err := pool.Exec(ctx,
		`DELETE FROM order_items WHERE ticket_id = $1`, seed.TicketID); err != nil {
		t.Fatalf("drop the order item: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE tickets SET order_id = NULL, tier_id = NULL WHERE id = $1`, seed.TicketID); err != nil {
		t.Fatalf("orphan the ticket from its order and tier: %v", err)
	}
	// A venue that was entered as a bare name: no structured address, no
	// legacy address line, no city.
	if _, err := pool.Exec(ctx,
		`UPDATE venues SET address_line1 = NULL, address = NULL, city_id = NULL
		 WHERE name = $1`, seed.VenueName); err != nil {
		t.Fatalf("strip the venue down to its name: %v", err)
	}

	row, err := gen.New(pool).GetTicketPresentationByID(ctx, seed.TicketID)
	if err != nil {
		t.Fatalf("GetTicketPresentationByID: %v", err)
	}
	if row.EventName == nil || *row.EventName != seed.EventName {
		t.Errorf("event_name = %v; want %q", row.EventName, seed.EventName)
	}
	if row.OrderNumber != nil || row.PriceMinor != nil || row.PriceCurrency != nil {
		t.Errorf("an orderless ticket must resolve no order columns: number=%v price=%v currency=%v",
			row.OrderNumber, row.PriceMinor, row.PriceCurrency)
	}
	if row.VenueName == nil || *row.VenueName != seed.VenueName {
		t.Errorf("venue_name = %v; want %q — the name survives an address-less venue",
			row.VenueName, seed.VenueName)
	}
	if row.VenueAddress != nil || row.VenueCity != nil {
		t.Errorf("an address-less, city-less venue must resolve neither: %v / %v",
			row.VenueAddress, row.VenueCity)
	}
	if row.TierName != nil {
		t.Errorf("tier_name = %v; want NULL for a GA ticket", row.TierName)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Assertion helpers
// ─────────────────────────────────────────────────────────────────────────────

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return b
}

// utf16beEscapedPDF mirrors gofpdf's utf8toutf16(s, false) + escape()
// pipeline — the encoding every string drawn with the embedded UTF-8
// TrueType font (delivery/pdf/fonts.go) gets in the content stream. A
// test that searched the raw bytes for a plain ASCII literal would find
// nothing, and would be asserting the wrong thing (see the AGENTS.md note
// and delivery/pdf/pdf_testutil_test.go, whose helper this duplicates
// because it lives in another package's _test files).
func utf16beEscapedPDF(s string) []byte {
	var out []byte
	for _, r := range s {
		for _, b := range [2]byte{byte(r >> 8), byte(r)} {
			switch b {
			case '\\', '(', ')':
				out = append(out, '\\', b)
			case '\r':
				out = append(out, '\\', 'r')
			default:
				out = append(out, b)
			}
		}
	}
	return out
}

// pdfDrawnText returns everything the page draws, in draw order, with
// every run of whitespace collapsed to a single space.
//
// A raw byte search for one drawn string is not enough any more: the
// ported layout lays its text out with MultiCell, so a value that does
// not fit its column ("Estonia puiestee 4, Tallinn" in the venue block, a
// long category name in a third-of-the-width info cell) is drawn as
// SEVERAL strings, one per line, and no single token contains it. Joining
// the runs with spaces and collapsing whitespace makes an assertion
// independent of where the renderer happened to break the line — which is
// what these tests actually mean to assert.
//
// Compression is off in the renderer (SetCompression(false)) and every
// string is drawn as "Td (<utf-16be>)Tj" (the text is UTF-16BE because
// the layout uses the embedded TrueType font — see the AGENTS.md note), so
// that pair is the anchor scanned for. Image and font streams contain no
// such sequence.
func pdfDrawnText(doc []byte) string {
	var parts []string
	marker := []byte("Td (")
	for rest := doc; ; {
		i := bytes.Index(rest, marker)
		if i < 0 {
			break
		}
		rest = rest[i+len(marker):]
		raw, n, ok := scanPDFStringLiteral(rest)
		if !ok {
			break
		}
		parts = append(parts, decodeUTF16BE(raw))
		rest = rest[n:]
	}
	return strings.Join(strings.Fields(strings.Join(parts, " ")), " ")
}

// scanPDFStringLiteral reads the body of a PDF string literal that ends at
// the next unescaped ")Tj", undoing gofpdf's escaping of '\\', '(', ')'
// and CR. n is how many bytes of b were consumed.
func scanPDFStringLiteral(b []byte) (raw []byte, n int, ok bool) {
	for i := 0; i < len(b); {
		if b[i] == '\\' && i+1 < len(b) {
			c := b[i+1]
			if c == 'r' {
				c = '\r'
			}
			raw = append(raw, c)
			i += 2
			continue
		}
		if bytes.HasPrefix(b[i:], []byte(")Tj")) {
			return raw, i + len(")Tj"), true
		}
		raw = append(raw, b[i])
		i++
	}
	return nil, 0, false
}

// decodeUTF16BE turns the big-endian UTF-16 bytes gofpdf writes for an
// embedded-font string back into Go text. Odd trailing bytes (impossible
// in practice) are dropped rather than panicking.
func decodeUTF16BE(b []byte) string {
	units := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		units = append(units, uint16(b[i])<<8|uint16(b[i+1]))
	}
	return string(utf16.Decode(units))
}

// pdfContainsText reports whether s was drawn on the page, ignoring where
// the renderer wrapped its lines.
func pdfContainsText(doc []byte, s string) bool {
	want := strings.Join(strings.Fields(s), " ")
	return want != "" && strings.Contains(pdfDrawnText(doc), want)
}

// assertPDFHasNoUUID fails when the internal ticket UUID appears anywhere
// in the document — as page text (UTF-16BE) or as a plain byte run
// (metadata, raw operators).
func assertPDFHasNoUUID(t *testing.T, doc []byte, ticketID string) {
	t.Helper()
	if bytes.Contains(doc, []byte(ticketID)) {
		t.Errorf("PDF contains the raw ticket UUID %q", ticketID)
	}
	if bytes.Contains(doc, utf16beEscapedPDF(ticketID)) {
		t.Errorf("PDF contains the UTF-16BE-encoded ticket UUID %q", ticketID)
	}
}

// emailTextBody returns the decoded text/plain part of a captured SMTP
// message. Both text parts are quoted-printable, so a raw substring
// search would miss any value with a non-ASCII character in it.
func emailTextBody(t *testing.T, raw string) string {
	t.Helper()
	msg, err := mail.ReadMessage(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("parse captured message: %v", err)
	}
	mediaType, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if err != nil {
		t.Fatalf("parse Content-Type: %v", err)
	}
	if !strings.HasPrefix(mediaType, "multipart/") {
		t.Fatalf("captured message is %q, expected multipart (the ticket PDF must be attached)", mediaType)
	}
	mr := multipart.NewReader(msg.Body, params["boundary"])
	for {
		part, err := mr.NextPart()
		if err != nil {
			break
		}
		if !strings.HasPrefix(part.Header.Get("Content-Type"), "text/plain") {
			continue
		}
		decoded, err := io.ReadAll(quotedprintable.NewReader(part))
		if err != nil {
			t.Fatalf("decode text/plain part: %v", err)
		}
		return string(decoded)
	}
	t.Fatal("captured message has no text/plain part")
	return ""
}
