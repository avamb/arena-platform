//go:build integration

// white_label_integration_test.go — white label of a brand (spec
// 08_architecture/52): the superadmin routes, which events and organizations
// it covers, and the monthly charge from the period to the charge row.
//
// Run against a scratch database (AGENTS.md CI-Integration recipe):
//
//	DATABASE_URL=... go test -tags integration -count=1 -v \
//	    ./apps/backend/internal/platform/httpserver/ -run TestWhiteLabel_
package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing/rate"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing/whitelabelcharge"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/whitelabel"
)

type whiteLabelFixture struct {
	*billingAdminEnv
	promoter    uuid.UUID
	ownEvent    uuid.UUID // an event of the organization without a promoter
	promoted    uuid.UUID // an event of the promoter
	otherOrg    uuid.UUID
	otherPromot uuid.UUID
}

func newWhiteLabelFixture(t *testing.T) *whiteLabelFixture {
	t.Helper()
	f := &whiteLabelFixture{billingAdminEnv: newBillingAdminEnv(t)}
	ctx := context.Background()
	pool := f.srv.pgxPool
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, q, args...); err != nil {
			t.Fatalf("fixture %q: %v", q, err)
		}
	}
	sfx := uuid.NewString()[:8]
	f.promoter, f.ownEvent, f.promoted = uuid.New(), uuid.New(), uuid.New()
	exec(`INSERT INTO org_promoters (id, org_id, name) VALUES ($1, $2, $3)`, f.promoter, f.orgID, "WL Promoter "+sfx)
	exec(`INSERT INTO events (id, org_id, name, status, visibility) VALUES ($1, $2, $3, 'published', 'public')`, f.ownEvent, f.orgID, "WL Own "+sfx)
	exec(`INSERT INTO events (id, org_id, name, status, visibility) VALUES ($1, $2, $3, 'published', 'public')`, f.promoted, f.orgID, "WL Promoted "+sfx)
	exec(`INSERT INTO event_promoters (event_id, org_id, promoter_id) VALUES ($1, $2, $3)`, f.promoted, f.orgID, f.promoter)
	f.otherOrg = f.org(t, "WLX")
	f.otherPromot = uuid.New()
	exec(`INSERT INTO org_promoters (id, org_id, name) VALUES ($1, $2, $3)`, f.otherPromot, f.otherOrg, "WL Foreign "+sfx)
	t.Cleanup(func() {
		for _, stmt := range []string{
			`DELETE FROM charge_allocations WHERE charge_id IN (SELECT c.id FROM charges c JOIN billable_events be ON be.id = c.billable_event_id WHERE be.org_id = ANY($1))`,
			`DELETE FROM charges WHERE billable_event_id IN (SELECT id FROM billable_events WHERE org_id = ANY($1))`,
			`DELETE FROM billable_events WHERE org_id = ANY($1)`,
			`DELETE FROM white_label_periods WHERE org_id = ANY($1)`,
			`DELETE FROM event_promoters WHERE org_id = ANY($1)`,
			`DELETE FROM events WHERE org_id = ANY($1)`,
			`DELETE FROM org_promoters WHERE org_id = ANY($1)`,
			`DELETE FROM billing_accounts WHERE org_id = ANY($1)`,
		} {
			if _, err := pool.Exec(context.Background(), stmt, []uuid.UUID{f.orgID, f.otherOrg}); err != nil {
				t.Logf("cleanup %q: %v", stmt, err)
			}
		}
	})
	return f
}

// period inserts a period straight into the table, as one that started in
// the past (the route only accepts today or later).
func (f *whiteLabelFixture) period(t *testing.T, promoter *uuid.UUID, starts time.Time, ends *time.Time, price *int64) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := f.srv.pgxPool.QueryRow(context.Background(), `
INSERT INTO white_label_periods (org_id, promoter_id, starts_on, ends_on, monthly_price_minor, reason)
VALUES ($1, $2, $3, $4, $5, 'wl fixture') RETURNING id`, f.orgID, promoter, starts, ends, price).Scan(&id); err != nil {
		t.Fatalf("insert period: %v", err)
	}
	return id
}

