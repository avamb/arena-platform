//go:build integration

// superadmin_org_provisioning_533_integration_test.go — end-to-end
// integration coverage proving a real platform_superadmin can provision a
// brand-new organization end-to-end through the public HTTP API, and that a
// Bil24-shaped import through it round-trips correctly through the compat
// gateway and the outbox.
//
// The flow, entirely through public HTTP endpoints (real login, real JWTs,
// real API keys) except where noted:
//
//  1. Register + grant platform_superadmin (NULL-org_id user_roles row, the
//     only shape a real login-issued JWT's DB-fallback role resolution can
//     see — see superadmin_org_bypass_531_integration_test.go) + login.
//  2. POST /v1/organizations — creates the org. This route is gated only on
//     the `org.create` permission (no {org_id} path param), so it does NOT
//     require X-Admin-Reason.
//  3. POST .../channels — creates a sales channel. This route DOES require
//     X-Admin-Reason (org-scoped, and the superadmin is not a member of the
//     org it just created).
//  4. PUT .../gateway-credential — mints the Bil24 fid/token pair
//     (X-Admin-Reason required).
//  5. PUT .../wp-webhook — registers a WordPress-style callback subscriber
//     pointed at a local stub receiver (X-Admin-Reason required).
//  6. POST .../api-keys — issues a service API key scoped to
//     import.bil24_session (X-Admin-Reason required).
//  7. POST .../imports/event-bundle — imports an arena-sourced event with
//     two ticket tiers and publish:true, authenticated with the raw API key
//     (Authorization: Bearer ak_...) rather than the superadmin's JWT — this
//     is the shape the site-side import module actually uses in production.
//  8. POST /compat/bil24/json GET_ALL_ACTIONS with the minted fid+token
//     proves the import is visible on the Bil24-compatible catalog read,
//     with prices rendered in MAJOR units.
//  9. POST .../channels/{id}/feed-tokens + POST /v1/events/{id}/publications
//     bind the imported event to the channel's WordPress feed — this is the
//     `agent_feed_tokens` / `event_publications` join
//     ListWPSubscribersForEvent requires before a catalog event fans out to
//     any wp-webhook subscriber; registering the wp-webhook alone is not
//     enough (that only satisfies the ORDER/TICKET dispatch path, which
//     looks up subscribers by channel_id directly).
//  10. A real bil24wire.Dispatcher, driven by a real outbox.OutboxEventsDispatcher
//     polling loop, is run against the outbox row the import's publish:true
//     step wrote (v1.event.published); the test asserts the local stub
//     receiver eventually gets a POST of type "event.created" naming the
//     action_event_id the import minted.
package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/bil24wire"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/outbox"
)

// superadminProvisioning533Server builds on productionIntegrationServer's
// pattern, additionally mounting the Bil24-compatible compat gateway
// (Options.Bil24CompatEnabled / Bil24RequireToken) that the plain auth-only
// server does not turn on.
func superadminProvisioning533Server(t *testing.T) (*Server, string) {
	t.Helper()
	srv, secret := productionIntegrationServer(t)
	// productionIntegrationServer already returns a fully-wired *Server; the
	// compat gateway mount decision is made once inside New(), so rebuild
	// with the extra flags rather than trying to flip them after construction.
	srv2 := New(Options{
		Config:             srv.cfg,
		Pool:               srv.pool,
		PgxPool:            srv.pgxPool,
		Auth:               srv.stub,
		Verifier:           srv.verifier,
		Bil24CompatEnabled: true,
		Bil24RequireToken:  true,
	})
	return srv2, secret
}

// provStubEvent is one accepted delivery on the catalog-event stub receiver.
// Deliberately decodes `data` as a raw []any (not a map): the catalog
// dispatch path (bil24wire.Dispatcher.dispatchCatalog) marshals its envelope
// data as a JSON ARRAY of {"actionEventId": N} objects — unlike the
// order.paid / ticket.refunded envelopes tests/compat/bil24/wpstub models,
// which carry an object. Reusing wpstub here would 400 on every delivery
// (json.Unmarshal into map[string]interface{} fails against a JSON array),
// so this test uses its own minimal receiver instead.
type provStubEvent struct {
	ID      int64            `json:"id"`
	Type    string           `json:"type"`
	Created string           `json:"created"`
	Data    []map[string]any `json:"data"`
}

// provStubReceiver is a minimal WordPress-callback stand-in: it accepts any
// well-formed envelope and records it, so the test can assert the catalog
// dispatch actually reached "the site".
type provStubReceiver struct {
	mu       sync.Mutex
	received []provStubEvent
	srv      *httptest.Server
}

