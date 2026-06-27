// Package main is the entry point for arena-seed, a development helper that
// loads a small set of test/fixture rows into the database so operators and
// QA can exercise the admin UI without manually creating organizations,
// venues, users, channels, and payment configs through the API.
//
// Scope (feature #247):
//   - 3 organizations with different country/locale combinations
//   - 3 venues per organization (9 total)
//   - 3 users with different roles (superadmin, org_admin, organizer, agent)
//   - 3 sales channels across the seeded organizations
//   - 3 payment provider configs (test mode credentials only)
//
// Everything inserted by this command is CLEARLY test data: names are
// prefixed with "TEST ", email addresses live under @test.arena.local, all
// payment credentials are fake placeholder strings, and provider configs
// run in 'test' mode only.
//
// Idempotency: every INSERT uses a fixed UUID primary key with ON CONFLICT
// (id) DO NOTHING (or, for join tables, ON CONFLICT on the natural key).
// Re-running the command against a database that already contains the seed
// rows is a no-op — no duplicates are produced and no existing data is
// modified. The exit code is 0 on success.
//
// Usage:
//
//	# Apply seed data (idempotent — safe to re-run)
//	DATABASE_URL=postgres://... go run ./apps/backend/cmd/arena-seed
//
//	# Or via the compiled binary
//	./bin/arena-seed
//
//	# Print what would be inserted without touching the database
//	./bin/arena-seed --dry-run
//
// Exit code is 0 on success, 1 on any error.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "arena-seed: fatal: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("arena-seed", flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "print the planned seed contents and exit without touching the database")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	logger := logging.NewWithOptions(logging.Options{
		Writer:  os.Stdout,
		Format:  cfg.LogFormat,
		Level:   cfg.LogLevel,
		App:     "arena-seed",
		Env:     string(cfg.AppEnv),
		Version: cfg.AppVersion,
	}).With(slog.String("commit", cfg.AppCommit))
	slog.SetDefault(logger)

	seed := BuildSeed()

	if *dryRun {
		printSummary(seed)
		logger.Info("dry-run complete; database not touched")
		return nil
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

	stats, err := ApplySeed(ctx, pool, seed)
	if err != nil {
		return fmt.Errorf("apply seed: %w", err)
	}

	logger.Info("seed applied",
		"organizations", stats.Organizations,
		"venues", stats.Venues,
		"users", stats.Users,
		"user_roles", stats.UserRoles,
		"memberships", stats.Memberships,
		"channels", stats.Channels,
		"payment_configs", stats.PaymentConfigs,
		"already_present", stats.AlreadyPresent,
	)
	return nil
}

// ---------------------------------------------------------------------------
// Seed model
// ---------------------------------------------------------------------------

// SeedData is the in-memory description of every fixture row arena-seed
// will write. It is exported (along with BuildSeed) so the package's tests —
// and any future tooling — can inspect the exact rows the binary intends to
// produce without standing up a database.
type SeedData struct {
	Organizations  []SeedOrganization
	Venues         []SeedVenue
	Users          []SeedUser
	UserRoles      []SeedUserRole
	Memberships    []SeedMembership
	Channels       []SeedChannel
	PaymentConfigs []SeedPaymentConfig
}

// SeedOrganization mirrors a single row that will be inserted into the
// organizations table. The ID is a hard-coded UUID so re-running the seed
// is idempotent via ON CONFLICT (id) DO NOTHING.
type SeedOrganization struct {
	ID                    string
	Name                  string
	Slug                  string
	Country               string
	DefaultLocale         string
	ReservationTTLSeconds int
}

type SeedVenue struct {
	ID              string
	OrgID           string
	CitySlug        string // looked up via cities.slug at apply time; "" means NULL
	Name            string
	Address         string
	CapacityDefault int // 0 means NULL
}

type SeedUser struct {
	ID               string
	Email            string
	PlaintextPwd     string // hashed at apply time with bcrypt cost 12
	PreferredLocale  string
	MarkEmailVerifiedAt bool
}

// SeedUserRole assigns a built-in or org-scoped role to a user via
// the user_roles join table. OrgID is empty for global assignments.
type SeedUserRole struct {
	UserID   string
	RoleName string
	OrgID    string // empty == NULL (global assignment)
}

