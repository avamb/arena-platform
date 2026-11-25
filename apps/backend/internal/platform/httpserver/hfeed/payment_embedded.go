// payment_embedded.go — the embedded checkout (spec 65 §3, stage E2): the
// buyer pays inside the widget with the provider's browser SDK (Stripe.js
// Elements) instead of being sent to the provider's hosted page.
//
// When a payment is embedded. ALL of these must allow it, otherwise the
// hosted page is created exactly as before:
//
//  1. the deployment switch STRIPE_ELEMENTS_ENABLED (Handler.embeddedCheckout);
//  2. the widget says it can run the form: checkout/start carries
//     capabilities ["stripe_elements"] — an older widget sends nothing;
//  3. the channel does not force the hosted page:
//     settings.checkout.stripe_ui is "auto" (the default; anything but
//     "hosted" reads as auto);
//  4. the provider module declares Capabilities.EmbeddedCheckout and the
//     organization's config holds its public browser key (Stripe
//     publishable_key).
//
// What stays as it is: the provider is called after the hold transaction
// commits, the session id (cs_…) is payment_intents.provider_payment_id, and
// only the provider's "session completed and paid" webhook pays the order.
// The client secret is handed to the buyer's browser and never stored or
// logged; the status endpoint reads it back from the provider.
package hfeed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
)

// CapabilityStripeElements is the checkout/start capability of a widget
// that can run the embedded Stripe form.
const CapabilityStripeElements = "stripe_elements"

// Channel setting settings.checkout.stripe_ui.
const (
	// checkoutUIAuto: embedded when everything else allows it (default).
	checkoutUIAuto = "auto"
	// checkoutUIHosted: always the hosted page (an operator's switch for a
	// channel with a problem).
	checkoutUIHosted = "hosted"
)

// embeddedIdempotencySuffix keeps the embedded session's provider
// idempotency key apart from the hosted one of the same checkout: a hosted
// page created after a refused embedded attempt must not be answered with
// the embedded session.
const embeddedIdempotencySuffix = ":embedded"

// hostedFallbackIdempotencySuffix is the key suffix of the hosted page that
// replaces an embedded session (POST /v1/public/checkout/{token}/hosted).
const hostedFallbackIdempotencySuffix = ":hosted"

// ErrEmbeddedCheckoutUnavailable means this payment cannot be embedded
// (the channel forces the hosted page, the provider has no embedded mode, or
// the config holds no browser key); the caller creates the hosted page.
var ErrEmbeddedCheckoutUnavailable = errors.New("hfeed: the embedded checkout is not available for this payment")

// EmbeddedPaymentBlock is the `payment` object of checkout/start and of the
// order status: what the widget initialises the provider's SDK with.
type EmbeddedPaymentBlock struct {
	Provider       string `json:"provider"`
	Mode           string `json:"mode"`
	ClientSecret   string `json:"client_secret"`
	PublishableKey string `json:"publishable_key"`
}

// EmbeddedCheckoutResult is the provider's answer to an embedded session.
type EmbeddedCheckoutResult struct {
	SessionID string
	Provider  string
	// Block is what the widget gets. Its ClientSecret is never stored.
	Block EmbeddedPaymentBlock
}

// EmbeddedPaymentStarter is the optional extension of PaymentStarter that
// creates, reads and closes embedded sessions. A starter without it (test
// stubs) only ever creates hosted pages.
type EmbeddedPaymentStarter interface {
	// StartEmbeddedCheckout creates an embedded session after the hold
	// committed. ErrEmbeddedCheckoutUnavailable: create the hosted page.
	StartEmbeddedCheckout(ctx context.Context, req HostedCheckoutRequest) (*EmbeddedCheckoutResult, error)
	// ReadEmbeddedCheckout reads a session back and returns the block the
	// widget needs while it is open (empty ClientSecret otherwise).
	ReadEmbeddedCheckout(ctx context.Context, orgID, channelID uuid.UUID, provider, sessionID string) (payments.EmbeddedCheckoutSession, EmbeddedPaymentBlock, error)
	// ExpireEmbeddedCheckout closes a session; a definitive refusal wraps
	// payments.ErrCheckoutSessionNotExpirable.
	ExpireEmbeddedCheckout(ctx context.Context, orgID, channelID uuid.UUID, provider, sessionID string) error
}

