//go:build integration

// smart_price_integration_test.go — the smart price presets (spec
// 08_architecture/51_4_2_smart_price_ru.md) written through the real
// event-bundle import, then driven by the real chain sweep.
package himports

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/tierchain"
)

func (f *bundle525Fixture) smartCall(h *Handler, method, path string, sessionID uuid.UUID, ruleID uuid.UUID, body any,
	handle func(*Handler, http.ResponseWriter, *http.Request)) *httptest.ResponseRecorder {
	f.t.Helper()
	var raw []byte
	if body != nil {
		var err error
		if raw, err = json.Marshal(body); err != nil {
			f.t.Fatalf("marshal: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("org_id", f.orgID.String())
	rctx.URLParams.Add("session_id", sessionID.String())
	if ruleID != uuid.Nil {
		rctx.URLParams.Add("rule_id", ruleID.String())
	}
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = auth.WithActor(ctx, auth.Actor{ID: f.userID.String(), Type: auth.ActorTypeUser})
	rec := httptest.NewRecorder()
	handle(h, rec, req.WithContext(ctx))
	return rec
}

func decodeRules(t *testing.T, rec *httptest.ResponseRecorder) SmartPriceRulesResponse {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d; body=%s", rec.Code, rec.Body.String())
	}
	var out SmartPriceRulesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

func TestSmartPrice_BySales_ChainRaisesThePriceAndKeepsTheHall(t *testing.T) {
	pool := import517Pool(t)
	ctx := context.Background()
	f := newBundle525Fixture(t, ctx, pool)
	defer f.cleanup()
	h := newBundle525Handler(t, pool)

	p := f.payload()
	p.ActionEvent.Day = time.Now().AddDate(0, 2, 0).Format("02.01.2006")
	rec, out := f.call(h, p)
	if rec.Code != http.StatusOK {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var parter, balcony uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT
	        (SELECT id FROM ticket_tiers WHERE session_id=$1 AND name='Parter'),
	        (SELECT id FROM ticket_tiers WHERE session_id=$1 AND name='Balcony')`, out.SessionID).Scan(&parter, &balcony); err != nil {
		t.Fatalf("tiers: %v", err)
	}
	base := "/v1/organizations/" + f.orgID.String() + "/sessions/" + out.SessionID.String() + "/smart-price"
	body := map[string]any{"tier_ids": []uuid.UUID{parter}, "kind": "by_sales", "step_percent": 10, "every": 30, "cap_percent": 50}

	// Preview writes nothing.
	prev := f.smartCall(h, http.MethodPost, base+"/preview", out.SessionID, uuid.Nil, body, (*Handler).HandleSmartPricePreview)
	if prev.Code != http.StatusOK {
		t.Fatalf("preview: %d %s", prev.Code, prev.Body.String())
	}
	var pv SmartPricePreviewResponse
	_ = json.Unmarshal(prev.Body.Bytes(), &pv)
	wantPrices := []int64{2500, 2800, 3000, 3300}
	wantPlaces := []int32{30, 30, 30, 10}
	if len(pv.Categories) != 1 || len(pv.Categories[0].Steps) != len(wantPrices) {
		t.Fatalf("preview = %s", prev.Body.String())
	}
	for i, s := range pv.Categories[0].Steps {
		if s.Price != wantPrices[i] || s.Places != wantPlaces[i] {
			t.Errorf("preview step %d = %+v", i, s)
		}
	}
	var tiers int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM ticket_tiers WHERE session_id=$1 AND deleted_at IS NULL`, out.SessionID).Scan(&tiers)
	if tiers != 2 {
		t.Fatalf("preview created categories: %d", tiers)
	}

	rules := decodeRules(t, f.smartCall(h, http.MethodPost, base, out.SessionID, uuid.Nil, body, (*Handler).HandleSmartPriceEnable))
	if len(rules.Rules) != 1 || rules.Rules[0].Status != "active" || rules.Rules[0].Now == nil || rules.Rules[0].Now.Price != 2500 ||
		rules.Rules[0].Now.Steps != 4 || rules.Rules[0].Now.RemainingAtPrice == nil || *rules.Rules[0].Now.RemainingAtPrice != 30 {
		t.Fatalf("enable answered %+v", rules)
	}
	hall := func(stage string) {
		t.Helper()
		var seats, capTotal int
		if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM session_seats WHERE session_id=$1), capacity_total FROM sessions WHERE id=$1`,
			out.SessionID).Scan(&seats, &capTotal); err != nil {
			t.Fatal(err)
		}
		if seats != 140 || capTotal != 140 {
			t.Errorf("%s: hall has %d places, capacity %d — want 140 both", stage, seats, capTotal)
		}
	}
	hall("enabled")
	if s := readTier(t, ctx, f, out.SessionID, parter); s.places != 30 || !s.open {
		t.Errorf("Parter after enable = %+v, want 30 open", s)
	}

	// A second preset on the same category is refused.
	again := f.smartCall(h, http.MethodPost, base, out.SessionID, uuid.Nil, body, (*Handler).HandleSmartPriceEnable)
	if again.Code != http.StatusConflict {
		t.Errorf("second preset: %d %s", again.Code, again.Body.String())
	}

	// Sell the first step, run the sweep: the price rises, the hall stays.
	if _, err := pool.Exec(ctx, `UPDATE session_seats SET status='sold' WHERE session_id=$1 AND tier_id=$2`, out.SessionID, parter); err != nil {
		t.Fatal(err)
	}
	sweep := tierchain.NewHandler(tierchain.Options{Store: tierchain.NewPGStore(pool)})
	if err := sweep(ctx, nil); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	list := decodeRules(t, f.smartCall(h, http.MethodGet, base, out.SessionID, uuid.Nil, nil, (*Handler).HandleSmartPriceList))
	n := list.Rules[0].Now
	if n == nil || n.Price != 2800 || n.Step != 2 || n.NextPrice == nil || *n.NextPrice != 3000 || *n.RemainingAtPrice != 30 {
		t.Fatalf("after 30 sold: %+v", n)
	}
	hall("first step sold")

	// Stop: the later steps take the current price, nothing moves.
	stopped := decodeRules(t, f.smartCall(h, http.MethodPost, base+"/"+list.Rules[0].ID.String()+"/stop", out.SessionID,
		list.Rules[0].ID, nil, (*Handler).HandleSmartPriceStop))
	if stopped.Rules[0].Status != "stopped" {
		t.Fatalf("stop answered %+v", stopped.Rules[0])
	}
	var maxPrice int64
	if err := pool.QueryRow(ctx, `SELECT max(price_amount) FROM ticket_tiers WHERE session_id=$1 AND name LIKE 'Parter%'`, out.SessionID).Scan(&maxPrice); err != nil || maxPrice != 2800 {
		t.Errorf("after stop the dearest Parter step costs %d (err %v), want 2800", maxPrice, err)
	}
	hall("stopped")
}

func TestSmartPrice_ByDays_WritesWindowsAndHandEditEndsIt(t *testing.T) {
	pool := import517Pool(t)
	ctx := context.Background()
	f := newBundle525Fixture(t, ctx, pool)
	defer f.cleanup()
	h := newBundle525Handler(t, pool)

	p := f.payload()
	p.ActionEvent.Day = time.Now().AddDate(0, 2, 0).Format("02.01.2006")
	rec, out := f.call(h, p)
	if rec.Code != http.StatusOK {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var balcony uuid.UUID
	_ = pool.QueryRow(ctx, `SELECT id FROM ticket_tiers WHERE session_id=$1 AND name='Balcony'`, out.SessionID).Scan(&balcony)
	base := "/v1/organizations/" + f.orgID.String() + "/sessions/" + out.SessionID.String() + "/smart-price"
	body := map[string]any{"tier_ids": []uuid.UUID{balcony}, "kind": "by_days", "step_percent": 10, "every": 7, "cap_percent": 30}
	rules := decodeRules(t, f.smartCall(h, http.MethodPost, base, out.SessionID, uuid.Nil, body, (*Handler).HandleSmartPriceEnable))
	if len(rules.Rules) != 1 || rules.Rules[0].Now == nil || rules.Rules[0].Now.Price != 1250 || rules.Rules[0].Now.NextAt == nil ||
		*rules.Rules[0].Now.NextPrice != 1400 {
		t.Fatalf("enable answered %+v", rules.Rules)
	}
	var windows int
	var top int64
	if err := pool.QueryRow(ctx, `SELECT count(*), max(price_amount) FROM ticket_tier_prices WHERE tier_id=$1`, balcony).Scan(&windows, &top); err != nil {
		t.Fatal(err)
	}
	// 12.50 +10 % = 13.75 -> 14, +20 % = 15, +30 % = 16.25 -> 17 (cap).
	if windows != 3 || top != 1700 {
		t.Errorf("windows = %d, top price %d; want 3 and 1700", windows, top)
	}

	// A price typed by hand ends the preset (owner decision В-5).
	n, err := gen.New(pool).OverrideSmartPriceRulesForTier(ctx, balcony)
	if err != nil || n != 1 {
		t.Fatalf("override: %d %v", n, err)
	}
	list := decodeRules(t, f.smartCall(h, http.MethodGet, base, out.SessionID, uuid.Nil, nil, (*Handler).HandleSmartPriceList))
	if list.Rules[0].Status != "overridden" || list.Rules[0].Now != nil {
		t.Errorf("after a hand edit: %+v", list.Rules[0])
	}

	// A preset never overwrites future price changes.
	again := f.smartCall(h, http.MethodPost, base, out.SessionID, uuid.Nil, body, (*Handler).HandleSmartPriceEnable)
	if again.Code != http.StatusConflict {
		t.Errorf("preset over a schedule: %d %s", again.Code, again.Body.String())
	}
}
