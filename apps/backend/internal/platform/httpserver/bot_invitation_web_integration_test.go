//go:build integration

package httpserver

// The web channel of the bot invitation (task 0-5 of spec 08_architecture/40):
// `channel: "web"` mails the web event center instead of the Telegram deep
// link. Covered through the real handler and a real database:
//
//   - a NEW e-mail: user, membership, an already-accepted invitation row, a
//     password_reset_tokens row whose key is SHA-256 of the token in an
//     auth.password_reset_email job (purpose org_invitation) written in the same
//     transaction, no bot.invitation_email job and no deep_link;
//   - an EXISTING member (organizer, owner asked for): the role stays, the
//     "added" letter is queued and no token is written;
//   - absent / `telegram` channel: the old behaviour, now reporting its channel;
//   - an unknown channel: 400, and a server without the event center address: 503,
//     both before anything is written;
//   - the lifecycle of a web invitation: team state "accepted", resend refused,
//     revoke keeps the membership.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/authemail"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/users"
)

const webInviteEventCenter = "https://events.bot.test"

type webInviteResponse struct {
	Invitation struct {
		ID             uuid.UUID `json:"id"`
		UserID         uuid.UUID `json:"user_id"`
		Email          string    `json:"email"`
		Role           string    `json:"role"`
		MembershipRole string    `json:"membership_role"`
		UserCreated    bool      `json:"user_created"`
		Channel        string    `json:"channel"`
	} `json:"invitation"`
	DeepLink *string `json:"deep_link"`
}

func decodeWebInvite(t *testing.T, body []byte) webInviteResponse {
	t.Helper()
	var out webInviteResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return out
}

func buildWebInviteServer(t *testing.T, f *botInviteFixture) *Server {
	t.Helper()
	return buildBotIntegrationServerCfg(t, f.pool, func(c *config.Config) {
		c.EventCenterPublicURL = webInviteEventCenter
	})
}

func (f *botInviteFixture) jobPayloads(jobType, email string) [][]byte {
	f.t.Helper()
	rows, err := f.pool.Query(context.Background(),
		`SELECT payload FROM worker_jobs WHERE job_type = $1 AND payload->>'email' = $2 ORDER BY created_at`, jobType, email)
	if err != nil {
		f.t.Fatal(err)
	}
	defer rows.Close()
	var out [][]byte
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			f.t.Fatal(err)
		}
		out = append(out, raw)
	}
	return out
}

func (f *botInviteFixture) count(sql string, args ...any) int {
	f.t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		f.t.Fatalf("%s: %v", sql, err)
	}
	return n
}

