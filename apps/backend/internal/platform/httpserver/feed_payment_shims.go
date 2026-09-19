// feed_payment_shims.go bridges the *Server god-object to the widget's
// hosted-payment dependencies: the payment starter that creates the
// provider-hosted page, and the policy that decides which return_url a buyer
// may be sent back to.
//
// Split out of feed_shims.go to respect the feature #175 per-file budget.
// Everything here is reached through feedHandler().WithPayments.
package httpserver

import (
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hfeed"
)

const (
	defaultWidgetPaymentWindowSeconds = 1860
	defaultWidgetPaymentGraceSeconds  = 120
)

// widgetPaymentWindow is how long the buyer has on the provider-hosted page —
// and the hosted session's own expires_at. Stripe refuses an expiry closer
// than 30 minutes, hence the 31-minute default.
func widgetPaymentWindow(cfg *config.Config) time.Duration {
	secs := defaultWidgetPaymentWindowSeconds
	if cfg != nil && cfg.WidgetPaymentWindowSeconds > 0 {
		secs = cfg.WidgetPaymentWindowSeconds
	}
	return time.Duration(secs) * time.Second
}

// widgetPaymentGrace is added on top of the window for the hold / order /
// checkout expiry, so arena releases the seats only AFTER the hosted session
// has already stopped accepting payment.
func widgetPaymentGrace(cfg *config.Config) time.Duration {
	secs := defaultWidgetPaymentGraceSeconds
	if cfg != nil && cfg.WidgetPaymentGraceSeconds > 0 {
		secs = cfg.WidgetPaymentGraceSeconds
	}
	return time.Duration(secs) * time.Second
}

// checkoutReturnURLPolicy builds the allow-list the buyer-supplied return_url
// is validated against: the deployment's CORS origins plus the canonical
// tickets base URL, which is also the fallback when the request carries no
// usable return_url.
//
// One function, one place: a later wave that adds per-channel allowed domains
// replaces this and nothing downstream changes.
func (s *Server) checkoutReturnURLPolicy() hfeed.ReturnURLPolicy {
	policy := hfeed.ReturnURLPolicy{}
	if s.cfg == nil {
		return policy
	}
	policy.Fallback = s.cfg.PublicTicketsBaseURL
	policy.AllowedOrigins = append(policy.AllowedOrigins, s.cfg.CORSAllowedOrigins...)
	if s.cfg.PublicTicketsBaseURL != "" {
		policy.AllowedOrigins = append(policy.AllowedOrigins, s.cfg.PublicTicketsBaseURL)
	}
	return policy
}

// hostedPaymentStarter builds the per-request payment starter. It holds no
// provider adapter: every organizer has their OWN Stripe account, so the
// adapter is constructed from that org's payment_provider_configs row on each
// call (see hfeed.StripePaymentStarter).
//
// stripeAPIBaseURL is the test seam — integration tests point it at a stub
// Stripe server through httpserver.Options.
func (s *Server) hostedPaymentStarter() hfeed.PaymentStarter {
	if s.orgQueries == nil || s.channelQueries == nil {
		return nil
	}
	return hfeed.NewStripePaymentStarter(s.orgQueries, s.channelQueries, s.stripeBaseURL())
}

// stripeBaseURL resolves the Stripe endpoint the hosted-checkout adapter
// talks to. The in-process Options override wins, because a Go integration
// test owns its whole server; STRIPE_API_BASE_URL is the seam for a test that
// runs a real arena-api BINARY against a stub (the widget acceptance job), and
// config.Validate refuses it outright under APP_ENV=production. Both empty
// means the real api.stripe.com.
func (s *Server) stripeBaseURL() string {
	if s.stripeAPIBaseURL != "" {
		return s.stripeAPIBaseURL
	}
	if s.cfg != nil {
		return s.cfg.StripeAPIBaseURL
	}
	return ""
}
