//go:build integration

package httpserver

// Proof of the static site's tracking route (spec 63 section 19, task L-10)
// through the real router and a real database:
//
//	POST /v1/public/feeds/{feed_token}/track
//
//	DATABASE_URL=... go test -tags integration -p 1 ./apps/backend/internal/platform/httpserver/ -run TestSiteTrack

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

const (
	trackSiteOrigin = "https://track-site.example"
	trackSiteKey    = "track-site-secret-key"
	trackBrowserUA  = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.2 Safari/605.1.15"
)

func trackFixture(t *testing.T) (*Server, *hostedOrgFixture) {
	t.Helper()
	pool := integrationPool(t)
	ctx := t.Context()
	f := newHostedOrgFixture(t, ctx, pool, "trk")
	t.Cleanup(f.cleanup)
	sum := sha256.Sum256([]byte(trackSiteKey))
	settings, _ := json.Marshal(map[string]any{"site": map[string]any{
		"origins": []string{trackSiteOrigin}, "track_key_hash": hex.EncodeToString(sum[:]),
	}})
	if _, err := pool.Exec(ctx, `UPDATE sales_channels SET settings = $2::jsonb WHERE id = $1`, f.channelID, string(settings)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO org_conversion_settings (org_id, meta_pixel_id, meta_capi_token, ga4_measurement_id, ga4_api_secret)
		VALUES ($1, '1352226752766944', 'meta-token-123', 'G-ABCD1234', 'ga-secret-123')`, f.orgID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		for _, s := range []string{
			`DELETE FROM conversion_deliveries WHERE org_id = $1`,
			`DELETE FROM org_conversion_settings WHERE org_id = $1`,
			`DELETE FROM outbound_clicks_daily WHERE org_id = $1`,
			`DELETE FROM sales_funnel_daily WHERE org_id = $1`,
		} {
			if _, err := pool.Exec(bg, s, f.orgID); err != nil {
				t.Logf("track cleanup (%.40s): %v", s, err)
			}
		}
	})
	return buildBotIntegrationServer(t, pool), f
}

type trackOpts struct {
	key, clientIP, ua, token string
	declined                 bool
	event                    string
}

func trackSend(t *testing.T, srv *Server, f *hostedOrgFixture, o trackOpts) (int, string) {
	t.Helper()
	if o.token == "" {
		o.token = f.feedToken
	}
	if o.event == "" {
		o.event = "outbound_ticket_click"
	}
	body, _ := json.Marshal(map[string]any{
		"event": o.event, "event_id": "evt_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16],
		"page_url": trackSiteOrigin + "/tours/a", "utm_source": "meta", "utm_campaign": "spring",
		"fbp": "fb.1.1700000000.123", "ga_client_id": "123456.7890", "vid": "visitor-1",
		"tracking_declined": o.declined,
		"data": map[string]any{
			"event_id": f.eventID.String(), "seller": "Karabas", "city": "Kyiv",
			"value": 25.5, "currency": "eur",
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/public/feeds/"+o.token+"/track", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "198.51.100.70:4000"
	ua := o.ua
	if ua == "" {
		ua = trackBrowserUA
	}
	req.Header.Set("User-Agent", ua)
	if o.key != "" {
		req.Header.Set("X-Site-Key", o.key)
		req.Header.Set("X-Site-Client-IP", o.clientIP)
	}
	rec := httptest.NewRecorder()
	srv.router.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func trackCount(t *testing.T, f *hostedOrgFixture, query string) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(t.Context(), query, f.orgID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

const (
	qTrackFunnel     = `SELECT COALESCE(sum(hits), 0)::int FROM sales_funnel_daily WHERE org_id = $1 AND step = 'outbound_click' AND surface = 'external_site'`
	qTrackSuspect    = `SELECT COALESCE(sum(suspect_hits), 0)::int FROM sales_funnel_daily WHERE org_id = $1`
	qTrackOutbound   = `SELECT COALESCE(sum(hits), 0)::int FROM outbound_clicks_daily WHERE org_id = $1 AND seller = 'Karabas'`
	qTrackDeliveries = `SELECT count(*)::int FROM conversion_deliveries WHERE org_id = $1 AND kind = 'tickets_click'`
)

func TestSiteTrack_ValidKeyCountsAndQueuesBothProviders(t *testing.T) {
	srv, f := trackFixture(t)
	code, body := trackSend(t, srv, f, trackOpts{key: trackSiteKey, clientIP: "203.0.113.9"})
	if code != http.StatusAccepted || !strings.Contains(body, `"accepted"`) {
		t.Fatalf("track: %d %s", code, body)
	}
	if n := trackCount(t, f, qTrackFunnel); n != 1 {
		t.Errorf("funnel hits = %d, want 1", n)
	}
	if n := trackCount(t, f, qTrackOutbound); n != 1 {
		t.Errorf("outbound_clicks_daily hits = %d, want 1", n)
	}
	if n := trackCount(t, f, qTrackDeliveries); n != 2 {
		t.Errorf("deliveries = %d, want 2 (meta and ga4)", n)
	}
	var payload string
	if err := f.pool.QueryRow(t.Context(), `SELECT payload::text FROM conversion_deliveries WHERE org_id = $1 AND provider = 'ga4'`, f.orgID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"ga_client_id": "123456.7890"`, `"client_ip": "203.0.113.9"`, `"checkout_type": "external"`, `"content_id": "` + f.eventID.String() + `"`} {
		if !strings.Contains(payload, want) {
			t.Errorf("payload lacks %s: %s", want, payload)
		}
	}
}

