//go:build integration

// refund_scanned_round4_integration_test.go - review round 3 of the
// scanned-ticket refund block.
package httpserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/refunds"
)

// N1: cancelling a scanned ticket without money (allowed) must not open the
// way to a refund of the whole payment, which would return that ticket's
// money too: the whole-payment check counts every scanned ticket sold with
// the payment, cancelled or not.
func TestRefundScanned_CancelledScannedTicketStillBlocksTheWholePayment(t *testing.T) {
	e := newScanEnv(t, "ticket.cancel", "refund.read", "refund.create", "refund.approve")
	e.exec(t, `UPDATE tickets SET used_at = now() WHERE id = $1`, e.f.tickets[0])
	if st, out := e.pf.do(t, http.MethodPost, "/v1/tickets/"+e.f.tickets[0].String()+"/cancel", e.key,
		`{"reason":"scan4","refund_mode":"none"}`); st != http.StatusOK {
		t.Fatalf("cancel without money: %d %v; want 200", st, out)
	}
	body := fmt.Sprintf(`{"payment_intent_id":%q,"amount":5000,"currency":"EUR","reason":"whole payment"}`, e.f.pay)
	st, out := e.pf.do(t, http.MethodPost, "/v1/refunds", e.key, body)
	if st != http.StatusConflict || prom0113Code(out) != refunds.CodeTicketScanned {
		t.Fatalf("flat refund of the whole payment: %d %v; want 409 %s", st, out, refunds.CodeTicketScanned)
	}

	// A whole-payment refund already waiting is stopped at the claim too.
	var id uuid.UUID
	if err := e.srv.pgxPool.QueryRow(context.Background(), `INSERT INTO refunds (payment_intent_id, org_id, amount, currency, reason,
		state, settlement, provider, origin, cancel_ticket)
		VALUES ($1, $2, 5000, 'EUR', 'whole payment', 'provider_pending', 'provider', 'stripe', 'arena', false) RETURNING id`,
		e.f.pay, e.org).Scan(&id); err != nil {
		t.Fatal(err)
	}
	got, err := e.srv.refundEngine().Drive(context.Background(), id)
	if err != nil {
		t.Fatalf("drive: %v", err)
	}
	if got.State != "failed" || e.calls.Load() != 0 {
		t.Fatalf("whole-payment refund with a cancelled scanned ticket = %s with %d provider call(s); want failed and none", got.State, e.calls.Load())
	}
}

// A refused refund whose ticket was scanned since is not offered for retry:
// the retry route would refuse it.
func TestRefundScanned_ScannedTicketIsNotRetryable(t *testing.T) {
	e := newScanEnv(t)
	var failed string
	if err := e.srv.pgxPool.QueryRow(context.Background(), `INSERT INTO refunds (payment_intent_id, org_id, order_id, ticket_id, amount, currency, reason,
		state, settlement, provider, provider_attempts, failure_code, failed_at, cancel_ticket)
		VALUES ($1, $2, $3, $4, 2500, 'EUR', 'declined', 'failed', 'provider', 'stripe', 1, 'card_declined', now(), true)
		RETURNING id::text`, e.f.pay, e.org, e.f.order, e.f.tickets[0]).Scan(&failed); err != nil {
		t.Fatal(err)
	}
	retryable := func() any {
		t.Helper()
		st, out := e.pf.do(t, http.MethodGet, "/v1/organizations/"+e.org.String()+"/refunds/"+failed, e.key, "")
		if st != http.StatusOK {
			t.Fatalf("get: %d %v", st, out)
		}
		r, _ := out["refund"].(map[string]any)
		if r == nil {
			r = out
		}
		return r["retryable"]
	}
	if got := retryable(); got != true {
		t.Fatalf("before the scan: retryable %v; want true", got)
	}
	e.exec(t, `UPDATE tickets SET used_at = now() WHERE id = $1`, e.f.tickets[0])
	if got := retryable(); got != false {
		t.Fatalf("after the scan: retryable %v; want false", got)
	}
}

