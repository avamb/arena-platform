//go:build integration

// ga_quota_admin_46_integration_test.go — live-DB coverage for the ADMIN
// half of plan 08_architecture/23 step 3: every General Admission category
// operation goes through the quota mechanism, and the session capacity stops
// being an operator input the moment a category owns places.
//
//   - POST .../sessions creates a plan-less GA session with NO places; the
//     first category is what materializes them and settles the capacity;
//   - POST .../tiers takes the quantity from `capacity`, or derives the
//     wave-A default from the session capacity minus what the other
//     categories already claim, and refuses with 400 tier.capacity_required
//     when nothing is left;
//   - PATCH .../tiers grows / shrinks the quantity (409
//     tier.quantity_below_used below what the category cannot give up) and
//     opens / closes the category;
//   - DELETE .../tiers removes the category with its places, and answers 409
//     tier.in_use for one that still holds or has sold a place;
//   - PATCH .../sessions IGNORES capacity_override on such a session and says
//     so with a session.capacity_is_category_sum warning (wave-A
//     compatibility — it becomes a 400 in wave B).
//
// Requires DATABASE_URL against a migrated database (see AGENTS.md).
package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
)

// ─────────────────────────────────────────────────────────────────────────────
// Fixture: an organization + venue + event, but NO session — the session is
// what the handlers under test create.
// ─────────────────────────────────────────────────────────────────────────────

type ga46Fixture struct {
	t       *testing.T
	pool    *pgxpool.Pool
	orgID   uuid.UUID
	venueID uuid.UUID
	eventID uuid.UUID
}

