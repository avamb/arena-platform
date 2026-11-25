//go:build integration

// holds_public_integration_test.go — the public seat hold at the click (spec
// 08_architecture/67, sections 4.1-4.3, 4.6, 4.7, 4.9, 6 and the checks of
// section 12 that concern these routes), through the real router on a real
// database:
//
//   - a click makes a hold without an order; a second seat goes into the same
//     hold; a taken seat is 409; a partly taken request is 200 with the key
//     and the conflicts;
//   - a click without a key from the same device joins the open hold; the
//     same Idempotency-Key replays the answer; twenty parallel creates of one
//     device make at most two holds;
//   - adding or removing a seat on the payment step is 409 hold.in_payment;
//   - a removed seat is free at once and seat_status_version grows;
//   - an extension before the last 75 s, a second one and one after the
//     closing mark are refused; on the payment step the order gets the same
//     expiry;
//   - limits: the 11th seat, the third hold of a device, the pause after an
//     unpaid hold, "hold again" once, "remove all and select again";
//   - another key changes nothing (404 without a leak);
//   - two buyers clicking the same seats in opposite order: no 40P01.
package httpserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
)

const phVisitorKey = "integration-hold-visitor-key-0123456789abcdef"

// phFixture is one published event with N seated sessions of M seats
// (keys A-01 .. A-MM) on one feed token.
type phFixture struct {
	t         *testing.T
	pool      *pgxpool.Pool
	orgID     uuid.UUID
	venueID   uuid.UUID
	eventID   uuid.UUID
	channelID uuid.UUID
	tokenID   uuid.UUID
	planID    uuid.UUID
	versionID uuid.UUID
	sessions  []uuid.UUID
	tiers     []uuid.UUID
	feedToken string
}

func newPHFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool, nSessions, nSeats int) *phFixture {
	t.Helper()
	f := &phFixture{
		t: t, pool: pool,
		orgID: uuid.New(), venueID: uuid.New(), eventID: uuid.New(), channelID: uuid.New(),
		tokenID: uuid.New(), planID: uuid.New(), versionID: uuid.New(),
	}
	suffix := f.orgID.String()[:8]
	f.feedToken = "hold-feed-" + suffix
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("hold fixture (%.70s): %v", sql, err)
		}
	}
	exec(`INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`, f.orgID, "Hold Org "+suffix, "hold-"+suffix)
	exec(`INSERT INTO venues (id, org_id, name, timezone) VALUES ($1, $2, $3, 'Europe/Prague')`, f.venueID, f.orgID, "Hold Venue "+suffix)
	exec(`INSERT INTO events (id, org_id, name, status, visibility) VALUES ($1, $2, $3, 'published', 'public')`, f.eventID, f.orgID, "Hold Event "+suffix)
	exec(`INSERT INTO sales_channels (id, org_id, name) VALUES ($1, $2, $3)`, f.channelID, f.orgID, "Hold Channel "+suffix)
	exec(`INSERT INTO agent_feed_tokens (id, token, sales_channel_id, label, is_active) VALUES ($1, $2, $3, 'hold', true)`, f.tokenID, f.feedToken, f.channelID)
	exec(`INSERT INTO event_publications (event_id, feed_token_id) VALUES ($1, $2)`, f.eventID, f.tokenID)
	exec(`INSERT INTO seating_plans (id, venue_id, owner_org_id, name, plan_type, status) VALUES ($1, $2, $3, $4, 'assigned_seats', 'active')`,
		f.planID, f.venueID, f.orgID, "Hold Plan "+suffix)
	exec(`INSERT INTO seating_plan_versions (id, seating_plan_id, version_number, geometry, geometry_checksum, capacity_seated)
	      VALUES ($1, $2, 1, '{}'::jsonb, $3, $4)`, f.versionID, f.planID, "hold-"+suffix, nSeats)
	for i := 0; i < nSessions; i++ {
		sid, tid := uuid.New(), uuid.New()
		f.sessions = append(f.sessions, sid)
		f.tiers = append(f.tiers, tid)
		exec(`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, status,
		          admission_mode, currency, currency_source, seating_plan_version_id)
		      VALUES ($1, $2, $3, now() + interval '30 days', now() + interval '30 days 2 hours', $4,
		          'scheduled', 'assigned_seats', 'EUR', 'override', $5)`, sid, f.eventID, f.venueID, nSeats, f.versionID)
		exec(`INSERT INTO ticket_tiers (id, session_id, name, pricing_mode, price_amount, currency, sort_order)
		      VALUES ($1, $2, 'Hold Tier', 'fixed', 2500, 'EUR', 0)`, tid, sid)
		exec(`INSERT INTO inventory_ledger (session_id, tier_id, capacity_total) VALUES ($1, NULL, $2)`, sid, nSeats)
		for s := 1; s <= nSeats; s++ {
			exec(`INSERT INTO session_seats (session_id, seat_key, sector_name, row_name, seat_number, tier_id, status)
			      VALUES ($1, $2, 'Hall', 'A', $3, $4, 'available')`, sid, phSeat(s), fmt.Sprint(s), tid)
		}
	}
	t.Cleanup(f.cleanup)
	return f
}

func phSeat(n int) string { return fmt.Sprintf("A-%02d", n) }

