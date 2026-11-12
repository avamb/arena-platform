package billing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// ErrNoBillingAccount: the organization has no organizer billing account, so
// a fact that is only counted (venue_plan_intake) has nowhere to be written.
// The caller skips the fact; it never fails the action that caused it.
var ErrNoBillingAccount = errors.New("billing: the organization has no billing account")

// VenuePlanIntakeFact is the billing fact of one approved hall plan intake
// (spec 08_architecture/64 section 4.7). Building a plan is free: the rate
// job charges 0 with the reason free, and the attributes say what the work
// was, so a price can be decided later.
type VenuePlanIntakeFact struct {
	// IntakeID is the subject of the event: a second write of the same
	// intake adds nothing.
	IntakeID uuid.UUID
	OrgID    uuid.UUID
	// ApprovedAt is when the intake was approved; the event's occurred_at.
	ApprovedAt time.Time
	// SourceKinds are the kinds of the files sent (pdf, xlsx, png, svg, ...).
	SourceKinds []string
	// OperatorInvolved is true when a platform administrator worked on it.
	OperatorInvolved bool
	// Questions is how many questions the organizer was asked, Drafts how
	// many drafts were made.
	Questions int
	Drafts    int
}

// venuePlanIntakeAttributes is the attributes JSON of a venue_plan_intake
// event.
type venuePlanIntakeAttributes struct {
	SourceKinds      []string `json:"source_kinds"`
	OperatorInvolved bool     `json:"operator_involved"`
	Questions        int      `json:"questions"`
	Drafts           int      `json:"drafts"`
}

// VenuePlanIntakeQueries is the query set RecordVenuePlanIntake needs.
// *gen.Queries satisfies it.
type VenuePlanIntakeQueries interface {
	RouteQueries
	GetOrgBillingCountry(ctx context.Context, orgID uuid.UUID) (string, error)
	GetBillingAccount(ctx context.Context, orgID uuid.UUID, currency, accountKind string) (gen.BillingAccountRow, error)
	ListBillingAccountsByOrg(ctx context.Context, orgID uuid.UUID) ([]gen.BillingAccountRow, error)
	InsertBillableEvent(ctx context.Context, p gen.InsertBillableEventParams) (uuid.UUID, bool, error)
}

// RecordVenuePlanIntake writes the venue_plan_intake billing fact of an
// approved intake inside tx, the transaction that approves it. The event is
// idempotent by (venue_plan_intake, intake id): a repeated call writes
// nothing and returns nil.
//
// The account is found like the monthly charges find it (package
// monthcharge): the organization's billing country (legal address, else
// country) routed to a currency, and the organizer account in that currency.
// Unlike them it never CREATES an account: when the routed account does not
// exist, the organization's first organizer account (by currency) is used,
// and without any it returns ErrNoBillingAccount so the caller can skip the
// fact.
func RecordVenuePlanIntake(ctx context.Context, tx pgx.Tx, in VenuePlanIntakeFact) error {
	return recordVenuePlanIntake(ctx, gen.New(tx), in)
}

func recordVenuePlanIntake(ctx context.Context, q VenuePlanIntakeQueries, in VenuePlanIntakeFact) error {
	if in.IntakeID == uuid.Nil || in.OrgID == uuid.Nil || in.ApprovedAt.IsZero() || in.Questions < 0 || in.Drafts < 0 {
		return fmt.Errorf("%w: venue plan intake fact %+v", ErrInconsistentEvent, in)
	}
	acc, err := venuePlanIntakeAccount(ctx, q, in.OrgID)
	if err != nil {
		return err
	}
	kinds := append([]string{}, in.SourceKinds...)
	sort.Strings(kinds)
	kinds = dedupSorted(kinds)
	attrs, err := json.Marshal(venuePlanIntakeAttributes{
		SourceKinds: kinds, OperatorInvolved: in.OperatorInvolved, Questions: in.Questions, Drafts: in.Drafts,
	})
	if err != nil {
		return err
	}
	if _, _, err := q.InsertBillableEvent(ctx, gen.InsertBillableEventParams{
		Kind:       string(KindVenuePlanIntake),
		SubjectID:  in.IntakeID,
		OccurredAt: in.ApprovedAt.UTC(),
		OrgID:      in.OrgID,
		AccountID:  acc.ID,
		Attributes: attrs,
	}); err != nil {
		return fmt.Errorf("billing: insert venue_plan_intake of intake %s: %w", in.IntakeID, err)
	}
	return nil
}

// venuePlanIntakeAccount is the organizer account a venue_plan_intake fact is
// written to (see RecordVenuePlanIntake).
func venuePlanIntakeAccount(ctx context.Context, q VenuePlanIntakeQueries, orgID uuid.UUID) (gen.BillingAccountRow, error) {
	country, err := q.GetOrgBillingCountry(ctx, orgID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return gen.BillingAccountRow{}, fmt.Errorf("billing: country of org %s: %w", orgID, err)
	}
	currency, _, err := RouteCurrency(ctx, q, country)
	if err != nil {
		return gen.BillingAccountRow{}, err
	}
	acc, err := q.GetBillingAccount(ctx, orgID, currency, AccountKindOrganizer)
	if err == nil {
		return acc, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return gen.BillingAccountRow{}, fmt.Errorf("billing: %s account of org %s: %w", currency, orgID, err)
	}
	all, err := q.ListBillingAccountsByOrg(ctx, orgID)
	if err != nil {
		return gen.BillingAccountRow{}, fmt.Errorf("billing: accounts of org %s: %w", orgID, err)
	}
	for _, a := range all {
		if a.AccountKind == AccountKindOrganizer {
			return a, nil
		}
	}
	return gen.BillingAccountRow{}, fmt.Errorf("%w: org %s", ErrNoBillingAccount, orgID)
}

func dedupSorted(s []string) []string {
	out := make([]string, 0, len(s))
	for i, v := range s {
		if i == 0 || v != s[i-1] {
			out = append(out, v)
		}
	}
	return out
}
