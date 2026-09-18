//go:build integration

// tickets_reconciliation_integration_test.go — GET /v1/admin/tickets
// event_id/session_id filters and the reconciliation console's extra
// response fields (feature TICKET-BOARD, owner decision 2026-09-18).
//
// Uses a direct DATABASE_URL connection rather than internal/tests/pgtest,
// which panics on this Windows host ("rootless Docker is not supported" —
// see AGENTS.md); this mirrors the pattern in
// apps/backend/internal/platform/macs/macs_roundtrip_integration_test.go.
//
// Prerequisites:
//
//	DATABASE_URL=postgres://arena:arena@localhost:45432/arena_ci_macs?sslmode=disable
//	(migrated to head >= 0101, seeded)
//
// Run with:
//
//	go test -tags integration -run TestAdminTickets_Reconciliation ./apps/backend/internal/platform/httpserver/hiam/
package hiam

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

func reconciliationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping admin tickets reconciliation integration test")
	}
	if !strings.HasPrefix(dsn, "postgres") {
		t.Skipf("DATABASE_URL %q is not a Postgres DSN; skipping", dsn)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("reconciliationPool: open: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatalf("reconciliationPool: ping: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestAdminTickets_Reconciliation_EventAndSessionFilters(t *testing.T) {
	pool := reconciliationPool(t)
	ctx := context.Background()
	q := gen.New(pool)

	suffix := uuid.New().String()[:8]
	orgID := uuid.New()
	venueID := uuid.New()
	eventID := uuid.New()
	sessionID := uuid.New()
	otherEventID := uuid.New()
	otherSessionID := uuid.New()
	channelID := uuid.New()

	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed exec: %v\nSQL: %s", err, sql)
		}
	}

	mustExec(`INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`,
		orgID, "Reconciliation Org "+suffix, "reconciliation-"+suffix)
	mustExec(`INSERT INTO venues (id, org_id, name) VALUES ($1, $2, $3)`,
		venueID, orgID, "Reconciliation Venue")
	mustExec(`INSERT INTO events (id, org_id, name, status, visibility) VALUES ($1, $2, $3, 'draft', 'private')`,
		eventID, orgID, "Reconciliation Event")
	mustExec(`INSERT INTO events (id, org_id, name, status, visibility) VALUES ($1, $2, $3, 'draft', 'private')`,
		otherEventID, orgID, "Other Event")
	mustExec(`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, status, admission_mode, currency, currency_source)
		VALUES ($1, $2, $3, NOW()+INTERVAL '90 days', NOW()+INTERVAL '90 days 3 hours', 100, 'draft', 'general_admission', 'RUB', 'override')`,
		sessionID, eventID, venueID)
	mustExec(`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, status, admission_mode, currency, currency_source)
		VALUES ($1, $2, $3, NOW()+INTERVAL '90 days', NOW()+INTERVAL '90 days 3 hours', 100, 'draft', 'general_admission', 'RUB', 'override')`,
		otherSessionID, otherEventID, venueID)
	mustExec(`INSERT INTO sales_channels (id, org_id, name) VALUES ($1, $2, $3)`,
		channelID, orgID, "Reconciliation Channel")

	t.Cleanup(func() {
		c := context.Background()
		pool.Exec(c, `DELETE FROM tickets WHERE session_id IN ($1, $2)`, sessionID, otherSessionID)
		pool.Exec(c, `DELETE FROM checkout_sessions WHERE org_id=$1`, orgID)
		pool.Exec(c, `DELETE FROM reservations WHERE session_id IN ($1, $2)`, sessionID, otherSessionID)
		pool.Exec(c, `DELETE FROM sessions WHERE id IN ($1, $2)`, sessionID, otherSessionID)
		pool.Exec(c, `DELETE FROM events WHERE id IN ($1, $2)`, eventID, otherEventID)
		pool.Exec(c, `DELETE FROM sales_channels WHERE id=$1`, channelID)
		pool.Exec(c, `DELETE FROM venues WHERE id=$1`, venueID)
		pool.Exec(c, `DELETE FROM organizations WHERE id=$1`, orgID)
	})

	tierID := uuid.New()
	mustExec(`INSERT INTO ticket_tiers (id, session_id, name, pricing_mode, price_amount, currency) VALUES ($1, $2, $3, 'fixed', 1000, 'RUB')`,
		tierID, sessionID, "VIP Reconciliation")

	resID := uuid.New()
	mustExec(`INSERT INTO reservations (id, org_id, channel_id, session_id, quantity, expires_at)
		VALUES ($1, $2, $3, $4, 1, $5)`,
		resID, orgID, channelID, sessionID, time.Now().Add(30*time.Minute))
	csID := uuid.New()
	mustExec(`INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state, subtotal, discount, total, currency)
		VALUES ($1, $2, $3, $4, 'completed', 1000, 0, 1000, 'RUB')`,
		csID, orgID, channelID, resID)

	ticketID := uuid.New()
	mustExec(`INSERT INTO tickets (id, session_id, checkout_session_id, tier_id, status, issued_at, holder_email)
		VALUES ($1, $2, $3, $4, 'active', NOW(), $5)`,
		ticketID, sessionID, csID, tierID, "buyer-"+suffix+"@example.com")

	resOtherID := uuid.New()
	mustExec(`INSERT INTO reservations (id, org_id, channel_id, session_id, quantity, expires_at)
		VALUES ($1, $2, $3, $4, 1, $5)`,
		resOtherID, orgID, channelID, otherSessionID, time.Now().Add(30*time.Minute))
	csOtherID := uuid.New()
	mustExec(`INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state, subtotal, discount, total, currency)
		VALUES ($1, $2, $3, $4, 'completed', 1000, 0, 1000, 'RUB')`,
		csOtherID, orgID, channelID, resOtherID)
	otherTicketID := uuid.New()
	mustExec(`INSERT INTO tickets (id, session_id, checkout_session_id, status, issued_at)
		VALUES ($1, $2, $3, 'active', NOW())`,
		otherTicketID, otherSessionID, csOtherID)

	h := New(q, q, q, pool, nil, slog.Default(), nil, nil)

	doList := func(query string) map[string]any {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/v1/admin/tickets?"+query, nil)
		req.Header.Set("X-Admin-Reason", "reconciliation test")
		rec := httptest.NewRecorder()
		h.HandleSuperadminListTickets(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /v1/admin/tickets?%s = %d: %s", query, rec.Code, rec.Body.String())
		}
		var resp map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		return resp
	}

	// event_id scopes the list to the chosen event only.
	resp := doList("event_id=" + eventID.String())
	tickets, _ := resp["tickets"].([]any)
	if len(tickets) != 1 {
		t.Fatalf("event_id filter: got %d tickets, want 1: %+v", len(tickets), resp)
	}
	row, _ := tickets[0].(map[string]any)
	if row["id"] != ticketID.String() {
		t.Errorf("event_id filter: got ticket %v, want %s", row["id"], ticketID)
	}
	if row["event_id"] != eventID.String() {
		t.Errorf("row missing event_id: %+v", row)
	}
	if row["category"] != "VIP Reconciliation" {
		t.Errorf("row category = %v, want %q", row["category"], "VIP Reconciliation")
	}
	if row["barcode"] == nil || row["barcode"] == "" {
		t.Errorf("row barcode should always have a value (derived fallback), got %v", row["barcode"])
	}
	// This ticket predates the orders aggregate (no tickets.order_id), so
	// order_system_id must be explicitly null, not omitted.
	if v, ok := row["order_system_id"]; !ok || v != nil {
		t.Errorf("row order_system_id = %v (ok=%v), want explicit nil", v, ok)
	}

	// session_id further narrows within the event (both tickets share the
	// org, but only one is in this session).
	resp2 := doList("session_id=" + sessionID.String())
	tickets2, _ := resp2["tickets"].([]any)
	if len(tickets2) != 1 {
		t.Fatalf("session_id filter: got %d tickets, want 1: %+v", len(tickets2), resp2)
	}

	// The other event's session must never leak into the first event's filter.
	respOther := doList("event_id=" + otherEventID.String())
	ticketsOther, _ := respOther["tickets"].([]any)
	if len(ticketsOther) != 1 {
		t.Fatalf("other event_id filter: got %d tickets, want 1: %+v", len(ticketsOther), respOther)
	}
	rowOther, _ := ticketsOther[0].(map[string]any)
	if rowOther["id"] == ticketID.String() {
		t.Error("other event's filter leaked the first event's ticket")
	}

	// Malformed event_id is a 400, not a 500 or a silently-ignored filter.
	badReq := httptest.NewRequest(http.MethodGet, "/v1/admin/tickets?event_id=not-a-uuid", nil)
	badReq.Header.Set("X-Admin-Reason", "reconciliation test")
	badRec := httptest.NewRecorder()
	h.HandleSuperadminListTickets(badRec, badReq)
	if badRec.Code != http.StatusBadRequest {
		t.Errorf("malformed event_id: status = %d, want 400: %s", badRec.Code, badRec.Body.String())
	}
}
