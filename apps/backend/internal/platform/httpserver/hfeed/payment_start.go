// payment_start.go — taking money for a widget checkout (hosted redirect flow).
//
// Until this landed, POST /v1/public/feeds/{feed_token}/checkout/start
// answered a hardcoded, dead redirect_url ("/checkout/<id>"): the widget
// navigated to it, got a 404 from whatever site embedded it, and the buyer's
// seats sat held until they expired. No payment provider was ever called.
//
// The flow now is:
//
//  1. The checkout transaction commits exactly as before (hold, checkout
//     session, pricing snapshot, order aggregate). Money is never taken
//     inside that transaction — a provider call can hang for seconds.
//  2. The channel says WHICH provider (sales_channels.provider) and the org's
//     payment_provider_configs row supplies the credentials, through the
//     existing hcheckout.ResolveProviderConfig (test/live + KYB rules
//     unchanged). Each organizer has their OWN Stripe account: the adapter is
//     built per request from that row's secrets.api_key, never from a
//     process-wide key.
//  3. The provider mints a hosted checkout page. Its session id (cs_…) is
//     stored as payment_intents.provider_payment_id — that is what every
//     checkout.session.* webhook event is keyed by — and its URL becomes the
//     redirect_url the widget navigates to.
//
// Failure never leaves a dead redirect: the handler answers a specific error
// code and the hold simply expires through the existing sweeps.
package hfeed

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/stripe"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// Error codes answered by the payment-start step. They are deliberately
// distinct: an operator reading the log must be able to tell "this org never
// finished connecting Stripe" from "Stripe itself refused us".
const (
	// ErrCodePaymentProviderUnsupported — the channel names a provider that
	// cannot host a checkout page (only stripe can, in this wave).
	ErrCodePaymentProviderUnsupported = "checkout.payment_provider_unsupported"
	// ErrCodePaymentNotConfigured — no usable payment_provider_configs row.
	ErrCodePaymentNotConfigured = "checkout.payment_not_configured"
	// ErrCodePaymentStartFailed — the provider call itself failed.
	ErrCodePaymentStartFailed = "checkout.payment_start_failed"
	// ErrCodeInvalidReturnURL — the buyer's return_url is not on the
	// allow-list and no PUBLIC_TICKETS_BASE_URL fallback is configured.
	ErrCodeInvalidReturnURL = "checkout.invalid_return_url"
)

// checkoutTokenQueryParam is the query parameter the widget already reads on
// load to resume an order (see getCheckoutTokenFromSearch in
// apps/widget/src/lib/store.ts). success_url and cancel_url both carry it, so
// after paying — or after abandoning — the buyer lands back on the embedding
// page and the widget shows the order status instead of an empty cart.
const checkoutTokenQueryParam = "checkout_token"

// PaymentStartError is a typed failure of the payment-start step, carrying
// the error code the HTTP layer must answer with.
type PaymentStartError struct {
	Code    string
	Message string
	// Status is the HTTP status the handler should use.
	Status int
}

func (e *PaymentStartError) Error() string { return e.Code + ": " + e.Message }

// ReturnURLPolicy decides whether a buyer-supplied return URL may be used and
// what to fall back to. Keeping the decision in ONE type means a later wave
// that adds per-channel allowed domains swaps this out and nothing else
// changes.
type ReturnURLPolicy struct {
	// AllowedOrigins is the configured allow-list (CORS_ALLOWED_ORIGINS plus
	// PUBLIC_TICKETS_BASE_URL). An entry of "*" allows any origin — that is
	// the local/dev default and production config validation already forbids
	// it.
	AllowedOrigins []string
	// Fallback is PUBLIC_TICKETS_BASE_URL: used when the request carries no
	// return_url, or one this policy refuses.
	Fallback string
}

// normalizeOrigin reduces a URL to its scheme://host[:port] form, lower-cased
// scheme and host. It returns "" for anything that is not an absolute http(s)
// URL — a relative path, a javascript: URI, or garbage.
func normalizeOrigin(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return ""
	}
	if u.Host == "" {
		return ""
	}
	return scheme + "://" + strings.ToLower(u.Host)
}

