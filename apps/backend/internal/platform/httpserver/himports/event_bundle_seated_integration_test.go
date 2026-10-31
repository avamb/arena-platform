//go:build integration

package himports

// event_bundle_seated_integration_test.go — task S1b against a live database:
// a source=arena bundle with actionEvent.arenaSeatingPlanVersionId creates a
// session with every place of the plan, the buyer and admin seat surfaces
// show them, a dry run writes nothing, a repeat import never touches a place,
// and the event-bundle read round-trips the seated session.

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/bil24compat"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/seating"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hseating"
)

// seatedFixture is a bundle525Fixture plus a venue of the organization with
// one seating plan version (seatedTestGeometry: 3 seats in two seated
// categories, a GA zone of 50, a legend entry without seats).
type seatedFixture struct {
	*bundle525Fixture
	venueID   uuid.UUID
	versionID uuid.UUID
}

func newSeatedFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) *seatedFixture {
	t.Helper()
	f := newBundle525Fixture(t, ctx, pool)
	sf := &seatedFixture{bundle525Fixture: f}
	sf.venueID, sf.versionID = insertSeatedPlan(t, ctx, pool, f.orgID, f.venueName, map[int]string{2: "Galerie"})
	return sf
}

// insertSeatedPlan creates a venue and one plan version of seatedTestGeometry.
func insertSeatedPlan(t *testing.T, ctx context.Context, pool *pgxpool.Pool, orgID uuid.UUID, venueName string, overrides map[int]string) (uuid.UUID, uuid.UUID) {
	t.Helper()
	q := gen.New(pool)
	addr := "1 Plan Street"
	venue, err := q.InsertArenaVenue(ctx, orgID, nil, venueName, &addr, "Europe/Madrid", nil, nil, nil)
	if err != nil {
		t.Fatalf("insert venue: %v", err)
	}
	plan, err := q.InsertSeatingPlan(ctx, venue.ID, orgID, "Main hall", "mixed", "private", "active", nil)
	if err != nil {
		t.Fatalf("insert plan: %v", err)
	}
	if len(overrides) > 0 {
		byKey := map[string]string{}
		for k, v := range overrides {
			byKey[strconv.Itoa(k)] = v
		}
		raw, _ := json.Marshal(byKey)
		if _, err := pool.Exec(ctx, `UPDATE seating_plans SET category_name_overrides = $2 WHERE id = $1`, plan.ID, raw); err != nil {
			t.Fatalf("set overrides: %v", err)
		}
	}
	g := seatedTestGeometry()
	raw, err := json.Marshal(g)
	if err != nil {
		t.Fatal(err)
	}
	sum, err := seating.Checksum(g)
	if err != nil {
		t.Fatal(err)
	}
	v, err := q.InsertSeatingPlanVersion(ctx, plan.ID, 1, raw, sum, nil, 3, 50)
	if err != nil {
		t.Fatalf("insert plan version: %v", err)
	}
	return venue.ID, v.ID
}

// cleanup runs the base cleanup, then removes the plans (their versions are
// referenced by sessions until those are gone) and what the base cleanup
// could not delete because a plan still pointed at the venue.
func (sf *seatedFixture) cleanup() {
	sf.bundle525Fixture.cleanup()
	ctx := context.Background()
	for _, sql := range []string{
		`UPDATE seating_plans SET current_version_id = NULL WHERE owner_org_id = $1`,
		`DELETE FROM seating_plan_versions WHERE seating_plan_id IN (SELECT id FROM seating_plans WHERE owner_org_id = $1)`,
		`DELETE FROM seating_plans WHERE owner_org_id = $1`,
		`DELETE FROM venues WHERE org_id = $1`,
		`DELETE FROM organizations WHERE id = $1`,
	} {
		if _, err := sf.pool.Exec(ctx, sql, sf.orgID); err != nil {
			sf.t.Logf("seatedFixture cleanup: %v", err)
		}
	}
}

