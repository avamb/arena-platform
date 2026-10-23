// billing_webhooks.go — the two places money for a platform invoice arrives
// from outside (billing tasks B3-3, B4):
//
//   - POST /v1/billing/stripe/webhook: the PLATFORM's Stripe account (never an
//     organizer's) reports a paid Checkout page (credit pack) or a paid
//     hosted Stripe invoice (usage). The event is verified with
//     BILLING_STRIPE_WEBHOOK_SECRET, the invoice is marked paid and, for a
//     pack, its credit lot is written in the SAME transaction. The event id
//     is recorded in billing_stripe_events in that transaction too, so a
//     redelivery changes nothing; a database failure answers 500 so Stripe
//     redelivers.
//   - POST /v1/billing/bank/webhook/{provider}: a bank (or an aggregator)
//     posts incoming transfers signed with BILLING_BANK_WEBHOOK_SECRET. Each
//     transfer is stored once (provider + external id) and matched to the
//     issued invoice whose payment reference it carries and whose amount it
//     equals (the total, or the total without the card fee: a transfer is a
//     net payment). Anything else waits for an operator with a reason.
//
// Neither route takes a JWT; both are flat public routes guarded by their
// signature.
package hbilling

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/stripebilling"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing/invoicing"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// Outcomes stored in billing_stripe_events.outcome.
const (
	stripeOutcomePaid           = "paid"
	stripeOutcomeAlreadyPaid    = "already_paid"
	stripeOutcomeUnknownInvoice = "unknown_invoice"
	stripeOutcomeAmountMismatch = "amount_mismatch"
	stripeOutcomeNotOpen        = "invoice_not_open"
	stripeOutcomeNotPaidYet     = "not_paid_yet"
	stripeOutcomeLegacyPaid     = "legacy_paid"
)

// maxWebhookBody caps a webhook body (a Stripe event is a few kilobytes, a
// bank batch of 500 transfers well under a megabyte).
const maxWebhookBody = 1 << 20

// HandleStripeWebhook answers POST /v1/billing/stripe/webhook.
func (h *AccountBillingHandler) HandleStripeWebhook(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w, r) {
		return
	}
	if h.stripe == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"stripe_billing.unavailable", "platform Stripe billing is not configured", r))
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBody+1))
	if err != nil || len(body) > maxWebhookBody {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("stripe_billing.read_error", "cannot read the request body", r))
		return
	}
	ev, err := h.stripe.ParseBillingEvent(body, r.Header.Get("Stripe-Signature"))
	if err != nil {
		if errors.Is(err, payments.ErrInvalidWebhookSignature) {
			httputil.WriteJSON(w, http.StatusUnauthorized, httputil.ErrorEnvelope(
				"stripe_billing.invalid_signature", "webhook signature verification failed", r))
			return
		}
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("stripe_billing.parse_error", "failed to parse the event", r))
		return
	}
	if ev.ID == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("stripe_billing.parse_error", "the event has no id", r))
		return
	}
	switch ev.Type {
	case stripebilling.EventCheckoutCompleted, stripebilling.EventCheckoutAsyncSucceeded, stripebilling.EventInvoicePaid:
	default:
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"received": true, "processed": false})
		return
	}
	outcome, invoiceID, err := h.applyStripeEvent(r.Context(), ev)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "billing stripe webhook: apply failed",
			"stripe_event_id", ev.ID, "event_type", ev.Type, "error", err)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"stripe_billing.apply_failed", "the payment could not be recorded yet; Stripe will deliver it again", r))
		return
	}
	switch outcome {
	case stripeOutcomeUnknownInvoice, stripeOutcomeAmountMismatch, stripeOutcomeNotOpen:
		// Money arrived that the platform cannot book on its own: an operator
		// must look (the event row keeps the outcome).
		h.logger.ErrorContext(r.Context(), "billing stripe webhook: payment needs an operator",
			"stripe_event_id", ev.ID, "outcome", outcome, "invoice_id", invoiceID)
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"received": true, "processed": true, "outcome": outcome})
}

