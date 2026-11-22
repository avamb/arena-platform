// payment_method_domains.go — Stripe Payment method domains (spec 65 section 5):
// the page domain must be registered in the organization's own Stripe account
// before Apple Pay and Google Pay appear there.
package stripe

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
)

var _ payments.PaymentMethodDomainRegistrar = (*Adapter)(nil)

type walletStatus struct {
	Status        string `json:"status"`
	StatusDetails struct {
		ErrorMessage string `json:"error_message"`
	} `json:"status_details"`
}

type paymentMethodDomainObject struct {
	ID         string       `json:"id"`
	DomainName string       `json:"domain_name"`
	Enabled    bool         `json:"enabled"`
	ApplePay   walletStatus `json:"apple_pay"`
	GooglePay  walletStatus `json:"google_pay"`
	Link       walletStatus `json:"link"`
}

func (o paymentMethodDomainObject) toDomain() payments.PaymentMethodDomain {
	d := payments.PaymentMethodDomain{
		ProviderID: o.ID,
		Domain:     o.DomainName,
		Enabled:    o.Enabled,
		ApplePay:   o.ApplePay.Status,
		GooglePay:  o.GooglePay.Status,
		Link:       o.Link.Status,
	}
	for _, w := range []walletStatus{o.ApplePay, o.GooglePay, o.Link} {
		if w.Status != "active" && w.StatusDetails.ErrorMessage != "" {
			d.Detail = w.StatusDetails.ErrorMessage
			break
		}
	}
	return d
}

// allWalletsActive reports whether no wallet needs a re-validation.
func (o paymentMethodDomainObject) allWalletsActive() bool {
	return o.ApplePay.Status == "active" && o.GooglePay.Status == "active" && o.Link.Status == "active"
}

// pmdError turns a failed call into an error that never carries the key.
// 401/403 is payments.ErrCredentialRefused.
func pmdError(status int, err error) error {
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return fmt.Errorf("%w: %s", payments.ErrCredentialRefused, err.Error())
	}
	return err
}

// EnsurePaymentMethodDomain looks the domain up (GET /v1/payment_method_domains
// ?domain_name=), registers it when absent and asks Stripe to validate it
// again while a wallet is not active.
//
// Implements payments.PaymentMethodDomainRegistrar.
func (a *Adapter) EnsurePaymentMethodDomain(ctx context.Context, domain string) (payments.PaymentMethodDomain, error) {
	q := url.Values{}
	q.Set("domain_name", domain)
	body, status, err := a.doRequest(ctx, http.MethodGet, a.cfg.BaseURL+"/payment_method_domains?"+q.Encode(), nil, "")
	if err != nil {
		return payments.PaymentMethodDomain{}, pmdError(status, err)
	}
	var list struct {
		Data []paymentMethodDomainObject `json:"data"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return payments.PaymentMethodDomain{}, fmt.Errorf("stripe: decode payment method domains: %w", err)
	}
	for _, o := range list.Data {
		if o.DomainName != domain {
			continue
		}
		if o.Enabled && !o.allWalletsActive() {
			// A failed validation is not an error here: the list object says what it says.
			if vb, _, verr := a.doRequest(ctx, http.MethodPost, a.cfg.BaseURL+"/payment_method_domains/"+url.PathEscape(o.ID)+"/validate", url.Values{}, ""); verr == nil {
				var v paymentMethodDomainObject
				if json.Unmarshal(vb, &v) == nil && v.ID != "" {
					o = v
				}
			}
		}
		return o.toDomain(), nil
	}
	form := url.Values{}
	form.Set("domain_name", domain)
	// The idempotency key keeps two syncs racing on one account, the API and
	// the worker, from creating the domain twice, which Stripe asks to avoid.
	cb, cstatus, err := a.doRequest(ctx, http.MethodPost, a.cfg.BaseURL+"/payment_method_domains", form, "arena-pmd-"+domain)
	if err != nil {
		return payments.PaymentMethodDomain{}, pmdError(cstatus, err)
	}
	var o paymentMethodDomainObject
	if err := json.Unmarshal(cb, &o); err != nil {
		return payments.PaymentMethodDomain{}, fmt.Errorf("stripe: decode payment method domain: %w", err)
	}
	return o.toDomain(), nil
}
