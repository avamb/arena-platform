package holdpolicy

import (
	"crypto/sha256"
	"encoding/json"
	"time"
)

// HashHoldToken is the reservations.hold_token_hash of a public hold key
// (spec 67 section 2): the SHA-256 of the key string exactly as the widget
// sends it. Only the hash is stored.
func HashHoldToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// FromSnapshot reads the reservations.hold_policy snapshot of a hold. An
// empty or unreadable snapshot (a hold of the old path) yields Default().
func FromSnapshot(raw []byte) Policy {
	if len(raw) == 0 || string(raw) == "null" {
		return Default()
	}
	var p Policy
	if err := json.Unmarshal(raw, &p); err != nil || p.Validate() != nil {
		return Default()
	}
	return p
}

// MinProviderPageLifetime is the shortest life of a provider payment page
// arena creates for a hold (Stripe refuses a Checkout Session that expires
// sooner than 30 minutes), plus one minute for the time the request takes to
// reach the provider: the widget window of the old path keeps the same margin
// (1860 s).
const MinProviderPageLifetime = 31 * time.Minute

// PageLifetime is how long the provider payment page of a hold lives from
// its creation (spec 67 section 3.3): max(30 minutes, payment + extend x
// extend_count + paying), the floor with the margin of
// MinProviderPageLifetime. arena closes the page itself when the hold ends.
func (p Policy) PageLifetime() time.Duration {
	d := p.Payment + p.Extend*time.Duration(p.ExtendCount) + p.Paying
	if d < MinProviderPageLifetime {
		return MinProviderPageLifetime
	}
	return d
}
