// Package credits spends and returns the credit lots of a billing account
// (billing wave B-B, tasks B3-1 and B3-2; 41 sections 5.2, 6.4, 6.5, 8.1).
//
// plan.go is the pure part: given one positive charge and the lots a charge
// at that moment may spend, it decides what each lot pays, what the pack
// tier makes cheaper and what stays for the invoice. No database, no clock.
//
// The rules, in order (41 section 6.5, owner decisions B-6, B-9, B-11,
// B-12, B-18, B-20):
//
//  1. The price of the ticket is set by the FIRST purchased lot (FIFO): its
//     tier rate for the unseated part plus its seated surcharge for a seated
//     ticket (B-18: the surcharge is paid from the same balance, never
//     multiplied by the tier). A tier price is never above the pay-as-you-go
//     price of the charge (each part is capped by it). An organization with
//     an individual rate buys packs at face, without a tier discount
//     (policy section 12 item 2): its tier price IS the charge. A charge of
//     any other rule than fixed_per_ticket (low price, venue percentage, a
//     fee per event) is paid from lots at its own amount (B-5: "из лотов
//     гасить"), no tier.
//  2. Bonus lots pay at most bonus_share_cap of the unseated part of that
//     price, rounded DOWN (B-11: 30 %), never the seated surcharge unless
//     bonus_covers_surcharge, and never below the revenue floor per ticket
//     (B-12, B-20: price minus bonus >= floor). Lots nearest to expiry pay
//     first. A low-price ticket or a venue percentage takes no bonus (41
//     section 5.3: LOW-PRICE does not stack with bonuses).
//  3. Purchased lots pay the rest FIFO. When a lot runs out, what is left
//     of the ticket is priced at the next lot's tier in proportion
//     (leftover × next price / this price, rounded down: the rounding is
//     the organizer's).
//  4. What no lot covers stays on the charge for the invoice, priced by the
//     pay-as-you-go rate in the same proportion (B-7: shortfall is invoiced
//     at the account's own prices).
//  5. The difference between the charge and bonus + purchased + invoiced is
//     the pack discount (pack_rate_discount allocation): never negative,
//     zero when no purchased lot took part.
package credits

import (
	"errors"
	"fmt"
	"math/bits"

	"github.com/google/uuid"
)

// Allocation kinds written to charge_allocations.
const (
	AllocPurchased    = "purchased_lot"
	AllocBonus        = "bonus_lot"
	AllocPackDiscount = "pack_rate_discount"
	AllocInvoiceLine  = "invoice_line"
)

// ErrBadPlanInput is returned for a charge or lot the plan cannot be made
// for (a negative amount, a rate above the bounds of the price list).
var ErrBadPlanInput = errors.New("credits: bad plan input")

// maxMinor bounds every amount the plan multiplies (rule params are bounded
// by 1e9 minor units, billing.ParseRule).
const maxMinor = int64(1) << 40

// Lot is a lot as the plan sees it. UnseatedRateMinor and
// SeatedSurchargeMinor are the pack tier's (purchased lots only).
type Lot struct {
	ID                   uuid.UUID
	Currency             string
	RemainingMinor       int64
	UnseatedRateMinor    int64
	SeatedSurchargeMinor int64
}

// ChargeInput is the charge as the plan sees it. Money is in Currency, the
// account currency.
type ChargeInput struct {
	Currency    string
	AmountMinor int64
	// UnseatedFactMinor is the pay-as-you-go price of the unseated part
	// (fixed_per_ticket: unseated_minor). SurchargeFactMinor is the rest of
	// the charge for a seated ticket (seated_minor - unseated_minor).
	UnseatedFactMinor  int64
	SurchargeFactMinor int64
	Seated             bool
	// TierPriced: the charge is a fixed_per_ticket of the standard line,
	// the one a pack tier makes cheaper.
	TierPriced bool
	// BonusAllowed: bonus lots may pay part of it (a per-ticket charge of a
	// sold ticket).
	BonusAllowed bool
	// NoTierDiscount: the organization has an individual rate, packs are at face.
	NoTierDiscount bool
}

