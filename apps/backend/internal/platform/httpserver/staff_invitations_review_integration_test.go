//go:build integration

// Staff invitations, review fixes of INV-01/INV-02 (spec 46): the hidden
// channel never takes a sales channel's name, a concurrent or altered replay
// of one request_id, a guest with an open purchase, the scan guard and the
// atomic cancellation, the seat-origin inconsistency and the billing of a
// fully cancelled invitation.
package httpserver

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/invitations"
)

// inv02AssertBilled checks that the invitation's order is still a paid
// order the billing collect query selects, with all its lines: the charge
// for an invitation stays when it is cancelled (owner rule).
func inv02AssertBilled(t *testing.T, f *inv02Fx, orderID uuid.UUID, lines int) {
	t.Helper()
	ctx := context.Background()
	var (
		status string
		paidAt *time.Time
	)
	if err := f.pool.QueryRow(ctx, `SELECT status, paid_at FROM orders WHERE id = $1`, orderID).Scan(&status, &paidAt); err != nil {
		t.Fatalf("read order: %v", err)
	}
	if paidAt == nil || (status != "paid" && status != "partially_refunded" && status != "refunded") {
		t.Fatalf("order %s is %s (paid_at %v): billing would skip it", orderID, status, paidAt)
	}
	got := 0
	after, afterID := *paidAt, uuid.Nil
	for page := 0; page < 20; page++ {
		items, err := gen.New(f.pool).ListCollectOrderItems(ctx, after, afterID, 500, true)
		if err != nil {
			t.Fatalf("ListCollectOrderItems: %v", err)
		}
		for _, it := range items {
			if it.OrderID != nil && *it.OrderID == orderID {
				if it.Kind != "ticket_complimentary" {
					t.Fatalf("collected kind %s, want ticket_complimentary", it.Kind)
				}
				got++
			}
		}
		if len(items) < 500 {
			break
		}
		last := items[len(items)-1]
		after, afterID = last.OccurredAt, last.SubjectID
	}
	if got != lines {
		t.Fatalf("billing collects %d lines of the cancelled invitation, want %d", got, lines)
	}
}

// issueAndMint issues one invitation and runs the worker's ticket issuance.
func (f *inv02Fx) issueAndMint(t *testing.T, body string) (uuid.UUID, []gen.TicketRow) {
	t.Helper()
	ctx := context.Background()
	code, out := f.issue(f.o.owner, body)
	if code != http.StatusCreated {
		t.Fatalf("issue: %d %s", code, out["_raw"])
	}
	orderID := uuid.MustParse(out["order_id"].(string))
	var csID uuid.UUID
	if err := f.pool.QueryRow(ctx, `SELECT checkout_session_id FROM orders WHERE id = $1`, orderID).Scan(&csID); err != nil {
		t.Fatalf("checkout session id: %v", err)
	}
	cs, err := gen.New(f.pool).GetCheckoutSessionByID(ctx, csID)
	if err != nil {
		t.Fatalf("checkout session: %v", err)
	}
	tickets, err := f.srv.ticketsHandler().IssueTicketsForCheckout(ctx, cs)
	if err != nil || len(tickets) == 0 {
		t.Fatalf("IssueTicketsForCheckout: %d tickets, %v", len(tickets), err)
	}
	return orderID, tickets
}

func (f *inv02Fx) cancelPath(orderID uuid.UUID) string {
	return "/v1/organizations/" + f.o.org.String() + "/invitations/" + orderID.String() + "/cancel"
}

func (f *inv02Fx) ticketStatus(t *testing.T, id uuid.UUID) string {
	t.Helper()
	var st string
	if err := f.pool.QueryRow(context.Background(), `SELECT status FROM tickets WHERE id = $1`, id).Scan(&st); err != nil {
		t.Fatalf("ticket status: %v", err)
	}
	return st
}