// Resolve validates the buyer-supplied return URL and returns the base URL
// the success/cancel URLs are built from.
//
// The returned URL keeps the caller's PATH (the embedding page may live at
// /tickets, not at the site root) but drops its query and fragment — the only
// query parameter the provider redirect may carry is arena's own
// checkout_token, and letting a caller smuggle extra ones in is how an open
// redirect starts.
//
// An absent or refused return_url falls back to PUBLIC_TICKETS_BASE_URL.
// With neither, the checkout cannot be completed at all and the caller must
// answer 400 checkout.invalid_return_url rather than hand out a URL the buyer
// will never come back from.
func (p ReturnURLPolicy) Resolve(candidate string) (string, error) {
	if origin := normalizeOrigin(candidate); origin != "" && p.allows(origin) {
		u, err := url.Parse(strings.TrimSpace(candidate))
		if err == nil {
			u.RawQuery = ""
			u.Fragment = ""
			u.Scheme = strings.ToLower(u.Scheme)
			u.Host = strings.ToLower(u.Host)
			return strings.TrimRight(u.String(), "/"), nil
		}
	}
	if fallback := strings.TrimSpace(p.Fallback); fallback != "" {
		return strings.TrimRight(fallback, "/"), nil
	}
	return "", &PaymentStartError{
		Code:    ErrCodeInvalidReturnURL,
		Message: "return_url is missing or not an allowed origin and no PUBLIC_TICKETS_BASE_URL fallback is configured",
		Status:  400,
	}
}

// allows reports whether the normalized origin is on the allow-list.
func (p ReturnURLPolicy) allows(origin string) bool {
	for _, a := range p.AllowedOrigins {
		a = strings.TrimSpace(a)
		if a == "*" {
			return true
		}
		if allowed := normalizeOrigin(a); allowed != "" && allowed == origin {
			return true
		}
	}
	return false
}

// ReturnURLWithToken appends arena's checkout_token to a resolved return URL.
// Both success_url and cancel_url use it: whichever way the buyer comes back,
// the widget resumes the same checkout.
func ReturnURLWithToken(base, checkoutToken string) string {
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	return base + sep + checkoutTokenQueryParam + "=" + url.QueryEscape(checkoutToken)
}

// ─────────────────────────────────────────────────────────────────────────────
// Payment starter
// ─────────────────────────────────────────────────────────────────────────────

// HostedCheckoutRequest is what the checkout/start handler asks the payment
// starter for once its own transaction has committed.
type HostedCheckoutRequest struct {
	OrgID             uuid.UUID
	ChannelID         uuid.UUID
	CheckoutSessionID uuid.UUID
	CheckoutToken     string
	OrderID           *uuid.UUID
	Amount            int64
	Currency          string
	ProductName       string
	BuyerEmail        string
	ReturnURL         string
	ExpiresAtUnix     int64
	// BuyerLocale is the language the buyer is shopping in, as stored on
	// checkout_sessions.buyer_locale. Forwarded so the provider's hosted
	// page speaks it too; empty leaves the provider to guess.
	BuyerLocale string
}

// HostedCheckoutResult is the provider's answer: where to send the buyer and
// under which provider-side id.
type HostedCheckoutResult struct {
	RedirectURL string
	SessionID   string
	PaymentID   string
}

// PaymentStarter creates the provider-hosted payment page for a confirmed
// checkout. It is an interface so tests can substitute a stub without
// standing up a fake Stripe, and so a future provider joins without the
// handler learning about it.
type PaymentStarter interface {
	// CheckPaymentConfigured reports whether a hosted payment COULD be
	// created for this org on this sales channel, from CONFIGURATION ALONE —
	// it must never call the provider or any other network service.
	//
	// It exists so the checkout handler can refuse a paid cart while its
	// hold transaction is still open. Before it, a missing or unusable
	// Stripe config was only discovered after the commit, and every buyer
	// attempt left a real hold that only the ~31-minute TTL sweep released:
	// a handful of failed attempts "sold out" a small session (observed
	// live 2026-09-20, availability 15 → 12 from three failures).
	//
	// It returns the SAME *PaymentStartError StartHostedCheckout would have
	// returned for the same configuration, so both paths answer identical
	// codes and statuses.
	CheckPaymentConfigured(ctx context.Context, orgID, channelID uuid.UUID) error

	StartHostedCheckout(ctx context.Context, req HostedCheckoutRequest) (*HostedCheckoutResult, error)
}

