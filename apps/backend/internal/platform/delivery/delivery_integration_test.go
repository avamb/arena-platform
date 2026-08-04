//go:build integration

// Package delivery — integration tests for PR-03 (honest ticket SMTP delivery).
//
// These tests prove the honest state-transition requirements of PR-03:
//
//   - Positive (TestDelivery_SMTPSuccess): a real configured sender causes the
//     delivery_jobs row to transition queued → processing → sent, and the
//     captured email contains a PDF attachment.
//
//   - Negative (TestDelivery_SMTPRefused): an unreachable SMTP endpoint causes
//     the handler to return an error (retriable). The delivery_jobs row must NOT
//     end up in 'sent' status.
//
//   - Dev-sender guard (TestDelivery_DevSenderProducesDisabled): when the
//     injected Sender is a LogSender (dev-only), the handler marks the
//     delivery_jobs row 'disabled', not 'sent'.
//
//   - No-email skip (TestDelivery_NoEmailProducesSkipped): when no recipient
//     address is available the row is marked 'skipped', not 'sent'.
//
// Run with:
//
//	go test -tags=integration -timeout 300s ./apps/backend/internal/platform/delivery/...
package delivery

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/email"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/tests/pgtest"
)

// ─────────────────────────────────────────────────────────────────────────────
// Minimal SMTP capture server
// ─────────────────────────────────────────────────────────────────────────────

// smtpCaptureServer is a minimal in-process SMTP server for integration tests.
// It accepts exactly one SMTP session, captures the raw DATA payload, and
// signals done when the session completes. Subsequent connections are accepted
// but not served (they close after greeting).
type smtpCaptureServer struct {
	ln       net.Listener
	Addr     string
	Captured chan []byte // receives the raw DATA payload once per session
}

// newSMTPCaptureServer starts a capture server on a random local port.
func newSMTPCaptureServer(t *testing.T) *smtpCaptureServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("smtpCaptureServer: listen: %v", err)
	}
	s := &smtpCaptureServer{
		ln:       ln,
		Addr:     ln.Addr().String(),
		Captured: make(chan []byte, 1),
	}
	t.Cleanup(func() { _ = s.ln.Close() })
	go s.serve(t)
	return s
}

// serve accepts one connection and handles the SMTP session.
func (s *smtpCaptureServer) serve(t *testing.T) {
	conn, err := s.ln.Accept()
	if err != nil {
		// Listener was closed — normal shutdown.
		return
	}
	defer conn.Close()

	w := bufio.NewWriter(conn)
	r := bufio.NewReader(conn)

	write := func(line string) {
		_, _ = fmt.Fprintf(w, "%s\r\n", line)
		_ = w.Flush()
	}

	write("220 testsmtp ESMTP")

	var dataLines []string
	inData := false

	for {
		line, err := r.ReadString('\n')
		if err != nil {
			if err != io.EOF {
				t.Logf("smtpCaptureServer: read: %v", err)
			}
			break
		}
		line = strings.TrimRight(line, "\r\n")

		if inData {
			if line == "." {
				inData = false
				write("250 Ok")
				payload := []byte(strings.Join(dataLines, "\r\n"))
				select {
				case s.Captured <- payload:
				default:
				}
				continue
			}
			// Dot-stuffing: leading dot in data is doubled by sender; strip one.
			if strings.HasPrefix(line, "..") {
				line = line[1:]
			}
			dataLines = append(dataLines, line)
			continue
		}

		upper := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(upper, "EHLO") || strings.HasPrefix(upper, "HELO"):
			write("250-testsmtp greets you")
			write("250 SIZE 10000000")
		case strings.HasPrefix(upper, "MAIL FROM"):
			write("250 Ok")
		case strings.HasPrefix(upper, "RCPT TO"):
			write("250 Ok")
		case upper == "DATA":
			write("354 End data with <CR><LF>.<CR><LF>")
			inData = true
			dataLines = nil
		case strings.HasPrefix(upper, "QUIT"):
			write("221 Bye")
			return
		default:
			write("250 Ok")
		}
	}
}

// smtpRefuseServer listens on a local port and immediately sends "550 Refused"
// to every SMTP session, simulating a server that rejects all messages.
type smtpRefuseServer struct {
	ln   net.Listener
	Addr string
}

// newSMTPRefuseServer starts a refusal server on a random local port.
func newSMTPRefuseServer(t *testing.T) *smtpRefuseServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("smtpRefuseServer: listen: %v", err)
	}
	s := &smtpRefuseServer{ln: ln, Addr: ln.Addr().String()}
	t.Cleanup(func() { _ = s.ln.Close() })
	go s.serveForever(t)
	return s
}