// SeedMembership corresponds to a row in the memberships table.
// The role must be one of the values accepted by memberships_role_check.
type SeedMembership struct {
	ID     string
	UserID string
	OrgID  string
	Role   string // organizer|agent|platform_operator|external_ticketing_operator|platform_superadmin
}

type SeedChannel struct {
	ID                     string
	OrgID                  string
	Name                   string
	PaymentMode            string  // direct_merchant|merchant_of_record
	Provider               string  // stripe|allpay
	ProviderAccountID      string  // "" means NULL (only valid for merchant_of_record)
	FeePercent             float64 // numeric(5,2)
	ReservationTTLOverride int     // 0 means NULL
}

// SeedPaymentConfig is a per-org provider credential set in 'test' mode.
// Secrets are intentionally fake strings — every value starts with
// "TEST_" so a careless export cannot accidentally hit a live provider.
type SeedPaymentConfig struct {
	ID                string
	OrgID             string
	Provider          string // stripe|allpay
	Mode              string // test|live (always test for the seed)
	ProviderAccountID string
	PublicConfigJSON  string // JSON object
	SecretsJSON       string // JSON object
	Status            string // configured|missing_required_fields
}

// ---------------------------------------------------------------------------
// BuildSeed — the canonical set of fixture rows.
// ---------------------------------------------------------------------------

// Fixed UUIDv7-shaped IDs. The first byte 'fe' makes them obviously
// hand-picked test IDs (real uuidv7 timestamps start with bytes derived
// from the current epoch, not 0xfe). Each ID is unique across all tables
// — collisions would silently lose rows on conflict.
const (
	OrgA = "fe000001-0000-7000-8000-000000000001"
	OrgB = "fe000001-0000-7000-8000-000000000002"
	OrgC = "fe000001-0000-7000-8000-000000000003"

	VenueA1 = "fe000002-0000-7000-8000-00000000000a"
	VenueA2 = "fe000002-0000-7000-8000-00000000000b"
	VenueA3 = "fe000002-0000-7000-8000-00000000000c"
	VenueB1 = "fe000002-0000-7000-8000-00000000000d"
	VenueB2 = "fe000002-0000-7000-8000-00000000000e"
	VenueB3 = "fe000002-0000-7000-8000-00000000000f"
	VenueC1 = "fe000002-0000-7000-8000-000000000010"
	VenueC2 = "fe000002-0000-7000-8000-000000000011"
	VenueC3 = "fe000002-0000-7000-8000-000000000012"

	UserSuper     = "fe000003-0000-7000-8000-000000000001"
	UserOrgAdmin  = "fe000003-0000-7000-8000-000000000002"
	UserOrganizer = "fe000003-0000-7000-8000-000000000003"
	UserAgent     = "fe000003-0000-7000-8000-000000000004"

	MembershipOrganizer = "fe000004-0000-7000-8000-000000000001"
	MembershipAgent     = "fe000004-0000-7000-8000-000000000002"

	ChannelAStripe = "fe000005-0000-7000-8000-000000000001"
	ChannelAAllpay = "fe000005-0000-7000-8000-000000000002"
	ChannelBStripe = "fe000005-0000-7000-8000-000000000003"

	PaymentCfgAStripe = "fe000006-0000-7000-8000-000000000001"
	PaymentCfgAAllpay = "fe000006-0000-7000-8000-000000000002"
	PaymentCfgBStripe = "fe000006-0000-7000-8000-000000000003"
)

// SeedPassword is the plaintext password assigned to every seeded user.
// It is intentionally well-known so QA can log in via the admin UI; only
// the bcrypt hash ever reaches the database.
const SeedPassword = "TestPass!23"

