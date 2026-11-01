package hanalytics

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/analytics"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
)

type recorder struct {
	mu     sync.Mutex
	events []analytics.Event
}

func (r *recorder) Track(_ context.Context, ev analytics.Event) {
	r.mu.Lock()
	r.events = append(r.events, ev)
	r.mu.Unlock()
}

func (r *recorder) list() []analytics.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]analytics.Event(nil), r.events...)
}

type fakeCodes map[string]bool

func (f fakeCodes) ContactCodeExists(_ context.Context, code string) (bool, error) {
	return f[code], nil
}

type errCodes struct{}

func (errCodes) ContactCodeExists(context.Context, string) (bool, error) {
	return false, errors.New("db down")
}

func post(h http.HandlerFunc, body string, ua string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/t", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", ua)
	req.RemoteAddr = "203.0.113.7:4000"
	w := httptest.NewRecorder()
	h(w, req)
	return w
}

const browser = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/120 Safari/537.36"

func TestSite_StoresUnderAIDWithServerSurface(t *testing.T) {
	rec := &recorder{}
	h := New(Options{Tracker: rec})
	w := post(h.HandleSite, `{"aid":"a_7Hk2abcd","pv":"p_Q9abcd","surface":"bot","events":[
		{"name":"page_view","at":"2026-11-01T10:00:00Z","props":{"path":"/pricing"}}]}`, browser)
	if w.Code != http.StatusNoContent || w.Body.Len() != 0 {
		t.Fatalf("status %d body %q", w.Code, w.Body.String())
	}
	evs := rec.list()
	if len(evs) != 1 {
		t.Fatalf("events = %d", len(evs))
	}
	e := evs[0]
	if e.Surface != analytics.SurfaceSite || e.IDKind != analytics.IDKindAID || e.IDValue != "a_7Hk2abcd" || e.Suspect {
		t.Errorf("event = %+v", e)
	}
}

func TestSite_AnonymousUsesPageViewID(t *testing.T) {
	rec := &recorder{}
	h := New(Options{Tracker: rec})
	post(h.HandleSite, `{"aid":"bad","pv":"p_Q9abcd","events":[{"name":"page_view","props":{}}]}`, browser)
	evs := rec.list()
	if len(evs) != 1 || evs[0].IDKind != analytics.IDKindAnon || evs[0].IDValue != "p_Q9abcd" {
		t.Fatalf("events = %+v", evs)
	}
}

func TestSite_NoUsableIdentifierStoresNothing(t *testing.T) {
	rec := &recorder{}
	h := New(Options{Tracker: rec})
	w := post(h.HandleSite, `{"aid":"x","pv":"!!","events":[{"name":"page_view"}]}`, browser)
	if w.Code != http.StatusNoContent || len(rec.list()) != 0 {
		t.Fatalf("status %d events %d", w.Code, len(rec.list()))
	}
}

func TestSite_SuspectAgentFlagged(t *testing.T) {
	rec := &recorder{}
	h := New(Options{Tracker: rec})
	post(h.HandleSite, `{"pv":"p_Q9abcd","events":[{"name":"page_view"}]}`, "Googlebot/2.1")
	if evs := rec.list(); len(evs) != 1 || !evs[0].Suspect {
		t.Fatalf("events = %+v", evs)
	}
}

func TestSite_UnreadableJSONIs400OthersAre204(t *testing.T) {
	h := New(Options{Tracker: &recorder{}})
	for _, body := range []string{``, `not json`, `[1,2]`, `{"events":`} {
		if w := post(h.HandleSite, body, browser); w.Code != http.StatusBadRequest {
			t.Errorf("body %q -> %d, want 400", body, w.Code)
		}
	}
	big := `{"pv":"p_Q9abcd","events":[{"name":"page_view","props":{"title":"` + strings.Repeat("x", MaxBodyBytes) + `"}}]}`
	if w := post(h.HandleSite, big, browser); w.Code != http.StatusBadRequest {
		t.Errorf("oversized body -> %d, want 400", w.Code)
	}
	if w := post(h.HandleSite, `{"pv":"p_Q9abcd"}`, browser); w.Code != http.StatusNoContent {
		t.Errorf("no events -> %d, want 204", w.Code)
	}
}

func TestSite_AtMostFiftyEvents(t *testing.T) {
	rec := &recorder{}
	h := New(Options{Tracker: rec})
	var sb strings.Builder
	sb.WriteString(`{"pv":"p_Q9abcd","events":[`)
	for i := 0; i < 80; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(`{"name":"click","props":{"name":"a"}}`)
	}
	sb.WriteString(`]}`)
	post(h.HandleSite, sb.String(), browser)
	if n := len(rec.list()); n != MaxEvents {
		t.Errorf("events = %d, want %d", n, MaxEvents)
	}
}

