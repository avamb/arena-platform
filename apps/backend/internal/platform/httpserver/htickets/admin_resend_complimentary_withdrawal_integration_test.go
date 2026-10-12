//go:build integration

// admin_resend_complimentary_withdrawal_integration_test.go — an operator-
// issued invitation (a complimentary ticket, no checkout session, no order)
// resent through POST /v1/admin/tickets/{id}/delivery/resend must never carry
// the "no right of withdrawal" sentence, even when the issuing organization
// sits in the EU/EEA. The resend enqueues a plain ticket.deliver payload (no
// invitation template), so the guard has to come from the ticket itself.
//
// The real resend handler runs first; the job it enqueued is then handed to the
// real delivery handler with a capturing sender.
//
// Run with:
//
//	go test -tags integration ./apps/backend/internal/platform/httpserver/htickets/ \
//	    -run TestAdminResendComplimentaryIntegration
package htickets

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/email"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/delivery"
)

type resendCaptureSender struct {
	mu   sync.Mutex
	msgs []email.Message
}

func (c *resendCaptureSender) Send(_ context.Context, m email.Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.msgs = append(c.msgs, m)
	return nil
}

func TestAdminResendComplimentaryIntegration_NoWithdrawalSentence(t *testing.T) {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		t.Skip("DATABASE_URL not set; skipping integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()
	q := gen.New(pool)

	nonce := uuid.NewString()[:8]
	orgID, eventID, venueID, sessionID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("fixture: %v", err)
		}
	}
	// An EU seller: without the guard this ticket would print the sentence.
	exec(`INSERT INTO organizations (id, name, slug, country) VALUES ($1, $2, $3, 'ES')`,
		orgID, "Invitation Org "+nonce, "invitation-org-"+nonce)
	exec(`INSERT INTO events (id, org_id, name) VALUES ($1, $2, $3)`, eventID, orgID, "Invitation Event "+nonce)
	exec(`INSERT INTO venues (id, org_id, name, timezone) VALUES ($1, $2, 'Invitation Venue', 'Europe/Madrid')`, venueID, orgID)
	exec(`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, currency, currency_source)
	      VALUES ($1, $2, $3, now() + interval '3 day', now() + interval '3 day 2 hours', 10, 'EUR', 'override')`,
		sessionID, eventID, venueID)

	holder := "guest-" + nonce + "@example.com"
	iss, err := q.InsertComplimentaryIssuance(ctx, orgID, sessionID, nil, 1, []string{holder}, "batch-"+nonce, nil, nil)
	if err != nil {
		t.Fatalf("InsertComplimentaryIssuance: %v", err)
	}
	tk, err := q.InsertComplimentaryTicket(ctx, iss.ID, sessionID, nil, &holder, nil, nil)
	if err != nil {
		t.Fatalf("InsertComplimentaryTicket: %v", err)
	}
	defer func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM worker_jobs WHERE job_type = $1 AND payload->>'ticket_id' = $2`, delivery.JobType, tk.ID.String())
		_, _ = pool.Exec(c, `DELETE FROM delivery_jobs WHERE ticket_id = $1`, tk.ID)
		_, _ = pool.Exec(c, `DELETE FROM ticket_credentials WHERE ticket_id = $1`, tk.ID)
		_, _ = pool.Exec(c, `DELETE FROM barcodes WHERE ticket_id = $1`, tk.ID)
		_, _ = pool.Exec(c, `DELETE FROM tickets WHERE id = $1`, tk.ID)
		_, _ = pool.Exec(c, `DELETE FROM complimentary_issuances WHERE id = $1`, iss.ID)
		_, _ = pool.Exec(c, `DELETE FROM sessions WHERE id = $1`, sessionID)
		_, _ = pool.Exec(c, `DELETE FROM venues WHERE id = $1`, venueID)
		_, _ = pool.Exec(c, `DELETE FROM events WHERE id = $1`, eventID)
		_, _ = pool.Exec(c, `DELETE FROM organizations WHERE id = $1`, orgID)
	}()
	if _, err := q.InsertDeliveryJob(ctx, tk.ID, &holder); err != nil {
		t.Fatalf("InsertDeliveryJob: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := New(q, q, q, q, q, q, q, nil, pool, pool, nil, logger, nil, nil, nil).
		WithRowOrgAccess(func(http.ResponseWriter, *http.Request, uuid.UUID, bool, string, string) bool { return true })

	// ── The real admin resend ────────────────────────────────────────────────
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/admin/tickets/"+tk.ID.String()+"/delivery/resend", nil)
	r.Header.Set("X-Admin-Reason", "integration: resend an invitation")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", tk.ID.String())
	r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
	h.HandleAdminResendTicketDelivery(w, r)
	if w.Code != http.StatusAccepted {
		t.Fatalf("resend status = %d, want 202 (body: %s)", w.Code, w.Body.String())
	}

	// ── The job it enqueued, run by the real delivery handler ───────────────
	var payload []byte
	if err := pool.QueryRow(ctx,
		`SELECT payload FROM worker_jobs WHERE job_type = $1 AND payload->>'ticket_id' = $2`,
		delivery.JobType, tk.ID.String(),
	).Scan(&payload); err != nil {
		t.Fatalf("read the enqueued worker job: %v", err)
	}
	cs := &resendCaptureSender{}
	run := delivery.NewHandler(delivery.HandlerOptions{
		TicketQueries:      q,
		DeliveryJobQueries: q,
		CredentialQueries:  q,
		Sender:             cs,
		Logger:             logger,
	})
	if err := run(ctx, payload); err != nil {
		t.Fatalf("delivery handler: %v", err)
	}
	if len(cs.msgs) != 1 {
		t.Fatalf("captured %d messages, want 1 (the resent invitation must still be delivered)", len(cs.msgs))
	}
	const sentence = "not covered by the right of withdrawal"
	if strings.Contains(cs.msgs[0].HTMLBody, sentence) || strings.Contains(cs.msgs[0].TextBody, sentence) {
		t.Fatal("a resent invitation printed the withdrawal sentence")
	}
}
