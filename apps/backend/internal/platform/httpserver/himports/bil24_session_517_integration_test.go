//go:build integration

// bil24_session_517_integration_test.go — live-PostgreSQL coverage for the
// Bil24 session import (feature #517, W1-C3c; spec §13.2 steps 2-5, 7-10).
//
// The unit tests in bil24_session_517_test.go prove the pre-transaction
// validation ladder without a database. Everything asserted HERE needs real
// rows: the venue/event/session/tier upserts, the compat-id mappings that make
// the endpoint idempotent, the org-membership gate driven through the real
// gen queries, and the publish transition.
//
// Run with:
//
//	DATABASE_URL=postgres://arena:arena@localhost:55432/arena?sslmode=disable \
//	    go test -tags integration \
//	    ./apps/backend/internal/platform/httpserver/himports/ \
//	    -run TestBil24SessionImport517Integration
package himports

import (
	"bytes"
	"context"
	"encoding/json"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/bil24compat"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
)

// ─────────────────────────────────────────────────────────────────────────────
// Fixture
// ─────────────────────────────────────────────────────────────────────────────

// import517Fixture is an organization plus a user who is a member of it. The
// Bil24 identifiers are randomized per run because compatibility_id_map is
// unique on (kind, system_id) GLOBALLY and venues.external_bil24_id is likewise
// not org-scoped: a fixed literal would collide with leftovers from an earlier
// interrupted run against the shared dev stand (see AGENTS.md).
type import517Fixture struct {
	t      *testing.T
	pool   *pgxpool.Pool
	orgID  uuid.UUID
	userID uuid.UUID

	actionID      int64
	actionEventID int64
	venueID       int64
	categoryA     int64
	categoryB     int64
}

func newImport517Fixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) *import517Fixture {
	t.Helper()
	q := gen.New(pool)

	org, err := q.InsertOrganization(ctx, "Import517 Test Org", "import517-"+uuid.NewString(), "HU", "en", 1200)
	if err != nil {
		t.Fatalf("newImport517Fixture: InsertOrganization: %v", err)
	}
	user, err := q.InsertUser(ctx, "import517-"+uuid.NewString()+"@test.arena.local", "x", "en")
	if err != nil {
		_, _ = pool.Exec(ctx, `DELETE FROM organizations WHERE id = $1`, org.ID)
		t.Fatalf("newImport517Fixture: InsertUser: %v", err)
	}
	// "organizer" is one of the values allowed by memberships_role_check
	// (migration 0042). The import gate only asks "is this actor a member of
	// the org", so the specific role matters no further than that constraint.
	if _, err := q.InsertMembership(ctx, user.ID, org.ID, "organizer"); err != nil {
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, user.ID)
		_, _ = pool.Exec(ctx, `DELETE FROM organizations WHERE id = $1`, org.ID)
		t.Fatalf("newImport517Fixture: InsertMembership: %v", err)
	}

	// Bil24 ids must stay in (0, 1e9); draw from a 100k-wide band well below
	// the ceiling so every generated value is valid by construction.
	base := int64(rand.Intn(800_000_000)) + 1_000 //nolint:gosec // test data, not security-sensitive
	return &import517Fixture{
		t: t, pool: pool, orgID: org.ID, userID: user.ID,
		actionID:      base,
		actionEventID: base + 1,
		venueID:       base + 2,
		categoryA:     base + 3,
		categoryB:     base + 4,
	}
}

// cleanup removes every row the import may have written, in FK-safe order.
// Best-effort: it runs from a defer after the assertions, so it logs instead of
// failing.
func (f *import517Fixture) cleanup() {
	ctx := context.Background()
	exec := func(label, sql string, args ...any) {
		if _, err := f.pool.Exec(ctx, sql, args...); err != nil {
			f.t.Logf("import517Fixture cleanup: %s: %v", label, err)
		}
	}
	exec("ticket_tiers", `DELETE FROM ticket_tiers WHERE session_id IN (
	          SELECT s.id FROM sessions s JOIN events e ON e.id = s.event_id WHERE e.org_id = $1)`, f.orgID)
	exec("sessions", `DELETE FROM sessions WHERE event_id IN (SELECT id FROM events WHERE org_id = $1)`, f.orgID)
	exec("events", `DELETE FROM events WHERE org_id = $1`, f.orgID)
	exec("venues", `DELETE FROM venues WHERE org_id = $1`, f.orgID)
	exec("compatibility_id_map", `DELETE FROM compatibility_id_map WHERE system_id = ANY($1)`,
		[]int64{f.actionID, f.actionEventID, f.venueID, f.categoryA, f.categoryB})
	exec("memberships", `DELETE FROM memberships WHERE org_id = $1`, f.orgID)
	exec("users", `DELETE FROM users WHERE id = $1`, f.userID)
	exec("organizations", `DELETE FROM organizations WHERE id = $1`, f.orgID)
}