// Policy is the price list's credit parameters at the moment of the charge.
type Policy struct {
	Currency             string
	BonusShareCapBP      int64
	BonusCoversSurcharge bool
	// FloorPerTicketMinor is the revenue floor per ticket, nil when the
	// price list names none (stop condition ST-2 until a superadmin sets it).
	FloorPerTicketMinor *int64
}

// Part is one allocation of the plan.
type Part struct {
	Kind        string
	LotID       uuid.UUID
	AmountMinor int64
	Currency    string
}

// Plan is what one charge gets: the lot allocations, the pack discount and
// what stays for the invoice. Bonus + Purchased + DiscountMinor +
// UncoveredMinor == the charge.
type Plan struct {
	Currency       string
	Bonus          []Part
	Purchased      []Part
	DiscountMinor  int64
	UncoveredMinor int64
}

// BonusMinor is what bonus lots pay.
func (p Plan) BonusMinor() int64 { return sumParts(p.Bonus) }

// PurchasedMinor is what purchased lots pay.
func (p Plan) PurchasedMinor() int64 { return sumParts(p.Purchased) }

func sumParts(ps []Part) int64 {
	var s int64
	for _, p := range ps {
		s += p.AmountMinor
	}
	return s
}

// MakePlan decides how one positive charge is paid. purchased must be in
// FIFO order and bonus nearest to expiry first; lots with nothing left are
// skipped.
func MakePlan(c ChargeInput, pol Policy, purchased, bonus []Lot) (Plan, error) {
	if err := c.check(); err != nil {
		return Plan{}, err
	}
	if pol.BonusShareCapBP < 0 || pol.BonusShareCapBP > 10000 {
		return Plan{}, fmt.Errorf("%w: bonus share cap %d bp", ErrBadPlanInput, pol.BonusShareCapBP)
	}
	out := Plan{Currency: c.Currency}
	if c.AmountMinor == 0 {
		return out, nil
	}
	purchased = withRemaining(purchased)
	bonus = withRemaining(bonus)
	for _, l := range append(append([]Lot(nil), purchased...), bonus...) {
		if l.Currency != c.Currency {
			return Plan{}, fmt.Errorf("%w: lot %s in %s for a charge in %s", ErrBadPlanInput, l.ID, l.Currency, c.Currency)
		}
		if l.RemainingMinor > maxMinor || l.UnseatedRateMinor < 0 || l.UnseatedRateMinor > maxMinor ||
			l.SeatedSurchargeMinor < 0 || l.SeatedSurchargeMinor > maxMinor {
			return Plan{}, fmt.Errorf("%w: lot %s out of bounds", ErrBadPlanInput, l.ID)
		}
	}

	// 1. The price of the ticket at the first purchased lot.
	refUnseated, refPrice := c.UnseatedFactMinor, c.AmountMinor
	if len(purchased) > 0 {
		refUnseated, refPrice = c.priceAt(purchased[0])
	}

	// 2. Bonus.
	var bonusTotal int64
	if c.BonusAllowed && len(bonus) > 0 {
		base := refUnseated
		if pol.BonusCoversSurcharge {
			base = refPrice
		}
		limit, err := mulDiv(base, pol.BonusShareCapBP, 10000)
		if err != nil {
			return Plan{}, err
		}
		if pol.FloorPerTicketMinor != nil {
			limit = min(limit, max(0, refPrice-*pol.FloorPerTicketMinor))
		}
		for _, l := range bonus {
			if limit-bonusTotal <= 0 {
				break
			}
			take := min(l.RemainingMinor, limit-bonusTotal)
			out.Bonus = append(out.Bonus, Part{Kind: AllocBonus, LotID: l.ID, AmountMinor: take, Currency: c.Currency})
			bonusTotal += take
		}
	}

	// 3. Purchased lots, FIFO, converting what is left between tiers.
	if len(purchased) == 0 {
		out.UncoveredMinor = c.AmountMinor - bonusTotal
		return out, nil
	}
	need := refPrice - bonusTotal
	var paid int64
	prevPrice := refPrice
	for i, l := range purchased {
		_, price := c.priceAt(l)
		if i > 0 {
			conv, err := mulDiv(need, price, prevPrice)
			if err != nil {
				return Plan{}, err
			}
			need = conv
		}
		prevPrice = price
		if need <= 0 {
			break
		}
		take := min(l.RemainingMinor, need)
		out.Purchased = append(out.Purchased, Part{Kind: AllocPurchased, LotID: l.ID, AmountMinor: take, Currency: c.Currency})
		paid += take
		need -= take
	}

	// 4. What no lot covered, at the pay-as-you-go price.
	if need > 0 {
		unc, err := mulDiv(need, c.AmountMinor, prevPrice)
		if err != nil {
			return Plan{}, err
		}
		out.UncoveredMinor = unc
	}

	// 5. The pack discount absorbs the rest (and the rounding).
	out.DiscountMinor = c.AmountMinor - bonusTotal - paid - out.UncoveredMinor
	if out.DiscountMinor < 0 {
		return Plan{}, fmt.Errorf("%w: plan exceeds the charge by %d", ErrBadPlanInput, -out.DiscountMinor)
	}
	return out, nil
}

