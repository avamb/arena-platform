//go:build integration

// ga_quota_sales_45_integration_test.go covers the sales paths that plan
// 08_architecture/23 steps 4 and 5 moved onto CATEGORY-OWNED PLACES, against
// a live database through the real handlers:
//
//   - POST /v1/reservations (quantity branch) refuses a GA hold that names
//     no category, and refuses one whose category is closed or off-sale;
//   - a free (complimentary) GA ticket consumes one of its category's
//     places and revoking it gives that place back (decision 6);
//   - an external partner quota BLOCKS places of its category and
//     reconciliation settles them into sold / available (decision 6);
//   - the public widget feed reports each category's remaining places and
//     its open flag (step 5).
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
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
)

// ─────────────────────────────────────────────────────────────────────────────
// Fixture: one GA session whose single category owns every place
// ─────────────────────────────────────────────────────────────────────────────

type gaQuotaFixture struct {
	t         *testing.T
	pool      *pgxpool.Pool
	orgID     uuid.UUID
	venueID   uuid.UUID
	eventID   uuid.UUID
	channelID uuid.UUID
	sessionID uuid.UUID
	tierID    uuid.UUID
	tokenID   uuid.UUID
	feedToken string
	quantity  int32
}

func newGAQuotaFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool, quantity int32) *gaQuotaFixture {
	t.Helper()
	f := &gaQuotaFixture{
		t: t, pool: pool,
		orgID:     uuid.New(),
		venueID:   uuid.New(),
		eventID:   uuid.New(),
		channelID: uuid.New(),
		sessionID: uuid.New(),
		tierID:    uuid.New(),
		tokenID:   uuid.New(),
		quantity:  quantity,
	}
	suffix := f.orgID.String()[:8]
	f.feedToken = "ga45-feed-" + suffix

	steps := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`,
			[]any{f.orgID, "GA45 Org " + suffix, "ga45-" + suffix}},
		{`INSERT INTO venues (id, org_id, name) VALUES ($1, $2, $3)`,
			[]any{f.venueID, f.orgID, "GA45 Venue " + suffix}},
		{`INSERT INTO events (id, org_id, name, status, visibility)
		  VALUES ($1, $2, $3, 'published', 'public')`,
			[]any{f.eventID, f.orgID, "GA45 Event " + suffix}},
		{`INSERT INTO sales_channels (id, org_id, name, provider, payment_mode)
		  VALUES ($1, $2, $3, 'stripe', 'direct_merchant')`,
			[]any{f.channelID, f.orgID, "GA45 Channel " + suffix}},
		{`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at,
		    capacity_total, status, admission_mode, currency, currency_source)
		  VALUES ($1, $2, $3, now() + interval '30 days',
		    now() + interval '30 days 3 hours', $4, 'scheduled',
		    'general_admission', 'EUR', 'override')`,
			[]any{f.sessionID, f.eventID, f.venueID, quantity}},
		// Migration 0101: the category OWNS its places, keyed under its own
		// 'ga|t<unit_seq>' prefix.
		{`INSERT INTO ticket_tiers (id, session_id, name, pricing_mode, price_amount,
		    currency, sort_order, capacity, unit_seq, is_open)
		  VALUES ($1, $2, 'GA45 Tier', 'fixed', 2500, 'EUR', 0, $3, 1, true)`,
			[]any{f.tierID, f.sessionID, quantity}},
		{`INSERT INTO inventory_ledger (session_id, tier_id, capacity_total)
		  VALUES ($1, NULL, $2)`,
			[]any{f.sessionID, quantity}},
		{`INSERT INTO session_seats
		    (session_id, seat_key, sector_name, row_name, seat_number,
		     tier_id, status, kind)
		  SELECT $1, 'ga|t1|' || lpad(gs::text, 6, '0'), '', '', '',
		         $3, 'available', 'ga_unit'
		  FROM generate_series(1, $2::int) gs`,
			[]any{f.sessionID, quantity, f.tierID}},
		{`INSERT INTO agent_feed_tokens (id, token, sales_channel_id, label, is_active)
		  VALUES ($1, $2, $3, 'ga45', true)`,
			[]any{f.tokenID, f.feedToken, f.channelID}},
		{`INSERT INTO event_publications (event_id, feed_token_id) VALUES ($1, $2)`,
			[]any{f.eventID, f.tokenID}},
	}
	for i, s := range steps {
		if _, err := pool.Exec(ctx, s.sql, s.args...); err != nil {
			f.cleanup()
			t.Fatalf("GA45 fixture step %d failed: %v", i, err)
		}
	}
	return f
}

func (f *gaQuotaFixture) cleanup() {
	ctx := context.Background()
	stmts := []struct {
		sql string
		arg any
	}{
		{`DELETE FROM delivery_jobs WHERE ticket_id IN (SELECT id FROM tickets WHERE session_id = $1)`, f.sessionID},
		{`DELETE FROM ticket_credentials WHERE ticket_id IN (SELECT id FROM tickets WHERE session_id = $1)`, f.sessionID},
		{`DELETE FROM barcodes WHERE ticket_id IN (SELECT id FROM tickets WHERE session_id = $1)`, f.sessionID},
		{`DELETE FROM tickets WHERE session_id = $1`, f.sessionID},
		{`DELETE FROM complimentary_issuances WHERE session_id = $1`, f.sessionID},
		{`DELETE FROM external_allocations WHERE session_id = $1`, f.sessionID},
		{`DELETE FROM reservation_ga_items WHERE reservation_id IN (SELECT id FROM reservations WHERE session_id = $1)`, f.sessionID},
		{`DELETE FROM reservation_seats WHERE reservation_id IN (SELECT id FROM reservations WHERE session_id = $1)`, f.sessionID},
		{`DELETE FROM session_seats WHERE session_id = $1`, f.sessionID},
		{`DELETE FROM reservations WHERE session_id = $1`, f.sessionID},
		{`DELETE FROM inventory_ledger WHERE session_id = $1`, f.sessionID},
		{`DELETE FROM ticket_tiers WHERE session_id = $1`, f.sessionID},
		{`DELETE FROM event_publications WHERE event_id = $1`, f.eventID},
		{`DELETE FROM agent_feed_tokens WHERE id = $1`, f.tokenID},
		{`DELETE FROM sessions WHERE id = $1`, f.sessionID},
		{`DELETE FROM sales_channels WHERE id = $1`, f.channelID},
		{`DELETE FROM events WHERE id = $1`, f.eventID},
		{`DELETE FROM venues WHERE id = $1`, f.venueID},
		{`DELETE FROM organizations WHERE id = $1`, f.orgID},
	}
	for _, s := range stmts {
		if _, err := f.pool.Exec(ctx, s.sql, s.arg); err != nil {
			f.t.Logf("GA45 cleanup (%.60s): %v", s.sql, err)
		}
	}
}

func (f *gaQuotaFixture) countPlaces(ctx context.Context, status string) int64 {
	f.t.Helper()
	var n int64
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM session_seats
		 WHERE session_id=$1 AND tier_id=$2 AND status=$3`,
		f.sessionID, f.tierID, status).Scan(&n); err != nil {
		f.t.Fatalf("count %s places: %v", status, err)
	}
	return n
}

