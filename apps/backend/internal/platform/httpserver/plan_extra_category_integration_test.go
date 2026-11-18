//go:build integration

// plan_extra_category_integration_test.go — an extra price category of a
// session bound to a seating plan (planExtra of the event-bundle import) owns
// no place until the organizer paints plan seats into it. Before the fix the
// buyer saw it as a General Admission quantity card: the public feed listed
// it with available:null ("fall back to capacity"), the price quote priced it
// and the gateway seat list advertised the session's remaining places for it.
//
// Through the REAL router, against a live database:
//
//   - the seated bundle is imported with an organization API key;
//   - the public feed, the schema, GET_SEAT_LIST and GET_ALL_ACTIONS (its
//     "from" price) do not offer the category;
//   - checkout/quote, checkout/start and the gateway RESERVATION refuse it and
//     take no place;
//   - once a plan seat is painted into it the category appears (the schema in
//     the colour of ticket_tiers.color) and the seat sells.
//
// Run against a migrated database (the test cleans up its own rows).
package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/bil24compat"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/seating"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/apikeys"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/compatids"
)

type planExtraFixture struct {
	t         *testing.T
	ts        *httptest.Server
	pool      *pgxpool.Pool
	q         *gen.Queries
	orgID     uuid.UUID
	key       string
	venueID   uuid.UUID
	versionID uuid.UUID
	channelID uuid.UUID
	fid       int64
	gwToken   string
	feed      string
	feedID    uuid.UUID
	suffix    string
}

// planExtraGeometry: two Parter seats, one Balcony seat and a standing zone
// of 5 General Admission places — a hybrid hall.
func planExtraGeometry() seating.Geometry {
	return seating.Canonicalize(seating.Geometry{
		Canvas: seating.Canvas{Width: 100, Height: 100},
		Categories: []seating.Category{
			{Index: 1, Name: "Parter", Color: "#111111"},
			{Index: 2, Name: "Balcony", Color: "#222222"},
			{Index: 3, Name: "Standing", Color: "#333333", Kind: seating.KindGeneralAdmission, Capacity: 5},
		},
		Sections: []seating.Section{{Key: "s", Name: "Hall", Rows: []seating.Row{
			{Key: "a", Name: "A", Seats: []seating.Seat{
				{Key: "s|a|1", Number: "1", X: 10, Y: 10, Radius: 2, CategoryIndex: 1},
				{Key: "s|a|2", Number: "2", X: 20, Y: 10, Radius: 2, CategoryIndex: 1},
			}},
			{Key: "b", Name: "B", Seats: []seating.Seat{
				{Key: "s|b|1", Number: "1", X: 10, Y: 30, Radius: 2, CategoryIndex: 2},
			}},
		}}},
	})
}

