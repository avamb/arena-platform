//go:build integration

package httpserver

// The Telegram event-center bot's invitation flow (spec 28 §3.3): an
// organization owner invites a person by e-mail; the API creates the user
// and membership, stores the code hash and queues bot.invitation_email; the
// bot process redeems the code under BOT_SERVICE_TOKEN and the Telegram
// account is bound to the invited user. Run against a migrated database:
//
//	DATABASE_URL=postgres://arena:arena@localhost:45432/arena_ci?sslmode=disable \
//	  go test -tags integration ./apps/backend/internal/platform/httpserver/ -run BotInvitation

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	emailadapter "github.com/abhteam/arena_new/apps/backend/internal/adapters/email"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/authemail"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

const (
	botTestServiceToken = "bot-service-token-for-tests"
	botTestJWTSecret    = "bot-invitations-integration-secret-32b!!"
	botTestJWTIssuer    = "arena-api"
	botTestJWTAudience  = "arena-api"
)

type botInviteFixture struct {
	t       *testing.T
	pool    *pgxpool.Pool
	orgID   uuid.UUID
	ownerID uuid.UUID
	emails  []string
}

func newBotInviteFixture(t *testing.T, pool *pgxpool.Pool) *botInviteFixture {
	t.Helper()
	ctx := context.Background()
	q := gen.New(pool)
	suffix := uuid.NewString()[:8]
	org, err := q.InsertOrganization(ctx, "Bot invite org "+suffix, "bot-invite-"+suffix, "CZ", "en", 1200)
	if err != nil {
		t.Fatalf("InsertOrganization: %v", err)
	}
	ownerEmail := fmt.Sprintf("bot-owner-%s@arena-integration.test", suffix)
	owner, err := q.InsertUser(ctx, ownerEmail, "x", "en")
	if err != nil {
		t.Fatalf("InsertUser owner: %v", err)
	}
	if _, err := q.InsertMembership(ctx, owner.ID, org.ID, "org_admin"); err != nil {
		t.Fatalf("InsertMembership owner as org_admin (migration 0115 widened the CHECK): %v", err)
	}
	f := &botInviteFixture{t: t, pool: pool, orgID: org.ID, ownerID: owner.ID, emails: []string{ownerEmail}}
	t.Cleanup(f.cleanup)
	return f
}

func (f *botInviteFixture) cleanup() {
	ctx := context.Background()
	_, _ = f.pool.Exec(ctx, `DELETE FROM worker_jobs WHERE job_type = $1 AND payload->>'email' = ANY($2)`, authemail.JobTypeBotInvitationEmail, f.emails)
	_, _ = f.pool.Exec(ctx, `DELETE FROM bot_drafts WHERE org_id = $1`, f.orgID)
	_, _ = f.pool.Exec(ctx, `DELETE FROM bot_telegram_links WHERE user_id IN (SELECT id FROM users WHERE email = ANY($1))`, f.emails)
	_, _ = f.pool.Exec(ctx, `DELETE FROM bot_invitations WHERE org_id = $1`, f.orgID)
	_, _ = f.pool.Exec(ctx, `DELETE FROM audit_events WHERE resource_type = 'bot_invitation' AND metadata->>'org_id' = $1`, f.orgID.String())
	_, _ = f.pool.Exec(ctx, `DELETE FROM memberships WHERE org_id = $1`, f.orgID)
	_, _ = f.pool.Exec(ctx, `DELETE FROM users WHERE email = ANY($1)`, f.emails)
	_, _ = f.pool.Exec(ctx, `DELETE FROM organizations WHERE id = $1`, f.orgID)
}

func (f *botInviteFixture) newEmail(label string) string {
	em := fmt.Sprintf("bot-%s-%s@arena-integration.test", label, uuid.NewString()[:8])
	f.emails = append(f.emails, em)
	return em
}

