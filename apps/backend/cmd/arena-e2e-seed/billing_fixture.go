package main

// Fixture of the web twin test of task B2-7 (the owner's "Billing → Usage",
// apps/event-center/tests/smoke/live/billing.spec.ts).
//
// Adds a second event to the fixture organization and a fixed set of billing
// facts (internal/platform/billing/e2eseed): in the CURRENT UTC month three
// sales of the main event (one of them reversed) and one sale of the second
// event, and one sale of the main event the day before the month. Every sale
// costs 0.59 EUR. The call is idempotent; the spec only reads, so there is no
// reset. Kept apart from main.go so the parallel tasks that extend the seed do
// not collide with it.

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing/e2eseed"
)

const (
	billingEventBID   = "fe0000e2-0000-7000-8000-0000000000b2"
	billingEventBName = "E2E Billing Second Show"
)

func applyBillingFixture(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO events (id, org_id, name, status, visibility)
		VALUES ($1, $2, $3, 'published', 'public')
		ON CONFLICT (id) DO NOTHING`, billingEventBID, orgID, billingEventBName); err != nil {
		return fmt.Errorf("billing fixture event: %w", err)
	}
	now := time.Now().UTC()
	err := e2eseed.SeedUsage(ctx, tx, e2eseed.Params{
		OrgID:      uuid.MustParse(orgID),
		EventA:     uuid.MustParse(eventID),
		EventB:     uuid.MustParse(billingEventBID),
		Session:    uuid.MustParse(sessionID),
		MonthStart: time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC),
		Country:    "CZ",
	})
	if err != nil {
		return fmt.Errorf("billing fixture: %w", err)
	}
	return nil
}
