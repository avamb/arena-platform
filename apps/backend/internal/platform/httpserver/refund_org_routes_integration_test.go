//go:build integration

// refund_org_routes_integration_test.go — PAY-06: the organizer's refund API
// through the real router: refund-options, one refund operation per call with
// an Idempotency-Key, and isolation between organizations.
package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

func refundOrgPost(t *testing.T, pf *prom0113Fixture, path, key, idem, body string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, pf.ts.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	if idem != "" {
		req.Header.Set("Idempotency-Key", idem)
	}
	resp, err := pf.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	out := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("decode %q: %v", raw, err)
		}
	}
	return resp.StatusCode, out
}

func TestRefundOrgRoutes_OptionsCreateReplayAndIsolation(t *testing.T) {
	stub := newStubStripe(t)
	srv, _ := productionIntegrationServerCfg(t, func(c *config.Config) { c.StripeAPIBaseURL = stub.baseURL() })
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	pf := &prom0113Fixture{ts: ts, client: ts.Client(), q: gen.New(srv.pgxPool)}
	user, err := pf.q.InsertUser(context.Background(), "pay06-"+uuid.NewString()+"@example.test", "x", "en")
	if err != nil {
		t.Fatal(err)
	}
	pf.userID = user.ID
	org := pf.org(t, "PAY06")
	other := pf.org(t, "PAY06OTHER")
	key := pf.key(t, org, "refund.read", "refund.create", "refund.approve")
	otherKey := pf.key(t, other, "refund.read", "refund.create", "refund.approve")
	stub.refundHandler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"re_pay06","status":"succeeded"}`)
	}
	stub.sessionPI = "pi_pay06_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
	f := newPay03Order(t, srv.pgxPool, org, "stripe")
	base := "/v1/organizations/" + org.String() + "/orders/" + f.order.String()

	// ── refund-options
	st, out := pf.do(t, http.MethodGet, base+"/refund-options", key, "")
	if st != http.StatusOK || out["route"] != "arena_provider" || out["partial_refund"] != true {
		t.Fatalf("options: %d %v", st, out)
	}
	tickets, _ := out["tickets"].([]any)
	if len(tickets) != 2 {
		t.Fatalf("options tickets = %v; want 2", out["tickets"])
	}
	first, _ := tickets[0].(map[string]any)
	if first["refundable"] != float64(2500) || first["status"] != "active" {
		t.Fatalf("ticket option = %v; want 2500 refundable and active", first)
	}
	// The tickets' refundable amounts may each be capped by what is left of
	// the payment, so the order also states that one total.
	if out["payment_left"] != float64(5000) {
		t.Fatalf("payment_left = %v; want 5000", out["payment_left"])
	}

	// ── another organization sees a missing order
	otherBase := "/v1/organizations/" + other.String() + "/orders/" + f.order.String()
	if st, out = pf.do(t, http.MethodGet, otherBase+"/refund-options", otherKey, ""); st != http.StatusNotFound {
		t.Fatalf("foreign options: %d %v; want 404", st, out)
	}
	body := fmt.Sprintf(`{"items":[{"ticket_id":%q}],"reason":"x"}`, f.tickets[0])
	if st, out = refundOrgPost(t, pf, otherBase+"/refunds", otherKey, "pay06-foreign", body); st != http.StatusNotFound {
		t.Fatalf("foreign create: %d %v; want 404", st, out)
	}
	// ...and the path organization is not a way into someone else's order.
	if st, _ = pf.do(t, http.MethodGet, base+"/refund-options", otherKey, ""); st != http.StatusForbidden && st != http.StatusNotFound {
		t.Fatalf("a key of another organization on this path: %d; want 403/404", st)
	}

	// ── create: an Idempotency-Key is required
	if st, out = refundOrgPost(t, pf, base+"/refunds", key, "", body); st != http.StatusBadRequest || prom0113Code(out) != "refund.invalid_request" {
		t.Fatalf("no key: %d %v; want 400 refund.invalid_request", st, out)
	}

	// ── create one ticket, driven at once (the key holds refund.approve)
	idem := "pay06-" + uuid.NewString()
	st, out = refundOrgPost(t, pf, base+"/refunds", key, idem, body+"")
	if st != http.StatusAccepted || out["replayed"] != false {
		t.Fatalf("create: %d %v; want 202", st, out)
	}
	items, _ := out["refunds"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["state"] != "succeeded" {
		t.Fatalf("refunds = %v; want one succeeded", out["refunds"])
	}
	if s, _ := f.ticket(t, 0); s != "cancelled" {
		t.Fatalf("ticket 0 = %s; want cancelled after the provider accepted", s)
	}

	// ── replay: the same key answers the same batch, nothing new
	st, again := refundOrgPost(t, pf, base+"/refunds", key, idem, body)
	if st != http.StatusAccepted || again["replayed"] != true || again["batch_id"] != out["batch_id"] {
		t.Fatalf("replay: %d %v; want the first batch", st, again)
	}
	// ...a different request under the same key is refused
	other2 := fmt.Sprintf(`{"items":[{"ticket_id":%q}],"reason":"x"}`, f.tickets[1])
	if st, out = refundOrgPost(t, pf, base+"/refunds", key, idem, other2); st != http.StatusConflict || prom0113Code(out) != "refund.idempotency_key_reused" {
		t.Fatalf("reused key: %d %v; want 409 refund.idempotency_key_reused", st, out)
	}

	// ── the refunded ticket has nothing left
	st, out = pf.do(t, http.MethodGet, base+"/refund-options", key, "")
	tickets, _ = out["tickets"].([]any)
	var left float64 = -1
	for _, raw := range tickets {
		tk := raw.(map[string]any)
		if tk["ticket_id"] == f.tickets[0].String() {
			left, _ = tk["refundable"].(float64)
		}
	}
	if st != http.StatusOK || left != 0 {
		t.Fatalf("options after refund: %d refundable %v; want 0", st, left)
	}

	// ── a key without refund.create is refused
	ro := pf.key(t, org, "refund.read")
	if st, _ = refundOrgPost(t, pf, base+"/refunds", ro, "pay06-ro", other2); st != http.StatusForbidden {
		t.Fatalf("read-only key create: %d; want 403", st)
	}
}
