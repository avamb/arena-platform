// refund.go — refunds through AllPay (PAY-14b, spec
// 08_architecture/37_allpay_payment_provider_ru.md §8).
//
// AllPay's refund method has no idempotency key and returns no refund id.
// A call whose answer was lost may have returned the money, and a blind
// repeat would return it a second time. So the module never sends a refund
// before it has read the order back (paymentstatus) and compared AllPay's
// refunded total with what arena knows:
//
//	P = what AllPay says is refunded on the order
//	K = refunds arena knows were accepted (RefundRequest.OtherRefundedMinor)
//	U = refunds whose call ended without an answer (OtherUncertainMinor)
//	A = this refund
//
//	P < K                 → a human checks (AllPay knows less than arena)
//	P − K = 0             → nothing unexplained: send the refund
//	P − K = A and U = 0   → an earlier call of THIS refund landed: succeeded,
//	                        nothing is sent
//	anything else         → a human checks; nothing is sent
//
// The refund engine holds the payment's lock while it computes K and U and
// lets only one call of a payment run at a time, so nothing changes the
// order on arena's side during the comparison. A refund made in the AllPay
// dashboard that arena does not know shows up as an unexplained difference
// and stops arena's own refunds of that order until a human looks.
package allpay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
)

const methodRefund = "refund"

// SettleWait is how long a REPEATED call of one refund waits after its
// first call before it may send the refund again. AllPay has no
// idempotency key: an earlier call that timed out on arena's side may still
// be processed by AllPay, and until then paymentstatus does not show it. A
// repeat inside the wait reads the order and, when nothing explains the
// money yet, sends nothing and reports an unknown outcome, so the engine
// asks again later.
const SettleWait = 10 * time.Minute

// AllPay error codes of the refund method (API reference, "Errors").
const (
	ErrCodeWrongStatus   = 16 // the order is not in a state that can be refunded
	ErrCodeRefundFailure = 17 // the card processor refused the refund
)

var (
	_ payments.Refunder          = (*Adapter)(nil)
	_ payments.ChargeRefResolver = (*Adapter)(nil)
)

// Stable failure codes the module reports to the refund engine.
const (
	RefundCodeNotPaid          = "allpay_order_not_paid"
	RefundCodeCurrency         = "allpay_currency_mismatch"
	RefundCodeKnowsLess        = "allpay_refunded_less_than_known"
	RefundCodeUnexplained      = "allpay_refund_unexplained"
	RefundCodeOverRemaining    = "allpay_amount_over_remaining"
	RefundCodePartialUnclear   = "allpay_partial_refund_unclear"
	RefundCodeOrderNotFound    = "allpay_order_not_found"
	RefundCodeAPIRefused       = "allpay_refused"
	RefundCodeProcessorRefused = "allpay_processor_refused"
)

// RefundID is the id arena keeps for a refund AllPay accepted. AllPay mints
// none, so it is the order id plus arena's own refund id: unique, stable
// across a repeated call, and readable by a human in the dashboard.
func RefundID(orderID, arenaRefundID string) string {
	return orderID + "/" + arenaRefundID
}

// ResolveChargeRef implements payments.ChargeRefResolver. AllPay refunds by
// order_id, which is the payment's provider_payment_id already.
func (a *Adapter) ResolveChargeRef(_ context.Context, providerPaymentID string) (string, error) {
	ref := strings.TrimSpace(providerPaymentID)
	if ref == "" {
		return "", payments.ErrRefundChargeRefMissing
	}
	return ref, nil
}

