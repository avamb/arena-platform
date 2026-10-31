//go:build integration

package hmedia

// The picture variant route answers with the CORS headers of the API on every
// branch: the first render of a width, a width already stored, and the
// original. The event center loads shop previews with fetch(), so a missing
// Access-Control-Allow-Origin on the render branch is a broken picture there.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"image"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	apihttp "github.com/abhteam/arena_new/apps/backend/internal/adapters/http"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/storage"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/mediastore"
)

func TestDownloadMedia_VariantAnswersCarryCORSOnEveryBranch(t *testing.T) {
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
	repo, err := mediastore.New(mediastore.Options{Pool: pool, Storage: st, SigningSecret: []byte("variant-cors-secret")})
	if err != nil {
		t.Fatal(err)
	}
	org, err := gen.New(pool).InsertOrganization(ctx, "Media cors "+uuid.NewString(), "media-cors-"+uuid.NewString(), "CZ", "en", 1200)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_objects WHERE org_id = $1`, org.ID)
		_, _ = pool.Exec(ctx, `DELETE FROM organizations WHERE id = $1`, org.ID)
	}()

	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, image.NewRGBA(image.Rect(0, 0, 1000, 500))); err != nil {
		t.Fatal(err)
	}
	key, err := mediastore.NewStorageKey("event_poster")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Put(ctx, storage.PutInput{Key: key, ContentType: "image/png", Body: bytes.NewReader(pngBuf.Bytes())}); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(pngBuf.Bytes())
	obj, err := repo.Insert(ctx, mediastore.InsertInput{
		OrgID: &org.ID, OwnerType: "event_poster", StorageBackend: repo.Backend(), StorageKey: key,
		ContentType: "image/png", ByteSize: int64(pngBuf.Len()), ChecksumSHA256: hex.EncodeToString(sum[:]),
	})
	if err != nil {
		t.Fatal(err)
	}
	signed, err := repo.SignedDownloadURL(ctx, obj.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(signed)
	if err != nil {
		t.Fatal(err)
	}

	const origin = "https://shop-staging.arenasoldout.com"
	router := apihttp.NewRouter(apihttp.Deps{
		Logger:             slog.New(slog.NewJSONHandler(io.Discard, nil)),
		CORSAllowedOrigins: []string{origin},
	})
	h := New(repo, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	router.Get("/v1/media-files/{id}", h.DownloadMedia)

	get := func(extra string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/v1/media-files/"+obj.ID.String()+"?"+u.RawQuery+extra, nil)
		req.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: status %d", extra, w.Code)
		}
		return w
	}
	check := func(label string, w *httptest.ResponseRecorder) {
		t.Helper()
		if got := w.Header().Get("Access-Control-Allow-Origin"); got != origin {
			t.Errorf("%s: Access-Control-Allow-Origin = %q, want %q", label, got, origin)
		}
		if got := w.Header().Values("Vary"); len(got) == 0 {
			t.Errorf("%s: no Vary header", label)
		}
	}
	first := get("&w=240") // renders the copy
	check("first render", first)
	if _, err := st.Stat(ctx, mediastore.VariantKey(obj.ID, 240, "png")); err != nil {
		t.Fatalf("the first request did not store the copy: %v", err)
	}
	check("stored copy", get("&w=240"))
	check("original", get(""))
}
