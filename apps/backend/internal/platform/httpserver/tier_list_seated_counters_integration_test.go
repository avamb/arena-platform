//go:build integration

// tier_list_seated_counters_integration_test.go — the admin category table
// counts the seats of a SEATED category (F-58, functional run 2026-09-19).
//
// The GA quota counters (gaquota.SessionStats) cover only ga_unit places, so
// GET .../tiers answered no sold/available for a seated category and the
// admin showed «sold 0, available —» next to sold seats. The list now reads
// a seated category's counters from its plan seats; a GA category keeps its
// quota counters unchanged.
package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

func TestTierList_SeatedCategoryReportsItsSeats(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()

	f := newGAQuotaFixture(t, ctx, pool, 4)
	defer f.cleanup()

	// A seated category on the same session: 5 plan seats — 2 sold, 1 held,
	// 1 free and 1 blocked ('unavailable', not for sale).
	seatedTier := uuid.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO ticket_tiers (id, session_id, name, pricing_mode, price_amount,
		    currency, sort_order, is_open)
		 VALUES ($1, $2, 'Parter', 'fixed', 3000, 'EUR', 1, true)`,
		seatedTier, f.sessionID); err != nil {
		t.Fatalf("insert seated tier: %v", err)
	}
	for i, status := range []string{"sold", "sold", "held", "available", "unavailable"} {
		if _, err := pool.Exec(ctx,
			`INSERT INTO session_seats
			    (session_id, seat_key, sector_name, row_name, seat_number, tier_id, status, kind)
			 VALUES ($1, $2, 'Parter', '1', $3, $4, $5, 'seat')`,
			f.sessionID, "parter|1|"+string(rune('1'+i)), string(rune('1'+i)), seatedTier, status); err != nil {
			t.Fatalf("insert seat %d: %v", i, err)
		}
	}
	// Two of the GA category's four places are sold.
	if _, err := pool.Exec(ctx,
		`UPDATE session_seats SET status='sold'
		 WHERE id IN (SELECT id FROM session_seats
		              WHERE session_id=$1 AND tier_id=$2 AND kind='ga_unit' LIMIT 2)`,
		f.sessionID, f.tierID); err != nil {
		t.Fatalf("sell GA places: %v", err)
	}

	srv := buildIntegrationResetServer(t, pool)
	req := ga45AdminRequest(http.MethodGet, nil, map[string]string{
		"org_id": f.orgID.String(), "event_id": f.eventID.String(), "session_id": f.sessionID.String(),
	})
	w := httptest.NewRecorder()
	srv.handleListTiers(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET tiers status = %d, body %s", w.Code, w.Body.String())
	}

	var body struct {
		Tiers []struct {
			ID        string `json:"id"`
			Kind      string `json:"kind"`
			SeatCount *int64 `json:"seat_count"`
			Quantity  *int32 `json:"quantity"`
			Held      *int32 `json:"held"`
			Sold      *int32 `json:"sold"`
			Available *int32 `json:"available"`
		} `json:"tiers"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	got := map[string]int{}
	for i, tr := range body.Tiers {
		got[tr.ID] = i
	}

	seated, ok := got[seatedTier.String()]
	if !ok {
		t.Fatalf("seated tier missing from %s", w.Body.String())
	}
	st := body.Tiers[seated]
	if st.Kind != "seated" {
		t.Errorf("seated kind = %q, want seated", st.Kind)
	}
	if st.Sold == nil || st.Held == nil || st.Available == nil || st.Quantity == nil {
		t.Fatalf("seated counters absent: %s", w.Body.String())
	}
	if *st.Sold != 2 || *st.Held != 1 || *st.Available != 1 || *st.Quantity != 4 {
		t.Errorf("seated quantity/sold/held/available = %d/%d/%d/%d, want 4/2/1/1 (the blocked seat is not for sale)",
			*st.Quantity, *st.Sold, *st.Held, *st.Available)
	}

	gaRow := body.Tiers[got[f.tierID.String()]]
	if gaRow.Kind != "ga" || gaRow.Sold == nil || *gaRow.Sold != 2 || gaRow.Quantity == nil || *gaRow.Quantity != 4 {
		t.Errorf("GA row = %+v, want kind ga, quantity 4, sold 2 (quota counters unchanged)", gaRow)
	}
}
