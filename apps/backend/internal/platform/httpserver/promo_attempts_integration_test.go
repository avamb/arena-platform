//go:build integration

package httpserver

// The read side of the promo-code attempt journal (migration 0166) through the
// real router: an organization reads its own attempts (page, filters, summary),
// a key of another organization gets the route's own 404 for the code and an
// empty organization-wide list. Run against a migrated database.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/apikeys"
)

func TestPromoAttemptsRoutes_ReadOwnJournalAndStayInsideTheOrganization(t *testing.T) {
	srv, _ := productionIntegrationServer(t)
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	pool := srv.pgxPool
	ctx := context.Background()
	q := gen.New(pool)
	suffix := uuid.NewString()[:8]

	mkOrg := func(label string) uuid.UUID {
		org, err := q.InsertOrganization(ctx, "PromoAtt "+label+" "+suffix, "promoatt-"+strings.ToLower(label)+"-"+suffix, "CZ", "en", 1200)
		if err != nil {
			t.Fatalf("InsertOrganization: %v", err)
		}
		return org.ID
	}
	orgA, orgB := mkOrg("A"), mkOrg("B")
	user, err := q.InsertUser(ctx, "promoatt-"+suffix+"@example.test", "x", "en")
	if err != nil {
		t.Fatalf("InsertUser: %v", err)
	}
	key := func(org uuid.UUID) string {
		_, raw, err := apikeys.Issue(ctx, apikeys.NewStoreFromQueries(q), apikeys.IssueInput{
			OrgID: org, Name: "promoatt-" + uuid.NewString()[:6], Scopes: []string{"promo.read"}, CreatedBy: user.ID,
		})
		if err != nil {
			t.Fatalf("apikeys.Issue: %v", err)
		}
		return raw
	}
	keyA, keyB := key(orgA), key(orgB)

	codeID := uuid.New()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("fixture %s: %v", sql, err)
		}
	}
	exec(`INSERT INTO promo_codes (id, org_id, code, discount_type, discount_value) VALUES ($1, $2, 'RED30', 'percent', 30)`, codeID, orgA)
	for i, row := range []struct {
		code, result, reason string
		withCode             bool
	}{
		{"RED30", "rejected", "wrong_session", true},
		{"red30", "rejected", "wrong_session", true},
		{"RED30", "applied", "", true},
		{"RED3O", "rejected", "unknown_code", false},
	} {
		var reason *string
		if row.reason != "" {
			reason = &row.reason
		}
		var pc *uuid.UUID
		if row.withCode {
			pc = &codeID
		}
		exec(`INSERT INTO promo_code_attempts (org_id, surface, code_entered, promo_code_id, result, reason, dedup_key, created_at)
		      VALUES ($1, 'widget_quote', $2, $3, $4, $5, $6, now() - make_interval(mins => $7))`,
			orgA, row.code, pc, row.result, reason, "k"+row.code, i)
	}
	t.Cleanup(func() {
		c := context.Background()
		for _, org := range []uuid.UUID{orgA, orgB} {
			_, _ = pool.Exec(c, `DELETE FROM promo_code_attempts WHERE org_id = $1`, org)
			_, _ = pool.Exec(c, `DELETE FROM promo_codes WHERE org_id = $1`, org)
			_, _ = pool.Exec(c, `DELETE FROM api_keys WHERE org_id = $1`, org)
			_, _ = pool.Exec(c, `DELETE FROM organizations WHERE id = $1`, org)
		}
		_, _ = pool.Exec(c, `DELETE FROM users WHERE id = $1`, user.ID)
	})

	get := func(apiKey, path string) (int, map[string]any) {
		t.Helper()
		resp := integDoRequest(t, ts.Client(), http.MethodGet, ts.URL+path, apiKey, "")
		body := integReadBody(t, resp)
		out := map[string]any{}
		_ = json.Unmarshal([]byte(body), &out)
		return resp.StatusCode, out
	}
	codePath := "/v1/organizations/" + orgA.String() + "/promo-codes/" + codeID.String()

	st, list := get(keyA, codePath+"/attempts")
	if st != http.StatusOK || list["total"].(float64) != 3 {
		t.Fatalf("own list = %d %v, want 200 with 3 attempts of the code", st, list)
	}
	st, list = get(keyA, codePath+"/attempts?result=rejected&reason=wrong_session&limit=1")
	if st != http.StatusOK || list["total"].(float64) != 2 || len(list["attempts"].([]any)) != 1 || list["has_more"] != true {
		t.Errorf("filtered page = %d %v, want total 2, one row, has_more", st, list)
	}
	first := list["attempts"].([]any)[0].(map[string]any)
	for _, forbidden := range []string{"ip_hash", "email", "phone", "name"} {
		if _, ok := first[forbidden]; ok {
			t.Errorf("attempt exposes %q", forbidden)
		}
	}

	st, sum := get(keyA, codePath+"/attempts/summary")
	if st != http.StatusOK || sum["total"].(float64) != 3 || sum["applied"].(float64) != 1 || sum["rejected"].(float64) != 2 {
		t.Errorf("summary = %d %v", st, sum)
	}
	reasons := sum["top_reasons"].([]any)
	if len(reasons) != 1 || reasons[0].(map[string]any)["reason"] != "wrong_session" || reasons[0].(map[string]any)["count"].(float64) != 2 {
		t.Errorf("top reasons = %v", reasons)
	}

	// A code nobody created has no row of its own: the organization view finds it.
	st, org := get(keyA, "/v1/organizations/"+orgA.String()+"/promo-code-attempts?code=red3o")
	if st != http.StatusOK || org["total"].(float64) != 1 {
		t.Errorf("org-wide typed-code search = %d %v, want 1", st, org)
	}

	if st, _ := get(keyA, codePath+"/attempts?result=maybe"); st != http.StatusBadRequest {
		t.Errorf("bad result filter = %d, want 400", st)
	}
	if st, _ := get(keyA, codePath+"/attempts?from=yesterday"); st != http.StatusBadRequest {
		t.Errorf("bad date = %d, want 400", st)
	}

	// Organization B: its own journal is empty, A's code is the route's own 404.
	foreign := "/v1/organizations/" + orgB.String() + "/promo-codes/" + codeID.String()
	if st, _ := get(keyB, foreign+"/attempts"); st != http.StatusNotFound {
		t.Errorf("foreign code attempts = %d, want 404", st)
	}
	if st, _ := get(keyB, foreign+"/attempts/summary"); st != http.StatusNotFound {
		t.Errorf("foreign code summary = %d, want 404", st)
	}
	st, own := get(keyB, "/v1/organizations/"+orgB.String()+"/promo-code-attempts")
	if st != http.StatusOK || own["total"].(float64) != 0 {
		t.Errorf("org B journal = %d %v, want empty", st, own)
	}
	// B's key on A's path is refused by the membership gate.
	if st, _ := get(keyB, "/v1/organizations/"+orgA.String()+"/promo-code-attempts"); st == http.StatusOK {
		t.Errorf("org B key read org A's journal")
	}
}
