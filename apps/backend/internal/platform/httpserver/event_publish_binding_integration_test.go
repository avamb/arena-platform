//go:build integration

// event_publish_binding_integration_test.go — UXH-22: publishing an event
// through POST .../events/{id}/status binds it to a storefront channel when it
// has no publication, so its hosted page opens. The web event center saves
// with publish:false and publishes here; before the fix the event was
// "published" with no event_publications row and answered 404 on its page.
//
// Goes through the REAL router. Run against a migrated database (the tests
// clean up their own rows).
package httpserver

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"
)

// bareEvent is event() without the publication: a draft event with one dated
// session and one priced category, exactly what the web wizard leaves behind.
func (f *evStatusFixture) bareEvent() (eventID uuid.UUID, slug string) {
	f.t.Helper()
	eventID, _ = f.event("draft")
	f.exec(`DELETE FROM event_publications WHERE event_id = $1`, eventID)
	if err := f.pool.QueryRow(context.Background(), `SELECT slug FROM events WHERE id = $1`, eventID).Scan(&slug); err != nil {
		f.t.Fatal(err)
	}
	return eventID, slug
}

// dropFixtureChannel takes the fixture's own channel out of play (soft delete),
// so a test controls exactly which channels the organization has.
func (f *evStatusFixture) dropFixtureChannel() {
	f.t.Helper()
	f.exec(`UPDATE sales_channels SET deleted_at = now() WHERE id = $1`, f.channel)
}

// channel inserts an organization channel created `ageHours` hours ago.
func (f *evStatusFixture) newChannel(name, settings string, ageHours int) uuid.UUID {
	f.t.Helper()
	id := uuid.New()
	f.exec(`INSERT INTO sales_channels (id, org_id, name, settings, created_at) VALUES ($1, $2, $3, $4::jsonb, now() - make_interval(hours => $5))`,
		id, f.orgID, name+" "+f.suffix, settings, ageHours)
	return id
}

func (f *evStatusFixture) channelSettings(id uuid.UUID) string {
	f.t.Helper()
	var s string
	if err := f.pool.QueryRow(context.Background(), `SELECT settings::text FROM sales_channels WHERE id = $1`, id).Scan(&s); err != nil {
		f.t.Fatal(err)
	}
	return s
}

func (f *evStatusFixture) hostedFlag(id uuid.UUID) string {
	f.t.Helper()
	var v *string
	if err := f.pool.QueryRow(context.Background(), `SELECT settings #>> '{hosted_page,enabled}' FROM sales_channels WHERE id = $1`, id).Scan(&v); err != nil {
		f.t.Fatal(err)
	}
	if v == nil {
		return ""
	}
	return *v
}

// publicationChannels lists the channels the event is published into.
func (f *evStatusFixture) publicationChannels(eventID uuid.UUID) []uuid.UUID {
	f.t.Helper()
	rows, err := f.pool.Query(context.Background(),
		`SELECT ft.sales_channel_id FROM event_publications ep JOIN agent_feed_tokens ft ON ft.id = ep.feed_token_id
		  WHERE ep.event_id = $1 ORDER BY ep.published_at, ep.id`, eventID)
	if err != nil {
		f.t.Fatal(err)
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			f.t.Fatal(err)
		}
		out = append(out, id)
	}
	return out
}

func (f *evStatusFixture) pageStatus(eventSlug string) int {
	f.t.Helper()
	resp := integDoRequest(f.t, f.ts.Client(), http.MethodGet,
		f.ts.URL+"/v1/public/pages/ev-status-"+f.suffix+"/"+eventSlug, "", "")
	_ = integReadBody(f.t, resp)
	return resp.StatusCode
}

func (f *evStatusFixture) publish(eventID uuid.UUID) {
	f.t.Helper()
	if st, body := f.do(f.key, http.MethodPost, f.base(eventID)+"/status", `{"status":"published"}`); st != http.StatusOK {
		f.t.Fatalf("publish: %d %v", st, body)
	}
}

