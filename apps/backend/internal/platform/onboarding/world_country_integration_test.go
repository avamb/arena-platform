//go:build integration

package onboarding

// world_country_integration_test.go — spec 64 section 2.1.1, "a new country at
// onboarding": an application may name any country of the world table; a
// country outside the reference is a WARNING of country_allowed (so nothing is
// approved automatically), nothing is created before approval, and approval
// creates the reference row and queues ONE "new country" notification that
// reaches every platform administrator once.

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	venueplannotify "github.com/abhteam/arena_new/apps/backend/internal/platform/venueplan/notify"
)

const wcISO2 = "BT"

func wcPurgeCountry(ctx context.Context, pool *pgxpool.Pool) error {
	var id *uuid.UUID
	_ = pool.QueryRow(ctx, `SELECT id FROM countries WHERE iso2 = $1`, wcISO2).Scan(&id)
	if id != nil {
		for _, s := range []struct {
			sql string
			arg any
		}{
			{`DELETE FROM worker_jobs WHERE job_type = 'venue_plan.notify' AND payload->>'country_id' = $1`, id.String()},
			{`DELETE FROM venue_plan_notify_log WHERE subject_id = $1`, *id},
			{`DELETE FROM countries WHERE id = $1`, *id},
		} {
			if _, err := pool.Exec(ctx, s.sql, s.arg); err != nil {
				return fmt.Errorf("%s: %w", s.sql, err)
			}
		}
	}
	_, err := pool.Exec(ctx, `DELETE FROM i18n_text WHERE namespace = 'geo.countries' AND key = $1`, wcISO2)
	return err
}

