//go:build integration

package httpserver

import (
	"context"
	"strings"
	"testing"
)

// V1: an invitation can be issued as VIP from the confirmation screen. The
// tick is part of the operation (a new batch id) and travels as `vip` on the
// staff-invitation route (spec 46, INV-05): the order remembers it and every
// ticket minted for it carries the mark; a guest issued without the tick does
// not.
func TestBotE2E_Invitations_VIPTick(t *testing.T) {
	e := newInviteE2E(t)
	d := e.driver(ecManagerTG)
	plain, star := e.email("plain-guest"), e.email("star-guest")
	vip := func(email string) bool {
		t.Helper()
		var on bool
		if err := e.pool.QueryRow(context.Background(), `SELECT t.vip FROM tickets t JOIN orders o ON o.checkout_session_id = t.checkout_session_id
			WHERE o.org_id = $1 AND o.source = 'complimentary' AND o.buyer_email = $2`, e.seed.orgID, email).Scan(&on); err != nil {
			t.Fatalf("vip of %s: %v", email, err)
		}
		return on
	}

	e.toCategory(d, 1)
	d.press("iv:q:1", "Кто эти 1 гостей?")
	d.say(star, "Отправить приглашения?")
	batch := func() string {
		var b string
		if err := e.pool.QueryRow(context.Background(), `SELECT coalesce(state->>'batch', '') FROM bot_dialogs WHERE telegram_user_id = $1 AND kind = 'invite'`, d.id).Scan(&b); err != nil {
			t.Fatal(err)
		}
		return b
	}
	before := batch()
	d.press("iv:vip:1", "отмечены как VIP")
	if after := batch(); after == before || after == "" {
		t.Error("the tick changes the request, so it must mint a new batch id")
	}
	d.press("iv:vip:0", "Отправить приглашения?")
	d.press("iv:vip:1", "отмечены как VIP")
	d.press("iv:go", "Выдано приглашений: 1")
	var asked bool
	if err := e.pool.QueryRow(context.Background(), `SELECT coalesce((metadata->'invitation'->>'vip')::boolean, false) FROM orders
		WHERE org_id = $1 AND source = 'complimentary' AND buyer_email = $2`, e.seed.orgID, star).Scan(&asked); err != nil || !asked {
		t.Fatalf("the order does not remember the tick: %v %v", asked, err)
	}
	e.mintTickets()
	if !vip(star) {
		t.Error("the invitation issued with the tick is not marked VIP")
	}

	// The list row and the card mark it (the star), as the old card did.
	d.press("iv:l", "Все сеансы, сначала новые")
	if card := d.press("iv:no:0", "Места:"); !strings.Contains(card, "⭐ VIP") {
		t.Errorf("the card does not mark the VIP invitation: %s", card)
	}
	d.press("home", "Что будем делать")
	e.toCategory(d, 1)
	d.press("iv:q:1", "Кто эти 1 гостей?")
	d.say(plain, "Отправить приглашения?")
	d.press("iv:go", "Выдано приглашений: 1")
	e.mintTickets()
	if vip(plain) {
		t.Error("an invitation issued without the tick is marked VIP")
	}
	if !vip(star) {
		t.Error("the first invitation lost its mark")
	}
	// The plain invitation's card has no star.
	d.press("iv:l", "Все сеансы, сначала новые")
	if card := d.press("iv:no:0", "Места:"); strings.Contains(card, "VIP") {
		t.Errorf("the card of a plain invitation is marked VIP: %s", card)
	}
}
