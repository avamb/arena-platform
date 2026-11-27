package hfeed

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/conversions"
)

type fakePageConversions struct {
	events []conversions.SiteEvent
}

func (f *fakePageConversions) EnqueueEvent(_ context.Context, _ uuid.UUID, ev conversions.SiteEvent) error {
	f.events = append(f.events, ev)
	return nil
}

func (f *fakePageConversions) SessionItem(context.Context, uuid.UUID) (conversions.PurchaseItem, bool) {
	return conversions.PurchaseItem{}, false
}

const convCtxYes = `{"tracking_consent":true,"tracking_consent_text_version":"v1","fbp":"fb.1.1.2"}`

func TestPageConversion_ParseBody(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, body, kind, id string
		ok                   bool
	}{
		{"view", `{"kind":"view_content","event_id":"abc-1_X"}`, conversions.KindViewContent, "vc:abc-1_X", true},
		{"click", `{"kind":"tickets_click","event_id":"abc"}`, conversions.KindTicketsClick, "tc:abc", true},
		{"bad kind", `{"kind":"page_view","event_id":"abc"}`, "", "", false},
		{"no id", `{"kind":"view_content"}`, "", "", false},
		{"bad id char", `{"kind":"view_content","event_id":"a b"}`, "", "", false},
		{"id too long", `{"kind":"view_content","event_id":"` + strings.Repeat("a", 65) + `"}`, "", "", false},
		{"trailing", `{"kind":"view_content","event_id":"a"} {}`, "", "", false},
		{"not json", `nope`, "", "", false},
	}
	for _, c := range cases {
		kind, id, _, ok := parsePageConversion(strings.NewReader(c.body))
		if ok != c.ok || kind != c.kind || id != c.id {
			t.Errorf("%s: got %q %q %v", c.name, kind, id, ok)
		}
	}
}

func TestPageConversion_ContentName(t *testing.T) {
	t.Parallel()
	if got := pageContentName("Show", []string{"Prague"}); got != "Show — Prague" {
		t.Errorf("one city: %q", got)
	}
	if got := pageContentName("Show", []string{"Prague", "Brno"}); got != "Show" {
		t.Errorf("two cities: %q", got)
	}
	if got := pageContentName("Show", []string{""}); got != "Show" {
		t.Errorf("unknown city: %q", got)
	}
	if got := pageContentName("Show", nil); got != "Show" {
		t.Errorf("none: %q", got)
	}
}

func TestPageConversion_EventNeedsConsent(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{``, `null`, `{"tracking_consent":false,"tracking_consent_text_version":"v1"}`, `{"tracking_consent":true}`} {
		if _, ok := pageConversionEvent(conversions.KindViewContent, "vc:a", json.RawMessage(raw), "1.2.3.4", "UA", "id", "n"); ok {
			t.Errorf("context %q must queue nothing", raw)
		}
	}
	ev, ok := pageConversionEvent(conversions.KindTicketsClick, "tc:a", json.RawMessage(convCtxYes), "1.2.3.4", "UA", "id", "n")
	if !ok || ev.EventID != "tc:a" || ev.ContentID != "id" || ev.ContentName != "n" || ev.Context.FBP != "fb.1.1.2" || ev.Context.ClientIP != "1.2.3.4" {
		t.Fatalf("event = %+v ok=%v", ev, ok)
	}
}

func convRequest(body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/v1/public/pages/acme/show/conversion", strings.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("org_slug", "acme")
	rctx.URLParams.Add("event_slug", "show")
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

func TestPageConversion_NilQueries_503(t *testing.T) {
	t.Parallel()
	w := httptest.NewRecorder()
	unsignedFeed().HandlePublicPageConversion(w, convRequest(`{}`))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestPageConversion_RateLimitFirstOnOwnKey(t *testing.T) {
	t.Parallel()
	lim := &trackLimiter{allow: 0}
	fake := &fakePageConversions{}
	h := &Handler{publicFeedQueries: gen.New(nil), logger: slog.Default(), rl: lim, cartConversions: fake}
	w := httptest.NewRecorder()
	h.HandlePublicPageConversion(w, convRequest(`{"kind":"view_content","event_id":"a"}`))
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}
	if len(lim.keys) != 1 || lim.keys[0] != "conv:acme/show" {
		t.Errorf("keys = %v", lim.keys)
	}
	if len(fake.events) != 0 {
		t.Error("queued on a blocked request")
	}
}

func TestPageConversion_BadBodyIs400BeforeAnyRead(t *testing.T) {
	t.Parallel()
	h := &Handler{publicFeedQueries: gen.New(nil), logger: slog.Default(), rl: &trackLimiter{allow: 10}}
	for _, body := range []string{`{"kind":"nope","event_id":"a"}`, `{"kind":"view_content","event_id":""}`, `x`} {
		w := httptest.NewRecorder()
		h.HandlePublicPageConversion(w, convRequest(body))
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d", body, w.Code)
		}
	}
}