func newPlanExtraFixture(t *testing.T) *planExtraFixture {
	t.Helper()
	base, _ := productionIntegrationServer(t)
	// The gateway is mounted only when the options ask for it (see
	// superadminProvisioning533Server), so the server is built again with it.
	srv := New(Options{
		Config:             base.cfg,
		Pool:               base.pool,
		PgxPool:            base.pgxPool,
		Auth:               base.stub,
		Verifier:           base.verifier,
		Bil24CompatEnabled: true,
		Bil24RequireToken:  true,
	})
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	pool := base.pgxPool
	f := &planExtraFixture{t: t, ts: ts, pool: pool, q: gen.New(pool), suffix: uuid.NewString()[:8]}
	ctx := context.Background()

	user, err := f.q.InsertUser(ctx, "plan-extra-"+uuid.NewString()+"@example.test", "x", "en")
	if err != nil {
		t.Fatalf("InsertUser: %v", err)
	}
	org, err := f.q.InsertOrganization(ctx, "Plan Extra "+f.suffix, "plan-extra-"+f.suffix, "EE", "en", 1200)
	if err != nil {
		t.Fatalf("InsertOrganization: %v", err)
	}
	f.orgID = org.ID
	t.Cleanup(func() { f.cleanup(user.ID) })

	addr := "1 Plan Street"
	venue, err := f.q.InsertArenaVenue(ctx, org.ID, nil, "Plan Extra Hall "+f.suffix, &addr, "Europe/Madrid", nil, nil, nil)
	if err != nil {
		t.Fatalf("InsertArenaVenue: %v", err)
	}
	f.venueID = venue.ID
	plan, err := f.q.InsertSeatingPlan(ctx, venue.ID, org.ID, "Main hall", "mixed", "private", "active", nil)
	if err != nil {
		t.Fatalf("InsertSeatingPlan: %v", err)
	}
	g := planExtraGeometry()
	raw, err := json.Marshal(g)
	if err != nil {
		t.Fatal(err)
	}
	sum, err := seating.Checksum(g)
	if err != nil {
		t.Fatal(err)
	}
	v, err := f.q.InsertSeatingPlanVersion(ctx, plan.ID, 1, raw, sum, nil, 3, 5)
	if err != nil {
		t.Fatalf("InsertSeatingPlanVersion: %v", err)
	}
	f.versionID = v.ID

	// A channel a WordPress site speaks the gateway through, and its feed.
	f.channelID = uuid.New()
	f.gwToken = "plan-extra-gw-" + f.suffix
	hash, err := bcrypt.GenerateFromPassword([]byte(f.gwToken), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	settings, _ := json.Marshal(map[string]string{"gateway_token_hash": string(hash)})
	if err := pool.QueryRow(ctx,
		`INSERT INTO sales_channels (id, org_id, name, settings) VALUES ($1, $2, $3, $4::jsonb) RETURNING display_number`,
		f.channelID, org.ID, "Plan Extra Channel "+f.suffix, string(settings)).Scan(&f.fid); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	f.feed = "plan-extra-" + uuid.NewString()
	tok, err := f.q.InsertFeedToken(ctx, f.feed, f.channelID, "plan extra")
	if err != nil {
		t.Fatalf("InsertFeedToken: %v", err)
	}
	f.feedID = tok.ID

	_, f.key, err = apikeys.Issue(ctx, apikeys.NewStoreFromQueries(f.q), apikeys.IssueInput{
		OrgID: org.ID, Name: "plan-extra-" + f.suffix, Scopes: []string{"event.read", "import.bil24_session"}, CreatedBy: user.ID,
	})
	if err != nil {
		t.Fatalf("apikeys.Issue: %v", err)
	}
	return f
}

func (f *planExtraFixture) cleanup(userID uuid.UUID) {
	c := context.Background()
	sessions := `(SELECT s.id FROM sessions s JOIN events e ON e.id = s.event_id WHERE e.org_id = $1)`
	for _, sql := range []string{
		`DELETE FROM reservation_ga_items WHERE reservation_id IN (SELECT id FROM reservations WHERE org_id = $1)`,
		`DELETE FROM reservation_seats WHERE reservation_id IN (SELECT id FROM reservations WHERE org_id = $1)`,
		`DELETE FROM gateway_sessions WHERE channel_id IN (SELECT id FROM sales_channels WHERE org_id = $1)`,
		`DELETE FROM checkout_sessions WHERE org_id = $1`,
		`DELETE FROM session_seats WHERE session_id IN ` + sessions,
		`DELETE FROM reservations WHERE org_id = $1`,
		`DELETE FROM inventory_ledger WHERE session_id IN ` + sessions,
		`DELETE FROM event_publications WHERE event_id IN (SELECT id FROM events WHERE org_id = $1)`,
		`DELETE FROM agent_feed_tokens WHERE sales_channel_id IN (SELECT id FROM sales_channels WHERE org_id = $1)`,
		`DELETE FROM ticket_tiers WHERE session_id IN ` + sessions,
		`DELETE FROM sessions WHERE id IN ` + sessions,
		`DELETE FROM events WHERE org_id = $1`,
		`UPDATE seating_plans SET current_version_id = NULL WHERE owner_org_id = $1`,
		`DELETE FROM seating_plan_versions WHERE seating_plan_id IN (SELECT id FROM seating_plans WHERE owner_org_id = $1)`,
		`DELETE FROM seating_plans WHERE owner_org_id = $1`,
		`DELETE FROM api_keys WHERE org_id = $1`,
		`DELETE FROM audit_events WHERE metadata->>'org_id' = $1::text`,
		`DELETE FROM outbox_events WHERE payload->>'org_id' = $1::text`,
		`DELETE FROM sales_channels WHERE org_id = $1`,
		`DELETE FROM venues WHERE org_id = $1`,
		`DELETE FROM organizations WHERE id = $1`,
	} {
		if _, err := f.pool.Exec(c, sql, f.orgID); err != nil {
			f.t.Logf("plan extra cleanup: %s: %v", sql, err)
		}
	}
	_, _ = f.pool.Exec(c, `DELETE FROM users WHERE id = $1`, userID)
}

// request sends one JSON request and decodes the JSON answer.
func (f *planExtraFixture) request(method, path, bearer string, body any) (int, map[string]any, string) {
	f.t.Helper()
	raw := ""
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			f.t.Fatal(err)
		}
		raw = string(b)
	}
	resp := integDoRequest(f.t, f.ts.Client(), method, f.ts.URL+path, bearer, raw)
	text := integReadBody(f.t, resp)
	out := map[string]any{}
	if strings.TrimSpace(text) != "" {
		_ = json.Unmarshal([]byte(text), &out)
	}
	return resp.StatusCode, out, text
}