func newProvStubReceiver() *provStubReceiver {
	r := &provStubReceiver{}
	r.srv = httptest.NewServer(http.HandlerFunc(r.handle))
	return r
}

func (r *provStubReceiver) handle(w http.ResponseWriter, req *http.Request) {
	var ev provStubEvent
	if err := json.NewDecoder(req.Body).Decode(&ev); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"ok":false,"error":"malformed_json"}`))
		return
	}
	r.mu.Lock()
	r.received = append(r.received, ev)
	r.mu.Unlock()
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"ok":true}`))
}

func (r *provStubReceiver) Received() []provStubEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]provStubEvent, len(r.received))
	copy(out, r.received)
	return out
}

func (r *provStubReceiver) URL() string { return r.srv.URL }
func (r *provStubReceiver) Close()      { r.srv.Close() }

// prov533NoopDispatcher stands in for the base/MACS members of the
// production fan-out (cmd/arena-worker/main.go's multiDispatcher): it
// consumes every row without side effects.
type prov533NoopDispatcher struct{}

func (prov533NoopDispatcher) Dispatch(context.Context, outbox.Event) error { return nil }

// prov533Drain runs a real outbox.OutboxEventsDispatcher until `until`
// reports true or the timeout expires, then stops it — same pattern as
// wp508Drain in tests/compat/bil24/wp_roundtrip_508_integration_test.go,
// duplicated here because that helper is unexported in a different package.
func prov533Drain(t *testing.T, opts outbox.OutboxEventsDispatcherOptions, until func() bool, timeout time.Duration) bool {
	t.Helper()
	oed, err := outbox.NewOutboxEventsDispatcher(opts)
	if err != nil {
		t.Fatalf("NewOutboxEventsDispatcher: %v", err)
	}
	go func() { _ = oed.Run(context.Background()) }()

	deadline := time.Now().Add(timeout)
	satisfied := false
	for time.Now().Before(deadline) {
		if until() {
			satisfied = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = oed.Stop()
	return satisfied
}

// TestSuperadminOrgProvisioning533_Integration drives the full flow described
// in the file doc comment above.
func TestSuperadminOrgProvisioning533_Integration(t *testing.T) {
	srv, _ := superadminProvisioning533Server(t)
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	client := ts.Client()
	ctx := context.Background()

	// ── 1. Superadmin bootstrap ─────────────────────────────────────────────
	superadminEmail := fmt.Sprintf("w1s0-prov533-superadmin-%d@example.com", time.Now().UnixNano())
	const password = "Test1234!"
	registerUser(t, client, ts.URL, superadminEmail, password)

	var superadminUserID uuid.UUID
	if err := srv.pgxPool.QueryRow(ctx,
		`SELECT id FROM users WHERE email = $1`, superadminEmail,
	).Scan(&superadminUserID); err != nil {
		t.Fatalf("look up superadmin user id: %v", err)
	}
	if _, err := srv.pgxPool.Exec(ctx,
		`INSERT INTO user_roles (user_id, role_id, org_id)
SELECT $1, id, NULL FROM roles WHERE name = 'platform_superadmin' AND org_id IS NULL`,
		superadminUserID,
	); err != nil {
		t.Fatalf("grant platform_superadmin: %v", err)
	}
	superadminToken := loginUser(t, client, ts.URL, superadminEmail, password)

	// ── 2. POST /v1/organizations — no X-Admin-Reason required ─────────────
	suffix := uuid.New().String()[:8]
	orgBody := fmt.Sprintf(
		`{"name":%q,"slug":%q,"country":"EE","default_locale":"en","reservation_ttl_seconds":1200}`,
		"W1-S0 Prov533 Org "+suffix, "w1-s0-prov533-org-"+suffix,
	)
	resp := integDoRequest(t, client, http.MethodPost, ts.URL+"/v1/organizations", superadminToken, orgBody)
	body := integReadBody(t, resp)
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		t.Fatalf("create org: expected 200/201, got %d, body: %s", resp.StatusCode, body)
	}
	var orgResp struct {
		Organization struct {
			ID string `json:"id"`
		} `json:"organization"`
	}
	if err := json.Unmarshal([]byte(body), &orgResp); err != nil {
		t.Fatalf("decode create-org response: %v; body: %s", err, body)
	}
	orgID := orgResp.Organization.ID
	if orgID == "" {
		t.Fatalf("create org: empty organization.id in response: %s", body)
	}

	// ── 3. POST .../channels — X-Admin-Reason required (superadmin is not
	// yet a member of the org it just created) ──────────────────────────────
	channelBody := fmt.Sprintf(`{"name":%q,"payment_mode":"merchant_of_record"}`, "Prov533 Channel "+suffix)
	resp = provDoRequestWithReasonAndBody(t, client, http.MethodPost,
		ts.URL+"/v1/organizations/"+orgID+"/channels", superadminToken, "W1-S0 provisioning test", channelBody)
	body = integReadBody(t, resp)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create channel: expected 201, got %d, body: %s", resp.StatusCode, body)
	}
	var channelResp struct {
		Channel struct {
			ID            string `json:"id"`
			DisplayNumber int64  `json:"display_number"`
		} `json:"channel"`
	}
	if err := json.Unmarshal([]byte(body), &channelResp); err != nil {
		t.Fatalf("decode create-channel response: %v; body: %s", err, body)
	}
	channelID := channelResp.Channel.ID
	fid := channelResp.Channel.DisplayNumber
	if channelID == "" || fid == 0 {
		t.Fatalf("create channel: missing id/display_number in response: %s", body)
	}

	// ── 4. PUT .../gateway-credential — mints the fid/token pair ────────────
	resp = provDoRequestWithReasonAndBody(t, client, http.MethodPut,
		ts.URL+"/v1/organizations/"+orgID+"/channels/"+channelID+"/gateway-credential",
		superadminToken, "W1-S0 provisioning test", "{}")
	body = integReadBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("put gateway-credential: expected 200, got %d, body: %s", resp.StatusCode, body)
	}
	var credResp struct {
		FID   int64  `json:"fid"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal([]byte(body), &credResp); err != nil {
		t.Fatalf("decode gateway-credential response: %v; body: %s", err, body)
	}
	if credResp.Token == "" {
		t.Fatalf("put gateway-credential: empty token in response: %s", body)
	}
	gatewayToken := credResp.Token

	// ── 5. PUT .../wp-webhook — registers the callback subscriber ──────────
	stub := newProvStubReceiver()
	t.Cleanup(stub.Close)
	webhookBody := fmt.Sprintf(`{"callback_url":%q,"signing_secret":"prov533-signing-secret"}`, stub.URL())
	resp = provDoRequestWithReasonAndBody(t, client, http.MethodPut,
		ts.URL+"/v1/organizations/"+orgID+"/channels/"+channelID+"/wp-webhook",
		superadminToken, "W1-S0 provisioning test", webhookBody)
	body = integReadBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("put wp-webhook: expected 200, got %d, body: %s", resp.StatusCode, body)
	}

	// ── 6. POST .../api-keys — service credential for the import call ──────
	apiKeyBody := `{"name":"prov533-import-key","scopes":["import.bil24_session"]}`
	resp = provDoRequestWithReasonAndBody(t, client, http.MethodPost,
		ts.URL+"/v1/organizations/"+orgID+"/api-keys",
		superadminToken, "W1-S0 provisioning test", apiKeyBody)
	body = integReadBody(t, resp)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create api key: expected 201, got %d, body: %s", resp.StatusCode, body)
	}
	var apiKeyResp struct {
		APIKey struct {
			APIKey string `json:"api_key"`
		} `json:"api_key"`
	}
	if err := json.Unmarshal([]byte(body), &apiKeyResp); err != nil {
		t.Fatalf("decode api-key response: %v; body: %s", err, body)
	}
	rawAPIKey := apiKeyResp.APIKey.APIKey
	if rawAPIKey == "" {
		t.Fatalf("create api key: empty api_key in response: %s", body)
	}

	// ── 7. POST .../imports/event-bundle — via the API key, not the JWT ────
	// An arena-sourced bundle creating a subtree from scratch carries NO
	// Bil24-shaped numeric ids at all (event-bundle spec §9 scenario 1,
	// himports/event_bundle_525_integration_test.go's fixture payload): the
	// session is addressed by externalRef, and the importer itself mints the
	// action/actionEvent/venue/categoryPrice compat ids (always minted at or
	// above the 1_000_000_000 arena ceiling) and echoes them back in
	// compat_ids. Supplying a client-chosen numeric id here is instead the
	// UPDATE path — it means "this id, which must already resolve to an
	// object of MY organization", and a fresh, never-registered id 404s with
	// import.compat_id_unknown rather than being accepted as a new identity.
	importBody := fmt.Sprintf(`{
		"source": "arena",
		"externalRef": "prov533-%s",
		"publish": true,
		"action": {
			"actionName": "Prov533 Event %s",
			"fullActionName": "Prov533 Event %s"
		},
		"actionEvent": {
			"day": "01.06.2027",
			"time": "19:00",
			"currency": "EUR"
		},
		"venue": {
			"venueName": "Prov533 Venue",
			"address": "1 Test Street",
			"timezone": "Europe/Tallinn"
		},
		"categoryList": [
			{"categoryPriceName": "Standard", "price": 15.00, "availability": 100},
			{"categoryPriceName": "VIP", "price": 20.00, "availability": 50}
		]
	}`, suffix, suffix, suffix)

	resp = integDoRequest(t, client, http.MethodPost,
		ts.URL+"/v1/organizations/"+orgID+"/imports/event-bundle", rawAPIKey, importBody)
	body = integReadBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("import event-bundle: expected 200, got %d, body: %s", resp.StatusCode, body)
	}
	var importResp struct {
		EventID   string `json:"event_id"`
		SessionID string `json:"session_id"`
		Created   bool   `json:"created"`
		CompatIDs struct {
			ActionID      int64 `json:"action_id"`
			ActionEventID int64 `json:"action_event_id"`
		} `json:"compat_ids"`
	}
	if err := json.Unmarshal([]byte(body), &importResp); err != nil {
		t.Fatalf("decode import response: %v; body: %s", err, body)
	}
	if !importResp.Created {
		t.Fatalf("import event-bundle: expected created=true on first import, body: %s", body)
	}
	eventID := importResp.EventID
	if eventID == "" {
		t.Fatalf("import event-bundle: empty event_id in response: %s", body)
	}

	// ── 8. GET_ALL_ACTIONS via the compat gateway — MAJOR-unit prices ──────
	catalogBody := fmt.Sprintf(`{"command":"GET_ALL_ACTIONS","fid":"%d","token":%q}`, fid, gatewayToken)
	resp = integDoRequest(t, client, http.MethodPost, ts.URL+"/compat/bil24/json", "", catalogBody)
	body = integReadBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET_ALL_ACTIONS: expected 200, got %d, body: %s", resp.StatusCode, body)
	}
	var catalogResp struct {
		ResultCode int `json:"resultCode"`
		ActionList []struct {
			ActionName string  `json:"actionName"`
			MinPrice   float64 `json:"minPrice"`
			MaxPrice   float64 `json:"maxPrice"`
		} `json:"actionList"`
	}
	if err := json.Unmarshal([]byte(body), &catalogResp); err != nil {
		t.Fatalf("decode GET_ALL_ACTIONS response: %v; body: %s", err, body)
	}
	if catalogResp.ResultCode != 0 {
		t.Fatalf("GET_ALL_ACTIONS: resultCode = %d, want 0, body: %s", catalogResp.ResultCode, body)
	}
	found := false
	for _, a := range catalogResp.ActionList {
		if a.ActionName == "Prov533 Event "+suffix {
			found = true
			if a.MinPrice != 15.00 {
				t.Errorf("GET_ALL_ACTIONS: minPrice = %v, want 15 (major units)", a.MinPrice)
			}
			if a.MaxPrice != 20.00 {
				t.Errorf("GET_ALL_ACTIONS: maxPrice = %v, want 20 (major units)", a.MaxPrice)
			}
		}
	}
	if !found {
		t.Fatalf("GET_ALL_ACTIONS: imported action %q not found in actionList, body: %s", "Prov533 Event "+suffix, body)
	}

	// ── 9. Feed token + publication — the join ListWPSubscribersForEvent
	// requires before catalog dispatch reaches the wp-webhook subscriber ────
	// Feed-token creation is org-scoped ({org_id} in the path) and its shim
	// (handleCreateFeedToken -> s.enforceOrgMembership) requires
	// X-Admin-Reason for a non-member superadmin, same as channels/
	// gateway-credential/wp-webhook/api-keys above.
	resp = provDoRequestWithReasonAndBody(t, client, http.MethodPost,
		ts.URL+"/v1/organizations/"+orgID+"/channels/"+channelID+"/feed-tokens",
		superadminToken, "W1-S0 provisioning test", `{"label":"prov533"}`)
	body = integReadBody(t, resp)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create feed token: expected 201, got %d, body: %s", resp.StatusCode, body)
	}
	var feedTokenResp struct {
		FeedToken struct {
			ID string `json:"id"`
		} `json:"feed_token"`
	}
	if err := json.Unmarshal([]byte(body), &feedTokenResp); err != nil {
		t.Fatalf("decode feed-token response: %v; body: %s", err, body)
	}
	feedTokenID := feedTokenResp.FeedToken.ID
	if feedTokenID == "" {
		t.Fatalf("create feed token: empty id in response: %s", body)
	}

	pubBody := fmt.Sprintf(`{"feed_token_id":%q}`, feedTokenID)
	resp = integDoRequest(t, client, http.MethodPost,
		ts.URL+"/v1/events/"+eventID+"/publications", superadminToken, pubBody)
	body = integReadBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("publish event to feed: expected 200, got %d, body: %s", resp.StatusCode, body)
	}

	// ── 10. Drain the real outbox against the real bil24wire dispatcher ────
	// The import's publish:true step already wrote a v1.event.published
	// outbox row (himports.Handler.publishCatalogEvent, fired after commit).
	wpDispatcher := bil24wire.NewDispatcher(srv.pgxPool)
	if wpDispatcher == nil {
		t.Fatal("bil24wire.NewDispatcher returned nil for a live pool")
	}
	fanOut := &prov533MultiDispatcher{dispatchers: []outbox.Dispatcher{
		prov533NoopDispatcher{}, // base outbox dispatcher
		prov533NoopDispatcher{}, // MACS dispatcher
		wpDispatcher,            // bil24_wp dispatcher
	}}
	dispatchOpts := outbox.OutboxEventsDispatcherOptions{
		Store:        outbox.NewPGOutboxEventStore(srv.pgxPool),
		Dispatcher:   fanOut,
		PollInterval: 20 * time.Millisecond,
		MaxAttempts:  5,
		BackoffFunc:  func(int) time.Duration { return time.Hour },
	}

	// The PUT .../wp-webhook call above already delivered a synthetic
	// connectivity-check ping (Type "test") to the stub, so the drain
	// predicate must wait for an event.created delivery specifically rather
	// than for any delivery at all.
	if !prov533Drain(t, dispatchOpts, func() bool {
		for _, ev := range stub.Received() {
			if ev.Type == "event.created" {
				return true
			}
		}
		return false
	}, 15*time.Second) {
		t.Fatal("v1.event.published never reached the wp-webhook stub receiver as event.created")
	}

	delivered := stub.Received()
	var eventCreated *provStubEvent
	for i := range delivered {
		if delivered[i].Type == "event.created" {
			eventCreated = &delivered[i]
			break
		}
	}
	if eventCreated == nil {
		t.Fatalf("stub receiver got %d delivery(ies) but none of type event.created: %+v", len(delivered), delivered)
	}
	if len(eventCreated.Data) == 0 {
		t.Fatalf("event.created delivered with empty data array: %+v", eventCreated)
	}
	sawActionEventID := false
	for _, entry := range eventCreated.Data {
		if id, ok := entry["actionEventId"].(float64); ok && int64(id) == importResp.CompatIDs.ActionEventID {
			sawActionEventID = true
		}
	}
	if !sawActionEventID {
		t.Fatalf("event.created data %+v does not reference the imported action_event_id %d",
			eventCreated.Data, importResp.CompatIDs.ActionEventID)
	}
}

// prov533MultiDispatcher mirrors the unexported multiDispatcher of
// cmd/arena-worker/main.go and wp508MultiDispatcher of
// tests/compat/bil24/wp_roundtrip_508_integration_test.go: every registered
// dispatcher sees every row, and the first error aborts the fan-out so the
// outbox retries the whole envelope.
type prov533MultiDispatcher struct {
	dispatchers []outbox.Dispatcher
}

func (m *prov533MultiDispatcher) Dispatch(ctx context.Context, ev outbox.Event) error {
	for _, d := range m.dispatchers {
		if err := d.Dispatch(ctx, ev); err != nil {
			return err
		}
	}
	return nil
}

// provDoRequestWithReasonAndBody is integDoRequestWithReason plus a request
// body — the shared helper in superadmin_org_bypass_531_integration_test.go
// only covers bodyless GET requests, so this file adds the body-carrying
// variant rather than editing the existing helper's signature.
func provDoRequestWithReasonAndBody(t *testing.T, client *http.Client, method, url, bearer, reason, body string) *http.Response {
	t.Helper()
	var bodyReader *bytes.Reader
	if body != "" {
		bodyReader = bytes.NewReader([]byte(body))
	} else {
		bodyReader = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, url, bodyReader)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if reason != "" {
		req.Header.Set("X-Admin-Reason", reason)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	return resp
}
