//go:build integration

// channel_publication_536_integration_test.go — feature #536 (W1-S1b), spec
// 08_architecture/22_site_facing_gaps_w1s1_ru.md §2.2.
//
// The gap this pins shut: a site that posts an event bundle with publish:true
// used to get a *published* event that reached nobody. The WP fan-out join
// (gen.ListWPSubscribersForEvent) walks
//
//	event_publications → agent_feed_tokens → webhook_subscribers(kind='bil24_wp')
//
// so an event that was never published INTO a channel has zero subscribers and
// the `v1.event.published` outbox row is dispatched to no one — silently, with
// no error anywhere. Feature #536 closes that by binding the import to the
// sales channel of the CALLING API key, inside the import transaction.
//
// Everything below the wpstub receiver is real: the full httpserver, the real
// /v1/organizations/{org}/imports/event-bundle route, the real
// outbox.OutboxEventsDispatcher polling loop and the real bil24wire.Dispatcher
// behind the same fan-out cmd/arena-worker/main.go uses. The only thing the
// test does NOT do is the part the feature removes: there is no manual
// feed-token creation and no manual POST /v1/events/{id}/publications.
//
// Prerequisites:
//
//	DATABASE_URL=postgres://arena:arena@localhost:55432/arena?sslmode=disable
//	JWT_SIGNING_SECRET=<anything>
//
// Run with:
//
//	go test -tags integration -run TestCompatBil24_536 ./apps/backend/tests/compat/bil24/
package compat_bil24_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/bil24wire"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/outbox"
	"github.com/abhteam/arena_new/apps/backend/tests/compat/bil24/wpstub"
)