// buildBotIntegrationServer builds a Server with auth enabled (the bot routes
// only mount when it is) and the bot service token configured.
func buildBotIntegrationServer(t *testing.T, pool *pgxpool.Pool) *Server {
	t.Helper()
	const secret = botTestJWTSecret
	const issuer = botTestJWTIssuer
	const audience = botTestJWTAudience
	stub, err := auth.NewStubProvider(auth.StubConfig{
		Secret: secret, Issuer: issuer, Audience: audience, DefaultTTL: time.Hour, Enabled: true,
	})
	if err != nil {
		t.Fatalf("NewStubProvider: %v", err)
	}
	verifier, err := auth.NewJWTVerifier(secret, issuer, audience)
	if err != nil {
		t.Fatalf("NewJWTVerifier: %v", err)
	}
	cfg := &config.Config{
		AppEnv:                    config.EnvDevelopment,
		AppName:                   "test",
		AppVersion:                "0.0.0-dev",
		RequestTimeout:            30 * time.Second,
		BodyLimitBytes:            1 << 20,
		JWTSecretStub:             secret,
		JWTIssuer:                 issuer,
		JWTAudience:               audience,
		JWTDefaultTTL:             time.Hour,
		EnableStubAuth:            true,
		DefaultLocale:             "en",
		ActiveLocales:             []string{"en"},
		AppPublicURL:              "https://app.bot.test",
		BotServiceToken:           botTestServiceToken,
		EventsTelegramBotUsername: "ArenaEventsCentrBot",
	}
	return New(Options{Config: cfg, Pool: pool, PgxPool: pool, Auth: stub, Verifier: verifier})
}

// invite calls the org route's handler as the given user (the route's
// permission middleware is covered by the router tests; here the tenant
// guard and the transaction are under test).
func (f *botInviteFixture) invite(srv *Server, asUser uuid.UUID, body string) *httptest.ResponseRecorder {
	f.t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/organizations/"+f.orgID.String()+"/bot-invitations", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("org_id", f.orgID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = auth.WithActor(ctx, auth.Actor{ID: asUser.String(), Type: auth.ActorTypeUser})
	rec := httptest.NewRecorder()
	srv.handleCreateBotInvitation(rec, req.WithContext(ctx))
	return rec
}

// queuedCode reads the one-time code back from the queued e-mail job — the
// only place the API ever writes it in clear.
func (f *botInviteFixture) queuedCode(email string) (code string, payload authemail.BotInvitationEmailPayload) {
	f.t.Helper()
	var raw []byte
	err := f.pool.QueryRow(context.Background(),
		`SELECT payload FROM worker_jobs WHERE job_type = $1 AND payload->>'email' = $2 ORDER BY created_at DESC LIMIT 1`,
		authemail.JobTypeBotInvitationEmail, email).Scan(&raw)
	if err != nil {
		f.t.Fatalf("read queued invitation job for %s: %v", email, err)
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		f.t.Fatalf("decode job payload: %v", err)
	}
	return payload.Code, payload
}

func (f *botInviteFixture) accept(srv *Server, token string, body string) *httptest.ResponseRecorder {
	f.t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/bot/invitations/accept", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	srv.router.ServeHTTP(rec, req)
	return rec
}

