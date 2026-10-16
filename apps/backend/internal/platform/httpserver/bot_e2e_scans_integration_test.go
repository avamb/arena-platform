//go:build integration

// bot_e2e_scans_integration_test.go — task M2 through the real bot: the order
// card prints how the door treated each ticket (who reported it, which gate),
// and the date's card lists the tickets that have not come in. Scans of both
// sources (arena's scanner and MACS) are rows of scan_events.
package httpserver

import (
	"context"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestBotE2E_ScansOnTheOrderCardAndTheNotEnteredList(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	srv := buildBotIntegrationServer(t, pool)
	api := httptest.NewServer(srv.router)
	defer api.Close()
	seed := seedOrdersBotOrg(t, pool, f.orgID)
	linkECBotUser(t, f, srv, ecOwnerTG, "owner")
	ctx := context.Background()

	// Anna's two tickets, in line order. The second one is a legacy ticket
	// with a scanned barcode; the MACS admission below is the first entry the
	// card reports. The first ticket was turned away by arena's scanner.
	var t1, t2 uuid.UUID
	rows, err := pool.Query(ctx, `SELECT id FROM tickets WHERE order_id = $1 ORDER BY ordinal`, seed.anna)
	if err != nil {
		t.Fatal(err)
	}
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if len(ids) != 2 {
		t.Fatalf("Anna has %d tickets, want 2", len(ids))
	}
	t1, t2 = ids[0], ids[1]

	run := uuid.NewString()[:8]
	var subID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO webhook_subscribers (site_url, callback_url, signing_secret, event_types, active, kind, org_id)
		VALUES ('', 'https://macs.example.test/api/_wh/tickets', 'whsec_m2e2e', '{}', true, 'macs', $1) RETURNING id`, f.orgID).Scan(&subID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM scan_events WHERE org_id = $1`, f.orgID)
		_, _ = pool.Exec(c, `DELETE FROM webhook_subscribers WHERE id = $1`, subID)
	})
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	if _, err := pool.Exec(ctx, `INSERT INTO scan_events (org_id, session_id, ticket_id, credential_code, scanned_at, gate, result, source)
		VALUES ($1, $2, $3, $4, $5, 'North', 'denied', 'arena_scanner')`, f.orgID, seed.session1, t1, "m2e-"+run+"-a", base); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO scan_events (org_id, session_id, ticket_id, credential_code, scanned_at, gate, result, source, scanner_label, external_scan_id, subscriber_id)
		VALUES ($1, $2, $3, $4, $5, '2', 'admitted', 'macs', 'Scanner 7', $6, $7)`,
		f.orgID, seed.session1, t2, "m2e-"+run+"-b", base.Add(10*time.Minute), "m2e-"+run, subID); err != nil {
		t.Fatal(err)
	}

	tg := newStubTelegram(t)
	startDialogTestBot(t, pool, api, tg)
	d := ecDriver{t, tg, ecOwnerTG}

	// ── the order card ────────────────────────────────────────────────────
	d.press("or:new", "Последние")
	d.say(seed.annaCode, "найдено 1")
	card := d.press("or:o:0", "Заказ №")
	for _, w := range []string{"✔ вошёл", "МАКС", "вход 2", "✖ отказ на входе", "North", "сканер Arena"} {
		if !strings.Contains(card, w) {
			t.Errorf("Anna's card lacks %q:\n%s", w, card)
		}
	}

	// ── the date's "Not entered" list ─────────────────────────────────────
	d.press("ses:list:"+seed.event1.String(), "Сеансы мероприятия")
	d.press("ses:o:0", "Здесь можно перенести")
	list := d.press("ses:ne:1", "Ещё не прошли")
	for _, w := range []string{"Билетов: 1", "№" + itoa64(seed.annaSID), "Anna Orderova", "отказ на входе", "North"} {
		if !strings.Contains(list, w) {
			t.Errorf("the not-entered list lacks %q:\n%s", w, list)
		}
	}
	// The ticket MACS let in is not on the list.
	var enteredNumber int64
	if err := pool.QueryRow(ctx, `SELECT system_ticket_id FROM tickets WHERE id = $1`, t2).Scan(&enteredNumber); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(list, "<code>"+itoa64(enteredNumber)+"</code>") {
		t.Errorf("an entered ticket (%d) is on the list:\n%s", enteredNumber, list)
	}

	// Letting the second ticket in too empties the list.
	if _, err := pool.Exec(ctx, `INSERT INTO scan_events (org_id, session_id, ticket_id, credential_code, scanned_at, gate, result, source)
		VALUES ($1, $2, $3, $4, now(), 'North', 'admitted', 'arena_scanner')`, f.orgID, seed.session1, t1, "m2e-"+run+"-c"); err != nil {
		t.Fatal(err)
	}
	d.press("ses:ne:1", "Все, у кого есть действующий билет, уже прошли")
}

func itoa64(n int64) string { return strconv.FormatInt(n, 10) }
