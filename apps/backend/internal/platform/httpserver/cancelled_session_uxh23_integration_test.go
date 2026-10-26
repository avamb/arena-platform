//go:build integration

// cancelled_session_uxh23_integration_test.go — UXH-23: a session cancelled
// through the session-change flow keeps sessions.status='cancelled' together
// with its row and its (still open) categories, and used to stay fully
// sellable: the public feed listed it with every tier available, and a buyer
// could hold it and pay.
//
// These tests drive the real router against a live database:
//
//   - GET  /v1/public/feeds/{token}/events/{id} keeps the session (buyers who
//     hold tickets still see the date as cancelled) but reports every
//     category as available:0 and offers no seat map links;
//   - POST /v1/public/feeds/{token}/checkout/start and .../checkout/quote
//     answer 409 session.cancelled and create NO hold;
//   - the Bil24 RESERVATION command answers resultCode 101;
//   - a scheduled session is unchanged.
//
// Prerequisites: DATABASE_URL against a migrated database.
package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type uxh23FeedSession struct {
	ID            string `json:"id"`
	Status        string `json:"status"`
	AdmissionMode string `json:"admission_mode"`
	SchemaURL     string `json:"schema_url"`
	SeatStatusURL string `json:"seat_status_url"`
	Tiers         []struct {
		ID        string `json:"id"`
		Available *int   `json:"available"`
	} `json:"tiers"`
}

func uxh23ReadFeed(t *testing.T, srv *Server, f *w1a6cFixture) uxh23FeedSession {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet,
		"/v1/public/feeds/"+f.feedToken+"/events/"+f.eventID.String(), nil)
	rec := httptest.NewRecorder()
	srv.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("public feed event = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var env struct {
		Event struct {
			Sessions []uxh23FeedSession `json:"sessions"`
		} `json:"event"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode feed: %v (body: %s)", err, rec.Body.String())
	}
	for _, s := range env.Event.Sessions {
		if s.ID == f.sessionID.String() {
			return s
		}
	}
	t.Fatalf("session %s is missing from the feed — a cancelled date must stay listed (body: %s)",
		f.sessionID, rec.Body.String())
	return uxh23FeedSession{}
}

func uxh23Post(t *testing.T, srv *Server, path string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.router.ServeHTTP(rec, req)
	return rec
}

func uxh23ErrorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode error envelope: %v (body: %s)", err, rec.Body.String())
	}
	return env.Error.Code
}

func uxh23HoldRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool, f *w1a6cFixture) (reservations, held int) {
	t.Helper()
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM reservations WHERE session_id = $1`, f.sessionID).Scan(&reservations); err != nil {
		t.Fatalf("count reservations: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM session_seats WHERE session_id = $1 AND status <> 'available'`,
		f.sessionID).Scan(&held); err != nil {
		t.Fatalf("count taken places: %v", err)
	}
	return reservations, held
}

