//go:build integration

// brevo_user_agent_integration_test.go — U-7: the reader's user agent that
// Brevo reports on an open, proxy open or click is stored on the interaction
// row and read only through GET .../orders/{order_id}/letter-interactions,
// for the order's organization. Brevo sends no reader IP address, so none is
// stored. Run against a FRESH migrated database (AGENTS.md recipe).
package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/apikeys"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/emailstatus"
)

const (
	uaOpen  = "Mozilla/5.0 (U-7 open test) Arena/1.0"
	uaClick = "Mozilla/5.0 (U-7 click test) Arena/1.0"
	uaProxy = "Mozilla/5.0 (Macintosh) AppleWebKit/605.1.15 (via proxy U-7)"
)

type letterInteractionsWire struct {
	OrderID string `json:"order_id"`
	Letters []struct {
		LetterID     string `json:"letter_id"`
		Kind         string `json:"kind"`
		SentAt       string `json:"sent_at"`
		Interactions []struct {
			Event      string  `json:"event"`
			OccurredAt string  `json:"occurred_at"`
			Link       *string `json:"link"`
			UserAgent  *string `json:"user_agent"`
		} `json:"interactions"`
	} `json:"letters"`
}

func TestBrevoWebhook_LetterInteractionsCarryUserAgentAndStayInsideTheOrganization(t *testing.T) {
	f := newECOrdersFixture(t)
	hook := newBrevoHook(t)
	ctx := context.Background()
	t.Cleanup(func() {
		_, _ = f.pool.Exec(ctx, `DELETE FROM email_letters WHERE org_id = $1`, f.orgA)
	})

	ticketLetter, ticketMID := sentLetter(t, f, emailstatus.KindTicket, "anna@example.com", []uuid.UUID{f.ticket1, f.ticket2})
	// A second letter nobody opens is listed with no interactions.
	quietLetter, _ := sentLetter(t, f, emailstatus.KindRefund, "anna@example.com", nil)
	t0 := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)

	hook.send("delivered", ticketMID, t0, map[string]any{"user_agent": "delivered-agent-must-not-be-kept", "sending_ip": "198.51.100.5"})
	hook.send("opened", ticketMID, t0.Add(time.Minute), map[string]any{
		"user_agent": uaOpen, "sending_ip": "198.51.100.5", "device_used": "DESKTOP"})
	hook.send("proxy_open", ticketMID, t0.Add(2*time.Minute), map[string]any{"user_agent": uaProxy})
	hook.send("click", ticketMID, t0.Add(3*time.Minute), map[string]any{
		"user_agent": uaClick, "link": "https://tickets.example.com/t/abcdefghijklmnopqrstuvwxyz0123?utm=1"})
	// An open without a user agent stores NULL, a redelivery stays one row.
	hook.send("opened", ticketMID, t0.Add(4*time.Minute), nil)
	hook.send("opened", ticketMID, t0.Add(time.Minute), map[string]any{"user_agent": uaOpen})

	// ── what is stored ────────────────────────────────────────────────────
	rows, err := f.pool.Query(ctx, `SELECT event, user_agent FROM email_letter_interactions
		WHERE letter_id = $1 ORDER BY occurred_at`, ticketLetter)
	if err != nil {
		t.Fatalf("read interactions: %v", err)
	}
	type stored struct {
		event string
		ua    *string
	}
	var got []stored
	for rows.Next() {
		var s stored
		if err := rows.Scan(&s.event, &s.ua); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, s)
	}
	rows.Close()
	want := []struct{ event, ua string }{{"opened", uaOpen}, {"proxy_open", uaProxy}, {"click", uaClick}, {"opened", ""}}
	if len(got) != len(want) {
		t.Fatalf("stored %d interactions, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].event != w.event || (w.ua == "") != (got[i].ua == nil) || (w.ua != "" && *got[i].ua != w.ua) {
			t.Errorf("interaction %d = %q / %v, want %q / %q", i, got[i].event, got[i].ua, w.event, w.ua)
		}
	}
	// The user agent lives on the interaction row only.
	var leaked int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM email_letter_events
		WHERE letter_id = $1 AND (payload::text LIKE '%U-7%' OR payload::text LIKE '%198.51.100.5%' OR payload::text LIKE '%delivered-agent%')`,
		ticketLetter).Scan(&leaked); err != nil || leaked != 0 {
		t.Fatalf("email_letter_events payload carries the browser or an address: %d rows, err %v", leaked, err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE metadata::text LIKE '%U-7%'`).Scan(&leaked); err != nil || leaked != 0 {
		t.Fatalf("audit rows carry the browser: %d, err %v", leaked, err)
	}

	// ── the organization reads it ─────────────────────────────────────────
	path := "/v1/organizations/" + f.orgA.String() + "/orders/" + f.paidOrder.String() + "/letter-interactions"
	resp := integDoRequest(t, f.ts.Client(), http.MethodGet, f.ts.URL+path, f.managerTok, "")
	raw := integReadBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET letter-interactions: %d %s", resp.StatusCode, raw)
	}
	var wire letterInteractionsWire
	if err := json.Unmarshal([]byte(raw), &wire); err != nil {
		t.Fatalf("decode: %v %s", err, raw)
	}
	if wire.OrderID != f.paidOrder.String() || len(wire.Letters) != 2 {
		t.Fatalf("answer = %+v", wire)
	}
	byID := map[string]int{}
	for i, l := range wire.Letters {
		byID[l.LetterID] = i
	}
	tl := wire.Letters[byID[ticketLetter.String()]]
	if tl.Kind != emailstatus.KindTicket || len(tl.Interactions) != 4 {
		t.Fatalf("ticket letter = %+v", tl)
	}
	if tl.Interactions[0].Event != "opened" || tl.Interactions[0].UserAgent == nil || *tl.Interactions[0].UserAgent != uaOpen {
		t.Errorf("first interaction = %+v", tl.Interactions[0])
	}
	if c := tl.Interactions[2]; c.Event != "click" || c.UserAgent == nil || *c.UserAgent != uaClick ||
		c.Link == nil || *c.Link != "https://tickets.example.com/t/{token}" {
		t.Errorf("click = %+v", c)
	}
	if tl.Interactions[3].UserAgent != nil {
		t.Errorf("an open without a user agent answered %q", *tl.Interactions[3].UserAgent)
	}
	if ql := wire.Letters[byID[quietLetter.String()]]; len(ql.Interactions) != 0 {
		t.Errorf("an unopened letter lists %d interactions", len(ql.Interactions))
	}
	if strings.Contains(raw, "198.51.100.5") {
		t.Errorf("the answer carries the sender's address: %s", raw)
	}

	// ── another organization: the route's own 404, no data ───────────────
	resp = integDoRequest(t, f.ts.Client(), http.MethodGet,
		f.ts.URL+"/v1/organizations/"+f.orgB.String()+"/orders/"+f.paidOrder.String()+"/letter-interactions", f.managerTok, "")
	raw = integReadBody(t, resp)
	if resp.StatusCode != http.StatusNotFound || strings.Contains(raw, "U-7") {
		t.Fatalf("org B read org A's letter interactions: %d %s", resp.StatusCode, raw)
	}
	// An agent (no order.read in this fixture's gate) never gets data either.
	resp = integDoRequest(t, f.ts.Client(), http.MethodGet, f.ts.URL+path, f.agentTok, "")
	raw = integReadBody(t, resp)
	if resp.StatusCode == http.StatusOK && strings.Contains(raw, "U-7") {
		t.Fatalf("the agent read the browser data: %s", raw)
	}

	// ── an organization API key is refused ────────────────────────────────
	var managerID uuid.UUID
	if err := f.pool.QueryRow(ctx, `SELECT user_id FROM memberships WHERE org_id = $1 AND role = 'organizer' LIMIT 1`, f.orgA).Scan(&managerID); err != nil {
		t.Fatalf("find the manager: %v", err)
	}
	_, rawKey, err := apikeys.Issue(ctx, apikeys.NewStoreFromQueries(gen.New(f.pool)), apikeys.IssueInput{
		OrgID: f.orgA, Name: "u7-" + uuid.NewString()[:6], Scopes: []string{"order.read"}, CreatedBy: managerID,
	})
	if err != nil {
		t.Fatalf("apikeys.Issue: %v", err)
	}
	resp = integDoRequest(t, f.ts.Client(), http.MethodGet, f.ts.URL+path, rawKey, "")
	raw = integReadBody(t, resp)
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(raw, "orders.letter_interactions_api_key_forbidden") || strings.Contains(raw, "U-7") {
		t.Fatalf("API key read: %d %s", resp.StatusCode, raw)
	}
}
