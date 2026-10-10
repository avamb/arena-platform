// channels.go — which provider a SALES CHANNEL may name (spec 36 §5, PAY-02).
//
// Until migration 0132 the list lived three times: the
// sales_channels_provider_check constraint and two hand-kept copies in
// hcatalog (create and PATCH), plus a fourth guess in provisioning. Each new
// provider needed a migration and four edits, and a missed copy failed only
// at the database. Now the rule is the registry: a channel may name any
// provider with a module behind it (not merely Declared), spelled exactly as
// its descriptor names it. The database keeps only a non-empty check.
package payments

import (
	"strings"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/stripe"
	domain "github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
)

// DefaultChannelProvider is the provider a new sales channel gets when its
// creator names none — what the old `provider` column default and the
// handlers' fallback both said.
func DefaultChannelProvider() string { return stripe.Name }

// ChannelProviders lists the values a sales channel may store, in
// registration order.
func ChannelProviders() []string { return registry.ModuleNames() }

// IsChannelProvider reports whether name may be stored in
// sales_channels.provider.
func IsChannelProvider(name string) bool { return registry.IsModule(name) }

// ChannelProviderForChoice maps a free-form provider choice (the onboarding
// form's answer) to the provider a new channel starts on. A choice that names
// a module able to render a hosted payment page is taken as is (accepted =
// true); anything else — "other", "undecided", a provider arena has no
// hosted page for — starts on DefaultChannelProvider and the owner changes it
// later.
func ChannelProviderForChoice(choice string) (provider string, accepted bool) {
	return channelProviderForChoice(registry, choice)
}

func channelProviderForChoice(reg *domain.Registry, choice string) (string, bool) {
	name := strings.TrimSpace(choice)
	if reg.IsModule(name) {
		if e, _ := reg.Get(name); e.Descriptor.Capabilities.HostedCheckout {
			return name, true
		}
	}
	return DefaultChannelProvider(), false
}
