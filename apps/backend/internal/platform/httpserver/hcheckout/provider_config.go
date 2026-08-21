// provider_config.go — AB-41: payment_provider_configs is READ by checkout.
//
// Before AB-41 the table, its CRUD API and the Payment Configs UI were a
// dead end: checkout resolved the provider from sales_channels and
// POST /v1/payment-intents took `provider` as free text from the client.
// Now:
//
//   - sales_channels keeps deciding WHICH provider (channel.provider);
//   - payment_provider_configs supplies THE CREDENTIALS and the go/no-go:
//     the org must hold an active, fully configured config for that
//     provider or the payment path fails with a specific 422 — never a
//     silent fall-through to a default;
//   - clients no longer choose the provider: a supplied value that
//     contradicts the channel is rejected.
//
// KYB decision (AB-41 step 5, decided here): organizations.kyb_status
// gates going LIVE. A `mode=live` config is only usable (and only
// activatable, see hpayments) when the org is `verified`; `test` configs
// are unrestricted so integration work never waits on verification.
package hcheckout

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// Provider-config error codes (422).
const (
	ErrCodeProviderNotConfigured = "payment.provider_not_configured"
	ErrCodeProviderInactive      = "payment.provider_inactive"
	ErrCodeProviderMissingFields = "payment.provider_missing_required_fields"
	ErrCodeProviderKYBRequired   = "payment.provider_kyb_required"
	ErrCodeProviderMismatch      = "payment.provider_mismatch"

	kybVerified                    = "verified"
	providerConfigStatusConfigured = "configured"
)

// ProviderConfigError is the typed outcome of a failed resolution.
type ProviderConfigError struct {
	Code    string
	Message string
	Details map[string]any
}

func (e *ProviderConfigError) Error() string { return e.Code + ": " + e.Message }

// SelectProviderConfig is the pure decision: given an org's config rows,
// the channel's provider and the org's kyb_status, pick the usable config
// or explain why none is. Preference when both modes are usable: live.
func SelectProviderConfig(rows []gen.PaymentProviderConfigRow, provider, kybStatus string) (gen.PaymentProviderConfigRow, *ProviderConfigError) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	var candidates []gen.PaymentProviderConfigRow
	for _, r := range rows {
		if r.DeletedAt == nil && strings.EqualFold(r.Provider, provider) {
			candidates = append(candidates, r)
		}
	}
	if len(candidates) == 0 {
		return gen.PaymentProviderConfigRow{}, &ProviderConfigError{
			Code:    ErrCodeProviderNotConfigured,
			Message: "the organization has no payment provider config for " + provider,
			Details: map[string]any{"provider": provider},
		}
	}
	var (
		best       *gen.PaymentProviderConfigRow
		sawMissing bool
		sawKYB     bool
	)
	for i := range candidates {
		c := candidates[i]
		switch {
		case !c.IsActive:
			// inactive — fall through to the generic inactive error below
		case c.Status != providerConfigStatusConfigured:
			sawMissing = true
		case c.Mode == "live" && kybStatus != kybVerified:
			sawKYB = true
		default:
			if best == nil || (c.Mode == "live" && best.Mode != "live") {
				cc := c
				best = &cc
			}
		}
	}
	if best != nil {
		return *best, nil
	}
	switch {
	case sawKYB:
		return gen.PaymentProviderConfigRow{}, &ProviderConfigError{
			Code:    ErrCodeProviderKYBRequired,
			Message: "only a live payment config exists and the organization is not KYB-verified",
			Details: map[string]any{"provider": provider, "kyb_status": kybStatus},
		}
	case sawMissing:
		return gen.PaymentProviderConfigRow{}, &ProviderConfigError{
			Code:    ErrCodeProviderMissingFields,
			Message: "the payment provider config is missing required credential fields",
			Details: map[string]any{"provider": provider},
		}
	default:
		return gen.PaymentProviderConfigRow{}, &ProviderConfigError{
			Code:    ErrCodeProviderInactive,
			Message: "the payment provider config is inactive",
			Details: map[string]any{"provider": provider},
		}
	}
}