func TestWorldCountry_ApprovalCreatesCountryAndOneNotification(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	if err := wcPurgeCountry(ctx, pool); err != nil {
		t.Skipf("the database already uses %s: %v", wcISO2, err)
	}
	t.Cleanup(func() { _ = wcPurgeCountry(context.Background(), pool) })

	svc := New(Options{Pool: pool, Notifier: &captureNotifier{}, AdminURL: "https://admin.example.test"})
	email := fmt.Sprintf("onb-wc-%d@example.com", time.Now().UnixNano())
	cleanupApplication(t, pool, email)
	orgName := "WC Dragon Events " + uuid.NewString()[:8]

	// A platform administrator who must hear of the new country once.
	staff := uuid.New()
	staffEmail := "wc-staff-" + staff.String()[:8] + "@example.test"
	if _, err := pool.Exec(ctx, `INSERT INTO users (id, email, password_hash) VALUES ($1, $2, 'x')`, staff, staffEmail); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO user_roles (user_id, role_id, org_id)
		SELECT $1, id, NULL FROM roles WHERE name = 'platform_superadmin' AND org_id IS NULL`, staff); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM worker_jobs WHERE job_type IN ('admin_notify.email', 'admin_notify.telegram') AND payload::text LIKE '%' || $1 || '%'`, orgName)
		_, _ = pool.Exec(c, `DELETE FROM user_roles WHERE user_id = $1`, staff)
		_, _ = pool.Exec(c, `DELETE FROM users WHERE id = $1`, staff)
	})

	started, err := svc.Start(ctx, StartInput{FirstName: "Tashi", LastName: "Dorji", Email: email, Phone: "+975 17 111 222", Locale: "en"})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	app, token := started.App, started.AccessToken
	// An unknown code is refused as invalid; a world country is accepted.
	if _, err := svc.SaveAnswers(ctx, app.ID, token, map[string]any{"country": "ZZ"}); err == nil {
		t.Fatal("country ZZ was accepted")
	}
	answers := fullAnswers(wcISO2)
	answers["org_name"] = orgName
	answers["address_country"] = wcISO2
	if _, err := svc.SaveAnswers(ctx, app.ID, token, answers); err != nil {
		t.Fatalf("save with a country outside the reference: %v", err)
	}
	var link string
	if err := pool.QueryRow(ctx, `SELECT payload->>'token' FROM worker_jobs WHERE job_type = 'onboarding.email' AND payload->>'email' = $1 ORDER BY created_at LIMIT 1`, email).Scan(&link); err != nil {
		t.Fatalf("link token: %v", err)
	}
	confirmed, err := svc.Confirm(ctx, link)
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if _, err := svc.Submit(ctx, app.ID, confirmed.AccessToken); err != nil {
		t.Fatalf("submit: %v", err)
	}
	detail, err := svc.Detail(ctx, app.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range detail.Checks {
		if c.Key == "country_allowed" {
			found = true
			if c.Result != CheckWarn {
				t.Errorf("country_allowed = %s (%s), want warn", c.Result, c.Detail)
			}
		}
	}
	if !found || WouldApprove(detail.Checks) {
		t.Fatalf("checks = %+v: a new country must keep automatic approval off", detail.Checks)
	}
	var n int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM countries WHERE iso2 = $1`, wcISO2).Scan(&n)
	if n != 0 {
		t.Fatal("the country was created before approval")
	}

	reviewer := uuid.New()
	_, _ = pool.Exec(ctx, `INSERT INTO users (id, email, password_hash) VALUES ($1, $2, 'x')`, reviewer, "reviewer-"+email)
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, reviewer) })
	res, err := svc.Approve(ctx, app.ID, operatorActor(reviewer), reviewer)
	if err != nil {
		t.Fatalf("approve: %v", err)
	}

	var iso3, currency, region string
	if err := pool.QueryRow(ctx, `SELECT iso3, btrim(currency), region FROM countries WHERE iso2 = $1`, wcISO2).Scan(&iso3, &currency, &region); err != nil {
		t.Fatalf("country row after approval: %v", err)
	}
	if iso3 != "BTN" || currency != "BTN" || region != "asia" {
		t.Errorf("country row = %s %s %s", iso3, currency, region)
	}
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM i18n_text WHERE namespace = 'geo.countries' AND key = $1 AND
		((locale = 'en' AND value = 'Bhutan') OR (locale = 'ru' AND value = 'Бутан'))`, wcISO2).Scan(&n)
	if n != 2 {
		t.Errorf("country names = %d, want 2", n)
	}

	var payloads []string
	rows, err := pool.Query(ctx, `SELECT payload::text FROM worker_jobs WHERE job_type = 'venue_plan.notify'
		AND payload->>'kind' = 'country_created' AND payload->>'country_iso2' = $1 AND payload->>'org_id' = $2`, wcISO2, res.OrgID.String())
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var p string
		_ = rows.Scan(&p)
		payloads = append(payloads, p)
	}
	rows.Close()
	if len(payloads) != 1 {
		t.Fatalf("country_created jobs = %d, want 1", len(payloads))
	}

	handle := venueplannotify.NewHandler(venueplannotify.Options{Pool: pool, AppURL: "https://admin.example.test", Telegram: true})
	title := "Новая страна: Бутан, заявка " + orgName
	for run := 1; run <= 2; run++ {
		if err := handle(ctx, []byte(payloads[0])); err != nil {
			t.Fatalf("notify run %d: %v", run, err)
		}
		var mails int
		var details string
		if err := pool.QueryRow(ctx, `SELECT count(*), COALESCE(max(payload->>'details'), '') FROM worker_jobs WHERE job_type = 'admin_notify.email'
			AND payload->>'email' = $1 AND payload->>'title' = $2`, staffEmail, title).Scan(&mails, &details); err != nil {
			t.Fatal(err)
		}
		if mails != 1 {
			t.Fatalf("run %d: e-mail jobs for the administrator = %d, want 1", run, mails)
		}
		for _, want := range []string{"Организация: " + orgName, "Страна: Бутан (BT)", "Валюта: BTN"} {
			if !strings.Contains(details, want) {
				t.Errorf("the message lacks %q: %s", want, details)
			}
		}
		for _, secret := range []string{email, "Tashi", "Dorji"} {
			if strings.Contains(details, secret) {
				t.Errorf("the message carries %q of the applicant", secret)
			}
		}
	}
}