// N2: an exception spent by a refund the provider refused moved no money; the
// retry of that refund inherits it instead of being refused as scanned (and
// telling the owners that someone tried).
func TestRefundScanned_RetryInheritsTheExceptionOfARefusedRefund(t *testing.T) {
	e := newScanEnv(t)
	ctx := context.Background()
	e.exec(t, `UPDATE tickets SET used_at = now() WHERE id = $1`, e.f.tickets[0])
	if _, err := e.srv.refundEngine().GrantScanException(ctx, refunds.ScanExceptionInput{OrgID: e.org, TicketID: e.f.tickets[0],
		Actor: refunds.Actor{Type: "user", ID: uuid.NewString()}, Reason: "show cancelled"}); err != nil {
		t.Fatal(err)
	}
	declined := true
	e.stub.refundHandler = func(w http.ResponseWriter, _ *http.Request) {
		e.calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if declined {
			w.WriteHeader(http.StatusPaymentRequired)
			_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","code":"insufficient_funds","message":"balance too low"}}`))
			return
		}
		_, _ = fmt.Fprintf(w, `{"id":"re_%s","status":"succeeded"}`, uuid.NewString()[:8])
	}
	st, out := e.createRefund(t, 0)
	if st != http.StatusAccepted {
		t.Fatalf("create on the exception: %d %v", st, out)
	}
	first := out["refunds"].([]any)[0].(map[string]any)
	if first["state"] != "failed" {
		t.Fatalf("first refund = %v; want failed by the provider", first["state"])
	}
	if st, q := e.pf.do(t, http.MethodGet, "/v1/organizations/"+e.org.String()+"/refunds/"+first["id"].(string), e.key, ""); st != http.StatusOK ||
		q["refund"].(map[string]any)["retryable"] != true {
		t.Fatalf("the refused refund on an exception is not offered for retry: %d %v", st, q)
	}
	declined = false
	st, out = refundOrgPost(t, e.pf, "/v1/organizations/"+e.org.String()+"/refunds/"+first["id"].(string)+"/retry", e.key, "", `{}`)
	if st != http.StatusAccepted {
		t.Fatalf("retry of the refused refund: %d %v; want 202", st, out)
	}
	if got := out["refunds"].([]any)[0].(map[string]any)["state"]; got != "succeeded" {
		t.Fatalf("retried refund = %v; want succeeded", got)
	}
	if n := e.count(t, `SELECT count(*) FROM refund_scan_blocks WHERE ticket_id = $1`, e.f.tickets[0]); n != 0 {
		t.Fatalf("the retry was journaled as a blocked attempt (%d rows)", n)
	}
}

func (e *scanEnv) grantException(t *testing.T, i int) {
	t.Helper()
	if _, err := e.srv.refundEngine().GrantScanException(context.Background(), refunds.ScanExceptionInput{OrgID: e.org,
		TicketID: e.f.tickets[i], Actor: refunds.Actor{Type: "user", ID: uuid.NewString()}, Reason: "show cancelled"}); err != nil {
		t.Fatal(err)
	}
}

func (e *scanEnv) exceptionUsedBy(t *testing.T, i int) string {
	t.Helper()
	var by string
	if err := e.srv.pgxPool.QueryRow(context.Background(), `SELECT COALESCE(used_by_refund::text, CASE WHEN used_at IS NULL THEN '' ELSE 'spent' END)
		FROM refund_scan_exceptions WHERE ticket_id = $1`, e.f.tickets[i]).Scan(&by); err != nil {
		t.Fatal(err)
	}
	return by
}

// A refund of the per-ticket route spends the exception in the transaction
// that writes it, so an approval after the exception's expiry still goes
// through (it was the refund's, not a pending promise).
func TestRefundScanned_TicketRefundSpendsTheExceptionWhenWritten(t *testing.T) {
	e := newScanEnv(t, "refund.read", "refund.create", "refund.approve")
	creator := e.pf.key(t, e.org, "refund.read", "refund.create")
	e.dropExceptionsOnCleanup(t)
	e.exec(t, `UPDATE tickets SET used_at = now() - interval '1 hour' WHERE id = $1`, e.f.tickets[0])
	e.grantException(t, 0)
	body := fmt.Sprintf(`{"items":[{"ticket_id":%q}],"reason":"show cancelled"}`, e.f.tickets[0])
	st, out := refundOrgPost(t, e.pf, "/v1/organizations/"+e.org.String()+"/orders/"+e.f.order.String()+"/refunds", creator, "scan5-"+uuid.NewString(), body)
	if st != http.StatusAccepted {
		t.Fatalf("ticket refund on the exception: %d %v", st, out)
	}
	id := out["refunds"].([]any)[0].(map[string]any)["id"].(string)
	if by := e.exceptionUsedBy(t, 0); by != id {
		t.Fatalf("exception used by %q; want the new refund %s", by, id)
	}
	e.exec(t, `UPDATE refund_scan_exceptions SET expires_at = now() - interval '1 minute', created_at = now() - interval '2 minutes' WHERE ticket_id = $1`, e.f.tickets[0])
	st, out = e.pf.do(t, http.MethodPost, "/v1/refunds/"+id+"/approve", e.key, "")
	if st != http.StatusOK || out["refund"].(map[string]any)["state"] != "succeeded" {
		t.Fatalf("approval after the exception's expiry: %d %v; want 200 succeeded", st, out)
	}
}

