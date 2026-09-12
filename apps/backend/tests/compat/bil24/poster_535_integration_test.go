//go:build integration

// poster_535_integration_test.go — feature #535 (W1-S1a, spec
// 08_architecture/22_site_facing_gaps_w1s1_ru.md §2.1).
//
// Two site-facing links the WordPress plugin consumes were unusable before
// this feature, and both are proven end to end here against the real server:
//
//   - GET_ALL_ACTIONS.bigPosterUrl was a bare, host-relative
//     `/v1/media-files/{uuid}`. The site downloads the artwork itself, and
//     `GET /v1/media-files/{id}` answers 401 without an `expires`/`sig` pair,
//     so the artwork sync silently got nothing. The catalog now emits an
//     ABSOLUTE signed URL on API_PUBLIC_URL — this test takes whatever the
//     wire carried, re-issues it against the live server and asserts the
//     poster bytes come back with HTTP 200. The unsigned variant of the very
//     same path is asserted to still be rejected, so the 200 above proves the
//     signature is what carried the request, not a hole in the route.
//   - PUT .../gateway-credential answered with the bare origin, which the
//     operator pastes into the plugin's "Bil24 API URL" field — every command
//     then goes to `/` and 404s. It must now hand over the gateway ENDPOINTS.
//
// The event-bundle import with a side-loaded poster is the same fixture
// machinery scenario 6 uses (event_bundle_527_integration_test.go).
package compat_bil24_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
)

// TestCompatBil24_535_PosterURLIsFetchable is the spec §2.1 integration
// check: GET_ALL_ACTIONS → bigPosterUrl → GET on it through the test
// httpserver → 200 and the poster bytes.
func TestCompatBil24_535_PosterURLIsFetchable(t *testing.T) {
	st := setupHarness(t)
	base := startHarnessServer(t, st)

	orgID, err := uuid.Parse(st.OrgID)
	if err != nil {
		t.Fatalf("parse st.OrgID: %v", err)
	}
	rawKey := sc8ImportKey(t, st, base, orgID)

	// A local origin server for the artwork the bundle references, so the
	// side-load in himports/import_exec.go succeeds without reaching the
	// public internet. These exact bytes must come back out of the signed
	// URL at the end of the test.
	posterBytes := []byte("\x89PNG\r\n\x1a\nharness-535-poster")
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
	eventID := sc8UUIDField(t, resp, "event_id")
	sessionID := sc8UUIDField(t, resp, "session_id")
	bundle527RegisterCleanup(t, st, eventID, sessionID)

	compatIDs, ok := resp["compat_ids"].(map[string]interface{})
	if !ok {
		t.Fatalf("event-bundle response has no compat_ids object: %v", resp)
	}
	actionEventID := numberField(t, compatIDs, "action_event_id")

	// ── the wire value ──────────────────────────────────────────────────────
	req, _ := loadWPFixture(t, "GET_ALL_ACTIONS", "basic")
	req["fid"] = st.ChannelFID
	req["token"] = st.ChannelToken
	catalog := postBil24(t, base, req)
	if code := numberField(t, catalog, "resultCode"); code != 0 {
		t.Fatalf("GET_ALL_ACTIONS resultCode = %v, want 0 (description %v)", code, catalog["description"])
	}
	action := sc1FindActionByEvent(t, catalog, actionEventID)
	posterURL, _ := action["bigPosterUrl"].(string)
	if posterURL == "" {
		t.Fatalf("action has no bigPosterUrl: %v", action)
	}
	if !strings.HasPrefix(posterURL, harnessAPIPublicBaseURL+"/v1/media-files/") {
		t.Fatalf("bigPosterUrl = %q, want an absolute %s/v1/media-files/{uuid} URL",
			posterURL, harnessAPIPublicBaseURL)
	}
	u, err := url.Parse(posterURL)
	if err != nil {
		t.Fatalf("bigPosterUrl %q does not parse: %v", posterURL, err)
	}
	if u.Query().Get("expires") == "" || u.Query().Get("sig") == "" {
		t.Fatalf("bigPosterUrl = %q, want the mediastore expires+sig pair", posterURL)
	}

	// ── follow it: same path+query, this listener instead of the public
	// origin the config names (the harness cannot resolve api.harness.test).
	fetch := func(path string) (int, []byte) {
		t.Helper()
		resp, err := http.Get(base + path) //nolint:noctx // short-lived test fetch
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer func() { _ = resp.Body.Close() }()
		payload, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("read %s body: %v", path, err)
		}
		return resp.StatusCode, payload
	}

	gotStatus, gotBody := fetch(u.RequestURI())
	if gotStatus != http.StatusOK {
		t.Fatalf("GET %s (the signed bigPosterUrl) status = %d, want 200 (body %.200s)",
			u.RequestURI(), gotStatus, gotBody)
	}
	if !bytes.Equal(gotBody, posterBytes) {
		t.Errorf("signed bigPosterUrl returned %d bytes (%q), want the %d poster bytes (%q)",
			len(gotBody), gotBody, len(posterBytes), posterBytes)
	}

	// Negative control: without the signature the very same object is denied,
	// so the 200 above was earned by the signed URL and not by an open route.
	unsignedStatus, _ := fetch(u.Path)
	if unsignedStatus == http.StatusOK {
		t.Errorf("GET %s without expires/sig returned 200 — the media route must stay signature-gated", u.Path)
	}
}