// The web flow end to end: the wizard's event-bundle import with
// publish:false and no channelIds, then the Publish button.
func TestEventPublishBinding_WebFlowOpensTheHostedPage(t *testing.T) {
	f := newEvStatusFixture(t)
	// The import creates rows the shared fixture cleanup does not know about;
	// this runs first (cleanups are LIFO).
	t.Cleanup(func() {
		for _, sql := range []string{
			`DELETE FROM session_seats WHERE session_id IN (SELECT s.id FROM sessions s JOIN events e ON e.id = s.event_id WHERE e.org_id = $1)`,
			`DELETE FROM inventory_ledger WHERE session_id IN (SELECT s.id FROM sessions s JOIN events e ON e.id = s.event_id WHERE e.org_id = $1)`,
		} {
			if _, err := f.pool.Exec(context.Background(), sql, f.orgID); err != nil {
				t.Logf("cleanup: %s: %v", sql, err)
			}
		}
	})
	ref := "uxh22-" + uuid.NewString()[:8]
	bundle := fmt.Sprintf(`{
		"source": "arena",
		"externalRef": %q,
		"publish": false,
		"action": {"actionName": "UXH22 Web %s"},
		"actionEvent": {"day": "01.06.2037", "time": "19:00", "currency": "EUR"},
		"venue": {"venueName": "UXH22 Venue %s", "cityName": "Tallinn", "countryName": "EE", "timezone": "Europe/Tallinn"},
		"categoryList": [{"categoryPriceName": "Standard", "price": 15.00, "availability": 10}]
	}`, ref, ref, ref)
	st, body := f.do(f.key, http.MethodPost, "/v1/organizations/"+f.orgID.String()+"/imports/event-bundle", bundle)
	if st != http.StatusOK {
		t.Fatalf("import: %d %v", st, body)
	}
	idStr, _ := body["event_id"].(string)
	eventID, err := uuid.Parse(idStr)
	if err != nil {
		t.Fatalf("import answer has no event_id: %v", body)
	}
	var slug string
	if err := f.pool.QueryRow(context.Background(), `SELECT slug FROM events WHERE id = $1`, eventID).Scan(&slug); err != nil || slug == "" {
		t.Fatalf("imported event has no slug: %q %v", slug, err)
	}
	if n := f.publications(eventID); n != 0 {
		t.Fatalf("a publish:false import must bind nothing, have %d", n)
	}

	f.publish(eventID)

	if n := f.publications(eventID); n != 1 {
		t.Fatalf("publications after the Publish button = %d, want 1", n)
	}
	if got := f.hostedFlag(f.channel); got != "true" {
		t.Errorf("hosted_page.enabled after publish = %q, want true", got)
	}
	if got := f.pageStatus(slug); got != http.StatusOK {
		t.Errorf("hosted page status = %d, want 200", got)
	}
}

// A WordPress site's channel (gateway credential, either shape) is never
// bound and never touched: the publish itself still works.
func TestEventPublishBinding_GatewayChannelsAreNeverBound(t *testing.T) {
	for name, settings := range map[string]string{
		"nested": `{"gateway":{"token_hash":"$2a$10$notarealhash"}}`,
		"legacy": `{"gateway_token_hash":"$2a$10$notarealhash"}`,
	} {
		t.Run(name, func(t *testing.T) {
			f := newEvStatusFixture(t)
			f.exec(`UPDATE sales_channels SET settings = $2::jsonb WHERE id = $1`, f.channel, settings)
			before := f.channelSettings(f.channel)
			eventID, slug := f.bareEvent()

			f.publish(eventID)

			if got := f.status(eventID); got != "published" {
				t.Fatalf("status = %q, want published", got)
			}
			if n := f.publications(eventID); n != 0 {
				t.Errorf("an event must never be pushed into a site's channel, have %d publications", n)
			}
			if after := f.channelSettings(f.channel); after != before {
				t.Errorf("the site's channel settings changed: %s -> %s", before, after)
			}
			if got := f.pageStatus(slug); got != http.StatusNotFound {
				t.Errorf("hosted page status = %d, want 404 (no storefront channel)", got)
			}
		})
	}
}

// An organization with no channel at all publishes exactly as before.
func TestEventPublishBinding_NoChannelPublishesAsBefore(t *testing.T) {
	f := newEvStatusFixture(t)
	f.dropFixtureChannel()
	eventID, _ := f.bareEvent()
	f.publish(eventID)
	if got := f.status(eventID); got != "published" {
		t.Fatalf("status = %q, want published", got)
	}
	if n := f.publications(eventID); n != 0 {
		t.Errorf("publications = %d, want 0", n)
	}
}

