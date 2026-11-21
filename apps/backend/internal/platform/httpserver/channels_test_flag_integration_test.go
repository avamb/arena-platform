//go:build integration

package httpserver

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcatalog"
)

// TestChannelFlagsIntegration_CreateAndPatch drives the real handlers against
// a live database: is_test, collect_name and collect_phone are accepted by
// POST and PATCH, an absent key keeps the stored value, and the response
// carries them.
func TestChannelFlagsIntegration_CreateAndPatch(t *testing.T) {
	pool := channelTTLIntegrationPool(t)
	ctx := context.Background()
	q := gen.New(pool)

	org, err := q.InsertOrganization(ctx, "Channel Flags Org", "channel-flags-org-"+uuid.NewString(), "DE", "en", 1200)
	if err != nil {
		t.Fatalf("InsertOrganization: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM sales_channels WHERE org_id = $1`, org.ID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM organizations WHERE id = $1`, org.ID)
	})
	h := hcatalog.New(nil, nil, nil, gen.New(pool), nil, nil, nil, pool,
		audit.NewPGWriter(pool), slog.Default(), nil).
		WithMembershipQueries(gen.New(pool))
	orgID := org.ID.String()

	// Defaults: all three flags off.
	w := httptest.NewRecorder()
	h.HandleCreateChannel(w, channelTTLRequest(http.MethodPost, orgID, "",
		[]byte(`{"name":"Plain","payment_mode":"merchant_of_record","provider":"stripe"}`)))
	if w.Code != http.StatusCreated {
		t.Fatalf("create plain: %d %s", w.Code, w.Body.String())
	}
	if c := decodeChannelEnvelope(t, w); c.IsTest || c.CollectName || c.CollectPhone {
		t.Fatalf("defaults must be false, got %+v", c)
	}

	// Create a test channel that collects the name.
	w = httptest.NewRecorder()
	h.HandleCreateChannel(w, channelTTLRequest(http.MethodPost, orgID, "",
		[]byte(`{"name":"Test","payment_mode":"merchant_of_record","provider":"stripe","is_test":true,"collect_name":true}`)))
	if w.Code != http.StatusCreated {
		t.Fatalf("create test: %d %s", w.Code, w.Body.String())
	}
	ch := decodeChannelEnvelope(t, w)
	if !ch.IsTest || !ch.CollectName || ch.CollectPhone {
		t.Fatalf("create flags wrong: %+v", ch)
	}

	// PATCH only collect_phone: the others stay.
	w = httptest.NewRecorder()
	h.HandleUpdateChannel(w, channelTTLRequest(http.MethodPatch, orgID, ch.ID, []byte(`{"collect_phone":true}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("patch phone: %d %s", w.Code, w.Body.String())
	}
	ch = decodeChannelEnvelope(t, w)
	if !ch.IsTest || !ch.CollectName || !ch.CollectPhone {
		t.Fatalf("after patch phone: %+v", ch)
	}

	// PATCH false values switch them off; an explicit false is not "absent".
	w = httptest.NewRecorder()
	h.HandleUpdateChannel(w, channelTTLRequest(http.MethodPatch, orgID, ch.ID,
		[]byte(`{"is_test":false,"collect_name":false}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("patch off: %d %s", w.Code, w.Body.String())
	}
	ch = decodeChannelEnvelope(t, w)
	if ch.IsTest || ch.CollectName || !ch.CollectPhone {
		t.Fatalf("after patch off: %+v", ch)
	}

	// A PATCH of an unrelated field keeps all flags.
	w = httptest.NewRecorder()
	h.HandleUpdateChannel(w, channelTTLRequest(http.MethodPatch, orgID, ch.ID, []byte(`{"name":"Renamed"}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("patch name: %d %s", w.Code, w.Body.String())
	}
	if ch = decodeChannelEnvelope(t, w); ch.IsTest || ch.CollectName || !ch.CollectPhone {
		t.Fatalf("after rename: %+v", ch)
	}
}
