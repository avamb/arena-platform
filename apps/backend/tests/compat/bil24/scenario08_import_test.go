//go:build integration

// scenario08_import_test.go — spec §15.3 scenario 8, the Bil24 session import
// (feature #518, W1-C3d; spec §13.2 steps 6 and 10).
//
// The scenario drives POST /v1/organizations/{org_id}/imports/bil24-session as
// a real service actor (an api key carrying the import.bil24_session scope)
// with a HYBRID payload — an sbt/1.0 svg with placed seats plus one
// placement:false quota category — and then proves the three properties the
// WordPress side depends on:
//
//   - importing the SAME payload twice is idempotent: created:false on the
//     second call, and the seating plan version / seat set is reused verbatim
//     rather than re-materialized (spec §13.2 step 10);
//   - the ORIGINAL Bil24 identities survive the round trip: GET_SEAT_LIST puts
//     the payload's seatList[].seatId on the wire and the sbt image renders the
//     same values as sbt:id;
//   - the imported session actually sells: RESERVATION/RESERVE takes a hold on
//     one of the imported seats, priced through the platform pipeline.
//
// The svg is authored inline instead of reusing testdata/wp/svg/
// palac_akropolis.sbt.svg on purpose: that golden's <sbt:category sbt:id>
// values are ≥ 1e9, above the compat ceiling ImportSessionRequest.
// ValidateExternalIDs enforces on categoryPriceId, so its categories could
// never be keyed onto ticket tiers by an import.
//
// KNOWN GAP (deliberate, not a defect of this scenario): the spec §15.3 text
// continues past RESERVATION into CREATE_ORDER_EXT → PAY_ORDER and asserts the
// original ids in the v1.order.paid event. Neither command can be exercised
// today — PAY_ORDER is absent from the gateway dispatch table
// (hbil24/bil24_compat.go) and handleBil24CreateOrderExt (hbil24/cmd_order.go)
// is a stub answering resultCode -5 NOT_IMPLEMENTED. Both belong to feature
// #495 (scenario 2), which is itself still skipped. This scenario therefore
// stops at the hold and must be extended when #495 lands.
package compat_bil24_test

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
)

// sc8Fixture is the identity block of one import run. Every id is derived from
// a single random base so a repeated run against the shared dev-stand database
// never collides with a leftover compatibility_id_map row, while staying below
// the 1e9 compat ceiling (bil24compat.ExternalIDCeiling).
type sc8Fixture struct {
	actionID      int64
	actionEventID int64
	venueID       int64
	seatedCatID   int64
	gaCatID       int64
	seatIDs       []int64 // the four placed seats, in svg order
	blockedSeat   int64   // seatIDs[3] — imported with available:false
}

func sc8NewFixture() sc8Fixture {
	// uuid.New().ID() is a uint32 drawn from the same CSPRNG the harness
	// already trusts for its other unique literals.
	base := int64(100_000_000) + int64(uuid.New().ID()%700_000_000)
	f := sc8Fixture{
		actionID:      base,
		actionEventID: base + 1,
		venueID:       base + 2,
		seatedCatID:   base + 3,
		gaCatID:       base + 4,
	}
	for i := 1; i <= 4; i++ {
		f.seatIDs = append(f.seatIDs, base+int64(100+i))
	}
	f.blockedSeat = f.seatIDs[3]
	return f
}

// sc8SVG renders a minimal but fully valid sbt/1.0 document: one category, one
// sector, one row, four seats. The seat sbt:id values are the Bil24 seat ids
// the import must carry into session_seats.system_seat_id verbatim.
func sc8SVG(f sc8Fixture) string {
	var seats strings.Builder
	for i, id := range f.seatIDs {
		fmt.Fprintf(&seats,
			`      <circle sbt:id="%d" sbt:state="1" sbt:cat="1" sbt:seat="%d" cx="%d" cy="20" r="6" fill="#e53935"/>`+"\n",
			id, i+1, 20+i*30)
	}
	return `<svg xmlns="http://www.w3.org/2000/svg" xmlns:sbt="` + sbtNamespaceForTest + `" viewBox="0 0 200 100" sbt:statusVersion="1">
  <metadata>
    <sbt:category sbt:id="` + strconv.FormatInt(f.seatedCatID, 10) + `" sbt:index="1" sbt:name="Parter" sbt:color="#e53935" sbt:price="900"/>
  </metadata>
  <g sbt:sect="Parter">
    <g sbt:row="1">
` + seats.String() + `    </g>
  </g>
</svg>
`
}

