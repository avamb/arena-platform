// account_billing.go — the organizer's billing account: balance and lots,
// credit packs and their purchase, invoices and card payment, the credit
// journal and the payer details (billing tasks B3-3, B3-4, B4; 41 section
// 6.9). Every route carries {org_id}; the server checks membership and the
// route's permission in the PATH organization before the handler runs, and
// every row is loaded inside that organization (another organization's id is
// the route's own 404).
//
// Money is minor units next to its currency. Buying is billing.purchase,
// which organization API keys can never hold (apikeys.ForbiddenScopeExact):
// a pack is bought by a person.
package hbilling

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/stripebilling"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing/invoicing"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// PlatformStripe is what billing asks of the PLATFORM's own Stripe account
// (never an organizer's): customers, a Checkout page for a pack, a hosted
// invoice for a usage invoice, voiding, and the webhook.
type PlatformStripe interface {
	CreateOrUpdateCustomer(ctx context.Context, email, name, idempotencyKey string) (string, error)
	CreateCheckoutSession(ctx context.Context, in stripebilling.CheckoutInput) (string, string, error)
	CreateHostedInvoice(ctx context.Context, in stripebilling.HostedInvoiceInput) (string, string, error)
	VoidInvoice(ctx context.Context, stripeInvoiceID string) error
	ExpireCheckoutSession(ctx context.Context, sessionID string) error
	ParseBillingEvent(body []byte, sigHeader string) (*stripebilling.BillingEvent, error)
}

var _ PlatformStripe = (*stripebilling.Adapter)(nil)

// AccountBillingHandler serves the organizer's billing account routes, the
// platform Stripe webhook and the bank webhook.
type AccountBillingHandler struct {
	pool          *pgxpool.Pool
	stripe        PlatformStripe
	returnBaseURL string
	bankSecret    string
	logger        *slog.Logger
	now           func() time.Time
}

// NewAccountBilling builds the handler. stripe nil switches card payment off
// (503); returnBaseURL is the web event center's address the Checkout page
// sends the buyer back to.
func NewAccountBilling(pool *pgxpool.Pool, stripe PlatformStripe, returnBaseURL, bankSecret string, logger *slog.Logger) *AccountBillingHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &AccountBillingHandler{pool: pool, stripe: stripe, returnBaseURL: strings.TrimRight(returnBaseURL, "/"),
		bankSecret: bankSecret, logger: logger, now: func() time.Time { return time.Now().UTC() }}
}

// WithClock replaces the clock (tests).
func (h *AccountBillingHandler) WithClock(now func() time.Time) *AccountBillingHandler {
	h.now = now
	return h
}

func (h *AccountBillingHandler) ready(w http.ResponseWriter, r *http.Request) bool {
	if h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.billing_unavailable", "billing is not available right now, please try again in a moment", r))
		return false
	}
	return true
}

func (h *AccountBillingHandler) fail(w http.ResponseWriter, r *http.Request, what string, err error) {
	h.logger.ErrorContext(r.Context(), "account billing: "+what, "error", err)
	httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
		"billing.internal", "the operation failed, nothing was charged; please try again", r))
}

func pathUUID(w http.ResponseWriter, r *http.Request, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("billing.bad_request", name+" must be a UUID", r))
		return uuid.Nil, false
	}
	return id, true
}

// ─────────────────────────────────────────────────────────────────────────────
// Balance, packs, ledger
// ─────────────────────────────────────────────────────────────────────────────

type lotJSON struct {
	ID                   string  `json:"id"`
	Kind                 string  `json:"kind"`
	Source               string  `json:"source"`
	Currency             string  `json:"currency"`
	Tier                 *int16  `json:"tier"`
	UnseatedRateMinor    *int64  `json:"unseated_rate_minor"`
	SeatedSurchargeMinor *int64  `json:"seated_surcharge_minor"`
	FaceMinor            int64   `json:"face_minor"`
	RemainingMinor       int64   `json:"remaining_minor"`
	PurchasedAt          string  `json:"purchased_at"`
	ExpiresAt            *string `json:"expires_at"`
}

