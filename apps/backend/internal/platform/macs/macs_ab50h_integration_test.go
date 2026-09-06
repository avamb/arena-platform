//go:build integration

// Package macs — AB-50h integration test (feature #445).
//
// Tests:
//  1. Export with promo discount: discountReason="Промокод {code}",
//     per-ticket discount/charge computed from order totals.
//  2. Export without promo: discountReason="" (omitted).
//  3. Export for external (payment_provider IS NULL): discountReason="Внешняя система".
//
// Prerequisites:
//
//	DATABASE_URL=postgres://arena:arena@localhost:55432/arena?sslmode=disable
//
// Run with:
//
//	go test -tags integration -run TestMACS_AB50h ./apps/backend/internal/platform/macs/
package macs_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/barcodes/ean13"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/compatids"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/macs"
)

// TestMACS_AB50h_ExportFidelity tests discount/charge proration and discountReason
// in the MACS export using a seeded session.
func TestMACS_AB50h_ExportFidelity(t *testing.T) {
	pool := roundtripPool(t) // skips when DATABASE_URL not set
	ctx := context.Background()

	suffix := uuid.New().String()[:8]
	orgID := uuid.New()
	cityID := uuid.New()
	venueID := uuid.New()
	eventID := uuid.New()
	sessionID := uuid.New()
	channelID := uuid.New()
	citySlug := "ab50h-city-" + suffix

	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed exec: %v\nSQL: %s", err, sql)
		}
	}

	// Seed geography.
	var countryID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM countries WHERE iso2='IL' LIMIT 1`).Scan(&countryID); err != nil {
		t.Skipf("IL country not found (migration 0006 not applied?): %v", err)
	}
	mustExec(`INSERT INTO cities (id, country_id, slug) VALUES ($1, $2, $3)`,
		cityID, countryID, citySlug)
	mustExec(`INSERT INTO i18n_text (namespace, key, locale, value) VALUES ('geo.cities', $1, 'en', 'AB50h City')`,
		citySlug)

	mustExec(`INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`,
		orgID, "AB50h Org "+suffix, "ab50h-"+suffix)
	mustExec(`INSERT INTO venues (id, org_id, name, city_id) VALUES ($1, $2, $3, $4)`,
		venueID, orgID, "AB50h Venue", cityID)
	mustExec(`INSERT INTO events (id, org_id, name, status, visibility) VALUES ($1, $2, $3, 'draft', 'private')`,
		eventID, orgID, "AB50h Event")
	mustExec(`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, status, admission_mode, currency, currency_source)
		VALUES ($1, $2, $3, NOW()+INTERVAL '90 days', NOW()+INTERVAL '90 days 3 hours', 100, 'draft', 'general_admission', 'RUB', 'override')`,
		sessionID, eventID, venueID)
	mustExec(`INSERT INTO sales_channels (id, org_id, name) VALUES ($1, $2, $3)`,
		channelID, orgID, "AB50h Channel")

	// Seed a promo code.
	promoID := uuid.New()
	mustExec(`INSERT INTO promo_codes (id, org_id, code, discount_type, discount_value, status)
		VALUES ($1, $2, $3, 'percent', 10, 'active')`,
		promoID, orgID, "TESTPROMO"+suffix)

	t.Cleanup(func() {
		c := context.Background()
		pool.Exec(c, `DELETE FROM tickets WHERE session_id=$1`, sessionID)
		pool.Exec(c, `DELETE FROM checkout_sessions WHERE org_id=$1`, orgID)
		pool.Exec(c, `DELETE FROM reservations WHERE session_id=$1`, sessionID)
		pool.Exec(c, `DELETE FROM inventory_ledger WHERE session_id=$1`, sessionID)
		pool.Exec(c, `DELETE FROM promo_codes WHERE id=$1`, promoID)
		pool.Exec(c, `DELETE FROM sales_channels WHERE id=$1`, channelID)
		pool.Exec(c, `DELETE FROM sessions WHERE id=$1`, sessionID)
		pool.Exec(c, `DELETE FROM events WHERE id=$1`, eventID)
		pool.Exec(c, `DELETE FROM venues WHERE id=$1`, venueID)
		pool.Exec(c, `DELETE FROM organizations WHERE id=$1`, orgID)
		pool.Exec(c, `DELETE FROM i18n_text WHERE namespace='geo.cities' AND key=$1`, citySlug)
		pool.Exec(c, `DELETE FROM cities WHERE id=$1`, cityID)
	})

	// ── Case 1: ticket with promo discount ────────────────────────────────
	resID1 := uuid.New()
	csID1 := uuid.New()
	ticketID1 := uuid.New()
	mustExec(`INSERT INTO inventory_ledger (session_id, tier_id, capacity_total, capacity_sold) VALUES ($1, NULL, 100, 2)`,
		sessionID)
	mustExec(`INSERT INTO reservations (id, org_id, channel_id, session_id, quantity, expires_at)
		VALUES ($1, $2, $3, $4, 2, $5)`,
		resID1, orgID, channelID, sessionID, time.Now().Add(30*time.Minute))
	// Checkout session with promo applied: 2 tickets at 1000, 10% off = 200 discount, 1800 total.
	mustExec(`INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state, subtotal, discount, total, currency, promo_code_id, payment_provider)
		VALUES ($1, $2, $3, $4, 'completed', 2000, 200, 1800, 'RUB', $5, 'yookassa')`,
		csID1, orgID, channelID, resID1, promoID)
	mustExec(`INSERT INTO tickets (id, session_id, checkout_session_id, status, issued_at)
		VALUES ($1, $2, $3, 'active', NOW())`,
		ticketID1, sessionID, csID1)
	ticketID1b := uuid.New()
	mustExec(`INSERT INTO tickets (id, session_id, checkout_session_id, status, issued_at, ordinal)
		VALUES ($1, $2, $3, 'active', NOW(), 2)`,
		ticketID1b, sessionID, csID1)

	// ── Case 2: ticket without promo, with payment provider ───────────────
	resID2 := uuid.New()
	csID2 := uuid.New()
	ticketID2 := uuid.New()
	mustExec(`INSERT INTO reservations (id, org_id, channel_id, session_id, quantity, expires_at)
		VALUES ($1, $2, $3, $4, 1, $5)`,
		resID2, orgID, channelID, sessionID, time.Now().Add(30*time.Minute))
	mustExec(`INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state, subtotal, discount, total, currency, payment_provider)
		VALUES ($1, $2, $3, $4, 'completed', 1500, 0, 1500, 'RUB', 'stripe')`,
		csID2, orgID, channelID, resID2)
	mustExec(`INSERT INTO tickets (id, session_id, checkout_session_id, status, issued_at)
		VALUES ($1, $2, $3, 'active', NOW())`,
		ticketID2, sessionID, csID2)

	// ── Case 3: external ticket (payment_provider IS NULL) ────────────────
	resID3 := uuid.New()
	csID3 := uuid.New()
	ticketID3 := uuid.New()
	mustExec(`INSERT INTO reservations (id, org_id, channel_id, session_id, quantity, expires_at)
		VALUES ($1, $2, $3, $4, 1, $5)`,
		resID3, orgID, channelID, sessionID, time.Now().Add(30*time.Minute))
	mustExec(`INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state, subtotal, discount, total, currency)
		VALUES ($1, $2, $3, $4, 'completed', 0, 0, 0, 'RUB')`,
		csID3, orgID, channelID, resID3)
	mustExec(`INSERT INTO tickets (id, session_id, checkout_session_id, status, issued_at)
		VALUES ($1, $2, $3, 'active', NOW())`,
		ticketID3, sessionID, csID3)

	// ── Run the export ────────────────────────────────────────────────────
	export, err := macs.QueryAndBuildExport(ctx, pool, sessionID)
	if err != nil {
		t.Fatalf("QueryAndBuildExport: %v", err)
	}
	if len(export) == 0 {
		t.Fatal("expected at least 1 order in export")
	}

	// Build ticket map from export (by matching order/ticket counts).
	// Since we can't directly correlate by UUID (export uses int ids),
	// we do structural assertions instead.
	for _, o := range export {
		for _, tk := range o.TicketList {
			// Check: if discountReason is "Промокод ...", assert discount > 0.
			if o.Discount > 0 {
				// This is the promo order.
				t.Run("promo_order", func(t *testing.T) {
					promoCode := "TESTPROMO" + suffix
					expectedDR := "Промокод " + promoCode
					if tk.DiscountReason != expectedDR {
						t.Errorf("ticket.discountReason = %q, want %q", tk.DiscountReason, expectedDR)
					}
					if o.DiscountReason != expectedDR {
						t.Errorf("order.discountReason = %q, want %q", o.DiscountReason, expectedDR)
					}
					if tk.Discount <= 0 {
						t.Errorf("ticket.discount = %d, want > 0 (promo discount applied)", tk.Discount)
					}
					if tk.Charge <= 0 {
						t.Errorf("ticket.charge = %d, want > 0", tk.Charge)
					}
					if tk.TotalPrice != tk.Charge {
						t.Errorf("ticket.totalPrice = %d, want == charge (%d)", tk.TotalPrice, tk.Charge)
					}
				})
			}
			if o.Discount == 0 && o.PaymentMethod == "stripe" {
				t.Run("no_promo_order", func(t *testing.T) {
					if tk.DiscountReason != "" {
						t.Errorf("ticket.discountReason = %q, want empty for no-promo order", tk.DiscountReason)
					}
					if tk.Discount != 0 {
						t.Errorf("ticket.discount = %d, want 0 for no-promo order", tk.Discount)
					}
				})
			}
			if o.PaymentMethod == "" && o.Discount == 0 && o.Sum == 0 {
				t.Run("external_order", func(t *testing.T) {
					if tk.DiscountReason != "Внешняя система" {
						t.Errorf("ticket.discountReason = %q, want %q", tk.DiscountReason, "Внешняя система")
					}
				})
			}
		}
	}

	// ── W1-Mb (spec §10 M3/M4): ids and barcode ───────────────────────────
	// The export must name the SESSION's actionEventId and the EVENT's
	// actionId out of compatibility_id_map — the same integers the
	// Bil24-compatible gateway hands the WordPress sites — and a barcode a
	// site can actually print as EAN-13.
	wantActionEventID, err := compatids.Ensure(ctx, pool, compatids.KindActionEvent, sessionID)
	if err != nil {
		t.Fatalf("compatids.Ensure(action_event): %v", err)
	}
	wantActionID, err := compatids.Ensure(ctx, pool, compatids.KindAction, eventID)
	if err != nil {
		t.Fatalf("compatids.Ensure(action): %v", err)
	}
	t.Cleanup(func() {
		c := context.Background()
		pool.Exec(c, `DELETE FROM compatibility_id_map WHERE platform_id IN ($1, $2)`, sessionID, eventID)
	})

	for _, o := range export {
		for _, tk := range o.TicketList {
			if tk.ActionEvent.ID != wantActionEventID {
				t.Errorf("actionEvent.id = %d, want the session's actionEventId %d", tk.ActionEvent.ID, wantActionEventID)
			}
			if tk.ActionEvent.ActionID != wantActionID {
				t.Errorf("actionEvent.actionId = %d, want the event's actionId %d", tk.ActionEvent.ActionID, wantActionID)
			}
			if !ean13.Valid(tk.Barcode) {
				t.Errorf("ticket %d barcode = %q, want a checksum-valid EAN-13", tk.ID, tk.Barcode)
			}
			if tk.BarcodeFormat.ID != 0 || tk.BarcodeFormat.Name != "EAN-13" {
				t.Errorf("barcodeFormat = %+v, want {0 EAN-13}", tk.BarcodeFormat)
			}
		}
	}

	t.Logf("AB-50h export fidelity OK: %d orders, actionEventId=%d actionId=%d",
		len(export), wantActionEventID, wantActionID)
}
