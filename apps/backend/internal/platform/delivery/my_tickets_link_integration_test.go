//go:build integration

package delivery

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/email"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/mytickets"
)

type myTicketsCaptureSender struct {
	mu   sync.Mutex
	sent []email.Message
}

func (c *myTicketsCaptureSender) Send(_ context.Context, m email.Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent = append(c.sent, m)
	return nil
}

// TestMyTicketsLinkHandler_MintsTheTokenAtSendTimeAndMailsIt runs the worker
// handler against a live database: the stored row gets only the token's hash
// and the letter carries the same token, which then exchanges for a session.
func TestMyTicketsLinkHandler_MintsTheTokenAtSendTimeAndMailsIt(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set - skipping live DB integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("cannot connect: %v", err)
	}
	defer pool.Close()

	run := uuid.NewString()
	orgID, reqID := uuid.New(), uuid.New()
	addr := "letter-" + run + "@example.com"
	if _, err := pool.Exec(ctx, `INSERT INTO organizations (id, name, slug, country, default_locale) VALUES ($1,$2,$3,'DE','ru')`,
		orgID, "Letter Org "+run, "letter-org-"+run); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM my_tickets_sessions WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(ctx, `DELETE FROM my_tickets_link_requests WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(ctx, `DELETE FROM organizations WHERE id = $1`, orgID)
	})
	if _, err := pool.Exec(ctx, `INSERT INTO my_tickets_link_requests (id, org_id, email_normalized, page_url)
	      VALUES ($1,$2,$3,'https://vinoandco.events/my-tickets/')`, reqID, orgID, addr); err != nil {
		t.Fatal(err)
	}

	sender := &myTicketsCaptureSender{}
	h := NewMyTicketsLinkHandler(MyTicketsLinkOptions{Sender: sender, DB: pool})
	payload, _ := json.Marshal(mytickets.JobPayload{RequestID: reqID.String()})
	if err := h(ctx, payload); err != nil {
		t.Fatal(err)
	}
	if len(sender.sent) != 1 {
		t.Fatalf("letters sent = %d, want 1", len(sender.sent))
	}
	msg := sender.sent[0]
	if msg.To != addr || !strings.Contains(msg.TextBody, "https://vinoandco.events/my-tickets/?t=") {
		t.Fatalf("letter = %+v", msg)
	}
	// The organization's language (ru) was used.
	if !strings.Contains(msg.Subject, "Ваши билеты") {
		t.Errorf("subject = %q, want the Russian one", msg.Subject)
	}
	tok := msg.TextBody[strings.Index(msg.TextBody, "?t=")+3:]
	tok = strings.Fields(tok)[0]
	var stored string
	if err := pool.QueryRow(ctx, `SELECT token_hash FROM my_tickets_link_requests WHERE id = $1`, reqID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != mytickets.HashToken(tok) || stored == tok {
		t.Fatalf("the row must hold the hash of the mailed token, got %q", stored)
	}
	if _, _, err := mytickets.New(pool, nil).Exchange(ctx, tok); err != nil {
		t.Fatalf("the mailed token must exchange: %v", err)
	}
	// A retry of a spent request sends nothing more.
	if err := h(ctx, payload); err != nil || len(sender.sent) != 1 {
		t.Fatalf("retry after use: err=%v sent=%d", err, len(sender.sent))
	}
}