func (s *smtpRefuseServer) serveForever(t *testing.T) {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			defer c.Close()
			// Send a 554 (permanent failure) banner so the client fails immediately.
			_, _ = fmt.Fprintf(c, "554 Service unavailable\r\n")
		}(conn)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Seed data helper
// ─────────────────────────────────────────────────────────────────────────────

// seedResult holds the IDs of all seed rows created for one test.
type seedResult struct {
	TicketID       uuid.UUID
	DeliveryJobID  uuid.UUID
	RecipientEmail string
}

// seedMinimalDeliveryData inserts the minimum FK chain required for a
// delivery_jobs row:
//
//	organizations → events → sessions → sales_channels → reservations
//	→ checkout_sessions → tickets → delivery_jobs
//
// UUIDs are generated in Go so callers know them in advance without a
// round-trip. The recipient email is stored on both the tickets and
// delivery_jobs rows. Passing an empty string produces a NULL recipient
// (for the "skipped" test case).
func seedMinimalDeliveryData(ctx context.Context, t *testing.T, pool *pgxpool.Pool, recipientEmail string) seedResult {
	t.Helper()

	// Use UUIDs we control so IDs are known without a DB round-trip.
	orgID := uuid.New()
	evtID := uuid.New()
	sessID := uuid.New()
	chanID := uuid.New()
	resvID := uuid.New()
	csID := uuid.New()
	tktID := uuid.New()
	djID := uuid.New()

	slug := fmt.Sprintf("test-org-%s", orgID.String()[:8])

	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seedMinimalDeliveryData: %s\n  error: %v", sql[:min(60, len(sql))], err)
		}
	}

	// 1. Organization (no FK)
	exec(`INSERT INTO organizations (id, name, slug, country) VALUES ($1, 'Test Org', $2, 'RU')`, orgID, slug)

	// 2. Event (dates live on sessions since migration 0080)
	exec(`INSERT INTO events (id, org_id, name)
	      VALUES ($1, $2, 'Test Event')`, evtID, orgID)

	// 2b. Venue (sessions.venue_id is NOT NULL since migration 0079)
	venueID := uuid.New()
	exec(`INSERT INTO venues (id, org_id, name)
	      VALUES ($1, $2, 'Test Venue')`, venueID, orgID)

	// 3. Session (owns venue + currency since migrations 0079/0081)
	exec(`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, currency, currency_source)
	      VALUES ($1, $2, $3, now() + interval '1 day', now() + interval '1 day 2 hours', 100, 'USD', 'override')`, sessID, evtID, venueID)

	// 4. Sales channel
	exec(`INSERT INTO sales_channels (id, org_id, name, payment_mode, provider)
	      VALUES ($1, $2, 'Test Channel', 'direct_merchant', 'stripe')`, chanID, orgID)

	// 5. Reservation (converted, expires in the future)
	exec(`INSERT INTO reservations (id, org_id, channel_id, session_id, quantity, state, expires_at, converted_at)
	      VALUES ($1, $2, $3, $4, 1, 'converted', now() + interval '1 hour', now())`,
		resvID, orgID, chanID, sessID)

	// 6. Checkout session (completed)
	exec(`INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state)
	      VALUES ($1, $2, $3, $4, 'completed')`, csID, orgID, chanID, resvID)

	// 7. Ticket (holder_email is nullable; NULL when recipientEmail is empty)
	var holderEmail *string
	if recipientEmail != "" {
		holderEmail = &recipientEmail
	}
	exec(`INSERT INTO tickets (id, checkout_session_id, session_id, holder_email)
	      VALUES ($1, $2, $3, $4)`, tktID, csID, sessID, holderEmail)

	// 8. Delivery job (pending)
	exec(`INSERT INTO delivery_jobs (id, ticket_id, recipient_email)
	      VALUES ($1, $2, $3)`, djID, tktID, holderEmail)

	return seedResult{
		TicketID:       tktID,
		DeliveryJobID:  djID,
		RecipientEmail: recipientEmail,
	}
}

// min returns the smaller of a and b.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

// deliveryJobStatus reads the current status of a delivery_jobs row.
func deliveryJobStatus(ctx context.Context, t *testing.T, pool *pgxpool.Pool, id uuid.UUID) string {
	t.Helper()
	var status string
	if err := pool.QueryRow(ctx,
		`SELECT status FROM delivery_jobs WHERE id = $1`, id,
	).Scan(&status); err != nil {
		t.Fatalf("deliveryJobStatus: %v", err)
	}
	return status
}

// buildSMTPSender constructs a real email.SMTPSender pointed at addr (host:port).
func buildSMTPSender(addr string) email.Sender {
	host, port, _ := net.SplitHostPort(addr)
	return email.NewSMTPSender(email.SMTPConfig{
		Host:   host,
		Port:   port,
		From:   "test@arena.example.com",
		UseTLS: false,
	})
}

