//go:build integration

package mediastore

// Insert with PrerenderVariants queues a media.render_variants job, and the
// handler turns that job into stored copies. Needs a migrated database.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/storage"
)

func TestInsert_QueuesPrerenderJobAndHandlerRendersIt(t *testing.T) {
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
	repo, err := New(Options{Pool: pool, Storage: st, SigningSecret: []byte("prerender-test"), PrerenderVariants: true})
	if err != nil {
		t.Fatal(err)
	}
	org, err := gen.New(pool).InsertOrganization(ctx, "Prerender "+uuid.NewString(), "prerender-"+uuid.NewString(), "CZ", "en", 1200)
	if err != nil {
		t.Fatal(err)
	}
	var mediaID uuid.UUID
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM worker_jobs WHERE job_type = $1 AND payload->>'media_id' = $2`, RenderVariantsJobType, mediaID.String())
		_, _ = pool.Exec(ctx, `DELETE FROM media_objects WHERE org_id = $1`, org.ID)
		_, _ = pool.Exec(ctx, `DELETE FROM organizations WHERE id = $1`, org.ID)
	}()

	raw := encodeJPEG(t, 1000, 600)
	key, err := NewStorageKey("shop_product")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Put(ctx, storage.PutInput{Key: key, ContentType: "image/jpeg", Body: bytes.NewReader(raw)}); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	obj, err := repo.Insert(ctx, InsertInput{
		OrgID: &org.ID, OwnerType: "shop_product", StorageBackend: repo.Backend(), StorageKey: key,
		ContentType: "image/jpeg", ByteSize: int64(len(raw)), ChecksumSHA256: hex.EncodeToString(sum[:]),
	})
	if err != nil {
		t.Fatal(err)
	}
	mediaID = obj.ID

	var payload []byte
	err = pool.QueryRow(ctx, `SELECT payload FROM worker_jobs WHERE job_type = $1 AND payload->>'media_id' = $2`,
		RenderVariantsJobType, obj.ID.String()).Scan(&payload)
	if err != nil {
		t.Fatalf("no render job was queued: %v", err)
	}
	if err := NewRenderVariantsHandler(RenderVariantsHandlerOptions{Repo: repo})(ctx, payload); err != nil {
		t.Fatal(err)
	}
	for _, w := range []int{165, 240, 360, 480, 720} {
		if _, err := st.Stat(ctx, VariantKey(obj.ID, w, "jpg")); err != nil {
			t.Errorf("width %d not rendered: %v", w, err)
		}
	}
}