func toLotJSON(l gen.CreditLotRow) lotJSON {
	out := lotJSON{ID: l.ID.String(), Kind: l.Kind, Source: l.Source, Currency: l.Currency, Tier: l.Tier,
		UnseatedRateMinor: l.UnseatedRateMinor, SeatedSurchargeMinor: l.SeatedSurchargeMinor,
		FaceMinor: l.FaceMinor, RemainingMinor: l.RemainingMinor, PurchasedAt: l.PurchasedAt.UTC().Format(time.RFC3339)}
	if l.ExpiresAt != nil {
		s := l.ExpiresAt.UTC().Format(time.RFC3339)
		out.ExpiresAt = &s
	}
	return out
}

// HandleBalance answers GET /v1/organizations/{org_id}/billing/balance: per
// billing account (one per currency) what can be spent now, of it how much is
// bonus and when the nearest bonus expires, the per-ticket rate of the lot
// that pays next, and the lots that still hold something.
func (h *AccountBillingHandler) HandleBalance(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w, r) {
		return
	}
	orgID, ok := pathUUID(w, r, "org_id")
	if !ok {
		return
	}
	ctx, q, now := r.Context(), gen.New(h.pool), h.now()
	accounts, err := q.ListBillingAccountsByOrg(ctx, orgID)
	if err != nil {
		h.fail(w, r, "accounts", err)
		return
	}
	items := make([]map[string]any, 0, len(accounts))
	for _, a := range accounts {
		bal, err := q.GetCreditBalance(ctx, a.ID, now)
		if err != nil {
			h.fail(w, r, "balance", err)
			return
		}
		all, err := q.ListCreditLotsByAccount(ctx, a.ID)
		if err != nil {
			h.fail(w, r, "lots", err)
			return
		}
		lots := []lotJSON{}
		var next *lotJSON
		for _, l := range all {
			if l.RemainingMinor <= 0 || (l.ExpiresAt != nil && !l.ExpiresAt.After(now)) {
				continue
			}
			j := toLotJSON(l)
			lots = append(lots, j)
			if l.Kind == "purchased" && (next == nil || l.PurchasedAt.Format(time.RFC3339Nano) < next.PurchasedAt) {
				jj := j
				next = &jj
			}
		}
		var rate any
		if next != nil {
			rate = map[string]any{"tier": next.Tier, "unseated_rate_minor": next.UnseatedRateMinor,
				"seated_surcharge_minor": next.SeatedSurchargeMinor}
		}
		items = append(items, map[string]any{
			"account_id": a.ID.String(), "currency": a.Currency,
			"balance_minor": bal.PurchasedMinor + bal.BonusMinor, "purchased_minor": bal.PurchasedMinor,
			"bonus_minor": bal.BonusMinor, "next_bonus_expires_at": timeJSON(bal.NextBonusExpiresAt),
			"current_rate": rate, "lots": lots,
		})
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"accounts": items})
}

