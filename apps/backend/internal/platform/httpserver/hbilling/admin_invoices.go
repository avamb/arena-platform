// admin_invoices.go — the operator's side of platform invoices (billing tasks
// B3-3, B4): usage invoices of a month, issuing, recording a payment that
// arrived some other way, voiding, the average card fee of the payment
// system, and incoming bank payments that need a decision.
//
// Same rules as the rest of /v1/admin/billing: billing.admin plus the
// platform superadmin marker, X-Admin-Reason on every write, the audit row on
// the change's own transaction (admin_audit_guard_test.go reads this file).
package hbilling

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/stripebilling"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing/invoicing"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// Audit actions of the invoice routes.
const (
	AuditUsageInvoiceCreate  = "v1.admin.billing.invoice.generate"
	AuditInvoiceIssue        = "v1.admin.billing.invoice.issue"
	AuditInvoiceMarkPaid     = "v1.admin.billing.invoice.mark_paid"
	AuditInvoiceVoid         = "v1.admin.billing.invoice.void"
	AuditPaymentFeeCreate    = "v1.admin.billing.payment_fee.create"
	AuditBankTransferImport  = "v1.admin.billing.bank_transaction.import"
	AuditBankTransferMatch   = "v1.admin.billing.bank_transaction.match"
	AuditBankTransferIgnore  = "v1.admin.billing.bank_transaction.ignore"
	manualBankProvider       = "manual"
	maxAdminBankImportReason = 500
)

// WithPlatformStripe gives the handler the platform Stripe account, used to
// void a Stripe invoice or close a Checkout page before an invoice is voided
// and to make the card payment page of an invoice it issues.
func (h *AdminHandler) WithPlatformStripe(s PlatformStripe, returnBaseURL string) *AdminHandler {
	h.stripe = s
	h.returnBaseURL = strings.TrimRight(returnBaseURL, "/")
	return h
}

func (h *AdminHandler) invoiceID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "invoice_id"))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("billing.bad_request", "invoice_id must be a UUID", r))
		return uuid.Nil, false
	}
	return id, true
}

// invoiceError answers the domain errors of invoicing; false when err is not
// one of them.
func invoiceError(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		notFound(w, r, "billing.invoice_not_found", "there is no such invoice")
	case errors.Is(err, invoicing.ErrNotDraft):
		conflict(w, r, "billing.invoice_not_draft", "only a draft invoice can be issued")
	case errors.Is(err, invoicing.ErrNotPayable):
		conflict(w, r, "billing.invoice_not_open", "the invoice is not open for payment")
	case errors.Is(err, invoicing.ErrAlreadyPaid):
		conflict(w, r, "billing.invoice_already_paid", "the invoice is already paid")
	case errors.Is(err, invoicing.ErrPaidNeedsCreditNote):
		conflict(w, r, "billing.invoice_paid", "a paid invoice is never voided; correct it with a credit note")
	case errors.Is(err, invoicing.ErrAmountMismatch):
		unprocessable(w, r, "billing.amount_mismatch", err.Error())
	case errors.Is(err, invoicing.ErrCardFeeNotConfigured):
		unprocessable(w, r, "billing.card_fee_not_configured",
			"there is no card fee for this currency yet; add it under payment fees or issue for a bank transfer")
	case errors.Is(err, invoicing.ErrPeriodNotClosed):
		unprocessable(w, r, "billing.period_not_closed", "the month is not over yet")
	case errors.Is(err, invoicing.ErrBadInput):
		unprocessable(w, r, "billing.invalid_invoice", err.Error())
	default:
		return false
	}
	return true
}

