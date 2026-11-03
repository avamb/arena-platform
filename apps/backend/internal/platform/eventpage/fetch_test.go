package eventpage

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/text/encoding/charmap"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/egress"
)

// testPolicy is egress.Fetch: inside a test binary it reaches httptest
// servers on 127.0.0.1.
var testPolicy = egress.Fetch

func pageServer(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func TestFetchPage_ReadsHTMLAndFollowsRedirects(t *testing.T) {
	var gotUA string
	srv := pageServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/short" {
			http.Redirect(w, r, "/event", http.StatusFound)
			return
		}
		gotUA = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<html><title>Hola</title></html>"))
	})
	p, err := NewFetcher(testPolicy).FetchPage(context.Background(), srv.URL+"/short#tickets")
	if err != nil {
		t.Fatal(err)
	}
	if p.FinalURL.Path != "/event" || !bytes.Contains(p.HTML, []byte("Hola")) {
		t.Errorf("page = %s %s", p.FinalURL, p.HTML)
	}
	if gotUA != UserAgent {
		t.Errorf("user agent = %q", gotUA)
	}
}

func TestFetchPage_DecodesTheDeclaredCharset(t *testing.T) {
	body, err := charmap.Windows1251.NewEncoder().String("<html><head><meta charset=\"windows-1251\"></head><body><p>Спектакль</p></body></html>")
	if err != nil {
		t.Fatal(err)
	}
	srv := pageServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(body))
	})
	p, err := NewFetcher(testPolicy).FetchPage(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(p.HTML), "Спектакль") {
		t.Errorf("not decoded: %q", p.HTML)
	}
}

func TestFetchPage_Refusals(t *testing.T) {
	srv := pageServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"a":1}`))
		case "/big":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write(bytes.Repeat([]byte("a"), MaxPageBytes+10))
		case "/gone":
			http.Error(w, "no", http.StatusNotFound)
		case "/to-ticketmaster":
			http.Redirect(w, r, "https://www.ticketmaster.es/event/123", http.StatusFound)
		case "/loop":
			http.Redirect(w, r, "/loop", http.StatusFound)
		}
	})
	f := NewFetcher(testPolicy)
	ctx := context.Background()
	cases := []struct {
		url  string
		want error
	}{
		{"ftp://example.org/x", ErrInvalidURL},
		{"not a url", ErrInvalidURL},
		{"https://user:pw@example.org/", ErrInvalidURL},
		{"https://www.eventbrite.com/e/123", ErrTicketingPlatform},
		{srv.URL + "/json", ErrNotHTML},
		{srv.URL + "/big", ErrTooLarge},
		{srv.URL + "/to-ticketmaster", ErrTicketingPlatform},
		{srv.URL + "/loop", ErrUnreachable},
	}
	for _, c := range cases {
		if _, err := f.FetchPage(ctx, c.url); !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", c.url, err, c.want)
		}
	}
	var up *UpstreamError
	if _, err := f.FetchPage(ctx, srv.URL+"/gone"); !errors.As(err, &up) || up.Status != http.StatusNotFound {
		t.Errorf("404: err = %v", err)
	}
}

// TestFetchPage_PrivateDestinationRefused proves the egress guard: with the
// production rule (no private addresses) a page on 127.0.0.1, localhost and
// the cloud metadata address are refused without reading anything. A
// redirect hop is dialed through the same guard (egress's own tests), and
// the page client keeps the policy's redirect check (the ticketing test).
func TestFetchPage_PrivateDestinationRefused(t *testing.T) {
	hit := false
	srv := pageServer(t, func(w http.ResponseWriter, _ *http.Request) {
		hit = true
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html>secret</html>"))
	})
	strict := egress.Policy{AllowHTTP: true, MaxRedirects: 5, Private: egress.PrivateDeny}
	f := NewFetcher(strict)
	for _, raw := range []string{srv.URL, "http://localhost:1/", "http://169.254.169.254/latest/meta-data/"} {
		if _, err := f.FetchPage(context.Background(), raw); !errors.Is(err, ErrNotAllowed) {
			t.Errorf("%s: err = %v, want ErrNotAllowed", raw, err)
		}
	}
	if hit {
		t.Error("the private server must never be reached")
	}
}

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestFetchImage(t *testing.T) {
	pic := pngBytes(t, 1000, 1250)
	srv := pageServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/poster":
			// A wrong header: the bytes decide.
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(pic)
		case "/svg":
			w.Header().Set("Content-Type", "image/svg+xml")
			_, _ = w.Write([]byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`))
		}
	})
	f := NewFetcher(testPolicy)
	got, err := f.FetchImage(context.Background(), srv.URL+"/poster")
	if err != nil {
		t.Fatal(err)
	}
	if got.ContentType != "image/png" || got.Width != 1000 || got.Height != 1250 {
		t.Errorf("picture = %s %dx%d", got.ContentType, got.Width, got.Height)
	}
	if _, err := f.FetchImage(context.Background(), srv.URL+"/svg"); !errors.Is(err, ErrNotImage) {
		t.Errorf("svg: err = %v", err)
	}
	strict := NewFetcher(egress.Policy{AllowHTTP: true, MaxRedirects: 5, Private: egress.PrivateDeny})
	if _, err := strict.FetchImage(context.Background(), srv.URL+"/poster"); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("private picture: err = %v", err)
	}
}
