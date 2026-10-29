// Package rate is the billing.rate job (billing wave B-A, task B2-3, spec 41
// sections 6.3 and 8.1): it turns the billable events the collect job wrote
// into charges, with the engine of package billing, and writes the paired
// reversal of a cancelled session.
//
// An event is rated by its own date: the account's terms, the price-list
// version and the individual rate are the ones in force at the event's
// occurred_at, never at the time the job runs. Every event, even one that
// costs nothing, gets exactly one DIRECT charge (a zero amount with a reason),
// so "rated" is "has a direct charge" and the append-only event row needs no
// marker. A refund and a cancelled ticket are journal-only: the charge of the
// sold ticket stays (owner decision B-6).
//
// The only reversal is a cancelled session: its session_cancelled event writes,
// for every direct charge of the session's ticket_sold events that has none,
// a PAIRED charge (negative amount, reversal_of) and a charge_reversals entry.
// Whatever the money does next - bonus credits, Settler - is block B3.
//
// Each event is rated in its own transaction under an advisory lock on its
// session (else the event), so two passes, or a sale and the cancellation of
// its session, never interleave. An event the job cannot rate yet stays
// unrated and is tried again: a deferred one (no exchange rate), one without
// usable terms, one whose rule the engine cannot compute, a cancellation that
// waits for the unrated sales of its session. A database failure stops the
// pass.
package rate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing"
)

const (
	// DefaultBatchSize is the page size of the unrated-events query.
	DefaultBatchSize = 500
	// maxPages bounds how many pages one pass reads.
	maxPages = 40

	// ReasonSessionCancelled labels a reversal charge and its journal entry.
	ReasonSessionCancelled = "session_cancelled"
)

// Settler is what block B3 plugs in: it spends credits against a new charge
// and gives them back for a reversal. B2 ships NopSettler, which does nothing.
// Both methods run inside the transaction that writes the charge, on queries
// bound to it, so a failure rolls the charge back with it. The transaction
// already holds the advisory lock of the event's session: an implementation
// that takes more locks (on credit lots) must take them AFTER it, never
// before, so the order stays session, then lots.
type Settler interface {
	// Settle is called after a DIRECT charge with a positive amount.
	Settle(ctx context.Context, q *gen.Queries, ev gen.BillableEventRow, charge gen.ChargeRow) error
	// SettleReversal is called after the paired reversal of an original
	// charge, caused by the cancellation event cause.
	SettleReversal(ctx context.Context, q *gen.Queries, cause gen.BillableEventRow, original, reversal gen.ChargeRow) error
}

// NopSettler is the B2 settler: nothing is spent, nothing is given back.
type NopSettler struct{}

// Settle implements Settler.
func (NopSettler) Settle(context.Context, *gen.Queries, gen.BillableEventRow, gen.ChargeRow) error {
	return nil
}

// SettleReversal implements Settler.
func (NopSettler) SettleReversal(context.Context, *gen.Queries, gen.BillableEventRow, gen.ChargeRow, gen.ChargeRow) error {
	return nil
}

// Lister reads the events waiting for a charge, a keyset page after
// (afterAt, afterID). *gen.Queries satisfies it.
type Lister interface {
	ListUnratedBillableEventsAfter(ctx context.Context, afterAt time.Time, afterID uuid.UUID, limit int32) ([]gen.BillableEventRow, error)
}

// Beginner opens the transaction one event is rated in. *pgxpool.Pool
// satisfies it.
type Beginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Options configures a Rater.
type Options struct {
	// Since bounds the events looked at (the date X); the zero value reads
	// from the beginning.
	Since     time.Time
	BatchSize int32
	// Lister overrides the unrated-events reader (tests scope it to their
	// own organization); nil reads through the pool.
	Lister  Lister
	Settler Settler
	Logger  *slog.Logger
}

// Rater runs the rate passes.
type Rater struct {
	pool Beginner
	list Lister
	opts Options
}

