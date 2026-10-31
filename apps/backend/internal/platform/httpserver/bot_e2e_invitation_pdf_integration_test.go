//go:build integration

package httpserver

// INV-09 (spec 46, owner decision 2026-10-29): a staff invitation issued from
// Telegram reaches the issuer's own chat as a PDF document sent by the bot.
// Telegram's WebApp.downloadFile is not used and no unauthenticated or
// short-lived PDF address exists: the bot claims the invitation under its
// service token (each comes once), fetches tickets.pdf as the issuer and
// sends it with sendDocument.
//
//   - TestInvitationPDFNotices_ClaimIsOnceOnlyForTelegramOnlyIssuedOnlyLinked
//     proves the claim route itself, without a bot: only an invitation issued
//     by the bot or by a Mini App session, by a user with a live link, with
//     EVERY ticket issued, and only once.
//   - TestBotE2E_InvitationPDF_ReachesTheIssuersChat runs the real bot against
//     the stub Telegram: the bot's own GA flow and a Mini App issue each end
//     in `[document invitation-<n>.pdf]` in the issuer's chat, a web issue
//     ends in nothing, and nothing is sent twice.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/eventbot"
)

// invIssued is the answer of the issue route the tests use.
type invIssued struct {
	orderID  uuid.UUID
	systemID int64
}

// invToken is an access token of the user behind a Telegram account. channel
// "" is an ordinary sign-in, audit.ChannelEventCenterTG a Mini App session.
func (e *invE2E) invToken(tgID int64, channel string) string {
	e.t.Helper()
	var tok string
	var err error
	if channel == "" {
		tok, err = eventbot.NewTokenMinter(botTestJWTSecret, botTestJWTIssuer, botTestJWTAudience).Mint(e.userOf(tgID))
	} else {
		tok, _, err = auth.IssueSessionJWT(botTestJWTSecret, e.userOf(tgID), botTestJWTIssuer, botTestJWTAudience,
			30*time.Minute, auth.SessionClaims{Channel: channel})
	}
	if err != nil {
		e.t.Fatal(err)
	}
	return tok
}

// issueWith issues a one-guest invitation on the VIP category with the given
// token and X-Client-Channel header ("" = none, a plain web request).
func (e *invE2E) issueWith(token, header, guest string, qty int) invIssued {
	return e.issueIn(e.seed.vip, token, header, guest, qty)
}

// issueIn is issueWith on a chosen category.
func (e *invE2E) issueIn(tier uuid.UUID, token, header, guest string, qty int) invIssued {
	e.t.Helper()
	raw, _ := json.Marshal(map[string]any{
		"request_id": uuid.NewString(),
		"ga":         []map[string]any{{"tier_id": tier.String(), "qty": qty}},
		"guest":      map[string]any{"name": guest},
		"delivery":   "pdf_only",
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/organizations/"+e.seed.orgID.String()+"/sessions/"+e.seed.session.String()+"/invitations", bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	if header != "" {
		req.Header.Set(audit.HeaderClientChannel, header)
	}
	w := httptest.NewRecorder()
	e.srv.router.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		e.t.Fatalf("issue %q: %d %s", guest, w.Code, w.Body.String())
	}
	var out struct {
		OrderID  uuid.UUID `json:"order_id"`
		SystemID int64     `json:"system_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		e.t.Fatal(err)
	}
	return invIssued{out.OrderID, out.SystemID}
}

// via is orders.metadata.invitation.via and whether pdf_notified_at is set.
func (e *invE2E) invMeta(orderID uuid.UUID) (via string, notified bool) {
	e.t.Helper()
	if err := e.pool.QueryRow(context.Background(), `
		SELECT COALESCE(metadata->'invitation'->>'via', ''), metadata->'invitation'->>'pdf_notified_at' IS NOT NULL
		FROM orders WHERE id = $1`, orderID).Scan(&via, &notified); err != nil {
		e.t.Fatal(err)
	}
	return via, notified
}

// claim calls the bot's claim route under the service token.
func (e *invE2E) claim() []map[string]any {
	e.t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/bot/invitations/pdf-notices/claim", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+botTestServiceToken)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.srv.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		e.t.Fatalf("claim: %d %s", w.Code, w.Body.String())
	}
	var out struct {
		Notices []map[string]any `json:"notices"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		e.t.Fatal(err)
	}
	return out.Notices
}

func claimedOrders(ns []map[string]any) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, n := range ns {
		out[fmt.Sprint(n["order_id"])] = n
	}
	return out
}

