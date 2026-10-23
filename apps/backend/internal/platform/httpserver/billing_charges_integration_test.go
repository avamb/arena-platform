//go:build integration

package httpserver

// billing_charges_integration_test.go - task B2-6: the organizer's read-only
// view of their billing charges.
//
//	GET /v1/organizations/{org_id}/billing/charges
//	GET /v1/organizations/{org_id}/billing/events/{event_id}/summary
//
// What it proves, through the real router and real JWTs:
//   - the owner (org_admin, billing.read) reads the charges of their own
//     organization, with sums per currency over the whole filter and a cursor
//     that walks every row once;
//   - the manager (organizer) holds no billing permission: 403;
//   - the owner of another organization is refused on the path organization
//     (403) and gets the route's own 404 for a foreign event_id on their OWN
//     path, on both routes, and never sees a foreign charge;
//   - an organization API key with billing.read reads its own organization
//     only.
//
// Run against a migrated database (AGENTS.md CI-Integration recipe).

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/apikeys"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing"
)

type chargesEnv struct {
	ts                   *httptest.Server
	pool                 *pgxpool.Pool
	q                    *gen.Queries
	orgA, orgB           uuid.UUID
	eventA1, eventA2     uuid.UUID
	eventB               uuid.UUID
	ownerA, mgrA, ownerB string
	keyB                 string
	t0                   time.Time
}

func newChargesEnv(t *testing.T) *chargesEnv {
	t.Helper()
	srv, secret := productionIntegrationServer(t)
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	pool := srv.pgxPool
	q := gen.New(pool)
	ctx := context.Background()
	e := &chargesEnv{ts: ts, pool: pool, q: q, t0: time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)}

	mkOrg := func(label string) uuid.UUID {
		suffix := uuid.NewString()[:8]
		org, err := q.InsertOrganization(ctx, "Charges "+label+" "+suffix, "charges-"+strings.ToLower(label)+"-"+suffix, "CZ", "en", 1200)
		if err != nil {
			t.Fatalf("InsertOrganization: %v", err)
		}
		return org.ID
	}
	e.orgA, e.orgB = mkOrg("A"), mkOrg("B")

	mkUser := func(org uuid.UUID, role string) (uuid.UUID, string) {
		u, err := q.InsertUser(ctx, "charges-"+role+"-"+uuid.NewString()+"@example.test", "x", "en")
		if err != nil {
			t.Fatalf("InsertUser: %v", err)
		}
		if _, err := q.InsertMembership(ctx, u.ID, org, role); err != nil {
			t.Fatalf("InsertMembership(%s): %v", role, err)
		}
		tok, _, err := auth.IssueJWT(secret, u.ID, nil, nil, "arena-api", "arena-api", time.Hour)
		if err != nil {
			t.Fatalf("IssueJWT: %v", err)
		}
		return u.ID, tok
	}
	_, e.ownerA = mkUser(e.orgA, "org_admin")
	_, e.mgrA = mkUser(e.orgA, "organizer")
	ownerBID, ownerB := mkUser(e.orgB, "org_admin")
	e.ownerB = ownerB
	_, rawKey, err := apikeys.Issue(ctx, apikeys.NewStoreFromQueries(q), apikeys.IssueInput{
		OrgID: e.orgB, Name: "charges-" + uuid.NewString()[:6], Scopes: []string{"billing.read"}, CreatedBy: ownerBID,
	})
	if err != nil {
		t.Fatalf("apikeys.Issue: %v", err)
	}
	e.keyB = rawKey

	mkEvent := func(org uuid.UUID, name string) uuid.UUID {
		ev, err := q.InsertEvent(ctx, org, name, nil, "published", "public", nil)
		if err != nil {
			t.Fatalf("InsertEvent: %v", err)
		}
		return ev.ID
	}
	e.eventA1, e.eventA2, e.eventB = mkEvent(e.orgA, "Charges A1"), mkEvent(e.orgA, "Charges A2"), mkEvent(e.orgB, "Charges B")

	t.Cleanup(func() {
		c := context.Background()
		for _, org := range []uuid.UUID{e.orgA, e.orgB} {
			_, _ = pool.Exec(c, `DELETE FROM charge_reversals WHERE charge_id IN (SELECT c.id FROM charges c JOIN billing_accounts a ON a.id = c.account_id WHERE a.org_id = $1)`, org)
			_, _ = pool.Exec(c, `DELETE FROM charges WHERE reversal_of IS NOT NULL AND account_id IN (SELECT id FROM billing_accounts WHERE org_id = $1)`, org)
			_, _ = pool.Exec(c, `DELETE FROM charges WHERE account_id IN (SELECT id FROM billing_accounts WHERE org_id = $1)`, org)
			_, _ = pool.Exec(c, `DELETE FROM billable_events WHERE org_id = $1`, org)
			_, _ = pool.Exec(c, `DELETE FROM billing_account_terms WHERE account_id IN (SELECT id FROM billing_accounts WHERE org_id = $1)`, org)
			_, _ = pool.Exec(c, `DELETE FROM billing_accounts WHERE org_id = $1`, org)
			_, _ = pool.Exec(c, `DELETE FROM events WHERE org_id = $1`, org)
			_, _ = pool.Exec(c, `DELETE FROM api_keys WHERE org_id = $1`, org)
			_, _ = pool.Exec(c, `DELETE FROM memberships WHERE org_id = $1`, org)
			_, _ = pool.Exec(c, `DELETE FROM organizations WHERE id = $1`, org)
		}
	})
	return e
}