// BuildSeed returns the canonical set of fixture rows the arena-seed
// command will insert. The same SeedData is consumed by ApplySeed and
// by the dry-run summary printer, so the two paths can never drift.
func BuildSeed() SeedData {
	return SeedData{
		Organizations: []SeedOrganization{
			{ID: OrgA, Name: "TEST Arena Tel Aviv", Slug: "test-arena-tel-aviv", Country: "IL", DefaultLocale: "en", ReservationTTLSeconds: 1200},
			{ID: OrgB, Name: "TEST Arena Tallinn", Slug: "test-arena-tallinn", Country: "EE", DefaultLocale: "en", ReservationTTLSeconds: 900},
			{ID: OrgC, Name: "TEST Arena Riga", Slug: "test-arena-riga", Country: "LV", DefaultLocale: "ru", ReservationTTLSeconds: 1800},
		},
		Venues: []SeedVenue{
			{ID: VenueA1, OrgID: OrgA, CitySlug: "tel-aviv", Name: "TEST Bloomfield Hall", Address: "1 Test Way, Tel Aviv", CapacityDefault: 1500},
			{ID: VenueA2, OrgID: OrgA, CitySlug: "jerusalem", Name: "TEST Jerusalem Pavilion", Address: "2 Test Way, Jerusalem", CapacityDefault: 800},
			{ID: VenueA3, OrgID: OrgA, CitySlug: "haifa", Name: "TEST Haifa Open Air", Address: "3 Test Way, Haifa", CapacityDefault: 5000},
			{ID: VenueB1, OrgID: OrgB, CitySlug: "tallinn", Name: "TEST Old Town Stage", Address: "1 Test Tee, Tallinn", CapacityDefault: 600},
			{ID: VenueB2, OrgID: OrgB, CitySlug: "tartu", Name: "TEST University Hall", Address: "2 Test Tee, Tartu", CapacityDefault: 400},
			{ID: VenueB3, OrgID: OrgB, CitySlug: "parnu", Name: "TEST Beach Amphitheatre", Address: "3 Test Tee, Pärnu", CapacityDefault: 1200},
			// Org C cities aren't seeded in 0006_geo.sql (Latvia has no city
			// rows) so the city_id will be NULL for these venues. The
			// venue.city_id column is nullable; the seed gracefully falls back.
			{ID: VenueC1, OrgID: OrgC, CitySlug: "", Name: "TEST Riga Dome", Address: "1 Test iela, Riga", CapacityDefault: 2200},
			{ID: VenueC2, OrgID: OrgC, CitySlug: "", Name: "TEST Riga Studio", Address: "2 Test iela, Riga", CapacityDefault: 250},
			{ID: VenueC3, OrgID: OrgC, CitySlug: "", Name: "TEST Riga Park", Address: "3 Test iela, Riga", CapacityDefault: 3500},
		},
		Users: []SeedUser{
			{ID: UserSuper, Email: "super@test.arena.local", PlaintextPwd: SeedPassword, PreferredLocale: "en", MarkEmailVerifiedAt: true},
			{ID: UserOrgAdmin, Email: "admin@test.arena.local", PlaintextPwd: SeedPassword, PreferredLocale: "en", MarkEmailVerifiedAt: true},
			{ID: UserOrganizer, Email: "organizer@test.arena.local", PlaintextPwd: SeedPassword, PreferredLocale: "en", MarkEmailVerifiedAt: true},
			{ID: UserAgent, Email: "agent@test.arena.local", PlaintextPwd: SeedPassword, PreferredLocale: "ru", MarkEmailVerifiedAt: true},
		},
		UserRoles: []SeedUserRole{
			// super: global admin role (org_id NULL) — gives every permission
			// via the broad role_permissions grant in 0008_rbac.sql.
			{UserID: UserSuper, RoleName: "admin", OrgID: ""},
			// org_admin user: scoped to Org A so all org.*/venue.*/channel.*
			// permissions apply only within that tenant.
			{UserID: UserOrgAdmin, RoleName: "org_admin", OrgID: OrgA},
		},
		Memberships: []SeedMembership{
			// memberships_role_check rejects 'admin'/'org_admin' — those are
			// granted via user_roles above. Only domain roles go here.
			{ID: MembershipOrganizer, UserID: UserOrganizer, OrgID: OrgA, Role: "organizer"},
			{ID: MembershipAgent, UserID: UserAgent, OrgID: OrgB, Role: "agent"},
		},
		Channels: []SeedChannel{
			{ID: ChannelAStripe, OrgID: OrgA, Name: "TEST Stripe IL", PaymentMode: "direct_merchant", Provider: "stripe", ProviderAccountID: "acct_TEST_ORGA", FeePercent: 2.50, ReservationTTLOverride: 0},
			{ID: ChannelAAllpay, OrgID: OrgA, Name: "TEST AllPay MoR", PaymentMode: "merchant_of_record", Provider: "allpay", ProviderAccountID: "", FeePercent: 4.90, ReservationTTLOverride: 600},
			{ID: ChannelBStripe, OrgID: OrgB, Name: "TEST Stripe EE", PaymentMode: "direct_merchant", Provider: "stripe", ProviderAccountID: "acct_TEST_ORGB", FeePercent: 2.90, ReservationTTLOverride: 0},
		},
		PaymentConfigs: []SeedPaymentConfig{
			{
				ID:                PaymentCfgAStripe,
				OrgID:             OrgA,
				Provider:          "stripe",
				Mode:              "test",
				ProviderAccountID: "acct_TEST_ORGA",
				PublicConfigJSON:  `{"statement_descriptor":"TEST ARENA TLV"}`,
				SecretsJSON:       `{"api_key":"sk_test_TEST_ORGA_DO_NOT_USE","webhook_secret":"whsec_TEST_ORGA_DO_NOT_USE"}`,
				Status:            "configured",
			},
			{
				ID:                PaymentCfgAAllpay,
				OrgID:             OrgA,
				Provider:          "allpay",
				Mode:              "test",
				ProviderAccountID: "merchant_TEST_ORGA",
				PublicConfigJSON:  `{"terminal_id":"TEST-TERM-1"}`,
				SecretsJSON:       `{"merchant_id":"merchant_TEST_ORGA","secret_key":"TEST_ORGA_ALLPAY_SECRET"}`,
				Status:            "configured",
			},
			{
				ID:                PaymentCfgBStripe,
				OrgID:             OrgB,
				Provider:          "stripe",
				Mode:              "test",
				ProviderAccountID: "acct_TEST_ORGB",
				PublicConfigJSON:  `{"statement_descriptor":"TEST ARENA TLN"}`,
				SecretsJSON:       `{"api_key":"sk_test_TEST_ORGB_DO_NOT_USE","webhook_secret":"whsec_TEST_ORGB_DO_NOT_USE"}`,
				Status:            "configured",
			},
		},
	}
}

