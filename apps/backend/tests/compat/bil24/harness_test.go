//go:build integration

// harness_test.go — feature #450 (W1-0) contract harness skeleton.
//
// This file boots the real arena_new HTTP server against a live PostgreSQL
// database and runs the 10 wire-contract scenarios defined in
// 08_architecture/18_bil24_compat_wave1_specification_ru.md §15.3.
//
// STATUS TODAY: every scenario calls t.Skip with the feature id that will
// implement the corresponding command. As those features land they replace
// the Skip line with the real assertion. The harness itself is green until
// then, which is the "done" bar for feature #450 (see backlog entry).
//
// Golden-response comparison rules (spec §15.2):
//   - STRICT key-set: a missing OR extra key fails the test.
//   - Placeholders in golden files ({{actionEventId}}, {{seatId:Parter-3-12}},
//     {{orderId}}, {{now+ttl}}, {{sessionId}}, {{token}}) are resolved by the
//     harness from seeded state before comparison.
//   - Numbers are compared as JSON numbers (float64), so integer/float drift
//     inside the JSON encoder is a defect.
//   - Never edit a golden file to make a test go green — extend the spec or
//     stop and ask.
//
// Prerequisites:
//   - DATABASE_URL=postgres://arena:arena@localhost:55432/arena?sslmode=disable
//   - JWT_SIGNING_SECRET=<anything> (config loader demands it even in tests)
//   - Migrations applied (Integration CI job does this automatically).
package compat_bil24_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ─────────────────────────────────────────────────────────────────────────────
// harness state
// ─────────────────────────────────────────────────────────────────────────────

// harnessState is the seeded fixture shared by every scenario. The concrete
// seed helpers land alongside the features that consume them (#451 seeds
// org/channel, #453 seeds venue, #454 seeds event, etc.). Until then the
// struct exists to document the target shape.
type harnessState struct {
	OrgID          string
	ChannelFID     int64  // = sales_channels.display_number
	ChannelToken   string // raw token; bcrypt is stored server-side
	VenueID        string
	VenueTimezone  string // e.g. "Europe/Prague"
	EventID        string
	AssignedSessID string
	// AssignedTierID is the CZK 500 tier stamped on every seat of the
	// assigned-seats session (feature #484) — it is what gives a §7.4
	// RESERVATION response a non-zero sum / charge / totalSum.
	AssignedTierID string
	GAsessID       string
	SeatIDs        map[string]string // "Parter-3-12" → system_seat_id
	// Pool is the seeded connection pool. Scenarios that boot the real
	// server (harness_server_test.go) hand it to httpserver.Options.PgxPool
	// and use it for the few out-of-band assertions (expiring a gateway
	// session, minting a compatibility id) that have no wire command.
	Pool *pgxpool.Pool
}

// setupHarness reads DATABASE_URL, opens a pgxpool, seeds the fixture via
// seedHarness (seed_test.go, feature #470) and returns the resulting state.
// Individual scenarios that un-skip are expected to boot the real
// httpserver.Server on top of the seeded state; that server bootstrap lands
// with the scenario that first needs it (each t.Skip line names the id).
func setupHarness(t *testing.T) *harnessState {
	t.Helper()
	if os.Getenv("DATABASE_URL") == "" {
		t.Skip("harness requires DATABASE_URL; see harness_test.go docs")
	}
	if os.Getenv("JWT_SIGNING_SECRET") == "" {
		t.Skip("harness requires JWT_SIGNING_SECRET; see harness_test.go docs")
	}
	// seedHarness lives in seed_test.go (feature #470). It creates the
	// full fixture (org + channel + venue + published event + one
	// assigned-seats session bound to Palác Akropolis + one GA session +
	// tiers + promo code) and registers cleanup via t.Cleanup.
	return seedHarness(t)
}

// ─────────────────────────────────────────────────────────────────────────────
// fixture loader (request + golden)
// ─────────────────────────────────────────────────────────────────────────────

