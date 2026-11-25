// browser_key_probe.go — refuse a browser-side (publishable) key the provider
// does not know, before it is saved. Same philosophy as payment_config_verify.go:
// only a definite refusal blocks; a check that could not be made never does.
package hpayments

import (
	"errors"
	"net/http"

	paymodules "github.com/abhteam/arena_new/apps/backend/internal/app/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// publishableKeyRefusedMessage is what the operator reads on a 422.
const publishableKeyRefusedMessage = "Stripe does not recognise this publishable key. Copy it again from Stripe → Developers → API keys."

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

// rejectUnknownBrowserKey probes the browser key set by this patch (if any)
// and writes a 422 payment_config.publishable_key_refused when the provider
// says it does not exist. Returns true when the request was refused (the
// response is written).
func (h *Handler) rejectUnknownBrowserKey(w http.ResponseWriter, r *http.Request, provider, mode string, patch map[string]string) bool {
	d, known := descriptorFor(provider)
	if !known || !d.Capabilities.EmbeddedCheckout || d.Capabilities.EmbeddedCheckoutKey == "" {
		return false
	}
	key := patch[d.Capabilities.EmbeddedCheckoutKey]
	if key == "" {
		return false
	}
	opts := h.providerOptions
	opts.Mode = mode
	// The module is built from the patch alone: probing a browser key needs
	// no stored credential.
	module, err := paymodules.Registry().Build(d.Name, patch, opts)
	if err != nil {
		return false
	}
	prober, ok := module.(payments.BrowserKeyProber)
	if !ok {
		return false
	}
	if err := prober.ProbeBrowserKey(r.Context(), key); errors.Is(err, payments.ErrCredentialRefused) {
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
			"payment_config.publishable_key_refused", publishableKeyRefusedMessage, r,
			map[string]any{"field": d.Capabilities.EmbeddedCheckoutKey},
		))
		return true
	}
	return false
}
