//go:build integration

package httpserver

// org_isolation_billing_integration_test.go - SEC-3: the flat billing invoice
// routes carry no {org_id} in the path, so the organization comes from the
// invoice row. The handler must compare it with the caller's organizations and
// answer the route's own 404 for an invoice of another organization.
//
// Organization B calls each route with organization A's invoice id:
//   - an owner JWT (org_admin membership, EMPTY roles claim). The owner holds
//     billing.read only, so the reads are 404 and the mutations are 403.
//   - a manager JWT (organizer membership). No billing permission: 403 on all.
//   - an organization API key. billing.read can be carried by a key, so the
//     read is 404, while billing.admin is a forbidden scope for keys, so the
//     mutations are 403.
//   - a user with an org_admin membership of B AND the global `admin` role,
//     the only non-superadmin shape that holds billing.admin: 404 on all.
//
// Organization A's invoice row must be exactly what it was after every call.
// The POST /v1/billing/stripe/push-invoice/{id} route is mounted only when a
// Stripe Billing adapter is wired, which this server has not, so it is not
// exercised here: it applies the same guard right after loading the invoice.
//
// Run against a migrated database (AGENTS.md CI-Integration recipe).

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/apikeys"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
)

type billEnv struct {
	ts     *httptest.Server
	pool   *pgxpool.Pool
	q      *gen.Queries
	orgA   uuid.UUID
	orgB   uuid.UUID
	owner  string
	mgr    string
	key    string
	admin  string
	period int
}

type billCaller struct {
	name  string
	token string
	// readStatus / writeStatus are the expected answers for organization A's invoice.
	readStatus, writeStatus int
}

func (e *billEnv) callers() []billCaller {
	return []billCaller{
		{"b-owner-jwt", e.owner, http.StatusNotFound, http.StatusForbidden},
		{"b-manager-jwt", e.mgr, http.StatusForbidden, http.StatusForbidden},
		{"b-api-key", e.key, http.StatusNotFound, http.StatusForbidden},
		{"b-admin-role-jwt", e.admin, http.StatusNotFound, http.StatusNotFound},
	}
}

func newBillEnv(t *testing.T) *billEnv {
	t.Helper()
	srv, secret := productionIntegrationServer(t)
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	pool := srv.pgxPool
	q := gen.New(pool)
	ctx := context.Background()
	e := &billEnv{ts: ts, pool: pool, q: q}

	mkOrg := func(label string) uuid.UUID {
		suffix := uuid.NewString()[:8]
		org, err := q.InsertOrganization(ctx, "Bill "+label+" "+suffix, "bill-"+strings.ToLower(label)+"-"+suffix, "CZ", "en", 1200)
		if err != nil {
			t.Fatalf("InsertOrganization: %v", err)
		}
		return org.ID
	}
	e.orgA, e.orgB = mkOrg("A"), mkOrg("B")

	mkUser := func(role string) (uuid.UUID, string) {
		u, err := q.InsertUser(ctx, "bill-"+role+"-"+uuid.NewString()+"@example.test", "x", "en")
		if err != nil {
			t.Fatalf("InsertUser: %v", err)
		}
		if _, err := q.InsertMembership(ctx, u.ID, e.orgB, role); err != nil {
			t.Fatalf("InsertMembership(%s): %v", role, err)
		}
		tok, _, err := auth.IssueJWT(secret, u.ID, nil, nil, "arena-api", "arena-api", time.Hour)
		if err != nil {
			t.Fatalf("IssueJWT: %v", err)
		}
		return u.ID, tok
	}
	ownerID, ownerTok := mkUser("org_admin")
	_, mgrTok := mkUser("organizer")
	adminID, adminTok := mkUser("org_admin")
	e.owner, e.mgr, e.admin = ownerTok, mgrTok, adminTok
	e.exec(t, `INSERT INTO user_roles (user_id, role_id, org_id) SELECT $1, id, NULL FROM roles WHERE name = 'admin' AND org_id IS NULL`, adminID)

	_, raw, err := apikeys.Issue(ctx, apikeys.NewStoreFromQueries(q), apikeys.IssueInput{
		OrgID: e.orgB, Name: "bill-" + uuid.NewString()[:6], Scopes: []string{"billing.read"}, CreatedBy: ownerID,
	})
	if err != nil {
		t.Fatalf("apikeys.Issue: %v", err)
	}
	e.key = raw

	t.Cleanup(func() {
		c := context.Background()
		for _, org := range []uuid.UUID{e.orgA, e.orgB} {
			_, _ = pool.Exec(c, `DELETE FROM invoices WHERE org_id = $1`, org)
		}
		_, _ = pool.Exec(c, `DELETE FROM api_keys WHERE org_id = $1`, e.orgB)
		_, _ = pool.Exec(c, `DELETE FROM user_roles WHERE user_id = $1`, adminID)
		_, _ = pool.Exec(c, `DELETE FROM memberships WHERE org_id = $1`, e.orgB)
		for _, org := range []uuid.UUID{e.orgA, e.orgB} {
			_, _ = pool.Exec(c, `DELETE FROM organizations WHERE id = $1`, org)
		}
	})
	return e
}

