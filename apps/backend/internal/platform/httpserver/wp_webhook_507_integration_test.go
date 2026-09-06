//go:build integration

// wp_webhook_507_integration_test.go — live-PostgreSQL coverage for feature
// #507 (W1-B7d): the synchronous `test` delivery flow and the PUT/GET/DELETE
// lifecycle of the Bil24-compat WordPress webhook subscriber, driven through
// the REAL hcatalog handlers and the REAL bil24wire signing helper (per
// AGENTS.md: "Integration tests must use real handlers + real dispatcher").
//
// Route-level auth/admin-reason/membership gating is already covered by the
// unit tests in wp_webhook_507_test.go against dbDownPool; this file exists
// to prove the handler's actual database writes and its outbound HTTP call
// to a mock WordPress receiver, which needs a live PostgreSQL.
//
// Run with:
//
//	DATABASE_URL=postgres://arena:arena@localhost:55432/arena?sslmode=disable \
//	    go test -tags integration ./apps/backend/internal/platform/httpserver/ \
//	    -run TestWPWebhook507Integration
package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/bil24wire"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcatalog"
)

// wp507Fixture is an org + sales-channel pair, created via raw gen queries and
// torn down with FK-safe ordered deletes, mirroring the w1a6cFixture pattern
// in order_wiring_w1a6c_488_integration_test.go.
type wp507Fixture struct {
	t     *testing.T
	pool  *pgxpool.Pool
	orgID uuid.UUID
	chID  uuid.UUID
}

func newWP507Fixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) *wp507Fixture {
	t.Helper()
	q := gen.New(pool)

	org, err := q.InsertOrganization(ctx, "WP507 Test Org", "wp507-test-org-"+uuid.NewString(), "DE", "en", 1200)
	if err != nil {
		t.Fatalf("newWP507Fixture: InsertOrganization: %v", err)
	}
	ch, err := q.InsertSalesChannel(ctx, org.ID, "WP507 Test Channel", "merchant_of_record", "stripe", nil, "0", nil, nil)
	if err != nil {
		_, _ = pool.Exec(ctx, `DELETE FROM organizations WHERE id = $1`, org.ID)
		t.Fatalf("newWP507Fixture: InsertSalesChannel: %v", err)
	}
	return &wp507Fixture{t: t, pool: pool, orgID: org.ID, chID: ch.ID}
}

// cleanup deletes the fixture rows in FK-safe order. Best-effort: logs rather
// than fails, since it runs via defer after assertions have already run.
func (f *wp507Fixture) cleanup() {
	ctx := context.Background()
	steps := []string{
		`DELETE FROM webhook_subscribers WHERE channel_id = $1`,
		`DELETE FROM sales_channels WHERE id = $2`,
		`DELETE FROM organizations WHERE id = $1`,
	}
	if _, err := f.pool.Exec(ctx, steps[0], f.chID); err != nil {
		f.t.Logf("wp507Fixture cleanup: delete webhook_subscribers: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `DELETE FROM sales_channels WHERE id = $1`, f.chID); err != nil {
		f.t.Logf("wp507Fixture cleanup: delete sales_channels: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `DELETE FROM organizations WHERE id = $1`, f.orgID); err != nil {
		f.t.Logf("wp507Fixture cleanup: delete organizations: %v", err)
	}
}

// wp507IntegrationPool connects to DATABASE_URL, skipping the test when unset
// (matches auth_password_reset_integration_test.go's integrationPool helper).
func wp507IntegrationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping integration test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Skipf("cannot connect to PostgreSQL (%v); skipping", err)
	}
	t.Cleanup(func() { pool.Close() })
	return pool
}