// ---------------------------------------------------------------------------
// ApplyStats / ApplySeed
// ---------------------------------------------------------------------------

// ApplyStats holds the per-table count of rows inserted on this run.
// "AlreadyPresent" is the total number of seed rows that were detected as
// already existing (i.e. ON CONFLICT DO NOTHING short-circuited the
// INSERT). This makes the seed output useful as a sanity check — the
// first run will show all rows inserted, every subsequent run will show
// all rows already present.
type ApplyStats struct {
	Organizations  int
	Venues         int
	Users          int
	UserRoles      int
	Memberships    int
	Channels       int
	PaymentConfigs int
	AlreadyPresent int
}

// ApplySeed runs every seed INSERT inside a single transaction so a
// failure halfway through leaves the database in its prior state. Every
// statement uses ON CONFLICT DO NOTHING against the primary key (or the
// natural join key for user_roles), so re-running the seed is a no-op.
func ApplySeed(ctx context.Context, pool *pgxpool.Pool, seed SeedData) (ApplyStats, error) {
	var stats ApplyStats
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return stats, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() //nolint:errcheck // rollback is a no-op after commit

	stats, err = applyAll(ctx, tx, seed)
	if err != nil {
		return stats, err
	}

	if err := tx.Commit(ctx); err != nil {
		return stats, fmt.Errorf("commit tx: %w", err)
	}
	return stats, nil
}

