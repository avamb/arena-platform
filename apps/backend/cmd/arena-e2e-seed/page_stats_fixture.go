package main

// Fixture of the web twin test of task 2-3 (the page statistics block of the
// event screen, apps/event-center/tests/smoke/live/page-stats.spec.ts).
//
// Counter rows of the main fixture's event for the two days before today, with
// fixed numbers the spec asserts: per day page_view 2 people + 1 probable bot
// with no utm and 4 with instagram/spring, video_play 2, partner_click 1
// person + 1 bot. Over two days: page views 14 (2 bots), video plays 4,
// partner clicks 4 (2 bots), and the source "instagram / spring" with 8.
//
// Idempotent: a rerun sets the same numbers instead of adding to them. Kept
// apart from main.go so the parallel tasks that extend the seed do not collide.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

func applyPageStatsFixture(ctx context.Context, tx pgx.Tx) error {
	rows := []struct {
		kind, source, campaign string
		hits, suspect          int64
	}{
		{"page_view", "", "", 2, 1},
		{"page_view", "instagram", "spring", 4, 0},
		{"video_play", "", "", 2, 0},
		{"partner_click", "", "", 1, 1},
	}
	for back := 1; back <= 2; back++ {
		for _, r := range rows {
			if _, err := tx.Exec(ctx, `
				INSERT INTO event_page_stats (event_id, org_id, day, kind, utm_source, utm_campaign, hits, suspect_hits)
				VALUES ($1, $2, (now() AT TIME ZONE 'UTC')::date - $3::int, $4, $5, $6, $7, $8)
				ON CONFLICT (event_id, day, kind, utm_source, utm_campaign)
				DO UPDATE SET hits = EXCLUDED.hits, suspect_hits = EXCLUDED.suspect_hits`,
				eventID, orgID, back, r.kind, r.source, r.campaign, r.hits, r.suspect); err != nil {
				return fmt.Errorf("page stats fixture: %w", err)
			}
		}
	}
	return nil
}
