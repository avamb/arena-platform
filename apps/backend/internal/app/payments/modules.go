// modules.go — THE list of payment providers arena knows (spec
// 08_architecture/36_payment_modules_refunds_acquiring_ru.md §5, PAY-01).
//
// This is the only place in the codebase where provider names appear as a
// list. The handler packages (hcheckout, hfeed, hpayments, htickets,
// eventbot) never name a provider: they ask Registry() for the entry stored
// in a channel's or a config's `provider` value and act on its Descriptor
// (secrets, capabilities) and on the optional interfaces the built module
// implements. tests/staticanalysis/payment_provider_literals_test.go fails on
// a provider-name literal there.
//
// The list lives here and not in internal/domain/payments because the
// adapter packages import the domain package (they implement its
// interfaces); the domain package holds the mechanism (Registry, Entry,
// Descriptor), this file holds the wiring.
//
// Adding a provider: a package under internal/adapters/<provider> with a
// Descriptor, a Factory and whichever optional interfaces it implements, a
// contract test (domain/payments/contracttest.Run) in that package, and ONE
// line below. Nothing else in the core changes.
package payments

import (
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/allpay"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/flitt"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/stripe"
	domain "github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
)

// Modules lists the registered providers in a fixed order. The order is
// observable (error messages that list "supported" providers follow it), so
// append, do not reorder.
func Modules() []domain.Entry {
	return []domain.Entry{
		stripe.Entry(),
		flitt.Entry(),
		allpay.Entry(),
		// Declared only: an organization may store a configuration for
		// these (payment_provider_configs accepted them before PAY-01), but
		// arena has no module behind them, so nothing can be done with it.
		domain.Declared(domain.Descriptor{
			Name:  "cloudpayments",
			Title: "CloudPayments",
			Secrets: []domain.SecretField{
				{Key: "public_id", Label: "CloudPayments public id", Required: true, Credential: true},
				{Key: "api_secret", Label: "CloudPayments API secret", Required: true, Credential: true, Hidden: true},
			},
		}),
		domain.Declared(domain.Descriptor{
			Name:  "yookassa",
			Title: "YooKassa",
			Secrets: []domain.SecretField{
				{Key: "shop_id", Label: "YooKassa shop id", Required: true, Credential: true},
				{Key: "secret_key", Label: "YooKassa secret key", Required: true, Credential: true, Hidden: true},
			},
		}),
		// manual: money taken by the seller outside arena (the Bil24
		// gateway's sites). No credentials, no module — a config row for it
		// is "configured" with nothing stored.
		domain.Declared(domain.Descriptor{Name: "manual", Title: "Manual (paid outside arena)"}),
	}
}

var registry = domain.MustRegistry(Modules()...)

// Registry is the process-wide registry built from Modules. It holds
// descriptors and factories only — no credentials, no connections — so one
// shared value is safe; every request builds its own module from its own
// configuration's secrets.
func Registry() *domain.Registry { return registry }
