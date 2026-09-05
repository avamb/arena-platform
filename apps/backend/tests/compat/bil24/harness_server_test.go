//go:build integration

// harness_server_test.go — feature #484 (W1-A5b) live-server bootstrap for the
// §15.3 wire-contract harness.
//
// harness_test.go documents that "individual scenarios that un-skip are
// expected to boot the real httpserver.Server on top of the seeded state; that
// server bootstrap lands with the scenario that first needs it". Scenario 3
// (RESERVATION over the session cart, spec §7.4) is the first such scenario, so
// the bootstrap lands here.
//
// The server is the production composition root — httpserver.New over the same
// *pgxpool.Pool the seed wrote through, with BIL24_COMPAT_ENABLED and
// BIL24_REQUIRE_TOKEN on — served through httptest so no port is reserved.
// Nothing about the Bil24 code path is stubbed: requests travel real HTTP into
// the real chi router and the real hbil24 handler.

package compat_bil24_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/compatids"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver"
)

// harnessServerConfig is the minimal *config.Config httpserver.New consults.
// ActiveLocales carries the four spec §6 gateway locales so localized
// descriptions (ru/he goldens) resolve through the real i18n bundle.
func harnessServerConfig() *config.Config {
	return &config.Config{
		AppEnv:          config.EnvDevelopment,
		AppName:         "arena-api-bil24-harness",
		AppVersion:      "0.0.0-test",
		AppCommit:       "test",
		HTTPListenAddr:  "127.0.0.1:0",
		BodyLimitBytes:  1 << 20,
		RequestTimeout:  30 * time.Second,
		ShutdownTimeout: 5 * time.Second,
		DefaultLocale:   "en",
		ActiveLocales:   []string{"en", "ru", "cs", "he"},
		LogLevel:        "error",
		LogFormat:       "json",
	}
}

// startHarnessServer boots the real server over the seeded pool and returns the
// base URL of an httptest listener in front of its chi router. It also registers
// the cleanup for every row the wire scenarios create (gateway sessions, holds,
// customers, compatibility ids) so the seed's own cleanup — which runs after,
// t.Cleanup being LIFO — is not blocked by foreign keys.
func startHarnessServer(t *testing.T, st *harnessState) string {
	t.Helper()
	if st.Pool == nil {
		t.Fatal("startHarnessServer: harnessState.Pool is nil — seedHarness must expose the pool")
	}

	// Both Pool and PgxPool must be set: PgxPool feeds the *gen.Queries
	// fallbacks, while Pool feeds s.pool — the guard bil24_shims.go checks
	// before wiring CREATE_USER's customer store and the #484 cart deps.
	// With only PgxPool set, CREATE_USER self-gates with resultCode -99.
	srv := httpserver.New(httpserver.Options{
		Config:             harnessServerConfig(),
		Logger:             slog.New(slog.NewJSONHandler(io.Discard, nil)),
		Pool:               st.Pool,
		PgxPool:            st.Pool,
		Bil24CompatEnabled: true,
		Bil24RequireToken:  true,
	})
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	t.Cleanup(func() { cleanupHarnessWireRows(t, st) })
	return ts.URL
}