// ResolveProviderConfig loads the org's configs + kyb_status and applies
// SelectProviderConfig. A nil queries handle yields a not-configured
// error — the caller must never fall through to a default.
func ResolveProviderConfig(ctx context.Context, q *gen.Queries, orgID uuid.UUID, provider string) (gen.PaymentProviderConfigRow, *ProviderConfigError) {
	if q == nil {
		return gen.PaymentProviderConfigRow{}, &ProviderConfigError{
			Code:    ErrCodeProviderNotConfigured,
			Message: "payment provider configs are not available",
			Details: map[string]any{"provider": provider},
		}
	}
	rows, err := q.ListPaymentProviderConfigsByOrg(ctx, orgID)
	if err != nil {
		return gen.PaymentProviderConfigRow{}, &ProviderConfigError{
			Code:    ErrCodeProviderNotConfigured,
			Message: "failed to load payment provider configs",
			Details: map[string]any{"provider": provider},
		}
	}
	kyb := ""
	if org, orgErr := q.GetOrganizationByID(ctx, orgID); orgErr == nil {
		kyb = org.KybStatus
	} else if !errors.Is(orgErr, pgx.ErrNoRows) {
		return gen.PaymentProviderConfigRow{}, &ProviderConfigError{
			Code:    ErrCodeProviderNotConfigured,
			Message: "failed to load the organization",
			Details: map[string]any{"provider": provider},
		}
	}
	return SelectProviderConfig(rows, provider, kyb)
}

// WebhookSecretFromConfig extracts the provider's webhook signing secret
// from a config's secrets blob (stripe: webhook_secret; allpay:
// secret_key). Empty when absent.
func WebhookSecretFromConfig(cfg gen.PaymentProviderConfigRow) string {
	if len(cfg.Secrets) == 0 {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal(cfg.Secrets, &m); err != nil {
		return ""
	}
	key := "webhook_secret"
	if strings.EqualFold(cfg.Provider, "allpay") {
		key = "secret_key"
	}
	if v, ok := m[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

// channelProviderForCheckout resolves the sales channel's provider for a
// checkout session (the authority on WHICH provider).
func (h *Handler) channelProviderForCheckout(ctx context.Context, cs gen.CheckoutSessionRow) (string, error) {
	if h.channelQueries == nil {
		return "", errors.New("channel queries unavailable")
	}
	ch, err := h.channelQueries.GetSalesChannelByID(ctx, cs.ChannelID, cs.OrgID)
	if err != nil {
		return "", err
	}
	return ch.Provider, nil
}

// webhookSecretsFromOrgConfig locates the organization behind an inbound
// webhook body (refund_id or payment_intent_id) and returns the Stripe /
// AllPay webhook secrets from its usable payment_provider_configs rows.
// Empty strings mean "no per-org secret" — the caller falls back to the
// process-env secrets. Never errors: a lookup problem simply yields the
// fallback, and the signature is still verified.
func (h *Handler) webhookSecretsFromOrgConfig(ctx context.Context, body []byte) (stripeSecret, allPaySecret string) {
	if h.orgQueries == nil {
		return "", ""
	}
	var probe struct {
		RefundID        string `json:"refund_id"`
		PaymentIntentID string `json:"payment_intent_id"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return "", ""
	}
	var orgID uuid.UUID
	switch {
	case probe.RefundID != "" && h.refundQueries != nil:
		id, err := uuid.Parse(probe.RefundID)
		if err != nil {
			return "", ""
		}
		ref, err := h.refundQueries.GetRefundByID(ctx, id)
		if err != nil {
			return "", ""
		}
		orgID = ref.OrgID
	case probe.PaymentIntentID != "" && h.paymentIntentQueries != nil:
		id, err := uuid.Parse(probe.PaymentIntentID)
		if err != nil {
			return "", ""
		}
		pi, err := h.paymentIntentQueries.GetPaymentIntentByID(ctx, id)
		if err != nil {
			return "", ""
		}
		orgID = pi.OrgID
	default:
		return "", ""
	}
	if cfg, cfgErr := ResolveProviderConfig(ctx, h.orgQueries, orgID, "stripe"); cfgErr == nil {
		stripeSecret = WebhookSecretFromConfig(cfg)
	}
	if cfg, cfgErr := ResolveProviderConfig(ctx, h.orgQueries, orgID, "allpay"); cfgErr == nil {
		allPaySecret = WebhookSecretFromConfig(cfg)
	}
	return stripeSecret, allPaySecret
}
