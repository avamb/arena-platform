//go:build integration

// event_public_url_integration_test.go — the event card's public_url (plan
// stage 0, task 0-3): the buyers' page address built by publicurl.Page from
// PUBLIC_TICKETS_BASE_URL, the promoter's (else the organization's) slug and
// the event slug. Real router, organization API keys, live PostgreSQL.
//
// Run against a FRESH migrated database (AGENTS.md CI-Integration recipe):
//
//	DATABASE_URL=... go test -tags integration -p 1 \
//	    ./apps/backend/internal/platform/httpserver/ -run TestEventPublicURL
package httpserver

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

const eventPublicURLBase = "https://tickets.public-url.test"

func newEventPublicURLFixture(t *testing.T, base string) *prom0113Fixture {
	t.Helper()
	srv, _ := productionIntegrationServerCfg(t, func(c *config.Config) {
		c.PublicTicketsBaseURL = base
	})
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	q := gen.New(srv.pgxPool)
	user, err := q.InsertUser(context.Background(), "puburl-"+uuid.NewString()+"@example.test", "x", "en")
	if err != nil {
		t.Fatalf("InsertUser: %v", err)
	}
	return &prom0113Fixture{ts: ts, client: ts.Client(), q: q, userID: user.ID}
}

// publicURLOf reads the event's public_url from the card and from the
// organization's list, and fails when the two disagree.
func publicURLOf(t *testing.T, f *prom0113Fixture, orgID, eventID uuid.UUID, key string) string {
	t.Helper()
	st, card := f.do(t, http.MethodGet, "/v1/events/"+eventID.String(), key, "")
	if st != http.StatusOK {
		t.Fatalf("GET event card: status %d body %v", st, card)
	}
	fromCard, ok := prom0113Field(t, card, "event", "public_url").(string)
	if !ok {
		t.Fatalf("card public_url is not a string: %v", card["event"])
	}
	st, list := f.do(t, http.MethodGet, "/v1/organizations/"+orgID.String()+"/events", key, "")
	if st != http.StatusOK {
		t.Fatalf("list events: status %d body %v", st, list)
	}
	for _, raw := range list["events"].([]any) {
		e := raw.(map[string]any)
		if e["id"] != eventID.String() {
			continue
		}
		fromList, _ := e["public_url"].(string)
		if fromList != fromCard {
			t.Errorf("list public_url %q differs from card %q", fromList, fromCard)
		}
		return fromCard
	}
	t.Fatalf("event %s not in the organization's list", eventID)
	return ""
}

func TestEventPublicURL_PromoterOrganizationAndNoSlug(t *testing.T) {
	f := newEventPublicURLFixture(t, eventPublicURLBase+"/")
	orgID := f.org(t, "P")
	org, err := f.q.GetOrganizationByID(context.Background(), orgID)
	if err != nil {
		t.Fatalf("GetOrganizationByID: %v", err)
	}
	key := f.key(t, orgID, "event.read", "event.update", "promoter.read", "promoter.manage")
	base := "/v1/organizations/" + orgID.String()

	ev, err := f.q.InsertEvent(context.Background(), orgID, "Public URL Event", nil, "draft", "public", nil, "text")
	if err != nil {
		t.Fatalf("InsertEvent: %v", err)
	}

	// No event slug yet: nothing to point at.
	if got := publicURLOf(t, f, orgID, ev.ID, key); got != "" {
		t.Errorf("event without slug: public_url = %q, want empty", got)
	}

	if _, err := f.q.SetEventSlugIfEmpty(context.Background(), ev.ID, orgID, "silver-hoof"); err != nil {
		t.Fatalf("SetEventSlugIfEmpty: %v", err)
	}

	// No promoter: the organization's slug. A draft event still has the
	// address (it is an address, not a sign of availability), and the base's
	// trailing slash is not doubled.
	want := eventPublicURLBase + "/" + org.Slug + "/silver-hoof"
	if got := publicURLOf(t, f, orgID, ev.ID, key); got != want {
		t.Errorf("organization event: public_url = %q, want %q", got, want)
	}

	// With a promoter that has a page: the promoter's slug.
	st, body := f.do(t, http.MethodPost, base+"/promoters", key, `{"name":"Public Url Kolibel"}`)
	if st != http.StatusCreated {
		t.Fatalf("create promoter: status %d body %v", st, body)
	}
	promID := prom0113Field(t, body, "promoter", "id").(string)
	promSlug, _ := prom0113Field(t, body, "promoter", "slug").(string)
	if promSlug == "" {
		t.Fatalf("promoter has no slug: %v", body["promoter"])
	}
	st, body = f.do(t, http.MethodPut, base+"/events/"+ev.ID.String()+"/promoter", key, fmt.Sprintf(`{"promoter_id":%q}`, promID))
	if st != http.StatusOK {
		t.Fatalf("link promoter: status %d body %v", st, body)
	}
	want = eventPublicURLBase + "/" + promSlug + "/silver-hoof"
	if got := publicURLOf(t, f, orgID, ev.ID, key); got != want {
		t.Errorf("promoter event: public_url = %q, want %q", got, want)
	}

	// An archived promoter has no page any more: back to the organization.
	if st, body := f.do(t, http.MethodPatch, base+"/promoters/"+promID, key, `{"archived":true}`); st != http.StatusOK {
		t.Fatalf("archive promoter: status %d body %v", st, body)
	}
	want = eventPublicURLBase + "/" + org.Slug + "/silver-hoof"
	if got := publicURLOf(t, f, orgID, ev.ID, key); got != want {
		t.Errorf("archived promoter: public_url = %q, want %q", got, want)
	}
}

func TestEventPublicURL_EmptyWithoutTicketsBase(t *testing.T) {
	f := newEventPublicURLFixture(t, "")
	orgID := f.org(t, "N")
	key := f.key(t, orgID, "event.read")
	ev, err := f.q.InsertEvent(context.Background(), orgID, "Public URL No Base", nil, "draft", "public", nil, "text")
	if err != nil {
		t.Fatalf("InsertEvent: %v", err)
	}
	if _, err := f.q.SetEventSlugIfEmpty(context.Background(), ev.ID, orgID, "no-base"); err != nil {
		t.Fatalf("SetEventSlugIfEmpty: %v", err)
	}
	if got := publicURLOf(t, f, orgID, ev.ID, key); got != "" {
		t.Errorf("no tickets base: public_url = %q, want empty", got)
	}
}