func utcDay(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

func TestWhiteLabel_OnlyASuperadminTurnsItOn(t *testing.T) {
	f := newWhiteLabelFixture(t)
	today := time.Now().UTC().Format("2006-01-02")
	yesterday := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")
	org := fmt.Sprintf(`{"org_id":%q,"starts_on":%q}`, f.orgID, today)

	// The owner reads, never writes.
	f.expect(t, http.StatusForbidden, http.MethodPost, "/v1/admin/billing/white-label", f.owner, "wl", org)
	// A start in the past, and a promoter of another organization, are refused.
	f.expect(t, http.StatusUnprocessableEntity, http.MethodPost, "/v1/admin/billing/white-label", f.super, "wl",
		fmt.Sprintf(`{"org_id":%q,"starts_on":%q}`, f.orgID, yesterday))
	f.expect(t, http.StatusNotFound, http.MethodPost, "/v1/admin/billing/white-label", f.super, "wl",
		fmt.Sprintf(`{"org_id":%q,"promoter_id":%q,"starts_on":%q}`, f.orgID, f.otherPromot, today))

	created := f.expect(t, http.StatusCreated, http.MethodPost, "/v1/admin/billing/white-label", f.super, "wl on", org)
	id := jsonField(t, created, "id")
	if jsonField(t, created, "status") != "in_force" || jsonField(t, created, "reason") != "wl on" {
		t.Fatalf("created: %s", created)
	}
	// The same brand again: two periods never share a day.
	body := f.expect(t, http.StatusConflict, http.MethodPost, "/v1/admin/billing/white-label", f.super, "wl", org)
	if c := billingErrCode(body); c != "billing.white_label_overlap" {
		t.Errorf("overlap code %q", c)
	}
	// The promoter is a brand of its own.
	f.expect(t, http.StatusCreated, http.MethodPost, "/v1/admin/billing/white-label", f.super, "wl promoter",
		fmt.Sprintf(`{"org_id":%q,"promoter_id":%q,"starts_on":%q,"monthly_price_minor":0}`, f.orgID, f.promoter, today))

	// Ending: yesterday ends it now; a later end is refused afterwards.
	ended := f.expect(t, http.StatusOK, http.MethodPost, "/v1/admin/billing/white-label/"+id+"/end", f.super, "wl off",
		fmt.Sprintf(`{"ends_on":%q}`, yesterday))
	if jsonField(t, ended, "status") != "ended" {
		t.Errorf("ended: %s", ended)
	}
	f.expect(t, http.StatusUnprocessableEntity, http.MethodPost, "/v1/admin/billing/white-label/"+id+"/end", f.super, "wl",
		fmt.Sprintf(`{"ends_on":%q}`, today))
	f.expect(t, http.StatusForbidden, http.MethodPost, "/v1/admin/billing/white-label/"+id+"/end", f.owner, "wl",
		fmt.Sprintf(`{"ends_on":%q}`, yesterday))

	// The owner's read-only view names both brands with the price that applies.
	view := f.expect(t, http.StatusOK, http.MethodGet, "/v1/organizations/"+f.orgID.String()+"/billing/white-label", f.owner, "", "")
	var got struct {
		Items []struct {
			BrandName string `json:"brand_name"`
			Effective *int64 `json:"effective_monthly_minor"`
			Status    string `json:"status"`
			Reason    *string
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(view), &got); err != nil {
		t.Fatalf("decode %s: %v", view, err)
	}
	if len(got.Items) != 2 {
		t.Fatalf("owner view: %s", view)
	}
	for _, it := range got.Items {
		if it.Reason != nil {
			t.Errorf("the owner sees the operator's reason: %s", view)
		}
	}
	// Another organization's owner sees nothing of it.
	otherOwner := f.user(t, "wlxowner")
	f.member(t, otherOwner, f.otherOrg, "org_admin")
	f.expect(t, http.StatusForbidden, http.MethodGet, "/v1/organizations/"+f.orgID.String()+"/billing/white-label", f.token(t, otherOwner), "", "")
}

func TestWhiteLabel_CoversOnlyItsBrandAndItsDays(t *testing.T) {
	f := newWhiteLabelFixture(t)
	ctx := context.Background()
	r := whitelabel.New(gen.New(f.srv.pgxPool))
	flags := func() map[uuid.UUID]bool {
		t.Helper()
		m, err := r.Events(ctx, []uuid.UUID{f.ownEvent, f.promoted})
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	if m := flags(); m[f.ownEvent] || m[f.promoted] {
		t.Fatalf("no period yet: %v", m)
	}
	// The promoter's brand covers only the promoter's event.
	f.period(t, &f.promoter, utcDay(2026, 1, 1), nil, nil)
	if m := flags(); m[f.ownEvent] || !m[f.promoted] {
		t.Errorf("promoter brand: %v", m)
	}
	if on, _ := r.Org(ctx, f.orgID); on {
		t.Error("a promoter's white label switched the organization's shop")
	}
	// An organization period that ended yesterday covers nothing.
	yesterday := time.Now().UTC().AddDate(0, 0, -1)
	f.period(t, nil, utcDay(2026, 1, 1), &yesterday, nil)
	if m := flags(); m[f.ownEvent] {
		t.Errorf("ended period still on: %v", m)
	}
	// A period that starts tomorrow is not on yet.
	tomorrow := time.Now().UTC().AddDate(0, 0, 1)
	f.period(t, nil, tomorrow, nil, nil)
	if m := flags(); m[f.ownEvent] {
		t.Errorf("future period already on: %v", m)
	}
	if on, _ := r.WithClock(func() time.Time { return tomorrow }).Org(ctx, f.orgID); !on {
		t.Error("tomorrow the organization should have white label")
	}
}

type orgScopedLister struct {
	q   *gen.Queries
	org uuid.UUID
}

func (l orgScopedLister) ListUnratedBillableEventsAfter(ctx context.Context, at time.Time, id uuid.UUID, limit int32) ([]gen.BillableEventRow, error) {
	rows, err := l.q.ListUnratedBillableEventsAfter(ctx, at, id, limit)
	if err != nil {
		return nil, err
	}
	out := rows[:0]
	for _, r := range rows {
		if r.OrgID == l.org {
			out = append(out, r)
		}
	}
	return out, nil
}

func TestWhiteLabel_ChargedOncePerMonthByDays(t *testing.T) {
	f := newWhiteLabelFixture(t)
	ctx := context.Background()
	f.account(t)
	free := int64(0)
	// The organization from 16 October, the promoter free from 1 October.
	orgPeriod := f.period(t, nil, utcDay(2026, 10, 16), nil, nil)
	f.period(t, &f.promoter, utcDay(2026, 10, 1), nil, &free)

	charger := whitelabelcharge.New(f.q, whitelabelcharge.Options{
		Since: utcDay(2026, 9, 1), OrgID: &f.orgID,
		Now: func() time.Time { return time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC) },
	})
	n, err := charger.Run(ctx)
	if err != nil || n != 2 {
		t.Fatalf("first pass: %d %v, want 2 events (October, two brands)", n, err)
	}
	if n, err := charger.Run(ctx); err != nil || n != 0 {
		t.Fatalf("second pass: %d %v, want nothing new", n, err)
	}

	rater := rate.New(f.srv.pgxPool, rate.Options{Since: utcDay(2026, 9, 1), Lister: orgScopedLister{q: f.q, org: f.orgID}})
	if _, err := rater.Run(ctx); err != nil {
		t.Fatalf("rate: %v", err)
	}
	rows, err := f.srv.pgxPool.Query(ctx, `
SELECT be.subject_id, c.amount_minor, c.currency, c.rule_type
FROM charges c JOIN billable_events be ON be.id = c.billable_event_id
WHERE be.org_id = $1 AND be.kind = 'white_label_month'`, f.orgID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[uuid.UUID]int64{}
	for rows.Next() {
		var subject uuid.UUID
		var amount int64
		var cur, rule string
		if err := rows.Scan(&subject, &amount, &cur, &rule); err != nil {
			t.Fatal(err)
		}
		if cur != "EUR" || rule != string(billing.RuleSubscription) {
			t.Errorf("charge %s %s", cur, rule)
		}
		got[subject] = amount
	}
	if len(got) != 2 {
		t.Fatalf("charges: %v", got)
	}
	// 45.00 x 16 / 31 days of October, floored.
	if want := int64(4500 * 16 / 31); got[whitelabelcharge.SubjectID(orgPeriod, utcDay(2026, 10, 1))] != want {
		t.Errorf("organization October: %v, want %d", got, want)
	}
	for subject, amount := range got {
		if subject != whitelabelcharge.SubjectID(orgPeriod, utcDay(2026, 10, 1)) && amount != 0 {
			t.Errorf("the free promoter was charged %d", amount)
		}
	}
	// November is not over: nothing is charged for it yet.
	var nov int
	if err := f.srv.pgxPool.QueryRow(ctx, `SELECT count(*) FROM billable_events WHERE org_id = $1 AND kind = 'white_label_month' AND occurred_at >= '2026-11-01'`, f.orgID).Scan(&nov); err != nil || nov != 0 {
		t.Errorf("November events: %d %v", nov, err)
	}
}
