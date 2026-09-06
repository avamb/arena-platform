//go:build integration

// scenario09_api_keys_test.go — spec §15.3 scenario 9, service API keys with
// scope limits (feature #514, W1-C1c).
//
// The scenario exercises the org-admin api-keys CRUD surface end to end as a
// real user (JWT), then drives the spec §13.4 "no seats" catalog flow
// (create event -> session -> tiers -> publish, media step omitted per the
// AB-42 gate) as a real service actor (the minted api key). It then asserts
// the published event surfaces in GET_ALL_ACTIONS, that an org-A key gets a
// hard cross-org 403 against org B, and that a revoked key is rejected with
// 401 on the very next call.
package compat_bil24_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
)

// sc9ScopeSet is the exact spec §13.1 scope list for the "event center for a
// site" service key (18_bil24_compat_wave1_specification_ru.md lines
// 1052-1056). It grants everything the §13.4 no-seats flow touches plus the
// bil24-session importer scope, and deliberately nothing else.
var sc9ScopeSet = []string{
	"event.create", "event.read", "event.update",
	"event.publish", "session.create", "session.read", "session.update",
	"tier.create", "tier.read", "tier.update",
	"venue.read", "seating_plan.create", "seating_plan.read", "seating_plan.update.own",
	"event_session.assign_seating_plan", "media.write", "media.read", "import.bil24_session",
}

