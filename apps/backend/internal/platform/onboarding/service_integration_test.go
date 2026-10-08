//go:build integration

package onboarding

// Live-database test of the application lifecycle: draft -> confirm -> submit ->
// approve (workspace created) and the reject / request-info / purge branches.
//
//	DATABASE_URL=... JWT_SIGNING_SECRET=x go.exe test -tags integration \
//	  ./apps/backend/internal/platform/onboarding/ -run TestLifecycle

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type captureNotifier struct{ messages []string }

func (c *captureNotifier) Send(_ context.Context, text string) error {
	c.messages = append(c.messages, text)
	return nil
}

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func fullAnswers(country string) map[string]any {
	return map[string]any{
		"org_name": "Lifecycle Events", "legal_name": "Lifecycle Events SL", "country": country,
		"tax_id": "ES" + fmt.Sprint(time.Now().UnixNano()%1_000_000_000), "tax_id_scheme": "vat",
		"address_line1": "Calle Mayor 1", "address_postal_code": "28013", "address_city": "Madrid",
		"event_types": []any{"concert", "theatre"}, "seating": "ga", "events_per_year": "6-20",
		"tickets_per_year": "500-5k", "payment_provider": "stripe",
		"accept_terms": true, "accept_privacy": true, "confirm_authority": true,
	}
}

