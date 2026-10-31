//go:build integration

// collect_invitations_integration_test.go — billing of staff invitations
// (spec 46 section 3.6, task INV-07) through the real collector AND the real
// rate job, against a live database. Run it on a FRESH migrated database.
package collect_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing/rate"
)

// orgLister narrows the rate job to the fixture's organization.
type orgLister struct {
	*gen.Queries
	org uuid.UUID
}

func (s orgLister) ListUnratedBillableEventsAfter(ctx context.Context, at time.Time, id uuid.UUID, limit int32) ([]gen.BillableEventRow, error) {
	rows, err := s.Queries.ListUnratedBillableEventsAfter(ctx, at, id, 100000)
	var out []gen.BillableEventRow
	for _, r := range rows {
		if r.OrgID == s.org && int32(len(out)) < limit {
			out = append(out, r)
		}
	}
	return out, err
}

// invitation writes a complimentary order the way the staff issue route does
// (source complimentary, total 0, paid) with issued_via set.
func (f *fixture) invitation(issuedVia string, paidAt time.Time, lines ...line) (uuid.UUID, []uuid.UUID, []uuid.UUID) {
	f.t.Helper()
	orderID, items, tickets := f.paidOrder("complimentary", "EUR", paidAt, lines...)
	f.must(`UPDATE orders SET issued_via = $2 WHERE id = $1`, orderID, issuedVia)
	return orderID, items, tickets
}

func (f *fixture) rateAll() {
	f.t.Helper()
	r := rate.New(f.pool, rate.Options{Since: billing.InitialTermsEffectiveFrom, Lister: orgLister{gen.New(f.pool), f.orgID}})
	res, err := r.Run(context.Background())
	if err != nil {
		f.t.Fatalf("rate pass: %v", err)
	}
	if res.Skipped != 0 {
		f.t.Fatalf("rate pass skipped %d events: %v", res.Skipped, res.SkipNotes)
	}
}

type chargeOf struct {
	amount   int64
	reason   *string
	reversed int
}

// charge returns the direct charge of the billable event of (kind, subject).
func (f *fixture) charge(kind string, subject uuid.UUID) chargeOf {
	f.t.Helper()
	var c chargeOf
	err := f.pool.QueryRow(context.Background(), `
		SELECT c.amount_minor, c.reason,
		       (SELECT count(*) FROM charges r WHERE r.reversal_of = c.id)
		FROM   billable_events e JOIN charges c ON c.billable_event_id = e.id AND c.reversal_of IS NULL
		WHERE  e.kind = $1 AND e.subject_id = $2`, kind, subject).Scan(&c.amount, &c.reason, &c.reversed)
	if err != nil {
		f.t.Fatalf("charge of %s %s: %v", kind, subject, err)
	}
	return c
}

func (f *fixture) account() gen.BillingAccountRow {
	f.t.Helper()
	acc, err := gen.New(f.pool).GetBillingAccount(context.Background(), f.orgID, "EUR", billing.AccountKindOrganizer)
	if err != nil {
		f.t.Fatalf("account: %v", err)
	}
	return acc
}

// fixedRates reads the fixed_per_ticket rule the account is rated by at at.
func (f *fixture) fixedRates(accountID uuid.UUID, at time.Time) (unseated, seated int64) {
	f.t.Helper()
	terms, err := billing.TermsAt(context.Background(), gen.New(f.pool), accountID, at)
	if err != nil {
		f.t.Fatalf("terms: %v", err)
	}
	for _, r := range terms.Rules {
		if r.Type == billing.RuleFixedPerTicket && r.Line == "standard" && r.Fixed != nil {
			return r.Fixed.UnseatedMinor, r.Fixed.SeatedMinor
		}
	}
	f.t.Fatal("no fixed_per_ticket on the standard line")
	return 0, 0
}

