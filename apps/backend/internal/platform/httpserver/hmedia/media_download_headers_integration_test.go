//go:build integration

package hmedia

// Every signed download is sandboxed and not sniffed; only an inline-safe
// picture is served without Content-Disposition: attachment.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/storage"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/mediastore"
)

func TestDownloadMedia_HardeningHeaders(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("cannot connect to PostgreSQL (%v); skipping", err)
	}
	defer pool.Close()
	st, err := storage.NewLocalStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo, err := mediastore.New(mediastore.Options{Pool: pool, Storage: st, SigningSecret: []byte("headers-test-secret")})
	if err != nil {
		t.Fatal(err)
	}
	org, err := gen.New(pool).InsertOrganization(ctx, "Media headers "+uuid.NewString(), "media-headers-"+uuid.NewString(), "CZ", "en", 1200)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_objects WHERE org_id = $1`, org.ID)
		_, _ = pool.Exec(ctx, `DELETE FROM organizations WHERE id = $1`, org.ID)
	}()
	h := New(repo, slog.New(slog.NewJSONHandler(io.Discard, nil))).
		WithRowOrgAccess(func(http.ResponseWriter, *http.Request, uuid.UUID, bool, string, string) bool { return true })
	router := chi.NewRouter()
	router.Get("/v1/media-files/{id}", h.DownloadMedia)

	upload := func(filename, contentType string, body []byte) uuid.UUID {
		t.Helper()
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		_ = mw.WriteField("owner_type", "event_poster")
		_ = mw.WriteField("org_id", org.ID.String())
		_ = mw.WriteField("content_type", contentType)
		part, _ := mw.CreateFormFile("file", filename)
		_, _ = part.Write(body)
		_ = mw.Close()
		req := httptest.NewRequest(http.MethodPost, "/v1/media", &buf)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		rec := httptest.NewRecorder()
		h.CreateMedia(rec, req)
		if rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
			t.Fatalf("upload %s: %d %s", filename, rec.Code, rec.Body.String())
		}
		var out struct {
			MediaObject struct {
				ID uuid.UUID `json:"id"`
			} `json:"media_object"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return out.MediaObject.ID
	}
	download := func(id uuid.UUID) *httptest.ResponseRecorder {
		t.Helper()
		signed, err := repo.SignedDownloadURL(ctx, id, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		u, err := url.Parse(signed)
		if err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/media-files/"+id.String()+"?"+u.RawQuery, nil))
		return w
	}

	const csp = "default-src 'none'; img-src data:; style-src 'unsafe-inline'; font-src data:; sandbox"
	cases := []struct {
		name       string
		file, typ  string
		body       []byte
		attachment bool
	}{
		{"svg", "logo.svg", "image/svg+xml", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`), false},
		{"html", "page.html", "text/html", []byte("<html><script>alert(1)</script></html>"), true},
		{"png", "poster.png", "image/png", []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR" + uuid.NewString()), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := download(upload(c.file, c.typ, c.body))
			if w.Code != http.StatusOK {
				t.Fatalf("download = %d", w.Code)
			}
			if got := w.Header().Get("Content-Security-Policy"); got != csp {
				t.Errorf("CSP = %q", got)
			}
			if got := w.Header().Get("X-Content-Type-Options"); got != "nosniff" {
				t.Errorf("nosniff = %q", got)
			}
			if got := w.Header().Get("Content-Disposition") == "attachment"; got != c.attachment {
				t.Errorf("attachment = %v; want %v", got, c.attachment)
			}
		})
	}
}