// StripePaymentStarter resolves the org's own Stripe credentials per request
// and calls Stripe.
//
// It holds NO adapter: each organizer has a separate Stripe account, so the
// adapter must be constructed from the org's config row on every call. The
// only shared state is the queries handle and the (test-only) BaseURL
// override.
type StripePaymentStarter struct {
	// orgQueries reads payment_provider_configs + organizations.kyb_status.
	orgQueries *gen.Queries
	// channelQueries reads sales_channels.provider.
	channelQueries *gen.Queries
	// baseURL overrides the Stripe API endpoint. Empty means the real one;
	// integration tests point it at a stub server.
	baseURL string
}

// NewStripePaymentStarter builds the starter. Nil queries are allowed — the
// starter then answers ErrCodePaymentNotConfigured, which is the correct
// "this deployment cannot take money" answer rather than a nil dereference.
func NewStripePaymentStarter(orgQ, channelQ *gen.Queries, baseURL string) *StripePaymentStarter {
	return &StripePaymentStarter{orgQueries: orgQ, channelQueries: channelQ, baseURL: baseURL}
}

// supportedHostedProvider is the only provider that can host a checkout page
// in this wave.
const supportedHostedProvider = "stripe"

// hostedPaymentConfig is what the configuration checks yield: the provider
// the channel names and the org's own credential for it.
type hostedPaymentConfig struct {
	provider string
	apiKey   string
}

// resolveConfig is the ONE implementation of "can this org take money on this
// channel?": the channel's provider, that provider being one arena can host a
// page with, the org's usable payment_provider_configs row (active,
// configured, KYB-cleared for live), and a non-empty api_key on it.
//
// It reads configuration only — no provider call, no network — which is what
// lets the checkout handler run it as a pre-flight inside an open
// transaction. Both CheckPaymentConfigured and StartHostedCheckout go through
// it, so the pre-flight can never drift from the real thing.
func (s *StripePaymentStarter) resolveConfig(ctx context.Context, orgID, channelID uuid.UUID) (hostedPaymentConfig, error) {
	if s == nil || s.orgQueries == nil || s.channelQueries == nil {
		return hostedPaymentConfig{}, &PaymentStartError{
			Code:    ErrCodePaymentNotConfigured,
			Message: "payment provider configuration is not available",
			Status:  503,
		}
	}

	ch, err := s.channelQueries.GetSalesChannelByID(ctx, channelID, orgID)
	if err != nil {
		return hostedPaymentConfig{}, &PaymentStartError{
			Code:    ErrCodePaymentNotConfigured,
			Message: "failed to resolve the sales channel's payment provider",
			Status:  503,
		}
	}
	provider := strings.ToLower(strings.TrimSpace(ch.Provider))
	if provider != supportedHostedProvider {
		return hostedPaymentConfig{}, &PaymentStartError{
			Code: ErrCodePaymentProviderUnsupported,
			Message: fmt.Sprintf(
				"sales channel payment provider %q cannot host a checkout page; only %q is supported",
				ch.Provider, supportedHostedProvider),
			Status: 422,
		}
	}

	cfg, cfgErr := hcheckout.ResolveProviderConfig(ctx, s.orgQueries, orgID, provider)
	if cfgErr != nil {
		return hostedPaymentConfig{}, &PaymentStartError{
			Code:    ErrCodePaymentNotConfigured,
			Message: cfgErr.Message,
			Status:  422,
		}
	}
	apiKey := hcheckout.SecretFieldFromConfig(cfg, "api_key")
	if apiKey == "" {
		return hostedPaymentConfig{}, &PaymentStartError{
			Code:    ErrCodePaymentNotConfigured,
			Message: "the organization's stripe config has no api_key",
			Status:  422,
		}
	}
	return hostedPaymentConfig{provider: provider, apiKey: apiKey}, nil
}

