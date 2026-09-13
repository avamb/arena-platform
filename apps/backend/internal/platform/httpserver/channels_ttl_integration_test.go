//go:build integration

// channels_ttl_integration_test.go — live-PostgreSQL coverage for the
// reservation_ttl_override TTL-wipe fix: UpdateSalesChannel used to assign
// reservation_ttl_override = $8 unconditionally in SQL (every other column
// was COALESCE/CASE-guarded), so ANY partial update that omitted the field —
// notably PUT .../channels/{id}/gateway-credential, which only ever touches
// settings — silently reset a configured hold TTL back to the 20-minute
// default. The fix threads an explicit set_reservation_ttl_override boolean
// down to SQL and gives the PATCH handler a tri-state decode (absent=keep,
// null=clear, value=set) via the package-level optionalInt32 type.
//
// This test drives the REAL hcatalog handlers (HandleCreateChannel,
// HandlePutChannelGatewayCredential, HandleGetChannel, HandleUpdateChannel)
// against a live PostgreSQL, per AGENTS.md: "Integration tests must use real
// handlers + real dispatcher." Route-level auth/validation is already
// covered by the unit tests in channels_test.go and
// hcatalog/channels_ttl_test.go against dbDownPool / gen.New(nil); this file
// exists to prove the actual database round-trip.
//
// Run with:
//
//	DATABASE_URL=postgres://arena:arena@localhost:45432/arena_ci_channelttl?sslmode=disable \
//	    go test -tags integration -p 1 ./apps/backend/internal/platform/httpserver/ \
//	    -run TestChannelTTLIntegration
package httpserver

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
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcatalog"
)

// channelTTLIntegrationPool connects to DATABASE_URL, skipping the test when
// unset (mirrors wp507IntegrationPool in wp_webhook_507_integration_test.go).
func channelTTLIntegrationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping integration test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Skipf("cannot connect to PostgreSQL (%v); skipping", err)
	}
	t.Cleanup(func() { pool.Close() })
	return pool
}

// channelTTLRequest builds an authenticated (superadmin-bypass), admin-
// reasoned request with chi URL params pre-populated, so it can be handed
// directly to an hcatalog.Handler method without mounting the full
// router/JWT stack (same "call the handler directly against a real pool"
// pattern as wp507Request).
func channelTTLRequest(method, orgID, chID string, body []byte) *http.Request {
	var req *http.Request
	if body != nil {
		req = httptest.NewRequest(method, "/", bytes.NewReader(body))
	} else {
		req = httptest.NewRequest(method, "/", nil)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Admin-Reason", "channel TTL integration test")

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("org_id", orgID)
	if chID != "" {
		rctx.URLParams.Add("id", chID)
	}
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = auth.WithActor(ctx, auth.Actor{ID: uuid.NewString(), Type: auth.ActorTypeUser})
	ctx = auth.WithSuperadminOrgAccess(ctx)
	return req.WithContext(ctx)
}

// channelEnvelope decodes the {"channel": {...}} shape every channel
// endpoint returns, reusing hcatalog's exported ChannelResponse rather than
// re-declaring its fields.
type channelEnvelope struct {
	Channel hcatalog.ChannelResponse `json:"channel"`
}

func decodeChannelEnvelope(t *testing.T, w *httptest.ResponseRecorder) hcatalog.ChannelResponse {
	t.Helper()
	var env channelEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode channel envelope: %v (body: %s)", err, w.Body.String())
	}
	return env.Channel
}