func (f *planExtraFixture) gateway(cmd map[string]any) map[string]any {
	f.t.Helper()
	cmd["fid"] = f.fid
	cmd["token"] = f.gwToken
	cmd["locale"] = "en-US"
	st, out, text := f.request(http.MethodPost, "/compat/bil24/json", "", cmd)
	if st != http.StatusOK {
		f.t.Fatalf("%v: HTTP %d %s", cmd["command"], st, text)
	}
	return out
}

func planExtraErrorCode(body map[string]any) string {
	e, _ := body["error"].(map[string]any)
	s, _ := e["code"].(string)
	return s
}

func (f *planExtraFixture) feedTierNames(sessionID uuid.UUID) map[string]bool {
	f.t.Helper()
	var env struct {
		Event struct {
			Sessions []struct {
				ID    string `json:"id"`
				Tiers []struct {
					Name string `json:"name"`
				} `json:"tiers"`
			} `json:"sessions"`
		} `json:"event"`
	}
	st, _, text := f.request(http.MethodGet, "/v1/public/feeds/"+f.feed+"/events/"+f.eventOf(sessionID).String(), "", nil)
	if st != http.StatusOK || json.Unmarshal([]byte(text), &env) != nil {
		f.t.Fatalf("public feed: %d %s", st, text)
	}
	for _, s := range env.Event.Sessions {
		if s.ID == sessionID.String() {
			out := map[string]bool{}
			for _, tr := range s.Tiers {
				out[tr.Name] = true
			}
			return out
		}
	}
	f.t.Fatalf("session %s is not in the feed: %s", sessionID, text)
	return nil
}

func (f *planExtraFixture) eventOf(sessionID uuid.UUID) uuid.UUID {
	f.t.Helper()
	var id uuid.UUID
	if err := f.pool.QueryRow(context.Background(), `SELECT event_id FROM sessions WHERE id = $1`, sessionID).Scan(&id); err != nil {
		f.t.Fatal(err)
	}
	return id
}

func (f *planExtraFixture) seatListNames(actionEventID int64) map[string]bool {
	f.t.Helper()
	out := f.gateway(map[string]any{"command": "GET_SEAT_LIST", "actionEventId": actionEventID})
	if code, _ := out["resultCode"].(float64); code != 0 {
		f.t.Fatalf("GET_SEAT_LIST: %v", out)
	}
	names := map[string]bool{}
	list, _ := out["categoryList"].([]any)
	for _, c := range list {
		m, _ := c.(map[string]any)
		n, _ := m["categoryPriceName"].(string)
		names[n] = true
	}
	return names
}

type planExtraSchema struct {
	Extra []struct {
		Name   string `json:"name"`
		TierID string `json:"tier_id"`
		Color  string `json:"color"`
	} `json:"extra_categories"`
}

func (f *planExtraFixture) schema(sessionID uuid.UUID) planExtraSchema {
	f.t.Helper()
	var s planExtraSchema
	st, _, text := f.request(http.MethodGet, "/v1/event-sessions/"+sessionID.String()+"/schema", "", nil)
	if st != http.StatusOK || json.Unmarshal([]byte(text), &s) != nil {
		f.t.Fatalf("schema: %d %s", st, text)
	}
	return s
}