func (f *gaQuotaFixture) close() {
	f.t.Helper()
	if _, err := f.pool.Exec(context.Background(),
		`UPDATE ticket_tiers SET is_open=false WHERE id=$1`, f.tierID); err != nil {
		f.t.Fatalf("close category: %v", err)
	}
}

// ga45AdminRequest builds a superadmin-bypass request with chi URL params
// pre-populated, so a handler method can be called directly against a real
// pool (the channels_ttl_integration_test.go pattern).
func ga45AdminRequest(method string, body []byte, params map[string]string) *http.Request {
	var req *http.Request
	if body != nil {
		req = httptest.NewRequest(method, "/", bytes.NewReader(body))
	} else {
		req = httptest.NewRequest(method, "/", nil)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Admin-Reason", "GA quota integration test")

	rctx := chi.NewRouteContext()
	for k, v := range params {
		rctx.URLParams.Add(k, v)
	}
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	// Deliberately not a UUID: HandleCreateReservation stamps
	// reservations.user_id from the actor when it parses as one, and this
	// fixture seeds no users — a FK violation would masquerade as a
	// reservation failure.
	ctx = auth.WithActor(ctx, auth.Actor{ID: "ga45-integration-actor", Type: auth.ActorTypeUser})
	ctx = auth.WithSuperadminOrgAccess(ctx)
	return req.WithContext(ctx)
}

// ─────────────────────────────────────────────────────────────────────────────
// REST POST /v1/reservations — the quantity branch
// ─────────────────────────────────────────────────────────────────────────────

// TestGA45_RESTReservation_RequiresCategoryAndHonoursTheGate pins the REST
// surface of step 4: a GA hold must name its category (a NULL one matches no
// place since migration 0101 and would read as a misleading sold-out), and a
// closed category answers 409 tier.closed.
func TestGA45_RESTReservation_RequiresCategoryAndHonoursTheGate(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()

	f := newGAQuotaFixture(t, ctx, pool, 5)
	defer f.cleanup()
	srv := buildIntegrationResetServer(t, pool)

	post := func(body map[string]any) *httptest.ResponseRecorder {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		req := ga45AdminRequest(http.MethodPost, raw, nil)
		w := httptest.NewRecorder()
		srv.checkoutHandler().HandleCreateReservation(w, req)
		return w
	}

	base := map[string]any{
		"session_id": f.sessionID.String(),
		"channel_id": f.channelID.String(),
		"org_id":     f.orgID.String(),
		"quantity":   1,
	}

	t.Run("no category is a 400, not a sold-out", func(t *testing.T) {
		w := post(base)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 (body %s)", w.Code, w.Body.String())
		}
		if code := ga45ErrorCode(t, w); code != "reservation.tier_required" {
			t.Errorf("error code = %q, want reservation.tier_required", code)
		}
		if held := f.countPlaces(ctx, "held"); held != 0 {
			t.Errorf("held places = %d, want 0", held)
		}
	})

	withTier := map[string]any{}
	for k, v := range base {
		withTier[k] = v
	}
	withTier["tier_id"] = f.tierID.String()

	t.Run("with a category the hold takes one of its places", func(t *testing.T) {
		w := post(withTier)
		if w.Code != http.StatusCreated {
			t.Fatalf("status = %d, want 201 (body %s)", w.Code, w.Body.String())
		}
		if held := f.countPlaces(ctx, "held"); held != 1 {
			t.Errorf("held places = %d, want 1", held)
		}
	})

	t.Run("a closed category is a 409 tier.closed", func(t *testing.T) {
		f.close()
		w := post(withTier)
		if w.Code != http.StatusConflict {
			t.Fatalf("status = %d, want 409 (body %s)", w.Code, w.Body.String())
		}
		if code := ga45ErrorCode(t, w); code != "tier.closed" {
			t.Errorf("error code = %q, want tier.closed", code)
		}
		if held := f.countPlaces(ctx, "held"); held != 1 {
			t.Errorf("held places = %d, want 1 — the refused hold must not take a place", held)
		}
	})
}