// HandlePacks answers GET /v1/organizations/{org_id}/billing/packs: the
// active packs of the price list in force for each account now, with the
// card fee a card payment would add (null when card payment is not set up).
func (h *AccountBillingHandler) HandlePacks(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w, r) {
		return
	}
	orgID, ok := pathUUID(w, r, "org_id")
	if !ok {
		return
	}
	ctx, q, now := r.Context(), gen.New(h.pool), h.now()
	accounts, err := q.ListBillingAccountsByOrg(ctx, orgID)
	if err != nil {
		h.fail(w, r, "accounts", err)
		return
	}
	out := []map[string]any{}
	for _, a := range accounts {
		terms, err := q.GetBillingAccountTermsAt(ctx, a.ID, now)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			h.fail(w, r, "terms", err)
			return
		}
		pl, err := q.GetPriceListVersionAt(ctx, terms.PriceListCode, a.Currency, now)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			h.fail(w, r, "price list", err)
			return
		}
		packs, err := q.ListPriceListPacks(ctx, pl.ID)
		if err != nil {
			h.fail(w, r, "packs", err)
			return
		}
		fee, feeErr := q.GetBillingPaymentFeeAt(ctx, invoicing.MethodStripeCard, a.Currency, now)
		cardOK := feeErr == nil && h.stripe != nil
		if feeErr != nil && !errors.Is(feeErr, pgx.ErrNoRows) {
			h.fail(w, r, "card fee", feeErr)
			return
		}
		for _, p := range packs {
			if !p.Active {
				continue
			}
			var cardFee any
			if cardOK {
				if f, err := invoicing.CardFee(p.FaceMinor, fee.PctBp, fee.FixedMinor); err == nil {
					cardFee = f
				}
			}
			out = append(out, map[string]any{
				"id": p.ID.String(), "account_id": a.ID.String(), "currency": a.Currency, "tier": p.Tier,
				"face_minor": p.FaceMinor, "unseated_rate_minor": p.UnseatedRateMinor,
				"seated_surcharge_minor": p.SeatedSurchargeMinor,
				"tickets_unseated":       ticketsFor(p.FaceMinor, p.UnseatedRateMinor),
				"card_fee_minor":         cardFee, "card_payment_available": cardOK,
			})
		}
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"packs": out})
}

func ticketsFor(face, rate int64) int64 {
	if rate <= 0 {
		return 0
	}
	return face / rate
}

