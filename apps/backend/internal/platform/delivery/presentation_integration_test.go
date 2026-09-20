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
	RecipientEmail string

	EventName    string
	VenueName    string
	VenueCity    string
	VenueTZ      string
	TierName     string
	HolderName   string
	SessionStart time.Time
	SystemID     int64
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

	seed := presentationSeed{
		TicketID:       tktID,
		DeliveryJobID:  djID,
		RecipientEmail: fmt.Sprintf("buyer-%s@example.com", nonce),
		EventName:      "Podzimní Koncert " + nonce,
		VenueName:      "Estonia Concert Hall " + nonce,
		VenueCity:      "Tallinn",
		VenueTZ:        "Europe/Tallinn",
		TierName:       "Balcony Left " + nonce,
		HolderName:     "Jana Nováková " + nonce,
		// 19:00 UTC on 2026-10-03 is 22:00 in Europe/Tallinn (EEST, still
		// UTC+3 until the last Sunday of October), so "local" and "UTC"
		// cannot be confused in the assertions below.
		SessionStart: time.Date(2026, 10, 3, 19, 0, 0, 0, time.UTC),
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

	exec(`INSERT INTO venues (id, org_id, name, city_id, timezone)
	      VALUES ($1, $2, $3, (SELECT id FROM cities WHERE slug = $4), $5)`,
		venueID, orgID, seed.VenueName, tallinnCitySlug, seed.VenueTZ)

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
	              'EUR', 1890, 0, 0, 1890, $9, $10)`,
		orderID, orgID, chanID, evtID, sessID, custID, csID, resvID, seed.HolderName, seed.RecipientEmail)

	exec(`INSERT INTO ticket_tiers (id, session_id, name, pricing_mode, price_amount, currency)
	      VALUES ($1, $2, $3, 'fixed', 1890, 'EUR')`, tierID, sessID, seed.TierName)

	exec(`INSERT INTO tickets (id, checkout_session_id, session_id, tier_id, holder_email, order_id)
	      VALUES ($1, $2, $3, $4, $5, $6)`,
		tktID, csID, sessID, tierID, seed.RecipientEmail, orderID)

	exec(`INSERT INTO delivery_jobs (id, ticket_id, recipient_email) VALUES ($1, $2, $3)`,
		djID, tktID, seed.RecipientEmail)

	if err := pool.QueryRow(ctx,
		`SELECT system_ticket_id FROM tickets WHERE id = $1`, tktID,
	).Scan(&seed.SystemID); err != nil {
		t.Fatalf("seedPresentationTicket: read system_ticket_id: %v", err)
	}
	if seed.SystemID <= 0 {
		t.Fatalf("seedPresentationTicket: system_ticket_id = %d; want a positive sequence value", seed.SystemID)
	}

	cleanup := func() {
		// FK order: credentials/jobs → ticket → order → tier →
		// checkout/reservation → session → venue/event → customer → org.
		// worker_jobs is not touched: this test never enqueues one.
		for _, stmt := range []struct {
			sql  string
			args []any
		}{
			{`DELETE FROM ticket_credentials WHERE ticket_id = $1`, []any{tktID}},
			{`DELETE FROM delivery_jobs WHERE ticket_id = $1`, []any{tktID}},
			{`DELETE FROM tickets WHERE id = $1`, []any{tktID}},
			{`DELETE FROM orders WHERE id = $1`, []any{orderID}},
			{`DELETE FROM ticket_tiers WHERE id = $1`, []any{tierID}},
			{`DELETE FROM checkout_sessions WHERE id = $1`, []any{csID}},
			{`DELETE FROM reservations WHERE id = $1`, []any{resvID}},
			{`DELETE FROM sessions WHERE id = $1`, []any{sessID}},
			{`DELETE FROM sales_channels WHERE id = $1`, []any{chanID}},
			{`DELETE FROM events WHERE id = $1`, []any{evtID}},
			{`DELETE FROM venues WHERE id = $1`, []any{venueID}},
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
	smtp := newSMTPCaptureServer(t)
	queries := gen.New(pool)
	handler := NewHandler(HandlerOptions{
		TicketQueries:      queries,
		DeliveryJobQueries: queries,
		CredentialQueries:  queries,
		Sender:             buildSMTPSender(smtp.Addr),
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
		"venue":      seed.VenueName + ", " + seed.VenueCity,
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

	// ── PDF: the venue-local session time, with its zone named ────────
	const wantLocal = "Sat, 3 Oct 2026, 22:00 (Europe/Tallinn)"
	if !pdfContainsText(doc, wantLocal) {
		t.Errorf("PDF does not print the venue-local session time %q", wantLocal)
	}
	if pdfContainsText(doc, "19:00 (UTC)") {
		t.Error("PDF still prints the session time in UTC")
	}

	// ── PDF: the human-facing number, never the UUID ──────────────────
	wantNumber := fmt.Sprintf("%d", seed.SystemID)
	if !pdfContainsText(doc, "Ticket ID: "+wantNumber) {
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
	// The un-hinted fields still resolve. drawDetails prints the venue
	// as "<name>, <city>", so assert on that whole drawn cell.
	if venue := seed.VenueName + ", " + seed.VenueCity; !pdfContainsText(doc, venue) {
		t.Errorf("PDF does not print the resolved venue %q", venue)
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

	const wantUTC = "Sat, 3 Oct 2026, 19:00 (UTC)"
	if !pdfContainsText(doc, wantUTC) {
		t.Errorf("PDF does not fall back to %q when the venue has no timezone", wantUTC)
	}
	if venue := seed.VenueName + ", " + seed.VenueCity; !pdfContainsText(doc, venue) {
		t.Errorf("a venue without a timezone must still print its name %q", venue)
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

	row, err := gen.New(pool).GetTicketPresentationByID(ctx, seed.TicketID)
	if err != nil {
		t.Fatalf("GetTicketPresentationByID: %v", err)
	}
	if row.SystemTicketID != seed.SystemID {
		t.Errorf("system_ticket_id = %d; want %d", row.SystemTicketID, seed.SystemID)
	}
	for field, got := range map[string]*string{
		"event_name":     row.EventName,
		"venue_name":     row.VenueName,
		"venue_city":     row.VenueCity,
		"venue_timezone": row.VenueTimezone,
		"tier_name":      row.TierName,
		"holder_name":    row.HolderName,
	} {
		if got == nil {
			t.Errorf("%s is NULL; want a value", field)
		}
	}
	want := map[string]string{
		"event_name":     seed.EventName,
		"venue_name":     seed.VenueName,
		"venue_city":     seed.VenueCity,
		"venue_timezone": seed.VenueTZ,
		"tier_name":      seed.TierName,
		"holder_name":    seed.HolderName,
	}
	got := map[string]*string{
		"event_name":     row.EventName,
		"venue_name":     row.VenueName,
		"venue_city":     row.VenueCity,
		"venue_timezone": row.VenueTimezone,
		"tier_name":      row.TierName,
		"holder_name":    row.HolderName,
	}
	for field, w := range want {
		if g := got[field]; g != nil && *g != w {
			t.Errorf("%s = %q; want %q", field, *g, w)
		}
	}
	if row.SessionStartAt == nil || !row.SessionStartAt.UTC().Equal(seed.SessionStart) {
		t.Errorf("session_start_at = %v; want %v", row.SessionStartAt, seed.SessionStart)
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

// pdfContainsText reports whether s was drawn on the page. Compression is
// off in the renderer (SetCompression(false)), so the content stream is
// searchable verbatim.
func pdfContainsText(doc []byte, s string) bool {
	token := append([]byte{'('}, utf16beEscapedPDF(s)...)
	token = append(token, ')')
	return bytes.Contains(doc, token)
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