// wp507Request builds an authenticated (superadmin-bypass), admin-reasoned
// request with chi URL params pre-populated, so it can be handed directly to
// an hcatalog.Handler method without mounting the full router/JWT stack.
// This is the same "call the handler directly against a real pool" pattern
// used by hiam/orgs_legal_integration_test.go.
func wp507Request(method, orgID, chID string, body []byte) *http.Request {
	var req *http.Request
	if body != nil {
		req = httptest.NewRequest(method, "/", bytes.NewReader(body))
	} else {
		req = httptest.NewRequest(method, "/", nil)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Admin-Reason", "wp507 integration test")

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("org_id", orgID)
	rctx.URLParams.Add("id", chID)
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = auth.WithActor(ctx, auth.Actor{ID: uuid.NewString(), Type: auth.ActorTypeUser})
	ctx = auth.WithSuperadminOrgAccess(ctx)
	return req.WithContext(ctx)
}

// TestWPWebhook507Integration_PutGetDeleteLifecycle drives the full
// register → test-delivery → read → deactivate → re-register lifecycle
// against a live PostgreSQL and a mock WordPress receiver.
func TestWPWebhook507Integration_PutGetDeleteLifecycle(t *testing.T) {
	pool := wp507IntegrationPool(t)
	ctx := context.Background()
	f := newWP507Fixture(t, ctx, pool)
	defer f.cleanup()

	h := hcatalog.New(nil, nil, nil, gen.New(pool), nil, nil, nil, pool,
		audit.NewPGWriter(pool), slog.Default(), nil).
		WithMembershipQueries(gen.New(pool))

	orgID := f.orgID.String()
	chID := f.chID.String()

	// 1. PUT against a receiver that answers 200: test_delivery.ok must be true.
	var receivedSig, receivedType string
	var receivedBody []byte
	okSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedSig = r.Header.Get("X-Arena-Signature")
		receivedType = r.Header.Get("X-Arena-Event-Type")
		receivedBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer okSrv.Close()

	putBody, _ := json.Marshal(map[string]string{
		"callback_url":   okSrv.URL,
		"signing_secret": "wp507-secret",
	})
	w1 := httptest.NewRecorder()
	h.HandlePutChannelWPWebhook(pool, w1, wp507Request(http.MethodPut, orgID, chID, putBody))
	if w1.Code != http.StatusOK {
		t.Fatalf("PUT (ok receiver) = %d: %s", w1.Code, w1.Body.String())
	}
	var putResp struct {
		SigningSecret string `json:"signing_secret"`
		Active        bool   `json:"active"`
		TestDelivery  struct {
			OK         bool `json:"ok"`
			HTTPStatus int  `json:"http_status"`
		} `json:"test_delivery"`
	}
	if err := json.Unmarshal(w1.Body.Bytes(), &putResp); err != nil {
		t.Fatalf("decode PUT response: %v", err)
	}
	if !putResp.Active || putResp.SigningSecret != "wp507-secret" {
		t.Fatalf("PUT response mismatch: %+v", putResp)
	}
	if !putResp.TestDelivery.OK || putResp.TestDelivery.HTTPStatus != http.StatusOK {
		t.Fatalf("expected successful test_delivery, got %+v", putResp.TestDelivery)
	}
	if receivedType != bil24wire.SiteEventTest {
		t.Fatalf("receiver got X-Arena-Event-Type=%q, want %q", receivedType, bil24wire.SiteEventTest)
	}
	wantSig := bil24wire.Sign(receivedBody, "wp507-secret")
	if receivedSig != wantSig {
		t.Fatalf("receiver got X-Arena-Signature=%q, want %q (computed over the exact body received)", receivedSig, wantSig)
	}
	var env struct {
		Type string `json:"type"`
		Data any    `json:"data"`
	}
	if err := json.Unmarshal(receivedBody, &env); err != nil {
		t.Fatalf("decode envelope body: %v", err)
	}
	if env.Type != "test" || env.Data != nil {
		t.Fatalf("envelope = %+v, want type=test data=null", env)
	}

	// 2. GET returns the active subscriber, never the signing_secret.
	w2 := httptest.NewRecorder()
	h.HandleGetChannelWPWebhook(pool, w2, wp507Request(http.MethodGet, orgID, chID, nil))
	if w2.Code != http.StatusOK {
		t.Fatalf("GET (active) = %d: %s", w2.Code, w2.Body.String())
	}
	if bodyContains(w2.Body.String(), "signing_secret") {
		t.Fatalf("GET response must never include signing_secret: %s", w2.Body.String())
	}
	var getResp struct {
		Active      bool   `json:"active"`
		CallbackURL string `json:"callback_url"`
	}
	if err := json.Unmarshal(w2.Body.Bytes(), &getResp); err != nil {
		t.Fatalf("decode GET response: %v", err)
	}
	if !getResp.Active || getResp.CallbackURL != okSrv.URL {
		t.Fatalf("GET response mismatch: %+v", getResp)
	}

	// 3. PUT against a receiver that answers 500: test_delivery.ok must be
	// false, but the PUT itself still succeeds (delivery never fails the
	// registration), and the previous subscriber is deactivated in favor of
	// the new one.
	failSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer failSrv.Close()

	putBody2, _ := json.Marshal(map[string]string{
		"callback_url":   failSrv.URL,
		"signing_secret": "wp507-secret-2",
	})
	w3 := httptest.NewRecorder()
	h.HandlePutChannelWPWebhook(pool, w3, wp507Request(http.MethodPut, orgID, chID, putBody2))
	if w3.Code != http.StatusOK {
		t.Fatalf("PUT (failing receiver) = %d: %s", w3.Code, w3.Body.String())
	}
	var putResp2 struct {
		Active       bool `json:"active"`
		TestDelivery struct {
			OK         bool `json:"ok"`
			HTTPStatus int  `json:"http_status"`
		} `json:"test_delivery"`
	}
	if err := json.Unmarshal(w3.Body.Bytes(), &putResp2); err != nil {
		t.Fatalf("decode second PUT response: %v", err)
	}
	if putResp2.TestDelivery.OK || putResp2.TestDelivery.HTTPStatus != http.StatusInternalServerError {
		t.Fatalf("expected failed test_delivery with http_status=500, got %+v", putResp2.TestDelivery)
	}
	if !putResp2.Active {
		t.Fatalf("re-registration must still report active=true even though delivery failed")
	}

	// The old subscriber to okSrv.URL must now be inactive; only the new one
	// (to failSrv.URL) is active.
	w4 := httptest.NewRecorder()
	h.HandleGetChannelWPWebhook(pool, w4, wp507Request(http.MethodGet, orgID, chID, nil))
	if w4.Code != http.StatusOK {
		t.Fatalf("GET after re-register = %d: %s", w4.Code, w4.Body.String())
	}
	var getResp2 struct {
		CallbackURL string `json:"callback_url"`
	}
	if err := json.Unmarshal(w4.Body.Bytes(), &getResp2); err != nil {
		t.Fatalf("decode GET-after-re-register response: %v", err)
	}
	if getResp2.CallbackURL != failSrv.URL {
		t.Fatalf("GET after re-register callback_url = %q, want %q (old one must be deactivated)", getResp2.CallbackURL, failSrv.URL)
	}

	// 4. DELETE deactivates the active subscriber.
	w5 := httptest.NewRecorder()
	h.HandleDeleteChannelWPWebhook(pool, w5, wp507Request(http.MethodDelete, orgID, chID, nil))
	if w5.Code != http.StatusOK {
		t.Fatalf("DELETE = %d: %s", w5.Code, w5.Body.String())
	}

	// 5. A follow-up GET now 404s: there is no active subscriber left.
	w6 := httptest.NewRecorder()
	h.HandleGetChannelWPWebhook(pool, w6, wp507Request(http.MethodGet, orgID, chID, nil))
	if w6.Code != http.StatusNotFound {
		t.Fatalf("GET after DELETE = %d, want 404: %s", w6.Code, w6.Body.String())
	}

	// A second DELETE also 404s: nothing left to deactivate.
	w7 := httptest.NewRecorder()
	h.HandleDeleteChannelWPWebhook(pool, w7, wp507Request(http.MethodDelete, orgID, chID, nil))
	if w7.Code != http.StatusNotFound {
		t.Fatalf("second DELETE = %d, want 404: %s", w7.Code, w7.Body.String())
	}
}

func bodyContains(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}