// loadWPFixture returns the request and matching golden payload for a
// (command, case) pair, e.g. ("RESERVATION", "reserve_by_seat"). Both files
// are read from testdata/wp/{requests,golden}/<COMMAND>/<case>.json.
func loadWPFixture(t *testing.T, command, caseName string) (request, golden map[string]interface{}) {
	t.Helper()
	reqPath := filepath.Join("testdata", "wp", "requests", command, caseName+".json")
	gldPath := filepath.Join("testdata", "wp", "golden", command, caseName+".json")
	request = mustReadJSON(t, reqPath)
	golden = mustReadJSON(t, gldPath)
	return
}

func mustReadJSON(t *testing.T, path string) map[string]interface{} {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return m
}

// harnessTTLSeconds mirrors hcheckout.DefaultReservationTTL (1200s) so a
// golden may carry the symbolic {{now+ttl}} form for cartTimeout. Spec §7.4
// requires cartTimeout to be an integer on the wire, so the RESERVATION
// goldens spell the number out; the placeholder stays supported for goldens
// that prefer the symbolic form.
const harnessTTLSeconds = 1200

// resolveGolden replaces the documented placeholders in a golden object with
// values from the harness state. Placeholders that only exist at request time
// ({{sessionId}} is the gateway session token minted by CREATE_USER,
// {{actionEventId}} / {{categoryPriceId}} are compatibility_id_map bigints
// minted on first projection) are supplied by the calling scenario through
// `runtime`; anything it does not override falls back to a seed value.
func resolveGolden(g map[string]interface{}, st *harnessState, runtime ...map[string]string) map[string]interface{} {
	over := map[string]string{}
	for _, m := range runtime {
		for k, v := range m {
			over[k] = v
		}
	}
	subst := func(v, key, fallback string) string {
		repl := fallback
		if o, ok := over[key]; ok {
			repl = o
		}
		return strings.ReplaceAll(v, "{{"+key+"}}", repl)
	}
	return walk(g, func(v string) string {
		// Spec §4: actionEventId travels as the int64 compatibility id of the
		// SESSION (not the event, and not a UUID). The seeded session uuid is
		// only a fallback for scenarios that have not minted the id yet.
		v = subst(v, "actionEventId", st.AssignedSessID)
		v = subst(v, "categoryPriceId", st.AssignedTierID)
		// Spec §7.3: sessionId is the gateway session token, not a platform
		// session uuid.
		v = subst(v, "sessionId", "")
		v = subst(v, "token", st.ChannelToken)
		v = subst(v, "orderId", "")
		v = subst(v, "now+ttl", strconv.Itoa(harnessTTLSeconds))
		for label, seat := range st.SeatIDs {
			v = strings.ReplaceAll(v, "{{seatId:"+label+"}}", seat)
		}
		return v
	}).(map[string]interface{})
}

func walk(v interface{}, fn func(string) string) interface{} {
	switch x := v.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(x))
		for k, sub := range x {
			out[k] = walk(sub, fn)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(x))
		for i, sub := range x {
			out[i] = walk(sub, fn)
		}
		return out
	case string:
		return fn(x)
	default:
		return v
	}
}

// assertGoldenKeySet performs the STRICT key-set comparison required by spec
// §15.2 between an actual response and a (placeholder-resolved) golden. It
// reports missing and extra keys per level.
func assertGoldenKeySet(t *testing.T, actual, expected map[string]interface{}) {
	t.Helper()
	compareKeys(t, "", actual, expected)
}

