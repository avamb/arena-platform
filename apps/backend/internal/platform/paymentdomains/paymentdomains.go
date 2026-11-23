// Package paymentdomains keeps the wallet domains (Apple Pay, Google Pay) of
// every organization's Stripe account in step with the hosts that sell its
// tickets (spec 65 section 5).
//
// The wanted set of one Stripe configuration is the platform hosts plus the
// site hosts of every live Stripe sales channel of the same organization
// (sales_channel_site_domains). Each wanted host is registered in Stripe
// through payments.PaymentMethodDomainRegistrar (look up first, so a repeat
// creates nothing) and the answer is stored in payment_method_domains. A
// Stripe failure is stored in last_error and never stops the other domains.
// 'site' rows that are no longer wanted are deleted locally; Stripe is not
// told, a stale registration is harmless.
//
// The secret key is never logged and never stored in last_error: only the
// provider's own error text is.
package paymentdomains

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	paymodules "github.com/abhteam/arena_new/apps/backend/internal/app/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/worker"
)

// Job types.
const (
	// JobSync syncs ONE organization. Payload {"org_id": "<uuid>"}.
	JobSync = "payment_domains.sync"
	// JobSweep syncs every organization and schedules its own next run.
	JobSweep = "payment_domains.sweep"
)

// SweepInterval is how often the sweep runs.
const SweepInterval = 24 * time.Hour

const (
	// SourcePlatform marks a domain that comes from STRIPE_WALLET_DOMAINS.
	SourcePlatform = "platform"
	// SourceSite marks a domain that comes from a sales channel's site list.
	SourceSite = "site"
)

const maxErrorLen = 500

// ModuleBuilder builds a payment module; paymodules.Registry().Build by default.
type ModuleBuilder func(provider string, secrets map[string]string, opts payments.Options) (payments.Module, error)

// Syncer registers wallet domains. The zero value is not usable: set Queries.
type Syncer struct {
	// Queries is bound to the pool.
	Queries *gen.Queries
	// Options are the deployment's provider options (StripeAPIBaseURL).
	Options payments.Options
	// Build overrides module construction (tests). Nil uses the registry.
	Build ModuleBuilder
	// PlatformDomains are the hosts registered for every organization.
	PlatformDomains []string
	// Logger may be nil.
	Logger *slog.Logger
}

func (s *Syncer) logger() *slog.Logger {
	if s.Logger != nil {
		return s.Logger
	}
	return slog.Default()
}

func (s *Syncer) build(provider string, secrets map[string]string, opts payments.Options) (payments.Module, error) {
	if s.Build != nil {
		return s.Build(provider, secrets, opts)
	}
	return paymodules.Registry().Build(provider, secrets, opts)
}

// eligible: only an active, live Stripe configuration whose key Stripe has not
// refused and that holds a key.
func eligible(cfg gen.PaymentProviderConfigRow) bool {
	return cfg.Provider == "stripe" && cfg.IsActive && cfg.DeletedAt == nil && cfg.VerificationStatus != "failed"
}

type want struct{ domain, source string }

// wanted is the platform hosts then the site hosts, one entry per host
// (the platform source wins).
func (s *Syncer) wanted(ctx context.Context, orgID uuid.UUID) ([]want, error) {
	var out []want
	seen := map[string]bool{}
	for _, d := range s.PlatformDomains {
		d = strings.ToLower(strings.TrimSpace(d))
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, want{d, SourcePlatform})
	}
	sites, err := s.Queries.ListDesiredSiteDomainsForOrgStripe(ctx, orgID)
	if err != nil {
		return nil, err
	}
	for _, d := range sites {
		if seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, want{d, SourceSite})
	}
	return out, nil
}

// SyncConfig registers the wanted domains in the Stripe account of one
// configuration and returns the stored rows afterwards. A configuration that
// is not eligible (not Stripe, inactive, deleted, refused, no key) is skipped
// and returns nil, nil. A Stripe failure is stored per domain and is not an
// error of the call; only a database failure is.
func (s *Syncer) SyncConfig(ctx context.Context, cfg gen.PaymentProviderConfigRow) ([]gen.PaymentMethodDomainRow, error) {
	if !eligible(cfg) {
		return nil, nil
	}
	secrets := payments.SecretsFromJSON(cfg.Secrets)
	if strings.TrimSpace(secrets["api_key"]) == "" {
		return nil, nil
	}
	opts := s.Options
	opts.Mode = cfg.Mode
	mod, err := s.build(cfg.Provider, secrets, opts)
	if err != nil {
		return nil, fmt.Errorf("paymentdomains: build module for config %s: %w", cfg.ID, err)
	}
	reg, ok := mod.(payments.PaymentMethodDomainRegistrar)
	if !ok {
		return nil, nil
	}
	wanteds, err := s.wanted(ctx, cfg.OrgID)
	if err != nil {
		return nil, err
	}
	keep := make([]string, 0, len(wanteds))
	for _, w := range wanteds {
		keep = append(keep, w.domain)
	}
	if _, err := s.Queries.DeleteStalePaymentMethodDomains(ctx, cfg.ID, keep); err != nil {
		return nil, err
	}
	for _, w := range wanteds {
		d, err := reg.EnsurePaymentMethodDomain(ctx, w.domain)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			msg := errText(err)
			s.logger().Warn("wallet domain sync failed",
				slog.String("config_id", cfg.ID.String()), slog.String("domain", w.domain), slog.String("error", msg))
			if derr := s.Queries.SetPaymentMethodDomainError(ctx, cfg.ID, cfg.OrgID, w.domain, w.source, msg); derr != nil {
				return nil, derr
			}
			continue
		}
		if err := s.Queries.UpsertPaymentMethodDomain(ctx, gen.UpsertPaymentMethodDomainParams{
			PaymentConfigID:  cfg.ID,
			OrgID:            cfg.OrgID,
			Domain:           w.domain,
			Source:           w.source,
			ProviderDomainID: strPtr(d.ProviderID),
			Enabled:          &d.Enabled,
			ApplePayStatus:   statusPtr(d.ApplePay),
			GooglePayStatus:  statusPtr(d.GooglePay),
			LinkStatus:       statusPtr(d.Link),
			StatusDetail:     strPtr(d.Detail),
		}); err != nil {
			return nil, err
		}
	}
	return s.Queries.ListPaymentMethodDomains(ctx, cfg.ID, cfg.OrgID)
}

