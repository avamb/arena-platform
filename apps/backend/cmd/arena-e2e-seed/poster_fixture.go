package main

// Fixture of the web twin test of task 7-1 (event posters,
// apps/event-center/tests/smoke/live/poster.spec.ts).
//
// ONE media object that belongs to the OTHER organization (foreignOrgID, made
// by applyForeignPromoter), so the twin test can prove that its id is refused
// as a poster of the main organization and cannot be read through the main
// organization's token. Only the metadata row exists: the test never needs
// its bytes. Additive and idempotent like the rest of the seed.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

const (
	foreignMediaID = "fe0000e2-0000-7000-8000-0000000000f3"
	// 64 hex characters, as the checksum column expects.
	foreignMediaChecksum = "0000000000000000000000000000000000000000000000000000000000000000"
)

func applyForeignPoster(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO media_objects (id, org_id, owner_type, storage_backend, storage_key, content_type, byte_size, checksum_sha256)
		VALUES ($1, $2, 'event_poster', 'local', 'e2e/foreign-poster.png', 'image/png', 1, $3)
		ON CONFLICT (id) DO NOTHING`, foreignMediaID, foreignOrgID, foreignMediaChecksum); err != nil {
		return fmt.Errorf("poster fixture foreign media: %w", err)
	}
	return nil
}
