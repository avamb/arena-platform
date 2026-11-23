//go:build integration

package conversions

import (
	"context"
	"encoding/base64"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/adminnotify"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/metaconnect"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/secretbox"
)

// TestConversions_MetaConnectionBroken_OwnerNotice_LiveDB: the 190 that moves
// a connection from active to error queues, in the same transaction, one
// e-mail per owner and one Telegram job per live bot link of that owner. A
// second 190 while in error queues nothing; a manager, an agent, a
// deactivated owner and another organization's owner are never told; a
// reconnect followed by a new 190 tells the owners again. Without Telegram
// only e-mails go.
func TestConversions_MetaConnectionBroken_OwnerNotice_LiveDB(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	box, err := secretbox.ParseKeys("v1:" + base64.StdEncoding.EncodeToString([]byte(strings.Repeat("w", 32))))
	if err != nil {
		t.Fatal(err)
	}
	const plainToken = "EAAB-owner-notice-token-77"

	orgA, orgB := uuid.New(), uuid.New()
	nonce := orgA.String()[:8]
	var users []uuid.UUID
	var chats []int64
	tgBase := time.Now().UnixNano()%1_000_000_000 + 7_000_000_000
	defer func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM worker_jobs WHERE job_type = $1 AND payload->>'user_id' = ANY($2)`, adminnotify.JobTypeEmail, uuidStrings(users))
		_, _ = pool.Exec(c, `DELETE FROM worker_jobs WHERE job_type = $1 AND (payload->>'chat_id')::bigint = ANY($2)`, adminnotify.JobTypeTelegram, chats)
		_, _ = pool.Exec(c, `DELETE FROM bot_telegram_links WHERE user_id = ANY($1)`, users)
		_, _ = pool.Exec(c, `DELETE FROM organizations WHERE id = ANY($1)`, []uuid.UUID{orgA, orgB})
		_, _ = pool.Exec(c, `DELETE FROM users WHERE id = ANY($1)`, users)
	}()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("fixture %q: %v", sql, err)
		}
	}
	for _, o := range []uuid.UUID{orgA, orgB} {
		exec(`INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`, o, "Owner Notice "+o.String()[:8], "onotice-"+o.String()[:8])
	}
	member := func(org uuid.UUID, role string, links ...bool) uuid.UUID {
		t.Helper()
		id := uuid.New()
		users = append(users, id)
		exec(`INSERT INTO users (id, email, password_hash) VALUES ($1, $2, 'x')`, id, "onotice-"+id.String()[:8]+"-"+nonce+"@example.test")
		exec(`INSERT INTO memberships (user_id, org_id, role) VALUES ($1, $2, $3)`, id, org, role)
		for _, revoked := range links {
			tg := tgBase + int64(len(chats))
			chats = append(chats, tg)
			if revoked {
				exec(`INSERT INTO bot_telegram_links (telegram_user_id, user_id, revoked_at) VALUES ($1, $2, now())`, tg, id)
			} else {
				exec(`INSERT INTO bot_telegram_links (telegram_user_id, user_id) VALUES ($1, $2)`, tg, id)
			}
		}
		return id
	}
	owner1 := member(orgA, "org_admin", false, true) // one live chat, one revoked
	owner2 := member(orgA, "org_admin")              // e-mail only
	manager := member(orgA, "organizer", false)
	agent := member(orgA, "agent", false)
	gone := member(orgA, "org_admin", false)
	exec(`UPDATE users SET deactivated_at = now() WHERE id = $1`, gone)
	ownerB := member(orgB, "org_admin", false)

	connect := func(org uuid.UUID) {
		t.Helper()
		sealed, err := metaconnect.SealToken(box, org, plainToken)
		if err != nil {
			t.Fatal(err)
		}
		exec(`INSERT INTO org_meta_connections (org_id, meta_user_id, access_token_enc, status) VALUES ($1, $2, $3, 'active')
		      ON CONFLICT (org_id) DO UPDATE SET status = 'active', access_token_enc = EXCLUDED.access_token_enc, last_error = NULL`,
			org, "mu-"+org.String()[:8], sealed)
	}
	connect(orgA)
	connect(orgB)

	emails := func(user uuid.UUID) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM worker_jobs WHERE job_type = $1 AND payload->>'user_id' = $2 AND payload->>'kind' = $3`,
			adminnotify.JobTypeEmail, user.String(), adminnotify.KindMetaConnectionBroken).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	telegrams := func(chat int64) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM worker_jobs WHERE job_type = $1 AND (payload->>'chat_id')::bigint = $2`,
			adminnotify.JobTypeTelegram, chat).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	check := func(stage string, want int) {
		t.Helper()
		if n := emails(owner1); n != want {
			t.Errorf("%s: owner1 e-mails = %d, want %d", stage, n, want)
		}
		if n := emails(owner2); n != want {
			t.Errorf("%s: owner2 e-mails = %d, want %d", stage, n, want)
		}
		if n := telegrams(chats[0]); n != want {
			t.Errorf("%s: owner1 live chat jobs = %d, want %d", stage, n, want)
		}
		if n := telegrams(chats[1]); n != 0 {
			t.Errorf("%s: revoked link got %d jobs", stage, n)
		}
		for name, u := range map[string]uuid.UUID{"manager": manager, "agent": agent, "deactivated owner": gone, "other org owner": ownerB} {
			if n := emails(u); n != 0 {
				t.Errorf("%s: %s got %d e-mails", stage, name, n)
			}
		}
		for i := 2; i < len(chats); i++ {
			if n := telegrams(chats[i]); n != 0 {
				t.Errorf("%s: chat %d (not an owner of A) got %d jobs", stage, i, n)
			}
		}
	}

	store := NewPGStore(pool).WithMetaBox(box).WithOwnerNotice("https://events.example.test", true, slog.Default())
	changed, err := store.MarkMetaTokenInvalid(ctx, orgA, "graph_190")
	if err != nil || !changed {
		t.Fatalf("first 190: changed=%v err=%v", changed, err)
	}
	check("first 190", 1)
	var title, link string
	if err := pool.QueryRow(ctx, `SELECT payload->>'title', payload->>'link' FROM worker_jobs WHERE job_type = $1 AND payload->>'user_id' = $2`,
		adminnotify.JobTypeEmail, owner1.String()).Scan(&title, &link); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(title, "Owner Notice "+orgA.String()[:8]) || link != "https://events.example.test/o/"+orgA.String()+"/ads/meta" {
		t.Errorf("letter: title=%q link=%q", title, link)
	}
	var leak int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM worker_jobs WHERE job_type IN ($1, $2) AND payload::text LIKE '%' || $3 || '%'`,
		adminnotify.JobTypeEmail, adminnotify.JobTypeTelegram, plainToken).Scan(&leak)
	if leak != 0 {
		t.Fatalf("%d notice jobs carry the token", leak)
	}

	// A second 190 while in error: nothing changes, nothing more is queued.
	changed, err = store.MarkMetaTokenInvalid(ctx, orgA, "graph_190")
	if err != nil || changed {
		t.Fatalf("second 190: changed=%v err=%v", changed, err)
	}
	check("second 190", 1)
	// Organization B is untouched.
	var statusB string
	_ = pool.QueryRow(ctx, `SELECT status FROM org_meta_connections WHERE org_id = $1`, orgB).Scan(&statusB)
	if statusB != metaconnect.StatusActive {
		t.Fatalf("org B status = %s", statusB)
	}

	// Reconnect, then a new 190: the owners are told again.
	connect(orgA)
	if changed, err := store.MarkMetaTokenInvalid(ctx, orgA, "graph_190"); err != nil || !changed {
		t.Fatalf("190 after reconnect: changed=%v err=%v", changed, err)
	}
	check("after reconnect", 2)

	// Without Telegram on the worker only e-mails go.
	connect(orgA)
	noTG := NewPGStore(pool).WithMetaBox(box).WithOwnerNotice("https://events.example.test", false, slog.Default())
	if changed, err := noTG.MarkMetaTokenInvalid(ctx, orgA, "graph_190"); err != nil || !changed {
		t.Fatalf("190 without Telegram: changed=%v err=%v", changed, err)
	}
	if n := emails(owner1); n != 3 {
		t.Errorf("e-mails without Telegram = %d, want 3", n)
	}
	if n := telegrams(chats[0]); n != 2 {
		t.Errorf("Telegram jobs without Telegram = %d, want still 2", n)
	}
}

func uuidStrings(ids []uuid.UUID) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = id.String()
	}
	return out
}