func (f *planExtraFixture) takenPlaces(sessionID uuid.UUID) int {
	f.t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM session_seats WHERE session_id = $1 AND status <> 'available'`, sessionID).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

func TestPlanExtraCategory_NotSoldUntilSeatsArePainted(t *testing.T) {
	f := newPlanExtraFixture(t)
	ctx := context.Background()

	// ── the seated bundle with two extra price categories ───────────────────
	three := 3
	bundle := bil24compat.ImportSessionRequest{
		Source:      bil24compat.SourceArena,
		ExternalRef: "plan-extra-" + f.suffix,
		Action:      bil24compat.ImportSessionAction{ActionName: "Plan Extra " + f.suffix},
		ActionEvent: bil24compat.ImportSessionActionEvent{
			Day: time.Now().UTC().Add(60 * 24 * time.Hour).Format("02.01.2006"), Time: "19:00", Currency: "EUR",
			ArenaSeatingPlanVersionID: f.versionID.String(),
		},
		Venue: bil24compat.ImportSessionVenue{ArenaVenueID: f.venueID.String()},
		CategoryList: []bil24compat.ImportSessionCategory{
			{CategoryPriceName: "Parter", Price: 40},
			{CategoryPriceName: "Balcony", Price: 25},
			{CategoryPriceName: "Standing", Price: 15, PlanCategoryIndex: &three, Availability: 5},
			{CategoryPriceName: "VIP", Price: 90, PlanExtra: true, Color: "#00AA00"},
			{CategoryPriceName: "Student", Price: 5, PlanExtra: true},
		},
	}
	st, out, text := f.request(http.MethodPost, "/v1/organizations/"+f.orgID.String()+"/imports/event-bundle", f.key, bundle)
	if st != http.StatusOK {
		t.Fatalf("import: %d %s", st, text)
	}
	sid, _ := out["session_id"].(string)
	sessionID, err := uuid.Parse(sid)
	if err != nil {
		t.Fatalf("import answer: %s", text)
	}
	var vip uuid.UUID
	if err := f.pool.QueryRow(ctx, `SELECT id FROM ticket_tiers WHERE session_id = $1 AND name = 'VIP' AND deleted_at IS NULL`, sessionID).Scan(&vip); err != nil {
		t.Fatalf("no VIP tier (%v) in %s", err, text)
	}
	eventID := f.eventOf(sessionID)
	for _, sql := range []string{
		`UPDATE events SET status = 'published' WHERE id = $1`,
		`UPDATE sessions SET status = 'scheduled' WHERE event_id = $1`,
	} {
		if _, err := f.pool.Exec(ctx, sql, eventID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.q.PublishEvent(ctx, eventID, f.feedID, nil); err != nil {
		t.Fatalf("PublishEvent: %v", err)
	}
	var places int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM session_seats WHERE session_id = $1 AND tier_id = $2`, sessionID, vip).Scan(&places); err != nil || places != 0 {
		t.Fatalf("VIP owns %d places (%v), want 0", places, err)
	}
	actionEventID, err := compatids.Ensure(ctx, f.pool, compatids.KindActionEvent, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	vipWire, err := compatids.Ensure(ctx, f.pool, compatids.KindCategoryPrice, vip)
	if err != nil {
		t.Fatal(err)
	}
	vipLine := map[string]any{"session_id": sessionID.String(), "ga_items": []map[string]any{{"tier_id": vip.String(), "quantity": 1}}}

	t.Run("the public feed does not offer it", func(t *testing.T) {
		names := f.feedTierNames(sessionID)
		if names["VIP"] || names["Student"] || !names["Parter"] || !names["Standing"] {
			t.Fatalf("feed tiers = %v, want the plan categories without VIP and Student", names)
		}
	})

	t.Run("the schema has no category for it", func(t *testing.T) {
		for _, e := range f.schema(sessionID).Extra {
			if e.TierID == vip.String() {
				t.Fatalf("schema extra_categories lists VIP before it has a seat: %+v", e)
			}
		}
	})

	t.Run("checkout/quote and checkout/start refuse it", func(t *testing.T) {
		st, body, text := f.request(http.MethodPost, "/v1/public/feeds/"+f.feed+"/checkout/quote", "", vipLine)
		if st != http.StatusConflict || planExtraErrorCode(body) != "tier.not_on_sale" {
			t.Fatalf("quote: %d %s, want 409 tier.not_on_sale", st, text)
		}
		start := map[string]any{
			"session_id": sessionID.String(),
			"ga_items":   []map[string]any{{"tier_id": vip.String(), "quantity": 1}},
			"buyer":      map[string]any{"email": "plan-extra-buyer@example.test"},
		}
		st, body, text = f.request(http.MethodPost, "/v1/public/feeds/"+f.feed+"/checkout/start", "", start)
		if st != http.StatusConflict || planExtraErrorCode(body) != "tier.not_on_sale" {
			t.Fatalf("checkout/start: %d %s, want 409 tier.not_on_sale", st, text)
		}
		if n := f.takenPlaces(sessionID); n != 0 {
			t.Fatalf("taken places = %d, want 0", n)
		}
	})

	t.Run("the gateway does not list it and refuses to reserve it", func(t *testing.T) {
		names := f.seatListNames(actionEventID)
		if names["VIP"] || names["Student"] || !names["Parter"] || !names["Standing"] {
			t.Fatalf("GET_SEAT_LIST categories = %v, want the plan categories without VIP and Student", names)
		}
		catalog := f.gateway(map[string]any{"command": "GET_ALL_ACTIONS"})
		found := false
		actions, _ := catalog["actionList"].([]any)
		for _, a := range actions {
			am, _ := a.(map[string]any)
			evs, _ := am["actionEventList"].([]any)
			for _, e := range evs {
				em, _ := e.(map[string]any)
				if em["arenaSessionId"] != sessionID.String() {
					continue
				}
				found = true
				if mp, _ := em["minPrice"].(float64); mp != 15 {
					t.Errorf("GET_ALL_ACTIONS minPrice = %v, want 15 (Student at 5 has no place)", em["minPrice"])
				}
				if strings.Contains(string(mustJSON(em["categoryLimitList"])), "VIP") {
					t.Errorf("GET_ALL_ACTIONS lists VIP as a category: %v", em["categoryLimitList"])
				}
			}
		}
		if !found {
			t.Fatalf("GET_ALL_ACTIONS has no entry for the session: %v", catalog)
		}

		user := f.gateway(map[string]any{"command": "CREATE_USER", "email": "plan-extra-gw@example.test", "firstName": "Plan", "lastName": "Extra"})
		reserve := f.gateway(map[string]any{
			"command": "RESERVATION", "type": "RESERVE", "userId": user["userId"], "sessionId": user["sessionId"],
			"actionEventId": actionEventID,
			"categoryList":  []any{map[string]any{"categoryPriceId": vipWire, "quantity": 1, "tariffPlanId": nil}},
		})
		if code, _ := reserve["resultCode"].(float64); code == 0 {
			t.Fatalf("RESERVATION of VIP answered 0: %v", reserve)
		}
		if n := f.takenPlaces(sessionID); n != 0 {
			t.Fatalf("taken places after the refused reserve = %d, want 0", n)
		}
	})

	// ── the organizer paints a Parter seat into VIP ──────────────────────────
	if _, err := f.pool.Exec(ctx, `UPDATE session_seats SET tier_id = $1 WHERE session_id = $2 AND seat_key = 's|a|2'`, vip, sessionID); err != nil {
		t.Fatal(err)
	}

	t.Run("a painted VIP seat is offered in its own colour", func(t *testing.T) {
		names := f.feedTierNames(sessionID)
		if !names["VIP"] || names["Student"] {
			t.Fatalf("feed tiers after painting = %v, want VIP and still no Student", names)
		}
		s := f.schema(sessionID)
		ok := false
		for _, e := range s.Extra {
			if e.TierID == vip.String() {
				ok = e.Color == "#00aa00"
			}
		}
		if !ok {
			t.Fatalf("schema extra_categories = %+v, want VIP in #00aa00 (ticket_tiers.color)", s.Extra)
		}
		if names := f.seatListNames(actionEventID); !names["VIP"] || names["Student"] {
			t.Fatalf("GET_SEAT_LIST categories after painting = %v", names)
		}
	})

	t.Run("the painted seat sells", func(t *testing.T) {
		st, _, text := f.request(http.MethodPost, "/v1/public/feeds/"+f.feed+"/checkout/quote", "",
			map[string]any{"session_id": sessionID.String(), "seats": []string{"s|a|2"}})
		if st != http.StatusOK || !strings.Contains(text, "9000") {
			t.Fatalf("quote of the VIP seat: %d %s, want 200 at 9000", st, text)
		}
		var seatID int64
		if err := f.pool.QueryRow(ctx, `SELECT system_seat_id FROM session_seats WHERE session_id = $1 AND seat_key = 's|a|2'`, sessionID).Scan(&seatID); err != nil {
			t.Fatal(err)
		}
		user := f.gateway(map[string]any{"command": "CREATE_USER", "email": "plan-extra-gw2@example.test", "firstName": "Plan", "lastName": "Seat"})
		reserve := f.gateway(map[string]any{
			"command": "RESERVATION", "type": "RESERVE", "userId": user["userId"], "sessionId": user["sessionId"],
			"actionEventId": actionEventID,
			"seatList":      []any{map[string]any{"seatId": seatID}},
		})
		if code, _ := reserve["resultCode"].(float64); code != 0 {
			t.Fatalf("RESERVATION of the VIP seat: %v", reserve)
		}
		if n := f.takenPlaces(sessionID); n != 1 {
			t.Fatalf("taken places = %d, want the one VIP seat", n)
		}
	})
}
