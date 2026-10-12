//go:build integration

package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

// The queue's retryable flag is the retry route's own verdict: a refused
// refund whose ticket can no longer be refunded is not offered for retry.
func TestRefundQueue_RetryableFollowsTheRetryRoute(t *testing.T) {
	e := newSec1Env(t)
	org := e.org(t, "RETRYABLE")
	key := e.key(t, org, "refund.read")
	f := newPay03Order(t, e.srv.pgxPool, org, "stripe")
	ctx := context.Background()
	var failed string
	if err := e.srv.pgxPool.QueryRow(ctx, `INSERT INTO refunds (payment_intent_id, org_id, order_id, ticket_id, amount, currency, reason,
		state, settlement, provider, provider_attempts, failure_code, failed_at, cancel_ticket)
		VALUES ($1, $2, $3, $4, 2500, 'EUR', 'declined', 'failed', 'provider', 'stripe', 1, 'card_declined', now(), true)
		RETURNING id::text`, f.pay, org, f.order, f.tickets[0]).Scan(&failed); err != nil {
		t.Fatal(err)
	}
	retryable := func() any {
		t.Helper()
		out := e.expect(t, http.StatusOK, http.MethodGet, "/v1/organizations/"+org.String()+"/refunds?limit=100", key, "", "")
		var page struct {
			Refunds []map[string]any `json:"refunds"`
		}
		if err := json.Unmarshal([]byte(out), &page); err != nil {
			t.Fatal(err)
		}
		for _, r := range page.Refunds {
			if r["id"] == failed {
				return r["retryable"]
			}
		}
		t.Fatalf("refund %s missing from the queue", failed)
		return nil
	}
	if got := retryable(); got != true {
		t.Fatalf("a refused refund of an active ticket: retryable %v; want true", got)
	}
	for _, step := range []struct {
		name, set, undo string
	}{
		{"review hold", `UPDATE tickets SET review_hold = true WHERE id = $1`, `UPDATE tickets SET review_hold = false WHERE id = $1`},
		{"cancelled ticket", `UPDATE tickets SET status = 'cancelled' WHERE id = $1`, `UPDATE tickets SET status = 'active' WHERE id = $1`},
	} {
		if _, err := e.srv.pgxPool.Exec(ctx, step.set, f.tickets[0]); err != nil {
			t.Fatal(err)
		}
		if got := retryable(); got != false {
			t.Errorf("%s: retryable %v; want false", step.name, got)
		}
		if _, err := e.srv.pgxPool.Exec(ctx, step.undo, f.tickets[0]); err != nil {
			t.Fatal(err)
		}
	}
	// Another live refund already cancels the ticket.
	if _, err := e.srv.pgxPool.Exec(ctx, `INSERT INTO refunds (payment_intent_id, org_id, order_id, ticket_id, amount, currency, reason,
		state, settlement, provider, cancel_ticket)
		VALUES ($1, $2, $3, $4, 2500, 'EUR', 'again', 'requested', 'provider', 'stripe', true)`, f.pay, org, f.order, f.tickets[0]); err != nil {
		t.Fatal(err)
	}
	if got := retryable(); got != false {
		t.Errorf("another live refund of the ticket: retryable %v; want false", got)
	}
}
