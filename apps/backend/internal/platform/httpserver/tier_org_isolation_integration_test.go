//go:build integration

package httpserver

// A category (ticket tier) belongs to a session, a session to an event, an
// event to ONE organization. The tier routes carry {org_id} in the path and
// check only the caller's membership of THAT organization, so every route that
// then acts on {session_id}/{id} must also prove the session is the path
// organization's — otherwise a member of organization B (or a key bound to it,
// holding tier.update) reaches organization A's categories by their UUIDs.
// EC-15 hands tier.update to the manager and puts these routes behind a Telegram
// screen, which made the hole worth closing. Run against a migrated database.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/apikeys"
)

func TestTierRoutes_StayInsideTheOrganization(t *testing.T) {
	srv, _ := productionIntegrationServer(t)
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	pool := srv.pgxPool
	ctx := context.Background()
	q := gen.New(pool)
	suffix := uuid.NewString()[:8]

	mkOrg := func(label string) uuid.UUID {
		org, err := q.InsertOrganization(ctx, "TierIso "+label+" "+suffix, "tieriso-"+strings.ToLower(label)+"-"+suffix, "CZ", "en", 1200)
		if err != nil {
			t.Fatalf("InsertOrganization: %v", err)
		}
		return org.ID
	}
	orgA, orgB := mkOrg("A"), mkOrg("B")
	user, err := q.InsertUser(ctx, "tieriso-"+suffix+"@example.test", "x", "en")
	if err != nil {
		t.Fatalf("InsertUser: %v", err)
	}
	key := func(org uuid.UUID) string {
		_, raw, err := apikeys.Issue(ctx, apikeys.NewStoreFromQueries(q), apikeys.IssueInput{
			OrgID: org, Name: "tieriso-" + uuid.NewString()[:6], Scopes: []string{"tier.read", "tier.update", "tier.create", "tier.delete"}, CreatedBy: user.ID,
		})
		if err != nil {
			t.Fatalf("apikeys.Issue: %v", err)
		}
		return raw
	}
	keyB := key(orgB)

	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("fixture %s: %v", sql, err)
		}
	}
	venue, event, session, tier := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	exec(`INSERT INTO venues (id, org_id, name, timezone) VALUES ($1, $2, 'TierIso Hall', 'Europe/Prague')`, venue, orgA)
	exec(`INSERT INTO events (id, org_id, name, status, visibility) VALUES ($1, $2, 'TierIso Show', 'published', 'public')`, event, orgA)
	exec(`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, status, currency, currency_source)
	      VALUES ($1, $2, $3, $4, $4::timestamptz + interval '2 hours', 50, 'scheduled', 'EUR', 'override')`,
		session, event, venue, time.Now().UTC().Add(48*time.Hour).Truncate(time.Hour))
	exec(`INSERT INTO ticket_tiers (id, session_id, name, pricing_mode, price_amount, currency, capacity, unit_seq, is_open)
	      VALUES ($1, $2, 'Standing', 'fixed', 2500, 'EUR', 50, 1, true)`, tier, session)
	t.Cleanup(func() {
		c := context.Background()
		for _, st := range []struct {
			sql string
			arg uuid.UUID
		}{
			{`DELETE FROM ticket_tier_prices WHERE tier_id = $1`, tier},
			{`DELETE FROM session_seats WHERE session_id = $1`, session},
			{`DELETE FROM inventory_ledger WHERE session_id = $1`, session},
			{`DELETE FROM ticket_tiers WHERE id = $1`, tier},
			{`DELETE FROM sessions WHERE id = $1`, session},
			{`DELETE FROM events WHERE id = $1`, event},
			{`DELETE FROM venues WHERE id = $1`, venue},
		} {
			if _, err := pool.Exec(c, st.sql, st.arg); err != nil {
				t.Logf("tier isolation cleanup %s: %v", st.sql, err)
			}
		}
		for _, org := range []uuid.UUID{orgA, orgB} {
			_, _ = pool.Exec(c, `DELETE FROM api_keys WHERE org_id = $1`, org)
			_, _ = pool.Exec(c, `DELETE FROM organizations WHERE id = $1`, org)
		}
		_, _ = pool.Exec(c, `DELETE FROM users WHERE id = $1`, user.ID)
	})

	// Organization B's key asks for organization A's session through B's own
	// path: every route must answer as if the session did not exist.
	base := ts.URL + "/v1/organizations/" + orgB.String() + "/events/" + event.String() + "/sessions/" + session.String() + "/tiers"
	do := func(method, url, body string) int {
		resp := integDoRequest(t, ts.Client(), method, url, keyB, body)
		_ = integReadBody(t, resp)
		return resp.StatusCode
	}
	for _, c := range []struct{ name, method, url, body string }{
		{"list", http.MethodGet, base, ""},
		{"get", http.MethodGet, base + "/" + tier.String(), ""},
		{"patch price", http.MethodPatch, base + "/" + tier.String(), `{"price_amount": 1}`},
		{"patch close", http.MethodPatch, base + "/" + tier.String(), `{"is_open": false}`},
		{"patch quantity", http.MethodPatch, base + "/" + tier.String(), `{"capacity": 5}`},
		{"price schedule get", http.MethodGet, base + "/" + tier.String() + "/price-schedule", ""},
		{"price schedule put", http.MethodPut, base + "/" + tier.String() + "/price-schedule", `{"windows":[]}`},
		{"create", http.MethodPost, base, `{"name":"Hijack","pricing_mode":"fixed","price_amount":100}`},
		{"delete", http.MethodDelete, base + "/" + tier.String(), ""},
		{"bulk pricing", http.MethodPost, ts.URL + "/v1/organizations/" + orgB.String() + "/events/" + event.String() + "/sessions/pricing-bulk",
			`{"session_ids":["` + session.String() + `"],"prices":[{"tier_name":"Standing","price_amount":1}]}`},
	} {
		if st := do(c.method, c.url, c.body); st != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404 for a session of another organization", c.name, st)
		}
	}
	var price int64
	var open bool
	var capacity *int32
	if err := pool.QueryRow(ctx, `SELECT price_amount, is_open, capacity FROM ticket_tiers WHERE id = $1 AND deleted_at IS NULL`, tier).Scan(&price, &open, &capacity); err != nil {
		t.Fatalf("the category must still exist: %v", err)
	}
	if price != 2500 || !open || capacity == nil || *capacity != 50 {
		t.Errorf("a foreign request changed the category: price=%d open=%v capacity=%v", price, open, capacity)
	}
}
