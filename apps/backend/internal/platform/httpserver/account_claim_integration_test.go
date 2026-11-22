//go:build integration

package httpserver

// An existing account whose e-mail address was never confirmed receives a
// NEW membership only together with a fresh secret: its password is replaced,
// its refresh tokens are revoked and the set-your-password letter is queued
// (package accountclaim). Covered through the real handlers and a real
// database for the bot invitation (web and Telegram channels) and the admin
// "add member" route; an account with a confirmed address is left as it was.
// The password-reset confirmation confirms the address, and a registration
// for an address that already has an account answers like a new one and
// writes nothing.
//
//	DATABASE_URL=... go test -tags integration ./apps/backend/internal/platform/httpserver/ -run AccountClaim

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
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/authemail"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/users"
)

// claimAccount is an existing account with a known password hash and one
// active refresh token.
type claimAccount struct {
	id    uuid.UUID
	email string
	hash  string
}

func newClaimAccount(t *testing.T, pool *pgxpool.Pool, email string, verified bool) claimAccount {
	t.Helper()
	ctx := context.Background()
	hash, err := users.HashPassword("known-password-123")
	if err != nil {
		t.Fatal(err)
	}
	u, err := gen.New(pool).InsertUser(ctx, email, hash, "en")
	if err != nil {
		t.Fatalf("InsertUser: %v", err)
	}
	if verified {
		if _, err := pool.Exec(ctx, `UPDATE users SET email_verified_at = now() WHERE id = $1`, u.ID); err != nil {
			t.Fatal(err)
		}
	}
	if err := gen.New(pool).InsertRefreshToken(ctx, users.TokenHash(uuid.NewString()), u.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("InsertRefreshToken: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM password_reset_tokens WHERE user_id = $1`, u.ID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM refresh_tokens WHERE user_id = $1`, u.ID)
	})
	return claimAccount{id: u.ID, email: email, hash: hash}
}

// state reports whether the password hash changed, how many refresh tokens
// are still active and how many setup tokens exist.
func (a claimAccount) state(t *testing.T, pool *pgxpool.Pool) (hashChanged bool, activeRefresh, setupTokens int) {
	t.Helper()
	ctx := context.Background()
	var hash string
	if err := pool.QueryRow(ctx, `SELECT password_hash FROM users WHERE id = $1`, a.id).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM refresh_tokens WHERE user_id = $1 AND revoked_at IS NULL`, a.id).Scan(&activeRefresh); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM password_reset_tokens WHERE user_id = $1`, a.id).Scan(&setupTokens); err != nil {
		t.Fatal(err)
	}
	return hash != a.hash, activeRefresh, setupTokens
}

func setupLetters(t *testing.T, pool *pgxpool.Pool, email string) []authemail.PasswordResetEmailPayload {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`SELECT payload FROM worker_jobs WHERE job_type = $1 AND payload->>'email' = $2`, authemail.JobTypePasswordResetEmail, email)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []authemail.PasswordResetEmailPayload
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var p authemail.PasswordResetEmailPayload
		if err := json.Unmarshal(raw, &p); err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