func (e *billEnv) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := e.pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("fixture %q: %v", sql, err)
	}
}

// invoice seeds a draft invoice for org. Each call uses its own billing period
// because (org_id, billing_period) is unique.
func (e *billEnv) invoice(t *testing.T, org uuid.UUID) uuid.UUID {
	t.Helper()
	e.period++
	period := time.Date(2030, time.Month(e.period%12+1), 1, 0, 0, 0, 0, time.UTC).Format("2006-01")
	inv, err := e.q.InsertInvoice(context.Background(), org, period, 1000, "EUR")
	if err != nil {
		t.Fatalf("InsertInvoice: %v", err)
	}
	return inv.ID
}

func (e *billEnv) invoiceSnapshot(t *testing.T, id uuid.UUID) string {
	t.Helper()
	var s string
	err := e.pool.QueryRow(context.Background(),
		`SELECT concat_ws('|', org_id, state, total_amount_minor, issued_at, paid_at, voided_at, updated_at) FROM invoices WHERE id = $1`, id).Scan(&s)
	if err != nil {
		t.Fatalf("invoice snapshot: %v", err)
	}
	return s
}

func (e *billEnv) do(t *testing.T, method, path, token string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(method, e.ts.URL+path, strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Admin-Reason", "org isolation sweep")
	resp, err := e.ts.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	raw := integReadBody(t, resp)
	out := map[string]any{}
	if strings.TrimSpace(raw) != "" {
		_ = json.Unmarshal([]byte(raw), &out)
	}
	return resp.StatusCode, out
}

type billRoute struct {
	method, suffix string
	write          bool
}

func billRoutes() []billRoute {
	return []billRoute{
		{http.MethodGet, "", false},
		{http.MethodPost, "/issue", true},
		{http.MethodPost, "/pay", true},
		{http.MethodPost, "/void", true},
	}
}

func TestOrgIsolationBilling_ForeignInvoiceIsNotFoundAndNothingMoves(t *testing.T) {
	e := newBillEnv(t)
	for _, route := range billRoutes() {
		for _, caller := range e.callers() {
			route, caller := route, caller
			t.Run(route.method+route.suffix+"/"+caller.name, func(t *testing.T) {
				id := e.invoice(t, e.orgA)
				before := e.invoiceSnapshot(t, id)

				status, out := e.do(t, route.method, "/v1/billing/invoices/"+id.String()+route.suffix, caller.token)

				want := caller.readStatus
				if route.write {
					want = caller.writeStatus
				}
				if status != want {
					t.Errorf("status %d, want %d (%v)", status, want, out)
				}
				if want == http.StatusNotFound {
					if errObj, _ := out["error"].(map[string]any); errObj["code"] != "billing.not_found" {
						t.Errorf("error code %v, want billing.not_found", errObj["code"])
					}
				}
				if after := e.invoiceSnapshot(t, id); after != before {
					t.Errorf("organization A's invoice moved:\n  before %s\n  after  %s", before, after)
				}
			})
		}
	}
}

// The same calls on organization B's OWN invoice keep working wherever the
// caller holds the permission.
func TestOrgIsolationBilling_OwnInvoiceStillWorks(t *testing.T) {
	e := newBillEnv(t)

	t.Run("owner reads", func(t *testing.T) {
		id := e.invoice(t, e.orgB)
		if status, out := e.do(t, http.MethodGet, "/v1/billing/invoices/"+id.String(), e.owner); status != http.StatusOK {
			t.Errorf("owner read own invoice: status %d (%v)", status, out)
		}
	})
	t.Run("api key reads", func(t *testing.T) {
		id := e.invoice(t, e.orgB)
		if status, out := e.do(t, http.MethodGet, "/v1/billing/invoices/"+id.String(), e.key); status != http.StatusOK {
			t.Errorf("api key read own invoice: status %d (%v)", status, out)
		}
	})
	t.Run("admin role issues", func(t *testing.T) {
		id := e.invoice(t, e.orgB)
		if status, out := e.do(t, http.MethodPost, "/v1/billing/invoices/"+id.String()+"/issue", e.admin); status != http.StatusOK {
			t.Errorf("admin role issue own invoice: status %d (%v)", status, out)
		}
	})
}
