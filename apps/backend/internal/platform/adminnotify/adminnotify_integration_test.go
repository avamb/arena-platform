//go:build integration

package adminnotify

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestFanout_OneEmailAndOneTelegramJobPerAdmin(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

	nonce := uuid.NewString()[:8]
	var users []uuid.UUID
	var chats []int64
	mkUser := func(name string) (uuid.UUID, string) {
		email := fmt.Sprintf("%s-%s@fanout.example", name, nonce)
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
	tgID := time.Now().UnixNano()%1_000_000_000 + 8_000_000_000
	live, liveEmail := mkUser("super-live")
	globalRole(live, "platform_superadmin")
	linkTG(live, tgID, false)
	revoked, revokedEmail := mkUser("super-revoked")
	globalRole(revoked, "platform_superadmin")
	linkTG(revoked, tgID+1, true)
	orgAdmin, orgAdminEmail := mkUser("org-admin")
	globalRole(orgAdmin, "org_admin")
	linkTG(orgAdmin, tgID+2, false)

	title := "Fanout test " + nonce
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM worker_jobs WHERE job_type = $1 AND payload->>'title' = $2`, JobTypeEmail, title)
		_, _ = pool.Exec(c, `DELETE FROM worker_jobs WHERE job_type = $1 AND (payload->>'chat_id')::bigint = ANY($2)`, JobTypeTelegram, chats)
		_, _ = pool.Exec(c, `DELETE FROM bot_telegram_links WHERE user_id = ANY($1)`, users)
		_, _ = pool.Exec(c, `DELETE FROM user_roles WHERE user_id = ANY($1)`, users)
		_, _ = pool.Exec(c, `DELETE FROM users WHERE id = ANY($1)`, users)
	})

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	n, err := Fanout(ctx, tx, Message{Title: title, Details: []string{"a"}, Link: "https://admin.example.test/x", ButtonLabel: "Open"}, Options{Telegram: true})
	if err != nil {
		t.Fatalf("Fanout: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if n < 2 {
		t.Errorf("admins = %d, want at least 2 (the shared database may hold more)", n)
	}

	count := func(q string, args ...any) int {
		var c int
		if err := pool.QueryRow(ctx, q, args...).Scan(&c); err != nil {
			t.Fatal(err)
		}
		return c
	}
	for _, em := range []string{liveEmail, revokedEmail} {
		if c := count(`SELECT count(*) FROM worker_jobs WHERE job_type = $1 AND payload->>'title' = $2 AND payload->>'email' = $3`, JobTypeEmail, title, em); c != 1 {
			t.Errorf("e-mail jobs for %s = %d, want 1", em, c)
		}
	}
	if c := count(`SELECT count(*) FROM worker_jobs WHERE job_type = $1 AND payload->>'title' = $2 AND payload->>'email' = $3`, JobTypeEmail, title, orgAdminEmail); c != 0 {
		t.Errorf("org admin got %d e-mail jobs", c)
	}
	if c := count(`SELECT count(*) FROM worker_jobs WHERE job_type = $1 AND (payload->>'chat_id')::bigint = $2`, JobTypeTelegram, tgID); c != 1 {
		t.Errorf("telegram jobs for the live chat = %d, want 1", c)
	}
	if c := count(`SELECT count(*) FROM worker_jobs WHERE job_type = $1 AND (payload->>'chat_id')::bigint = ANY($2)`, JobTypeTelegram, []int64{tgID + 1, tgID + 2}); c != 0 {
		t.Errorf("revoked link or org admin got %d telegram jobs", c)
	}
}
