//go:build integration

// public_site_integration_test.go: GET /v1/public/feeds/{feed_token}/site on
// a live database.
//
//	DATABASE_URL=postgres://... go test -tags integration -p 1 \
//	    ./apps/backend/internal/platform/httpserver/hfeed/ -run TestPublicFeedSite
package hfeed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

func TestPublicFeedSite(t *testing.T) {
	pool := publicPageIntegrationPool(t)
	ctx := context.Background()
	q := gen.New(pool)
	run := uuid.NewString()
	org, err := q.InsertOrganization(ctx, "Site Test Org "+run, "site-org-"+run, "DE", "en", 1200)
	if err != nil {
		t.Fatal(err)
	}
	mk := func(name, settings string) string {
		ch, err := q.InsertSalesChannel(ctx, org.ID, name+run, "merchant_of_record", "stripe", nil, "5.00", nil, json.RawMessage(settings))
		if err != nil {
			t.Fatal(err)
		}
		ft, err := q.InsertFeedToken(ctx, "site-"+name+"-"+run, ch.ID, "site test")
		if err != nil {
			t.Fatal(err)
		}
		return ft.Token
	}
	withTY := mk("with", `{"site":{"origins":["https://x.example"],"track_key_hash":"SECRETHASH","thank_you":{"title":" Thanks ","text":"Use it","promo_code":"BACK10","url":"http://insecure.example"},"socials":[{"network":"instagram","url":"https://instagram.com/a"},{"network":"myspace","url":"https://m.example"},{"network":"x","url":"http://x.com/a"}]},"contact_form":{"recipients":{"contact":["owner@example.com"]}}}`)
	without := mk("without", `{"site":{"origins":["https://x.example"],"track_key_hash":"SECRETHASH"}}`)

	h := promoterPageHandler(pool)
	get := func(token string) (int, http.Header, string) {
		w := httptest.NewRecorder()
		h.HandlePublicFeedSite(w, seriesFeedReq("/x", map[string]string{"feed_token": token}))
		return w.Code, w.Header(), w.Body.String()
	}

	code, hdr, body := get(withTY)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if !strings.HasPrefix(hdr.Get("Cache-Control"), "public") {
		t.Errorf("Cache-Control = %q", hdr.Get("Cache-Control"))
	}
	var out struct {
		ThankYou *struct {
			Title     string  `json:"title"`
			Text      string  `json:"text"`
			PromoCode *string `json:"promo_code"`
			URL       *string `json:"url"`
		} `json:"thank_you"`
		Socials []struct {
			Network string `json:"network"`
			URL     string `json:"url"`
		} `json:"socials"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	if out.ThankYou == nil || out.ThankYou.Title != "Thanks" || out.ThankYou.Text != "Use it" ||
		out.ThankYou.PromoCode == nil || *out.ThankYou.PromoCode != "BACK10" || out.ThankYou.URL != nil {
		t.Errorf("thank_you = %+v", out.ThankYou)
	}
	if len(out.Socials) != 1 || out.Socials[0].Network != "instagram" || out.Socials[0].URL != "https://instagram.com/a" {
		t.Errorf("socials = %+v", out.Socials)
	}
	for _, leak := range []string{"SECRETHASH", "owner@example.com", "origins", "track_key"} {
		if strings.Contains(body, leak) {
			t.Errorf("body leaks %q: %s", leak, body)
		}
	}

	code, _, body = get(without)
	if code != 200 || strings.TrimSpace(body) != `{"socials":[],"thank_you":null,"thank_you_posters":true}` {
		t.Errorf("without: %d %s", code, body)
	}
	if code, _, _ = get("no-such-token-" + run); code != 404 {
		t.Errorf("unknown token status %d", code)
	}
}
