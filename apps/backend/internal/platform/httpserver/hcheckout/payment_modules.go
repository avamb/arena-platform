// payment_modules.go — how hcheckout reaches a payment provider (PAY-01,
// spec 08_architecture/36_payment_modules_refunds_acquiring_ru.md §5).
//
// hcheckout never names a provider. It asks the module registry
// (internal/app/payments) for the entry stored in a config's or an intent's
// `provider` value, reads its Descriptor (secrets, capabilities) and
// type-asserts the optional interface it needs (payments.WebhookParser here).
// tests/staticanalysis/payment_provider_literals_test.go enforces it.
package hcheckout

import (
	"context"
	"fmt"
	"net/http"

	paymodules "github.com/abhteam/arena_new/apps/backend/internal/app/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
)

// webhookParserFor builds the named provider's module from the given secrets
// and returns it as a WebhookParser, with its descriptor. ok is false for an
// unknown provider, a declared provider with no module, or a module that
// does not parse webhooks.
func webhookParserFor(provider string, secrets map[string]string) (payments.WebhookParser, payments.Descriptor, bool) {
	entry, known := paymodules.Registry().Get(provider)
	if !known || entry.New == nil {
		return nil, payments.Descriptor{}, false
	}
	module, err := entry.Build(secrets, payments.Options{})
	if err != nil {
		return nil, payments.Descriptor{}, false
	}
	parser, ok := module.(payments.WebhookParser)
	return parser, entry.Descriptor, ok
}

// configRouteOnlyProvider reports whether body is shaped like a delivery of
// a provider whose callbacks only the per-config route can verify
// (Capabilities.WebhookConfigRouteOnly: Flitt signs its callback in the body
// with the merchant's own key). Recognizing the shape needs no secret.
func configRouteOnlyProvider(body []byte) (payments.Descriptor, bool) {
	in := payments.WebhookInput{Body: body, Header: http.Header{}}
	for _, name := range paymodules.Registry().NamesWhere(func(d payments.Descriptor) bool {
		return d.Capabilities.WebhookConfigRouteOnly
	}) {
		parser, d, ok := webhookParserFor(name, nil)
		if ok && parser.RecognizesWebhook(in) {
			return d, true
		}
	}
	return payments.Descriptor{}, false
}

// legacyWebhookProviders lists, in registry order, the providers whose
// deliveries the LEGACY un-suffixed webhook route can authenticate: a module
// that parses webhooks and does not demand the per-config route.
func legacyWebhookProviders() []string {
	out := []string{}
	for _, name := range paymodules.Registry().NamesWhere(func(d payments.Descriptor) bool {
		return !d.Capabilities.WebhookConfigRouteOnly && d.WebhookSecretKey() != ""
	}) {
		if _, _, ok := webhookParserFor(name, nil); ok {
			out = append(out, name)
		}
	}
	return out
}

// verifyLegacyWebhook is the legacy route's signature check, provider by
// provider in registry order: the first provider that holds a secret (the
// organization's own config first, else the platform's process-env secret)
// AND recognizes the request (its signature header) decides. With no secret
// at all the request passes (dev/mock mode; production config validation
// forbids it). With secrets but no recognized header it is refused.
func verifyLegacyWebhook(ctx context.Context, r *http.Request, body []byte, orgSecrets, envSecrets map[string]string) error {
	in := payments.WebhookInput{Body: body, Header: r.Header}
	anySecret := false
	for _, name := range legacyWebhookProviders() {
		secret := orgSecrets[name]
		if secret == "" {
			secret = envSecrets[name]
		}
		if secret == "" {
			continue
		}
		anySecret = true
		entry, _ := paymodules.Registry().Get(name)
		parser, _, ok := webhookParserFor(name, map[string]string{entry.Descriptor.WebhookSecretKey(): secret})
		if !ok || !parser.RecognizesWebhook(in) {
			continue
		}
		_, err := parser.VerifyAndParse(ctx, in)
		return err
	}
	if !anySecret {
		return nil
	}
	return fmt.Errorf(
		"%w: no provider signature header present (expected Stripe-Signature or X-AllPay-Signature)",
		payments.ErrInvalidWebhookSignature,
	)
}
