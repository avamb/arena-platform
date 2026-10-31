//go:build integration

package hmedia

// GET /v1/media-files/{id}?w=<width>: a signed URL with a width answers 200
// with a smaller picture of the same format. The signature covers the id and
// the expiry only, so the width is appended to a URL that was signed without
// it. A width that is not smaller than the original, or no width, serves the
// original bytes.

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
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

func TestDownloadMedia_WidthParameterResizesAndKeepsTheSignature(t *testing.T) {
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
	repo, err := mediastore.New(mediastore.Options{Pool: pool, Storage: st, SigningSecret: []byte("variant-test-secret")})
	if err != nil {
		t.Fatal(err)
	}
	org, err := gen.New(pool).InsertOrganization(ctx, "Media variant "+uuid.NewString(), "media-variant-"+uuid.NewString(), "CZ", "en", 1200)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_objects WHERE org_id = $1`, org.ID)
		_, _ = pool.Exec(ctx, `DELETE FROM organizations WHERE id = $1`, org.ID)
	}()
	h := New(repo, slog.New(slog.NewJSONHandler(io.Discard, nil))).
		WithRowOrgAccess(func(http.ResponseWriter, *http.Request, uuid.UUID, bool, string, string) bool { return true })

	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, image.NewRGBA(image.Rect(0, 0, 1000, 500))); err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("owner_type", "event_poster")
	_ = mw.WriteField("org_id", org.ID.String())
	_ = mw.WriteField("content_type", "image/png")
	part, _ := mw.CreateFormFile("file", "p.png")
	_, _ = part.Write(pngBuf.Bytes())
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/v1/media", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	h.CreateMedia(rec, req)
	if rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body.String())
	}
	var up struct {
		MediaObject struct {
			ID string `json:"id"`
		} `json:"media_object"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &up); err != nil {
		t.Fatal(err)
	}
	id := uuid.MustParse(up.MediaObject.ID)

	signed, err := repo.SignedDownloadURL(ctx, id, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(signed)
	if err != nil {
		t.Fatal(err)
	}

	router := chi.NewRouter()
	router.Get("/v1/media-files/{id}", h.DownloadMedia)
	get := func(extra string) *httptest.ResponseRecorder {
		t.Helper()
		target := "/v1/media-files/" + id.String() + "?" + u.RawQuery + extra
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, target, nil))
		return w
	}
	widthOf := func(w *httptest.ResponseRecorder) int {
		t.Helper()
		cfg, format, err := image.DecodeConfig(bytes.NewReader(w.Body.Bytes()))
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		if format != "png" {
			t.Fatalf("format %s, want png", format)
		}
		return cfg.Width
	}

	if w := get(""); w.Code != http.StatusOK || widthOf(w) != 1000 {
		t.Fatalf("original: %d", w.Code)
	}
	// 111 rounds up to 165; the signature was made without a width.
	w := get("&w=111")
	if w.Code != http.StatusOK || widthOf(w) != 165 {
		t.Fatalf("w=111: status %d width %d, want 200 and 165", w.Code, widthOf(w))
	}
	if got := w.Header().Get("Content-Type"); got != "image/png" {
		t.Fatalf("variant content type %q", got)
	}
	if w := get("&w=720"); w.Code != http.StatusOK || widthOf(w) != 720 {
		t.Fatalf("w=720: %d", w.Code)
	}
	// 1200 is above the 1000 px original: the original comes back.
	if w := get("&w=1200"); w.Code != http.StatusOK || widthOf(w) != 1000 {
		t.Fatalf("w=1200: %d", w.Code)
	}
	if w := get("&w=99999"); w.Code != http.StatusOK || widthOf(w) != 1000 {
		t.Fatalf("w=99999: %d", w.Code)
	}
	if w := get("&w=abc"); w.Code != http.StatusBadRequest {
		t.Fatalf("w=abc: %d, want 400", w.Code)
	}
	// A bad signature stays refused whatever the width.
	bad := httptest.NewRecorder()
	router.ServeHTTP(bad, httptest.NewRequest(http.MethodGet, "/v1/media-files/"+id.String()+"?expires=9999999999&sig=00&w=165", nil))
	if bad.Code != http.StatusUnauthorized {
		t.Fatalf("bad signature: %d, want 401", bad.Code)
	}
}
