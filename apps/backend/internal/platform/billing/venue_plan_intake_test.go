package billing

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// fakeIntakeQueries is an in-memory VenuePlanIntakeQueries: one route
// (IL -> ILS), the default issuer, the given accounts, and the events written.
type fakeIntakeQueries struct {
	country  string
	accounts []gen.BillingAccountRow
	events   []gen.InsertBillableEventParams
}

func (f *fakeIntakeQueries) GetBillingCountryRoute(_ context.Context, country string) (gen.BillingCountryRouteRow, error) {
	if country == "IL" {
		return gen.BillingCountryRouteRow{Country: "IL", Currency: "ILS", IssuerID: uuid.New()}, nil
	}
	return gen.BillingCountryRouteRow{}, pgx.ErrNoRows
}

func (f *fakeIntakeQueries) GetBillingIssuerByCode(_ context.Context, code string) (gen.BillingIssuerRow, error) {
	return gen.BillingIssuerRow{ID: uuid.New(), Code: code}, nil
}

func (f *fakeIntakeQueries) GetOrgBillingCountry(context.Context, uuid.UUID) (string, error) {
	return f.country, nil
}

func (f *fakeIntakeQueries) GetBillingAccount(_ context.Context, orgID uuid.UUID, currency, kind string) (gen.BillingAccountRow, error) {
	for _, a := range f.accounts {
		if a.OrgID == orgID && a.Currency == currency && a.AccountKind == kind {
			return a, nil
		}
	}
	return gen.BillingAccountRow{}, pgx.ErrNoRows
}

func (f *fakeIntakeQueries) ListBillingAccountsByOrg(_ context.Context, orgID uuid.UUID) ([]gen.BillingAccountRow, error) {
	var out []gen.BillingAccountRow
	for _, a := range f.accounts {
		if a.OrgID == orgID {
			out = append(out, a)
		}
	}
	return out, nil
}

func (f *fakeIntakeQueries) InsertBillableEvent(_ context.Context, p gen.InsertBillableEventParams) (uuid.UUID, bool, error) {
	for _, e := range f.events {
		if e.Kind == p.Kind && e.SubjectID == p.SubjectID {
			return uuid.Nil, false, nil // ON CONFLICT DO NOTHING
		}
	}
	f.events = append(f.events, p)
	return uuid.New(), true, nil
}

func TestRecordVenuePlanIntake_WritesOneFreeFactToTheRoutedAccount(t *testing.T) {
	org := uuid.New()
	ils := gen.BillingAccountRow{ID: uuid.New(), OrgID: org, Currency: "ILS", AccountKind: AccountKindOrganizer}
	eur := gen.BillingAccountRow{ID: uuid.New(), OrgID: org, Currency: "EUR", AccountKind: AccountKindOrganizer}
	q := &fakeIntakeQueries{country: "IL", accounts: []gen.BillingAccountRow{eur, ils}}
	in := VenuePlanIntakeFact{
		IntakeID: uuid.New(), OrgID: org, ApprovedAt: time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC),
		SourceKinds: []string{"png", "pdf", "png"}, OperatorInvolved: true, Questions: 3, Drafts: 2,
	}
	for i := 0; i < 2; i++ { // the second call is a no-op
		if err := recordVenuePlanIntake(context.Background(), q, in); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if len(q.events) != 1 {
		t.Fatalf("events: %d, want 1", len(q.events))
	}
	ev := q.events[0]
	if ev.Kind != string(KindVenuePlanIntake) || ev.SubjectID != in.IntakeID || ev.AccountID != ils.ID ||
		ev.OrgID != org || !ev.OccurredAt.Equal(in.ApprovedAt) {
		t.Fatalf("event %+v", ev)
	}
	var attrs map[string]any
	if err := json.Unmarshal(ev.Attributes, &attrs); err != nil {
		t.Fatal(err)
	}
	want := `{"drafts":2,"operator_involved":true,"questions":3,"source_kinds":["pdf","png"]}`
	got, _ := json.Marshal(attrs)
	if string(got) != want {
		t.Fatalf("attributes %s, want %s", got, want)
	}
}

func TestRecordVenuePlanIntake_FallsBackToAnyOrganizerAccount(t *testing.T) {
	org := uuid.New()
	usd := gen.BillingAccountRow{ID: uuid.New(), OrgID: org, Currency: "USD", AccountKind: AccountKindOrganizer}
	q := &fakeIntakeQueries{country: "IL", accounts: []gen.BillingAccountRow{usd}}
	in := VenuePlanIntakeFact{IntakeID: uuid.New(), OrgID: org, ApprovedAt: time.Now()}
	if err := recordVenuePlanIntake(context.Background(), q, in); err != nil {
		t.Fatal(err)
	}
	if len(q.events) != 1 || q.events[0].AccountID != usd.ID {
		t.Fatalf("events %+v, want one on the USD account", q.events)
	}
	if string(q.events[0].Attributes) != `{"source_kinds":[],"operator_involved":false,"questions":0,"drafts":0}` {
		t.Fatalf("attributes %s", q.events[0].Attributes)
	}
}

func TestRecordVenuePlanIntake_NoAccountIsATypedError(t *testing.T) {
	q := &fakeIntakeQueries{country: "IL"}
	err := recordVenuePlanIntake(context.Background(), q, VenuePlanIntakeFact{IntakeID: uuid.New(), OrgID: uuid.New(), ApprovedAt: time.Now()})
	if !errors.Is(err, ErrNoBillingAccount) {
		t.Fatalf("err = %v, want ErrNoBillingAccount", err)
	}
	if len(q.events) != 0 {
		t.Fatalf("events written: %d", len(q.events))
	}
}

func TestRecordVenuePlanIntake_RefusesAnIncompleteFact(t *testing.T) {
	q := &fakeIntakeQueries{}
	for _, in := range []VenuePlanIntakeFact{
		{OrgID: uuid.New(), ApprovedAt: time.Now()},
		{IntakeID: uuid.New(), ApprovedAt: time.Now()},
		{IntakeID: uuid.New(), OrgID: uuid.New()},
		{IntakeID: uuid.New(), OrgID: uuid.New(), ApprovedAt: time.Now(), Questions: -1},
	} {
		if err := recordVenuePlanIntake(context.Background(), q, in); !errors.Is(err, ErrInconsistentEvent) {
			t.Errorf("%+v: err = %v, want ErrInconsistentEvent", in, err)
		}
	}
}
