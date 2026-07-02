// bank_accounts_shims.go bridges the *Server god-object to the hbankaccounts
// sub-package (feature #255). All handler and validation logic lives in
// hbankaccounts/; these thin delegating methods preserve the unexported
// *Server method surface so test files and mount files (mount_iam.go)
// compile unchanged.
package httpserver

import (
	"net/http"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hbankaccounts"
)

// bankAccountsHandler constructs a hbankaccounts.Handler from the server's
// dependencies.
func (s *Server) bankAccountsHandler() *hbankaccounts.Handler {
	return hbankaccounts.New(
		s.bankAccountQueries,
		s.pool,
		s.audit,
		s.logger,
	)
}

// ─── type aliases ─────────────────────────────────────────────────────────────
// These let test files in package httpserver reference types that live in
// hbankaccounts without importing that package directly.

type bankAccountResponse = hbankaccounts.BankAccountResponse

// ─── pure-function forwarders ─────────────────────────────────────────────────

func bankAccountFromRow(b gen.OrganizationBankAccountRow) bankAccountResponse {
	return hbankaccounts.BankAccountFromRow(b)
}

// ─── bank account handler shims ───────────────────────────────────────────────

func (s *Server) handleListBankAccounts(w http.ResponseWriter, r *http.Request) {
	s.bankAccountsHandler().HandleListBankAccounts(w, r)
}

func (s *Server) handleCreateBankAccount(w http.ResponseWriter, r *http.Request) {
	s.bankAccountsHandler().HandleCreateBankAccount(w, r)
}

func (s *Server) handleUpdateBankAccount(w http.ResponseWriter, r *http.Request) {
	s.bankAccountsHandler().HandleUpdateBankAccount(w, r)
}

func (s *Server) handleDeleteBankAccount(w http.ResponseWriter, r *http.Request) {
	s.bankAccountsHandler().HandleDeleteBankAccount(w, r)
}
