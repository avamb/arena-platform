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
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/compatids"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver"
)

// harnessJWTStubSecret is the stub HS256 secret used to mint JWTs for
// scenarios (e.g. scenario 9, feature #514) that must drive the real v1 REST
// surface as a user actor rather than the Bil24 gateway wire protocol.
const harnessJWTStubSecret = "harness-jwt-stub-secret-long-enough-for-hs256"

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
		JWTSecretStub:   harnessJWTStubSecret,
		EnableStubAuth:  true,
	}
}

// harnessStubAuth builds the StubProvider matching harnessServerConfig's
// JWTSecretStub, so scenarios can mint bearer tokens for the v1 REST surface
// (feature #514, scenario 9) via stub.IssueToken.
func harnessStubAuth(t *testing.T) *auth.StubProvider {
	t.Helper()
	stub, err := auth.NewStubProvider(auth.StubConfig{
		Secret:  harnessJWTStubSecret,
		Issuer:  "arena-bil24-harness",
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("harnessStubAuth: NewStubProvider: %v", err)
	}
	return stub
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
		Auth:               harnessStubAuth(t),
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

// getBil24Image performs the scenario-7 GET against /compat/bil24/image
// (feature #501, spec §8) and returns the raw status, headers and body.
//
// Unlike postBil24 this helper never fails on a non-200: the route's whole
// contract is expressed in status codes (200 / 304 / 404), so the caller must
// see them. ifNoneMatch is sent only when non-empty, and redirects are left at
// the default because the route never issues one.
func getBil24Image(
	t *testing.T,
	base string,
	query map[string]string,
	ifNoneMatch string,
) (int, http.Header, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, base+"/compat/bil24/image", nil)
	if err != nil {
		t.Fatalf("build image request: %v", err)
	}
	q := req.URL.Query()
	for k, v := range query {
		q.Set(k, v)
	}
	req.URL.RawQuery = q.Encode()
	if ifNoneMatch != "" {
		req.Header.Set("If-None-Match", ifNoneMatch)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /compat/bil24/image: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read image body: %v", err)
	}
	return resp.StatusCode, resp.Header, body
}

// attrValues collects the value of one XML attribute across every occurrence
// of an element prefix — e.g. attrValues(svg, "<circle ", `sbt:cat="`) returns
// the category index of every seat.
//
// A regex-free scan is deliberate: the assertion is about which literal
// attribute strings the encoder emitted, and a parser that normalises
// namespaces would hide exactly the drift this scenario exists to catch.
func attrValues(doc, elementPrefix, attrPrefix string) []string {
	var out []string
	rest := doc
	for {
		i := strings.Index(rest, elementPrefix)
		if i < 0 {
			return out
		}
		rest = rest[i+len(elementPrefix):]
		end := strings.Index(rest, ">")
		if end < 0 {
			return out
		}
		tag := rest[:end]
		if j := strings.Index(tag, attrPrefix); j >= 0 {
			val := tag[j+len(attrPrefix):]
			if k := strings.Index(val, `"`); k >= 0 {
				out = append(out, val[:k])
			}
		}
	}
}

// truncateSVG keeps a failure message readable: a full seating plan is tens of
// kilobytes and would bury the assertion that actually failed.
func truncateSVG(svg string) string {
	const max = 2000
	if len(svg) <= max {
		return svg
	}
	return svg[:max] + "\n…(truncated)"
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

// restJSON sends one request to the real v1 REST surface (as opposed to the
// Bil24 wire protocol postBil24 speaks) with an optional bearer token and
// extra headers, and returns the raw status plus the decoded JSON body.
// Feature #514 scenario 9 is the first consumer: it drives the org-admin
// api-keys CRUD surface and the §13.4 no-seats catalog flow as a real user
// (JWT) and then as a real service actor (api key), so unlike postBil24 a
// non-2xx status is not fatal — the caller asserts on it directly (e.g. the
// cross-org 403 and the revoked-key 401).
func restJSON(
	t *testing.T,
	base, method, path, bearer string,
	headers map[string]string,
	body any,
) (int, map[string]interface{}) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, base+path, reader)
	if err != nil {
		t.Fatalf("build %s %s: %v", method, path, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body for %s %s: %v", method, path, err)
	}
	var out map[string]interface{}
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &out); err != nil {
			t.Fatalf("parse response %s %s = %s: %v", method, path, payload, err)
		}
	}
	return resp.StatusCode, out
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
