//go:build integration

// test_channel_catalog_integration_test.go — spec 62 6.3: an event published
// only into TEST sales channels (sales_channels.is_test, migration 0202) must
// never reach the catalogue a non-test channel's GET_ALL_ACTIONS returns. The
// gateway used to list every published event of the organization and ignore
// publications altogether, so a test event showed up on the live WordPress
// site of the same organization (production, 2026-11-07).
package compat_bil24_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

func TestCompatBil24_GetAllActions_HidesTestChannelOnlyEvents(t *testing.T) {
	st := setupHarness(t)
	base := startHarnessServer(t, st)
	ctx := context.Background()
	suffix := uuid.NewString()[:8]

	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := st.Pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("exec %q: %v", sql, err)
		}
	}

	normalChannelID := pub536ChannelID(t, st)

	// A second, TEST gateway channel of the same organization with its own token.
	testToken := "test-channel-token-" + suffix
	hash, err := bcrypt.GenerateFromPassword([]byte(testToken), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("bcrypt: %v", err)
	}
	testChannelID := uuid.New()
	var testFID int64
	if err := st.Pool.QueryRow(ctx,
		`INSERT INTO sales_channels (id, org_id, name, provider, payment_mode, fee_percent, settings, is_test)
		 VALUES ($1, $2, $3, 'stripe', 'direct_merchant', 0, jsonb_build_object('gateway_token_hash', $4::text), true)
		 RETURNING display_number`,
		testChannelID, st.OrgID, "Test channel "+suffix, string(hash),
	).Scan(&testFID); err != nil {
		t.Fatalf("seed test channel: %v", err)
	}

	tokenFor := func(channelID uuid.UUID, label string) uuid.UUID {
		t.Helper()
		id := uuid.New()
		exec(`INSERT INTO agent_feed_tokens (id, token, sales_channel_id, label) VALUES ($1, $2, $3, $4)`,
			id, "tok-"+label+"-"+suffix, channelID, label)
		return id
	}
	normalToken := tokenFor(normalChannelID, "normal")
	testTokenID := tokenFor(testChannelID, "test")

	start := time.Now().Add(20 * 24 * time.Hour).UTC().Truncate(time.Second)
	mkEvent := func(name string) uuid.UUID {
		t.Helper()
		eventID, sessionID := uuid.New(), uuid.New()
		exec(`INSERT INTO events (id, org_id, name, status, visibility) VALUES ($1, $2, $3, 'published', 'public')`,
			eventID, st.OrgID, name)
		exec(`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, status,
		          admission_mode, currency, currency_source)
		      VALUES ($1, $2, $3, $4, $5, 10, 'scheduled', 'general_admission', 'CZK', 'derived')`,
			sessionID, eventID, st.VenueID, start, start.Add(2*time.Hour))
		t.Cleanup(func() {
			bg := context.Background()
			_, _ = st.Pool.Exec(bg, `DELETE FROM event_publications WHERE event_id = $1`, eventID)
			_, _ = st.Pool.Exec(bg, `DELETE FROM inventory_ledger WHERE session_id = $1`, sessionID)
			_, _ = st.Pool.Exec(bg, `DELETE FROM session_seats WHERE session_id = $1`, sessionID)
			_, _ = st.Pool.Exec(bg, `DELETE FROM sessions WHERE id = $1`, sessionID)
			_, _ = st.Pool.Exec(bg, `DELETE FROM events WHERE id = $1`, eventID)
		})
		return eventID
	}
	nameA := "Live event A " + suffix
	nameB := "Test-only event B " + suffix
	eventA := mkEvent(nameA)
	eventB := mkEvent(nameB)
	publish := func(eventID, tokenID uuid.UUID) {
		t.Helper()
		exec(`INSERT INTO event_publications (event_id, feed_token_id) VALUES ($1, $2)`, eventID, tokenID)
	}
	publish(eventA, normalToken)
	publish(eventB, testTokenID)

	t.Cleanup(func() {
		bg := context.Background()
		_, _ = st.Pool.Exec(bg, `DELETE FROM agent_feed_tokens WHERE id = ANY($1)`, []uuid.UUID{normalToken, testTokenID})
		_, _ = st.Pool.Exec(bg, `DELETE FROM channel_catalog_fetches WHERE channel_id = ANY($1)`, []uuid.UUID{normalChannelID, testChannelID})
		_, _ = st.Pool.Exec(bg, `DELETE FROM sales_channels WHERE id = $1`, testChannelID)
	})

	listed := func(fid int64, token string) map[string]bool {
		t.Helper()
		req, _ := loadWPFixture(t, "GET_ALL_ACTIONS", "basic")
		req["fid"] = fid
		req["token"] = token
		resp := postBil24(t, base, req)
		if code := numberField(t, resp, "resultCode"); code != 0 {
			t.Fatalf("GET_ALL_ACTIONS resultCode = %v, want 0 (description %v)", code, resp["description"])
		}
		names := map[string]bool{}
		for _, a := range sc1Objects(t, resp, "actionList") {
			if n, ok := a["actionName"].(string); ok {
				names[n] = true
			}
		}
		return names
	}

	normal := listed(st.ChannelFID, st.ChannelToken)
	if !normal[nameA] {
		t.Errorf("normal channel catalogue lacks event A %q: %v", nameA, normal)
	}
	if normal[nameB] {
		t.Errorf("normal channel catalogue leaks the test-only event B %q: %v", nameB, normal)
	}

	test := listed(testFID, testToken)
	if !test[nameB] {
		t.Errorf("test channel catalogue lacks event B %q: %v", nameB, test)
	}
	if !test[nameA] {
		t.Errorf("test channel catalogue lacks event A %q: %v", nameA, test)
	}

	// Publishing B into the normal channel as well makes it visible there.
	publish(eventB, normalToken)
	if got := listed(st.ChannelFID, st.ChannelToken); !got[nameB] {
		t.Errorf("event B published in a normal channel too is still hidden: %v", got)
	}
}