func (f *phFixture) cleanup() {
	ctx := context.Background()
	ids := f.sessions
	for _, s := range []struct {
		sql string
		arg any
	}{
		{`DELETE FROM orders WHERE org_id = $1`, f.orgID},
		{`DELETE FROM checkout_sessions WHERE org_id = $1`, f.orgID},
		{`UPDATE session_seats SET reservation_id = NULL, status = 'available' WHERE session_id = ANY($1::uuid[])`, ids},
		{`DELETE FROM reservation_seats WHERE reservation_id IN (SELECT id FROM reservations WHERE session_id = ANY($1::uuid[]))`, ids},
		{`UPDATE reservations SET rehold_of = NULL WHERE session_id = ANY($1::uuid[])`, ids},
		{`DELETE FROM reservations WHERE session_id = ANY($1::uuid[])`, ids},
		{`DELETE FROM session_seats WHERE session_id = ANY($1::uuid[])`, ids},
		{`DELETE FROM inventory_ledger WHERE session_id = ANY($1::uuid[])`, ids},
		{`DELETE FROM ticket_tiers WHERE session_id = ANY($1::uuid[])`, ids},
		{`DELETE FROM idempotency_keys WHERE scope = $1`, "POST holds:" + f.feedToken},
		{`DELETE FROM event_publications WHERE event_id = $1`, f.eventID},
		{`DELETE FROM agent_feed_tokens WHERE id = $1`, f.tokenID},
		{`DELETE FROM sessions WHERE id = ANY($1::uuid[])`, ids},
		{`DELETE FROM seating_plan_versions WHERE id = $1`, f.versionID},
		{`DELETE FROM seating_plans WHERE id = $1`, f.planID},
		{`DELETE FROM sales_channels WHERE id = $1`, f.channelID},
		{`DELETE FROM events WHERE id = $1`, f.eventID},
		{`DELETE FROM venues WHERE id = $1`, f.venueID},
		{`DELETE FROM organizations WHERE id = $1`, f.orgID},
	} {
		if _, err := f.pool.Exec(ctx, s.sql, s.arg); err != nil {
			f.t.Logf("hold fixture cleanup (%.60s): %v", s.sql, err)
		}
	}
}

func buildPHServer(t *testing.T, pool *pgxpool.Pool, tweak ...func(*config.Config)) *Server {
	t.Helper()
	all := append([]func(*config.Config){func(c *config.Config) {
		c.HoldOnClickEnabled = true
		c.HoldVisitorKey = phVisitorKey
		c.PublicFeedTokenRateLimit = 100000
		c.PublicCheckoutTokenRateLimit = 100000
		c.PublicAPIIPRateLimit = 100000
	}}, tweak...)
	return buildHostedCheckoutServer(t, pool, "", all...)
}

type phAnswer struct {
	HoldToken       string              `json:"hold_token"`
	SessionID       string              `json:"session_id"`
	State           string              `json:"state"`
	Step            string              `json:"step"`
	SeatKeys        []string            `json:"seat_keys"`
	ExpiresAt       time.Time           `json:"expires_at"`
	ExtendAvailable bool                `json:"extend_available"`
	ExtensionsLeft  int                 `json:"extensions_left"`
	Conflicts       []map[string]string `json:"conflicts"`
	CheckoutToken   *string             `json:"checkout_token"`
	Policy          struct {
		SelectSeconds  int `json:"select_seconds"`
		PaymentSeconds int `json:"payment_seconds"`
		ExtendSeconds  int `json:"extend_seconds"`
	} `json:"policy"`
	Error *struct {
		Code    string         `json:"code"`
		Details map[string]any `json:"details"`
	} `json:"error"`
}

func (a phAnswer) code() string {
	if a.Error == nil {
		return ""
	}
	return a.Error.Code
}

func phCall(t *testing.T, srv *Server, method, path string, body any, idemKey string) (int, phAnswer, []byte) {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rd)
	req.Header.Set("Content-Type", "application/json")
	if idemKey != "" {
		req.Header.Set("Idempotency-Key", idemKey)
	}
	rec := httptest.NewRecorder()
	srv.router.ServeHTTP(rec, req)
	var out phAnswer
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out, rec.Body.Bytes()
}

func phPost(t *testing.T, srv *Server, f *phFixture, body map[string]any, idemKey string) (int, phAnswer) {
	t.Helper()
	code, out, _ := phCall(t, srv, http.MethodPost, "/v1/public/feeds/"+f.feedToken+"/holds", body, idemKey)
	return code, out
}

func phClick(session uuid.UUID, device string, seats ...string) map[string]any {
	return map[string]any{"session_id": session.String(), "device_id": device, "seat_keys": seats}
}

func phHoldID(t *testing.T, ctx context.Context, pool *pgxpool.Pool, token string) uuid.UUID {
	t.Helper()
	sum := sha256.Sum256([]byte(token))
	var id uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM reservations WHERE hold_token_hash = $1`, sum[:]).Scan(&id); err != nil {
		t.Fatalf("hold of token: %v", err)
	}
	return id
}

func phSeatStatus(t *testing.T, ctx context.Context, pool *pgxpool.Pool, session uuid.UUID, key string) string {
	t.Helper()
	var st string
	if err := pool.QueryRow(ctx, `SELECT status FROM session_seats WHERE session_id = $1 AND seat_key = $2`, session, key).Scan(&st); err != nil {
		t.Fatalf("seat status: %v", err)
	}
	return st
}

func phSeatVersion(t *testing.T, ctx context.Context, pool *pgxpool.Pool, session uuid.UUID) int64 {
	t.Helper()
	var v int64
	if err := pool.QueryRow(ctx, `SELECT seat_status_version FROM sessions WHERE id = $1`, session).Scan(&v); err != nil {
		t.Fatalf("seat version: %v", err)
	}
	return v
}

func phOpenHolds(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sessions []uuid.UUID) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM reservations WHERE session_id = ANY($1::uuid[])
	      AND hold_token_hash IS NOT NULL AND state IN ('draft','active')`, sessions).Scan(&n); err != nil {
		t.Fatalf("count holds: %v", err)
	}
	return n
}

