//go:build integration

// orgread_cross_org_integration_test.go — the routes that learn the
// organization from the event they load (package orgread): GET
// /v1/events/{id}, the cross-org GET /v1/events, /v1/events/{event_id}/
// publications and /v1/events/{event_id}/report. Until 2026-09-28 they
// checked only a scope that every organization API key carries, so one
// organizer could read — and publish or unpublish — another's event by UUID.
//
// Every request goes through the REAL router with organization API keys
// (prom0113Fixture, promoters_integration_test.go).
package httpserver

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"
)

func TestOrgRead_EventRoutesStayInsideTheOrganization(t *testing.T) {
	f := newProm0113Fixture(t)
	ctx := context.Background()
	orgA, orgB := f.org(t, "A"), f.org(t, "B")
	keyA := f.key(t, orgA, "event.read", "publication.read", "publication.create", "publication.delete", "report.read", "report.generate")

	evA, err := f.q.InsertEvent(ctx, orgA, "OrgRead A Event", nil, "draft", "public", nil)
	if err != nil {
		t.Fatalf("InsertEvent A: %v", err)
	}
	evB, err := f.q.InsertEvent(ctx, orgB, "OrgRead B Event", nil, "draft", "public", nil)
	if err != nil {
		t.Fatalf("InsertEvent B: %v", err)
	}
	feed := func(org uuid.UUID) uuid.UUID {
		t.Helper()
		ch, err := f.q.InsertSalesChannel(ctx, org, "OrgRead channel "+uuid.NewString()[:6], "merchant_of_record", "stripe", nil, "0", nil, nil)
		if err != nil {
			t.Fatalf("InsertSalesChannel: %v", err)
		}
		tok, err := f.q.InsertFeedToken(ctx, "orgread-"+uuid.NewString(), ch.ID, "orgread")
		if err != nil {
			t.Fatalf("InsertFeedToken: %v", err)
		}
		return tok.ID
	}
	feedA, feedB := feed(orgA), feed(orgB)

	expect := func(method, path, body string, want int, wantCode string) map[string]any {
		t.Helper()
		st, out := f.do(t, method, path, keyA, body)
		if st != want {
			t.Fatalf("%s %s: status %d body %v, want %d", method, path, st, out, want)
		}
		if wantCode != "" && prom0113Code(out) != wantCode {
			t.Fatalf("%s %s: code %q, want %q", method, path, prom0113Code(out), wantCode)
		}
		return out
	}

	// The event itself.
	expect(http.MethodGet, "/v1/events/"+evA.ID.String(), "", http.StatusOK, "")
	expect(http.MethodGet, "/v1/events/"+evB.ID.String(), "", http.StatusNotFound, "event.not_found")

	// The cross-org list shows only the key's own organization.
	list := expect(http.MethodGet, "/v1/events?visibility=all", "", http.StatusOK, "")
	seenA, seenB := false, false
	for _, raw := range list["events"].([]any) {
		e := raw.(map[string]any)
		if e["org_id"] != orgA.String() {
			t.Fatalf("list returned an event of organization %v", e["org_id"])
		}
		seenA = seenA || e["id"] == evA.ID.String()
		seenB = seenB || e["id"] == evB.ID.String()
	}
	if !seenA || seenB {
		t.Fatalf("list: own event seen %v, foreign event seen %v", seenA, seenB)
	}

	// Publications: another organization's event, and another organization's feed.
	expect(http.MethodGet, "/v1/events/"+evB.ID.String()+"/publications", "", http.StatusNotFound, "publication.event_not_found")
	expect(http.MethodDelete, "/v1/events/"+evB.ID.String()+"/publications/"+feedB.String(), "", http.StatusNotFound, "publication.event_not_found")
	expect(http.MethodPost, "/v1/events/"+evB.ID.String()+"/publications",
		fmt.Sprintf(`{"feed_token_id":%q}`, feedA), http.StatusNotFound, "publication.event_not_found")
	expect(http.MethodPost, "/v1/events/"+evA.ID.String()+"/publications",
		fmt.Sprintf(`{"feed_token_id":%q}`, feedB), http.StatusNotFound, "publication.feed_token_not_found")
	expect(http.MethodPost, "/v1/events/"+evA.ID.String()+"/publications",
		fmt.Sprintf(`{"feed_token_id":%q}`, feedA), http.StatusOK, "")
	pubs := expect(http.MethodGet, "/v1/events/"+evA.ID.String()+"/publications", "", http.StatusOK, "")
	if n := len(pubs["publications"].([]any)); n != 1 {
		t.Fatalf("own publications = %d, want 1", n)
	}

	// Reports: another organization's event is not found; a report of the own
	// event is filed under the event's organization, not under the event id.
	expect(http.MethodGet, "/v1/events/"+evB.ID.String()+"/report", "", http.StatusNotFound, "report.not_found")
	expect(http.MethodPost, "/v1/events/"+evB.ID.String()+"/report", "", http.StatusNotFound, "event.not_found")
	expect(http.MethodPost, "/v1/events/"+evA.ID.String()+"/report", "", http.StatusAccepted, "")
	var reportOrg uuid.UUID
	if err := f.q.DB().QueryRow(ctx, `SELECT org_id FROM event_reports WHERE event_id = $1 ORDER BY created_at DESC LIMIT 1`, evA.ID).Scan(&reportOrg); err != nil {
		t.Fatalf("read report: %v", err)
	}
	if reportOrg != orgA {
		t.Fatalf("report org_id = %s, want the event's organization %s", reportOrg, orgA)
	}
}