// The ticket-cancel route's automatic refund never takes an exception: the
// cancellation is refused before anything is cancelled, the exception stays
// unspent for the per-ticket route, and no refund row is written.
func TestRefundScanned_AutomaticCancelRefusesAScannedTicketWithAnException(t *testing.T) {
	e := newScanEnv(t, "ticket.cancel", "refund.read")
	e.dropExceptionsOnCleanup(t)
	e.exec(t, `UPDATE tickets SET used_at = now() WHERE id = $1`, e.f.tickets[0])
	e.grantException(t, 0)
	st, out := e.pf.do(t, http.MethodPost, "/v1/tickets/"+e.f.tickets[0].String()+"/cancel", e.key,
		`{"reason":"show cancelled","refund_mode":"automatic","refund_amount":2500}`)
	if st != http.StatusConflict || prom0113Code(out) != refunds.CodeTicketScanned {
		t.Fatalf("automatic cancel on the exception: %d %v; want 409 %s", st, out, refunds.CodeTicketScanned)
	}
	if by := e.exceptionUsedBy(t, 0); by != "" {
		t.Fatalf("the refused cancellation spent the exception (%q)", by)
	}
	if n := e.count(t, `SELECT count(*) FROM refunds WHERE cancelled_ticket_id = $1`, e.f.tickets[0]); n != 0 {
		t.Fatalf("a refund was written for the refused cancellation (%d rows)", n)
	}
	if n := e.count(t, `SELECT count(*) FROM tickets WHERE id = $1 AND status = 'active'`, e.f.tickets[0]); n != 1 {
		t.Fatal("the refused cancellation cancelled the ticket")
	}
}

// N4: a manual cancellation the handler refuses (here an unknown field) does
// not burn the exception.
func TestRefundScanned_RefusedManualCancelKeepsTheException(t *testing.T) {
	e := newScanEnv(t, "ticket.cancel", "refund.read")
	e.exec(t, `UPDATE tickets SET used_at = now() WHERE id = $1`, e.f.tickets[0])
	e.grantException(t, 0)
	st, out := e.pf.do(t, http.MethodPost, "/v1/tickets/"+e.f.tickets[0].String()+"/cancel", e.key,
		`{"reason":"show cancelled","refund_mode":"manual","surprise":1}`)
	if st != http.StatusBadRequest {
		t.Fatalf("cancel with an unknown field: %d %v; want 400", st, out)
	}
	if by := e.exceptionUsedBy(t, 0); by != "" {
		t.Fatalf("a refused cancellation spent the exception (%q)", by)
	}
	st, out = e.pf.do(t, http.MethodPost, "/v1/tickets/"+e.f.tickets[0].String()+"/cancel", e.key,
		`{"reason":"show cancelled","refund_mode":"manual"}`)
	if st != http.StatusOK || e.exceptionUsedBy(t, 0) != "spent" {
		t.Fatalf("manual cancel on the exception: %d %v, exception %q; want 200 and spent", st, out, e.exceptionUsedBy(t, 0))
	}
}

// N5: anything after the JSON object is refused, so the scan check and the
// handler can never read two different requests out of one body.
func TestRefundScanned_CancelBodyWithTrailingDataIsRefused(t *testing.T) {
	e := newScanEnv(t, "ticket.cancel", "refund.read")
	e.exec(t, `UPDATE tickets SET used_at = now() WHERE id = $1`, e.f.tickets[0])
	st, out := e.pf.do(t, http.MethodPost, "/v1/tickets/"+e.f.tickets[0].String()+"/cancel", e.key,
		`{"reason":"r","refund_mode":"automatic","refund_amount":2500} x`)
	if st != http.StatusBadRequest {
		t.Fatalf("cancel body with trailing data: %d %v; want 400", st, out)
	}
	if got, _ := e.f.ticket(t, 0); got != "active" {
		t.Fatalf("ticket is %q; want active", got)
	}
}

