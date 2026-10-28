// refund.go — refunds through PayPlus (Transactions/RefundByTransactionUID).
//
// PayPlus answers a refund synchronously (the refund transaction comes back
// with status_code 000) and documents no idempotency key. A call whose
// answer was lost may have returned the money, and a blind repeat would
// return it twice. So every refund carries a marker in more_info
// ("arena-refund:<arena refund id>"), and a call that may be a repeat
// (Attempt != 1) first searches PayPlus for a refund under that marker
// (Transactions/View by more_info):
//
//	found, approved   → that refund is the answer; nothing is sent again
//	nothing found     → inside SettleWait of the first call: nothing is
//	                    sent, the engine asks again later (a refund still
//	                    being booked by PayPlus may not be searchable yet);
//	                    after it: the refund is sent
//	search failed     → nothing is sent, the engine asks again later
//
// Whether View finds REFUND transactions by more_info must be confirmed on
// restapidev (docs/ops/payplus.md); until then a lost answer can at worst
// stall a refund for a human, never pay it twice within SettleWait.
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
// first call before it may send the refund again when PayPlus shows none.
const SettleWait = 10 * time.Minute

// RefundMarkerPrefix prefixes the arena refund id in a refund's more_info.
const RefundMarkerPrefix = "arena-refund:"

// Stable failure codes the module reports to the refund engine.
const (
	RefundCodeRefused    = "payplus_refused"
	RefundCodeDeclined   = "payplus_declined"
	RefundCodeNoCharge   = "payplus_charge_not_found"
	RefundCodeNoRefundID = "payplus_refund_without_id"
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

// Refund implements payments.Refunder.
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
		// Possibly a repeat: find what an earlier call did.
		done, found, err := a.findRefund(ctx, marker)
		if err != nil {
			return payments.RefundResult{}, fmt.Errorf("payplus: Refund: look for an earlier call of this refund: %w", err)
		}
		if found {
			return done, nil
		}
		if req.Attempt > 1 && !req.FirstAttemptAt.IsZero() && a.now().Sub(req.FirstAttemptAt) < SettleWait {
			return payments.RefundResult{}, fmt.Errorf("payplus: Refund: an earlier call of this refund may still be booked by PayPlus, nothing sent until %s after the first call", SettleWait)
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
			// unknown. The engine retries, and the retry searches first.
			return payments.RefundResult{}, fmt.Errorf("payplus: Refund: %w", err)
		}
		// PayPlus answered and refused. Its refusal codes are not
		// documented: some ("already refunded", "amount above what is
		// left") may mean the money is ALREADY back, so a human checks.
		return payments.RefundResult{}, &payments.RefundDeclinedError{
			Code:        RefundCodeRefused,
			Message:     apiErr.Error(),
			NeedsReview: true,
		}
	}
	txs := transactionsFromData(env.Data)
	if len(txs) == 0 || txs[0].UID == "" {
		// Success without a transaction: cannot be tracked. The engine
		// treats an id-less acceptance as unknown and the retry searches.
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

// findRefund searches PayPlus for an approved refund under marker.
func (a *Adapter) findRefund(ctx context.Context, marker string) (payments.RefundResult, bool, error) {
	txs, err := a.ViewByMoreInfo(ctx, marker)
	if err != nil {
		return payments.RefundResult{}, false, err
	}
	for _, tx := range txs {
		if tx.MoreInfo != "" && tx.MoreInfo != marker {
			continue
		}
		if tx.UID != "" && tx.StatusCode == StatusCodeApproved && (tx.Type == "" || tx.IsRefund()) {
			return payments.RefundResult{ProviderRefundID: tx.UID, Status: payments.RefundSucceeded}, true, nil
		}
	}
	return payments.RefundResult{}, false, nil
}