// Refund implements payments.Refunder. See the package comment of this file
// for the comparison that runs before anything is sent.
func (a *Adapter) Refund(ctx context.Context, req payments.RefundRequest) (payments.RefundResult, error) {
	orderID := strings.TrimSpace(req.ProviderChargeRef)
	switch {
	case req.AmountMinor <= 0:
		return payments.RefundResult{}, payments.ErrRefundAmountInvalid
	case req.PaymentAmountMinor > 0 && req.AmountMinor > req.PaymentAmountMinor:
		return payments.RefundResult{}, payments.ErrRefundExceedsPayment
	case orderID == "":
		return payments.RefundResult{}, payments.ErrRefundChargeRefMissing
	case strings.TrimSpace(req.IdempotencyKey) == "":
		return payments.RefundResult{}, errors.New("allpay: Refund: an idempotency key (the arena refund id) is required")
	case a.cfg.Login == "" || a.cfg.APIKey == "":
		return payments.RefundResult{}, errors.New("allpay: Refund: the config has no API login or API key")
	}
	accepted := payments.RefundResult{
		ProviderRefundID: RefundID(orderID, strings.TrimSpace(req.IdempotencyKey)),
		Status:           payments.RefundSucceeded,
	}

	st, err := a.PaymentStatus(ctx, orderID)
	if err != nil {
		if errors.Is(err, ErrOrderNotFound) {
			return payments.RefundResult{}, &payments.RefundDeclinedError{Code: RefundCodeOrderNotFound,
				Message: "AllPay has no order " + orderID + " for this API login", NeedsReview: true}
		}
		// Not read: nothing was sent, the engine asks again later.
		return payments.RefundResult{}, fmt.Errorf("allpay: Refund: read the order before refunding: %w", err)
	}
	if req.Currency != "" && st.Currency != "" && !strings.EqualFold(req.Currency, st.Currency) {
		return payments.RefundResult{}, review(RefundCodeCurrency,
			fmt.Sprintf("the refund is in %s, AllPay holds the order in %s", strings.ToUpper(req.Currency), st.Currency))
	}
	charged := st.ChargedMinor()
	if (st.Status == StatusPaid || st.Status == StatusPartiallyRefunded || st.Status == StatusRefunded) &&
		(charged <= 0 || st.Currency == "") {
		// An answer without the order's amount or currency cannot be
		// compared: nothing is sent, the engine asks again later.
		return payments.RefundResult{}, fmt.Errorf("allpay: Refund: the order %s is reported without an amount or currency", orderID)
	}
	var refunded int64
	switch st.Status {
	case StatusPaid:
		refunded = 0
	case StatusPartiallyRefunded:
		// Status 4 must name a part: neither nothing nor everything.
		if st.RefundedMinor <= 0 || st.RefundedMinor >= charged {
			return payments.RefundResult{}, review(RefundCodePartialUnclear,
				fmt.Sprintf("AllPay reports the order refunded in part, refunded %s of %s", MinorToMajor(st.RefundedMinor), MinorToMajor(charged)))
		}
		refunded = st.RefundedMinor
	case StatusRefunded:
		refunded = charged
	default:
		return payments.RefundResult{}, review(RefundCodeNotPaid,
			fmt.Sprintf("AllPay reports the order with status %d, not paid", st.Status))
	}
	known, uncertain := req.OtherRefundedMinor, req.OtherUncertainMinor
	switch extra := refunded - known; {
	case extra < 0:
		return payments.RefundResult{}, review(RefundCodeKnowsLess,
			fmt.Sprintf("AllPay reports %s refunded, arena knows of %s", MinorToMajor(refunded), MinorToMajor(known)))
	case extra == req.AmountMinor && uncertain == 0:
		// An earlier call of this refund reached AllPay and its answer was
		// lost. The money is back; nothing is sent again.
		return accepted, nil
	case extra != 0:
		return payments.RefundResult{}, review(RefundCodeUnexplained,
			fmt.Sprintf("AllPay reports %s refunded, arena can account for %s (%s more may have been refunded by calls without an answer)",
				MinorToMajor(refunded), MinorToMajor(known), MinorToMajor(uncertain)))
	}
	if req.Attempt > 1 && !req.FirstAttemptAt.IsZero() && a.now().Sub(req.FirstAttemptAt) < SettleWait {
		return payments.RefundResult{}, fmt.Errorf("allpay: Refund: an earlier call of this refund may still be processed by AllPay, nothing sent until %s after the first call",
			SettleWait)
	}
	if left := charged - refunded; req.AmountMinor > left {
		return payments.RefundResult{}, review(RefundCodeOverRemaining,
			fmt.Sprintf("the refund of %s is more than the %s AllPay has left on the order", MinorToMajor(req.AmountMinor), MinorToMajor(left)))
	}

	out, err := a.post(ctx, methodRefund, map[string]any{
		"order_id": orderID,
		"amount":   json.Number(MinorToMajor(req.AmountMinor)),
	})
	if err != nil {
		var apiErr *APIError
		if !errors.As(err, &apiErr) {
			// Transport error, 5xx, an unreadable answer: the refund may or
			// may not have happened. The engine retries, and the next call
			// reads the order first.
			return payments.RefundResult{}, fmt.Errorf("allpay: Refund: %w", err)
		}
		code := RefundCodeAPIRefused
		if apiErr.Code == ErrCodeRefundFailure {
			code = RefundCodeProcessorRefused
		}
		return payments.RefundResult{}, &payments.RefundDeclinedError{
			Code:    code,
			Message: fmt.Sprintf("AllPay error %d: %s", apiErr.Code, apiErr.Message),
			// "Wrong status" may mean the order is already refunded.
			NeedsReview: apiErr.Code == ErrCodeWrongStatus,
		}
	}
	status, _ := scalarValue(out["status"])
	if status != "3" && status != "4" {
		// AllPay answered without an error and without a refund status. The
		// next call reads the order and finds out.
		return payments.RefundResult{}, fmt.Errorf("allpay: Refund: the answer has status %q, not 3 or 4", status)
	}
	return accepted, nil
}

func review(code, message string) error {
	return &payments.RefundDeclinedError{Code: code, Message: message, NeedsReview: true}
}
