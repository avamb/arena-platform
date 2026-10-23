// Package e2eseed writes a small, fixed set of billing facts for the live
// twin test of the organizer's billing usage screen (task B2-7,
// apps/event-center/tests/smoke/live/billing.spec.ts). It exists as a billing
// package because only code under internal/platform/billing may insert into
// the append-only billing tables (tests/staticanalysis/billing_append_only_test.go);
// arena-e2e-seed calls it and nothing else does.
//
// Every row has a fixed id derived from a name, so the call is idempotent: a
// second run finds the rows and writes nothing. The charges are the real
// shape the rate job writes (one direct charge per event, a reversal as a
// paired negative row without its charge_reversals journal row), with plain
// amounts so the spec can assert them.
package e2eseed

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing"
)

// Params names the organization and the two events the charges belong to.
type Params struct {
	OrgID   uuid.UUID
	EventA  uuid.UUID
	EventB  uuid.UUID
	Session uuid.UUID
	// MonthStart is the first moment of the UTC month the current-month
	// charges fall in; one more charge sits a day before it.
	MonthStart time.Time
	// Country routes the billing account (the venue country).
	Country string
}

// FeeMinor is the fee of one sale in the fixture, in minor units.
const FeeMinor int64 = 59

var namespace = uuid.MustParse("fe0000e2-0000-7000-8000-00000000b111")

func subject(name string) uuid.UUID { return uuid.NewSHA1(namespace, []byte(name)) }

type sale struct {
	name     string
	event    uuid.UUID
	at       time.Time
	reversed bool
}

// SeedUsage writes: three sales of event A and one of event B in the current
// month (one of A reversed), and one sale of A the day before the month.
func SeedUsage(ctx context.Context, tx pgx.Tx, p Params) error {
	q := gen.New(tx)
	acc, err := billing.EnsureAccount(ctx, q, p.OrgID, p.Country)
	if err != nil {
		return fmt.Errorf("billing e2e seed: account: %w", err)
	}
	sales := []sale{
		{"a-1", p.EventA, p.MonthStart.Add(1 * time.Minute), false},
		{"a-2", p.EventA, p.MonthStart.Add(2 * time.Minute), true},
		{"a-3", p.EventA, p.MonthStart.Add(3 * time.Minute), false},
		{"b-1", p.EventB, p.MonthStart.Add(4 * time.Minute), false},
		{"a-old", p.EventA, p.MonthStart.Add(-24 * time.Hour), false},
	}
	cur, unit, seated := "EUR", int64(1500), false
	for _, s := range sales {
		ev := p.EventA
		if s.event == p.EventB {
			ev = p.EventB
		}
		session := p.Session
		id, ok, err := q.InsertBillableEvent(ctx, gen.InsertBillableEventParams{
			Kind: "ticket_sold", SubjectID: subject(s.name), OccurredAt: s.at, OrgID: p.OrgID, AccountID: acc.ID,
			EventID: &ev, SessionID: &session, Seated: &seated, Currency: &cur, UnitPriceMinor: &unit, Source: "collect",
		})
		if err != nil {
			return fmt.Errorf("billing e2e seed: event %s: %w", s.name, err)
		}
		if !ok {
			row, err := q.GetBillableEvent(ctx, "ticket_sold", subject(s.name))
			if err != nil {
				return fmt.Errorf("billing e2e seed: read event %s: %w", s.name, err)
			}
			id = row.ID
		}
		direct, err := q.GetChargeByEvent(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			direct, err = q.InsertCharge(ctx, gen.InsertChargeParams{
				BillableEventID: id, AccountID: acc.ID, Currency: acc.Currency, AmountMinor: FeeMinor, RuleType: "fixed_per_ticket",
			})
		}
		if err != nil {
			return fmt.Errorf("billing e2e seed: charge %s: %w", s.name, err)
		}
		if !s.reversed {
			continue
		}
		// The paired negative row of a cancelled session. The journal row of
		// charge_reversals is left out: the read API does not use it.
		var has bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM charges WHERE reversal_of = $1)`, direct.ID).Scan(&has); err != nil {
			return fmt.Errorf("billing e2e seed: reversal lookup %s: %w", s.name, err)
		}
		if has {
			continue
		}
		if _, err := q.InsertCharge(ctx, gen.InsertChargeParams{
			BillableEventID: id, AccountID: acc.ID, Currency: acc.Currency, AmountMinor: -FeeMinor,
			RuleType: "fixed_per_ticket", ReversalOf: &direct.ID,
		}); err != nil {
			return fmt.Errorf("billing e2e seed: reversal %s: %w", s.name, err)
		}
	}
	return nil
}
