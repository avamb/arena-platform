// refund.go — refunds through PayPlus (Transactions/RefundByTransactionUID).
//
// PayPlus answers a refund synchronously (the refund transaction comes back
// with status_code 000) and documents no idempotency key. A call whose
// answer was lost may have returned the money, and a blind repeat would
// return it twice — for a partial refund nothing on PayPlus's side would
// stop the second one. So every refund carries a marker in more_info
// ("arena-refund:<arena refund id>"), and a call that may be a repeat
// (Attempt != 1) never sends before it has accounted for the money:
//
//  1. It searches for a refund under its own marker (Transactions/View by
//     more_info, exact match). Found and approved: that refund is the
//     answer, nothing is sent.
//
//  2. Otherwise it reads the charge's refunded total from PayPlus
//     (PaymentPages/ipn by transaction_uid with related transactions) and
//     applies the AllPay rule (spec 39 §8). With P the approved refunds
//     PayPlus shows on the charge, K the refunds arena knows were accepted
//     (RefundRequest.OtherRefundedMinor), U the refunds whose call ended
//     without an answer (OtherUncertainMinor) and A this refund.
//
// The rule, in that order:
//
//   - the total cannot be read: a human checks, nothing is sent;
//   - P < K: a human checks (PayPlus shows less than arena knows);
//   - P - K = A and U = 0: an earlier call of THIS refund landed; a refund
//     of A under our marker is the answer, otherwise a human checks;
//   - P - K = 0: nothing unexplained; inside SettleWait of the first call
//     nothing is sent (a refund still being booked may not show yet),
//     after it the refund is sent;
//   - anything else: a human checks, nothing is sent.
//
// "A human checks" is a RefundDeclinedError with NeedsReview: the refund
// engine parks the refund in manual_review and leaves the tickets valid. A
// refund made in the PayPlus dashboard that arena does not know shows up as
// an unexplained difference and stops arena's later re-sends on that charge.
//
// The first call of a refund (Attempt == 1) sends at once: nothing earlier
// can have reached PayPlus. Which fields PayPlus returns for related
// transactions, and whether View finds refunds by more_info, must be
// confirmed on restapidev (docs/ops/payplus.md §5).
package payplus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
)

var (
	_ payments.Refunder          = (*Adapter)(nil)
	_ payments.ChargeRefResolver = (*Adapter)(nil)
)

// SettleWait is how long a repeated call of one refund waits after its
// first call before it may send the refund again when PayPlus shows nothing
// unexplained on the charge.
const SettleWait = 10 * time.Minute

// RefundMarkerPrefix prefixes the arena refund id in a refund's more_info.
const RefundMarkerPrefix = "arena-refund:"

// Stable failure codes the module reports to the refund engine.
const (
	RefundCodeRefused        = "payplus_refused"
	RefundCodeDeclined       = "payplus_declined"
	RefundCodeNoCharge       = "payplus_charge_not_found"
	RefundCodeNoRefundID     = "payplus_refund_without_id"
	RefundCodeTotalUnknown   = "payplus_refunded_total_unknown"
	RefundCodeKnowsLess      = "payplus_refunded_less_than_known"
	RefundCodeUnexplained    = "payplus_refund_unexplained"
	RefundCodeLandedUnmarked = "payplus_refund_landed_unmarked"
)

// RefundMarker is the more_info value of the refund arena sends for refundID.
func RefundMarker(refundID string) string { return RefundMarkerPrefix + strings.TrimSpace(refundID) }