// charge seeds one billable event and its direct charge; reverse adds the
// paired reversal. It returns the charge id.
func (e *chargesEnv) charge(t *testing.T, org uuid.UUID, acc gen.BillingAccountRow, event uuid.UUID, at time.Time, amount int64, reverse bool) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	cur, unit := "EUR", int64(1500)
	subject := uuid.New()
	id, ok, err := e.q.InsertBillableEvent(ctx, gen.InsertBillableEventParams{
		Kind: "ticket_sold", SubjectID: subject, OccurredAt: at, OrgID: org, AccountID: acc.ID,
		EventID: &event, Currency: &cur, UnitPriceMinor: &unit, Source: "collect",
	})
	if err != nil || !ok {
		t.Fatalf("event: ok=%v err=%v", ok, err)
	}
	var chargeID uuid.UUID
	if err := e.pool.QueryRow(ctx, `INSERT INTO charges (billable_event_id, account_id, currency, amount_minor, rule_type)
	        VALUES ($1, $2, $3, $4, 'fixed_per_ticket') RETURNING id`, id, acc.ID, acc.Currency, amount).Scan(&chargeID); err != nil {
		t.Fatalf("charge: %v", err)
	}
	if reverse {
		if _, err := e.pool.Exec(ctx, `INSERT INTO charges (billable_event_id, account_id, currency, amount_minor, rule_type, reversal_of)
		        VALUES ($1, $2, $3, $4, 'fixed_per_ticket', $5)`, id, acc.ID, acc.Currency, -amount, chargeID); err != nil {
			t.Fatalf("reversal: %v", err)
		}
	}
	return chargeID
}

func (e *chargesEnv) get(t *testing.T, token, path string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, e.ts.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return resp.StatusCode, body
}

func list(body map[string]any, key string) []any {
	v, _ := body[key].([]any)
	return v
}

func TestBillingCharges_OwnerReadsTheirOrganization(t *testing.T) {
	e := newChargesEnv(t)
	ctx := context.Background()
	accA, err := billing.EnsureAccount(ctx, e.q, e.orgA, "CZ")
	if err != nil {
		t.Fatal(err)
	}
	accB, err := billing.EnsureAccount(ctx, e.q, e.orgB, "CZ")
	if err != nil {
		t.Fatal(err)
	}
	// Event A1: three sales, one of them reversed. Event A2: one sale.
	e.charge(t, e.orgA, accA, e.eventA1, e.t0, 59, false)
	e.charge(t, e.orgA, accA, e.eventA1, e.t0.Add(time.Hour), 59, true)
	e.charge(t, e.orgA, accA, e.eventA1, e.t0.Add(2*time.Hour), 59, false)
	e.charge(t, e.orgA, accA, e.eventA2, e.t0.Add(3*time.Hour), 59, false)
	foreign := e.charge(t, e.orgB, accB, e.eventB, e.t0, 59, false)

	base := "/v1/organizations/" + e.orgA.String() + "/billing"
	code, body := e.get(t, e.ownerA, base+"/charges")
	if code != http.StatusOK {
		t.Fatalf("charges = %d %v", code, body)
	}
	items := list(body, "items")
	// 4 direct charges + 1 reversal.
	if len(items) != 5 {
		t.Fatalf("items = %d, want 5", len(items))
	}
	for _, it := range items {
		if it.(map[string]any)["id"] == foreign.String() {
			t.Fatal("a charge of another organization was returned")
		}
	}
	if body["preliminary"] != true {
		t.Errorf("preliminary = %v, want true", body["preliminary"])
	}
	totals := list(body, "totals")
	if len(totals) != 1 {
		t.Fatalf("totals = %v, want one currency", totals)
	}
	tot := totals[0].(map[string]any)
	if tot["currency"] != accA.Currency || tot["charged_minor"].(float64) != 236 || tot["reversed_minor"].(float64) != -59 ||
		tot["net_minor"].(float64) != 177 || tot["count"].(float64) != 4 {
		t.Errorf("totals = %v, want charged 236, reversed -59, net 177, 4 charges", tot)
	}

	// The cursor walks every row exactly once.
	seen := map[string]int{}
	path := base + "/charges?limit=2"
	for guard := 0; guard < 10; guard++ {
		code, page := e.get(t, e.ownerA, path)
		if code != http.StatusOK {
			t.Fatalf("page = %d %v", code, page)
		}
		for _, it := range list(page, "items") {
			seen[it.(map[string]any)["id"].(string)]++
		}
		next, _ := page["next_cursor"].(string)
		if next == "" {
			break
		}
		path = base + "/charges?limit=2&cursor=" + url.QueryEscape(next)
	}
	if len(seen) != 5 {
		t.Errorf("walk saw %d charges, want 5", len(seen))
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("charge %s seen %d times", id, n)
		}
	}

	// Filters: one event, and a time window that holds only the first sale.
	code, body = e.get(t, e.ownerA, base+"/charges?event_id="+e.eventA2.String())
	if code != http.StatusOK || len(list(body, "items")) != 1 {
		t.Errorf("event filter = %d %v, want the one sale of A2", code, body)
	}
	from, to := e.t0.Format(time.RFC3339), e.t0.Add(30*time.Minute).Format(time.RFC3339)
	code, body = e.get(t, e.ownerA, base+"/charges?from="+url.QueryEscape(from)+"&to="+url.QueryEscape(to))
	if code != http.StatusOK || len(list(body, "items")) != 1 {
		t.Errorf("window = %d %v, want the first sale only", code, body)
	}

	// Event summary.
	code, sum := e.get(t, e.ownerA, base+"/events/"+e.eventA1.String()+"/summary")
	if code != http.StatusOK {
		t.Fatalf("summary = %d %v", code, sum)
	}
	if len(list(sum, "totals")) != 1 || list(sum, "totals")[0].(map[string]any)["net_minor"].(float64) != 118 {
		t.Errorf("summary totals = %v, want net 118 (3 sales, one reversed)", sum["totals"])
	}
	if rules := list(sum, "by_rule"); len(rules) != 1 || rules[0].(map[string]any)["rule_type"] != "fixed_per_ticket" {
		t.Errorf("by_rule = %v", sum["by_rule"])
	}
	if kinds, _ := sum["events_by_kind"].(map[string]any); kinds["ticket_sold"].(float64) != 3 {
		t.Errorf("events_by_kind = %v, want 3 ticket_sold", sum["events_by_kind"])
	}
}

