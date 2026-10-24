//go:build integration

// outbound_policy_integration_test.go — the WordPress and MACS webhook
// registrations refuse a callback URL that the platform must not reach, and
// refuse it BEFORE anything is written: the previous subscriber stays active
// and no request ever reaches the address.
//
// The handlers are pinned to egress.PrivateDeny (production behaviour); the
// test binary otherwise allows private targets so the other tests can use
// httptest receivers on 127.0.0.1.
package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/egress"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcatalog"
)

var refuseInward = egress.Policy{Private: egress.PrivateDeny}

// TestOutboundPolicy_WPWebhookRefusesInwardURLAndKeepsTheOldSubscriber
// registers a working subscriber, then tries to replace it with addresses the
// platform must never call.
func TestOutboundPolicy_WPWebhookRefusesInwardURLAndKeepsTheOldSubscriber(t *testing.T) {
	pool := wp507IntegrationPool(t)
	ctx := context.Background()
	f := newWP507Fixture(t, ctx, pool)
	defer f.cleanup()
	orgID, chID := f.orgID.String(), f.chID.String()

	var hits atomic.Int32
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
	}))
	defer receiver.Close()

	// 1. A subscriber registered while private targets are allowed (the test
	//    binary's default), standing in for a legitimate existing site.
	open := hcatalog.New(nil, nil, nil, gen.New(pool), nil, nil, nil, pool,
		audit.NewPGWriter(pool), slog.Default(), nil).
		WithMembershipQueries(gen.New(pool))
	body, _ := json.Marshal(map[string]string{"callback_url": receiver.URL})
	w := httptest.NewRecorder()
	open.HandlePutChannelWPWebhook(pool, w, wp507Request(http.MethodPut, orgID, chID, body))
	if w.Code != http.StatusOK {
		t.Fatalf("seed PUT = %d: %s", w.Code, w.Body.String())
	}
	before := hits.Load()

	// 2. The production policy refuses every inward address.
	h := hcatalog.New(nil, nil, nil, gen.New(pool), nil, nil, nil, pool,
		audit.NewPGWriter(pool), slog.Default(), nil).
		WithMembershipQueries(gen.New(pool)).
		WithWebhookEgress(refuseInward)
	for _, bad := range []string{
		receiver.URL, // http://127.0.0.1:<port>
		"https://127.0.0.1/hook",
		"https://169.254.169.254/latest/meta-data/",
		"https://[::1]/hook",
		"https://10.0.0.5/hook",
		"https://localhost/hook",
		"https://user:pw@site.example/hook",
	} {
		body, _ := json.Marshal(map[string]string{"callback_url": bad})
		w := httptest.NewRecorder()
		h.HandlePutChannelWPWebhook(pool, w, wp507Request(http.MethodPut, orgID, chID, body))
		if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "wp_webhook.invalid_callback_url") {
			t.Errorf("PUT %q = %d %s, want 422 wp_webhook.invalid_callback_url", bad, w.Code, w.Body.String())
		}
	}
	if got := hits.Load(); got != before {
		t.Fatalf("a refused registration reached the receiver (%d -> %d requests)", before, got)
	}

	// 3. The original subscriber is still the active one.
	var active int
	var url string
	if err := pool.QueryRow(ctx, `
		SELECT count(*), max(callback_url) FROM webhook_subscribers
		WHERE channel_id = $1 AND kind = 'bil24_wp' AND active`, f.chID).Scan(&active, &url); err != nil {
		t.Fatal(err)
	}
	if active != 1 || url != receiver.URL {
		t.Fatalf("active subscribers = %d (%q), want exactly the original %q", active, url, receiver.URL)
	}

	// 4. The audit row of the registration names only the origin.
	var meta []byte
	if err := pool.QueryRow(ctx, `
		SELECT metadata FROM audit_events
		WHERE action = 'v1.channel.wp_webhook.registered' AND resource_id = $1
		ORDER BY occurred_at DESC LIMIT 1`, chID).Scan(&meta); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(meta, []byte("callback_url")) {
		t.Fatalf("audit metadata still carries the full callback_url: %s", meta)
	}
}

// TestOutboundPolicy_MACSWebhookRefusesInwardAndSmuggledSuffix proves the
// MACS registration judges the URL's path, not its text, and refuses inward
// addresses without writing a subscriber.
func TestOutboundPolicy_MACSWebhookRefusesInwardAndSmuggledSuffix(t *testing.T) {
	pool := wp507IntegrationPool(t)
	ctx := context.Background()
	f := newWP507Fixture(t, ctx, pool)
	defer f.cleanup()
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM webhook_subscribers WHERE org_id = $1 AND kind = 'macs'`, f.orgID)
	}()

	h := hcatalog.New(nil, nil, nil, nil, nil, gen.New(pool), nil, pool,
		audit.NewPGWriter(pool), slog.Default(), nil).
		WithMembershipQueries(gen.New(pool)).
		WithWebhookEgress(refuseInward)

	for _, bad := range []string{
		"https://169.254.169.254/latest/meta-data?x=/api/_wh/tickets",
		"https://site.example/admin#/api/_wh/tickets",
		"https://127.0.0.1/api/_wh/tickets",
		"http://site.example/api/_wh/tickets",
		"https://172.17.0.1:8000/api/_wh/tickets",
	} {
		body, _ := json.Marshal(map[string]string{"callback_url": bad})
		w := httptest.NewRecorder()
		h.HandleUpsertMACSWebhook(pool, w, macsPolicyRequest(http.MethodPut, f.orgID.String(), body, true))
		if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "macs.invalid_callback_url") {
			t.Errorf("PUT %q = %d %s, want 422 macs.invalid_callback_url", bad, w.Code, w.Body.String())
		}
	}

	// Without X-Admin-Reason the PUT is refused before the body is read.
	body, _ := json.Marshal(map[string]string{"callback_url": "https://site.example/api/_wh/tickets"})
	w := httptest.NewRecorder()
	h.HandleUpsertMACSWebhook(pool, w, macsPolicyRequest(http.MethodPut, f.orgID.String(), body, false))
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "superadmin.missing_reason") {
		t.Errorf("PUT without reason = %d %s, want 400 superadmin.missing_reason", w.Code, w.Body.String())
	}

	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM webhook_subscribers WHERE org_id = $1 AND kind = 'macs'`, f.orgID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("refused registrations wrote %d MACS subscribers, want 0", n)
	}
}

func macsPolicyRequest(method, orgID string, body []byte, withReason bool) *http.Request {
	req := httptest.NewRequest(method, "/", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if withReason {
		req.Header.Set("X-Admin-Reason", "outbound policy integration test")
	}
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("org_id", orgID)
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = auth.WithActor(ctx, auth.Actor{ID: uuid.NewString(), Type: auth.ActorTypeUser})
	ctx = auth.WithSuperadminOrgAccess(ctx)
	return req.WithContext(ctx)
}