// runScenario09APIKeys is the body of the 09_api_keys_service_scope sub-test.
func runScenario09APIKeys(t *testing.T, st *harnessState) {
	t.Helper()
	ctx := context.Background()
	base := startHarnessServer(t, st)

	orgID, err := uuid.Parse(st.OrgID)
	if err != nil {
		t.Fatalf("parse st.OrgID: %v", err)
	}

	// ── fixture: a real org-admin user + membership in org A ────────────────
	userID := uuid.New()
	if _, err := st.Pool.Exec(ctx,
		`INSERT INTO users (id, email, password_hash, email_verified_at)
		 VALUES ($1, $2, 'x', now())`,
		userID, "harness-514-admin-"+userID.String()[:8]+"@example.test",
	); err != nil {
		t.Fatalf("seed org-admin user: %v", err)
	}
	t.Cleanup(func() {
		if _, err := st.Pool.Exec(context.Background(),
			`DELETE FROM users WHERE id = $1`, userID); err != nil {
			t.Logf("cleanup org-admin user: %v", err)
		}
	})

	if _, err := st.Pool.Exec(ctx,
		`INSERT INTO memberships (user_id, org_id, role) VALUES ($1, $2, 'organizer')`,
		userID, orgID,
	); err != nil {
		t.Fatalf("seed org-admin membership: %v", err)
	}
	t.Cleanup(func() {
		if _, err := st.Pool.Exec(context.Background(),
			`DELETE FROM memberships WHERE user_id = $1 AND org_id = $2`, userID, orgID); err != nil {
			t.Logf("cleanup org-admin membership: %v", err)
		}
	})

	// ── fixture: a second organization, the counterparty for the cross-org
	// 403 assertion. No channel/venue needed — the assertion only exercises
	// the org-scoping gate on POST .../events.
	orgBID := uuid.New()
	suffix := orgBID.String()[:8]
	if _, err := st.Pool.Exec(ctx,
		`INSERT INTO organizations (id, name, slug, legal_name)
		 VALUES ($1, $2, $3, $4)`,
		orgBID, "W1-514 Foreign Org "+suffix, "w1-514-"+suffix, "Foreign s.r.o.",
	); err != nil {
		t.Fatalf("seed org B: %v", err)
	}
	t.Cleanup(func() {
		if _, err := st.Pool.Exec(context.Background(),
			`DELETE FROM organizations WHERE id = $1`, orgBID); err != nil {
			t.Logf("cleanup org B: %v", err)
		}
	})

	// ── mint the org-admin JWT ───────────────────────────────────────────────
	// DBChecker.Check (rbac_checker.go) trusts a JWT's Roles claim directly —
	// no user_roles row is required to reach api_key.manage; the genuine
	// memberships row above satisfies hapikeys/orgauth.go's separate
	// org-membership gate.
	stub := harnessStubAuth(t)
	adminJWT, _, err := stub.IssueToken(ctx, auth.IssueRequest{
		ActorID: userID.String(),
		Roles:   []string{"org_admin"},
		TTL:     time.Hour,
	})
	if err != nil {
		t.Fatalf("mint org-admin jwt: %v", err)
	}

	// ── issue the service api key with the spec §13.1 scope set ────────────
	adminHeaders := map[string]string{"X-Admin-Reason": "feature #514 scenario 9 harness"}
	status, resp := restJSON(t, base, "POST", "/v1/organizations/"+st.OrgID+"/api-keys", adminJWT, adminHeaders,
		map[string]any{
			"name":   "W1 site gateway key",
			"scopes": sc9ScopeSet,
		})
	if status != 201 {
		t.Fatalf("POST api-keys status = %d, want 201 (body %v)", status, resp)
	}
	keyObj, ok := resp["api_key"].(map[string]interface{})
	if !ok {
		t.Fatalf("POST api-keys response has no api_key object: %v", resp)
	}
	rawKey, _ := keyObj["api_key"].(string)
	keyID, _ := keyObj["id"].(string)
	if rawKey == "" || keyID == "" {
		t.Fatalf("POST api-keys response missing api_key/id: %v", keyObj)
	}
	// DELETE /api-keys only revokes (sets revoked_at); the row itself still
	// FK-references the org-admin user via created_by, so it must be removed
	// before the user cleanup below runs (t.Cleanup is LIFO — registering
	// this after the user/membership cleanups guarantees it runs first).
	t.Cleanup(func() {
		if _, err := st.Pool.Exec(context.Background(),
			`DELETE FROM api_keys WHERE id = $1`, keyID); err != nil {
			t.Logf("cleanup api key: %v", err)
		}
	})

	// ── §13.4 no-seats flow, driven entirely by the freshly minted key ──────
	eStatus, eResp := restJSON(t, base, "POST", "/v1/organizations/"+st.OrgID+"/events", rawKey, nil,
		map[string]any{
			"name":       "Harness #514 no-seats event",
			"status":     "draft",
			"visibility": "public",
		})
	if eStatus != 201 {
		t.Fatalf("POST events (service key) status = %d, want 201 (body %v)", eStatus, eResp)
	}
	eventObj, _ := eResp["event"].(map[string]interface{})
	eventIDStr, _ := eventObj["id"].(string)
	if eventIDStr == "" {
		t.Fatalf("POST events response missing event.id: %v", eResp)
	}
	eventID, err := uuid.Parse(eventIDStr)
	if err != nil {
		t.Fatalf("parse created event id %q: %v", eventIDStr, err)
	}
	sc1RegisterEventCleanup(t, st, eventID, uuid.Nil)

	start := time.Now().Add(30 * 24 * time.Hour).UTC().Truncate(time.Second)
	end := start.Add(3 * time.Hour)
	sStatus, sResp := restJSON(t, base, "POST",
		"/v1/organizations/"+st.OrgID+"/events/"+eventIDStr+"/sessions", rawKey, nil,
		map[string]any{
			"venue_id":          st.VenueID,
			"start_at":          start.Format(time.RFC3339),
			"end_at":            end.Format(time.RFC3339),
			"admission_mode":    "general_admission",
			"capacity_override": 20,
		})
	if sStatus != 201 {
		t.Fatalf("POST sessions (service key) status = %d, want 201 (body %v)", sStatus, sResp)
	}
	sessionObj, _ := sResp["session"].(map[string]interface{})
	sessionIDStr, _ := sessionObj["id"].(string)
	if sessionIDStr == "" {
		t.Fatalf("POST sessions response missing session.id: %v", sResp)
	}

	tStatus, tResp := restJSON(t, base, "POST",
		"/v1/organizations/"+st.OrgID+"/events/"+eventIDStr+"/sessions/"+sessionIDStr+"/tiers", rawKey, nil,
		map[string]any{
			"name":         "GA",
			"pricing_mode": "fixed",
			"price_amount": 1000,
			"sort_order":   0,
		})
	if tStatus != 201 {
		t.Fatalf("POST tiers (service key) status = %d, want 201 (body %v)", tStatus, tResp)
	}

	// AB-42 publish gate: no media step required (poster is optional).
	pStatus, pResp := restJSON(t, base, "POST",
		"/v1/organizations/"+st.OrgID+"/events/"+eventIDStr+"/status", rawKey, nil,
		map[string]any{"status": "published"})
	if pStatus != 200 {
		t.Fatalf("POST events/status publish (service key) status = %d, want 200 (body %v)", pStatus, pResp)
	}

	// ── the published event must surface in GET_ALL_ACTIONS ─────────────────
	actionEventID := mustActionEventID(t, st, sessionIDStr)
	catalogResp := postBil24(t, base, map[string]any{
		"command": "GET_ALL_ACTIONS",
		"fid":     st.ChannelFID,
		"token":   st.ChannelToken,
		"locale":  "en-US",
	})
	if code := numberField(t, catalogResp, "resultCode"); code != 0 {
		t.Fatalf("GET_ALL_ACTIONS resultCode = %v, want 0 (description %v)", code, catalogResp["description"])
	}
	sc1FindActionByEvent(t, catalogResp, float64(actionEventID))

	// ── cross-org: the org-A key must never reach org B's catalog ───────────
	xStatus, xResp := restJSON(t, base, "POST", "/v1/organizations/"+orgBID.String()+"/events", rawKey, nil,
		map[string]any{
			"name":       "should never be created",
			"status":     "draft",
			"visibility": "public",
		})
	if xStatus != 403 {
		t.Fatalf("cross-org POST events status = %d, want 403 (body %v)", xStatus, xResp)
	}
	if errObj, _ := xResp["error"].(map[string]interface{}); errObj == nil || errObj["code"] != "org.access_denied" {
		t.Errorf("cross-org POST events error = %v, want code org.access_denied", xResp["error"])
	}

	// ── revoke the key, then confirm it is rejected outright ────────────────
	rStatus, rResp := restJSON(t, base, "DELETE",
		"/v1/organizations/"+st.OrgID+"/api-keys/"+keyID, adminJWT, adminHeaders, nil)
	if rStatus != 200 {
		t.Fatalf("DELETE api-keys status = %d, want 200 (body %v)", rStatus, rResp)
	}

	revokedStatus, revokedResp := restJSON(t, base, "POST",
		"/v1/organizations/"+st.OrgID+"/events", rawKey, nil,
		map[string]any{
			"name":       "should never be created either",
			"status":     "draft",
			"visibility": "public",
		})
	if revokedStatus != 401 {
		t.Fatalf("POST events with revoked key status = %d, want 401 (body %v)", revokedStatus, revokedResp)
	}
	if errObj, _ := revokedResp["error"].(map[string]interface{}); errObj == nil || errObj["code"] != "auth.invalid_token" {
		t.Errorf("revoked-key error = %v, want code auth.invalid_token", revokedResp["error"])
	}
}
