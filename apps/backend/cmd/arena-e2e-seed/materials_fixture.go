package main

// Fixture of the sales page materials twin test
// (apps/event-center/tests/smoke/live/materials.spec.ts): the published
// fixture event gets a slug, a hosted-page sales channel, a feed token and a
// publication, so the spec can read the PUBLIC page and feed answers after it
// saved materials through the web screen. Constants are mirrored in the spec.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

const (
	materialsChannelID  = "fe0000e2-0000-7000-8000-0000000000e1"
	materialsFeedID     = "fe0000e2-0000-7000-8000-0000000000e2"
	materialsFeedToken  = "e2e-materials-feed-token" // #nosec G101 -- Public synthetic token of the local E2E fixture, not a production credential.
	materialsEventSlug  = "e2e-materials-event"
	materialsChannelNam = "E2E Materials Page"
)

// applyMaterialsFixture is idempotent.
func applyMaterialsFixture(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `UPDATE events SET slug = $2 WHERE id = $1 AND slug IS NULL`, eventID, materialsEventSlug); err != nil {
		return fmt.Errorf("materials fixture event slug: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO sales_channels (id, org_id, name, payment_mode, provider, fee_percent, settings)
		VALUES ($1, $2, $3, 'merchant_of_record', 'stripe', '5.00', '{"hosted_page": {"enabled": true}}'::jsonb)
		ON CONFLICT (id) DO NOTHING`, materialsChannelID, orgID, materialsChannelNam); err != nil {
		return fmt.Errorf("materials fixture channel: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO agent_feed_tokens (id, token, sales_channel_id, label)
		VALUES ($1, $2, $3, 'materials twin test')
		ON CONFLICT (id) DO NOTHING`, materialsFeedID, materialsFeedToken, materialsChannelID); err != nil {
		return fmt.Errorf("materials fixture feed token: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO event_publications (event_id, feed_token_id)
		VALUES ($1, $2)
		ON CONFLICT (event_id, feed_token_id) DO NOTHING`, eventID, materialsFeedID); err != nil {
		return fmt.Errorf("materials fixture publication: %w", err)
	}
	return nil
}