// HandleListBillingInvoices answers GET /v1/admin/billing/invoices
// (?state=draft|issued|paid|void, default issued; ?limit= 1-200).
func (h *AdminHandler) HandleListBillingInvoices(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w, r, false) {
		return
	}
	state := r.URL.Query().Get("state")
	switch state {
	case "":
		state = "issued"
	case "draft", "issued", "paid", "void":
	default:
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("billing.bad_request", "state must be draft, issued, paid or void", r))
		return
	}
	limit, ok := adminLimit(w, r, 100)
	if !ok {
		return
	}
	rows, err := h.queries.ListOpenBillingInvoices(r.Context(), state, limit)
	if err != nil {
		h.fail(w, r, "list invoices", err)
		return
	}
	items := make([]map[string]any, 0, len(rows))
	for _, inv := range rows {
		items = append(items, invoiceJSON(invoicing.Invoice{BillingInvoiceRow: inv}))
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"invoices": items})
}

func adminLimit(w http.ResponseWriter, r *http.Request, def int32) (int32, bool) {
	v := r.URL.Query().Get("limit")
	if v == "" {
		return def, true
	}
	n, err := strconv.ParseInt(v, 10, 32)
	if err != nil || n < 1 || n > 200 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("billing.bad_request", "limit must be 1-200", r))
		return 0, false
	}
	return int32(n), true
}

// HandleGetBillingInvoice answers GET /v1/admin/billing/invoices/{invoice_id}.
func (h *AdminHandler) HandleGetBillingInvoice(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w, r, false) {
		return
	}
	id, ok := h.invoiceID(w, r)
	if !ok {
		return
	}
	inv, err := invoicing.Load(r.Context(), h.queries, id)
	if err != nil {
		if !invoiceError(w, r, err) {
			h.fail(w, r, "invoice", err)
		}
		return
	}
	httputil.WriteJSON(w, http.StatusOK, invoiceJSON(inv))
}

