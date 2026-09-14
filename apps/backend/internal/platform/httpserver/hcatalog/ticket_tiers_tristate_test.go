// ticket_tiers_tristate_test.go — unit tests for the tri-state ticket-tier
// PATCH body (plan 08_architecture/23 step 6, wave B).
//
// Before wave B `UpdateTicketTier` used `CASE WHEN $n IS NOT NULL`, so an
// explicit JSON `null` was indistinguishable from an omitted key and a
// nullable column could never be cleared: the admin sent `null` and the old
// value silently survived. The request struct now carries tri-state types
// for pwyw_min, pwyw_max, capacity, sale_window_start and sale_window_end,
// and the SQL carries a set flag per column.
//
// All tests are pure unit tests — no live PostgreSQL required.
package hcatalog

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// optionalInt64
// ─────────────────────────────────────────────────────────────────────────────

func TestOptionalInt64_Unmarshal_AbsentNullValue(t *testing.T) {
	var absent struct {
		V optionalInt64 `json:"v"`
	}
	if err := json.Unmarshal([]byte(`{}`), &absent); err != nil {
		t.Fatalf("absent: %v", err)
	}
	if absent.V.Present {
		t.Errorf("absent key: Present = true, want false")
	}

	var cleared struct {
		V optionalInt64 `json:"v"`
	}
	if err := json.Unmarshal([]byte(`{"v":null}`), &cleared); err != nil {
		t.Fatalf("null: %v", err)
	}
	if !cleared.V.Present || cleared.V.Value != nil {
		t.Errorf("null: Present=%v Value=%v, want true / nil", cleared.V.Present, cleared.V.Value)
	}

	var set struct {
		V optionalInt64 `json:"v"`
	}
	if err := json.Unmarshal([]byte(`{"v":1500}`), &set); err != nil {
		t.Fatalf("value: %v", err)
	}
	if !set.V.Present || set.V.Value == nil || *set.V.Value != 1500 {
		t.Errorf("value: Present=%v Value=%v, want true / 1500", set.V.Present, set.V.Value)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// updateTierRequest — the whole body round-trips tri-state
// ─────────────────────────────────────────────────────────────────────────────

func TestUpdateTierRequest_OmittedKeysKeepEverything(t *testing.T) {
	var req updateTierRequest
	if err := json.Unmarshal([]byte(`{"name":"VIP"}`), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for name, present := range map[string]bool{
		"pwyw_min":          req.PwywMin.Present,
		"pwyw_max":          req.PwywMax.Present,
		"capacity":          req.Capacity.Present,
		"sale_window_start": req.SaleWindowStart.Present,
		"sale_window_end":   req.SaleWindowEnd.Present,
	} {
		if present {
			t.Errorf("%s: Present = true for an omitted key, want false", name)
		}
	}
	if req.IsOpen != nil {
		t.Errorf("is_open = %v for an omitted key, want nil", req.IsOpen)
	}
}

func TestUpdateTierRequest_ExplicitNullsAreClears(t *testing.T) {
	const body = `{"pwyw_min":null,"pwyw_max":null,"capacity":null,` +
		`"sale_window_start":null,"sale_window_end":null}`
	var req updateTierRequest
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !req.PwywMin.Present || req.PwywMin.Value != nil {
		t.Errorf("pwyw_min: %+v, want present with a nil value", req.PwywMin)
	}
	if !req.PwywMax.Present || req.PwywMax.Value != nil {
		t.Errorf("pwyw_max: %+v, want present with a nil value", req.PwywMax)
	}
	if !req.Capacity.Present || req.Capacity.Value != nil {
		t.Errorf("capacity: %+v, want present with a nil value", req.Capacity)
	}
	if !req.SaleWindowStart.Present || req.SaleWindowStart.Value != nil {
		t.Errorf("sale_window_start: %+v, want present with a nil value", req.SaleWindowStart)
	}
	if !req.SaleWindowEnd.Present || req.SaleWindowEnd.Value != nil {
		t.Errorf("sale_window_end: %+v, want present with a nil value", req.SaleWindowEnd)
	}
}

func TestUpdateTierRequest_ValuesAreSets(t *testing.T) {
	const body = `{"pwyw_min":500,"pwyw_max":9000,"capacity":180,` +
		`"sale_window_end":"2026-09-01T00:00:00Z","is_open":false}`
	var req updateTierRequest
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if req.PwywMin.Value == nil || *req.PwywMin.Value != 500 {
		t.Errorf("pwyw_min = %v, want 500", req.PwywMin.Value)
	}
	if req.PwywMax.Value == nil || *req.PwywMax.Value != 9000 {
		t.Errorf("pwyw_max = %v, want 9000", req.PwywMax.Value)
	}
	if req.Capacity.Value == nil || *req.Capacity.Value != 180 {
		t.Errorf("capacity = %v, want 180", req.Capacity.Value)
	}
	if req.SaleWindowEnd.Value == nil || *req.SaleWindowEnd.Value != "2026-09-01T00:00:00Z" {
		t.Errorf("sale_window_end = %v, want the RFC3339 string", req.SaleWindowEnd.Value)
	}
	if req.IsOpen == nil || *req.IsOpen {
		t.Errorf("is_open = %v, want false", req.IsOpen)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// parseOptionalTime
// ─────────────────────────────────────────────────────────────────────────────

func TestParseOptionalTime_TriState(t *testing.T) {
	stamp := "2026-09-01T00:00:00Z"
	blank := "   "
	tests := []struct {
		name     string
		opt      optionalString
		wantSet  bool
		wantNil  bool
		wantTime string
	}{
		{name: "absent keeps", opt: optionalString{}, wantSet: false, wantNil: true},
		{name: "null clears", opt: optionalString{Present: true}, wantSet: true, wantNil: true},
		{name: "blank clears", opt: optionalString{Present: true, Value: &blank}, wantSet: true, wantNil: true},
		{name: "value sets", opt: optionalString{Present: true, Value: &stamp}, wantSet: true, wantTime: stamp},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest("PATCH", "/x", nil)
			got, set, ok := parseOptionalTime(rec, req, tc.opt, "sale_window_end", "tier.invalid_sale_window_end")
			if !ok {
				t.Fatalf("ok = false, body = %s", rec.Body.String())
			}
			if set != tc.wantSet {
				t.Errorf("set = %v, want %v", set, tc.wantSet)
			}
			if tc.wantNil && got != nil {
				t.Errorf("value = %v, want nil", got)
			}
			if tc.wantTime != "" {
				if got == nil || !got.Equal(mustTime(t, tc.wantTime)) {
					t.Errorf("value = %v, want %s", got, tc.wantTime)
				}
			}
		})
	}
}

func TestParseOptionalTime_InvalidTimestampWrites400(t *testing.T) {
	bad := "not-a-date"
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("PATCH", "/x", nil)
	_, _, ok := parseOptionalTime(rec, req,
		optionalString{Present: true, Value: &bad},
		"sale_window_start", "tier.invalid_sale_window_start")
	if ok {
		t.Fatal("ok = true for an unparseable timestamp, want false")
	}
	if rec.Code != 400 {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, "tier.invalid_sale_window_start") {
		t.Errorf("body = %s, want the tier.invalid_sale_window_start code", body)
	}
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return parsed
}