// TestCompatBil24_536_ChannelBoundKeyPublishesAndReachesSite is the happy
// path: a channel-bound service key posts a publish:true bundle and the site
// receives event.created naming the very action_event_id the import response
// reported — with no manual publication step anywhere.
func TestCompatBil24_536_ChannelBoundKeyPublishesAndReachesSite(t *testing.T) {
	st := setupHarness(t)
	base := startHarnessServer(t, st)
	ctx := context.Background()

	orgID, err := uuid.Parse(st.OrgID)
	if err != nil {
		t.Fatalf("parse st.OrgID: %v", err)
	}
	channelID := pub536ChannelID(t, st)

	// The WordPress site: one active bil24_wp subscriber on the channel the
	// key below is bound to. This is the only half of the chain an operator
	// configures by hand; the other two links are what #536 now creates.
	recv := wpstub.New()
	defer recv.Close()
	pub536Exec(t, st, `INSERT INTO webhook_subscribers (site_url, callback_url, signing_secret,
			event_types, active, kind, org_id, channel_id)
		VALUES ('', $1, 'wp536-secret', '{}', TRUE, 'bil24_wp', $2, $3)`,
		recv.URL(), orgID, channelID)
	t.Cleanup(func() {
		pub536Cleanup(t, st, `DELETE FROM webhook_subscribers WHERE channel_id = $1 AND callback_url = $2`,
			channelID, recv.URL())
	})

	rawKey := pub536ImportKey(t, st, base, orgID, &channelID)

	poster := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("\x89PNG\r\n\x1a\nharness-536"))
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
	pub536RegisterPublicationCleanup(t, st, eventID, channelID)

	if sc8HasWarning(resp, "import.channel_publication_skipped") {
		t.Errorf("a channel-bound key must NOT raise import.channel_publication_skipped (warnings %v)",
			resp["warnings"])
	}

	// ── the response block the site persists ────────────────────────────────
	publication, ok := resp["publication"].(map[string]interface{})
	if !ok {
		t.Fatalf("import response publication = %#v, want an object for a channel-bound key", resp["publication"])
	}
	if got, _ := publication["channel_id"].(string); got != channelID.String() {
		t.Errorf("publication.channel_id = %q, want the key's channel %s", got, channelID)
	}
	feedTokenID := sc8UUIDField(t, publication, "feed_token_id")
	publicationID := sc8UUIDField(t, publication, "publication_id")

	// ── the rows themselves — published INTO the channel, not just published ─
	var (
		tokenChannel uuid.UUID
		tokenLabel   string
	)
	if err := st.Pool.QueryRow(ctx,
		`SELECT sales_channel_id, label FROM agent_feed_tokens WHERE id = $1`, feedTokenID,
	).Scan(&tokenChannel, &tokenLabel); err != nil {
		t.Fatalf("read the auto-minted feed token %s: %v", feedTokenID, err)
	}
	if tokenChannel != channelID {
		t.Errorf("feed token %s hangs off channel %s, want %s", feedTokenID, tokenChannel, channelID)
	}
	if tokenLabel != "auto:event-bundle" {
		t.Errorf("feed token label = %q, want auto:event-bundle so an operator can tell it apart", tokenLabel)
	}
	var pubEvent, pubToken uuid.UUID
	if err := st.Pool.QueryRow(ctx,
		`SELECT event_id, feed_token_id FROM event_publications WHERE id = $1`, publicationID,
	).Scan(&pubEvent, &pubToken); err != nil {
		t.Fatalf("read the publication row %s: %v", publicationID, err)
	}
	if pubEvent != eventID || pubToken != feedTokenID {
		t.Errorf("event_publications row = (event %s, token %s), want (%s, %s)",
			pubEvent, pubToken, eventID, feedTokenID)
	}

	wantActionEventID := int64(numberField(t, pub536CompatIDs(t, resp), "action_event_id"))

	// ── delivery: the real outbox + the real bil24wire dispatcher ───────────
	// The import's publish:true already wrote the v1.event.published row after
	// commit; nothing else in this test touches the outbox.
	wpDispatcher := bil24wire.NewDispatcher(st.Pool)
	if wpDispatcher == nil {
		t.Fatal("bil24wire.NewDispatcher returned nil for a live pool")
	}
	fanOut := &wp508MultiDispatcher{dispatchers: []outbox.Dispatcher{
		wp508NoopDispatcher{}, // base outbox dispatcher
		wp508NoopDispatcher{}, // MACS dispatcher
		wpDispatcher,          // bil24_wp dispatcher
	}}
	dispatchOpts := outbox.OutboxEventsDispatcherOptions{
		Store:        outbox.NewPGOutboxEventStore(st.Pool),
		Dispatcher:   fanOut,
		PollInterval: 20 * time.Millisecond,
		MaxAttempts:  5,
		BackoffFunc:  func(int) time.Duration { return time.Hour },
	}
	if !wp508Drain(t, dispatchOpts, func() bool {
		return wp508Occurrences(recv, "event.created") > 0
	}, 20*time.Second) {
		t.Fatal("the imported event never reached the site as event.created — " +
			"the publish:true import must bind the event to the calling key's channel")
	}

	delivered, ok := wp508Last(recv, "event.created")
	if !ok {
		t.Fatal("no event.created in the receiver log")
	}
	sawActionEventID := false
	for _, entry := range delivered.DataList {
		if id, ok := entry["actionEventId"].(float64); ok && int64(id) == wantActionEventID {
			sawActionEventID = true
		}
	}
	if !sawActionEventID {
		t.Fatalf("event.created data %+v does not name the imported action_event_id %d",
			delivered.DataList, wantActionEventID)
	}

	// ── repeat: idempotent, no second token and no second publication ───────
	tokensBefore := pub536Count(t, st, `SELECT count(*) FROM agent_feed_tokens WHERE sales_channel_id = $1`, channelID)
	pubsBefore := pub536Count(t, st, `SELECT count(*) FROM event_publications WHERE event_id = $1`, eventID)

	status2, resp2 := restJSON(t, base, "POST",
		"/v1/organizations/"+st.OrgID+"/imports/event-bundle", rawKey, nil, body)
	if status2 != 200 {
		t.Fatalf("repeated import status = %d, want 200 (body %v)", status2, resp2)
	}
	if created, _ := resp2["created"].(bool); created {
		t.Errorf("repeated import created = true, want false — the bundle is idempotent on externalRef")
	}
	publication2, ok := resp2["publication"].(map[string]interface{})
	if !ok {
		t.Fatalf("repeated import publication = %#v, want the same object again", resp2["publication"])
	}
	if got, _ := publication2["feed_token_id"].(string); got != feedTokenID.String() {
		t.Errorf("repeated import feed_token_id = %q, want the reused token %s", got, feedTokenID)
	}
	if got, _ := publication2["publication_id"].(string); got != publicationID.String() {
		t.Errorf("repeated import publication_id = %q, want the reused publication %s", got, publicationID)
	}
	if got := pub536Count(t, st, `SELECT count(*) FROM agent_feed_tokens WHERE sales_channel_id = $1`, channelID); got != tokensBefore {
		t.Errorf("agent_feed_tokens for channel %s = %d after the repeat, want %d — the import must REUSE an active token",
			channelID, got, tokensBefore)
	}
	if got := pub536Count(t, st, `SELECT count(*) FROM event_publications WHERE event_id = $1`, eventID); got != pubsBefore {
		t.Errorf("event_publications for event %s = %d after the repeat, want %d — PublishEvent is ON CONFLICT DO NOTHING",
			eventID, got, pubsBefore)
	}
}

