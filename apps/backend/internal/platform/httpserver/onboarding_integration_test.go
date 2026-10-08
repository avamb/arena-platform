//go:build integration

// onboarding_integration_test.go — the organizer application flow through the
// real router: the public routes the website calls and the operator's routes,
// including the permission gate (08_architecture/34_onboarding_applications_ru.md).
//
//	DATABASE_URL=... JWT_SIGNING_SECRET=x go.exe test -tags integration \
//	  ./apps/backend/internal/platform/httpserver/ -run TestOnboarding_
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
)

func onbDo(t *testing.T, client *http.Client, method, url, token, body string, headers map[string]string) (int, string) {
	t.Helper()
	var rdr *strings.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	} else {
		rdr = strings.NewReader("")
	}
	req, err := http.NewRequestWithContext(context.Background(), method, url, rdr)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, integReadBody(t, resp)
}

func TestOnboarding_PublicFlowAndOperatorDecision(t *testing.T) {
	srv, _ := productionIntegrationServerCfg(t, func(c *config.Config) { c.OnboardingEnabled = true })
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	client := ts.Client()
	ctx := context.Background()
	base := ts.URL + "/v1"

	email := fmt.Sprintf("onb-http-%d@example.com", time.Now().UnixNano())
	t.Cleanup(func() {
		var orgID *uuid.UUID
		_ = srv.pgxPool.QueryRow(ctx, `SELECT org_id FROM onboarding_applications WHERE applicant_email = $1 AND org_id IS NOT NULL`, email).Scan(&orgID)
		_, _ = srv.pgxPool.Exec(ctx, `DELETE FROM worker_jobs WHERE payload->>'email' = $1`, email)
		_, _ = srv.pgxPool.Exec(ctx, `DELETE FROM worker_jobs WHERE job_type = 'onboarding.notify' AND payload->>'application_id' IN (SELECT id::text FROM onboarding_applications WHERE applicant_email = $1)`, email)
		_, _ = srv.pgxPool.Exec(ctx, `DELETE FROM onboarding_applications WHERE applicant_email = $1`, email)
		if orgID != nil {
			_, _ = srv.pgxPool.Exec(ctx, `DELETE FROM memberships WHERE org_id = $1`, *orgID)
			_, _ = srv.pgxPool.Exec(ctx, `DELETE FROM sales_channels WHERE org_id = $1`, *orgID)
			_, _ = srv.pgxPool.Exec(ctx, `DELETE FROM organizations WHERE id = $1`, *orgID)
		}
		_, _ = srv.pgxPool.Exec(ctx, `DELETE FROM password_reset_tokens WHERE user_id IN (SELECT id FROM users WHERE email = $1)`, email)
		_, _ = srv.pgxPool.Exec(ctx, `DELETE FROM users WHERE email = $1`, email)
	})

	// The form schema is public and speaks the requested language.
	code, body := onbDo(t, client, http.MethodGet, base+"/onboarding/form-schema?locale=ru", "", "", nil)
	if code != http.StatusOK || !strings.Contains(body, "Ваша организация") || !strings.Contains(body, `"accepted_countries":[]`) {
		t.Fatalf("form-schema: %d %s", code, body)
	}

	// A hidden-field bot gets a convincing answer and nothing is stored.
	code, _ = onbDo(t, client, http.MethodPost, base+"/onboarding/applications", "",
		fmt.Sprintf(`{"first_name":"Bot","last_name":"Net","email":"bot-%s","phone":"+34600000000","honeypot":"x"}`, email), nil)
	if code != http.StatusCreated {
		t.Fatalf("honeypot start: %d", code)
	}
	var n int
	_ = srv.pgxPool.QueryRow(ctx, `SELECT count(*) FROM onboarding_applications WHERE applicant_email = $1`, "bot-"+email).Scan(&n)
	if n != 0 {
		t.Fatal("a honeypot request stored an application")
	}

	// Start.
	code, body = onbDo(t, client, http.MethodPost, base+"/onboarding/applications", "",
		fmt.Sprintf(`{"first_name":"Ana","last_name":"Pérez","email":%q,"phone":"+34 600 111 222","locale":"es","utm":{"source":"test"}}`, email), nil)
	if code != http.StatusCreated {
		t.Fatalf("start: %d %s", code, body)
	}
	var started struct {
		ApplicationID string `json:"application_id"`
		AccessToken   string `json:"access_token"`
	}
	if err := json.Unmarshal([]byte(body), &started); err != nil || started.AccessToken == "" {
		t.Fatalf("start body: %s", body)
	}
	tokenHeader := map[string]string{"X-Onboarding-Token": started.AccessToken}
	appURL := base + "/onboarding/applications/" + started.ApplicationID

	// Reading needs the token.
	if code, _ = onbDo(t, client, http.MethodGet, appURL, "", "", nil); code != http.StatusNotFound {
		t.Fatalf("get without token: %d", code)
	}
	if code, body = onbDo(t, client, http.MethodGet, appURL, "", "", tokenHeader); code != http.StatusOK || strings.Contains(body, "access_token_hash") {
		t.Fatalf("get: %d %s", code, body)
	}

	// A bad value is a 422 with a field map.
	code, body = onbDo(t, client, http.MethodPut, appURL+"/answers", "", `{"answers":{"country":"Spain"}}`, tokenHeader)
	if code != http.StatusUnprocessableEntity || !strings.Contains(body, "onboarding.invalid_field") || !strings.Contains(body, `"country":"invalid"`) {
		t.Fatalf("bad answer: %d %s", code, body)
	}
	good := `{"answers":{"org_name":"HTTP Events","legal_name":"HTTP Events SL","country":"ES","tax_id":"ESB1234567","tax_id_scheme":"vat",
"address_line1":"Calle 1","address_postal_code":"28001","address_city":"Madrid","event_types":["concert"],"seating":"ga",
"events_per_year":"1-5","tickets_per_year":"<500","payment_provider":"stripe","accept_terms":true,"accept_privacy":true,"confirm_authority":true}}`
	if code, body = onbDo(t, client, http.MethodPut, appURL+"/answers", "", good, tokenHeader); code != http.StatusOK {
		t.Fatalf("save: %d %s", code, body)
	}

	// Submit before confirming the e-mail.
	code, body = onbDo(t, client, http.MethodPost, appURL+"/submit", "", "", tokenHeader)
	if code != http.StatusConflict || !strings.Contains(body, "onboarding.email_not_confirmed") {
		t.Fatalf("submit unconfirmed: %d %s", code, body)
	}

	// Confirm through the e-mailed link; the old token dies, the new one works.
	var link string
	_ = srv.pgxPool.QueryRow(ctx, `SELECT payload->>'token' FROM worker_jobs WHERE job_type = 'onboarding.email' AND payload->>'email' = $1 LIMIT 1`, email).Scan(&link)
	code, body = onbDo(t, client, http.MethodPost, base+"/onboarding/confirm", "", fmt.Sprintf(`{"token":%q}`, link), nil)
	if code != http.StatusOK {
		t.Fatalf("confirm: %d %s", code, body)
	}
	var confirmed struct {
		AccessToken string `json:"access_token"`
	}
	_ = json.Unmarshal([]byte(body), &confirmed)
	tokenHeader = map[string]string{"X-Onboarding-Token": confirmed.AccessToken}
	if code, body = onbDo(t, client, http.MethodPost, appURL+"/submit", "", "", tokenHeader); code != http.StatusOK || !strings.Contains(body, "pending_approval") {
		t.Fatalf("submit: %d %s", code, body)
	}

	// "Continue" never reveals whether an address exists.
	for _, addr := range []string{email, "nobody-" + email} {
		if code, _ = onbDo(t, client, http.MethodPost, base+"/onboarding/resume", "", fmt.Sprintf(`{"email":%q}`, addr), nil); code != http.StatusAccepted {
			t.Fatalf("resume %s: %d", addr, code)
		}
	}

	// The operator side: an ordinary user is refused, a superadmin decides.
	const password = "Test1234!"
	plainEmail := "plain-" + email
	registerUser(t, client, ts.URL, plainEmail, password)
	plainToken := loginUser(t, client, ts.URL, plainEmail, password)
	t.Cleanup(func() {
		_, _ = srv.pgxPool.Exec(ctx, `DELETE FROM users WHERE email = ANY($1)`, []string{plainEmail, "super-" + email})
	})
	if code, _ = onbDo(t, client, http.MethodGet, base+"/admin/onboarding/applications", plainToken, "", nil); code != http.StatusForbidden {
		t.Fatalf("ordinary user reads the queue: %d", code)
	}

	superEmail := "super-" + email
	registerUser(t, client, ts.URL, superEmail, password)
	var superID uuid.UUID
	_ = srv.pgxPool.QueryRow(ctx, `SELECT id FROM users WHERE email = $1`, superEmail).Scan(&superID)
	if _, err := srv.pgxPool.Exec(ctx, `INSERT INTO user_roles (user_id, role_id, org_id)
SELECT $1, id, NULL FROM roles WHERE name = 'platform_superadmin' AND org_id IS NULL`, superID); err != nil {
		t.Fatal(err)
	}
	superToken := loginUser(t, client, ts.URL, superEmail, password)

	code, body = onbDo(t, client, http.MethodGet, base+"/admin/onboarding/applications?status=pending_approval&q="+email, superToken, "", nil)
	if code != http.StatusOK || !strings.Contains(body, started.ApplicationID) {
		t.Fatalf("queue: %d %s", code, body)
	}
	// A decision without a reason header is refused before anything changes.
	if code, body = onbDo(t, client, http.MethodPost, base+"/admin/onboarding/applications/"+started.ApplicationID+"/approve", superToken, "", nil); code != http.StatusBadRequest {
		t.Fatalf("approve without reason: %d %s", code, body)
	}
	reason := map[string]string{"X-Admin-Reason": "onboarding test"}
	code, body = onbDo(t, client, http.MethodPost, base+"/admin/onboarding/applications/"+started.ApplicationID+"/approve", superToken, "", reason)
	if code != http.StatusOK || !strings.Contains(body, "org_id") {
		t.Fatalf("approve: %d %s", code, body)
	}
	// The applicant now sees the final status and cannot edit.
	if code, body = onbDo(t, client, http.MethodPut, appURL+"/answers", "", `{"answers":{"notes":"x"}}`, tokenHeader); code != http.StatusConflict {
		t.Fatalf("edit after approval: %d %s", code, body)
	}
	if code, body = onbDo(t, client, http.MethodGet, base+"/admin/onboarding/settings", superToken, "", nil); code != http.StatusOK || !strings.Contains(body, `"approval_mode":"manual"`) {
		t.Fatalf("settings: %d %s", code, body)
	}
}

func TestOnboarding_RoutesAbsentWhenDisabled(t *testing.T) {
	srv, _ := productionIntegrationServer(t)
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	code, _ := onbDo(t, ts.Client(), http.MethodGet, ts.URL+"/v1/onboarding/form-schema", "", "", nil)
	if code != http.StatusNotFound {
		t.Fatalf("form-schema with ONBOARDING_ENABLED off: %d, want 404", code)
	}
}
