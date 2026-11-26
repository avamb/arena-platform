// browser_key_probe.go — refuse a browser-side (publishable) key the provider
// does not know, before it is saved. Same philosophy as payment_config_verify.go:
// only a definite refusal blocks; a check that could not be made never does.
package hpayments

import (
	"encoding/json"
	"errors"
	"net/http"

	paymodules "github.com/abhteam/arena_new/apps/backend/internal/app/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// publishableKeyRefusedMessage is what the operator reads on a 422.
const publishableKeyRefusedMessage = "Stripe does not recognise this publishable key. Copy it again from Stripe → Developers → API keys."

// publishableKeyMismatchMessage is what the operator reads when the two keys
// come from different Stripe accounts.
const publishableKeyMismatchMessage = "This publishable key belongs to another Stripe account than the secret key. Copy both keys from the same Stripe account."

// patchTouchesCredential reports whether a secrets patch sets or deletes a
// key the provider's descriptor marks as Credential. An unknown provider
// counts as touched, so the old behaviour (always verify) stays there.
func patchTouchesCredential(provider string, patch map[string]string) bool {
	d, known := descriptorFor(provider)
	if !known {
		return true
	}
	for _, key := range d.CredentialKeys() {
		if _, ok := patch[key]; ok {
			return true
		}
	}
	return false
}

// patchSetsCredential reports whether a secrets patch sets a credential key
// to a new non-empty value (a deletion does not count: there is no new
// secret key to compare the stored browser key with).
func patchSetsCredential(d payments.Descriptor, patch map[string]string) bool {
	for _, k := range d.CredentialKeys() {
		if patch[k] != "" {
			return true
		}
	}
	return false
}

// rejectUnknownBrowserKey probes the browser key set by this patch (if any)
// and writes a 422 payment_config.publishable_key_refused when the provider
// says it does not exist. Returns true when the request was refused (the
// response is written).
func (h *Handler) rejectUnknownBrowserKey(w http.ResponseWriter, r *http.Request, provider, mode string, patch map[string]string, stored json.RawMessage) bool {
	d, known := descriptorFor(provider)
	if !known || !d.Capabilities.EmbeddedCheckout || d.Capabilities.EmbeddedCheckoutKey == "" {
		return false
	}
	// The secret key may come from the patch or from the stored row.
	merged := map[string]string{}
	if len(stored) > 0 {
		var cur map[string]any
		if json.Unmarshal(stored, &cur) == nil {
			for k, v := range cur {
				if s, ok := v.(string); ok {
					merged[k] = s
				}
			}
		}
	}
	for k, v := range patch {
		if v == "" {
			delete(merged, k)
		} else {
			merged[k] = v
		}
	}
	// Probe when the patch sets the browser key, and also when it replaces
	// the secret key while a browser key is stored: a new sk of another
	// account would break the embedded form just the same.
	key := patch[d.Capabilities.EmbeddedCheckoutKey]
	if key == "" && patchSetsCredential(d, patch) {
		key = merged[d.Capabilities.EmbeddedCheckoutKey]
	}
	if key == "" {
		return false
	}
	opts := h.providerOptions
	opts.Mode = mode
	module, err := paymodules.Registry().Build(d.Name, merged, opts)
	if err != nil {
		return false
	}
	prober, ok := module.(payments.BrowserKeyProber)
	if !ok {
		return false
	}
	err = prober.ProbeBrowserKey(r.Context(), key, merged)
	switch {
	case errors.Is(err, payments.ErrCredentialRefused):
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
			"payment_config.publishable_key_refused", publishableKeyRefusedMessage, r,
			map[string]any{"field": d.Capabilities.EmbeddedCheckoutKey},
		))
		return true
	case errors.Is(err, payments.ErrBrowserKeyAccountMismatch):
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
			"payment_config.publishable_key_account_mismatch", publishableKeyMismatchMessage, r,
			map[string]any{"field": d.Capabilities.EmbeddedCheckoutKey},
		))
		return true
	}
	return false
}