// sbtNamespaceForTest mirrors seating.SBTNamespaceURI. It is spelled out here
// so the fixture documents the wire contract instead of importing the domain
// package purely for a string constant.
const sbtNamespaceForTest = "http://www.w3.org/2015/sbt/1.0"

// sc8Payload builds the import body. publish:true is load-bearing: the sbt
// image route filters on events.status = 'published' (GetPublicSessionSchema),
// so without it the image assertion below could only ever see a 404.
func sc8Payload(f sc8Fixture, st *harnessState) map[string]any {
	start := time.Now().Add(45 * 24 * time.Hour).UTC()
	seatList := make([]map[string]any, 0, len(f.seatIDs))
	for i, id := range f.seatIDs {
		seatList = append(seatList, map[string]any{
			"seatId":          id,
			"categoryPriceId": f.seatedCatID,
			"location": map[string]any{
				"sector": "Parter",
				"row":    "1",
				"number": strconv.Itoa(i + 1),
			},
			// The last seat arrives sold-out from Bil24 and must land as a
			// blocked ('unavailable') arena seat plus an import.seats_blocked
			// warning — never as a silently on-sale seat.
			"available": id != f.blockedSeat,
		})
	}
	return map[string]any{
		"action": map[string]any{
			"actionId":       f.actionID,
			"actionName":     "Harness #518 hybrid import",
			"fullActionName": "Harness #518 hybrid import — sbt round trip",
			"description":    "Imported by the spec §15.3 scenario 8 harness.",
			"age":            "12+",
			"organizerName":  "W1 Harness",
		},
		"actionEvent": map[string]any{
			"actionEventId":   f.actionEventID,
			"day":             start.Format("02.01.2006"),
			"time":            start.Format("15:04"),
			"currency":        "CZK",
			"sellEndTime":     start.Add(2 * time.Hour).Format(time.RFC3339),
			"chargePercent":   5,
			"seatingPlanId":   f.venueID,
			"seatingPlanName": "Harness 518 hall " + strconv.FormatInt(f.venueID, 10),
		},
		"venue": map[string]any{
			"venueId":     f.venueID,
			"venueName":   "Harness 518 venue " + strconv.FormatInt(f.venueID, 10),
			"address":     "Kubelíkova 27",
			"cityName":    "Praha",
			"countryName": "CZ",
			"timezone":    st.VenueTimezone,
		},
		"categoryList": []map[string]any{
			{
				"categoryPriceId":   f.seatedCatID,
				"categoryPriceName": "Parter",
				"price":             900,
				"placement":         true,
				"availability":      len(f.seatIDs),
			},
			{
				// placement:false + availability > 0 is what makes the
				// imported session HYBRID (spec §13.2 step 6).
				"categoryPriceId":   f.gaCatID,
				"categoryPriceName": "Standing",
				"price":             500,
				"placement":         false,
				"availability":      30,
			},
		},
		"seatList": seatList,
		"svg":      sc8SVG(f),
		"publish":  true,
	}
}

