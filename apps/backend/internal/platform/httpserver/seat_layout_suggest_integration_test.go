//go:build integration

package httpserver

// seat_layout_suggest_integration_test.go — task S1e: the seat-layout
// allocator routes through the REAL router against a migrated database.
//
//   - Palác Akropolis bound to a session with partial sales: sold seats
//     (some with an order line, one sold upstream), held seats and closed
//     seats. suggest leaves every sold and held seat out of the layout,
//     counts them "by fact" and writes nothing.
//   - A second, trivial algorithm registered only in this test binary
//     goes through the same two routes with no change to the API.
//   - A foreign session is 404, a session without a plan 422, an unknown
//     algorithm or a parameter outside the schema 400, a missing stage 422,
//     an agent 403.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/seating"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/seating/layoutgen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
)

// s1eFlat is the trivial second algorithm: one category at price_max for
// every free seat. It exists only in this test binary.
type s1eFlat struct{}

func (s1eFlat) ID() string { return "test_flat_s1e" }

func (s1eFlat) Params() layoutgen.ParamSchema {
	maxPrice := int64(100_000_000)
	zero := int64(0)
	return layoutgen.ParamSchema{
		LabelKey:            "layoutgen.algorithm.test_flat",
		RequiredAnnotations: []string{},
		Params: []layoutgen.Param{{
			Name: "price_max", Type: layoutgen.TypeInteger, Required: true,
			Min: &zero, Max: &maxPrice, LabelKey: "layoutgen.param.price_max",
		}},
	}
}

func (s1eFlat) Suggest(g seating.Geometry, _ layoutgen.Annotations, st layoutgen.SessionState, p layoutgen.Values) (layoutgen.Layout, layoutgen.Metrics, error) {
	price, _ := p.Int("price_max")
	l := layoutgen.Layout{Slots: []layoutgen.Slot{{Key: "all", PriceAmount: price, Share: 1}}, Assignments: map[string]string{}}
	for _, s := range layoutgen.Seats(g) {
		if _, frozen := st.SoldHeld[s.Key]; !frozen {
			l.Assignments[s.Key] = "all"
		}
	}
	return l, layoutgen.Evaluate(g, l, st), nil
}

func init() { layoutgen.Register(s1eFlat{}) }

type s1eEnv struct {
	ts                 *httptest.Server
	pool               *pgxpool.Pool
	orgA, orgB         uuid.UUID
	owner, mgr, agent  string
	foreignOwner       string
	seated, gaSession  uuid.UUID
	geometry           seating.Geometry
	soldWithOrder      []string
	soldUpstream, held []string
	closed             []string
}

const (
	s1eTierPrice  = int64(200000)
	s1eOrderPrice = int64(150000)
)

