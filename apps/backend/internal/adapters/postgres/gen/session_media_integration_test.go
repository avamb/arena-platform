//go:build integration

package gen_test

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// TestSessionMedia_AB47b_LiveDB is the AB-47b (feature #435) round-trip
// integration test required by the feature description ("plus an
// integration-tagged DB round-trip").
//
// The test exercises migration 0085 end-to-end:
//
//   - ListSessionMediaItems returns empty for a fresh session (no
//     pre-seeded rows).
//   - InsertSessionMediaItem writes both kinds correctly and
//     ListSessionMediaItems returns them in position order.
//   - The kind_payload CHECK constraint refuses inconsistent rows
//     (poster without media_id, video without video_url,
//     video with a media_id).
//   - The (session_id, position) UNIQUE constraint refuses duplicate
//     positions.
//   - DeleteSessionMediaItems wipes every row for the session.
//   - Deleting the parent session cascades (ON DELETE CASCADE).
//   - GetMediaObjectOwnerType returns the seeded owner_type and
//     pgx.ErrNoRows for a missing row.
//   - SessionExistsActive returns true for active, false for missing.
//   - Cross-transaction visibility works via WithTx.
func TestSessionMedia_AB47b_LiveDB(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping live DB integration test")
	}

	ctx := context.Background()
	pool, err := pgxpool.NewWithConfig(ctx, mustPoolConfig(t, dsn, 8))
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()

	f := createSessionMediaFixture(t, ctx, pool)
	defer f.cleanup()

	q := gen.New(pool)

	// Step 1: empty gallery for a fresh session.
	rows, err := q.ListSessionMediaItems(ctx, f.sessionID)
	if err != nil {
		t.Fatalf("ListSessionMediaItems empty: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("ListSessionMediaItems empty: got %d rows, want 0", len(rows))
	}

	// Step 2: SessionExistsActive returns true.
	exists, err := q.SessionExistsActive(ctx, f.sessionID)
	if err != nil {
		t.Fatalf("SessionExistsActive: %v", err)
	}
	if !exists {
		t.Fatalf("SessionExistsActive: got false, want true for active session")
	}
	// … and false for a missing session.
	missing, err := q.SessionExistsActive(ctx, uuid.New())
	if err != nil {
		t.Fatalf("SessionExistsActive missing: %v", err)
	}
	if missing {
		t.Fatalf("SessionExistsActive missing: got true, want false")
	}

	// Step 3: GetMediaObjectOwnerType round-trip (owner_type + org_id —
	// the org is part of the gallery tenant-isolation check).
	owner, mediaOrg, err := q.GetMediaObjectOwnerType(ctx, f.mediaID)
	if err != nil {
		t.Fatalf("GetMediaObjectOwnerType: %v", err)
	}
	if owner != "session_poster" {
		t.Fatalf("GetMediaObjectOwnerType: got %q, want session_poster", owner)
	}
	if mediaOrg == nil || *mediaOrg != f.orgID {
		t.Fatalf("GetMediaObjectOwnerType org: got %v, want %s", mediaOrg, f.orgID)
	}
	if _, _, err := q.GetMediaObjectOwnerType(ctx, uuid.New()); err == nil {
		t.Fatalf("GetMediaObjectOwnerType(missing): got nil, want pgx.ErrNoRows")
	}

	// Step 4: insert a mixed gallery (poster + video) and read back.
	videoURL := "https://www.youtube.com/watch?v=dQw4w9WgXcQ"
	posterRow, err := q.InsertSessionMediaItem(ctx, f.sessionID, "poster", &f.mediaID, nil, 0)
	if err != nil {
		t.Fatalf("InsertSessionMediaItem poster: %v", err)
	}
	if posterRow.Kind != "poster" || posterRow.MediaID == nil || *posterRow.MediaID != f.mediaID {
		t.Fatalf("InsertSessionMediaItem poster: bad row: %+v", posterRow)
	}
	videoRow, err := q.InsertSessionMediaItem(ctx, f.sessionID, "video", nil, &videoURL, 1)
	if err != nil {
		t.Fatalf("InsertSessionMediaItem video: %v", err)
	}
	if videoRow.Kind != "video" || videoRow.VideoURL == nil || *videoRow.VideoURL != videoURL {
		t.Fatalf("InsertSessionMediaItem video: bad row: %+v", videoRow)
	}

	rows, err = q.ListSessionMediaItems(ctx, f.sessionID)
	if err != nil {
		t.Fatalf("ListSessionMediaItems: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("ListSessionMediaItems: got %d rows, want 2", len(rows))
	}
	if rows[0].Position != 0 || rows[0].Kind != "poster" {
		t.Errorf("row 0: got kind=%q pos=%d, want poster/0", rows[0].Kind, rows[0].Position)
	}
	if rows[1].Position != 1 || rows[1].Kind != "video" {
		t.Errorf("row 1: got kind=%q pos=%d, want video/1", rows[1].Kind, rows[1].Position)
	}

	// Step 5: kind_payload CHECK — poster without media_id.
	if _, err := q.InsertSessionMediaItem(ctx, f.sessionID, "poster", nil, nil, 5); err == nil {
		t.Errorf("kind_payload CHECK: poster without media_id was accepted")
	}
	// Video without video_url.
	if _, err := q.InsertSessionMediaItem(ctx, f.sessionID, "video", nil, nil, 5); err == nil {
		t.Errorf("kind_payload CHECK: video without video_url was accepted")
	}
	// Video with media_id.
	if _, err := q.InsertSessionMediaItem(ctx, f.sessionID, "video", &f.mediaID, &videoURL, 5); err == nil {
		t.Errorf("kind_payload CHECK: video with media_id was accepted")
	}
	// Poster with video_url.
	if _, err := q.InsertSessionMediaItem(ctx, f.sessionID, "poster", &f.mediaID, &videoURL, 5); err == nil {
		t.Errorf("kind_payload CHECK: poster with video_url was accepted")
	}
	// kind not in ('poster','video').
	if _, err := q.InsertSessionMediaItem(ctx, f.sessionID, "audio", &f.mediaID, nil, 5); err == nil {
		t.Errorf("kind CHECK: 'audio' was accepted")
	}

	// Step 6: (session_id, position) UNIQUE — reuse of position 0.
	if _, err := q.InsertSessionMediaItem(ctx, f.sessionID, "poster", &f.mediaID, nil, 0); err == nil {
		t.Errorf("session_media_items_position_unique: duplicate position accepted")
	}

	// Step 7: WithTx atomic replace pattern (matches handler).
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	txq := q.WithTx(tx)
	if err := txq.DeleteSessionMediaItems(ctx, f.sessionID); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("DeleteSessionMediaItems (tx): %v", err)
	}
	if _, err := txq.InsertSessionMediaItem(ctx, f.sessionID, "poster", &f.mediaID, nil, 0); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("InsertSessionMediaItem after delete (tx): %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	rows, err = q.ListSessionMediaItems(ctx, f.sessionID)
	if err != nil {
		t.Fatalf("ListSessionMediaItems after atomic replace: %v", err)
	}
	if len(rows) != 1 || rows[0].Kind != "poster" || rows[0].Position != 0 {
		t.Fatalf("atomic replace: got %+v, want single poster at position 0", rows)
	}

	// Step 8: ON DELETE CASCADE — deleting the session drops gallery rows.
	if _, err := pool.Exec(ctx, `DELETE FROM sessions WHERE id = $1`, f.sessionID); err != nil {
		t.Fatalf("delete session: %v", err)
	}
	rows, err = q.ListSessionMediaItems(ctx, f.sessionID)
	if err != nil {
		t.Fatalf("ListSessionMediaItems after cascade: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("ON DELETE CASCADE: expected 0 rows after session delete, got %d", len(rows))
	}
	// After cascade the fixture cleanup should skip the sessions row.
	f.sessionDeleted = true
}

// sessionMediaFixture seeds the minimum row graph needed to insert
// session_media_items: organization → venue → event → session, plus one
// media_object of owner_type='session_poster'.
type sessionMediaFixture struct {
	t              *testing.T
	pool           *pgxpool.Pool
	orgID          uuid.UUID
	venueID        uuid.UUID
	eventID        uuid.UUID
	sessionID      uuid.UUID
	mediaID        uuid.UUID
	sessionDeleted bool
}

func createSessionMediaFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) *sessionMediaFixture {
	f := &sessionMediaFixture{
		t: t, pool: pool,
		orgID:     uuid.New(),
		venueID:   uuid.New(),
		eventID:   uuid.New(),
		sessionID: uuid.New(),
		mediaID:   uuid.New(),
	}
	suffix := f.orgID.String()[:8]
	steps := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`,
			[]any{f.orgID, "SM Org " + suffix, "sm-" + suffix}},
		{`INSERT INTO venues (id, org_id, name) VALUES ($1, $2, $3)`,
			[]any{f.venueID, f.orgID, "SM Venue " + suffix}},
		{`INSERT INTO events (id, org_id, name, status, visibility)
		  VALUES ($1, $2, $3, 'draft', 'private')`,
			[]any{f.eventID, f.orgID, "SM Event " + suffix}},
		{`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at,
		    capacity_total, status, admission_mode, currency, currency_source)
		  VALUES ($1, $2, $3, now() + interval '30 days',
		    now() + interval '30 days 3 hours', 100, 'draft',
		    'general_admission', 'EUR', 'override')`,
			[]any{f.sessionID, f.eventID, f.venueID}},
		{`INSERT INTO media_objects
		    (id, org_id, owner_type, owner_id,
		     storage_backend, storage_key,
		     content_type, byte_size, checksum_sha256)
		  VALUES ($1, $2, 'session_poster', $3,
		    'local', $4,
		    'image/png', 1024,
		    'e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855')`,
			[]any{f.mediaID, f.orgID, f.sessionID, "test/session_poster/" + suffix + ".png"}},
	}
	for i, s := range steps {
		if _, err := pool.Exec(ctx, s.sql, s.args...); err != nil {
			f.cleanup()
			t.Fatalf("session_media fixture step %d failed: %v", i, err)
		}
	}
	return f
}

func (f *sessionMediaFixture) cleanup() {
	ctx := context.Background()
	// session_media_items cascade when the session is dropped, so try to
	// drop the session first. If it's already gone (cascade test), skip.
	if !f.sessionDeleted {
		if _, err := f.pool.Exec(ctx, `DELETE FROM session_media_items WHERE session_id = $1`, f.sessionID); err != nil {
			f.t.Logf("cleanup session_media_items: %v", err)
		}
		if _, err := f.pool.Exec(ctx, `DELETE FROM sessions WHERE id = $1`, f.sessionID); err != nil {
			f.t.Logf("cleanup sessions: %v", err)
		}
	}
	for _, step := range []struct {
		sql string
		arg uuid.UUID
	}{
		{`DELETE FROM media_objects WHERE id = $1`, f.mediaID},
		{`DELETE FROM events WHERE id = $1`, f.eventID},
		{`DELETE FROM venues WHERE id = $1`, f.venueID},
		{`DELETE FROM organizations WHERE id = $1`, f.orgID},
	} {
		if _, err := f.pool.Exec(ctx, step.sql, step.arg); err != nil {
			f.t.Logf("cleanup: %v", err)
		}
	}
}
