package credits

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// ErrPackMismatch is returned when a pack does not belong to the account's
// currency.
var ErrPackMismatch = errors.New("credits: pack does not fit the account")

// PurchaseInput is a paid credit pack: the lot it becomes carries the pack
// tier and rates as they were at the purchase, so a later price-list version
// never changes what was bought.
type PurchaseInput struct {
	AccountID uuid.UUID
	OrgID     uuid.UUID
	Currency  string
	Pack      gen.PriceListPackRow
	// InvoiceID is the paid credit_pack invoice: with the account it is the
	// idempotency key of the lot.
	InvoiceID uuid.UUID
	PaidAt    time.Time
	ActorType string
	ActorID   string
}

// Purchase writes the lot and the purchase journal entry of a paid pack. It
// must run in the SAME transaction that marks the invoice paid (41 section
// 6.5), and is idempotent by the invoice: a repeat returns the existing lot
// and created=false, writing nothing.
func Purchase(ctx context.Context, q *gen.Queries, in PurchaseInput) (lot gen.CreditLotRow, created bool, err error) {
	if in.Pack.Currency != in.Currency {
		return gen.CreditLotRow{}, false, fmt.Errorf("%w: pack in %s, account in %s", ErrPackMismatch, in.Pack.Currency, in.Currency)
	}
	if in.Pack.FaceMinor <= 0 {
		return gen.CreditLotRow{}, false, fmt.Errorf("%w: pack face %d", ErrPackMismatch, in.Pack.FaceMinor)
	}
	if err := q.LockBillingAccount(ctx, in.AccountID); err != nil {
		return gen.CreditLotRow{}, false, fmt.Errorf("credits: lock account %s: %w", in.AccountID, err)
	}
	tier := in.Pack.Tier
	rate, surcharge := in.Pack.UnseatedRateMinor, in.Pack.SeatedSurchargeMinor
	plID, packID, invID := in.Pack.PriceListID, in.Pack.ID, in.InvoiceID
	row, inserted, err := q.InsertCreditLot(ctx, gen.InsertCreditLotParams{
		AccountID: in.AccountID, OrgID: in.OrgID, Currency: in.Currency, Kind: KindPurchased,
		Source: SourceInvoice, SourceRef: &invID, Tier: &tier, UnseatedRateMinor: &rate,
		SeatedSurchargeMinor: &surcharge, PriceListID: &plID, PackID: &packID,
		PurchasedAt: in.PaidAt, FaceMinor: in.Pack.FaceMinor,
	})
	if err != nil {
		return gen.CreditLotRow{}, false, fmt.Errorf("credits: insert purchased lot: %w", err)
	}
	if !inserted {
		existing, err := q.GetCreditLotBySource(ctx, in.AccountID, SourceInvoice, in.InvoiceID)
		return existing, false, err
	}
	if _, err := q.AppendCreditLedger(ctx, gen.AppendCreditLedgerParams{
		AccountID: in.AccountID, LotID: row.ID, EntryType: EntryPurchase, AmountMinor: row.FaceMinor,
		Currency: in.Currency, RemainingAfter: row.RemainingMinor, InvoiceID: &invID,
		ActorType: in.ActorType, ActorID: in.ActorID,
	}); err != nil {
		return gen.CreditLotRow{}, false, fmt.Errorf("credits: journal purchase: %w", err)
	}
	return row, true, nil
}

// ExpireLot takes what is left of one expired lot (41 section 6.5). It locks
// the account, re-reads the lot and does nothing when it has nothing left or
// has not expired at now.
func ExpireLot(ctx context.Context, q *gen.Queries, lotID uuid.UUID, now time.Time) (int64, error) {
	lot, err := q.GetCreditLot(ctx, lotID)
	if err != nil {
		return 0, err
	}
	if err := q.LockBillingAccount(ctx, lot.AccountID); err != nil {
		return 0, err
	}
	if lot, err = q.GetCreditLot(ctx, lotID); err != nil {
		return 0, err
	}
	if lot.RemainingMinor <= 0 || lot.ExpiresAt == nil || lot.ExpiresAt.After(now) {
		return 0, nil
	}
	amount := lot.RemainingMinor
	if err := q.SetCreditLotRemaining(ctx, lot.ID, 0); err != nil {
		return 0, err
	}
	if _, err := q.AppendCreditLedger(ctx, gen.AppendCreditLedgerParams{
		AccountID: lot.AccountID, LotID: lot.ID, EntryType: EntryExpire, AmountMinor: amount,
		Currency: lot.Currency, RemainingAfter: 0,
	}); err != nil {
		return 0, err
	}
	return amount, nil
}
