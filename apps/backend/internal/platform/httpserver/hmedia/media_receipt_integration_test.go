//go:build integration

package hmedia

// An archived payment provider receipt (owner type payment_receipt, spec 51
// section 2) is dispute evidence: the organization cannot delete it, and the
// copy, which may be the provider's HTML page, is served as a download that
// never runs on the API's origin.

import (
	"bytes"
	"context"
	"io"
	"log/slog"
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

func TestPaymentReceiptMedia_IsNotDeletableAndDownloadsAsAnAttachment(t *testing.T) {
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
	repo, err := mediastore.New(mediastore.Options{Pool: pool, Storage: st, SigningSecret: []byte("receipt-test-secret")})
	if err != nil {
		t.Fatal(err)
	}
	org, err := gen.New(pool).InsertOrganization(ctx, "Media receipt "+uuid.NewString(), "media-receipt-"+uuid.NewString(), "CZ", "en", 1200)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_objects WHERE org_id = $1`, org.ID)
		_, _ = pool.Exec(ctx, `DELETE FROM organizations WHERE id = $1`, org.ID)
	}()

	store := func(ownerType, contentType string, body []byte) uuid.UUID {
		t.Helper()
		key, err := mediastore.NewStorageKey(ownerType)
		if err != nil {
			t.Fatal(err)
		}
		sum, size, err := repo.PutAndStream(ctx, key, contentType, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		owner := uuid.New()
		obj, err := repo.Insert(ctx, mediastore.InsertInput{
			OrgID: &org.ID, OwnerType: ownerType, OwnerID: &owner,
			StorageBackend: repo.Backend(), StorageKey: key,
			ContentType: contentType, ByteSize: size, ChecksumSHA256: sum,
		})
		if err != nil {
			t.Fatal(err)
		}
		return obj.ID
	}
	receipt := store(mediastore.OwnerPaymentReceipt, "text/html; charset=utf-8", []byte("<html><script>alert(1)</script>receipt</html>"))
	poster := store("event_poster", "image/png", []byte("\x89PNG\r\n\x1a\n"))

	h := New(repo, slog.New(slog.NewJSONHandler(io.Discard, nil))).
		WithRowOrgAccess(func(http.ResponseWriter, *http.Request, uuid.UUID, bool, string, string) bool { return true })
	router := chi.NewRouter()
	router.Delete("/v1/media/{id}", h.DeleteMedia)
	router.Get("/v1/media-files/{id}", h.DownloadMedia)

	del := func(id uuid.UUID) int {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/v1/media/"+id.String(), nil))
		return w.Code
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

	if code := del(receipt); code != http.StatusConflict {
		t.Fatalf("delete of a receipt = %d; want 409", code)
	}
	var deleted *time.Time
	if err := pool.QueryRow(ctx, `SELECT deleted_at FROM media_objects WHERE id = $1`, receipt).Scan(&deleted); err != nil || deleted != nil {
		t.Fatalf("receipt deleted_at = %v (%v); want it kept", deleted, err)
	}

	w := download(receipt)
	if w.Code != http.StatusOK {
		t.Fatalf("receipt download = %d", w.Code)
	}
	for header, want := range map[string]string{
		"Content-Disposition":     "attachment",
		"Content-Security-Policy": "default-src 'none'; img-src data:; style-src 'unsafe-inline'; font-src data:; sandbox",
		"X-Content-Type-Options":  "nosniff",
	} {
		if got := w.Header().Get(header); got != want {
			t.Errorf("receipt %s = %q; want %q", header, got, want)
		}
	}

	// Other media keep their behaviour: served inline, deletable.
	if w := download(poster); w.Code != http.StatusOK || w.Header().Get("Content-Disposition") != "" {
		t.Fatalf("poster download = %d, disposition %q", w.Code, w.Header().Get("Content-Disposition"))
	}
	if code := del(poster); code != http.StatusNoContent {
		t.Fatalf("delete of a poster = %d; want 204", code)
	}
}
