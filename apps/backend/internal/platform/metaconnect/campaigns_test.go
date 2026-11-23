package metaconnect

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

type campStub struct {
	mu    sync.Mutex
	calls []string // "METHOD path?query body"
	srv   *httptest.Server
}

func newCampStub(t *testing.T, h func(w http.ResponseWriter, r *http.Request, path string)) *campStub {
	t.Helper()
	s := &campStub{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		s.mu.Lock()
		s.calls = append(s.calls, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery+" "+string(body))
		s.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"error":{"code":190}}`))
			return
		}
		h(w, r, strings.TrimPrefix(r.URL.Path, "/v25.0/"))
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *campStub) client() *Client {
	return NewClient(s.srv.URL, "v25.0", "app", "secret", s.srv.Client())
}

func (s *campStub) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

func TestCampaignIDsAndAccounts(t *testing.T) {
	for id, want := range map[string]bool{
		"1": true, "12345678901234567890123456789012": true, "123456789012345678901234567890123": false,
		"": false, "12a": false, "../me": false, "1/2": false, "1?x=1": false, " 1": false, "-1": false, "１": false,
	} {
		if got := ValidCampaignID(id); got != want {
			t.Errorf("ValidCampaignID(%q) = %v", id, got)
		}
	}
	for in, want := range map[string]string{"act_77": "act_77", "77": "act_77", " act_123 ": "act_123", "act_": "", "act_7x": "", "me": "", "act_act_1": ""} {
		got, ok := NormalizeAdAccountID(in)
		if got != want || ok != (want != "") {
			t.Errorf("NormalizeAdAccountID(%q) = %q %v", in, got, ok)
		}
	}
	if !SameAdAccount("77", "act_77") || !SameAdAccount("act_77", "77") || SameAdAccount("78", "act_77") || SameAdAccount("", "") || SameAdAccount("x", "x") {
		t.Error("SameAdAccount")
	}
	for code, want := range map[int]bool{4: true, 17: true, 32: true, 613: true, 80000: true, 80014: true, 80015: false, 190: false, 100: false} {
		if got := IsRateLimited(&GraphError{Code: code}); got != want {
			t.Errorf("IsRateLimited(%d) = %v", code, got)
		}
	}
	if IsRateLimited(errors.New("x")) {
		t.Error("plain error is not a rate limit")
	}
}

func TestClient_CampaignsPaging(t *testing.T) {
	s := newCampStub(t, func(w http.ResponseWriter, r *http.Request, path string) {
		if r.Method != http.MethodGet || path != "act_77/campaigns" {
			w.WriteHeader(400)
			return
		}
		q := r.URL.Query()
		if q.Get("fields") != "id,name,status,effective_status" || q.Get("limit") != "200" {
			w.WriteHeader(400)
			return
		}
		switch q.Get("after") {
		case "":
			_, _ = w.Write([]byte(`{"data":[{"id":"1","name":"One","status":"ACTIVE","effective_status":"ACTIVE"},{"id":"../x","name":"bad"}],"paging":{"cursors":{"after":"c1"},"next":"https://x"}}`))
		case "c1":
			_, _ = w.Write([]byte(`{"data":[{"id":2,"name":"Two","status":"PAUSED","effective_status":"PAUSED"}],"paging":{"cursors":{"after":"c2"}}}`))
		default:
			w.WriteHeader(500)
		}
	})
	got, err := s.client().Campaigns(context.Background(), "tok", "77")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "1" || got[1].ID != "2" || got[1].Status != "PAUSED" || got[0].Name != "One" {
		t.Fatalf("campaigns = %+v", got)
	}
	if s.count() != 2 {
		t.Fatalf("calls = %d", s.count())
	}
}

func TestClient_CampaignsStopsAfterFivePages(t *testing.T) {
	n := 0
	s := newCampStub(t, func(w http.ResponseWriter, r *http.Request, path string) {
		n++
		_, _ = w.Write([]byte(`{"data":[{"id":"` + strings.Repeat("1", n) + `"}],"paging":{"cursors":{"after":"c` + strings.Repeat("x", n) + `"},"next":"https://x"}}`))
	})
	got, err := s.client().Campaigns(context.Background(), "tok", "act_1")
	if err != nil || len(got) != 5 || s.count() != 5 {
		t.Fatalf("got %d campaigns, %d calls, %v", len(got), s.count(), err)
	}
	if _, err := s.client().Campaigns(context.Background(), "tok", "act_1/../me"); !errors.Is(err, ErrInvalidAdAccount) || s.count() != 5 {
		t.Fatalf("bad account: %v, calls %d", err, s.count())
	}
}

func TestClient_CampaignAndStatus(t *testing.T) {
	var form url.Values
	s := newCampStub(t, func(w http.ResponseWriter, r *http.Request, path string) {
		switch {
		case r.Method == http.MethodGet && path == "55":
			if r.URL.Query().Get("fields") != "id,name,status,effective_status,account_id" {
				w.WriteHeader(400)
				return
			}
			_, _ = w.Write([]byte(`{"id":"55","name":"Camp","status":"ACTIVE","effective_status":"ACTIVE","account_id":"77"}`))
		case r.Method == http.MethodPost && path == "55":
			_ = r.ParseForm()
			form = r.PostForm
			_, _ = w.Write([]byte(`{"success":true}`))
		case r.Method == http.MethodPost && path == "56":
			_, _ = w.Write([]byte(`{}`))
		case path == "57":
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":{"code":17,"message":"secret text"}}`))
		default:
			w.WriteHeader(404)
		}
	})
	c := s.client()
	ctx := context.Background()
	cp, err := c.Campaign(ctx, "tok", "55")
	if err != nil || cp.ID != "55" || cp.AccountID != "77" || cp.Status != CampaignActive || !SameAdAccount(cp.AccountID, "act_77") {
		t.Fatalf("campaign = %+v %v", cp, err)
	}
	if err := c.SetCampaignStatus(ctx, "tok", "55", CampaignPaused); err != nil {
		t.Fatal(err)
	}
	if len(form) != 1 || form.Get("status") != "PAUSED" {
		t.Fatalf("form = %v", form)
	}
	if err := c.SetCampaignStatus(ctx, "tok", "56", CampaignActive); !errors.Is(err, ErrCampaignNotConfirmed) {
		t.Fatalf("unconfirmed: %v", err)
	}
	if _, err := c.Campaign(ctx, "tok", "57"); !IsRateLimited(err) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("rate limit: %v", err)
	}
	if _, err := c.Campaign(ctx, "bad", "55"); !IsTokenInvalid(err) {
		t.Fatalf("190: %v", err)
	}
	before := s.count()
	for _, st := range []string{"DELETED", "ARCHIVED", "paused", "", "PAUSED&daily_budget=1"} {
		if err := c.SetCampaignStatus(ctx, "tok", "55", st); !errors.Is(err, ErrInvalidCampaignStatus) {
			t.Errorf("status %q: %v", st, err)
		}
	}
	if err := c.SetCampaignStatus(ctx, "tok", "55/x", CampaignPaused); !errors.Is(err, ErrInvalidCampaignID) {
		t.Errorf("bad id: %v", err)
	}
	if _, err := c.Campaign(ctx, "tok", "me"); !errors.Is(err, ErrInvalidCampaignID) {
		t.Errorf("bad id read: %v", err)
	}
	if s.count() != before {
		t.Fatalf("a refused call reached Meta: %d -> %d", before, s.count())
	}
}