func TestSite_RateLimitPerAddressAnswers204(t *testing.T) {
	rec := &recorder{}
	h := New(Options{Tracker: rec, SiteLimit: 3})
	for i := 0; i < 6; i++ {
		if w := post(h.HandleSite, `{"pv":"p_Q9abcd","events":[{"name":"page_view"}]}`, browser); w.Code != http.StatusNoContent {
			t.Fatalf("request %d -> %d", i, w.Code)
		}
	}
	if n := len(rec.list()); n != 3 {
		t.Errorf("stored %d batches, want 3", n)
	}
}

func TestSite_ForwardedForIsNotTrusted(t *testing.T) {
	rec := &recorder{}
	h := New(Options{Tracker: rec, SiteLimit: 2})
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodPost, "/v1/t", strings.NewReader(`{"pv":"p_Q9abcd","events":[{"name":"page_view"}]}`))
		req.RemoteAddr = "203.0.113.9:1"
		req.Header.Set("X-Forwarded-For", fmt.Sprintf("198.51.100.%d", i))
		h.HandleSite(httptest.NewRecorder(), req)
	}
	if n := len(rec.list()); n != 2 {
		t.Errorf("a changing X-Forwarded-For escaped the limit: stored %d", n)
	}
}

func TestSite_EmailClickOnlyForKnownCode(t *testing.T) {
	body := `{"aid":"a_7Hk2abcd","cid":"x7k2qpm3ab","pv":"p_Q9abcd","events":[
		{"name":"page_view","props":{"path":"/pricing","utm_campaign":"nov"}}]}`

	rec := &recorder{}
	New(Options{Tracker: rec, Codes: fakeCodes{"x7k2qpm3ab": true}}).HandleSite(httptest.NewRecorder(), mustReq(body))
	evs := rec.list()
	if len(evs) != 2 {
		t.Fatalf("events = %d, want page_view + email_click", len(evs))
	}
	c := evs[1]
	if c.Name != "email_click" || c.IDKind != analytics.IDKindCID || c.IDValue != "x7k2qpm3ab" || !c.ServerOrigin {
		t.Errorf("email_click = %+v", c)
	}
	if c.Props["path"] != "/pricing" || c.Props["utm_campaign"] != "nov" || c.Props["aid"] != "a_7Hk2abcd" {
		t.Errorf("email_click props = %v", c.Props)
	}

	for name, codes := range map[string]CodeLookup{"unknown": fakeCodes{}, "error": errCodes{}, "none": nil} {
		rec := &recorder{}
		New(Options{Tracker: rec, Codes: codes}).HandleSite(httptest.NewRecorder(), mustReq(body))
		if n := len(rec.list()); n != 1 {
			t.Errorf("%s: events = %d, want only the page_view", name, n)
		}
	}
}

func mustReq(body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/v1/t", strings.NewReader(body))
	req.RemoteAddr = "203.0.113.50:1"
	req.Header.Set("User-Agent", browser)
	return req
}

func TestBatchTimes_KeepsOrderAndDiscardsFarClientClocks(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	got := batchTimes([]batchEvent{
		{At: "2026-11-01T10:00:00Z"}, // client clock 2h behind: only offsets matter
		{At: "2026-11-01T10:00:03Z"},
		{At: "2020-01-01T00:00:00Z"}, // years away: discarded
		{At: "garbage"},
		{},
	}, now)
	if !got[1].Equal(now) || !got[0].Equal(now.Add(-3*time.Second)) {
		t.Errorf("offsets lost: %v", got)
	}
	for _, i := range []int{2, 3, 4} {
		if !got[i].Equal(now) {
			t.Errorf("event %d = %v, want server time", i, got[i])
		}
	}
	for i, ts := range got {
		if ts.After(now) {
			t.Errorf("event %d is in the future: %v", i, ts)
		}
	}
}

func meReq(userID, body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/v1/me/t", strings.NewReader(body))
	req.Header.Set("User-Agent", browser)
	ctx := auth.WithActor(req.Context(), auth.Actor{ID: userID, Type: auth.ActorTypeUser})
	return req.WithContext(ctx)
}