// Finding 1: the hidden "Invitations" channel and a sales channel of the
// same name live side by side, in either order, and a sales name is still
// unique among sales channels (the 409 channel.duplicate mapping holds).
func TestINV02Review_InternalChannelNeverCollidesWithASalesChannelName(t *testing.T) {
	f := newINV02Fx(t, 30*24*time.Hour)
	ctx := context.Background()
	q := gen.New(f.pool)

	// An organization that already sells through "Invitations" gets its
	// hidden channel all the same.
	suffix := uuid.NewString()[:8]
	org, err := q.InsertOrganization(ctx, "INV02R Org "+suffix, "inv02r-"+suffix, "CZ", "en", 1200)
	if err != nil {
		t.Fatalf("InsertOrganization: %v", err)
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = f.pool.Exec(c, `DELETE FROM sales_channels WHERE org_id = $1`, org.ID)
		_, _ = f.pool.Exec(c, `DELETE FROM organizations WHERE id = $1`, org.ID)
	})
	if _, err := q.InsertSalesChannel(ctx, org.ID, invitations.InternalChannelName, "direct_merchant", "stripe", nil, "0", nil, nil); err != nil {
		t.Fatalf("sales channel named Invitations: %v", err)
	}
	if _, err := invitations.EnsureInternalChannel(ctx, q, org.ID); err != nil {
		t.Fatalf("EnsureInternalChannel next to a sales channel named Invitations: %v", err)
	}

	// The fixture organization already has its hidden channel: renaming a
	// sales channel to "Invitations" succeeds and leaks nothing.
	chPath := "/v1/organizations/" + f.o.org.String() + "/channels"
	code, out := f.do(f.o.owner, http.MethodPatch, chPath+"/"+f.o.sales.String(), `{"name":"Invitations"}`)
	if code != http.StatusOK {
		t.Fatalf("rename sales channel to Invitations: %d %s", code, out["_raw"])
	}
	// A second SALES channel of that name is still a duplicate.
	code, out = f.do(f.o.owner, http.MethodPost, chPath,
		`{"name":"Invitations","payment_mode":"direct_merchant","provider":"stripe","provider_account_id":"acct_inv02review","fee_percent":"0"}`)
	if code != http.StatusConflict || !strings.Contains(out["_raw"].(string), "channel.duplicate") {
		t.Fatalf("second sales channel named Invitations: %d %s", code, out["_raw"])
	}
	// Invitations keep being issued on the hidden channel.
	code, out = f.issue(f.o.owner, inv02Body(uuid.New(), []string{"A|1|6"}, nil, 0, "pdf_only", ""))
	if code != http.StatusCreated {
		t.Fatalf("issue after the rename: %d %s", code, out["_raw"])
	}
	var channel uuid.UUID
	if err := f.pool.QueryRow(ctx, `SELECT channel_id FROM orders WHERE id = $1`, uuid.MustParse(out["order_id"].(string))).Scan(&channel); err != nil || channel != f.o.internal {
		t.Fatalf("invitation channel %s (%v), want the internal one", channel, err)
	}
}