// cleanupHarnessWireRows removes everything the live scenarios wrote against
// the seeded org. Errors are logged, not fatal — a leaked row is annoying, a
// panic inside cleanup hides the real failure.
func cleanupHarnessWireRows(t *testing.T, st *harnessState) {
	t.Helper()
	ctx := context.Background()
	orgID := st.OrgID
	stmts := []string{
		// session_seats.reservation_id points at the hold; release the seats
		// back to 'available' before the reservations rows go away.
		`UPDATE session_seats SET reservation_id = NULL, status = 'available'
		 WHERE reservation_id IN
		     (SELECT id FROM reservations WHERE org_id = $1::uuid)`,
		`DELETE FROM reservation_seats WHERE reservation_id IN
		     (SELECT id FROM reservations WHERE org_id = $1::uuid)`,
		`DELETE FROM reservation_ga_items WHERE reservation_id IN
		     (SELECT id FROM reservations WHERE org_id = $1::uuid)`,
		`DELETE FROM reservations WHERE org_id = $1::uuid`,
		`DELETE FROM gateway_sessions WHERE org_id = $1::uuid`,
		// Customers are platform-scoped (migration 0091): the org link lives
		// in customer_org_links, and every other child table cascades.
		`DELETE FROM customers WHERE id IN
		     (SELECT customer_id FROM customer_org_links WHERE org_id = $1::uuid)`,
		`DELETE FROM compatibility_id_map WHERE platform_id IN
		     (SELECT id FROM sessions WHERE event_id IN
		         (SELECT id FROM events WHERE org_id = $1::uuid))`,
		`DELETE FROM compatibility_id_map WHERE platform_id IN
		     (SELECT id FROM ticket_tiers WHERE session_id IN
		         (SELECT id FROM sessions WHERE event_id IN
		             (SELECT id FROM events WHERE org_id = $1::uuid)))`,
	}
	for _, sql := range stmts {
		if _, err := st.Pool.Exec(ctx, sql, orgID); err != nil {
			t.Logf("wire-row cleanup %.60s… : %v", sql, err)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// wire helpers
// ─────────────────────────────────────────────────────────────────────────────

// postBil24 sends one command envelope to POST /compat/bil24/json and returns
// the decoded flat response object. A non-200 HTTP status is a defect: the
// Bil24 contract carries application errors in resultCode, not in the status.
func postBil24(t *testing.T, base string, body map[string]any) map[string]interface{} {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	resp, err := http.Post(base+"/compat/bil24/json", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("POST /compat/bil24/json: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /compat/bil24/json: status %d, body %s", resp.StatusCode, payload)
	}
	var out map[string]interface{}
	if err := json.Unmarshal(payload, &out); err != nil {
		t.Fatalf("parse response %s: %v", payload, err)
	}
	return out
}

// createGatewayUser runs CREATE_USER (spec §7.3) and returns the minted
// sessionId / userId pair every subsequent command echoes back.
func createGatewayUser(t *testing.T, base string, st *harnessState, email string) (sessionID string, userID float64) {
	t.Helper()
	resp := postBil24(t, base, map[string]any{
		"command":   "CREATE_USER",
		"fid":       st.ChannelFID,
		"token":     st.ChannelToken,
		"locale":    "en-US",
		"email":     email,
		"firstName": "Wave1",
		"lastName":  "Harness",
	})
	if code := resp["resultCode"]; code != float64(0) {
		t.Fatalf("CREATE_USER resultCode = %v, want 0 (description %v)", code, resp["description"])
	}
	sessionID, _ = resp["sessionId"].(string)
	userID, _ = resp["userId"].(float64)
	if sessionID == "" {
		t.Fatalf("CREATE_USER returned no sessionId: %v", resp)
	}
	return sessionID, userID
}

// mustActionEventID mints (or reads) the spec §4 int64 wire id for a session
// UUID. The gateway itself resolves `actionEventId` through the same
// compatibility_id_map, so the harness must speak int64 — not UUID — on the
// wire (see cmd_cart.go: a UUID is rejected with -2 once compatDB is wired).
func mustActionEventID(t *testing.T, st *harnessState, sessionUUID string) int64 {
	t.Helper()
	id, err := uuid.Parse(sessionUUID)
	if err != nil {
		t.Fatalf("parse session uuid %q: %v", sessionUUID, err)
	}
	sysID, err := compatids.Ensure(context.Background(), st.Pool, compatids.KindActionEvent, id)
	if err != nil {
		t.Fatalf("compatids.Ensure(action_event): %v", err)
	}
	return sysID
}

// sortedSeatLabels returns the seeded seat labels in a deterministic order so a
// scenario can pick "the first" and "the second" seat without depending on the
// SVG's section naming.
func sortedSeatLabels(st *harnessState) []string {
	out := make([]string, 0, len(st.SeatIDs))
	for label := range st.SeatIDs {
		out = append(out, label)
	}
	sort.Strings(out)
	return out
}

// expireGatewaySession backdates expires_at so the next command with that token
// hits the spec §6 stale-session path (resultCode 1).
func expireGatewaySession(t *testing.T, st *harnessState, token string) {
	t.Helper()
	if _, err := st.Pool.Exec(context.Background(),
		`UPDATE gateway_sessions SET expires_at = now() - interval '1 day'
		 WHERE session_token = $1`, token); err != nil {
		t.Fatalf("expire gateway session: %v", err)
	}
}

// numberField reads a JSON number out of a decoded response, failing the test
// when the key is missing or carries another type.
func numberField(t *testing.T, resp map[string]interface{}, key string) float64 {
	t.Helper()
	v, ok := resp[key]
	if !ok {
		t.Fatalf("response has no %q key: %v", key, resp)
	}
	n, ok := v.(float64)
	if !ok {
		t.Fatalf("response %q = %#v, want a JSON number", key, v)
	}
	return n
}