// CheckPaymentConfigured implements the PaymentStarter pre-flight: the
// configuration half of StartHostedCheckout, and nothing else.
func (s *StripePaymentStarter) CheckPaymentConfigured(ctx context.Context, orgID, channelID uuid.UUID) error {
	_, err := s.resolveConfig(ctx, orgID, channelID)
	return err
}

// StartHostedCheckout resolves the channel's provider and the org's usable
// config, then creates the hosted page.
func (s *StripePaymentStarter) StartHostedCheckout(ctx context.Context, req HostedCheckoutRequest) (*HostedCheckoutResult, error) {
	cfg, err := s.resolveConfig(ctx, req.OrgID, req.ChannelID)
	if err != nil {
		return nil, err
	}

	adapter := stripe.New(stripe.Config{SecretKey: cfg.apiKey, BaseURL: s.baseURL})

	metadata := map[string]string{
		"arena_checkout_session_id": req.CheckoutSessionID.String(),
		"arena_org_id":              req.OrgID.String(),
	}
	if req.OrderID != nil {
		metadata["arena_order_id"] = req.OrderID.String()
	}

	returnWithToken := ReturnURLWithToken(req.ReturnURL, req.CheckoutToken)

	resp, err := adapter.CreateCheckoutSession(ctx, payments.CreateHostedCheckoutRequest{
		Amount:            req.Amount,
		Currency:          req.Currency,
		ProductName:       req.ProductName,
		CustomerEmail:     req.BuyerEmail,
		ClientReferenceID: req.CheckoutSessionID.String(),
		Metadata:          metadata,
		// The buyer returns to the SAME page either way. Stripe's own
		// cancel flow and a successful payment differ only in what the
		// order-status endpoint then reports, and the widget polls it.
		SuccessURL:    returnWithToken,
		CancelURL:     returnWithToken,
		ExpiresAtUnix: req.ExpiresAtUnix,
		Locale:        req.BuyerLocale,
		// One hosted session per arena checkout: a retry of this call for the
		// same checkout must return the SAME cs_ id, never charge twice.
		IdempotencyKey: req.CheckoutSessionID.String(),
	})
	if err != nil {
		return nil, fmt.Errorf("stripe hosted checkout: %w", err)
	}

	return &HostedCheckoutResult{
		RedirectURL: resp.URL,
		SessionID:   resp.SessionID,
		PaymentID:   resp.PaymentID,
	}, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Handler glue
// ─────────────────────────────────────────────────────────────────────────────

// preflightPaidCheckout is the configuration-only go/no-go for a cart that
// will have to be paid for. On failure it writes the HTTP error itself and
// reports false; the caller must return immediately, which lets its deferred
// tx.Rollback release everything the request had taken.
//
// WHY IT LIVES HERE, after pricing and before the hold transaction commits,
// rather than before the transaction opens:
//
//   - Only the total decides whether a provider is needed at all, and the
//     total is only known once pricing has run. A seated cart is priced from
//     the tier bindings of the seats it has just locked, so the answer cannot
//     be had any earlier without pricing twice.
//   - Nothing the request has written is durable until the caller's commit,
//     so "after pricing, before commit" is the narrowest point at which a
//     refusal costs the session nothing — and it needs no undo path of its
//     own beyond the rollback the caller already defers. Releasing a
//     committed hold by hand would be a second write path for the same
//     thing, and a second way to get it wrong.
//   - A zero-total cart returns true without looking at anything: a free
//     checkout has no provider and must keep working for an organization
//     that has never connected one.
//
// It answers exactly the codes and statuses the post-commit payment step
// answers, because PaymentStarter.CheckPaymentConfigured runs exactly the
// checks StartHostedCheckout runs first (StripePaymentStarter.resolveConfig
// is the single implementation of both).
func (h *Handler) preflightPaidCheckout(
	ctx context.Context,
	w http.ResponseWriter,
	r *http.Request,
	checkCtx gen.PublicCheckoutContextRow,
	total int64,
	returnURL string,
) bool {
	if total <= 0 {
		return true
	}

	// A buyer with nowhere to be sent back to cannot complete a payment
	// either, and that failure burns inventory in exactly the same way. It
	// used to be caught after the hold had committed; the code and status
	// are unchanged, only the timing.
	if _, err := h.returnURLPolicy.Resolve(returnURL); err != nil {
		h.writePaymentStartError(w, r, err, checkCtx,
			"public_feed_checkout: refusing a paid cart before it takes inventory (return_url)")
		return false
	}

	if h.paymentStarter == nil {
		// Loud on purpose: a deployment that confirms carts it cannot charge
		// is misconfigured, and every buyer hitting this loses their seats.
		h.logger.Error("public_feed_checkout: no payment starter is wired; a paid checkout cannot be completed",
			slog.String("org_id", checkCtx.OrgID.String()),
			slog.String("session_id", checkCtx.SessionID.String()),
		)
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			ErrCodePaymentNotConfigured, "payments are not configured for this deployment", r,
		))
		return false
	}

	if err := h.paymentStarter.CheckPaymentConfigured(ctx, checkCtx.OrgID, checkCtx.SalesChannelID); err != nil {
		h.writePaymentStartError(w, r, err, checkCtx,
			"public_feed_checkout: refusing a paid cart before it takes inventory (payment config)")
		return false
	}
	return true
}