func TestBotInvitationIntegration_OwnerInvitesManagerAndTelegramIsBound(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	srv := buildBotIntegrationServer(t, pool)
	ctx := context.Background()

	managerEmail := f.newEmail("manager")
	rec := f.invite(srv, f.ownerID, fmt.Sprintf(`{"email":%q,"role":"manager","locale":"ru"}`, strings.ToUpper(managerEmail)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("invite: status = %d body=%s; want 201", rec.Code, rec.Body.String())
	}
	var created struct {
		Invitation struct {
			ID             string `json:"id"`
			UserID         string `json:"user_id"`
			Email          string `json:"email"`
			Role           string `json:"role"`
			MembershipRole string `json:"membership_role"`
			UserCreated    bool   `json:"user_created"`
			Delivery       string `json:"delivery"`
		} `json:"invitation"`
		DeepLink string `json:"deep_link"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode invite response: %v; body=%s", err, rec.Body.String())
	}
	inv := created.Invitation
	if inv.Email != managerEmail || inv.Role != "manager" || inv.MembershipRole != "organizer" || !inv.UserCreated || inv.Delivery != "email" {
		t.Fatalf("unexpected invitation: %+v", inv)
	}
	if created.DeepLink != "" {
		t.Fatalf("an organization owner must never receive the code; deep_link=%q", created.DeepLink)
	}
	if strings.Contains(rec.Body.String(), "\"code\"") {
		t.Fatalf("the response must not carry the code: %s", rec.Body.String())
	}

	// The membership exists before the person ever opens Telegram.
	var role string
	if err := pool.QueryRow(ctx, `SELECT role FROM memberships WHERE org_id = $1 AND user_id = $2 AND status = 'active'`, f.orgID, inv.UserID).Scan(&role); err != nil || role != "organizer" {
		t.Fatalf("manager membership: role=%q err=%v; want organizer", role, err)
	}

	// The e-mail job carries the code, the org name and the locale; the
	// stored row carries only the hash.
	code, payload := f.queuedCode(managerEmail)
	if code == "" || payload.OrgName == "" || payload.Locale != "ru" || payload.Role != "manager" {
		t.Fatalf("queued payload incomplete: %+v", payload)
	}
	var storedHash string
	if err := pool.QueryRow(ctx, `SELECT code_hash FROM bot_invitations WHERE id = $1`, inv.ID).Scan(&storedHash); err != nil {
		t.Fatalf("read invitation row: %v", err)
	}
	if storedHash == code || len(storedHash) != 64 {
		t.Fatalf("bot_invitations.code_hash must be the SHA-256 of the code, got %q", storedHash)
	}
	// And the worker can render the mail from that payload.
	sender := &captureBotSender{}
	handler := authemail.NewHandler(authemail.HandlerOptions{Sender: sender, BotUsername: "ArenaEventsCentrBot"})
	rawPayload, _ := json.Marshal(payload)
	if err := handler.HandleBotInvitationEmail(ctx, rawPayload); err != nil {
		t.Fatalf("render invitation e-mail: %v", err)
	}
	if !strings.Contains(sender.text, "https://t.me/ArenaEventsCentrBot?start=inv_"+code) {
		t.Fatalf("e-mail lacks the deep link: %s", sender.text)
	}

	// The bot redeems the code. No service token → 401; wrong e-mail → 422.
	const tgID = 4242424242
	if rec := f.accept(srv, "", fmt.Sprintf(`{"code":%q,"email":%q,"telegram_user_id":%d}`, code, managerEmail, tgID)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("accept without token: status = %d; want 401", rec.Code)
	}
	if rec := f.accept(srv, botTestServiceToken, fmt.Sprintf(`{"code":%q,"email":"someone-else@x.test","telegram_user_id":%d}`, code, tgID)); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "bot.invitation_email_mismatch") {
		t.Fatalf("accept with wrong e-mail: status = %d body=%s; want 422", rec.Code, rec.Body.String())
	}
	// The deep-link prefix and e-mail case are tolerated.
	rec = f.accept(srv, botTestServiceToken, fmt.Sprintf(`{"code":%q,"email":%q,"telegram_user_id":%d,"telegram_username":"@manager_tg","locale":"ru-RU"}`,
		"inv_"+code, strings.ToUpper(managerEmail), tgID))
	if rec.Code != http.StatusOK {
		t.Fatalf("accept: status = %d body=%s; want 200", rec.Code, rec.Body.String())
	}
	var accepted struct {
		UserID         string `json:"user_id"`
		OrgID          string `json:"org_id"`
		OrgName        string `json:"org_name"`
		Role           string `json:"role"`
		MembershipRole string `json:"membership_role"`
		Locale         string `json:"locale"`
		TelegramUserID int64  `json:"telegram_user_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &accepted); err != nil {
		t.Fatalf("decode accept: %v", err)
	}
	if accepted.UserID != inv.UserID || accepted.OrgID != f.orgID.String() || accepted.Role != "manager" ||
		accepted.MembershipRole != "organizer" || accepted.Locale != "ru" || accepted.TelegramUserID != tgID || accepted.OrgName == "" {
		t.Fatalf("unexpected accept response: %+v", accepted)
	}
	link, err := gen.New(pool).GetBotTelegramLink(ctx, tgID)
	if err != nil {
		t.Fatalf("GetBotTelegramLink: %v", err)
	}
	if link.UserID.String() != inv.UserID || link.Locale != "ru" || link.CurrentOrgID == nil || *link.CurrentOrgID != f.orgID ||
		link.TelegramUsername == nil || *link.TelegramUsername != "manager_tg" {
		t.Fatalf("unexpected link row: %+v", link)
	}

	// A code is single-use: the replay answers the same 404 as an unknown code.
	if rec := f.accept(srv, botTestServiceToken, fmt.Sprintf(`{"code":%q,"email":%q,"telegram_user_id":%d}`, code, managerEmail, tgID)); rec.Code != http.StatusNotFound {
		t.Fatalf("replay: status = %d body=%s; want 404", rec.Code, rec.Body.String())
	}
	if rec := f.accept(srv, botTestServiceToken, fmt.Sprintf(`{"code":"nope","email":%q,"telegram_user_id":%d}`, managerEmail, tgID)); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown code: status = %d; want 404", rec.Code)
	}

	// A second invitation for another person cannot steal this Telegram account.
	otherEmail := f.newEmail("other")
	if rec := f.invite(srv, f.ownerID, fmt.Sprintf(`{"email":%q,"role":"owner"}`, otherEmail)); rec.Code != http.StatusCreated {
		t.Fatalf("invite other: status = %d body=%s", rec.Code, rec.Body.String())
	}
	otherCode, _ := f.queuedCode(otherEmail)
	if rec := f.accept(srv, botTestServiceToken, fmt.Sprintf(`{"code":%q,"email":%q,"telegram_user_id":%d}`, otherCode, otherEmail, tgID)); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "bot.telegram_already_linked") {
		t.Fatalf("steal attempt: status = %d body=%s; want 409", rec.Code, rec.Body.String())
	}
	// The refused redemption did not consume the other code: a fresh
	// Telegram account can still use it, and gets the owner role.
	rec = f.accept(srv, botTestServiceToken, fmt.Sprintf(`{"code":%q,"email":%q,"telegram_user_id":%d}`, otherCode, otherEmail, tgID+1))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"membership_role":"org_admin"`) {
		t.Fatalf("other accept: status = %d body=%s; want 200 org_admin", rec.Code, rec.Body.String())
	}
}

func TestBotInvitationIntegration_ExistingMemberKeepsRoleAndOutsidersAreRefused(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	srv := buildBotIntegrationServer(t, pool)
	ctx := context.Background()
	q := gen.New(pool)

	// A person who is already an agent of the organization is invited as
	// owner: the membership keeps its role, the invitation reports manager.
	agentEmail := f.newEmail("agent")
	agent, err := q.InsertUser(ctx, agentEmail, "x", "en")
	if err != nil {
		t.Fatalf("InsertUser agent: %v", err)
	}
	if _, err := q.InsertMembership(ctx, agent.ID, f.orgID, "agent"); err != nil {
		t.Fatalf("InsertMembership agent: %v", err)
	}
	rec := f.invite(srv, f.ownerID, fmt.Sprintf(`{"email":%q,"role":"owner"}`, agentEmail))
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"membership_role":"agent"`) || !strings.Contains(rec.Body.String(), `"role":"manager"`) || !strings.Contains(rec.Body.String(), `"user_created":false`) {
		t.Fatalf("invite existing agent: status = %d body=%s", rec.Code, rec.Body.String())
	}
	var memberships int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM memberships WHERE org_id = $1 AND user_id = $2`, f.orgID, agent.ID).Scan(&memberships); err != nil || memberships != 1 {
		t.Fatalf("memberships for the agent = %d err=%v; want exactly 1 (no duplicate, no widening)", memberships, err)
	}

	// A user of ANOTHER organization cannot invite into this one.
	strangerEmail := f.newEmail("stranger")
	stranger, err := q.InsertUser(ctx, strangerEmail, "x", "en")
	if err != nil {
		t.Fatalf("InsertUser stranger: %v", err)
	}
	if rec := f.invite(srv, stranger.ID, fmt.Sprintf(`{"email":%q,"role":"manager"}`, f.newEmail("victim"))); rec.Code != http.StatusForbidden {
		t.Fatalf("stranger invite: status = %d body=%s; want 403", rec.Code, rec.Body.String())
	}

	// Validation.
	for name, body := range map[string]string{
		"bad role":  fmt.Sprintf(`{"email":%q,"role":"org_admin"}`, f.newEmail("v1")),
		"bad email": `{"email":"nope","role":"manager"}`,
		"no body":   ``,
	} {
		if rec := f.invite(srv, f.ownerID, body); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d body=%s; want 400", name, rec.Code, rec.Body.String())
		}
	}
	// The service route validates too, before touching any row.
	for name, body := range map[string]string{
		"no code":   `{"email":"a@b.test","telegram_user_id":1}`,
		"no tg id":  `{"code":"x","email":"a@b.test"}`,
		"bad email": `{"code":"x","email":"nope","telegram_user_id":1}`,
	} {
		if rec := f.accept(srv, botTestServiceToken, body); rec.Code != http.StatusBadRequest {
			t.Fatalf("accept %s: status = %d body=%s; want 400", name, rec.Code, rec.Body.String())
		}
	}
}

type captureBotSender struct{ text string }

func (s *captureBotSender) Send(_ context.Context, m emailadapter.Message) error {
	s.text = m.TextBody
	return nil
}
