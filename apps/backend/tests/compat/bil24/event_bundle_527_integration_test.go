//go:build integration

// event_bundle_527_integration_test.go — feature #527 (W1-E [EPIC-VERIFY]),
// event-bundle spec §9 scenarios 6 and 7.
//
// Scenarios 1-5 (create/idempotent-repeat/edit/id-errors/venue-by-name) are
// already proven at the himports package level, directly against the real
// handler, in event_bundle_525_integration_test.go — they do not need the
// Bil24 wire. Scenarios 6 and 7 are the two assertions that DO need it:
//
//   - Scenario 6: a published arena bundle must round-trip through the real
//     `hbil24` GET_ALL_ACTIONS command (the channel's fid/token) with exactly
//     the compat_ids the bundle response minted, day/time rendered in the
//     venue's timezone, and bigPosterUrl pointing at the canonical
//     /v1/media-files/{uuid} host (spec §7, §9.6).
//   - Scenario 7: the legacy alias route /imports/bil24-session rejects a
//     source=arena body with 422 import.source_mismatch (already pinned at
//     the unit level by himports/event_bundle_524_test.go) AND, unchanged by
//     this whole wave, still accepts a real Bil24-shaped payload with no
//     `source` field over the same real HTTP route (spec §9.7).
//
// This test drives the same org/channel/api-key pattern as
// scenario08_import_test.go (sc8ImportKey) and the same catalog-parsing
// helpers as scenario01_catalog_test.go, both in this package.
package compat_bil24_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// bundle527Fixture decodes the arena_ga_lampyris.json contract fixture into a
// generic map and randomizes every literal that feeds a global-unique column
// (externalRef, venue/action names) so a repeat run against the shared
// dev-stand never collides with a leftover row (AGENTS.md).
func bundle527Fixture(t *testing.T, posterURL string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(eventBundleFixturePath)
	if err != nil {
		t.Fatalf("read fixture %s: %v", eventBundleFixturePath, err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}

	suffix := uuid.New().String()[:8]
	body["externalRef"] = "wp:lampyris-staging:product:527-" + suffix

	action, ok := body["action"].(map[string]any)
	if !ok {
		t.Fatalf("fixture has no action object: %v", body)
	}
	action["actionName"] = "Harness 527 Tasting " + suffix
	action["fullActionName"] = "Harness 527 Wine & Light Tasting " + suffix
	action["bigPosterUrl"] = posterURL

	venue, ok := body["venue"].(map[string]any)
	if !ok {
		t.Fatalf("fixture has no venue object: %v", body)
	}
	venue["venueName"] = "Harness 527 Venue " + suffix

	return body
}

// bundle527RegisterCleanup tears down one bundle-created event/session in FK
// order. It mirrors sc8RegisterCleanup (scenario08_import_test.go) minus the
// seating-plan half: the arena-bundle fixture is pure general admission, so
// there is no seating plan and no reservation to sweep — but GA capacity does
// materialise as session_seats rows, which must still go.
func bundle527RegisterCleanup(t *testing.T, st *harnessState, eventID, sessionID uuid.UUID) {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		var venueID uuid.UUID
		if err := st.Pool.QueryRow(ctx,
			`SELECT venue_id FROM sessions WHERE id = $1`, sessionID).Scan(&venueID); err != nil {
			t.Logf("scenario-527 cleanup: read venue id: %v", err)
		}
		type stmt struct {
			sql string
			arg any
		}
		stmts := []stmt{
			{`DELETE FROM session_external_refs WHERE session_id = $1`, sessionID},
			{`DELETE FROM inventory_ledger WHERE session_id = $1`, sessionID},
			// The bundle is general admission, but GA capacity is still
			// materialised as session_seats rows of kind='ga_unit' — they FK
			// both the session and its tiers with no cascade, so they have to
			// go before either (feature #535: without this sweep the session,
			// event and venue deletes below all failed with 23503 and leaked
			// rows into the shared dev stand).
			{`DELETE FROM session_seats WHERE session_id = $1`, sessionID},
			{`DELETE FROM compatibility_id_map WHERE platform_id IN
			      (SELECT id FROM ticket_tiers WHERE session_id = $1)`, sessionID},
			{`DELETE FROM ticket_tiers WHERE session_id = $1`, sessionID},
			{`DELETE FROM compatibility_id_map WHERE platform_id = $1`, sessionID},
			{`DELETE FROM sessions WHERE id = $1`, sessionID},
			{`DELETE FROM compatibility_id_map WHERE platform_id = $1`, eventID},
			{`DELETE FROM events WHERE id = $1`, eventID},
		}
		if venueID != uuid.Nil {
			stmts = append(stmts,
				stmt{`DELETE FROM compatibility_id_map WHERE platform_id = $1`, venueID},
				stmt{`DELETE FROM venues WHERE id = $1`, venueID},
			)
		}
		for _, s := range stmts {
			if _, err := st.Pool.Exec(ctx, s.sql, s.arg); err != nil {
				t.Logf("scenario-527 cleanup %.60s… : %v", s.sql, err)
			}
		}
		// media_objects the poster side-load created are org-scoped, not
		// session-scoped, and safe to sweep independently.
		if _, err := st.Pool.Exec(ctx,
			`DELETE FROM media_objects WHERE org_id = $1::uuid AND owner_type = 'event_poster'
			     AND owner_id = $2::uuid`, st.OrgID, eventID,
		); err != nil {
			t.Logf("scenario-527 cleanup media_objects: %v", err)
		}
	})
}