// phExpire ends a hold the way reservation.expire_sweep does.
func phExpire(t *testing.T, ctx context.Context, pool *pgxpool.Pool, token string) {
	t.Helper()
	id := phHoldID(t, ctx, pool, token)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := hcheckout.ExpireHoldForOrderTx(ctx, gen.New(pool).WithTx(tx), id); err != nil {
		t.Fatalf("expire hold: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit expire: %v", err)
	}
}

// phInPayment gives the hold an order waiting for payment, as checkout/start
// with the hold token would.
func phInPayment(t *testing.T, ctx context.Context, f *phFixture, session uuid.UUID, token string) (orderID uuid.UUID, checkoutToken string) {
	t.Helper()
	res := phHoldID(t, ctx, f.pool, token)
	cs := uuid.New()
	orderID = uuid.New()
	checkoutToken = "cs-tok-" + cs.String()[:12]
	if _, err := f.pool.Exec(ctx, `INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state, checkout_token)
	      VALUES ($1, $2, $3, $4, 'payment_started', $5)`, cs, f.orgID, f.channelID, res, checkoutToken); err != nil {
		t.Fatalf("checkout session: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO orders (id, org_id, channel_id, event_id, session_id, checkout_session_id, reservation_id,
	          source, status, currency, subtotal, discount, charge, total, expires_at)
	      VALUES ($1, $2, $3, $4, $5, $6, $7, 'public_feed', 'pending_payment', 'EUR', 2500, 0, 0, 2500, now() + interval '15 minutes')`,
		orderID, f.orgID, f.channelID, f.eventID, session, cs, res); err != nil {
		t.Fatalf("order: %v", err)
	}
	return orderID, checkoutToken
}

func phNear(a, b time.Time, slack time.Duration) bool {
	d := a.Sub(b)
	return d < slack && d > -slack
}

func TestHoldsPublic_DisabledAnswers404(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	f := newPHFixture(t, ctx, pool, 1, 3)
	srv := buildHostedCheckoutServer(t, pool, "")
	code, out := phPost(t, srv, f, phClick(f.sessions[0], "dev-off", phSeat(1)), "")
	if code != http.StatusNotFound || out.code() != "hold.disabled" {
		t.Fatalf("disabled POST = %d %q, want 404 hold.disabled", code, out.code())
	}
	for _, m := range []struct{ method, path string }{
		{http.MethodGet, "/v1/public/holds/abc"},
		{http.MethodDelete, "/v1/public/holds/abc"},
		{http.MethodDelete, "/v1/public/holds/abc/seats/A-01"},
		{http.MethodPost, "/v1/public/holds/abc/extend"},
	} {
		code, out, _ := phCall(t, srv, m.method, m.path, nil, "")
		if code != http.StatusNotFound || out.code() != "hold.disabled" {
			t.Errorf("%s %s = %d %q, want 404 hold.disabled", m.method, m.path, code, out.code())
		}
	}
	if phSeatStatus(t, ctx, pool, f.sessions[0], phSeat(1)) != "available" {
		t.Error("a disabled route held a seat")
	}
}

func TestHoldsPublic_ClickAddConflictJoinIdempotencyRelease(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	f := newPHFixture(t, ctx, pool, 2, 12)
	srv := buildPHServer(t, pool)
	s := f.sessions[0]

	// First click: a hold without an order.
	before := time.Now()
	code, h1 := phPost(t, srv, f, phClick(s, "dev-one", phSeat(1)), "")
	if code != http.StatusCreated || h1.HoldToken == "" || h1.Step != "select" || len(h1.SeatKeys) != 1 {
		t.Fatalf("first click = %d %+v", code, h1)
	}
	if !phNear(h1.ExpiresAt, before.Add(600*time.Second), 5*time.Second) || h1.Policy.SelectSeconds != 600 {
		t.Errorf("select deadline = %v policy %+v, want now+600s", h1.ExpiresAt, h1.Policy)
	}
	resID := phHoldID(t, ctx, pool, h1.HoldToken)
	var orders, checkouts int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM orders WHERE reservation_id = $1`, resID).Scan(&orders)
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM checkout_sessions WHERE reservation_id = $1`, resID).Scan(&checkouts)
	if orders != 0 || checkouts != 0 {
		t.Errorf("a click made %d orders and %d checkouts, want none", orders, checkouts)
	}
	if st := phSeatStatus(t, ctx, pool, s, phSeat(1)); st != "held" {
		t.Errorf("clicked seat = %q, want held", st)
	}
	var stored []byte
	_ = pool.QueryRow(ctx, `SELECT hold_token_hash FROM reservations WHERE id = $1`, resID).Scan(&stored)
	if sum := sha256.Sum256([]byte(h1.HoldToken)); !bytes.Equal(stored, sum[:]) {
		t.Error("the stored hash is not SHA-256 of the hold token")
	}

	// Second seat with the key: the same hold, the same deadline.
	code, h1b := phPost(t, srv, f, map[string]any{"session_id": s.String(), "device_id": "dev-one",
		"seat_keys": []string{phSeat(2)}, "hold_token": h1.HoldToken}, "")
	if code != http.StatusOK || h1b.HoldToken != h1.HoldToken || len(h1b.SeatKeys) != 2 || !h1b.ExpiresAt.Equal(h1.ExpiresAt) {
		t.Fatalf("second seat = %d %+v (first expires %v)", code, h1b, h1.ExpiresAt)
	}
	if n := phOpenHolds(t, ctx, pool, f.sessions); n != 1 {
		t.Fatalf("open holds = %d, want 1", n)
	}

	// A taken seat: 409, nothing held for the second buyer.
	code, taken := phPost(t, srv, f, phClick(s, "dev-two", phSeat(2)), "")
	if code != http.StatusConflict || taken.code() != "hold.seats_taken" {
		t.Fatalf("taken seat = %d %q, want 409 hold.seats_taken", code, taken.code())
	}
	if c, _ := taken.Error.Details["conflicts"].([]any); len(c) != 1 {
		t.Errorf("conflicts = %v, want one", taken.Error.Details)
	}
	if n := phOpenHolds(t, ctx, pool, f.sessions); n != 1 {
		t.Fatalf("open holds after a conflict = %d, want 1", n)
	}

	// Partly taken: 200/201 with the key and the conflicts.
	code, h2 := phPost(t, srv, f, phClick(s, "dev-two", phSeat(2), phSeat(3)), "")
	if code != http.StatusCreated || h2.HoldToken == "" || len(h2.SeatKeys) != 1 || h2.SeatKeys[0] != phSeat(3) ||
		len(h2.Conflicts) != 1 || h2.Conflicts[0]["seat_key"] != phSeat(2) || h2.Conflicts[0]["status"] != "held" {
		t.Fatalf("partial = %d %+v", code, h2)
	}

	// No key, same device: joins the open hold and answers its key.
	code, joined := phPost(t, srv, f, phClick(s, "dev-one", phSeat(4)), "")
	if code != http.StatusOK || joined.HoldToken != h1.HoldToken || len(joined.SeatKeys) != 3 {
		t.Fatalf("join = %d %+v, want hold %s with 3 seats", code, joined, h1.HoldToken)
	}

	// The same Idempotency-Key answers the stored answer.
	_, _, raw1 := phCall(t, srv, http.MethodPost, "/v1/public/feeds/"+f.feedToken+"/holds", phClick(s, "dev-one", phSeat(5)), "click-key-5")
	v1 := phSeatVersion(t, ctx, pool, s)
	code, _, raw2 := phCall(t, srv, http.MethodPost, "/v1/public/feeds/"+f.feedToken+"/holds", phClick(s, "dev-one", phSeat(5)), "click-key-5")
	// The stored answer is replayed from jsonb, so compare the JSON values.
	var ans1, ans2 map[string]any
	_ = json.Unmarshal(raw1, &ans1)
	_ = json.Unmarshal(raw2, &ans2)
	if code != http.StatusOK || ans1 == nil || !reflect.DeepEqual(ans1, ans2) {
		t.Fatalf("replay = %d\n%s\nwant\n%s", code, raw2, raw1)
	}
	replayBody, _ := json.Marshal(phClick(s, "dev-one", phSeat(5)))
	replayReq := httptest.NewRequest(http.MethodPost, "/v1/public/feeds/"+f.feedToken+"/holds", bytes.NewReader(replayBody))
	replayReq.Header.Set("Content-Type", "application/json")
	replayReq.Header.Set("Idempotency-Key", "click-key-5")
	replayRec := httptest.NewRecorder()
	srv.router.ServeHTTP(replayRec, replayReq)
	if replayRec.Header().Get("Idempotent-Replay") != "true" {
		t.Errorf("the same Idempotency-Key was not replayed (status %d)", replayRec.Code)
	}
	if v2 := phSeatVersion(t, ctx, pool, s); v2 != v1 {
		t.Errorf("a replay changed seat_status_version %d -> %d", v1, v2)
	}

	// GET answers the same hold.
	code, got, _ := phCall(t, srv, http.MethodGet, "/v1/public/holds/"+h1.HoldToken, nil, "")
	if code != http.StatusOK || got.State != "draft" || got.Step != "select" || len(got.SeatKeys) != 4 || got.CheckoutToken != nil {
		t.Fatalf("GET = %d %+v", code, got)
	}

	// Removing a seat frees it at once with a new version; a repeat is 200.
	v0 := phSeatVersion(t, ctx, pool, s)
	code, rel, _ := phCall(t, srv, http.MethodDelete, "/v1/public/holds/"+h1.HoldToken+"/seats/"+phSeat(5), nil, "")
	if code != http.StatusOK || len(rel.SeatKeys) != 3 {
		t.Fatalf("remove seat = %d %+v", code, rel)
	}
	if st := phSeatStatus(t, ctx, pool, s, phSeat(5)); st != "available" {
		t.Errorf("removed seat = %q, want available", st)
	}
	if v := phSeatVersion(t, ctx, pool, s); v <= v0 {
		t.Errorf("seat_status_version %d -> %d, want it to grow", v0, v)
	}
	code, rel2, _ := phCall(t, srv, http.MethodDelete, "/v1/public/holds/"+h1.HoldToken+"/seats/"+phSeat(5), nil, "")
	if code != http.StatusOK || len(rel2.SeatKeys) != 3 {
		t.Errorf("repeat remove = %d %+v", code, rel2)
	}

	// Removing the whole hold cancels it; a repeat is 200.
	code, gone, _ := phCall(t, srv, http.MethodDelete, "/v1/public/holds/"+h2.HoldToken, nil, "")
	if code != http.StatusOK || gone.State != "cancelled" || len(gone.SeatKeys) != 0 {
		t.Fatalf("remove hold = %d %+v", code, gone)
	}
	if st := phSeatStatus(t, ctx, pool, s, phSeat(3)); st != "available" {
		t.Errorf("seat of a removed hold = %q, want available", st)
	}
	if code, _, _ := phCall(t, srv, http.MethodDelete, "/v1/public/holds/"+h2.HoldToken, nil, ""); code != http.StatusOK {
		t.Errorf("repeat remove hold = %d, want 200", code)
	}

	// Another key changes nothing and leaks nothing.
	for _, m := range []struct{ method, path string }{
		{http.MethodGet, "/v1/public/holds/not-a-hold"},
		{http.MethodDelete, "/v1/public/holds/not-a-hold"},
		{http.MethodDelete, "/v1/public/holds/not-a-hold/seats/" + phSeat(1)},
		{http.MethodPost, "/v1/public/holds/not-a-hold/extend"},
	} {
		code, out, _ := phCall(t, srv, m.method, m.path, nil, "")
		if code != http.StatusNotFound || out.code() != "hold.not_found" {
			t.Errorf("%s %s = %d %q, want 404 hold.not_found", m.method, m.path, code, out.code())
		}
	}
	code, foreign := phPost(t, srv, f, map[string]any{"session_id": s.String(), "seat_keys": []string{phSeat(6)}, "hold_token": "not-a-hold"}, "")
	if code != http.StatusNotFound || foreign.code() != "hold.not_found" {
		t.Errorf("unknown key POST = %d %q, want 404", code, foreign.code())
	}
	code, other := phPost(t, srv, f, map[string]any{"session_id": f.sessions[1].String(), "seat_keys": []string{phSeat(1)}, "hold_token": h1.HoldToken}, "")
	if code != http.StatusNotFound || other.code() != "hold.not_found" {
		t.Errorf("key of another session = %d %q, want 404", code, other.code())
	}
	if st := phSeatStatus(t, ctx, pool, f.sessions[1], phSeat(1)); st != "available" {
		t.Errorf("a foreign key held a seat: %q", st)
	}
}

func TestHoldsPublic_PaymentStepAndExtend(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	f := newPHFixture(t, ctx, pool, 1, 6)
	srv := buildPHServer(t, pool)
	s := f.sessions[0]

	code, h := phPost(t, srv, f, phClick(s, "dev-ext", phSeat(1), phSeat(2)), "")
	if code != http.StatusCreated {
		t.Fatalf("click = %d %+v", code, h)
	}
	id := phHoldID(t, ctx, pool, h.HoldToken)
	extend := func() (int, phAnswer) {
		code, out, _ := phCall(t, srv, http.MethodPost, "/v1/public/holds/"+h.HoldToken+"/extend", nil, "")
		return code, out
	}

	// Too early.
	if code, out := extend(); code != http.StatusConflict || out.code() != "hold.extend_not_yet" {
		t.Fatalf("early extend = %d %q, want 409 hold.extend_not_yet", code, out.code())
	}
	// In the last minute: +300 s, the select deadline moves with it.
	if _, err := pool.Exec(ctx, `UPDATE reservations SET expires_at = now() + interval '60 seconds',
	      select_deadline = now() + interval '60 seconds' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	var old time.Time
	_ = pool.QueryRow(ctx, `SELECT expires_at FROM reservations WHERE id = $1`, id).Scan(&old)
	code, ext := extend()
	if code != http.StatusOK || !phNear(ext.ExpiresAt, old.Add(300*time.Second), 2*time.Second) || ext.ExtensionsLeft != 0 {
		t.Fatalf("extend = %d %+v, want %v", code, ext, old.Add(300*time.Second))
	}
	var sel time.Time
	_ = pool.QueryRow(ctx, `SELECT select_deadline FROM reservations WHERE id = $1`, id).Scan(&sel)
	if !phNear(sel, ext.ExpiresAt, time.Second) {
		t.Errorf("select deadline %v, want %v", sel, ext.ExpiresAt)
	}
	// A second extension is refused.
	if _, err := pool.Exec(ctx, `UPDATE reservations SET expires_at = now() + interval '60 seconds' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if code, out := extend(); code != http.StatusConflict || out.code() != "hold.extend_used" {
		t.Fatalf("second extend = %d %q, want 409 hold.extend_used", code, out.code())
	}

	// Payment step: no seat in or out.
	orderID, checkoutToken := phInPayment(t, ctx, f, s, h.HoldToken)
	code, add := phPost(t, srv, f, map[string]any{"session_id": s.String(), "seat_keys": []string{phSeat(3)}, "hold_token": h.HoldToken}, "")
	if code != http.StatusConflict || add.code() != "hold.in_payment" {
		t.Errorf("add on payment = %d %q, want 409 hold.in_payment", code, add.code())
	}
	code, join := phPost(t, srv, f, phClick(s, "dev-ext", phSeat(3)), "")
	if code != http.StatusConflict || join.code() != "hold.in_payment" {
		t.Errorf("join on payment = %d %q, want 409 hold.in_payment", code, join.code())
	}
	if code, out, _ := phCall(t, srv, http.MethodDelete, "/v1/public/holds/"+h.HoldToken+"/seats/"+phSeat(1), nil, ""); code != http.StatusConflict || out.code() != "hold.in_payment" {
		t.Errorf("remove seat on payment = %d %q", code, out.code())
	}
	if code, out, _ := phCall(t, srv, http.MethodDelete, "/v1/public/holds/"+h.HoldToken, nil, ""); code != http.StatusConflict || out.code() != "hold.in_payment" {
		t.Errorf("remove hold on payment = %d %q", code, out.code())
	}
	if st := phSeatStatus(t, ctx, pool, s, phSeat(3)); st != "available" {
		t.Errorf("a seat was added on the payment step: %q", st)
	}
	code, got, _ := phCall(t, srv, http.MethodGet, "/v1/public/holds/"+h.HoldToken, nil, "")
	if code != http.StatusOK || got.Step != "payment" || got.CheckoutToken == nil || *got.CheckoutToken != checkoutToken {
		t.Errorf("GET on payment = %d %+v", code, got)
	}

	// Extension on the payment step: the order gets the same expiry. The
	// first extension is spent, so give this hold a fresh one.
	if _, err := pool.Exec(ctx, `UPDATE reservations SET extensions_used = 0, expires_at = now() + interval '50 seconds' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	code, pext := extend()
	if code != http.StatusOK || pext.Step != "payment" {
		t.Fatalf("extend on payment = %d %+v", code, pext)
	}
	var resExp, ordExp time.Time
	_ = pool.QueryRow(ctx, `SELECT expires_at FROM reservations WHERE id = $1`, id).Scan(&resExp)
	_ = pool.QueryRow(ctx, `SELECT expires_at FROM orders WHERE id = $1`, orderID).Scan(&ordExp)
	if !resExp.Equal(ordExp) {
		t.Errorf("order expires %v, hold %v: want one expiry", ordExp, resExp)
	}

	// After the closing mark nothing extends.
	if _, err := pool.Exec(ctx, `UPDATE reservations SET extensions_used = 0, expires_at = now() + interval '50 seconds',
	      closing_started_at = now() WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if code, out := extend(); code != http.StatusConflict || out.code() != "hold.expired" {
		t.Errorf("extend while closing = %d %q, want 409 hold.expired", code, out.code())
	}
}

func TestHoldsPublic_Limits(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	f := newPHFixture(t, ctx, pool, 3, 20)
	srv := buildPHServer(t, pool)
	s := f.sessions[0]

	t.Run("eleventh seat", func(t *testing.T) {
		seats := make([]string, 0, 11)
		for i := 1; i <= 11; i++ {
			seats = append(seats, phSeat(i))
		}
		if code, out := phPost(t, srv, f, phClick(s, "dev-many", seats...), ""); code != http.StatusUnprocessableEntity || out.code() != "hold.too_many_seats" {
			t.Fatalf("11 seats at once = %d %q", code, out.code())
		}
		code, h := phPost(t, srv, f, phClick(s, "dev-many", seats[:10]...), "")
		if code != http.StatusCreated || len(h.SeatKeys) != 10 {
			t.Fatalf("10 seats = %d %+v", code, h)
		}
		if code, out := phPost(t, srv, f, phClick(s, "dev-many", phSeat(11)), ""); code != http.StatusUnprocessableEntity || out.code() != "hold.too_many_seats" {
			t.Fatalf("11th seat = %d %q", code, out.code())
		}
		if st := phSeatStatus(t, ctx, pool, s, phSeat(11)); st != "available" {
			t.Errorf("11th seat = %q, want available", st)
		}
		phCall(t, srv, http.MethodDelete, "/v1/public/holds/"+h.HoldToken, nil, "")
	})

	t.Run("third hold of a device", func(t *testing.T) {
		for i := 0; i < 2; i++ {
			if code, out := phPost(t, srv, f, phClick(f.sessions[i], "dev-three", phSeat(19)), ""); code != http.StatusCreated {
				t.Fatalf("hold %d = %d %q", i+1, code, out.code())
			}
		}
		if code, out := phPost(t, srv, f, phClick(f.sessions[2], "dev-three", phSeat(19)), ""); code != http.StatusTooManyRequests || out.code() != "hold.too_many_active" {
			t.Fatalf("third hold = %d %q, want 429 hold.too_many_active", code, out.code())
		}
		if st := phSeatStatus(t, ctx, pool, f.sessions[2], phSeat(19)); st != "available" {
			t.Errorf("third hold took a seat: %q", st)
		}
	})

	t.Run("pause after an unpaid hold", func(t *testing.T) {
		code, h := phPost(t, srv, f, phClick(s, "dev-pause", phSeat(12), phSeat(13)), "")
		if code != http.StatusCreated {
			t.Fatalf("hold = %d %q", code, h.code())
		}
		phExpire(t, ctx, pool, h.HoldToken)
		code, out := phPost(t, srv, f, phClick(s, "dev-pause", phSeat(12)), "")
		if code != http.StatusConflict || out.code() != "hold.rehold_paused" {
			t.Fatalf("same seat again = %d %q, want 409 hold.rehold_paused", code, out.code())
		}
		if r, _ := out.Error.Details["retry_after_seconds"].(float64); r <= 0 || r > 300 {
			t.Errorf("retry_after_seconds = %v", out.Error.Details["retry_after_seconds"])
		}
		code, other := phPost(t, srv, f, phClick(s, "dev-pause", phSeat(14), phSeat(13)), "")
		if code != http.StatusCreated || len(other.SeatKeys) != 1 || other.SeatKeys[0] != phSeat(14) ||
			len(other.Conflicts) != 1 || other.Conflicts[0]["status"] != "paused" {
			t.Fatalf("other seats = %d %+v", code, other)
		}
		if code, out := phPost(t, srv, f, phClick(s, "dev-neighbour", phSeat(12)), ""); code != http.StatusCreated {
			t.Errorf("another device on the paused seat = %d %q, want 201", code, out.code())
		}
		phCall(t, srv, http.MethodDelete, "/v1/public/holds/"+other.HoldToken, nil, "")
	})

	t.Run("hold again once", func(t *testing.T) {
		s2 := f.sessions[1]
		code, h1 := phPost(t, srv, f, phClick(s2, "dev-again", phSeat(1), phSeat(2)), "")
		if code != http.StatusCreated {
			t.Fatalf("hold = %d %q", code, h1.code())
		}
		phExpire(t, ctx, pool, h1.HoldToken)
		if code, out := phPost(t, srv, f, phClick(s2, "dev-taker", phSeat(2)), ""); code != http.StatusCreated {
			t.Fatalf("taker = %d %q", code, out.code())
		}
		before := time.Now()
		code, h2 := phPost(t, srv, f, map[string]any{"session_id": s2.String(), "device_id": "dev-again", "rehold_of": h1.HoldToken}, "")
		if code != http.StatusCreated || len(h2.SeatKeys) != 1 || h2.SeatKeys[0] != phSeat(1) ||
			len(h2.Conflicts) != 1 || h2.Conflicts[0]["seat_key"] != phSeat(2) {
			t.Fatalf("hold again = %d %+v", code, h2)
		}
		if !phNear(h2.ExpiresAt, before.Add(600*time.Second), 5*time.Second) {
			t.Errorf("hold again expires %v, want a fresh select step", h2.ExpiresAt)
		}
		var reholdOf uuid.UUID
		_ = pool.QueryRow(ctx, `SELECT rehold_of FROM reservations WHERE id = $1`, phHoldID(t, ctx, pool, h2.HoldToken)).Scan(&reholdOf)
		if reholdOf != phHoldID(t, ctx, pool, h1.HoldToken) {
			t.Errorf("rehold_of = %s", reholdOf)
		}
		phExpire(t, ctx, pool, h2.HoldToken)
		for _, of := range []string{h1.HoldToken, h2.HoldToken} {
			code, out := phPost(t, srv, f, map[string]any{"session_id": s2.String(), "device_id": "dev-again", "rehold_of": of}, "")
			if code != http.StatusConflict || out.code() != "hold.rehold_used" {
				t.Errorf("second hold again = %d %q, want 409 hold.rehold_used", code, out.code())
			}
		}
		// The pause starts when the "hold again" hold expires too.
		if code, out := phPost(t, srv, f, phClick(s2, "dev-again", phSeat(1)), ""); code != http.StatusConflict || out.code() != "hold.rehold_paused" {
			t.Errorf("after the second expiry = %d %q, want 409 hold.rehold_paused", code, out.code())
		}
		// Another device cannot use the old key.
		if code, out := phPost(t, srv, f, map[string]any{"session_id": s2.String(), "device_id": "dev-stranger", "rehold_of": h1.HoldToken}, ""); code != http.StatusNotFound {
			t.Errorf("hold again from another device = %d %q, want 404", code, out.code())
		}
	})

	t.Run("remove all and select again keeps the deadline", func(t *testing.T) {
		s3 := f.sessions[2]
		code, h := phPost(t, srv, f, phClick(s3, "dev-reset", phSeat(5)), "")
		if code != http.StatusCreated {
			t.Fatalf("hold = %d %q", code, h.code())
		}
		id := phHoldID(t, ctx, pool, h.HoldToken)
		if _, err := pool.Exec(ctx, `UPDATE reservations SET expires_at = now() + interval '200 seconds',
		      select_deadline = now() + interval '200 seconds' WHERE id = $1`, id); err != nil {
			t.Fatal(err)
		}
		var deadline time.Time
		_ = pool.QueryRow(ctx, `SELECT select_deadline FROM reservations WHERE id = $1`, id).Scan(&deadline)
		if code, out, _ := phCall(t, srv, http.MethodDelete, "/v1/public/holds/"+h.HoldToken+"/seats/"+phSeat(5), nil, ""); code != http.StatusOK || out.State != "cancelled" {
			t.Fatalf("remove the last seat = %d %+v", code, out)
		}
		code, again := phPost(t, srv, f, phClick(s3, "dev-reset", phSeat(6)), "")
		if code != http.StatusCreated || again.HoldToken == h.HoldToken {
			t.Fatalf("select again = %d %+v", code, again)
		}
		if !phNear(again.ExpiresAt, deadline, time.Second) {
			t.Errorf("new hold expires %v, want the old deadline %v", again.ExpiresAt, deadline)
		}
	})
}

func TestHoldsPublic_TwentyParallelCreatesOneDevice(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	f := newPHFixture(t, ctx, pool, 4, 12)
	srv := buildPHServer(t, pool)

	var wg sync.WaitGroup
	codes := make([]int, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			session := f.sessions[i%4]
			codes[i], _ = phPost(t, srv, f, phClick(session, "dev-burst", phSeat(i/4+1)), "")
		}(i)
	}
	wg.Wait()
	for i, c := range codes {
		switch c {
		case http.StatusCreated, http.StatusOK, http.StatusTooManyRequests:
		default:
			t.Errorf("request %d = %d", i, c)
		}
	}
	if n := phOpenHolds(t, ctx, pool, f.sessions); n < 1 || n > 2 {
		t.Fatalf("open holds of one device = %d, want at most 2", n)
	}
	// Every seat that is held belongs to one of those holds.
	var held, linked int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM session_seats WHERE session_id = ANY($1::uuid[]) AND status = 'held'`, f.sessions).Scan(&held)
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM reservation_seats rs JOIN reservations r ON r.id = rs.reservation_id
	      WHERE r.session_id = ANY($1::uuid[]) AND r.state = 'draft'`, f.sessions).Scan(&linked)
	if held != linked {
		t.Errorf("held seats %d, linked seats %d", held, linked)
	}
}

func TestHoldsPublic_OppositeOrderClicksNoDeadlock(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	f := newPHFixture(t, ctx, pool, 1, 8)
	srv := buildPHServer(t, pool)
	s := f.sessions[0]

	deadlocks := func() int64 {
		var n int64
		if err := pool.QueryRow(ctx, `SELECT deadlocks FROM pg_stat_database WHERE datname = current_database()`).Scan(&n); err != nil {
			t.Fatalf("pg_stat_database: %v", err)
		}
		return n
	}
	start := deadlocks()

	var mu sync.Mutex
	var bad []string
	clicker := func(device string, order []string, multi bool) string {
		token := ""
		if multi {
			code, h := phPost(t, srv, f, phClick(s, device, order...), "")
			if code >= 500 {
				mu.Lock()
				bad = append(bad, fmt.Sprintf("%s multi = %d", device, code))
				mu.Unlock()
			}
			return h.HoldToken
		}
		for _, k := range order {
			body := phClick(s, device, k)
			if token != "" {
				body["hold_token"] = token
			}
			code, h := phPost(t, srv, f, body, "")
			if code >= 500 {
				mu.Lock()
				bad = append(bad, fmt.Sprintf("%s %s = %d", device, k, code))
				mu.Unlock()
			}
			if h.HoldToken != "" {
				token = h.HoldToken
			}
		}
		return token
	}
	forward := []string{phSeat(1), phSeat(2), phSeat(3), phSeat(4)}
	backward := []string{phSeat(4), phSeat(3), phSeat(2), phSeat(1)}
	for round := 0; round < 20; round++ {
		multi := round%2 == 1
		var wg sync.WaitGroup
		tokens := make([]string, 2)
		wg.Add(2)
		go func() { defer wg.Done(); tokens[0] = clicker("dev-left", forward, multi) }()
		go func() { defer wg.Done(); tokens[1] = clicker("dev-right", backward, multi) }()
		wg.Wait()
		// Never one seat in two holds.
		var doubled int
		_ = pool.QueryRow(ctx, `SELECT count(*) FROM (SELECT rs.session_seat_id FROM reservation_seats rs
		      JOIN reservations r ON r.id = rs.reservation_id WHERE r.session_id = $1 AND r.state = 'draft'
		      GROUP BY rs.session_seat_id HAVING count(*) > 1) d`, s).Scan(&doubled)
		if doubled != 0 {
			t.Fatalf("round %d: %d seats in two holds", round, doubled)
		}
		for _, tok := range tokens {
			if tok != "" {
				phCall(t, srv, http.MethodDelete, "/v1/public/holds/"+tok, nil, "")
			}
		}
	}
	if len(bad) > 0 {
		t.Fatalf("server errors: %v", bad)
	}
	// Statistics reach pg_stat_database within about a second.
	time.Sleep(1500 * time.Millisecond)
	if _, err := pool.Exec(ctx, `SELECT pg_stat_clear_snapshot()`); err != nil {
		t.Fatal(err)
	}
	if end := deadlocks(); end != start {
		t.Fatalf("deadlocks %d -> %d, want none (40P01)", start, end)
	}
}

func TestHoldsPublic_SeatStatusPollInterval(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	f := newPHFixture(t, ctx, pool, 1, 2)
	srv := buildPHServer(t, pool)
	read := func(query string) float64 {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/v1/event-sessions/"+f.sessions[0].String()+"/seat-status"+query, nil)
		rec := httptest.NewRecorder()
		srv.router.ServeHTTP(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		if rec.Code != http.StatusOK {
			t.Fatalf("seat-status = %d %s", rec.Code, rec.Body.String())
		}
		v, _ := out["poll_interval_ms"].(float64)
		return v
	}
	if v := read(""); v != 3000 {
		t.Errorf("standard poll_interval_ms = %v, want 3000", v)
	}
	if _, err := pool.Exec(ctx, `UPDATE events SET hold_profile = 'high_demand' WHERE id = $1`, f.eventID); err != nil {
		t.Fatal(err)
	}
	if v := read("?since_version=999999"); v != 1500 {
		t.Errorf("high_demand poll_interval_ms = %v, want 1500", v)
	}
}