// invokeHandler calls the delivery handler synchronously with a
// minimal ticket.deliver payload.
func invokeHandler(ctx context.Context, t *testing.T, pool *pgxpool.Pool, seed seedResult, sender email.Sender) error {
	t.Helper()
	queries := gen.New(pool)
	handler := NewHandler(HandlerOptions{
		TicketQueries:      queries,
		DeliveryJobQueries: queries,
		Sender:             sender,
		Logger:             slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	payload, err := json.Marshal(Payload{
		TicketID:     seed.TicketID.String(),
		EventName:    "Integration Test Event",
		SessionStart: time.Now().Add(24 * time.Hour),
		HolderName:   "Test Holder",
	})
	if err != nil {
		t.Fatalf("invokeHandler: marshal payload: %v", err)
	}
	return handler(ctx, payload)
}

// ─────────────────────────────────────────────────────────────────────────────
// Tests
// ─────────────────────────────────────────────────────────────────────────────

// TestDelivery_SMTPSuccess proves the full happy-path state transition:
//
//	queued (pending) → processing → sent
//
// A real in-process SMTP capture server accepts the connection.
// The captured email must contain a PDF attachment (multipart/mixed with
// application/pdf part).
func TestDelivery_SMTPSuccess(t *testing.T) {
	pool, cleanup := pgtest.NewTestDB(t)
	defer cleanup()

	ctx := context.Background()
	smtp := newSMTPCaptureServer(t)
	seed := seedMinimalDeliveryData(ctx, t, pool, "holder@example.com")

	// Verify initial status is 'pending'.
	if got := deliveryJobStatus(ctx, t, pool, seed.DeliveryJobID); got != StatusPending {
		t.Fatalf("expected initial status=%q, got %q", StatusPending, got)
	}

	sender := buildSMTPSender(smtp.Addr)
	err := invokeHandler(ctx, t, pool, seed, sender)
	if err != nil {
		t.Fatalf("handler returned unexpected error: %v", err)
	}

	// ── Assert final status = 'sent' ─────────────────────────────────────
	if got := deliveryJobStatus(ctx, t, pool, seed.DeliveryJobID); got != StatusSent {
		t.Fatalf("expected delivery_jobs.status=%q after SMTP success, got %q", StatusSent, got)
	}

	// ── Assert email was captured by the server ───────────────────────────
	select {
	case raw := <-smtp.Captured:
		rawStr := string(raw)

		// Must have a PDF attachment (Content-Type: application/pdf).
		if !strings.Contains(rawStr, "application/pdf") {
			t.Errorf("captured email does not contain application/pdf attachment")
		}

		// Must be addressed to the holder.
		if !strings.Contains(rawStr, "holder@example.com") {
			t.Errorf("captured email does not contain recipient address")
		}

		t.Logf("SMTP success: captured %d bytes, has PDF: %v",
			len(raw), strings.Contains(rawStr, "application/pdf"))

	case <-time.After(5 * time.Second):
		t.Fatal("SMTP capture server did not receive a message within 5s")
	}
}

// TestDelivery_SMTPRefused proves that an SMTP refusal does NOT produce
// delivery_jobs.status='sent'. The handler must return an error (retriable)
// and the row must stay in a non-sent state.
func TestDelivery_SMTPRefused(t *testing.T) {
	pool, cleanup := pgtest.NewTestDB(t)
	defer cleanup()

	ctx := context.Background()
	refuse := newSMTPRefuseServer(t)
	seed := seedMinimalDeliveryData(ctx, t, pool, "holder@refuse-test.com")

	sender := buildSMTPSender(refuse.Addr)
	err := invokeHandler(ctx, t, pool, seed, sender)

	// The handler must return a non-nil error when SMTP fails, so the worker
	// knows to retry.
	if err == nil {
		t.Fatalf("handler returned nil on SMTP refusal; expected a retriable error")
	}
	t.Logf("SMTP refusal returned expected error: %v", err)

	// The delivery_jobs row must NOT be 'sent'.
	status := deliveryJobStatus(ctx, t, pool, seed.DeliveryJobID)
	if status == StatusSent {
		t.Fatalf("delivery_jobs.status must not be %q after SMTP refusal; got %q", StatusSent, status)
	}
	t.Logf("SMTP refusal: delivery_jobs.status=%q (expected non-sent) ✓", status)
}

// TestDelivery_DevSenderProducesDisabled proves that using a dev-only sender
// (LogSender) causes the delivery_jobs row to be marked 'disabled', not 'sent'.
// This satisfies the PR-03 requirement: LogSender cannot masquerade as a
// successful production delivery.
func TestDelivery_DevSenderProducesDisabled(t *testing.T) {
	pool, cleanup := pgtest.NewTestDB(t)
	defer cleanup()

	ctx := context.Background()
	seed := seedMinimalDeliveryData(ctx, t, pool, "holder@log-test.com")

	logSender := &email.LogSender{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	err := invokeHandler(ctx, t, pool, seed, logSender)
	if err != nil {
		t.Fatalf("handler returned unexpected error with LogSender: %v", err)
	}

	status := deliveryJobStatus(ctx, t, pool, seed.DeliveryJobID)
	if status != StatusDisabled {
		t.Fatalf("expected delivery_jobs.status=%q with LogSender, got %q", StatusDisabled, status)
	}
	t.Logf("LogSender: delivery_jobs.status=%q ✓", status)
}

// TestDelivery_NilSenderProducesDisabled proves that nil Sender (no email
// configured at all) also produces 'disabled', not 'sent'.
func TestDelivery_NilSenderProducesDisabled(t *testing.T) {
	pool, cleanup := pgtest.NewTestDB(t)
	defer cleanup()

	ctx := context.Background()
	seed := seedMinimalDeliveryData(ctx, t, pool, "holder@nil-sender.com")

	err := invokeHandler(ctx, t, pool, seed, nil)
	if err != nil {
		t.Fatalf("handler returned unexpected error with nil Sender: %v", err)
	}

	status := deliveryJobStatus(ctx, t, pool, seed.DeliveryJobID)
	if status != StatusDisabled {
		t.Fatalf("expected delivery_jobs.status=%q with nil Sender, got %q", StatusDisabled, status)
	}
	t.Logf("nil Sender: delivery_jobs.status=%q ✓", status)
}

// TestDelivery_NoEmailProducesSkipped proves that when no recipient email
// address is available, the delivery_jobs row is marked 'skipped', not 'sent'.
func TestDelivery_NoEmailProducesSkipped(t *testing.T) {
	pool, cleanup := pgtest.NewTestDB(t)
	defer cleanup()

	ctx := context.Background()
	// Seed with an empty recipient email so neither delivery_jobs nor ticket
	// provides an address.
	seed := seedMinimalDeliveryData(ctx, t, pool, "" /* no email */)

	// A capture server is started but should NOT receive any message.
	smtp := newSMTPCaptureServer(t)
	sender := buildSMTPSender(smtp.Addr)

	err := invokeHandler(ctx, t, pool, seed, sender)
	if err != nil {
		t.Fatalf("handler returned unexpected error for no-email ticket: %v", err)
	}

	status := deliveryJobStatus(ctx, t, pool, seed.DeliveryJobID)
	if status != StatusSkipped {
		t.Fatalf("expected delivery_jobs.status=%q with no email address, got %q", StatusSkipped, status)
	}
	t.Logf("no email: delivery_jobs.status=%q ✓", status)

	// Verify SMTP server received nothing (no spurious send).
	select {
	case <-smtp.Captured:
		t.Fatalf("SMTP capture server received a message but should not have (no recipient email)")
	case <-time.After(200 * time.Millisecond):
		// Expected: no message received.
	}
}

// TestDelivery_IdempotencyProcessingSkip proves that if the delivery_jobs row
// is already in 'processing' state when the handler is invoked (simulating a
// concurrent or retry scenario), the handler skips the send and returns nil,
// preventing a duplicate email.
func TestDelivery_IdempotencyProcessingSkip(t *testing.T) {
	pool, cleanup := pgtest.NewTestDB(t)
	defer cleanup()

	ctx := context.Background()
	smtp := newSMTPCaptureServer(t)
	seed := seedMinimalDeliveryData(ctx, t, pool, "holder@idempotency.com")

	// Manually advance the row to 'processing' state (simulating a prior handler
	// invocation that claimed the row but has not yet completed).
	_, err := pool.Exec(ctx,
		`UPDATE delivery_jobs SET status='processing', processing_at=now() WHERE id=$1`,
		seed.DeliveryJobID,
	)
	if err != nil {
		t.Fatalf("advance to processing: %v", err)
	}

	sender := buildSMTPSender(smtp.Addr)
	err = invokeHandler(ctx, t, pool, seed, sender)
	if err != nil {
		t.Fatalf("handler returned unexpected error on processing-skip: %v", err)
	}

	// The row must still be 'processing' — the handler must NOT have changed it
	// to 'sent' without actually sending.
	status := deliveryJobStatus(ctx, t, pool, seed.DeliveryJobID)
	if status == StatusSent {
		t.Fatalf("delivery_jobs.status became %q after idempotency skip; expected 'processing'", StatusSent)
	}

	// Verify SMTP server received nothing (skip = no duplicate send).
	select {
	case <-smtp.Captured:
		t.Fatalf("SMTP capture server received a message during idempotency skip; duplicate send detected")
	case <-time.After(300 * time.Millisecond):
		t.Logf("idempotency: no duplicate send when row is already 'processing' ✓")
	}
}
