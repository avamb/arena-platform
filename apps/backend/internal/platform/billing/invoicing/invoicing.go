package invoicing

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing/credits"
)

// Invoice kinds and line kinds.
const (
	KindUsage      = "usage"
	KindCreditPack = "credit_pack"

	LineUsage   = "usage"
	LinePack    = "pack"
	LineCardFee = "card_fee"
)

// defaultSeries names an issuer's invoice series until it has its own.
const defaultSeries = "ARENA"

// Invoice is an invoice with its lines.
type Invoice struct {
	gen.BillingInvoiceRow
	Lines []gen.BillingInvoiceLineRow `json:"lines"`
}

// Load reads an invoice and its lines.
func Load(ctx context.Context, q *gen.Queries, id uuid.UUID) (Invoice, error) {
	inv, err := q.GetBillingInvoice(ctx, id)
	if err != nil {
		return Invoice{}, err
	}
	lines, err := q.ListBillingInvoiceLines(ctx, id)
	if err != nil {
		return Invoice{}, err
	}
	return Invoice{BillingInvoiceRow: inv, Lines: lines}, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Credit pack order
// ─────────────────────────────────────────────────────────────────────────────

// PackOrder is an organizer's order of a credit pack.
type PackOrder struct {
	OrgID     uuid.UUID
	AccountID uuid.UUID
	PackID    uuid.UUID
	Method    string
	// IdempotencyKey is the caller's key; a repeat returns the first invoice.
	IdempotencyKey string
	Actor          string
	Now            time.Time
}

// OrderPack writes an ISSUED credit_pack invoice for a pack of the price list
// in force for the account now: one pack line at face, plus the card fee line
// for a card payment. The lot is written only when the invoice is paid
// (MarkPaid). A repeat with the same key answers the first invoice and
// created=false.
func OrderPack(ctx context.Context, q *gen.Queries, o PackOrder) (inv Invoice, created bool, err error) {
	if o.Method != MethodBankTransfer && o.Method != MethodStripeCard {
		return Invoice{}, false, fmt.Errorf("%w: payment method %q", ErrBadInput, o.Method)
	}
	if strings.TrimSpace(o.IdempotencyKey) == "" || len(o.IdempotencyKey) > 200 {
		return Invoice{}, false, fmt.Errorf("%w: an idempotency key of 1-200 characters is required", ErrBadInput)
	}
	key := "pack:" + o.AccountID.String() + ":" + o.IdempotencyKey
	if prev, err := q.GetBillingInvoiceByIdempotencyKey(ctx, key); err == nil {
		if prev.PackID == nil || *prev.PackID != o.PackID || prev.PaymentMethod == nil || *prev.PaymentMethod != o.Method {
			return Invoice{}, false, fmt.Errorf("%w: the idempotency key was used for another order", ErrBadInput)
		}
		inv, err := Load(ctx, q, prev.ID)
		return inv, false, err
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return Invoice{}, false, err
	}

	acc, err := q.GetBillingAccountByID(ctx, o.AccountID)
	if err != nil {
		return Invoice{}, false, err
	}
	if acc.OrgID != o.OrgID {
		return Invoice{}, false, fmt.Errorf("%w: the account is another organization's", ErrBadInput)
	}
	pack, err := q.GetPriceListPackByID(ctx, o.PackID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Invoice{}, false, ErrPackUnavailable
	}
	if err != nil {
		return Invoice{}, false, err
	}
	if err := packOnSale(ctx, q, acc, pack, o.Now); err != nil {
		return Invoice{}, false, err
	}
	if err := q.LockBillingAccount(ctx, acc.ID); err != nil {
		return Invoice{}, false, err
	}
	method := o.Method
	actor := o.Actor
	row, inserted, err := q.InsertBillingInvoice(ctx, gen.InsertBillingInvoiceParams{
		OrgID: acc.OrgID, AccountID: acc.ID, IssuerID: acc.IssuerID, Kind: KindCreditPack,
		BillingPeriod: periodOf(o.Now), IdempotencyKey: &key, PaymentMethod: &method, PackID: &pack.ID,
		Currency: acc.Currency, TaxStatus: acc.TaxStatus, CreatedBy: &actor,
	})
	if err != nil {
		return Invoice{}, false, err
	}
	if !inserted {
		return Invoice{}, false, fmt.Errorf("%w: a concurrent order used the same key", ErrBadInput)
	}
	if _, err := q.InsertBillingInvoiceLine(ctx, gen.InsertBillingInvoiceLineParams{
		InvoiceID: row.ID, Kind: LinePack, Description: fmt.Sprintf("Credit pack, tier %d", pack.Tier),
		Quantity: 1, UnitAmountMinor: pack.FaceMinor, TotalAmountMinor: pack.FaceMinor, Currency: acc.Currency,
	}); err != nil {
		return Invoice{}, false, err
	}
	if _, err := q.SetBillingInvoiceTotals(ctx, row.ID, pack.FaceMinor, 0, 0); err != nil {
		return Invoice{}, false, err
	}
	issued, err := Issue(ctx, q, row.ID, o.Method, o.Now)
	if err != nil {
		return Invoice{}, false, err
	}
	return issued, true, nil
}

// packOnSale checks that a pack is active and belongs to the version of the
// account's price list in force now, in the account's currency.
func packOnSale(ctx context.Context, q *gen.Queries, acc gen.BillingAccountRow, pack gen.PriceListPackRow, now time.Time) error {
	if !pack.Active || pack.Currency != acc.Currency {
		return ErrPackUnavailable
	}
	terms, err := q.GetBillingAccountTermsAt(ctx, acc.ID, now)
	if err != nil {
		return fmt.Errorf("%w: the account has no terms now", ErrPackUnavailable)
	}
	pl, err := q.GetPriceListVersionAt(ctx, terms.PriceListCode, acc.Currency, now)
	if err != nil || pl.ID != pack.PriceListID {
		return ErrPackUnavailable
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Usage invoice
// ─────────────────────────────────────────────────────────────────────────────

// GenerateUsage writes a DRAFT usage invoice of an account for one calendar
// month (YYYY-MM, in the ISSUER's time zone, 41 section 6.2): every direct
// charge of an event in that month that was not reversed, with what lots and
// discounts did not cover. Each charge is tied to its line by an invoice_line
// allocation, so it is never billed twice and a later cancellation of its
// session turns it into bonus credits (B-6). Lines are per rule type. It
// answers nil when there is nothing to bill. The month must be over.
func GenerateUsage(ctx context.Context, q *gen.Queries, accountID uuid.UUID, period, actor string, now time.Time) (*Invoice, error) {
	acc, err := q.GetBillingAccountByID(ctx, accountID)
	if err != nil {
		return nil, err
	}
	issuer, err := q.GetBillingIssuer(ctx, acc.IssuerID)
	if err != nil {
		return nil, err
	}
	from, to, err := monthBounds(period, issuer.Timezone)
	if err != nil {
		return nil, err
	}
	if to.After(now) {
		return nil, ErrPeriodNotClosed
	}
	if err := q.LockBillingAccount(ctx, acc.ID); err != nil {
		return nil, err
	}
	charges, err := q.ListUninvoicedCharges(ctx, acc.ID, from, to)
	if err != nil {
		return nil, err
	}
	if len(charges) == 0 {
		return nil, nil
	}
	n, err := q.CountBillingInvoicesForPeriod(ctx, acc.ID, KindUsage, period)
	if err != nil {
		return nil, err
	}
	key := fmt.Sprintf("usage:%s:%s:%d", acc.ID, period, n+1)
	row, inserted, err := q.InsertBillingInvoice(ctx, gen.InsertBillingInvoiceParams{
		OrgID: acc.OrgID, AccountID: acc.ID, IssuerID: acc.IssuerID, Kind: KindUsage, BillingPeriod: period,
		IdempotencyKey: &key, PeriodFrom: &from, PeriodTo: &to, Currency: acc.Currency, TaxStatus: acc.TaxStatus,
		CreatedBy: &actor,
	})
	if err != nil {
		return nil, err
	}
	if !inserted {
		return nil, fmt.Errorf("%w: usage invoice %s already exists", ErrBadInput, key)
	}
	groups := map[string][]gen.UninvoicedChargeRow{}
	for _, c := range charges {
		if c.Currency != acc.Currency {
			return nil, fmt.Errorf("%w: charge %s in %s on an account in %s", ErrBadInput, c.ChargeID, c.Currency, acc.Currency)
		}
		groups[c.RuleType] = append(groups[c.RuleType], c)
	}
	rules := make([]string, 0, len(groups))
	for r := range groups {
		rules = append(rules, r)
	}
	sort.Strings(rules)
	var subtotal int64
	for _, rule := range rules {
		list := groups[rule]
		var total int64
		unit := list[0].UncoveredMinor
		for _, c := range list {
			total += c.UncoveredMinor
			if c.UncoveredMinor != unit {
				unit = 0
			}
		}
		ruleType := rule
		line, err := q.InsertBillingInvoiceLine(ctx, gen.InsertBillingInvoiceLineParams{
			InvoiceID: row.ID, Kind: LineUsage, RuleType: &ruleType,
			Description: fmt.Sprintf("Platform service: %s, %d charges", rule, len(list)),
			Quantity:    int64(len(list)), UnitAmountMinor: unit, TotalAmountMinor: total, Currency: acc.Currency,
		})
		if err != nil {
			return nil, err
		}
		lineID := line.ID
		for _, c := range list {
			if _, err := q.InsertChargeAllocation(ctx, gen.InsertChargeAllocationParams{
				ChargeID: c.ChargeID, Kind: credits.AllocInvoiceLine, InvoiceLineID: &lineID,
				AmountMinor: c.UncoveredMinor, Currency: acc.Currency,
			}); err != nil {
				return nil, fmt.Errorf("allocate charge %s: %w", c.ChargeID, err)
			}
		}
		subtotal += total
	}
	if _, err := q.SetBillingInvoiceTotals(ctx, row.ID, subtotal, 0, 0); err != nil {
		return nil, err
	}
	inv, err := Load(ctx, q, row.ID)
	if err != nil {
		return nil, err
	}
	return &inv, nil
}

func periodOf(t time.Time) string { return t.UTC().Format("2006-01") } // allow:timeformat

func monthBounds(period, zone string) (time.Time, time.Time, error) {
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("%w: issuer zone %q", ErrBadInput, zone)
	}
	m, err := time.ParseInLocation("2006-01", period, loc)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("%w: period %q is not YYYY-MM", ErrBadInput, period)
	}
	return m, m.AddDate(0, 1, 0), nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Issue
// ─────────────────────────────────────────────────────────────────────────────

// Issue moves a draft to issued: the card fee line for a card payment (from
// billing_payment_fees in force now; refused when none), the next gapless
// number of the issuer's series for the year, a payment reference and the due
// date from the account's terms. Tax is not computed (stop condition ST-3:
// rates and texts wait for the consultant); tax_minor stays 0.
func Issue(ctx context.Context, q *gen.Queries, invoiceID uuid.UUID, method string, now time.Time) (Invoice, error) {
	if method != MethodBankTransfer && method != MethodStripeCard {
		return Invoice{}, fmt.Errorf("%w: payment method %q", ErrBadInput, method)
	}
	inv, err := q.LockBillingInvoice(ctx, invoiceID)
	if err != nil {
		return Invoice{}, err
	}
	if inv.State != "draft" {
		return Invoice{}, ErrNotDraft
	}
	if inv.AccountID == nil || inv.IssuerID == nil {
		return Invoice{}, fmt.Errorf("%w: a legacy invoice without a billing account", ErrBadInput)
	}
	if inv.SubtotalMinor <= 0 {
		return Invoice{}, fmt.Errorf("%w: nothing to bill", ErrBadInput)
	}
	if err := q.DeleteBillingInvoiceCardFeeLines(ctx, inv.ID); err != nil {
		return Invoice{}, err
	}
	var fee int64
	if method == MethodStripeCard {
		f, err := q.GetBillingPaymentFeeAt(ctx, MethodStripeCard, inv.Currency, now)
		if errors.Is(err, pgx.ErrNoRows) {
			return Invoice{}, ErrCardFeeNotConfigured
		}
		if err != nil {
			return Invoice{}, err
		}
		if fee, err = CardFee(inv.SubtotalMinor+inv.TaxMinor, f.PctBp, f.FixedMinor); err != nil {
			return Invoice{}, err
		}
		if fee > 0 {
			if _, err := q.InsertBillingInvoiceLine(ctx, gen.InsertBillingInvoiceLineParams{
				InvoiceID: inv.ID, Kind: LineCardFee, Description: "Payment system fee (card payment)",
				Quantity: 1, UnitAmountMinor: fee, TotalAmountMinor: fee, Currency: inv.Currency,
			}); err != nil {
				return Invoice{}, err
			}
		}
	}
	if _, err := q.SetBillingInvoiceTotals(ctx, inv.ID, inv.SubtotalMinor, fee, inv.TaxMinor); err != nil {
		return Invoice{}, err
	}
	issuer, err := q.GetBillingIssuer(ctx, *inv.IssuerID)
	if err != nil {
		return Invoice{}, err
	}
	loc, err := time.LoadLocation(issuer.Timezone)
	if err != nil {
		return Invoice{}, fmt.Errorf("%w: issuer zone %q", ErrBadInput, issuer.Timezone)
	}
	year := int32(now.In(loc).Year()) //nolint:gosec // a calendar year fits int32
	seq, err := q.NextInvoiceNumber(ctx, issuer.ID, year)
	if err != nil {
		return Invoice{}, err
	}
	series := defaultSeries
	if issuer.InvoiceSeries != nil && strings.TrimSpace(*issuer.InvoiceSeries) != "" {
		series = strings.TrimSpace(*issuer.InvoiceSeries)
	}
	ref, err := NewPaymentReference()
	if err != nil {
		return Invoice{}, err
	}
	var due *time.Time
	if terms, err := q.GetBillingAccountTermsAt(ctx, *inv.AccountID, now); err == nil && terms.DueDays != nil {
		d := now.AddDate(0, 0, int(*terms.DueDays))
		due = &d
	} else if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Invoice{}, err
	}
	if _, err := q.IssueBillingInvoice(ctx, gen.IssueBillingInvoiceParams{
		ID: inv.ID, Number: fmt.Sprintf("%s-%d-%06d", series, year, seq), NumberYear: year, NumberSeq: seq,
		PaymentMethod: method, PaymentReference: ref, DueAt: due, IssuedAt: now,
	}); err != nil {
		return Invoice{}, err
	}
	return Load(ctx, q, inv.ID)
}

// ─────────────────────────────────────────────────────────────────────────────
// Payment
// ─────────────────────────────────────────────────────────────────────────────

// Payment is money that arrived for an invoice.
type Payment struct {
	Via             string
	Reference       string
	AmountMinor     int64
	Currency        string
	At              time.Time
	PaymentIntentID *string
	ActorType       string
	ActorID         string
}

// MarkPaid records the payment of an issued invoice. A card or Stripe payment
// must equal the total; a bank transfer (or one an operator records by hand)
// may be the total or the total without the card fee (a transfer is a net
// payment). For a credit_pack invoice the
// lot and its purchase entry are written in the SAME transaction (41 section
// 6.5), so a failure leaves the invoice unpaid. A repeat of the same payment
// (same reference) changes nothing and answers changed=false.
func MarkPaid(ctx context.Context, q *gen.Queries, invoiceID uuid.UUID, p Payment) (inv gen.BillingInvoiceRow, changed bool, err error) {
	inv, err = q.LockBillingInvoice(ctx, invoiceID)
	if err != nil {
		return inv, false, err
	}
	if inv.State == "paid" {
		if inv.PaidReference != nil && *inv.PaidReference == p.Reference {
			return inv, false, nil
		}
		return inv, false, ErrAlreadyPaid
	}
	if inv.State != "issued" {
		return inv, false, ErrNotPayable
	}
	if p.Currency != inv.Currency {
		return inv, false, fmt.Errorf("%w: %s paid for an invoice in %s", ErrAmountMismatch, p.Currency, inv.Currency)
	}
	ok := p.AmountMinor == inv.TotalAmountMinor
	if (p.Via == PaidViaBankTransfer || p.Via == PaidViaManual) && inv.CardFeeMinor > 0 && p.AmountMinor == inv.TotalAmountMinor-inv.CardFeeMinor {
		ok = true
	}
	if !ok {
		return inv, false, fmt.Errorf("%w: paid %d, invoice %d", ErrAmountMismatch, p.AmountMinor, inv.TotalAmountMinor)
	}
	paid, err := q.MarkBillingInvoicePaid(ctx, gen.MarkBillingInvoicePaidParams{
		ID: inv.ID, PaidAt: p.At, PaidAmountMinor: p.AmountMinor, Currency: p.Currency, PaidVia: p.Via,
		PaidReference: p.Reference, PaymentIntentID: p.PaymentIntentID,
	})
	if err != nil {
		return inv, false, err
	}
	if paid.Kind == KindCreditPack {
		if paid.PackID == nil || paid.AccountID == nil {
			return inv, false, fmt.Errorf("%w: credit pack invoice without pack or account", ErrBadInput)
		}
		pack, err := q.GetPriceListPackByID(ctx, *paid.PackID)
		if err != nil {
			return inv, false, err
		}
		if _, _, err := credits.Purchase(ctx, q, credits.PurchaseInput{
			AccountID: *paid.AccountID, OrgID: paid.OrgID, Currency: paid.Currency, Pack: pack,
			InvoiceID: paid.ID, PaidAt: p.At, ActorType: p.ActorType, ActorID: p.ActorID,
		}); err != nil {
			return inv, false, err
		}
	}
	return paid, true, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Void
// ─────────────────────────────────────────────────────────────────────────────

// Void voids a draft or issued invoice and gives its charges back for the
// next invoice. A paid invoice is never voided (credit note only, 41 section
// 6.6). An invoice collected by Stripe must be voided in Stripe FIRST by the
// caller; when Stripe says it is already paid, the caller marks it paid.
func Void(ctx context.Context, q *gen.Queries, invoiceID uuid.UUID, reason string, now time.Time) (gen.BillingInvoiceRow, error) {
	if strings.TrimSpace(reason) == "" {
		return gen.BillingInvoiceRow{}, fmt.Errorf("%w: a reason is required", ErrBadInput)
	}
	inv, err := q.LockBillingInvoice(ctx, invoiceID)
	if err != nil {
		return inv, err
	}
	switch inv.State {
	case "paid":
		return inv, ErrPaidNeedsCreditNote
	case "void":
		return inv, nil
	}
	voided, err := q.VoidBillingInvoice(ctx, inv.ID, now, reason)
	if err != nil {
		return inv, err
	}
	if _, err := q.DeleteInvoiceLineAllocations(ctx, inv.ID); err != nil {
		return inv, err
	}
	return voided, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Bank payments
// ─────────────────────────────────────────────────────────────────────────────

// Unmatched reasons of a bank payment.
const (
	UnmatchedNoReference      = "no_reference"
	UnmatchedUnknownReference = "unknown_reference"
	UnmatchedNotOpen          = "invoice_not_open"
	UnmatchedAmount           = "amount_mismatch"
	UnmatchedCurrency         = "currency_mismatch"
)

// MatchResult says what became of a bank payment.
type MatchResult struct {
	Transaction gen.BillingBankTransactionRow
	Invoice     *gen.BillingInvoiceRow
	Reason      string
}

// MatchBankPayment ties an unmatched incoming payment to the issued invoice
// whose payment reference it carries and whose amount (total, or total
// without the card fee) it equals, and records the payment. Anything else is
// left unmatched with a reason for an operator. Under a lock of the payment
// row, so two deliveries never pay twice.
func MatchBankPayment(ctx context.Context, q *gen.Queries, txID uuid.UUID, by string, now time.Time) (MatchResult, error) {
	t, err := q.LockBankTransaction(ctx, txID)
	if err != nil {
		return MatchResult{}, err
	}
	if t.Status == "matched" || t.Status == "ignored" {
		return MatchResult{Transaction: t}, nil
	}
	refs := []string(nil)
	if t.Reference != nil {
		refs = FindReferences(*t.Reference)
	}
	reason := UnmatchedNoReference
	if len(refs) > 0 {
		reason = UnmatchedUnknownReference
	}
	for _, ref := range refs {
		inv, err := q.GetBillingInvoiceByPaymentReference(ctx, ref)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return MatchResult{}, err
		}
		paid, _, err := MarkPaid(ctx, q, inv.ID, Payment{
			Via: PaidViaBankTransfer, Reference: t.Provider + ":" + t.ExternalID, AmountMinor: t.AmountMinor,
			Currency: t.Currency, At: t.BookedAt, ActorType: "bank", ActorID: t.Provider,
		})
		switch {
		case err == nil:
			row, err := q.MatchBankTransaction(ctx, t.ID, inv.ID, now, by)
			if err != nil {
				return MatchResult{}, err
			}
			return MatchResult{Transaction: row, Invoice: &paid}, nil
		case errors.Is(err, ErrAmountMismatch):
			reason = UnmatchedAmount
			if t.Currency != inv.Currency {
				reason = UnmatchedCurrency
			}
		case errors.Is(err, ErrNotPayable), errors.Is(err, ErrAlreadyPaid):
			reason = UnmatchedNotOpen
		default:
			return MatchResult{}, err
		}
	}
	row, err := q.SetBankTransactionStatus(ctx, t.ID, "unmatched", &reason)
	if err != nil {
		return MatchResult{}, err
	}
	return MatchResult{Transaction: row, Reason: reason}, nil
}

// MatchBankPaymentTo ties an unmatched payment to an invoice an operator
// chose, with the same amount rule (a reference is not required).
func MatchBankPaymentTo(ctx context.Context, q *gen.Queries, txID, invoiceID uuid.UUID, by string, now time.Time) (MatchResult, error) {
	t, err := q.LockBankTransaction(ctx, txID)
	if err != nil {
		return MatchResult{}, err
	}
	if t.Status == "matched" {
		if t.InvoiceID != nil && *t.InvoiceID == invoiceID {
			return MatchResult{Transaction: t}, nil
		}
		return MatchResult{}, ErrAlreadyPaid
	}
	paid, _, err := MarkPaid(ctx, q, invoiceID, Payment{
		Via: PaidViaBankTransfer, Reference: t.Provider + ":" + t.ExternalID, AmountMinor: t.AmountMinor,
		Currency: t.Currency, At: t.BookedAt, ActorType: "user", ActorID: by,
	})
	if err != nil {
		return MatchResult{}, err
	}
	row, err := q.MatchBankTransaction(ctx, t.ID, invoiceID, now, by)
	if err != nil {
		return MatchResult{}, err
	}
	return MatchResult{Transaction: row, Invoice: &paid}, nil
}