// seatedPayload: Parter by name, the balcony by its display-name override,
// the GA zone by index under another name (availability 0 = keep the plan's
// 50).
func (sf *seatedFixture) seatedPayload() bil24compat.ImportSessionRequest {
	body := sf.payload()
	body.Venue = bil24compat.ImportSessionVenue{ArenaVenueID: sf.venueID.String()}
	body.ActionEvent.ArenaSeatingPlanVersionID = sf.versionID.String()
	three := 3
	body.CategoryList = []bil24compat.ImportSessionCategory{
		{CategoryPriceName: "Parter", Price: 40, Availability: 999},
		{CategoryPriceName: "Galerie", Price: 25},
		{CategoryPriceName: "Fans", Price: 15, PlanCategoryIndex: &three},
	}
	return body
}

type seatRowSnapshot struct {
	Key, Kind, Status string
	Tier              uuid.UUID // uuid.Nil for a seat without a tier
	SystemID          int64
}

func sessionSeatSnapshot(ctx context.Context, t *testing.T, pool *pgxpool.Pool, sessionID uuid.UUID) map[string]seatRowSnapshot {
	t.Helper()
	rows, err := gen.New(pool).ListSessionSeats(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	out := make(map[string]seatRowSnapshot, len(rows))
	for _, r := range rows {
		s := seatRowSnapshot{Key: r.SeatKey, Kind: r.Kind, Status: r.Status, SystemID: r.SystemSeatID}
		if r.TierID != nil {
			s.Tier = *r.TierID
		}
		out[r.SeatKey] = s
	}
	return out
}

func tierByName(ctx context.Context, t *testing.T, pool *pgxpool.Pool, sessionID uuid.UUID) map[string]gen.TicketTierRow {
	t.Helper()
	tiers, err := gen.New(pool).ListTicketTiersBySession(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]gen.TicketTierRow{}
	for _, tt := range tiers {
		out[tt.Name] = tt
	}
	return out
}

func callSeatingHandler(t *testing.T, sessionID uuid.UUID, serve func(http.ResponseWriter, *http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/event-sessions/"+sessionID.String()+"/x", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	rec := httptest.NewRecorder()
	serve(rec, req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx)))
	return rec
}