// newInviteAPIOnly is newInviteE2E without the bot: the claim route is driven
// by hand, so nothing consumes the notices.
func newInviteAPIOnly(t *testing.T) *invE2E {
	t.Helper()
	pool := onboardingIntegrationPool(t)
	e := &invE2E{t: t, pool: pool}
	e.f = newBotInviteFixture(t, pool)
	e.srv = buildBotIntegrationServerCfg(t, pool, nil)
	e.api = httptest.NewServer(e.srv.router)
	t.Cleanup(e.api.Close)
	e.seed = seedInviteBotOrg(t, pool, e.f.orgID)
	linkECBotUser(t, e.f, e.srv, ecOwnerTG, "owner")
	linkECBotUser(t, e.f, e.srv, ecManagerTG, "manager")
	return e
}

func TestInvitationPDFNotices_ClaimIsOnceOnlyForTelegramOnlyIssuedOnlyLinked(t *testing.T) {
	e := newInviteAPIOnly(t)
	owner := e.invToken(ecOwnerTG, "")
	ownerMini := e.invToken(ecOwnerTG, audit.ChannelEventCenterTG)
	manager := e.invToken(ecManagerTG, "")

	// The service token guards the route.
	req := httptest.NewRequest(http.MethodPost, "/v1/bot/invitations/pdf-notices/claim", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.srv.router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("claim without the service token: %d, want 401", w.Code)
	}

	fromBot := e.issueWith(owner, audit.ChannelTelegramBot, "Via Bot", 1)
	fromMini := e.issueWith(ownerMini, audit.ChannelEventCenterWeb, "Via Mini App", 1)
	fromWeb := e.issueWith(owner, audit.ChannelEventCenterWeb, "Via Web", 1)
	plain := e.issueWith(owner, "", "Via Nothing", 1)
	two := e.issueWith(owner, audit.ChannelTelegramBot, "Two Places", 2)
	// The manager's link is revoked: nobody to send the file to.
	if _, err := e.pool.Exec(context.Background(), `UPDATE bot_telegram_links SET revoked_at = now() WHERE telegram_user_id = $1`, ecManagerTG); err != nil {
		t.Fatal(err)
	}
	unlinked := e.issueIn(e.seed.std, manager, audit.ChannelTelegramBot, "Unlinked Issuer", 1)

	for _, c := range []struct {
		name string
		o    invIssued
		via  string
	}{
		{"bot", fromBot, "telegram_bot"},
		{"mini app (named by its session, not by the header)", fromMini, "telegram_miniapp"},
		{"web", fromWeb, ""},
		{"no header", plain, ""},
		{"two places", two, "telegram_bot"},
		{"issuer without a link", unlinked, "telegram_bot"},
	} {
		if via, _ := e.invMeta(c.o.orderID); via != c.via {
			t.Errorf("%s: metadata.invitation.via = %q, want %q", c.name, via, c.via)
		}
	}

	// No ticket exists yet: nothing is handed out.
	if got := e.claim(); len(got) != 0 {
		t.Fatalf("claimed before the tickets are issued: %v", got)
	}

	e.mintTickets()

	// One place of the two-place invitation is not issued yet: it waits.
	ctx := context.Background()
	var itemID, ticketID uuid.UUID
	if err := e.pool.QueryRow(ctx, `SELECT id, ticket_id FROM order_items WHERE order_id = $1 ORDER BY ordinal DESC LIMIT 1`, two.orderID).Scan(&itemID, &ticketID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE order_items SET ticket_id = NULL WHERE id = $1`, itemID); err != nil {
		t.Fatal(err)
	}
	got := claimedOrders(e.claim())
	if _, ok := got[two.orderID.String()]; ok {
		t.Fatalf("a half-issued invitation was handed out: %v", got)
	}
	// The complete ones went with that claim already.
	if len(got) != 2 {
		t.Fatalf("claimed %d invitations, want the bot one and the Mini App one: %v", len(got), got)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE order_items SET ticket_id = $2 WHERE id = $1`, itemID, ticketID); err != nil {
		t.Fatal(err)
	}

	// Now exactly the Telegram-issued, fully issued, linked ones, once each.
	last := claimedOrders(e.claim())
	if len(last) != 1 {
		t.Fatalf("claimed %d invitations once the last place was issued, want the two-place one: %v", len(last), last)
	}
	for k, v := range last {
		got[k] = v
	}
	owner2 := int64(ecOwnerTG)
	for _, c := range []struct {
		name  string
		o     invIssued
		guest string
	}{{"bot", fromBot, "Via Bot"}, {"mini app", fromMini, "Via Mini App"}, {"two places", two, "Two Places"}} {
		n, ok := got[c.o.orderID.String()]
		if !ok {
			t.Errorf("%s invitation was not handed out", c.name)
			continue
		}
		if int64(n["system_id"].(float64)) != c.o.systemID || int64(n["telegram_user_id"].(float64)) != owner2 ||
			n["guest_name"] != c.guest || n["org_id"] != e.seed.orgID.String() || n["event_name"] != e.seed.eventName {
			t.Errorf("%s notice = %v", c.name, n)
		}
		for _, leak := range []string{"email", "guest_email", "phone", "buyer_email"} {
			if _, has := n[leak]; has {
				t.Errorf("%s notice carries %q: %v", c.name, leak, n)
			}
		}
	}
	for _, c := range []struct {
		name     string
		o        invIssued
		notified bool
	}{{"bot", fromBot, true}, {"mini app", fromMini, true}, {"two places", two, true},
		{"web", fromWeb, false}, {"no header", plain, false}, {"unlinked issuer", unlinked, false}} {
		if _, n := e.invMeta(c.o.orderID); n != c.notified {
			t.Errorf("%s: pdf_notified_at set = %v, want %v", c.name, n, c.notified)
		}
	}
	if again := e.claim(); len(again) != 0 {
		t.Fatalf("the second claim handed out %v, each invitation comes once", again)
	}
}