func compareKeys(t *testing.T, path string, got, want map[string]interface{}) {
	t.Helper()
	extra, missing := diffKeys(got, toSet(mapKeysOf(want)))
	if len(extra) > 0 {
		t.Errorf("%s: extra keys %v", pathOr(path, "<root>"), extra)
	}
	if len(missing) > 0 {
		t.Errorf("%s: missing keys %v", pathOr(path, "<root>"), missing)
	}
	for k, sub := range want {
		gv, ok := got[k]
		if !ok {
			continue
		}
		if wm, isMap := sub.(map[string]interface{}); isMap {
			if gm, isGotMap := gv.(map[string]interface{}); isGotMap {
				compareKeys(t, path+"."+k, gm, wm)
			} else {
				t.Errorf("%s.%s: expected object, got %T", pathOr(path, ""), k, gv)
			}
		}
	}
}

func mapKeysOf(m map[string]interface{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func pathOr(p, fallback string) string {
	if p == "" {
		return fallback
	}
	return p
}

// ─────────────────────────────────────────────────────────────────────────────
// spec §15.3 — 10 scenarios (each t.Skip'd with the implementing feature id)
// ─────────────────────────────────────────────────────────────────────────────

// TestCompatBil24_450_Harness_Scenarios runs the 10 wire-contract scenarios
// as sub-tests. Each sub-test's first line is a t.Skip that names the feature
// id which will implement it. Removing the Skip line is how a subsequent
// feature adopts the scenario.
func TestCompatBil24_450_Harness_Scenarios(t *testing.T) {
	st := setupHarness(t)

	// Scenario skip ids retargeted by feature #470 (W1-A1a) against the
	// post-W1-split backlog in 09_autoforge/wp_bil24_compat_backlog.md.
	// The mapping is: 1→#497, 2→#495, 3→#484, 4→#509, 5→#494, 6→#492,
	// 7→#501, 8→#518, 9→#514, 10→#520. Removing the Skip line is how the
	// implementing feature adopts the scenario.
	// Scenario 1 (feature #497, spec §7.1) boots the real server and walks the
	// nested GET_ALL_ACTIONS catalog. The implementation lives in
	// scenario01_catalog_test.go because it seeds extra fixtures of its own.
	t.Run("01_catalog_get_all_actions", func(t *testing.T) {
		runScenario01Catalog(t, st)
	})

	t.Run("02_ga_purchase_flow", func(t *testing.T) {
		t.Skip("feature #495: CREATE_USER→RESERVE×2→GET_CART→ADD_PROMO_CODES→CREATE_ORDER_EXT→PAY_ORDER→GET_TICKETS_BY_ORDER end-to-end")
	})

	// Scenario 3 (feature #484, spec §7.4) is the first scenario to boot the
	// real server — the bootstrap lives in harness_server_test.go. It walks
	// the four RESERVATION shapes over one session cart: RESERVE by seat,
	// a parallel RESERVE of the same seat from a second gateway session
	// (resultCode 101), UN_RESERVE_ALL, and finally a command on a
	// backdated gateway session (resultCode 1).
	t.Run("03_seated_seats_conflict_and_expiry", func(t *testing.T) {
		base := startHarnessServer(t, st)

		// The wire speaks int64 (spec §4): actionEventId is the compat id of
		// the platform session UUID, never the UUID itself.
		actionEventID := mustActionEventID(t, st, st.AssignedSessID)
		labels := sortedSeatLabels(st)
		if len(labels) == 0 {
			t.Fatal("seed produced no seats for the assigned-seats session")
		}
		seatLabel := labels[0]
		seatID := st.SeatIDs[seatLabel]

		// ── step 1: buyer A opens a gateway session ──────────────────────
		sessA, userA := createGatewayUser(t, base, st, "harness-484-a@example.test")

		runtime := map[string]string{
			"actionEventId": strconv.FormatInt(actionEventID, 10),
			"sessionId":     sessA,
		}

		// ── step 2: RESERVE the seat over the checked-in wire fixture ────
		reqReserve, gldReserve := loadWPFixture(t, "RESERVATION", "reserve_by_seat")
		reqReserve = resolveGolden(reqReserve, st, runtime)
		reqReserve["fid"] = st.ChannelFID
		reqReserve["token"] = st.ChannelToken
		reqReserve["userId"] = userA
		// The fixture spells the seat as {{seatId:Parter-3-12}}; the seed's
		// first sorted seat is what actually exists, so retarget both the
		// request and the golden onto it.
		reqReserve["seatList"] = []any{map[string]any{"seatId": seatID}}

		resp := postBil24(t, base, reqReserve)
		if code := numberField(t, resp, "resultCode"); code != 0 {
			t.Fatalf("RESERVE resultCode = %v, want 0 (description %v)", code, resp["description"])
		}
		gldReserve = resolveGolden(gldReserve, st, runtime)
		assertGoldenKeySet(t, resp, gldReserve)

		// Spec §7.4 values: one CZK 500 seat, 5% channel fee.
		if got, want := resp["currency"], "CZK"; got != want {
			t.Errorf("RESERVE currency = %v, want %v", got, want)
		}
		if got := numberField(t, resp, "sum"); got != 500 {
			t.Errorf("RESERVE sum = %v, want 500", got)
		}
		if got := numberField(t, resp, "discount"); got != 0 {
			t.Errorf("RESERVE discount = %v, want 0", got)
		}
		if got := numberField(t, resp, "charge"); got != 25 {
			t.Errorf("RESERVE charge = %v, want 25 (5%% of 500)", got)
		}
		if got := numberField(t, resp, "totalSum"); got != 525 {
			t.Errorf("RESERVE totalSum = %v, want 525", got)
		}
		// cartTimeout is an integer number of seconds (spec §7.4), never a
		// timestamp and never a string.
		ct := numberField(t, resp, "cartTimeout")
		if ct <= 0 || ct > harnessTTLSeconds {
			t.Errorf("RESERVE cartTimeout = %v, want 0 < n <= %d", ct, harnessTTLSeconds)
		}
		if ct != float64(int64(ct)) {
			t.Errorf("RESERVE cartTimeout = %v, want an integer", ct)
		}
		seats, ok := resp["seatList"].([]interface{})
		if !ok || len(seats) != 1 {
			t.Fatalf("RESERVE seatList = %#v, want exactly one row", resp["seatList"])
		}
		row, _ := seats[0].(map[string]interface{})
		wantSeat, err := strconv.ParseFloat(seatID, 64)
		if err != nil {
			t.Fatalf("seed seat id %q is not an int64 literal: %v", seatID, err)
		}
		if got := numberField(t, row, "seatId"); got != wantSeat {
			t.Errorf("RESERVE seatList[0].seatId = %v, want %v", got, wantSeat)
		}

		// ── step 3: buyer B races for the same seat → 101, localized ─────
		sessB, userB := createGatewayUser(t, base, st, "harness-484-b@example.test")
		conflict := postBil24(t, base, map[string]any{
			"command":       "RESERVATION",
			"fid":           st.ChannelFID,
			"token":         st.ChannelToken,
			"locale":        "en-US",
			"type":          "RESERVE",
			"userId":        userB,
			"sessionId":     sessB,
			"actionEventId": actionEventID,
			"seatList":      []any{map[string]any{"seatId": seatID}},
		})
		if code := numberField(t, conflict, "resultCode"); code != 101 {
			t.Fatalf("parallel RESERVE resultCode = %v, want 101 (description %v)",
				code, conflict["description"])
		}
		assertGoldenKeySet(t, conflict,
			resolveGolden(mustReadJSON(t,
				filepath.Join("testdata", "wp", "golden", "RESERVATION", "seat_taken.json")), st, runtime))
		if desc, _ := conflict["description"].(string); desc == "" {
			t.Error("parallel RESERVE: description must carry the localized user-visible reason")
		}

		// ── step 4: buyer A empties the cart with UN_RESERVE_ALL ─────────
		reqAll, gldAll := loadWPFixture(t, "RESERVATION", "un_reserve_all")
		reqAll = resolveGolden(reqAll, st, runtime)
		reqAll["fid"] = st.ChannelFID
		reqAll["token"] = st.ChannelToken
		reqAll["userId"] = userA
		delete(reqAll, "actionEventId") // §7.4: UN_RESERVE_ALL carries none

		cleared := postBil24(t, base, reqAll)
		if code := numberField(t, cleared, "resultCode"); code != 0 {
			t.Fatalf("UN_RESERVE_ALL resultCode = %v, want 0 (description %v)",
				code, cleared["description"])
		}
		assertGoldenKeySet(t, cleared, resolveGolden(gldAll, st, runtime))
		if got := numberField(t, cleared, "sum"); got != 0 {
			t.Errorf("UN_RESERVE_ALL sum = %v, want 0", got)
		}
		if got := numberField(t, cleared, "totalSum"); got != 0 {
			t.Errorf("UN_RESERVE_ALL totalSum = %v, want 0", got)
		}
		if got := numberField(t, cleared, "cartTimeout"); got != 0 {
			t.Errorf("UN_RESERVE_ALL cartTimeout = %v, want 0 on an empty cart", got)
		}
		if left, _ := cleared["seatList"].([]interface{}); len(left) != 0 {
			t.Errorf("UN_RESERVE_ALL seatList = %#v, want empty", cleared["seatList"])
		}

		// ── step 5: the released seat is free again for buyer B ──────────
		regained := postBil24(t, base, map[string]any{
			"command":       "RESERVATION",
			"fid":           st.ChannelFID,
			"token":         st.ChannelToken,
			"locale":        "en-US",
			"type":          "RESERVE",
			"userId":        userB,
			"sessionId":     sessB,
			"actionEventId": actionEventID,
			"seatList":      []any{map[string]any{"seatId": seatID}},
		})
		if code := numberField(t, regained, "resultCode"); code != 0 {
			t.Fatalf("RESERVE after UN_RESERVE_ALL resultCode = %v, want 0 (description %v)",
				code, regained["description"])
		}

		// ── step 6: a stale gateway session is rejected with 1 ───────────
		expireGatewaySession(t, st, sessA)
		stale := postBil24(t, base, map[string]any{
			"command":       "RESERVATION",
			"fid":           st.ChannelFID,
			"token":         st.ChannelToken,
			"locale":        "en-US",
			"type":          "RESERVE",
			"userId":        userA,
			"sessionId":     sessA,
			"actionEventId": actionEventID,
			"seatList":      []any{map[string]any{"seatId": st.SeatIDs[labels[len(labels)-1]]}},
		})
		if code := numberField(t, stale, "resultCode"); code != 1 {
			t.Fatalf("RESERVE on expired session resultCode = %v, want 1 (description %v)",
				code, stale["description"])
		}
	})

	t.Run("04_refund_dedup", func(t *testing.T) {
		t.Skip("feature #509: REFUND_TICKET fires ticket.refunded to wpstub AND MACS-stub; repeat → dedup; orders.status updates")
	})

	t.Run("05_expired_hold_on_pay_order", func(t *testing.T) {
		t.Skip("feature #494: PAY_ORDER on expired hold → ReacquireHold success path AND failure path (manual_review + operator alert)")
	})

	t.Run("06_one_open_order_rule", func(t *testing.T) {
		t.Skip("feature #492: two sequential CREATE_ORDER_EXT for the same session return the same orderId (one-open-order invariant)")
	})

	// Scenario 7 (feature #501, W1-B5b) — GET /compat/bil24/image.
	//
	// The seat picker downloads the plan as a plain cacheable asset before it
	// can draw anything, so this scenario asserts the three things the picker
	// actually depends on: the sbt/1.0 element shape of spec §8, the composite
	// ETag revalidating to 304, and every <circle sbt:cat> pointing at a
	// category index that <metadata> really declares (a dangling index makes
	// the picker render seats it cannot price). The GA and wrong-`type` 404s
	// are asserted too: they must be byte-identical to "does not exist", which
	// is what keeps the unauthenticated route non-enumerable.
	t.Run("07_svg_image_and_etag", func(t *testing.T) {
		// Sanity: the SVG skeleton is checked in.
		if _, err := os.Stat(filepath.Join("testdata", "wp", "svg", "palac_akropolis.sbt.svg")); err != nil {
			t.Fatalf("SVG skeleton missing: %v", err)
		}

		st := setupHarness(t)
		base := startHarnessServer(t, st)
		actionEventID := mustActionEventID(t, st, st.AssignedSessID)

		// ── step 1: the plan downloads with sbt/1.0 shape and caching ────
		status, hdr, body := getBil24Image(t, base, map[string]string{
			"type":          "seatingPlan",
			"actionEventId": strconv.FormatInt(actionEventID, 10),
			"userId":        "0",
			"fid":           strconv.FormatInt(st.ChannelFID, 10),
			"locale":        "en-US",
		}, "")
		if status != 200 {
			t.Fatalf("GET image status = %d, want 200 (body %s)", status, body)
		}
		if ct := hdr.Get("Content-Type"); !strings.HasPrefix(ct, "image/svg+xml") {
			t.Errorf("Content-Type = %q, want image/svg+xml…", ct)
		}
		if cc := hdr.Get("Cache-Control"); cc != "no-cache" {
			t.Errorf("Cache-Control = %q, want no-cache", cc)
		}
		etag := hdr.Get("ETag")
		if etag == "" {
			t.Fatal("response carries no ETag; the picker cannot revalidate")
		}
		// Spec §8: ETag = "<geometry_checksum>:<seat_status_version>" — both
		// halves are required, geometry alone would serve a stale free/taken
		// bitmap after every reservation.
		if !strings.HasPrefix(etag, `"`) || !strings.HasSuffix(etag, `"`) ||
			!strings.Contains(etag, ":") {
			t.Errorf("ETag = %s, want a strong \"checksum:version\" validator", etag)
		}

		svg := string(body)
		for _, want := range []string{
			`xmlns:sbt="http://www.w3.org/2015/sbt/1.0"`,
			`sbt:statusVersion="`,
			`<metadata>`,
			`<sbt:category `,
			`sbt:sect=`,
			`sbt:row=`,
			`<circle sbt:id=`,
			`sbt:state=`,
			`sbt:cat=`,
		} {
			if !strings.Contains(svg, want) {
				t.Errorf("sbt/1.0 body is missing %q\n---\n%s", want, truncateSVG(svg))
			}
		}

		// ── step 2: every sbt:cat resolves to a declared sbt:index ───────
		declared := attrValues(svg, `<sbt:category `, `sbt:index="`)
		if len(declared) == 0 {
			t.Fatalf("no <sbt:category sbt:index> in <metadata>\n---\n%s", truncateSVG(svg))
		}
		known := make(map[string]bool, len(declared))
		for _, idx := range declared {
			known[idx] = true
		}
		used := attrValues(svg, `<circle `, `sbt:cat="`)
		if len(used) == 0 {
			t.Fatalf("no <circle sbt:cat> in the plan\n---\n%s", truncateSVG(svg))
		}
		for _, cat := range used {
			if !known[cat] {
				t.Errorf("circle sbt:cat=%q has no matching <sbt:category sbt:index>; declared %v",
					cat, declared)
			}
		}
		// Seat states use the two-value spec §8 alphabet only.
		for _, state := range attrValues(svg, `<circle `, `sbt:state="`) {
			if state != "1" && state != "4" {
				t.Errorf("circle sbt:state=%q, want 1 (free) or 4 (taken)", state)
			}
		}

		// ── step 3: If-None-Match revalidates to 304 with no body ────────
		status304, hdr304, body304 := getBil24Image(t, base, map[string]string{
			"type":          "seatingPlan",
			"actionEventId": strconv.FormatInt(actionEventID, 10),
			"userId":        "0",
			"fid":           strconv.FormatInt(st.ChannelFID, 10),
			"locale":        "en-US",
		}, etag)
		if status304 != 304 {
			t.Fatalf("If-None-Match status = %d, want 304 (body %s)", status304, body304)
		}
		if len(body304) != 0 {
			t.Errorf("304 body = %q, want empty", body304)
		}
		if got := hdr304.Get("ETag"); got != etag {
			t.Errorf("304 ETag = %s, want %s — a cache needs it to refresh its entry", got, etag)
		}

		// ── step 4: a GA session must be indistinguishable from missing ──
		gaActionEventID := mustActionEventID(t, st, st.GAsessID)
		gaStatus, _, _ := getBil24Image(t, base, map[string]string{
			"type":          "seatingPlan",
			"actionEventId": strconv.FormatInt(gaActionEventID, 10),
			"userId":        "0",
			"fid":           strconv.FormatInt(st.ChannelFID, 10),
			"locale":        "en-US",
		}, "")
		if gaStatus != 404 {
			t.Errorf("GA session image status = %d, want 404 — a GA session has no plan "+
				"and must not be distinguishable from an unknown id", gaStatus)
		}

		// ── step 5: an unsupported artefact kind is a 404, not a 400 ─────
		badType, _, _ := getBil24Image(t, base, map[string]string{
			"type":          "poster",
			"actionEventId": strconv.FormatInt(actionEventID, 10),
			"userId":        "0",
			"fid":           strconv.FormatInt(st.ChannelFID, 10),
			"locale":        "en-US",
		}, "")
		if badType != 404 {
			t.Errorf("type=poster status = %d, want 404", badType)
		}

		// ── step 6: an unknown fid may not read another org's plan ───────
		badFID, _, _ := getBil24Image(t, base, map[string]string{
			"type":          "seatingPlan",
			"actionEventId": strconv.FormatInt(actionEventID, 10),
			"userId":        "0",
			"fid":           strconv.FormatInt(st.ChannelFID+7777, 10),
			"locale":        "en-US",
		}, "")
		if badFID != 404 {
			t.Errorf("unknown fid status = %d, want 404", badFID)
		}
	})

	t.Run("08_bil24_session_import_idempotent", func(t *testing.T) {
		t.Skip("feature #518: POST /v1/organizations/{org}/imports/bil24-session twice with same payload → created:false on 2nd; GET_SEAT_LIST preserves Bil24 seatIds")
	})

	t.Run("09_api_keys_service_scope", func(t *testing.T) {
		t.Skip("feature #514: service API keys with scope limits: cross-org → 403; revoked → 401")
	})

	t.Run("10_customer_import_c7_dry_run_then_apply", func(t *testing.T) {
		t.Skip("feature #520: dry-run on bil24_orders_pseudonymized.json → report; apply → idempotent")
	})
}

// TestCompatBil24_450_Harness_ScenarioCoverage guards against future PRs
// silently dropping a scenario. Every §15.3 scenario id must remain named
// in this file.
func TestCompatBil24_450_Harness_ScenarioCoverage(t *testing.T) {
	want := []string{
		"01_catalog_get_all_actions",
		"02_ga_purchase_flow",
		"03_seated_seats_conflict_and_expiry",
		"04_refund_dedup",
		"05_expired_hold_on_pay_order",
		"06_one_open_order_rule",
		"07_svg_image_and_etag",
		"08_bil24_session_import_idempotent",
		"09_api_keys_service_scope",
		"10_customer_import_c7_dry_run_then_apply",
	}
	src, err := os.ReadFile("harness_test.go")
	if err != nil {
		t.Fatalf("read self: %v", err)
	}
	body := string(src)
	for _, name := range want {
		if !strings.Contains(body, `t.Run("`+name+`"`) {
			t.Errorf("scenario %q missing from harness_test.go — spec §15.3 requires all 10", name)
		}
	}
}
