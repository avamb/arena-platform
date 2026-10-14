//go:build integration

package httpserver

import (
	"context"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/eventbot"
)

// The order card says what became of the letters the buyer got: the ticket
// letter (sent with time and address, partly or wholly failed with the
// reason and a "send again" button) and the refund letter; the orders list
// marks a failed ticket letter without opening the order.
func TestBotE2E_OrderCard_LetterStatus(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	srv := buildBotIntegrationServer(t, pool)
	api := httptest.NewServer(srv.router)
	defer api.Close()
	seed := seedOrdersBotOrg(t, pool, f.orgID)
	linkECBotUser(t, f, srv, ecOwnerTG, "owner")
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%q: %v", sql, err)
		}
	}
	// Runs before the seed's own cleanup (LIFO): the refund rows point at
	// the payment and the order the seed deletes.
	t.Cleanup(func() {
		for _, sql := range []string{
			`DELETE FROM refunds WHERE org_id = $1`,
			`DELETE FROM refund_batches WHERE org_id = $1`,
		} {
			if _, err := pool.Exec(context.Background(), sql, f.orgID); err != nil {
				t.Logf("letters cleanup: %s: %v", sql, err)
			}
		}
	})

	tg := newStubTelegram(t)
	startDialogTestBot(t, pool, api, tg)
	d := ecDriver{t, tg, ecOwnerTG}

	var tickets []uuid.UUID
	rows, err := pool.Query(ctx, `SELECT ticket_id FROM order_items WHERE order_id = $1 ORDER BY ordinal`, seed.anna)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		tickets = append(tickets, id)
	}
	rows.Close()
	if len(tickets) != 2 {
		t.Fatalf("Anna's order must have two tickets, got %d", len(tickets))
	}

	// 1. Both letters sent: time in the venue's zone, the address it went to.
	var sentAt time.Time
	if err := pool.QueryRow(ctx, `SELECT max(sent_at) FROM delivery_jobs WHERE ticket_id = ANY($1)`, tickets).Scan(&sentAt); err != nil {
		t.Fatal(err)
	}
	d.press("or:new", "Последние · 8")
	d.say(seed.annaEmail, "найдено 1")
	card := d.press("or:o:0", "Заказ №")
	want := "📧 Письмо с билетами: отправлено " + eventbot.FormatWhen(sentAt, "Europe/Madrid") + " на " + seed.annaEmail + "."
	if !strings.Contains(card, want) {
		t.Errorf("the sent card lacks %q:\n%s", want, card)
	}
	if strings.Contains(card, "Письмо о возврате") {
		t.Errorf("an order without refunds talks about a refund letter:\n%s", card)
	}
	id, _, _ := tg.lastOf(ecOwnerTG)
	if slices.Contains(tg.labelsOf(id), "📧 Отправить письмо ещё раз") {
		t.Errorf("a sent letter must keep the plain resend button: %v", tg.labelsOf(id))
	}

	// 2. One letter failed: "1 of 2", the reason, the retry button, and
	// the list row is marked.
	exec(`UPDATE delivery_jobs SET status = 'failed', sent_at = NULL, last_error = '550 mailbox unavailable' WHERE ticket_id = $1`, tickets[1])
	list := d.press("or:b", "найдено 1")
	if !strings.Contains(list, "⚠️📧 письмо покупателю не дошло") {
		t.Errorf("the list legend must explain the mark:\n%s", list)
	}
	id, _, _ = tg.lastOf(ecOwnerTG)
	if labels := tg.labelsOf(id); !slices.ContainsFunc(labels, func(l string) bool { return strings.Contains(l, "⚠️📧") }) {
		t.Errorf("the list row of a failed letter must carry the mark: %v", labels)
	}
	card = d.press("or:o:0", "Заказ №")
	want = "⚠️📧 Письмо с билетами: отправлено 1 из 2. Остальные Arena отправить не смогла. Причина: 550 mailbox unavailable"
	if !strings.Contains(card, want) {
		t.Errorf("the partly failed card lacks %q:\n%s", want, card)
	}
	id, _, data := tg.lastOf(ecOwnerTG)
	if !slices.Contains(tg.labelsOf(id), "📧 Отправить письмо ещё раз") || !slices.Contains(data, "or:r:s:"+seed.anna.String()) {
		t.Errorf("a failed letter must offer the resend route as «send again»: %v %v", tg.labelsOf(id), data)
	}

	// 3. Both failed, and a refund whose letter went out.
	exec(`UPDATE delivery_jobs SET status = 'failed', sent_at = NULL, last_error = '550 mailbox unavailable' WHERE ticket_id = $1`, tickets[0])
	var cs uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT checkout_session_id FROM orders WHERE id = $1`, seed.anna).Scan(&cs); err != nil {
		t.Fatal(err)
	}
	var pi, batch uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO payment_intents (checkout_session_id, org_id, provider, provider_payment_id, amount, currency, state)
	      VALUES ($1, $2, 'stripe', $3, 5000, 'EUR', 'succeeded') RETURNING id`, cs, f.orgID, "cs_test_letters_"+uuid.NewString()).Scan(&pi); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO refund_batches (org_id, order_id, payment_intent_id, idempotency_key, reason, notify_buyer)
	      VALUES ($1, $2, $3, $4, 'letters test', true) RETURNING id`, f.orgID, seed.anna, pi, "letters-"+uuid.NewString()).Scan(&batch); err != nil {
		t.Fatal(err)
	}
	refundedAt := time.Now().UTC().Add(-5 * time.Minute).Truncate(time.Second)
	exec(`INSERT INTO refunds (payment_intent_id, org_id, order_id, ticket_id, amount, currency, reason, state, settlement, batch_id, buyer_notified_at)
	      VALUES ($1, $2, $3, $4, 2500, 'EUR', 'letters test', 'succeeded', 'provider', $5, $6)`, pi, f.orgID, seed.anna, tickets[1], batch, refundedAt)
	d.press("or:b", "найдено 1")
	card = d.press("or:o:0", "Заказ №")
	for _, w := range []string{
		"⚠️📧 Письмо с билетами: Arena не смогла его отправить на " + seed.annaEmail + ". Причина: 550 mailbox unavailable",
		"📧 Письмо о возврате: отправлено " + eventbot.FormatWhen(refundedAt, "Europe/Madrid") + " на " + seed.annaEmail + ".",
	} {
		if !strings.Contains(card, w) {
			t.Errorf("the failed card lacks %q:\n%s", w, card)
		}
	}
	t.Logf("order card with a failed ticket letter and a sent refund letter:\n%s", card)
}
