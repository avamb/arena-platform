// modules.go — the module REGISTRY (spec 36 §5, PAY-01).
//
// A module is constructed PER REQUEST from one organization's own secrets
// (every organizer holds their own provider account), so the registry holds
// factories, not instances. Options carries the deployment-level test seams
// (stub endpoints) the factories honour.
//
// This file holds the mechanism only. The LIST of connected modules — the one
// place a provider name may appear in a list — is
// internal/app/payments/modules.go: it imports the adapter packages, which
// this package cannot (they implement its interfaces).
package payments

import (
	"errors"
	"fmt"
	"strings"
)

// Options carries the deployment-level test seams a factory honours. Empty
// values mean the real provider endpoints; config.Validate refuses the
// corresponding environment variables in production.
type Options struct {
	// StripeAPIBaseURL overrides https://api.stripe.com/v1 (must carry the
	// /v1 segment). httpserver.Options.StripeAPIBaseURL, then
	// STRIPE_API_BASE_URL.
	StripeAPIBaseURL string
	// FlittAPIBaseURL overrides https://pay.flitt.com/api.
	// FLITT_API_BASE_URL in a deployed process; tests set it directly.
	FlittAPIBaseURL string
}

// Factory builds a module for one configuration. secrets is the config's
// secrets blob as a flat map (SecretsFromJSON); a missing value is NOT an
// error here — the module is built and the caller checks the descriptor's
// Required/Credential keys for what it is about to do, so a module can
// always be built to RecognizesWebhook a body or to read its descriptor.
type Factory func(secrets map[string]string, opts Options) (Module, error)

// Entry is one registered provider: its static descriptor plus, for a
// provider with code behind it, the factory. A declared provider (one the
// admin may configure but arena has no module for yet) has a nil factory.
type Entry struct {
	Descriptor Descriptor
	New        Factory
}

// ErrModuleNotImplemented is returned by Build for a provider that is
// declared but has no module yet.
var ErrModuleNotImplemented = errors.New("payments: provider is declared but has no module")

// Declared registers a provider that has a descriptor and no code: the admin
// may store its configuration, nothing can be done with it.
func Declared(d Descriptor) Entry {
	return Entry{Descriptor: d}
}

// Implemented reports whether the entry has a module behind it, as opposed
// to a provider that is only Declared.
func (e Entry) Implemented() bool { return e.New != nil }

// Build constructs the module.
func (e Entry) Build(secrets map[string]string, opts Options) (Module, error) {
	if e.New == nil {
		return nil, fmt.Errorf("%w: %s", ErrModuleNotImplemented, e.Descriptor.Name)
	}
	if secrets == nil {
		secrets = map[string]string{}
	}
	m, err := e.New(secrets, opts)
	if err != nil {
		return nil, err
	}
	if m == nil {
		return nil, fmt.Errorf("payments: factory for %s returned no module", e.Descriptor.Name)
	}
	return m, nil
}

// Registry is the set of known providers, in registration order.
type Registry struct {
	entries map[string]Entry
	order   []string
}

// NewRegistry builds a registry from entries, refusing an invalid
// descriptor or a duplicate name.
func NewRegistry(entries ...Entry) (*Registry, error) {
	r := &Registry{entries: map[string]Entry{}}
	for _, e := range entries {
		if err := r.Register(e); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// MustRegistry is NewRegistry for application wiring, where an invalid
// module list is a programming error caught by the first test run.
func MustRegistry(entries ...Entry) *Registry {
	r, err := NewRegistry(entries...)
	if err != nil {
		// allow:panic: init-time programmer-error precondition (the module
		// list is static, built once at boot, and validated by every test
		// that touches payments; never reached from a request path).
		panic(err)
	}
	return r
}

// Register adds an entry. The descriptor must validate and the name must be
// new; registration order is preserved and is the order Names reports.
func (r *Registry) Register(e Entry) error {
	if err := e.Descriptor.Validate(); err != nil {
		return err
	}
	name := e.Descriptor.Name
	if _, dup := r.entries[name]; dup {
		return fmt.Errorf("payments: provider %q registered twice", name)
	}
	r.entries[name] = e
	r.order = append(r.order, name)
	return nil
}

// Get looks a provider up by name (trimmed, case-insensitive).
func (r *Registry) Get(name string) (Entry, bool) {
	if r == nil {
		return Entry{}, false
	}
	e, ok := r.entries[NormalizeProviderName(name)]
	return e, ok
}

// Names lists the registered providers in registration order.
func (r *Registry) Names() []string {
	if r == nil {
		return nil
	}
	out := make([]string, len(r.order))
	copy(out, r.order)
	return out
}

// NamesWhere lists the providers whose descriptor satisfies keep, in
// registration order.
func (r *Registry) NamesWhere(keep func(Descriptor) bool) []string {
	if r == nil {
		return nil
	}
	out := make([]string, 0, len(r.order))
	for _, name := range r.order {
		if keep(r.entries[name].Descriptor) {
			out = append(out, name)
		}
	}
	return out
}

// ModuleNames lists the providers that have a module behind them (not merely
// Declared), in registration order.
func (r *Registry) ModuleNames() []string {
	if r == nil {
		return nil
	}
	out := make([]string, 0, len(r.order))
	for _, name := range r.order {
		if r.entries[name].Implemented() {
			out = append(out, name)
		}
	}
	return out
}

// IsModule reports whether name is EXACTLY (no case folding, no trimming) the
// canonical name of a provider with a module behind it. This is the rule for
// a value arena stores and later acts on — sales_channels.provider — which
// replaced the sales_channels_provider_check list in migration 0132 (PAY-02):
// a new module joins by its registration alone, no migration.
func (r *Registry) IsModule(name string) bool {
	e, ok := r.Get(name)
	return ok && e.Descriptor.Name == name && e.Implemented()
}

// Build looks the provider up and constructs its module. An unknown name is
// ErrUnknownProvider (wrapped).
func (r *Registry) Build(name string, secrets map[string]string, opts Options) (Module, error) {
	e, ok := r.Get(name)
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownProvider, strings.TrimSpace(name))
	}
	return e.Build(secrets, opts)
}