// Several eligible channels: the ones with the hosted page on win; a site's
// channel is skipped even when it is the oldest.
func TestEventPublishBinding_SeveralChannelsPrefersTheEnabledOne(t *testing.T) {
	f := newEvStatusFixture(t)
	f.dropFixtureChannel()
	site := f.newChannel("Site", `{"gateway":{"token_hash":"$2a$10$notarealhash"}}`, 30)
	oldOff := f.newChannel("Old off", `{}`, 20)
	hosted := f.newChannel("Hosted", `{"hosted_page":{"enabled":true}}`, 10)
	eventID, slug := f.bareEvent()

	f.publish(eventID)

	got := f.publicationChannels(eventID)
	if len(got) != 1 || got[0] != hosted {
		t.Fatalf("bound channels = %v, want only the hosted one %s", got, hosted)
	}
	if f.hostedFlag(oldOff) != "" || f.hostedFlag(site) != "" {
		t.Errorf("channels that were not chosen must stay untouched")
	}
	if st := f.pageStatus(slug); st != http.StatusOK {
		t.Errorf("hosted page status = %d, want 200", st)
	}
	var label string
	if err := f.pool.QueryRow(context.Background(),
		`SELECT label FROM agent_feed_tokens WHERE sales_channel_id = $1`, hosted).Scan(&label); err != nil || label != "auto:event-bundle" {
		t.Errorf("a feed token must have been minted for the chosen channel, got %q %v", label, err)
	}
}

// Several eligible channels and none with the page on: the oldest one is
// chosen and its page switched on.
func TestEventPublishBinding_NoneEnabledPicksTheOldest(t *testing.T) {
	f := newEvStatusFixture(t)
	f.dropFixtureChannel()
	oldest := f.newChannel("Oldest", `{}`, 30)
	younger := f.newChannel("Younger", `{}`, 5)
	eventID, slug := f.bareEvent()

	f.publish(eventID)

	got := f.publicationChannels(eventID)
	if len(got) != 1 || got[0] != oldest {
		t.Fatalf("bound channels = %v, want the oldest %s", got, oldest)
	}
	if f.hostedFlag(oldest) != "true" {
		t.Errorf("the chosen channel's page must be switched on")
	}
	if f.hostedFlag(younger) != "" {
		t.Errorf("the other channel must stay untouched")
	}
	if st := f.pageStatus(slug); st != http.StatusOK {
		t.Errorf("hosted page status = %d, want 200", st)
	}
}

// Take off sale and publish again: the existing rows are kept, none is added,
// even when the organization has since gained a better-looking channel.
func TestEventPublishBinding_RepublishKeepsTheExistingRows(t *testing.T) {
	f := newEvStatusFixture(t)
	eventID, slug := f.bareEvent()
	f.publish(eventID)
	first := f.publicationChannels(eventID)
	if len(first) != 1 || first[0] != f.channel {
		t.Fatalf("first publish bound %v, want the fixture channel", first)
	}
	var pubID uuid.UUID
	if err := f.pool.QueryRow(context.Background(), `SELECT id FROM event_publications WHERE event_id = $1`, eventID).Scan(&pubID); err != nil {
		t.Fatal(err)
	}

	if st, body := f.do(f.key, http.MethodPost, f.base(eventID)+"/status", `{"status":"draft"}`); st != http.StatusOK {
		t.Fatalf("take off sale: %d %v", st, body)
	}
	f.newChannel("Newcomer", `{"hosted_page":{"enabled":true}}`, 0)
	f.publish(eventID)

	var again uuid.UUID
	if err := f.pool.QueryRow(context.Background(), `SELECT id FROM event_publications WHERE event_id = $1`, eventID).Scan(&again); err != nil {
		t.Fatalf("expected exactly the original row: %v", err)
	}
	if again != pubID || f.publications(eventID) != 1 {
		t.Errorf("republish changed the publications: id %s -> %s, count %d", pubID, again, f.publications(eventID))
	}
	if st := f.pageStatus(slug); st != http.StatusOK {
		t.Errorf("hosted page status = %d, want 200", st)
	}
}
