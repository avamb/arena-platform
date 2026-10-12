package refunds

import (
	"context"

	"github.com/google/uuid"
)

type providerConfigKey struct{}

// WithProviderConfig names the payment configuration a provider call must be
// made with. A refund webhook carries the configuration whose URL and secret
// verified it: reading the refund back with another configuration of the same
// organization (a live key for a test refund) would find nothing.
func WithProviderConfig(ctx context.Context, configID uuid.UUID) context.Context {
	return context.WithValue(ctx, providerConfigKey{}, configID)
}

// ProviderConfigFromContext returns the configuration named by
// WithProviderConfig, if any. A ModuleSource builds from it when present.
func ProviderConfigFromContext(ctx context.Context) (uuid.UUID, bool) {
	id, ok := ctx.Value(providerConfigKey{}).(uuid.UUID)
	return id, ok && id != uuid.Nil
}
