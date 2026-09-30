//go:build integration

package hmedia

// An upload that names no type — Go's multipart CreateFormFile labels the
// part application/octet-stream, which is what the Telegram bot sent until
// 2026-09-30 — is still a PNG: the stored type is sniffed from the bytes,
// so the import's poster side-load and the signed download see an image.
// A type the client DOES name is kept as sent.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/storage"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/mediastore"
)

func TestCreateMedia_SniffsTheTypeOfAnUntypedUpload(t *testing.T) {
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
	repo, err := mediastore.New(mediastore.Options{Pool: pool, Storage: st})
	if err != nil {
		t.Fatal(err)
	}
	org, err := gen.New(pool).InsertOrganization(ctx, "Media sniff "+uuid.NewString(), "media-sniff-"+uuid.NewString(), "CZ", "en", 1200)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_objects WHERE org_id = $1`, org.ID)
		_, _ = pool.Exec(ctx, `DELETE FROM organizations WHERE id = $1`, org.ID)
	}()
	h := New(repo, slog.New(slog.NewJSONHandler(io.Discard, nil)))

	upload := func(named string) string {
		t.Helper()
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		_ = mw.WriteField("owner_type", "event_poster")
		_ = mw.WriteField("org_id", org.ID.String())
		if named != "" {
			_ = mw.WriteField("content_type", named)
		}
		part, _ := mw.CreateFormFile("file", "poster.bin")
		_, _ = part.Write([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR" + uuid.NewString()))
		_ = mw.Close()
		req := httptest.NewRequest(http.MethodPost, "/v1/media", &buf)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		rec := httptest.NewRecorder()
		h.CreateMedia(rec, req)
		if rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
			t.Fatalf("upload (named %q): %d %s", named, rec.Code, rec.Body.String())
		}
		var out struct {
			MediaObject struct {
				ContentType string `json:"content_type"`
			} `json:"media_object"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return out.MediaObject.ContentType
	}
	if got := upload(""); got != "image/png" {
		t.Fatalf("untyped upload stored as %q, want image/png", got)
	}
	if got := upload("image/jpeg"); got != "image/jpeg" {
		t.Fatalf("named upload stored as %q, want image/jpeg", got)
	}
}