func TestMe_UserComesFromTheTokenNeverTheBody(t *testing.T) {
	rec := &recorder{}
	h := New(Options{Tracker: rec})
	real := uuid.New().String()
	forged := uuid.New().String()
	w := httptest.NewRecorder()
	h.HandleMe(w, meReq(real, `{"user_id":"`+forged+`","id_value":"`+forged+`","id_kind":"aid","surface":"site",
		"events":[{"name":"flow_step","props":{"flow":"event_create","step":"venue","user_id":"`+forged+`"}}]}`))
	if w.Code != http.StatusNoContent {
		t.Fatalf("status %d", w.Code)
	}
	evs := rec.list()
	if len(evs) != 1 {
		t.Fatalf("events = %d", len(evs))
	}
	e := evs[0]
	if e.IDKind != analytics.IDKindUser || e.IDValue != real || e.Surface != analytics.SurfaceEventCenter {
		t.Errorf("event = %+v", e)
	}
}

func TestMe_OrgKeptOnlyWhenAllowed(t *testing.T) {
	own, foreign := uuid.New(), uuid.New()
	rec := &recorder{}
	h := New(Options{Tracker: rec, OrgAccess: func(_ context.Context, id uuid.UUID) (bool, error) {
		if id == own {
			return true, nil
		}
		if id == foreign {
			return false, nil
		}
		return false, errors.New("boom")
	}})
	user := uuid.New().String()
	for _, org := range []string{own.String(), foreign.String(), uuid.NewString(), "not-a-uuid", ""} {
		h.HandleMe(httptest.NewRecorder(), meReq(user, `{"org_id":"`+org+`","events":[{"name":"screen_view","props":{"screen":"/events"}}]}`))
	}
	evs := rec.list()
	if len(evs) != 5 {
		t.Fatalf("events = %d", len(evs))
	}
	if evs[0].OrgID == nil || *evs[0].OrgID != own {
		t.Errorf("own org lost: %v", evs[0].OrgID)
	}
	for i := 1; i < 5; i++ {
		if evs[i].OrgID != nil {
			t.Errorf("case %d: org %v kept", i, evs[i].OrgID)
		}
	}
}

func TestMe_AnonymousActorStoresNothing(t *testing.T) {
	rec := &recorder{}
	h := New(Options{Tracker: rec})
	req := httptest.NewRequest(http.MethodPost, "/v1/me/t", strings.NewReader(`{"events":[{"name":"screen_view"}]}`))
	w := httptest.NewRecorder()
	h.HandleMe(w, req)
	if w.Code != http.StatusNoContent || len(rec.list()) != 0 {
		t.Fatalf("status %d events %d", w.Code, len(rec.list()))
	}
}

func TestMe_RateLimitPerUser(t *testing.T) {
	rec := &recorder{}
	h := New(Options{Tracker: rec, UserLimit: 2})
	a, b := uuid.NewString(), uuid.NewString()
	for i := 0; i < 5; i++ {
		h.HandleMe(httptest.NewRecorder(), meReq(a, `{"events":[{"name":"screen_view"}]}`))
	}
	h.HandleMe(httptest.NewRecorder(), meReq(b, `{"events":[{"name":"screen_view"}]}`))
	if n := len(rec.list()); n != 3 {
		t.Errorf("stored %d, want 2 for a and 1 for b", n)
	}
}

// ------------------------------------------------------- real writer ---

type blockedCopier struct{ release chan struct{} }

func (b blockedCopier) CopyFrom(_ context.Context, _ pgx.Identifier, _ []string, src pgx.CopyFromSource) (int64, error) {
	<-b.release
	return 0, nil
}

// A full channel must not slow the request: with the database write stuck and
// a tiny buffer, every request still answers 204 at once and the overflow is
// counted.
func TestSite_FullBufferDoesNotSlowTheRequest(t *testing.T) {
	var mu sync.Mutex
	dropped := 0.0
	release := make(chan struct{})
	w := analytics.NewWriter(analytics.Options{
		DB: blockedCopier{release: release}, Buffer: 5, BatchSize: 1, FlushEvery: time.Hour,
		Dropped: adder(func(v float64) { mu.Lock(); dropped += v; mu.Unlock() }),
	})
	h := New(Options{Tracker: w, SiteLimit: 100000})
	start := time.Now()
	for i := 0; i < 300; i++ {
		rw := post(h.HandleSite, `{"pv":"p_Q9abcd","events":[{"name":"page_view","props":{"path":"/"}},{"name":"page_view","props":{"path":"/"}}]}`, browser)
		if rw.Code != http.StatusNoContent {
			t.Fatalf("request %d -> %d", i, rw.Code)
		}
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("300 requests took %v with the database stuck", d)
	}
	mu.Lock()
	got := dropped
	mu.Unlock()
	if got < 500 {
		t.Errorf("dropped = %v, want most of the 600 events", got)
	}
	close(release)
	_ = w.Close(context.Background())
}

type adder func(float64)

func (a adder) Add(v float64) { a(v) }