// applyAll runs every INSERT against the supplied pgx.Tx. It is split
// out of ApplySeed so callers (e.g. integration tests that want to test
// inside a savepoint) can drive it directly without re-opening the pool.
func applyAll(ctx context.Context, tx pgx.Tx, seed SeedData) (ApplyStats, error) {
	var stats ApplyStats

	for _, org := range seed.Organizations {
		tag, err := tx.Exec(ctx, `
			INSERT INTO organizations (id, name, slug, country, default_locale, reservation_ttl_seconds)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (id) DO NOTHING
		`, org.ID, org.Name, org.Slug, org.Country, org.DefaultLocale, org.ReservationTTLSeconds)
		if err != nil {
			return stats, fmt.Errorf("insert organization %q: %w", org.Slug, err)
		}
		stats.Organizations += int(tag.RowsAffected())
		if tag.RowsAffected() == 0 {
			stats.AlreadyPresent++
		}
	}

	for _, v := range seed.Venues {
		var cityID any
		if v.CitySlug != "" {
			var id string
			err := tx.QueryRow(ctx, `SELECT id FROM cities WHERE slug = $1`, v.CitySlug).Scan(&id)
			if err == nil {
				cityID = id
			} else if !errors.Is(err, pgx.ErrNoRows) {
				return stats, fmt.Errorf("lookup city %q for venue %q: %w", v.CitySlug, v.Name, err)
			}
			// pgx.ErrNoRows: city wasn't seeded; insert venue with NULL city_id.
		}
		var capacity any
		if v.CapacityDefault > 0 {
			capacity = v.CapacityDefault
		}
		tag, err := tx.Exec(ctx, `
			INSERT INTO venues (id, org_id, city_id, name, address, capacity_default)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (id) DO NOTHING
		`, v.ID, v.OrgID, cityID, v.Name, v.Address, capacity)
		if err != nil {
			return stats, fmt.Errorf("insert venue %q: %w", v.Name, err)
		}
		stats.Venues += int(tag.RowsAffected())
		if tag.RowsAffected() == 0 {
			stats.AlreadyPresent++
		}
	}

	for _, u := range seed.Users {
		hash, err := bcrypt.GenerateFromPassword([]byte(u.PlaintextPwd), bcrypt.DefaultCost+2)
		if err != nil {
			return stats, fmt.Errorf("hash password for %q: %w", u.Email, err)
		}
		var verifiedAt any
		if u.MarkEmailVerifiedAt {
			verifiedAt = time.Now().UTC()
		}
		tag, err := tx.Exec(ctx, `
			INSERT INTO users (id, email, password_hash, preferred_locale, email_verified_at)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (id) DO NOTHING
		`, u.ID, u.Email, string(hash), u.PreferredLocale, verifiedAt)
		if err != nil {
			return stats, fmt.Errorf("insert user %q: %w", u.Email, err)
		}
		stats.Users += int(tag.RowsAffected())
		if tag.RowsAffected() == 0 {
			stats.AlreadyPresent++
		}
	}

	for _, ur := range seed.UserRoles {
		var orgID any
		if ur.OrgID != "" {
			orgID = ur.OrgID
		}
		// All built-in role definitions ('admin', 'org_admin', 'organizer',
		// 'agent', …) live in the roles table with org_id IS NULL — that is
		// the global role catalogue. The user_roles row may still scope the
		// assignment to a specific organization via its own org_id column.
		tag, err := tx.Exec(ctx, `
			INSERT INTO user_roles (user_id, role_id, org_id)
			SELECT $1, r.id, $3
			FROM   roles r
			WHERE  r.name = $2 AND r.org_id IS NULL
			ON CONFLICT (user_id, role_id) DO NOTHING
		`, ur.UserID, ur.RoleName, orgID)
		if err != nil {
			return stats, fmt.Errorf("insert user_role (%s,%s): %w", ur.UserID, ur.RoleName, err)
		}
		stats.UserRoles += int(tag.RowsAffected())
		if tag.RowsAffected() == 0 {
			stats.AlreadyPresent++
		}
	}

	for _, m := range seed.Memberships {
		tag, err := tx.Exec(ctx, `
			INSERT INTO memberships (id, user_id, org_id, role, status)
			VALUES ($1, $2, $3, $4, 'active')
			ON CONFLICT (id) DO NOTHING
		`, m.ID, m.UserID, m.OrgID, m.Role)
		if err != nil {
			return stats, fmt.Errorf("insert membership %q: %w", m.ID, err)
		}
		stats.Memberships += int(tag.RowsAffected())
		if tag.RowsAffected() == 0 {
			stats.AlreadyPresent++
		}
	}

	for _, c := range seed.Channels {
		var providerAccount any
		if c.ProviderAccountID != "" {
			providerAccount = c.ProviderAccountID
		}
		var ttlOverride any
		if c.ReservationTTLOverride > 0 {
			ttlOverride = c.ReservationTTLOverride
		}
		tag, err := tx.Exec(ctx, `
			INSERT INTO sales_channels (id, org_id, name, payment_mode, provider,
			                             provider_account_id, fee_percent, reservation_ttl_override)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (id) DO NOTHING
		`, c.ID, c.OrgID, c.Name, c.PaymentMode, c.Provider, providerAccount, c.FeePercent, ttlOverride)
		if err != nil {
			return stats, fmt.Errorf("insert sales_channel %q: %w", c.Name, err)
		}
		stats.Channels += int(tag.RowsAffected())
		if tag.RowsAffected() == 0 {
			stats.AlreadyPresent++
		}
	}

	for _, pc := range seed.PaymentConfigs {
		var providerAccount any
		if pc.ProviderAccountID != "" {
			providerAccount = pc.ProviderAccountID
		}
		tag, err := tx.Exec(ctx, `
			INSERT INTO payment_provider_configs (id, org_id, provider, mode,
			                                       provider_account_id, public_config, secrets,
			                                       status, is_active)
			VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7::jsonb, $8, true)
			ON CONFLICT (id) DO NOTHING
		`, pc.ID, pc.OrgID, pc.Provider, pc.Mode, providerAccount, pc.PublicConfigJSON, pc.SecretsJSON, pc.Status)
		if err != nil {
			return stats, fmt.Errorf("insert payment_provider_config %q: %w", pc.ID, err)
		}
		stats.PaymentConfigs += int(tag.RowsAffected())
		if tag.RowsAffected() == 0 {
			stats.AlreadyPresent++
		}
	}

	return stats, nil
}

