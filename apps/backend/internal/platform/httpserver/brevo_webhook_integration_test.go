//go:build integration

// brevo_webhook_integration_test.go — the Brevo delivery-status webhook
// through the REAL router against a live database: the token check, events
// of messages arena did not send, duplicates, events out of order, a failure
// a later delivery overrides, matching by arena's tracking header alone,
// ignored event types, a batch — and the order card / list that show the
// result (letters, letter_failed).
//
// Run against a FRESH migrated database (AGENTS.md CI-Integration recipe).
package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/emailstatus"
)

const brevoTestToken = "brevo-webhook-integration-token-0123456789abcdef"

type brevoHook struct {
	t  *testing.T
	ts *httptest.Server
}

func newBrevoHook(t *testing.T) *brevoHook {
	t.Helper()
	srv, _ := productionIntegrationServerCfg(t, func(c *config.Config) { c.BrevoWebhookToken = brevoTestToken })
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	return &brevoHook{t: t, ts: ts}
}

// post sends body to the webhook with token and returns status and JSON.
func (h *brevoHook) post(token, body string) (int, map[string]any) {
	h.t.Helper()
	resp := integDoRequest(h.t, h.ts.Client(), http.MethodPost, h.ts.URL+"/v1/webhooks/brevo/"+token, "", body)
	raw := integReadBody(h.t, resp)
	out := map[string]any{}
	if strings.TrimSpace(raw) != "" {
		_ = json.Unmarshal([]byte(raw), &out)
	}
	return resp.StatusCode, out
}

