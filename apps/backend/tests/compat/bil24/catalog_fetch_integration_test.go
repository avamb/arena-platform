//go:build integration

// catalog_fetch_integration_test.go — migration 0152: a successful
// GET_ALL_ACTIONS of an authenticated site records when the fetch STARTED in
// channel_catalog_fetches, so the event watcher can tell the operator that the
// site has picked up an announced change. The write is throttled: a repeat
// fetch within 30 s leaves the stored time alone.
package compat_bil24_test

import (
	"context"
	"testing"
	"time"
)

func TestCompatBil24_GetAllActions_RecordsCatalogFetch(t *testing.T) {
	st := setupHarness(t)
	base := startHarnessServer(t, st)
	ctx := context.Background()
	channelID := pub536ChannelID(t, st)

	pub536Exec(t, st, `DELETE FROM channel_catalog_fetches WHERE channel_id = $1`, channelID)
	t.Cleanup(func() {
		_, _ = st.Pool.Exec(context.Background(), `DELETE FROM channel_catalog_fetches WHERE channel_id = $1`, channelID)
	})

	fetch := func() {
		t.Helper()
		req, _ := loadWPFixture(t, "GET_ALL_ACTIONS", "basic")
		req["fid"] = st.ChannelFID
		req["token"] = st.ChannelToken
		resp := postBil24(t, base, req)
		if code := numberField(t, resp, "resultCode"); code != 0 {
			t.Fatalf("GET_ALL_ACTIONS resultCode = %v, want 0 (description %v)", code, resp["description"])
		}
	}
	stored := func() time.Time {
		t.Helper()
		var at time.Time
		if err := st.Pool.QueryRow(ctx,
			`SELECT last_fetched_at FROM channel_catalog_fetches WHERE channel_id = $1`, channelID,
		).Scan(&at); err != nil {
			t.Fatalf("read channel_catalog_fetches: %v", err)
		}
		return at
	}

	before := time.Now().Add(-time.Second)
	fetch()
	after := time.Now().Add(time.Second)
	first := stored()
	if first.Before(before) || first.After(after) {
		t.Fatalf("last_fetched_at = %s, want between %s and %s", first, before, after)
	}

	// A repeat within the throttle window does not move it.
	fetch()
	if got := stored(); !got.Equal(first) {
		t.Errorf("a repeat fetch within 30 s rewrote last_fetched_at: %s → %s", first, got)
	}

	// An older stored value is replaced by the next fetch.
	pub536Exec(t, st, `UPDATE channel_catalog_fetches SET last_fetched_at = now() - interval '5 minutes' WHERE channel_id = $1`, channelID)
	before = time.Now().Add(-time.Second)
	fetch()
	if got := stored(); got.Before(before) {
		t.Errorf("a fetch after the throttle window did not record: last_fetched_at = %s, want >= %s", got, before)
	}
}