func cleanupApplication(t *testing.T, pool *pgxpool.Pool, email string) {
	t.Helper()
	ctx := context.Background()
	t.Cleanup(func() {
		var orgID *uuid.UUID
		_ = pool.QueryRow(ctx, `SELECT org_id FROM onboarding_applications WHERE applicant_email = $1 AND org_id IS NOT NULL`, email).Scan(&orgID)
		_, _ = pool.Exec(ctx, `DELETE FROM worker_jobs WHERE payload->>'email' = $1`, email)
		_, _ = pool.Exec(ctx, `DELETE FROM worker_jobs WHERE job_type = 'onboarding.notify' AND payload->>'application_id' IN (SELECT id::text FROM onboarding_applications WHERE applicant_email = $1)`, email)
		_, _ = pool.Exec(ctx, `DELETE FROM onboarding_applications WHERE applicant_email = $1`, email)
		if orgID != nil {
			_, _ = pool.Exec(ctx, `DELETE FROM memberships WHERE org_id = $1`, *orgID)
			_, _ = pool.Exec(ctx, `DELETE FROM sales_channels WHERE org_id = $1`, *orgID)
			_, _ = pool.Exec(ctx, `DELETE FROM organizations WHERE id = $1`, *orgID)
		}
		_, _ = pool.Exec(ctx, `DELETE FROM password_reset_tokens WHERE user_id IN (SELECT id FROM users WHERE email = $1)`, email)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE email = $1`, email)
	})
}

func TestLifecycle_DraftToApproval(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	notifier := &captureNotifier{}
	svc := New(Options{Pool: pool, Notifier: notifier, AdminURL: "https://admin.example.test"})
	email := fmt.Sprintf("onb-life-%d@example.com", time.Now().UnixNano())
	cleanupApplication(t, pool, email)

	started, err := svc.Start(ctx, StartInput{FirstName: "Ana", LastName: "Pérez", Email: strings.ToUpper(email), Phone: "+34 600 111 222", Locale: "es-ES"})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	app, token := started.App, started.AccessToken
	if app.Status != StatusDraft || app.Locale != "es" || app.Email != email {
		t.Fatalf("started = %+v", app)
	}

	var jobs int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM worker_jobs WHERE job_type = 'onboarding.email' AND payload->>'email' = $1 AND payload->>'kind' = 'confirm'`, email).Scan(&jobs); err != nil || jobs != 1 {
		t.Fatalf("confirm e-mail jobs = %d (%v), want 1", jobs, err)
	}

	// A wrong token sees nothing.
	if _, err := svc.Get(ctx, app.ID, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get with a wrong token: %v", err)
	}
	// A bad value is refused and nothing is saved.
	if _, err := svc.SaveAnswers(ctx, app.ID, token, map[string]any{"org_name": "X", "country": "Spain"}); err == nil {
		t.Fatal("country 'Spain' was accepted")
	} else {
		var fe *FieldsError
		if !errors.As(err, &fe) || fe.Fields["country"] != "invalid" {
			t.Fatalf("error = %v", err)
		}
	}
	got, err := svc.Get(ctx, app.ID, token)
	if err != nil || got.Answers.String("org_name") != "" {
		t.Fatalf("a refused patch leaked: %+v %v", got, err)
	}

	// Submit before the e-mail is confirmed is refused.
	if _, err := svc.SaveAnswers(ctx, app.ID, token, fullAnswers("ES")); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := svc.Submit(ctx, app.ID, token); !errors.Is(err, ErrEmailNotConfirmed) {
		t.Fatalf("submit before confirm: %v", err)
	}

	// The operator does not see an unconfirmed draft.
	list, err := svc.List(ctx, ListFilter{Query: email})
	if err != nil || list.Total != 0 {
		t.Fatalf("operator sees the unconfirmed draft: %+v %v", list, err)
	}

	// Confirm with the link token read from the queued job.
	var link string
	if err := pool.QueryRow(ctx, `SELECT payload->>'token' FROM worker_jobs WHERE job_type = 'onboarding.email' AND payload->>'email' = $1 ORDER BY created_at LIMIT 1`, email).Scan(&link); err != nil || link == "" {
		t.Fatalf("link token: %q %v", link, err)
	}
	confirmed, err := svc.Confirm(ctx, link)
	if err != nil || confirmed.App.EmailConfirmedAt == nil {
		t.Fatalf("confirm: %+v %v", confirmed, err)
	}
	if _, err := svc.Get(ctx, app.ID, token); !errors.Is(err, ErrNotFound) {
		t.Fatal("the old access token still works after the link was opened")
	}
	token = confirmed.AccessToken

	// Incomplete -> 422 with the field list.
	if _, err := svc.SaveAnswers(ctx, app.ID, token, map[string]any{"payment_provider": nil}); err != nil {
		t.Fatalf("clear field: %v", err)
	}
	if _, err := svc.Submit(ctx, app.ID, token); err == nil {
		t.Fatal("incomplete application was submitted")
	} else {
		var fe *FieldsError
		if !errors.As(err, &fe) || fe.Code != "incomplete" || fe.Fields["payment_provider"] != "required" {
			t.Fatalf("incomplete error = %v", err)
		}
	}
	if _, err := svc.SaveAnswers(ctx, app.ID, token, map[string]any{"payment_provider": "stripe"}); err != nil {
		t.Fatal(err)
	}

	submitted, err := svc.Submit(ctx, app.ID, token)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if submitted.Status != StatusPendingApproval || submitted.TermsVersion == nil {
		t.Fatalf("submitted = %+v", submitted)
	}
	if _, err := svc.SaveAnswers(ctx, app.ID, token, map[string]any{"notes": "late"}); !errors.Is(err, ErrLocked) {
		t.Fatalf("edit after submit: %v", err)
	}
	var notifyPayload []byte
	if err := pool.QueryRow(ctx, `SELECT payload FROM worker_jobs WHERE job_type = 'onboarding.notify' AND payload->>'application_id' = $1`, app.ID.String()).Scan(&notifyPayload); err != nil {
		t.Fatalf("operator message job: %v", err)
	}
	if err := svc.HandleNotify(ctx, notifyPayload); err != nil {
		t.Fatalf("send operator message: %v", err)
	}
	if len(notifier.messages) != 1 || !strings.Contains(notifier.messages[0], email) || !strings.Contains(notifier.messages[0], "+34600111222") {
		t.Fatalf("operator message = %v (want e-mail and phone)", notifier.messages)
	}

	list, err = svc.List(ctx, ListFilter{Status: StatusPendingApproval, Query: email})
	if err != nil || list.Total != 1 || list.Items[0].ID != app.ID {
		t.Fatalf("queue: %+v %v", list, err)
	}
	detail, err := svc.Detail(ctx, app.ID)
	if err != nil || len(detail.Checks) != len(checkKeys) {
		t.Fatalf("detail: %+v %v", detail, err)
	}
	for _, c := range detail.Checks {
		if c.Result == CheckFail {
			t.Fatalf("check %s failed: %s", c.Key, c.Detail)
		}
	}

	reviewer := uuid.New()
	_, _ = pool.Exec(ctx, `INSERT INTO users (id, email, password_hash) VALUES ($1, $2, 'x')`, reviewer, "reviewer-"+email)
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, reviewer) })
	res, err := svc.Approve(ctx, app.ID, operatorActor(reviewer), reviewer)
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if !res.OwnerCreated {
		t.Fatal("owner account was not created")
	}
	var role, kyb, mode string
	if err := pool.QueryRow(ctx, `SELECT m.role, o.kyb_status, c.payment_mode FROM memberships m
 JOIN organizations o ON o.id = m.org_id JOIN sales_channels c ON c.org_id = o.id
 WHERE m.org_id = $1 AND m.user_id = $2`, res.OrgID, res.OwnerID).Scan(&role, &kyb, &mode); err != nil {
		t.Fatalf("workspace rows: %v", err)
	}
	if role != "org_admin" || kyb != "unverified" || mode != "direct_merchant" {
		t.Fatalf("workspace = role %s kyb %s mode %s", role, kyb, mode)
	}
	var setups int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM worker_jobs WHERE job_type = 'auth.password_reset_email' AND payload->>'email' = $1`, email).Scan(&setups)
	if setups != 1 {
		t.Fatalf("password setup e-mails = %d, want 1", setups)
	}
	final, _ := svc.Detail(ctx, app.ID)
	if final.Application.Status != StatusApproved || final.Application.OrgID == nil {
		t.Fatalf("after approval: %+v", final.Application)
	}
	if _, err := svc.Approve(ctx, app.ID, operatorActor(reviewer), reviewer); !errors.Is(err, ErrWrongState) {
		t.Fatalf("second approval: %v", err)
	}
}

func TestLifecycle_RequestInfoRejectAndPurge(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	svc := New(Options{Pool: pool})
	email := fmt.Sprintf("onb-info-%d@example.com", time.Now().UnixNano())
	cleanupApplication(t, pool, email)
	reviewer := uuid.New()
	_, _ = pool.Exec(ctx, `INSERT INTO users (id, email, password_hash) VALUES ($1, $2, 'x')`, reviewer, "reviewer-"+email)
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, reviewer) })

	st, err := svc.Start(ctx, StartInput{FirstName: "Bo", LastName: "Li", Email: email, Phone: "+420777111222"})
	if err != nil {
		t.Fatal(err)
	}
	id := st.App.ID
	var link string
	_ = pool.QueryRow(ctx, `SELECT payload->>'token' FROM worker_jobs WHERE job_type = 'onboarding.email' AND payload->>'email' = $1 LIMIT 1`, email).Scan(&link)
	conf, err := svc.Confirm(ctx, link)
	if err != nil {
		t.Fatal(err)
	}
	token := conf.AccessToken
	if _, err := svc.SaveAnswers(ctx, id, token, fullAnswers("CZ")); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Submit(ctx, id, token); err != nil {
		t.Fatal(err)
	}

	// Request details: only the requested fields may change.
	if err := svc.RequestInfo(ctx, id, reviewer, []string{"website"}, "Please send your website"); err != nil {
		t.Fatalf("request info: %v", err)
	}
	conf, err = svc.Confirm(ctx, mustLatestToken(t, pool, email))
	if err != nil {
		t.Fatalf("open the new link: %v", err)
	}
	token = conf.AccessToken
	if _, err := svc.SaveAnswers(ctx, id, token, map[string]any{"org_name": "Changed"}); err == nil {
		t.Fatal("a field that was not requested could be edited")
	}
	if _, err := svc.SaveAnswers(ctx, id, token, map[string]any{"website": "example.org"}); err != nil {
		t.Fatalf("edit the requested field: %v", err)
	}
	got, err := svc.Submit(ctx, id, token)
	if err != nil || got.Status != StatusPendingApproval || got.Answers.String("website") != "https://example.org" {
		t.Fatalf("resubmit: %+v %v", got, err)
	}

	if err := svc.Reject(ctx, id, reviewer, "  ", ""); err == nil {
		t.Fatal("rejected without a reason")
	}
	if err := svc.Reject(ctx, id, reviewer, "not a real organizer", ""); err != nil {
		t.Fatalf("reject: %v", err)
	}
	if err := svc.Purge(ctx, id, reviewer); err != nil {
		t.Fatalf("purge: %v", err)
	}
	var mail string
	var answers string
	if err := pool.QueryRow(ctx, `SELECT applicant_email, answers::text FROM onboarding_applications WHERE id = $1`, id).Scan(&mail, &answers); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(mail, "example.com") || answers != "{}" {
		t.Fatalf("personal data survived the purge: %s %s", mail, answers)
	}
	if _, err := svc.Get(ctx, id, token); !errors.Is(err, ErrNotFound) {
		t.Fatal("a purged application is still readable")
	}
}

func mustLatestToken(t *testing.T, pool *pgxpool.Pool, email string) string {
	t.Helper()
	var tok string
	if err := pool.QueryRow(context.Background(), `SELECT payload->>'token' FROM worker_jobs
 WHERE job_type = 'onboarding.email' AND payload->>'email' = $1 AND coalesce(payload->>'token','') <> ''
 ORDER BY created_at DESC LIMIT 1`, email).Scan(&tok); err != nil {
		t.Fatalf("latest token: %v", err)
	}
	return tok
}

func TestSweep_ExpiresRemindsAndPurges(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	svc := New(Options{Pool: pool})
	email := fmt.Sprintf("onb-sweep-%d@example.com", time.Now().UnixNano())
	cleanupApplication(t, pool, email)

	st, err := svc.Start(ctx, StartInput{FirstName: "Sy", LastName: "Wu", Email: email, Phone: "+34600999888"})
	if err != nil {
		t.Fatal(err)
	}
	id := st.App.ID
	if _, err := svc.Confirm(ctx, mustLatestToken(t, pool, email)); err != nil {
		t.Fatal(err)
	}

	// Quiet for 4 days -> the first reminder.
	_, _ = pool.Exec(ctx, `UPDATE onboarding_applications SET last_activity_at = now() - interval '4 days' WHERE id = $1`, id)
	if _, err := svc.Sweep(ctx); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	var sent int
	_ = pool.QueryRow(ctx, `SELECT reminders_sent FROM onboarding_applications WHERE id = $1`, id).Scan(&sent)
	if sent != 1 {
		t.Fatalf("reminders_sent = %d, want 1", sent)
	}
	// A second run the same day sends nothing more.
	if _, err := svc.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	_ = pool.QueryRow(ctx, `SELECT reminders_sent FROM onboarding_applications WHERE id = $1`, id).Scan(&sent)
	if sent != 1 {
		t.Fatalf("reminders_sent = %d after a repeat run, want 1", sent)
	}

	// Past its expiry -> expired, then purged after the retention.
	_, _ = pool.Exec(ctx, `UPDATE onboarding_applications SET expires_at = now() - interval '1 day' WHERE id = $1`, id)
	if _, err := svc.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	var status string
	_ = pool.QueryRow(ctx, `SELECT status FROM onboarding_applications WHERE id = $1`, id).Scan(&status)
	if status != StatusExpired {
		t.Fatalf("status = %s, want expired", status)
	}
	_, _ = pool.Exec(ctx, `UPDATE onboarding_applications SET updated_at = now() - interval '400 days' WHERE id = $1`, id)
	if _, err := svc.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	var purged *time.Time
	_ = pool.QueryRow(ctx, `SELECT purged_at FROM onboarding_applications WHERE id = $1`, id).Scan(&purged)
	if purged == nil {
		t.Fatal("an old expired application was not purged")
	}
}