func newGA46Fixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) *ga46Fixture {
	t.Helper()
	f := &ga46Fixture{
		t: t, pool: pool,
		orgID:   uuid.New(),
		venueID: uuid.New(),
		eventID: uuid.New(),
	}
	suffix := f.orgID.String()[:8]
	steps := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`,
			[]any{f.orgID, "GA46 Org " + suffix, "ga46-" + suffix}},
		{`INSERT INTO venues (id, org_id, name) VALUES ($1, $2, $3)`,
			[]any{f.venueID, f.orgID, "GA46 Venue " + suffix}},
		{`INSERT INTO events (id, org_id, name, status, visibility)
		  VALUES ($1, $2, $3, 'draft', 'private')`,
			[]any{f.eventID, f.orgID, "GA46 Event " + suffix}},
	}
	for i, s := range steps {
		if _, err := pool.Exec(ctx, s.sql, s.args...); err != nil {
			f.cleanup()
			t.Fatalf("GA46 fixture step %d failed: %v", i, err)
		}
	}
	return f
}

func (f *ga46Fixture) cleanup() {
	ctx := context.Background()
	for _, sql := range []string{
		`DELETE FROM session_seats WHERE session_id IN (SELECT id FROM sessions WHERE event_id = $1)`,
		`DELETE FROM inventory_ledger WHERE session_id IN (SELECT id FROM sessions WHERE event_id = $1)`,
		`DELETE FROM ticket_tiers WHERE session_id IN (SELECT id FROM sessions WHERE event_id = $1)`,
		`DELETE FROM sessions WHERE event_id = $1`,
		`DELETE FROM events WHERE id = $1`,
	} {
		if _, err := f.pool.Exec(ctx, sql, f.eventID); err != nil {
			f.t.Logf("GA46 cleanup (%.60s): %v", sql, err)
		}
	}
	for _, s := range []struct {
		sql string
		arg uuid.UUID
	}{
		{`DELETE FROM venues WHERE id = $1`, f.venueID},
		{`DELETE FROM organizations WHERE id = $1`, f.orgID},
	} {
		if _, err := f.pool.Exec(ctx, s.sql, s.arg); err != nil {
			f.t.Logf("GA46 cleanup (%.60s): %v", s.sql, err)
		}
	}
}

// createSession drives the real POST .../sessions handler and returns the
// created session id.
func (f *ga46Fixture) createSession(srv *Server, body map[string]any) (uuid.UUID, *httptest.ResponseRecorder) {
	f.t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		f.t.Fatalf("marshal session body: %v", err)
	}
	req := ga46AdminRequest(http.MethodPost, raw, map[string]string{
		"org_id":   f.orgID.String(),
		"event_id": f.eventID.String(),
	})
	w := httptest.NewRecorder()
	srv.catalogHandler().HandleCreateSession(w, req)
	if w.Code != http.StatusCreated {
		return uuid.Nil, w
	}
	var out struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		f.t.Fatalf("decode session response: %v; body=%s", err, w.Body.String())
	}
	id, err := uuid.Parse(out.Session.ID)
	if err != nil {
		f.t.Fatalf("parse session id %q: %v", out.Session.ID, err)
	}
	return id, w
}

// ga46TierResponse is the slice of the tier envelope these tests assert on.
type ga46TierResponse struct {
	Tier struct {
		ID       string `json:"id"`
		Capacity *int32 `json:"capacity"`
		IsOpen   bool   `json:"is_open"`
	} `json:"tier"`
}

func (f *ga46Fixture) createTier(
	srv *Server, sessionID uuid.UUID, body map[string]any,
) (ga46TierResponse, *httptest.ResponseRecorder) {
	f.t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		f.t.Fatalf("marshal tier body: %v", err)
	}
	req := ga46AdminRequest(http.MethodPost, raw, map[string]string{
		"org_id":     f.orgID.String(),
		"event_id":   f.eventID.String(),
		"session_id": sessionID.String(),
	})
	w := httptest.NewRecorder()
	srv.catalogHandler().HandleCreateTier(w, req)
	var out ga46TierResponse
	if w.Code == http.StatusCreated {
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			f.t.Fatalf("decode tier response: %v; body=%s", err, w.Body.String())
		}
	}
	return out, w
}

func (f *ga46Fixture) patchTier(
	srv *Server, sessionID, tierID uuid.UUID, body map[string]any,
) (ga46TierResponse, *httptest.ResponseRecorder) {
	f.t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		f.t.Fatalf("marshal tier patch: %v", err)
	}
	req := ga46AdminRequest(http.MethodPatch, raw, map[string]string{
		"org_id":     f.orgID.String(),
		"event_id":   f.eventID.String(),
		"session_id": sessionID.String(),
		"id":         tierID.String(),
	})
	w := httptest.NewRecorder()
	srv.catalogHandler().HandleUpdateTier(w, req)
	var out ga46TierResponse
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			f.t.Fatalf("decode tier patch response: %v; body=%s", err, w.Body.String())
		}
	}
	return out, w
}

func (f *ga46Fixture) deleteTier(srv *Server, sessionID, tierID uuid.UUID) *httptest.ResponseRecorder {
	f.t.Helper()
	req := ga46AdminRequest(http.MethodDelete, nil, map[string]string{
		"org_id":     f.orgID.String(),
		"event_id":   f.eventID.String(),
		"session_id": sessionID.String(),
		"id":         tierID.String(),
	})
	w := httptest.NewRecorder()
	srv.catalogHandler().HandleDeleteTier(w, req)
	return w
}

// ga46AdminRequest is ga45AdminRequest with a UUID actor: the tier write
// handlers audit who changed what, and audit_events.actor_id is a uuid column
// (AGENTS.md) — a label like "ga45-integration-actor" aborts the transaction
// with SQLSTATE 22P02 and surfaces as tier.audit_failed.
func ga46AdminRequest(method string, body []byte, params map[string]string) *http.Request {
	req := ga45AdminRequest(method, body, params)
	ctx := auth.WithActor(req.Context(),
		auth.Actor{ID: uuid.NewString(), Type: auth.ActorTypeUser})
	ctx = auth.WithSuperadminOrgAccess(ctx)
	return req.WithContext(ctx)
}

// ga46SessionBody is the smallest valid create body: the fixture venue has no
// geography, so the currency must be explicit (AB-38).
func ga46SessionBody(venueID uuid.UUID, capacityOverride int32) map[string]any {
	return map[string]any{
		"venue_id":          venueID.String(),
		"start_at":          "2026-11-20T18:00:00Z",
		"end_at":            "2026-11-20T21:00:00Z",
		"currency":          "EUR",
		"capacity_override": capacityOverride,
	}
}

func ga46Int(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sql string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := pool.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
		t.Fatalf("query (%.70s): %v", sql, err)
	}
	return n
}

func ga46ErrorCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var env struct {
		Error struct {
			Code    string         `json:"code"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode error envelope: %v; body=%s", err, w.Body.String())
	}
	return env.Error.Code
}

