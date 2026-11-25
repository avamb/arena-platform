// publishable_key_probe.go — ask Stripe whether a publishable key exists.
//
// A publishable key is public and never a credential, so nothing verifies it
// at save time; a mistyped one only surfaces in the buyer's browser. Stripe
// answers 401 to an unknown or revoked key on any endpoint, so one read of a
// token id that cannot exist tells a bad key (401) from a good one (checked
// 2026-11-25: a real pk_test_ answers 403 "cannot be made with a publishable
// key", other keys may answer 404).
//
// Only 401 is a refusal. Every other answer, a timeout and a network error
// mean "could not check" and never block a save.
package stripe

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
)

// publishableKeyProbeTimeout bounds the probe so a save never hangs on Stripe.
const publishableKeyProbeTimeout = 5 * time.Second

// probeTokenID is a token id that does not exist; Stripe answers it with 403
// or 404 to a known key and 401 to an unknown one.
const probeTokenID = "tok_arena_pk_probe"

// ProbePublishableKey returns an error wrapping payments.ErrCredentialRefused
// only when Stripe answers 401. baseURL is the API root WITH its /v1 segment;
// empty means https://api.stripe.com/v1. A nil client uses a default one.
// The key is never put into an error.
func ProbePublishableKey(ctx context.Context, client *http.Client, baseURL, publishableKey string) error {
	pk := strings.TrimSpace(publishableKey)
	if pk == "" {
		return nil
	}
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	if client == nil {
		client = &http.Client{}
	}
	ctx, cancel := context.WithTimeout(ctx, publishableKeyProbeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/tokens/"+probeTokenID, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Authorization", "Bearer "+pk)
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("%w: stripe does not recognise the publishable key", payments.ErrCredentialRefused)
	}
	return nil
}

// ProbeBrowserKey implements payments.BrowserKeyProber.
func (a *Adapter) ProbeBrowserKey(ctx context.Context, key string) error {
	return ProbePublishableKey(ctx, a.client, a.cfg.BaseURL, key)
}
