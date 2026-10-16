package main

// Fixture of the web twin test of task 6-1a (the event wizard, sections
// "Event" and "Venue", apps/event-center/tests/smoke/live/wizard-event-venue.spec.ts).
//
// The main fixture already has a venue ("E2E Hall", Europe/Madrid, no country,
// no seat count). The wizard also lists promoters, so the organization gets
// one with fixed ids, and the hall gets a seat count so the wizard's "facts"
// line has a number to show. Both are additive and idempotent.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

const (
	wizardPromoterID   = "fe0000e2-0000-7000-8000-000000000e61"
	wizardPromoterName = "E2E Wizard Promoter"
	wizardPromoterSlug = "e2e-wizard-promoter"
)

func applyWizardFixture(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO org_promoters (id, org_id, name, slug)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (id) DO NOTHING`, wizardPromoterID, orgID, wizardPromoterName, wizardPromoterSlug); err != nil {
		return fmt.Errorf("wizard fixture promoter: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE venues SET capacity_default = 300 WHERE id = $1 AND capacity_default IS NULL`, venueID); err != nil {
		return fmt.Errorf("wizard fixture venue capacity: %w", err)
	}
	return nil
}