// applyStripeEvent books one event in one transaction. It returns the
// outcome; err only for a failure that must be retried.
func (h *AccountBillingHandler) applyStripeEvent(ctx context.Context, ev *stripebilling.BillingEvent) (string, string, error) {
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		return "", "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := gen.New(tx)

	// A checkout event before the money settled (an async method) is not a
	// payment; it is NOT recorded, so the later async_payment_succeeded event
	// of the same session still counts.
	if ev.Type == stripebilling.EventCheckoutCompleted && ev.PaymentStatus != "paid" {
		return stripeOutcomeNotPaidYet, "", nil
	}

	inv, err := findStripeInvoice(ctx, q, ev)
	outcome, via := "", invoicing.PaidViaStripeCard
	if ev.Type == stripebilling.EventInvoicePaid {
		via = invoicing.PaidViaStripeInvoice
	}
	var invID *uuid.UUID
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		outcome = stripeOutcomeUnknownInvoice
	case err != nil:
		return "", "", err
	case inv.AccountID == nil:
		// A legacy tariff invoice pushed to Stripe before billing accounts
		// existed: keep its old behaviour (issued -> paid, nothing else).
		invID = &inv.ID
		outcome = stripeOutcomeNotOpen
		if inv.State == "issued" {
			if _, err := q.UpdateInvoiceState(ctx, inv.ID, "paid"); err != nil {
				return "", "", err
			}
			outcome = stripeOutcomeLegacyPaid
		}
	default:
		invID = &inv.ID
		var pi *string
		if ev.PaymentIntentID != "" {
			pi = &ev.PaymentIntentID
		}
		_, changed, err := invoicing.MarkPaid(ctx, q, inv.ID, invoicing.Payment{
			Via: via, Reference: "stripe:" + ev.ObjectID, AmountMinor: ev.AmountMinor, Currency: ev.Currency,
			At: h.now(), PaymentIntentID: pi, ActorType: "stripe", ActorID: ev.ID,
		})
		switch {
		case err == nil && changed:
			outcome = stripeOutcomePaid
		case err == nil:
			outcome = stripeOutcomeAlreadyPaid
		case errors.Is(err, invoicing.ErrAmountMismatch):
			outcome = stripeOutcomeAmountMismatch
		case errors.Is(err, invoicing.ErrNotPayable), errors.Is(err, invoicing.ErrAlreadyPaid):
			outcome = stripeOutcomeNotOpen
		default:
			return "", "", err
		}
	}
	fresh, err := q.InsertBillingStripeEvent(ctx, ev.ID, ev.Type, invID, outcome)
	if err != nil {
		return "", "", err
	}
	if !fresh {
		// Applied before: roll this attempt back (it changed nothing new
		// anyway, MarkPaid is idempotent by reference) and acknowledge.
		return stripeOutcomeAlreadyPaid, idString(invID), nil
	}
	if err := tx.Commit(ctx); err != nil {
		return "", "", err
	}
	return outcome, idString(invID), nil
}

func idString(id *uuid.UUID) string {
	if id == nil {
		return ""
	}
	return id.String()
}

// findStripeInvoice finds the platform invoice an event pays: a Checkout
// Session by its id, a Stripe invoice by its id, then the arena_invoice_id
// metadata as a fallback.
func findStripeInvoice(ctx context.Context, q *gen.Queries, ev *stripebilling.BillingEvent) (gen.BillingInvoiceRow, error) {
	var inv gen.BillingInvoiceRow
	err := pgx.ErrNoRows
	if ev.Type == stripebilling.EventInvoicePaid {
		inv, err = q.GetBillingInvoiceByStripeInvoice(ctx, ev.ObjectID)
	} else if ev.ObjectID != "" {
		inv, err = q.GetBillingInvoiceByCheckoutSession(ctx, ev.ObjectID)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return inv, err
	}
	ref := ev.ArenaInvoiceID
	if ref == "" {
		ref = ev.ClientReferenceID
	}
	id, perr := uuid.Parse(ref)
	if perr != nil {
		return inv, pgx.ErrNoRows
	}
	return q.GetBillingInvoice(ctx, id)
}