// A staff invitation is free, a CSV (staff_bulk) invitation pays the fixed
// seated / unseated rate, the switch to complimentary_billing standard
// charges only the invitations issued after it starts, and a cancelled
// invitation keeps its charge: its cancellation is a journal-only event.
func TestCollectRate_StaffInvitations(t *testing.T) {
	f := newFixture(t, "CZ")
	now := time.Now().UTC().Truncate(time.Second)
	_, bulkItems, _ := f.invitation(billing.IssuedViaStaffBulk, now.Add(-3*time.Hour), line{0, true}, line{0, false})
	_, staffItems, _ := f.invitation(billing.IssuedViaStaff, now.Add(-2*time.Hour), line{0, true}, line{0, false})
	_, lateItems, lateTickets := f.invitation(billing.IssuedViaStaff, now.Add(-time.Hour), line{0, true})

	c := f.collector(now.Add(-24 * time.Hour))
	pass(t, c)
	comp := f.events("ticket_complimentary")
	if len(comp) != 5 {
		t.Fatalf("%d ticket_complimentary events, want 5", len(comp))
	}
	via := map[uuid.UUID]any{}
	for _, e := range comp {
		via[e.SubjectID] = e.Attrs["issued_via"]
		if e.Attrs["order_source"] != "complimentary" {
			t.Errorf("event %s attributes %v, want order_source complimentary", e.SubjectID, e.Attrs)
		}
	}
	if via[bulkItems[0]] != "staff_bulk" || via[staffItems[0]] != "staff" || via[lateItems[0]] != "staff" {
		t.Fatalf("issued_via attributes = %v", via)
	}
	if sold := f.events("ticket_sold"); len(sold) != 0 {
		t.Fatalf("%d ticket_sold events from invitations, want 0", len(sold))
	}

	// The superadmin switches the account to standard; it starts between the
	// second and the third invitation.
	acc := f.account()
	f.must(`INSERT INTO billing_account_terms (account_id, effective_from, price_list_code, plan_kind, billing_cycle,
	            shortfall_policy, issuer_id, created_by, reason, complimentary_billing)
	        VALUES ($1, $2, $3, 'standard', 'monthly', 'invoice_postpaid', $4, 'test', 'inv07 test', 'standard')`,
		acc.ID, now.Add(-90*time.Minute), billing.StandardPriceListCode, acc.IssuerID)

	// The third invitation is cancelled before rating: the order stays paid.
	// (cancelled after the first pass, so the next pass's cursor reaches it)
	f.must(`UPDATE tickets SET status = 'cancelled', cancelled_at = $2 WHERE id = $1`, lateTickets[0], time.Now().UTC())
	pass(t, c)
	if got := f.events("ticket_cancelled"); len(got) != 1 || got[0].SubjectID != lateTickets[0] {
		t.Fatalf("ticket_cancelled = %+v, want the cancelled invitation ticket", got)
	}

	f.rateAll()
	unseated, seated := f.fixedRates(acc.ID, now)
	want := []struct {
		name    string
		subject uuid.UUID
		amount  int64
		reason  string
	}{
		{"bulk seated", bulkItems[0], seated, ""},
		{"bulk GA", bulkItems[1], unseated, ""},
		{"staff seated before the switch", staffItems[0], 0, billing.ReasonFree},
		{"staff GA before the switch", staffItems[1], 0, billing.ReasonFree},
		{"staff seated after the switch, cancelled", lateItems[0], seated, ""},
	}
	for _, w := range want {
		got := f.charge("ticket_complimentary", w.subject)
		reason := ""
		if got.reason != nil {
			reason = *got.reason
		}
		if got.amount != w.amount || reason != w.reason || got.reversed != 0 {
			t.Errorf("%s: %d %q reversed=%d, want %d %q, never reversed", w.name, got.amount, reason, got.reversed, w.amount, w.reason)
		}
	}
	if got := f.charge("ticket_cancelled", lateTickets[0]); got.amount != 0 || got.reason == nil || *got.reason != billing.ReasonJournalOnly {
		t.Errorf("cancellation charge = %+v, want journal_only 0", got)
	}
	if seated <= 0 || unseated <= 0 {
		t.Errorf("fixed rates %d / %d: the test proves nothing with a zero rate", unseated, seated)
	}

	// The preview of the issue route gives the same numbers.
	q := gen.New(f.pool)
	quote, err := billing.QuoteComplimentaryForSession(context.Background(), q, f.orgID, f.sessionID, now, 1, 1, false)
	if err != nil {
		t.Fatalf("quote: %v", err)
	}
	if quote.Mode != billing.QuoteModeStandard || quote.AmountMinor != seated+unseated || quote.ChargedTickets != 2 {
		t.Errorf("quote after the switch = %+v, want standard %d", quote, seated+unseated)
	}
	quote, err = billing.QuoteComplimentaryForSession(context.Background(), q, f.orgID, f.sessionID, now.Add(-2*time.Hour), 1, 1, false)
	if err != nil {
		t.Fatalf("quote: %v", err)
	}
	if quote.Mode != billing.QuoteModeFree || quote.AmountMinor != 0 || quote.FreeTickets != 2 {
		t.Errorf("quote before the switch = %+v, want free", quote)
	}
}

