//go:build integration

// addon_tier_integration_test.go — add-on categories (migration 0197) through
// the real widget handlers: a cart of add-on tickets only is refused with
// 409 tier.addon_requires_main while a cart with a main ticket is priced;
// the feed marks the add-on category, and once the main category is sold out
// the date reads "sold_out" and the add-on offers no place.
//
// Requires DATABASE_URL against a migrated database (see AGENTS.md).
package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hfeed"
)

func TestAddonTier_WidgetRefusesAddonAloneAndFeedClosesTheDate(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()

	f := newGAQuotaFixture(t, ctx, pool, 2)
	defer f.cleanup()
	extraID := uuid.New()
	for _, s := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO ticket_tiers (id, session_id, name, pricing_mode, price_amount,
		    currency, sort_order, capacity, unit_seq, is_open)
		  VALUES ($1, $2, 'Extra child', 'fixed', 1500, 'EUR', 1, 1, 2, true)`, []any{extraID, f.sessionID}},
		{`INSERT INTO session_seats (session_id, seat_key, sector_name, row_name, seat_number, tier_id, status, kind)
		  VALUES ($1, 'ga|t2|000001', '', '', '', $2, 'available', 'ga_unit')`, []any{f.sessionID, extraID}},
		{`UPDATE inventory_ledger SET capacity_total = 3 WHERE session_id = $1 AND tier_id IS NULL`, []any{f.sessionID}},
	} {
		if _, err := pool.Exec(ctx, s.sql, s.args...); err != nil {
			t.Fatalf("fixture: %v", err)
		}
	}
	if err := gen.New(pool).SetTicketTierAddon(ctx, extraID, true); err != nil {
		t.Fatalf("mark add-on: %v", err)
	}
	srv := buildIntegrationResetServer(t, pool)

	quote := func(items ...hfeed.PublicGAItem) (int, string) {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"session_id": f.sessionID.String(), "ga_items": items})
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("feed_token", f.feedToken)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
		w := httptest.NewRecorder()
		srv.feedHandler().HandlePublicFeedCheckoutQuote(w, req)
		var env struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &env)
		return w.Code, env.Error.Code
	}
	if code, ec := quote(hfeed.PublicGAItem{TierID: extraID.String(), Quantity: 1}); code != http.StatusConflict || ec != "tier.addon_requires_main" {
		t.Errorf("add-on alone: %d %q, want 409 tier.addon_requires_main", code, ec)
	}
	if code, ec := quote(hfeed.PublicGAItem{TierID: f.tierID.String(), Quantity: 1}, hfeed.PublicGAItem{TierID: extraID.String(), Quantity: 1}); code != http.StatusOK {
		t.Errorf("main with an add-on: %d %q, want 200", code, ec)
	}

	type feedTier struct {
		ID        string `json:"id"`
		Available *int   `json:"available"`
		IsAddon   bool   `json:"is_addon"`
	}
	read := func() (string, map[string]feedTier) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("feed_token", f.feedToken)
		rctx.URLParams.Add("event_id", f.eventID.String())
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
		w := httptest.NewRecorder()
		srv.feedHandler().HandlePublicFeedEvent(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("public feed status = %d (body %s)", w.Code, w.Body.String())
		}
		var env struct {
			Event struct {
				Sessions []struct {
					SalesState string     `json:"sales_state"`
					Tiers      []feedTier `json:"tiers"`
				} `json:"sessions"`
			} `json:"event"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil || len(env.Event.Sessions) != 1 {
			t.Fatalf("decode feed: %v (body %s)", err, w.Body.String())
		}
		out := map[string]feedTier{}
		for _, tr := range env.Event.Sessions[0].Tiers {
			out[tr.ID] = tr
		}
		return env.Event.Sessions[0].SalesState, out
	}
	state, tiers := read()
	if state != "open" || !tiers[extraID.String()].IsAddon || tiers[f.tierID.String()].IsAddon {
		t.Errorf("before the sell-out: state %q, tiers %+v", state, tiers)
	}

	if _, err := pool.Exec(ctx, `UPDATE session_seats SET status = 'sold' WHERE tier_id = $1`, f.tierID); err != nil {
		t.Fatal(err)
	}
	state, tiers = read()
	if state != "sold_out" {
		t.Errorf("main category sold out: state %q, want sold_out", state)
	}
	if a := tiers[extraID.String()].Available; a == nil || *a != 0 {
		t.Errorf("the add-on still offers %v places, want 0", a)
	}
}