// payload builds a valid two-category general-admission import body.
func (f *import517Fixture) payload() bil24compat.ImportSessionRequest {
	return bil24compat.ImportSessionRequest{
		Action: bil24compat.ImportSessionAction{
			ActionID:       f.actionID,
			ActionName:     "Import517",
			FullActionName: "Import517 Grand Tasting",
			Description:    "imported by the #517 integration test",
			Age:            "18+",
		},
		ActionEvent: bil24compat.ImportSessionActionEvent{
			ActionEventID: f.actionEventID,
			Day:           "26.04.2026",
			Time:          "17:00",
			Currency:      "EUR",
			SellEndTime:   "2026-04-26T14:00:00Z",
			ChargePercent: 5,
		},
		Venue: bil24compat.ImportSessionVenue{
			VenueID:   f.venueID,
			VenueName: "Import517 Hall",
			Address:   "1 Test Street",
			Timezone:  "Europe/Madrid",
		},
		CategoryList: []bil24compat.ImportSessionCategory{
			{CategoryPriceID: f.categoryA, CategoryPriceName: "Parter", Price: 25, Availability: 100},
			{CategoryPriceID: f.categoryB, CategoryPriceName: "Balcony", Price: 12.5, Availability: 40},
		},
	}
}

func import517Pool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping integration test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Skipf("cannot connect to PostgreSQL (%v); skipping", err)
	}
	t.Cleanup(func() { pool.Close() })
	return pool
}

// call drives the real handler against the real pool, authenticated as the
// fixture's member user. membershipQueries is wired, so the org gate is the
// production one — not the nil-skip test shortcut.
func (f *import517Fixture) call(h *Handler, body bil24compat.ImportSessionRequest) (*httptest.ResponseRecorder, ImportSessionResponse) {
	f.t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		f.t.Fatalf("marshal payload: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost,
		"/v1/organizations/"+f.orgID.String()+"/imports/bil24-session", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("org_id", f.orgID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = auth.WithActor(ctx, auth.Actor{ID: f.userID.String(), Type: auth.ActorTypeUser})

	rec := httptest.NewRecorder()
	h.HandleBil24Session(rec, req.WithContext(ctx))

	var out ImportSessionResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			f.t.Fatalf("decode response: %v; body=%s", err, rec.Body.String())
		}
	}
	return rec, out
}

// ─────────────────────────────────────────────────────────────────────────────
// Tests
// ─────────────────────────────────────────────────────────────────────────────

