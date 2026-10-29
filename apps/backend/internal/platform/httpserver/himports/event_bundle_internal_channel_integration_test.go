//go:build integration

package himports

// The organization's hidden "Invitations" channel (sales_channels.kind =
// 'internal', spec 46, task INV-01) must never be accepted as an
// event-bundle channelIds entry: binding an event to it would publish the
// event into a channel no storefront serves. Run against a migrated database:
//
//	DATABASE_URL=postgres://arena:arena@localhost:45432/arena_ci?sslmode=disable \
//	  go test -tags integration ./apps/backend/internal/platform/httpserver/himports/ -run InternalChannel

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/invitations"
)

func TestEventBundle_ChannelIDs_RefusesTheInternalChannel(t *testing.T) {
	pool := import517Pool(t)
	ctx := context.Background()
	f := newBundle525Fixture(t, ctx, pool)
	defer f.cleanup()

	internal, err := invitations.EnsureInternalChannel(ctx, gen.New(pool), f.orgID)
	if err != nil {
		t.Fatalf("EnsureInternalChannel: %v", err)
	}
	defer func() { _, _ = pool.Exec(ctx, `DELETE FROM sales_channels WHERE id = $1`, internal) }()

	h := newBundle525Handler(t, pool)
	body := f.payload()
	body.Publish = true
	body.ChannelIDs = []string{internal.String()}
	rec, _ := f.call(h, body)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "import.invalid_channel") {
		t.Fatalf("import: status = %d body=%s; want 422 import.invalid_channel", rec.Code, rec.Body.String())
	}
	out := dryRun(t, f, h, body)
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if out.Valid || !strings.Contains(string(raw), "import.invalid_channel") {
		t.Fatalf("dry run must name import.invalid_channel: %s", raw)
	}
	var events int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM events WHERE org_id = $1`, f.orgID).Scan(&events); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if events != 0 {
		t.Fatalf("a refused channel list must not create the event; events = %d", events)
	}
	var pubs int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM agent_feed_tokens WHERE sales_channel_id = $1`, internal).Scan(&pubs); err != nil {
		t.Fatalf("count feed tokens: %v", err)
	}
	if pubs != 0 {
		t.Fatalf("the internal channel got %d feed tokens", pubs)
	}
}
