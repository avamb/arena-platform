//go:build integration

// Staff invitations, INV-13: the minor findings of the final review.
//
//   - the letters of an invitation link the contact form like a purchase does
//     (the internal channel has no hosted page of its own);
//   - issuing never burns an "Invitations" channel number after the first;
//   - cancelling an invitation of several tickets while the same tickets are
//     cancelled one by one cannot deadlock (session lock first);
//   - an invitation order line always records its seat origin.
package httpserver

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// The invitation order sits on the internal channel, which has no hosted
// page: the contact lookups must look at the organization's other channels,
// exactly as for a ticket without a channel, and send the guest to the
// organizer's contact form instead of printing the organization's address.
func TestINV13_ContactRouteOfAnInvitationTicket_IgnoresTheInternalChannel(t *testing.T) {
	f := newINV02Fx(t, 30*24*time.Hour)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `UPDATE sales_channels SET settings = '{"hosted_page":{"enabled":true}}'::jsonb WHERE id = $1`, f.o.sales); err != nil {
		t.Fatalf("enable the hosted page: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE organizations SET contact_email = 'organizer-private@example.com' WHERE id = $1`, f.o.org); err != nil {
		t.Fatalf("contact email: %v", err)
	}
	_, tickets := f.issueAndMint(t, inv02Body(uuid.New(), []string{"A|1|4"}, nil, 0, "pdf_only", ""))
	q := gen.New(f.pool)

	route, err := q.GetTicketContactRoute(ctx, tickets[0].ID)
	if err != nil {
		t.Fatalf("GetTicketContactRoute: %v", err)
	}
	if !route.HostedPage {
		t.Fatalf("hosted_page = false: the internal channel decided instead of the organization's channels (%+v)", route)
	}
	branding, err := q.GetOrgBrandingByTicketID(ctx, tickets[0].ID)
	if err != nil {
		t.Fatalf("GetOrgBrandingByTicketID: %v", err)
	}
	if branding.ContactEmail != nil {
		t.Fatalf("the letter would print the organizer's address %q instead of the contact form", *branding.ContactEmail)
	}
}

// Issuing reads the internal channel first: only the very first invitation
// inserts, so the channel number sequence moves once, not once per issue.
func TestINV13_IssuingDoesNotBurnChannelNumbers(t *testing.T) {
	f := newINV02Fx(t, 30*24*time.Hour)
	ctx := context.Background()
	last := func() int64 {
		var v int64
		if err := f.pool.QueryRow(ctx, `SELECT last_value FROM sales_channels_display_number_seq`).Scan(&v); err != nil {
			t.Fatalf("sequence: %v", err)
		}
		return v
	}
	if code, out := f.issue(f.o.owner, inv02Body(uuid.New(), []string{"A|1|4"}, nil, 0, "pdf_only", "")); code != http.StatusCreated {
		t.Fatalf("first issue: %d %s", code, out["_raw"])
	}
	after := last()
	for _, seat := range []string{"A|1|5", "A|1|6", "A|1|7"} {
		if code, out := f.issue(f.o.owner, inv02Body(uuid.New(), []string{seat}, nil, 0, "pdf_only", "")); code != http.StatusCreated {
			t.Fatalf("issue %s: %d %s", seat, code, out["_raw"])
		}
	}
	// Other tests may create channels in parallel packages, but not between
	// these calls on an otherwise idle sequence: allow no drift of ours.
	if got := last(); got != after {
		t.Fatalf("channel number sequence moved from %d to %d across three further invitations", after, got)
	}
}

// Cancelling a whole invitation while its tickets are cancelled one by one:
// the invitation cancel locks the session first, then ticket after ticket;
// the single cancel used to take its ticket first and the session second,
// and Postgres killed one of them with 40P01. Now both take the session
// first, so every request answers 200 or the calm "not active" 409.
func TestINV13_CancelInvitationAgainstSingleTicketCancels_NoDeadlock(t *testing.T) {
	f := newINV02Fx(t, 30*24*time.Hour)
	ctx := context.Background()
	seats := []string{"A|1|4", "A|1|5", "A|1|6", "A|1|7"}

	for round := 0; round < 8; round++ {
		orderID, tickets := f.issueAndMint(t, inv02Body(uuid.New(), seats, nil, 0, "pdf_only", ""))
		if len(tickets) != len(seats) {
			t.Fatalf("round %d: tickets = %d", round, len(tickets))
		}
		type answer struct {
			what string
			code int
			raw  string
			err  error
		}
		var (
			wg      sync.WaitGroup
			mu      sync.Mutex
			answers []answer
		)
		start := make(chan struct{})
		call := func(what, token, path, body string) {
			defer wg.Done()
			<-start
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.ts.URL+path, strings.NewReader(body))
			var a answer
			a.what = what
			if err == nil {
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Authorization", "Bearer "+token)
				req.Header.Set("X-Admin-Reason", "inv-13 integration test")
				var resp *http.Response
				if resp, err = f.ts.Client().Do(req); err == nil {
					a.code = resp.StatusCode
					a.raw = integReadBody(t, resp)
				}
			}
			a.err = err
			mu.Lock()
			answers = append(answers, a)
			mu.Unlock()
		}
		wg.Add(1 + len(tickets))
		go call("invitation", f.manager, f.cancelPath(orderID), `{}`)
		for _, tk := range tickets {
			go call("ticket "+tk.ID.String(), f.o.owner, "/v1/tickets/"+tk.ID.String()+"/cancel", `{"reason":"inv-13 race","refund_mode":"none"}`)
		}
		close(start)
		wg.Wait()

		for _, a := range answers {
			if a.err != nil {
				t.Fatalf("round %d %s: %v", round, a.what, a.err)
			}
			if a.code != http.StatusOK && a.code != http.StatusConflict {
				t.Fatalf("round %d %s answered %d: %s", round, a.what, a.code, a.raw)
			}
		}
		for _, tk := range tickets {
			if st := f.ticketStatus(t, tk.ID); st != "cancelled" {
				t.Fatalf("round %d: ticket %s is %s", round, tk.ID, st)
			}
		}
		for _, k := range seats {
			if s := f.seat(k); s.Status != "available" {
				t.Fatalf("round %d: seat %s is %s after the cancellations, want available", round, k, s.Status)
			}
		}
	}
}
