//go:build integration

// Package macs — AB-50i integration test (feature #447).
//
// Handler-level tests for GET .../sessions/{id}/macs-export:
//   - 200 with a complete fixture (city-linked venue)
//   - 422 macs.export_incomplete when venue has no city
//   - 404 when session belongs to a different org (tenant isolation)
//
// Prerequisites:
//
//	DATABASE_URL=postgres://arena:arena@localhost:55432/arena?sslmode=disable
//	(migrated to head >= 0089)
//
// Run with:
//
//	go test -tags integration -run TestMACS_AB50i ./apps/backend/internal/platform/macs/
package macs_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/barcodes/ean13"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/compatids"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcatalog"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/macs"
)

// macsExportReq creates a GET request for HandleMACSExport with chi URL params
// and superadmin context (bypasses org membership check).
func macsExportReq(orgID, eventID, sessionID uuid.UUID) *http.Request {
	req := httptest.NewRequest(http.MethodGet,
		"/v1/organizations/"+orgID.String()+"/events/"+eventID.String()+"/sessions/"+sessionID.String()+"/macs-export",
		nil,
	)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("org_id", orgID.String())
	rctx.URLParams.Add("event_id", eventID.String())
	rctx.URLParams.Add("id", sessionID.String())

	ctx := auth.WithSuperadminOrgAccess(
		auth.WithActor(req.Context(), auth.Actor{ID: "ab50i-test-actor", Type: "user"}),
	)
	ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)
	req = req.WithContext(ctx)
	req.Header.Set("X-Admin-Reason", "AB-50i integration test")
	return req
}

