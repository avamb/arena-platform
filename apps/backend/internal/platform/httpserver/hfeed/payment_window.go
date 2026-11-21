// payment_window.go — the payment window a buyer gets on a sales channel
// (spec 62 §5.3, П-3).
//
// The deployment sets one window for every hosted payment page
// (WIDGET_PAYMENT_WINDOW_SECONDS, default 1860). A channel may set its own
// (sales_channels.payment_window_seconds, migration 0205): Vino&Co's AllPay
// site gives the buyer 20 minutes, like its WordPress shop did. The hold, the
// order and the checkout still expire at window + grace (SetHoldExpiryTx), only
// the window comes from the channel.
//
// A provider with a floor of its own (Capabilities.MinPageLifetimeSeconds —
// Stripe refuses a Checkout Session that expires sooner than 30 minutes) never
// gets a window shorter than the deployment's: the global value stays its
// floor, a longer channel value is still honoured.
package hfeed

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
)

// EffectivePaymentWindow is the window a page is created with. global is the
// deployment window (0 = none configured: nothing is changed), override the
// channel's own value in seconds (nil = none), floorSeconds the provider's
// shortest page life (0 = no floor).
func EffectivePaymentWindow(global time.Duration, override *int32, floorSeconds int) time.Duration {
	if global <= 0 || override == nil || *override <= 0 {
		return global
	}
	own := time.Duration(*override) * time.Second
	if floorSeconds > 0 && own < global {
		return global
	}
	return own
}

// providerPageFloor is the provider's shortest page life in seconds, 0 when
// it has none or arena cannot host a page with it.
func providerPageFloor(provider string) int {
	entry, ok := hostedEntry(provider)
	if !ok {
		return 0
	}
	return entry.Descriptor.Capabilities.MinPageLifetimeSeconds
}

// channelPaymentWindow reads the channel on the POOL — callers run it before
// their transaction opens — and answers its effective window. A failed read
// falls back to the deployment window: the buyer still gets a page.
func (h *Handler) channelPaymentWindow(ctx context.Context, orgID, channelID uuid.UUID) time.Duration {
	if h.paymentWindow <= 0 || h.publicFeedQueries == nil {
		return h.paymentWindow
	}
	ch, err := h.publicFeedQueries.GetSalesChannelByID(ctx, channelID, orgID)
	if err != nil {
		h.logger.Warn("public_feed_checkout: channel payment window lookup failed, using the default window",
			slog.String("channel_id", channelID.String()), slog.String("error", err.Error()))
		return h.paymentWindow
	}
	return EffectivePaymentWindow(h.paymentWindow, ch.PaymentWindowSeconds, providerPageFloor(ch.Provider))
}

// windowSeconds renders a window for the public payloads.
func windowSeconds(d time.Duration) int64 { return int64(d / time.Second) }