// ─────────────────────────────────────────────────────────────────────────────
// Session create → no places; first category settles the capacity
// ─────────────────────────────────────────────────────────────────────────────

func TestGA46_SessionCreateHasNoPlaces_FirstCategorySetsCapacity(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	f := newGA46Fixture(t, ctx, pool)
	defer f.cleanup()
	srv := buildIntegrationResetServer(t, pool)

	sessionID, w := f.createSession(srv, ga46SessionBody(f.venueID, 100))
	if sessionID == uuid.Nil {
		t.Fatalf("create session: status = %d, body = %s", w.Code, w.Body.String())
	}

	if n := ga46Int(t, ctx, pool,
		`SELECT count(*) FROM session_seats WHERE session_id = $1`, sessionID); n != 0 {
		t.Fatalf("a freshly created GA session has %d places, want 0 — the capacity "+
			"must come from its categories", n)
	}

	// No capacity in the body: the wave-A default is the whole session
	// capacity, because no category claims anything yet.
	tier, w := f.createTier(srv, sessionID, map[string]any{
		"name": "Standing", "pricing_mode": "fixed", "price_amount": 2500,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create tier: status = %d, body = %s", w.Code, w.Body.String())
	}
	if tier.Tier.Capacity == nil || *tier.Tier.Capacity != 100 {
		t.Fatalf("tier capacity = %v, want 100 (session capacity minus nothing)", tier.Tier.Capacity)
	}
	if !tier.Tier.IsOpen {
		t.Errorf("a freshly created category is closed, want open")
	}

	tierID := uuid.MustParse(tier.Tier.ID)
	if n := ga46Int(t, ctx, pool,
		`SELECT count(*) FROM session_seats WHERE session_id=$1 AND tier_id=$2 AND kind='ga_unit'`,
		sessionID, tierID); n != 100 {
		t.Fatalf("category owns %d places, want 100", n)
	}
	if n := ga46Int(t, ctx, pool,
		`SELECT capacity_total FROM sessions WHERE id=$1`, sessionID); n != 100 {
		t.Fatalf("sessions.capacity_total = %d, want 100", n)
	}
	if n := ga46Int(t, ctx, pool,
		`SELECT capacity_total FROM inventory_ledger WHERE session_id=$1 AND tier_id IS NULL`,
		sessionID); n != 100 {
		t.Fatalf("ledger capacity_total = %d, want 100", n)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// PATCH .../sessions — capacity_override is inert once a category owns places
// ─────────────────────────────────────────────────────────────────────────────

func TestGA46_SessionPatchCapacityOverrideIgnoredWithWarning(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	f := newGA46Fixture(t, ctx, pool)
	defer f.cleanup()
	srv := buildIntegrationResetServer(t, pool)

	sessionID, w := f.createSession(srv, ga46SessionBody(f.venueID, 40))
	if sessionID == uuid.Nil {
		t.Fatalf("create session: status = %d, body = %s", w.Code, w.Body.String())
	}
	if _, w := f.createTier(srv, sessionID, map[string]any{
		"name": "Standing", "pricing_mode": "fixed", "price_amount": 2500, "capacity": 40,
	}); w.Code != http.StatusCreated {
		t.Fatalf("create tier: status = %d, body = %s", w.Code, w.Body.String())
	}

	raw, err := json.Marshal(map[string]any{"capacity_override": 250})
	if err != nil {
		t.Fatalf("marshal patch: %v", err)
	}
	req := ga46AdminRequest(http.MethodPatch, raw, map[string]string{
		"org_id":   f.orgID.String(),
		"event_id": f.eventID.String(),
		"id":       sessionID.String(),
	})
	rec := httptest.NewRecorder()
	srv.catalogHandler().HandleUpdateSession(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch session: status = %d, want 200 (wave-A compatibility); body = %s",
			rec.Code, rec.Body.String())
	}

	var out struct {
		Session struct {
			CapacityTotal    int32  `json:"capacity_total"`
			CapacityOverride *int32 `json:"capacity_override"`
		} `json:"session"`
		Warnings []struct {
			Code string `json:"code"`
		} `json:"warnings"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode patch response: %v; body=%s", err, rec.Body.String())
	}
	found := false
	for _, warn := range out.Warnings {
		if warn.Code == "session.capacity_is_category_sum" {
			found = true
		}
	}
	if !found {
		t.Errorf("warnings = %+v, want session.capacity_is_category_sum", out.Warnings)
	}
	if out.Session.CapacityTotal != 40 {
		t.Errorf("capacity_total = %d, want 40 (the category sum, unchanged)", out.Session.CapacityTotal)
	}
	if n := ga46Int(t, ctx, pool,
		`SELECT count(*) FROM session_seats WHERE session_id=$1`, sessionID); n != 40 {
		t.Errorf("places = %d, want 40 (the PATCH must not resize anything)", n)
	}
	if n := ga46Int(t, ctx, pool,
		`SELECT capacity_total FROM sessions WHERE id=$1`, sessionID); n != 40 {
		t.Errorf("stored capacity_total = %d, want 40", n)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// POST .../tiers — explicit quantity, derived default, exhausted default
// ─────────────────────────────────────────────────────────────────────────────

func TestGA46_TierCreateQuantityRules(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	f := newGA46Fixture(t, ctx, pool)
	defer f.cleanup()
	srv := buildIntegrationResetServer(t, pool)

	sessionID, w := f.createSession(srv, ga46SessionBody(f.venueID, 30))
	if sessionID == uuid.Nil {
		t.Fatalf("create session: status = %d, body = %s", w.Code, w.Body.String())
	}

	first, w := f.createTier(srv, sessionID, map[string]any{
		"name": "Front", "pricing_mode": "fixed", "price_amount": 5000, "capacity": 10,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("first tier: status = %d, body = %s", w.Code, w.Body.String())
	}
	if first.Tier.Capacity == nil || *first.Tier.Capacity != 10 {
		t.Fatalf("first tier capacity = %v, want the requested 10", first.Tier.Capacity)
	}
	if n := ga46Int(t, ctx, pool,
		`SELECT capacity_total FROM sessions WHERE id=$1`, sessionID); n != 10 {
		t.Fatalf("capacity_total after the first category = %d, want 10 (its quantity)", n)
	}

	// No capacity: the remaining 30 − 10 = 20 is the wave-A default.
	second, w := f.createTier(srv, sessionID, map[string]any{
		"name": "Back", "pricing_mode": "fixed", "price_amount": 2500,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("second tier: status = %d, body = %s", w.Code, w.Body.String())
	}
	if second.Tier.Capacity == nil || *second.Tier.Capacity != 20 {
		t.Fatalf("second tier capacity = %v, want the derived default 20", second.Tier.Capacity)
	}
	if n := ga46Int(t, ctx, pool,
		`SELECT capacity_total FROM sessions WHERE id=$1`, sessionID); n != 30 {
		t.Fatalf("capacity_total = %d, want 30 (10 + 20)", n)
	}

	// Nothing left to derive from: decision 3 forbids a category without a
	// quantity, so this is a 400 rather than a silently unsellable category.
	_, w = f.createTier(srv, sessionID, map[string]any{
		"name": "Overflow", "pricing_mode": "fixed", "price_amount": 100,
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("third tier: status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
	if code := ga46ErrorCode(t, w); code != "tier.capacity_required" {
		t.Errorf("third tier error code = %q, want tier.capacity_required", code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// PATCH .../tiers — grow, shrink, below-used, open/close
// ─────────────────────────────────────────────────────────────────────────────

func TestGA46_TierUpdateQuantityAndOpenFlag(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	f := newGA46Fixture(t, ctx, pool)
	defer f.cleanup()
	srv := buildIntegrationResetServer(t, pool)

	sessionID, w := f.createSession(srv, ga46SessionBody(f.venueID, 5))
	if sessionID == uuid.Nil {
		t.Fatalf("create session: status = %d, body = %s", w.Code, w.Body.String())
	}
	created, w := f.createTier(srv, sessionID, map[string]any{
		"name": "Standing", "pricing_mode": "fixed", "price_amount": 2500, "capacity": 5,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create tier: status = %d, body = %s", w.Code, w.Body.String())
	}
	tierID := uuid.MustParse(created.Tier.ID)

	// Grow.
	grown, w := f.patchTier(srv, sessionID, tierID, map[string]any{"capacity": 8})
	if w.Code != http.StatusOK {
		t.Fatalf("grow: status = %d, body = %s", w.Code, w.Body.String())
	}
	if grown.Tier.Capacity == nil || *grown.Tier.Capacity != 8 {
		t.Fatalf("grown capacity = %v, want 8", grown.Tier.Capacity)
	}
	if n := ga46Int(t, ctx, pool,
		`SELECT count(*) FROM session_seats WHERE session_id=$1 AND tier_id=$2`,
		sessionID, tierID); n != 8 {
		t.Fatalf("places after grow = %d, want 8", n)
	}

	// Two places are taken; the quantity can no longer drop below them.
	if _, err := pool.Exec(ctx, `
		UPDATE session_seats SET status='held'
		WHERE id IN (SELECT id FROM session_seats
		             WHERE session_id=$1 AND tier_id=$2 AND status='available'
		             ORDER BY seat_key LIMIT 2)`, sessionID, tierID); err != nil {
		t.Fatalf("hold two places: %v", err)
	}
	_, w = f.patchTier(srv, sessionID, tierID, map[string]any{"capacity": 1})
	if w.Code != http.StatusConflict {
		t.Fatalf("shrink below used: status = %d, want 409; body = %s", w.Code, w.Body.String())
	}
	if code := ga46ErrorCode(t, w); code != "tier.quantity_below_used" {
		t.Errorf("shrink below used: code = %q, want tier.quantity_below_used", code)
	}
	if n := ga46Int(t, ctx, pool,
		`SELECT count(*) FROM session_seats WHERE session_id=$1 AND tier_id=$2`,
		sessionID, tierID); n != 8 {
		t.Fatalf("a refused shrink removed places: %d left, want 8 untouched", n)
	}

	// Shrink down to exactly what is taken plus one.
	shrunk, w := f.patchTier(srv, sessionID, tierID, map[string]any{"capacity": 3})
	if w.Code != http.StatusOK {
		t.Fatalf("shrink: status = %d, body = %s", w.Code, w.Body.String())
	}
	if shrunk.Tier.Capacity == nil || *shrunk.Tier.Capacity != 3 {
		t.Fatalf("shrunk capacity = %v, want 3", shrunk.Tier.Capacity)
	}
	if n := ga46Int(t, ctx, pool,
		`SELECT count(*) FROM session_seats WHERE session_id=$1 AND tier_id=$2`,
		sessionID, tierID); n != 3 {
		t.Fatalf("places after shrink = %d, want 3", n)
	}
	if n := ga46Int(t, ctx, pool,
		`SELECT capacity_total FROM sessions WHERE id=$1`, sessionID); n != 3 {
		t.Fatalf("capacity_total after shrink = %d, want 3", n)
	}

	// Close, then re-open.
	closed, w := f.patchTier(srv, sessionID, tierID, map[string]any{"is_open": false})
	if w.Code != http.StatusOK {
		t.Fatalf("close: status = %d, body = %s", w.Code, w.Body.String())
	}
	if closed.Tier.IsOpen {
		t.Errorf("is_open = true after closing the category")
	}
	reopened, w := f.patchTier(srv, sessionID, tierID, map[string]any{"is_open": true})
	if w.Code != http.StatusOK {
		t.Fatalf("reopen: status = %d, body = %s", w.Code, w.Body.String())
	}
	if !reopened.Tier.IsOpen {
		t.Errorf("is_open = false after reopening the category")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// DELETE .../tiers — in use vs removable
// ─────────────────────────────────────────────────────────────────────────────

func TestGA46_TierDeleteRemovesPlacesAndRefusesWhenInUse(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	f := newGA46Fixture(t, ctx, pool)
	defer f.cleanup()
	srv := buildIntegrationResetServer(t, pool)

	sessionID, w := f.createSession(srv, ga46SessionBody(f.venueID, 12))
	if sessionID == uuid.Nil {
		t.Fatalf("create session: status = %d, body = %s", w.Code, w.Body.String())
	}
	keep, w := f.createTier(srv, sessionID, map[string]any{
		"name": "Keep", "pricing_mode": "fixed", "price_amount": 2500, "capacity": 8,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create keep tier: status = %d, body = %s", w.Code, w.Body.String())
	}
	drop, w := f.createTier(srv, sessionID, map[string]any{
		"name": "Drop", "pricing_mode": "fixed", "price_amount": 1500, "capacity": 4,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create drop tier: status = %d, body = %s", w.Code, w.Body.String())
	}
	keepID, dropID := uuid.MustParse(keep.Tier.ID), uuid.MustParse(drop.Tier.ID)

	// One of the doomed category's places is sold: it may only be closed.
	if _, err := pool.Exec(ctx, `
		UPDATE session_seats SET status='sold'
		WHERE id = (SELECT id FROM session_seats
		            WHERE session_id=$1 AND tier_id=$2 ORDER BY seat_key LIMIT 1)`,
		sessionID, dropID); err != nil {
		t.Fatalf("sell one place: %v", err)
	}
	w = f.deleteTier(srv, sessionID, dropID)
	if w.Code != http.StatusConflict {
		t.Fatalf("delete in-use category: status = %d, want 409; body = %s", w.Code, w.Body.String())
	}
	if code := ga46ErrorCode(t, w); code != "tier.in_use" {
		t.Errorf("delete in-use category: code = %q, want tier.in_use", code)
	}

	// Give the place back; now the category is removable.
	if _, err := pool.Exec(ctx,
		`UPDATE session_seats SET status='available' WHERE session_id=$1 AND tier_id=$2`,
		sessionID, dropID); err != nil {
		t.Fatalf("release the sold place: %v", err)
	}
	w = f.deleteTier(srv, sessionID, dropID)
	if w.Code != http.StatusOK {
		t.Fatalf("delete category: status = %d, want 200; body = %s", w.Code, w.Body.String())
	}
	if n := ga46Int(t, ctx, pool,
		`SELECT count(*) FROM session_seats WHERE session_id=$1 AND tier_id=$2`,
		sessionID, dropID); n != 0 {
		t.Fatalf("the deleted category still owns %d places, want 0", n)
	}
	if n := ga46Int(t, ctx, pool,
		`SELECT capacity_total FROM sessions WHERE id=$1`, sessionID); n != 8 {
		t.Fatalf("capacity_total after the delete = %d, want 8 (only the kept category)", n)
	}
	if n := ga46Int(t, ctx, pool,
		`SELECT count(*) FROM session_seats WHERE session_id=$1 AND tier_id=$2`,
		sessionID, keepID); n != 8 {
		t.Fatalf("the kept category owns %d places, want 8 untouched", n)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Decision 10 — a GA category added to a seated session turns it hybrid
// ─────────────────────────────────────────────────────────────────────────────

func TestGA46_GACategoryOnSeatedSessionTurnsItHybrid(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	f := newGA46Fixture(t, ctx, pool)
	defer f.cleanup()
	srv := buildIntegrationResetServer(t, pool)

	// A minimal seated session: sessions_seated_requires_plan wants a bound
	// version, and two seats stand in for the plan geometry.
	planID, versionID, sessionID := uuid.New(), uuid.New(), uuid.New()
	seatedTierID := uuid.New()
	// A plain defer, NOT t.Cleanup: the fixture's own `defer f.cleanup()`
	// above deletes the venue and the organization, and a deferred call runs
	// before every t.Cleanup callback — the plan rows below would still
	// reference them and the whole fixture would leak.
	defer func() {
		for _, sql := range []string{
			`DELETE FROM session_seats WHERE session_id = $1`,
			`DELETE FROM inventory_ledger WHERE session_id = $1`,
			`DELETE FROM ticket_tiers WHERE session_id = $1`,
			`DELETE FROM sessions WHERE id = $1`,
		} {
			if _, err := pool.Exec(context.Background(), sql, sessionID); err != nil {
				t.Logf("GA46 seated cleanup: %v", err)
			}
		}
		if _, err := pool.Exec(context.Background(),
			`DELETE FROM seating_plan_versions WHERE id = $1`, versionID); err != nil {
			t.Logf("GA46 seated cleanup: %v", err)
		}
		if _, err := pool.Exec(context.Background(),
			`DELETE FROM seating_plans WHERE id = $1`, planID); err != nil {
			t.Logf("GA46 seated cleanup: %v", err)
		}
	}()
	steps := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO seating_plans (id, venue_id, owner_org_id, name, plan_type, status)
		  VALUES ($1, $2, $3, $4, 'assigned_seats', 'active')`,
			[]any{planID, f.venueID, f.orgID, "GA46 Plan " + planID.String()[:8]}},
		{`INSERT INTO seating_plan_versions
		    (id, seating_plan_id, version_number, geometry, geometry_checksum, capacity_seated)
		  VALUES ($1, $2, 1, '{}'::jsonb, $3, 2)`,
			[]any{versionID, planID, "ga46-" + versionID.String()[:8]}},
		{`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at,
		    capacity_total, status, admission_mode, currency, currency_source,
		    seating_plan_version_id)
		  VALUES ($1, $2, $3, now() + interval '30 days',
		    now() + interval '30 days 2 hours', 2, 'draft', 'assigned_seats',
		    'EUR', 'override', $4)`,
			[]any{sessionID, f.eventID, f.venueID, versionID}},
		{`INSERT INTO ticket_tiers (id, session_id, name, pricing_mode, price_amount,
		    currency, sort_order)
		  VALUES ($1, $2, 'Seated', 'fixed', 5000, 'EUR', 0)`,
			[]any{seatedTierID, sessionID}},
		{`INSERT INTO session_seats
		    (session_id, seat_key, sector_name, row_name, seat_number, tier_id, status, kind)
		  VALUES ($1, 'A|1|1', 'A', '1', '1', $2, 'available', 'seat'),
		         ($1, 'A|1|2', 'A', '1', '2', $2, 'available', 'seat')`,
			[]any{sessionID, seatedTierID}},
		{`INSERT INTO inventory_ledger (session_id, tier_id, capacity_total)
		  VALUES ($1, NULL, 2)`, []any{sessionID}},
	}
	for i, s := range steps {
		if _, err := pool.Exec(ctx, s.sql, s.args...); err != nil {
			t.Fatalf("GA46 seated fixture step %d: %v", i, err)
		}
	}

	// A quantity on a seated session means "a category without seats"
	// (decision 10): it becomes a GA category and the session turns hybrid.
	ga, w := f.createTier(srv, sessionID, map[string]any{
		"name": "Standing", "pricing_mode": "fixed", "price_amount": 1500, "capacity": 6,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create GA category on a seated session: status = %d, body = %s",
			w.Code, w.Body.String())
	}
	gaTierID := uuid.MustParse(ga.Tier.ID)

	var mode string
	if err := pool.QueryRow(ctx,
		`SELECT admission_mode FROM sessions WHERE id=$1`, sessionID).Scan(&mode); err != nil {
		t.Fatalf("read admission_mode: %v", err)
	}
	if mode != "hybrid" {
		t.Errorf("admission_mode = %q, want hybrid", mode)
	}
	if n := ga46Int(t, ctx, pool,
		`SELECT count(*) FROM session_seats WHERE session_id=$1 AND tier_id=$2 AND kind='ga_unit'`,
		sessionID, gaTierID); n != 6 {
		t.Fatalf("GA category owns %d places, want 6", n)
	}
	if n := ga46Int(t, ctx, pool,
		`SELECT capacity_total FROM sessions WHERE id=$1`, sessionID); n != 8 {
		t.Fatalf("capacity_total = %d, want 8 (2 plan seats + 6 GA places)", n)
	}

	// The seated category's quantity is read-only, and it cannot be deleted.
	_, w = f.patchTier(srv, sessionID, seatedTierID, map[string]any{"capacity": 4})
	if w.Code != http.StatusConflict {
		t.Fatalf("resize seated category: status = %d, want 409; body = %s", w.Code, w.Body.String())
	}
	if code := ga46ErrorCode(t, w); code != "tier.seated_category" {
		t.Errorf("resize seated category: code = %q, want tier.seated_category", code)
	}
	w = f.deleteTier(srv, sessionID, seatedTierID)
	if w.Code != http.StatusConflict {
		t.Fatalf("delete seated category: status = %d, want 409; body = %s", w.Code, w.Body.String())
	}
	if code := ga46ErrorCode(t, w); code != "tier.seated_category" {
		t.Errorf("delete seated category: code = %q, want tier.seated_category", code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// arena-seed — a seeded stand can sell
// ─────────────────────────────────────────────────────────────────────────────

// TestGA46_SeededStandSellsItsGACategory closes the loop on the arena-seed
// half of step 3: before it seeded categories, the stand's one GA session had
// a capacity and no place, so every hold answered "sold out". The ids are the
// seed's own fixed ones (cmd/arena-seed); the test skips when the database
// under test was never seeded.
func TestGA46_SeededStandSellsItsGACategory(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()

	const seededSessionID = "fe0000a1-0000-7000-8000-000000000002"
	sessionID := uuid.MustParse(seededSessionID)

	var orgID, channelID, tierID uuid.UUID
	err := pool.QueryRow(ctx, `
		SELECT e.org_id,
		       (SELECT sc.id FROM sales_channels sc
		         WHERE sc.org_id = e.org_id AND sc.deleted_at IS NULL
		         ORDER BY sc.created_at LIMIT 1),
		       (SELECT tt.id FROM ticket_tiers tt
		         WHERE tt.session_id = s.id AND tt.deleted_at IS NULL
		         ORDER BY tt.sort_order LIMIT 1)
		FROM   sessions s JOIN events e ON e.id = s.event_id
		WHERE  s.id = $1 AND s.deleted_at IS NULL`, sessionID).
		Scan(&orgID, &channelID, &tierID)
	if err != nil {
		t.Skipf("seeded session %s not present (run arena-seed): %v", seededSessionID, err)
	}
	if tierID == uuid.Nil {
		t.Fatalf("the seeded GA session carries no category — a seeded stand cannot sell")
	}

	before := ga46Int(t, ctx, pool,
		`SELECT count(*) FROM session_seats
		  WHERE session_id=$1 AND tier_id=$2 AND status='available'`, sessionID, tierID)
	if before < 2 {
		t.Fatalf("the seeded category has %d available places, want at least 2", before)
	}

	srv := buildIntegrationResetServer(t, pool)
	raw, err := json.Marshal(map[string]any{
		"session_id": sessionID.String(),
		"channel_id": channelID.String(),
		"org_id":     orgID.String(),
		"tier_id":    tierID.String(),
		"quantity":   2,
	})
	if err != nil {
		t.Fatalf("marshal reservation: %v", err)
	}
	// The reservation stamps reservations.user_id from the actor when it
	// parses as a UUID, and the seed's users are not this test's to borrow —
	// ga45AdminRequest's deliberately non-UUID actor avoids the FK.
	req := ga45AdminRequest(http.MethodPost, raw, nil)
	rec := httptest.NewRecorder()
	srv.checkoutHandler().HandleCreateReservation(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("reservation on the seeded session: status = %d, want 201; body = %s",
			rec.Code, rec.Body.String())
	}

	var reservationID uuid.UUID
	if err := pool.QueryRow(ctx, `
		SELECT DISTINCT ss.reservation_id FROM session_seats ss
		 WHERE ss.session_id=$1 AND ss.tier_id=$2 AND ss.status='held'`,
		sessionID, tierID).Scan(&reservationID); err != nil {
		t.Fatalf("the sale took no place of the seeded category: %v", err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		for _, sql := range []string{
			`UPDATE session_seats SET status='available', reservation_id=NULL
			  WHERE reservation_id = $1`,
			`DELETE FROM reservation_ga_items WHERE reservation_id = $1`,
			`DELETE FROM reservation_seats WHERE reservation_id = $1`,
			`DELETE FROM reservations WHERE id = $1`,
		} {
			if _, err := pool.Exec(bg, sql, reservationID); err != nil {
				t.Logf("GA46 seeded-sale cleanup: %v", err)
			}
		}
		if _, err := pool.Exec(bg,
			`UPDATE inventory_ledger SET capacity_held = 0 WHERE session_id = $1 AND tier_id IS NULL`,
			sessionID); err != nil {
			t.Logf("GA46 seeded-sale ledger cleanup: %v", err)
		}
	})

	if n := ga46Int(t, ctx, pool,
		`SELECT count(*) FROM session_seats
		  WHERE session_id=$1 AND tier_id=$2 AND status='held'`, sessionID, tierID); n != 2 {
		t.Errorf("held places = %d, want 2", n)
	}
}