// TestCompatBil24_527_EventBundleRoundTrip is spec §9 scenario 6: publish an
// arena bundle and prove GET_ALL_ACTIONS reports it with the same compat ids,
// venue-local day/time, and a canonical poster URL.
func TestCompatBil24_527_EventBundleRoundTrip(t *testing.T) {
	st := setupHarness(t)
	base := startHarnessServer(t, st)

	orgID, err := uuid.Parse(st.OrgID)
	if err != nil {
		t.Fatalf("parse st.OrgID: %v", err)
	}
	rawKey := sc8ImportKey(t, st, base, orgID)

	// A local poster server so the side-load in himports/import_exec.go
	// succeeds without reaching the public internet — the bigPosterUrl
	// assertion below depends on events.poster_media_id being set.
	posterBytes := []byte("\x89PNG\r\n\x1a\nharness-527")
	poster := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(posterBytes)
	}))
	defer poster.Close()

	body := bundle527Fixture(t, poster.URL+"/poster.png")

	status, resp := restJSON(t, base, "POST",
		"/v1/organizations/"+st.OrgID+"/imports/event-bundle", rawKey, nil, body)
	if status != 200 {
		t.Fatalf("POST imports/event-bundle status = %d, want 200 (body %v)", status, resp)
	}
	if created, _ := resp["created"].(bool); !created {
		t.Errorf("event-bundle created = %v, want true", resp["created"])
	}
	eventID := sc8UUIDField(t, resp, "event_id")
	sessionID := sc8UUIDField(t, resp, "session_id")
	bundle527RegisterCleanup(t, st, eventID, sessionID)

	compatIDs, ok := resp["compat_ids"].(map[string]interface{})
	if !ok {
		t.Fatalf("event-bundle response has no compat_ids object: %v", resp)
	}
	actionID := numberField(t, compatIDs, "action_id")
	actionEventID := numberField(t, compatIDs, "action_event_id")
	venueID := numberField(t, compatIDs, "venue_id")
	catIDsRaw, _ := compatIDs["category_price_ids"].([]interface{})
	if len(catIDsRaw) != 2 {
		t.Fatalf("compat_ids.category_price_ids = %v, want 2 entries", catIDsRaw)
	}
	const ceiling = float64(1_000_000_000)
	for _, id := range []float64{actionID, actionEventID, venueID} {
		if id < ceiling {
			t.Errorf("compat id %v is below the arena-minted ceiling %v", id, ceiling)
		}
	}

	// ── round-trip through the real hbil24 GET_ALL_ACTIONS command ──────────
	req, _ := loadWPFixture(t, "GET_ALL_ACTIONS", "basic")
	req["fid"] = st.ChannelFID
	req["token"] = st.ChannelToken
	catalog := postBil24(t, base, req)
	if code := numberField(t, catalog, "resultCode"); code != 0 {
		t.Fatalf("GET_ALL_ACTIONS resultCode = %v, want 0 (description %v)", code, catalog["description"])
	}

	action := sc1FindActionByEvent(t, catalog, actionEventID)
	sc1WantNumber(t, action, "actionId", actionID)

	events := sc1Objects(t, action, "actionEventList")
	event := sc1FindEvent(t, events, actionEventID)
	sc1WantNumber(t, event, "venueId", venueID)

	// The fixture's day/time (26.10.2026 19:00) is already the venue-local
	// wall clock in Europe/Prague (event-bundle spec §3), so the catalog must
	// echo it verbatim.
	sc1WantString(t, event, "day", "26.10.2026")
	sc1WantString(t, event, "time", "19:00")
	sc1WantString(t, event, "currency", "CZK")

	catLimits := sc1Objects(t, event, "categoryLimitList")
	if len(catLimits) != 1 {
		t.Fatalf("categoryLimitList has %d entries, want 1", len(catLimits))
	}
	cats := sc1Objects(t, catLimits[0], "categoryList")
	if len(cats) != 2 {
		t.Fatalf("categoryList has %d entries, want 2 (Standard + VIP)", len(cats))
	}
	wantNames := []string{"Standard", "VIP"}
	// Units round-trip losslessly (spec 20 §2): the bundle's MAJOR-unit price
	// is stored as minor units by ImportSessionCategory.PriceMinorUnits
	// (450 → 45000) and GET_ALL_ACTIONS converts back on the way out, so the
	// catalog quotes the very number the bundle declared.
	wantPrices := []float64{450, 900}
	wantIDs := make([]float64, len(catIDsRaw))
	for i, raw := range catIDsRaw {
		id, ok := raw.(float64)
		if !ok {
			t.Fatalf("category_price_ids[%d] = %#v, want a number", i, raw)
		}
		wantIDs[i] = id
	}
	for i, cat := range cats {
		sc1WantString(t, cat, "categoryPriceName", wantNames[i])
		sc1WantNumber(t, cat, "price", wantPrices[i])
		sc1WantNumber(t, cat, "categoryPriceId", wantIDs[i])
	}

	// bigPosterUrl must be the canonical media route, never the original
	// https://staging… URL the bundle supplied. Feature #535: it is now an
	// ABSOLUTE signed URL on API_PUBLIC_URL, because the site downloads the
	// artwork itself and /v1/media-files/{id} answers 401 without a signature.
	posterURL, _ := action["bigPosterUrl"].(string)
	if !strings.HasPrefix(posterURL, harnessAPIPublicBaseURL+"/v1/media-files/") {
		t.Errorf("actionList[0].bigPosterUrl = %q, want a %s/v1/media-files/{uuid} prefix",
			posterURL, harnessAPIPublicBaseURL)
	}
	if !strings.Contains(posterURL, "expires=") || !strings.Contains(posterURL, "sig=") {
		t.Errorf("actionList[0].bigPosterUrl = %q, want the mediastore expires+sig pair", posterURL)
	}
}

