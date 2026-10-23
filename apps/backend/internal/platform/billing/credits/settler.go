package credits

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing"
)

// Lot kinds and sources of credit_lots.
const (
	KindPurchased = "purchased"
	KindBonus     = "bonus"

	SourceInvoice      = "invoice"
	SourceCancellation = "cancellation"
	SourceCampaign     = "campaign"
	SourceAdjustment   = "adjustment"
)

// Journal entry types of credit_ledger.
const (
	EntryPurchase         = "purchase"
	EntryBonusGrant       = "bonus_grant"
	EntryConsume          = "consume"
	EntryReversal         = "reversal"
	EntryReversalDropped  = "reversal_dropped"
	EntryExpire           = "expire"
	EntryHold             = "hold"
	EntryRelease          = "release"
	EntryAdjustmentCredit = "adjustment_credit"
	EntryAdjustmentDebit  = "adjustment_debit"
)

// Options configures a Settler.
type Options struct {
	// Now is the clock (tests); defaults to time.Now.
	Now func() time.Time
	// CancellationBonusExpiry returns the expiry of a bonus lot born from a
	// cancelled session, nil for none. The owner has not named the term yet
	// (B-6 "срок как у офферов", stop condition ST-2): the default is nil.
	CancellationBonusExpiry func(grantedAt time.Time) *time.Time
}

// Settler spends credit lots against new charges and turns the charges of a
// cancelled session into bonus credits (B-6). It implements rate.Settler and
// runs inside the rating transaction: every method takes the billing
// account's row lock first (spec 42 rule 10). The lock order is the rate
// job's session lock, then the account.
type Settler struct{ opts Options }

// NewSettler returns a Settler.
func NewSettler(opts Options) *Settler {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.CancellationBonusExpiry == nil {
		opts.CancellationBonusExpiry = func(time.Time) *time.Time { return nil }
	}
	return &Settler{opts: opts}
}

// Settle spends lots against one positive direct charge. A lot is available
// only when it was bought (or granted) no later than the event and has not
// expired at it (41 section 5.2): a late-collected sale never spends credits
// bought after it. A bonus lot never pays for the event its cancellation
// came from (B-19). Nothing is written when the account has no lot.
func (s *Settler) Settle(ctx context.Context, q *gen.Queries, ev gen.BillableEventRow, ch gen.ChargeRow) error {
	if ch.AmountMinor <= 0 || ch.ReversalOf != nil {
		return nil
	}
	if err := q.LockBillingAccount(ctx, ch.AccountID); err != nil {
		return fmt.Errorf("credits: lock account %s: %w", ch.AccountID, err)
	}
	purchasedRows, err := q.ListAvailableCreditLots(ctx, ch.AccountID, KindPurchased, ev.OccurredAt)
	if err != nil {
		return err
	}
	bonusRows, err := q.ListAvailableCreditLots(ctx, ch.AccountID, KindBonus, ev.OccurredAt)
	if err != nil {
		return err
	}
	bonusRows = notFromEvent(bonusRows, ev.EventID)
	if len(purchasedRows) == 0 && len(bonusRows) == 0 {
		return nil
	}
	if ch.PriceListID == nil {
		return fmt.Errorf("credits: charge %s has no price list", ch.ID)
	}
	pl, err := q.GetPriceListByID(ctx, *ch.PriceListID)
	if err != nil {
		return fmt.Errorf("credits: price list of charge %s: %w", ch.ID, err)
	}
	in, err := chargeInput(ev, ch)
	if err != nil {
		return err
	}
	pol := Policy{Currency: ch.Currency, BonusShareCapBP: int64(pl.BonusShareCapBP),
		BonusCoversSurcharge: pl.BonusCoversSurcharge, FloorPerTicketMinor: pl.FloorPerTicketMinor}
	plan, err := MakePlan(in, pol, toLots(purchasedRows), toLots(bonusRows))
	if err != nil {
		return fmt.Errorf("credits: plan for charge %s: %w", ch.ID, err)
	}
	lots := map[uuid.UUID]gen.CreditLotRow{}
	for _, l := range append(append([]gen.CreditLotRow(nil), purchasedRows...), bonusRows...) {
		lots[l.ID] = l
	}
	for _, part := range append(append([]Part(nil), plan.Bonus...), plan.Purchased...) {
		lot := lots[part.LotID]
		lotID := part.LotID
		alloc, err := q.InsertChargeAllocation(ctx, gen.InsertChargeAllocationParams{
			ChargeID: ch.ID, Kind: part.Kind, LotID: &lotID, AmountMinor: part.AmountMinor, Currency: ch.Currency,
		})
		if err != nil {
			return fmt.Errorf("credits: allocate charge %s to lot %s: %w", ch.ID, lotID, err)
		}
		after := lot.RemainingMinor - part.AmountMinor
		if after < 0 {
			return fmt.Errorf("credits: lot %s would go negative", lotID)
		}
		if err := q.SetCreditLotRemaining(ctx, lotID, after); err != nil {
			return err
		}
		lot.RemainingMinor = after
		lots[lotID] = lot
		chargeID, allocID, evID := ch.ID, alloc.ID, ev.ID
		if _, err := q.AppendCreditLedger(ctx, gen.AppendCreditLedgerParams{
			AccountID: ch.AccountID, LotID: lotID, EntryType: EntryConsume, AmountMinor: part.AmountMinor,
			Currency: ch.Currency, RemainingAfter: after, ChargeID: &chargeID, AllocationID: &allocID,
			CauseEventID: &evID,
		}); err != nil {
			return fmt.Errorf("credits: journal consume of lot %s: %w", lotID, err)
		}
	}
	if plan.DiscountMinor > 0 {
		if _, err := q.InsertChargeAllocation(ctx, gen.InsertChargeAllocationParams{
			ChargeID: ch.ID, Kind: AllocPackDiscount, AmountMinor: plan.DiscountMinor, Currency: ch.Currency,
		}); err != nil {
			return fmt.Errorf("credits: pack discount of charge %s: %w", ch.ID, err)
		}
	}
	return nil
}