// runScenario08Import is the body of the 08_bil24_session_import_idempotent
// sub-test.
func runScenario08Import(t *testing.T, st *harnessState) {
	t.Helper()
	base := startHarnessServer(t, st)

	orgID, err := uuid.Parse(st.OrgID)
	if err != nil {
		t.Fatalf("parse st.OrgID: %v", err)
	}
	rawKey := sc8ImportKey(t, st, base, orgID)

	f := sc8NewFixture()
	payload := sc8Payload(f, st)
	path := "/v1/organizations/" + st.OrgID + "/imports/bil24-session"

	// ── first import: the session is created, the plan bound, seats made ─────
	status, first := restJSON(t, base, "POST", path, rawKey, nil, payload)
	if status != 200 {
		t.Fatalf("first import status = %d, want 200 (body %v)", status, first)
	}
	eventID := sc8UUIDField(t, first, "event_id")
	sessionID := sc8UUIDField(t, first, "session_id")
	sc8RegisterCleanup(t, st, eventID, sessionID)

	if created, _ := first["created"].(bool); !created {
		t.Fatalf("first import created = %v, want true (body %v)", first["created"], first)
	}
	planVersion, _ := first["seating_plan_version_id"].(string)
	if planVersion == "" {
		t.Fatalf("first import seating_plan_version_id is empty; the svg must have produced a plan version (body %v)", first)
	}
	// Four placed seats plus the 30-unit standing quota. GA units are
	// session_seats rows with a "ga|" key prefix, not a separate table, so the
	// materialized count legitimately covers both halves of a hybrid session.
	const wantSeats = 34
	if got := numberField(t, first, "seats_materialized"); got != wantSeats {
		t.Errorf("first import seats_materialized = %v, want %d (4 placed seats + 30 GA units)", got, wantSeats)
	}
	if !sc8HasWarning(first, "import.seats_blocked") {
		t.Errorf("first import warnings = %v, want an import.seats_blocked entry for the available:false seat", first["warnings"])
	}
	tierIDs, _ := first["tier_ids"].(map[string]interface{})
	for _, want := range []int64{f.seatedCatID, f.gaCatID} {
		if _, ok := tierIDs[strconv.FormatInt(want, 10)]; !ok {
			t.Errorf("tier_ids has no entry for categoryPriceId %d: %v", want, tierIDs)
		}
	}

	// ── second import of the SAME payload: idempotent ────────────────────────
	status2, second := restJSON(t, base, "POST", path, rawKey, nil, payload)
	if status2 != 200 {
		t.Fatalf("second import status = %d, want 200 (body %v)", status2, second)
	}
	if created, _ := second["created"].(bool); created {
		t.Errorf("second import created = true, want false — the endpoint is idempotent on actionEvent.actionEventId")
	}
	if got := second["event_id"]; got != first["event_id"] {
		t.Errorf("second import event_id = %v, want %v", got, first["event_id"])
	}
	if got := second["session_id"]; got != first["session_id"] {
		t.Errorf("second import session_id = %v, want %v", got, first["session_id"])
	}
	if got := second["seating_plan_version_id"]; got != planVersion {
		t.Errorf("second import seating_plan_version_id = %v, want %v — an unchanged "+
			"geometry must REUSE the bound version, never append a new one", got, planVersion)
	}
	if got := numberField(t, second, "seats_materialized"); got != wantSeats {
		t.Errorf("second import seats_materialized = %v, want %d", got, wantSeats)
	}

	// ── GET_SEAT_LIST speaks the ORIGINAL Bil24 seat ids ─────────────────────
	seatResp := postBil24(t, base, map[string]any{
		"command":       "GET_SEAT_LIST",
		"fid":           st.ChannelFID,
		"token":         st.ChannelToken,
		"locale":        "en-US",
		"actionEventId": f.actionEventID,
	})
	if code := numberField(t, seatResp, "resultCode"); code != 0 {
		t.Fatalf("GET_SEAT_LIST resultCode = %v, want 0 (description %v)", code, seatResp["description"])
	}
	if got := seatResp["admissionMode"]; got != "hybrid" {
		t.Errorf("GET_SEAT_LIST admissionMode = %v, want hybrid (placed seats + a placement:false category)", got)
	}
	wireStatus := map[int64]float64{}
	rows, _ := seatResp["seatList"].([]interface{})
	for _, raw := range rows {
		row, _ := raw.(map[string]interface{})
		id, ok := row["seatId"].(float64)
		if !ok {
			continue
		}
		wireStatus[int64(id)] = numberField(t, row, "status")
	}
	for _, id := range f.seatIDs {
		st8, ok := wireStatus[id]
		if !ok {
			t.Errorf("GET_SEAT_LIST does not carry imported Bil24 seatId %d; the import "+
				"must preserve the upstream identity (got %v)", id, seatResp["seatList"])
			continue
		}
		// BSS status codes (spec §7.2): 0 unavailable, 1 available.
		want := float64(1)
		if id == f.blockedSeat {
			want = 0
		}
		if st8 != want {
			t.Errorf("GET_SEAT_LIST seat %d status = %v, want %v", id, st8, want)
		}
	}

	// ── the sbt image renders the same ids ───────────────────────────────────
	imgStatus, hdr, body := getBil24Image(t, base, map[string]string{
		"type":          "seatingPlan",
		"actionEventId": strconv.FormatInt(f.actionEventID, 10),
		"userId":        "0",
		"fid":           strconv.FormatInt(st.ChannelFID, 10),
		"locale":        "en-US",
	}, "")
	if imgStatus != 200 {
		t.Fatalf("GET image status = %d, want 200 (body %s)", imgStatus, body)
	}
	if ct := hdr.Get("Content-Type"); !strings.HasPrefix(ct, "image/svg+xml") {
		t.Errorf("GET image Content-Type = %q, want image/svg+xml…", ct)
	}
	rendered := map[string]struct{}{}
	for _, v := range attrValues(string(body), "<circle ", `sbt:id="`) {
		rendered[v] = struct{}{}
	}
	for _, id := range f.seatIDs {
		if _, ok := rendered[strconv.FormatInt(id, 10)]; !ok {
			t.Errorf("rendered plan has no circle with sbt:id=%d; the image must echo the "+
				"imported Bil24 ids so the WP seat picker can talk back in them", id)
		}
	}

	// ── the imported session sells: RESERVATION over an imported seat ────────
	gwSession, userID := createGatewayUser(t, base, st, "harness-518-"+uuid.NewString()[:8]+"@example.test")
	hold := postBil24(t, base, map[string]any{
		"command":       "RESERVATION",
		"fid":           st.ChannelFID,
		"token":         st.ChannelToken,
		"locale":        "en-US",
		"type":          "RESERVE",
		"userId":        userID,
		"sessionId":     gwSession,
		"actionEventId": f.actionEventID,
		"seatList":      []any{map[string]any{"seatId": f.seatIDs[0]}},
	})
	if code := numberField(t, hold, "resultCode"); code != 0 {
		t.Fatalf("RESERVE on the imported session resultCode = %v, want 0 (description %v)",
			code, hold["description"])
	}
	heldRows, _ := hold["seatList"].([]interface{})
	if len(heldRows) != 1 {
		t.Fatalf("RESERVE seatList = %#v, want exactly one held seat", hold["seatList"])
	}
	heldRow, _ := heldRows[0].(map[string]interface{})
	if got := numberField(t, heldRow, "seatId"); got != float64(f.seatIDs[0]) {
		t.Errorf("RESERVE held seatId = %v, want the imported Bil24 id %d", got, f.seatIDs[0])
	}
	// The imported category price (900) is stored in MINOR units by
	// ImportSessionCategory.PriceMinorUnits (feature #517, pinned by
	// TestPriceMinorUnits), so ticket_tiers.price_amount is 90000. The cart
	// projection puts price_amount on the wire verbatim — seed_test.go seeds
	// price_amount=500 and scenario 3 asserts sum==500 — so the hold reports
	// 90000 here.
	//
	// NOTE (cross-feature, deliberately asserted as-is rather than "fixed"
	// here): those two conventions disagree. Real Bil24 payloads carry MAJOR
	// units on the wire (testdata/wp/bil24_orders_pseudonymized.json shows
	// sums like 1710 CZK), so an imported 900 CZK category is quoted to the
	// WordPress basket as 90000. The fix belongs to whichever of the import
	// (#517) or the cart projection (#484/#485) owns the unit contract; both
	// sides are currently pinned by their own tests, and silently changing one
	// from a scenario test would hide the disagreement instead of surfacing it.
	if got := numberField(t, hold, "sum"); got != 90000 {
		t.Errorf("RESERVE sum = %v, want 90000 (the imported Parter price in minor units)", got)
	}
	if got := numberField(t, hold, "totalSum"); got != 94500 {
		t.Errorf("RESERVE totalSum = %v, want 94500 (90000 + 5%% channel fee)", got)
	}

	// ── a blocked seat is genuinely off sale ─────────────────────────────────
	refused := postBil24(t, base, map[string]any{
		"command":       "RESERVATION",
		"fid":           st.ChannelFID,
		"token":         st.ChannelToken,
		"locale":        "en-US",
		"type":          "RESERVE",
		"userId":        userID,
		"sessionId":     gwSession,
		"actionEventId": f.actionEventID,
		"seatList":      []any{map[string]any{"seatId": f.blockedSeat}},
	})
	if code := numberField(t, refused, "resultCode"); code == 0 {
		t.Errorf("RESERVE of the available:false seat %d succeeded; a Bil24-blocked seat "+
			"must not be sellable (body %v)", f.blockedSeat, refused)
	}
}