// HandleLedger answers GET /v1/organizations/{org_id}/billing/ledger: the
// credit journal of the organization's accounts, newest first, keyset paged
// per account (?account_id=, ?before=<seq>, ?limit= 1-200).
func (h *AccountBillingHandler) HandleLedger(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w, r) {
		return
	}
	orgID, ok := pathUUID(w, r, "org_id")
	if !ok {
		return
	}
	ctx, q := r.Context(), gen.New(h.pool)
	accounts, err := q.ListBillingAccountsByOrg(ctx, orgID)
	if err != nil {
		h.fail(w, r, "accounts", err)
		return
	}
	limit := int32(50)
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.ParseInt(v, 10, 32)
		if err != nil || n < 1 || n > 200 {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("billing.bad_request", "limit must be 1-200", r))
			return
		}
		limit = int32(n)
	}
	var before *int64
	if v := r.URL.Query().Get("before"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 1 {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("billing.bad_request", "before must be a positive integer", r))
			return
		}
		before = &n
	}
	var account *gen.BillingAccountRow
	want := r.URL.Query().Get("account_id")
	for i := range accounts {
		if want == "" || accounts[i].ID.String() == want {
			account = &accounts[i]
			break
		}
	}
	if account == nil {
		if want != "" {
			notFound(w, r, "billing.account_not_found", "this organization has no such billing account")
			return
		}
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"entries": []any{}, "next_before": nil})
		return
	}
	rows, err := q.ListCreditLedgerByAccount(ctx, account.ID, before, limit)
	if err != nil {
		h.fail(w, r, "ledger", err)
		return
	}
	entries := make([]map[string]any, 0, len(rows))
	for _, e := range rows {
		entries = append(entries, map[string]any{
			"seq": e.Seq, "lot_id": e.LotID.String(), "entry_type": e.EntryType, "amount_minor": e.AmountMinor,
			"currency": e.Currency, "remaining_after": e.RemainingAfter, "charge_id": uuidJSON(e.ChargeID),
			"invoice_id": uuidJSON(e.InvoiceID), "reason": e.Reason, "created_at": e.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	var next any
	if len(rows) == int(limit) {
		next = rows[len(rows)-1].Seq
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"account_id": account.ID.String(), "currency": account.Currency,
		"entries": entries, "next_before": next})
}

func uuidJSON(id *uuid.UUID) any {
	if id == nil {
		return nil
	}
	return id.String()
}

// ─────────────────────────────────────────────────────────────────────────────
// Purchase and invoices
// ─────────────────────────────────────────────────────────────────────────────

// HandleOrderPack answers POST /v1/organizations/{org_id}/billing/packs/{pack_id}/order
// (billing.purchase). Body {"payment_method": "bank_transfer"|"stripe_card"};
// the Idempotency-Key header is required and a repeat answers the first
// invoice. It writes an ISSUED credit_pack invoice: a bank transfer gets the
// payment reference to write in the transfer, a card payment a Stripe
// Checkout page (with the payment system fee line). The credits appear when
// the payment is recorded.
func (h *AccountBillingHandler) HandleOrderPack(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w, r) {
		return
	}
	orgID, ok := pathUUID(w, r, "org_id")
	if !ok {
		return
	}
	packID, ok := pathUUID(w, r, "pack_id")
	if !ok {
		return
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" || len(key) > 200 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("billing.idempotency_key_required",
			"send an Idempotency-Key header of 1-200 characters, so a repeated click buys one pack", r))
		return
	}
	var body struct {
		PaymentMethod string `json:"payment_method"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if body.PaymentMethod != invoicing.MethodBankTransfer && body.PaymentMethod != invoicing.MethodStripeCard {
		unprocessable(w, r, "billing.invalid_payment_method", "payment_method must be bank_transfer or stripe_card")
		return
	}
	if body.PaymentMethod == invoicing.MethodStripeCard && (h.stripe == nil || h.returnBaseURL == "") {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope("billing.card_payment_unavailable",
			"card payment is not available right now; choose a bank transfer or try later", r))
		return
	}
	ctx := r.Context()
	pack, err := gen.New(h.pool).GetPriceListPackByID(ctx, packID)
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w, r, "billing.pack_not_found", "this pack does not exist")
		return
	}
	if err != nil {
		h.fail(w, r, "pack", err)
		return
	}
	acc, err := gen.New(h.pool).GetBillingAccount(ctx, orgID, pack.Currency, "organizer")
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w, r, "billing.pack_not_found", "this pack is not offered to this organization")
		return
	}
	if err != nil {
		h.fail(w, r, "account", err)
		return
	}
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		h.fail(w, r, "begin", err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	inv, created, err := invoicing.OrderPack(ctx, gen.New(tx), invoicing.PackOrder{
		OrgID: orgID, AccountID: acc.ID, PackID: packID, Method: body.PaymentMethod, IdempotencyKey: key,
		Actor: actorID(r), Now: h.now(),
	})
	switch {
	case errors.Is(err, invoicing.ErrPackUnavailable):
		notFound(w, r, "billing.pack_not_found", "this pack is not on sale for this organization now")
		return
	case errors.Is(err, invoicing.ErrCardFeeNotConfigured):
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope("billing.card_payment_unavailable",
			"card payment is not set up for "+acc.Currency+" yet; choose a bank transfer", r))
		return
	case errors.Is(err, invoicing.ErrBadInput):
		conflict(w, r, "billing.idempotency_key_reused", "this Idempotency-Key was used for another order")
		return
	case err != nil:
		h.fail(w, r, "order", err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		h.fail(w, r, "commit", err)
		return
	}
	status := http.StatusCreated
	if !created {
		status = http.StatusOK
	}
	out := invoiceJSON(inv)
	if body.PaymentMethod == invoicing.MethodStripeCard {
		row, err := h.ensureCardPayment(ctx, inv.BillingInvoiceRow)
		if err != nil {
			h.logger.ErrorContext(ctx, "account billing: card payment page", "invoice_id", inv.ID.String(), "error", err)
			out["payment_url_error"] = "the card payment page could not be opened; try again from the invoice"
		} else {
			out["payment_url"] = row.HostedPaymentURL
		}
	}
	httputil.WriteJSON(w, status, out)
}

// HandleListInvoices answers GET /v1/organizations/{org_id}/billing/account-invoices
// (?state=, ?limit= 1-100, ?offset=).
func (h *AccountBillingHandler) HandleListInvoices(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w, r) {
		return
	}
	orgID, ok := pathUUID(w, r, "org_id")
	if !ok {
		return
	}
	state := r.URL.Query().Get("state")
	switch state {
	case "", "issued", "paid", "void":
	default:
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("billing.bad_request", "state must be issued, paid or void", r))
		return
	}
	limit, offset := int32(50), int32(0)
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.ParseInt(v, 10, 32)
		if err != nil || n < 1 || n > 100 {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("billing.bad_request", "limit must be 1-100", r))
			return
		}
		limit = int32(n)
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		n, err := strconv.ParseInt(v, 10, 32)
		if err != nil || n < 0 {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("billing.bad_request", "offset must be >= 0", r))
			return
		}
		offset = int32(n)
	}
	rows, err := gen.New(h.pool).ListBillingInvoicesByOrg(r.Context(), orgID, state, limit+1, offset)
	if err != nil {
		h.fail(w, r, "list invoices", err)
		return
	}
	more := len(rows) > int(limit)
	if more {
		rows = rows[:limit]
	}
	items := make([]map[string]any, 0, len(rows))
	for _, inv := range rows {
		if inv.State == "draft" {
			continue // a draft is the operator's, not yet the organizer's
		}
		items = append(items, invoiceJSON(invoicing.Invoice{BillingInvoiceRow: inv}))
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"invoices": items, "has_more": more})
}

// loadOwnInvoice loads an invoice of the path organization that is not a
// draft; anything else is the route's 404.
func (h *AccountBillingHandler) loadOwnInvoice(w http.ResponseWriter, r *http.Request) (invoicing.Invoice, bool) {
	orgID, ok := pathUUID(w, r, "org_id")
	if !ok {
		return invoicing.Invoice{}, false
	}
	id, ok := pathUUID(w, r, "invoice_id")
	if !ok {
		return invoicing.Invoice{}, false
	}
	inv, err := invoicing.Load(r.Context(), gen.New(h.pool), id)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (inv.OrgID != orgID || inv.AccountID == nil || inv.State == "draft")) {
		notFound(w, r, "billing.invoice_not_found", "this organization has no such invoice")
		return invoicing.Invoice{}, false
	}
	if err != nil {
		h.fail(w, r, "invoice", err)
		return invoicing.Invoice{}, false
	}
	return inv, true
}

// HandleGetInvoice answers GET /v1/organizations/{org_id}/billing/account-invoices/{invoice_id}.
func (h *AccountBillingHandler) HandleGetInvoice(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w, r) {
		return
	}
	inv, ok := h.loadOwnInvoice(w, r)
	if !ok {
		return
	}
	httputil.WriteJSON(w, http.StatusOK, invoiceJSON(inv))
}

// HandlePayByCard answers POST /v1/organizations/{org_id}/billing/account-invoices/{invoice_id}/pay-by-card
// (billing.purchase): the Stripe payment page of an issued invoice that is
// paid by card, made on first use (Checkout for a pack, a hosted Stripe
// invoice for a usage invoice).
func (h *AccountBillingHandler) HandlePayByCard(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w, r) {
		return
	}
	inv, ok := h.loadOwnInvoice(w, r)
	if !ok {
		return
	}
	if inv.State != "issued" || inv.PaymentMethod == nil || *inv.PaymentMethod != invoicing.MethodStripeCard {
		conflict(w, r, "billing.invoice_not_card_payable", "this invoice is not open for a card payment")
		return
	}
	if h.stripe == nil || h.returnBaseURL == "" {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope("billing.card_payment_unavailable",
			"card payment is not available right now; please try later", r))
		return
	}
	row, err := h.ensureCardPayment(r.Context(), inv.BillingInvoiceRow)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "account billing: card payment page", "invoice_id", inv.ID.String(), "error", err)
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope("billing.card_payment_unavailable",
			"the card payment page could not be opened; please try again in a moment", r))
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"invoice_id": row.ID.String(), "payment_url": row.HostedPaymentURL})
}

// ensureCardPayment returns the invoice with its Stripe payment page, making
// the page once: the Stripe customer of the account, then a Checkout Session
// (credit pack) or a finalized hosted Stripe invoice (usage). Every Stripe
// call carries an idempotency key derived from the invoice, so a retry after
// a lost answer gets the same page.
func (h *AccountBillingHandler) ensureCardPayment(ctx context.Context, inv gen.BillingInvoiceRow) (gen.BillingInvoiceRow, error) {
	return EnsureCardPayment(ctx, h.pool, h.stripe, h.returnBaseURL, inv)
}

// EnsureCardPayment is ensureCardPayment for the operator's routes too.
func EnsureCardPayment(ctx context.Context, pool *pgxpool.Pool, stripe PlatformStripe, returnBase string, inv gen.BillingInvoiceRow) (gen.BillingInvoiceRow, error) {
	if inv.HostedPaymentURL != nil && *inv.HostedPaymentURL != "" {
		return inv, nil
	}
	if stripe == nil || inv.AccountID == nil || inv.Number == nil {
		return inv, errors.New("card payment is not set up")
	}
	q := gen.New(pool)
	customer, err := q.GetStripeCustomerIDByAccount(ctx, *inv.AccountID)
	if errors.Is(err, pgx.ErrNoRows) {
		customer, err = stripe.CreateOrUpdateCustomer(ctx, "", "", "cust-acc-"+inv.AccountID.String())
		if err != nil {
			return inv, fmt.Errorf("stripe customer: %w", err)
		}
		if err := q.InsertStripeCustomerForAccount(ctx, inv.OrgID, *inv.AccountID, customer); err != nil {
			return inv, err
		}
		if customer, err = q.GetStripeCustomerIDByAccount(ctx, *inv.AccountID); err != nil {
			return inv, err
		}
	} else if err != nil {
		return inv, err
	}
	lines, err := q.ListBillingInvoiceLines(ctx, inv.ID)
	if err != nil {
		return inv, err
	}
	items := make([]stripebilling.LineItem, 0, len(lines))
	for _, l := range lines {
		if l.TotalAmountMinor > 0 {
			items = append(items, stripebilling.LineItem{Name: l.Description, AmountMinor: l.TotalAmountMinor, Currency: l.Currency})
		}
	}
	if inv.Kind == invoicing.KindCreditPack {
		if returnBase == "" {
			return inv, errors.New("no return address for the payment page")
		}
		back := returnBase + "/o/" + inv.OrgID.String() + "/billing/balance?invoice=" + inv.ID.String()
		id, url, err := stripe.CreateCheckoutSession(ctx, stripebilling.CheckoutInput{
			Currency: inv.Currency, Lines: items, InvoiceID: inv.ID.String(), InvoiceNumber: *inv.Number,
			StripeCustomerID: customer, SuccessURL: back + "&paid=1", CancelURL: back + "&cancelled=1",
			IdempotencyKey: "checkout-" + inv.ID.String(),
		})
		if err != nil {
			return inv, err
		}
		return q.SetBillingInvoiceCheckout(ctx, inv.ID, id, url)
	}
	days := 14
	if inv.DueAt != nil && inv.IssuedAt != nil {
		if d := int(inv.DueAt.Sub(*inv.IssuedAt).Hours() / 24); d > 0 {
			days = d
		}
	}
	id, url, err := stripe.CreateHostedInvoice(ctx, stripebilling.HostedInvoiceInput{
		StripeCustomerID: customer, Currency: inv.Currency, Lines: items, InvoiceID: inv.ID.String(),
		InvoiceNumber: *inv.Number, DaysUntilDue: days, IdempotencyKey: "inv-" + inv.ID.String(),
	})
	if err != nil {
		return inv, err
	}
	return q.SetBillingInvoiceStripeInvoice(ctx, inv.ID, id, &url)
}

// HandlePutPayer answers PUT /v1/organizations/{org_id}/billing/payer
// (billing.payer_manage): the details printed on the organization's
// invoices. They are stored on every billing account of the organization.
func (h *AccountBillingHandler) HandlePutPayer(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w, r) {
		return
	}
	orgID, ok := pathUUID(w, r, "org_id")
	if !ok {
		return
	}
	var body struct {
		LegalName string `json:"legal_name"`
		Address   string `json:"address"`
		Country   string `json:"country"`
		TaxID     string `json:"tax_id"`
		Email     string `json:"email"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	body.LegalName, body.Address = strings.TrimSpace(body.LegalName), strings.TrimSpace(body.Address)
	body.Country, body.TaxID = strings.ToUpper(strings.TrimSpace(body.Country)), strings.TrimSpace(body.TaxID)
	body.Email = strings.TrimSpace(body.Email)
	switch {
	case body.LegalName == "" || len(body.LegalName) > 200:
		unprocessable(w, r, "billing.invalid_payer", "legal_name is required, at most 200 characters")
		return
	case len(body.Address) > 500, len(body.TaxID) > 50:
		unprocessable(w, r, "billing.invalid_payer", "address is at most 500 characters, tax_id at most 50")
		return
	case body.Country != "" && (len(body.Country) != 2 || strings.Trim(body.Country, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") != ""):
		unprocessable(w, r, "billing.invalid_payer", "country is a two-letter code")
		return
	case body.Email != "" && (len(body.Email) > 254 || !strings.Contains(body.Email, "@")):
		unprocessable(w, r, "billing.invalid_payer", "email is not an address")
		return
	}
	raw, err := json.Marshal(body)
	if err != nil {
		h.fail(w, r, "payer", err)
		return
	}
	n, err := gen.New(h.pool).UpdateBillingAccountsPayer(r.Context(), orgID, raw)
	if err != nil {
		h.fail(w, r, "payer", err)
		return
	}
	if n == 0 {
		notFound(w, r, "billing.account_not_found", "this organization has no billing account yet")
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"payer": body, "accounts_updated": n})
}

// invoiceJSON renders an invoice for the organizer and the operator.
func invoiceJSON(inv invoicing.Invoice) map[string]any {
	lines := make([]map[string]any, 0, len(inv.Lines))
	for _, l := range inv.Lines {
		lines = append(lines, map[string]any{
			"id": l.ID.String(), "kind": l.Kind, "rule_type": l.RuleType, "description": l.Description,
			"quantity": l.Quantity, "unit_amount_minor": l.UnitAmountMinor, "total_amount_minor": l.TotalAmountMinor,
			"currency": l.Currency,
		})
	}
	return map[string]any{
		"id": inv.ID.String(), "org_id": inv.OrgID.String(), "account_id": uuidJSON(inv.AccountID), "kind": inv.Kind,
		"state": inv.State, "number": inv.Number, "billing_period": inv.BillingPeriod,
		"payment_method": inv.PaymentMethod, "payment_reference": inv.PaymentReference, "currency": inv.Currency,
		"subtotal_minor": inv.SubtotalMinor, "card_fee_minor": inv.CardFeeMinor, "tax_minor": inv.TaxMinor,
		"total_minor": inv.TotalAmountMinor, "period_from": timeJSON(inv.PeriodFrom), "period_to": timeJSON(inv.PeriodTo),
		"issued_at": timeJSON(inv.IssuedAt), "due_at": timeJSON(inv.DueAt), "paid_at": timeJSON(inv.PaidAt),
		"paid_amount_minor": inv.PaidAmountMinor, "paid_via": inv.PaidVia, "voided_at": timeJSON(inv.VoidedAt),
		"payment_url": inv.HostedPaymentURL, "pack_id": uuidJSON(inv.PackID), "lines": lines,
	}
}