// Finding 2: N identical seated requests at once end with ONE order and
// every answer names it; none of them finds its own seats taken.
func TestINV02Review_ConcurrentReplayOfOneRequest_OneOrder(t *testing.T) {
	f := newINV02Fx(t, 30*24*time.Hour)
	ctx := context.Background()
	rid := uuid.New()
	body := strings.ReplaceAll(inv02Body(rid, []string{"A|1|2", "A|1|6"}, &f.gaTier, 1, "pdf_only", ""), inv02PhonePlaceholder, f.phone)

	const n = 8
	type answer struct {
		code  int
		order string
		raw   string
	}
	answers := make([]answer, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.ts.URL+f.issuePath(), strings.NewReader(body))
			if err != nil {
				answers[i] = answer{raw: err.Error()}
				return
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+f.o.owner)
			resp, err := f.ts.Client().Do(req)
			if err != nil {
				answers[i] = answer{raw: err.Error()}
				return
			}
			defer func() { _ = resp.Body.Close() }()
			raw, _ := io.ReadAll(resp.Body)
			var out struct {
				OrderID string `json:"order_id"`
			}
			_ = json.Unmarshal(raw, &out)
			answers[i] = answer{code: resp.StatusCode, order: out.OrderID, raw: string(raw)}
		}(i)
	}
	close(start)
	wg.Wait()

	created := 0
	for i, a := range answers {
		if a.code != http.StatusCreated && a.code != http.StatusOK {
			t.Fatalf("answer %d: %d %s", i, a.code, a.raw)
		}
		if a.code == http.StatusCreated {
			created++
		}
		if a.order == "" || a.order != answers[0].order {
			t.Fatalf("answer %d names order %q, answer 0 names %q", i, a.order, answers[0].order)
		}
	}
	if created != 1 {
		t.Fatalf("%d answers were 201, want exactly one", created)
	}
	var orders int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM orders WHERE org_id = $1 AND external_ref = $2`, f.o.org, "inv:"+rid.String()).Scan(&orders); err != nil || orders != 1 {
		t.Fatalf("orders for the request = %d (%v), want 1", orders, err)
	}
}

// Finding 3: the same request_id with a different body is refused, never
// answered with the first invitation.
func TestINV02Review_ReplayWithADifferentBody_Refused(t *testing.T) {
	f := newINV02Fx(t, 30*24*time.Hour)
	rid := uuid.New()
	code, first := f.issue(f.o.owner, inv02Body(rid, []string{"A|1|4"}, nil, 0, "pdf_only", ""))
	if code != http.StatusCreated {
		t.Fatalf("first issue: %d %s", code, first["_raw"])
	}
	// The seat list in another order is the same request.
	code, again := f.issue(f.o.owner, inv02Body(rid, []string{"A|1|4"}, nil, 0, "pdf_only", ""))
	if code != http.StatusOK || again["order_id"] != first["order_id"] {
		t.Fatalf("identical replay: %d %s", code, again["_raw"])
	}
	for name, body := range map[string]string{
		"other seat":     inv02Body(rid, []string{"A|1|5"}, nil, 0, "pdf_only", ""),
		"extra place":    inv02Body(rid, []string{"A|1|4"}, &f.gaTier, 1, "pdf_only", ""),
		"other delivery": inv02Body(rid, []string{"A|1|4"}, nil, 0, "guest_email", "x-"+uuid.NewString()[:8]+"@example.test"),
		"other guest":    strings.Replace(inv02Body(rid, []string{"A|1|4"}, nil, 0, "pdf_only", ""), "Ana López", "Eva Ruiz", 1),
	} {
		code, out := f.issue(f.o.owner, body)
		if code != http.StatusConflict || !strings.Contains(out["_raw"].(string), "invitation.request_id_reused") {
			t.Fatalf("%s: %d %s, want 409 invitation.request_id_reused", name, code, out["_raw"])
		}
	}
	if s := f.seat("A|1|5"); s.Status != "available" {
		t.Fatalf("a refused replay took A|1|5: %s", s.Status)
	}
}

// Finding 7: a guest who has an open purchase of their own on the session
// still receives an invitation by e-mail.
func TestINV02Review_GuestWithAnOpenPurchase_GetsTheInvitation(t *testing.T) {
	f := newINV02Fx(t, 30*24*time.Hour)
	ctx := context.Background()
	email := "guest-" + uuid.NewString()[:8] + "@example.test"

	code, out := f.issue(f.o.owner, inv02Body(uuid.New(), []string{"A|1|4"}, nil, 0, "guest_email", email))
	if code != http.StatusCreated {
		t.Fatalf("first invitation: %d %s", code, out["_raw"])
	}
	firstID := uuid.MustParse(out["order_id"].(string))
	var customer *uuid.UUID
	if err := f.pool.QueryRow(ctx, `SELECT customer_id FROM orders WHERE id = $1`, firstID).Scan(&customer); err != nil || customer == nil {
		t.Fatalf("the paid invitation is not linked to the guest: %v %v", customer, err)
	}
	// The same customer now holds a pending purchase on this session.
	if _, err := f.pool.Exec(ctx, `UPDATE orders SET status = 'pending_payment' WHERE id = $1`, firstID); err != nil {
		t.Fatalf("open purchase: %v", err)
	}

	code, out = f.issue(f.o.owner, inv02Body(uuid.New(), []string{"A|1|5"}, nil, 0, "guest_email", email))
	if code != http.StatusCreated {
		t.Fatalf("invitation for a guest with an open purchase: %d %s", code, out["_raw"])
	}
	var (
		status string
		linked *uuid.UUID
	)
	if err := f.pool.QueryRow(ctx, `SELECT status, customer_id FROM orders WHERE id = $1`, uuid.MustParse(out["order_id"].(string))).Scan(&status, &linked); err != nil {
		t.Fatalf("read order: %v", err)
	}
	if status != "paid" || linked == nil || *linked != *customer {
		t.Fatalf("second invitation: status %s customer %v, want paid and %s", status, linked, *customer)
	}
}

// Findings 5 and 6: a ticket already used at the door is never cancelled,
// and the refusal cancels nothing else of the invitation either.
func TestINV02Review_CancelRefusesAScannedTicket_NothingCancelled(t *testing.T) {
	f := newINV02Fx(t, 30*24*time.Hour)
	ctx := context.Background()
	orderID, tickets := f.issueAndMint(t, inv02Body(uuid.New(), []string{"A|1|1", "A|1|4"}, nil, 0, "pdf_only", ""))
	if len(tickets) != 2 {
		t.Fatalf("tickets = %d, want 2", len(tickets))
	}
	scanned, other := tickets[0], tickets[1]
	if _, err := f.pool.Exec(ctx, `UPDATE tickets SET used_at = now() WHERE id = $1`, scanned.ID); err != nil {
		t.Fatalf("scan: %v", err)
	}

	code, out := f.do(f.manager, http.MethodPost, f.cancelPath(orderID), `{}`)
	if code != http.StatusConflict || !strings.Contains(out["_raw"].(string), "invitation.ticket_scanned") ||
		!strings.Contains(out["_raw"].(string), scanned.ID.String()) {
		t.Fatalf("cancel with a scanned ticket: %d %s", code, out["_raw"])
	}
	for _, tk := range tickets {
		if st := f.ticketStatus(t, tk.ID); st != "active" {
			t.Fatalf("ticket %s is %s after a refused cancel", tk.ID, st)
		}
	}
	for _, k := range []string{"A|1|1", "A|1|4"} {
		if s := f.seat(k); s.Status != "sold" {
			t.Fatalf("%s is %s after a refused cancel", k, s.Status)
		}
	}
	var events int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM order_events WHERE order_id = $1 AND type = 'invitation_cancelled'`, orderID).Scan(&events); err != nil || events != 0 {
		t.Fatalf("order_events after a refused cancel = %d (%v)", events, err)
	}

	// The ticket that was not scanned can still be cancelled on its own.
	code, out = f.do(f.manager, http.MethodPost, f.cancelPath(orderID), `{"ticket_ids":["`+other.ID.String()+`"]}`)
	if code != http.StatusOK || len(out["cancelled_ticket_ids"].([]any)) != 1 {
		t.Fatalf("cancel the unscanned ticket: %d %s", code, out["_raw"])
	}
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM order_events WHERE order_id = $1 AND type = 'invitation_cancelled'`, orderID).Scan(&events); err != nil || events != 1 {
		t.Fatalf("order_events after the cancel = %d (%v), want 1", events, err)
	}
	if st := f.ticketStatus(t, scanned.ID); st != "active" {
		t.Fatalf("the scanned ticket is %s", st)
	}
}

// Finding 8: an invitation ticket whose order line lost its seat origin is
// an inconsistency — the cancellation aborts, the reserve seat never goes on
// sale, and once the origin is back the cancellation goes through.
func TestINV02Review_MissingSeatOriginAbortsTheCancellation(t *testing.T) {
	f := newINV02Fx(t, 30*24*time.Hour)
	ctx := context.Background()
	orderID, tickets := f.issueAndMint(t, inv02Body(uuid.New(), []string{"A|1|2"}, nil, 0, "pdf_only", ""))
	if _, err := f.pool.Exec(ctx, `UPDATE order_items SET seat_origin = NULL WHERE order_id = $1`, orderID); err != nil {
		t.Fatalf("drop origin: %v", err)
	}
	code, out := f.do(f.o.owner, http.MethodPost, f.cancelPath(orderID), `{}`)
	if code != http.StatusInternalServerError || !strings.Contains(out["_raw"].(string), "ticket.release_failed") {
		t.Fatalf("cancel without an origin: %d %s", code, out["_raw"])
	}
	if s := f.seat("A|1|2"); s.Status != "sold" {
		t.Fatalf("the reserve seat is %s after an aborted cancel, want sold", s.Status)
	}
	if st := f.ticketStatus(t, tickets[0].ID); st != "active" {
		t.Fatalf("ticket is %s after an aborted cancel", st)
	}

	if _, err := f.pool.Exec(ctx, `UPDATE order_items SET seat_origin = 'unavailable' WHERE order_id = $1`, orderID); err != nil {
		t.Fatalf("restore origin: %v", err)
	}
	code, out = f.do(f.o.owner, http.MethodPost, f.cancelPath(orderID), `{}`)
	if code != http.StatusOK {
		t.Fatalf("cancel with the origin back: %d %s", code, out["_raw"])
	}
	if s := f.seat("A|1|2"); s.Status != "unavailable" {
		t.Fatalf("the reserve seat is %s, want unavailable", s.Status)
	}
	inv02AssertBilled(t, f, orderID, 1)
}
