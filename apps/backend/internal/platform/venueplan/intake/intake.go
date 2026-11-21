// Package intake holds the steps of a hall plan intake that both arena-api
// (internal/platform/httpserver/hvenueplan) and arena-worker (the job
// venue_plan.generate) take: the guarded state move, the check of a plan SVG
// and the summary of a draft. There is ONE copy of each; the routes and the
// worker call these.
package intake

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/seating"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/venueplan"
)

// MoveState moves the locked intake from its state to `to`, checking the
// state machine first. ok=false: the move is not allowed or the state changed
// under the caller (the route answers 409 venue_plan_intake.wrong_state).
func MoveState(ctx context.Context, q *gen.Queries, in gen.VenuePlanIntakeRow, to venueplan.State, p gen.UpdateVenuePlanIntakeStateParams) (gen.VenuePlanIntakeRow, bool, error) {
	if !venueplan.CanTransition(venueplan.State(in.State), to) {
		return in, false, nil
	}
	p.ID, p.From, p.To = in.ID, in.State, string(to)
	out, err := q.UpdateVenuePlanIntakeState(ctx, p)
	if errors.Is(err, pgx.ErrNoRows) {
		return in, false, nil
	}
	return out, err == nil, err
}

// CheckPlanSVG imports an SVG and validates it for the plan type its geometry
// fits, like the bot's plan upload.
func CheckPlanSVG(raw []byte) (seating.Geometry, []seating.ValidationError, string, seating.ValidationErrors) {
	g, warnings, errs := seating.ImportAnySVG(raw)
	planType := seating.PlanTypeOf(g)
	if len(errs) == 0 {
		errs = seating.ValidateForPlanType(g, planType)
	}
	return g, warnings, planType, errs
}

// Summary is the summary of a draft, stored in venue_plan_drafts.report and
// shown on the intake card.
type Summary struct {
	Seats    int      `json:"seats"`
	Rows     int      `json:"rows"`
	Sections int      `json:"sections"`
	PlanType string   `json:"plan_type"`
	Warnings []string `json:"warnings"`
}

// SummaryOf builds a draft's summary from its geometry.
func SummaryOf(g seating.Geometry, planType string, warnings []seating.ValidationError) Summary {
	s := Summary{Seats: g.SeatCount(), Sections: len(g.Sections), PlanType: planType, Warnings: []string{}}
	for _, sec := range g.Sections {
		s.Rows += len(sec.Rows)
	}
	for _, w := range warnings {
		s.Warnings = append(s.Warnings, w.Code)
	}
	return s
}
