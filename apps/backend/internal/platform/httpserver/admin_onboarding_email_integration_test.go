//go:build integration

// admin_onboarding_email_integration_test.go — the admin-created account and
// the organization invitation must reach the recipient through the SAME
// delivery path as the self-service password reset: an
// auth.password_reset_email worker job, committed with the account, rendered
// by authemail with a link built from APP_PUBLIC_URL, delivered over SMTP, and
// redeemable at POST /v1/auth/password-reset/confirm.
//
// Until 2026-09-19 both handlers only slog'd a "dev-mode" line carrying the
// full link (token included), sent nothing, stored the RAW token although the
// confirm endpoint looks tokens up by SHA-256, and built the link from the
// request (http:// behind the proxy, pointing at a POST-only API path).
//
// Reuses the SMTP capture and worker helpers of auth_email_integration_test.go.
//
//	go test -tags integration ./apps/backend/internal/platform/httpserver/ \
//	    -run TestAdminOnboardingEmailIntegration
package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/email"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/authemail"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/users"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/worker"
)

const onboardingTestPublicURL = "https://app.onboarding.test"

func onboardingIntegrationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		t.Skip("DATABASE_URL not set; skipping admin onboarding email integration test")
	}
	pool, err := pgxpool.New(t.Context(), dbURL)
	if err != nil {
		t.Skipf("pgxpool.New: %v; skipping", err)
	}
	t.Cleanup(func() { pool.Close() })
	if err := pool.Ping(t.Context()); err != nil {
		t.Skipf("pool.Ping: %v; skipping", err)
	}
	return pool
}

func onboardingEmailRegistry(smtp *smtpCapture) *worker.Registry {
	authHandler := authemail.NewHandler(authemail.HandlerOptions{
		Sender: email.NewSMTPSender(email.SMTPConfig{
			Host: smtp.host(),
			Port: smtp.port(),
			From: "noreply@arena.test",
		}),
		AppPublicURL: onboardingTestPublicURL,
		FromAddress:  "noreply@arena.test",
	})
	reg := worker.NewRegistry()
	reg.Register(authemail.JobTypeEmailVerification, authHandler.HandleEmailVerification)
	reg.Register(authemail.JobTypePasswordResetEmail, authHandler.HandlePasswordResetEmail)
	return reg
}

// sweepOnboardingRows removes everything the test created, worker_jobs FIRST
// (AGENTS.md: no FK cascades them, and leftovers break generic drainers).
func sweepOnboardingRows(pool *pgxpool.Pool, em string, orgID *uuid.UUID) {
	ctx := context.Background()
	_, _ = pool.Exec(ctx, `DELETE FROM worker_jobs WHERE job_type = $1 AND payload->>'email' = $2`,
		authemail.JobTypePasswordResetEmail, em)
	_, _ = pool.Exec(ctx, `DELETE FROM password_reset_tokens WHERE user_id IN (SELECT id FROM users WHERE email = $1)`, em)
	_, _ = pool.Exec(ctx, `DELETE FROM refresh_tokens WHERE user_id IN (SELECT id FROM users WHERE email = $1)`, em)
	_, _ = pool.Exec(ctx, `DELETE FROM user_roles WHERE user_id IN (SELECT id FROM users WHERE email = $1)`, em)
	_, _ = pool.Exec(ctx, `DELETE FROM memberships WHERE user_id IN (SELECT id FROM users WHERE email = $1)`, em)
	_, _ = pool.Exec(ctx, `DELETE FROM users WHERE email = $1`, em)
	if orgID != nil {
		_, _ = pool.Exec(ctx, `DELETE FROM memberships WHERE org_id = $1`, *orgID)
		_, _ = pool.Exec(ctx, `DELETE FROM organizations WHERE id = $1`, *orgID)
	}
}