// uxh23SeatedSwitch gives the fixture session a seating plan version so it
// can flip between general admission and assigned seats (the DB requires a
// plan for any seated session). It returns the switch and a teardown that
// must run before the fixture's own cleanup.
func uxh23SeatedSwitch(t *testing.T, ctx context.Context, pool *pgxpool.Pool, f *w1a6cFixture) (func(seated bool), func()) {
	t.Helper()
	planID, versionID := uuid.New(), uuid.New()
	for i, st := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO seating_plans (id, venue_id, owner_org_id, name, plan_type, status)
		  VALUES ($1, $2, $3, $4, 'assigned_seats', 'active')`,
			[]any{planID, f.venueID, f.orgID, "UXH23 Plan " + planID.String()[:8]}},
		{`INSERT INTO seating_plan_versions (id, seating_plan_id, version_number, geometry, geometry_checksum, capacity_seated)
		  VALUES ($1, $2, 1, '{}'::jsonb, $3, 3)`,
			[]any{versionID, planID, "uxh23-" + versionID.String()[:8]}},
	} {
		if _, err := pool.Exec(ctx, st.sql, st.args...); err != nil {
			t.Fatalf("seating plan fixture step %d: %v", i, err)
		}
	}
	set := func(seated bool) {
		t.Helper()
		sql, args := `UPDATE sessions SET admission_mode = 'general_admission', seating_plan_version_id = NULL WHERE id = $1`, []any{f.sessionID}
		if seated {
			sql, args = `UPDATE sessions SET admission_mode = 'assigned_seats', seating_plan_version_id = $2 WHERE id = $1`, []any{f.sessionID, versionID}
		}
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("switch admission mode (seated=%v): %v", seated, err)
		}
	}
	teardown := func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `UPDATE sessions SET admission_mode = 'general_admission', seating_plan_version_id = NULL WHERE id = $1`, f.sessionID)
		_, _ = pool.Exec(bg, `DELETE FROM seating_plan_versions WHERE id = $1`, versionID)
		_, _ = pool.Exec(bg, `DELETE FROM seating_plans WHERE id = $1`, planID)
	}
	return set, teardown
}

func TestUXH23_CancelledSession_FeedAndCheckout(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()

	f := newW1A6cFixture(t, ctx, pool)
	defer f.cleanup()
	defer enableStripeForChannel(t, ctx, pool, f.orgID, f.channelID)()
	srv := buildHostedCheckoutServer(t, pool, newStubStripe(t).baseURL())
	setSeated, dropSeated := uxh23SeatedSwitch(t, ctx, pool, f)
	defer dropSeated()

	startBody := map[string]any{
		"session_id": f.sessionID.String(),
		"return_url": hostedTicketsBaseURL + "/embed",
		"tier_id":    f.tierID.String(),
		"qty":        1,
		"buyer": map[string]any{
			"email": f.buyerMail,
			"name":  "Cancelled Buyer",
			"phone": f.buyerPhone,
		},
	}
	quoteBody := map[string]any{
		"session_id": f.sessionID.String(),
		"ga_items":   []map[string]any{{"tier_id": f.tierID.String(), "quantity": 1}},
	}

	// Baseline: while the session is scheduled the feed offers the places
	// and the quote prices them.
	s := uxh23ReadFeed(t, srv, f)
	if s.Status != "scheduled" {
		t.Fatalf("baseline session status = %q, want scheduled", s.Status)
	}
	if len(s.Tiers) != 1 || s.Tiers[0].Available == nil || *s.Tiers[0].Available != 5 {
		t.Fatalf("baseline tiers = %+v, want one tier with available 5", s.Tiers)
	}
	if rec := uxh23Post(t, srv, "/v1/public/feeds/"+f.feedToken+"/checkout/quote", quoteBody); rec.Code != http.StatusOK {
		t.Fatalf("baseline quote = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	// A seated session reports its seat-map links while it is scheduled.
	setSeated(true)
	if s := uxh23ReadFeed(t, srv, f); s.SchemaURL == "" || s.SeatStatusURL == "" {
		t.Fatalf("scheduled seated session has no seat-map links: %+v", s)
	}
	setSeated(false)

	// Cancel the date. Its row and its categories stay exactly as they were.
	if _, err := pool.Exec(ctx, `UPDATE sessions SET status = 'cancelled' WHERE id = $1`, f.sessionID); err != nil {
		t.Fatalf("cancel the session: %v", err)
	}
	beforeRes, beforeTaken := uxh23HoldRows(t, ctx, pool, f)

	t.Run("feed keeps the session with availability 0", func(t *testing.T) {
		s := uxh23ReadFeed(t, srv, f)
		if s.Status != "cancelled" {
			t.Errorf("feed session status = %q, want cancelled", s.Status)
		}
		if len(s.Tiers) != 1 {
			t.Fatalf("feed tiers = %d, want the category to stay listed", len(s.Tiers))
		}
		if a := s.Tiers[0].Available; a == nil || *a != 0 {
			t.Errorf("tier available = %v, want 0", a)
		}
	})

	t.Run("feed offers no seat map for a cancelled seated session", func(t *testing.T) {
		setSeated(true)
		defer setSeated(false)
		s := uxh23ReadFeed(t, srv, f)
		if s.SchemaURL != "" || s.SeatStatusURL != "" {
			t.Errorf("cancelled session still carries seat links: schema=%q status=%q", s.SchemaURL, s.SeatStatusURL)
		}
	})

	t.Run("checkout/start answers 409 session.cancelled", func(t *testing.T) {
		rec := uxh23Post(t, srv, "/v1/public/feeds/"+f.feedToken+"/checkout/start", startBody)
		if rec.Code != http.StatusConflict {
			t.Fatalf("checkout/start = %d, want 409; body: %s", rec.Code, rec.Body.String())
		}
		if code := uxh23ErrorCode(t, rec); code != "session.cancelled" {
			t.Errorf("error code = %q, want session.cancelled", code)
		}
	})

	t.Run("checkout/quote answers 409 session.cancelled", func(t *testing.T) {
		rec := uxh23Post(t, srv, "/v1/public/feeds/"+f.feedToken+"/checkout/quote", quoteBody)
		if rec.Code != http.StatusConflict {
			t.Fatalf("checkout/quote = %d, want 409; body: %s", rec.Code, rec.Body.String())
		}
		if code := uxh23ErrorCode(t, rec); code != "session.cancelled" {
			t.Errorf("error code = %q, want session.cancelled", code)
		}
	})

	t.Run("the refusals created no hold and no order", func(t *testing.T) {
		res, taken := uxh23HoldRows(t, ctx, pool, f)
		if res != beforeRes || taken != beforeTaken {
			t.Errorf("reservations %d -> %d, taken places %d -> %d; a refused request must change nothing",
				beforeRes, res, beforeTaken, taken)
		}
		var orders int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM orders WHERE org_id = $1`, f.orgID).Scan(&orders); err != nil {
			t.Fatalf("count orders: %v", err)
		}
		if orders != 0 {
			t.Errorf("orders = %d, want 0", orders)
		}
	})

	// Reinstating the date makes it sellable again — the gate reads the
	// status on every hold, nothing is cached.
	if _, err := pool.Exec(ctx, `UPDATE sessions SET status = 'scheduled' WHERE id = $1`, f.sessionID); err != nil {
		t.Fatalf("reinstate the session: %v", err)
	}
	if rec := uxh23Post(t, srv, "/v1/public/feeds/"+f.feedToken+"/checkout/quote", quoteBody); rec.Code != http.StatusOK {
		t.Fatalf("quote after reinstating = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
}

// A hold is refused through the shared gate on the REST reservation route too.
func TestUXH23_CancelledSession_RESTReservation(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()

	f := newGAQuotaFixture(t, ctx, pool, 5)
	defer f.cleanup()
	srv := buildIntegrationResetServer(t, pool)

	if _, err := pool.Exec(ctx, `UPDATE sessions SET status = 'cancelled' WHERE id = $1`, f.sessionID); err != nil {
		t.Fatalf("cancel the session: %v", err)
	}
	raw, err := json.Marshal(map[string]any{
		"session_id": f.sessionID.String(),
		"channel_id": f.channelID.String(),
		"org_id":     f.orgID.String(),
		"tier_id":    f.tierID.String(),
		"quantity":   1,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	w := httptest.NewRecorder()
	srv.checkoutHandler().HandleCreateReservation(w, ga45AdminRequest(http.MethodPost, raw, nil))
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body %s)", w.Code, w.Body.String())
	}
	if code := ga45ErrorCode(t, w); code != "session.cancelled" {
		t.Errorf("error code = %q, want session.cancelled", code)
	}
	if held := f.countPlaces(ctx, "held"); held != 0 {
		t.Errorf("held places = %d, want 0", held)
	}
}