// SettleReversal handles the paired reversal of a charge of a cancelled
// session (owner decision B-6, revised 16.10 evening): what lots or an
// invoice already paid of the original charge becomes ONE bonus lot per
// cancellation (source cancellation, ref = the session_cancelled event), with
// every bonus rule (30 % share, next events only); what a bonus lot paid goes
// back to that lot while it has not expired, else is journaled as
// reversal_dropped (41 section 6.5); the pack discount and what was never
// paid (the uncovered part, still to be invoiced) give nothing: the reversal
// row nets it out of the invoice.
func (s *Settler) SettleReversal(ctx context.Context, q *gen.Queries, cause gen.BillableEventRow, orig, _ gen.ChargeRow) error {
	if err := q.LockBillingAccount(ctx, orig.AccountID); err != nil {
		return fmt.Errorf("credits: lock account %s: %w", orig.AccountID, err)
	}
	allocs, err := q.ListCreditAllocationsByCharge(ctx, orig.ID)
	if err != nil {
		return err
	}
	now := s.opts.Now()
	origID, causeID := orig.ID, cause.ID
	var toBonus int64
	for _, a := range allocs {
		switch a.Kind {
		case AllocPurchased, AllocInvoiceLine:
			toBonus += a.AmountMinor
		case AllocBonus:
			if a.LotID == nil {
				return fmt.Errorf("credits: bonus allocation %s without a lot", a.ID)
			}
			lot, err := q.GetCreditLot(ctx, *a.LotID)
			if err != nil {
				return err
			}
			entry, after := EntryReversalDropped, lot.RemainingMinor
			if lot.ExpiresAt == nil || lot.ExpiresAt.After(now) {
				entry, after = EntryReversal, lot.RemainingMinor+a.AmountMinor
				if after > lot.FaceMinor {
					return fmt.Errorf("credits: lot %s would exceed its face", lot.ID)
				}
				if err := q.SetCreditLotRemaining(ctx, lot.ID, after); err != nil {
					return err
				}
			}
			allocID := a.ID
			if _, err := q.AppendCreditLedger(ctx, gen.AppendCreditLedgerParams{
				AccountID: orig.AccountID, LotID: lot.ID, EntryType: entry, AmountMinor: a.AmountMinor,
				Currency: a.Currency, RemainingAfter: after, ChargeID: &origID, AllocationID: &allocID,
				CauseEventID: &causeID,
			}); err != nil {
				return err
			}
		}
	}
	if toBonus <= 0 {
		return nil
	}
	return s.grantCancellationBonus(ctx, q, cause, orig, toBonus)
}

