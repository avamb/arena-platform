// payment_webhook_config_route.go — POST /v1/payment-intents/webhook/{config_id}
//
// Why a second webhook route exists at all.
//
// Each organizer connects their OWN Stripe account, and that account is
// shared with whatever else they sell online. Their endpoint therefore
// delivers arena events AND events for payments arena has never heard of, all
// down the same URL. On the un-suffixed route that ends badly twice over:
//
//   - A foreign `cs_…` / `pi_…` matches no payment intent, so the org behind
//     the delivery cannot be resolved, so no per-org signing secret can be
//     chosen — the request is answered 401 (or 404 once past the signature).
//   - Stripe treats both as a failing endpoint: it retries for up to three
//     days and mails the account owner that arena is broken.
//
// The config id in the path fixes both. It names the
// payment_provider_configs row up front, so the signature is checked against
// exactly one known secret before anything else happens, and a VERIFIED event
// that turns out not to be arena's is acknowledged with 200 instead of 404.
//
// What this route does NOT relax:
//
//   - The signature must verify against THAT config's secret. There is no
//     env-secret fallback here, ever — the whole point is a known account.
//   - A payment intent found under a foreign organization is treated as "not
//     ours", identically to one that does not exist. Signing an event with
//     your own account's secret proves the delivery is yours; it does not
//     make someone else's order yours.
//
// The state machine itself is NOT duplicated: both routes run
// processPaymentWebhook.
package hcheckout

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/observability"
)

// HandlePaymentIntentWebhookForConfig serves
// POST /v1/payment-intents/webhook/{config_id}.
//
// Unauthenticated until the signature check, like every provider webhook.
// The only work done before that check is ONE primary-key read of
// payment_provider_configs, so an unauthenticated caller cannot make this
// route expensive.
//
// Answers, in order:
//
//	400 — unreadable / empty body, or a config_id that is not a UUID
//	404 — unknown, deleted, inactive or unusable config (no detail: the
//	      response must not let a caller enumerate which config ids exist)
//	401 — the config has no signing secret, or the signature does not verify
//	200 — everything else, including a verified event that is not arena's
func (h *Handler) HandlePaymentIntentWebhookForConfig(w http.ResponseWriter, r *http.Request) {
	// Read the body first: the HMAC is computed over the raw bytes, and a
	// forged request must be rejected without revealing anything about
	// service state.
	body, err := io.ReadAll(io.LimitReader(r.Body, 512*1024))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"webhook.invalid_body", "cannot read request body: "+err.Error(), r))
		return
	}
	if len(body) == 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"webhook.empty_body", "request body is required", r))
		return
	}

	configID, err := uuid.Parse(strings.TrimSpace(chi.URLParam(r, "config_id")))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"webhook.invalid_config_id", "config_id must be a valid UUID", r))
		return
	}

	cfg, ok := h.usableWebhookConfig(r, configID)
	if !ok {
		// Deliberately detail-free and identical for "no such id", "deleted",
		// "inactive" and "not fully configured": this endpoint is public, and
		// a caller must not be able to enumerate our organizers' config ids.
		httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
			"webhook.config_not_found", "webhook endpoint not found", r))
		return
	}

	secret := WebhookSecretFromConfig(cfg)
	if secret == "" {
		// The organizer saved a config but never pasted their signing secret.
		// Refusing is the only safe answer — accepting unsigned events on a
		// route whose entire purpose is "this account is known" would make
		// the config id a bearer token anyone could guess.
		h.logger.Warn("webhook: payment config has no webhook secret; rejecting",
			slog.String("config_id", configID.String()),
			slog.String("provider", cfg.Provider),
		)
		h.recordSignatureFailure(observability.RouteKindConfig)
		h.recordWebhookEvent(observability.PaymentWebhookEventOther, webhookOutcomeRejected)
		httputil.WriteJSON(w, http.StatusUnauthorized, httputil.ErrorEnvelope(
			"webhook.invalid_signature", "webhook signature verification failed", r))
		return
	}

	if sigErr := h.verifyConfigWebhookSignature(r, body, cfg, secret); sigErr != nil {
		h.logger.Warn("webhook: invalid signature on the per-config route; rejecting request",
			slog.String("config_id", configID.String()),
			slog.String("provider", cfg.Provider),
			slog.String("error", sigErr.Error()),
			slog.String("remote_addr", r.RemoteAddr),
		)
		h.recordSignatureFailure(observability.RouteKindConfig)
		h.recordWebhookEvent(observability.PaymentWebhookEventOther, webhookOutcomeRejected)
		httputil.WriteJSON(w, http.StatusUnauthorized, httputil.ErrorEnvelope(
			"webhook.invalid_signature", "webhook signature verification failed", r))
		return
	}

	orgID := cfg.OrgID
	h.processPaymentWebhook(w, r, body, webhookRoute{
		Kind:          observability.RouteKindConfig,
		ExpectedOrgID: &orgID,
		// The whole reason this route exists.
		ForeignEventIsOK: true,
	})
}

// usableWebhookConfig loads the config named in the URL and decides whether
// it may receive webhooks at all.
//
// "Usable" here deliberately mirrors SelectProviderConfig's notion — active
// and fully configured — minus the KYB/live gating, which governs whether an
// org may CHARGE, not whether an already-created payment may report its
// outcome. An org whose live config was switched off mid-flight must still
// hear that yesterday's payment succeeded.
func (h *Handler) usableWebhookConfig(r *http.Request, configID uuid.UUID) (gen.PaymentProviderConfigRow, bool) {
	if h.orgQueries == nil {
		return gen.PaymentProviderConfigRow{}, false
	}
	cfg, err := h.orgQueries.GetPaymentProviderConfigByIDUnscoped(r.Context(), configID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			h.logger.Error("webhook: payment config lookup failed",
				slog.String("config_id", configID.String()),
				slog.String("error", err.Error()),
			)
		}
		return gen.PaymentProviderConfigRow{}, false
	}
	if cfg.DeletedAt != nil || !cfg.IsActive || cfg.Status != providerConfigStatusConfigured {
		return gen.PaymentProviderConfigRow{}, false
	}
	return cfg, true
}

// verifyConfigWebhookSignature checks the request against ONE secret — the
// one belonging to the config in the URL.
//
// It deliberately does not reuse verifyWebhookSignature: that helper falls
// back to the process-env secrets and resolves the org from the body, both of
// which would defeat the point of this route. Here the provider comes from
// the config row, so the expected header is known too.
func (h *Handler) verifyConfigWebhookSignature(r *http.Request, body []byte, cfg gen.PaymentProviderConfigRow, secret string) error {
	if strings.EqualFold(cfg.Provider, "allpay") {
		header := r.Header.Get("X-AllPay-Signature")
		if header == "" {
			return errNoSignatureHeader
		}
		return payments.VerifyAllPaySignature(header, body, secret)
	}
	header := r.Header.Get("Stripe-Signature")
	if header == "" {
		return errNoSignatureHeader
	}
	return payments.VerifyStripeSignature(header, body, secret, payments.DefaultWebhookTolerance)
}

// errNoSignatureHeader is returned when the expected provider signature
// header is absent entirely.
var errNoSignatureHeader = errors.New(
	"hcheckout: no provider signature header present on the per-config webhook route")
