//go:build integration

// admin_ticket_delivery_resend_integration_test.go — integration test proving
// the fix to HandleAdminResendTicketDelivery (admin_ticket_delivery.go):
// resend must call RequeueDeliveryJob, not InsertDeliveryJob.
//
// `delivery_jobs` holds at most one row per ticket, and InsertDeliveryJob's
// SQL is `ON CONFLICT (ticket_id) DO UPDATE SET ticket_id = EXCLUDED.ticket_id`
// — a deliberate no-op on conflict, so a ticket already in a terminal state
// ('sent'/'failed'/'skipped'/'disabled') stayed in that state. The worker's
// ClaimDeliveryJobForProcessing only transitions 'pending' -> 'processing',
// so the enqueued worker job silently skipped the send and the admin saw 202
// + "success" with no e-mail ever sent. RequeueDeliveryJob resets the row
// back to status='pending', attempts=0, last_error=NULL, sent_at=NULL,
// processing_at=NULL, queued_at=now().
//
// Run with:
//
//	go test -tags integration ./apps/backend/internal/platform/httpserver/htickets/ \
//	    -run TestAdminResendTicketDeliveryIntegration
package htickets

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/delivery"
)

// TestAdminResendTicketDeliveryIntegration_ResetsTerminalJobToPending proves
// that POST /v1/admin/tickets/{id}/delivery/resend, driven through the real
// handler, resets a delivery_jobs row that is already in a terminal state
// ('sent', attempts>0, sent_at set) back to 'pending' with attempts=0 and
// sent_at cleared — and returns that reset row in the response body. Before
// the fix (handler calling InsertDeliveryJob) this row would never move: the
// test would see status still "sent" and attempts still >0.
func TestAdminResendTicketDeliveryIntegration_ResetsTerminalJobToPending(t *testing.T) {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		t.Skip("DATABASE_URL not set; skipping admin resend delivery integration test")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("pgxpool.New: %v — DATABASE_URL is set, so a connection failure must fail the gate", err)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("pool.Ping: %v — DATABASE_URL is set, so an unreachable database must fail the gate", err)
	}

	q := gen.New(pool)

	// ── Find an existing (org, channel, session) triple ──────────────────────
	// arena-seed guarantees this exists in CI (feature #388).
	var orgID, channelID, sessionID uuid.UUID
	row := pool.QueryRow(ctx, `
		SELECT sc.org_id, sc.id, s.id
		FROM   sales_channels sc
		JOIN   events e   ON e.org_id = sc.org_id
		JOIN   sessions s ON s.event_id = e.id
		JOIN   inventory_ledger il ON il.session_id = s.id AND il.tier_id IS NULL
		LIMIT  1
	`)
	if err := row.Scan(&orgID, &channelID, &sessionID); err != nil {
		t.Fatalf("no (org, channel, session) triple with GA inventory found in DB: %v — "+
			"run arena-seed against the migrated database first", err)
	}
	t.Logf("using org=%s channel=%s session=%s", orgID, channelID, sessionID)

	// ── Reserve + activate a 1-unit GA reservation, then issue a ticket ───────
	qty := int32(1)
	futureExpiry := time.Now().UTC().Add(10 * time.Minute)
	reservation, err := q.InsertReservation(ctx, orgID, channelID, sessionID, nil, nil, qty, futureExpiry)
	if err != nil {
		t.Fatalf("InsertReservation: %v", err)
	}
	defer func() {
		_, _ = pool.Exec(ctx, "DELETE FROM reservations WHERE id = $1", reservation.ID)
	}()

	reservation, err = q.UpdateReservationState(ctx, reservation.ID, "active")
	if err != nil {
		t.Fatalf("UpdateReservationState(active): %v", err)
	}
	reservation, err = q.UpdateReservationState(ctx, reservation.ID, "converted")
	if err != nil {
		t.Fatalf("UpdateReservationState(converted): %v", err)
	}

	cs, err := q.InsertCheckoutSession(ctx, orgID, channelID, reservation.ID, nil)
	if err != nil {
		t.Fatalf("InsertCheckoutSession: %v", err)
	}
	defer func() {
		_, _ = pool.Exec(ctx, "DELETE FROM checkout_sessions WHERE id = $1", cs.ID)
	}()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	h := New(q, q, q, q, q, q, q, nil, pool, pool, nil, logger, nil, nil, nil)
	tickets, err := h.IssueTicketsForCheckout(ctx, cs)
	if err != nil {
		t.Fatalf("IssueTicketsForCheckout: %v", err)
	}
	if len(tickets) != 1 {
		t.Fatalf("IssueTicketsForCheckout returned %d tickets, want 1", len(tickets))
	}
	ticket := tickets[0]
	defer func() {
		// worker_jobs has no FK to delivery_jobs/tickets, so it never cascades
		// (AGENTS.md: sweep worker_jobs rows explicitly, before the rows their
		// payload references).
		_, _ = pool.Exec(ctx, "DELETE FROM worker_jobs WHERE job_type = $1 AND payload->>'ticket_id' = $2",
			delivery.JobType, ticket.ID.String())
		_, _ = pool.Exec(ctx, "DELETE FROM delivery_jobs WHERE ticket_id = $1", ticket.ID)
		_, _ = pool.Exec(ctx, "DELETE FROM barcodes WHERE ticket_id = $1", ticket.ID)
		_, _ = pool.Exec(ctx, "DELETE FROM ticket_credentials WHERE ticket_id = $1", ticket.ID)
		_, _ = pool.Exec(ctx, "DELETE FROM tickets WHERE id = $1", ticket.ID)
	}()

	// ── Seed a delivery_jobs row and drive it into a TERMINAL state through
	// the real queries — exactly what an earlier, already-delivered (or
	// already-failed) resend would have left behind ──────────────────────────
	dj, err := q.InsertDeliveryJob(ctx, ticket.ID, ticket.HolderEmail)
	if err != nil {
		t.Fatalf("InsertDeliveryJob (seed): %v", err)
	}
	dj, err = q.UpdateDeliveryJobStatus(ctx, dj.ID, "sent", nil)
	if err != nil {
		t.Fatalf("UpdateDeliveryJobStatus(sent) (seed): %v", err)
	}
	if dj.Status != "sent" || dj.Attempts < 1 || dj.SentAt == nil {
		t.Fatalf("seed fixture is not in the expected terminal state: status=%q attempts=%d sent_at=%v",
			dj.Status, dj.Attempts, dj.SentAt)
	}

	// ── Drive the REAL handler exactly as the router would, via a chi route
	// context carrying the "id" path param (same pattern as
	// admin_onboarding_email_integration_test.go) ────────────────────────────
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/admin/tickets/"+ticket.ID.String()+"/delivery/resend", nil)
	r.Header.Set("X-Admin-Reason", "integration: prove resend requeues a terminal delivery job")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", ticket.ID.String())
	r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))

	h.HandleAdminResendTicketDelivery(w, r)

	if w.Code != http.StatusAccepted {
		t.Fatalf("POST .../delivery/resend: status = %d, want 202 (body: %s)", w.Code, w.Body.String())
	}

	var resp struct {
		Delivery struct {
			Status   string  `json:"status"`
			Attempts int32   `json:"attempts"`
			SentAt   *string `json:"sent_at"`
		} `json:"delivery"`
		WorkerJobID string `json:"worker_job_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v (body: %s)", err, w.Body.String())
	}

	// ── The response body must reflect the RESET row, not the stale terminal
	// one — this is the handler returning what it wrote ──────────────────────
	if resp.Delivery.Status != "pending" {
		t.Errorf("response delivery.status = %q, want %q (handler must return the requeued row)",
			resp.Delivery.Status, "pending")
	}
	if resp.Delivery.Attempts != 0 {
		t.Errorf("response delivery.attempts = %d, want 0", resp.Delivery.Attempts)
	}
	if resp.Delivery.SentAt != nil {
		t.Errorf("response delivery.sent_at = %v, want null", *resp.Delivery.SentAt)
	}
	if resp.WorkerJobID == "" {
		t.Error("response worker_job_id is empty; a companion worker_jobs row must have been enqueued")
	}

	// ── The actual delivery_jobs row in the database must have moved out of
	// the terminal state too — this is the whole point of the fix. Before it
	// (InsertDeliveryJob's no-op-on-conflict), this row would still read
	// status='sent', attempts=1, sent_at=<non-nil> here ──────────────────────
	after, err := q.GetDeliveryJobByTicketID(ctx, ticket.ID)
	if err != nil {
		t.Fatalf("GetDeliveryJobByTicketID (after resend): %v", err)
	}
	if after.Status != "pending" {
		t.Errorf("delivery_jobs.status = %q after resend, want %q — the terminal row was not requeued", after.Status, "pending")
	}
	if after.Attempts != 0 {
		t.Errorf("delivery_jobs.attempts = %d after resend, want 0", after.Attempts)
	}
	if after.SentAt != nil {
		t.Errorf("delivery_jobs.sent_at = %v after resend, want NULL", *after.SentAt)
	}
	if after.LastError != nil {
		t.Errorf("delivery_jobs.last_error = %v after resend, want NULL", *after.LastError)
	}
	if after.ProcessingAt != nil {
		t.Errorf("delivery_jobs.processing_at = %v after resend, want NULL", *after.ProcessingAt)
	}

	// The worker's claim step only ever transitions 'pending' -> 'processing',
	// so the requeued row must actually be claimable — proving the whole point
	// of the fix end to end, not just the column values.
	claimed, err := q.ClaimDeliveryJobForProcessing(ctx, after.ID)
	if err != nil {
		t.Fatalf("ClaimDeliveryJobForProcessing on the requeued row: %v — the worker would silently skip this send", err)
	}
	if claimed.Status != "processing" {
		t.Errorf("claimed.Status = %q, want %q", claimed.Status, "processing")
	}
}
