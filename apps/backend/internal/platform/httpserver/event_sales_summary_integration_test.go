//go:build integration

// event_sales_summary_integration_test.go — WEB-LIST (spec 40 task 0-1):
// GET /v1/organizations/{org_id}/events/sales-summary?ids=… through the REAL
// router. For every event it must answer exactly the top level of the event
// summary (everything but the per-category table and the per-session list),
// leave an event of another organization and an unknown id out of the
// answer, refuse more than 50 ids, and keep the agent role out.
//
// Run against a FRESH migrated database (AGENTS.md CI-Integration recipe).
package httpserver

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// salesSummaryItems reads the items of a batch answer by event id.
func salesSummaryItems(t *testing.T, body map[string]any) ([]map[string]any, map[string]map[string]any) {
	t.Helper()
	raw, ok := body["items"].([]any)
	if !ok {
		t.Fatalf("items must be an array, got %v", body["items"])
	}
	var list []map[string]any
	byID := map[string]map[string]any{}
	for _, it := range raw {
		m, _ := it.(map[string]any)
		list = append(list, m)
		byID[m["event"].(map[string]any)["id"].(string)] = m
	}
	return list, byID
}

func TestEventSalesSummary_BatchEqualsPerEventSummary(t *testing.T) {
	x := newECSumFixture(t)
	ctx := context.Background()

	// A refunded ticket on the paid order, so the batch carries a refund too.
	var ticketID uuid.UUID
	if err := x.q.DB().QueryRow(ctx, `SELECT id FROM tickets WHERE order_id = $1 ORDER BY ordinal LIMIT 1`, x.paidOrder).Scan(&ticketID); err != nil {
		t.Fatalf("paid order ticket: %v", err)
	}
	if _, err := x.q.DB().Exec(ctx, `INSERT INTO refunds (id, org_id, amount, currency, state, settlement, order_id, ticket_id, succeeded_at)
	      VALUES ($1, $2, 2350, 'EUR', 'succeeded', 'external', $3, $4, now())`, uuid.New(), x.org, x.paidOrder, ticketID); err != nil {
		t.Fatalf("insert refund: %v", err)
	}
	t.Cleanup(func() {
		_, _ = x.q.DB().Exec(context.Background(), `DELETE FROM refunds WHERE org_id = $1`, x.org)
	})

	// An event of ANOTHER organization.
	foreignEvent := uuid.New()
	if _, err := x.q.DB().Exec(ctx, `INSERT INTO events (id, org_id, name, status, visibility) VALUES ($1, $2, 'Foreign', 'published', 'public')`, foreignEvent, x.foreign); err != nil {
		t.Fatalf("insert foreign event: %v", err)
	}
	t.Cleanup(func() {
		_, _ = x.q.DB().Exec(context.Background(), `DELETE FROM events WHERE id = $1`, foreignEvent)
	})
	unknown := uuid.New()

	// A soft-deleted event of THIS organization, and, inside the on-sale
	// event, one cancelled and one soft-deleted session: the cancelled one
	// counts in the event (as in the single summary), the deleted one does not.
	deletedEvent := uuid.New()
	if _, err := x.q.DB().Exec(ctx, `INSERT INTO events (id, org_id, name, status, visibility, deleted_at) VALUES ($1, $2, 'Deleted', 'published', 'public', now())`, deletedEvent, x.org); err != nil {
		t.Fatalf("insert deleted event: %v", err)
	}
	for _, s := range []struct{ status, deleted string }{{"cancelled", "NULL"}, {"scheduled", "now()"}} {
		if _, err := x.q.DB().Exec(ctx, `INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, status, currency, currency_source, deleted_at)
		      SELECT $1, $2, (SELECT id FROM venues WHERE org_id = $3 LIMIT 1), now() + interval '60 days', now() + interval '60 days 2 hours', 5, $4, 'EUR', 'override', `+s.deleted,
			uuid.New(), x.evOnSale, x.org, s.status); err != nil {
			t.Fatalf("insert %s session: %v", s.status, err)
		}
	}

	base := "/v1/organizations/" + x.org.String()
	// The on-sale event carries the paid order, the invitation, the promo and
	// the refund; the empty one has no session; the sold-out one has places
	// sold upstream only; the foreign and the unknown id must be left out.
	requested := []uuid.UUID{x.evOnSale, foreignEvent, x.evEmpty, deletedEvent, unknown, x.evSoldOut, x.evUpcoming, x.evOnSale}
	parts := make([]string, 0, len(requested))
	for _, id := range requested {
		parts = append(parts, id.String())
	}
	st, body := x.get(base+"/events/sales-summary?ids="+strings.Join(parts, ","), x.manager)
	if st != http.StatusOK {
		t.Fatalf("batch: %d %v", st, body)
	}
	list, byID := salesSummaryItems(t, body)

	if _, ok := byID[foreignEvent.String()]; ok {
		t.Errorf("an event of another organization must be absent: %v", list)
	}
	if _, ok := byID[unknown.String()]; ok {
		t.Errorf("an unknown id must be absent: %v", list)
	}
	if _, ok := byID[deletedEvent.String()]; ok {
		t.Errorf("a deleted event must be absent: %v", list)
	}
	if len(list) != 4 {
		t.Fatalf("want 4 entries (a repeated id counts once), got %d: %v", len(list), list)
	}
	wantOrder := []uuid.UUID{x.evOnSale, x.evEmpty, x.evSoldOut, x.evUpcoming}
	for i, id := range wantOrder {
		if got := list[i]["event"].(map[string]any)["id"]; got != id.String() {
			t.Errorf("entry %d: event %v, want %s (request order)", i, got, id)
		}
	}

	for _, id := range wantOrder {
		st, single := x.get(base+"/events/"+id.String()+"/summary", x.manager)
		if st != http.StatusOK {
			t.Fatalf("event summary %s: %d %v", id, st, single)
		}
		delete(single, "tiers")
		delete(single, "sessions")
		if !reflect.DeepEqual(single, byID[id.String()]) {
			t.Errorf("event %s: batch entry differs from the event summary\n batch:  %v\n single: %v", id, byID[id.String()], single)
		}
	}

	// The figures are really there, not two empty documents that agree.
	onSale := byID[x.evOnSale.String()]
	if ecNum(onSale, "tickets", "active") != 3 || ecNum(onSale, "event", "session_count") != 3 {
		t.Errorf("on-sale entry: %v", onSale)
	}
	money, _ := onSale["money"].([]any)
	if len(money) != 1 || money[0].(map[string]any)["paid"] != float64(4700) || money[0].(map[string]any)["refunded"] != float64(2350) || money[0].(map[string]any)["net"] != float64(2350) {
		t.Errorf("on-sale money (paid, refunded, net): %v", onSale["money"])
	}
	if refunds, _ := onSale["refunds"].([]any); len(refunds) != 1 {
		t.Errorf("on-sale refunds: %v", onSale["refunds"])
	}
	if ecNum(byID[x.evSoldOut.String()], "places", "ga", "sold_upstream") != 5 {
		t.Errorf("sold-out entry: %v", byID[x.evSoldOut.String()])
	}
	if ecNum(byID[x.evEmpty.String()], "event", "session_count") != 0 || len(byID[x.evEmpty.String()]["money"].([]any)) != 0 {
		t.Errorf("empty entry: %v", byID[x.evEmpty.String()])
	}
	for _, k := range []string{"tiers", "sessions"} {
		if _, has := onSale[k]; has {
			t.Errorf("the batch entry must not carry %q", k)
		}
	}

	// Another organization's user, asking in his own organization with this
	// organization's event ids, gets an empty list rather than an error.
	st, body = x.get("/v1/organizations/"+x.foreign.String()+"/events/sales-summary?ids="+x.evOnSale.String(), x.outside)
	if st != http.StatusOK {
		t.Fatalf("outside user in his own organization: %d %v", st, body)
	}
	if list, _ := salesSummaryItems(t, body); len(list) != 0 {
		t.Errorf("foreign events must not be readable: %v", list)
	}
}