// HandleCreateUsageInvoice answers POST /v1/admin/billing/accounts/{account_id}/invoices
// {"period":"YYYY-MM"}: a DRAFT usage invoice of a finished month (the
// issuer's time zone) of every charge no lot or earlier invoice covered.
// 200 {"invoice": null} when there is nothing to bill.
func (h *AdminHandler) HandleCreateUsageInvoice(w http.ResponseWriter, r *http.Request) {
	reason, ok := h.writeStart(w, r)
	if !ok {
		return
	}
	accountID, err := uuid.Parse(chi.URLParam(r, "account_id"))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("billing.bad_request", "account_id must be a UUID", r))
		return
	}
	var body struct {
		Period string `json:"period"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	ctx := r.Context()
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		h.fail(w, r, "begin", err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	inv, err := invoicing.GenerateUsage(ctx, gen.New(tx), accountID, strings.TrimSpace(body.Period), actorID(r), h.now())
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w, r, "billing.account_not_found", "there is no such billing account")
		return
	}
	if err != nil {
		if !invoiceError(w, r, err) {
			h.fail(w, r, "generate", err)
		}
		return
	}
	if inv == nil {
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"invoice": nil})
		return
	}
	if err := h.auditTx(r, tx, AuditUsageInvoiceCreate, "invoice", inv.ID, reason, map[string]any{
		"account_id": accountID.String(), "period": body.Period, "subtotal_minor": inv.SubtotalMinor, "currency": inv.Currency,
	}); err != nil {
		h.fail(w, r, "audit", err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		h.fail(w, r, "commit", err)
		return
	}
	httputil.WriteJSON(w, http.StatusCreated, map[string]any{"invoice": invoiceJSON(*inv)})
}

// HandleCreateInvoiceIssue answers POST /v1/admin/billing/invoices/{invoice_id}/issue
// {"payment_method": "bank_transfer"|"stripe_card"}: number, payment
// reference, due date and, for a card payment, the payment system fee line.
// The card payment page is made after the commit.
func (h *AdminHandler) HandleCreateInvoiceIssue(w http.ResponseWriter, r *http.Request) {
	reason, ok := h.writeStart(w, r)
	if !ok {
		return
	}
	id, ok := h.invoiceID(w, r)
	if !ok {
		return
	}
	var body struct {
		PaymentMethod string `json:"payment_method"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if body.PaymentMethod == invoicing.MethodStripeCard && h.stripe == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope("billing.card_payment_unavailable",
			"the platform Stripe account is not configured; issue for a bank transfer", r))
		return
	}
	ctx := r.Context()
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		h.fail(w, r, "begin", err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	inv, err := invoicing.Issue(ctx, gen.New(tx), id, body.PaymentMethod, h.now())
	if err != nil {
		if !invoiceError(w, r, err) {
			h.fail(w, r, "issue", err)
		}
		return
	}
	if err := h.auditTx(r, tx, AuditInvoiceIssue, "invoice", inv.ID, reason, map[string]any{
		"number": inv.Number, "payment_method": body.PaymentMethod, "total_minor": inv.TotalAmountMinor,
		"card_fee_minor": inv.CardFeeMinor, "currency": inv.Currency,
	}); err != nil {
		h.fail(w, r, "audit", err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		h.fail(w, r, "commit", err)
		return
	}
	out := invoiceJSON(inv)
	if body.PaymentMethod == invoicing.MethodStripeCard {
		row, err := EnsureCardPayment(ctx, h.pool, h.stripe, h.returnBaseURL, inv.BillingInvoiceRow)
		if err != nil {
			h.logger.ErrorContext(ctx, "admin billing: card payment page", "invoice_id", inv.ID.String(), "error", err)
			out["payment_url_error"] = "the Stripe payment page could not be made; the organizer can open it from the invoice later"
		} else {
			out["payment_url"] = row.HostedPaymentURL
		}
	}
	httputil.WriteJSON(w, http.StatusOK, out)
}

// HandleCreateInvoicePayment answers POST /v1/admin/billing/invoices/{invoice_id}/mark-paid
// {"amount_minor", "paid_at", "reference"}: money that arrived outside the
// webhooks (a transfer the bank feed missed). Same amount rule as a bank
// transfer; a pack's credits are written in the same transaction.
func (h *AdminHandler) HandleCreateInvoicePayment(w http.ResponseWriter, r *http.Request) {
	reason, ok := h.writeStart(w, r)
	if !ok {
		return
	}
	id, ok := h.invoiceID(w, r)
	if !ok {
		return
	}
	var body struct {
		AmountMinor int64  `json:"amount_minor"`
		PaidAt      string `json:"paid_at"`
		Reference   string `json:"reference"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	body.Reference = strings.TrimSpace(body.Reference)
	paidAt, err := time.Parse(time.RFC3339, body.PaidAt)
	switch {
	case err != nil:
		unprocessable(w, r, "billing.invalid_payment", "paid_at must be RFC 3339")
		return
	case paidAt.After(h.now().Add(time.Minute)):
		unprocessable(w, r, "billing.invalid_payment", "paid_at is in the future")
		return
	case body.Reference == "" || len(body.Reference) > 200:
		unprocessable(w, r, "billing.invalid_payment", "reference is required (the bank statement line), at most 200 characters")
		return
	case body.AmountMinor <= 0:
		unprocessable(w, r, "billing.invalid_payment", "amount_minor must be positive")
		return
	}
	ctx := r.Context()
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		h.fail(w, r, "begin", err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := gen.New(tx)
	cur, err := q.GetBillingInvoice(ctx, id)
	if err != nil {
		if !invoiceError(w, r, err) {
			h.fail(w, r, "invoice", err)
		}
		return
	}
	paid, changed, err := invoicing.MarkPaid(ctx, q, id, invoicing.Payment{
		Via: invoicing.PaidViaManual, Reference: "manual:" + body.Reference, AmountMinor: body.AmountMinor,
		Currency: cur.Currency, At: paidAt.UTC(), ActorType: "user", ActorID: actorID(r),
	})
	if err != nil {
		if !invoiceError(w, r, err) {
			h.fail(w, r, "mark paid", err)
		}
		return
	}
	if changed {
		if err := h.auditTx(r, tx, AuditInvoiceMarkPaid, "invoice", id, reason, map[string]any{
			"amount_minor": body.AmountMinor, "currency": cur.Currency, "reference": body.Reference,
		}); err != nil {
			h.fail(w, r, "audit", err)
			return
		}
		if err := tx.Commit(ctx); err != nil {
			h.fail(w, r, "commit", err)
			return
		}
	}
	inv, err := invoicing.Load(ctx, h.queries, paid.ID)
	if err != nil {
		h.fail(w, r, "reload", err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, invoiceJSON(inv))
}

// HandleCreateInvoiceVoid answers POST /v1/admin/billing/invoices/{invoice_id}/void
// {"reason"}: a draft or unpaid invoice is voided and its charges go back to
// the next invoice. Stripe goes FIRST: its invoice is voided or its Checkout
// page closed, and when Stripe says the money already arrived nothing is
// voided here (409, the webhook books the payment).
func (h *AdminHandler) HandleCreateInvoiceVoid(w http.ResponseWriter, r *http.Request) {
	reason, ok := h.writeStart(w, r)
	if !ok {
		return
	}
	id, ok := h.invoiceID(w, r)
	if !ok {
		return
	}
	var body struct {
		Reason string `json:"reason"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	body.Reason = strings.TrimSpace(body.Reason)
	if body.Reason == "" || len(body.Reason) > 500 {
		unprocessable(w, r, "billing.invalid_void", "reason is required, at most 500 characters")
		return
	}
	ctx := r.Context()
	cur, err := h.queries.GetBillingInvoice(ctx, id)
	if err != nil {
		if !invoiceError(w, r, err) {
			h.fail(w, r, "invoice", err)
		}
		return
	}
	if cur.AccountID == nil {
		conflict(w, r, "billing.legacy_invoice", "this is a legacy invoice; void it through /v1/billing/invoices/{id}/void")
		return
	}
	if cur.State == "issued" {
		if err := h.closeStripePayment(ctx, cur); err != nil {
			if errors.Is(err, stripebilling.ErrInvoiceAlreadyPaid) {
				conflict(w, r, "billing.stripe_already_paid",
					"Stripe has already collected this invoice; it will be marked paid by the webhook, correct it with a credit note")
				return
			}
			h.logger.ErrorContext(ctx, "admin billing: void in Stripe", "invoice_id", id.String(), "error", err)
			httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope("billing.stripe_unavailable",
				"nothing was voided: Stripe could not close the payment page, please try again", r))
			return
		}
	}
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		h.fail(w, r, "begin", err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	voided, err := invoicing.Void(ctx, gen.New(tx), id, body.Reason, h.now())
	if err != nil {
		if !invoiceError(w, r, err) {
			h.fail(w, r, "void", err)
		}
		return
	}
	if err := h.auditTx(r, tx, AuditInvoiceVoid, "invoice", id, reason, map[string]any{
		"void_reason": body.Reason, "number": voided.Number,
	}); err != nil {
		h.fail(w, r, "audit", err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		h.fail(w, r, "commit", err)
		return
	}
	inv, err := invoicing.Load(ctx, h.queries, id)
	if err != nil {
		h.fail(w, r, "reload", err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, invoiceJSON(inv))
}

// closeStripePayment voids the Stripe invoice or expires the Checkout page of
// an issued invoice, when it has one.
func (h *AdminHandler) closeStripePayment(ctx context.Context, inv gen.BillingInvoiceRow) error {
	hasStripe := (inv.StripeInvoiceID != nil && *inv.StripeInvoiceID != "") ||
		(inv.StripeCheckoutSessionID != nil && *inv.StripeCheckoutSessionID != "")
	if !hasStripe {
		return nil
	}
	if h.stripe == nil {
		return errors.New("the platform Stripe account is not configured")
	}
	if inv.StripeInvoiceID != nil && *inv.StripeInvoiceID != "" {
		if err := h.stripe.VoidInvoice(ctx, *inv.StripeInvoiceID); err != nil {
			return err
		}
	}
	if inv.StripeCheckoutSessionID != nil && *inv.StripeCheckoutSessionID != "" {
		return h.stripe.ExpireCheckoutSession(ctx, *inv.StripeCheckoutSessionID)
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Payment fees
// ─────────────────────────────────────────────────────────────────────────────

func paymentFeeJSON(f gen.BillingPaymentFeeRow) map[string]any {
	return map[string]any{
		"id": f.ID.String(), "method": f.Method, "currency": f.Currency, "pct_bp": f.PctBp,
		"fixed_minor": f.FixedMinor, "effective_from": f.EffectiveFrom.UTC().Format(time.RFC3339),
		"reason": f.Reason, "created_at": f.CreatedAt.UTC().Format(time.RFC3339),
	}
}

// HandleListPaymentFees answers GET /v1/admin/billing/payment-fees.
func (h *AdminHandler) HandleListPaymentFees(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w, r, false) {
		return
	}
	rows, err := h.queries.ListBillingPaymentFees(r.Context())
	if err != nil {
		h.fail(w, r, "list fees", err)
		return
	}
	items := make([]map[string]any, 0, len(rows))
	for _, f := range rows {
		items = append(items, paymentFeeJSON(f))
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"payment_fees": items})
}

// HandleCreatePaymentFee answers POST /v1/admin/billing/payment-fees: a new
// version of the average card fee a card payer adds to an invoice. It starts
// in the future and after the latest version of its method and currency, so
// no issued invoice changes. There is no default: the operator types it.
func (h *AdminHandler) HandleCreatePaymentFee(w http.ResponseWriter, r *http.Request) {
	reason, ok := h.writeStart(w, r)
	if !ok {
		return
	}
	var body struct {
		Method        string `json:"method"`
		Currency      string `json:"currency"`
		PctBp         *int32 `json:"pct_bp"`
		FixedMinor    *int64 `json:"fixed_minor"`
		EffectiveFrom string `json:"effective_from"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	body.Currency = strings.ToUpper(strings.TrimSpace(body.Currency))
	from, err := time.Parse(time.RFC3339, body.EffectiveFrom)
	now := h.now()
	switch {
	case body.Method != invoicing.MethodStripeCard:
		unprocessable(w, r, "billing.invalid_payment_fee", "method must be stripe_card")
		return
	case len(body.Currency) != 3:
		unprocessable(w, r, "billing.invalid_payment_fee", "currency is a 3-letter code")
		return
	case body.PctBp == nil || *body.PctBp < 0 || *body.PctBp > 2000:
		unprocessable(w, r, "billing.invalid_payment_fee", "pct_bp is required, 0-2000 basis points")
		return
	case body.FixedMinor == nil || *body.FixedMinor < 0 || *body.FixedMinor > 100000:
		unprocessable(w, r, "billing.invalid_payment_fee", "fixed_minor is required, 0-100000")
		return
	case err != nil:
		unprocessable(w, r, "billing.invalid_payment_fee", "effective_from must be RFC 3339")
		return
	case !from.After(now):
		unprocessable(w, r, "billing.effective_from_not_future", "effective_from must be in the future")
		return
	}
	ctx := r.Context()
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		h.fail(w, r, "begin", err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := gen.New(tx)
	latest, err := q.GetBillingPaymentFeeAt(ctx, body.Method, body.Currency, time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC))
	if err == nil && !from.After(latest.EffectiveFrom) {
		unprocessable(w, r, "billing.effective_from_not_after_latest",
			"effective_from must be after the latest version ("+latest.EffectiveFrom.UTC().Format(time.RFC3339)+")")
		return
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		h.fail(w, r, "latest fee", err)
		return
	}
	actor := actorID(r)
	row, err := q.InsertBillingPaymentFee(ctx, gen.InsertBillingPaymentFeeParams{
		Method: body.Method, Currency: body.Currency, PctBp: *body.PctBp, FixedMinor: *body.FixedMinor,
		EffectiveFrom: from.UTC(), CreatedBy: ptrString(actor), Reason: ptrString(reason),
	})
	if err != nil {
		h.fail(w, r, "insert fee", err)
		return
	}
	if err := h.auditTx(r, tx, AuditPaymentFeeCreate, "billing_payment_fee", row.ID, reason, map[string]any{
		"method": row.Method, "currency": row.Currency, "pct_bp": row.PctBp, "fixed_minor": row.FixedMinor,
		"effective_from": row.EffectiveFrom.UTC().Format(time.RFC3339),
	}); err != nil {
		h.fail(w, r, "audit", err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		h.fail(w, r, "commit", err)
		return
	}
	httputil.WriteJSON(w, http.StatusCreated, paymentFeeJSON(row))
}

// ─────────────────────────────────────────────────────────────────────────────
// Bank transactions
// ─────────────────────────────────────────────────────────────────────────────

func bankTxJSON(t gen.BillingBankTransactionRow) map[string]any {
	return map[string]any{
		"id": t.ID.String(), "provider": t.Provider, "external_id": t.ExternalID,
		"booked_at": t.BookedAt.UTC().Format(time.RFC3339), "amount_minor": t.AmountMinor, "currency": t.Currency,
		"reference": t.Reference, "counterparty_name": t.CounterpartyName, "status": t.Status,
		"unmatched_reason": t.UnmatchedReason, "invoice_id": uuidJSON(t.InvoiceID),
		"matched_at": timeJSON(t.MatchedAt), "received_at": t.ReceivedAt.UTC().Format(time.RFC3339),
	}
}

// HandleListBankTransactions answers GET /v1/admin/billing/bank-transactions
// (?status=matched|unmatched|ignored, default all; ?limit= 1-200).
func (h *AdminHandler) HandleListBankTransactions(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w, r, false) {
		return
	}
	status := r.URL.Query().Get("status")
	switch status {
	case "", "matched", "unmatched", "ignored":
	default:
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("billing.bad_request", "status must be matched, unmatched or ignored", r))
		return
	}
	limit, ok := adminLimit(w, r, 100)
	if !ok {
		return
	}
	rows, err := h.queries.ListBankTransactions(r.Context(), status, limit)
	if err != nil {
		h.fail(w, r, "list bank", err)
		return
	}
	items := make([]map[string]any, 0, len(rows))
	for _, t := range rows {
		items = append(items, bankTxJSON(t))
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"transactions": items})
}

// HandleCreateBankImport answers POST /v1/admin/billing/bank-transactions
// {"transactions": [...]}: a statement the operator pastes when a bank has
// no feed. Same rules as the bank webhook (provider "manual"), each transfer
// on its own transaction with its audit row.
func (h *AdminHandler) HandleCreateBankImport(w http.ResponseWriter, r *http.Request) {
	reason, ok := h.writeStart(w, r)
	if !ok {
		return
	}
	var body struct {
		Transactions []BankTransfer `json:"transactions"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	list, msg := parseBankTransfers(body.Transactions)
	if msg != "" {
		unprocessable(w, r, "billing.invalid_bank_import", msg)
		return
	}
	ctx := r.Context()
	res := BankIngestResult{Received: len(list), Items: []map[string]any{}}
	by := "user:" + actorID(r)
	for _, p := range list {
		if p.t.AmountMinor <= 0 {
			res.Skipped++
			continue
		}
		tx, err := h.pool.Begin(ctx)
		if err != nil {
			h.fail(w, r, "begin", err)
			return
		}
		item, err := ingestBankTransferTx(ctx, gen.New(tx), manualBankProvider, p, by, h.now())
		if err == nil && item["status"] != "duplicate" {
			txID, _ := uuid.Parse(item["transaction_id"].(string))
			if err = h.auditTx(r, tx, AuditBankTransferImport, "bank_transaction", txID, reason, map[string]any{
				"external_id": p.t.ID, "amount_minor": p.t.AmountMinor, "currency": p.t.Currency, "status": item["status"],
			}); err == nil {
				err = tx.Commit(ctx)
			}
		}
		_ = tx.Rollback(ctx)
		if err != nil {
			h.fail(w, r, "import", err)
			return
		}
		res.add(item)
	}
	httputil.WriteJSON(w, http.StatusOK, res)
}

func (h *AdminHandler) bankTxID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "transaction_id"))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("billing.bad_request", "transaction_id must be a UUID", r))
		return uuid.Nil, false
	}
	return id, true
}

