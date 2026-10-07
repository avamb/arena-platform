//go:build integration

// channels_mor_integration_test.go — live-PostgreSQL coverage of the rule that
// only a platform superadmin may put a sales channel into
// payment_mode='merchant_of_record' (owner decision 2026-10-07).
//
// merchant_of_record makes the platform the seller of record and moves the
// refund/chargeback risk onto it, so an organization owner, an organizer or an
// organization API key must not be able to switch it on by themselves. The
// guard sits in hcatalog's create and PATCH handlers; these tests drive the
// REAL handlers against a real pool, with a service actor standing in for an
// ordinary organization caller (it passes the membership check without a
// membership row) and the superadmin marker for the operator.
//
// Run with:
//
//	DATABASE_URL=postgres://arena:arena@localhost:45432/arena_ci_mor?sslmode=disable \
//	    go test -tags integration -p 1 ./apps/backend/internal/platform/httpserver/ \
//	    -run TestChannelMoRIntegration
package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcatalog"
)

// channelMoRRequest builds a handler request for the organization orgID. With
// superadmin=false the caller is an organization API key (a service actor of
// that organization): it is allowed into the org but carries no superadmin
// marker. With superadmin=true the marker and a reason header are added.
func channelMoRRequest(method, orgID, chID string, body []byte, superadmin bool) *http.Request {
	req := httptest.NewRequest(method, "/", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("org_id", orgID)
	if chID != "" {
		rctx.URLParams.Add("id", chID)
	}
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	if superadmin {
		req.Header.Set("X-Admin-Reason", "channel merchant_of_record integration test")
		ctx = auth.WithActor(ctx, auth.Actor{ID: uuid.NewString(), Type: auth.ActorTypeUser})
		ctx = auth.WithSuperadminOrgAccess(ctx)
	} else {
		ctx = auth.WithActor(ctx, auth.Actor{ID: uuid.NewString(), Type: auth.ActorTypeService, OrgID: orgID})
	}
	return req.WithContext(ctx)
}

func TestChannelMoRIntegration_OnlySuperadminMayUseMerchantOfRecord(t *testing.T) {
	pool := channelTTLIntegrationPool(t)
	ctx := context.Background()
	q := gen.New(pool)

	org, err := q.InsertOrganization(ctx, "Channel MoR Test Org", "channel-mor-test-org-"+uuid.NewString(), "DE", "en", 1200)
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

	storedMode := func(chID string) string {
		t.Helper()
		var mode string
		if err := pool.QueryRow(ctx, `SELECT payment_mode FROM sales_channels WHERE id = $1`, chID).Scan(&mode); err != nil {
			t.Fatalf("read payment_mode: %v", err)
		}
		return mode
	}
	channelCount := func() int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM sales_channels WHERE org_id = $1`, org.ID).Scan(&n); err != nil {
			t.Fatalf("count channels: %v", err)
		}
		return n
	}
	decodeErrCode := func(w *httptest.ResponseRecorder) string {
		t.Helper()
		var env struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
			t.Fatalf("decode error envelope: %v (body: %s)", err, w.Body.String())
		}
		return env.Error.Code
	}
	const wantCode = "channel.merchant_of_record_superadmin_only"

	// 1. An ordinary organization caller cannot CREATE a merchant_of_record
	// channel, and nothing is written.
	body, _ := json.Marshal(map[string]any{"name": "MoR by owner", "payment_mode": "merchant_of_record", "provider": "stripe"})
	w := httptest.NewRecorder()
	h.HandleCreateChannel(w, channelMoRRequest(http.MethodPost, orgID, "", body, false))
	if w.Code != http.StatusForbidden {
		t.Fatalf("owner create MoR: got %d, want 403 (body: %s)", w.Code, w.Body.String())
	}
	if got := decodeErrCode(w); got != wantCode {
		t.Fatalf("owner create MoR: code %q, want %q", got, wantCode)
	}
	if n := channelCount(); n != 0 {
		t.Fatalf("owner create MoR: %d channels written, want 0", n)
	}

	// 2. The same caller can create a direct_merchant channel.
	body, _ = json.Marshal(map[string]any{
		"name": "Direct by owner", "payment_mode": "direct_merchant", "provider": "stripe",
		"provider_account_id": "acct_mor_test",
	})
	w = httptest.NewRecorder()
	h.HandleCreateChannel(w, channelMoRRequest(http.MethodPost, orgID, "", body, false))
	if w.Code != http.StatusCreated {
		t.Fatalf("owner create direct: got %d, want 201 (body: %s)", w.Code, w.Body.String())
	}
	directID := decodeChannelEnvelope(t, w).ID

	// 3. The caller cannot PATCH that channel INTO merchant_of_record.
	body, _ = json.Marshal(map[string]any{"payment_mode": "merchant_of_record"})
	w = httptest.NewRecorder()
	h.HandleUpdateChannel(w, channelMoRRequest(http.MethodPatch, orgID, directID, body, false))
	if w.Code != http.StatusForbidden {
		t.Fatalf("owner patch to MoR: got %d, want 403 (body: %s)", w.Code, w.Body.String())
	}
	if got := decodeErrCode(w); got != wantCode {
		t.Fatalf("owner patch to MoR: code %q, want %q", got, wantCode)
	}
	if got := storedMode(directID); got != "direct_merchant" {
		t.Fatalf("owner patch to MoR: stored mode %q, want direct_merchant", got)
	}

	// 4. A superadmin can create a merchant_of_record channel...
	body, _ = json.Marshal(map[string]any{"name": "MoR by operator", "payment_mode": "merchant_of_record", "provider": "stripe"})
	w = httptest.NewRecorder()
	h.HandleCreateChannel(w, channelMoRRequest(http.MethodPost, orgID, "", body, true))
	if w.Code != http.StatusCreated {
		t.Fatalf("operator create MoR: got %d, want 201 (body: %s)", w.Code, w.Body.String())
	}
	morID := decodeChannelEnvelope(t, w).ID

	// 5. ...and switch an existing channel to it.
	body, _ = json.Marshal(map[string]any{"payment_mode": "merchant_of_record"})
	w = httptest.NewRecorder()
	h.HandleUpdateChannel(w, channelMoRRequest(http.MethodPatch, orgID, directID, body, true))
	if w.Code != http.StatusOK {
		t.Fatalf("operator patch to MoR: got %d, want 200 (body: %s)", w.Code, w.Body.String())
	}
	if got := storedMode(directID); got != "merchant_of_record" {
		t.Fatalf("operator patch to MoR: stored mode %q, want merchant_of_record", got)
	}

	// 6. The owner of a channel that is ALREADY merchant_of_record may repeat
	// that mode in a PATCH (a client that resends the whole form) — nothing
	// changes, so nothing is refused.
	body, _ = json.Marshal(map[string]any{"name": "MoR renamed by owner", "payment_mode": "merchant_of_record"})
	w = httptest.NewRecorder()
	h.HandleUpdateChannel(w, channelMoRRequest(http.MethodPatch, orgID, morID, body, false))
	if w.Code != http.StatusOK {
		t.Fatalf("owner repeats MoR on a MoR channel: got %d, want 200 (body: %s)", w.Code, w.Body.String())
	}

	// 7. Leaving merchant_of_record lowers the platform's exposure, so the
	// owner may do it.
	body, _ = json.Marshal(map[string]any{
		"payment_mode": "direct_merchant", "provider": "stripe", "provider_account_id": "acct_mor_test",
	})
	w = httptest.NewRecorder()
	h.HandleUpdateChannel(w, channelMoRRequest(http.MethodPatch, orgID, morID, body, false))
	if w.Code != http.StatusOK {
		t.Fatalf("owner leaves MoR: got %d, want 200 (body: %s)", w.Code, w.Body.String())
	}
	if got := storedMode(morID); got != "direct_merchant" {
		t.Fatalf("owner leaves MoR: stored mode %q, want direct_merchant", got)
	}
}
