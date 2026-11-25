//go:build integration

package paymentdomains_test

// Prerequisites: DATABASE_URL against a migrated scratch database.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/paymentdomains"
)

type stripeStub struct {
	mu      sync.Mutex
	creates int
	failFor map[string]bool
	known   map[string]string // domain -> pmd id
}

func (s *stripeStub) handler(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/payment_method_domains"):
		d := r.URL.Query().Get("domain_name")
		if s.failFor[d] {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","message":"Stub refused ` + d + `"}}`))
			return
		}
		if id, ok := s.known[d]; ok {
			_, _ = w.Write([]byte(`{"data":[` + obj(id, d) + `]}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[]}`))
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/payment_method_domains"):
		_ = r.ParseForm()
		d := r.PostForm.Get("domain_name")
		s.creates++
		id := "pmd_" + strings.ReplaceAll(d, ".", "_")
		s.known[d] = id
		_, _ = w.Write([]byte(obj(id, d)))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func obj(id, d string) string {
	return `{"id":"` + id + `","domain_name":"` + d + `","enabled":true,` +
		`"apple_pay":{"status":"active"},"google_pay":{"status":"active"},"link":{"status":"inactive"}}`
}

func TestSyncConfig_RegistersIsIdempotentDeletesStaleAndStoresErrors(t *testing.T) {
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

	stub := &stripeStub{failFor: map[string]bool{}, known: map[string]string{}}
	srv := httptest.NewServer(http.HandlerFunc(stub.handler))
	defer srv.Close()

	suffix := uuid.NewString()[:8]
	org, channel := uuid.New(), uuid.New()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", strings.Join(strings.Fields(sql)[:3], " "), err)
		}
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM payment_method_domains WHERE org_id = $1`, org)
		_, _ = pool.Exec(c, `DELETE FROM sales_channel_site_domains WHERE org_id = $1`, org)
		_, _ = pool.Exec(c, `DELETE FROM payment_provider_configs WHERE org_id = $1`, org)
		_, _ = pool.Exec(c, `DELETE FROM sales_channels WHERE org_id = $1`, org)
		_, _ = pool.Exec(c, `DELETE FROM organizations WHERE id = $1`, org)
	})
	exec(`INSERT INTO organizations (id, name, slug, country, default_locale) VALUES ($1, $2, $3, 'IL', 'en')`, org, "PMD "+suffix, "pmd-"+suffix)
	exec(`INSERT INTO sales_channels (id, org_id, name, settings) VALUES ($1, $2, 'site', '{}'::jsonb)`, channel, org)

	q := gen.New(pool)
	cfg, err := q.InsertPaymentProviderConfig(ctx, org, "stripe", "test", nil, nil,
		[]byte(`{"api_key":"sk_test_stub"}`), "configured", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := q.ReplaceSalesChannelSiteDomains(ctx, channel, org, []string{"shop.example.test", "old.example.test"}); err != nil {
		t.Fatal(err)
	}

	s := &paymentdomains.Syncer{
		Queries:         q,
		Options:         payments.Options{StripeAPIBaseURL: srv.URL + "/v1"},
		PlatformDomains: []string{"tickets.example.test"},
	}

	rows, err := s.SyncConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("rows = %d; want 3 (platform + two sites): %+v", len(rows), rows)
	}
	for _, r := range rows {
		if r.LastError != nil || r.ProviderDomainID == nil || r.ApplePayStatus == nil || *r.ApplePayStatus != "active" {
			t.Fatalf("row not stored from the answer: %+v", r)
		}
		if r.Domain == "tickets.example.test" && r.Source != "platform" {
			t.Fatalf("source of the platform domain = %q", r.Source)
		}
	}
	if stub.creates != 3 {
		t.Fatalf("creates = %d; want 3", stub.creates)
	}

	// Second run: everything is found, nothing is created again.
	if _, err := s.SyncConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if stub.creates != 3 {
		t.Fatalf("second run created again: creates = %d", stub.creates)
	}

	// A site host leaves the channel: its row goes, the platform row stays.
	if err := q.ReplaceSalesChannelSiteDomains(ctx, channel, org, []string{"shop.example.test"}); err != nil {
		t.Fatal(err)
	}
	rows, err = s.SyncConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows after removal = %d; want 2: %+v", len(rows), rows)
	}
	for _, r := range rows {
		if r.Domain == "old.example.test" {
			t.Fatal("stale site row not deleted")
		}
	}

	// Stripe refuses one host: the error is stored, earlier statuses stay.
	stub.failFor["shop.example.test"] = true
	rows, err = s.SyncConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("a Stripe refusal must not fail the sync: %v", err)
	}
	var shop *gen.PaymentMethodDomainRow
	for i := range rows {
		if rows[i].Domain == "shop.example.test" {
			shop = &rows[i]
		}
	}
	if shop == nil || shop.LastError == nil || !strings.Contains(*shop.LastError, "Stub refused") {
		t.Fatalf("last_error not stored: %+v", shop)
	}
	if shop.ApplePayStatus == nil || *shop.ApplePayStatus != "active" {
		t.Fatalf("earlier status lost: %+v", shop)
	}
	if strings.Contains(*shop.LastError, "sk_test_stub") {
		t.Fatal("last_error carries the key")
	}

	// An inactive configuration is skipped.
	cfg.IsActive = false
	before := stub.creates
	if rows, err := s.SyncConfig(ctx, cfg); err != nil || rows != nil || stub.creates != before {
		t.Fatalf("inactive config not skipped: rows=%v err=%v", rows, err)
	}
}
