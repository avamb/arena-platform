//go:build integration

// sale_subscription_public_integration_test.go — the widget's "notify me when
// sales open" routes through the real router: an open session refuses, a
// coming-soon one answers 202 and keeps the consent but never a page from an
// origin outside the allow-list, a missing consent is 400, a session that is
// not on the feed is 404, and the confirmation link confirms once.
//
// Prerequisites: DATABASE_URL against a migrated database.
package httpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/salesubscribe"
)

func TestSaleSubscription_PublicRoutes(t *testing.T) {
	srv, _ := productionIntegrationServer(t)
	pool := integrationPool(t)
	ctx := context.Background()

	f := newW1A6cFixture(t, ctx, pool)
	defer func() {
		for _, s := range []struct {
			sql string
			arg any
		}{
			{`DELETE FROM worker_jobs WHERE job_type = 'sales_state.subscribe_confirm_email'
			  AND payload->>'subscription_id' IN (SELECT id::text FROM session_sale_subscriptions WHERE session_id = $1)`, f.sessionID},
			{`DELETE FROM session_sale_subscriptions WHERE session_id = $1`, f.sessionID},
			{`DELETE FROM session_sales_states WHERE session_id = $1`, f.sessionID},
		} {
			if _, err := pool.Exec(ctx, s.sql, s.arg); err != nil {
				t.Logf("cleanup (%.50s): %v", s.sql, err)
			}
		}
		f.cleanup()
	}()

	path := "/v1/public/feeds/" + f.feedToken + "/sessions/" + f.sessionID.String() + "/sale-subscriptions"
	body := func(email string, consent bool) string {
		c := "false"
		if consent {
			c = "true"
		}
		return `{"email":"` + email + `","locale":"es","consent":` + c +
			`,"consent_version":"` + salesubscribe.ConsentVersion + `","page_url":"https://evil.example/phish"}`
	}

	st, out := sstDo(t, srv, http.MethodPost, path, "", "", body("open@example.test", true))
	if st != http.StatusConflict || errCode(out) != "subscription.sales_open" {
		t.Fatalf("open session = %d %v", st, out)
	}

	if _, err := pool.Exec(ctx, `INSERT INTO session_sales_states (session_id, state) VALUES ($1, 'soon')`, f.sessionID); err != nil {
		t.Fatal(err)
	}
	st, out = sstDo(t, srv, http.MethodPost, path, "", "", body("noconsent@example.test", false))
	if st != http.StatusBadRequest || errCode(out) != "subscription.consent_required" {
		t.Fatalf("no consent = %d %v", st, out)
	}
	other := "/v1/public/feeds/" + f.feedToken + "/sessions/" + uuid.NewString() + "/sale-subscriptions"
	st, out = sstDo(t, srv, http.MethodPost, other, "", "", body("ghost@example.test", true))
	if st != http.StatusNotFound || errCode(out) != "feed.session_not_on_feed" {
		t.Fatalf("session not on the feed = %d %v", st, out)
	}
	st, out = sstDo(t, srv, http.MethodPost, path, "", "", body("Widget@Example.test", true))
	if st != http.StatusAccepted || out["status"] != "check_email" {
		t.Fatalf("subscribe = %d %v", st, out)
	}
	var (
		id                   uuid.UUID
		page, source, status string
	)
	if err := pool.QueryRow(ctx, `SELECT id, page_url, source, status FROM session_sale_subscriptions
		WHERE session_id = $1 AND email_normalized = 'widget@example.test'`, f.sessionID).Scan(&id, &page, &source, &status); err != nil {
		t.Fatalf("row: %v", err)
	}
	if page != "" || source != "widget" || status != "pending" {
		t.Fatalf("row = page %q source %s status %s", page, source, status)
	}

	// The letter's link: the worker mints the token; do it by hand here.
	token, err := salesubscribe.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE session_sale_subscriptions SET confirm_token_hash = $2, confirm_sent_at = now() WHERE id = $1`,
		id, salesubscribe.HashToken(token)); err != nil {
		t.Fatal(err)
	}
	confirm := func(tok string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/v1/public/sale-subscriptions/confirm?token="+tok, nil)
		rec := httptest.NewRecorder()
		srv.router.ServeHTTP(rec, req)
		return rec
	}
	rec := confirm(token)
	if rec.Code != http.StatusSeeOther && rec.Code != http.StatusOK {
		t.Fatalf("confirm = %d %s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); rec.Code == http.StatusSeeOther && !strings.Contains(loc, "arena_subscription=confirmed") {
		t.Fatalf("confirm redirect = %s", loc)
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM session_sale_subscriptions WHERE id = $1`, id).Scan(&status); err != nil || status != "confirmed" {
		t.Fatalf("status after confirm = %s, %v", status, err)
	}
	rec = confirm(token)
	if rec.Code == http.StatusSeeOther {
		if loc := rec.Header().Get("Location"); !strings.Contains(loc, "arena_subscription=invalid") {
			t.Fatalf("second confirm redirect = %s", loc)
		}
	} else if rec.Code != http.StatusNotFound {
		t.Fatalf("second confirm = %d", rec.Code)
	}
}
