//go:build integration

// scenario01_catalog_test.go — spec §15.3 scenario 1, GET_ALL_ACTIONS
// (feature #497 W1-B3a, spec §7.1).
//
// The scenario lives in its own file because it is the only one that has to
// grow fixtures of its own: the seed (seed_test.go) gives us an assigned-seats
// session and a GA session, but spec §7.1 also pins the HYBRID shape (a plan id
// AND non-empty categories on the same session) and the NO-TIMEZONE rule (a
// session whose venue has no IANA zone is dropped, never guessed). Those two
// need extra rows, so they are seeded here, asserted, and torn down again
// before the next scenario runs — the parent test's fixture is left exactly as
// scenario 2 and 3 expect to find it.
//
// Why so many hand-written value assertions on top of assertGoldenKeySet: the
// harness key-set comparison (harness_test.go compareKeys) recurses into nested
// OBJECTS but not into nested ARRAYS. Everything interesting in GET_ALL_ACTIONS
// — actionList[], actionEventList[], categoryLimitList[].categoryList[] — is an
// array, so the golden alone would only prove the six top-level keys. This file
// therefore walks the arrays itself and compares each element's key set against
// the matching golden element, then asserts the values the spec fixes.
package compat_bil24_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

// runScenario01Catalog is the body of the 01_catalog_get_all_actions sub-test.
// It stays a named function so harness_test.go keeps its one-line-per-scenario
// table (TestCompatBil24_450_Harness_ScenarioCoverage greps that file for the
// t.Run literals).
func runScenario01Catalog(t *testing.T, st *harnessState) {
	t.Helper()
	base := startHarnessServer(t, st)
	ctx := context.Background()

	// The venue-local calendar day/time the response must carry. Computed from
	// the row the seed actually wrote rather than from time.Now(): the seed
	// truncates and the test must compare against the same instant.
	var startAt, endAt time.Time
	if err := st.Pool.QueryRow(ctx,
		`SELECT start_at, end_at FROM sessions WHERE id = $1`,
		st.AssignedSessID,
	).Scan(&startAt, &endAt); err != nil {
		t.Fatalf("read seeded session start_at: %v", err)
	}
	loc, err := time.LoadLocation(st.VenueTimezone)
	if err != nil {
		t.Fatalf("load venue timezone %q: %v", st.VenueTimezone, err)
	}
	local := startAt.In(loc)
	wantDay := local.Format("02.01.2006") // allow:timeformat: spec §7.1 DD.MM.YYYY
	wantTime := local.Format("15:04")     // allow:timeformat: spec §7.1 HH:MM local
	// No seeded tier bounds its sale window, so spec §7.1 makes sellEndTime the
	// session start — rendered in the VENUE's zone, offset and all.
	wantSellEnd := local.Format(time.RFC3339)

	seatedEventID := mustActionEventID(t, st, st.AssignedSessID)
	gaEventID := mustActionEventID(t, st, st.GAsessID)

	// ── the catalog as the WP plugin sees it ────────────────────────────────
	req, gld := loadWPFixture(t, "GET_ALL_ACTIONS", "basic")
	req["fid"] = st.ChannelFID
	req["token"] = st.ChannelToken
	resp := postBil24(t, base, req)
	if code := numberField(t, resp, "resultCode"); code != 0 {
		t.Fatalf("GET_ALL_ACTIONS resultCode = %v, want 0 (response %v)", code, resp)
	}
	assertGoldenKeySet(t, resp, resolveGolden(gld, st))

	actions := sc1Objects(t, resp, "actionList")
	if len(actions) != 1 {
		t.Fatalf("actionList has %d entries, want exactly the one seeded published event", len(actions))
	}
	action := actions[0]
	gldActions := sc1Objects(t, resolveGolden(gld, st), "actionList")
	compareKeys(t, "actionList[0]", action, gldActions[0])

	// Spec §7.1: the price envelope spans EVERY live tier of the action, seated
	// ones included — a pure-seating action must still advertise a "from" price.
	// Seed: CZK 500 (Parter) + EUR 900 / 1250 (Early Bird / Standard).
	sc1WantNumber(t, action, "minPrice", 500)
	sc1WantNumber(t, action, "maxPrice", 1250)
	// Both dates come off the venue-local session projection, not the UTC
	// first_session_at cache — an evening show must not roll a day.
	sc1WantString(t, action, "firstEventDate", wantDay)
	sc1WantString(t, action, "lastEventDate", wantDay)
	if organizerID := numberField(t, action, "organizerId"); organizerID <= 0 {
		t.Errorf("actionList[0].organizerId = %v, want the org's display_number", organizerID)
	}

	events := sc1Objects(t, action, "actionEventList")
	if len(events) != 2 {
		t.Fatalf("actionEventList has %d entries, want 2 (assigned-seats + GA)", len(events))
	}
	seated := sc1FindEvent(t, events, float64(seatedEventID))
	ga := sc1FindEvent(t, events, float64(gaEventID))

	// ── the assigned-seats session (golden: seated.json) ────────────────────
	_, seatedGolden := loadWPFixture(t, "GET_ALL_ACTIONS", "seated")
	seatedGoldenEvent := sc1Objects(t, sc1Objects(t, resolveGolden(seatedGolden, st), "actionList")[0], "actionEventList")[0]
	compareKeys(t, "seated actionEvent", seated, seatedGoldenEvent)

	sc1WantString(t, seated, "day", wantDay)
	sc1WantString(t, seated, "time", wantTime)
	sc1WantString(t, seated, "currency", "CZK")
	sc1WantString(t, seated, "sellEndTime", wantSellEnd)
	// Spec §7.1: sellEndTime is RFC3339 WITH the venue's offset. "Z" would be a
	// silent hour shift on the site, so assert the offset explicitly rather
	// than trusting the string compare above to have covered it.
	if parsed, perr := time.Parse(time.RFC3339, wantSellEnd); perr != nil {
		t.Errorf("sellEndTime %q does not parse as RFC3339: %v", wantSellEnd, perr)
	} else if _, off := parsed.Zone(); off == 0 {
		t.Errorf("sellEndTime %q carries a zero UTC offset; spec §7.1 wants the venue's local offset", wantSellEnd)
	}
	// Spec §7.1: the plan is addressed BY THE SESSION, because GET_SCHEMA takes
	// an actionEventId — so a seated session reports its own id here.
	sc1WantNumber(t, seated, "seatingPlanId", float64(seatedEventID))
	sc1WantNumber(t, seated, "availability", float64(len(st.SeatIDs)))
	sc1WantNumber(t, seated, "minPrice", 500)
	// fee_percent is 5.00 on the seeded channel and travels as an int.
	sc1WantNumber(t, seated, "chargePercent", 5)
	if got := sc1Array(t, seated, "categoryLimitList"); len(got) != 0 {
		// Load-bearing emptiness: bil24-acf-sync.php:434-446 reads an empty
		// categoryLimitList as "pure seating, render the seat map only".
		t.Errorf("seated categoryLimitList = %v, want [] for a session with no GA tier", got)
	}
	if _, ok := seated["seatingPlanName"]; !ok {
		t.Error("seated actionEvent is missing seatingPlanName")
	}

	// ── the GA session (golden: ga.json) ────────────────────────────────────
	_, gaGolden := loadWPFixture(t, "GET_ALL_ACTIONS", "ga")
	gaGoldenEvent := sc1Objects(t, sc1Objects(t, resolveGolden(gaGolden, st), "actionList")[0], "actionEventList")[0]
	compareKeys(t, "ga actionEvent", ga, gaGoldenEvent)

	sc1WantString(t, ga, "day", wantDay)
	sc1WantString(t, ga, "time", wantTime)
	// The GA session overrides the currency to EUR (currency_source='override'),
	// which must survive onto the wire per session, not per action.
	sc1WantString(t, ga, "currency", "EUR")
	// Spec §7.1: a pure-GA session has no plan; 0 is how the plugin decides not
	// to render a seat map at all.
	sc1WantNumber(t, ga, "seatingPlanId", 0)
	if _, ok := ga["seatingPlanName"]; ok {
		t.Error("GA actionEvent must not carry seatingPlanName")
	}
	// 50 ga_unit rows, all available; their tier_id is NULL, so the categories
	// below fall back to this session-level count rather than reporting 0.
	sc1WantNumber(t, ga, "availability", 50)
	sc1WantNumber(t, ga, "minPrice", 900)

	gaLimits := sc1Objects(t, ga, "categoryLimitList")
	if len(gaLimits) != 1 {
		t.Fatalf("GA categoryLimitList has %d entries, want exactly 1 wrapper", len(gaLimits))
	}
	gaGoldenLimits := sc1Objects(t, gaGoldenEvent, "categoryLimitList")
	compareKeys(t, "ga categoryLimitList[0]", gaLimits[0], gaGoldenLimits[0])

	cats := sc1Objects(t, gaLimits[0], "categoryList")
	goldenCats := sc1Objects(t, gaGoldenLimits[0], "categoryList")
	if len(cats) != 2 {
		t.Fatalf("GA categoryList has %d entries, want 2 (Early Bird + Standard)", len(cats))
	}
	// sort_order 0/1 on the tiers pins the order, so index comparison is safe.
	wantNames := []string{"Early Bird", "Standard"}
	wantPrices := []float64{900, 1250}
	for i, cat := range cats {
		compareKeys(t, "ga categoryList", cat, goldenCats[i])
		sc1WantString(t, cat, "categoryPriceName", wantNames[i])
		sc1WantNumber(t, cat, "price", wantPrices[i])
		sc1WantNumber(t, cat, "availability", 50)
		if placement, ok := cat["placement"].(bool); !ok || placement {
			t.Errorf("categoryList[%d].placement = %v, want false — a GA category has no seat to choose", i, cat["placement"])
		}
		if m, ok := cat["tariffIdMap"].(map[string]interface{}); !ok || len(m) != 0 {
			t.Errorf("categoryList[%d].tariffIdMap = %v, want an empty object", i, cat["tariffIdMap"])
		}
		if id := numberField(t, cat, "categoryPriceId"); id <= 0 {
			t.Errorf("categoryList[%d].categoryPriceId = %v, want a minted int64 compat id", i, id)
		}
	}

	// ── org isolation (golden: isolation.json) ──────────────────────────────
	//
	// A different channel of a different org must see an EMPTY catalog over the
	// same endpoint. This is the one assertion that protects every WP site on
	// the gateway from every other one.
	t.Run("org_isolation", func(t *testing.T) {
		otherFID, otherToken := sc1SeedForeignChannel(t, st)
		isoReq, isoGolden := loadWPFixture(t, "GET_ALL_ACTIONS", "isolation")
		isoReq["fid"] = otherFID
		isoReq["token"] = otherToken
		isoResp := postBil24(t, base, isoReq)
		if code := numberField(t, isoResp, "resultCode"); code != 0 {
			t.Fatalf("foreign-org GET_ALL_ACTIONS resultCode = %v, want 0", code)
		}
		assertGoldenKeySet(t, isoResp, resolveGolden(isoGolden, st))
		for _, key := range []string{"actionList", "countryList", "cityList"} {
			if got := sc1Array(t, isoResp, key); len(got) != 0 {
				t.Errorf("foreign org sees %d %s entries, want 0 — org isolation is broken", len(got), key)
			}
		}
	})

	// ── hybrid session (golden: hybrid.json) ────────────────────────────────
	t.Run("hybrid", func(t *testing.T) {
		hybridSessID := sc1SeedHybridEvent(t, st, startAt, endAt)
		hybridEventID := mustActionEventID(t, st, hybridSessID)

		hReq, hGolden := loadWPFixture(t, "GET_ALL_ACTIONS", "hybrid")
		hReq["fid"] = st.ChannelFID
		hReq["token"] = st.ChannelToken
		hResp := postBil24(t, base, hReq)
		hAction := sc1FindActionByEvent(t, hResp, float64(hybridEventID))
		hGoldenAction := sc1Objects(t, resolveGolden(hGolden, st), "actionList")[0]
		compareKeys(t, "hybrid actionList entry", hAction, hGoldenAction)
		sc1WantNumber(t, hAction, "minPrice", 700)
		sc1WantNumber(t, hAction, "maxPrice", 1500)

		hEvent := sc1Objects(t, hAction, "actionEventList")[0]
		compareKeys(t, "hybrid actionEvent", hEvent,
			sc1Objects(t, hGoldenAction, "actionEventList")[0])
		// Spec §7.1: hybrid is the shape that has BOTH — a seat map addressed by
		// the session id, and GA categories alongside it.
		sc1WantNumber(t, hEvent, "seatingPlanId", float64(hybridEventID))
		sc1WantNumber(t, hEvent, "availability", 10)
		sc1WantNumber(t, hEvent, "minPrice", 700)
		hCats := sc1Objects(t, sc1Objects(t, hEvent, "categoryLimitList")[0], "categoryList")
		if len(hCats) != 1 {
			t.Fatalf("hybrid categoryList has %d entries, want 1 — only the ga_unit-backed tier is a category", len(hCats))
		}
		sc1WantString(t, hCats[0], "categoryPriceName", "Stání")
		sc1WantNumber(t, hCats[0], "price", 700)
		// The GA tier owns its OWN ga_unit rows here, so the count is the
		// tier's, not the session's fallback.
		sc1WantNumber(t, hCats[0], "availability", 10)
	})

	// ── venue without a timezone (golden: no_timezone.json) ─────────────────
	t.Run("no_timezone", func(t *testing.T) {
		noTZEventUUID := sc1SeedNoTimezoneEvent(t, st, startAt, endAt)

		nReq, nGolden := loadWPFixture(t, "GET_ALL_ACTIONS", "no_timezone")
		nReq["fid"] = st.ChannelFID
		nReq["token"] = st.ChannelToken
		nResp := postBil24(t, base, nReq)

		nAction := sc1FindActionByName(t, nResp, "W1 Harness NoTZ "+noTZEventUUID.String()[:8])
		nGoldenAction := sc1Objects(t, resolveGolden(nGolden, st), "actionList")[0]
		compareKeys(t, "no_timezone actionList entry", nAction, nGoldenAction)
		// Spec §7.1: a session whose venue has no IANA zone cannot be given a
		// local calendar day, and a WRONG day is worse than a missing session.
		if got := sc1Array(t, nAction, "actionEventList"); len(got) != 0 {
			t.Errorf("actionEventList = %v, want [] — the session's venue has no timezone", got)
		}
		// With no projectable session there is no live tier either, so the
		// action advertises 0/0 rather than dropping the keys.
		sc1WantNumber(t, nAction, "minPrice", 0)
		sc1WantNumber(t, nAction, "maxPrice", 0)
		// The dates fall back to the trigger-maintained UTC caches, which is the
		// best available answer when no zone is known.
		sc1WantString(t, nAction, "firstEventDate", startAt.UTC().Format("02.01.2006")) // allow:timeformat: spec §7.1 DD.MM.YYYY
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// extra fixtures — torn down before the next scenario runs
// ─────────────────────────────────────────────────────────────────────────────

// sc1SeedForeignChannel creates a second org with its own gateway channel and
// no events at all. It is the counterparty for the org-isolation assertion.
func sc1SeedForeignChannel(t *testing.T, st *harnessState) (fid int64, token string) {
	t.Helper()
	ctx := context.Background()
	orgID := uuid.New()
	suffix := orgID.String()[:8]
	if _, err := st.Pool.Exec(ctx,
		`INSERT INTO organizations (id, name, slug, legal_name)
		 VALUES ($1, $2, $3, $4)`,
		orgID, "W1-497 Foreign Org "+suffix, "w1-497-"+suffix, "Foreign s.r.o.",
	); err != nil {
		t.Fatalf("seed foreign org: %v", err)
	}
	token = "wave1-foreign-token-" + suffix
	hash, err := bcrypt.GenerateFromPassword([]byte(token), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("bcrypt hash: %v", err)
	}
	settings, err := json.Marshal(map[string]string{"gateway_token_hash": string(hash)})
	if err != nil {
		t.Fatalf("marshal settings: %v", err)
	}
	channelID := uuid.New()
	if err := st.Pool.QueryRow(ctx,
		`INSERT INTO sales_channels
		     (id, org_id, name, provider, payment_mode, fee_percent, settings)
		 VALUES ($1, $2, $3, 'stripe', 'direct_merchant', 5.00, $4::jsonb)
		 RETURNING display_number`,
		channelID, orgID, "Foreign WP gateway "+suffix, settings,
	).Scan(&fid); err != nil {
		t.Fatalf("seed foreign sales_channel: %v", err)
	}
	t.Cleanup(func() {
		cctx := context.Background()
		for _, sql := range []string{
			`DELETE FROM sales_channels WHERE org_id = $1`,
			`DELETE FROM organizations WHERE id = $1`,
		} {
			if _, err := st.Pool.Exec(cctx, sql, orgID); err != nil {
				t.Logf("foreign-org cleanup %.50s… : %v", sql, err)
			}
		}
	})
	return fid, token
}

// sc1SeedHybridEvent creates a published event of the seeded org carrying ONE
// hybrid session at the seeded venue: a plan version (so the seat map exists),
// a GA tier backed by ten ga_unit rows, and a seated tier with none. It returns
// the session uuid as a string and registers full teardown.
func sc1SeedHybridEvent(t *testing.T, st *harnessState, start, end time.Time) string {
	t.Helper()
	ctx := context.Background()

	var planVersionID uuid.UUID
	if err := st.Pool.QueryRow(ctx,
		`SELECT spv.id
		   FROM seating_plan_versions spv
		   JOIN seating_plans sp ON sp.id = spv.seating_plan_id
		  WHERE sp.venue_id = $1
		  LIMIT 1`, st.VenueID,
	).Scan(&planVersionID); err != nil {
		t.Fatalf("resolve seeded plan version: %v", err)
	}

	eventID := uuid.New()
	sessID := uuid.New()
	if _, err := st.Pool.Exec(ctx,
		`INSERT INTO events (id, org_id, name, status, visibility)
		 VALUES ($1, $2, $3, 'published', 'public')`,
		eventID, st.OrgID, "W1 Harness Hybrid "+eventID.String()[:8],
	); err != nil {
		t.Fatalf("seed hybrid event: %v", err)
	}
	sc1RegisterEventCleanup(t, st, eventID, uuid.Nil)

	if _, err := st.Pool.Exec(ctx,
		`INSERT INTO sessions
		     (id, event_id, venue_id, start_at, end_at, capacity_total,
		      status, admission_mode, seating_plan_version_id,
		      currency, currency_source)
		 VALUES ($1, $2, $3, $4, $5, 10, 'scheduled', 'hybrid', $6,
		         'CZK', 'override')`,
		sessID, eventID, st.VenueID, start, end, planVersionID,
	); err != nil {
		t.Fatalf("seed hybrid session: %v", err)
	}
	var gaTierID uuid.UUID
	if err := st.Pool.QueryRow(ctx,
		`INSERT INTO ticket_tiers (session_id, name, pricing_mode,
		     price_amount, currency, sort_order)
		 VALUES ($1, 'Stání', 'fixed', 700, 'CZK', 0)
		 RETURNING id`, sessID,
	).Scan(&gaTierID); err != nil {
		t.Fatalf("seed hybrid GA tier: %v", err)
	}
	// A seated tier with no ga_unit rows: it must NOT become a category, but it
	// must still widen the action's maxPrice to 1500.
	if _, err := st.Pool.Exec(ctx,
		`INSERT INTO ticket_tiers (session_id, name, pricing_mode,
		     price_amount, currency, sort_order)
		 VALUES ($1, 'Balkon', 'fixed', 1500, 'CZK', 1)`, sessID,
	); err != nil {
		t.Fatalf("seed hybrid seated tier: %v", err)
	}
	if _, err := st.Pool.Exec(ctx,
		`INSERT INTO session_seats
		     (session_id, seat_key, sector_name, row_name, seat_number,
		      tier_id, status, kind)
		 SELECT $1, 'hyb|pool|' || lpad(gs::text, 6, '0'), '', '', '',
		        $2, 'available', 'ga_unit'
		 FROM generate_series(1, 10) gs`,
		sessID, gaTierID,
	); err != nil {
		t.Fatalf("seed hybrid ga_units: %v", err)
	}
	return sessID.String()
}

// sc1SeedNoTimezoneEvent creates a second venue with a NULL timezone plus a
// published event with one session there. Spec §7.1 requires the session to be
// dropped from the response entirely.
func sc1SeedNoTimezoneEvent(t *testing.T, st *harnessState, start, end time.Time) uuid.UUID {
	t.Helper()
	ctx := context.Background()

	venueID := uuid.New()
	if _, err := st.Pool.Exec(ctx,
		`INSERT INTO venues (id, org_id, city_id, name, country, timezone)
		 SELECT $1, $2, v.city_id, $3, 'CZ', NULL
		   FROM venues v WHERE v.id = $4`,
		venueID, st.OrgID, "W1 Harness NoTZ venue "+venueID.String()[:8], st.VenueID,
	); err != nil {
		t.Fatalf("seed no-timezone venue: %v", err)
	}

	eventID := uuid.New()
	if _, err := st.Pool.Exec(ctx,
		`INSERT INTO events (id, org_id, name, status, visibility)
		 VALUES ($1, $2, $3, 'published', 'public')`,
		eventID, st.OrgID, "W1 Harness NoTZ "+eventID.String()[:8],
	); err != nil {
		t.Fatalf("seed no-timezone event: %v", err)
	}
	sc1RegisterEventCleanup(t, st, eventID, venueID)

	if _, err := st.Pool.Exec(ctx,
		`INSERT INTO sessions
		     (id, event_id, venue_id, start_at, end_at, capacity_total,
		      status, admission_mode, currency, currency_source)
		 VALUES ($1, $2, $3, $4, $5, 20, 'scheduled', 'general_admission',
		         'CZK', 'override')`,
		uuid.New(), eventID, venueID, start, end,
	); err != nil {
		t.Fatalf("seed no-timezone session: %v", err)
	}
	return eventID
}

// sc1RegisterEventCleanup tears down one scenario-local event (and optionally
// the venue it hangs off) in FK order, compatibility ids included. Registered
// eagerly right after the event row exists so a mid-seed failure still cleans.
func sc1RegisterEventCleanup(t *testing.T, st *harnessState, eventID, venueID uuid.UUID) {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		stmts := []struct {
			sql string
			arg any
		}{
			{`DELETE FROM compatibility_id_map WHERE platform_id IN
			      (SELECT id FROM ticket_tiers WHERE session_id IN
			          (SELECT id FROM sessions WHERE event_id = $1))`, eventID},
			{`DELETE FROM compatibility_id_map WHERE platform_id IN
			      (SELECT id FROM sessions WHERE event_id = $1)`, eventID},
			{`DELETE FROM compatibility_id_map WHERE platform_id = $1`, eventID},
			{`DELETE FROM session_seats WHERE session_id IN
			      (SELECT id FROM sessions WHERE event_id = $1)`, eventID},
			{`DELETE FROM ticket_tiers WHERE session_id IN
			      (SELECT id FROM sessions WHERE event_id = $1)`, eventID},
			{`DELETE FROM inventory_ledger WHERE session_id IN
			      (SELECT id FROM sessions WHERE event_id = $1)`, eventID},
			{`DELETE FROM sessions WHERE event_id = $1`, eventID},
			{`DELETE FROM events WHERE id = $1`, eventID},
		}
		if venueID != uuid.Nil {
			stmts = append(stmts,
				struct {
					sql string
					arg any
				}{`DELETE FROM compatibility_id_map WHERE platform_id = $1`, venueID},
				struct {
					sql string
					arg any
				}{`DELETE FROM venues WHERE id = $1`, venueID},
			)
		}
		for _, s := range stmts {
			if _, err := st.Pool.Exec(ctx, s.sql, s.arg); err != nil {
				t.Logf("scenario-01 cleanup %.50s… : %v", s.sql, err)
			}
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// small JSON accessors (sc1 prefix keeps them out of the shared namespace)
// ─────────────────────────────────────────────────────────────────────────────

func sc1Array(t *testing.T, m map[string]interface{}, key string) []interface{} {
	t.Helper()
	v, ok := m[key]
	if !ok {
		t.Fatalf("missing key %q in %v", key, m)
	}
	arr, ok := v.([]interface{})
	if !ok {
		t.Fatalf("key %q is %T, want an array", key, v)
	}
	return arr
}

func sc1Objects(t *testing.T, m map[string]interface{}, key string) []map[string]interface{} {
	t.Helper()
	arr := sc1Array(t, m, key)
	out := make([]map[string]interface{}, 0, len(arr))
	for i, v := range arr {
		obj, ok := v.(map[string]interface{})
		if !ok {
			t.Fatalf("%s[%d] is %T, want an object", key, i, v)
		}
		out = append(out, obj)
	}
	return out
}

func sc1WantString(t *testing.T, m map[string]interface{}, key, want string) {
	t.Helper()
	got, ok := m[key].(string)
	if !ok {
		t.Errorf("%s = %v (%T), want the string %q", key, m[key], m[key], want)
		return
	}
	if got != want {
		t.Errorf("%s = %q, want %q", key, got, want)
	}
}

func sc1WantNumber(t *testing.T, m map[string]interface{}, key string, want float64) {
	t.Helper()
	got, ok := m[key].(float64)
	if !ok {
		t.Errorf("%s = %v (%T), want the number %v", key, m[key], m[key], want)
		return
	}
	if got != want {
		t.Errorf("%s = %v, want %v", key, got, want)
	}
}

// sc1FindEvent locates an actionEventList entry by its actionEventId. Index
// lookup would be a flake: the two seeded sessions share a start_at, so the
// ORDER BY falls through to the random session uuid.
func sc1FindEvent(t *testing.T, events []map[string]interface{}, id float64) map[string]interface{} {
	t.Helper()
	for _, e := range events {
		if got, ok := e["actionEventId"].(float64); ok && got == id {
			return e
		}
	}
	t.Fatalf("no actionEvent with actionEventId=%v among %v", id, events)
	return nil
}

// sc1FindActionByEvent returns the actionList entry that contains the given
// actionEventId.
func sc1FindActionByEvent(t *testing.T, resp map[string]interface{}, id float64) map[string]interface{} {
	t.Helper()
	for _, action := range sc1Objects(t, resp, "actionList") {
		for _, e := range sc1Objects(t, action, "actionEventList") {
			if got, ok := e["actionEventId"].(float64); ok && got == id {
				return action
			}
		}
	}
	t.Fatalf("no action carrying actionEventId=%v", id)
	return nil
}

// sc1FindActionByName is the lookup for an action with NO projectable session —
// there is no actionEventId to key on, so the seeded name is the handle.
func sc1FindActionByName(t *testing.T, resp map[string]interface{}, name string) map[string]interface{} {
	t.Helper()
	for _, action := range sc1Objects(t, resp, "actionList") {
		if got, ok := action["actionName"].(string); ok && got == name {
			return action
		}
	}
	t.Fatalf("no action named %q in the catalog", name)
	return nil
}