func TestBotInvitationWeb_NewEmailGetsAMembershipAndASetupLinkInOneTransaction(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	srv := buildWebInviteServer(t, f)

	email := f.newEmail("web-new")
	rec := f.invite(srv, f.ownerID, `{"email":"`+email+`","role":"manager","channel":"web","locale":"ru"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "deep_link") {
		t.Fatalf("a web invitation has no deep link: %s", rec.Body.String())
	}
	out := decodeWebInvite(t, rec.Body.Bytes())
	if out.Invitation.Channel != "web" || !out.Invitation.UserCreated || out.Invitation.MembershipRole != "organizer" || out.Invitation.Role != "manager" {
		t.Fatalf("invitation = %+v", out.Invitation)
	}

	// The membership exists at once.
	if n := f.count(`SELECT count(*) FROM memberships WHERE user_id = $1 AND org_id = $2 AND role = 'organizer' AND status = 'active'`, out.Invitation.UserID, f.orgID); n != 1 {
		t.Fatalf("memberships = %d, want 1", n)
	}
	// The row is born accepted and carries no Telegram account.
	row, err := gen.New(pool).GetBotInvitationInOrg(context.Background(), out.Invitation.ID, f.orgID)
	if err != nil {
		t.Fatal(err)
	}
	if row.AcceptedAt == nil || row.AcceptedTelegramUserID != nil || !row.MembershipCreated {
		t.Fatalf("row = accepted_at %v telegram %v created %v", row.AcceptedAt, row.AcceptedTelegramUserID, row.MembershipCreated)
	}

	// One setup-link job, purpose org_invitation, keyed to a stored token hash.
	jobs := f.jobPayloads(authemail.JobTypePasswordResetEmail, email)
	if len(jobs) != 1 {
		t.Fatalf("password jobs = %d, want 1", len(jobs))
	}
	var p authemail.PasswordResetEmailPayload
	if err := json.Unmarshal(jobs[0], &p); err != nil {
		t.Fatal(err)
	}
	if p.Purpose != authemail.PurposeOrgInvitation || p.UserID != out.Invitation.UserID.String() || p.OrgName == "" || p.Token == "" {
		t.Fatalf("payload = %+v", p)
	}
	if n := f.count(`SELECT count(*) FROM password_reset_tokens WHERE token = $1 AND user_id = $2 AND used_at IS NULL AND expires_at > now() + interval '6 days'`,
		users.TokenHash(p.Token), out.Invitation.UserID); n != 1 {
		t.Fatalf("stored token rows = %d, want 1 (SHA-256 of the job's token, valid about 7 days)", n)
	}
	if n := f.count(`SELECT count(*) FROM password_reset_tokens WHERE token = $1`, p.Token); n != 0 {
		t.Fatal("the raw token must never be stored")
	}
	// No Telegram letter, no "added" letter.
	if got := len(f.jobPayloads(authemail.JobTypeBotInvitationEmail, email)); got != 0 {
		t.Fatalf("bot.invitation_email jobs = %d, want 0", got)
	}
	if got := len(f.jobPayloads(authemail.JobTypeOrgAddedEmail, email)); got != 0 {
		t.Fatalf("org added jobs = %d, want 0", got)
	}
}

func TestBotInvitationWeb_ExistingMemberKeepsTheRoleAndGetsTheAddedLetter(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	srv := buildWebInviteServer(t, f)
	ctx := context.Background()

	email := f.newEmail("web-existing")
	u, err := gen.New(pool).InsertUser(ctx, email, "x", "es")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gen.New(pool).InsertMembership(ctx, u.ID, f.orgID, "organizer"); err != nil {
		t.Fatal(err)
	}

	// The owner asks for the owner role: an existing membership is never widened.
	rec := f.invite(srv, f.ownerID, `{"email":"`+email+`","role":"owner","channel":"web"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	out := decodeWebInvite(t, rec.Body.Bytes())
	if out.Invitation.UserCreated || out.Invitation.MembershipRole != "organizer" || out.Invitation.Role != "manager" || out.DeepLink != nil {
		t.Fatalf("answer = %+v deep_link=%v", out.Invitation, out.DeepLink)
	}
	if n := f.count(`SELECT count(*) FROM memberships WHERE user_id = $1 AND org_id = $2 AND role = 'organizer'`, u.ID, f.orgID); n != 1 {
		t.Fatal("the organizer membership must stay")
	}
	if n := f.count(`SELECT count(*) FROM memberships WHERE user_id = $1 AND org_id = $2 AND role = 'org_admin'`, u.ID, f.orgID); n != 0 {
		t.Fatal("the role must not be widened to owner")
	}

	// The "added" letter is queued, in the person's own language, with no token.
	jobs := f.jobPayloads(authemail.JobTypeOrgAddedEmail, email)
	if len(jobs) != 1 {
		t.Fatalf("org added jobs = %d, want 1", len(jobs))
	}
	var p authemail.OrgAddedEmailPayload
	if err := json.Unmarshal(jobs[0], &p); err != nil {
		t.Fatal(err)
	}
	if p.UserID != u.ID.String() || p.OrgName == "" || p.Locale != "es" {
		t.Fatalf("payload = %+v", p)
	}
	if strings.Contains(string(jobs[0]), "token") {
		t.Fatalf("the added letter carries no token: %s", jobs[0])
	}
	if n := f.count(`SELECT count(*) FROM password_reset_tokens WHERE user_id = $1`, u.ID); n != 0 {
		t.Fatal("an existing account gets no setup token")
	}
	if got := len(f.jobPayloads(authemail.JobTypePasswordResetEmail, email)); got != 0 {
		t.Fatalf("password jobs = %d, want 0", got)
	}
	// The membership was not created by this invitation, so the row says so.
	row, err := gen.New(pool).GetBotInvitationInOrg(ctx, out.Invitation.ID, f.orgID)
	if err != nil {
		t.Fatal(err)
	}
	if row.MembershipCreated {
		t.Fatal("membership_created must be false for a person who was a member before")
	}
}

func TestBotInvitationWeb_DefaultAndTelegramChannelKeepTheOldBehaviour(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	srv := buildWebInviteServer(t, f)

	for name, body := range map[string]string{
		"absent":   `{"email":"%s","role":"manager"}`,
		"explicit": `{"email":"%s","role":"manager","channel":"Telegram"}`,
	} {
		email := f.newEmail("tg-" + name)
		rec := f.invite(srv, f.ownerID, strings.Replace(body, "%s", email, 1))
		if rec.Code != http.StatusCreated {
			t.Fatalf("%s: status %d: %s", name, rec.Code, rec.Body.String())
		}
		out := decodeWebInvite(t, rec.Body.Bytes())
		if out.Invitation.Channel != "telegram" {
			t.Fatalf("%s: channel = %q", name, out.Invitation.Channel)
		}
		code, _ := f.queuedCode(email)
		if code == "" {
			t.Fatalf("%s: the Telegram invitation must queue its code", name)
		}
		row, err := gen.New(pool).GetBotInvitationInOrg(context.Background(), out.Invitation.ID, f.orgID)
		if err != nil {
			t.Fatal(err)
		}
		if row.AcceptedAt != nil || row.ExpiresAt.Before(time.Now().Add(6*24*time.Hour)) {
			t.Fatalf("%s: a Telegram invitation waits to be opened: %+v", name, row)
		}
		if got := len(f.jobPayloads(authemail.JobTypePasswordResetEmail, email)); got != 0 {
			t.Fatalf("%s: password jobs = %d, want 0", name, got)
		}
	}
}

func TestBotInvitationWeb_UnknownChannelIs400AndWritesNothing(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	srv := buildWebInviteServer(t, f)

	email := f.newEmail("bad-channel")
	rec := f.invite(srv, f.ownerID, `{"email":"`+email+`","role":"manager","channel":"carrier-pigeon"}`)
	if rec.Code != http.StatusBadRequest || errCodeOf(rec.Body.Bytes()) != "bot.invalid_channel" {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if n := f.count(`SELECT count(*) FROM users WHERE email = $1`, email); n != 0 {
		t.Fatal("a refused request must not create the user")
	}
}

func TestBotInvitationWeb_WithoutTheEventCenterAddressIs503AndWritesNothing(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	srv := buildBotIntegrationServer(t, pool) // EVENT_CENTER_PUBLIC_URL is empty

	email := f.newEmail("web-unconfigured")
	rec := f.invite(srv, f.ownerID, `{"email":"`+email+`","role":"manager","channel":"web"}`)
	if rec.Code != http.StatusServiceUnavailable || errCodeOf(rec.Body.Bytes()) != "bot.web_invitation_not_configured" {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if n := f.count(`SELECT count(*) FROM users WHERE email = $1`, email); n != 0 {
		t.Fatal("nothing may be written")
	}
	// The Telegram channel does not need the address.
	ok := f.invite(srv, f.ownerID, `{"email":"`+f.newEmail("tg-unconfigured")+`","role":"manager"}`)
	if ok.Code != http.StatusCreated {
		t.Fatalf("telegram: status %d: %s", ok.Code, ok.Body.String())
	}
}

func TestBotInvitationWeb_Lifecycle_AcceptedAtOnceNoResendRevokeKeepsTheMember(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	srv := buildWebInviteServer(t, f)
	m := &invManage{t: t, pool: pool, f: f, srv: srv}

	email := f.newEmail("web-lifecycle")
	rec := f.invite(srv, f.ownerID, `{"email":"`+email+`","role":"manager","channel":"web"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	out := decodeWebInvite(t, rec.Body.Bytes())
	inv, user := out.Invitation.ID, out.Invitation.UserID

	// The team list reports it accepted.
	code, _, body := m.call(&f.ownerID, http.MethodGet, "/v1/organizations/"+f.orgID.String()+"/bot-team", nil)
	if code != http.StatusOK {
		t.Fatalf("bot-team: %d %s", code, body)
	}
	var team struct {
		Members []struct {
			UserID          uuid.UUID `json:"user_id"`
			InvitationState string    `json:"invitation_state"`
			InvitationID    uuid.UUID `json:"invitation_id"`
		} `json:"members"`
	}
	if err := json.Unmarshal(body, &team); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, mem := range team.Members {
		if mem.UserID == user {
			found = true
			if mem.InvitationState != "accepted" || mem.InvitationID != inv {
				t.Fatalf("member = %+v", mem)
			}
		}
	}
	if !found {
		t.Fatal("the invited person is missing from the team list")
	}

	// A lost link is replaced by "forgot password", not by a resend.
	code, _, body = m.call(&f.ownerID, http.MethodPost, m.invPath(f.orgID, inv, "/resend"), nil)
	if code != http.StatusConflict || errCodeOf(body) != "bot.invitation_channel_unsupported" {
		t.Fatalf("resend: %d %s", code, body)
	}

	// Revoking voids the row; the member stays (the remove-member flow takes them out).
	status, out2 := m.revoke(inv)
	if status != http.StatusOK || out2["membership_removed"] != false || out2["kept_reason"] != "accepted" {
		t.Fatalf("revoke: %d %v", status, out2)
	}
	if !m.hasMembership(user) {
		t.Fatal("the membership must stay after revoking a web invitation")
	}
}