// event builds one Brevo event body.
func brevoEventJSON(event, messageID string, at time.Time, extra map[string]any) string {
	m := map[string]any{
		"event":      event,
		"email":      "buyer@example.com",
		"message-id": "<" + messageID + ">",
		"ts_epoch":   at.UnixMilli(),
		"subject":    "Your tickets",
	}
	for k, v := range extra {
		m[k] = v
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func (h *brevoHook) send(event, messageID string, at time.Time, extra map[string]any) map[string]any {
	h.t.Helper()
	code, body := h.post(brevoTestToken, brevoEventJSON(event, messageID, at, extra))
	if code != http.StatusOK {
		h.t.Fatalf("%s %s: status %d body %v", event, messageID, code, body)
	}
	return body
}

type letterState struct {
	Status                      string
	DeliveredAt, ProxyOpenedAt  *time.Time
	OpenedAt, FailedAt          *time.Time
	FailureEvent, FailureReason *string
}

func readLetter(t *testing.T, f *ecOrdersFixture, id uuid.UUID) letterState {
	t.Helper()
	var s letterState
	if err := f.pool.QueryRow(context.Background(), `SELECT status, delivered_at, proxy_opened_at, opened_at,
		failed_at, failure_event, failure_reason FROM email_letters WHERE id = $1`, id).
		Scan(&s.Status, &s.DeliveredAt, &s.ProxyOpenedAt, &s.OpenedAt, &s.FailedAt, &s.FailureEvent, &s.FailureReason); err != nil {
		t.Fatalf("read letter %s: %v", id, err)
	}
	return s
}

// sentLetter records a letter of the paid order as the worker does and
// returns its id and message id.
func sentLetter(t *testing.T, f *ecOrdersFixture, kind, recipient string, jobTickets []uuid.UUID) (uuid.UUID, string) {
	t.Helper()
	ctx := context.Background()
	id, err := emailstatus.Begin(ctx, f.pool, emailstatus.NewLetter{OrgID: f.orgA, OrderID: &f.paidOrder, Kind: kind, Recipient: recipient})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	var jobs []uuid.UUID
	if len(jobTickets) > 0 {
		rows, err := f.pool.Query(ctx, `SELECT id FROM delivery_jobs WHERE ticket_id = ANY($1)`, jobTickets)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var j uuid.UUID
			_ = rows.Scan(&j)
			jobs = append(jobs, j)
		}
		rows.Close()
	}
	mid := fmt.Sprintf("2026101310.%s@smtp-relay.mailin.fr", uuid.NewString()[:12])
	if err := emailstatus.Accepted(ctx, f.pool, id, mid, jobs); err != nil {
		t.Fatalf("Accepted: %v", err)
	}
	return id, mid
}

func TestBrevoWebhook_StatusesAndOrderRead(t *testing.T) {
	f := newECOrdersFixture(t)
	hook := newBrevoHook(t)
	ctx := context.Background()
	t.Cleanup(func() {
		_, _ = f.pool.Exec(ctx, `DELETE FROM email_letters WHERE org_id = $1`, f.orgA)
	})

	buyer := "anna@example.com"
	ticketLetter, ticketMID := sentLetter(t, f, emailstatus.KindTicket, buyer, []uuid.UUID{f.ticket1, f.ticket2})
	changeLetter, changeMID := sentLetter(t, f, emailstatus.KindChange, buyer, nil)
	refundLetter, _ := sentLetter(t, f, emailstatus.KindRefund, buyer, nil)
	t0 := time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)

	// Bad token: refused, nothing recorded.
	if code, _ := hook.post("wrong-token", brevoEventJSON("delivered", ticketMID, t0, nil)); code != http.StatusUnauthorized {
		t.Fatalf("bad token: status %d, want 401", code)
	}
	if s := readLetter(t, f, ticketLetter); s.Status != emailstatus.StatusSent {
		t.Fatalf("a refused delivery must change nothing, letter is %q", s.Status)
	}

	// A message arena did not send (shared Brevo account): 200, ignored.
	if body := hook.send("delivered", "someone-else@smtp-relay.mailin.fr", t0, nil); body["applied"] != float64(0) {
		t.Fatalf("unknown message id must not be applied: %v", body)
	}
	// An event type arena does not model: 200, ignored, no row.
	hook.send("click", ticketMID, t0, map[string]any{"link": "https://example.com"})
	var clicks int
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM email_letter_events WHERE letter_id = $1 AND event = 'click'`, ticketLetter).Scan(&clicks)
	if clicks != 0 {
		t.Fatal("an ignored event type must not be stored")
	}

	// Ticket letter: request, delivered, delivered again (duplicate).
	hook.send("request", ticketMID, t0, nil)
	hook.send("delivered", ticketMID, t0.Add(time.Minute), nil)
	hook.send("delivered", ticketMID, t0.Add(time.Minute), nil)
	s := readLetter(t, f, ticketLetter)
	if s.Status != emailstatus.StatusDelivered || s.DeliveredAt == nil || !s.DeliveredAt.Equal(t0.Add(time.Minute)) {
		t.Fatalf("ticket letter after delivered x2 = %+v", s)
	}
	var seen int
	_ = f.pool.QueryRow(ctx, `SELECT seen_count FROM email_letter_events WHERE letter_id = $1 AND event = 'delivered'`, ticketLetter).Scan(&seen)
	if seen != 2 {
		t.Fatalf("duplicate delivered must be one row seen twice, seen_count = %d", seen)
	}

	// Change letter, out of order: the open arrives before the delivery,
	// and a proxy open arrives too — opened, with each step's own time.
	hook.send("unique_opened", changeMID, t0.Add(10*time.Minute), nil)
	hook.send("proxy_open", changeMID, t0.Add(6*time.Minute), nil)
	hook.send("delivered", changeMID, t0.Add(5*time.Minute), nil)
	s = readLetter(t, f, changeLetter)
	if s.Status != emailstatus.StatusOpened || !s.OpenedAt.Equal(t0.Add(10*time.Minute)) ||
		!s.DeliveredAt.Equal(t0.Add(5*time.Minute)) || !s.ProxyOpenedAt.Equal(t0.Add(6*time.Minute)) {
		t.Fatalf("change letter = %+v", s)
	}

	// Refund letter, matched by arena's tracking header only (no message
	// id), in a batch: a soft bounce, then a later delivery overrides it.
	batch := fmt.Sprintf(`[{"event":"soft_bounce","ts_epoch":%d,"reason":"452 mailbox full","X-Mailin-custom":%q},
		{"event":"delivered","ts_epoch":%d,"X-Mailin-custom":%q}]`,
		t0.Add(time.Minute).UnixMilli(), emailstatus.TrackingID(refundLetter),
		t0.Add(30*time.Minute).UnixMilli(), emailstatus.TrackingID(refundLetter))
	code, body := hook.post(brevoTestToken, batch)
	if code != http.StatusOK || body["applied"] != float64(2) {
		t.Fatalf("batch: status %d body %v", code, body)
	}
	if s = readLetter(t, f, refundLetter); s.Status != emailstatus.StatusDelivered || s.FailedAt != nil {
		t.Fatalf("refund letter: a later delivery must override the soft bounce: %+v", s)
	}

	// Card: three letters, no failure yet.
	code, card := f.get("/v1/organizations/"+f.orgA.String()+"/orders/"+f.paidOrder.String(), f.managerTok)
	if code != http.StatusOK {
		t.Fatalf("card: %d %v", code, card)
	}
	letters, _ := card["letters"].([]any)
	if len(letters) != 3 || card["letter_failed"] != false {
		t.Fatalf("card letters = %v, letter_failed = %v", letters, card["letter_failed"])
	}
	first := letters[0].(map[string]any)
	if first["kind"] != "ticket" || first["status"] != "delivered" || first["recipient"] != buyer ||
		len(first["ticket_ids"].([]any)) != 2 || first["delivered_at"] == nil || first["sent_at"] == nil {
		t.Fatalf("ticket letter on the card = %v", first)
	}
	if second := letters[1].(map[string]any); second["kind"] != "change" || second["status"] != "opened" ||
		second["proxy_opened_at"] == nil || second["opened_at"] == nil {
		t.Fatalf("change letter on the card = %v", second)
	}

	// The ticket letter bounces after it was delivered: failed, with the
	// reason (address scrubbed), and the list flags the order.
	hook.send("hard_bounce", ticketMID, t0.Add(20*time.Minute),
		map[string]any{"reason": "550 5.1.1 <anna@example.com>: Recipient address rejected"})
	s = readLetter(t, f, ticketLetter)
	if s.Status != emailstatus.StatusFailed || s.FailureEvent == nil || *s.FailureEvent != "hard_bounce" ||
		s.FailureReason == nil || strings.Contains(*s.FailureReason, "anna@example.com") {
		t.Fatalf("ticket letter after a later hard bounce = %+v", s)
	}
	// A delivery reported BEFORE the bounce, arriving late, changes nothing.
	hook.send("delivered", ticketMID, t0.Add(2*time.Minute), nil)
	if s = readLetter(t, f, ticketLetter); s.Status != emailstatus.StatusFailed {
		t.Fatalf("an older delivery must not override a newer failure: %q", s.Status)
	}

	_, card = f.get("/v1/organizations/"+f.orgA.String()+"/orders/"+f.paidOrder.String(), f.managerTok)
	if card["letter_failed"] != true {
		t.Fatalf("card letter_failed = %v, want true", card["letter_failed"])
	}
	code, list := f.get("/v1/organizations/"+f.orgA.String()+"/orders?tab=recent", f.managerTok)
	if code != http.StatusOK {
		t.Fatalf("list: %d %v", code, list)
	}
	flags := map[string]any{}
	for _, r := range list["orders"].([]any) {
		m := r.(map[string]any)
		flags[m["id"].(string)] = m["letter_failed"]
	}
	if flags[f.paidOrder.String()] != true || flags[f.expiredOrder.String()] != false {
		t.Fatalf("list letter_failed flags = %v", flags)
	}
}

func TestBrevoWebhook_RefusesMalformedAndOversizedBodies(t *testing.T) {
	hook := newBrevoHook(t)
	if code, _ := hook.post(brevoTestToken, `not json`); code != http.StatusBadRequest {
		t.Fatalf("malformed: %d, want 400", code)
	}
	big := `{"event":"delivered","pad":"` + strings.Repeat("x", 300<<10) + `"}`
	if code, _ := hook.post(brevoTestToken, big); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized: %d, want 413", code)
	}
}

func TestBrevoWebhook_NotMountedWithoutToken(t *testing.T) {
	srv, _ := productionIntegrationServer(t)
	ts := httptest.NewServer(srv.Router())
	defer ts.Close()
	resp := integDoRequest(t, ts.Client(), http.MethodPost, ts.URL+"/v1/webhooks/brevo/anything", "", `{"event":"delivered"}`)
	_ = integReadBody(t, resp)
	if resp.StatusCode == http.StatusOK {
		t.Fatal("without BREVO_WEBHOOK_TOKEN the route must not exist")
	}
}