// TestCompatBil24_536_KeyWithoutChannelWarns is the diagnosable failure mode:
// a key that is not bound to any channel still imports and still publishes the
// event, but nothing is published into a channel — and the response says so
// instead of leaving the operator to discover the silence.
func TestCompatBil24_536_KeyWithoutChannelWarns(t *testing.T) {
	st := setupHarness(t)
	base := startHarnessServer(t, st)

	orgID, err := uuid.Parse(st.OrgID)
	if err != nil {
		t.Fatalf("parse st.OrgID: %v", err)
	}
	rawKey := pub536ImportKey(t, st, base, orgID, nil)

	poster := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("\x89PNG\r\n\x1a\nharness-536b"))
	}))
	defer poster.Close()

	status, resp := restJSON(t, base, "POST",
		"/v1/organizations/"+st.OrgID+"/imports/event-bundle", rawKey, nil,
		bundle527Fixture(t, poster.URL+"/poster.png"))
	if status != 200 {
		t.Fatalf("POST imports/event-bundle status = %d, want 200 (body %v)", status, resp)
	}
	eventID := sc8UUIDField(t, resp, "event_id")
	sessionID := sc8UUIDField(t, resp, "session_id")
	bundle527RegisterCleanup(t, st, eventID, sessionID)

	if resp["publication"] != nil {
		t.Errorf("publication = %#v, want null for a key that is not bound to a channel", resp["publication"])
	}
	if !sc8HasWarning(resp, "import.channel_publication_skipped") {
		t.Errorf("warnings = %v, want an import.channel_publication_skipped entry", resp["warnings"])
	}
	if got := pub536Count(t, st, `SELECT count(*) FROM event_publications WHERE event_id = $1`, eventID); got != 0 {
		t.Errorf("event_publications for event %s = %d, want 0 — there is no channel to publish into", eventID, got)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// helpers
// ─────────────────────────────────────────────────────────────────────────────

// pub536ChannelID resolves the harness channel's uuid from its wire fid.
func pub536ChannelID(t *testing.T, st *harnessState) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := st.Pool.QueryRow(context.Background(),
		`SELECT id FROM sales_channels WHERE display_number = $1`, st.ChannelFID,
	).Scan(&id); err != nil {
		t.Fatalf("resolve harness channel uuid for fid %d: %v", st.ChannelFID, err)
	}
	return id
}

func pub536Exec(t *testing.T, st *harnessState, sql string, args ...any) {
	t.Helper()
	if _, err := st.Pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("exec %.60s… : %v", sql, err)
	}
}

func pub536Cleanup(t *testing.T, st *harnessState, sql string, args ...any) {
	t.Helper()
	if _, err := st.Pool.Exec(context.Background(), sql, args...); err != nil {
		t.Logf("cleanup %.60s… : %v", sql, err)
	}
}