// TestMACS_AB50i_ExportHandler_200_WithCity verifies that HandleMACSExport
// returns 200 when the session venue is linked to a city.
func TestMACS_AB50i_ExportHandler_200_WithCity(t *testing.T) {
	pool := roundtripPool(t)
	ctx := context.Background()

	suffix := uuid.New().String()[:8]
	orgID := uuid.New()
	cityID := uuid.New()
	venueID := uuid.New()
	eventID := uuid.New()
	sessionID := uuid.New()
	channelID := uuid.New()
	citySlug := "ab50i-200-city-" + suffix

	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed: %v\nSQL: %s", err, sql)
		}
	}

	var countryID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM countries WHERE iso2='IL' LIMIT 1`).Scan(&countryID); err != nil {
		t.Skipf("IL country not found: %v", err)
	}

	mustExec(`INSERT INTO cities (id, country_id, slug) VALUES ($1, $2, $3)`, cityID, countryID, citySlug)
	mustExec(`INSERT INTO i18n_text (namespace, key, locale, value) VALUES ('geo.cities', $1, 'en', 'AB50i Test City')`, citySlug)
	mustExec(`INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`, orgID, "AB50i 200 Org "+suffix, "ab50i-200-"+suffix)
	mustExec(`INSERT INTO venues (id, org_id, name, city_id) VALUES ($1, $2, $3, $4)`, venueID, orgID, "AB50i Venue", cityID)
	mustExec(`INSERT INTO events (id, org_id, name, status, visibility) VALUES ($1, $2, $3, 'draft', 'private')`, eventID, orgID, "AB50i Event")
	mustExec(`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, status, admission_mode, currency, currency_source)
		VALUES ($1, $2, $3, NOW()+INTERVAL '60 days', NOW()+INTERVAL '60 days 3 hours', 100, 'draft', 'general_admission', 'RUB', 'override')`,
		sessionID, eventID, venueID)
	mustExec(`INSERT INTO sales_channels (id, org_id, name) VALUES ($1, $2, $3)`, channelID, orgID, "AB50i Channel")
	mustExec(`INSERT INTO inventory_ledger (session_id, tier_id, capacity_total) VALUES ($1, NULL, 100)`, sessionID)

	q := gen.New(pool)
	res, err := q.InsertReservation(ctx, orgID, channelID, sessionID, nil, nil, 1, time.Now().UTC().Add(30*time.Minute))
	if err != nil {
		t.Fatalf("InsertReservation: %v", err)
	}

	csID := uuid.New()
	ticketID := uuid.New()
	mustExec(`INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state) VALUES ($1, $2, $3, $4, 'completed')`,
		csID, orgID, channelID, res.ID)
	mustExec(`INSERT INTO tickets (id, session_id, checkout_session_id, status, issued_at) VALUES ($1, $2, $3, 'active', NOW())`,
		ticketID, sessionID, csID)

	// W1-Mb (spec §10 M4 / §11): the export must name the ticket's EAN-13
	// credential, never its 64-hex static_qr. Seed BOTH so the join is
	// actually discriminating.
	const wantBarcode = "2100000000005"
	staticQR := strings.ReplaceAll(uuid.New().String(), "-", "") + strings.ReplaceAll(uuid.New().String(), "-", "")
	mustExec(`INSERT INTO ticket_credentials (ticket_id, type, payload) VALUES ($1, 'static_qr', $2)`,
		ticketID, staticQR)
	mustExec(`INSERT INTO ticket_credentials (ticket_id, type, payload) VALUES ($1, 'ean13', $2)`,
		ticketID, wantBarcode)

	t.Cleanup(func() {
		c := context.Background()
		pool.Exec(c, `DELETE FROM ticket_credentials WHERE ticket_id=$1`, ticketID)
		pool.Exec(c, `DELETE FROM tickets WHERE id=$1`, ticketID)
		pool.Exec(c, `DELETE FROM checkout_sessions WHERE id=$1`, csID)
		pool.Exec(c, `DELETE FROM reservations WHERE id=$1`, res.ID)
		pool.Exec(c, `DELETE FROM inventory_ledger WHERE session_id=$1`, sessionID)
		pool.Exec(c, `DELETE FROM sales_channels WHERE id=$1`, channelID)
		pool.Exec(c, `DELETE FROM sessions WHERE id=$1`, sessionID)
		pool.Exec(c, `DELETE FROM events WHERE id=$1`, eventID)
		pool.Exec(c, `DELETE FROM venues WHERE id=$1`, venueID)
		pool.Exec(c, `DELETE FROM organizations WHERE id=$1`, orgID)
		pool.Exec(c, `DELETE FROM i18n_text WHERE namespace='geo.cities' AND key=$1`, citySlug)
		pool.Exec(c, `DELETE FROM cities WHERE id=$1`, cityID)
		pool.Exec(c, `DELETE FROM compatibility_id_map WHERE platform_id IN ($1, $2)`, sessionID, eventID)
	})

	h := hcatalog.New(nil, nil, nil, nil, nil, gen.New(pool), nil, pool, nil, slog.Default(), nil)
	w := httptest.NewRecorder()
	h.HandleMACSExport(pool, w, macsExportReq(orgID, eventID, sessionID))

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d\nBody: %s", w.Code, w.Body.String())
	}

	// The bytes on the wire — not the Go struct — are what a MACS importer
	// reads, so assert the M3/M4 shape after a JSON round-trip.
	var export macs.Export
	if err := json.Unmarshal(w.Body.Bytes(), &export); err != nil {
		t.Fatalf("decode export: %v\nBody: %s", err, w.Body.String())
	}
	if len(export) != 1 || len(export[0].TicketList) != 1 {
		t.Fatalf("want 1 order with 1 ticket, got %d order(s): %s", len(export), w.Body.String())
	}
	tk := export[0].TicketList[0]

	if tk.Barcode != wantBarcode {
		t.Errorf("ticket.barcode = %q, want the ean13 credential %q (static_qr must not leak)", tk.Barcode, wantBarcode)
	}
	if !ean13.Valid(tk.Barcode) {
		t.Errorf("ticket.barcode = %q is not a checksum-valid EAN-13", tk.Barcode)
	}
	if tk.BarcodeFormat.ID != 0 || tk.BarcodeFormat.Name != "EAN-13" {
		t.Errorf("barcodeFormat = %+v, want {0 EAN-13}", tk.BarcodeFormat)
	}

	// M3: actionEvent.id is the SESSION's actionEventId and actionId the
	// EVENT's actionId, both minted into compatibility_id_map by the export.
	wantActionEventID, err := compatids.Ensure(ctx, pool, compatids.KindActionEvent, sessionID)
	if err != nil {
		t.Fatalf("compatids.Ensure(action_event): %v", err)
	}
	wantActionID, err := compatids.Ensure(ctx, pool, compatids.KindAction, eventID)
	if err != nil {
		t.Fatalf("compatids.Ensure(action): %v", err)
	}
	if tk.ActionEvent.ID != wantActionEventID {
		t.Errorf("actionEvent.id = %d, want %d", tk.ActionEvent.ID, wantActionEventID)
	}
	if tk.ActionEvent.ActionID != wantActionID {
		t.Errorf("actionEvent.actionId = %d, want %d", tk.ActionEvent.ActionID, wantActionID)
	}
}

// TestMACS_AB50i_ExportHandler_422_NoCityVenue verifies that HandleMACSExport
// returns 422 macs.export_incomplete when the venue has no city.
func TestMACS_AB50i_ExportHandler_422_NoCityVenue(t *testing.T) {
	pool := roundtripPool(t)
	ctx := context.Background()

	suffix := uuid.New().String()[:8]
	orgID := uuid.New()
	venueID := uuid.New()
	eventID := uuid.New()
	sessionID := uuid.New()
	channelID := uuid.New()

	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed: %v\nSQL: %s", err, sql)
		}
	}

	mustExec(`INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`, orgID, "AB50i 422 Org "+suffix, "ab50i-422-"+suffix)
	// Venue WITHOUT city_id → cityName will be empty in export.
	mustExec(`INSERT INTO venues (id, org_id, name) VALUES ($1, $2, $3)`, venueID, orgID, "AB50i NoCityVenue")
	mustExec(`INSERT INTO events (id, org_id, name, status, visibility) VALUES ($1, $2, $3, 'draft', 'private')`, eventID, orgID, "AB50i 422 Event")
	mustExec(`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, status, admission_mode, currency, currency_source)
		VALUES ($1, $2, $3, NOW()+INTERVAL '60 days', NOW()+INTERVAL '60 days 3 hours', 100, 'draft', 'general_admission', 'RUB', 'override')`,
		sessionID, eventID, venueID)
	mustExec(`INSERT INTO sales_channels (id, org_id, name) VALUES ($1, $2, $3)`, channelID, orgID, "AB50i 422 Channel")
	mustExec(`INSERT INTO inventory_ledger (session_id, tier_id, capacity_total) VALUES ($1, NULL, 100)`, sessionID)

	q := gen.New(pool)
	res, err := q.InsertReservation(ctx, orgID, channelID, sessionID, nil, nil, 1, time.Now().UTC().Add(30*time.Minute))
	if err != nil {
		t.Fatalf("InsertReservation: %v", err)
	}

	csID := uuid.New()
	ticketID := uuid.New()
	mustExec(`INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state) VALUES ($1, $2, $3, $4, 'completed')`,
		csID, orgID, channelID, res.ID)
	mustExec(`INSERT INTO tickets (id, session_id, checkout_session_id, status, issued_at) VALUES ($1, $2, $3, 'active', NOW())`,
		ticketID, sessionID, csID)

	t.Cleanup(func() {
		c := context.Background()
		pool.Exec(c, `DELETE FROM tickets WHERE id=$1`, ticketID)
		pool.Exec(c, `DELETE FROM checkout_sessions WHERE id=$1`, csID)
		pool.Exec(c, `DELETE FROM reservations WHERE id=$1`, res.ID)
		pool.Exec(c, `DELETE FROM inventory_ledger WHERE session_id=$1`, sessionID)
		pool.Exec(c, `DELETE FROM sales_channels WHERE id=$1`, channelID)
		pool.Exec(c, `DELETE FROM sessions WHERE id=$1`, sessionID)
		pool.Exec(c, `DELETE FROM events WHERE id=$1`, eventID)
		pool.Exec(c, `DELETE FROM venues WHERE id=$1`, venueID)
		pool.Exec(c, `DELETE FROM organizations WHERE id=$1`, orgID)
	})

	h := hcatalog.New(nil, nil, nil, nil, nil, gen.New(pool), nil, pool, nil, slog.Default(), nil)
	w := httptest.NewRecorder()
	h.HandleMACSExport(pool, w, macsExportReq(orgID, eventID, sessionID))

	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("want 422 (macs.export_incomplete), got %d\nBody: %s", w.Code, w.Body.String())
	}
}

// TestMACS_AB50i_ExportHandler_404_WrongOrg verifies that HandleMACSExport
// returns 404 when the session belongs to a different org (tenant isolation).
func TestMACS_AB50i_ExportHandler_404_WrongOrg(t *testing.T) {
	pool := roundtripPool(t)
	ctx := context.Background()

	suffix := uuid.New().String()[:8]
	org1ID := uuid.New() // actual session owner
	org2ID := uuid.New() // the caller's org (different)
	venueID := uuid.New()
	eventID := uuid.New()
	sessionID := uuid.New()
	channelID := uuid.New()

	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed: %v\nSQL: %s", err, sql)
		}
	}

	// Seed org1 with its own session.
	mustExec(`INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`, org1ID, "AB50i 404 Org1 "+suffix, "ab50i-404-1-"+suffix)
	mustExec(`INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`, org2ID, "AB50i 404 Org2 "+suffix, "ab50i-404-2-"+suffix)
	mustExec(`INSERT INTO venues (id, org_id, name) VALUES ($1, $2, $3)`, venueID, org1ID, "AB50i 404 Venue")
	mustExec(`INSERT INTO events (id, org_id, name, status, visibility) VALUES ($1, $2, $3, 'draft', 'private')`, eventID, org1ID, "AB50i 404 Event")
	mustExec(`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, status, admission_mode, currency, currency_source)
		VALUES ($1, $2, $3, NOW()+INTERVAL '60 days', NOW()+INTERVAL '60 days 3 hours', 100, 'draft', 'general_admission', 'RUB', 'override')`,
		sessionID, eventID, venueID)
	mustExec(`INSERT INTO sales_channels (id, org_id, name) VALUES ($1, $2, $3)`, channelID, org1ID, "AB50i 404 Channel")
	mustExec(`INSERT INTO inventory_ledger (session_id, tier_id, capacity_total) VALUES ($1, NULL, 100)`, sessionID)

	t.Cleanup(func() {
		c := context.Background()
		pool.Exec(c, `DELETE FROM inventory_ledger WHERE session_id=$1`, sessionID)
		pool.Exec(c, `DELETE FROM sales_channels WHERE id=$1`, channelID)
		pool.Exec(c, `DELETE FROM sessions WHERE id=$1`, sessionID)
		pool.Exec(c, `DELETE FROM events WHERE id=$1`, eventID)
		pool.Exec(c, `DELETE FROM venues WHERE id=$1`, venueID)
		pool.Exec(c, `DELETE FROM organizations WHERE id=$1`, org1ID)
		pool.Exec(c, `DELETE FROM organizations WHERE id=$1`, org2ID)
	})

	// Call the handler with org2ID in the path, but the session belongs to org1ID.
	h := hcatalog.New(nil, nil, nil, nil, nil, gen.New(pool), nil, pool, nil, slog.Default(), nil)
	w := httptest.NewRecorder()
	h.HandleMACSExport(pool, w, macsExportReq(org2ID, eventID, sessionID))

	if w.Code != http.StatusNotFound {
		t.Errorf("want 404 (session of another org), got %d\nBody: %s", w.Code, w.Body.String())
	}
}