func TestEventSalesSummary_IDsLimitsAndGates(t *testing.T) {
	x := newECSumFixture(t)
	path := "/v1/organizations/" + x.org.String() + "/events/sales-summary"

	idList := func(n int) string {
		ids := make([]string, n)
		for i := range ids {
			ids[i] = uuid.NewString()
		}
		return strings.Join(ids, ",")
	}

	st, body := x.get(path+"?ids="+idList(50), x.manager)
	if st != http.StatusOK {
		t.Fatalf("50 ids: %d %v", st, body)
	}
	if list, _ := salesSummaryItems(t, body); len(list) != 0 {
		t.Errorf("50 unknown ids must answer an empty list: %v", list)
	}

	st, body = x.get(path+"?ids="+idList(51), x.manager)
	if st != http.StatusBadRequest {
		t.Fatalf("51 ids: %d, want 400", st)
	}
	if code := fmt.Sprint(body["error"].(map[string]any)["code"]); code != "events.too_many_ids" {
		t.Errorf("51 ids: error code %q, want events.too_many_ids", code)
	}

	for name, query := range map[string]string{
		"missing":    "",
		"empty":      "?ids=",
		"not uuid":   "?ids=" + x.evOnSale.String() + ",nope",
		"hole":       "?ids=" + x.evOnSale.String() + ",,",
		"blank":      "?ids=%20",
		"only comma": "?ids=,",
	} {
		if st, _ := x.get(path+query, x.manager); st != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", name, st)
		}
	}

	one := "?ids=" + x.evOnSale.String()
	if st, _ := x.get(path+one, x.agent); st != http.StatusForbidden {
		t.Errorf("agent: %d, want 403", st)
	}
	// A user of another organization asking in THIS organization's path.
	if st, _ := x.get(path+one, x.outside); st != http.StatusForbidden {
		t.Errorf("outsider in another organization's path: %d, want 403", st)
	}
	if st, _ := x.get(path+one, ""); st != http.StatusUnauthorized {
		t.Errorf("no token: %d, want 401", st)
	}
}