// TestCompatBil24_535_GatewayCredentialURLs is the spec §2.1 operator-facing
// half: the one-shot PUT response must name the gateway ENDPOINTS on the API
// public origin, never the bare origin (which would make the plugin POST to
// `/`) and never the SPA origin.
func TestCompatBil24_535_GatewayCredentialURLs(t *testing.T) {
	st := setupHarness(t)
	base := startHarnessServer(t, st)
	ctx := context.Background()

	orgID, err := uuid.Parse(st.OrgID)
	if err != nil {
		t.Fatalf("parse st.OrgID: %v", err)
	}

	var channelID uuid.UUID
	if err := st.Pool.QueryRow(ctx,
		`SELECT id FROM sales_channels WHERE org_id = $1 AND display_number = $2`,
		orgID, st.ChannelFID,
	).Scan(&channelID); err != nil {
		t.Fatalf("look up the seeded sales channel: %v", err)
	}

	// An org-admin user + membership + JWT: `channel.update` reaches the
	// route through the Roles claim (DBChecker), the memberships row
	// satisfies the separate org-membership gate.
	userID := uuid.New()
	if _, err := st.Pool.Exec(ctx,
		`INSERT INTO users (id, email, password_hash, email_verified_at)
		 VALUES ($1, $2, 'x', now())`,
		userID, "harness-535-admin-"+userID.String()[:8]+"@example.test",
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

	adminJWT, _, err := harnessStubAuth(t).IssueToken(ctx, auth.IssueRequest{
		ActorID: userID.String(),
		Roles:   []string{"org_admin"},
		TTL:     time.Hour,
	})
	if err != nil {
		t.Fatalf("mint org-admin jwt: %v", err)
	}

	status, resp := restJSON(t, base, "PUT",
		"/v1/organizations/"+st.OrgID+"/channels/"+channelID.String()+"/gateway-credential",
		adminJWT, map[string]string{"X-Admin-Reason": "feature #535 harness"}, nil)
	if status != 200 {
		t.Fatalf("PUT gateway-credential status = %d, want 200 (body %v)", status, resp)
	}

	baseURL, _ := resp["base_url"].(string)
	imageURL, _ := resp["image_url"].(string)
	if want := harnessAPIPublicBaseURL + "/compat/bil24"; baseURL != want {
		t.Errorf("gateway-credential base_url = %q, want %q", baseURL, want)
	}
	if !strings.HasSuffix(baseURL, "/compat/bil24") {
		t.Errorf("gateway-credential base_url = %q, want a /compat/bil24 suffix", baseURL)
	}
	if want := harnessAPIPublicBaseURL + "/compat/bil24/image"; imageURL != want {
		t.Errorf("gateway-credential image_url = %q, want %q", imageURL, want)
	}
	if strings.HasPrefix(baseURL, harnessPublicBaseURL) || strings.HasPrefix(imageURL, harnessPublicBaseURL) {
		t.Errorf("gateway URLs must sit on the API origin, not the SPA origin %s: base_url=%q image_url=%q",
			harnessPublicBaseURL, baseURL, imageURL)
	}

	// The rotated token must actually work on the endpoint the response
	// names — otherwise the operator pastes a pair that cannot authenticate.
	token, _ := resp["token"].(string)
	if token == "" {
		t.Fatalf("gateway-credential response has no token: %v", resp)
	}
	req, _ := loadWPFixture(t, "GET_ALL_ACTIONS", "basic")
	req["fid"] = numberField(t, resp, "fid")
	req["token"] = token
	catalog := postBil24(t, base, req)
	if code := numberField(t, catalog, "resultCode"); code != 0 {
		t.Errorf("GET_ALL_ACTIONS with the rotated credential resultCode = %v, want 0 (description %v)",
			code, catalog["description"])
	}
}
