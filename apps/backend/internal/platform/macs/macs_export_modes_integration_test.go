//go:build integration

// Package macs — export mode integration test (owner decision 2026-09-18,
// see export_filter.go). MACS's file importer ignores holderStatus and
// stores every imported ticket as valid, so the scanner is now fed a
// "valid tickets" file plus a separate "revoked" list. This test proves the
// filtering runs correctly against a real seeded session mixing an active,
// a refunded (cancelled+refund_date) and a cancelled-without-refund ticket.
//
// Prerequisites:
//
//	DATABASE_URL=postgres://arena:arena@localhost:55432/arena?sslmode=disable
//	(migrated to head >= 0089)
//
// Run with:
//
//	go test -tags integration -run TestMACS_ExportModes ./apps/backend/internal/platform/macs/
package macs_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/macs"
)

func TestMACS_ExportModes_Integration(t *testing.T) {
	pool := roundtripPool(t)
	ctx := context.Background()

	suffix := uuid.New().String()[:8]
	orgID := uuid.New()
	cityID := uuid.New()
	venueID := uuid.New()
	eventID := uuid.New()
	sessionID := uuid.New()
	channelID := uuid.New()
	citySlug := "macs-modes-city-" + suffix

	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed exec: %v\nSQL: %s", err, sql)
		}
	}

	var countryID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM countries WHERE iso2='IL' LIMIT 1`).Scan(&countryID); err != nil {
		t.Skipf("IL country not found (migration 0006 not applied?): %v", err)
	}
	mustExec(`INSERT INTO cities (id, country_id, slug) VALUES ($1, $2, $3)`,
		cityID, countryID, citySlug)
	mustExec(`INSERT INTO i18n_text (namespace, key, locale, value) VALUES ('geo.cities', $1, 'en', 'Modes City')`,
		citySlug)
	mustExec(`INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`,
		orgID, "MACS Modes Org "+suffix, "macs-modes-"+suffix)
	mustExec(`INSERT INTO venues (id, org_id, name, city_id) VALUES ($1, $2, $3, $4)`,
		venueID, orgID, "Modes Venue", cityID)
	mustExec(`INSERT INTO events (id, org_id, name, status, visibility) VALUES ($1, $2, $3, 'draft', 'private')`,
		eventID, orgID, "Modes Event")
	mustExec(`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, status, admission_mode, currency, currency_source)
		VALUES ($1, $2, $3, NOW()+INTERVAL '90 days', NOW()+INTERVAL '90 days 3 hours', 100, 'draft', 'general_admission', 'RUB', 'override')`,
		sessionID, eventID, venueID)
	mustExec(`INSERT INTO sales_channels (id, org_id, name) VALUES ($1, $2, $3)`,
		channelID, orgID, "Modes Channel")

	t.Cleanup(func() {
		c := context.Background()
		pool.Exec(c, `DELETE FROM tickets WHERE session_id=$1`, sessionID)
		pool.Exec(c, `DELETE FROM checkout_sessions WHERE org_id=$1`, orgID)
		pool.Exec(c, `DELETE FROM reservations WHERE session_id=$1`, sessionID)
		pool.Exec(c, `DELETE FROM inventory_ledger WHERE session_id=$1`, sessionID)
		pool.Exec(c, `DELETE FROM sales_channels WHERE id=$1`, channelID)
		pool.Exec(c, `DELETE FROM sessions WHERE id=$1`, sessionID)
		pool.Exec(c, `DELETE FROM events WHERE id=$1`, eventID)
		pool.Exec(c, `DELETE FROM venues WHERE id=$1`, venueID)
		pool.Exec(c, `DELETE FROM organizations WHERE id=$1`, orgID)
		pool.Exec(c, `DELETE FROM i18n_text WHERE namespace='geo.cities' AND key=$1`, citySlug)
		pool.Exec(c, `DELETE FROM cities WHERE id=$1`, cityID)
	})

	resID := uuid.New()
	csID := uuid.New()
	mustExec(`INSERT INTO reservations (id, org_id, channel_id, session_id, quantity, expires_at)
		VALUES ($1, $2, $3, $4, 3, $5)`,
		resID, orgID, channelID, sessionID, time.Now().Add(30*time.Minute))
	mustExec(`INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state, subtotal, discount, total, currency, payment_provider)
		VALUES ($1, $2, $3, $4, 'completed', 3000, 0, 3000, 'RUB', 'yookassa')`,
		csID, orgID, channelID, resID)

	activeID := uuid.New()
	mustExec(`INSERT INTO tickets (id, session_id, checkout_session_id, status, issued_at, ordinal)
		VALUES ($1, $2, $3, 'active', NOW(), 1)`,
		activeID, sessionID, csID)

	// Cancelled WITH a refund — cancelled_at and refund_date both set.
	refundedID := uuid.New()
	refundTime := time.Now().Add(-2 * time.Hour)
	mustExec(`INSERT INTO tickets (id, session_id, checkout_session_id, status, issued_at, ordinal, cancelled_at, refund_date)
		VALUES ($1, $2, $3, 'cancelled', NOW(), 2, $4, $4)`,
		refundedID, sessionID, csID, refundTime)

	// Cancelled WITHOUT a refund ("none" refund mode) — cancelled_at only.
	cancelledNoRefundID := uuid.New()
	cancelTime := time.Now().Add(-1 * time.Hour)
	mustExec(`INSERT INTO tickets (id, session_id, checkout_session_id, status, issued_at, ordinal, cancelled_at)
		VALUES ($1, $2, $3, 'cancelled', NOW(), 3, $4)`,
		cancelledNoRefundID, sessionID, csID, cancelTime)

	// ── all: every ticket present ──────────────────────────────────────
	allExport, err := macs.QueryAndBuildExportFiltered(ctx, pool, sessionID, macs.ExportModeAll, nil)
	if err != nil {
		t.Fatalf("QueryAndBuildExportFiltered(all): %v", err)
	}
	if got := countTickets(allExport); got != 3 {
		t.Errorf("all mode: got %d tickets, want 3", got)
	}

	// ── valid: only the active ticket ──────────────────────────────────
	validExport, err := macs.QueryAndBuildExportFiltered(ctx, pool, sessionID, macs.ExportModeValid, nil)
	if err != nil {
		t.Fatalf("QueryAndBuildExportFiltered(valid): %v", err)
	}
	if got := countTickets(validExport); got != 1 {
		t.Errorf("valid mode: got %d tickets, want 1", got)
	}
	for _, o := range validExport {
		for _, tk := range o.TicketList {
			if tk.HolderStatus != 0 {
				t.Errorf("valid mode: holderStatus = %d, want 0", tk.HolderStatus)
			}
		}
	}

	// ── revoked: both cancelled tickets, none of the active one ────────
	revokedExport, err := macs.QueryAndBuildExportFiltered(ctx, pool, sessionID, macs.ExportModeRevoked, nil)
	if err != nil {
		t.Fatalf("QueryAndBuildExportFiltered(revoked): %v", err)
	}
	if got := countTickets(revokedExport); got != 2 {
		t.Errorf("revoked mode: got %d tickets, want 2", got)
	}
	for _, o := range revokedExport {
		for _, tk := range o.TicketList {
			if tk.HolderStatus != 3 {
				t.Errorf("revoked mode: holderStatus = %d, want 3", tk.HolderStatus)
			}
		}
	}

	// ── revoked_since: only the cancelled-without-refund ticket, whose
	// cancelled_at (-1h) is after the cutoff; the refunded ticket's
	// refund_date (-2h) is before it and must be excluded.
	cutoff := time.Now().Add(-90 * time.Minute) // between cancelTime(-1h) and refundTime(-2h)
	sinceExport, err := macs.QueryAndBuildExportFiltered(ctx, pool, sessionID, macs.ExportModeRevoked, &cutoff)
	if err != nil {
		t.Fatalf("QueryAndBuildExportFiltered(revoked, since): %v", err)
	}
	if got := countTickets(sinceExport); got != 1 {
		t.Errorf("revoked+since mode: got %d tickets, want 1 (only the ticket cancelled after the cutoff)", got)
	}
	// The survivor carries its cancellation time as refundDate (F-31: a
	// "none" cancellation still tells MACS when it happened), which is
	// after the cutoff; the refunded ticket's date is before it.
	for _, o := range sinceExport {
		for _, tk := range o.TicketList {
			if tk.RefundDate == nil {
				t.Errorf("revoked+since mode: the no-refund ticket has no refundDate, want its cancellation time")
				continue
			}
			when, err := time.Parse(time.RFC3339, *tk.RefundDate)
			if err != nil || !when.After(cutoff) {
				t.Errorf("revoked+since mode: refundDate %q, want the cancellation time after the cutoff", *tk.RefundDate)
			}
		}
	}

	t.Logf("export modes OK: all=%d valid=%d revoked=%d revoked_since=%d",
		countTickets(allExport), countTickets(validExport), countTickets(revokedExport), countTickets(sinceExport))
}

func countTickets(export macs.Export) int {
	n := 0
	for _, o := range export {
		n += len(o.TicketList)
	}
	return n
}