// sc8ImportKey mints an org-admin user + membership and issues a service api
// key carrying the spec §13.1 scope set (which includes import.bil24_session).
// It mirrors scenario 9 — the import endpoint's primary caller is exactly that
// site-side service key, not a logged-in human.
func sc8ImportKey(t *testing.T, st *harnessState, base string, orgID uuid.UUID) string {
	t.Helper()
	ctx := context.Background()

	userID := uuid.New()
	if _, err := st.Pool.Exec(ctx,
		`INSERT INTO users (id, email, password_hash, email_verified_at)
		 VALUES ($1, $2, 'x', now())`,
		userID, "harness-518-admin-"+userID.String()[:8]+"@example.test",
	); err != nil {
		t.Fatalf("seed import admin user: %v", err)
	}
	t.Cleanup(func() {
		if _, err := st.Pool.Exec(context.Background(),
			`DELETE FROM users WHERE id = $1`, userID); err != nil {
			t.Logf("cleanup import admin user: %v", err)
		}
	})

	if _, err := st.Pool.Exec(ctx,
		`INSERT INTO memberships (user_id, org_id, role) VALUES ($1, $2, 'organizer')`,
		userID, orgID,
	); err != nil {
		t.Fatalf("seed import admin membership: %v", err)
	}
	t.Cleanup(func() {
		if _, err := st.Pool.Exec(context.Background(),
			`DELETE FROM memberships WHERE user_id = $1 AND org_id = $2`, userID, orgID); err != nil {
			t.Logf("cleanup import admin membership: %v", err)
		}
	})

	stub := harnessStubAuth(t)
	adminJWT, _, err := stub.IssueToken(ctx, auth.IssueRequest{
		ActorID: userID.String(),
		Roles:   []string{"org_admin"},
		TTL:     time.Hour,
	})
	if err != nil {
		t.Fatalf("mint org-admin jwt: %v", err)
	}

	status, resp := restJSON(t, base, "POST", "/v1/organizations/"+st.OrgID+"/api-keys", adminJWT,
		map[string]string{"X-Admin-Reason": "feature #518 scenario 8 harness"},
		map[string]any{"name": "W1 import key", "scopes": sc9ScopeSet})
	if status != 201 {
		t.Fatalf("POST api-keys status = %d, want 201 (body %v)", status, resp)
	}
	keyObj, _ := resp["api_key"].(map[string]interface{})
	rawKey, _ := keyObj["api_key"].(string)
	keyID, _ := keyObj["id"].(string)
	if rawKey == "" || keyID == "" {
		t.Fatalf("POST api-keys response missing api_key/id: %v", resp)
	}
	// api_keys.created_by FK-references the user above and DELETE only
	// revokes, so the row must go first — t.Cleanup is LIFO, hence this
	// registration comes last.
	t.Cleanup(func() {
		if _, err := st.Pool.Exec(context.Background(),
			`DELETE FROM api_keys WHERE id = $1`, keyID); err != nil {
			t.Logf("cleanup import api key: %v", err)
		}
	})
	return rawKey
}

