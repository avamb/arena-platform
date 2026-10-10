//go:build integration

package httpserver

// Revoking a bot invitation and sending its letter again (spec 35 EC-16)
// through the real router with real JWTs: the permission gates (an owner may,
// a manager gets 403, no token 401), the tenant rules (another organization's
// invitation is the route's own 404, a path organization the caller does not
// belong to is 403), and what "takes part in nothing else" means in code - one
// scenario per reason a membership stays: it was accepted, the person was a
// member before, another invitation still stands, they already work here
// through the bot, the owner changed their role, they are the last owner; and
// the one case where it goes: never accepted, nothing else. The resend: a new
// code (the old one stops working), the ten-minute limit, the refusals for an
// accepted or revoked invitation, an expired one revived, and the team list's
// accepted / waiting / expired states.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/eventbot"
)

type invManage struct {
	t    *testing.T
	pool *pgxpool.Pool
	f    *botInviteFixture
	srv  *Server
}

func newInvManage(t *testing.T) *invManage {
	t.Helper()
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	return &invManage{t: t, pool: pool, f: f, srv: buildBotIntegrationServer(t, pool)}
}

// call makes a request through the router as a user (a minted JWT: no roles
// claim, the permissions come from the memberships) or, with a nil user,
// without any token.
func (m *invManage) call(user *uuid.UUID, method, path string, body any) (int, http.Header, []byte) {
	m.t.Helper()
	var rd io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if user != nil {
		tok, err := eventbot.NewTokenMinter(botTestJWTSecret, botTestJWTIssuer, botTestJWTAudience).Mint(*user)
		if err != nil {
			m.t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	w := httptest.NewRecorder()
	m.srv.router.ServeHTTP(w, req)
	return w.Code, w.Header(), w.Body.Bytes()
}

func (m *invManage) invPath(org, inv uuid.UUID, suffix string) string {
	return "/v1/organizations/" + org.String() + "/bot-invitations/" + inv.String() + suffix
}

// invite creates an invitation through the real route and returns its id and
// the person's user id.
func (m *invManage) invite(f *botInviteFixture, email, role string) (inv, user uuid.UUID) {
	m.t.Helper()
	rec := f.invite(m.srv, f.ownerID, fmt.Sprintf(`{"email":%q,"role":%q,"locale":"es"}`, email, role))
	if rec.Code != http.StatusCreated {
		m.t.Fatalf("invite %s: %d %s", email, rec.Code, rec.Body.String())
	}
	var out struct {
		Invitation struct {
			ID     uuid.UUID `json:"id"`
			UserID uuid.UUID `json:"user_id"`
		} `json:"invitation"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		m.t.Fatal(err)
	}
	return out.Invitation.ID, out.Invitation.UserID
}

func (m *invManage) revoke(inv uuid.UUID) (int, map[string]any) {
	m.t.Helper()
	code, _, body := m.call(&m.f.ownerID, http.MethodDelete, m.invPath(m.f.orgID, inv, ""), nil)
	out := map[string]any{}
	_ = json.Unmarshal(body, &out)
	return code, out
}

func (m *invManage) hasMembership(user uuid.UUID) bool {
	m.t.Helper()
	var n int
	if err := m.pool.QueryRow(context.Background(), `SELECT count(*) FROM memberships WHERE user_id = $1 AND org_id = $2`, user, m.f.orgID).Scan(&n); err != nil {
		m.t.Fatal(err)
	}
	return n > 0
}

func (m *invManage) exec(sql string, args ...any) {
	m.t.Helper()
	if _, err := m.pool.Exec(context.Background(), sql, args...); err != nil {
		m.t.Fatalf("exec %q: %v", sql, err)
	}
}

func errCodeOf(body []byte) string {
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &env)
	return env.Error.Code
}

// acceptCode redeems a code for a Telegram account and answers the status.
func (m *invManage) acceptCode(code, email string, tg int64) int {
	m.t.Helper()
	t := m.t
	t.Cleanup(func() {
		_, _ = m.pool.Exec(context.Background(), `DELETE FROM bot_telegram_links WHERE telegram_user_id = $1`, tg)
	})
	return m.f.accept(m.srv, botTestServiceToken, fmt.Sprintf(`{"code":%q,"email":%q,"telegram_user_id":%d}`, code, email, tg)).Code
}

// A never-accepted invitation: revoking annuls the code AND removes the
// membership it created; the second revoke is a 409; the audit row exists.
func TestBotInvitationRevoke_NeverAcceptedRemovesTheMembership(t *testing.T) {
	m := newInvManage(t)
	email := m.f.newEmail("never")
	inv, user := m.invite(m.f, email, "manager")
	code, _ := m.f.queuedCode(email)
	if !m.hasMembership(user) {
		t.Fatal("the invitation must create the membership at once")
	}
	var created bool
	if err := m.pool.QueryRow(context.Background(), `SELECT membership_created FROM bot_invitations WHERE id = $1`, inv).Scan(&created); err != nil || !created {
		t.Fatalf("membership_created = %v (%v), want true for a membership the invitation inserted", created, err)
	}

	status, out := m.revoke(inv)
	if status != http.StatusOK || out["revoked"] != true || out["membership_removed"] != true || out["kept_reason"] != nil {
		t.Fatalf("revoke: %d %v", status, out)
	}
	if m.hasMembership(user) {
		t.Error("the membership must be gone")
	}
	var revoked *time.Time
	if err := m.pool.QueryRow(context.Background(), `SELECT revoked_at FROM bot_invitations WHERE id = $1`, inv).Scan(&revoked); err != nil || revoked == nil {
		t.Errorf("revoked_at = %v (%v), the row is kept and marked", revoked, err)
	}
	// The code is dead.
	if got := m.acceptCode(code, email, 9100000001); got != http.StatusNotFound {
		t.Errorf("redeeming a revoked code: %d, want 404", got)
	}
	// Twice: 409. Audited.
	code2, _, body := m.call(&m.f.ownerID, http.MethodDelete, m.invPath(m.f.orgID, inv, ""), nil)
	if code2 != http.StatusConflict || errCodeOf(body) != "bot.invitation_revoked" {
		t.Errorf("second revoke: %d %s", code2, body)
	}
	var audits int
	if err := m.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_events WHERE action = 'v1.bot.invitation.revoke' AND resource_id = $1`, inv.String()).Scan(&audits); err != nil || audits != 1 {
		t.Errorf("audit rows = %d (%v), want 1", audits, err)
	}
	m.exec(`DELETE FROM audit_events WHERE resource_id = $1`, inv.String())
	// The user account stays (harmless, reusable by a later invitation).
	var users int
	if err := m.pool.QueryRow(context.Background(), `SELECT count(*) FROM users WHERE id = $1`, user).Scan(&users); err != nil || users != 1 {
		t.Errorf("the user account must stay: %d (%v)", users, err)
	}
}

// Every reason a membership stays after the invitation is revoked.
func TestBotInvitationRevoke_TheMembershipStaysWhen(t *testing.T) {
	m := newInvManage(t)
	ctx := context.Background()
	q := gen.New(m.pool)

	t.Run("it was accepted", func(t *testing.T) {
		email := m.f.newEmail("accepted")
		inv, user := m.invite(m.f, email, "manager")
		code, _ := m.f.queuedCode(email)
		if got := m.acceptCode(code, email, 9100000010); got != http.StatusOK {
			t.Fatalf("accept: %d", got)
		}
		status, out := m.revoke(inv)
		if status != http.StatusOK || out["membership_removed"] != false || out["kept_reason"] != "accepted" {
			t.Fatalf("revoke: %d %v", status, out)
		}
		if !m.hasMembership(user) {
			t.Error("an accepted member stays; the owner uses the remove-member flow")
		}
		link, err := q.GetBotTelegramLink(ctx, 9100000010)
		if err != nil || link.RevokedAt != nil {
			t.Errorf("the Telegram link must stay: %+v %v", link, err)
		}
	})

	t.Run("the person was a member before", func(t *testing.T) {
		email := m.f.newEmail("before")
		u, err := q.InsertUser(ctx, email, "x", "en")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := q.InsertMembership(ctx, u.ID, m.f.orgID, "organizer"); err != nil {
			t.Fatal(err)
		}
		inv, _ := m.invite(m.f, email, "manager")
		var created bool
		if err := m.pool.QueryRow(ctx, `SELECT membership_created FROM bot_invitations WHERE id = $1`, inv).Scan(&created); err != nil || created {
			t.Fatalf("membership_created = %v (%v), want false: the invitation did not create it", created, err)
		}
		status, out := m.revoke(inv)
		if status != http.StatusOK || out["membership_removed"] != false || out["kept_reason"] != "member_before" {
			t.Fatalf("revoke: %d %v", status, out)
		}
		if !m.hasMembership(u.ID) {
			t.Error("an earlier member keeps the membership")
		}
	})

	t.Run("another invitation still stands", func(t *testing.T) {
		email := m.f.newEmail("twice")
		first, user := m.invite(m.f, email, "manager")
		second, _ := m.invite(m.f, email, "manager")
		status, out := m.revoke(first)
		if status != http.StatusOK || out["membership_removed"] != false || out["kept_reason"] != "other_invitation" {
			t.Fatalf("revoke of the first: %d %v", status, out)
		}
		if !m.hasMembership(user) {
			t.Fatal("the second invitation still keeps them in")
		}
		status, out = m.revoke(second)
		if status != http.StatusOK || out["membership_removed"] != true {
			t.Fatalf("revoke of the second: %d %v", status, out)
		}
		if m.hasMembership(user) {
			t.Error("with no invitation left, the membership goes")
		}
	})

	t.Run("they already work here through the bot", func(t *testing.T) {
		email := m.f.newEmail("busy")
		inv, user := m.invite(m.f, email, "manager")
		// A Telegram link from elsewhere whose current organization is this one.
		const tg = 9100000020
		org := m.f.orgID
		if _, err := q.UpsertBotTelegramLink(ctx, tg, user, nil, "en", &org); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = m.pool.Exec(ctx, `DELETE FROM bot_telegram_links WHERE telegram_user_id = $1`, tg) })
		status, out := m.revoke(inv)
		if status != http.StatusOK || out["membership_removed"] != false || out["kept_reason"] != "in_use" {
			t.Fatalf("revoke: %d %v", status, out)
		}
		if !m.hasMembership(user) {
			t.Error("a person who already works here keeps the membership")
		}
	})

	t.Run("a wizard draft exists in the organization", func(t *testing.T) {
		email := m.f.newEmail("draft")
		inv, user := m.invite(m.f, email, "manager")
		const tg = 9100000021
		if _, err := q.UpsertBotTelegramLink(ctx, tg, user, nil, "en", nil); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = m.pool.Exec(ctx, `DELETE FROM bot_telegram_links WHERE telegram_user_id = $1`, tg) })
		m.exec(`INSERT INTO bot_drafts (telegram_user_id, org_id, mode, step, state) VALUES ($1, $2, 'create', 'name', '{}'::jsonb)`, tg, m.f.orgID)
		status, out := m.revoke(inv)
		if status != http.StatusOK || out["kept_reason"] != "in_use" {
			t.Fatalf("revoke: %d %v", status, out)
		}
	})

	t.Run("the owner changed the role after inviting", func(t *testing.T) {
		email := m.f.newEmail("promoted")
		inv, user := m.invite(m.f, email, "manager")
		m.exec(`UPDATE memberships SET role = 'agent' WHERE user_id = $1 AND org_id = $2`, user, m.f.orgID)
		status, out := m.revoke(inv)
		if status != http.StatusOK || out["membership_removed"] != false || out["kept_reason"] != "role_changed" {
			t.Fatalf("revoke: %d %v", status, out)
		}
		if !m.hasMembership(user) {
			t.Error("a membership the owner changed on purpose stays")
		}
	})

	t.Run("they are the last owner", func(t *testing.T) {
		// B is invited as an owner while A (the fixture's owner) is demoted, so
		// B would be the only org_admin; the caller is a member, so the tenant
		// guard passes, and the handler is called directly (the permission
		// middleware is covered by the router tests above).
		email := m.f.newEmail("last-owner")
		inv, user := m.invite(m.f, email, "owner")
		m.exec(`UPDATE memberships SET role = 'organizer' WHERE user_id = $1 AND org_id = $2`, m.f.ownerID, m.f.orgID)
		t.Cleanup(func() {
			m.exec(`UPDATE memberships SET role = 'org_admin' WHERE user_id = $1 AND org_id = $2`, m.f.ownerID, m.f.orgID)
		})

		req := httptest.NewRequest(http.MethodDelete, m.invPath(m.f.orgID, inv, ""), nil)
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("org_id", m.f.orgID.String())
		rctx.URLParams.Add("id", inv.String())
		c := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
		c = auth.WithActor(c, auth.Actor{ID: m.f.ownerID.String(), Type: auth.ActorTypeUser})
		// The fixture owner was demoted above, so the call is the operator's:
		// a platform superadmin naming a reason (SEC-1 requires membership.revoke).
		c = auth.WithSuperadminOrgAccess(c)
		req.Header.Set("X-Admin-Reason", "integration: last owner guard")
		rec := httptest.NewRecorder()
		m.srv.handleRevokeBotInvitation(rec, req.WithContext(c))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"kept_reason":"last_owner"`) {
			t.Fatalf("revoke: %d %s", rec.Code, rec.Body.String())
		}
		if !m.hasMembership(user) {
			t.Error("the organization must not be left without an owner")
		}
	})
}

// An invitation that expired without being opened is "expired" in the team
// list and can still be revoked (the membership goes) or resent (revived).
func TestBotInvitationResend_NewCodeLimitRefusalsAndStates(t *testing.T) {
	m := newInvManage(t)
	ctx := context.Background()

	waiting := m.f.newEmail("waiting")
	waitingInv, waitingUser := m.invite(m.f, waiting, "manager")
	oldCode, _ := m.f.queuedCode(waiting)

	// ── the ten-minute limit: a fresh invitation counts as just sent ────────
	status, hdr, body := m.call(&m.f.ownerID, http.MethodPost, m.invPath(m.f.orgID, waitingInv, "/resend"), nil)
	if status != http.StatusTooManyRequests || errCodeOf(body) != "bot.invitation_resend_too_soon" {
		t.Fatalf("resend right after the invitation: %d %s, want 429", status, body)
	}
	if ra, err := strconv.Atoi(hdr.Get("Retry-After")); err != nil || ra < 500 || ra > 601 {
		t.Errorf("Retry-After = %q, want about 600 seconds", hdr.Get("Retry-After"))
	}
	var env struct {
		Error struct {
			Details struct {
				Retry int `json:"retry_after_seconds"`
			} `json:"details"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &env)
	if env.Error.Details.Retry < 500 {
		t.Errorf("retry_after_seconds = %d", env.Error.Details.Retry)
	}
	jobs := func(email string) int {
		var n int
		if err := m.pool.QueryRow(ctx, `SELECT count(*) FROM worker_jobs WHERE job_type = 'bot.invitation_email' AND payload->>'email' = $1`, email).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if jobs(waiting) != 1 {
		t.Fatalf("a refused resend queued a letter: %d jobs", jobs(waiting))
	}

	// ── a resend after the interval: a NEW code, a fresh life, one more job ─
	m.exec(`UPDATE bot_invitations SET last_sent_at = now() - interval '11 minutes' WHERE id = $1`, waitingInv)
	status, _, body = m.call(&m.f.ownerID, http.MethodPost, m.invPath(m.f.orgID, waitingInv, "/resend"), map[string]string{"locale": "ru"})
	if status != http.StatusOK {
		t.Fatalf("resend: %d %s", status, body)
	}
	if strings.Contains(string(body), "\"code\"") || strings.Contains(string(body), "deep_link") {
		t.Errorf("an owner never receives the code: %s", body)
	}
	if jobs(waiting) != 2 {
		t.Fatalf("jobs = %d, want a second letter", jobs(waiting))
	}
	newCode, payload := m.f.queuedCode(waiting)
	if newCode == "" || newCode == oldCode || payload.Locale != "ru" {
		t.Fatalf("new code %q (old %q) payload %+v: the resend must mint a different code", newCode, oldCode, payload)
	}
	var expires time.Time
	if err := m.pool.QueryRow(ctx, `SELECT expires_at FROM bot_invitations WHERE id = $1`, waitingInv).Scan(&expires); err != nil {
		t.Fatal(err)
	}
	if d := time.Until(expires); d < 6*24*time.Hour+23*time.Hour || d > 7*24*time.Hour+time.Minute {
		t.Errorf("expiry in %v, want a fresh 7 days", d)
	}
	if got := m.acceptCode(oldCode, waiting, 9100000030); got != http.StatusNotFound {
		t.Errorf("the OLD code redeemed: %d, want 404", got)
	}
	// And straight away again: too soon.
	if status, _, body = m.call(&m.f.ownerID, http.MethodPost, m.invPath(m.f.orgID, waitingInv, "/resend"), nil); status != http.StatusTooManyRequests {
		t.Errorf("second resend: %d %s, want 429", status, body)
	}

	// ── team list states ────────────────────────────────────────────────────
	expired := m.f.newEmail("expired")
	expiredInv, expiredUser := m.invite(m.f, expired, "manager")
	m.exec(`UPDATE bot_invitations SET expires_at = now() - interval '1 day', last_sent_at = now() - interval '8 days' WHERE id = $1`, expiredInv)
	accepted := m.f.newEmail("joined")
	acceptedInv, acceptedUser := m.invite(m.f, accepted, "manager")
	acceptedCode, _ := m.f.queuedCode(accepted)
	if got := m.acceptCode(acceptedCode, accepted, 9100000031); got != http.StatusOK {
		t.Fatalf("accept: %d", got)
	}
	team := func() map[uuid.UUID]map[string]any {
		status, _, body := m.call(&m.f.ownerID, http.MethodGet, "/v1/organizations/"+m.f.orgID.String()+"/bot-team", nil)
		if status != http.StatusOK {
			t.Fatalf("team: %d %s", status, body)
		}
		var out struct {
			Members []map[string]any `json:"members"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatal(err)
		}
		byUser := map[uuid.UUID]map[string]any{}
		for _, mem := range out.Members {
			byUser[uuid.MustParse(mem["user_id"].(string))] = mem
		}
		return byUser
	}
	tm := team()
	for name, want := range map[string]struct {
		user  uuid.UUID
		inv   uuid.UUID
		state string
	}{"waiting": {waitingUser, waitingInv, "waiting"}, "expired": {expiredUser, expiredInv, "expired"}, "accepted": {acceptedUser, acceptedInv, "accepted"}} {
		mem := tm[want.user]
		if mem["invitation_state"] != want.state || mem["invitation_id"] != want.inv.String() {
			t.Errorf("%s: member = %v, want state %s with invitation %s", name, mem, want.state, want.inv)
		}
	}
	if tm[waitingUser]["resend_available_at"] == nil {
		t.Error("a waiting invitation whose letter just went out carries resend_available_at")
	}
	if tm[expiredUser]["resend_available_at"] != nil || tm[expiredUser]["invitation_pending"] != false {
		t.Errorf("an expired invitation may be resent at once and is not pending: %v", tm[expiredUser])
	}
	if tm[acceptedUser]["resend_available_at"] != nil || tm[m.f.ownerID]["invitation_state"] != nil {
		t.Errorf("an accepted member has no resend; the owner has no invitation: %v %v", tm[acceptedUser], tm[m.f.ownerID])
	}

	// ── an expired one is revived by a resend ───────────────────────────────
	status, _, body = m.call(&m.f.ownerID, http.MethodPost, m.invPath(m.f.orgID, expiredInv, "/resend"), nil)
	if status != http.StatusOK {
		t.Fatalf("resend of an expired invitation: %d %s", status, body)
	}
	if tm = team(); tm[expiredUser]["invitation_state"] != "waiting" {
		t.Errorf("after the resend: %v, want waiting", tm[expiredUser])
	}
	newExpiredCode, _ := m.f.queuedCode(expired)
	if got := m.acceptCode(newExpiredCode, expired, 9100000032); got != http.StatusOK {
		t.Errorf("the revived invitation did not redeem: %d", got)
	}

	// ── refusals: accepted, revoked ─────────────────────────────────────────
	if status, _, body = m.call(&m.f.ownerID, http.MethodPost, m.invPath(m.f.orgID, acceptedInv, "/resend"), nil); status != http.StatusConflict || errCodeOf(body) != "bot.invitation_accepted" {
		t.Errorf("resend of an accepted invitation: %d %s", status, body)
	}
	if status, _ := m.revoke(waitingInv); status != http.StatusOK {
		t.Fatalf("revoke: %d", status)
	}
	if status, _, body = m.call(&m.f.ownerID, http.MethodPost, m.invPath(m.f.orgID, waitingInv, "/resend"), nil); status != http.StatusConflict || errCodeOf(body) != "bot.invitation_revoked" {
		t.Errorf("resend of a revoked invitation: %d %s", status, body)
	}
	var resendAudits int
	_ = m.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE action = 'v1.bot.invitation.resend' AND resource_id = ANY($1)`, []string{waitingInv.String(), expiredInv.String()}).Scan(&resendAudits)
	if resendAudits != 2 {
		t.Errorf("resend audit rows = %d, want 2", resendAudits)
	}
	m.exec(`DELETE FROM audit_events WHERE resource_id = ANY($1)`, []string{waitingInv.String(), expiredInv.String()})
}

// An expired invitation that was never opened can be revoked: the membership goes.
func TestBotInvitationRevoke_ExpiredNeverOpenedStillRemoves(t *testing.T) {
	m := newInvManage(t)
	email := m.f.newEmail("lapsed")
	inv, user := m.invite(m.f, email, "manager")
	m.exec(`UPDATE bot_invitations SET expires_at = now() - interval '2 days' WHERE id = $1`, inv)
	status, out := m.revoke(inv)
	if status != http.StatusOK || out["membership_removed"] != true || m.hasMembership(user) {
		t.Fatalf("revoke: %d %v membership=%v", status, out, m.hasMembership(user))
	}
}

// Gates and tenants: the manager and an anonymous caller are refused on both
// routes, another organization's invitation is a 404 for the owner of this
// one, and a path organization the caller does not belong to is a 403.
func TestBotInvitationManage_GatesAndTenants(t *testing.T) {
	m := newInvManage(t)
	other := newBotInviteFixture(t, m.pool)

	managerEmail := m.f.newEmail("gate-manager")
	_, managerUser := m.invite(m.f, managerEmail, "manager")
	target := m.f.newEmail("gate-target")
	inv, targetUser := m.invite(m.f, target, "manager")
	foreign, foreignUser := m.invite(other, other.newEmail("foreign"), "manager")

	for _, route := range []struct{ method, suffix string }{{http.MethodDelete, ""}, {http.MethodPost, "/resend"}} {
		path := m.invPath(m.f.orgID, inv, route.suffix)
		// Anonymous: 401. The manager holds neither membership.grant nor
		// membership.revoke: 403.
		if status, _, _ := m.call(nil, route.method, path, nil); status != http.StatusUnauthorized {
			t.Errorf("%s %s without a token: %d, want 401", route.method, route.suffix, status)
		}
		if status, _, body := m.call(&managerUser, route.method, path, nil); status != http.StatusForbidden {
			t.Errorf("%s %s as a manager: %d %s, want 403", route.method, route.suffix, status, body)
		}
		// The owner of THIS organization naming another organization's
		// invitation in their own path: the route's own 404.
		fpath := m.invPath(m.f.orgID, foreign, route.suffix)
		if status, _, body := m.call(&m.f.ownerID, route.method, fpath, nil); status != http.StatusNotFound || errCodeOf(body) != "bot.invitation_not_found" {
			t.Errorf("%s %s of a foreign invitation: %d %s, want 404", route.method, route.suffix, status, body)
		}
		// An organization the caller does not belong to: refused at the gate.
		opath := m.invPath(other.orgID, foreign, route.suffix)
		if status, _, _ := m.call(&m.f.ownerID, route.method, opath, nil); status != http.StatusForbidden {
			t.Errorf("%s %s in another organization: %d, want 403", route.method, route.suffix, status)
		}
		// Not a UUID.
		if status, _, _ := m.call(&m.f.ownerID, route.method, "/v1/organizations/"+m.f.orgID.String()+"/bot-invitations/nope"+route.suffix, nil); status != http.StatusBadRequest {
			t.Errorf("%s %s with a bad id: %d, want 400", route.method, route.suffix, status)
		}
	}
	// Nothing changed anywhere.
	if !m.hasMembership(targetUser) || !m.hasMembership(managerUser) {
		t.Error("a refused call removed a membership")
	}
	var revoked int
	if err := m.pool.QueryRow(context.Background(), `SELECT count(*) FROM bot_invitations WHERE id = ANY($1) AND revoked_at IS NOT NULL`, []uuid.UUID{inv, foreign}).Scan(&revoked); err != nil || revoked != 0 {
		t.Errorf("revoked rows = %d (%v), want 0", revoked, err)
	}
	var fm int
	_ = m.pool.QueryRow(context.Background(), `SELECT count(*) FROM memberships WHERE user_id = $1 AND org_id = $2`, foreignUser, other.orgID).Scan(&fm)
	if fm != 1 {
		t.Error("the other organization's membership changed")
	}
}
