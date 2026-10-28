package eventbot

// arena_client_plans.go — creating a hall plan from the wizard: the same
// three calls the admin console makes (POST /v1/venues/{id}/seating-plans,
// the SVG as a media object, POST /v1/seating-plans/{id}/versions), under the
// signed-in person's own rights (seating_plan.create / update.own, held by the
// owner and the manager) and for the organization the draft belongs to.

import (
	"context"
	"net/http"
	"net/url"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

// seatingPlanSVGOwnerType is media_objects.owner_type of a plan's SVG asset.
const seatingPlanSVGOwnerType = "seating_plan_svg"

// CanUploadPlans reports that this client can create plans: the wizard offers
// the upload only to a reference source that can do it.
func (c *ArenaClient) CanUploadPlans() bool { return true }

// CreateSeatingPlan makes an active plan of the venue, owned by the
// organization. It has no version yet, so it is not offered for a date until
// CreatePlanVersion gives it one.
func (c *ArenaClient) CreateSeatingPlan(ctx context.Context, jwt string, orgID uuid.UUID, venueID, name, planType string) (openapi.SeatingPlan, error) {
	var out struct {
		Plan openapi.SeatingPlan `json:"seating_plan"`
	}
	body := map[string]any{"owner_org_id": orgID.String(), "name": name, "plan_type": planType, "status": "active"}
	if err := c.do(ctx, http.MethodPost, "/v1/venues/"+url.PathEscape(venueID)+"/seating-plans", jwt, body, &out); err != nil {
		return openapi.SeatingPlan{}, err
	}
	return out.Plan, nil
}

// PlanVersionResult is what the version call answered: the plan (now with a
// current version), the version, and the importer's advisory warnings.
type PlanVersionResult struct {
	Plan     openapi.SeatingPlan
	Version  openapi.SeatingPlanVersion
	Warnings []string // importer warning codes
}

// CreatePlanVersion imports the SVG as the plan's next version and makes it
// the current one. mediaID is the stored copy of the file (may be empty).
func (c *ArenaClient) CreatePlanVersion(ctx context.Context, jwt string, planID uuid.UUID, svg, mediaID string) (PlanVersionResult, error) {
	var out struct {
		Plan     openapi.SeatingPlan        `json:"seating_plan"`
		Version  openapi.SeatingPlanVersion `json:"seating_plan_version"`
		Warnings []struct {
			Code string `json:"code"`
		} `json:"warnings"`
	}
	body := map[string]any{"svg": svg}
	if mediaID != "" {
		body["svg_asset_media_id"] = mediaID
	}
	if err := c.do(ctx, http.MethodPost, "/v1/seating-plans/"+planID.String()+"/versions", jwt, body, &out); err != nil {
		return PlanVersionResult{}, err
	}
	res := PlanVersionResult{Plan: out.Plan, Version: out.Version}
	for _, w := range out.Warnings {
		res.Warnings = append(res.Warnings, w.Code)
	}
	return res, nil
}

// ArchiveSeatingPlan takes a plan out of use: the clean-up of a plan whose
// first version could not be made.
func (c *ArenaClient) ArchiveSeatingPlan(ctx context.Context, jwt string, planID uuid.UUID) error {
	return c.do(ctx, http.MethodPatch, "/v1/seating-plans/"+planID.String(), jwt, map[string]any{"status": "archived"}, nil)
}
