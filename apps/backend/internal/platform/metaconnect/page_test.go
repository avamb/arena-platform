package metaconnect

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// pageStub answers GET /v25.0/{id} with a Page and serves its picture.
func pageStub(t *testing.T, pageBody string, status int) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/pic.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("PNGDATA"))
		case "/pic.html":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<html>"))
		case "/big.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte(strings.Repeat("x", maxPictureBytes+1)))
		case "/v25.0/555":
			if r.Header.Get("Authorization") != "Bearer tok" {
				w.WriteHeader(401)
				_, _ = w.Write([]byte(`{"error":{"code":190,"message":"secret text"}}`))
				return
			}
			if got := r.URL.Query().Get("fields"); got != "name,category,link,fan_count,picture{url}" {
				t.Errorf("fields = %q", got)
			}
			w.WriteHeader(status)
			_, _ = w.Write([]byte(strings.ReplaceAll(pageBody, "{base}", srv.URL)))
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestPage_ReadsPreview(t *testing.T) {
	srv := pageStub(t, `{"id":"555","name":" Club ","category":"Concert venue","link":"https://www.facebook.com/club",`+
		`"fan_count":1234,"picture":{"data":{"url":"{base}/pic.png"}}}`, 200)
	c := NewClient(srv.URL, "v25.0", "app", "secret", srv.Client())
	p, err := c.Page(context.Background(), "tok", "555")
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "555" || p.Name != "Club" || p.Category != "Concert venue" || p.Link != "https://www.facebook.com/club" ||
		p.FanCount == nil || *p.FanCount != 1234 || p.PictureURL != srv.URL+"/pic.png" {
		t.Fatalf("page = %+v", p)
	}
	data, err := c.PictureDataURL(context.Background(), p.PictureURL)
	if err != nil || data != "data:image/png;base64,UE5HREFUQQ==" {
		t.Fatalf("picture = %q %v", data, err)
	}
}

func TestPage_ErrorsAndRefusals(t *testing.T) {
	srv := pageStub(t, `{"error":{"code":10,"message":"Meta words"}}`, 403)
	c := NewClient(srv.URL, "v25.0", "app", "secret", srv.Client())
	if _, err := c.Page(context.Background(), "tok", "555"); !IsPermissionError(err) || strings.Contains(err.Error(), "Meta words") {
		t.Fatalf("permission error: %v", err)
	}
	if _, err := c.Page(context.Background(), "other", "555"); !IsTokenInvalid(err) || strings.Contains(err.Error(), "secret text") {
		t.Fatalf("190: %v", err)
	}
	for _, bad := range []string{"", "me", "555/feed", "../x", "act_1"} {
		if _, err := c.Page(context.Background(), "tok", bad); !errors.Is(err, ErrBadPageID) {
			t.Fatalf("page id %q: %v", bad, err)
		}
	}
	// A link that is not https is dropped.
	srv2 := pageStub(t, `{"id":"555","name":"N","link":"javascript:alert(1)","picture":{"data":{"url":"ftp://x/y"}}}`, 200)
	c2 := NewClient(srv2.URL, "v25.0", "app", "secret", srv2.Client())
	p, err := c2.Page(context.Background(), "tok", "555")
	if err != nil || p.Link != "" || p.PictureURL != "" || p.FanCount != nil {
		t.Fatalf("unsafe fields kept: %+v %v", p, err)
	}
}

func TestPictureDataURL_Refusals(t *testing.T) {
	srv := pageStub(t, `{}`, 200)
	c := NewClient(srv.URL, "v25.0", "app", "secret", srv.Client())
	for _, u := range []string{srv.URL + "/pic.html", srv.URL + "/big.png", srv.URL + "/missing.png", "https://evil.example/pic.png", "http://scontent.fbcdn.net/x.png"} {
		if got, err := c.PictureDataURL(context.Background(), u); err == nil || got != "" || strings.Contains(err.Error(), u) {
			t.Fatalf("%s: %q %v", u, got, err)
		}
	}
	// The production client fetches only Meta's CDN.
	prod := NewClient("", "v25.0", "app", "secret", nil)
	for _, u := range []string{srv.URL + "/pic.png", "https://fbcdn.net.evil.example/x.png"} {
		if _, err := prod.PictureDataURL(context.Background(), u); err == nil {
			t.Fatalf("production client fetched %s", u)
		}
	}
}
