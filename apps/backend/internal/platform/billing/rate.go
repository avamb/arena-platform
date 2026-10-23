package billing

import (
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// EventKind is the kind of a billable event (41 section 6.2).
type EventKind string

const (
	KindTicketSold          EventKind = "ticket_sold"
	KindTicketRefunded      EventKind = "ticket_refunded"
	KindTicketCancelled     EventKind = "ticket_cancelled"
	KindSessionCancelled    EventKind = "session_cancelled"
	KindEventPublished      EventKind = "event_published"
	KindTicketComplimentary EventKind = "ticket_complimentary"
)

// Pricing classes a sale is snapshotted with (ClassifyLowPrice).
const (
	PricingClassStandard = "standard"
	PricingClassLowPrice = "low_price"
)

// Charge.Reason values.
const (
	ReasonNone        = ""
	ReasonFree        = "free"
	ReasonOverride    = "override"
	ReasonNoRule      = "no_rule"
	ReasonJournalOnly = "journal_only"
	// ReasonBuyerPays marks a charge equal to the service charge the buyer
	// paid (fee_mode buyer_pays).
	ReasonBuyerPays = "buyer_pays"
)

// Fee modes of billing_account_terms.fee_mode (migration 0160).
const (
	// FeeModeOrganizerPays: the organizer pays the price-list rate.
	FeeModeOrganizerPays = "organizer_pays"
	// FeeModeBuyerPays: the organizer wants the net amount; the service
	// charge the buyer pays on a ticket IS the platform's commission.
	FeeModeBuyerPays = "buyer_pays"
)

// RuleBuyerServiceCharge is the rule type of a charge made in fee_mode
// buyer_pays. It is not a price-list rule: no price_list_rules row carries it.
const RuleBuyerServiceCharge RuleType = "buyer_service_charge"

// DeferredFXRateMissing marks a charge that cannot be computed yet: a
// percentage rule on a sale in another currency than the account's needs
// the exchange rate of the sale day, which the code does not have (41
// section 5.7, run decision R-B5). The job leaves such an event for later.
const DeferredFXRateMissing = "fx_rate_missing"

var (
	// ErrInconsistentEvent: the event contradicts itself or the terms it is
	// rated with (another OccurredAt, a negative amount, a low_price class
	// that the terms could not have given it).
	ErrInconsistentEvent = errors.New("billing: inconsistent billable event")
	// ErrNoRule: the price list has no rule for an event that must be
	// charged. Never read as a zero charge.
	ErrNoRule = errors.New("billing: no price list rule for the event")
	// ErrUnknownEventKind: an event kind Rate does not know.
	ErrUnknownEventKind = errors.New("billing: unknown billable event kind")
)

// Event is the part of a billable event Rate reads.
type Event struct {
	Kind       EventKind
	OccurredAt time.Time
	Seated     bool
	// Currency is the SALE currency (orders.currency), which may differ
	// from the account currency (Terms.Currency).
	Currency string
	// UnitPriceMinor is the ticket price before the promo code, without the
	// service charge.
	UnitPriceMinor int64
	// DiscountMinor is the promo-code discount on this ticket.
	DiscountMinor int64
	// ServiceChargeMinor is the buyer's service charge on this ticket. Rate
	// only records it; no rule reads it.
	ServiceChargeMinor int64
	// PricingClass is the class snapshotted at the sale (ClassifyLowPrice):
	// PricingClassStandard or PricingClassLowPrice. Empty reads as standard.
	PricingClass string
	// FreeTicketsOverLimit marks a complimentary ticket beyond the yearly
	// free limit. Always false until the limit is counted (block B2).
	FreeTicketsOverLimit bool
}

// Charge is what Rate computes for one event. AmountMinor is in Currency,
// the ACCOUNT currency. Inputs lists every number the calculation read, so
// a charge can be reproduced and explained.
type Charge struct {
	AmountMinor      int64
	Currency         string
	RuleType         RuleType
	RuleID           *uuid.UUID
	PriceListID      uuid.UUID
	PriceListVersion int32
	OverrideID       *uuid.UUID
	Inputs           map[string]int64
	Reason           string
	Deferred         string
}

// Rate computes the charge of one billable event under terms. It is a pure
// function: no database, no clock. Every percentage is rounded DOWN to a
// whole minor unit of the account currency (integer floor of amount × basis
// points / 10000, the convention of hcheckout.ComputePricingLines): 2 % of
// 25.75 is 0.51, 4 % of 3.33 is 0.13. Caps and minimums apply after it.
//
// Order of the rules:
//
//   - ticket_refunded, ticket_cancelled: 0, journal_only. A sold ticket's
//     charge stays (41 section 6.3, owner decision B-6).
//   - session_cancelled: 0, journal_only. The reversal of a cancelled
//     session is written by the rating job (B2-3), never by Rate.
//   - ticket_complimentary: 0, free; beyond the yearly limit it is charged
//     as an unseated ticket at the organization's rate.
//   - ticket_sold whose price after the promo code is 0 or less: 0, free.
//   - ticket_sold with an individual rate: that rate, seated or not. The
//     individual rate replaces the price list EVERYWHERE, the low-price
//     formula and the venue percentage included (owner decision B-2, 41
//     section 8.1, run decision R-B6).
//   - ticket_sold on a venue plan: percent_of_price_with_cap of the venue
//     line on (price − discount), seated or not; a sale in another currency
//     than the account's is deferred (fx_rate_missing).
//   - ticket_sold of class low_price: low_price_formula of the standard line
//     for the sale currency; a sale currency other than the account's is
//     deferred (fx_rate_missing), a class without such a rule is an error.
//   - ticket_sold otherwise: fixed_per_ticket of the standard line.
//   - event_published: fixed_per_event when the price list has one, else 0,
//     no_rule (standard-2026 has none).
//
// A line that carries a rule type Rate cannot compute yet
// (volume_above_baseline, subscription, generator_run) for the event kind
// fails with ErrRuleNotImplemented instead of being charged by another rule.
func Rate(terms Terms, ev Event) (Charge, error) {
	if !ev.OccurredAt.Equal(terms.At) {
		return Charge{}, fmt.Errorf("%w: event at %s rated with terms loaded for %s", ErrInconsistentEvent,
			ev.OccurredAt.UTC().Format(time.RFC3339Nano), terms.At.UTC().Format(time.RFC3339Nano))
	}
	if ev.UnitPriceMinor < 0 || ev.DiscountMinor < 0 || ev.ServiceChargeMinor < 0 {
		return Charge{}, fmt.Errorf("%w: negative amount in event", ErrInconsistentEvent)
	}
	c := Charge{
		Currency:         terms.Currency,
		PriceListID:      terms.PriceList.ID,
		PriceListVersion: terms.PriceList.Version,
		Inputs: map[string]int64{
			"unit_price_minor":     ev.UnitPriceMinor,
			"discount_minor":       ev.DiscountMinor,
			"service_charge_minor": ev.ServiceChargeMinor,
			"seated":               boolInput(ev.Seated),
		},
	}
	switch ev.Kind {
	case KindTicketRefunded, KindTicketCancelled, KindSessionCancelled:
		c.Reason = ReasonJournalOnly
		return c, nil
	case KindTicketComplimentary:
		if !ev.FreeTicketsOverLimit {
			c.Reason = ReasonFree
			return c, nil
		}
		c.Inputs["over_free_limit"] = 1
		return rateUnseatedAtOrgRate(terms, c)
	case KindEventPublished:
		return rateEventPublished(terms, ev, c)
	case KindTicketSold:
		return rateTicketSold(terms, ev, c)
	default:
		return Charge{}, fmt.Errorf("%w: %q", ErrUnknownEventKind, ev.Kind)
	}
}

func rateTicketSold(terms Terms, ev Event, c Charge) (Charge, error) {
	net := ev.UnitPriceMinor - ev.DiscountMinor
	c.Inputs["net_price_minor"] = net
	if net <= 0 {
		c.Reason = ReasonFree
		return c, nil
	}
	if terms.FeeMode() == FeeModeBuyerPays {
		return rateBuyerPays(terms, ev, c)
	}
	return rateTicketAtList(terms, ev, net, c)
}

// StandardTicketCharge is what one sold ticket of ev costs the organizer at
// the price list (or the individual rate), whatever the fee mode. In
// fee_mode buyer_pays it is the commission per ticket the checkout adds to
// the buyer's service charge (BuyerCharge.PerTicket), so the organizer keeps
// the ticket price. ev.Kind is ignored (a
// sold ticket is assumed); ev.OccurredAt must equal terms.At.
func StandardTicketCharge(terms Terms, ev Event) (Charge, error) {
	ev.Kind = KindTicketSold
	ev.ServiceChargeMinor = 0
	if !ev.OccurredAt.Equal(terms.At) || ev.UnitPriceMinor < 0 || ev.DiscountMinor < 0 {
		return Charge{}, fmt.Errorf("%w: quote event", ErrInconsistentEvent)
	}
	c := Charge{
		Currency: terms.Currency, PriceListID: terms.PriceList.ID, PriceListVersion: terms.PriceList.Version,
		Inputs: map[string]int64{"unit_price_minor": ev.UnitPriceMinor, "discount_minor": ev.DiscountMinor,
			"seated": boolInput(ev.Seated)},
	}
	net := ev.UnitPriceMinor - ev.DiscountMinor
	c.Inputs["net_price_minor"] = net
	if net <= 0 {
		c.Reason = ReasonFree
		return c, nil
	}
	return rateTicketAtList(terms, ev, net, c)
}

// rateBuyerPays charges the platform's commission the buyer paid inside the
// service charge on this ticket: the organizer keeps the ticket price. The
// checkout adds the commission (BuyerCharge.PerTicket, the rate of the ticket
// at its price BEFORE the promo code) ON TOP of the channel's own fee_percent,
// which stays the organizer's, and the order puts each ticket's commission in
// full into order_items.charge before spreading the percentage. So the
// charge is min(service charge, standard_minor): the platform never takes the
// organizer's percentage, and a service charge below the rate (a sale priced
// without the commission) is charged as paid, the gap visible through
// standard_minor. When the rate cannot be computed the service charge is
// charged as paid. A service charge in another currency than the account's
// waits for an exchange rate (fx_rate_missing), like every other
// cross-currency amount.
func rateBuyerPays(terms Terms, ev Event, c Charge) (Charge, error) {
	listEv := ev
	listEv.DiscountMinor = 0
	std, err := rateTicketAtList(terms, listEv, ev.UnitPriceMinor, Charge{
		Currency: c.Currency, PriceListID: c.PriceListID, PriceListVersion: c.PriceListVersion,
		Inputs: map[string]int64{},
	})
	stdKnown := err == nil && std.Deferred == ""
	if stdKnown {
		c.Inputs["standard_minor"] = std.AmountMinor
	}
	c.RuleType = RuleBuyerServiceCharge
	c.Reason = ReasonBuyerPays
	if ev.Currency != terms.Currency {
		c.Deferred = DeferredFXRateMissing
		return c, nil
	}
	c.AmountMinor = ev.ServiceChargeMinor
	if stdKnown && std.AmountMinor < c.AmountMinor {
		c.AmountMinor = std.AmountMinor
	}
	return c, nil
}

// rateTicketAtList is the price-list (or individual) rate of a sold ticket.
func rateTicketAtList(terms Terms, ev Event, net int64, c Charge) (Charge, error) {
	if o := terms.Override; o != nil {
		return applyOverride(o, ev.Seated, c), nil
	}
	switch terms.PlanKind() {
	case PlanVenue:
		return rateVenue(terms, ev, net, c)
	case PlanStandard:
	default:
		return Charge{}, fmt.Errorf("%w: unknown plan kind %q", ErrInconsistentEvent, terms.PlanKind())
	}
	if r, bad := terms.unimplemented(lineStandard, string(KindTicketSold)); bad {
		return Charge{}, fmt.Errorf("%w: %s rule %s on the standard line", ErrRuleNotImplemented, r.Type, r.ID)
	}
	switch ev.PricingClass {
	case PricingClassLowPrice:
		return rateLowPrice(terms, ev, net, c)
	case PricingClassStandard, "":
		return rateFixed(terms, ev.Seated, c)
	default:
		return Charge{}, fmt.Errorf("%w: unknown pricing class %q", ErrInconsistentEvent, ev.PricingClass)
	}
}

func applyOverride(o *gen.OrgRateOverrideRow, seated bool, c Charge) Charge {
	id := o.ID
	c.OverrideID = &id
	c.RuleType = RuleOrgRateOverride
	c.Reason = ReasonOverride
	c.Inputs["unseated_minor"] = o.UnseatedMinor
	c.Inputs["seated_minor"] = o.SeatedMinor
	c.AmountMinor = o.UnseatedMinor
	if seated {
		c.AmountMinor = o.SeatedMinor
	}
	return c
}

func rateFixed(terms Terms, seated bool, c Charge) (Charge, error) {
	r, ok := terms.rule(lineStandard, string(KindTicketSold), RuleFixedPerTicket, "")
	if !ok {
		return Charge{}, fmt.Errorf("%w: no fixed_per_ticket on price list %s", ErrNoRule, terms.PriceList.ID)
	}
	setRule(&c, r)
	c.Inputs["unseated_minor"] = r.Fixed.UnseatedMinor
	c.Inputs["seated_minor"] = r.Fixed.SeatedMinor
	c.AmountMinor = r.Fixed.UnseatedMinor
	if seated {
		c.AmountMinor = r.Fixed.SeatedMinor
	}
	return c, nil
}

func rateVenue(terms Terms, ev Event, net int64, c Charge) (Charge, error) {
	if r, bad := terms.unimplemented(lineVenue, string(KindTicketSold)); bad {
		return Charge{}, fmt.Errorf("%w: %s rule %s on the venue line", ErrRuleNotImplemented, r.Type, r.ID)
	}
	r, ok := terms.rule(lineVenue, string(KindTicketSold), RulePercentOfPriceWithCap, "")
	if !ok {
		return Charge{}, fmt.Errorf("%w: no percent_of_price_with_cap on the venue line of price list %s",
			ErrNoRule, terms.PriceList.ID)
	}
	setRule(&c, r)
	c.Inputs["pct_bp"] = r.Percent.PctBP
	c.Inputs["cap_minor"] = r.Percent.CapMinor
	if ev.Currency != terms.Currency {
		c.Deferred = DeferredFXRateMissing
		return c, nil
	}
	share, err := basisPoints(net, r.Percent.PctBP)
	if err != nil {
		return Charge{}, err
	}
	c.Inputs["share_minor"] = share
	c.AmountMinor = min(share, r.Percent.CapMinor)
	return c, nil
}

func rateLowPrice(terms Terms, ev Event, net int64, c Charge) (Charge, error) {
	r, ok := lowPriceRule(terms, ev.Currency)
	if !ok {
		return Charge{}, fmt.Errorf("%w: low_price class but price list %s has no low_price_formula for %s",
			ErrInconsistentEvent, terms.PriceList.ID, ev.Currency)
	}
	lp := r.LowPriceFormula
	setRule(&c, r)
	c.Inputs["threshold_minor"] = lp.ThresholdMinor
	c.Inputs["pct_bp"] = lp.PctBP
	c.Inputs["min_minor"] = lp.MinMinor
	c.Inputs["max_minor"] = lp.MaxMinor
	c.Inputs["seated_surcharge_minor"] = lp.SeatedSurchargeMinor
	if ev.Currency != terms.Currency {
		// A threshold in the sale currency, a charge in the account
		// currency: the percentage needs the exchange rate of the sale day
		// (41 section 5.7), deferred exactly like the venue percentage.
		c.Deferred = DeferredFXRateMissing
		return c, nil
	}
	share, err := basisPoints(net, lp.PctBP)
	if err != nil {
		return Charge{}, err
	}
	c.Inputs["share_minor"] = share
	amount := min(max(share, lp.MinMinor), lp.MaxMinor)
	if ev.Seated {
		amount += lp.SeatedSurchargeMinor
	}
	c.AmountMinor = amount
	return c, nil
}

// rateUnseatedAtOrgRate charges a complimentary ticket beyond the free limit
// like an unseated sold ticket: the individual rate, else fixed_per_ticket.
// A venue plan has no flat rate the policy names for it, so it is refused
// (stop condition ST-2) instead of guessed.
func rateUnseatedAtOrgRate(terms Terms, c Charge) (Charge, error) {
	if o := terms.Override; o != nil {
		return applyOverride(o, false, c), nil
	}
	if terms.PlanKind() != PlanStandard {
		return Charge{}, fmt.Errorf("%w: no unseated rate for a free ticket over the limit on a %s plan",
			ErrNoRule, terms.PlanKind())
	}
	if r, bad := terms.unimplemented(lineStandard, string(KindTicketSold)); bad {
		return Charge{}, fmt.Errorf("%w: %s rule %s on the standard line", ErrRuleNotImplemented, r.Type, r.ID)
	}
	return rateFixed(terms, false, c)
}

func rateEventPublished(terms Terms, _ Event, c Charge) (Charge, error) {
	line := lineStandard
	if terms.PlanKind() == PlanVenue {
		line = lineVenue
	}
	kind := string(KindEventPublished)
	if r, bad := terms.unimplemented(line, kind); bad {
		return Charge{}, fmt.Errorf("%w: %s rule %s for %s", ErrRuleNotImplemented, r.Type, r.ID, kind)
	}
	r, ok := terms.rule(line, kind, RuleFixedPerEvent, "")
	if !ok {
		c.Reason = ReasonNoRule
		return c, nil
	}
	setRule(&c, r)
	c.Inputs["amount_minor"] = r.PerEvent.AmountMinor
	c.AmountMinor = r.PerEvent.AmountMinor
	return c, nil
}

// ClassifyLowPrice is the pricing class a sale is snapshotted with when it
// is collected (task B2-2). It reads the low_price_formula of the standard
// line whose sale_currency is saleCurrency. Without one the class is
// standard and resolved is false: the currency has no threshold (run
// decision R-B5, the event carries low_price_unresolved). With one, the
// class is low_price when unitPriceMinor is below the threshold (strictly
// when strict_below), else standard; resolved is true.
//
// unitPriceMinor is the price BEFORE the promo code and without the service
// charge (policy sections 5 and 12.6): a promo code on a 12.00 ticket does
// not move it into the low-price class.
func ClassifyLowPrice(terms Terms, saleCurrency string, unitPriceMinor int64) (class string, resolved bool) {
	r, ok := lowPriceRule(terms, saleCurrency)
	if !ok {
		return PricingClassStandard, false
	}
	lp := r.LowPriceFormula
	below := unitPriceMinor <= lp.ThresholdMinor
	if lp.StrictBelow {
		below = unitPriceMinor < lp.ThresholdMinor
	}
	if below {
		return PricingClassLowPrice, true
	}
	return PricingClassStandard, true
}

// PackTicketRate is the per-ticket rate of a credit pack (policy section 3):
// its unseated rate, plus its seated surcharge for a seated ticket, in the
// currency of the pack's price list. Spending packs is block B3.
func PackTicketRate(pack gen.PriceListPackRow, seated bool) int64 {
	if seated {
		return pack.UnseatedRateMinor + pack.SeatedSurchargeMinor
	}
	return pack.UnseatedRateMinor
}

func lowPriceRule(terms Terms, saleCurrency string) (Rule, bool) {
	if saleCurrency == "" {
		return Rule{}, false
	}
	return terms.rule(lineStandard, string(KindTicketSold), RuleLowPriceFormula, saleCurrency)
}

func setRule(c *Charge, r Rule) {
	id := r.ID
	c.RuleID = &id
	c.RuleType = r.Type
}

// basisPoints is floor(amount × bp / 10000) for non-negative operands,
// refusing a product that would overflow int64.
func basisPoints(amount, bp int64) (int64, error) {
	if bp != 0 && amount > math.MaxInt64/bp {
		return 0, fmt.Errorf("%w: %d × %d basis points overflows", ErrInconsistentEvent, amount, bp)
	}
	return amount * bp / maxBasisPoints, nil
}

func boolInput(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