// HandleCreateBankMatch answers POST /v1/admin/billing/bank-transactions/{transaction_id}/match
// {"invoice_id"}: the operator ties an unmatched payment to an invoice
// (a payer forgot the reference). Same amount rule.
func (h *AdminHandler) HandleCreateBankMatch(w http.ResponseWriter, r *http.Request) {
	reason, ok := h.writeStart(w, r)
	if !ok {
		return
	}
	txID, ok := h.bankTxID(w, r)
	if !ok {
		return
	}
	var body struct {
		InvoiceID string `json:"invoice_id"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	invID, err := uuid.Parse(body.InvoiceID)
	if err != nil {
		unprocessable(w, r, "billing.bad_request", "invoice_id must be a UUID")
		return
	}
	ctx := r.Context()
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		h.fail(w, r, "begin", err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	m, err := invoicing.MatchBankPaymentTo(ctx, gen.New(tx), txID, invID, "user:"+actorID(r), h.now())
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w, r, "billing.not_found", "there is no such payment or invoice")
		return
	}
	if err != nil {
		if !invoiceError(w, r, err) {
			h.fail(w, r, "match", err)
		}
		return
	}
	if err := h.auditTx(r, tx, AuditBankTransferMatch, "bank_transaction", txID, reason, map[string]any{
		"invoice_id": invID.String(), "amount_minor": m.Transaction.AmountMinor, "currency": m.Transaction.Currency,
	}); err != nil {
		h.fail(w, r, "audit", err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		h.fail(w, r, "commit", err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, bankTxJSON(m.Transaction))
}

// HandleCreateBankIgnore answers POST /v1/admin/billing/bank-transactions/{transaction_id}/ignore
// {"reason"}: a payment that is not for an invoice (a refund from a
// supplier, a test transfer).
func (h *AdminHandler) HandleCreateBankIgnore(w http.ResponseWriter, r *http.Request) {
	reason, ok := h.writeStart(w, r)
	if !ok {
		return
	}
	txID, ok := h.bankTxID(w, r)
	if !ok {
		return
	}
	var body struct {
		Reason string `json:"reason"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	body.Reason = strings.TrimSpace(body.Reason)
	if body.Reason == "" || len(body.Reason) > maxAdminBankImportReason {
		unprocessable(w, r, "billing.bad_request", "reason is required, at most 500 characters")
		return
	}
	ctx := r.Context()
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		h.fail(w, r, "begin", err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := gen.New(tx)
	cur, err := q.LockBankTransaction(ctx, txID)
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w, r, "billing.not_found", "there is no such payment")
		return
	}
	if err != nil {
		h.fail(w, r, "lock", err)
		return
	}
	if cur.Status == "matched" {
		conflict(w, r, "billing.bank_transaction_matched", "a matched payment paid an invoice and cannot be ignored")
		return
	}
	row, err := q.SetBankTransactionStatus(ctx, txID, "ignored", &body.Reason)
	if err != nil {
		h.fail(w, r, "ignore", err)
		return
	}
	if err := h.auditTx(r, tx, AuditBankTransferIgnore, "bank_transaction", txID, reason, map[string]any{
		"ignore_reason": body.Reason,
	}); err != nil {
		h.fail(w, r, "audit", err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		h.fail(w, r, "commit", err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, bankTxJSON(row))
}