func (c ChargeInput) check() error {
	if c.AmountMinor < 0 || c.AmountMinor > maxMinor {
		return fmt.Errorf("%w: charge %d", ErrBadPlanInput, c.AmountMinor)
	}
	if c.UnseatedFactMinor < 0 || c.SurchargeFactMinor < 0 || c.UnseatedFactMinor+c.SurchargeFactMinor > c.AmountMinor {
		return fmt.Errorf("%w: parts %d + %d of a charge of %d", ErrBadPlanInput,
			c.UnseatedFactMinor, c.SurchargeFactMinor, c.AmountMinor)
	}
	if c.Currency == "" {
		return fmt.Errorf("%w: no currency", ErrBadPlanInput)
	}
	return nil
}

// priceAt is the ticket price (unseated part, whole) when lot l pays it.
func (c ChargeInput) priceAt(l Lot) (unseated, total int64) {
	if !c.TierPriced || c.NoTierDiscount {
		return c.UnseatedFactMinor, c.AmountMinor
	}
	unseated = min(l.UnseatedRateMinor, c.UnseatedFactMinor)
	total = unseated
	if c.Seated {
		total += min(l.SeatedSurchargeMinor, c.SurchargeFactMinor)
	}
	// The part of the charge that is neither the unseated rate nor the
	// surcharge (none today) is paid at its own amount.
	total += c.AmountMinor - c.UnseatedFactMinor - c.SurchargeFactMinor
	return unseated, total
}

func withRemaining(ls []Lot) []Lot {
	out := make([]Lot, 0, len(ls))
	for _, l := range ls {
		if l.RemainingMinor > 0 {
			out = append(out, l)
		}
	}
	return out
}

// mulDiv is floor(a × b / d) for non-negative a, b and positive d, exact
// in 128 bits.
func mulDiv(a, b, d int64) (int64, error) {
	if a < 0 || b < 0 || d <= 0 {
		if d == 0 && a == 0 {
			return 0, nil
		}
		return 0, fmt.Errorf("%w: mulDiv(%d, %d, %d)", ErrBadPlanInput, a, b, d)
	}
	hi, lo := bits.Mul64(uint64(a), uint64(b))
	if hi >= uint64(d) {
		return 0, fmt.Errorf("%w: mulDiv overflow", ErrBadPlanInput)
	}
	q, _ := bits.Div64(hi, lo, uint64(d))
	if q > uint64(maxMinor) {
		return 0, fmt.Errorf("%w: mulDiv result out of bounds", ErrBadPlanInput)
	}
	return int64(q), nil
}