func pub536Count(t *testing.T, st *harnessState, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := st.Pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("count %.60s… : %v", sql, err)
	}
	return n
}

// pub536CompatIDs pulls the compat_ids object out of an import response.
func pub536CompatIDs(t *testing.T, resp map[string]interface{}) map[string]interface{} {
	t.Helper()
	ids, ok := resp["compat_ids"].(map[string]interface{})
	if !ok {
		t.Fatalf("import response has no compat_ids object: %v", resp)
	}
	return ids
}

// pub536RegisterPublicationCleanup sweeps the two rows the import minted on
// the caller's behalf. Publications go first — they FK the feed token — and
// only the auto-minted token is removed, so a channel that already had an
// operator-created token keeps it.
func pub536RegisterPublicationCleanup(t *testing.T, st *harnessState, eventID, channelID uuid.UUID) {
	t.Helper()
	t.Cleanup(func() {
		pub536Cleanup(t, st, `DELETE FROM event_publications WHERE event_id = $1`, eventID)
		pub536Cleanup(t, st,
			`DELETE FROM agent_feed_tokens WHERE sales_channel_id = $1 AND label = 'auto:event-bundle'`,
			channelID)
		pub536Cleanup(t, st, `DELETE FROM outbox_events WHERE aggregate_id = $1::text`, eventID)
	})
}

// pub536ImportKey mints an org-admin user + membership and issues a service
// api key carrying the spec §13.1 scope set, optionally BOUND TO A CHANNEL.
// It mirrors sc8ImportKey (scenario08_import_test.go); the channel binding is
// the one thing that helper cannot express and the whole subject of #536.
func pub536ImportKey(t *testing.T, st *harnessState, base string, orgID uuid.UUID, channelID *uuid.UUID) string {
	t.Helper()
	ctx := context.Background()

	userID := uuid.New()
	pub536Exec(t, st, `INSERT INTO users (id, email, password_hash, email_verified_at)
		VALUES ($1, $2, 'x', now())`,
		userID, "harness-536-admin-"+userID.String()[:8]+"@example.test")
	t.Cleanup(func() {
		pub536Cleanup(t, st, `DELETE FROM users WHERE id = $1`, userID)
	})

	pub536Exec(t, st, `INSERT INTO memberships (user_id, org_id, role) VALUES ($1, $2, 'organizer')`,
		userID, orgID)
	t.Cleanup(func() {
		pub536Cleanup(t, st, `DELETE FROM memberships WHERE user_id = $1 AND org_id = $2`, userID, orgID)
	})

	stub := harnessStubAuth(t)
	adminJWT, _, err := stub.IssueToken(ctx, auth.IssueRequest{
		ActorID: userID.String(),
		Roles:   []string{"org_admin"},
		TTL:     time.Hour,
	})
	if err != nil {
		t.Fatalf("mint org-admin jwt: %v", err)
	}

	reqBody := map[string]any{"name": "W1-S1b import key", "scopes": sc9ScopeSet}
	if channelID != nil {
		reqBody["channel_id"] = channelID.String()
	}
	status, resp := restJSON(t, base, "POST", "/v1/organizations/"+st.OrgID+"/api-keys", adminJWT,
		map[string]string{"X-Admin-Reason": "feature #536 channel-publication harness"}, reqBody)
	if status != 201 {
		t.Fatalf("POST api-keys status = %d, want 201 (body %v)", status, resp)
	}
	keyObj, _ := resp["api_key"].(map[string]interface{})
	rawKey, _ := keyObj["api_key"].(string)
	keyID, _ := keyObj["id"].(string)
	if rawKey == "" || keyID == "" {
		t.Fatalf("POST api-keys response missing api_key/id: %v", resp)
	}
	// api_keys.created_by FK-references the user above and DELETE only
	// revokes, so the row must go first — t.Cleanup is LIFO, hence this
	// registration comes last.
	t.Cleanup(func() {
		pub536Cleanup(t, st, `DELETE FROM api_keys WHERE id = $1`, keyID)
	})
	return rawKey
}
