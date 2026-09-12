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
//  9. The API key from step 6 is bound to the channel created in step 3
//     (feature #536, W1-S1b): the event-bundle import's publish:true step
//     therefore auto-publishes the event INTO that channel — minting an
//     `agent_feed_tokens` row and an `event_publications` row itself, inside
//     the import transaction — with NO manual feed-token/publications call
//     anywhere in this test. That join is what ListWPSubscribersForEvent
//     requires before a catalog event fans out to any wp-webhook subscriber;
//     registering the wp-webhook alone is not enough (that only satisfies the
//     ORDER/TICKET dispatch path, which looks up subscribers by channel_id
//     directly).
//  10. A real bil24wire.Dispatcher, driven by a real outbox.OutboxEventsDispatcher
//     polling loop, is run against the outbox row the import's publish:true
//     step wrote (v1.event.published); the test asserts the local stub
//     receiver eventually gets a POST of type "event.created" naming the
//     action_event_id the import minted.
//  11. GET_ALL_ACTIONS also proves the two remaining W1-S1 fixes (features
//     #535, #537): the imported venue's city comes back as "Praha" in its
//     original case (not the lowercased slug), and the action's
//     bigPosterUrl is an ABSOLUTE, signed URL on API_PUBLIC_URL that a plain
//     GET through the live test server resolves to the exact poster bytes a
//     local httptest origin served during the import's side-load.
//  12. PUT .../gateway-credential's base_url/image_url (already minted in
//     step 4) are asserted to sit on API_PUBLIC_URL specifically — never the
//     bare origin and never APP_PUBLIC_URL, which this test deliberately
//     sets to a DIFFERENT host so a regression rebuilding the link on the
//     wrong origin is caught.
//
// A sibling test, TestSuperadminOrgProvisioning538_JWTActorNoAutoPublication,
// proves the flip side of #536: a real JWT user actor (not a service key)
// importing with publish:true does NOT auto-publish anything — no feed token,
// no event_publications row — because auth.Actor.ChannelID is only ever
// populated for service (API-key) actors.
package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/storage"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/bil24wire"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/mediastore"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/outbox"
)

// prov533AppPublicURL / prov533APIPublicURL are deliberately DIFFERENT hosts
// (spec 22 §2.1, feature #535): every site-facing link this test checks
// (gateway-credential base_url/image_url, the signed poster URL) must sit on
// the API origin, never the SPA origin, so a regression rebuilding one of
// them on the wrong host is caught rather than coincidentally passing because
// both origins matched.
const (
	prov533AppPublicURL       = "https://app.prov533.test"
	prov533APIPublicURL       = "https://api.prov533.test"
	prov533MediaSigningSecret = "prov533-media-signing-secret"
)