// writePaymentStartError logs and answers a typed payment-start failure. An
// untyped error cannot be attributed to a specific misconfiguration, so it
// degrades to the "this deployment cannot take money" answer rather than
// leaking an internal message to a public caller.
func (h *Handler) writePaymentStartError(
	w http.ResponseWriter,
	r *http.Request,
	err error,
	checkCtx gen.PublicCheckoutContextRow,
	logMessage string,
) {
	pse, isTyped := AsPaymentStartError(err)
	if !isTyped || pse == nil {
		pse = &PaymentStartError{
			Code:    ErrCodePaymentNotConfigured,
			Message: "the payment configuration for this sales channel could not be resolved",
			Status:  http.StatusServiceUnavailable,
		}
	}
	h.logger.Error(logMessage,
		slog.String("code", pse.Code),
		slog.String("org_id", checkCtx.OrgID.String()),
		slog.String("session_id", checkCtx.SessionID.String()),
		slog.String("error", err.Error()),
	)
	httputil.WriteJSON(w, pse.Status, httputil.ErrorEnvelope(pse.Code, pse.Message, r))
}

// hostedPaymentInput carries everything the payment step needs from the
// just-committed checkout transaction.
type hostedPaymentInput struct {
	CheckCtx      gen.PublicCheckoutContextRow
	Session       gen.CheckoutSessionRow
	CheckoutToken string
	OrderID       *uuid.UUID
	Breakdown     hcheckout.PricingBreakdown
	Buyer         publicOrderBuyer
	ReturnBase    string
	ExpiresAt     time.Time
}

