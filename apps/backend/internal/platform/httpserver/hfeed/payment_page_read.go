// payment_page_read.go — reading a provider checkout session with the state
// of its payment, with the organization's own module keys (spec
// 08_architecture/67 section 5.2). hold.close_payment (arena-worker,
// internal/platform/holdclose) asks it before it closes the page of an
// expired seat hold; the close itself is ExpireEmbeddedCheckout.
package hfeed

import (
	"context"
	"strings"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
)

// ReadCheckoutSessionPayment reads the provider session sessionID (embedded or
// hosted) of the organization's channel config together with the state of the
// payment behind it. ErrEmbeddedCheckoutUnavailable: the channel's provider is
// not provider or its module cannot read a session.
func (s *StripePaymentStarter) ReadCheckoutSessionPayment(ctx context.Context, orgID, channelID uuid.UUID, provider, sessionID string) (payments.CheckoutSessionPayment, error) {
	emb, cfg, _, err := s.embeddedModule(ctx, orgID, channelID, false)
	if err != nil {
		return payments.CheckoutSessionPayment{}, err
	}
	if !strings.EqualFold(cfg.entry.Descriptor.Name, provider) {
		return payments.CheckoutSessionPayment{}, ErrEmbeddedCheckoutUnavailable
	}
	reader, ok := emb.(payments.CheckoutSessionPaymentReader)
	if !ok {
		return payments.CheckoutSessionPayment{}, ErrEmbeddedCheckoutUnavailable
	}
	return reader.ReadCheckoutSessionPayment(ctx, sessionID)
}
