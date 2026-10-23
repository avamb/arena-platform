//go:build integration

package httpserver

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// V1: the order card marks a ticket VIP and takes the mark off. The press
// carries the wanted value, so a repeated press changes nothing, and a person
// of another organization changes nothing at all.
func TestBotE2E_OrderCard_VIPMark(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	other := newBotInviteFixture(t, pool)
	srv := buildBotIntegrationServer(t, pool)
	api := httptest.NewServer(srv.router)
	defer api.Close()
	seed := seedOrdersBotOrg(t, pool, f.orgID)
	linkECBotUser(t, f, srv, ecOwnerTG, "owner")
	linkECBotUser(t, f, srv, ecManagerTG, "manager")
	linkECBotUser(t, other, srv, ecOutsiderTG, "owner")

	tg := newStubTelegram(t)
	startDialogTestBot(t, pool, api, tg)

	ctx := context.Background()
	rows, err := pool.Query(ctx, `SELECT id FROM tickets WHERE order_id = $1 AND status = 'active' ORDER BY ordinal`, seed.anna)
	if err != nil {
		t.Fatal(err)
	}
	var tickets []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		tickets = append(tickets, id)
	}
	rows.Close()
	if len(tickets) < 2 {
		t.Fatalf("Anna's order has %d active tickets, want 2", len(tickets))
	}
	marked := func() (n int, first bool) {
		if err := pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE vip), coalesce(bool_or(vip) FILTER (WHERE id = $2), false)
			FROM tickets WHERE order_id = $1`, seed.anna, tickets[0]).Scan(&n, &first); err != nil {
			t.Fatal(err)
		}
		return n, first
	}

	for name, tgID := range map[string]int64{"owner": ecOwnerTG, "manager": ecManagerTG} {
		d := ecDriver{t, tg, tgID}
		card := d.press("or:v:"+seed.anna.String(), "Anna")
		if strings.Contains(card, "VIP") && !strings.Contains(card, "Отметить") {
			t.Errorf("%s: an unmarked card talks about VIP:\n%s", name, card)
		}
		got := d.press("or:vip:1:"+tickets[0].String(), "VIP")
		if n, first := marked(); n != 1 || !first {
			t.Fatalf("%s: marked = %d first = %v after the press, want exactly the first ticket", name, n, first)
		}
		if !strings.Contains(got, "Снять VIP") && !strings.Contains(got, "⭐ VIP") {
			t.Errorf("%s: the card does not show the mark:\n%s", name, got)
		}
		// The same press again keeps the state (it carries the wanted value).
		d.press("or:vip:1:"+tickets[0].String(), "VIP")
		if n, _ := marked(); n != 1 {
			t.Errorf("%s: a repeated press changed the marks: %d", name, n)
		}
		d.press("or:vip:0:"+tickets[0].String(), "Anna")
		if n, _ := marked(); n != 0 {
			t.Errorf("%s: the mark was not taken off: %d", name, n)
		}
	}

	// Another organization's person has no card of this order open, and the
	// route refuses a foreign ticket anyway: nothing is marked.
	out := ecDriver{t, tg, ecOutsiderTG}
	out.press("or:vip:1:"+tickets[1].String(), "")
	if n, _ := marked(); n != 0 {
		t.Errorf("an outsider marked %d tickets", n)
	}
}
