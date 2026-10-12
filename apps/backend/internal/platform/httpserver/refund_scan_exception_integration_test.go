//go:build integration

package httpserver

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/refunds"
)

// A platform superadmin's exception lets exactly one refund of a scanned
// ticket through (owner decision 2026-10-13): without it the refund is 409,
// with it the first refund passes and the next one is refused again; only a
// superadmin with X-Admin-Reason may grant it, only for a ticket of the path
// organization, once per ticket; the grant is audited and the owners and the
// sales bot are told.
func TestScanException_SuperadminOnlyInsideTheOrganization(t *testing.T) {
	stub := newStubStripe(t)
	stub.sessionPI = "pi_scanexc_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
	stub.refundHandler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":"re_%s","status":"succeeded"}`, uuid.NewString()[:8])
	}
	e := newSec1EnvCfg(t, func(c *config.Config) { c.StripeAPIBaseURL = stub.baseURL() })
	org, other := e.org(t, "SCANEXC"), e.org(t, "SCANEXCOTHER")
	_, super := e.superadmin(t)
	owner := e.user(t, "scanexc-owner")
	e.member(t, owner, org, "org_admin")
	ownerJWT := e.token(t, owner)
	f := newPay03Order(t, e.srv.pgxPool, org, "stripe")
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := e.srv.pgxPool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = e.srv.pgxPool.Exec(c, `DELETE FROM refund_scan_exceptions WHERE org_id = $1`, org)
		_, _ = e.srv.pgxPool.Exec(c, `DELETE FROM refund_scan_blocks WHERE org_id = $1`, org)
		_, _ = e.srv.pgxPool.Exec(c, `DELETE FROM worker_jobs WHERE payload->>'org_id' = $1`, org.String())
		_, _ = e.srv.pgxPool.Exec(c, `DELETE FROM outbox_events WHERE payload->>'org_id' = $1`, org.String())
	})
	exec(`UPDATE tickets SET used_at = now() WHERE id = $1`, f.tickets[0])

	refund := func(amount int) (int, string) {
		t.Helper()
		body := fmt.Sprintf(`{"items":[{"ticket_id":%q,"amount":%d}],"reason":"show stopped","cancel_tickets":false}`, f.tickets[0], amount)
		req, _ := http.NewRequest(http.MethodPost, e.ts.URL+"/v1/organizations/"+org.String()+"/orders/"+f.order.String()+"/refunds", bytes.NewReader([]byte(body)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+ownerJWT)
		req.Header.Set("Idempotency-Key", "scanexc-"+uuid.NewString())
		resp, err := e.ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(raw)
	}

	// ── without an exception: refused
	if st, out := refund(1000); st != http.StatusConflict || sec1Code(out) != refunds.CodeTicketScanned {
		t.Fatalf("refund without an exception: %d %s; want 409 %s", st, out, refunds.CodeTicketScanned)
	}

	path := "/v1/organizations/" + org.String() + "/tickets/" + f.tickets[0].String() + "/scan-refund-exception"
	body := `{"note":"the show was stopped after the first act","hours":2}`
	if out := e.expect(t, http.StatusForbidden, http.MethodPost, path, ownerJWT, "", body); sec1Code(out) != "refund.scan_exception_superadmin_only" {
		t.Errorf("owner: code %q", sec1Code(out))
	}
	if out := e.expect(t, http.StatusBadRequest, http.MethodPost, path, super, "", body); sec1Code(out) != "superadmin.missing_reason" {
		t.Errorf("no reason: code %q", sec1Code(out))
	}
	foreign := "/v1/organizations/" + other.String() + "/tickets/" + f.tickets[0].String() + "/scan-refund-exception"
	e.expect(t, http.StatusNotFound, http.MethodPost, foreign, super, "support ticket 7", body)
	unscanned := "/v1/organizations/" + org.String() + "/tickets/" + f.tickets[1].String() + "/scan-refund-exception"
	if out := e.expect(t, http.StatusConflict, http.MethodPost, unscanned, super, "support ticket 7", body); sec1Code(out) != refunds.CodeTicketNotScanned {
		t.Errorf("unscanned ticket: code %q", sec1Code(out))
	}

	e.expect(t, http.StatusCreated, http.MethodPost, path, super, "support ticket 7", body)
	// The same operator sending it again (the first answer was lost) gets the
	// same exception back, and nothing is announced twice.
	if out := e.expect(t, http.StatusOK, http.MethodPost, path, super, "support ticket 7", body); !strings.Contains(out, `"replayed":true`) {
		t.Errorf("second grant by the same operator: %s; want the same exception, replayed", out)
	}
	var audited, mailed, announced int
	_ = e.srv.pgxPool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE action = 'v1.refund.scan_exception_grant' AND resource_id = $1`, f.tickets[0].String()).Scan(&audited)
	_ = e.srv.pgxPool.QueryRow(ctx, `SELECT count(*) FROM worker_jobs WHERE job_type = $1 AND payload->>'kind' = 'exception' AND payload->>'ticket_id' = $2`,
		refunds.JobTypeScannedAlertEmail, f.tickets[0].String()).Scan(&mailed)
	_ = e.srv.pgxPool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE event_type = $1 AND aggregate_id = $2`,
		refunds.EventScanExceptionGranted, f.tickets[0].String()).Scan(&announced)
	if audited != 1 || mailed != 1 || announced != 1 {
		t.Fatalf("grant: audit %d, owner e-mail %d, sales-bot event %d; want 1 each", audited, mailed, announced)
	}
	// Round 4, N6: the operator's X-Admin-Reason stays in the audit trail;
	// the owners and the organization's group get the note, nothing else.
	var jobPayload, eventPayload string
	_ = e.srv.pgxPool.QueryRow(ctx, `SELECT payload::text FROM worker_jobs WHERE job_type = $1 AND payload->>'kind' = 'exception' AND payload->>'ticket_id' = $2`,
		refunds.JobTypeScannedAlertEmail, f.tickets[0].String()).Scan(&jobPayload)
	_ = e.srv.pgxPool.QueryRow(ctx, `SELECT payload::text FROM outbox_events WHERE event_type = $1 AND aggregate_id = $2`,
		refunds.EventScanExceptionGranted, f.tickets[0].String()).Scan(&eventPayload)
	for name, p := range map[string]string{"owner e-mail job": jobPayload, "sales-bot event": eventPayload} {
		if strings.Contains(p, "support ticket 7") || !strings.Contains(p, "the show was stopped after the first act") {
			t.Errorf("%s payload carries the admin reason or lacks the note: %s", name, p)
		}
	}
	var auditedReason string
	_ = e.srv.pgxPool.QueryRow(ctx, `SELECT metadata->>'reason' FROM audit_events WHERE action = 'v1.refund.scan_exception_grant' AND resource_id = $1`,
		f.tickets[0].String()).Scan(&auditedReason)
	if auditedReason != "support ticket 7" {
		t.Errorf("audited reason = %q; want the X-Admin-Reason", auditedReason)
	}

	// ── with it: one refund passes, the next is refused again
	if st, out := refund(1000); st != http.StatusAccepted || !strings.Contains(out, `"succeeded"`) {
		t.Fatalf("refund with the exception: %d %s; want 202 and succeeded", st, out)
	}
	if st, out := refund(500); st != http.StatusConflict || sec1Code(out) != refunds.CodeTicketScanned {
		t.Fatalf("second refund after the exception was spent: %d %s; want 409 %s", st, out, refunds.CodeTicketScanned)
	}
}
