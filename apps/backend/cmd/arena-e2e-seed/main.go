// Package main is arena-e2e-seed, the fixture the web event center's live
// twin tests run against (apps/event-center/scripts/live-harness.mjs).
//
// It creates ONE organization with three people - an owner (membership role
// org_admin), a manager (organizer) and an agent (agent) - and one
// published event with one scheduled session and one open General Admission
// category, so the event list answers sales_state "on_sale". The three roles
// are MEMBERSHIPS of the same organization, as in production and as the bot
// mints them; arena-seed's org-scoped user_roles never reach a real login's
// permission checks (AGENTS.md), so its users cannot stand in for them.
//
// Idempotent: fixed UUIDs, ON CONFLICT DO NOTHING, and the category's
// places are only minted for a row this run created. The command prints one
// JSON document (organization, event, session and the three sign-ins) to
// stdout, or to --out. Every credential is a TEST value that exists only in
// this file.
//
// Usage: DATABASE_URL=... go run ./apps/backend/cmd/arena-e2e-seed [--out file]
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/gaquota"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

const (
	orgID     = "fe0000e2-0000-7000-8000-000000000001"
	venueID   = "fe0000e2-0000-7000-8000-000000000002"
	eventID   = "fe0000e2-0000-7000-8000-000000000003"
	sessionID = "fe0000e2-0000-7000-8000-000000000004"
	ledgerID  = "fe0000e2-0000-7000-8000-000000000005"
	tierID    = "fe0000e2-0000-7000-8000-000000000006"

	eventName = "E2E Twin Show"
	orgName   = "E2E Event Center Org"

	// seedPassword is the plaintext of all three accounts. Only its bcrypt
	// hash reaches the database; the value is a test one for this fixture.
	seedPassword = "E2e-Twin-Pass!47" //nolint:gosec // G101: test fixture credential, never a real one.
)

// person is one account of the fixture.
type person struct {
	Role         string `json:"role"` // the membership role
	Email        string `json:"email"`
	Password     string `json:"password"`
	UserID       string `json:"user_id"`
	MembershipID string `json:"-"`
}

func people() []person {
	return []person{
		{Role: "org_admin", Email: "e2e-owner@test.arena.local", UserID: "fe0000e3-0000-7000-8000-000000000001", MembershipID: "fe0000e4-0000-7000-8000-000000000001"},
		{Role: "organizer", Email: "e2e-manager@test.arena.local", UserID: "fe0000e3-0000-7000-8000-000000000002", MembershipID: "fe0000e4-0000-7000-8000-000000000002"},
		{Role: "agent", Email: "e2e-agent@test.arena.local", UserID: "fe0000e3-0000-7000-8000-000000000003", MembershipID: "fe0000e4-0000-7000-8000-000000000003"},
	}
}

