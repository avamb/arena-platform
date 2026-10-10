// channel_provider.go — a sales channel's `provider` is checked against the
// payment module registry (spec 36 §5, PAY-02), never against a hand-kept
// list. Migration 0132 dropped sales_channels_provider_check for the same
// reason: a new provider is one module plus one line in
// internal/app/payments/modules.go, no migration and no edit here.
package hcatalog

import (
	"fmt"
	"strings"

	paymodules "github.com/abhteam/arena_new/apps/backend/internal/app/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
)

// channelProviderRegistry is the registry the handlers validate against.
// Tests use ValidateChannelProviderWith with their own registry instead.
var channelProviderRegistry = paymodules.Registry()

// ValidateChannelProvider returns "" when provider may be stored in
// sales_channels.provider, otherwise the operator-facing message the
// handlers wrap in 400 channel.invalid_config.
func ValidateChannelProvider(provider string) string {
	return ValidateChannelProviderWith(channelProviderRegistry, provider)
}

// ValidateChannelProviderWith is ValidateChannelProvider against reg.
func ValidateChannelProviderWith(reg *payments.Registry, provider string) string {
	if strings.TrimSpace(provider) == "" {
		return "provider is required"
	}
	if reg.IsModule(provider) {
		return ""
	}
	return fmt.Sprintf("provider must be %s, got %q", quotedAlternatives(reg.ModuleNames()), provider)
}

// quotedAlternatives renders ["a","b","c"] as "'a', 'b' or 'c'".
func quotedAlternatives(names []string) string {
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = "'" + n + "'"
	}
	switch len(quoted) {
	case 0:
		return "a registered payment provider"
	case 1:
		return quoted[0]
	default:
		return strings.Join(quoted[:len(quoted)-1], ", ") + " or " + quoted[len(quoted)-1]
	}
}