// ─────────────────────────────────────────────────────────────────────────────
// Bank webhook
// ─────────────────────────────────────────────────────────────────────────────

// BankTransfer is one incoming transfer as a bank posts it.
type BankTransfer struct {
	ID               string `json:"id"`
	BookedAt         string `json:"booked_at"`
	AmountMinor      int64  `json:"amount_minor"`
	Currency         string `json:"currency"`
	Reference        string `json:"reference"`
	CounterpartyName string `json:"counterparty_name"`
}

// maxBankTransfers caps one delivery.
const maxBankTransfers = 500

var bankProviderRE = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,39}$`)

// HandleBankWebhook answers POST /v1/billing/bank/webhook/{provider}. The
// body is {"transactions": [BankTransfer...]} signed in X-Arena-Signature as
// "sha256=<hex HMAC-SHA256 of the raw body>" with BILLING_BANK_WEBHOOK_SECRET.
// Outgoing and zero amounts are skipped. A repeat of a transfer is a no-op.
func (h *AccountBillingHandler) HandleBankWebhook(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w, r) {
		return
	}
	if h.bankSecret == "" {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"billing.bank_webhook_not_configured", "the bank webhook is not configured", r))
		return
	}
	provider := chi.URLParam(r, "provider")
	if !bankProviderRE.MatchString(provider) || provider == "manual" {
		httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("billing.bank_provider_unknown", "unknown provider", r))
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBody+1))
	if err != nil || len(body) > maxWebhookBody {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("billing.invalid_body", "cannot read the request body", r))
		return
	}
	if !validBankSignature(r.Header.Get("X-Arena-Signature"), body, h.bankSecret) {
		httputil.WriteJSON(w, http.StatusUnauthorized, httputil.ErrorEnvelope("billing.bank_invalid_signature", "signature verification failed", r))
		return
	}
	var in struct {
		Transactions []BankTransfer `json:"transactions"`
	}
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("billing.invalid_body", "the body is not {\"transactions\": [...]}", r))
		return
	}
	res, status, msg := h.IngestBankTransfers(r.Context(), provider, in.Transactions, "bank:"+provider)
	if status != 0 {
		httputil.WriteJSON(w, status, httputil.ErrorEnvelope("billing.invalid_body", msg, r))
		return
	}
	httputil.WriteJSON(w, http.StatusOK, res)
}

func validBankSignature(header string, body []byte, secret string) bool {
	sig, ok := strings.CutPrefix(strings.TrimSpace(header), "sha256=")
	if !ok {
		return false
	}
	got, err := hex.DecodeString(sig)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(got, mac.Sum(nil))
}

// BankIngestResult summarizes one delivery.
type BankIngestResult struct {
	Received  int              `json:"received"`
	Skipped   int              `json:"skipped"`
	Duplicate int              `json:"duplicate"`
	Matched   int              `json:"matched"`
	Unmatched int              `json:"unmatched"`
	Items     []map[string]any `json:"items"`
}

// parsedTransfer is one validated incoming transfer.
type parsedTransfer struct {
	t      BankTransfer
	booked time.Time
}

// parseBankTransfers validates a whole batch before anything is written; a
// non-empty message refuses it.
func parseBankTransfers(list []BankTransfer) ([]parsedTransfer, string) {
	if len(list) == 0 || len(list) > maxBankTransfers {
		return nil, "send 1-500 transactions"
	}
	ok := make([]parsedTransfer, 0, len(list))
	for i, t := range list {
		t.ID = strings.TrimSpace(t.ID)
		t.Currency = strings.ToUpper(strings.TrimSpace(t.Currency))
		booked, err := time.Parse(time.RFC3339, t.BookedAt)
		at := "transactions[" + strconv.Itoa(i) + "]"
		switch {
		case t.ID == "" || len(t.ID) > 200:
			return nil, at + ".id is required, at most 200 characters"
		case err != nil:
			return nil, at + ".booked_at must be RFC 3339"
		case len(t.Currency) != 3:
			return nil, at + ".currency must be a 3-letter code"
		case len(t.Reference) > 1000 || len(t.CounterpartyName) > 300:
			return nil, at + " reference or counterparty is too long"
		}
		ok = append(ok, parsedTransfer{t: t, booked: booked.UTC()})
	}
	return ok, ""
}

// ingestBankTransferTx stores one transfer and tries to match it, on the
// caller's transaction.
func ingestBankTransferTx(ctx context.Context, q *gen.Queries, provider string, p parsedTransfer, by string, now time.Time) (map[string]any, error) {
	t := p.t
	row, inserted, err := q.InsertBankTransaction(ctx, gen.InsertBankTransactionParams{
		Provider: provider, ExternalID: t.ID, BookedAt: p.booked, AmountMinor: t.AmountMinor, Currency: t.Currency,
		Reference: ptrString(strings.TrimSpace(t.Reference)), CounterpartyName: ptrString(strings.TrimSpace(t.CounterpartyName)),
	})
	if err != nil {
		return nil, err
	}
	if !inserted {
		return map[string]any{"id": t.ID, "status": "duplicate"}, nil
	}
	m, err := invoicing.MatchBankPayment(ctx, q, row.ID, by, now)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"id": t.ID, "transaction_id": m.Transaction.ID.String(), "status": m.Transaction.Status}
	if m.Invoice != nil {
		out["invoice_id"] = m.Invoice.ID.String()
		out["invoice_number"] = m.Invoice.Number
	}
	if m.Reason != "" {
		out["unmatched_reason"] = m.Reason
	}
	return out, nil
}

func (r *BankIngestResult) add(item map[string]any) {
	switch item["status"] {
	case "duplicate":
		r.Duplicate++
	case "matched":
		r.Matched++
	default:
		r.Unmatched++
	}
	r.Items = append(r.Items, item)
}

// IngestBankTransfers validates a batch, stores each transfer and tries to
// match it, one transaction per transfer (a failure of one never undoes
// another's payment). A non-zero status means the batch was refused; a 500
// may come after earlier transfers were stored, and a redelivery skips them.
func (h *AccountBillingHandler) IngestBankTransfers(ctx context.Context, provider string, list []BankTransfer, by string) (BankIngestResult, int, string) {
	res := BankIngestResult{Items: []map[string]any{}}
	ok, msg := parseBankTransfers(list)
	if msg != "" {
		return res, http.StatusBadRequest, msg
	}
	res.Received = len(ok)
	for _, p := range ok {
		if p.t.AmountMinor <= 0 {
			res.Skipped++ // an outgoing or empty movement is not a payment
			continue
		}
		item, err := h.ingestOne(ctx, provider, p, by)
		if err != nil {
			h.logger.ErrorContext(ctx, "billing bank: transfer not stored", "provider", provider, "error", err)
			return res, http.StatusInternalServerError, "a transfer could not be stored; the bank should deliver the batch again"
		}
		res.add(item)
	}
	return res, 0, ""
}

func (h *AccountBillingHandler) ingestOne(ctx context.Context, provider string, p parsedTransfer, by string) (map[string]any, error) {
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	item, err := ingestBankTransferTx(ctx, gen.New(tx), provider, p, by, h.now())
	if err != nil {
		return nil, err
	}
	if item["status"] == "duplicate" {
		return item, nil
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return item, nil
}
