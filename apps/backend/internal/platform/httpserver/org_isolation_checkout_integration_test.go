//go:build integration

package httpserver

// org_isolation_checkout_integration_test.go — SEC-3: the authenticated
// reservation and checkout routes carry no {org_id} in the path. Organization
// B, as an owner, a manager and an API key, names organization A's ids (or
// A's org_id in a body) and must get the route's own 404 with A's rows
// unchanged. The anonymous widget flow lives under /v1/public and is not
// touched here.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/apikeys"
)

var checkoutSweepScopes = []string{
	"reservation.read", "reservation.activate", "reservation.create", "reservation.cancel",
	"checkout.read", "checkout.start", "checkout.confirm", "checkout.complete", "checkout.abandon",
	"promo.validate", "pricing.quote",
}

func (e *sweepEnv) seedReservationAndCheckout(t *testing.T, w *sweepWorld) (res, cs uuid.UUID) {
	t.Helper()
	res, cs = uuid.New(), uuid.New()
	e.exec(t, `INSERT INTO reservations (id, org_id, channel_id, session_id, tier_id, quantity, state, expires_at)
	           VALUES ($1, $2, $3, $4, $5, 1, 'active', now() + interval '1 hour')`, res, w.org, w.channel, w.s1, w.tier)
	e.exec(t, `INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state)
	           VALUES ($1, $2, $3, $4, 'created')`, cs, w.org, w.channel, res)
	t.Cleanup(func() {
		c := context.Background()
		_, _ = e.pool.Exec(c, `DELETE FROM checkout_sessions WHERE id = $1`, cs)
		_, _ = e.pool.Exec(c, `DELETE FROM reservations WHERE id = $1`, res)
	})
	return res, cs
}

func (e *sweepEnv) checkoutSnapshot(t *testing.T, org uuid.UUID) string {
	t.Helper()
	var out string
	if err := e.pool.QueryRow(context.Background(), `
		SELECT (SELECT COALESCE(string_agg(id||':'||state||':'||expires_at, ',' ORDER BY id), '') FROM reservations WHERE org_id = $1)
		    || '|' || (SELECT COALESCE(string_agg(id||':'||state, ',' ORDER BY id), '') FROM checkout_sessions WHERE org_id = $1)`,
		org).Scan(&out); err != nil {
		t.Fatalf("checkout snapshot: %v", err)
	}
	return out
}

func (e *sweepEnv) checkoutKey(t *testing.T) string {
	t.Helper()
	u, err := e.q.InsertUser(context.Background(), "sweep-key-"+uuid.NewString()+"@example.test", "x", "en")
	if err != nil {
		t.Fatalf("InsertUser: %v", err)
	}
	t.Cleanup(func() { _, _ = e.pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, u.ID) })
	_, raw, err := apikeys.Issue(context.Background(), apikeys.NewStoreFromQueries(e.q), apikeys.IssueInput{
		OrgID: e.orgB, Name: "sweep-co-" + uuid.NewString()[:6], Scopes: checkoutSweepScopes, CreatedBy: u.ID,
	})
	if err != nil {
		t.Fatalf("apikeys.Issue: %v", err)
	}
	return raw
}

type checkoutRoute struct {
	name, method, path, perm, body string
}

