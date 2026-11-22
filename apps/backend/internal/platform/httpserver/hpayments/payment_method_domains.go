// payment_method_domains.go — the wallet domains (Apple Pay, Google Pay) of a
// Stripe configuration (spec 65 section 5, task E3).
//
// A save of a Stripe configuration registers the wanted hosts in the Stripe
// account through paymentdomains.Syncer. Like the credential check it NEVER
// blocks the save: a failure is only logged, and Stripe's own wording is kept
// per domain in payment_method_domains.last_error where the operator sees it.
package hpayments

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/paymentdomains"
)

// domainSyncTimeout bounds one synchronous domain sync after a save. Every
// wanted host costs one or two Stripe calls, so it is longer than verifyTimeout.
const domainSyncTimeout = 20 * time.Second

// WithDomainSyncer attaches the wallet-domain syncer. Nil (the default) turns
// the sync after a save off; the list route then only reads what is stored.
func (h *Handler) WithDomainSyncer(s *paymentdomains.Syncer) *Handler {
	h.domainSyncer = s
	return h
}

// PaymentMethodDomainResponse is one wallet domain of a payment configuration.
type PaymentMethodDomainResponse struct {
	Domain          string  `json:"domain"`
	Source          string  `json:"source"`
	Enabled         *bool   `json:"enabled"`
	ApplePayStatus  *string `json:"apple_pay_status"`
	GooglePayStatus *string `json:"google_pay_status"`
	LinkStatus      *string `json:"link_status"`
	StatusDetail    *string `json:"status_detail"`
	LastError       *string `json:"last_error"`
	CheckedAt       *string `json:"checked_at"`
}

func paymentMethodDomainsFromRows(rows []gen.PaymentMethodDomainRow) []PaymentMethodDomainResponse {
	out := make([]PaymentMethodDomainResponse, 0, len(rows))
	for _, r := range rows {
		var checked *string
		if r.CheckedAt != nil {
			s := r.CheckedAt.UTC().Format(time.RFC3339)
			checked = &s
		}
		out = append(out, PaymentMethodDomainResponse{
			Domain:          r.Domain,
			Source:          r.Source,
			Enabled:         r.Enabled,
			ApplePayStatus:  r.ApplePayStatus,
			GooglePayStatus: r.GooglePayStatus,
			LinkStatus:      r.LinkStatus,
			StatusDetail:    r.StatusDetail,
			LastError:       r.LastError,
			CheckedAt:       checked,
		})
	}
	return out
}

// syncWalletDomains registers the wanted wallet domains for a row whose
// credential just checked out, and returns what is stored afterwards. It never
// fails the caller: with no syncer, another provider, or a credential that is
// not verified ok it does nothing and returns the stored rows.
func (h *Handler) syncWalletDomains(ctx context.Context, row gen.PaymentProviderConfigRow) []PaymentMethodDomainResponse {
	if h.domainSyncer != nil && row.VerificationStatus == VerificationOK {
		callCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), domainSyncTimeout)
		defer cancel()
		rows, err := h.domainSyncer.SyncConfig(callCtx, row)
		if err != nil {
			h.logger.Warn("payment_config: wallet domain sync failed",
				slog.String("payment_config_id", row.ID.String()),
				slog.String("error", err.Error()),
			)
		} else if rows != nil {
			return paymentMethodDomainsFromRows(rows)
		}
	}
	stored, err := h.paymentConfigQueries.ListPaymentMethodDomains(ctx, row.ID, row.OrgID)
	if err != nil {
		h.logger.Warn("payment_config: list wallet domains failed",
			slog.String("payment_config_id", row.ID.String()),
			slog.String("error", err.Error()),
		)
	}
	return paymentMethodDomainsFromRows(stored)
}

// HandleListPaymentMethodDomains backs
// GET /v1/organizations/{org_id}/payment-configs/{id}/payment-method-domains.
func (h *Handler) HandleListPaymentMethodDomains(w http.ResponseWriter, r *http.Request) {
	if h.paymentConfigQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	id, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}
	if !h.requireOrgMembership(w, r, orgID) {
		return
	}
	ctx := r.Context()
	row, err := h.paymentConfigQueries.GetPaymentProviderConfigByID(ctx, id, orgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"payment_config.not_found", "payment config not found", r,
			))
			return
		}
		h.logger.Error("payment_config: domains lookup failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"payment_config.domains_failed", "failed to load payment config", r,
		))
		return
	}
	rows, err := h.paymentConfigQueries.ListPaymentMethodDomains(ctx, row.ID, row.OrgID)
	if err != nil {
		h.logger.Error("payment_config: list wallet domains failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"payment_config.domains_failed", "failed to load wallet domains", r,
		))
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"payment_method_domains": paymentMethodDomainsFromRows(rows),
	})
}