func (s *Settler) grantCancellationBonus(ctx context.Context, q *gen.Queries, cause gen.BillableEventRow, orig gen.ChargeRow, amount int64) error {
	causeID, origID := cause.ID, orig.ID
	reason := "session cancelled: charges become bonus credits (B-6)"
	lot, err := q.GetCreditLotBySource(ctx, orig.AccountID, SourceCancellation, cause.ID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		row, inserted, err := q.InsertCreditLot(ctx, gen.InsertCreditLotParams{
			AccountID: orig.AccountID, OrgID: cause.OrgID, Currency: orig.Currency, Kind: KindBonus,
			Source: SourceCancellation, SourceRef: &causeID, SourceEventID: cause.EventID,
			PurchasedAt: cause.OccurredAt, FaceMinor: amount,
			ExpiresAt: s.opts.CancellationBonusExpiry(cause.OccurredAt),
		})
		if err != nil {
			return fmt.Errorf("credits: cancellation bonus lot: %w", err)
		}
		if !inserted {
			return fmt.Errorf("credits: cancellation bonus lot of %s appeared under the account lock", cause.ID)
		}
		lot = row
	case err != nil:
		return err
	default:
		if lot, err = q.GrowCreditLot(ctx, lot.ID, amount); err != nil {
			return fmt.Errorf("credits: grow cancellation bonus lot %s: %w", lot.ID, err)
		}
	}
	_, err = q.AppendCreditLedger(ctx, gen.AppendCreditLedgerParams{
		AccountID: orig.AccountID, LotID: lot.ID, EntryType: EntryBonusGrant, AmountMinor: amount,
		Currency: orig.Currency, RemainingAfter: lot.RemainingMinor, ChargeID: &origID,
		CauseEventID: &causeID, Reason: &reason,
	})
	return err
}

func notFromEvent(lots []gen.CreditLotRow, eventID *uuid.UUID) []gen.CreditLotRow {
	if eventID == nil {
		return lots
	}
	out := lots[:0:0]
	for _, l := range lots {
		if l.SourceEventID != nil && *l.SourceEventID == *eventID {
			continue
		}
		out = append(out, l)
	}
	return out
}

func toLots(rows []gen.CreditLotRow) []Lot {
	out := make([]Lot, 0, len(rows))
	for _, r := range rows {
		l := Lot{ID: r.ID, Currency: r.Currency, RemainingMinor: r.RemainingMinor}
		if r.UnseatedRateMinor != nil {
			l.UnseatedRateMinor = *r.UnseatedRateMinor
		}
		if r.SeatedSurchargeMinor != nil {
			l.SeatedSurchargeMinor = *r.SeatedSurchargeMinor
		}
		out = append(out, l)
	}
	return out
}

// chargeInput reads what the plan needs from a charge row: its rule, and
// for a per-ticket rule the unseated price and the seated surcharge the
// rating engine recorded in inputs.
func chargeInput(ev gen.BillableEventRow, ch gen.ChargeRow) (ChargeInput, error) {
	in := ChargeInput{Currency: ch.Currency, AmountMinor: ch.AmountMinor, UnseatedFactMinor: ch.AmountMinor}
	rule := billing.RuleType(ch.RuleType)
	if rule != billing.RuleFixedPerTicket && rule != billing.RuleOrgRateOverride {
		return in, nil
	}
	var inputs map[string]int64
	if len(ch.Inputs) > 0 {
		if err := json.Unmarshal(ch.Inputs, &inputs); err != nil {
			return ChargeInput{}, fmt.Errorf("credits: inputs of charge %s: %w", ch.ID, err)
		}
	}
	unseated, ok := inputs["unseated_minor"]
	if !ok {
		return ChargeInput{}, fmt.Errorf("credits: charge %s has no unseated_minor input", ch.ID)
	}
	unseated = min(max(unseated, 0), ch.AmountMinor)
	in.UnseatedFactMinor = unseated
	in.Seated = inputs["seated"] == 1
	if in.Seated {
		in.SurchargeFactMinor = ch.AmountMinor - unseated
	} else {
		in.UnseatedFactMinor = ch.AmountMinor
	}
	in.TierPriced = true
	in.NoTierDiscount = rule == billing.RuleOrgRateOverride
	in.BonusAllowed = ev.Kind == string(billing.KindTicketSold)
	return in, nil
}