// With a yearly free limit of 2, the third free invitation ticket of the year
// is charged as an unseated ticket; a bulk ticket never uses the limit.
func TestCollectRate_StaffInvitationsFreeLimit(t *testing.T) {
	f := newFixture(t, "CZ")
	ctx := context.Background()
	code := "inv07-" + uuid.NewString()[:8]
	t.Cleanup(func() {
		for _, sql := range []string{
			`DELETE FROM charges WHERE account_id IN (SELECT id FROM billing_accounts WHERE org_id = $2)`,
			`DELETE FROM billable_events WHERE org_id = $2`,
			`DELETE FROM billing_account_terms WHERE price_list_code = $1`,
			`DELETE FROM price_list_rules WHERE price_list_id IN (SELECT id FROM price_lists WHERE code = $1)`,
			`DELETE FROM price_list_packs WHERE price_list_id IN (SELECT id FROM price_lists WHERE code = $1)`,
			`DELETE FROM price_lists WHERE code = $1`,
		} {
			if _, err := f.pool.Exec(ctx, sql, code, f.orgID); err != nil {
				t.Logf("cleanup %s: %v", sql, err)
			}
		}
	})
	var plID uuid.UUID
	if err := f.pool.QueryRow(ctx, `
		INSERT INTO price_lists (code, version, currency, effective_from, locked, free_tickets_annual_limit,
		       floor_per_ticket_minor, bonus_share_cap, bonus_covers_surcharge, created_by, reason)
		SELECT $1, 1, currency, '2026-01-01T00:00:00Z', locked, 2, floor_per_ticket_minor,
		       bonus_share_cap, bonus_covers_surcharge, 'test', 'inv07 test'
		FROM   price_lists WHERE code = 'standard-2026' AND currency = 'EUR' AND version = 1
		RETURNING id`, code).Scan(&plID); err != nil {
		t.Fatal(err)
	}
	f.must(`INSERT INTO price_list_rules (price_list_id, rule_type, line, event_kind, sale_currency, params, sort_order)
	        SELECT $1, rule_type, line, event_kind, sale_currency, params, sort_order
	        FROM price_list_rules WHERE price_list_id = (SELECT id FROM price_lists WHERE code = 'standard-2026' AND currency = 'EUR' AND version = 1)`, plID)

	now := time.Now().UTC().Truncate(time.Second)
	_, a, _ := f.invitation(billing.IssuedViaStaff, now.Add(-4*time.Hour), line{0, true})
	_, bulk, _ := f.invitation(billing.IssuedViaStaffBulk, now.Add(-3*time.Hour), line{0, false})
	_, b, _ := f.invitation(billing.IssuedViaStaff, now.Add(-2*time.Hour), line{0, false}, line{0, true})

	c := f.collector(now.Add(-24 * time.Hour))
	pass(t, c)
	acc := f.account()
	f.must(`INSERT INTO billing_account_terms (account_id, effective_from, price_list_code, plan_kind, billing_cycle,
	            shortfall_policy, issuer_id, created_by, reason)
	        VALUES ($1, $2, $3, 'standard', 'monthly', 'invoice_postpaid', $4, 'test', 'inv07 test')`,
		acc.ID, now.Add(-5*time.Hour), code, acc.IssuerID)

	// The preview before rating: nothing used yet, 2 of 2 left.
	quote, err := billing.QuoteComplimentaryForSession(ctx, gen.New(f.pool), f.orgID, f.sessionID, now, 0, 1, false)
	if err != nil || quote.FreeLimit == nil || *quote.FreeLimit != 2 || quote.FreeLeft == nil || *quote.FreeLeft != 1 {
		t.Fatalf("quote before rating = %+v (err %v), want limit 2, 1 left after one ticket", quote, err)
	}

	f.rateAll()
	unseated, seated := f.fixedRates(acc.ID, now)
	for _, w := range []struct {
		name    string
		subject uuid.UUID
		amount  int64
	}{
		{"first free ticket", a[0], 0},
		{"bulk ticket, outside the limit", bulk[0], unseated},
	} {
		if got := f.charge("ticket_complimentary", w.subject); got.amount != w.amount {
			t.Errorf("%s: %d, want %d", w.name, got.amount, w.amount)
		}
	}
	// The last order's two tickets share one instant, so which one is rated
	// first is the id order: exactly one of them is the second free ticket,
	// the other is over the limit and pays the UNSEATED rate even when seated.
	x, y := f.charge("ticket_complimentary", b[0]).amount, f.charge("ticket_complimentary", b[1]).amount
	if x+y != unseated || (x != 0 && y != 0) {
		t.Errorf("last invitation charged %d and %d, want 0 and %d (seated rate %d must not apply)", x, y, unseated, seated)
	}

	// The preview now: the limit is used up, a new ticket is charged.
	quote, err = billing.QuoteComplimentaryForSession(ctx, gen.New(f.pool), f.orgID, f.sessionID, now, 0, 1, false)
	if err != nil || quote.AmountMinor != unseated || quote.FreeLeft == nil || *quote.FreeLeft != 0 {
		raw, _ := json.Marshal(quote)
		t.Fatalf("quote after rating = %s (err %v), want %d charged, 0 left", raw, err, unseated)
	}
}
