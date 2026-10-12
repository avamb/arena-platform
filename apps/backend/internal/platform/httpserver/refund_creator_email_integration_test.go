//go:build integration

package httpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
)

// The person who asked for a refund is named by their e-mail when they have
// no name: members of the organization see it, an API key does not.
func TestRefundOrgRoutes_CreatorEmailIsHiddenFromAPIKeys(t *testing.T) {
	srv, secret := productionIntegrationServer(t)
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	ctx := context.Background()
	pf := &prom0113Fixture{ts: ts, client: ts.Client(), q: gen.New(srv.pgxPool)}
	owner, err := pf.q.InsertUser(ctx, "creator-"+uuid.NewString()+"@example.test", "x", "en")
	if err != nil {
		t.Fatal(err)
	}
	pf.userID = owner.ID
	org := pf.org(t, "CREATOR")
	if _, err := pf.q.InsertMembership(ctx, owner.ID, org, "org_admin"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = srv.pgxPool.Exec(context.Background(), `DELETE FROM memberships WHERE user_id = $1`, owner.ID)
	})
	f := newPay03Order(t, srv.pgxPool, org, "stripe")
	var refundID uuid.UUID
	if err := srv.pgxPool.QueryRow(ctx, `INSERT INTO refunds (payment_intent_id, org_id, order_id, amount, currency, reason, state, settlement, requested_by)
		VALUES ($1, $2, $3, 2500, 'EUR', 'creator test', 'requested', 'provider', $4) RETURNING id`,
		f.pay, org, f.order, owner.ID.String()).Scan(&refundID); err != nil {
		t.Fatal(err)
	}
	path := "/v1/organizations/" + org.String() + "/refunds/" + refundID.String()
	label := func(bearer string) any {
		t.Helper()
		st, out := pf.do(t, http.MethodGet, path, bearer, "")
		if st != http.StatusOK {
			t.Fatalf("detail: %d %v", st, out)
		}
		c, _ := out["refund"].(map[string]any)["creator"].(map[string]any)
		if c == nil || c["kind"] != "user" {
			t.Fatalf("creator = %v; want a user", c)
		}
		return c["label"]
	}
	tok, _, err := auth.IssueJWT(secret, owner.ID, nil, nil, "arena-api", "arena-api", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if got := label(tok); got != owner.Email {
		t.Errorf("member sees creator %v; want the e-mail", got)
	}
	if got := label(pf.key(t, org, "refund.read")); got != nil {
		t.Errorf("API key sees creator %v; want no label", got)
	}
}