func TestEventBundleSeated_CreatesEveryPlaceOfThePlan(t *testing.T) {
	pool := import517Pool(t)
	ctx := context.Background()
	sf := newSeatedFixture(t, ctx, pool)
	defer sf.cleanup()
	h := newBundle525Handler(t, pool)

	rec, out := sf.call(h, sf.seatedPayload())
	if rec.Code != http.StatusOK || !out.Created {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	if out.SeatingPlanVersionID == nil || *out.SeatingPlanVersionID != sf.versionID || out.SeatsMaterialized != 53 {
		t.Fatalf("response seating: version %v places %d, want %s and 53", out.SeatingPlanVersionID, out.SeatsMaterialized, sf.versionID)
	}

	var mode string
	var version *uuid.UUID
	var capacity int32
	if err := pool.QueryRow(ctx, `SELECT admission_mode, seating_plan_version_id, capacity_total FROM sessions WHERE id = $1`,
		out.SessionID).Scan(&mode, &version, &capacity); err != nil {
		t.Fatal(err)
	}
	if mode != "hybrid" || version == nil || *version != sf.versionID || capacity != 53 {
		t.Fatalf("session: mode %s version %v capacity %d", mode, version, capacity)
	}
	var ledger int32
	if err := pool.QueryRow(ctx, `SELECT capacity_total FROM inventory_ledger WHERE session_id = $1 AND tier_id IS NULL`, out.SessionID).Scan(&ledger); err != nil || ledger != 53 {
		t.Fatalf("session ledger: %d %v", ledger, err)
	}
	var locked bool
	if err := pool.QueryRow(ctx, `SELECT locked_at IS NOT NULL FROM seating_plan_versions WHERE id = $1`, sf.versionID).Scan(&locked); err != nil || !locked {
		t.Fatalf("the bound version must be locked: %v %v", locked, err)
	}

	tiers := tierByName(ctx, t, pool, out.SessionID)
	seats := sessionSeatSnapshot(ctx, t, pool, out.SessionID)
	if len(seats) != 53 {
		t.Fatalf("session_seats = %d, want 53", len(seats))
	}
	for key, want := range map[string]string{"s|a|1": "Parter", "s|a|2": "Parter", "s|b|1": "Galerie"} {
		s := seats[key]
		if s.Kind != "seat" || s.Tier != tiers[want].ID {
			t.Errorf("seat %s: %+v, want the %s tier", key, s, want)
		}
	}
	zone := 0
	for _, s := range seats {
		if s.Kind == "ga_unit" && strings.HasPrefix(s.Key, "ga|c3|") && s.Tier == tiers["Fans"].ID {
			zone++
		}
	}
	if zone != 50 {
		t.Fatalf("GA zone places = %d, want 50", zone)
	}
	if c := tiers["Parter"].Capacity; c != nil {
		t.Errorf("a seated tier carries no quantity, got %d (availability 999 is ignored)", *c)
	}
	if c := tiers["Fans"].Capacity; c == nil || *c != 50 {
		t.Errorf("the GA zone tier owns the plan's 50 places, got %v", c)
	}

	// The organizer's seat list and the buyer's schema both show the places.
	sh := hseating.New(gen.New(pool), pool, nil, slog.Default())
	adminRec := callSeatingHandler(t, out.SessionID, sh.HandleListSessionSeatsAdmin)
	var admin hseating.AdminSeatsResponse
	if adminRec.Code != http.StatusOK || json.Unmarshal(adminRec.Body.Bytes(), &admin) != nil || len(admin.Seats) != 53 {
		t.Fatalf("seats/admin: %d, %d seats; %s", adminRec.Code, len(admin.Seats), adminRec.Body.String())
	}
	if _, err := pool.Exec(ctx, `UPDATE events SET status = 'published' WHERE id = $1`, out.EventID); err != nil {
		t.Fatal(err)
	}
	schemaRec := callSeatingHandler(t, out.SessionID, sh.HandleGetPublicSessionSchema)
	if schemaRec.Code != http.StatusOK || !strings.Contains(schemaRec.Body.String(), `"s|b|1"`) {
		t.Fatalf("public schema: %d %s", schemaRec.Code, schemaRec.Body.String())
	}
}

func TestEventBundleSeated_DryRunChecksAndWritesNothing(t *testing.T) {
	pool := import517Pool(t)
	ctx := context.Background()
	sf := newSeatedFixture(t, ctx, pool)
	defer sf.cleanup()
	h := newBundle525Handler(t, pool)

	counts := func() string {
		var s string
		if err := pool.QueryRow(ctx, `SELECT concat_ws('/',
		    (SELECT count(*) FROM sessions s JOIN events e ON e.id = s.event_id WHERE e.org_id = $1),
		    (SELECT count(*) FROM session_seats ss JOIN sessions s ON s.id = ss.session_id JOIN events e ON e.id = s.event_id WHERE e.org_id = $1),
		    (SELECT count(*) FROM events WHERE org_id = $1),
		    (SELECT count(*) FROM seating_plan_versions WHERE id = $2 AND locked_at IS NOT NULL),
		    (SELECT count(*) FROM compatibility_id_map m JOIN venues v ON v.id = m.platform_id WHERE v.org_id = $1))`,
			sf.orgID, sf.versionID).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	before := counts()
	dry := dryRun(t, sf.bundle525Fixture, h, sf.seatedPayload())
	if !dry.Valid || len(dry.Errors) != 0 {
		t.Fatalf("a valid seated bundle: %+v", dry.Errors)
	}
	if after := counts(); after != before {
		t.Fatalf("the dry run wrote something: %s -> %s", before, after)
	}

	// Every category problem at once, each with its field.
	bad := sf.seatedPayload()
	nine := 9
	bad.CategoryList[0].CategoryPriceName = "VIP"
	bad.CategoryList[2].PlanCategoryIndex = &nine
	dry = dryRun(t, sf.bundle525Fixture, h, bad)
	fields := map[string]string{}
	for _, e := range dry.Errors {
		fields[e.Field] = e.Code
	}
	if dry.Valid || fields["/categoryList/0/categoryPriceName"] != "import.seating_category_unmatched" ||
		fields["/categoryList/2/planCategoryIndex"] != "import.seating_category_unmatched" {
		t.Fatalf("dry run errors: %+v", dry.Errors)
	}

	// A version of another organization reads like an unknown one.
	other := newBundle525Fixture(t, ctx, pool)
	_, foreign := insertSeatedPlan(t, ctx, pool, other.orgID, other.venueName, nil)
	defer (&seatedFixture{bundle525Fixture: other}).cleanup()
	bad = sf.seatedPayload()
	bad.ActionEvent.ArenaSeatingPlanVersionID = foreign.String()
	dry = dryRun(t, sf.bundle525Fixture, h, bad)
	if dry.Valid || len(dry.Errors) == 0 || dry.Errors[0].Code != "import.invalid_seating_plan_version" ||
		dry.Errors[0].Field != seatingVersionField {
		t.Fatalf("foreign version: %+v", dry.Errors)
	}
	if after := counts(); after != before {
		t.Fatalf("the refused dry runs wrote something: %s -> %s", before, after)
	}
}

func TestEventBundleSeated_RefusalsAnswerWithTheirCodes(t *testing.T) {
	pool := import517Pool(t)
	ctx := context.Background()
	sf := newSeatedFixture(t, ctx, pool)
	defer sf.cleanup()
	h := newBundle525Handler(t, pool)

	// A plan drawn for another venue of the organization.
	otherVenue, _ := insertSeatedPlan(t, ctx, pool, sf.orgID, sf.venueName+" B", nil)
	// A plan of another organization.
	other := newBundle525Fixture(t, ctx, pool)
	_, foreign := insertSeatedPlan(t, ctx, pool, other.orgID, other.venueName, nil)
	defer (&seatedFixture{bundle525Fixture: other}).cleanup()
	cases := []struct {
		name   string
		mutate func(*bil24compat.ImportSessionRequest)
		status int
		code   string
	}{
		{"unknown version", func(b *bil24compat.ImportSessionRequest) {
			b.ActionEvent.ArenaSeatingPlanVersionID = uuid.NewString()
		}, 422, "import.invalid_seating_plan_version"},
		{"a version of another organization", func(b *bil24compat.ImportSessionRequest) {
			b.ActionEvent.ArenaSeatingPlanVersionID = foreign.String()
		}, 422, "import.invalid_seating_plan_version"},
		{"venue mismatch", func(b *bil24compat.ImportSessionRequest) {
			b.Venue.ArenaVenueID = otherVenue.String()
		}, 422, "import.seating_plan_venue_mismatch"},
		{"a plan category nobody prices", func(b *bil24compat.ImportSessionRequest) {
			b.CategoryList = b.CategoryList[:2]
		}, 422, "import.seating_category_missing"},
		{"a chain through a seated category", func(b *bil24compat.ImportSessionRequest) {
			two := 2
			b.CategoryList[0].NextCategoryIndex = &two
		}, 422, "import.seated_category_chain"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := sf.seatedPayload()
			tc.mutate(&body)
			rec, _ := sf.call(h, body)
			if rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.code) {
				t.Fatalf("%d %s, want %d %s", rec.Code, rec.Body.String(), tc.status, tc.code)
			}
		})
	}
	var sessions int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM sessions s JOIN events e ON e.id = s.event_id WHERE e.org_id = $1`,
		sf.orgID).Scan(&sessions); err != nil || sessions != 0 {
		t.Fatalf("a refused bundle left %d sessions (%v)", sessions, err)
	}
}

func TestEventBundleSeated_RepeatImportNeverTouchesPlaces(t *testing.T) {
	pool := import517Pool(t)
	ctx := context.Background()
	sf := newSeatedFixture(t, ctx, pool)
	defer sf.cleanup()
	h := newBundle525Handler(t, pool)

	rec, created := sf.call(h, sf.seatedPayload())
	if rec.Code != http.StatusOK {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	before := sessionSeatSnapshot(ctx, t, pool, created.SessionID)

	// The same bundle again (by externalRef), new prices, a new GA quantity,
	// and no version at all: places stay, prices move, the zone resizes.
	again := sf.seatedPayload()
	again.ActionEvent.ArenaSeatingPlanVersionID = ""
	again.CategoryList[0].Price = 45
	again.CategoryList[2].Availability = 60
	rec, out := sf.call(h, again)
	if rec.Code != http.StatusOK || out.Created || out.SessionID != created.SessionID {
		t.Fatalf("repeat: %d %s", rec.Code, rec.Body.String())
	}
	if out.SeatingPlanVersionID == nil || *out.SeatingPlanVersionID != sf.versionID {
		t.Fatalf("the repeat answers the session's own version: %v", out.SeatingPlanVersionID)
	}
	after := sessionSeatSnapshot(ctx, t, pool, created.SessionID)
	for key, s := range before {
		if after[key] != s {
			t.Fatalf("place %s changed: %+v -> %+v", key, s, after[key])
		}
	}
	if len(after) != 63 {
		t.Fatalf("places after the zone resize = %d, want 63 (53 + 10 new GA places)", len(after))
	}
	tiers := tierByName(ctx, t, pool, created.SessionID)
	if tiers["Parter"].PriceAmount != 4500 {
		t.Errorf("seated category price = %d, want 4500", tiers["Parter"].PriceAmount)
	}

	// Another version for the existing session is a conflict and writes nothing.
	_, other := insertSeatedPlan(t, ctx, pool, sf.orgID, sf.venueName+" C", nil)
	conflict := sf.seatedPayload()
	conflict.ActionEvent.ArenaSeatingPlanVersionID = other.String()
	if rec, _ := sf.call(h, conflict); rec.Code != http.StatusConflict ||
		!strings.Contains(rec.Body.String(), "import.seating_plan_version_conflict") {
		t.Fatalf("other version: %d %s", rec.Code, rec.Body.String())
	}
	if final := sessionSeatSnapshot(ctx, t, pool, created.SessionID); len(final) != len(after) {
		t.Fatalf("the refused repeat changed the places: %d -> %d", len(after), len(final))
	}

	// A general-admission session cannot be given a plan by a bundle either.
	ga := sf.payload()
	ga.ExternalRef = sf.externalRef + ":ga"
	rec, gaOut := sf.call(h, ga)
	if rec.Code != http.StatusOK {
		t.Fatalf("GA create: %d %s", rec.Code, rec.Body.String())
	}
	ga.ActionEvent.ArenaSeatingPlanVersionID = sf.versionID.String()
	if rec, _ := sf.call(h, ga); rec.Code != http.StatusConflict {
		t.Fatalf("binding a plan to a GA session by a bundle: %d %s", rec.Code, rec.Body.String())
	}
	_ = gaOut
}

// TestEventBundleSeated_GAZoneQuantity: the plan draws the GA zone with 50
// places. A bundle that names no quantity (availability 0) keeps the plan's
// 50; one that names 70 gives the zone category 70 places. Either way the
// session capacity and its inventory ledger are the 3 plan seats plus the
// zone's places, and every GA place carries the zone category's tier.
func TestEventBundleSeated_GAZoneQuantity(t *testing.T) {
	pool := import517Pool(t)
	ctx := context.Background()
	h := newBundle525Handler(t, pool)
	for _, tc := range []struct {
		name         string
		availability int32
		wantZone     int32
	}{
		{"availability 0 keeps the plan's 50", 0, 50},
		{"availability 70 sets the zone to 70", 70, 70},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sf := newSeatedFixture(t, ctx, pool)
			defer sf.cleanup()
			body := sf.seatedPayload()
			body.CategoryList[2].Availability = tc.availability
			rec, out := sf.call(h, body)
			if rec.Code != http.StatusOK || !out.Created {
				t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
			}
			wantTotal := 3 + tc.wantZone

			fans := tierByName(ctx, t, pool, out.SessionID)["Fans"]
			if fans.Capacity == nil || *fans.Capacity != tc.wantZone {
				t.Errorf("zone tier capacity = %v, want %d", fans.Capacity, tc.wantZone)
			}
			var units int32
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM session_seats
				WHERE session_id = $1 AND kind = 'ga_unit' AND tier_id = $2`,
				out.SessionID, fans.ID).Scan(&units); err != nil {
				t.Fatal(err)
			}
			if units != tc.wantZone {
				t.Errorf("ga_unit places of the zone tier = %d, want %d", units, tc.wantZone)
			}
			var orphan int32
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM session_seats
				WHERE session_id = $1 AND kind = 'ga_unit' AND tier_id IS DISTINCT FROM $2`,
				out.SessionID, fans.ID).Scan(&orphan); err != nil {
				t.Fatal(err)
			}
			if orphan != 0 {
				t.Errorf("%d GA places carry no or another tier", orphan)
			}
			var capacity, ledger int32
			if err := pool.QueryRow(ctx, `SELECT capacity_total FROM sessions WHERE id = $1`,
				out.SessionID).Scan(&capacity); err != nil {
				t.Fatal(err)
			}
			if err := pool.QueryRow(ctx, `SELECT capacity_total FROM inventory_ledger
				WHERE session_id = $1 AND tier_id IS NULL`, out.SessionID).Scan(&ledger); err != nil {
				t.Fatal(err)
			}
			if capacity != wantTotal || ledger != wantTotal {
				t.Errorf("session capacity %d, ledger %d, want %d both", capacity, ledger, wantTotal)
			}
			if out.SeatsMaterialized != int(wantTotal) {
				t.Errorf("response places = %d, want %d", out.SeatsMaterialized, wantTotal)
			}
		})
	}
}

func TestEventBundleSeated_ReadRoundTripsAndCopies(t *testing.T) {
	pool := import517Pool(t)
	ctx := context.Background()
	sf := newSeatedFixture(t, ctx, pool)
	defer sf.cleanup()
	h := newBundle525Handler(t, pool)

	rec, created := sf.call(h, sf.seatedPayload())
	if rec.Code != http.StatusOK {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}

	first, firstRaw := sf.readBundles(h, created.EventID)
	if len(first.Bundles) != 1 || len(first.Skipped) != 0 {
		t.Fatalf("read: %s", firstRaw)
	}
	b := first.Bundles[0]
	if b.ActionEvent.ArenaSeatingPlanVersionID != sf.versionID.String() {
		t.Fatalf("the read names version %q, want %s", b.ActionEvent.ArenaSeatingPlanVersionID, sf.versionID)
	}
	byName := map[string]bil24compat.ImportSessionCategory{}
	for _, c := range b.CategoryList {
		byName[c.CategoryPriceName] = c
	}
	for name, want := range map[string]struct {
		index     int
		placement bool
	}{"Parter": {1, true}, "Galerie": {2, true}, "Fans": {3, false}} {
		c := byName[name]
		if c.PlanCategoryIndex == nil || *c.PlanCategoryIndex != want.index || c.Placement != want.placement {
			t.Errorf("category %s: index %v placement %v, want %d %v", name, c.PlanCategoryIndex, c.Placement, want.index, want.placement)
		}
	}

	if dry := dryRun(t, sf.bundle525Fixture, h, b); !dry.Valid || len(dry.Errors) != 0 {
		t.Fatalf("dry run of the read bundle: %+v", dry.Errors)
	}
	before := catalogSnapshot(ctx, t, pool, sf.orgID)
	seatsBefore := sessionSeatSnapshot(ctx, t, pool, created.SessionID)
	sf.reimport(h, first)
	if after := catalogSnapshot(ctx, t, pool, sf.orgID); after != before {
		t.Fatalf("re-importing the read bundle changed the rows:\nbefore %s\nafter  %s", before, after)
	}
	seatsAfter := sessionSeatSnapshot(ctx, t, pool, created.SessionID)
	for key, s := range seatsBefore {
		if seatsAfter[key] != s {
			t.Fatalf("place %s changed on the round trip: %+v -> %+v", key, s, seatsAfter[key])
		}
	}
	if _, secondRaw := sf.readBundles(h, created.EventID); secondRaw != firstRaw {
		t.Fatalf("second read differs:\nfirst  %s\nsecond %s", firstRaw, secondRaw)
	}

	// A copy: the same bundle as a new date of the event (no session, no tier
	// ids) creates the same places from the plan, matched by planCategoryIndex.
	cp := b
	cp.ExternalRef = sf.externalRef + ":copy"
	cp.ActionEvent.ArenaSessionID = ""
	cp.ActionEvent.Day = "27.04.2026"
	cp.CategoryList = append([]bil24compat.ImportSessionCategory(nil), b.CategoryList...)
	for i := range cp.CategoryList {
		cp.CategoryList[i].ArenaTierID = ""
		cp.CategoryList[i].CategoryPriceName = "Copy " + cp.CategoryList[i].CategoryPriceName
	}
	rec, copied := sf.call(h, cp)
	if rec.Code != http.StatusOK || !copied.Created || copied.SeatsMaterialized != 53 {
		t.Fatalf("copy: %d %s", rec.Code, rec.Body.String())
	}
}

// TestEventBundleSeated_ExtraPriceCategories: a plan drawn with fewer
// categories than the organizer sells. planExtra entries become categories
// of the new session with no place of their own (availability ignored) and
// their colour; the hall keeps exactly the plan's places and admission mode.
// A repeat import that names them without a quantity (what the event-bundle
// read sends for a category nobody painted yet) still gives them no General
// Admission place.
func TestEventBundleSeated_ExtraPriceCategories(t *testing.T) {
	pool := import517Pool(t)
	ctx := context.Background()
	sf := newSeatedFixture(t, ctx, pool)
	defer sf.cleanup()
	h := newBundle525Handler(t, pool)

	body := sf.seatedPayload()
	body.CategoryList[0].Color = "#AA0000"
	body.CategoryList = append(body.CategoryList,
		bil24compat.ImportSessionCategory{CategoryPriceName: "VIP", Price: 90, PlanExtra: true, Availability: 30, Color: "#00AA00"},
		bil24compat.ImportSessionCategory{CategoryPriceName: "Student", Price: 10, PlanExtra: true},
	)
	rec, out := sf.call(h, body)
	if rec.Code != http.StatusOK || !out.Created || out.SeatsMaterialized != 53 {
		t.Fatalf("create: %d places %d %s", rec.Code, out.SeatsMaterialized, rec.Body.String())
	}
	var mode string
	if err := pool.QueryRow(ctx, `SELECT admission_mode FROM sessions WHERE id = $1`, out.SessionID).Scan(&mode); err != nil || mode != "hybrid" {
		t.Fatalf("admission mode %q %v, want the plan's hybrid", mode, err)
	}
	tiers := tierByName(ctx, t, pool, out.SessionID)
	for _, name := range []string{"VIP", "Student"} {
		tier, ok := tiers[name]
		if !ok {
			t.Fatalf("extra category %s not created: %v", name, tiers)
		}
		if tier.Capacity != nil {
			t.Errorf("%s carries a quantity %d, want none", name, *tier.Capacity)
		}
		var places int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM session_seats WHERE session_id = $1 AND tier_id = $2`, out.SessionID, tier.ID).Scan(&places); err != nil || places != 0 {
			t.Errorf("%s owns %d places (%v), want 0", name, places, err)
		}
	}
	if tiers["VIP"].PriceAmount != 9000 {
		t.Errorf("VIP price = %d, want 9000", tiers["VIP"].PriceAmount)
	}
	q := gen.New(pool)
	for name, want := range map[string]string{"Parter": "#aa0000", "VIP": "#00aa00"} {
		c, err := q.GetTicketTierColor(ctx, tiers[name].ID, out.SessionID)
		if err != nil || c == nil || *c != want {
			t.Errorf("%s colour = %v %v, want %s", name, c, err, want)
		}
	}
	if c, err := q.GetTicketTierColor(ctx, tiers["Student"].ID, out.SessionID); err != nil || c != nil {
		t.Errorf("Student colour = %v %v, want none", c, err)
	}
	before := sessionSeatSnapshot(ctx, t, pool, out.SessionID)

	// The repeat: every category by its arena id, the extras without a
	// quantity and without the flag (it is ignored on an existing session).
	again := sf.seatedPayload()
	again.ActionEvent.ArenaSeatingPlanVersionID = ""
	again.CategoryList = append(again.CategoryList,
		bil24compat.ImportSessionCategory{ArenaTierID: tiers["VIP"].ID.String(), CategoryPriceName: "VIP", Price: 95},
		bil24compat.ImportSessionCategory{ArenaTierID: tiers["Student"].ID.String(), CategoryPriceName: "Student", Price: 12},
	)
	rec, re := sf.call(h, again)
	if rec.Code != http.StatusOK || re.Created {
		t.Fatalf("repeat: %d %s", rec.Code, rec.Body.String())
	}
	after := sessionSeatSnapshot(ctx, t, pool, out.SessionID)
	if len(after) != len(before) {
		t.Fatalf("the repeat changed the places: %d -> %d", len(before), len(after))
	}
	if err := pool.QueryRow(ctx, `SELECT admission_mode FROM sessions WHERE id = $1`, out.SessionID).Scan(&mode); err != nil || mode != "hybrid" {
		t.Fatalf("admission mode after the repeat %q %v", mode, err)
	}
	tiers = tierByName(ctx, t, pool, out.SessionID)
	if tiers["VIP"].PriceAmount != 9500 || !tiers["VIP"].IsOpen {
		t.Errorf("VIP after the repeat: price %d open %v", tiers["VIP"].PriceAmount, tiers["VIP"].IsOpen)
	}

	// A name clash with a plan category is refused and writes nothing.
	clash := sf.seatedPayload()
	clash.ExternalRef = sf.externalRef + ":clash"
	clash.CategoryList = append(clash.CategoryList, bil24compat.ImportSessionCategory{CategoryPriceName: "parter", Price: 5, PlanExtra: true})
	if rec, _ := sf.call(h, clash); rec.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(rec.Body.String(), "import.seating_category_duplicate") {
		t.Fatalf("clash: %d %s", rec.Code, rec.Body.String())
	}
	bad := sf.seatedPayload()
	bad.ExternalRef = sf.externalRef + ":color"
	bad.CategoryList[1].Color = "green"
	if rec, _ := sf.call(h, bad); rec.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(rec.Body.String(), "import.invalid_color") {
		t.Fatalf("bad colour: %d %s", rec.Code, rec.Body.String())
	}
}