// superadminProvisioning533Server builds on productionIntegrationServer's
// pattern, additionally mounting the Bil24-compatible compat gateway
// (Options.Bil24CompatEnabled / Bil24RequireToken) that the plain auth-only
// server does not turn on, plus a local-disk mediastore.Repo (feature #535)
// so the signed poster URL and the gateway-credential endpoint URLs resolve
// to real, fetchable links instead of empty strings.
func superadminProvisioning533Server(t *testing.T) (*Server, string) {
	t.Helper()
	srv, secret := productionIntegrationServer(t)

	local, err := storage.NewLocalStorage(t.TempDir())
	if err != nil {
		t.Fatalf("storage.NewLocalStorage: %v", err)
	}
	media, err := mediastore.New(mediastore.Options{
		Pool:          srv.pgxPool,
		Storage:       local,
		SigningSecret: []byte(prov533MediaSigningSecret),
	})
	if err != nil {
		t.Fatalf("mediastore.New: %v", err)
	}

	// The bundle fixture below names venue.countryName "Czechia", which is NOT
	// in the 0006_geo.sql seed list. On the shared dev stand the row happens
	// to exist because tests/compat/bil24's harness seeds it, but the CI
	// Integration job runs packages in parallel on a fresh database, so this
	// package must seed it itself (same idempotent idiom as the harness) or
	// the import degrades to import.country_unresolved and the venue is
	// stored without a city. Seen red on CI run 34711169622 (d740d44).
	if _, err := srv.pgxPool.Exec(context.Background(),
		`INSERT INTO countries (iso2, iso3, slug, currency)
		 VALUES ('CZ','CZE','czechia','CZK')
		 ON CONFLICT (iso2) DO NOTHING`); err != nil {
		t.Fatalf("seed country CZ: %v", err)
	}

	// productionIntegrationServer already returns a fully-wired *Server; the
	// compat gateway mount decision is made once inside New(), so rebuild
	// with the extra flags rather than trying to flip them after construction.
	cfg := *srv.cfg
	cfg.AppPublicURL = prov533AppPublicURL
	cfg.APIPublicURL = prov533APIPublicURL

	srv2 := New(Options{
		Config:             &cfg,
		Pool:               srv.pool,
		PgxPool:            srv.pgxPool,
		Auth:               srv.stub,
		Verifier:           srv.verifier,
		Bil24CompatEnabled: true,
		Bil24RequireToken:  true,
		Media:              media,
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
		FID      int64  `json:"fid"`
		Token    string `json:"token"`
		BaseURL  string `json:"base_url"`
		ImageURL string `json:"image_url"`
	}
	if err := json.Unmarshal([]byte(body), &credResp); err != nil {
		t.Fatalf("decode gateway-credential response: %v; body: %s", err, body)
	}
	if credResp.Token == "" {
		t.Fatalf("put gateway-credential: empty token in response: %s", body)
	}
	gatewayToken := credResp.Token

	// (#535, spec §2.1 / feature #538 step 12) the endpoint URLs must sit on
	// API_PUBLIC_URL, ending with the gateway's own paths — never the bare
	// origin (which the plugin would POST straight to "/" and 404) and never
	// APP_PUBLIC_URL (the SPA origin this test deliberately set to a
	// different host above).
	if want := prov533APIPublicURL + "/compat/bil24"; credResp.BaseURL != want {
		t.Errorf("gateway-credential base_url = %q, want %q", credResp.BaseURL, want)
	}
	if want := prov533APIPublicURL + "/compat/bil24/image"; credResp.ImageURL != want {
		t.Errorf("gateway-credential image_url = %q, want %q", credResp.ImageURL, want)
	}
	if strings.HasPrefix(credResp.BaseURL, prov533AppPublicURL) || strings.HasPrefix(credResp.ImageURL, prov533AppPublicURL) {
		t.Errorf("gateway-credential URLs must not sit on the SPA origin %s: base_url=%q image_url=%q",
			prov533AppPublicURL, credResp.BaseURL, credResp.ImageURL)
	}

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

	// ── 6. POST .../api-keys — service credential for the import call, BOUND
	// TO THE CHANNEL (feature #536) so the import's publish:true step below
	// auto-publishes into it — no manual feed-token/publications call. ──────
	apiKeyBody := fmt.Sprintf(`{"name":"prov533-import-key","scopes":["import.bil24_session"],"channel_id":%q}`, channelID)
	resp = provDoRequestWithReasonAndBody(t, client, http.MethodPost,
		ts.URL+"/v1/organizations/"+orgID+"/api-keys",
		superadminToken, "W1-S0 provisioning test", apiKeyBody)
	body = integReadBody(t, resp)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create api key: expected 201, got %d, body: %s", resp.StatusCode, body)
	}
	var apiKeyResp struct {
		APIKey struct {
			APIKey    string  `json:"api_key"`
			ChannelID *string `json:"channel_id"`
		} `json:"api_key"`
	}
	if err := json.Unmarshal([]byte(body), &apiKeyResp); err != nil {
		t.Fatalf("decode api-key response: %v; body: %s", err, body)
	}
	rawAPIKey := apiKeyResp.APIKey.APIKey
	if rawAPIKey == "" {
		t.Fatalf("create api key: empty api_key in response: %s", body)
	}
	if apiKeyResp.APIKey.ChannelID == nil || *apiKeyResp.APIKey.ChannelID != channelID {
		t.Fatalf("create api key: channel_id = %v, want %q", apiKeyResp.APIKey.ChannelID, channelID)
	}

	// A local origin for the event's artwork (feature #535): the import
	// side-loads it into mediastore, and GET_ALL_ACTIONS must later hand back
	// an absolute, signed URL that resolves to these exact bytes.
	posterBytes := []byte("\x89PNG\r\n\x1a\nprov533-poster")
	poster := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(posterBytes)
	}))
	t.Cleanup(poster.Close)

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
	//
	// venue.cityName "Praha" (spec 22 §1 item 4 / feature #537) and
	// action.bigPosterUrl (feature #535) are the two remaining site-facing
	// gaps this epic-verify test closes the loop on.
	importBody := fmt.Sprintf(`{
		"source": "arena",
		"externalRef": "prov533-%s",
		"publish": true,
		"action": {
			"actionName": "Prov533 Event %s",
			"fullActionName": "Prov533 Event %s",
			"bigPosterUrl": %q
		},
		"actionEvent": {
			"day": "01.06.2027",
			"time": "19:00",
			"currency": "EUR"
		},
		"venue": {
			"venueName": "Prov533 Venue %s",
			"address": "1 Test Street",
			"cityName": "Praha",
			"countryName": "Czechia",
			"timezone": "Europe/Tallinn"
		},
		"categoryList": [
			{"categoryPriceName": "Standard", "price": 15.00, "availability": 100},
			{"categoryPriceName": "VIP", "price": 20.00, "availability": 50}
		]
	}`, suffix, suffix, suffix, poster.URL+"/poster.png", suffix)

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
		Publication *struct {
			ChannelID     string `json:"channel_id"`
			FeedTokenID   string `json:"feed_token_id"`
			PublicationID string `json:"publication_id"`
		} `json:"publication"`
		Warnings []importWarning `json:"warnings"`
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
	for _, w := range importResp.Warnings {
		if w.Code == "import.channel_publication_skipped" {
			t.Fatalf("import event-bundle: got import.channel_publication_skipped for a channel-bound key, body: %s", body)
		}
	}
	if importResp.Publication == nil {
		t.Fatalf("import event-bundle: publication = null, want an auto-publication object for a channel-bound key, body: %s", body)
	}
	if importResp.Publication.ChannelID != channelID {
		t.Errorf("import publication.channel_id = %q, want the key's channel %s", importResp.Publication.ChannelID, channelID)
	}
	if importResp.Publication.FeedTokenID == "" || importResp.Publication.PublicationID == "" {
		t.Fatalf("import publication missing feed_token_id/publication_id: %+v", importResp.Publication)
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
		CityList   []struct {
			CityName  string `json:"cityName"`
			VenueList []struct {
				VenueName string `json:"venueName"`
			} `json:"venueList"`
		} `json:"cityList"`
		ActionList []struct {
			ActionName   string  `json:"actionName"`
			MinPrice     float64 `json:"minPrice"`
			MaxPrice     float64 `json:"maxPrice"`
			BigPosterURL string  `json:"bigPosterUrl"`
		} `json:"actionList"`
	}
	if err := json.Unmarshal([]byte(body), &catalogResp); err != nil {
		t.Fatalf("decode GET_ALL_ACTIONS response: %v; body: %s", err, body)
	}
	if catalogResp.ResultCode != 0 {
		t.Fatalf("GET_ALL_ACTIONS: resultCode = %d, want 0, body: %s", catalogResp.ResultCode, body)
	}
	found := false
	var bigPosterURL string
	for _, a := range catalogResp.ActionList {
		if a.ActionName == "Prov533 Event "+suffix {
			found = true
			if a.MinPrice != 15.00 {
				t.Errorf("GET_ALL_ACTIONS: minPrice = %v, want 15 (major units)", a.MinPrice)
			}
			if a.MaxPrice != 20.00 {
				t.Errorf("GET_ALL_ACTIONS: maxPrice = %v, want 20 (major units)", a.MaxPrice)
			}
			bigPosterURL = a.BigPosterURL
		}
	}
	if !found {
		t.Fatalf("GET_ALL_ACTIONS: imported action %q not found in actionList, body: %s", "Prov533 Event "+suffix, body)
	}

	// ── 11a. City name (feature #537): "Praha" comes back in its original
	// case, not the lowercased "praha" slug — GET_ALL_ACTIONS.cityList is
	// matched by the venue this import minted, so a global slug collision
	// with pre-existing data (cities.slug is NOT org-scoped, AGENTS.md) can
	// never make this assertion pass by accident. ──────────────────────────
	venueName := "Prov533 Venue " + suffix
	gotCity := ""
	for _, c := range catalogResp.CityList {
		for _, v := range c.VenueList {
			if v.VenueName == venueName {
				gotCity = c.CityName
			}
		}
	}
	if gotCity != "Praha" {
		t.Errorf("GET_ALL_ACTIONS: cityName hosting venue %q = %q, want \"Praha\" "+
			"(a lowercase value means the slug fallback fired and no translation was written); "+
			"import warnings=%v; catalog body=%s", venueName, gotCity, importResp.Warnings, body)
	}

	// ── 11b. Poster URL (feature #535): absolute, signed, and fetchable ─────
	if bigPosterURL == "" {
		t.Fatalf("GET_ALL_ACTIONS: imported action has no bigPosterUrl, body: %s", body)
	}
	if !strings.HasPrefix(bigPosterURL, prov533APIPublicURL+"/v1/media-files/") {
		t.Fatalf("bigPosterUrl = %q, want an absolute %s/v1/media-files/{uuid} URL",
			bigPosterURL, prov533APIPublicURL)
	}
	posterURLParsed, err := url.Parse(bigPosterURL)
	if err != nil {
		t.Fatalf("bigPosterUrl %q does not parse: %v", bigPosterURL, err)
	}
	if posterURLParsed.Query().Get("expires") == "" || posterURLParsed.Query().Get("sig") == "" {
		t.Fatalf("bigPosterUrl = %q, want the mediastore expires+sig pair", bigPosterURL)
	}
	posterResp, err := http.Get(ts.URL + posterURLParsed.RequestURI()) //nolint:noctx // short-lived test fetch
	if err != nil {
		t.Fatalf("GET the signed bigPosterUrl: %v", err)
	}
	posterBody, err := io.ReadAll(posterResp.Body)
	_ = posterResp.Body.Close()
	if err != nil {
		t.Fatalf("read signed bigPosterUrl body: %v", err)
	}
	if posterResp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s (the signed bigPosterUrl) status = %d, want 200 (body %.200s)",
			posterURLParsed.RequestURI(), posterResp.StatusCode, posterBody)
	}
	if !bytes.Equal(posterBody, posterBytes) {
		t.Errorf("signed bigPosterUrl returned %d bytes (%q), want the %d poster bytes (%q)",
			len(posterBody), posterBody, len(posterBytes), posterBytes)
	}
	// Negative control: the same object without expires/sig must stay denied,
	// so the 200 above was earned by the signature, not an open route.
	unsignedResp, err := http.Get(ts.URL + posterURLParsed.Path) //nolint:noctx // short-lived test fetch
	if err != nil {
		t.Fatalf("GET the unsigned media path: %v", err)
	}
	_ = unsignedResp.Body.Close()
	if unsignedResp.StatusCode == http.StatusOK {
		t.Errorf("GET %s without expires/sig returned 200 — the media route must stay signature-gated", posterURLParsed.Path)
	}

	// ── 9. The auto-publication rows the import minted in step 7 (feature
	// #536) — no manual feed-token/publications call anywhere in this test.
	var (
		autoFeedTokenChannel uuid.UUID
		autoFeedTokenLabel   string
	)
	if err := srv.pgxPool.QueryRow(ctx,
		`SELECT sales_channel_id, label FROM agent_feed_tokens WHERE id = $1`, importResp.Publication.FeedTokenID,
	).Scan(&autoFeedTokenChannel, &autoFeedTokenLabel); err != nil {
		t.Fatalf("read the auto-minted feed token %s: %v", importResp.Publication.FeedTokenID, err)
	}
	if autoFeedTokenChannel.String() != channelID {
		t.Errorf("auto-minted feed token %s hangs off channel %s, want %s",
			importResp.Publication.FeedTokenID, autoFeedTokenChannel, channelID)
	}
	if autoFeedTokenLabel != "auto:event-bundle" {
		t.Errorf("auto-minted feed token label = %q, want auto:event-bundle", autoFeedTokenLabel)
	}
	var autoPubEvent uuid.UUID
	if err := srv.pgxPool.QueryRow(ctx,
		`SELECT event_id FROM event_publications WHERE id = $1`, importResp.Publication.PublicationID,
	).Scan(&autoPubEvent); err != nil {
		t.Fatalf("read the auto-created publication %s: %v", importResp.Publication.PublicationID, err)
	}
	if autoPubEvent.String() != eventID {
		t.Errorf("auto-created publication %s references event %s, want %s",
			importResp.Publication.PublicationID, autoPubEvent, eventID)
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

// TestSuperadminOrgProvisioning538_JWTActorNoAutoPublication is the flip side
// of feature #536: a real JWT user actor (the platform_superadmin itself,
// minted through the same real login flow as the main test above, not a
// service API key) imports an arena bundle with publish:true directly. The
// #536 auto-publication logic only fires for a SERVICE actor carrying a
// channel (auth.Actor.ChannelID, copied only in server_apikey_auth.go), so a
// human/JWT actor must get the same "no channel to publish into" outcome as
// an unbound API key: publication = null, the import.channel_publication_skipped
// warning, and — the assertion the spec calls out explicitly — no
// event_publications row and no agent_feed_tokens row for the event at all.
func TestSuperadminOrgProvisioning538_JWTActorNoAutoPublication(t *testing.T) {
	srv, _ := superadminProvisioning533Server(t)
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	client := ts.Client()
	ctx := context.Background()

	superadminEmail := fmt.Sprintf("w1s1-538-superadmin-%d@example.com", time.Now().UnixNano())
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

	suffix := uuid.New().String()[:8]
	orgBody := fmt.Sprintf(
		`{"name":%q,"slug":%q,"country":"EE","default_locale":"en","reservation_ttl_seconds":1200}`,
		"W1-S1 538b Org "+suffix, "w1-s1-538b-org-"+suffix,
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

	// A channel exists in this org, but the actor below is never bound to it —
	// only proving that a channel's mere EXISTENCE does not trigger
	// auto-publication is the point of this test.
	channelBody := fmt.Sprintf(`{"name":%q,"payment_mode":"merchant_of_record"}`, "Prov538b Channel "+suffix)
	resp = provDoRequestWithReasonAndBody(t, client, http.MethodPost,
		ts.URL+"/v1/organizations/"+orgID+"/channels", superadminToken, "W1-S1 538b provisioning test", channelBody)
	body = integReadBody(t, resp)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create channel: expected 201, got %d, body: %s", resp.StatusCode, body)
	}

	// The import itself, authenticated with the superadmin's own JWT
	// (auth.Actor for a JWT-authenticated request is a "user" actor — its
	// ChannelID is always nil, see auth.Actor and server_apikey_auth.go).
	// platform_superadmin holds import.bil24_session via migration 0100's
	// permission-parity catch-up, and markSuperadminOrgAccess grants the
	// cross-tenant bypass for a non-member superadmin on this org-scoped route.
	importBody := fmt.Sprintf(`{
		"source": "arena",
		"externalRef": "prov538b-%s",
		"publish": true,
		"action": {
			"actionName": "Prov538b Event %s",
			"fullActionName": "Prov538b Event %s"
		},
		"actionEvent": {
			"day": "01.06.2027",
			"time": "19:00",
			"currency": "EUR"
		},
		"venue": {
			"venueName": "Prov538b Venue %s",
			"address": "1 Test Street",
			"timezone": "Europe/Tallinn"
		},
		"categoryList": [
			{"categoryPriceName": "Standard", "price": 10.00, "availability": 20}
		]
	}`, suffix, suffix, suffix, suffix)

	resp = provDoRequestWithReasonAndBody(t, client, http.MethodPost,
		ts.URL+"/v1/organizations/"+orgID+"/imports/event-bundle", superadminToken,
		"W1-S1 538b provisioning test", importBody)
	body = integReadBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("import event-bundle (JWT actor): expected 200, got %d, body: %s", resp.StatusCode, body)
	}
	var importResp struct {
		EventID     string          `json:"event_id"`
		Created     bool            `json:"created"`
		Publication *struct{}       `json:"publication"`
		Warnings    []importWarning `json:"warnings"`
	}
	if err := json.Unmarshal([]byte(body), &importResp); err != nil {
		t.Fatalf("decode import response: %v; body: %s", err, body)
	}
	if !importResp.Created {
		t.Fatalf("import event-bundle: expected created=true, body: %s", body)
	}
	eventID := importResp.EventID
	if eventID == "" {
		t.Fatalf("import event-bundle: empty event_id in response: %s", body)
	}

	if importResp.Publication != nil {
		t.Errorf("import response publication = %#v, want null for a JWT user actor", importResp.Publication)
	}
	// ensureChannelPublication (import_publication.go) short-circuits to
	// (nil, nil) — no warning at all — for any actor whose auth.Actor.Type
	// isn't auth.ActorTypeService: a human operator publishes through the
	// admin UI, where the channel is an explicit choice, so there is nothing
	// to warn about. WarnChannelPublicationSkipped fires only for a SERVICE
	// (API-key) actor that IS eligible for auto-publication but has no bound
	// channel — a case this test does not construct. Assert its absence here
	// instead, so a regression that started stamping the warning onto human
	// imports (which would be misleading — nothing was skipped, it was never
	// attempted) is caught.
	for _, w := range importResp.Warnings {
		if w.Code == "import.channel_publication_skipped" {
			t.Errorf("import response warnings = %v, want no import.channel_publication_skipped for a JWT/human actor "+
				"(that warning is reserved for a channel-less SERVICE actor)", importResp.Warnings)
		}
	}

	// The assertion the spec calls out explicitly: NO event_publications row
	// and NO agent_feed_tokens row were minted for this event — a JWT/human
	// actor must never auto-publish, regardless of what channels exist in
	// the organization.
	var pubCount int
	if err := srv.pgxPool.QueryRow(ctx,
		`SELECT count(*) FROM event_publications WHERE event_id = $1`, eventID,
	).Scan(&pubCount); err != nil {
		t.Fatalf("count event_publications for %s: %v", eventID, err)
	}
	if pubCount != 0 {
		t.Errorf("event_publications for event %s = %d, want 0 — a JWT actor must never auto-publish", eventID, pubCount)
	}
	var feedTokenCount int
	if err := srv.pgxPool.QueryRow(ctx,
		`SELECT count(*) FROM agent_feed_tokens WHERE label = 'auto:event-bundle'
		   AND sales_channel_id IN (SELECT id FROM sales_channels WHERE org_id = $1)`, orgID,
	).Scan(&feedTokenCount); err != nil {
		t.Fatalf("count auto-minted feed tokens for org %s: %v", orgID, err)
	}
	if feedTokenCount != 0 {
		t.Errorf("auto:event-bundle feed tokens for org %s = %d, want 0 — a JWT actor must never mint one", orgID, feedTokenCount)
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

// importWarning mirrors the {code,message} objects the import endpoints put
// in `warnings` (spec 19 section 5) — on a fresh database the bundle legitimately
// returns advisory warnings, so the response must decode as objects, not strings.
type importWarning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
