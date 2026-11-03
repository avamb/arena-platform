package heventpage

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/egress"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/eventpage"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/mediastore"
)

var (
	testOrg = uuid.MustParse("01900000-0000-7000-8000-000000000001")
	testNow = time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
)

func posterPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewGray(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// site serves the organizer's pages.
func site(t *testing.T) *httptest.Server {
	t.Helper()
	poster := posterPNG(t, 1000, 1250)
	wide := posterPNG(t, 1200, 630)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/event":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, `<html lang="es"><head>
<meta property="og:image" content="/wide.png">
<script type="application/ld+json">{"@context":"https://schema.org","@type":"Event","name":"Concierto de otoño",
"startDate":"2099-11-20T20:00:00+01:00","image":"/poster.png",
"location":{"@type":"Place","name":"Sala Clamores","address":{"@type":"PostalAddress","addressLocality":"Madrid","addressCountry":"ES"}},
"offers":[{"@type":"Offer","name":"General","price":"15","priceCurrency":"EUR"}]}</script>
</head><body>Concierto</body></html>`)
		case "/og":
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, `<html><head><meta property="og:title" content="Noche de jazz"><meta property="og:image" content="/wide.png"></head>
<body><p>Noche de jazz el sábado 21 de noviembre a las 21:00. Entrada 12 euros en la puerta.</p></body></html>`)
		case "/empty":
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, `<html><body></body></html>`)
		case "/poster.png":
			_, _ = w.Write(poster)
		case "/wide.png":
			_, _ = w.Write(wide)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

type fakeStore struct {
	mu   sync.Mutex
	objs []mediastore.InsertInput
}

func (f *fakeStore) PutAndStream(_ context.Context, _, _ string, body io.Reader) (string, int64, error) {
	b, _ := io.ReadAll(body)
	return "sum", int64(len(b)), nil
}

func (f *fakeStore) Insert(_ context.Context, in mediastore.InsertInput) (mediastore.Object, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objs = append(f.objs, in)
	return mediastore.Object{ID: uuid.MustParse("01900000-0000-7000-8000-0000000000aa")}, nil
}

func (f *fakeStore) Backend() string { return "local" }

type fakeModel struct {
	calls int
	facts eventpage.ModelFacts
}

func (m *fakeModel) ReadText(_ context.Context, _ string, _ time.Time) (eventpage.ModelFacts, error) {
	m.calls++
	return m.facts, nil
}

type countQuota struct{ left, taken int }

func (q *countQuota) Take(string) bool {
	if q.left == 0 {
		return false
	}
	q.left--
	q.taken++
	return true
}

func allow(_ http.ResponseWriter, _ *http.Request, _ uuid.UUID, _ bool, _, _ string) bool { return true }

func newHandler(store *fakeStore, model *fakeModel, quota *countQuota) *Handler {
	h := New(slog.New(slog.NewTextHandler(io.Discard, nil))).
		WithRowOrgAccess(allow).
		withPictureStore(store).
		withClock(func() time.Time { return testNow })
	if model != nil {
		h = h.WithModel(model, quota)
	}
	return h
}

func call(t *testing.T, h *Handler, url string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	r := chi.NewRouter()
	r.Post("/v1/organizations/{org_id}/event-page-facts", h.PageFacts)
	body, _ := json.Marshal(map[string]string{"url": url})
	req := httptest.NewRequest(http.MethodPost, "/v1/organizations/"+testOrg.String()+"/event-page-facts", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

func errCode(out map[string]any) string {
	e, _ := out["error"].(map[string]any)
	s, _ := e["code"].(string)
	return s
}

func TestPageFacts_JSONLDPageNeedsNoModel(t *testing.T) {
	srv := site(t)
	store, model := &fakeStore{}, &fakeModel{}
	rec, out := call(t, newHandler(store, model, nil), srv.URL+"/event")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	facts := out["facts"].(map[string]any)
	if facts["name"] != "Concierto de otoño" || facts["date"] != "2099-11-20" || facts["time"] != "20:00" || facts["country"] != "ES" {
		t.Errorf("facts = %v", facts)
	}
	cats := facts["categories"].([]any)
	if len(cats) != 1 || cats[0].(map[string]any)["price_minor"].(float64) != 1500 {
		t.Errorf("categories = %v", cats)
	}
	img := facts["image"].(map[string]any)
	if img["fits_poster"] != true || img["width"].(float64) != 1000 {
		t.Errorf("image = %v (the 4:5 JSON-LD picture must win over the wide og:image)", img)
	}
	if len(store.objs) != 1 || store.objs[0].OwnerType != "event_poster" || *store.objs[0].OrgID != testOrg || *store.objs[0].Width != 1000 {
		t.Errorf("stored = %+v", store.objs)
	}
	if model.calls != 0 {
		t.Error("a page with dates and prices must not call the model")
	}
	if out["final_url"] != srv.URL+"/event" || out["language"] != "es" {
		t.Errorf("final_url %v, language %v", out["final_url"], out["language"])
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Error("the answer must not be cached")
	}
}

func TestPageFacts_ModelFillsDatesAndPricesWithinItsCap(t *testing.T) {
	srv := site(t)
	model := &fakeModel{facts: eventpage.ModelFacts{
		Dates:      []eventpage.ModelDate{{Date: "2026-11-21", Time: "21:00"}},
		Categories: []eventpage.ModelCategory{{Name: "Entrada", Price: "12", Currency: "EUR"}},
	}}
	quota := &countQuota{left: 1}
	h := newHandler(&fakeStore{}, model, quota)
	rec, out := call(t, h, srv.URL+"/og")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	facts := out["facts"].(map[string]any)
	if facts["name"] != "Noche de jazz" || facts["date"] != "2026-11-21" {
		t.Errorf("facts = %v", facts)
	}
	src := out["sources"].(map[string]any)
	if src["name"] != "open_graph" || src["dates"] != "model" || src["categories"] != "model" {
		t.Errorf("sources = %v", src)
	}
	if !hasWarning(out, eventpage.WarnReadByModel) || !hasWarning(out, eventpage.WarnImageNotPoster) {
		t.Errorf("warnings = %v", out["warnings"])
	}
	if facts["image"].(map[string]any)["fits_poster"] != false {
		t.Error("a wide picture is stored but does not fit the poster rules")
	}
	// The cap is spent: the next reading answers without the model.
	rec, out = call(t, h, srv.URL+"/og")
	if rec.Code != http.StatusOK || !hasWarning(out, eventpage.WarnModelQuota) || model.calls != 1 {
		t.Errorf("past the cap: %d %v calls=%d", rec.Code, out["warnings"], model.calls)
	}
}

func hasWarning(out map[string]any, code string) bool {
	ws, _ := out["warnings"].([]any)
	for _, w := range ws {
		if w.(map[string]any)["code"] == code {
			return true
		}
	}
	return false
}

func TestPageFacts_Refusals(t *testing.T) {
	srv := site(t)
	cases := []struct {
		name   string
		h      *Handler
		url    string
		status int
		code   string
	}{
		{"ticketing", newHandler(&fakeStore{}, nil, nil), "https://www.ticketmaster.es/event/1", 422, "event_page.ticketing_platform"},
		{"invalid", newHandler(&fakeStore{}, nil, nil), "javascript:alert(1)", 400, "event_page.invalid_url"},
		{"private", newHandler(&fakeStore{}, nil, nil).WithPolicy(egress.Policy{AllowHTTP: true, MaxRedirects: 5, Private: egress.PrivateDeny}), srv.URL + "/event", 422, "event_page.destination_not_allowed"},
		{"metadata", newHandler(&fakeStore{}, nil, nil).WithPolicy(egress.Policy{AllowHTTP: true, MaxRedirects: 5, Private: egress.PrivateDeny}), "http://169.254.169.254/latest", 422, "event_page.destination_not_allowed"},
		{"upstream", newHandler(&fakeStore{}, nil, nil), srv.URL + "/missing", 503, "event_page.fetch_failed"},
		{"nothing", newHandler(&fakeStore{}, nil, nil), srv.URL + "/empty", 422, "event_page.nothing_found"},
	}
	for _, c := range cases {
		rec, out := call(t, c.h, c.url)
		if rec.Code != c.status || errCode(out) != c.code {
			t.Errorf("%s: %d %q, want %d %q", c.name, rec.Code, errCode(out), c.status, c.code)
		}
	}
	_, out := call(t, newHandler(&fakeStore{}, nil, nil), srv.URL+"/missing")
	if d, _ := out["error"].(map[string]any)["details"].(map[string]any); d["status"].(float64) != 404 {
		t.Errorf("details = %v", d)
	}
}

func TestPageFacts_PageCapCountsOnlyCheckedAddresses(t *testing.T) {
	srv := site(t)
	quota := &countQuota{left: 1}
	h := newHandler(&fakeStore{}, nil, nil).WithPageQuota(quota)
	if rec, _ := call(t, h, "https://www.eventbrite.com/e/1"); rec.Code != 422 || quota.taken != 0 {
		t.Errorf("a refused address must not take a slot: %d taken=%d", rec.Code, quota.taken)
	}
	if rec, _ := call(t, h, srv.URL+"/event"); rec.Code != 200 {
		t.Fatalf("first read: %d", rec.Code)
	}
	if rec, out := call(t, h, srv.URL+"/event"); rec.Code != 429 || errCode(out) != "event_page.rate_limited" {
		t.Errorf("past the cap: %d %v", rec.Code, out)
	}
}

func TestPageFacts_GuardAndBody(t *testing.T) {
	deny := func(w http.ResponseWriter, r *http.Request, _ uuid.UUID, _ bool, code, msg string) bool {
		w.WriteHeader(http.StatusNotFound)
		return false
	}
	h := newHandler(&fakeStore{}, nil, nil).WithRowOrgAccess(deny)
	if rec, _ := call(t, h, "https://example.org/"); rec.Code != 404 {
		t.Errorf("foreign organization: %d", rec.Code)
	}
	h = New(nil)
	if rec, out := call(t, h, "https://example.org/"); rec.Code != 503 || errCode(out) != "dependency.org_access_unavailable" {
		t.Errorf("no guard: %d %v", rec.Code, out)
	}
	h = newHandler(&fakeStore{}, nil, nil)
	r := chi.NewRouter()
	r.Post("/v1/organizations/{org_id}/event-page-facts", h.PageFacts)
	req := httptest.NewRequest(http.MethodPost, "/v1/organizations/"+testOrg.String()+"/event-page-facts", strings.NewReader("not json"))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Errorf("bad body: %d", rec.Code)
	}
}
