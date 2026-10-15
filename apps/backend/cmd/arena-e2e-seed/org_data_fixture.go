package main

// Fixture of the web twin test of task 4-7 (the owner's "Organization data",
// apps/event-center/tests/smoke/live/org-data.spec.ts).
//
// The fixture organization starts with NO legal data and NO contact, and gets
// one hosted-page sales channel of its own with no public contact. Two small
// operations the spec calls through the seed binary, because the test process
// has no SQL driver:
//
//	--org-data-op reset    puts the organization's legal data, contact and the
//	                       contact-form switch back to "nothing filled in" and
//	                       the channel's settings back to a bare hosted page,
//	                       so a retried or repeated run starts clean;
//	--org-data-op inspect  prints what the database holds now (legal fields,
//	                       the channel's public contact, the Telegram switch);
//	--org-data-op hide     sets the platform operator's seller_hidden on the
//	                       channel, the way the operator would (the owner
//	                       cannot), so the spec can see the screen show it.
//
// Kept apart from main.go so the parallel tasks that extend the seed do not
// collide with it.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	orgDataChannelID   = "fe0000e2-0000-7000-8000-0000000000d0"
	orgDataChannelName = "E2E Org Data Page"
)

// applyOrgDataFixture is idempotent AND resetting.
func applyOrgDataFixture(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO sales_channels (id, org_id, name, settings)
		VALUES ($1, $2, $3, '{"hosted_page": {"enabled": true}}'::jsonb)
		ON CONFLICT (id) DO UPDATE SET settings = '{"hosted_page": {"enabled": true}}'::jsonb`,
		orgDataChannelID, orgID, orgDataChannelName); err != nil {
		return fmt.Errorf("org-data fixture channel: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE organizations SET
			legal_name = NULL, legal_form = NULL, registration_number = NULL,
			tax_id = NULL, tax_id_scheme = NULL,
			legal_address_line1 = NULL, legal_address_line2 = NULL, legal_address_city = NULL,
			legal_address_postal_code = NULL, legal_address_country = NULL,
			contact_email = NULL, contact_phone = NULL,
			contact_form_to_telegram = true
		WHERE id = $1`, orgID); err != nil {
		return fmt.Errorf("org-data fixture organization: %w", err)
	}
	return nil
}

// runOrgDataOp serves --org-data-op. It prints one JSON line.
func runOrgDataOp(op string) error {
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

	switch op {
	case "reset":
		tx, err := pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if err := applyOrgDataFixture(ctx, tx); err != nil {
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		return printOrgDataJSON(map[string]string{"ok": "reset"})
	case "hide":
		if _, err := pool.Exec(ctx, `
			UPDATE sales_channels
			SET settings = jsonb_set(settings, '{hosted_page,seller_hidden}', 'true'::jsonb)
			WHERE id = $1`, orgDataChannelID); err != nil {
			return fmt.Errorf("hide seller block: %w", err)
		}
		return printOrgDataJSON(map[string]string{"ok": "hidden"})
	case "inspect":
		var legalName, legalForm, taxID, taxScheme, city, contactEmail, publicEmail, publicPhone *string
		var toTelegram bool
		if err := pool.QueryRow(ctx, `
			SELECT legal_name, legal_form, tax_id, tax_id_scheme, legal_address_city, contact_email, contact_form_to_telegram
			FROM organizations WHERE id = $1`, orgID).
			Scan(&legalName, &legalForm, &taxID, &taxScheme, &city, &contactEmail, &toTelegram); err != nil {
			return fmt.Errorf("inspect organization: %w", err)
		}
		if err := pool.QueryRow(ctx, `
			SELECT settings #>> '{hosted_page,public_contact,email}', settings #>> '{hosted_page,public_contact,phone}'
			FROM sales_channels WHERE id = $1`, orgDataChannelID).Scan(&publicEmail, &publicPhone); err != nil {
			return fmt.Errorf("inspect channel: %w", err)
		}
		return printOrgDataJSON(map[string]any{
			"legal_name": legalName, "legal_form": legalForm, "tax_id": taxID, "tax_id_scheme": taxScheme,
			"legal_address_city": city, "contact_email": contactEmail, "contact_form_to_telegram": toTelegram,
			"public_email": publicEmail, "public_phone": publicPhone,
		})
	default:
		return fmt.Errorf("unknown --org-data-op %q (reset, inspect, hide)", op)
	}
}

func printOrgDataJSON(v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(append(body, '\n'))
	return err
}
