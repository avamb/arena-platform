//go:build integration

// channel_provider_integration_test.go — PAY-02 against a live database
// migrated past 0132: a provider registered in the module registry is
// accepted by the REAL create and PATCH handlers and STORED by the database
// with no migration of its own; unknown, declared-only and empty providers
// keep their pre-PAY-02 codes and statuses and write nothing.
//
// Run with:
//
//	DATABASE_URL=postgres://arena:arena@localhost:45432/arena_ci_pay02?sslmode=disable \
//	    go test -tags integration -run TestChannelProviderRegistryIntegration \
//	    ./apps/backend/internal/platform/httpserver/hcatalog/
package hcatalog

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
)

func pay02Request(method, orgID, chID string, body []byte) *http.Request {
	req := httptest.NewRequest(method, "/", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("org_id", orgID)
	if chID != "" {
		rctx.URLParams.Add("id", chID)
	}
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = auth.WithActor(ctx, auth.Actor{ID: uuid.NewString(), Type: auth.ActorTypeService, OrgID: orgID})
	return req.WithContext(ctx)
}

func TestChannelProviderRegistryIntegration_NewModuleNeedsNoMigration(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	q := gen.New(pool)

	// A provider the dropped CHECK never listed, unique per run.
	fake := "pay02fake_" + uuid.NewString()[:8]
	saved := channelProviderRegistry
	channelProviderRegistry = pay02Registry(t, fake)
	t.Cleanup(func() { channelProviderRegistry = saved })

	org, err := q.InsertOrganization(ctx, "PAY-02 provider org", "pay02-provider-"+uuid.NewString(), "DE", "en", 1200)
	if err != nil {
		t.Fatalf("InsertOrganization: %v", err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		if _, err := pool.Exec(bg, `DELETE FROM sales_channels WHERE org_id = $1`, org.ID); err != nil {
			t.Logf("cleanup sales_channels: %v", err)
		}
		if _, err := pool.Exec(bg, `DELETE FROM organizations WHERE id = $1`, org.ID); err != nil {
			t.Logf("cleanup organizations: %v", err)
		}
	})
	orgID := org.ID.String()
	h := New(nil, nil, nil, gen.New(pool), nil, nil, nil, pool, audit.NewPGWriter(pool), slog.Default(), nil).
		WithMembershipQueries(gen.New(pool))

	count := func() int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM sales_channels WHERE org_id = $1`, org.ID).Scan(&n); err != nil {
			t.Fatalf("count: %v", err)
		}
		return n
	}
	stored := func(id string) string {
		t.Helper()
		var p string
		if err := pool.QueryRow(ctx, `SELECT provider FROM sales_channels WHERE id = $1`, id).Scan(&p); err != nil {
			t.Fatalf("read provider: %v", err)
		}
		return p
	}
	code := func(w *httptest.ResponseRecorder) string {
		t.Helper()
		var env struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &env)
		return env.Error.Code
	}
	create := func(body map[string]any) *httptest.ResponseRecorder {
		b, _ := json.Marshal(body)
		w := httptest.NewRecorder()
		h.HandleCreateChannel(w, pay02Request(http.MethodPost, orgID, "", b))
		return w
	}
	patch := func(id string, body map[string]any) *httptest.ResponseRecorder {
		b, _ := json.Marshal(body)
		w := httptest.NewRecorder()
		h.HandleUpdateChannel(w, pay02Request(http.MethodPatch, orgID, id, b))
		return w
	}

	// 1. The freshly registered module is accepted and the row is stored.
	w := create(map[string]any{"name": "Fake provider", "provider": fake, "provider_account_id": "acct_pay02"})
	if w.Code != http.StatusCreated {
		t.Fatalf("create with registered fake provider: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		Channel struct {
			ID string `json:"id"`
		} `json:"channel"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	ch := created.Channel
	if got := stored(ch.ID); got != fake {
		t.Fatalf("stored provider = %q; want %q", got, fake)
	}

	// 2. An empty provider still defaults to stripe.
	w = create(map[string]any{"name": "Default provider", "provider_account_id": "acct_pay02b"})
	if w.Code != http.StatusCreated {
		t.Fatalf("create with empty provider: %d %s", w.Code, w.Body.String())
	}
	var def struct {
		Channel struct {
			ID string `json:"id"`
		} `json:"channel"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &def)
	if got := stored(def.Channel.ID); got != "stripe" {
		t.Fatalf("default provider = %q; want stripe", got)
	}

	// 3. Unknown and declared-only providers: 400 channel.invalid_config,
	// nothing written.
	before := count()
	for _, p := range []string{"paypal", "pay02declared", "Stripe"} {
		w = create(map[string]any{"name": "Refused " + p, "provider": p, "provider_account_id": "acct_x"})
		if w.Code != http.StatusBadRequest || code(w) != "channel.invalid_config" {
			t.Fatalf("create %q: %d %s; want 400 channel.invalid_config", p, w.Code, w.Body.String())
		}
	}
	if n := count(); n != before {
		t.Fatalf("refused creates wrote %d rows", n-before)
	}

	// 4. PATCH moves the channel between registered providers and refuses
	// an unknown one without touching the row.
	if w = patch(ch.ID, map[string]any{"provider": "stripe"}); w.Code != http.StatusOK {
		t.Fatalf("patch to stripe: %d %s", w.Code, w.Body.String())
	}
	if got := stored(ch.ID); got != "stripe" {
		t.Fatalf("after patch provider = %q; want stripe", got)
	}
	if w = patch(ch.ID, map[string]any{"provider": "paypal"}); w.Code != http.StatusBadRequest || code(w) != "channel.invalid_config" {
		t.Fatalf("patch to unknown: %d %s; want 400 channel.invalid_config", w.Code, w.Body.String())
	}
	if w = patch(ch.ID, map[string]any{"provider": fake}); w.Code != http.StatusOK {
		t.Fatalf("patch back to the fake module: %d %s", w.Code, w.Body.String())
	}
	if got := stored(ch.ID); got != fake {
		t.Fatalf("after patch provider = %q; want %q", got, fake)
	}

	// 5. The database itself keeps the value well-formed (0132's format
	// check), whatever a handler would let through.
	if _, err := pool.Exec(ctx, `UPDATE sales_channels SET provider = '' WHERE id = $1`, ch.ID); err == nil {
		t.Fatal("database accepted an empty provider")
	}
	if _, err := pool.Exec(ctx, `UPDATE sales_channels SET provider = 'Bad Name' WHERE id = $1`, ch.ID); err == nil {
		t.Fatal("database accepted a malformed provider")
	}
}