func newS1eEnv(t *testing.T) *s1eEnv {
	t.Helper()
	srv, secret := productionIntegrationServer(t)
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	pool := srv.pgxPool
	q := gen.New(pool)
	ctx := context.Background()
	e := &s1eEnv{ts: ts, pool: pool}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("fixture %q: %v", sql, err)
		}
	}
	suffix := uuid.NewString()[:8]
	newOrg := func(label string) uuid.UUID {
		org, err := q.InsertOrganization(ctx, "S1e "+label+" "+suffix, "s1e-"+label+"-"+suffix, "CZ", "en", 1200)
		if err != nil {
			t.Fatalf("InsertOrganization: %v", err)
		}
		return org.ID
	}
	e.orgA, e.orgB = newOrg("a"), newOrg("b")
	member := func(org uuid.UUID, role string) string {
		u, err := q.InsertUser(ctx, "s1e-"+role+"-"+uuid.NewString()+"@example.test", "x", "en")
		if err != nil {
			t.Fatalf("InsertUser: %v", err)
		}
		if _, err := q.InsertMembership(ctx, u.ID, org, role); err != nil {
			t.Fatalf("InsertMembership: %v", err)
		}
		tok, _, err := auth.IssueJWT(secret, u.ID, nil, nil, "arena-api", "arena-api", time.Hour)
		if err != nil {
			t.Fatalf("IssueJWT: %v", err)
		}
		return tok
	}
	e.owner, e.mgr, e.agent = member(e.orgA, "org_admin"), member(e.orgA, "organizer"), member(e.orgA, "agent")
	e.foreignOwner = member(e.orgB, "org_admin")

	raw, err := os.ReadFile(filepath.Join("..", "..", "domain", "seating", "testdata", "Palac_Akropolis.svg"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	g, _, verrs := seating.ImportSVG(raw)
	if len(verrs) != 0 {
		t.Fatalf("import: %v", verrs)
	}
	e.geometry = seating.Canonicalize(g)
	geoJSON, _ := seating.CanonicalJSON(e.geometry)
	checksum, _ := seating.Checksum(e.geometry)

	venue, event, channel := uuid.New(), uuid.New(), uuid.New()
	plan, version, tier := uuid.New(), uuid.New(), uuid.New()
	e.seated, e.gaSession = uuid.New(), uuid.New()
	start := time.Now().UTC().Add(96 * time.Hour).Truncate(time.Hour)

	t.Cleanup(func() {
		c := context.Background()
		for _, st := range []string{
			`DELETE FROM order_items WHERE order_id IN (SELECT id FROM orders WHERE org_id = $1)`,
			`DELETE FROM orders WHERE org_id = $1`,
			`DELETE FROM checkout_sessions WHERE org_id = $1`,
			`DELETE FROM session_seats WHERE session_id IN (SELECT s.id FROM sessions s JOIN events e ON e.id = s.event_id WHERE e.org_id = $1)`,
			`DELETE FROM reservations WHERE org_id = $1`,
			`DELETE FROM ticket_tiers WHERE session_id IN (SELECT s.id FROM sessions s JOIN events e ON e.id = s.event_id WHERE e.org_id = $1)`,
			`DELETE FROM sessions WHERE event_id IN (SELECT id FROM events WHERE org_id = $1)`,
			`DELETE FROM events WHERE org_id = $1`,
			`DELETE FROM seating_plan_versions WHERE seating_plan_id IN (SELECT id FROM seating_plans WHERE owner_org_id = $1)`,
			`DELETE FROM seating_plans WHERE owner_org_id = $1`,
			`DELETE FROM sales_channels WHERE org_id = $1`,
			`DELETE FROM venues WHERE org_id = $1`,
			`DELETE FROM memberships WHERE org_id = $1`,
			`DELETE FROM organizations WHERE id = $1`,
		} {
			for _, org := range []uuid.UUID{e.orgA, e.orgB} {
				if _, err := pool.Exec(c, st, org); err != nil {
					fmt.Printf("s1e cleanup %q: %v\n", st, err)
				}
			}
		}
	})

	exec(`INSERT INTO venues (id, org_id, name, timezone) VALUES ($1, $2, $3, 'Europe/Prague')`, venue, e.orgA, "Akropolis "+suffix)
	exec(`INSERT INTO events (id, org_id, name, status, visibility) VALUES ($1, $2, $3, 'draft', 'public')`, event, e.orgA, "S1e Show "+suffix)
	exec(`INSERT INTO sales_channels (id, org_id, name) VALUES ($1, $2, $3)`, channel, e.orgA, "S1e Channel "+suffix)
	exec(`INSERT INTO seating_plans (id, venue_id, owner_org_id, name, plan_type, status) VALUES ($1, $2, $3, $4, 'assigned_seats', 'active')`,
		plan, venue, e.orgA, "Akropolis "+suffix)
	exec(`INSERT INTO seating_plan_versions (id, seating_plan_id, version_number, geometry, geometry_checksum, capacity_seated, capacity_standing)
	      VALUES ($1, $2, 1, $3::jsonb, $4, $5, 0)`, version, plan, string(geoJSON), checksum, e.geometry.SeatCount())
	exec(`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, status, currency, currency_source, admission_mode, seating_plan_version_id)
	      VALUES ($1, $2, $3, $4, $4::timestamptz + interval '2 hours', $5, 'scheduled', 'CZK', 'override', 'assigned_seats', $6)`,
		e.seated, event, venue, start, e.geometry.SeatCount(), version)
	exec(`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, status, currency, currency_source)
	      VALUES ($1, $2, $3, $4, $4::timestamptz + interval '2 hours', 50, 'scheduled', 'CZK', 'override')`,
		e.gaSession, event, venue, start.Add(24*time.Hour))
	exec(`INSERT INTO ticket_tiers (id, session_id, name, pricing_mode, price_amount, currency, unit_seq, is_open)
	      VALUES ($1, $2, 'Hall', 'fixed', $3, 'CZK', 1, true)`, tier, e.seated, s1eTierPrice)

	seats := layoutgen.Seats(e.geometry)
	for _, s := range seats {
		sec := e.geometry.Sections[s.Section]
		exec(`INSERT INTO session_seats (session_id, seat_key, sector_name, row_name, seat_number, tier_id, kind)
		      VALUES ($1, $2, $3, $4, $5, $6, 'seat')`, e.seated, s.Key, sec.Name, sec.Rows[s.Row].Name, s.Number, tier)
	}

	// Partial sales: 5 seats sold through an order at 1 500.00, 3 held
	// without an order yet, 1 sold upstream, 2 closed.
	soldRes, heldRes, cs, order := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	exec(`INSERT INTO reservations (id, org_id, channel_id, session_id, quantity, state, expires_at, converted_at)
	      VALUES ($1, $2, $3, $4, 5, 'converted', now() + interval '1 hour', now())`, soldRes, e.orgA, channel, e.seated)
	exec(`INSERT INTO reservations (id, org_id, channel_id, session_id, quantity, state, expires_at)
	      VALUES ($1, $2, $3, $4, 3, 'active', now() + interval '1 hour')`, heldRes, e.orgA, channel, e.seated)
	exec(`INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state) VALUES ($1, $2, $3, $4, 'completed')`, cs, e.orgA, channel, soldRes)
	exec(`INSERT INTO orders (id, org_id, channel_id, event_id, session_id, checkout_session_id, reservation_id,
	                          source, status, currency, subtotal, discount, charge, total, buyer_name, buyer_email, paid_at)
	      VALUES ($1, $2, $3, $4, $5, $6, $7, 'public_feed', 'paid', 'CZK', $8, 0, 0, $8, 'S1e Buyer', $9, now())`,
		order, e.orgA, channel, event, e.seated, cs, soldRes, 5*s1eOrderPrice, "s1e-buyer-"+suffix+"@example.test")
	for i, s := range seats[:5] {
		e.soldWithOrder = append(e.soldWithOrder, s.Key)
		exec(`UPDATE session_seats SET status = 'sold', reservation_id = $3 WHERE session_id = $1 AND seat_key = $2`, e.seated, s.Key, soldRes)
		exec(`INSERT INTO order_items (order_id, ordinal, tier_id, session_seat_id, unit_price, total)
		      SELECT $1, $2, $3, id, $4, $4 FROM session_seats WHERE session_id = $5 AND seat_key = $6`,
			order, i, tier, s1eOrderPrice, e.seated, s.Key)
	}
	for _, s := range seats[5:8] {
		e.held = append(e.held, s.Key)
		exec(`UPDATE session_seats SET status = 'held', reservation_id = $3 WHERE session_id = $1 AND seat_key = $2`, e.seated, s.Key, heldRes)
	}
	e.soldUpstream = []string{seats[8].Key}
	exec(`UPDATE session_seats SET status = 'sold' WHERE session_id = $1 AND seat_key = $2`, e.seated, seats[8].Key)
	for _, s := range seats[9:11] {
		e.closed = append(e.closed, s.Key)
		exec(`UPDATE session_seats SET status = 'unavailable' WHERE session_id = $1 AND seat_key = $2`, e.seated, s.Key)
	}
	return e
}

func (e *s1eEnv) call(t *testing.T, method, path, token, body string) (int, map[string]any) {
	t.Helper()
	resp := integDoRequest(t, e.ts.Client(), method, e.ts.URL+path, token, body)
	raw := integReadBody(t, resp)
	out := map[string]any{}
	_ = json.Unmarshal([]byte(raw), &out)
	return resp.StatusCode, out
}

func (e *s1eEnv) path(org, session uuid.UUID, tail string) string {
	return "/v1/organizations/" + org.String() + "/sessions/" + session.String() + "/seat-layout/" + tail
}

// seatFingerprint is everything suggest must not change.
func (e *s1eEnv) seatFingerprint(t *testing.T) string {
	t.Helper()
	var fp string
	err := e.pool.QueryRow(context.Background(), `
		SELECT (SELECT seat_status_version FROM sessions WHERE id = $1)::text || '|' ||
		       (SELECT md5(string_agg(seat_key || status || coalesce(tier_id::text, '') || status_version::text, ',' ORDER BY seat_key))
		          FROM session_seats WHERE session_id = $1) || '|' ||
		       (SELECT count(*) FROM audit_events WHERE metadata->>'session_id' = $1::text)::text`, e.seated).Scan(&fp)
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	return fp
}

const s1eStage = `{"focal_point":{"x":152,"y":20}}`

func TestS1e_SuggestOnPartlySoldSessionFreezesSalesAndWritesNothing(t *testing.T) {
	e := newS1eEnv(t)
	before := e.seatFingerprint(t)

	body := `{"algorithm":"desirability","params":{"categories":4,"price_max":250000,"price_min":90000},"annotations":` + s1eStage + `}`
	for _, tok := range []string{e.owner, e.mgr} {
		status, out := e.call(t, http.MethodPost, e.path(e.orgA, e.seated, "suggest"), tok, body)
		if status != http.StatusOK {
			t.Fatalf("suggest = %d %v", status, out)
		}
		if out["algorithm"] != "desirability" || out["currency"] != "CZK" {
			t.Fatalf("answer names %v / %v", out["algorithm"], out["currency"])
		}
		layout := out["layout"].(map[string]any)
		assignments := layout["assignments"].(map[string]any)
		frozen := map[string]bool{}
		for _, k := range append(append(append([]string{}, e.soldWithOrder...), e.soldUpstream...), e.held...) {
			frozen[k] = true
			if _, ok := assignments[k]; ok {
				t.Fatalf("frozen seat %s got a slot", k)
			}
		}
		if got, want := len(assignments), e.geometry.SeatCount()-len(frozen); got != want {
			t.Fatalf("assigned %d seats, want %d", got, want)
		}
		for _, k := range e.closed {
			if _, ok := assignments[k]; !ok {
				t.Fatalf("closed seat %s has no slot", k)
			}
		}
		if n := len(layout["frozen_seat_keys"].([]any)); n != len(frozen) {
			t.Fatalf("frozen_seat_keys has %d, want %d", n, len(frozen))
		}

		m := out["metrics"].(map[string]any)
		num := func(k string) int64 { return int64(m[k].(float64)) }
		wantSold := int64(len(e.soldWithOrder))*s1eOrderPrice + int64(len(e.soldUpstream))*s1eTierPrice
		wantHeld := int64(len(e.held)) * s1eTierPrice
		if num("revenue_sold_minor") != wantSold || num("revenue_held_minor") != wantHeld {
			t.Fatalf("sold %d held %d, want %d %d (sold by fact)", num("revenue_sold_minor"), num("revenue_held_minor"), wantSold, wantHeld)
		}
		if num("sold_frozen") != int64(len(frozen)) || num("closed_seats") != int64(len(e.closed)) {
			t.Fatalf("counts %v", m)
		}
		if num("revenue_estimate_minor") != wantSold+wantHeld+num("revenue_remaining_minor") {
			t.Fatalf("estimate is not sold + held + remaining: %v", m)
		}
	}
	if after := e.seatFingerprint(t); after != before {
		t.Fatalf("suggest changed the session: %s -> %s", before, after)
	}
}

func TestS1e_AlgorithmsListAndSecondAlgorithmThroughTheSameRoute(t *testing.T) {
	e := newS1eEnv(t)
	status, out := e.call(t, http.MethodGet, e.path(e.orgA, e.seated, "algorithms"), e.owner, "")
	if status != http.StatusOK || out["currency"] != "CZK" {
		t.Fatalf("algorithms = %d %v", status, out)
	}
	ids := map[string]map[string]any{}
	for _, a := range out["algorithms"].([]any) {
		item := a.(map[string]any)
		ids[item["id"].(string)] = item
	}
	des, ok := ids["desirability"]
	if !ok || ids["test_flat_s1e"] == nil {
		t.Fatalf("algorithms = %v", ids)
	}
	var categories map[string]any
	for _, p := range des["params"].([]any) {
		if p.(map[string]any)["name"] == "categories" {
			categories = p.(map[string]any)
		}
	}
	if categories == nil || categories["min"].(float64) != 1 || categories["max"].(float64) != 15 {
		t.Fatalf("categories param = %v", categories)
	}

	status, out = e.call(t, http.MethodPost, e.path(e.orgA, e.seated, "suggest"), e.owner,
		`{"algorithm":"test_flat_s1e","params":{"price_max":1000}}`)
	if status != http.StatusOK || out["algorithm"] != "test_flat_s1e" {
		t.Fatalf("second algorithm = %d %v", status, out)
	}
	slots := out["layout"].(map[string]any)["slots"].([]any)
	if len(slots) != 1 || slots[0].(map[string]any)["key"] != "all" {
		t.Fatalf("slots = %v", slots)
	}
}

func TestS1e_Refusals(t *testing.T) {
	e := newS1eEnv(t)
	ok := `{"algorithm":"desirability","params":{"price_max":1000,"price_min":500},"annotations":` + s1eStage + `}`
	cases := []struct {
		name   string
		method string
		path   string
		token  string
		body   string
		status int
		code   string
	}{
		{"foreign session suggest", http.MethodPost, e.path(e.orgB, e.seated, "suggest"), e.foreignOwner, ok, 404, "session.not_found"},
		{"foreign session list", http.MethodGet, e.path(e.orgB, e.seated, "algorithms"), e.foreignOwner, "", 404, "session.not_found"},
		{"not a member", http.MethodPost, e.path(e.orgA, e.seated, "suggest"), e.foreignOwner, ok, 403, ""},
		{"agent lacks the permission", http.MethodPost, e.path(e.orgA, e.seated, "suggest"), e.agent, ok, 403, ""},
		{"no plan", http.MethodPost, e.path(e.orgA, e.gaSession, "suggest"), e.owner, ok, 422, "seat_layout.no_seating_plan"},
		{"unknown algorithm", http.MethodPost, e.path(e.orgA, e.seated, "suggest"), e.owner, `{"algorithm":"nope"}`, 400, "seat_layout.unknown_algorithm"},
		{"16 categories", http.MethodPost, e.path(e.orgA, e.seated, "suggest"), e.owner,
			`{"algorithm":"desirability","params":{"categories":16,"price_max":1000,"price_min":500},"annotations":` + s1eStage + `}`, 400, "seat_layout.invalid_param"},
		{"unknown param", http.MethodPost, e.path(e.orgA, e.seated, "suggest"), e.owner,
			`{"algorithm":"desirability","params":{"bogus":1}}`, 400, "seat_layout.invalid_param"},
		{"no stage", http.MethodPost, e.path(e.orgA, e.seated, "suggest"), e.owner,
			`{"algorithm":"desirability","params":{"price_max":1000,"price_min":500}}`, 422, "seat_layout.annotation_required"},
		{"bad weight", http.MethodPost, e.path(e.orgA, e.seated, "suggest"), e.owner,
			`{"algorithm":"desirability","annotations":{"section_weights":{"parter":3}}}`, 400, "seat_layout.invalid_annotation"},
		{"unknown body field", http.MethodPost, e.path(e.orgA, e.seated, "suggest"), e.owner,
			`{"algorithm":"desirability","apply":true}`, 400, "seat_layout.invalid_body"},
	}
	for _, c := range cases {
		status, out := e.call(t, c.method, c.path, c.token, c.body)
		if status != c.status {
			t.Fatalf("%s: status %d %v, want %d", c.name, status, out, c.status)
		}
		if c.code != "" {
			errObj, _ := out["error"].(map[string]any)
			if errObj["code"] != c.code {
				t.Fatalf("%s: code %v, want %s", c.name, errObj["code"], c.code)
			}
		}
	}
}
