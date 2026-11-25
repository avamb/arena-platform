// payment_method_domains.go — the optional contract of a provider that needs
// the page's domain registered before a wallet (Apple Pay, Google Pay) works
// there (spec 65 section 5). Stripe registers it in the organization's OWN
// account, so the call is made with that organization's key.
package payments

import "context"

// PaymentMethodDomain is the provider's answer about one registered domain.
// A status is the provider's own lower-case word ("active", "inactive"); an
// empty one means the provider did not say.
type PaymentMethodDomain struct {
	// ProviderID is the provider's id of the registration (pmd_...).
	ProviderID string
	// Domain is the host as the provider holds it.
	Domain  string
	Enabled bool
	// Per-wallet statuses.
	ApplePay  string
	GooglePay string
	Link      string
	// Detail is the provider's explanation of an inactive wallet, if any.
	Detail string
}

// PaymentMethodDomainRegistrar registers a domain for wallets and reads back
// its status. It is idempotent: a registered domain is looked up first and
// re-validated only while a wallet is not active.
//
// A 401/403 from the provider is reported as ErrCredentialRefused (wrapped).
type PaymentMethodDomainRegistrar interface {
	EnsurePaymentMethodDomain(ctx context.Context, domain string) (PaymentMethodDomain, error)
}
