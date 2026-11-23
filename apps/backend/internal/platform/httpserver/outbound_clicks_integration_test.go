//go:build integration

package httpserver

// Proof of the organizer's read of clicks to box offices (spec 63, L-10):
//
//	GET /v1/organizations/{org_id}/events/{event_id}/outbound-clicks
//
//	DATABASE_URL=... go test -tags integration -p 1 ./apps/backend/internal/platform/httpserver/ -run TestOutboundClicks

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func (fx *funnelFixture) readOutbound(orgID, eventID any, key string) (int, []byte) {
	fx.t.Helper()
	path := fmt.Sprintf("/v1/organizations/%v/events/%v/outbound-clicks?from=2026-01-01&to=2026-12-31", orgID, eventID)
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	rec := httptest.NewRecorder()
	fx.readSrv.Router().ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

func TestOutboundClicks_ReadsCountsAndGuardsOrganization(t *testing.T) {
	fx := newFunnelFixture(t)
	ctx := t.Context()
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = fx.pool.Exec(bg, `DELETE FROM outbound_clicks_daily WHERE org_id = $1`, fx.f.orgID)
		_, _ = fx.pool.Exec(bg, `DELETE FROM sales_funnel_daily WHERE org_id = $1`, fx.f.orgID)
	})
	for _, s := range []string{
		`INSERT INTO outbound_clicks_daily (org_id, event_id, day, seller, hits) VALUES ($1, $2, '2026-05-01', 'Karabas', 3), ($1, $2, '2026-05-02', 'Karabas', 2), ($1, $2, '2026-05-02', 'Concert', 4)`,
		`INSERT INTO sales_funnel_daily (org_id, event_id, day, step, surface, utm_source, utm_campaign, hits) VALUES ($1, $2, '2026-05-01', 'outbound_click', 'external_site', 'meta', '', 9)`,
		`INSERT INTO sales_funnel_daily (org_id, event_id, day, step, surface, utm_source, utm_campaign, hits) VALUES ($1, $2, '2026-05-01', 'view', 'external_site', 'meta', '', 18)`,
	} {
		if _, err := fx.pool.Exec(ctx, s, fx.f.orgID, fx.f.eventID); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	keyA := fx.key(fx.f.orgID, "report.read")
	code, raw := fx.readOutbound(fx.f.orgID, fx.f.eventID, keyA)
	if code != http.StatusOK {
		t.Fatalf("read = %d %s", code, raw)
	}
	var got struct {
		Total  int64    `json:"total"`
		Visits int64    `json:"visits"`
		Share  *float64 `json:"share_of_visitors"`
		ByDay  []struct {
			Day    string
			Clicks int64
		} `json:"by_day"`
		BySeller []struct {
			Seller string
			Clicks int64
		} `json:"by_seller"`
		BySource []struct {
			UTMSource string `json:"utm_source"`
			Clicks    int64  `json:"clicks"`
		} `json:"by_source"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Total != 9 || got.Visits != 18 || got.Share == nil || *got.Share != 0.5 {
		t.Errorf("answer = %s", raw)
	}
	if len(got.ByDay) != 2 || len(got.BySeller) != 2 || got.BySeller[0].Seller != "Karabas" || got.BySeller[0].Clicks != 5 {
		t.Errorf("breakdown = %s", raw)
	}
	if len(got.BySource) != 1 || got.BySource[0].UTMSource != "meta" || got.BySource[0].Clicks != 9 {
		t.Errorf("by_source = %s", raw)
	}

	other := newHostedOrgFixture(t, ctx, fx.pool, "oc")
	t.Cleanup(other.cleanup)
	t.Cleanup(func() {
		_, _ = fx.pool.Exec(context.Background(), `DELETE FROM api_keys WHERE org_id = $1`, other.orgID)
	})
	keyB := fx.key(other.orgID, "report.read")
	if code, raw := fx.readOutbound(other.orgID, fx.f.eventID, keyB); code != http.StatusNotFound || !strings.Contains(string(raw), "event.not_found") {
		t.Errorf("foreign event = %d %s; want 404 event.not_found", code, raw)
	}
	if code, raw := fx.readOutbound(fx.f.orgID, fx.f.eventID, keyB); code != http.StatusForbidden && code != http.StatusNotFound {
		t.Errorf("foreign org path = %d %s", code, raw)
	}
	keyNo := fx.key(fx.f.orgID, "event.read")
	if code, _ := fx.readOutbound(fx.f.orgID, fx.f.eventID, keyNo); code != http.StatusForbidden {
		t.Errorf("key without report.read = %d; want 403", code)
	}
}
