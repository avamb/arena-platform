//go:build integration

package httpserver

// End-to-end proof of the bot's "Refunds" queue (spec 35 EC-23) through the
// real router, the real bot, the refund engine and a stub Stripe: a refund the
// provider refused shows under "Failed", the owner sends it again after a
// confirmation question and a NEW refund goes through; a refund that was
// accepted cannot be retried; a user of another organization sees nothing.
// The stub Telegram records message TEXT only, so effects are checked in the
// database.

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

func TestBotE2E_RefundQueue_FailedRefundIsRetried(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	other := newBotInviteFixture(t, pool)
	stripe := newStubStripe(t)
	declined := true
	stripe.refundHandler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if declined {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprint(w, `{"error":{"type":"invalid_request_error","code":"refund_declined","message":"declined for the test"}}`)
			return
		}
		_, _ = fmt.Fprint(w, `{"id":"re_queue_e2e","status":"succeeded"}`)
	}
	srv := buildBotIntegrationServerCfg(t, pool, func(c *config.Config) { c.StripeAPIBaseURL = stripe.baseURL() })
	api := httptest.NewServer(srv.router)
	defer api.Close()
	seed := seedOrdersBotOrg(t, pool, f.orgID)
	linkECBotUser(t, f, srv, ecOwnerTG, "owner")
	linkECBotUser(t, other, srv, ecOutsiderTG, "owner")
	tickets := seedRefundableOrder(t, pool, f.orgID, seed)
	ctx := context.Background()

	tg := newStubTelegram(t)
	stop := startDialogTestBot(t, pool, api, tg)
	defer func() { stop() }()
	d := ecDriver{t, tg, ecOwnerTG}

	// A refund of the first ticket, refused by the provider.
	d.press("or:v:"+seed.anna.String(), "Заказ №")
	d.press("or:f:s:"+seed.anna.String(), "Возврат · заказ №")
	d.press("or:f:t:1", "Возврат · заказ №")
	d.press("or:f:go", "Почему возвращаются деньги")
	d.press("or:f:r:3", "ВЕРНУТЬ")
	d.say("вернуть", "Последняя проверка")
	d.press("or:f:ok", "не удалось")
	var failedID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM refunds WHERE order_id = $1 AND state = 'failed'`, seed.anna).Scan(&failedID); err != nil {
		t.Fatalf("the declined refund is not in the database: %v", err)
	}

	// The queue: "Need a check" is empty, "Failed" lists it with both buttons.
	empty := d.press("rq:l:review:1", "Возвраты · Нужна проверка")
	if !strings.Contains(empty, "Здесь пусто") {
		t.Errorf("the review filter must be empty:\n%s", empty)
	}
	list := d.press("rq:l:failed:1", "Возвраты · Не удались")
	if !strings.Contains(list, "№") || !strings.Contains(list, "25") {
		t.Errorf("the failed filter does not show the refund:\n%s", list)
	}

	// Retry asks first and sends nothing until the second press.
	declined = false
	ask := d.press("rq:rt:"+failedID.String(), "Отправить этот возврат")
	if !strings.Contains(ask, "Заказ №") || !strings.Contains(ask, "25") {
		t.Errorf("the question names neither the order nor the amount:\n%s", ask)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM refunds WHERE order_id = $1`, seed.anna).Scan(&n); err != nil || n != 1 {
		t.Fatalf("refunds after the question = %d (%v); want 1", n, err)
	}
	done := d.press("rq:ry:"+failedID.String(), "Возврат отправлен повторно")
	if !strings.Contains(done, "возвращено") {
		t.Errorf("the retry result does not say the money is back:\n%s", done)
	}
	var succeeded int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM refunds WHERE order_id = $1 AND state = 'succeeded'`, seed.anna).Scan(&succeeded); err != nil || succeeded != 1 {
		t.Fatalf("succeeded refunds = %d (%v); want 1", succeeded, err)
	}
	var cancelled string
	if err := pool.QueryRow(ctx, `SELECT status FROM tickets WHERE id = $1`, tickets[0]).Scan(&cancelled); err != nil || cancelled != "cancelled" {
		t.Fatalf("the retried ticket is %q (%v); want cancelled", cancelled, err)
	}

	// An accepted refund is not retryable.
	var okID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM refunds WHERE order_id = $1 AND state = 'succeeded'`, seed.anna).Scan(&okID); err != nil {
		t.Fatal(err)
	}
	d.press("rq:rt:"+okID.String(), "нельзя отправить повторно")
	d.press("rq:ry:"+okID.String(), "нельзя отправить повторно")
	// the refund that was retried once is not offered again
	d.press("rq:rt:"+failedID.String(), "нельзя отправить повторно")
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM refunds WHERE order_id = $1`, seed.anna).Scan(&n); err != nil || n != 2 {
		t.Fatalf("refunds after a refused retry = %d (%v); want 2", n, err)
	}

	// Another organization's owner sees an empty queue.
	o := ecDriver{t, tg, ecOutsiderTG}
	got := o.press("rq:l:failed:1", "Возвраты · Не удались")
	if strings.Contains(got, "№") {
		t.Errorf("a foreign owner sees a refund:\n%s", got)
	}
}