func assertClaimed(t *testing.T, pool *pgxpool.Pool, a claimAccount) {
	t.Helper()
	changed, active, tokens := a.state(t, pool)
	if !changed {
		t.Error("the password of an unconfirmed account was not replaced")
	}
	if active != 0 {
		t.Errorf("active refresh tokens = %d, want 0", active)
	}
	if tokens != 1 {
		t.Errorf("setup tokens = %d, want 1", tokens)
	}
	letters := setupLetters(t, pool, a.email)
	if len(letters) != 1 || letters[0].Purpose != authemail.PurposeOrgInvitation || letters[0].Token == "" || letters[0].OrgName == "" {
		t.Fatalf("setup letters = %+v, want one org invitation", letters)
	}
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM password_reset_tokens WHERE user_id = $1 AND token = $2`,
		a.id, users.TokenHash(letters[0].Token)).Scan(&n); err != nil || n != 1 {
		t.Fatalf("the letter's token is not the stored one (n=%d, err=%v)", n, err)
	}
}

func assertUntouched(t *testing.T, pool *pgxpool.Pool, a claimAccount) {
	t.Helper()
	changed, active, tokens := a.state(t, pool)
	if changed || active != 1 || tokens != 0 {
		t.Fatalf("a confirmed account was changed: hash changed=%v active refresh=%d setup tokens=%d", changed, active, tokens)
	}
	if letters := setupLetters(t, pool, a.email); len(letters) != 0 {
		t.Fatalf("setup letters = %d, want 0", len(letters))
	}
}

func TestAccountClaimIntegration_WebInvitation(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	srv := buildWebInviteServer(t, f)

	unconfirmed := newClaimAccount(t, pool, f.newEmail("claim-web"), false)
	rec := f.invite(srv, f.ownerID, `{"email":"`+unconfirmed.email+`","role":"manager","channel":"web"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	assertClaimed(t, pool, unconfirmed)
	if got := len(f.jobPayloads(authemail.JobTypeOrgAddedEmail, unconfirmed.email)); got != 0 {
		t.Fatalf("org added letters = %d, want 0 for a reset account", got)
	}

	confirmed := newClaimAccount(t, pool, f.newEmail("claim-web-ok"), true)
	rec = f.invite(srv, f.ownerID, `{"email":"`+confirmed.email+`","role":"manager","channel":"web"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	assertUntouched(t, pool, confirmed)
	if got := len(f.jobPayloads(authemail.JobTypeOrgAddedEmail, confirmed.email)); got != 1 {
		t.Fatalf("org added letters = %d, want 1", got)
	}
}

// A person with an unconfirmed address who already works in another
// organization is an existing organizer: never reset, and the web invitation
// sends the usual "added" letter.
func TestAccountClaimIntegration_MemberElsewhereIsNeverReset(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	srv := buildWebInviteServer(t, f)
	ctx := context.Background()

	other, err := gen.New(pool).InsertOrganization(ctx, "Claim elsewhere "+uuid.NewString()[:8], "claim-elsewhere-"+uuid.NewString()[:8], "CZ", "en", 1200)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM memberships WHERE org_id = $1`, other.ID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM organizations WHERE id = $1`, other.ID)
	})
	a := newClaimAccount(t, pool, f.newEmail("claim-elsewhere"), false)
	if _, err := gen.New(pool).InsertMembership(ctx, a.id, other.ID, "organizer"); err != nil {
		t.Fatal(err)
	}
	// Any state counts: a revoked membership is still a membership row.
	if _, err := pool.Exec(ctx, `UPDATE memberships SET status = 'revoked' WHERE user_id = $1 AND org_id = $2`, a.id, other.ID); err != nil {
		t.Fatal(err)
	}

	rec := f.invite(srv, f.ownerID, `{"email":"`+a.email+`","role":"manager","channel":"web"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	assertUntouched(t, pool, a)
	if got := len(f.jobPayloads(authemail.JobTypeOrgAddedEmail, a.email)); got != 1 {
		t.Fatalf("org added letters = %d, want 1", got)
	}
}

// A person with an unconfirmed address and a Telegram bot link is never reset.
func TestAccountClaimIntegration_TelegramLinkedIsNeverReset(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	srv := buildBotIntegrationServer(t, pool)

	a := newClaimAccount(t, pool, f.newEmail("claim-linked"), false)
	tgID := 9300000000 + time.Now().UnixNano()%100000000
	if _, err := gen.New(pool).UpsertBotTelegramLink(context.Background(), tgID, a.id, nil, "en", nil); err != nil {
		t.Fatal(err)
	}
	rec := f.invite(srv, f.ownerID, fmt.Sprintf(`{"email":%q,"role":"manager"}`, a.email))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	assertUntouched(t, pool, a)
	if got := len(f.jobPayloads(authemail.JobTypeBotInvitationEmail, a.email)); got != 1 {
		t.Fatalf("bot invitation letters = %d, want 1", got)
	}
}

func TestAccountClaimIntegration_TelegramInvitation(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	srv := buildBotIntegrationServer(t, pool)

	unconfirmed := newClaimAccount(t, pool, f.newEmail("claim-tg"), false)
	rec := f.invite(srv, f.ownerID, fmt.Sprintf(`{"email":%q,"role":"manager"}`, unconfirmed.email))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	assertClaimed(t, pool, unconfirmed)
	if got := len(f.jobPayloads(authemail.JobTypeBotInvitationEmail, unconfirmed.email)); got != 1 {
		t.Fatalf("bot invitation letters = %d, want 1", got)
	}

	confirmed := newClaimAccount(t, pool, f.newEmail("claim-tg-ok"), true)
	rec = f.invite(srv, f.ownerID, fmt.Sprintf(`{"email":%q,"role":"manager"}`, confirmed.email))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	assertUntouched(t, pool, confirmed)
}

func TestAccountClaimIntegration_TelegramInvitationOfAMemberChangesNothing(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	srv := buildBotIntegrationServer(t, pool)

	member := newClaimAccount(t, pool, f.newEmail("claim-member"), false)
	if _, err := gen.New(pool).InsertMembership(context.Background(), member.id, f.orgID, "agent"); err != nil {
		t.Fatal(err)
	}
	rec := f.invite(srv, f.ownerID, fmt.Sprintf(`{"email":%q,"role":"manager"}`, member.email))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	assertUntouched(t, pool, member)
}

func adminAddMember(t *testing.T, srv *Server, orgID uuid.UUID, body string) *httptest.ResponseRecorder {
	t.Helper()
	return adminNamesCall(t, srv.handleAdminAddMember, http.MethodPost,
		"/v1/admin/organizations/"+orgID.String()+"/members", body, map[string]string{"org_id": orgID.String()})
}

func TestAccountClaimIntegration_AdminAddMember(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	srv := buildEmailIntegrationServer(t, pool, onboardingTestPublicURL)

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	orgID := uuid.New()
	if _, err := pool.Exec(t.Context(), `INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`,
		orgID, "Claim Org "+suffix, "claim-org-"+suffix); err != nil {
		t.Fatalf("insert organization: %v", err)
	}
	byEmail := "claim-admin-email-" + suffix + "@arena-integration.test"
	byID := "claim-admin-id-" + suffix + "@arena-integration.test"
	ok := "claim-admin-ok-" + suffix + "@arena-integration.test"
	t.Cleanup(func() {
		sweepOnboardingRows(pool, byEmail, nil)
		sweepOnboardingRows(pool, byID, nil)
		sweepOnboardingRows(pool, ok, &orgID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM audit_events WHERE resource_type = 'membership' AND metadata->>'org_id' = $1`, orgID.String())
	})

	a := newClaimAccount(t, pool, byEmail, false)
	if w := adminAddMember(t, srv, orgID, fmt.Sprintf(`{"email":%q,"role":"organizer"}`, byEmail)); w.Code != http.StatusCreated ||
		!strings.Contains(w.Body.String(), `"delivery":"email"`) {
		t.Fatalf("add by e-mail: %d %s", w.Code, w.Body.String())
	}
	assertClaimed(t, pool, a)

	b := newClaimAccount(t, pool, byID, false)
	if w := adminAddMember(t, srv, orgID, fmt.Sprintf(`{"user_id":%q,"role":"organizer"}`, b.id)); w.Code != http.StatusCreated {
		t.Fatalf("add by user id: %d %s", w.Code, w.Body.String())
	}
	assertClaimed(t, pool, b)

	c := newClaimAccount(t, pool, ok, true)
	w := adminAddMember(t, srv, orgID, fmt.Sprintf(`{"email":%q,"role":"organizer"}`, ok))
	if w.Code != http.StatusCreated || strings.Contains(w.Body.String(), `"invitation"`) {
		t.Fatalf("add confirmed: %d %s", w.Code, w.Body.String())
	}
	assertUntouched(t, pool, c)

	// An unconfirmed person who already holds another role here is not a new
	// member: a second role leaves the account as it was.
	member := "claim-admin-member-" + suffix + "@arena-integration.test"
	t.Cleanup(func() { sweepOnboardingRows(pool, member, nil) })
	d := newClaimAccount(t, pool, member, false)
	if _, err := gen.New(pool).InsertMembership(t.Context(), d.id, orgID, "organizer"); err != nil {
		t.Fatal(err)
	}
	if w := adminAddMember(t, srv, orgID, fmt.Sprintf(`{"user_id":%q,"role":"agent"}`, d.id)); w.Code != http.StatusCreated {
		t.Fatalf("add a second role: %d %s", w.Code, w.Body.String())
	}
	assertUntouched(t, pool, d)
}

func postAuthJSON(h http.HandlerFunc, path, body, remote string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.RemoteAddr = remote
	w := httptest.NewRecorder()
	h(w, r)
	return w
}

func TestAccountClaimIntegration_ResetConfirmConfirmsTheAddress(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	srv := buildEmailIntegrationServer(t, pool, onboardingTestPublicURL)
	ctx := context.Background()

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	em := "claim-reset-" + suffix + "@arena-integration.test"
	t.Cleanup(func() { sweepOnboardingRows(pool, em, nil) })
	a := newClaimAccount(t, pool, em, false)

	token := strings.ReplaceAll(uuid.NewString()+uuid.NewString(), "-", "")
	if err := gen.New(pool).InsertPasswordResetToken(ctx, users.TokenHash(token), a.id, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	w := postAuthJSON(srv.handleAuthPasswordResetConfirm, "/v1/auth/password-reset/confirm",
		fmt.Sprintf(`{"token":%q,"new_password":"a-new-password-1"}`, token), "192.0.2.10:1")
	if w.Code != http.StatusOK {
		t.Fatalf("confirm: %d %s", w.Code, w.Body.String())
	}
	var verified *time.Time
	if err := pool.QueryRow(ctx, `SELECT email_verified_at FROM users WHERE id = $1`, a.id).Scan(&verified); err != nil {
		t.Fatal(err)
	}
	if verified == nil {
		t.Fatal("email_verified_at is still NULL after a completed reset")
	}

	// A second reset keeps the first confirmation time.
	token2 := strings.ReplaceAll(uuid.NewString()+uuid.NewString(), "-", "")
	if err := gen.New(pool).InsertPasswordResetToken(ctx, users.TokenHash(token2), a.id, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if w := postAuthJSON(srv.handleAuthPasswordResetConfirm, "/v1/auth/password-reset/confirm",
		fmt.Sprintf(`{"token":%q,"new_password":"another-password-2"}`, token2), "192.0.2.10:1"); w.Code != http.StatusOK {
		t.Fatalf("second confirm: %d %s", w.Code, w.Body.String())
	}
	var again *time.Time
	if err := pool.QueryRow(ctx, `SELECT email_verified_at FROM users WHERE id = $1`, a.id).Scan(&again); err != nil {
		t.Fatal(err)
	}
	if again == nil || !again.Equal(*verified) {
		t.Fatalf("email_verified_at moved from %v to %v", verified, again)
	}
}

func TestAccountClaimIntegration_DuplicateRegistrationLooksNewAndWritesNothing(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	srv := buildEmailIntegrationServer(t, pool, onboardingTestPublicURL)
	ctx := context.Background()

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	em := "claim-register-" + suffix + "@arena-integration.test"
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM worker_jobs WHERE job_type = $1 AND payload->>'email' = $2`, authemail.JobTypeEmailVerification, em)
		_, _ = pool.Exec(context.Background(), `DELETE FROM email_verification_tokens WHERE user_id IN (SELECT id FROM users WHERE email = $1)`, em)
		sweepOnboardingRows(pool, em, nil)
	})

	type registered struct {
		UserID    string `json:"user_id"`
		Email     string `json:"email"`
		CreatedAt string `json:"created_at"`
		Message   string `json:"message"`
	}
	register := func(addr, remote string) (int, registered, []string) {
		w := postAuthJSON(srv.handleAuthRegister, "/v1/auth/register",
			fmt.Sprintf(`{"email":%q,"password":"a-password-123"}`, addr), remote)
		var out registered
		var keys map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		_ = json.Unmarshal(w.Body.Bytes(), &keys)
		var names []string
		for k := range keys {
			names = append(names, k)
		}
		return w.Code, out, names
	}
	counts := func() (users, tokens, jobs int) {
		_ = pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE email = $1`, em).Scan(&users)
		_ = pool.QueryRow(ctx, `SELECT count(*) FROM email_verification_tokens WHERE user_id IN (SELECT id FROM users WHERE email = $1)`, em).Scan(&tokens)
		_ = pool.QueryRow(ctx, `SELECT count(*) FROM worker_jobs WHERE job_type = $1 AND payload->>'email' = $2`, authemail.JobTypeEmailVerification, em).Scan(&jobs)
		return
	}

	code1, first, keys1 := register(em, "192.0.2.20:1")
	if code1 != http.StatusCreated {
		t.Fatalf("first registration: %d", code1)
	}
	u1, t1, j1 := counts()
	if u1 != 1 || t1 != 1 || j1 != 1 {
		t.Fatalf("after the first registration: users=%d tokens=%d jobs=%d, want 1 1 1", u1, t1, j1)
	}

	code2, second, keys2 := register(strings.ToUpper(em), "192.0.2.21:1")
	if code2 != http.StatusCreated {
		t.Fatalf("second registration: %d, want 201", code2)
	}
	if len(keys1) != len(keys2) || second.Email != em || second.Message != first.Message || second.CreatedAt == "" {
		t.Fatalf("answers differ in shape: %+v vs %+v", first, second)
	}
	if second.UserID == first.UserID {
		t.Fatal("the second answer names the existing account")
	}
	realID, err := uuid.Parse(first.UserID)
	if err != nil {
		t.Fatalf("user_id %q is not a UUID", first.UserID)
	}
	fakeID, err := uuid.Parse(second.UserID)
	if err != nil {
		t.Fatalf("user_id %q is not a UUID", second.UserID)
	}
	if realID.Version() != 7 || fakeID.Version() != realID.Version() {
		t.Fatalf("UUID versions: real %d, answer %d, want both 7", realID.Version(), fakeID.Version())
	}
	fraction := func(ts string) int {
		dot := strings.IndexByte(ts, '.')
		if dot < 0 {
			return 0
		}
		end := strings.IndexAny(ts[dot:], "Z+-")
		if end < 0 {
			t.Fatalf("created_at %q has no zone", ts)
		}
		return end - 1
	}
	if fr, ff := fraction(first.CreatedAt), fraction(second.CreatedAt); fr > 6 || ff > 6 {
		t.Fatalf("created_at fractions: real %q (%d digits), answer %q (%d digits), want at most 6", first.CreatedAt, fr, second.CreatedAt, ff)
	}
	if u2, t2, j2 := counts(); u2 != 1 || t2 != 1 || j2 != 1 {
		t.Fatalf("the second registration wrote rows: users=%d tokens=%d jobs=%d", u2, t2, j2)
	}
}