// TestCompatBil24_527_LegacyAliasSourceMismatch is spec §9 scenario 7: the
// legacy /imports/bil24-session route rejects a source=arena body with 422
// import.source_mismatch over the REAL HTTP route (the unit-level ladder
// check already lives in himports/event_bundle_524_test.go), and a genuine
// Bil24-shaped payload with no `source` field still succeeds unchanged.
func TestCompatBil24_527_LegacyAliasSourceMismatch(t *testing.T) {
	st := setupHarness(t)
	base := startHarnessServer(t, st)

	orgID, err := uuid.Parse(st.OrgID)
	if err != nil {
		t.Fatalf("parse st.OrgID: %v", err)
	}
	rawKey := sc8ImportKey(t, st, base, orgID)
	path := "/v1/organizations/" + st.OrgID + "/imports/bil24-session"

	// ── source=arena over the legacy alias → 422 import.source_mismatch ─────
	arenaBody := bundle527Fixture(t, "")
	status, resp := restJSON(t, base, "POST", path, rawKey, nil, arenaBody)
	if status != 422 {
		t.Fatalf("source=arena over legacy alias: status = %d, want 422 (body %v)", status, resp)
	}
	errObj, _ := resp["error"].(map[string]interface{})
	if code, _ := errObj["code"].(string); code != "import.source_mismatch" {
		t.Errorf("error.code = %v, want import.source_mismatch (body %v)", errObj["code"], resp)
	}

	// ── a genuine Bil24 payload (no `source` field) still imports fine ──────
	f := sc8NewFixture()
	bilPayload := sc8Payload(f, st)
	statusOK, respOK := restJSON(t, base, "POST", path, rawKey, nil, bilPayload)
	if statusOK != 200 {
		t.Fatalf("legacy bil24 payload: status = %d, want 200 (body %v)", statusOK, respOK)
	}
	eventID := sc8UUIDField(t, respOK, "event_id")
	sessionID := sc8UUIDField(t, respOK, "session_id")
	sc8RegisterCleanup(t, st, eventID, sessionID)
	if created, _ := respOK["created"].(bool); !created {
		t.Errorf("legacy bil24 payload: created = %v, want true", respOK["created"])
	}
}