// sc8RegisterCleanup tears down everything one import created, in FK order:
// holds → seats → tiers → session → event → seating plan → venue, plus the
// compatibility_id_map rows that key them. Registered as soon as the ids are
// known so a failure mid-scenario still leaves the shared dev-stand clean.
// sc8Stmt is one single-argument cleanup statement.
type sc8Stmt struct {
	sql string
	arg any
}

func sc8RegisterCleanup(t *testing.T, st *harnessState, eventID, sessionID uuid.UUID) {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		// venue_id is read before the session row goes away.
		var venueID uuid.UUID
		if err := st.Pool.QueryRow(ctx,
			`SELECT venue_id FROM sessions WHERE id = $1`, sessionID).Scan(&venueID); err != nil {
			t.Logf("scenario-08 cleanup: read venue id: %v", err)
		}
		stmts := []sc8Stmt{
			{`DELETE FROM reservation_seats WHERE reservation_id IN
			      (SELECT id FROM reservations WHERE session_id = $1)`, sessionID},
			{`DELETE FROM reservation_ga_items WHERE reservation_id IN
			      (SELECT id FROM reservations WHERE session_id = $1)`, sessionID},
			// session_seats.reservation_id FK-references reservations, so the
			// seats must go before the holds that point at them.
			{`DELETE FROM session_seats WHERE session_id = $1`, sessionID},
			{`DELETE FROM reservations WHERE session_id = $1`, sessionID},
			{`DELETE FROM compatibility_id_map WHERE platform_id IN
			      (SELECT id FROM ticket_tiers WHERE session_id = $1)`, sessionID},
			{`DELETE FROM ticket_tiers WHERE session_id = $1`, sessionID},
			{`DELETE FROM inventory_ledger WHERE session_id = $1`, sessionID},
			// The session row goes before the plan versions it references —
			// sessions_seated_requires_plan forbids detaching the version from
			// a seated session, so the FK must be resolved by deletion.
			{`DELETE FROM compatibility_id_map WHERE platform_id = $1`, sessionID},
			{`DELETE FROM sessions WHERE id = $1`, sessionID},
			{`DELETE FROM compatibility_id_map WHERE platform_id = $1`, eventID},
			{`DELETE FROM events WHERE id = $1`, eventID},
		}
		if venueID != uuid.Nil {
			stmts = append(stmts,
				sc8Stmt{`UPDATE seating_plans SET current_version_id = NULL WHERE venue_id = $1`, venueID},
				// Safe only because the session rows above are already gone:
				// sessions.seating_plan_version_id FK-references these.
				sc8Stmt{`DELETE FROM seating_plan_versions WHERE seating_plan_id IN
				       (SELECT id FROM seating_plans WHERE venue_id = $1)`, venueID},
				sc8Stmt{`DELETE FROM seating_plans WHERE venue_id = $1`, venueID},
				sc8Stmt{`DELETE FROM compatibility_id_map WHERE platform_id = $1`, venueID},
				sc8Stmt{`DELETE FROM venues WHERE id = $1`, venueID},
			)
		}
		for _, s := range stmts {
			if _, err := st.Pool.Exec(ctx, s.sql, s.arg); err != nil {
				t.Logf("scenario-08 cleanup %.60s… : %v", s.sql, err)
			}
		}
	})
}

// sc8UUIDField reads a UUID-valued response field, failing the test when it is
// missing or malformed.
func sc8UUIDField(t *testing.T, resp map[string]interface{}, key string) uuid.UUID {
	t.Helper()
	raw, _ := resp[key].(string)
	if raw == "" {
		t.Fatalf("import response has no %q: %v", key, resp)
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		t.Fatalf("import response %s = %q is not a uuid: %v", key, raw, err)
	}
	return id
}

// sc8HasWarning reports whether the import response carries a warning code.
func sc8HasWarning(resp map[string]interface{}, code string) bool {
	list, _ := resp["warnings"].([]interface{})
	for _, raw := range list {
		w, _ := raw.(map[string]interface{})
		if c, _ := w["code"].(string); c == code {
			return true
		}
	}
	return false
}
