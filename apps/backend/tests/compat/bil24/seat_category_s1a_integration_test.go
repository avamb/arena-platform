//go:build integration

// seat_category_s1a_integration_test.go - execution spec 40, task S1a.
//
// After PATCH /v1/event-sessions/{id}/seats/category moved seats to another
// tier, the three renders a BUYER sees must show ONE price per seat, the one
// of the seat's own session_seats.tier_id:
//
//	widget          GET /v1/event-sessions/{id}/schema (geometry seat
//	                category_index + seat_category_index + category_prices +
//	                extra_categories, folded the way the widget does)
//	Bil24 image     GET /compat/bil24/image (sbt:cat -> sbt:category sbt:price)
//	Bil24 seat list GET_SEAT_LIST seatList[].price
//
// Before S1a the first two anchored a whole plan category on its FIRST seat's
// tier, so a repainted seat kept the old category and price there while
// GET_SEAT_LIST (which reads each seat's own tier) already showed the new one.
package compat_bil24_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
)

var (
	s1aImageCat  = regexp.MustCompile(`<sbt:category sbt:id="(\d+)" sbt:index="(\d+)" [^>]*sbt:price="([^"]*)"`)
	s1aImageSeat = regexp.MustCompile(`<circle sbt:id="(\d+)" sbt:state="\d" sbt:cat="(\d+)"`)
)

// s1aSchema is the part of the /schema response the widget folds together.
type s1aSchema struct {
	Geometry struct {
		Sections []struct {
			Rows []struct {
				Seats []struct {
					Key           string `json:"key"`
					CategoryIndex int    `json:"category_index"`
				} `json:"seats"`
			} `json:"rows"`
		} `json:"sections"`
	} `json:"geometry"`
	CategoryPrices    []s1aCategory  `json:"category_prices"`
	SeatCategoryIndex map[string]int `json:"seat_category_index"`
	ExtraCategories   []s1aCategory  `json:"extra_categories"`
}

type s1aCategory struct {
	Index       int    `json:"index"`
	TierID      string `json:"tier_id"`
	PriceAmount *int64 `json:"price_amount"`
}