// startHostedPayment creates the provider-hosted payment page and records the
// payment_intents row, returning the URL to redirect the buyer to.
//
// On any failure it writes the HTTP error itself and reports ok=false. The
// hold is NOT released here: it expires through the existing reservation /
// order sweeps, exactly as an abandoned cart does. Rolling the committed
// checkout back by hand would be a second write path for the same thing and
// a second way to get it wrong.
func (h *Handler) startHostedPayment(
	ctx context.Context,
	w http.ResponseWriter,
	r *http.Request,
	in hostedPaymentInput,
) (redirectURL string, ok bool) {
	if h.paymentStarter == nil {
		// Loud on purpose: a deployment that confirms carts it cannot charge
		// is misconfigured, and every buyer hitting this loses their seats.
		h.logger.Error("public_feed_checkout: no payment starter is wired; a paid checkout cannot be completed",
			slog.String("checkout_session_id", in.Session.ID.String()),
			slog.String("org_id", in.CheckCtx.OrgID.String()),
		)
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			ErrCodePaymentNotConfigured, "payments are not configured for this deployment", r,
		))
		return "", false
	}

	result, err := h.paymentStarter.StartHostedCheckout(ctx, HostedCheckoutRequest{
		OrgID:             in.CheckCtx.OrgID,
		ChannelID:         in.CheckCtx.SalesChannelID,
		CheckoutSessionID: in.Session.ID,
		CheckoutToken:     in.CheckoutToken,
		OrderID:           in.OrderID,
		Amount:            in.Breakdown.Total,
		Currency:          in.Breakdown.Currency,
		ProductName:       h.hostedLineItemName(ctx, in.CheckCtx),
		BuyerEmail:        in.Buyer.Email,
		ReturnURL:         in.ReturnBase,
		ExpiresAtUnix:     in.ExpiresAt.Add(-h.paymentGrace).Unix(),
		BuyerLocale:       in.Buyer.Locale,
	})
	if err != nil {
		if pse, isTyped := AsPaymentStartError(err); isTyped {
			h.logger.Error("public_feed_checkout: payment cannot be started",
				slog.String("code", pse.Code),
				slog.String("checkout_session_id", in.Session.ID.String()),
				slog.String("org_id", in.CheckCtx.OrgID.String()),
				slog.String("error", pse.Message),
			)
			httputil.WriteJSON(w, pse.Status, httputil.ErrorEnvelope(pse.Code, pse.Message, r))
			return "", false
		}
		h.logger.Error("public_feed_checkout: payment provider refused to create a hosted checkout",
			slog.String("checkout_session_id", in.Session.ID.String()),
			slog.String("org_id", in.CheckCtx.OrgID.String()),
			slog.String("error", err.Error()),
		)
		// 503, NOT 502, and that is not a cosmetic choice: this endpoint is
		// public and sits behind Cloudflare, which REPLACES an origin 502
		// with its own 16-byte "error code: 502" text/plain page. The JSON
		// envelope — and therefore the error code the widget shows the buyer
		// — never survives the hop. Verified 2026-09-20 by curling the origin
		// directly with --resolve: correct JSON at the origin, Cloudflare's
		// stub on the public URL. A 503 is passed through untouched. Do not
		// "fix" this back to 502.
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			ErrCodePaymentStartFailed, "the payment provider could not start this payment", r,
		))
		return "", false
	}

	// The hosted SESSION id (cs_…) is the provider_payment_id: it is what
	// every checkout.session.* webhook event identifies this payment by.
	if h.checkoutQueries != nil {
		csID := in.Session.ID
		if _, err := h.checkoutQueries.InsertHostedPaymentIntent(ctx,
			&csID, in.CheckCtx.OrgID, supportedHostedProvider,
			result.SessionID, in.Breakdown.Total, in.Breakdown.Currency,
			result.RedirectURL,
		); err != nil {
			// The buyer's money would arrive against a payment intent that
			// does not exist and the webhook would 404 it, so this is fatal
			// — better to lose the cart now than the payment later.
			h.logger.Error("public_feed_checkout: recording the payment intent failed; refusing to redirect",
				slog.String("checkout_session_id", in.Session.ID.String()),
				slog.String("provider_payment_id", result.SessionID),
				slog.String("error", err.Error()),
			)
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				ErrCodePaymentStartFailed, "failed to record the payment", r,
			))
			return "", false
		}
	}

	h.logger.Info("public_feed_checkout: hosted payment started",
		slog.String("checkout_session_id", in.Session.ID.String()),
		slog.String("org_id", in.CheckCtx.OrgID.String()),
		slog.String("provider_payment_id", result.SessionID),
		slog.Int64("amount", in.Breakdown.Total),
		slog.String("currency", in.Breakdown.Currency),
	)
	return result.RedirectURL, true
}

// hostedLineItemName labels the single line item on the hosted page. The
// event title is what a buyer recognises on their card statement and on
// Stripe's own receipt; a lookup failure degrades to a generic label rather
// than failing the sale.
func (h *Handler) hostedLineItemName(ctx context.Context, checkCtx gen.PublicCheckoutContextRow) string {
	if h.eventQueries != nil {
		if ev, err := h.eventQueries.GetEventByID(ctx, checkCtx.EventID, ""); err == nil {
			if title := strings.TrimSpace(ev.Name); title != "" {
				return title
			}
		}
	}
	return "Tickets"
}

// AsPaymentStartError unwraps a typed payment-start failure, or reports false
// for a raw provider/transport error (which the caller answers 502 with).
func AsPaymentStartError(err error) (*PaymentStartError, bool) {
	var pse *PaymentStartError
	if errors.As(err, &pse) {
		return pse, true
	}
	return nil, false
}