func TestBotE2E_InvitationPDF_ReachesTheIssuersChat(t *testing.T) {
	e := newInviteE2EWith(t, nil, func(o *eventbot.Options) { o.NoticeEvery = 200 * time.Millisecond })
	d := e.driver(ecOwnerTG)
	docName := func(o invIssued) string { return fmt.Sprintf("invitation-%d.pdf", o.systemID) }

	// ── the bot's own general-admission flow ────────────────────────────────
	anna := e.email("anna")
	e.toCategory(d, 0)
	d.say("1", "Осталось указать: 1")
	d.say("Anna Test, "+anna, "Отправить приглашения?")
	d.press("iv:go", "Выдано приглашений: 1")
	inv := e.staffInvs()[anna]
	if inv.sysID == 0 {
		t.Fatalf("no invitation of %s: %+v", anna, e.staffInvs())
	}
	fromBot := invIssued{inv.orderID, inv.sysID}
	if via, _ := e.invMeta(fromBot.orderID); via != "telegram_bot" {
		t.Fatalf("an invitation issued in the chat has via %q", via)
	}

	// A Mini App issue, a web issue and an issue by the same person that
	// names no Telegram client at all.
	fromMini := e.issueWith(e.invToken(ecOwnerTG, audit.ChannelEventCenterTG), audit.ChannelEventCenterWeb, "Mini Guest", 1)
	fromWeb := e.issueWith(e.invToken(ecOwnerTG, ""), audit.ChannelEventCenterWeb, "Web Guest", 1)

	// The tickets are not issued yet: the chat gets nothing.
	time.Sleep(time.Second)
	if n := e.tg.callCount("sendDocument"); n != 0 {
		t.Fatalf("%d documents sent before the tickets exist", n)
	}

	e.mintTickets()

	for _, o := range []invIssued{fromBot, fromMini} {
		line := e.tg.waitFor(t, "[document "+docName(o)+"]")
		if !strings.Contains(line, fmt.Sprintf("№ %d", o.systemID)) || !strings.Contains(line, "Invite Show") {
			t.Errorf("caption of %s: %q", docName(o), line)
		}
		if chat, ok := e.tg.documentChat(docName(o)); !ok || chat != ecOwnerTG {
			t.Errorf("%s went to chat %d, want the issuer's %d", docName(o), chat, ecOwnerTG)
		}
		if pdf, ok := e.tg.document(docName(o)); !ok || !bytes.HasPrefix(pdf, []byte("%PDF-")) {
			t.Errorf("%s is not a PDF: %d bytes", docName(o), len(pdf))
		}
	}
	if line := e.tg.waitFor(t, "[document "+docName(fromBot)+"]"); !strings.Contains(line, "Anna Test") {
		t.Errorf("the caption does not name the guest: %q", line)
	}

	// A web issue is not sent to Telegram, and nothing is sent twice.
	time.Sleep(time.Second)
	if _, ok := e.tg.document(docName(fromWeb)); ok {
		t.Errorf("a web-issued invitation was sent to Telegram")
	}
	if n := e.tg.callCount("sendDocument"); n != 2 {
		t.Errorf("%d documents sent, want exactly 2", n)
	}
	if _, notified := e.invMeta(fromWeb.orderID); notified {
		t.Error("a web-issued invitation was claimed")
	}
}