func ga45ErrorCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode error envelope: %v (body %s)", err, w.Body.String())
	}
	return env.Error.Code
}

// ─────────────────────────────────────────────────────────────────────────────
// Decision 6 — free tickets
// ─────────────────────────────────────────────────────────────────────────────

// TestGA45_ComplimentaryConsumesCategoryPlaces is decision 6 for free
// tickets: an issuance on a GA session must name a category, takes that many
// of ITS places, stamps each ticket with the place it consumed, and gives
// the places back when the batch is revoked.
func TestGA45_ComplimentaryConsumesCategoryPlaces(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()

	f := newGAQuotaFixture(t, ctx, pool, 4)
	defer f.cleanup()
	srv := buildIntegrationResetServer(t, pool)

	issue := func(body map[string]any) *httptest.ResponseRecorder {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		req := ga45AdminRequest(http.MethodPost, raw, map[string]string{"org_id": f.orgID.String()})
		w := httptest.NewRecorder()
		srv.ticketsHandler().HandleCreateComplimentaryIssuance(w, req)
		return w
	}

	t.Run("a GA issuance without a category is refused", func(t *testing.T) {
		w := issue(map[string]any{
			"session_id": f.sessionID.String(),
			"qty":        1,
			"batch_id":   "ga45-no-tier-" + uuid.NewString()[:8],
		})
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 (body %s)", w.Code, w.Body.String())
		}
		if code := ga45ErrorCode(t, w); code != "tier.required" {
			t.Errorf("error code = %q, want tier.required", code)
		}
	})

	var issuanceID uuid.UUID
	t.Run("an issuance takes the category's places", func(t *testing.T) {
		w := issue(map[string]any{
			"session_id": f.sessionID.String(),
			"tier_id":    f.tierID.String(),
			"qty":        2,
			"batch_id":   "ga45-issue-" + uuid.NewString()[:8],
		})
		if w.Code != http.StatusCreated {
			t.Fatalf("status = %d, want 201 (body %s)", w.Code, w.Body.String())
		}
		var env struct {
			Issuance struct {
				ID uuid.UUID `json:"id"`
			} `json:"issuance"`
			Tickets []struct {
				SeatKey *string `json:"seat_key"`
			} `json:"tickets"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
			t.Fatalf("decode issuance: %v (body %s)", err, w.Body.String())
		}
		issuanceID = env.Issuance.ID

		if sold := f.countPlaces(ctx, "sold"); sold != 2 {
			t.Errorf("sold places = %d, want 2", sold)
		}
		if free := f.countPlaces(ctx, "available"); free != 2 {
			t.Errorf("available places = %d, want 2", free)
		}
		// Every free ticket carries the concrete place it consumed, exactly
		// as a sold one does — that stamp is what cancellation releases.
		var stamped int64
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM tickets
			 WHERE complimentary_issuance_id=$1 AND seat_key LIKE 'ga|t1|%'`,
			issuanceID).Scan(&stamped); err != nil {
			t.Fatalf("count stamped tickets: %v", err)
		}
		if stamped != 2 {
			t.Errorf("tickets carrying a place key = %d, want 2", stamped)
		}
	})

	t.Run("a short category refuses the issuance", func(t *testing.T) {
		w := issue(map[string]any{
			"session_id": f.sessionID.String(),
			"tier_id":    f.tierID.String(),
			"qty":        3, // only 2 places left
			"batch_id":   "ga45-short-" + uuid.NewString()[:8],
		})
		if w.Code != http.StatusConflict {
			t.Fatalf("status = %d, want 409 (body %s)", w.Code, w.Body.String())
		}
		if code := ga45ErrorCode(t, w); code != "tier.sold_out" && code != "complimentary.capacity_overflow" {
			t.Errorf("error code = %q, want tier.sold_out", code)
		}
		if sold := f.countPlaces(ctx, "sold"); sold != 2 {
			t.Errorf("sold places = %d, want 2 — the refused issuance must take nothing", sold)
		}
	})

	t.Run("revoking gives the places back to the category", func(t *testing.T) {
		req := ga45AdminRequest(http.MethodPost, nil, map[string]string{"id": issuanceID.String()})
		w := httptest.NewRecorder()
		srv.ticketsHandler().HandleRevokeComplimentaryIssuance(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("revoke status = %d, want 200 (body %s)", w.Code, w.Body.String())
		}
		if sold := f.countPlaces(ctx, "sold"); sold != 0 {
			t.Errorf("sold places = %d, want 0", sold)
		}
		if free := f.countPlaces(ctx, "available"); free != 4 {
			t.Errorf("available places = %d, want 4 — every revoked place returns to its category", free)
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Decision 6 — external partner quotas
// ─────────────────────────────────────────────────────────────────────────────

// TestGA45_ExternalAllocationBlocksAndSettlesPlaces is decision 6 for
// partner quotas: activating one withholds that many of the category's
// places (status 'unavailable' — the admin hold), and reconciliation turns
// the consumed ones into sales and puts the rest back on sale.
func TestGA45_ExternalAllocationBlocksAndSettlesPlaces(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()

	f := newGAQuotaFixture(t, ctx, pool, 6)
	defer f.cleanup()
	srv := buildIntegrationResetServer(t, pool)

	// Create the allocation straight into 'active': the same code path
	// pending→active uses.
	raw, err := json.Marshal(map[string]any{
		"session_id": f.sessionID.String(),
		"tier_id":    f.tierID.String(),
		"quota_qty":  4,
		"status":     "active",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := ga45AdminRequest(http.MethodPost, raw, map[string]string{"org_id": f.orgID.String()})
	w := httptest.NewRecorder()
	srv.inventoryHandler().HandleCreateExternalAllocation(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create allocation status = %d, want 201 (body %s)", w.Code, w.Body.String())
	}
	var created struct {
		Allocation struct {
			ID uuid.UUID `json:"id"`
		} `json:"allocation"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode allocation: %v (body %s)", err, w.Body.String())
	}

	if blocked := f.countPlaces(ctx, "unavailable"); blocked != 4 {
		t.Fatalf("withheld places = %d, want 4 — the quota must take real places, not just ledger counters", blocked)
	}
	if free := f.countPlaces(ctx, "available"); free != 2 {
		t.Errorf("available places = %d, want 2", free)
	}

	// Reconcile: 3 of the 4 were sold by the partner.
	raw, err = json.Marshal(map[string]any{"status": "reconciled", "quota_consumed": 3})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req = ga45AdminRequest(http.MethodPatch, raw, map[string]string{
		"org_id": f.orgID.String(), "id": created.Allocation.ID.String(),
	})
	w = httptest.NewRecorder()
	srv.inventoryHandler().HandlePatchExternalAllocation(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("reconcile status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}

	if sold := f.countPlaces(ctx, "sold"); sold != 3 {
		t.Errorf("sold places = %d, want 3", sold)
	}
	if free := f.countPlaces(ctx, "available"); free != 3 {
		t.Errorf("available places = %d, want 3 (2 untouched + 1 returned)", free)
	}
	if blocked := f.countPlaces(ctx, "unavailable"); blocked != 0 {
		t.Errorf("withheld places = %d, want 0 after reconciliation", blocked)
	}

	// The ledger and the rows must agree, as everywhere else.
	var held, ledgerSold int32
	if err := pool.QueryRow(ctx,
		`SELECT capacity_held, capacity_sold FROM inventory_ledger
		 WHERE session_id=$1 AND tier_id IS NULL`, f.sessionID).Scan(&held, &ledgerSold); err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	if held != 0 || ledgerSold != 3 {
		t.Errorf("ledger held=%d sold=%d, want 0/3", held, ledgerSold)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 5 — the widget feed
// ─────────────────────────────────────────────────────────────────────────────

// TestGA45_PublicFeedReportsRemainingPlaces pins step 5's widget contract:
// each category carries `available` (its free places, 0 when it is closed or
// off-sale) and `is_open`, so the picker caps at what is really left rather
// than at the declared capacity.
func TestGA45_PublicFeedReportsRemainingPlaces(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()

	f := newGAQuotaFixture(t, ctx, pool, 8)
	defer f.cleanup()
	srv := buildIntegrationResetServer(t, pool)

	// Sell two of the category's places so available != capacity.
	if _, err := pool.Exec(ctx,
		`UPDATE session_seats SET status='sold'
		  WHERE id IN (SELECT id FROM session_seats
		               WHERE session_id=$1 AND tier_id=$2 AND status='available'
		               ORDER BY seat_key LIMIT 2)`,
		f.sessionID, f.tierID); err != nil {
		t.Fatalf("sell two places: %v", err)
	}

	read := func() (available *int, isOpen bool, capacity *int32) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("feed_token", f.feedToken)
		rctx.URLParams.Add("event_id", f.eventID.String())
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
		w := httptest.NewRecorder()
		srv.feedHandler().HandlePublicFeedEvent(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("public feed status = %d, want 200 (body %s)", w.Code, w.Body.String())
		}
		var env struct {
			Event struct {
				Sessions []struct {
					Tiers []struct {
						ID        string `json:"id"`
						Available *int   `json:"available"`
						IsOpen    bool   `json:"is_open"`
						Capacity  *int32 `json:"capacity"`
					} `json:"tiers"`
				} `json:"sessions"`
			} `json:"event"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
			t.Fatalf("decode feed: %v (body %s)", err, w.Body.String())
		}
		for _, s := range env.Event.Sessions {
			for _, tr := range s.Tiers {
				if tr.ID == f.tierID.String() {
					return tr.Available, tr.IsOpen, tr.Capacity
				}
			}
		}
		t.Fatalf("category %s not found in the feed (body %s)", f.tierID, w.Body.String())
		return nil, false, nil
	}

	available, isOpen, capacity := read()
	if available == nil || *available != 6 {
		t.Errorf("available = %v, want 6 (8 places minus 2 sold)", available)
	}
	if !isOpen {
		t.Error("is_open = false for an open category")
	}
	if capacity == nil || *capacity != 8 {
		t.Errorf("capacity = %v, want 8 — the declared quantity stays in the payload", capacity)
	}

	f.close()
	available, isOpen, _ = read()
	if available == nil || *available != 0 {
		t.Errorf("available = %v for a closed category, want 0", available)
	}
	if isOpen {
		t.Error("is_open = true for a closed category")
	}

	// A sale window that has not opened reads the same way (decision 5).
	if _, err := pool.Exec(ctx,
		`UPDATE ticket_tiers SET is_open=true, sale_window_start=$2 WHERE id=$1`,
		f.tierID, time.Now().UTC().Add(24*time.Hour)); err != nil {
		t.Fatalf("set a future sale window: %v", err)
	}
	available, isOpen, _ = read()
	if available == nil || *available != 0 {
		t.Errorf("available = %v before the sale window opens, want 0", available)
	}
	if !isOpen {
		t.Error("is_open = false; the window is a separate signal from the open flag")
	}
}