func TestS1a_CategoryPatch_OneSeatOnePriceInAllThreeRenders(t *testing.T) {
	st := setupHarness(t)
	base := startHarnessServer(t, st)
	ctx := context.Background()
	sessionID := st.AssignedSessID
	actionEventID := mustActionEventID(t, st, sessionID)

	// ── fixture: an org admin and a second tier that anchors no plan category
	orgID := uuid.MustParse(st.OrgID)
	userID := uuid.New()
	if _, err := st.Pool.Exec(ctx,
		`INSERT INTO users (id, email, password_hash, email_verified_at) VALUES ($1, $2, 'x', now())`,
		userID, "s1a-admin-"+userID.String()[:8]+"@example.test",
	); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = st.Pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID)
	})
	if _, err := st.Pool.Exec(ctx,
		`INSERT INTO memberships (user_id, org_id, role) VALUES ($1, $2, 'organizer')`, userID, orgID,
	); err != nil {
		t.Fatalf("seed membership: %v", err)
	}
	t.Cleanup(func() {
		_, _ = st.Pool.Exec(context.Background(),
			`DELETE FROM memberships WHERE user_id = $1 AND org_id = $2`, userID, orgID)
	})
	jwt, _, err := harnessStubAuth(t).IssueToken(ctx, auth.IssueRequest{
		ActorID: userID.String(), Roles: []string{"org_admin"}, TTL: time.Hour,
	})
	if err != nil {
		t.Fatalf("mint jwt: %v", err)
	}

	var premiumTier uuid.UUID
	if err := st.Pool.QueryRow(ctx,
		`INSERT INTO ticket_tiers (session_id, name, pricing_mode, price_amount, currency, sort_order)
		 VALUES ($1, 'Premium S1a', 'fixed', 80000, 'CZK', 1) RETURNING id`, sessionID,
	).Scan(&premiumTier); err != nil {
		t.Fatalf("seed premium tier: %v", err)
	}
	t.Cleanup(func() {
		// Seats move back to nothing: the session cleanup of the seed removes
		// session_seats first, so only the tier row is left to drop here.
		_, _ = st.Pool.Exec(context.Background(),
			`UPDATE session_seats SET tier_id = $2 WHERE session_id = $1 AND tier_id = $3`,
			sessionID, st.AssignedTierID, premiumTier)
		_, _ = st.Pool.Exec(context.Background(), `DELETE FROM ticket_tiers WHERE id = $1`, premiumTier)
	})

	// ── pick three seats from the whole hall, spread over the plan ───────────
	rows, err := st.Pool.Query(ctx,
		`SELECT seat_key, system_seat_id FROM session_seats
		  WHERE session_id = $1 AND kind = 'seat' ORDER BY seat_key`, sessionID)
	if err != nil {
		t.Fatalf("list seats: %v", err)
	}
	sysIDByKey := map[string]int64{}
	var keys []string
	for rows.Next() {
		var k string
		var id int64
		if err := rows.Scan(&k, &id); err != nil {
			t.Fatalf("scan seat: %v", err)
		}
		sysIDByKey[k] = id
		keys = append(keys, k)
	}
	rows.Close()
	if len(keys) < 30 {
		t.Fatalf("harness session has %d seats, want a real hall", len(keys))
	}
	moved := []string{keys[0], keys[len(keys)/2], keys[len(keys)-1]}

	status, resp := restJSON(t, base, "PATCH", "/v1/event-sessions/"+sessionID+"/seats/category", jwt,
		nil, map[string]any{"tier_id": premiumTier.String(), "seat_keys": moved})
	if status != http.StatusOK {
		t.Fatalf("PATCH seats/category status = %d, want 200 (body %v)", status, resp)
	}

	// ── render 1: widget schema, folded exactly as apps/widget does ──────────
	var schema s1aSchema
	getJSON(t, base+"/v1/event-sessions/"+sessionID+"/schema", &schema)
	priceByIndex := map[int]int64{}
	for _, c := range append(append([]s1aCategory{}, schema.CategoryPrices...), schema.ExtraCategories...) {
		if c.PriceAmount != nil {
			priceByIndex[c.Index] = *c.PriceAmount
		}
	}
	widgetPrice := map[string]int64{} // seat_key -> minor units
	for _, sec := range schema.Geometry.Sections {
		for _, row := range sec.Rows {
			for _, s := range row.Seats {
				idx := s.CategoryIndex
				if o, ok := schema.SeatCategoryIndex[s.Key]; ok {
					idx = o
				}
				widgetPrice[s.Key] = priceByIndex[idx]
			}
		}
	}

	// ── render 2: Bil24 sbt/1.0 image ────────────────────────────────────────
	status, _, body := getBil24Image(t, base, map[string]string{
		"type":          "seatingPlan",
		"actionEventId": strconv.FormatInt(actionEventID, 10),
		"userId":        "0",
		"fid":           strconv.FormatInt(st.ChannelFID, 10),
		"locale":        "en-US",
	}, "")
	if status != http.StatusOK {
		t.Fatalf("GET image status = %d, body %s", status, body)
	}
	imgCatPrice := map[int]string{}
	imgCatID := map[int]string{} // sbt:index -> sbt:id (the categoryPriceId the plugin sends back)
	for _, m := range s1aImageCat.FindAllStringSubmatch(string(body), -1) {
		i, _ := strconv.Atoi(m[2])
		imgCatPrice[i] = m[3]
		imgCatID[i] = m[1]
	}
	imagePrice := map[int64]string{} // system_seat_id -> major units text
	imageCatID := map[int64]string{} // system_seat_id -> sbt:id of its sbt:cat
	for _, m := range s1aImageSeat.FindAllStringSubmatch(string(body), -1) {
		id, _ := strconv.ParseInt(m[1], 10, 64)
		cat, _ := strconv.Atoi(m[2])
		p, ok := imgCatPrice[cat]
		if !ok {
			t.Fatalf("image: seat %d points at sbt:cat %d that <metadata> does not declare", id, cat)
		}
		imagePrice[id] = p
		imageCatID[id] = imgCatID[cat]
	}

	// ── render 3: GET_SEAT_LIST ──────────────────────────────────────────────
	req := map[string]any{
		"command": "GET_SEAT_LIST", "fid": st.ChannelFID, "token": st.ChannelToken,
		"actionEventId": actionEventID, "locale": "en-US",
	}
	listResp := postBil24(t, base, req)
	if code := numberField(t, listResp, "resultCode"); code != 0 {
		t.Fatalf("GET_SEAT_LIST resultCode = %v (%v)", code, listResp["description"])
	}
	listPrice := map[int64]float64{}
	listCatID := map[int64]string{}
	for _, seat := range sc1Objects(t, listResp, "seatList") {
		listPrice[int64(numberField(t, seat, "seatId"))] = numberField(t, seat, "price")
		listCatID[int64(numberField(t, seat, "seatId"))] = strconv.FormatInt(int64(numberField(t, seat, "categoryPriceId")), 10)
	}

	// ── one price per seat, in every render, for EVERY seat of the hall ──────
	movedSet := map[string]bool{moved[0]: true, moved[1]: true, moved[2]: true}
	for key, sysID := range sysIDByKey {
		want := int64(50000)
		if movedSet[key] {
			want = 80000
		}
		if got := widgetPrice[key]; got != want {
			t.Errorf("widget schema: seat %s price = %d, want %d", key, got, want)
		}
		if got, wantMajor := imagePrice[sysID], strconv.FormatInt(want/100, 10); got != wantMajor {
			t.Errorf("bil24 image: seat %s (id %d) price = %q, want %q", key, sysID, got, wantMajor)
		}
		if got := listPrice[sysID]; got != float64(want)/100 {
			t.Errorf("GET_SEAT_LIST: seat %s (id %d) price = %v, want %v", key, sysID, got, float64(want)/100)
		}
		// The plugin RESERVEs with the sbt:id of the seat's sbt:cat: it must be
		// a real wire id and the one GET_SEAT_LIST names for the same seat.
		if img, list := imageCatID[sysID], listCatID[sysID]; img == "0" || img != list {
			t.Errorf("seat %s (id %d): image category id %q, GET_SEAT_LIST categoryPriceId %q", key, sysID, img, list)
		}
	}
	// The tier that anchors no plan category came through as a synthetic one.
	if len(schema.ExtraCategories) != 1 || schema.ExtraCategories[0].TierID != premiumTier.String() {
		t.Errorf("schema extra_categories = %+v, want one entry for the premium tier", schema.ExtraCategories)
	}
	if len(schema.SeatCategoryIndex) != len(moved) {
		t.Errorf("schema seat_category_index has %d entries, want %d (only the repainted seats)",
			len(schema.SeatCategoryIndex), len(moved))
	}
}

// getJSON GETs url and decodes the 200 body into out.
func getJSON(t *testing.T, url string, out any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", url, err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s status = %d, body %s", url, resp.StatusCode, raw)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("decode %s: %v", url, err)
	}
}