var _ EmbeddedPaymentStarter = (*StripePaymentStarter)(nil)

// wantsEmbeddedCheckout reports whether the widget announced it can run the
// embedded form.
func wantsEmbeddedCheckout(capabilities []string) bool {
	for _, c := range capabilities {
		if strings.EqualFold(strings.TrimSpace(c), CapabilityStripeElements) {
			return true
		}
	}
	return false
}

// channelForcesHostedCheckout reads settings.checkout.stripe_ui. Only the
// exact value "hosted" forces the hosted page; absent, "auto", an unknown
// value or unreadable settings all mean auto (owner decision 2026-11-22:
// the embedded form is on by default).
func channelForcesHostedCheckout(settings []byte) bool {
	if len(settings) == 0 {
		return false
	}
	var s struct {
		Checkout struct {
			StripeUI string `json:"stripe_ui"`
		} `json:"checkout"`
	}
	if err := json.Unmarshal(settings, &s); err != nil {
		return false
	}
	ui := strings.ToLower(strings.TrimSpace(s.Checkout.StripeUI))
	if ui == checkoutUIAuto {
		return false
	}
	return ui == checkoutUIHosted
}

// embeddedModule resolves the channel's provider and the organization's
// config exactly as the hosted page does (resolveConfig) and returns the
// provider's embedded-checkout module with the public browser key. forNew
// is true for a NEW session: the channel's stripe_ui setting then applies;
// reading or closing an existing session ignores it.
func (s *StripePaymentStarter) embeddedModule(ctx context.Context, orgID, channelID uuid.UUID, forNew bool) (payments.EmbeddedCheckoutProvider, hostedPaymentConfig, string, error) {
	cfg, err := s.resolveConfig(ctx, orgID, channelID)
	if err != nil {
		return nil, hostedPaymentConfig{}, "", err
	}
	caps := cfg.entry.Descriptor.Capabilities
	if !caps.EmbeddedCheckout || caps.EmbeddedCheckoutKey == "" {
		return nil, cfg, "", ErrEmbeddedCheckoutUnavailable
	}
	if forNew && channelForcesHostedCheckout(cfg.channelSettings) {
		return nil, cfg, "", ErrEmbeddedCheckoutUnavailable
	}
	key := strings.TrimSpace(cfg.secrets[caps.EmbeddedCheckoutKey])
	if forNew && key == "" {
		return nil, cfg, "", ErrEmbeddedCheckoutUnavailable
	}
	opts := s.opts
	opts.Mode = cfg.mode
	module, err := cfg.entry.Build(cfg.secrets, opts)
	if err != nil {
		return nil, cfg, "", fmt.Errorf("%s embedded checkout (config %s, mode %s): %w", cfg.entry.Descriptor.Name, cfg.configID, cfg.mode, err)
	}
	emb, ok := module.(payments.EmbeddedCheckoutProvider)
	if !ok {
		return nil, cfg, "", ErrEmbeddedCheckoutUnavailable
	}
	return emb, cfg, key, nil
}

