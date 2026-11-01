//go:build integration

// subscribe_integration_test.go — "notify me when sales open" against a live
// database: a coming-soon session (a category whose sale window has not
// started) takes a subscription and queues one confirmation letter; the
// letter's link confirms the address once; while sales stay closed nobody is
// told; when the window opens exactly one sale-open letter goes out, linking
// to the page the buyer subscribed on; an open session refuses new
// subscriptions; the per-client-address limit holds.
//
// Prerequisites: DATABASE_URL against a migrated scratch database.
package salesubscribe_test

import (
	"context"
	"errors"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/email"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/delivery"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/gaquota"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/salesubscribe"
)

type mailCapture struct {
	mu   sync.Mutex
	sent []email.Message
}

func (m *mailCapture) Send(_ context.Context, msg email.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, msg)
	return nil
}

func (m *mailCapture) take() []email.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := m.sent
	m.sent = nil
	return out
}

var tokenRe = regexp.MustCompile(`token=([0-9a-f]{64})`)

func TestSaleSubscription_Integration(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("cannot connect to PostgreSQL (%v); skipping", err)
	}
	defer pool.Close()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", strings.Join(strings.Fields(sql)[:3], " "), err)
		}
	}

	nonce := strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	orgID, venueID, eventID, sessionID, tierID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	eventName := "Soon Show " + nonce
	start := time.Now().UTC().Add(30 * 24 * time.Hour).Truncate(time.Hour)
	opens := time.Now().UTC().Add(7 * 24 * time.Hour)
	exec(`INSERT INTO organizations (id, name, slug, country, default_locale) VALUES ($1, $2, $3, 'ES', 'es')`, orgID, "Soon Org "+nonce, "soon-"+nonce)
	exec(`INSERT INTO venues (id, org_id, name, timezone) VALUES ($1, $2, 'Soon Hall', 'Europe/Madrid')`, venueID, orgID)
	exec(`INSERT INTO events (id, org_id, name, slug, status, visibility) VALUES ($1, $2, $3, $4, 'published', 'public')`,
		eventID, orgID, eventName, "soon-show-"+nonce)
	exec(`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, status, admission_mode, currency, currency_source)
	      VALUES ($1, $2, $3, $4, $4::timestamptz + interval '2 hours', 3, 'scheduled', 'general_admission', 'EUR', 'override')`, sessionID, eventID, venueID, start)
	exec(`INSERT INTO inventory_ledger (session_id, tier_id, capacity_total) VALUES ($1, NULL, 3)`, sessionID)
	exec(`INSERT INTO ticket_tiers (id, session_id, name, pricing_mode, price_amount, currency, capacity, sort_order, sale_window_start)
	      VALUES ($1, $2, 'Standing', 'fixed', 1000, 'EUR', 3, 0, $3)`, tierID, sessionID, opens)
	if err := gaquota.CreateCategory(ctx, gen.New(pool), sessionID, tierID, 3); err != nil {
		t.Fatalf("places: %v", err)
	}
	defer func() {
		for _, s := range []struct {
			sql string
			arg any
		}{
			{`DELETE FROM worker_jobs WHERE job_type IN ('sales_state.subscribe_confirm_email', 'sales_state.sale_open_email')
			  AND payload->>'subscription_id' IN (SELECT id::text FROM session_sale_subscriptions WHERE session_id = $1)`, sessionID},
			{`DELETE FROM session_sale_subscriptions WHERE session_id = $1`, sessionID},
			{`DELETE FROM session_seats WHERE session_id = $1`, sessionID},
			{`DELETE FROM ticket_tiers WHERE session_id = $1`, sessionID},
			{`DELETE FROM inventory_ledger WHERE session_id = $1`, sessionID},
			{`DELETE FROM sessions WHERE id = $1`, sessionID},
			{`DELETE FROM events WHERE id = $1`, eventID},
			{`DELETE FROM venues WHERE id = $1`, venueID},
			{`DELETE FROM organizations WHERE id = $1`, orgID},
		} {
			if _, err := pool.Exec(ctx, s.sql, s.arg); err != nil {
				t.Logf("cleanup %.40s: %v", s.sql, err)
			}
		}
	}()

	svc := salesubscribe.New(pool, nil)
	target := salesubscribe.Target{SessionID: sessionID, EventID: eventID, OrgID: orgID}
	buyer := "Buyer." + nonce + "@Soon.Test"
	page := "https://site.example/event/soon-" + nonce + "/#tickets"
	in := salesubscribe.Input{
		Target: target, Email: buyer, Locale: "ru", PageURL: page, Source: salesubscribe.SourceWidget,
		Consent: true, ConsentVersion: salesubscribe.ConsentVersion, IPHash: "ip-" + nonce,
	}
	jobs := func(jobType string) []uuid.UUID {
		t.Helper()
		rows, err := pool.Query(ctx, `SELECT (payload->>'subscription_id')::uuid FROM worker_jobs
			WHERE job_type = $1 AND payload->>'subscription_id' IN
			      (SELECT id::text FROM session_sale_subscriptions WHERE session_id = $2)`, jobType, sessionID)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []uuid.UUID
		for rows.Next() {
			var id uuid.UUID
			if err := rows.Scan(&id); err != nil {
				t.Fatal(err)
			}
			out = append(out, id)
		}
		return out
	}

	// Refusals write nothing.
	bad := in
	bad.Consent = false
	if err := svc.Subscribe(ctx, bad); !errors.Is(err, salesubscribe.ErrConsentRequired) {
		t.Fatalf("no consent: %v", err)
	}
	bad = in
	bad.ConsentVersion = "sale-notify-2020-01-01"
	if err := svc.Subscribe(ctx, bad); !errors.Is(err, salesubscribe.ErrConsentOutdated) {
		t.Fatalf("old consent: %v", err)
	}
	bad = in
	bad.Email = "not an address"
	if err := svc.Subscribe(ctx, bad); !errors.Is(err, salesubscribe.ErrInvalidEmail) {
		t.Fatalf("bad e-mail: %v", err)
	}

	// Coming soon: saved, consent recorded, one confirmation letter queued;
	// a repeat inside ten minutes queues no second one.
	if err := svc.Subscribe(ctx, in); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := svc.Subscribe(ctx, in); err != nil {
		t.Fatalf("repeat subscribe: %v", err)
	}
	var (
		subID                      uuid.UUID
		status, version, normEmail string
		storedPage, ipHash         string
	)
	if err := pool.QueryRow(ctx, `SELECT id, status, consent_version, email_normalized, page_url, COALESCE(ip_hash, '')
		FROM session_sale_subscriptions WHERE session_id = $1`, sessionID).
		Scan(&subID, &status, &version, &normEmail, &storedPage, &ipHash); err != nil {
		t.Fatal(err)
	}
	if status != "pending" || version != salesubscribe.ConsentVersion || normEmail != strings.ToLower(buyer) || ipHash != in.IPHash {
		t.Fatalf("row = %s %s %s %s", status, version, normEmail, ipHash)
	}
	if strings.Contains(storedPage, "#") {
		t.Fatalf("page kept its fragment: %s", storedPage)
	}
	if got := jobs(salesubscribe.JobTypeConfirmEmail); len(got) != 1 || got[0] != subID {
		t.Fatalf("confirmation jobs = %v", got)
	}

	// The confirmation letter mints the token at send time, in Russian.
	mail := &mailCapture{}
	opts := delivery.SaleLetterOptions{Sender: mail, DB: pool, APIBaseURL: "https://api.example", TicketsBaseURL: "https://tickets.example"}
	payload := []byte(`{"subscription_id":"` + subID.String() + `"}`)
	if err := delivery.NewSaleConfirmHandler(opts)(ctx, payload); err != nil {
		t.Fatalf("confirm letter: %v", err)
	}
	sent := mail.take()
	if len(sent) != 1 || sent[0].To != strings.TrimSpace(buyer) || !strings.Contains(sent[0].TextBody, eventName) {
		t.Fatalf("confirmation letter = %+v", sent)
	}
	if !strings.Contains(sent[0].TextBody, "никогда") {
		t.Fatalf("confirmation letter lacks the Russian unsubscribe note:\n%s", sent[0].TextBody)
	}
	m := tokenRe.FindStringSubmatch(sent[0].TextBody)
	if m == nil || !strings.Contains(sent[0].TextBody, "https://api.example/v1/public/sale-subscriptions/confirm?token=") {
		t.Fatalf("no confirmation link in:\n%s", sent[0].TextBody)
	}
	if _, err := svc.Confirm(ctx, strings.Repeat("0", 64)); !errors.Is(err, salesubscribe.ErrTokenInvalid) {
		t.Fatalf("unknown token: %v", err)
	}
	c, err := svc.Confirm(ctx, m[1])
	if err != nil || c.SubscriptionID != subID || !strings.HasPrefix(c.PageURL, "https://site.example/event/") {
		t.Fatalf("confirm = %+v, %v", c, err)
	}
	if _, err := svc.Confirm(ctx, m[1]); !errors.Is(err, salesubscribe.ErrTokenInvalid) {
		t.Fatalf("second use of a token: %v", err)
	}

	// Still coming soon: nobody is told.
	if n, err := salesubscribe.ClaimOpened(ctx, pool, time.Now().UTC()); err != nil || n != 0 {
		t.Fatalf("claim while soon = %d, %v", n, err)
	}

	// The per-client-address limit: 20 an hour, the first one included.
	for i := 1; i < 20; i++ {
		other := in
		other.Email = "x" + string(rune('a'+i)) + nonce + "@soon.test"
		if err := svc.Subscribe(ctx, other); err != nil {
			t.Fatalf("subscription %d: %v", i, err)
		}
	}
	over := in
	over.Email = "over" + nonce + "@soon.test"
	if err := svc.Subscribe(ctx, over); !errors.Is(err, salesubscribe.ErrRateLimited) {
		t.Fatalf("21st from one address: %v", err)
	}
	// Only the confirmed address is told; the pending ones stay quiet.

	// The sale window opens: one letter, once, linking to the page.
	exec(`UPDATE ticket_tiers SET sale_window_start = now() - interval '1 minute' WHERE id = $1`, tierID)
	if n, err := salesubscribe.ClaimOpened(ctx, pool, time.Now().UTC()); err != nil || n != 1 {
		t.Fatalf("claim on open = %d, %v", n, err)
	}
	if n, err := salesubscribe.ClaimOpened(ctx, pool, time.Now().UTC()); err != nil || n != 0 {
		t.Fatalf("second claim = %d, %v", n, err)
	}
	if got := jobs(salesubscribe.JobTypeOpenEmail); len(got) != 1 || got[0] != subID {
		t.Fatalf("open jobs = %v", got)
	}
	if err := delivery.NewSaleOpenHandler(opts)(ctx, payload); err != nil {
		t.Fatalf("open letter: %v", err)
	}
	sent = mail.take()
	if len(sent) != 1 || !strings.Contains(sent[0].TextBody, "https://site.example/event/soon-"+nonce+"/") {
		t.Fatalf("sale-open letter = %+v", sent)
	}

	// Sales are open: a new subscription is refused.
	late := in
	late.Email = "late" + nonce + "@soon.test"
	late.IPHash = "other-ip-" + nonce
	if err := svc.Subscribe(ctx, late); !errors.Is(err, salesubscribe.ErrSalesOpen) {
		t.Fatalf("subscribe while open: %v", err)
	}

	// A subscription without a page links to the event's page.
	if got := delivery.SaleBuyLink("", "https://tickets.example", "soon-"+nonce, "", "soon-show-"+nonce); !strings.Contains(got, "soon-show-"+nonce) {
		t.Fatalf("fallback link = %s", got)
	}
}