// assertQueuedSetupJob proves the handler committed exactly one
// auth.password_reset_email job for em with the expected purpose.
func assertQueuedSetupJob(t *testing.T, pool *pgxpool.Pool, em, wantPurpose string) {
	t.Helper()
	rows, err := pool.Query(t.Context(),
		`SELECT job_type, payload FROM worker_jobs WHERE payload->>'email' = $1`, em)
	if err != nil {
		t.Fatalf("query worker_jobs: %v", err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var jobType string
		var raw []byte
		if err := rows.Scan(&jobType, &raw); err != nil {
			t.Fatalf("scan worker_jobs: %v", err)
		}
		n++
		if jobType != authemail.JobTypePasswordResetEmail {
			t.Errorf("job_type = %q; want %q (same job type as the self-service reset)", jobType, authemail.JobTypePasswordResetEmail)
		}
		var p authemail.PasswordResetEmailPayload
		if err := json.Unmarshal(raw, &p); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		if p.Purpose != wantPurpose {
			t.Errorf("payload purpose = %q; want %q", p.Purpose, wantPurpose)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate worker_jobs: %v", err)
	}
	if n != 1 {
		t.Fatalf("worker_jobs rows for %s = %d; want 1", em, n)
	}
}

// unfoldQP undoes the quoted-printable soft line breaks and '=' escaping the
// SMTP sender applies, so long lines can be searched as a whole.
func unfoldQP(body string) string {
	body = strings.ReplaceAll(body, "=\r\n", "")
	body = strings.ReplaceAll(body, "=\n", "")
	return strings.ReplaceAll(body, "=3D", "=")
}

// redeemSetupLink extracts the token from the captured email, checks the link
// shape, sets a password through the real confirm endpoint, and proves the new
// password authenticates and the link is single-use.
func redeemSetupLink(t *testing.T, srv *Server, pool *pgxpool.Pool, msg smtpMessage, em string) {
	t.Helper()
	const linkPrefix = onboardingTestPublicURL + "/accept-invite?token="
	body := unfoldQP(msg.Body)
	if !strings.Contains(body, linkPrefix) {
		t.Fatalf("email lacks the SPA setup link %q...; body:\n%s", linkPrefix, body)
	}
	if strings.Contains(body, "/v1/auth/password-reset/confirm") {
		t.Error("email links to the POST-only API endpoint instead of the SPA page")
	}
	raw := extractTokenFromBody(t, msg.Body, "/accept-invite?token=")
	token, rest, _ := strings.Cut(raw, "&")
	if len(token) != 64 {
		t.Fatalf("extracted token %q has length %d; want 64 hex chars", token, len(token))
	}
	if !strings.Contains(rest, "email=") {
		t.Errorf("setup link lacks the email parameter the accept-invite page prefills: %q", raw)
	}

	confirm := func() int {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/v1/auth/password-reset/confirm",
			strings.NewReader(fmt.Sprintf(`{"token":%q,"new_password":"ChosenPassword1!"}`, token)))
		r.Header.Set("Content-Type", "application/json")
		srv.handleAuthPasswordResetConfirm(w, r)
		if w.Code != http.StatusOK && w.Code != http.StatusGone {
			t.Logf("confirm body: %s", w.Body.String())
		}
		return w.Code
	}
	if code := confirm(); code != http.StatusOK {
		t.Fatalf("confirm with emailed token: status = %d; want 200", code)
	}
	u, err := gen.New(pool).GetUserByEmail(t.Context(), em)
	if err != nil {
		t.Fatalf("GetUserByEmail: %v", err)
	}
	if err := users.CheckPassword(u.PasswordHash, "ChosenPassword1!"); err != nil {
		t.Errorf("chosen password does not authenticate: %v", err)
	}
	if code := confirm(); code != http.StatusGone {
		t.Errorf("second confirm: status = %d; want 410 (single-use)", code)
	}
}

// TestAdminOnboardingEmailIntegration_AdminCreatedUser covers
// POST /v1/admin/users end to end.
func TestAdminOnboardingEmailIntegration_AdminCreatedUser(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	smtp := newSMTPCapture(t)
	srv := buildEmailIntegrationServer(t, pool, onboardingTestPublicURL)

	em := fmt.Sprintf("onboard-setup-%d@arena-integration.test", time.Now().UnixNano())
	t.Cleanup(func() { sweepOnboardingRows(pool, em, nil) })

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/admin/users",
		strings.NewReader(fmt.Sprintf(`{"email":%q,"role":"platform_operator"}`, em)))
	r.Host = "api.should-not-appear.test"
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Admin-Reason", "integration: onboarding email")
	srv.handleAdminCreateUser(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("POST /v1/admin/users: status = %d (body: %s)", w.Code, w.Body.String())
	}
	var resp struct {
		Onboarding struct {
			PasswordResetIssued bool   `json:"password_reset_issued"`
			Delivery            string `json:"delivery"`
		} `json:"onboarding"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.Onboarding.PasswordResetIssued || resp.Onboarding.Delivery != "email" {
		t.Errorf("onboarding = %+v; want issued via email", resp.Onboarding)
	}

	assertQueuedSetupJob(t, pool, em, authemail.PurposeAccountSetup)

	msg := drainWorkerJobsUntilEmail(t, pool, onboardingEmailRegistry(smtp), smtp, em)
	if strings.Contains(msg.Body, "api.should-not-appear.test") {
		t.Error("email link was derived from the request host instead of APP_PUBLIC_URL")
	}
	if !strings.Contains(unfoldQP(msg.Body), "Set up your Arena Platform account") {
		t.Errorf("email is not the account-setup variant; body:\n%s", msg.Body)
	}
	redeemSetupLink(t, srv, pool, msg, em)
}

// TestAdminOnboardingEmailIntegration_OrgInvitation covers the new-email
// branch of POST /v1/admin/organizations/{org_id}/members end to end.
func TestAdminOnboardingEmailIntegration_OrgInvitation(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	smtp := newSMTPCapture(t)
	srv := buildEmailIntegrationServer(t, pool, onboardingTestPublicURL)

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	em := "onboard-invite-" + suffix + "@arena-integration.test"
	orgID := uuid.New()
	orgName := "Onboarding Org " + suffix
	if _, err := pool.Exec(t.Context(),
		`INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`,
		orgID, orgName, "onboarding-org-"+suffix); err != nil {
		t.Fatalf("insert organization: %v", err)
	}
	t.Cleanup(func() { sweepOnboardingRows(pool, em, &orgID) })

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/admin/organizations/"+orgID.String()+"/members",
		strings.NewReader(fmt.Sprintf(`{"email":%q,"role":"organizer"}`, em)))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Admin-Reason", "integration: invitation email")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("org_id", orgID.String())
	r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
	srv.handleAdminAddMember(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("POST members: status = %d (body: %s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"delivery":"email"`) {
		t.Errorf("response lacks invitation delivery metadata: %s", w.Body.String())
	}

	assertQueuedSetupJob(t, pool, em, authemail.PurposeOrgInvitation)

	msg := drainWorkerJobsUntilEmail(t, pool, onboardingEmailRegistry(smtp), smtp, em)
	if !strings.Contains(unfoldQP(msg.Body), "You have been invited to join "+orgName) {
		t.Errorf("invitation email does not name the organization %q; body:\n%s", orgName, msg.Body)
	}
	redeemSetupLink(t, srv, pool, msg, em)
}
