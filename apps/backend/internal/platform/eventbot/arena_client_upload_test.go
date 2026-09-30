package eventbot

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

// The poster upload names its type: CreateFormFile labels the part
// application/octet-stream, and a poster stored under that type was
// skipped by the import's side-load until 2026-09-30.
func TestArenaClient_UploadPoster_SendsTheContentType(t *testing.T) {
	var gotType, gotOwner, gotName string
	var gotBytes []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/media" || r.Method != http.MethodPost {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		_ = r.ParseMultipartForm(1 << 20)
		gotType = r.FormValue("content_type")
		gotOwner = r.FormValue("owner_type")
		f, hdr, err := r.FormFile("file")
		if err != nil {
			t.Errorf("file part: %v", err)
		} else {
			gotName = hdr.Filename
			gotBytes, _ = io.ReadAll(f)
			_ = f.Close()
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		// The real envelope of POST /v1/media: the object sits under media_object.
		_, _ = w.Write([]byte(`{"media_object":{"id":"01a0ee39-f1f3-718e-9dc3-48d6a8d341d1","content_type":"image/jpeg","width":1080,"height":1350}}`))
	}))
	defer srv.Close()

	c := NewArenaClient(srv.URL, "", srv.Client())
	up, err := c.UploadPoster(context.Background(), "jwt", uuid.New(), "poster.jpg", "image/jpeg", []byte("\xff\xd8\xff"))
	if err != nil {
		t.Fatalf("UploadPoster: %v", err)
	}
	if up.ID != "01a0ee39-f1f3-718e-9dc3-48d6a8d341d1" || up.Width == nil || *up.Width != 1080 {
		t.Fatalf("decoded = %+v", up)
	}
	if gotType != "image/jpeg" || gotOwner != "event_poster" || gotName != "poster.jpg" || string(gotBytes) != "\xff\xd8\xff" {
		t.Fatalf("form = type %q owner %q name %q bytes %q", gotType, gotOwner, gotName, gotBytes)
	}
}

// The signed URL sits under media_object too; read from the top level it
// was always empty, and the bundle went out without a poster.
func TestArenaClient_MediaSignedURL_ReadsTheEnvelope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/media/01a0ee39-f1f3-718e-9dc3-48d6a8d341d1" || r.Method != http.MethodGet {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"media_object":{"id":"01a0ee39-f1f3-718e-9dc3-48d6a8d341d1","signed_url":"https://api.test/v1/media-files/01a0ee39-f1f3-718e-9dc3-48d6a8d341d1?expires=1&sig=x","signed_url_ttl_seconds":420}}`))
	}))
	defer srv.Close()

	c := NewArenaClient(srv.URL, "", srv.Client())
	u, err := c.MediaSignedURL(context.Background(), "jwt", "01a0ee39-f1f3-718e-9dc3-48d6a8d341d1")
	if err != nil {
		t.Fatalf("MediaSignedURL: %v", err)
	}
	if u != "https://api.test/v1/media-files/01a0ee39-f1f3-718e-9dc3-48d6a8d341d1?expires=1&sig=x" {
		t.Fatalf("signed url = %q", u)
	}
}
