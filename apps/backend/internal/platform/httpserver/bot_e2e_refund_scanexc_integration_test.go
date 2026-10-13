//go:build integration

package httpserver

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

// Owner decisions 2026-10-13: a refund refused because the ticket was
// already scanned says so plainly and what to do next; only the platform
// superadmin is offered "Allow a refund of this ticket (24 h)", which asks
// for a typed reason, grants the exception and lets the refund continue.
func TestBotE2E_Refund_ScannedTicketRefusalAndOperatorException(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	stripe := newStubStripe(t)
	stripe.refundHandler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"re_bot_e2e_scanexc","status":"succeeded"}`)
	}
	srv := buildBotIntegrationServerCfg(t, pool, func(c *config.Config) { c.StripeAPIBaseURL = stripe.baseURL() })
	api := httptest.NewServer(srv.router)
	defer api.Close()
	seed := seedOrdersBotOrg(t, pool, f.orgID)
	linkECBotUser(t, f, srv, ecOwnerTG, "owner")
	linkECBotUser(t, f, srv, ecManagerTG, "manager")
	tickets := seedRefundableOrder(t, pool, f.orgID, seed)
	ctx := context.Background()
	ids := []string{tickets[0].String(), tickets[1].String()}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM refund_scan_exceptions WHERE ticket_id::text = ANY($1)`, ids)
		_, _ = pool.Exec(c, `DELETE FROM refund_scan_blocks WHERE ticket_id::text = ANY($1)`, ids)
		_, _ = pool.Exec(c, `DELETE FROM worker_jobs WHERE job_type = 'refund.scanned_alert_email' AND payload::text LIKE '%' || $1 || '%'`, f.orgID.String())
		_, _ = pool.Exec(c, `DELETE FROM outbox_events WHERE event_type LIKE 'v1.refund.%' AND payload::text LIKE '%' || $1 || '%'`, f.orgID.String())
	})

	tg := newStubTelegram(t)
	stop := startDialogTestBot(t, pool, api, tg)
	defer func() { stop() }()

	// ── 1. A manager: the ticket is scanned while the refund is being
	//    confirmed. The refusal names the ticket, says the money cannot go
	//    back and what to do next, and offers no exception button.
	m := ecDriver{t, tg, ecManagerTG}
	m.press("or:v:"+seed.anna.String(), "Заказ №")
	m.press("or:f:s:"+seed.anna.String(), "Возврат · заказ №")
	m.press("or:f:t:0", "Возврат · заказ №")
	m.press("or:f:go", "Почему возвращаются деньги")
	m.press("or:f:r:1", "ВЕРНУТЬ")
	m.say("вернуть", "Последняя проверка")
	var key string
	if err := pool.QueryRow(ctx, `SELECT state->>'key' FROM bot_dialogs WHERE telegram_user_id = $1 AND kind = 'refund'`, ecManagerTG).Scan(&key); err != nil {
		t.Fatalf("dialog key: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE tickets SET used_at = now() WHERE id = $1`, tickets[0]); err != nil {
		t.Fatal(err)
	}
	refused := m.press("or:f:ok:"+strings.TrimPrefix(key, "tg-")[:12], "уже отсканирован на входе")
	for _, want := range []string{"Билет №", "Деньги за него вернуть нельзя", "поддержку"} {
		if !strings.Contains(refused, want) {
			t.Errorf("the refusal lacks %q:\n%s", want, refused)
		}
	}
	if _, _, kb := tg.lastOf(ecManagerTG); hasPrefixData(kb, "or:f:x") {
		t.Fatalf("a manager was offered the operator's exception: %v", kb)
	}
	if n, _ := refundCount(t, srv, seed.anna); n != 0 {
		t.Fatalf("a scanned ticket was refunded: %d refund(s)", n)
	}

	// ── 2. The platform superadmin sees the scanned ticket left out of the
	//    list, with the button that allows one refund of it.
	var ownerUser uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT user_id FROM bot_telegram_links WHERE telegram_user_id = $1`, ecOwnerTG).Scan(&ownerUser); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO user_roles (user_id, role_id, org_id)
		SELECT $1, id, NULL FROM roles WHERE name = 'platform_superadmin' AND org_id IS NULL`, ownerUser); err != nil {
		t.Fatalf("grant platform_superadmin: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM user_roles WHERE user_id = $1 AND org_id IS NULL
			AND role_id IN (SELECT id FROM roles WHERE name = 'platform_superadmin')`, ownerUser)
	})
	o := ecDriver{t, tg, ecOwnerTG}
	o.press("or:v:"+seed.anna.String(), "Заказ №")
	pick := o.press("or:f:s:"+seed.anna.String(), "Уже отсканированы на входе")
	if !strings.Contains(pick, "оператор платформы") {
		t.Errorf("the operator is not told about the button:\n%s", pick)
	}
	if _, _, kb := tg.lastOf(ecOwnerTG); !hasPrefixData(kb, "or:f:x:0") || hasPrefixData(kb, "or:f:t:1") {
		t.Fatalf("the pick screen should offer ticket 1 and the exception for the scanned one: %v", kb)
	}
	o.press("or:f:x:0", "Напишите причину")
	o.say("Показ остановили на середине", "разрешён на 24 часа")
	var note string
	if err := pool.QueryRow(ctx, `SELECT coalesce(note, '') FROM refund_scan_exceptions WHERE ticket_id = $1`, tickets[0]).Scan(&note); err != nil {
		t.Fatalf("no exception was granted: %v", err)
	}
	if note != "Показ остановили на середине" {
		t.Errorf("exception note = %q", note)
	}

	// ── 3. "Continue the refund": the scanned ticket is offered now.
	again := o.press("or:f:s:"+seed.anna.String(), "Возврат · заказ №")
	if strings.Contains(again, "Уже отсканированы") {
		t.Errorf("the ticket is still left out after the exception:\n%s", again)
	}
	if _, _, kb := tg.lastOf(ecOwnerTG); !hasPrefixData(kb, "or:f:t:1") {
		t.Fatalf("both tickets should be offered now: %v", kb)
	}
}
