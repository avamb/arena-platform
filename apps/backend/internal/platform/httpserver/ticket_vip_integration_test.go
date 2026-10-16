//go:build integration

package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

// V1: the organizer's private VIP mark of a ticket. Owner and manager change
// it, an agent cannot, a ticket of another organization is the route's own
// 404, a repeated call writes neither a row nor an audit event, and the
// order card carries the mark.
func TestTicketVIP_StaysInsideTheOrganization(t *testing.T) {
	e := newSec1Env(t)
	org, other := e.org(t, "VIP"), e.org(t, "VIPOTHER")
	owner, manager, agent, stranger := e.user(t, "vip-owner"), e.user(t, "vip-manager"), e.user(t, "vip-agent"), e.user(t, "vip-stranger")
	e.member(t, owner, org, "org_admin")
	e.member(t, manager, org, "organizer")
	e.member(t, agent, org, "agent")
	e.member(t, stranger, other, "org_admin")
	f := newPay03Order(t, e.srv.pgxPool, org, "stripe")
	ctx := context.Background()
	ticket := f.tickets[0].String()
	path := "/v1/organizations/" + org.String() + "/tickets/" + ticket + "/vip"

	vip := func() (bool, bool) {
		var on, hasStamp bool
		if err := e.srv.pgxPool.QueryRow(ctx, `SELECT vip, vip_set_by IS NOT NULL AND vip_set_at IS NOT NULL FROM tickets WHERE id = $1`,
			f.tickets[0]).Scan(&on, &hasStamp); err != nil {
			t.Fatal(err)
		}
		return on, hasStamp
	}
	audits := func() int {
		var n int
		_ = e.srv.pgxPool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE action = 'v1.ticket.vip_set' AND resource_id = $1`, ticket).Scan(&n)
		return n
	}

	// An agent holds no ticket.update, a stranger is outside the organization,
	// and a body without a boolean is refused.
	e.expect(t, http.StatusForbidden, http.MethodPatch, path, e.token(t, agent), "", `{"vip":true}`)
	e.expect(t, http.StatusForbidden, http.MethodPatch, path, e.token(t, stranger), "", `{"vip":true}`)
	e.expect(t, http.StatusBadRequest, http.MethodPatch, path, e.token(t, owner), "", `{}`)
	e.expect(t, http.StatusBadRequest, http.MethodPatch, path, e.token(t, owner), "", `{"vip":"yes"}`)
	if on, _ := vip(); on {
		t.Fatal("a refused call changed the mark")
	}

	// The owner marks it; the same call again changes nothing.
	out := e.expect(t, http.StatusOK, http.MethodPatch, path, e.token(t, owner), "", `{"vip":true}`)
	var got struct {
		VIP     bool `json:"vip"`
		Changed bool `json:"changed"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil || !got.VIP || !got.Changed {
		t.Fatalf("first call: %s (%v)", out, err)
	}
	if on, stamp := vip(); !on || !stamp {
		t.Fatalf("vip = %v stamped = %v; want marked and stamped", on, stamp)
	}
	out = e.expect(t, http.StatusOK, http.MethodPatch, path, e.token(t, owner), "", `{"vip":true}`)
	if err := json.Unmarshal([]byte(out), &got); err != nil || !got.VIP || got.Changed {
		t.Fatalf("repeat: %s (%v); want changed=false", out, err)
	}
	if n := audits(); n != 1 {
		t.Errorf("audit rows = %d after a change and a repeat; want exactly 1", n)
	}

	// The order card carries the mark.
	card := e.expect(t, http.StatusOK, http.MethodGet, "/v1/organizations/"+org.String()+"/orders/"+f.order.String(), e.token(t, owner), "", "")
	var detail struct {
		Tickets []struct {
			ID  string `json:"id"`
			VIP bool   `json:"vip"`
		} `json:"tickets"`
	}
	if err := json.Unmarshal([]byte(card), &detail); err != nil {
		t.Fatal(err)
	}
	marked := 0
	for _, tk := range detail.Tickets {
		if tk.VIP {
			marked++
			if tk.ID != ticket {
				t.Errorf("ticket %s is marked, want %s", tk.ID, ticket)
			}
		}
	}
	if marked != 1 || len(detail.Tickets) != 2 {
		t.Errorf("card shows %d marked of %d tickets; want 1 of 2", marked, len(detail.Tickets))
	}

	// The manager takes it off.
	e.expect(t, http.StatusOK, http.MethodPatch, path, e.token(t, manager), "", `{"vip":false}`)
	if on, _ := vip(); on {
		t.Error("the manager could not take the mark off")
	}

	// A ticket of this organization through another organization's path, and
	// the other organization's owner on this one's, both read as missing or
	// are refused, and the mark does not move.
	foreign := "/v1/organizations/" + other.String() + "/tickets/" + ticket + "/vip"
	e.expect(t, http.StatusNotFound, http.MethodPatch, foreign, e.token(t, stranger), "", `{"vip":true}`)
	if on, _ := vip(); on {
		t.Error("a foreign organization marked the ticket")
	}
}
