//go:build integration

package himports

// event_bundle_venue_notify_integration_test.go — spec 08_architecture/64
// section 3.2: a venue an import creates tells every platform administrator,
// once. A dry run, and a bundle whose venue already exists, tell nobody.

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	venueplannotify "github.com/abhteam/arena_new/apps/backend/internal/platform/venueplan/notify"
)

func TestEventBundleImport_NewVenueNotifiesAdministratorsOnce(t *testing.T) {
	pool := import517Pool(t)
	ctx := context.Background()
	f := newBundle525Fixture(t, ctx, pool)
	rig := newDryRunRig(t, pool)

	// One platform superadmin with a live bot link, made for this test.
	suffix := uuid.NewString()[:8]
	adminEmail := "venue-notify-" + suffix + "@admin.example"
	chat := time.Now().UnixNano()%1_000_000_000 + 7_000_000_000
	var adminID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO users (email, password_hash) VALUES ($1, 'x') RETURNING id`, adminEmail).Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO user_roles (user_id, role_id, org_id)
SELECT $1, id, NULL FROM roles WHERE name = 'platform_superadmin' AND org_id IS NULL`, adminID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO bot_telegram_links (telegram_user_id, user_id) VALUES ($1, $2)`, chat, adminID); err != nil {
		t.Fatal(err)
	}
	var orgName string
	if err := pool.QueryRow(ctx, `SELECT name FROM organizations WHERE id = $1`, f.orgID).Scan(&orgName); err != nil {
		t.Fatal(err)
	}
	defer func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM worker_jobs WHERE job_type = 'venue_plan.notify' AND payload->>'venue_id' IN (SELECT id::text FROM venues WHERE org_id = $1)`, f.orgID)
		_, _ = pool.Exec(c, `DELETE FROM worker_jobs WHERE job_type IN ('admin_notify.email', 'admin_notify.telegram') AND payload::text LIKE '%' || $1 || '%'`, orgName)
		_, _ = pool.Exec(c, `DELETE FROM venue_plan_notify_log WHERE subject_id IN (SELECT id FROM venues WHERE org_id = $1)`, f.orgID)
		_, _ = pool.Exec(c, `DELETE FROM bot_telegram_links WHERE user_id = $1`, adminID)
		_, _ = pool.Exec(c, `DELETE FROM user_roles WHERE user_id = $1`, adminID)
		_, _ = pool.Exec(c, `DELETE FROM users WHERE id = $1`, adminID)
		f.cleanup()
	}()

	jobs := func() []string {
		rows, err := pool.Query(ctx, `SELECT payload::text FROM worker_jobs WHERE job_type = 'venue_plan.notify'
AND payload->>'kind' = 'venue_created' AND payload->>'venue_id' IN (SELECT id::text FROM venues WHERE org_id = $1)`, f.orgID)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var p string
			if err := rows.Scan(&p); err != nil {
				t.Fatal(err)
			}
			out = append(out, p)
		}
		return out
	}

	// A dry run creates nothing and tells nobody.
	dryRun(t, f, rig.h, f.payload())
	if n := len(jobs()); n != 0 {
		t.Fatalf("a dry run queued %d notify jobs", n)
	}

	// The real import creates the venue and queues ONE job.
	if rec, _ := f.call(rig.h, f.payload()); rec.Code != http.StatusOK {
		t.Fatalf("import: %d %s", rec.Code, rec.Body.String())
	}
	got := jobs()
	if len(got) != 1 {
		t.Fatalf("notify jobs after the import = %d, want 1", len(got))
	}

	// Running the job: one e-mail and one Telegram job for this administrator.
	handle := venueplannotify.NewHandler(venueplannotify.Options{Pool: pool, AppURL: "https://admin.example.test", Telegram: true})
	if err := handle(ctx, []byte(got[0])); err != nil {
		t.Fatalf("handler: %v", err)
	}
	count := func(sql string, args ...any) int {
		var n int
		if err := pool.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	emails := `SELECT count(*) FROM worker_jobs WHERE job_type = 'admin_notify.email' AND payload->>'email' = $1 AND payload::text LIKE '%' || $2 || '%'`
	chats := `SELECT count(*) FROM worker_jobs WHERE job_type = 'admin_notify.telegram' AND (payload->>'chat_id')::bigint = $1 AND payload->>'text' LIKE '%' || $2 || '%'`
	if n := count(emails, adminEmail, orgName); n != 1 {
		t.Errorf("e-mail jobs = %d, want 1", n)
	}
	if n := count(chats, chat, f.venueName); n != 1 {
		t.Errorf("telegram jobs = %d, want 1", n)
	}
	var text string
	if err := pool.QueryRow(ctx, `SELECT payload->>'text' FROM worker_jobs WHERE job_type = 'admin_notify.telegram' AND (payload->>'chat_id')::bigint = $1`, chat).Scan(&text); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, orgName) || !strings.Contains(text, "https://admin.example.test/venues") || strings.Contains(text, "bundle525-") {
		t.Errorf("telegram text: %s", text)
	}

	// The same job again adds nothing.
	if err := handle(ctx, []byte(got[0])); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if n := count(emails, adminEmail, orgName); n != 1 {
		t.Errorf("e-mail jobs after a repeat = %d, want 1", n)
	}
	if n := count(chats, chat, f.venueName); n != 1 {
		t.Errorf("telegram jobs after a repeat = %d, want 1", n)
	}

	// The same bundle again reuses the venue and queues nothing.
	if rec, _ := f.call(rig.h, f.payload()); rec.Code != http.StatusOK {
		t.Fatalf("second import: %d %s", rec.Code, rec.Body.String())
	}
	if n := len(jobs()); n != 1 {
		t.Errorf("notify jobs after a reused venue = %d, want 1", n)
	}
}