// ResolveChargeRef implements payments.ChargeRefResolver: a payment whose
// callback never reached arena knows only its page_request_uid; the approved
// charge made on that link is read back (PaymentPages/ipn).
func (a *Adapter) ResolveChargeRef(ctx context.Context, providerPaymentID string) (string, error) {
	ref := strings.TrimSpace(providerPaymentID)
	if ref == "" {
		return "", payments.ErrRefundChargeRefMissing
	}
	txs, err := a.PaymentRequestTransactions(ctx, ref)
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && (apiErr.notFound() || apiErr.authRefusal()) {
			return "", &payments.RefundDeclinedError{Code: RefundCodeNoCharge,
				Message: "PayPlus has no approved charge for payment link " + ref + ": " + apiErr.Description, NeedsReview: true}
		}
		return "", fmt.Errorf("payplus: ResolveChargeRef: %w", err)
	}
	for _, tx := range txs {
		if tx.Approved() && !tx.IsRefund() && tx.UID != "" {
			return tx.UID, nil
		}
	}
	return "", &payments.RefundDeclinedError{Code: RefundCodeNoCharge,
		Message: "PayPlus has no approved charge for payment link " + ref, NeedsReview: true}
}

// Refund implements payments.Refunder. See the comment at the top of this
// file for what a repeated call checks before it sends anything.
func (a *Adapter) Refund(ctx context.Context, req payments.RefundRequest) (payments.RefundResult, error) {
	chargeUID := strings.TrimSpace(req.ProviderChargeRef)
	refundID := strings.TrimSpace(req.IdempotencyKey)
	switch {
	case req.AmountMinor <= 0:
		return payments.RefundResult{}, payments.ErrRefundAmountInvalid
	case req.PaymentAmountMinor > 0 && req.AmountMinor > req.PaymentAmountMinor:
		return payments.RefundResult{}, payments.ErrRefundExceedsPayment
	case chargeUID == "":
		return payments.RefundResult{}, payments.ErrRefundChargeRefMissing
	case refundID == "":
		return payments.RefundResult{}, errors.New("payplus: Refund: an idempotency key (the arena refund id) is required")
	case a.cfg.APIKey == "" || a.cfg.SecretKey == "":
		return payments.RefundResult{}, errors.New("payplus: Refund: the config has no api_key or secret_key")
	}
	marker := RefundMarker(refundID)

	if req.Attempt != 1 {
		done, settled, err := a.accountForEarlierCalls(ctx, req, chargeUID, marker)
		if err != nil || settled {
			return done, err
		}
	}

	env, err := a.call(ctx, http.MethodPost, "Transactions/RefundByTransactionUID", map[string]any{
		"transaction_uid": chargeUID,
		"amount":          json.Number(MinorToMajor(req.AmountMinor)),
		"more_info":       marker,
	})
	if err != nil {
		var apiErr *APIError
		if !errors.As(err, &apiErr) {
			// Transport error, 5xx, an unreadable answer: the outcome is
			// unknown. The engine retries, and the retry accounts for the
			// money before it sends anything.
			return payments.RefundResult{}, fmt.Errorf("payplus: Refund: %w", err)
		}
		// PayPlus answered and refused. Its refusal codes are not
		// documented: some ("already refunded", "amount above what is
		// left") may mean the money is ALREADY back, so a human checks.
		return payments.RefundResult{}, review(RefundCodeRefused, apiErr.Error())
	}
	txs := transactionsFromData(env.Data)
	if len(txs) == 0 || txs[0].UID == "" {
		// Success without a transaction: cannot be tracked. The engine
		// treats it as unknown and the retry accounts for the money first.
		return payments.RefundResult{}, fmt.Errorf("payplus: Refund: %s", RefundCodeNoRefundID)
	}
	tx := txs[0]
	if tx.StatusCode != "" && tx.StatusCode != StatusCodeApproved {
		// The card processor refused the refund: no money moved.
		return payments.RefundResult{}, &payments.RefundDeclinedError{
			Code:    RefundCodeDeclined,
			Message: "PayPlus refund status_code " + tx.StatusCode,
		}
	}
	return payments.RefundResult{ProviderRefundID: tx.UID, Status: payments.RefundSucceeded}, nil
}

