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
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/refunds"
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

func TestRefundOrgRoutes_QueueAndDetailStayInsideTheOrganization(t *testing.T) {
	stub := newStubStripe(t)
	srv, _ := productionIntegrationServerCfg(t, func(c *config.Config) { c.StripeAPIBaseURL = stub.baseURL() })
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	pf := &prom0113Fixture{ts: ts, client: ts.Client(), q: gen.New(srv.pgxPool)}
	user, err := pf.q.InsertUser(context.Background(), "pay06b-"+uuid.NewString()+"@example.test", "x", "en")
	if err != nil {
		t.Fatal(err)
	}
	pf.userID = user.ID
	org := pf.org(t, "PAY06B")
	other := pf.org(t, "PAY06BOTHER")
	key := pf.key(t, org, "refund.read", "refund.create", "refund.approve")
	otherKey := pf.key(t, other, "refund.read")
	stub.refundHandler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"re_pay06b","status":"succeeded"}`)
	}
	stub.sessionPI = "pi_pay06b_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
	f := newPay03Order(t, srv.pgxPool, org, "stripe")
	base := "/v1/organizations/" + org.String()

	body := fmt.Sprintf(`{"items":[{"ticket_id":%q}],"reason":"queue test"}`, f.tickets[0])
	st, created := refundOrgPost(t, pf, base+"/orders/"+f.order.String()+"/refunds", key, "pay06b-"+uuid.NewString(), body)
	if st != http.StatusAccepted {
		t.Fatalf("create: %d %v", st, created)
	}
	refundID := created["refunds"].([]any)[0].(map[string]any)["id"].(string)

	// ── the queue lists it, with the order number
	st, out := pf.do(t, http.MethodGet, base+"/refunds", key, "")
	if st != http.StatusOK || out["total"] != float64(1) || out["has_more"] != false {
		t.Fatalf("queue: %d %v", st, out)
	}
	row := out["refunds"].([]any)[0].(map[string]any)
	if row["id"] != refundID || row["state"] != "succeeded" || row["order_number"] == nil || row["event_name"] == nil {
		t.Fatalf("queue row = %v", row)
	}
	// ── who asked: the API key that created it, by name
	if c, _ := row["creator"].(map[string]any); c == nil || c["kind"] != "api_key" || c["label"] == nil {
		t.Fatalf("creator = %v; want the API key", row["creator"])
	}
	// ── filters
	if st, out = pf.do(t, http.MethodGet, base+"/refunds?state=manual_review", key, ""); st != http.StatusOK || out["total"] != float64(0) {
		t.Fatalf("state filter: %d %v; want an empty queue", st, out)
	}
	if st, out = pf.do(t, http.MethodGet, base+"/refunds?state=succeeded&limit=1&offset=0", key, ""); st != http.StatusOK || out["total"] != float64(1) {
		t.Fatalf("succeeded filter: %d %v", st, out)
	}
	if st, out = pf.do(t, http.MethodGet, base+"/refunds?state=nonsense", key, ""); st != http.StatusBadRequest || prom0113Code(out) != "refund.invalid_request" {
		t.Fatalf("bad state: %d %v; want 400 refund.invalid_request", st, out)
	}
	if st, out = pf.do(t, http.MethodGet, base+"/refunds?limit=-1", key, ""); st != http.StatusBadRequest {
		t.Fatalf("bad limit: %d %v; want 400", st, out)
	}

	// ── detail with history
	st, out = pf.do(t, http.MethodGet, base+"/refunds/"+refundID, key, "")
	if st != http.StatusOK || out["refund"].(map[string]any)["id"] != refundID {
		t.Fatalf("detail: %d %v", st, out)
	}
	if hist, _ := out["history"].([]any); len(hist) == 0 {
		t.Fatalf("detail history is empty: %v", out)
	}
	// ── a long life: the detail keeps the LATEST hundred steps, oldest first
	if _, err := srv.pgxPool.Exec(context.Background(), `INSERT INTO audit_events (occurred_at, actor_type, action, resource_type, resource_id, metadata)
		SELECT now() + (g || ' seconds')::interval, 'system', 'v1.refund.test_step', 'refund', $1, jsonb_build_object('state', 'step' || g)
		FROM generate_series(1, 150) g`, refundID); err != nil {
		t.Fatal(err)
	}
	st, out = pf.do(t, http.MethodGet, base+"/refunds/"+refundID, key, "")
	hist, _ := out["history"].([]any)
	if st != http.StatusOK || len(hist) != 100 || out["history_total"].(float64) < 150 {
		t.Fatalf("long history: %d, %d steps, total %v; want 100 of 150+", st, len(hist), out["history_total"])
	}
	if last := hist[len(hist)-1].(map[string]any); last["state"] != "step150" {
		t.Fatalf("the last step is %v; want the newest (step150)", last)
	}
	t.Cleanup(func() {
		_, _ = srv.pgxPool.Exec(context.Background(), `DELETE FROM audit_events WHERE resource_type = 'refund' AND resource_id = $1 AND action = 'v1.refund.test_step'`, refundID)
	})

	// ── another organization sees none of it
	otherBase := "/v1/organizations/" + other.String()
	if st, out = pf.do(t, http.MethodGet, otherBase+"/refunds", otherKey, ""); st != http.StatusOK || out["total"] != float64(0) {
		t.Fatalf("foreign queue: %d %v; want empty", st, out)
	}
	if st, out = pf.do(t, http.MethodGet, otherBase+"/refunds/"+refundID, otherKey, ""); st != http.StatusNotFound || prom0113Code(out) != "refund.not_found" {
		t.Fatalf("foreign detail: %d %v; want 404 refund.not_found", st, out)
	}
	// ...and the path organization is not a way in.
	if st, _ = pf.do(t, http.MethodGet, base+"/refunds", otherKey, ""); st != http.StatusForbidden && st != http.StatusNotFound {
		t.Fatalf("a key of another organization on this path: %d; want 403/404", st)
	}
}

// A refund the provider refused can be sent again as a NEW operation, one
// the provider never saw cannot be retried blindly, and only a platform
// operator closes a refund in manual review (PAY-06c).
func TestRefundOrgRoutes_RetryAndResolve(t *testing.T) {
	stub := newStubStripe(t)
	srv, _ := productionIntegrationServerCfg(t, func(c *config.Config) { c.StripeAPIBaseURL = stub.baseURL() })
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	pf := &prom0113Fixture{ts: ts, client: ts.Client(), q: gen.New(srv.pgxPool)}
	user, err := pf.q.InsertUser(context.Background(), "pay06c-"+uuid.NewString()+"@example.test", "x", "en")
	if err != nil {
		t.Fatal(err)
	}
	pf.userID = user.ID
	org := pf.org(t, "PAY06C")
	other := pf.org(t, "PAY06COTHER")
	key := pf.key(t, org, "refund.read", "refund.create", "refund.approve")
	otherKey := pf.key(t, other, "refund.read", "refund.approve")
	stub.sessionPI = "pi_pay06c_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
	declined := true
	stub.refundHandler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if declined {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprint(w, `{"error":{"type":"invalid_request_error","code":"refund_declined","message":"declined for the test"}}`)
			return
		}
		_, _ = fmt.Fprint(w, `{"id":"re_pay06c","status":"succeeded"}`)
	}
	f := newPay03Order(t, srv.pgxPool, org, "stripe")
	base := "/v1/organizations/" + org.String()

	body := fmt.Sprintf(`{"items":[{"ticket_id":%q}],"reason":"retry test"}`, f.tickets[0])
	st, created := refundOrgPost(t, pf, base+"/orders/"+f.order.String()+"/refunds", key, "pay06c-"+uuid.NewString(), body)
	if st != http.StatusAccepted {
		t.Fatalf("create: %d %v", st, created)
	}
	first := created["refunds"].([]any)[0].(map[string]any)
	firstID := first["id"].(string)
	if first["state"] != "failed" {
		t.Fatalf("first refund = %v; want failed", first)
	}

	// another organization cannot reach it
	otherBase := "/v1/organizations/" + other.String()
	if st, out := refundOrgPost(t, pf, otherBase+"/refunds/"+firstID+"/retry", otherKey, "", `{}`); st != http.StatusNotFound || prom0113Code(out) != "refund.not_found" {
		t.Fatalf("foreign retry: %d %v; want 404 refund.not_found", st, out)
	}

	// retry sends a NEW refund and the provider takes it
	declined = false
	st, out := refundOrgPost(t, pf, base+"/refunds/"+firstID+"/retry", key, "", `{}`)
	if st != http.StatusAccepted {
		t.Fatalf("retry: %d %v", st, out)
	}
	second := out["refunds"].([]any)[0].(map[string]any)
	if second["id"] == firstID || second["state"] != "succeeded" {
		t.Fatalf("retried refund = %v; want a new succeeded refund", second)
	}
	// a second press replays the first retry and sends nothing new
	st, out = refundOrgPost(t, pf, base+"/refunds/"+firstID+"/retry", key, "", `{}`)
	if st != http.StatusAccepted || out["replayed"] != true {
		t.Fatalf("retry replay: %d %v; want replayed", st, out)
	}
	// an accepted refund is not retryable
	st, out = refundOrgPost(t, pf, base+"/refunds/"+second["id"].(string)+"/retry", key, "", `{}`)
	if st != http.StatusConflict || prom0113Code(out) != "refund.not_retryable" {
		t.Fatalf("retry of a succeeded refund: %d %v; want 409 refund.not_retryable", st, out)
	}

	// resolve: an organization key is not a platform operator
	if st, out = refundOrgPost(t, pf, base+"/refunds/"+firstID+"/resolve", key, "", `{"outcome":"not_refunded","note":"x"}`); st != http.StatusForbidden {
		t.Fatalf("resolve by an org key: %d %v; want 403", st, out)
	}

	// the engine closes a manual_review refund both ways
	ctx := context.Background()
	engine := srv.refundEngine()
	secondID := uuid.MustParse(second["id"].(string))
	if _, err := srv.pgxPool.Exec(ctx, `UPDATE refunds SET state='manual_review', provider_attempts=2 WHERE id=$1`, secondID); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Resolve(ctx, refunds.ResolveInput{OrgID: org, RefundID: secondID, Outcome: "not_refunded"}); err == nil {
		t.Fatal("resolve without a note succeeded")
	}
	got, err := engine.Resolve(ctx, refunds.ResolveInput{OrgID: org, RefundID: secondID, Outcome: "not_refunded", Note: "dashboard shows nothing"})
	if err != nil || got.State != "failed" {
		t.Fatalf("resolve not_refunded = %v, %v; want failed", got.State, err)
	}
	if _, err := engine.Resolve(ctx, refunds.ResolveInput{OrgID: org, RefundID: secondID, Outcome: "refunded", Note: "again"}); err == nil {
		t.Fatal("resolving a refund that is no longer in review succeeded")
	}
}

// Review fixes of the retry and resolve routes: a refusal before the provider
// is reached does not use up an attempt, a never-sent refund is replaced (not
// duplicated) by its retry, a refund is retried once, an operator cannot
// close a never-sent refund as refunded or push a payment over budget, the
// operator's note stays out of the organizer's view, and a ticket under
// review is not refundable.
func TestRefundOrgRoutes_RetryAndResolveReviewFixes(t *testing.T) {
	stub := newStubStripe(t)
	srv, _ := productionIntegrationServerCfg(t, func(c *config.Config) { c.StripeAPIBaseURL = stub.baseURL() })
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	pf := &prom0113Fixture{ts: ts, client: ts.Client(), q: gen.New(srv.pgxPool)}
	ctx := context.Background()
	user, err := pf.q.InsertUser(ctx, "pay06r-"+uuid.NewString()+"@example.test", "x", "en")
	if err != nil {
		t.Fatal(err)
	}
	pf.userID = user.ID
	org := pf.org(t, "PAY06R")
	key := pf.key(t, org, "refund.read", "refund.create", "refund.approve")
	stub.sessionPI = "pi_pay06r_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
	declined := false
	stub.refundHandler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if declined {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprint(w, `{"error":{"type":"invalid_request_error","code":"refund_declined","message":"declined for the test"}}`)
			return
		}
		_, _ = fmt.Fprintf(w, `{"id":"re_%s","status":"succeeded"}`, uuid.NewString()[:8])
	}
	base := "/v1/organizations/" + org.String()
	engine := srv.refundEngine()
	create := func(f *pay03Fixture, ticket int) (int, map[string]any) {
		body := fmt.Sprintf(`{"items":[{"ticket_id":%q}],"reason":"review fixes"}`, f.tickets[ticket])
		return refundOrgPost(t, pf, base+"/orders/"+f.order.String()+"/refunds", key, "pay06r-"+uuid.NewString(), body)
	}
	one := func(out map[string]any) map[string]any { return out["refunds"].([]any)[0].(map[string]any) }
	rowOf := func(id string) (state string, attempts int, code *string, reason *string) {
		if err := srv.pgxPool.QueryRow(ctx, `SELECT state, provider_attempts, failure_code, failure_reason FROM refunds WHERE id = $1`, id).
			Scan(&state, &attempts, &code, &reason); err != nil {
			t.Fatal(err)
		}
		return
	}

	// ── a refusal before the provider does not count as an attempt; the
	//    never-sent refund is replaced by its retry, once
	f := newPay03Order(t, srv.pgxPool, org, "stripe")
	if _, err := srv.pgxPool.Exec(ctx, `UPDATE payment_provider_configs SET is_active = false WHERE org_id = $1`, org); err != nil {
		t.Fatal(err)
	}
	st, out := create(f, 0)
	if st != http.StatusAccepted {
		t.Fatalf("create without a usable configuration: %d %v", st, out)
	}
	parked := one(out)["id"].(string)
	state, attempts, _, _ := rowOf(parked)
	if state != "manual_review" || attempts != 0 {
		t.Fatalf("parked refund = %s with %d attempt(s); want manual_review and 0 (nothing reached the provider)", state, attempts)
	}
	secondID := uuid.MustParse(parked)
	if _, err := engine.Resolve(ctx, refunds.ResolveInput{OrgID: org, RefundID: secondID, Outcome: "refunded", Note: "checked"}); err == nil ||
		!strings.Contains(err.Error(), "never reached the provider") {
		t.Fatalf("closing a never-sent refund as refunded: %v; want a refusal", err)
	}
	if _, err := srv.pgxPool.Exec(ctx, `UPDATE payment_provider_configs SET is_active = true WHERE org_id = $1`, org); err != nil {
		t.Fatal(err)
	}
	st, out = refundOrgPost(t, pf, base+"/refunds/"+parked+"/retry", key, "", `{}`)
	if st != http.StatusAccepted || one(out)["state"] != "succeeded" {
		t.Fatalf("retry of the never-sent refund: %d %v; want a succeeded replacement", st, out)
	}
	if state, _, code, _ := rowOf(parked); state != "failed" || code == nil || *code != "replaced_by_retry" {
		t.Fatalf("the replaced refund is %s/%v; want failed/replaced_by_retry", state, code)
	}

	// ── the queue says which rows may be retried: the replaced one no longer
	st, queue := pf.do(t, http.MethodGet, base+"/refunds?state=failed&limit=100", key, "")
	if st != http.StatusOK {
		t.Fatalf("queue: %d %v", st, queue)
	}
	for _, r := range queue["refunds"].([]any) {
		if m := r.(map[string]any); m["id"] == parked && m["retryable"] != false {
			t.Fatalf("the replaced refund is still offered for a retry: %v", m)
		}
	}

	// ── a retry that fails too: the first refund is not retried again
	f2 := newPay03Order(t, srv.pgxPool, org, "stripe")
	declined = true
	st, out = create(f2, 0)
	failedA := one(out)["id"].(string)
	if st != http.StatusAccepted || one(out)["state"] != "failed" {
		t.Fatalf("declined create: %d %v", st, out)
	}
	queueRow := func(id string) map[string]any {
		st, q := pf.do(t, http.MethodGet, base+"/refunds?state=failed&limit=100", key, "")
		if st != http.StatusOK {
			t.Fatalf("queue: %d %v", st, q)
		}
		for _, r := range q["refunds"].([]any) {
			if m := r.(map[string]any); m["id"] == id {
				return m
			}
		}
		t.Fatalf("refund %s missing from the queue", id)
		return nil
	}
	if m := queueRow(failedA); m["retryable"] != true || m["order_id"] != f2.order.String() {
		t.Fatalf("a refused refund in the queue: %v; want retryable and its order id", m)
	}
	st, out = refundOrgPost(t, pf, base+"/refunds/"+failedA+"/retry", key, "", `{}`)
	if st != http.StatusAccepted || one(out)["state"] != "failed" {
		t.Fatalf("declined retry: %d %v", st, out)
	}
	st, out = refundOrgPost(t, pf, base+"/refunds/"+failedA+"/retry", key, "", `{}`)
	if st != http.StatusConflict || prom0113Code(out) != "refund.already_replaced" {
		t.Fatalf("second retry of the same refund: %d %v; want 409 refund.already_replaced", st, out)
	}
	declined = false
	if m := queueRow(failedA); m["retryable"] != false {
		t.Fatalf("a refund that was already retried is offered again: %v", m)
	}

	// ── resolve: the note stays in the audit trail, the row keeps its reason,
	//    a refund found refunded carries no failure, and the budget is checked
	f3 := newPay03Order(t, srv.pgxPool, org, "stripe")
	st, out = create(f3, 0)
	id3 := one(out)["id"].(string)
	if st != http.StatusAccepted {
		t.Fatalf("create: %d %v", st, out)
	}
	if _, err := srv.pgxPool.Exec(ctx, `UPDATE refunds SET state='manual_review', provider_attempts=2, failure_code='unknown_outcome',
		failure_reason='check the dashboard', settled_at = NULL WHERE id=$1`, id3); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.pgxPool.Exec(ctx, `UPDATE refunds SET amount = 9000 WHERE id=$1`, id3); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Resolve(ctx, refunds.ResolveInput{OrgID: org, RefundID: uuid.MustParse(id3), Outcome: "refunded", Note: "ok"}); err == nil ||
		!strings.Contains(err.Error(), "more than was paid") {
		t.Fatalf("resolve over budget: %v; want a refusal", err)
	}
	if _, err := srv.pgxPool.Exec(ctx, `UPDATE refunds SET amount = 2500 WHERE id=$1`, id3); err != nil {
		t.Fatal(err)
	}
	got, err := engine.Resolve(ctx, refunds.ResolveInput{OrgID: org, RefundID: uuid.MustParse(id3), Outcome: "refunded", Note: "SECRET OPERATOR NOTE"})
	if err != nil || got.State != "succeeded" || got.FailureCode != nil || got.FailureReason != nil {
		t.Fatalf("resolve refunded = %+v, %v; want succeeded with no failure", got, err)
	}
	st, detail := pf.do(t, http.MethodGet, base+"/refunds/"+id3, key, "")
	if raw, _ := json.Marshal(detail); st != http.StatusOK || strings.Contains(string(raw), "SECRET OPERATOR NOTE") {
		t.Fatalf("the organizer sees the operator's note: %d %s", st, raw)
	}
	var noted bool
	_ = srv.pgxPool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM audit_events WHERE resource_id = $1 AND action = 'v1.refund.resolve'
		AND metadata->>'note' = 'SECRET OPERATOR NOTE' AND metadata->>'previous_failure_code' = 'unknown_outcome')`, id3).Scan(&noted)
	if !noted {
		t.Fatal("the audit event does not carry the note and the original failure code")
	}

	// ── a ticket under review is not refundable (options and create)
	f4 := newPay03Order(t, srv.pgxPool, org, "stripe")
	if _, err := srv.pgxPool.Exec(ctx, `UPDATE tickets SET review_hold = true, review_hold_reason = 'test' WHERE id = $1`, f4.tickets[0]); err != nil {
		t.Fatal(err)
	}
	st, opts := pf.do(t, http.MethodGet, base+"/orders/"+f4.order.String()+"/refund-options", key, "")
	held := false
	for _, tk := range opts["tickets"].([]any) {
		m := tk.(map[string]any)
		if m["ticket_id"] == f4.tickets[0].String() && m["not_refundable_reason"] == "review_hold" && m["refundable"] == float64(0) {
			held = true
		}
	}
	if st != http.StatusOK || !held {
		t.Fatalf("options of a ticket under review: %d %v; want refundable 0 with reason review_hold", st, opts)
	}
	if st, out = create(f4, 0); st != http.StatusConflict || prom0113Code(out) != "refund.ticket_not_refundable" {
		t.Fatalf("refund of a ticket under review: %d %v; want 409 refund.ticket_not_refundable", st, out)
	}
}

