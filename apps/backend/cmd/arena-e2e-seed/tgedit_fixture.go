package main

// Fixture of the twin test of the Telegram mini app editor (spec 44, RT-12b,
// apps/event-center/tests/smoke/live/tg-edit.spec.ts).
//
// The twin plays the bot: it asks POST /v1/bot/web-login-codes (with the
// harness's BOT_SERVICE_TOKEN) for a "miniapp" code in the name of a linked
// Telegram account, then opens the link in the browser. So each of the three
// people of the main fixture is linked to a fixed Telegram account of their
// own (person.TelegramUserID). Nothing else changes: the rows are new, and the
// link names the main organization as the person's current one.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// applyTgEditFixture is idempotent: a rerun leaves the links as they are.
func applyTgEditFixture(ctx context.Context, tx pgx.Tx) error {
	for _, p := range people() {
		if _, err := tx.Exec(ctx, `
			INSERT INTO bot_telegram_links (telegram_user_id, user_id, locale, current_org_id)
			VALUES ($1, $2, 'en', $3)
			ON CONFLICT (telegram_user_id) DO NOTHING`, p.TelegramUserID, p.UserID, orgID); err != nil {
			return fmt.Errorf("insert telegram link of %s: %w", p.Role, err)
		}
	}
	return nil
}