// TestChannelTTLIntegration_GatewayCredentialAndPATCHPreserveTTL is the
// scenario from the defect report: create a channel with
// reservation_ttl_override=120, PUT the gateway-credential (which used to
// wipe it), PATCH without the field (still preserved), PATCH null (clears
// to org default), PATCH a new value (sets it).
func TestChannelTTLIntegration_GatewayCredentialAndPATCHPreserveTTL(t *testing.T) {
	pool := channelTTLIntegrationPool(t)
	ctx := context.Background()
	q := gen.New(pool)

	org, err := q.InsertOrganization(ctx, "Channel TTL Test Org", "channel-ttl-test-org-"+uuid.NewString(), "DE", "en", 1200)
	if err != nil {
		t.Fatalf("InsertOrganization: %v", err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `DELETE FROM sales_channels WHERE org_id = $1`, org.ID); err != nil {
			t.Logf("cleanup: delete sales_channels: %v", err)
		}
		if _, err := pool.Exec(context.Background(), `DELETE FROM organizations WHERE id = $1`, org.ID); err != nil {
			t.Logf("cleanup: delete organizations: %v", err)
		}
	})

	h := hcatalog.New(nil, nil, nil, gen.New(pool), nil, nil, nil, pool,
		audit.NewPGWriter(pool), slog.Default(), nil).
		WithMembershipQueries(gen.New(pool))

	orgID := org.ID.String()

	// 1. Create the channel with reservation_ttl_override = 120.
	createBody, _ := json.Marshal(map[string]any{
		"name":                     "TTL Fixture Channel",
		"payment_mode":             "merchant_of_record",
		"provider":                 "stripe",
		"reservation_ttl_override": 120,
	})
	w := httptest.NewRecorder()
	h.HandleCreateChannel(w, channelTTLRequest(http.MethodPost, orgID, "", createBody))
	if w.Code != http.StatusCreated {
		t.Fatalf("create channel: got %d, want 201 (body: %s)", w.Code, w.Body.String())
	}
	ch := decodeChannelEnvelope(t, w)
	if ch.ReservationTTLOverride == nil || *ch.ReservationTTLOverride != 120 {
		t.Fatalf("create channel: ReservationTTLOverride = %v, want 120", ch.ReservationTTLOverride)
	}
	chID := ch.ID

	// 2. PUT the gateway credential — the TTL-wipe defect's trigger. It must
	// not touch reservation_ttl_override at all.
	w = httptest.NewRecorder()
	h.HandlePutChannelGatewayCredential(w, channelTTLRequest(http.MethodPut, orgID, chID, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("PUT gateway-credential: got %d, want 200 (body: %s)", w.Code, w.Body.String())
	}

	// 3. GET the channel: TTL must still be 120 after the gateway-credential PUT.
	w = httptest.NewRecorder()
	h.HandleGetChannel(w, channelTTLRequest(http.MethodGet, orgID, chID, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("get channel after PUT gateway-credential: got %d, want 200 (body: %s)", w.Code, w.Body.String())
	}
	ch = decodeChannelEnvelope(t, w)
	if ch.ReservationTTLOverride == nil || *ch.ReservationTTLOverride != 120 {
		t.Fatalf("after PUT gateway-credential: ReservationTTLOverride = %v, want 120 (this is the TTL-wipe defect if nil)", ch.ReservationTTLOverride)
	}

	// 4. PATCH without reservation_ttl_override (only name) — must still
	// preserve 120 (the same class of bug, reached via the ordinary PATCH
	// endpoint instead of the gateway-credential one).
	patchNameBody, _ := json.Marshal(map[string]any{"name": "TTL Fixture Channel Renamed"})
	w = httptest.NewRecorder()
	h.HandleUpdateChannel(w, channelTTLRequest(http.MethodPatch, orgID, chID, patchNameBody))
	if w.Code != http.StatusOK {
		t.Fatalf("PATCH name only: got %d, want 200 (body: %s)", w.Code, w.Body.String())
	}
	ch = decodeChannelEnvelope(t, w)
	if ch.Name != "TTL Fixture Channel Renamed" {
		t.Fatalf("PATCH name only: Name = %q, want %q", ch.Name, "TTL Fixture Channel Renamed")
	}
	if ch.ReservationTTLOverride == nil || *ch.ReservationTTLOverride != 120 {
		t.Fatalf("PATCH name only: ReservationTTLOverride = %v, want 120 (absent key must keep the stored value)", ch.ReservationTTLOverride)
	}

	// 5. PATCH reservation_ttl_override: null — must clear it to NULL
	// (falls back to the organization-level default).
	patchNullBody := []byte(`{"reservation_ttl_override":null}`)
	w = httptest.NewRecorder()
	h.HandleUpdateChannel(w, channelTTLRequest(http.MethodPatch, orgID, chID, patchNullBody))
	if w.Code != http.StatusOK {
		t.Fatalf("PATCH reservation_ttl_override=null: got %d, want 200 (body: %s)", w.Code, w.Body.String())
	}
	ch = decodeChannelEnvelope(t, w)
	if ch.ReservationTTLOverride != nil {
		t.Fatalf("PATCH reservation_ttl_override=null: ReservationTTLOverride = %v, want nil", *ch.ReservationTTLOverride)
	}

	// 6. PATCH reservation_ttl_override: 300 — must set it.
	patchValueBody := []byte(`{"reservation_ttl_override":300}`)
	w = httptest.NewRecorder()
	h.HandleUpdateChannel(w, channelTTLRequest(http.MethodPatch, orgID, chID, patchValueBody))
	if w.Code != http.StatusOK {
		t.Fatalf("PATCH reservation_ttl_override=300: got %d, want 200 (body: %s)", w.Code, w.Body.String())
	}
	ch = decodeChannelEnvelope(t, w)
	if ch.ReservationTTLOverride == nil || *ch.ReservationTTLOverride != 300 {
		t.Fatalf("PATCH reservation_ttl_override=300: ReservationTTLOverride = %v, want 300", ch.ReservationTTLOverride)
	}
}