// New builds a Rater over pool, which must also satisfy gen.DBTX.
func New(pool interface {
	Beginner
	gen.DBTX
}, opts Options) *Rater {
	if opts.BatchSize <= 0 {
		opts.BatchSize = DefaultBatchSize
	}
	if opts.Settler == nil {
		opts.Settler = NopSettler{}
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	list := opts.Lister
	if list == nil {
		list = gen.New(pool)
	}
	return &Rater{pool: pool, list: list, opts: opts}
}

// Result is what one pass did.
type Result struct {
	Rated     int // direct charges written
	Reversed  int // reversal charges written
	Skipped   int // events left for a later pass
	SkipNotes []string
}

// Run makes one pass: it rates the events that have no direct charge, oldest
// first, and returns what it did. A skipped event is not an error; a database
// failure is.
func (r *Rater) Run(ctx context.Context) (Result, error) {
	var res Result
	since := r.opts.Since
	if since.IsZero() {
		since = billing.InitialTermsEffectiveFrom
	}
	afterAt, afterID := since, uuid.Nil
	for page := 0; page < maxPages; page++ {
		events, err := r.list.ListUnratedBillableEventsAfter(ctx, afterAt, afterID, r.opts.BatchSize)
		if err != nil {
			return res, fmt.Errorf("billing rate: list unrated events: %w", err)
		}
		for _, ev := range events {
			afterAt, afterID = ev.OccurredAt, ev.ID
			out, err := r.rateOne(ctx, ev)
			if err != nil {
				return res, fmt.Errorf("billing rate: event %s %s: %w", ev.Kind, ev.ID, err)
			}
			res.Rated += out.rated
			res.Reversed += out.reversed
			if out.skip != "" {
				res.Skipped++
				if len(res.SkipNotes) < 20 {
					res.SkipNotes = append(res.SkipNotes, fmt.Sprintf("%s %s: %s", ev.Kind, ev.ID, out.skip))
				}
			}
		}
		if len(events) < int(r.opts.BatchSize) {
			break
		}
	}
	return res, nil
}

type outcome struct {
	rated, reversed int
	skip            string
}

// dataError tells "this event cannot be rated now" from a database failure.
func dataError(err error) bool {
	for _, target := range []error{
		billing.ErrNoTerms, billing.ErrNoPriceList, billing.ErrBadRule,
		billing.ErrBadTerms, billing.ErrInconsistentEvent, billing.ErrNoRule, billing.ErrUnknownEventKind,
		billing.ErrRuleNotImplemented,
	} {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}

// price turns an event into a charge. skip is non-empty when the event cannot
// be rated now. A journal-only event (a refund, a cancelled ticket, a session
// cancellation) costs nothing whatever the terms say, so unusable terms do
// not stop it: otherwise a cancellation could never reverse the sales it
// cancels.
func (r *Rater) price(ctx context.Context, q *gen.Queries, ev gen.BillableEventRow) (billing.Charge, string, error) {
	terms, err := billing.TermsAt(ctx, q, ev.AccountID, ev.OccurredAt)
	if err != nil {
		if !dataError(err) {
			return billing.Charge{}, "", err
		}
		if journalOnly(ev.Kind) {
			acc, aerr := q.GetBillingAccountByID(ctx, ev.AccountID)
			if aerr != nil {
				return billing.Charge{}, "", aerr
			}
			return billing.Charge{Currency: acc.Currency, Reason: billing.ReasonJournalOnly, Inputs: map[string]int64{}}, "", nil
		}
		return billing.Charge{}, err.Error(), nil
	}
	engineEv := toEngineEvent(ev)
	if over, err := overFreeLimit(ctx, q, terms, ev, engineEv); err != nil {
		return billing.Charge{}, "", err
	} else if over {
		engineEv.FreeTicketsOverLimit = true
	}
	charge, err := billing.Rate(terms, engineEv)
	if err != nil {
		if dataError(err) {
			return billing.Charge{}, err.Error(), nil
		}
		return billing.Charge{}, "", err
	}
	if charge.Deferred != "" {
		return billing.Charge{}, "deferred: " + charge.Deferred, nil
	}
	if charge.AmountMinor < 0 {
		return billing.Charge{}, fmt.Sprintf("the engine computed a negative charge (%d): the rule data is wrong", charge.AmountMinor), nil
	}
	return charge, "", nil
}

// overFreeLimit reports whether a complimentary ticket that would be free
// is beyond the yearly free limit of its terms (spec 46 section 3.6, rule 3):
// the account's complimentary tickets rated free earlier in the same
// calendar year (UTC) have already reached the limit. A Bulk ticket, an
// account billing invitations at the standard rate, and a price list without
// a limit never count. The count runs under a per-account lock taken after
// the session lock, so two passes never both take the last free place. An
// earlier complimentary ticket rated only later (it waited for its terms)
// does not move a decision already written: charges are never re-rated.
func overFreeLimit(ctx context.Context, q *gen.Queries, terms billing.Terms, row gen.BillableEventRow, ev billing.Event) (bool, error) {
	if ev.Kind != billing.KindTicketComplimentary || ev.Bulk ||
		terms.ComplimentaryBilling() != billing.ComplimentaryBillingFree {
		return false, nil
	}
	limit := terms.FreeTicketsAnnualLimit()
	if limit == nil {
		return false, nil
	}
	if err := q.LockComplimentaryFreeLimit(ctx, row.AccountID); err != nil {
		return false, fmt.Errorf("lock free limit: %w", err)
	}
	used, err := q.CountFreeComplimentaryChargesBefore(ctx, row.AccountID,
		billing.FreeLimitYearStart(row.OccurredAt), row.OccurredAt, row.ID)
	if err != nil {
		return false, fmt.Errorf("count free complimentary tickets: %w", err)
	}
	return used >= *limit, nil
}

func journalOnly(kind string) bool {
	switch billing.EventKind(kind) {
	case billing.KindTicketRefunded, billing.KindTicketCancelled, billing.KindSessionCancelled:
		return true
	}
	return false
}

func (r *Rater) rateOne(ctx context.Context, ev gen.BillableEventRow) (out outcome, err error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	q := gen.New(tx)

	// One lock per session (else per event) serializes the rating of a sale
	// with the cancellation of its session and with a second pass.
	lockKey := ev.ID.String()
	if ev.SessionID != nil {
		lockKey = ev.SessionID.String()
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "billing.rate:"+lockKey); err != nil {
		return out, fmt.Errorf("lock: %w", err)
	}
	if _, err := q.GetChargeByEvent(ctx, ev.ID); err == nil {
		return out, nil // rated by another pass while this one waited
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}

	if ev.Kind == string(billing.KindSessionCancelled) {
		if ev.SessionID == nil {
			return outcome{skip: "a session_cancelled event without a session"}, nil
		}
		waiting, err := q.CountUnratedBillableEventsForSessionUntil(ctx, *ev.SessionID, string(billing.KindTicketSold), ev.OccurredAt)
		if err != nil {
			return out, err
		}
		if waiting > 0 {
			return outcome{skip: fmt.Sprintf("waits for %d unrated ticket_sold events of the session", waiting)}, nil
		}
	}

	charge, skip, err := r.price(ctx, q, ev)
	if err != nil {
		return out, err
	}
	if skip != "" {
		return outcome{skip: skip}, nil
	}

	row, err := q.InsertCharge(ctx, chargeParams(ev, charge))
	if err != nil {
		return out, fmt.Errorf("insert charge: %w", err)
	}
	out.rated = 1
	if row.AmountMinor > 0 {
		if err := r.opts.Settler.Settle(ctx, q, ev, row); err != nil {
			return out, fmt.Errorf("settle: %w", err)
		}
	}

	switch ev.Kind {
	case string(billing.KindSessionCancelled):
		n, err := r.reverseSession(ctx, q, ev)
		if err != nil {
			return out, err
		}
		out.reversed = n
	case string(billing.KindTicketSold):
		n, err := r.reverseIfSessionAlreadyCancelled(ctx, q, ev, row)
		if err != nil {
			return out, err
		}
		out.reversed = n
	}
	if err := tx.Commit(ctx); err != nil {
		return outcome{}, err
	}
	committed = true
	return out, nil
}

// reverseSession writes the paired reversal of every direct charge of the
// session's ticket_sold events that has none. A charge of zero (a free ticket)
// has nothing to take back and is left alone.
func (r *Rater) reverseSession(ctx context.Context, q *gen.Queries, cause gen.BillableEventRow) (int, error) {
	charges, err := q.ListReversibleChargesForSessionUntil(ctx, *cause.SessionID, cause.OccurredAt)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, orig := range charges {
		if orig.AmountMinor <= 0 {
			continue
		}
		if err := r.writeReversal(ctx, q, cause, orig); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// reverseIfSessionAlreadyCancelled covers a sale that was collected after the
// cancellation of its session had been processed (a sale paid shortly before
// the cancellation but read late): its charge is reversed at once.
func (r *Rater) reverseIfSessionAlreadyCancelled(ctx context.Context, q *gen.Queries, sale gen.BillableEventRow, charge gen.ChargeRow) (int, error) {
	if sale.SessionID == nil || charge.AmountMinor <= 0 {
		return 0, nil
	}
	cancels, err := q.ListBillableEventsForSession(ctx, *sale.SessionID, string(billing.KindSessionCancelled))
	if err != nil {
		return 0, err
	}
	for _, c := range cancels {
		if c.OccurredAt.Before(sale.OccurredAt) {
			continue // the cancellation came before the sale
		}
		if _, err := q.GetChargeByEvent(ctx, c.ID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				continue // not processed yet: it will reverse this charge itself
			}
			return 0, err
		}
		if err := r.writeReversal(ctx, q, c, charge); err != nil {
			return 0, err
		}
		return 1, nil
	}
	return 0, nil
}

func (r *Rater) writeReversal(ctx context.Context, q *gen.Queries, cause gen.BillableEventRow, orig gen.ChargeRow) error {
	inputs, err := json.Marshal(map[string]string{
		"reversal_of":    orig.ID.String(),
		"cause_event_id": cause.ID.String(),
	})
	if err != nil {
		return err
	}
	reason := ReasonSessionCancelled
	rev, err := q.InsertCharge(ctx, gen.InsertChargeParams{
		BillableEventID:  orig.BillableEventID,
		AccountID:        orig.AccountID,
		Currency:         orig.Currency,
		AmountMinor:      -orig.AmountMinor,
		RuleType:         orig.RuleType,
		RuleID:           orig.RuleID,
		PriceListID:      orig.PriceListID,
		PriceListVersion: orig.PriceListVersion,
		OverrideID:       orig.OverrideID,
		Inputs:           inputs,
		Reason:           &reason,
		ReversalOf:       &orig.ID,
	})
	if err != nil {
		return fmt.Errorf("insert reversal of charge %s: %w", orig.ID, err)
	}
	if _, err := q.InsertChargeReversal(ctx, gen.InsertChargeReversalParams{
		ChargeID:         orig.ID,
		ReversalChargeID: rev.ID,
		CauseEventID:     cause.ID,
		Reason:           ReasonSessionCancelled,
	}); err != nil {
		return fmt.Errorf("journal reversal of charge %s: %w", orig.ID, err)
	}
	if err := r.opts.Settler.SettleReversal(ctx, q, cause, orig, rev); err != nil {
		return fmt.Errorf("settle reversal of charge %s: %w", orig.ID, err)
	}
	return nil
}

func toEngineEvent(ev gen.BillableEventRow) billing.Event {
	out := billing.Event{
		Kind:       billing.EventKind(ev.Kind),
		OccurredAt: ev.OccurredAt,
	}
	if ev.Seated != nil {
		out.Seated = *ev.Seated
	}
	if ev.Currency != nil {
		out.Currency = strings.TrimSpace(*ev.Currency)
	}
	if ev.UnitPriceMinor != nil {
		out.UnitPriceMinor = *ev.UnitPriceMinor
	}
	if ev.DiscountMinor != nil {
		out.DiscountMinor = *ev.DiscountMinor
	}
	if ev.ServiceChargeMinor != nil {
		out.ServiceChargeMinor = *ev.ServiceChargeMinor
	}
	if ev.PricingClass != nil {
		out.PricingClass = *ev.PricingClass
	}
	out.Bulk = billing.BulkFromAttributes(ev.Attributes)
	return out
}

func chargeParams(ev gen.BillableEventRow, c billing.Charge) gen.InsertChargeParams {
	inputs, _ := json.Marshal(c.Inputs) // a map of int64 cannot fail
	p := gen.InsertChargeParams{
		BillableEventID: ev.ID,
		AccountID:       ev.AccountID,
		Currency:        c.Currency,
		AmountMinor:     c.AmountMinor,
		RuleType:        ruleTypeOrNone(c.RuleType),
		RuleID:          c.RuleID,
		OverrideID:      c.OverrideID,
		Inputs:          inputs,
	}
	if c.PriceListID != uuid.Nil {
		pl, ver := c.PriceListID, c.PriceListVersion
		p.PriceListID, p.PriceListVersion = &pl, &ver
	}
	if c.Reason != "" {
		reason := c.Reason
		p.Reason = &reason
	}
	return p
}

// ruleTypeOrNone names the rule of a charge that no rule produced (free,
// journal-only): charges.rule_type is NOT NULL.
func ruleTypeOrNone(t billing.RuleType) string {
	if t == "" {
		return "none"
	}
	return string(t)
}