// output is what the harness reads.
type output struct {
	OrgID     string   `json:"org_id"`
	OrgName   string   `json:"org_name"`
	EventID   string   `json:"event_id"`
	EventName string   `json:"event_name"`
	SessionID string   `json:"session_id"`
	TierID    string   `json:"tier_id"`
	People    []person `json:"people"`
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "arena-e2e-seed: fatal: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("arena-e2e-seed", flag.ContinueOnError)
	out := fs.String("out", "", "write the JSON description to this file instead of stdout")
	cancelOp := fs.String("cancel-order-op", "", "4-2 twin helper: reset, inspect or pay (see cancel_order_fixture.go)")
	cancelOrder := fs.String("order-id", "", "the order --cancel-order-op inspect or pay works on")
	invitationsOp := fs.String("invitations-op", "", "4-4 twin helper: reset, inspect, deliver or fail (see invitations_fixture.go)")
	invitationEmail := fs.String("invitation-email", "", "the guest --invitations-op deliver or fail works on")
	teamOp := fs.String("team-op", "", "4-5 twin helper: reset, age, expire or inspect (see team_fixture.go)")
	teamEmail := fs.String("team-email", "", "the address --team-op age, expire or inspect works on")
	orgDataOp := fs.String("org-data-op", "", "4-7 twin helper: reset, inspect or hide (see org_data_fixture.go)")
	// Independent live fixtures for the three release features.
	eventStatusOp := fs.String("event-status-op", "", "5-1 twin helper: reset or inspect")
	saleTimesOp := fs.String("sale-times-op", "", "5-3 twin helper: reset or inspect")
	categoriesOp := fs.String("categories-op", "", "5-4 twin helper: reset or inspect")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *eventStatusOp != "" {
		return runEventStatusOp(*eventStatusOp)
	}
	if *categoriesOp != "" {
		return runCategoriesOp(*categoriesOp)
	}
	if *teamOp != "" {
		return runTeamOp(*teamOp, *teamEmail)
	}
	if *cancelOp != "" {
		return runCancelOrderOp(*cancelOp, *cancelOrder)
	}
	if *orgDataOp != "" {
		return runOrgDataOp(*orgDataOp)
	}
	if *saleTimesOp != "" {
		return runSaleTimesOp(*saleTimesOp)
	}
	if *invitationsOp != "" {
		return runInvitationsOp(*invitationsOp, *invitationEmail)
	}
	cfg, err := config.LoadFor(config.RoleMigrate)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, cfg.DBDSN())
	if err != nil {
		return fmt.Errorf("open pgx pool: %w", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping database: %w", err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := apply(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	doc := output{OrgID: orgID, OrgName: orgName, EventID: eventID, EventName: eventName, SessionID: sessionID, TierID: tierID, People: people()}
	for i := range doc.People {
		doc.People[i].Password = seedPassword
	}
	body, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	if *out != "" {
		return os.WriteFile(*out, body, 0o600)
	}
	_, err = os.Stdout.Write(body)
	return err
}

func apply(ctx context.Context, tx pgx.Tx) error {
	exec := func(what, sql string, args ...any) (int64, error) {
		tag, err := tx.Exec(ctx, sql, args...)
		if err != nil {
			return 0, fmt.Errorf("insert %s: %w", what, err)
		}
		return tag.RowsAffected(), nil
	}
	if _, err := exec("organization", `
		INSERT INTO organizations (id, name, slug, country, default_locale, reservation_ttl_seconds)
		VALUES ($1, $2, 'e2e-event-center-org', 'ES', 'en', 1200)
		ON CONFLICT (id) DO NOTHING`, orgID, orgName); err != nil {
		return err
	}
	if _, err := exec("venue", `
		INSERT INTO venues (id, org_id, name, timezone)
		VALUES ($1, $2, 'E2E Hall', 'Europe/Madrid')
		ON CONFLICT (id) DO NOTHING`, venueID, orgID); err != nil {
		return err
	}
	for _, p := range people() {
		hash, err := bcrypt.GenerateFromPassword([]byte(seedPassword), bcrypt.DefaultCost)
		if err != nil {
			return fmt.Errorf("hash password: %w", err)
		}
		if _, err := exec("user "+p.Role, `
			INSERT INTO users (id, email, password_hash, preferred_locale, email_verified_at)
			VALUES ($1, $2, $3, 'en', now())
			ON CONFLICT (id) DO NOTHING`, p.UserID, p.Email, string(hash)); err != nil {
			return err
		}
		if _, err := exec("membership "+p.Role, `
			INSERT INTO memberships (id, user_id, org_id, role, status)
			VALUES ($1, $2, $3, $4, 'active')
			ON CONFLICT (id) DO NOTHING`, p.MembershipID, p.UserID, orgID, p.Role); err != nil {
			return err
		}
	}
	if _, err := exec("event", `
		INSERT INTO events (id, org_id, name, status, visibility)
		VALUES ($1, $2, $3, 'published', 'public')
		ON CONFLICT (id) DO NOTHING`, eventID, orgID, eventName); err != nil {
		return err
	}
	start := time.Now().UTC().AddDate(0, 0, 30).Truncate(time.Hour)
	if _, err := exec("session", `
		INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, status, currency, currency_source)
		VALUES ($1, $2, $3, $4, $4::timestamptz + interval '2 hours', 30, 'scheduled', 'EUR', 'override')
		ON CONFLICT (id) DO NOTHING`, sessionID, eventID, venueID, start); err != nil {
		return err
	}
	if _, err := exec("inventory ledger", `
		INSERT INTO inventory_ledger (id, session_id, tier_id, capacity_total)
		VALUES ($1, $2, NULL, 30)
		ON CONFLICT (id) DO NOTHING`, ledgerID, sessionID); err != nil {
		return err
	}
	// The category OWNS its places: a ticket_tiers row alone sells nothing.
	n, err := exec("ticket tier", `
		INSERT INTO ticket_tiers (id, session_id, name, pricing_mode, price_amount, currency, capacity, sort_order)
		VALUES ($1, $2, 'E2E Standing', 'fixed', 2500, 'EUR', 30, 0)
		ON CONFLICT (id) DO NOTHING`, tierID, sessionID)
	if err != nil {
		return err
	}
	if n > 0 {
		sid, _ := uuid.Parse(sessionID)
		tid, _ := uuid.Parse(tierID)
		if err := gaquota.CreateCategory(ctx, gen.New(tx), sid, tid, 30); err != nil {
			return fmt.Errorf("materialize places: %w", err)
		}
	}
	if err := applyOrgDataFixture(ctx, tx); err != nil {
		return err
	}
	if err := applyCancelOrderFixture(ctx, tx); err != nil {
		return err
	}
	// 4-1: paid orders for the resend twin test.
	if err := applyResendOrders(ctx, tx); err != nil {
		return err
	}
	if err := applyTeamFixture(ctx, tx); err != nil {
		return err
	}
	// 4-3: the promo-code twin test's extra rows (promo_fixture.go).
	if err := applyPromoFixture(ctx, tx); err != nil {
		return err
	}
	if err := applyForeignPromoter(ctx, tx); err != nil {
		return err
	}
	if err := applyInvitationsFixture(ctx, tx); err != nil {
		return err
	}
	// 5-1: the status twin test's events (event_status_fixture.go).
	if err := applyEventStatusFixture(ctx, tx); err != nil {
		return err
	}
	// 5-3: the date of the sales end and doors-open twin test (sale_times_fixture.go).
	if err := applySaleTimesFixture(ctx, tx); err != nil {
		return err
	}
	// 5-4: the categories twin test's date (categories_fixture.go).
	if err := applyCategoriesFixture(ctx, tx); err != nil {
		return err
	}
	// 6-1a: a promoter and a seat count for the wizard's twin test.
	return applyWizardFixture(ctx, tx)
}

// Task 4-6 (the web promoters screens): ANOTHER organization with one
// promoter, so the twin test can prove that a promoter of a foreign
// organization is neither listed nor reachable by its id. The constants are
// public on purpose: the spec names them.
const (
	foreignOrgID        = "fe0000e2-0000-7000-8000-0000000000f1"
	foreignPromoterID   = "fe0000e2-0000-7000-8000-0000000000f2"
	foreignOrgName      = "E2E Other Org"
	foreignPromoterName = "E2E Foreign Promoter"
	foreignPromoterSlug = "e2e-foreign-promoter"
)

// applyForeignPromoter is additive and idempotent like the rest of the seed.
func applyForeignPromoter(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO organizations (id, name, slug, country, default_locale, reservation_ttl_seconds)
		VALUES ($1, $2, 'e2e-other-org', 'ES', 'en', 1200)
		ON CONFLICT (id) DO NOTHING`, foreignOrgID, foreignOrgName); err != nil {
		return fmt.Errorf("insert foreign organization: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO org_promoters (id, org_id, name, slug)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (id) DO NOTHING`, foreignPromoterID, foreignOrgID, foreignPromoterName, foreignPromoterSlug); err != nil {
		return fmt.Errorf("insert foreign promoter: %w", err)
	}
	return nil
}
