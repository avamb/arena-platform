// receipt_email.go — moving the receipt address of a paid payment (spec 63,
// decision R-17, П-2).
//
// When a reissue moves an order to the buyer's real address, the charge's
// receipt_email is updated too. Stripe then sends a new receipt to that
// address at once, and later refund receipts go there. The Checkout Session
// and billing_details keep the address typed at purchase: Stripe has no way
// to change those.
package stripe

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
)

var _ payments.ReceiptEmailUpdater = (*Adapter)(nil)

// UpdateReceiptEmail sets receipt_email on the latest charge of the payment.
// paymentRef is a pi_… or a hosted Checkout Session cs_…; a ch_… is updated
// directly.
func (a *Adapter) UpdateReceiptEmail(ctx context.Context, paymentRef, email, idempotencyKey string) error {
	email = strings.TrimSpace(email)
	if email == "" {
		return fmt.Errorf("stripe: UpdateReceiptEmail: empty address")
	}
	ref := strings.TrimSpace(paymentRef)
	if strings.HasPrefix(ref, "cs_") {
		resolved, err := a.ResolveChargeRef(ctx, ref)
		if err != nil {
			return fmt.Errorf("stripe: UpdateReceiptEmail: %w", err)
		}
		ref = resolved
	}
	charge := ref
	if strings.HasPrefix(ref, "pi_") {
		endpoint := a.cfg.BaseURL + "/payment_intents/" + url.PathEscape(ref)
		raw, _, err := a.doRequest(ctx, http.MethodGet, endpoint, nil, "")
		if err != nil {
			return fmt.Errorf("stripe: UpdateReceiptEmail: read payment intent: %w", err)
		}
		var body struct {
			LatestCharge json.RawMessage `json:"latest_charge"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			return fmt.Errorf("stripe: UpdateReceiptEmail: unmarshal payment intent: %w", err)
		}
		charge = chargeID(body.LatestCharge)
	}
	if !strings.HasPrefix(charge, "ch_") && !strings.HasPrefix(charge, "py_") {
		return fmt.Errorf("stripe: UpdateReceiptEmail: the payment has no charge")
	}
	form := url.Values{}
	form.Set("receipt_email", email)
	endpoint := a.cfg.BaseURL + "/charges/" + url.PathEscape(charge)
	if _, _, err := a.doRequest(ctx, http.MethodPost, endpoint, form, idempotencyKey); err != nil {
		return fmt.Errorf("stripe: UpdateReceiptEmail: update charge: %w", err)
	}
	return nil
}

// chargeID reads latest_charge, which Stripe answers as an id string or, when
// expanded, as an object.
func chargeID(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var id string
	if json.Unmarshal(raw, &id) == nil {
		return id
	}
	var obj struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(raw, &obj) == nil {
		return obj.ID
	}
	return ""
}