func checkoutRoutes(w *sweepWorld, res, cs uuid.UUID, org, channel, session, tier uuid.UUID) []checkoutRoute {
	reserveBody := func(o, c, s uuid.UUID) string {
		return fmt.Sprintf(`{"org_id":%q,"channel_id":%q,"session_id":%q,"tier_id":%q,"quantity":1}`, o, c, s, tier)
	}
	return []checkoutRoute{
		{"get reservation", "GET", "/v1/reservations/" + res.String(), "reservation.read", ""},
		{"activate reservation", "PATCH", "/v1/reservations/" + res.String() + "/activate", "reservation.activate", ""},
		{"cancel reservation", "DELETE", "/v1/reservations/" + res.String(), "reservation.cancel", ""},
		{"reserve in foreign org", "POST", "/v1/reservations", "reservation.create", reserveBody(org, channel, session)},
		{"get checkout", "GET", "/v1/checkout/" + cs.String(), "checkout.read", ""},
		{"price breakdown", "GET", "/v1/checkout/" + cs.String() + "/price-breakdown", "checkout.read", ""},
		{"confirm checkout", "POST", "/v1/checkout/" + cs.String() + "/confirm", "checkout.confirm", `{}`},
		{"complete checkout", "POST", "/v1/checkout/" + cs.String() + "/complete", "checkout.complete", `{}`},
		{"abandon checkout", "POST", "/v1/checkout/" + cs.String() + "/abandon", "checkout.abandon", `{}`},
		{"start checkout in foreign org", "POST", "/v1/checkout/start", "checkout.start",
			fmt.Sprintf(`{"org_id":%q,"channel_id":%q,"reservation_id":%q}`, org, channel, res)},
		{"validate foreign promo", "POST", "/v1/checkout/promo-validate", "promo.validate",
			fmt.Sprintf(`{"org_id":%q,"code":"NOPE","order_amount":1000}`, org)},
		{"quote in foreign org", "GET", fmt.Sprintf("/v1/checkout/quote?org_id=%s&session_id=%s&tier_id=%s&quantity=1", org, session, tier), "pricing.quote", ""},
	}
}

func TestOrgIsolationCheckout_ForeignRowsAreNotFoundAndNothingMoves(t *testing.T) {
	e := newSweepEnv(t)
	orgA := e.newOrg(t, "A")
	wA := e.newWorld(t, orgA, e.orgC)
	resA, csA := e.seedReservationAndCheckout(t, wA)
	wB := e.newWorld(t, e.orgB, e.orgB)

	callers := e.callers()
	callers[2].token = e.checkoutKey(t)

	// A's ids with A's own org_id, and A's session/channel under B's org_id:
	// both must be refused.
	variants := map[string][]checkoutRoute{
		"A org_id":             checkoutRoutes(wA, resA, csA, orgA, wA.channel, wA.s1, wA.tier),
		"B org_id with A rows": checkoutRoutes(wA, resA, csA, e.orgB, wA.channel, wA.s1, wA.tier),
	}
	before := e.checkoutSnapshot(t, orgA)
	for vname, routes := range variants {
		for _, rt := range routes {
			if vname == "B org_id with A rows" && !strings.Contains(rt.body, e.orgB.String()) && !strings.Contains(rt.path, e.orgB.String()) {
				continue
			}
			for _, c := range callers {
				name := vname + "/" + rt.name + "/" + c.name
				code, _ := e.do(t, rt.method, rt.path, c.token, rt.body)
				switch {
				case code == http.StatusNotFound:
				case code == http.StatusForbidden && c.lacks(rt.perm):
				default:
					t.Errorf("%s: got %d, want 404", name, code)
				}
			}
		}
	}
	if after := e.checkoutSnapshot(t, orgA); after != before {
		t.Fatalf("organization A's reservation or checkout rows changed:\n before %s\n after  %s", before, after)
	}

	// Control: the same owner still reads and abandons their OWN rows.
	resB, csB := e.seedReservationAndCheckout(t, wB)
	if code, out := e.do(t, "GET", "/v1/reservations/"+resB.String(), e.owner, ""); code != http.StatusOK {
		t.Errorf("own reservation: got %d %v, want 200", code, out)
	}
	if code, out := e.do(t, "GET", "/v1/checkout/"+csB.String(), e.owner, ""); code != http.StatusOK {
		t.Errorf("own checkout: got %d %v, want 200", code, out)
	}
	if code, out := e.do(t, "POST", "/v1/checkout/"+csB.String()+"/abandon", e.owner, `{}`); code != http.StatusOK {
		b, _ := json.Marshal(out)
		t.Errorf("own checkout abandon: got %d %s, want 200", code, b)
	}
}
