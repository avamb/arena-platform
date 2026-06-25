// routing.go implements PaymentRoutingPolicy — the resolver that maps a sales
// channel's (provider, payment_mode) configuration to the correct PaymentProvider
// adapter (ADR-011: direct-merchant vs merchant-of-record).
//
// HTTP handlers that receive ErrUnknownProvider should respond with 400 Bad Request.
package payments

import (
	"fmt"
)

// ChannelConfig holds the payment-relevant fields of a sales channel that are
// needed to route a payment operation to the correct adapter. It mirrors the
// provider and payment_mode columns of the sales_channels table.
type ChannelConfig struct {
	// Provider is the payment provider name, e.g. "stripe" or "allpay".
	Provider string
	// PaymentMode is the commercial model: "direct_merchant" or "merchant_of_record".
	PaymentMode string
}

// PaymentRoutingPolicy resolves the appropriate PaymentProvider adapter for a
// given sales channel configuration. It maintains an internal registry of adapters
// keyed by provider name (as returned by PaymentProvider.ProviderName).
//
// Typical setup (application boot):
//
//	policy := payments.NewPaymentRoutingPolicy()
//	policy.Register(stripeAdapter)
//	policy.Register(allpayAdapter)
//
// Per-request resolution:
//
//	provider, err := policy.ResolveProvider(channel)
//	if errors.Is(err, payments.ErrUnknownProvider) {
//	    return 400, "unknown payment provider"
//	}
type PaymentRoutingPolicy struct {
	adapters map[string]PaymentProvider
}

// NewPaymentRoutingPolicy creates a new, empty PaymentRoutingPolicy. Call
// Register to add provider adapters before resolving.
func NewPaymentRoutingPolicy() *PaymentRoutingPolicy {
	return &PaymentRoutingPolicy{
		adapters: make(map[string]PaymentProvider),
	}
}

// Register adds a PaymentProvider adapter to the routing registry.
// The adapter is indexed by its ProviderName() value.
//
// Register panics if adapter is nil or if ProviderName() returns an empty string.
// Registering the same provider name twice overwrites the previous entry.
func (p *PaymentRoutingPolicy) Register(adapter PaymentProvider) {
	if adapter == nil {
		// allow:panic: init-time programmer-error precondition (Register is
		// only ever called from application boot wiring, never from a request
		// path; a nil adapter is a wiring bug, not a runtime condition).
		panic("payments: Register called with nil adapter")
	}
	name := adapter.ProviderName()
	if name == "" {
		// allow:panic: init-time programmer-error precondition (see comment
		// on the nil-adapter branch above).
		panic("payments: Register called with adapter whose ProviderName() is empty")
	}
	p.adapters[name] = adapter
}

// Len returns the number of registered adapters.
func (p *PaymentRoutingPolicy) Len() int {
	return len(p.adapters)
}

// ResolveProvider returns the PaymentProvider registered for ch.Provider after
// validating the channel configuration. The resolution rules are:
//
//  1. ch.PaymentMode must be "direct_merchant" or "merchant_of_record"; any
//     other value (including empty string) returns a wrapped ErrUnknownProvider.
//  2. ch.Provider must be non-empty and must match a registered adapter name;
//     otherwise ErrUnknownProvider is returned.
//
// HTTP handlers should map ErrUnknownProvider to 400 Bad Request — it indicates
// a misconfigured channel, not a transient server error.
func (p *PaymentRoutingPolicy) ResolveProvider(ch ChannelConfig) (PaymentProvider, error) {
	// Validate payment_mode first — the value comes from the sales channel DB row
	// and must be one of the two known constants.
	switch ch.PaymentMode {
	case "direct_merchant", "merchant_of_record":
		// valid — proceed
	case "":
		return nil, fmt.Errorf("%w: payment_mode is required", ErrUnknownProvider)
	default:
		return nil, fmt.Errorf("%w: unrecognised payment_mode %q (must be 'direct_merchant' or 'merchant_of_record')",
			ErrUnknownProvider, ch.PaymentMode)
	}

	// Validate provider name.
	if ch.Provider == "" {
		return nil, fmt.Errorf("%w: provider name is required", ErrUnknownProvider)
	}

	adapter, ok := p.adapters[ch.Provider]
	if !ok {
		return nil, fmt.Errorf("%w: no adapter registered for provider %q", ErrUnknownProvider, ch.Provider)
	}

	return adapter, nil
}
