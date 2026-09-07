//go:build integration

// seat_list_499_test.go — GET_SEAT_LIST wire contract (feature #499 W1-B4,
// spec §7.2).
//
// GET_SEAT_LIST is the command the WordPress plugin calls to render a hall,
// so its shape is the one most likely to break a live site silently. This
// file walks all five documented shapes against the goldens under
// testdata/wp/{requests,golden}/GET_SEAT_LIST/:
//
//	basic          — assigned-seats session, no filter: the full envelope.
//	seated         — the same session with one seat held: `available` is a
//	                 BOOLEAN per seat and the category count drops by one.
//	hybrid         — a session carrying BOTH real seats and GA units: two
//	                 categories with placement true/false, and the GA units
//	                 projected as pseudo-seats sectored by their tier name.
//	ga             — pure general-admission: the `placement` key is ABSENT
//	                 and `seatList` is the empty array.
//	available_only — availableOnly:true shrinks `seatList` ONLY; the
//	                 categoryList still describes every category.
//
// Why the hand-written array walks: the harness key-set comparison
// (harness_test.go compareKeys) recurses into nested OBJECTS but not into
// nested ARRAYS, and everything interesting here — categoryList[],
// seatList[] — is an array. assertGoldenKeySet alone would only prove the
// six top-level keys, so each element is compared against its golden
// element explicitly and the spec-fixed values asserted on top.
package compat_bil24_test

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestCompatBil24_499_GetSeatList_WireShape(t *testing.T) {
	st := setupHarness(t)
	base := startHarnessServer(t, st)
	ctx := context.Background()

	seatedEventID := mustActionEventID(t, st, st.AssignedSessID)
	gaEventID := mustActionEventID(t, st, st.GAsessID)
	totalSeats := len(st.SeatIDs)
	if totalSeats == 0 {
		t.Fatal("harness seeded no session_seats — the assigned-seats cases cannot run")
	}

	// ── basic: the full envelope on an untouched assigned-seats session ─────
	t.Run("basic", func(t *testing.T) {
		req, gld := loadWPFixture(t, "GET_SEAT_LIST", "basic")
		resp := sl499Post(t, base, st, req, seatedEventID)
		gold := resolveGolden(gld, st)
		assertGoldenKeySet(t, resp, gold)

		sc1WantString(t, resp, "currency", "CZK")

		cats := sc1Objects(t, resp, "categoryList")
		goldCats := sc1Objects(t, gold, "categoryList")
		if len(cats) != 1 {
			t.Fatalf("categoryList has %d entries, want 1 (the seeded Parter tier)", len(cats))
		}
		compareKeys(t, "categoryList[0]", cats[0], goldCats[0])
		sc1WantString(t, cats[0], "categoryPriceName", "Parter")
		sc1WantNumber(t, cats[0], "price", 500)
		sc1WantNumber(t, cats[0], "availability", float64(totalSeats))
		sl499WantBool(t, cats[0], "placement", true)
		sl499WantEmptyObject(t, cats[0], "tariffIdMap")
		categoryPriceID := numberField(t, cats[0], "categoryPriceId")
		if categoryPriceID <= 0 {
			t.Errorf("categoryList[0].categoryPriceId = %v, want the minted compat bigint", categoryPriceID)
		}

		seats := sc1Objects(t, resp, "seatList")
		goldSeat := sc1Objects(t, gold, "seatList")[0]
		if len(seats) != totalSeats {
			t.Fatalf("seatList has %d entries, want all %d materialised seats", len(seats), totalSeats)
		}
		wantIDs := map[string]bool{}
		for _, id := range st.SeatIDs {
			wantIDs[id] = true
		}
		for i, seat := range seats {
			compareKeys(t, "seatList["+strconv.Itoa(i)+"]", seat, goldSeat)
			// Spec §4: seatId is session_seats.system_seat_id, an int64 —
			// never the platform UUID.
			gotID := strconv.FormatInt(int64(numberField(t, seat, "seatId")), 10)
			if !wantIDs[gotID] {
				t.Errorf("seatList[%d].seatId = %s, which is not a seeded system_seat_id", i, gotID)
			}
			sl499WantBool(t, seat, "available", true)
			sc1WantNumber(t, seat, "price", 500)
			sc1WantNumber(t, seat, "categoryPriceId", categoryPriceID)
			// arena has no tariff plans; the key is part of the contract the
			// plugin destructures and must be an EXPLICIT null.
			if v, present := seat["tariffPlanId"]; !present || v != nil {
				t.Errorf("seatList[%d].tariffPlanId = %#v (present=%v), want an explicit null", i, v, present)
			}
			loc, ok := seat["location"].(map[string]interface{})
			if !ok {
				t.Fatalf("seatList[%d].location = %#v, want an object", i, seat["location"])
			}
			for _, k := range []string{"sector", "row", "number"} {
				if _, isStr := loc[k].(string); !isStr {
					t.Errorf("seatList[%d].location.%s = %#v, want a string", i, k, loc[k])
				}
			}
		}
	})

	// ── seated: one held seat turns `available` false and drops the count ───
	t.Run("seated", func(t *testing.T) {
		heldID := sl499HoldOneSeat(t, st)

		req, gld := loadWPFixture(t, "GET_SEAT_LIST", "seated")
		resp := sl499Post(t, base, st, req, seatedEventID)
		gold := resolveGolden(gld, st)
		assertGoldenKeySet(t, resp, gold)

		cats := sc1Objects(t, resp, "categoryList")
		if len(cats) != 1 {
			t.Fatalf("categoryList has %d entries, want 1", len(cats))
		}
		// Spec §7.2: availability counts AVAILABLE units, so holding one seat
		// must show up here — a stale count oversells the hall.
		sc1WantNumber(t, cats[0], "availability", float64(totalSeats-1))
		sl499WantBool(t, cats[0], "placement", true)

		goldSeats := sc1Objects(t, gold, "seatList")
		seats := sc1Objects(t, resp, "seatList")
		if len(seats) != totalSeats {
			t.Fatalf("seatList has %d entries, want all %d seats (no filter was requested)", len(seats), totalSeats)
		}
		sawHeld := false
		for i, seat := range seats {
			compareKeys(t, "seatList["+strconv.Itoa(i)+"]", seat, goldSeats[0])
			avail, ok := seat["available"].(bool)
			if !ok {
				t.Fatalf("seatList[%d].available = %#v, want a bool (the numeric BSS status is retired)", i, seat["available"])
			}
			if int64(numberField(t, seat, "seatId")) == heldID {
				sawHeld = true
				if avail {
					t.Errorf("held seat %d reports available=true", heldID)
				}
			} else if !avail {
				t.Errorf("seatList[%d] reports available=false, but only seat %d was held", i, heldID)
			}
		}
		if !sawHeld {
			t.Errorf("seatList does not carry the held seat %d; a held seat must stay visible when availableOnly is off", heldID)
		}
	})

	// ── available_only: filters seatList and nothing else ───────────────────
	t.Run("available_only", func(t *testing.T) {
		heldID := sl499HoldOneSeat(t, st)

		req, gld := loadWPFixture(t, "GET_SEAT_LIST", "available_only")
		if req["availableOnly"] != true {
			t.Fatalf("fixture available_only.json must request availableOnly:true, got %#v", req["availableOnly"])
		}
		resp := sl499Post(t, base, st, req, seatedEventID)
		gold := resolveGolden(gld, st)
		assertGoldenKeySet(t, resp, gold)

		// Spec §7.2 is explicit that the filter applies to seatList ONLY:
		// categoryList must still describe the complete category set so a
		// sold-out category keeps rendering on the site.
		cats := sc1Objects(t, resp, "categoryList")
		if len(cats) != 1 {
			t.Fatalf("categoryList has %d entries, want 1 — availableOnly must not filter categories", len(cats))
		}
		sc1WantNumber(t, cats[0], "availability", float64(totalSeats-1))

		seats := sc1Objects(t, resp, "seatList")
		if len(seats) != totalSeats-1 {
			t.Fatalf("seatList has %d entries, want %d (every seat but the held one)", len(seats), totalSeats-1)
		}
		for i, seat := range seats {
			sl499WantBool(t, seat, "available", true)
			if int64(numberField(t, seat, "seatId")) == heldID {
				t.Errorf("seatList[%d] carries held seat %d despite availableOnly:true", i, heldID)
			}
		}
	})

	// ── hybrid: seats AND GA units on one session ───────────────────────────
	t.Run("hybrid", func(t *testing.T) {
		var start, end time.Time
		if err := st.Pool.QueryRow(ctx,
			`SELECT start_at, end_at FROM sessions WHERE id = $1`, st.AssignedSessID,
		).Scan(&start, &end); err != nil {
			t.Fatalf("read seeded session window: %v", err)
		}
		hybridSessID := sl499SeedHybridSession(t, st, start, end)
		hybridEventID := mustActionEventID(t, st, hybridSessID)

		req, gld := loadWPFixture(t, "GET_SEAT_LIST", "hybrid")
		resp := sl499Post(t, base, st, req, hybridEventID)
		gold := resolveGolden(gld, st)
		assertGoldenKeySet(t, resp, gold)

		sc1WantString(t, resp, "currency", "CZK")

		cats := sc1Objects(t, resp, "categoryList")
		goldCats := sc1Objects(t, gold, "categoryList")
		if len(cats) != 2 {
			t.Fatalf("categoryList has %d entries, want 2 (Balkon + Stání)", len(cats))
		}
		balkon := sl499FindByName(t, cats, "Balkon")
		stani := sl499FindByName(t, cats, "Stání")
		compareKeys(t, "categoryList[Balkon]", balkon, goldCats[0])
		compareKeys(t, "categoryList[Stání]", stani, goldCats[1])

		// A seated category inside a plan is placement:true; a GA category
		// inside the SAME plan is placement:false. Collapsing the two would
		// make the plugin try to place standing-room tickets on the map.
		sl499WantBool(t, balkon, "placement", true)
		sl499WantBool(t, stani, "placement", false)
		sc1WantNumber(t, balkon, "price", 1500)
		sc1WantNumber(t, stani, "price", 700)
		sc1WantNumber(t, balkon, "availability", 4)
		sc1WantNumber(t, stani, "availability", 10)

		seats := sc1Objects(t, resp, "seatList")
		goldSeats := sc1Objects(t, gold, "seatList")
		if len(seats) != 14 {
			t.Fatalf("seatList has %d entries, want 14 (4 real seats + 10 GA units as pseudo-seats)", len(seats))
		}
		realSeats, pseudoSeats := 0, 0
		for i, seat := range seats {
			loc, ok := seat["location"].(map[string]interface{})
			if !ok {
				t.Fatalf("seatList[%d].location = %#v, want an object", i, seat["location"])
			}
			switch loc["sector"] {
			case "Balkon":
				realSeats++
				compareKeys(t, "seatList["+strconv.Itoa(i)+"]", seat, goldSeats[0])
				sc1WantNumber(t, seat, "price", 1500)
				sc1WantNumber(t, seat, "categoryPriceId", numberField(t, balkon, "categoryPriceId"))
			case "Stání":
				pseudoSeats++
				compareKeys(t, "seatList["+strconv.Itoa(i)+"]", seat, goldSeats[1])
				sc1WantNumber(t, seat, "price", 700)
				sc1WantNumber(t, seat, "categoryPriceId", numberField(t, stani, "categoryPriceId"))
				// Spec §7.2: a GA unit has no coordinates of its own, so it
				// is sectored by its tier name with empty row/number.
				sc1WantString(t, loc, "row", "")
				sc1WantString(t, loc, "number", "")
			default:
				t.Errorf("seatList[%d].location.sector = %#v, want Balkon or Stání", i, loc["sector"])
			}
			sl499WantBool(t, seat, "available", true)
		}
		if realSeats != 4 || pseudoSeats != 10 {
			t.Errorf("seatList carries %d seated + %d GA rows, want 4 + 10", realSeats, pseudoSeats)
		}
	})

	// ── ga: no plan at all — no placement key, empty seatList ───────────────
	t.Run("ga", func(t *testing.T) {
		req, gld := loadWPFixture(t, "GET_SEAT_LIST", "ga")
		resp := sl499Post(t, base, st, req, gaEventID)
		gold := resolveGolden(gld, st)
		assertGoldenKeySet(t, resp, gold)

		sc1WantString(t, resp, "currency", "EUR")

		cats := sc1Objects(t, resp, "categoryList")
		goldCats := sc1Objects(t, gold, "categoryList")
		if len(cats) != 2 {
			t.Fatalf("categoryList has %d entries, want 2 (Early Bird + Standard)", len(cats))
		}
		early := sl499FindByName(t, cats, "Early Bird")
		standard := sl499FindByName(t, cats, "Standard")
		compareKeys(t, "categoryList[Early Bird]", early, goldCats[0])
		compareKeys(t, "categoryList[Standard]", standard, goldCats[1])
		sc1WantNumber(t, early, "price", 900)
		sc1WantNumber(t, standard, "price", 1250)
		// Spec §7.2: on a pure-GA session "is this category placed?" has no
		// answer, so the key is ABSENT — not false, which would claim a seat
		// map exists.
		for _, c := range cats {
			if v, present := c["placement"]; present {
				t.Errorf("categoryList[%v].placement = %#v; a pure-GA session must omit the key entirely",
					c["categoryPriceName"], v)
			}
		}
		// The seeded GA pool carries tier_id NULL, so neither tier owns units
		// of its own. Reporting 0 here would contradict GET_ALL_ACTIONS, which
		// advertises the session-level 50 for the very same tiers.
		sc1WantNumber(t, early, "availability", 50)
		sc1WantNumber(t, standard, "availability", 50)

		if seats := sc1Array(t, resp, "seatList"); len(seats) != 0 {
			t.Errorf("seatList = %#v, want [] on a session with no seating plan", seats)
		}
	})

	// ── a session outside the channel's org is -3, never a leaked hall ──────
	t.Run("out_of_scope_is_minus_3", func(t *testing.T) {
		// Real credentials of a DIFFERENT org: a bogus token would answer -4
		// and prove nothing about org scoping.
		otherFID, otherToken := sc1SeedForeignChannel(t, st)
		resp := postBil24(t, base, map[string]any{
			"command":       "GET_SEAT_LIST",
			"fid":           otherFID,
			"token":         otherToken,
			"locale":        "ru-RU",
			"actionEventId": seatedEventID,
		})
		if code := numberField(t, resp, "resultCode"); code != -3 {
			t.Errorf("cross-org GET_SEAT_LIST resultCode = %v, want -3 (description %v)", code, resp["description"])
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// helpers (sl499 prefix keeps them out of the shared package namespace)
// ─────────────────────────────────────────────────────────────────────────────

// sl499Post fills the fixture's placeholder credentials and actionEventId with
// live values and posts it, failing the test on any non-zero resultCode.
func sl499Post(t *testing.T, base string, st *harnessState, req map[string]any, actionEventID int64) map[string]interface{} {
	t.Helper()
	req["fid"] = st.ChannelFID
	req["token"] = st.ChannelToken
	req["actionEventId"] = actionEventID
	resp := postBil24(t, base, req)
	if code := numberField(t, resp, "resultCode"); code != 0 {
		t.Fatalf("GET_SEAT_LIST resultCode = %v, want 0 (description %v)", code, resp["description"])
	}
	return resp
}

// sl499HoldOneSeat flips exactly one of the seeded seats to 'held' and restores
// it afterwards, so the sub-tests stay order-independent. Returns the held
// seat's system_seat_id.
func sl499HoldOneSeat(t *testing.T, st *harnessState) int64 {
	t.Helper()
	ctx := context.Background()
	var seatID int64
	if err := st.Pool.QueryRow(ctx,
		`UPDATE session_seats SET status='held'
		  WHERE id = (SELECT id FROM session_seats
		               WHERE session_id = $1 AND status='available'
		               ORDER BY system_seat_id LIMIT 1)
		 RETURNING system_seat_id`,
		st.AssignedSessID,
	).Scan(&seatID); err != nil {
		t.Fatalf("hold one seeded seat: %v", err)
	}
	t.Cleanup(func() {
		if _, err := st.Pool.Exec(context.Background(),
			`UPDATE session_seats SET status='available' WHERE system_seat_id = $1`, seatID,
		); err != nil {
			t.Logf("restore held seat %d: %v", seatID, err)
		}
	})
	return seatID
}

// sl499SeedHybridSession creates a published event with ONE hybrid session at
// the seeded venue carrying BOTH kinds of inventory: four real kind='seat' rows
// under a "Balkon" tier and ten kind='ga_unit' rows under a "Stání" tier. The
// scenario-01 hybrid seeder cannot be reused — it materialises GA units only,
// and the whole point of the §7.2 hybrid shape is the two kinds side by side.
func sl499SeedHybridSession(t *testing.T, st *harnessState, start, end time.Time) string {
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
		eventID, st.OrgID, "W1-B4 Hybrid "+eventID.String()[:8],
	); err != nil {
		t.Fatalf("seed hybrid event: %v", err)
	}
	sc1RegisterEventCleanup(t, st, eventID, uuid.Nil)

	if _, err := st.Pool.Exec(ctx,
		`INSERT INTO sessions
		     (id, event_id, venue_id, start_at, end_at, capacity_total,
		      status, admission_mode, seating_plan_version_id,
		      currency, currency_source)
		 VALUES ($1, $2, $3, $4, $5, 14, 'scheduled', 'hybrid', $6,
		         'CZK', 'override')`,
		sessID, eventID, st.VenueID, start, end, planVersionID,
	); err != nil {
		t.Fatalf("seed hybrid session: %v", err)
	}

	// sort_order fixes the categoryList order the golden spells out.
	var seatedTierID uuid.UUID
	if err := st.Pool.QueryRow(ctx,
		`INSERT INTO ticket_tiers (session_id, name, pricing_mode,
		     price_amount, currency, sort_order)
		 VALUES ($1, 'Balkon', 'fixed', 1500, 'CZK', 0)
		 RETURNING id`, sessID,
	).Scan(&seatedTierID); err != nil {
		t.Fatalf("seed hybrid seated tier: %v", err)
	}
	var gaTierID uuid.UUID
	if err := st.Pool.QueryRow(ctx,
		`INSERT INTO ticket_tiers (session_id, name, pricing_mode,
		     price_amount, currency, sort_order)
		 VALUES ($1, 'Stání', 'fixed', 700, 'CZK', 1)
		 RETURNING id`, sessID,
	).Scan(&gaTierID); err != nil {
		t.Fatalf("seed hybrid GA tier: %v", err)
	}

	if _, err := st.Pool.Exec(ctx,
		`INSERT INTO session_seats
		     (session_id, seat_key, sector_name, row_name, seat_number,
		      tier_id, status, kind)
		 SELECT $1, 'balkon|1|' || gs::text, 'Balkon', '1', gs::text,
		        $2, 'available', 'seat'
		 FROM generate_series(1, 4) gs`,
		sessID, seatedTierID,
	); err != nil {
		t.Fatalf("seed hybrid seats: %v", err)
	}
	if _, err := st.Pool.Exec(ctx,
		`INSERT INTO session_seats
		     (session_id, seat_key, sector_name, row_name, seat_number,
		      tier_id, status, kind)
		 SELECT $1, 'stani|pool|' || lpad(gs::text, 6, '0'), '', '', '',
		        $2, 'available', 'ga_unit'
		 FROM generate_series(1, 10) gs`,
		sessID, gaTierID,
	); err != nil {
		t.Fatalf("seed hybrid ga_units: %v", err)
	}
	return sessID.String()
}

func sl499FindByName(t *testing.T, cats []map[string]interface{}, name string) map[string]interface{} {
	t.Helper()
	for _, c := range cats {
		if c["categoryPriceName"] == name {
			return c
		}
	}
	t.Fatalf("categoryList carries no category named %q (got %v)", name, cats)
	return nil
}

func sl499WantBool(t *testing.T, m map[string]interface{}, key string, want bool) {
	t.Helper()
	got, ok := m[key].(bool)
	if !ok {
		t.Errorf("%s = %#v (%T), want the bool %v", key, m[key], m[key], want)
		return
	}
	if got != want {
		t.Errorf("%s = %v, want %v", key, got, want)
	}
}

func sl499WantEmptyObject(t *testing.T, m map[string]interface{}, key string) {
	t.Helper()
	got, ok := m[key].(map[string]interface{})
	if !ok {
		t.Errorf("%s = %#v (%T), want an object", key, m[key], m[key])
		return
	}
	if len(got) != 0 {
		t.Errorf("%s = %v, want {} — arena has no tariff plans", key, got)
	}
}
