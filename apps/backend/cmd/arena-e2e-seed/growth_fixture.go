package main

// Fixture of the web twin test of "Mailings and reminders" (spec 51-1,
// apps/event-center/tests/smoke/live/growth.spec.ts).
//
// The fixture organization gets two promo codes and two mailing subscribers:
//
//   - E2EGROWTH: live, one use per buyer, so it may be the tick's reward;
//   - E2EGROWTHMANY: live but with no per-buyer limit, so the screen never
//     offers it (the API would refuse it as a reward);
//   - a confirmed subscriber (Anna) and a pending one (Boris).
//
// No growth settings row is left behind, so the organization starts on the
// defaults: reminder on, tick off, no reward. The spec reads everything back
// through the API; the seed binary only puts it back:
//
//	--growth-op reset   deletes the growth settings and the subscribers of the
//	                    organization, and restores the two codes and two
//	                    subscribers.
//
// Kept apart from main.go so the parallel tasks that extend the seed do not
// collide with it.

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

const (
	growthRewardCodeID = "fe0000e2-0000-7000-8000-00000000e801"
	growthManyCodeID   = "fe0000e2-0000-7000-8000-00000000e802"
	growthAnnaSubID    = "fe0000e2-0000-7000-8000-00000000e811"
	growthBorisSubID   = "fe0000e2-0000-7000-8000-00000000e812"
	growthRewardCode   = "E2EGROWTH"
	growthManyCode     = "E2EGROWTHMANY"
	growthAnnaEmail    = "anna.growth@e2e.test"
	growthBorisEmail   = "boris.growth@e2e.test"
)

// applyGrowthFixture is idempotent AND resetting.
func applyGrowthFixture(ctx context.Context, tx pgx.Tx) error {
	run := func(what, sql string, args ...any) error {
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			return fmt.Errorf("growth fixture %s: %w", what, err)
		}
		return nil
	}
	for _, step := range []struct{ what, sql string }{
		{"settings", `DELETE FROM org_growth_settings WHERE org_id = $1`},
		{"consents", `DELETE FROM marketing_consents WHERE org_id = $1`},
		{"subscriptions", `DELETE FROM marketing_subscriptions WHERE org_id = $1`},
	} {
		if err := run(step.what, step.sql, orgID); err != nil {
			return err
		}
	}
	for _, c := range []struct {
		id, code string
		perBuyer any
	}{
		{growthRewardCodeID, growthRewardCode, 1},
		{growthManyCodeID, growthManyCode, nil},
	} {
		if err := run("code "+c.code, `
			INSERT INTO promo_codes (id, org_id, code, discount_type, discount_value, max_uses_per_customer, status)
			VALUES ($1, $2, $3, 'percent', 10, $4, 'active')
			ON CONFLICT (id) DO UPDATE
			SET max_uses_per_customer = EXCLUDED.max_uses_per_customer, status = 'active', deleted_at = NULL`,
			c.id, orgID, c.code, c.perBuyer); err != nil {
			return err
		}
	}
	granted := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Second)
	for _, s := range []struct {
		id, email, name, status, token string
		confirmed                      any
	}{
		{growthAnnaSubID, growthAnnaEmail, "Anna Growth", "confirmed", "e2e-growth-anna", granted.Add(time.Hour)},
		{growthBorisSubID, growthBorisEmail, "Boris Growth", "pending", "e2e-growth-boris", nil},
	} {
		if err := run("subscriber "+s.email, `
			INSERT INTO marketing_subscriptions
				(id, org_id, email, email_normalized, buyer_name, locale, status, text_version, source,
				 granted_at, confirmed_at, unsubscribe_token)
			VALUES ($1, $2, $3, $3, $4, 'en', $5, 'e2e', 'widget', $6, $7, $8)`,
			s.id, orgID, s.email, s.name, s.status, granted, s.confirmed, s.token); err != nil {
			return err
		}
	}
	return nil
}

// runGrowthOp serves --growth-op. It prints one JSON line.
func runGrowthOp(op string) error {
	if op != "reset" {
		return fmt.Errorf("unknown --growth-op %q (reset)", op)
	}
	cfg, err := config.LoadFor(config.RoleMigrate)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, cfg.DBDSN())
	if err != nil {
		return fmt.Errorf("open pgx pool: %w", err)
	}
	defer pool.Close()
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := applyGrowthFixture(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return printOrgDataJSON(map[string]string{"ok": "reset"})
}
