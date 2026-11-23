// payments_shims.go bridges the *Server god-object to the hpayments
// sub-package. All handler and validation logic lives in hpayments/; these
// thin delegating methods preserve the unexported *Server method surface so
// test files and mount files (mount_iam.go) compile unchanged.
package httpserver

import (
	"encoding/json"
	"net/http"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hpayments"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/paymentdomains"
)

// paymentsHandler constructs a hpayments.Handler from the server's dependencies.
func (s *Server) paymentsHandler() *hpayments.Handler {
	return hpayments.New(
		s.paymentConfigQueries,
		s.pool,
		s.audit,
		s.logger,
	).WithMembershipQueries(s.membershipQueries).
		// Same Stripe root the hosted checkout uses, so a stand that points
		// checkout at a stub verifies credentials against that same stub
		// rather than reaching out to the real Stripe.
		WithStripeBaseURL(s.stripeBaseURL()).
		WithFlittBaseURL(s.flittBaseURL()).
		WithAllPayBaseURL(s.allpayBaseURL()).
		WithPayPlusBaseURL(s.payplusBaseURL()).
		WithCallbackBase(s.cfg.APIPublicBaseURL()).
		WithDomainSyncer(s.walletDomainSyncer())
}

// walletDomainSyncer builds the wallet-domain syncer (spec 65 E3) on the same
// Stripe root as the credential check, so a stub stands in for Stripe in tests.
func (s *Server) walletDomainSyncer() *paymentdomains.Syncer {
	if s.paymentConfigQueries == nil {
		return nil
	}
	var platform []string
	if s.cfg != nil {
		platform = s.cfg.WalletPlatformDomains()
	}
	return &paymentdomains.Syncer{
		Queries:         s.paymentConfigQueries,
		Options:         payments.Options{StripeAPIBaseURL: s.stripeBaseURL()},
		PlatformDomains: platform,
		Logger:          s.logger,
	}
}

// ─── type aliases ─────────────────────────────────────────────────────────────
// These let test files in package httpserver reference types that now live in
// hpayments without importing that package directly.

type paymentConfigResponse = hpayments.PaymentConfigResponse

// ─── var forwarders ──────────────────────────────────────────────────────────

// supportedPaymentProviders forwards hpayments.SupportedPaymentProviders so
// that payment_configs_test.go (package httpserver) can inspect the provider
// catalogue without importing hpayments directly.
var supportedPaymentProviders = hpayments.SupportedPaymentProviders

// ─── pure-function forwarders ─────────────────────────────────────────────────
// payment_configs_test.go calls these unqualified — keep the original
// lowercase names live in package httpserver so callers do not learn about
// the hpayments sub-package.

func paymentConfigFromRow(p gen.PaymentProviderConfigRow) paymentConfigResponse {
	return hpayments.PaymentConfigFromRow(p)
}

func extractStoredSecretKeys(raw json.RawMessage) []string {
	return hpayments.ExtractStoredSecretKeys(raw)
}

func computeMissingRequiredFields(provider string, secrets json.RawMessage) []string {
	return hpayments.ComputeMissingRequiredFields(provider, secrets)
}

func deriveStatus(provider string, secrets json.RawMessage) string {
	return hpayments.DeriveStatus(provider, secrets)
}

func mergeSecrets(existing json.RawMessage, patch map[string]string) (json.RawMessage, bool, error) {
	return hpayments.MergeSecrets(existing, patch)
}

// ─── payment config handler shims ─────────────────────────────────────────────

func (s *Server) handleListPaymentConfigs(w http.ResponseWriter, r *http.Request) {
	s.paymentsHandler().HandleListPaymentConfigs(w, r)
}

func (s *Server) handleGetPaymentConfig(w http.ResponseWriter, r *http.Request) {
	s.paymentsHandler().HandleGetPaymentConfig(w, r)
}

func (s *Server) handleCreatePaymentConfig(w http.ResponseWriter, r *http.Request) {
	s.paymentsHandler().HandleCreatePaymentConfig(w, r)
}

func (s *Server) handleUpdatePaymentConfig(w http.ResponseWriter, r *http.Request) {
	s.paymentsHandler().HandleUpdatePaymentConfig(w, r)
}

func (s *Server) handleVerifyPaymentConfig(w http.ResponseWriter, r *http.Request) {
	s.paymentsHandler().HandleVerifyPaymentConfig(w, r)
}

func (s *Server) handleDeletePaymentConfig(w http.ResponseWriter, r *http.Request) {
	s.paymentsHandler().HandleDeletePaymentConfig(w, r)
}

func (s *Server) handleListPaymentMethodDomains(w http.ResponseWriter, r *http.Request) {
	s.paymentsHandler().HandleListPaymentMethodDomains(w, r)
}