func TestBillingCharges_RightsAndOrganizationIsolation(t *testing.T) {
	e := newChargesEnv(t)
	ctx := context.Background()
	accA, err := billing.EnsureAccount(ctx, e.q, e.orgA, "CZ")
	if err != nil {
		t.Fatal(err)
	}
	e.charge(t, e.orgA, accA, e.eventA1, e.t0, 59, false)

	pathA := "/v1/organizations/" + e.orgA.String() + "/billing"
	pathB := "/v1/organizations/" + e.orgB.String() + "/billing"
	routes := func(path string, event uuid.UUID) []string {
		return []string{path + "/charges", path + "/events/" + event.String() + "/summary"}
	}

	// The manager holds no billing permission.
	for _, r := range routes(pathA, e.eventA1) {
		if code, _ := e.get(t, e.mgrA, r); code != http.StatusForbidden {
			t.Errorf("manager GET %s = %d, want 403", r, code)
		}
	}
	// Organization B's owner and key on organization A's path: refused.
	for _, tok := range []string{e.ownerB, e.keyB} {
		for _, r := range routes(pathA, e.eventA1) {
			if code, _ := e.get(t, tok, r); code != http.StatusForbidden && code != http.StatusNotFound {
				t.Errorf("foreign caller GET %s = %d, want 403 or 404", r, code)
			}
		}
	}
	// On their OWN path, a foreign event is 404 on both routes, and the list
	// filtered by a foreign event is 404 too.
	for _, tok := range []string{e.ownerB, e.keyB} {
		if code, _ := e.get(t, tok, pathB+"/events/"+e.eventA1.String()+"/summary"); code != http.StatusNotFound {
			t.Errorf("foreign event summary = %d, want 404", code)
		}
		if code, _ := e.get(t, tok, pathB+"/charges?event_id="+e.eventA1.String()); code != http.StatusNotFound {
			t.Errorf("foreign event filter = %d, want 404", code)
		}
		// Their own organization has no charges: an empty page, not an error.
		code, body := e.get(t, tok, pathB+"/charges")
		if code != http.StatusOK || len(list(body, "items")) != 0 {
			t.Errorf("own empty list = %d %v", code, body)
		}
	}
	// An unknown event is 404 as well; malformed input is 400.
	if code, _ := e.get(t, e.ownerA, pathA+"/events/"+uuid.NewString()+"/summary"); code != http.StatusNotFound {
		t.Errorf("unknown event = %d, want 404", code)
	}
	for _, bad := range []string{"?limit=0", "?limit=999", "?from=yesterday", "?cursor=zzz", "?event_id=x", "?from=2026-10-02&to=2026-10-01"} {
		if code, _ := e.get(t, e.ownerA, pathA+"/charges"+bad); code != http.StatusBadRequest {
			t.Errorf("GET charges%s = %d, want 400", bad, code)
		}
	}
}
