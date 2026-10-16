//go:build integration

package httpserver

import (
	"context"
	"strings"
	"testing"
	"time"
)

// V1: an invitation can be issued as VIP from the confirmation screen. The
// tick is part of the operation (a new batch id), every ticket of it carries
// the mark, the list shows it, and a guest issued without the tick does not.
func TestBotE2E_Invitations_VIPTick(t *testing.T) {
	e := newInviteE2E(t)
	d := e.driver(ecManagerTG)
	plain, star := e.email("plain-guest"), e.email("star-guest")
	vip := func(email string) bool {
		var on bool
		if err := e.pool.QueryRow(context.Background(), `SELECT t.vip FROM tickets t JOIN complimentary_issuances ci ON ci.id = t.complimentary_issuance_id
			WHERE ci.org_id = $1 AND t.holder_email = $2`, e.seed.orgID, email).Scan(&on); err != nil {
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
	d.tg.push(e2eCallbackAs(d.id, "iv:vip:1"))
	time.Sleep(2 * time.Second)
	var dbg string
	_ = e.pool.QueryRow(context.Background(), `SELECT step || ' ' || state::text FROM bot_dialogs WHERE telegram_user_id = $1 AND kind = 'invite'`, d.id).Scan(&dbg)
	t.Logf("DEBUGSTATE %s", dbg)
	d.press("iv:vip:1", "отмечены как VIP")
	if after := batch(); after == before || after == "" {
		t.Error("the tick changes the request, so it must mint a new batch id")
	}
	d.press("iv:vip:0", "Отправить приглашения?")
	d.press("iv:vip:1", "отмечены как VIP")
	d.press("iv:go", "Выдано приглашений: 1")
	if !vip(star) {
		t.Error("the invitation issued with the tick is not marked VIP")
	}

	// The list marks it; a plain invitation issued afterwards is not marked.
	list := d.press("iv:l", "Выдано: 1")
	if !strings.Contains(list, "⭐") {
		t.Errorf("the list does not mark the VIP invitation:\n%s", list)
	}
	e.toCategory(d, 1)
	d.press("iv:q:1", "Кто эти 1 гостей?")
	d.say(plain, "Отправить приглашения?")
	d.press("iv:go", "Выдано приглашений: 1")
	if vip(plain) {
		t.Error("an invitation issued without the tick is marked VIP")
	}
	if !vip(star) {
		t.Error("the first invitation lost its mark")
	}
}