// TestBil24SessionImport517Integration_CreateThenIdempotentRepeat is the core
// §13.2 guarantee: the first import creates the whole catalog subtree, and a
// second import of the same payload updates it in place, answering created=false
// with byte-identical identifiers.
func TestBil24SessionImport517Integration_CreateThenIdempotentRepeat(t *testing.T) {
	pool := import517Pool(t)
	ctx := context.Background()
	f := newImport517Fixture(t, ctx, pool)
	defer f.cleanup()

	h := New(gen.New(pool), pool, nil, nil).WithMembershipQueries(gen.New(pool))

	// ── first import ────────────────────────────────────────────────────────
	rec, first := f.call(h, f.payload())
	if rec.Code != http.StatusOK {
		t.Fatalf("first import: status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if !first.Created {
		t.Errorf("first import: created = false, want true")
	}
	if first.EventID == uuid.Nil || first.SessionID == uuid.Nil {
		t.Fatalf("first import: got nil ids: %+v", first)
	}
	if len(first.TierIDs) != 2 {
		t.Fatalf("first import: len(tier_ids) = %d, want 2 (%v)", len(first.TierIDs), first.TierIDs)
	}
	if first.SeatingPlanVersionID != nil {
		t.Errorf("first import: seating_plan_version_id = %v, want null (GA-only slice)", *first.SeatingPlanVersionID)
	}
	if first.SeatsMaterialized != 0 {
		t.Errorf("first import: seats_materialized = %d, want 0", first.SeatsMaterialized)
	}
	// chargePercent is informational only (step 7) — it must surface as a
	// warning and must NOT have been applied anywhere.
	if !hasWarning(first.Warnings, WarnChargePercentMismatch) {
		t.Errorf("first import: missing %s warning; got %+v", WarnChargePercentMismatch, first.Warnings)
	}

	assertSessionRow(t, ctx, pool, first.SessionID, sessionExpectation{
		orgID:    f.orgID,
		eventID:  first.EventID,
		currency: "EUR",
		capacity: 140, // 100 + 40, summed across categories
		status:   "draft",
		// 17:00 in Europe/Madrid on 26 Apr 2026 (CEST, UTC+2) is 15:00Z.
		startAtUTC: "2026-04-26T15:00:00Z",
	})
	assertTierPrices(t, ctx, pool, first.TierIDs, map[string]int64{
		externalIDString(f.categoryA): 2500,
		externalIDString(f.categoryB): 1250, // 12.5 major units → 1250 minor
	})

	// ── second import: same payload ─────────────────────────────────────────
	rec2, second := f.call(h, f.payload())
	if rec2.Code != http.StatusOK {
		t.Fatalf("repeat import: status = %d, want 200; body=%s", rec2.Code, rec2.Body.String())
	}
	if second.Created {
		t.Errorf("repeat import: created = true, want false (idempotent on actionEventId)")
	}
	if second.EventID != first.EventID {
		t.Errorf("repeat import: event_id = %s, want %s", second.EventID, first.EventID)
	}
	if second.SessionID != first.SessionID {
		t.Errorf("repeat import: session_id = %s, want %s", second.SessionID, first.SessionID)
	}
	for key, want := range first.TierIDs {
		if got := second.TierIDs[key]; got != want {
			t.Errorf("repeat import: tier_ids[%s] = %s, want %s", key, got, want)
		}
	}
	assertRowCount(t, ctx, pool, 1, `SELECT count(*) FROM events WHERE org_id = $1`, f.orgID)
	assertRowCount(t, ctx, pool, 1, `SELECT count(*) FROM venues WHERE org_id = $1`, f.orgID)
	assertRowCount(t, ctx, pool, 2, `SELECT count(*) FROM ticket_tiers WHERE session_id = $1`, first.SessionID)
}

// TestBil24SessionImport517Integration_PublishTransitionsCatalog proves step 8:
// publish:true moves the event to 'published' and the session to 'scheduled'
// through the standard gate, on a payload whose tiers satisfy it.
func TestBil24SessionImport517Integration_PublishTransitionsCatalog(t *testing.T) {
	pool := import517Pool(t)
	ctx := context.Background()
	f := newImport517Fixture(t, ctx, pool)
	defer f.cleanup()

	h := New(gen.New(pool), pool, nil, nil).WithMembershipQueries(gen.New(pool))

	body := f.payload()
	body.Publish = true
	rec, res := f.call(h, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("import: status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if hasWarning(res.Warnings, WarnPublishSkipped) {
		t.Fatalf("publish was skipped: %+v", res.Warnings)
	}

	var eventStatus, sessionStatus string
	if err := pool.QueryRow(ctx, `SELECT status FROM events WHERE id = $1`, res.EventID).Scan(&eventStatus); err != nil {
		t.Fatalf("read event status: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM sessions WHERE id = $1`, res.SessionID).Scan(&sessionStatus); err != nil {
		t.Fatalf("read session status: %v", err)
	}
	if eventStatus != "published" {
		t.Errorf("event status = %q, want published", eventStatus)
	}
	if sessionStatus != "scheduled" {
		t.Errorf("session status = %q, want scheduled", sessionStatus)
	}
}

// TestBil24SessionImport517Integration_RejectsNonMember proves the org gate runs
// against real membership rows: an authenticated user with no membership in the
// target organization gets 403 org.access_denied and writes nothing.
func TestBil24SessionImport517Integration_RejectsNonMember(t *testing.T) {
	pool := import517Pool(t)
	ctx := context.Background()
	f := newImport517Fixture(t, ctx, pool)
	defer f.cleanup()

	h := New(gen.New(pool), pool, nil, nil).WithMembershipQueries(gen.New(pool))

	// Swap in a user id that holds no membership anywhere.
	f.userID = uuid.New()
	rec, _ := f.call(h, f.payload())
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
	assertRowCount(t, ctx, pool, 0, `SELECT count(*) FROM events WHERE org_id = $1`, f.orgID)
	assertRowCount(t, ctx, pool, 0, `SELECT count(*) FROM venues WHERE org_id = $1`, f.orgID)
}

// ─────────────────────────────────────────────────────────────────────────────
// Assertion helpers
// ─────────────────────────────────────────────────────────────────────────────

type sessionExpectation struct {
	orgID      uuid.UUID
	eventID    uuid.UUID
	currency   string
	capacity   int32
	status     string
	startAtUTC string
}

func assertSessionRow(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sessionID uuid.UUID, want sessionExpectation) {
	t.Helper()
	var (
		eventID    uuid.UUID
		orgID      uuid.UUID
		currency   string
		capacity   *int32
		status     string
		admission  string
		startAtStr string
	)
	err := pool.QueryRow(ctx, `
		SELECT s.event_id, e.org_id, s.currency, s.capacity_total, s.status, s.admission_mode,
		       to_char(s.start_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"')
		FROM   sessions s
		JOIN   events e ON e.id = s.event_id
		WHERE  s.id = $1`, sessionID).
		Scan(&eventID, &orgID, &currency, &capacity, &status, &admission, &startAtStr)
	if err != nil {
		t.Fatalf("read session %s: %v", sessionID, err)
	}
	if eventID != want.eventID {
		t.Errorf("session.event_id = %s, want %s", eventID, want.eventID)
	}
	if orgID != want.orgID {
		t.Errorf("session org_id = %s, want %s", orgID, want.orgID)
	}
	if currency != want.currency {
		t.Errorf("session.currency = %q, want %q", currency, want.currency)
	}
	if capacity == nil || *capacity != want.capacity {
		t.Errorf("session.capacity = %v, want %d", capacity, want.capacity)
	}
	if status != want.status {
		t.Errorf("session.status = %q, want %q", status, want.status)
	}
	if admission != "general_admission" {
		t.Errorf("session.admission_mode = %q, want general_admission", admission)
	}
	if startAtStr != want.startAtUTC {
		t.Errorf("session.start_at = %s, want %s (venue-local wall clock)", startAtStr, want.startAtUTC)
	}
}

func assertTierPrices(t *testing.T, ctx context.Context, pool *pgxpool.Pool, tierIDs map[string]uuid.UUID, wantMinor map[string]int64) {
	t.Helper()
	for key, wantPrice := range wantMinor {
		id, ok := tierIDs[key]
		if !ok {
			t.Errorf("tier_ids is missing key %q (got %v)", key, tierIDs)
			continue
		}
		var price int64
		var currency string
		if err := pool.QueryRow(ctx,
			`SELECT price_amount, currency FROM ticket_tiers WHERE id = $1`, id).Scan(&price, &currency); err != nil {
			t.Errorf("read tier %s: %v", id, err)
			continue
		}
		if price != wantPrice {
			t.Errorf("tier %s price_amount = %d, want %d minor units", key, price, wantPrice)
		}
		if currency != "EUR" {
			t.Errorf("tier %s currency = %q, want EUR", key, currency)
		}
	}
}

func assertRowCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, want int, sql string, args ...any) {
	t.Helper()
	var got int
	if err := pool.QueryRow(ctx, sql, args...).Scan(&got); err != nil {
		t.Fatalf("count query %q: %v", sql, err)
	}
	if got != want {
		t.Errorf("count = %d, want %d (%s)", got, want, sql)
	}
}

func hasWarning(warnings []Warning, code string) bool {
	for _, w := range warnings {
		if w.Code == code {
			return true
		}
	}
	return false
}