// A client that hangs up while the provider is slow must not leave the refund
// half driven: the work is detached from the request, and repeating the call
// with the same Idempotency-Key reads the result.
func TestRefundOrgRoutes_ClientHangUpDoesNotAbortTheRefund(t *testing.T) {
	stub := newStubStripe(t)
	srv, _ := productionIntegrationServerCfg(t, func(c *config.Config) { c.StripeAPIBaseURL = stub.baseURL() })
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	pf := &prom0113Fixture{ts: ts, client: ts.Client(), q: gen.New(srv.pgxPool)}
	ctx := context.Background()
	user, err := pf.q.InsertUser(ctx, "pay06h-"+uuid.NewString()+"@example.test", "x", "en")
	if err != nil {
		t.Fatal(err)
	}
	pf.userID = user.ID
	org := pf.org(t, "PAY06H")
	key := pf.key(t, org, "refund.read", "refund.create", "refund.approve")
	stub.sessionPI = "pi_pay06h_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
	stub.refundHandler = func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(600 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"re_pay06h","status":"succeeded"}`)
	}
	f := newPay03Order(t, srv.pgxPool, org, "stripe")
	idem := "pay06h-" + uuid.NewString()
	body := fmt.Sprintf(`{"items":[{"ticket_id":%q}],"reason":"hang up"}`, f.tickets[0])
	path := "/v1/organizations/" + org.String() + "/orders/" + f.order.String() + "/refunds"

	short, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(short, http.MethodPost, ts.URL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Idempotency-Key", idem)
	if resp, err := ts.Client().Do(req); err == nil {
		resp.Body.Close()
		t.Fatal("the slow call answered before the client gave up; the test proves nothing")
	}

	// the refund still completes on the server
	deadline := time.Now().Add(5 * time.Second)
	var state string
	for time.Now().Before(deadline) {
		_ = srv.pgxPool.QueryRow(ctx, `SELECT COALESCE(max(state), '') FROM refunds WHERE order_id = $1`, f.order).Scan(&state)
		if state == "succeeded" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if state != "succeeded" {
		t.Fatalf("after the client hung up the refund is %q; want succeeded", state)
	}
	// and the same key reads the result back
	st, out := refundOrgPost(t, pf, path, key, idem, body)
	if st != http.StatusAccepted || out["replayed"] != true {
		t.Fatalf("replay after the hang-up: %d %v; want replayed", st, out)
	}
}

// Round-2 review R2-3: a refund whose provider call outlasts the server's
// WriteTimeout still reaches the caller. httptest's server has no write
// timeout of its own, so the test sets one on the real http.Server.
func TestRefundOrgRoutes_AnswerOutlivesWriteTimeout(t *testing.T) {
	stub := newStubStripe(t)
	srv, _ := productionIntegrationServerCfg(t, func(c *config.Config) { c.StripeAPIBaseURL = stub.baseURL() })
	ts := httptest.NewUnstartedServer(srv.Router())
	ts.Config.WriteTimeout = 500 * time.Millisecond
	ts.Start()
	t.Cleanup(ts.Close)
	pf := &prom0113Fixture{ts: ts, client: ts.Client(), q: gen.New(srv.pgxPool)}
	ctx := context.Background()
	user, err := pf.q.InsertUser(ctx, "r23-"+uuid.NewString()+"@example.test", "x", "en")
	if err != nil {
		t.Fatal(err)
	}
	pf.userID = user.ID
	org := pf.org(t, "R23")
	key := pf.key(t, org, "refund.read", "refund.create", "refund.approve")
	stub.sessionPI = "pi_r23_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
	stub.refundHandler = func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(1500 * time.Millisecond) // longer than the write timeout
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":"re_%s","status":"succeeded"}`, uuid.NewString()[:8])
	}
	f := newPay03Order(t, srv.pgxPool, org, "stripe")
	body := fmt.Sprintf(`{"items":[{"ticket_id":%q}],"reason":"slow provider"}`, f.tickets[0])
	st, out := refundOrgPost(t, pf, "/v1/organizations/"+org.String()+"/orders/"+f.order.String()+"/refunds", key, "r23-"+uuid.NewString(), body)
	if st != http.StatusAccepted {
		t.Fatalf("create through a slow provider: %d %v; want 202", st, out)
	}
	if got := out["refunds"].([]any)[0].(map[string]any)["state"]; got != "succeeded" {
		t.Fatalf("refund state = %v; want succeeded", got)
	}
}