// accountForEarlierCalls decides whether a possibly repeated call may send.
// settled true means the call is over: done (an earlier call of this refund
// landed) or err (a human checks, or wait and ask again). settled false with
// no error means the refund may be sent.
func (a *Adapter) accountForEarlierCalls(ctx context.Context, req payments.RefundRequest, chargeUID, marker string) (payments.RefundResult, bool, error) {
	if done, found, err := a.findRefund(ctx, marker); err == nil && found {
		return done, true, nil
	}
	// A failed marker search is not fatal: the refunded total below is
	// what decides, and it is read in any case.

	related, err := a.RelatedTransactions(ctx, chargeUID)
	if err != nil {
		return payments.RefundResult{}, true, review(RefundCodeTotalUnknown,
			"the refunded total of PayPlus charge "+chargeUID+" could not be read: "+err.Error())
	}
	refunded, ours, readable := refundedOnCharge(related, chargeUID, marker)
	if !readable {
		return payments.RefundResult{}, true, review(RefundCodeTotalUnknown,
			"PayPlus did not return charge "+chargeUID+" with its related transactions in a form arena can account for")
	}
	known, uncertain := req.OtherRefundedMinor, req.OtherUncertainMinor
	switch extra := refunded - known; {
	case extra < 0:
		return payments.RefundResult{}, true, review(RefundCodeKnowsLess,
			fmt.Sprintf("PayPlus shows %s refunded, arena knows of %s", MinorToMajor(refunded), MinorToMajor(known)))
	case extra == req.AmountMinor && uncertain == 0:
		if ours.UID != "" && ours.AmountMinor == req.AmountMinor {
			return payments.RefundResult{ProviderRefundID: ours.UID, Status: payments.RefundSucceeded}, true, nil
		}
		return payments.RefundResult{}, true, review(RefundCodeLandedUnmarked,
			fmt.Sprintf("PayPlus shows %s refunded beyond what arena knows, the size of this refund, but not under its marker", MinorToMajor(extra)))
	case extra != 0:
		return payments.RefundResult{}, true, review(RefundCodeUnexplained,
			fmt.Sprintf("PayPlus shows %s refunded, arena can account for %s (%s more may have been refunded by calls without an answer)",
				MinorToMajor(refunded), MinorToMajor(known), MinorToMajor(uncertain)))
	}
	if req.Attempt > 1 && !req.FirstAttemptAt.IsZero() && a.now().Sub(req.FirstAttemptAt) < SettleWait {
		return payments.RefundResult{}, true, fmt.Errorf("payplus: Refund: an earlier call of this refund may still be booked by PayPlus, nothing sent until %s after the first call", SettleWait)
	}
	return payments.RefundResult{}, false, nil
}

// refundedOnCharge sums the approved refunds among a charge's related
// transactions. readable is false when the charge itself is missing from the
// answer or a related transaction cannot be classified (no type), because
// then the total would be a guess. ours is the approved refund carrying
// marker, if any.
func refundedOnCharge(txs []Transaction, chargeUID, marker string) (total int64, ours Transaction, readable bool) {
	sawCharge := false
	for _, tx := range txs {
		if tx.UID == chargeUID {
			sawCharge = true
			continue
		}
		if tx.Type == "" {
			return 0, Transaction{}, false
		}
		if !tx.IsRefund() || !tx.Approved() {
			continue
		}
		total += tx.AmountMinor
		if tx.MoreInfo == marker {
			ours = tx
		}
	}
	return total, ours, sawCharge
}

// findRefund searches PayPlus for an approved refund under marker. Only a
// transaction whose more_info IS the marker counts: an empty or other
// more_info is somebody else's.
func (a *Adapter) findRefund(ctx context.Context, marker string) (payments.RefundResult, bool, error) {
	txs, err := a.ViewByMoreInfo(ctx, marker)
	if err != nil {
		return payments.RefundResult{}, false, err
	}
	for _, tx := range txs {
		if tx.MoreInfo != marker || tx.UID == "" {
			continue
		}
		if tx.StatusCode == StatusCodeApproved && !tx.Cancelled && (tx.Type == "" || tx.IsRefund()) {
			return payments.RefundResult{ProviderRefundID: tx.UID, Status: payments.RefundSucceeded}, true, nil
		}
	}
	return payments.RefundResult{}, false, nil
}

func review(code, message string) error {
	return &payments.RefundDeclinedError{Code: code, Message: message, NeedsReview: true}
}
