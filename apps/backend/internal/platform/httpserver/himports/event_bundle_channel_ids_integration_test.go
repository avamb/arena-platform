//go:build integration

package himports

// channelIds on the event-bundle (spec 28 §3.4): a human operator (JWT) has
// no api_keys.channel_id, so until this field existed an import from the
// Telegram event-center bot could publish the event but never bind it to a
// sales channel — no storefront listing, no site webhook. Run against a
// migrated database:
//
//	DATABASE_URL=postgres://arena:arena@localhost:45432/arena_ci?sslmode=disable \
//	  go test -tags integration ./apps/backend/internal/platform/httpserver/himports/ -run ChannelIDs

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

func TestEventBundle_ChannelIDs_UserPublishesIntoNamedChannels(t *testing.T) {
	pool := import517Pool(t)
	ctx := context.Background()
	f := newBundle525Fixture(t, ctx, pool)
	defer f.cleanup()
	q := gen.New(pool)

	chA, err := q.InsertSalesChannel(ctx, f.orgID, "Bot channel A "+uuid.NewString()[:6], "merchant_of_record", "stripe", nil, "0", nil, nil)
	if err != nil {
		t.Fatalf("InsertSalesChannel A: %v", err)
	}
	// Channel B is a WordPress site's: it carries a gateway credential, so the
	// import must never switch its storefront page on.
	chB, err := q.InsertSalesChannel(ctx, f.orgID, "Bot channel B "+uuid.NewString()[:6], "merchant_of_record", "stripe", nil, "0", nil,
		[]byte(`{"gateway":{"token_hash":"$2a$10$notarealhash"}}`))
	if err != nil {
		t.Fatalf("InsertSalesChannel B: %v", err)
	}
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM event_publications WHERE feed_token_id IN (SELECT id FROM agent_feed_tokens WHERE sales_channel_id = ANY($1))`, []uuid.UUID{chA.ID, chB.ID})
		_, _ = pool.Exec(ctx, `DELETE FROM agent_feed_tokens WHERE sales_channel_id = ANY($1)`, []uuid.UUID{chA.ID, chB.ID})
		_, _ = pool.Exec(ctx, `DELETE FROM sales_channels WHERE id = ANY($1)`, []uuid.UUID{chA.ID, chB.ID})
	}()

	h := newBundle525Handler(t, pool)

	// Two channels, one of them repeated: published once into each.
	body := f.payload()
	body.Publish = true
	body.ChannelIDs = []string{chA.ID.String(), chB.ID.String(), chA.ID.String()}
	rec, res := f.call(h, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("create: status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if len(res.Publications) != 2 {
		t.Fatalf("publications = %d, want 2: %+v", len(res.Publications), res.Publications)
	}
	if res.Publication == nil || res.Publication.ChannelID != chA.ID {
		t.Fatalf("publication (singular) should be the first binding into %s, got %+v", chA.ID, res.Publication)
	}
	if res.Publications[1].ChannelID != chB.ID {
		t.Fatalf("second binding should be channel B, got %+v", res.Publications[1])
	}
	hostedEnabled := false
	for _, w := range res.Warnings {
		if w.Code == WarnChannelPublicationSkipped {
			t.Fatalf("a user naming channels must not be told the publication was skipped: %+v", res.Warnings)
		}
		if w.Code == WarnHostedPageEnabled {
			hostedEnabled = true
		}
	}
	if !hostedEnabled {
		t.Fatalf("a channel without a page must be told its page was switched on: %+v", res.Warnings)
	}
	// Channel A (no gateway) now has its storefront page; channel B (a site)
	// keeps its settings exactly as they were.
	hostedFlag := func(id uuid.UUID) string {
		var v *string
		if err := pool.QueryRow(ctx, `SELECT settings #>> '{hosted_page,enabled}' FROM sales_channels WHERE id = $1`, id).Scan(&v); err != nil {
			t.Fatalf("read hosted_page flag of %s: %v", id, err)
		}
		if v == nil {
			return ""
		}
		return *v
	}
	if got := hostedFlag(chA.ID); got != "true" {
		t.Fatalf("channel A hosted_page.enabled = %q, want true", got)
	}
	if got := hostedFlag(chB.ID); got != "" {
		t.Fatalf("channel B (a site with a gateway credential) hosted_page.enabled = %q, want untouched", got)
	}
	var rows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM event_publications WHERE event_id = $1`, res.EventID).Scan(&rows); err != nil {
		t.Fatalf("count event_publications: %v", err)
	}
	if rows != 2 {
		t.Fatalf("event_publications rows = %d, want 2", rows)
	}

	// Repeating the bundle reuses the same feed tokens and publications.
	rec, again := f.call(h, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("repeat: status = %d; body=%s", rec.Code, rec.Body.String())
	}
	if len(again.Publications) != 2 || again.Publications[0].FeedTokenID != res.Publications[0].FeedTokenID ||
		again.Publications[0].PublicationID != res.Publications[0].PublicationID {
		t.Fatalf("repeat should reuse the bindings: first=%+v again=%+v", res.Publications, again.Publications)
	}
	for _, w := range again.Warnings {
		if w.Code == WarnHostedPageEnabled {
			t.Fatalf("an already-enabled page must not be reported again: %+v", again.Warnings)
		}
	}

	// Without channelIds a user binds nothing — and is not warned either.
	plain := f.payload()
	plain.ExternalRef = f.externalRef + "-plain"
	plain.Publish = true
	rec, none := f.call(h, plain)
	if rec.Code != http.StatusOK {
		t.Fatalf("plain: status = %d; body=%s", rec.Code, rec.Body.String())
	}
	if none.Publication != nil || len(none.Publications) != 0 {
		t.Fatalf("plain user import must bind nothing, got %+v", none.Publications)
	}
}

func TestEventBundle_ChannelIDs_RefusesForeignAndMalformed(t *testing.T) {
	pool := import517Pool(t)
	ctx := context.Background()
	f := newBundle525Fixture(t, ctx, pool)
	defer f.cleanup()
	q := gen.New(pool)

	other, err := q.InsertOrganization(ctx, "Other org "+uuid.NewString()[:6], "other-"+uuid.NewString(), "HU", "en", 1200)
	if err != nil {
		t.Fatalf("InsertOrganization: %v", err)
	}
	foreign, err := q.InsertSalesChannel(ctx, other.ID, "Foreign channel", "merchant_of_record", "stripe", nil, "0", nil, nil)
	if err != nil {
		t.Fatalf("InsertSalesChannel foreign: %v", err)
	}
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM sales_channels WHERE id = $1`, foreign.ID)
		_, _ = pool.Exec(ctx, `DELETE FROM organizations WHERE id = $1`, other.ID)
	}()

	h := newBundle525Handler(t, pool)
	for name, ids := range map[string][]string{
		"foreign":   {foreign.ID.String()},
		"unknown":   {uuid.NewString()},
		"malformed": {"not-a-uuid"},
	} {
		body := f.payload()
		body.Publish = true
		body.ChannelIDs = ids
		rec, _ := f.call(h, body)
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "import.invalid_channel") {
			t.Fatalf("%s: status = %d body=%s; want 422 import.invalid_channel", name, rec.Code, rec.Body.String())
		}
	}
	// Nothing was written: the check runs before the transaction.
	var events int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM events WHERE org_id = $1`, f.orgID).Scan(&events); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if events != 0 {
		t.Fatalf("a refused channel list must not create the event; events = %d", events)
	}
}