// printSummary emits a human-readable list of the seed contents to stdout.
// It is used by --dry-run so an operator can review what would be loaded
// before opening a connection to the database.
func printSummary(seed SeedData) {
	fmt.Fprintln(os.Stdout, "arena-seed: dry-run summary")
	fmt.Fprintf(os.Stdout, "  organizations:    %d\n", len(seed.Organizations))
	for _, o := range seed.Organizations {
		fmt.Fprintf(os.Stdout, "    - %s (%s, %s/%s)\n", o.Name, o.Slug, o.Country, o.DefaultLocale)
	}
	fmt.Fprintf(os.Stdout, "  venues:           %d\n", len(seed.Venues))
	for _, v := range seed.Venues {
		fmt.Fprintf(os.Stdout, "    - %s (org=%s, city=%q)\n", v.Name, v.OrgID, v.CitySlug)
	}
	fmt.Fprintf(os.Stdout, "  users:            %d (password=%q)\n", len(seed.Users), SeedPassword)
	for _, u := range seed.Users {
		fmt.Fprintf(os.Stdout, "    - %s (locale=%s)\n", u.Email, u.PreferredLocale)
	}
	fmt.Fprintf(os.Stdout, "  user_roles:       %d\n", len(seed.UserRoles))
	for _, ur := range seed.UserRoles {
		scope := "global"
		if ur.OrgID != "" {
			scope = "org=" + ur.OrgID
		}
		fmt.Fprintf(os.Stdout, "    - user=%s role=%s (%s)\n", ur.UserID, ur.RoleName, scope)
	}
	fmt.Fprintf(os.Stdout, "  memberships:      %d\n", len(seed.Memberships))
	for _, m := range seed.Memberships {
		fmt.Fprintf(os.Stdout, "    - user=%s org=%s role=%s\n", m.UserID, m.OrgID, m.Role)
	}
	fmt.Fprintf(os.Stdout, "  sales_channels:   %d\n", len(seed.Channels))
	for _, c := range seed.Channels {
		fmt.Fprintf(os.Stdout, "    - %s (org=%s, %s/%s)\n", c.Name, c.OrgID, c.Provider, c.PaymentMode)
	}
	fmt.Fprintf(os.Stdout, "  payment_configs:  %d\n", len(seed.PaymentConfigs))
	for _, pc := range seed.PaymentConfigs {
		fmt.Fprintf(os.Stdout, "    - org=%s provider=%s mode=%s status=%s\n", pc.OrgID, pc.Provider, pc.Mode, pc.Status)
	}
}