// SyncOrg syncs every eligible Stripe configuration of one organization.
func (s *Syncer) SyncOrg(ctx context.Context, orgID uuid.UUID) error {
	cfgs, err := s.Queries.ListActiveStripePaymentConfigs(ctx, &orgID)
	if err != nil {
		return err
	}
	return s.syncAll(ctx, cfgs)
}

// SyncAll syncs every eligible Stripe configuration of every organization.
func (s *Syncer) SyncAll(ctx context.Context) error {
	cfgs, err := s.Queries.ListActiveStripePaymentConfigs(ctx, nil)
	if err != nil {
		return err
	}
	return s.syncAll(ctx, cfgs)
}

func (s *Syncer) syncAll(ctx context.Context, cfgs []gen.PaymentProviderConfigRow) error {
	var errs []error
	for _, c := range cfgs {
		if _, err := s.SyncConfig(ctx, c); err != nil {
			s.logger().Warn("wallet domain sync of a configuration failed",
				slog.String("config_id", c.ID.String()), slog.String("error", err.Error()))
			errs = append(errs, err)
			if ctx.Err() != nil {
				break
			}
		}
	}
	return errors.Join(errs...)
}

// ---------------------------------------------------------------------------
// Worker
// ---------------------------------------------------------------------------

// Register registers both job types.
func (s *Syncer) Register(reg *worker.Registry) {
	reg.Register(JobSync, s.SyncHandler())
	reg.Register(JobSweep, s.SweepHandler())
}

// SyncHandler is the worker handler of payment_domains.sync.
func (s *Syncer) SyncHandler() worker.HandlerFunc {
	return func(ctx context.Context, payload []byte) error {
		var p struct {
			OrgID string `json:"org_id"`
		}
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("paymentdomains: bad payload: %w", err)
		}
		orgID, err := uuid.Parse(p.OrgID)
		if err != nil {
			return fmt.Errorf("paymentdomains: bad org_id: %w", err)
		}
		return s.SyncOrg(ctx, orgID)
	}
}

// SweepHandler is the worker handler of payment_domains.sweep. It schedules
// the next run first, so a failing pass never ends the chain, and it never
// returns an error for the pass itself (the next sweep retries).
func (s *Syncer) SweepHandler() worker.HandlerFunc {
	return func(ctx context.Context, _ []byte) error {
		if _, err := s.Queries.DB().Exec(ctx, `INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
			SELECT $1::text, '{}', 3, 'pending', $2
			WHERE NOT EXISTS (SELECT 1 FROM worker_jobs WHERE job_type = $1::text AND status = 'pending')`,
			JobSweep, time.Now().Add(SweepInterval)); err != nil {
			return fmt.Errorf("paymentdomains: schedule next sweep: %w", err)
		}
		if err := s.SyncAll(ctx); err != nil {
			s.logger().Warn("wallet domain sweep finished with errors", slog.String("error", err.Error()))
		}
		return nil
	}
}

// ScheduleSweep seeds the sweep chain once at worker start (a no-op while one
// is pending or claimed). The first run also catches up configurations that
// existed before this feature.
func (s *Syncer) ScheduleSweep(ctx context.Context) error {
	db := s.Queries.DB()
	var cnt int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM worker_jobs WHERE job_type = $1 AND status IN ('pending','claimed')`, JobSweep).Scan(&cnt); err != nil {
		return err
	}
	if cnt > 0 {
		return nil
	}
	_, err := db.Exec(ctx, `INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
		VALUES ($1, '{}', 3, 'pending', now())`, JobSweep)
	return err
}

// EnqueueSync queues payment_domains.sync for one organization, unless one is
// already pending for it.
func EnqueueSync(ctx context.Context, db gen.DBTX, orgID uuid.UUID) error {
	payload, _ := json.Marshal(map[string]string{"org_id": orgID.String()})
	_, err := db.Exec(ctx, `INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
		SELECT $1::text, $2::jsonb, 3, 'pending', now()
		WHERE NOT EXISTS (SELECT 1 FROM worker_jobs WHERE job_type = $1::text AND status = 'pending' AND payload->>'org_id' = $3::text)`,
		JobSync, payload, orgID.String())
	return err
}

func errText(err error) string {
	s := strings.TrimSpace(err.Error())
	if len(s) > maxErrorLen {
		s = s[:maxErrorLen]
	}
	return s
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// statusPtr keeps only a value the CHECK on the status columns accepts.
func statusPtr(s string) *string {
	if s == "" || len(s) > 32 {
		return nil
	}
	for _, r := range s {
		if (r < 'a' || r > 'z') && r != '_' {
			return nil
		}
	}
	return &s
}
