// credentials.go — "does the provider actually accept this key?"
//
// Arena stores a provider credential the moment an operator saves it, and
// until now the only thing that ever tried it was a buyer's checkout: a
// wrong key surfaced as a failed sale, hours later, to the one person who
// could do nothing about it. A live organization lost a day that way
// (2026-09-21) with an admin screen that showed the configuration as ready
// throughout, because "ready" only meant the field was not empty.
//
// This is a deliberately separate, narrow interface rather than another
// method on PaymentProvider: not every provider exposes a cheap read-only
// call that proves a credential, and widening PaymentProvider would force
// every adapter to grow a method it cannot honestly implement. Callers
// type-assert and treat a provider that does not implement it as "cannot be
// checked", which is not the same as "failed".
package payments

import (
	"context"
	"errors"
)

// The two outcomes a failed verification can have, which callers MUST keep
// apart. Refused is a fact about the credential and is worth showing an
// operator as a red state; unreachable is a fact about the network and says
// nothing about the key — recording it as a failure would teach operators to
// distrust a badge that is only reporting our own connectivity.
var (
	ErrCredentialRefused   = errors.New("payments: provider refused the credential")
	ErrProviderUnreachable = errors.New("payments: provider could not be reached")
)

// CredentialVerifier is implemented by providers that can confirm a stored
// credential without moving money.
type CredentialVerifier interface {
	// VerifyCredentials sends the configured credential to the provider on a
	// read-only call and reports what came back.
	//
	// nil means the provider accepted it. A non-nil error means either a
	// refusal (wrong or revoked key) or that the provider could not be
	// reached — callers that record a verdict must distinguish the two, since
	// an unreachable provider says nothing about the key and must not be
	// recorded as a failure of it.
	VerifyCredentials(ctx context.Context) error
}
