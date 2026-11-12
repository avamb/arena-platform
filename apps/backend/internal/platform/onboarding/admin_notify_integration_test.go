//go:build integration

package onboarding

// A new application reaches every platform superadmin once, however often the
// onboarding.notify job runs; an organization admin gets nothing.

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestHandleNotify_TellsEverySuperadminOnce(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	notifier := &captureNotifier{}
	svc := New(Options{Pool: pool, Notifier: notifier, AdminURL: "https://admin.example.test", AdminTelegram: true})

	nonce := uuid.NewString()[:8]
	var users []uuid.UUID
	var chats []int64
	mkUser := func(name string) (uuid.UUID, string) {
		email := fmt.Sprintf("%s-%s@notify.example", name, nonce)
		var id uuid.UUID
		if err := pool.QueryRow(ctx, `INSERT INTO users (email, password_hash) VALUES ($1, 'x') RETURNING id`, email).Scan(&id); err != nil {
			t.Fatal(err)
		}
		users = append(users, id)
		return id, email
	}
	globalRole := func(id uuid.UUID, role string) {
		if _, err := pool.Exec(ctx, `INSERT INTO user_roles (user_id, role_id, org_id)
SELECT $1, id, NULL FROM roles WHERE name = $2 AND org_id IS NULL`, id, role); err != nil {
			t.Fatal(err)
		}
	}
	linkTG := func(id uuid.UUID, tg int64, revoked bool) {
		chats = append(chats, tg)
		rev := "NULL"
		if revoked {
			rev = "now()"
		}
		if _, err := pool.Exec(ctx, `INSERT INTO bot_telegram_links (telegram_user_id, user_id, revoked_at) VALUES ($1, $2, `+rev+`)`, tg, id); err != nil {
			t.Fatal(err)
		}
	}
	linked, linkedEmail := mkUser("super-linked")
	globalRole(linked, "platform_superadmin")
	tgID := time.Now().UnixNano()%1_000_000_000 + 7_000_000_000
	linkTG(linked, tgID, false)
	linkTG(linked, tgID+1, true) // a revoked link gets nothing
	plain, plainEmail := mkUser("super-plain")
	globalRole(plain, "platform_superadmin")
	orgAdmin, orgAdminEmail := mkUser("org-admin")
	globalRole(orgAdmin, "org_admin")
	linkTG(orgAdmin, tgID+2, false)

	appEmail := fmt.Sprintf("onb-notify-%s@example.com", nonce)
	cleanupApplication(t, pool, appEmail)
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM worker_jobs WHERE job_type IN ('onboarding.email', 'onboarding.admin_telegram') AND payload::text LIKE '%'||$1||'%'`, nonce)
		_, _ = pool.Exec(c, `DELETE FROM worker_jobs WHERE job_type = 'onboarding.admin_telegram' AND (payload->>'chat_id')::bigint = ANY($1)`, chats)
		_, _ = pool.Exec(c, `DELETE FROM bot_telegram_links WHERE user_id = ANY($1)`, users)
		_, _ = pool.Exec(c, `DELETE FROM user_roles WHERE user_id = ANY($1)`, users)
		_, _ = pool.Exec(c, `DELETE FROM users WHERE id = ANY($1)`, users)
	})

	started, err := svc.Start(ctx, StartInput{FirstName: "Ana", LastName: "Pérez", Email: appEmail, Phone: "+34 600 111 222", Locale: "es"})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	app := started.App
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM worker_jobs WHERE job_type = 'onboarding.email' AND payload->>'application_id' = $1`, app.ID.String())
	})
	if err := addEvent(ctx, pool, app.ID, "submitted", ActorApplicant, nil, nil); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(NotifyPayload{Kind: NotifySubmitted, ApplicationID: app.ID.String(), WouldApprove: true})
	for i := 0; i < 2; i++ {
		if err := svc.HandleNotify(ctx, payload); err != nil {
			t.Fatalf("HandleNotify run %d: %v", i+1, err)
		}
	}

	count := func(q string, args ...any) int {
		var n int
		if err := pool.QueryRow(ctx, q, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	for _, em := range []string{linkedEmail, plainEmail} {
		if n := count(`SELECT count(*) FROM worker_jobs WHERE job_type = 'onboarding.email' AND payload->>'kind' = 'admin_new_application'
AND payload->>'application_id' = $1 AND payload->>'email' = $2`, app.ID.String(), em); n != 1 {
			t.Errorf("e-mail jobs for %s = %d, want 1", em, n)
		}
	}
	if n := count(`SELECT count(*) FROM worker_jobs WHERE job_type = 'onboarding.email' AND payload->>'application_id' = $1 AND payload->>'email' = $2`, app.ID.String(), orgAdminEmail); n != 0 {
		t.Errorf("org admin got %d e-mail jobs", n)
	}
	if n := count(`SELECT count(*) FROM worker_jobs WHERE job_type = 'onboarding.admin_telegram' AND (payload->>'chat_id')::bigint = $1`, tgID); n != 1 {
		t.Errorf("telegram jobs for the linked superadmin = %d, want 1", n)
	}
	if n := count(`SELECT count(*) FROM worker_jobs WHERE job_type = 'onboarding.admin_telegram' AND (payload->>'chat_id')::bigint = ANY($1)`, []int64{tgID + 1, tgID + 2}); n != 0 {
		t.Errorf("revoked link or org admin got %d telegram jobs", n)
	}
	if n := count(`SELECT count(*) FROM onboarding_application_events WHERE application_id = $1 AND kind = 'admins_notified'`, app.ID); n != 1 {
		t.Errorf("admins_notified markers = %d, want 1", n)
	}
	if len(notifier.messages) != 0 {
		t.Errorf("the ops chat got %v, want nothing while admins exist", notifier.messages)
	}
	detail, err := svc.Detail(ctx, app.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range detail.Events {
		if e.Kind == "admins_notified" {
			t.Error("the admin timeline shows the internal marker")
		}
	}

	// A resubmission is a new round: told again, once.
	if err := addEvent(ctx, pool, app.ID, "resubmitted", ActorApplicant, nil, nil); err != nil {
		t.Fatal(err)
	}
	again, _ := json.Marshal(NotifyPayload{Kind: NotifyResubmitted, ApplicationID: app.ID.String()})
	for i := 0; i < 2; i++ {
		if err := svc.HandleNotify(ctx, again); err != nil {
			t.Fatal(err)
		}
	}
	if n := count(`SELECT count(*) FROM worker_jobs WHERE job_type = 'onboarding.email' AND payload->>'application_id' = $1 AND payload->>'email' = $2`, app.ID.String(), plainEmail); n != 2 {
		t.Errorf("e-mail jobs after a resubmission = %d, want 2", n)
	}
}
