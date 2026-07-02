// payments_shims.go bridges the *Server god-object to the hpayments
// sub-package. All handler and validation logic lives in hpayments/; these
// thin delegating methods preserve the unexported *Server method surface so
// test files and mount files (mount_iam.go) compile unchanged.
package httpserver

import (
	"encoding/json"
	"net/http"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hpayments"
)

// paymentsHandler constructs a hpayments.Handler from the server's dependencies.
func (s *Server) paymentsHandler() *hpayments.Handler {
	return hpayments.New(
		s.paymentConfigQueries,
		s.pool,
		s.audit,
		s.logger,
	)
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

func (s *Server) handleDeletePaymentConfig(w http.ResponseWriter, r *http.Request) {
	s.paymentsHandler().HandleDeletePaymentConfig(w, r)
}