// StartEmbeddedCheckout implements EmbeddedPaymentStarter.
func (s *StripePaymentStarter) StartEmbeddedCheckout(ctx context.Context, req HostedCheckoutRequest) (*EmbeddedCheckoutResult, error) {
	if req.ShopOrderID != nil {
		// The shop keeps the hosted page (spec 65: out of scope).
		return nil, ErrEmbeddedCheckoutUnavailable
	}
	emb, cfg, key, err := s.embeddedModule(ctx, req.OrgID, req.ChannelID, true)
	if err != nil {
		return nil, err
	}
	provider := cfg.entry.Descriptor.Name
	metadata := map[string]string{
		"arena_checkout_session_id": req.CheckoutSessionID.String(),
		"arena_org_id":              req.OrgID.String(),
	}
	if req.OrderID != nil {
		metadata["arena_order_id"] = req.OrderID.String()
	}
	resp, err := emb.CreateEmbeddedCheckoutSession(ctx, payments.CreateHostedCheckoutRequest{
		Amount:            req.Amount,
		Currency:          req.Currency,
		ProductName:       req.ProductName,
		CustomerEmail:     req.BuyerEmail,
		ReceiptEmail:      s.receiptEmail(ctx, req.OrgID, req.BuyerEmail),
		CustomerName:      strings.TrimSpace(req.BuyerName),
		CustomerPhone:     strings.TrimSpace(req.BuyerPhone),
		ClientReferenceID: req.reference(),
		Metadata:          metadata,
		// 3-D Secure on the bank's own page comes back through arena's
		// payment-return hop to the buyer's page with the checkout_token
		// (spec 65 §3.2); the widget then polls the status as today.
		ReturnURL:      PaymentReturnURL(s.callbackBase, req.ReturnURL, req.CheckoutToken),
		ExpiresAtUnix:  req.ExpiresAtUnix,
		Locale:         req.BuyerLocale,
		IdempotencyKey: req.reference() + embeddedIdempotencySuffix,
	})
	if err != nil {
		return nil, fmt.Errorf("%s embedded checkout (config %s, mode %s): %w", provider, cfg.configID, cfg.mode, err)
	}
	return &EmbeddedCheckoutResult{
		SessionID: resp.SessionID,
		Provider:  provider,
		Block: EmbeddedPaymentBlock{
			Provider:       provider,
			Mode:           payments.CheckoutUIModeEmbedded,
			ClientSecret:   resp.ClientSecret,
			PublishableKey: key,
		},
	}, nil
}

// EmbeddedCheckoutAvailable reports, without calling the provider, whether a
// NEW payment of this channel would be embedded: nil when the provider module
// has the embedded mode, the config holds its browser key and the channel
// does not force the hosted page; ErrEmbeddedCheckoutUnavailable or the
// config error otherwise. The prepared order (spec 65) asks it before any
// hold, because it never falls back to the hosted page.
func (s *StripePaymentStarter) EmbeddedCheckoutAvailable(ctx context.Context, orgID, channelID uuid.UUID) error {
	_, _, _, err := s.embeddedModule(ctx, orgID, channelID, true)
	return err
}

// ReadEmbeddedCheckout implements EmbeddedPaymentStarter.
func (s *StripePaymentStarter) ReadEmbeddedCheckout(ctx context.Context, orgID, channelID uuid.UUID, provider, sessionID string) (payments.EmbeddedCheckoutSession, EmbeddedPaymentBlock, error) {
	emb, cfg, key, err := s.embeddedModule(ctx, orgID, channelID, false)
	if err != nil {
		return payments.EmbeddedCheckoutSession{}, EmbeddedPaymentBlock{}, err
	}
	if !strings.EqualFold(cfg.entry.Descriptor.Name, provider) {
		return payments.EmbeddedCheckoutSession{}, EmbeddedPaymentBlock{}, ErrEmbeddedCheckoutUnavailable
	}
	sess, err := emb.RetrieveEmbeddedCheckoutSession(ctx, sessionID)
	if err != nil {
		return payments.EmbeddedCheckoutSession{}, EmbeddedPaymentBlock{}, err
	}
	block := EmbeddedPaymentBlock{
		Provider:       cfg.entry.Descriptor.Name,
		Mode:           payments.CheckoutUIModeEmbedded,
		ClientSecret:   sess.ClientSecret,
		PublishableKey: key,
	}
	return sess, block, nil
}

// ExpireEmbeddedCheckout implements EmbeddedPaymentStarter.
func (s *StripePaymentStarter) ExpireEmbeddedCheckout(ctx context.Context, orgID, channelID uuid.UUID, provider, sessionID string) error {
	emb, cfg, _, err := s.embeddedModule(ctx, orgID, channelID, false)
	if err != nil {
		return err
	}
	if !strings.EqualFold(cfg.entry.Descriptor.Name, provider) {
		return ErrEmbeddedCheckoutUnavailable
	}
	return emb.ExpireEmbeddedCheckoutSession(ctx, sessionID)
}