func TestSiteTrack_DeclinedCountsInFunnelButSendsNothing(t *testing.T) {
	srv, f := trackFixture(t)
	if code, body := trackSend(t, srv, f, trackOpts{key: trackSiteKey, clientIP: "203.0.113.10", declined: true}); code != http.StatusAccepted {
		t.Fatalf("track: %d %s", code, body)
	}
	if n := trackCount(t, f, qTrackFunnel); n != 1 {
		t.Errorf("funnel hits = %d, want 1", n)
	}
	if n := trackCount(t, f, qTrackDeliveries); n != 0 {
		t.Errorf("deliveries = %d, want 0", n)
	}
}

func TestSiteTrack_NoKeyOrWrongKeySendsNothingToAds(t *testing.T) {
	srv, f := trackFixture(t)
	if code, _ := trackSend(t, srv, f, trackOpts{}); code != http.StatusAccepted {
		t.Fatalf("no key: %d", code)
	}
	if code, _ := trackSend(t, srv, f, trackOpts{key: "wrong-key", clientIP: "203.0.113.11", ua: trackBrowserUA + " x"}); code != http.StatusAccepted {
		t.Fatalf("wrong key: %d", code)
	}
	if n := trackCount(t, f, qTrackDeliveries); n != 0 {
		t.Errorf("deliveries = %d, want 0", n)
	}
	if n := trackCount(t, f, qTrackFunnel); n != 2 {
		t.Errorf("funnel hits = %d, want 2 (still counted anonymously)", n)
	}
}

func TestSiteTrack_BotCountsNowhere(t *testing.T) {
	srv, f := trackFixture(t)
	code, _ := trackSend(t, srv, f, trackOpts{key: trackSiteKey, clientIP: "203.0.113.12", ua: "curl/8.4.0"})
	if code != http.StatusAccepted {
		t.Fatalf("bot: %d", code)
	}
	if n := trackCount(t, f, qTrackFunnel) + trackCount(t, f, qTrackSuspect) + trackCount(t, f, qTrackOutbound) + trackCount(t, f, qTrackDeliveries); n != 0 {
		t.Errorf("a bot left %d traces", n)
	}
}

func TestSiteTrack_UnknownTokenIs404AndForeignPageIsNotCounted(t *testing.T) {
	srv, f := trackFixture(t)
	if code, _ := trackSend(t, srv, f, trackOpts{token: "no-such-token-" + uuid.NewString()[:8], key: trackSiteKey, clientIP: "203.0.113.13"}); code != http.StatusNotFound {
		t.Errorf("unknown token: %d, want 404", code)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/public/feeds/"+f.feedToken+"/track",
		strings.NewReader(`{"event":"outbound_ticket_click","event_id":"e1","page_url":"https://evil.example/x","data":{"event_id":"`+f.eventID.String()+`"}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", trackBrowserUA)
	rec := httptest.NewRecorder()
	srv.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Errorf("foreign page: %d, want 202", rec.Code)
	}
	if n := trackCount(t, f, qTrackFunnel); n != 0 {
		t.Errorf("foreign page counted %d", n)
	}
}

func TestSiteTrack_UnreadableBodyIs400(t *testing.T) {
	srv, f := trackFixture(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/public/feeds/"+f.feedToken+"/track", strings.NewReader(`{"event":`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("unreadable body: %d, want 400", rec.Code)
	}
}