// N7: an exception covers the scans made before it was granted (a show
// stopped half-way, a scan made by mistake). A scan after the grant is a real
// entry, and no refund goes through on the exception any more.
func TestRefundScanned_ScanAfterTheGrantVoidsTheException(t *testing.T) {
	e := newScanEnv(t)
	e.exec(t, `UPDATE tickets SET used_at = now() - interval '1 hour' WHERE id = $1`, e.f.tickets[0])
	e.grantException(t, 0)
	e.scanEvent(t, e.org, 0)
	st, out := e.createRefund(t, 0)
	if st != http.StatusConflict || prom0113Code(out) != refunds.CodeTicketScanned {
		t.Fatalf("refund after a scan that followed the exception: %d %v; want 409 %s", st, out, refunds.CodeTicketScanned)
	}
}

// N8: a grant whose answer was lost can be sent again by the same operator
// and answers the same exception (no second notice); one that expired unused
// can be granted again; a spent one cannot.
func TestRefundScanned_ExceptionGrantIsIdempotentAndRenewable(t *testing.T) {
	e := newScanEnv(t)
	ctx := context.Background()
	e.exec(t, `UPDATE tickets SET used_at = now() - interval '1 hour' WHERE id = $1`, e.f.tickets[0])
	actor := refunds.Actor{Type: "user", ID: uuid.NewString()}
	grant := func() (refunds.ScanException, error) {
		return e.srv.refundEngine().GrantScanException(ctx, refunds.ScanExceptionInput{OrgID: e.org, TicketID: e.f.tickets[0],
			Actor: actor, Reason: "show cancelled"})
	}
	notices := func() int {
		return e.count(t, `SELECT count(*) FROM outbox_events WHERE event_type = $1 AND aggregate_id = $2`,
			refunds.EventScanExceptionGranted, e.f.tickets[0].String())
	}
	first, err := grant()
	if err != nil {
		t.Fatal(err)
	}
	again, err := grant()
	if err != nil || again.ID != first.ID || !again.Replayed || notices() != 1 {
		t.Fatalf("repeated grant: %+v err=%v notices=%d; want the same exception, replayed, one notice", again, err, notices())
	}
	e.exec(t, `UPDATE refund_scan_exceptions SET created_at = now() - interval '3 hours', expires_at = now() - interval '1 hour' WHERE ticket_id = $1`, e.f.tickets[0])
	renewed, err := grant()
	if err != nil || renewed.Replayed || !renewed.ExpiresAt.After(time.Now()) || notices() != 2 {
		t.Fatalf("grant after an unused exception expired: %+v err=%v notices=%d; want a fresh one and a second notice", renewed, err, notices())
	}
	e.exec(t, `UPDATE refund_scan_exceptions SET used_at = now() WHERE ticket_id = $1`, e.f.tickets[0])
	var refErr *refunds.Error
	if _, err := grant(); !errors.As(err, &refErr) || refErr.Code != refunds.CodeScanExceptionExists {
		t.Fatalf("grant after the exception was spent: %v; want %s", err, refunds.CodeScanExceptionExists)
	}
}

// An exception that expired unused lets nothing through any more.
func TestRefundScanned_ExpiredExceptionLetsNothingThrough(t *testing.T) {
	e := newScanEnv(t)
	e.exec(t, `UPDATE tickets SET used_at = now() - interval '1 hour' WHERE id = $1`, e.f.tickets[0])
	e.grantException(t, 0)
	e.exec(t, `UPDATE refund_scan_exceptions SET created_at = now() - interval '3 hours', expires_at = now() - interval '1 minute' WHERE ticket_id = $1`, e.f.tickets[0])
	e.exec(t, `UPDATE tickets SET used_at = now() - interval '4 hours' WHERE id = $1`, e.f.tickets[0])
	st, out := e.createRefund(t, 0)
	if st != http.StatusConflict || prom0113Code(out) != refunds.CodeTicketScanned {
		t.Fatalf("refund on an expired exception: %d %v; want 409 %s", st, out, refunds.CodeTicketScanned)
	}
	if e.calls.Load() != 0 {
		t.Fatalf("the provider was called %d time(s)", e.calls.Load())
	}
}
