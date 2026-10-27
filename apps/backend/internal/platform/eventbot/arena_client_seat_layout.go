package eventbot

// arena_client_seat_layout.go — the calls behind the bot's "Seat layout"
// screens (task S3c of spec 40): the algorithm list and the proposal of the
// seat-layout allocator (S1e), the seat batches that apply and revert a
// layout (S1c) and the plan geometry the stage question reads. The rules live
// in arena-api; the bot only carries the answers. Nothing here names an
// algorithm.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/seating"
)

// The API's codes the layout screens give their own words to.
const (
	seatBatchCodeNothingToRevert = "seat_batch.nothing_to_revert"
	seatBatchCodeAlreadyReverted = "seat_batch.already_reverted"
	seatBatchCodeNotFound        = "seat_batch.not_found"
	seatBatchCodeTooMany         = "seat_batch.too_many_changes"
	seatLayoutCodeNoPlan         = "seat_layout.no_seating_plan"
	seatLayoutCodeAnnotation     = "seat_layout.annotation_required"
	seatLayoutCodeNoSeats        = "seat_layout.no_seats"
)

func seatLayoutPath(orgID, sessionID uuid.UUID) string {
	return "/v1/organizations/" + orgID.String() + "/sessions/" + sessionID.String() + "/seat-layout"
}

func seatBatchesPath(orgID, sessionID uuid.UUID) string {
	return "/v1/organizations/" + orgID.String() + "/sessions/" + sessionID.String() + "/seat-batches"
}

// SeatLayoutAlgorithms lists the allocator's algorithms with their parameter
// schemas.
func (c *ArenaClient) SeatLayoutAlgorithms(ctx context.Context, jwt string, orgID, sessionID uuid.UUID) (openapi.SeatLayoutAlgorithmsResponse, error) {
	var out openapi.SeatLayoutAlgorithmsResponse
	err := c.do(ctx, http.MethodGet, seatLayoutPath(orgID, sessionID)+"/algorithms", jwt, nil, &out)
	return out, err
}

// SeatLayoutSuggest asks for a proposal. It writes nothing.
func (c *ArenaClient) SeatLayoutSuggest(ctx context.Context, jwt string, orgID, sessionID uuid.UUID, req openapi.SeatLayoutSuggestRequest) (openapi.SeatLayoutSuggestResponse, error) {
	var out openapi.SeatLayoutSuggestResponse
	err := c.do(ctx, http.MethodPost, seatLayoutPath(orgID, sessionID)+"/suggest", jwt, req, &out)
	return out, err
}

// CreateSeatBatch applies a batch of seat changes.
func (c *ArenaClient) CreateSeatBatch(ctx context.Context, jwt string, orgID, sessionID uuid.UUID, req openapi.SeatBatchRequest) (openapi.SeatBatch, error) {
	var out openapi.SeatBatch
	err := c.do(ctx, http.MethodPost, seatBatchesPath(orgID, sessionID), jwt, req, &out)
	return out, err
}

// RevertSeatBatch puts a batch back (a new batch of kind revert).
func (c *ArenaClient) RevertSeatBatch(ctx context.Context, jwt string, orgID, sessionID, batchID uuid.UUID) (openapi.SeatBatch, error) {
	var out openapi.SeatBatch
	err := c.do(ctx, http.MethodPost, seatBatchesPath(orgID, sessionID)+"/"+batchID.String()+"/revert", jwt, nil, &out)
	return out, err
}

// LastSeatBatch is the newest batch of the session, or nil when it has none.
func (c *ArenaClient) LastSeatBatch(ctx context.Context, jwt string, orgID, sessionID uuid.UUID) (*openapi.SeatBatchSummary, error) {
	var out openapi.SeatBatchList
	q := url.Values{"limit": {"1"}}
	if err := c.do(ctx, http.MethodGet, seatBatchesPath(orgID, sessionID)+"?"+q.Encode(), jwt, nil, &out); err != nil {
		return nil, err
	}
	if len(out.Items) == 0 {
		return nil, nil
	}
	return &out.Items[0], nil
}

// PlanBounds is the box the seats of a plan version fill, in canvas
// coordinates.
type PlanBounds struct {
	MinX, MaxX, MinY, MaxY float64
	Seats                  int
}

// PlanVersionBounds finds the seating plan version among the plans of the
// venue and returns the box its seats fill. found is false when the version
// is not among them (the plan was archived) or has no seats.
func (c *ArenaClient) PlanVersionBounds(ctx context.Context, jwt string, venueID, versionID uuid.UUID) (PlanBounds, bool, error) {
	var plans struct {
		Plans []struct {
			ID uuid.UUID `json:"id"`
		} `json:"seating_plans"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/venues/"+venueID.String()+"/seating-plans", jwt, nil, &plans); err != nil {
		return PlanBounds{}, false, err
	}
	for _, p := range plans.Plans {
		var versions struct {
			Versions []struct {
				ID       uuid.UUID       `json:"id"`
				Geometry json.RawMessage `json:"geometry"`
			} `json:"seating_plan_versions"`
		}
		if err := c.do(ctx, http.MethodGet, "/v1/seating-plans/"+p.ID.String()+"/versions", jwt, nil, &versions); err != nil {
			return PlanBounds{}, false, err
		}
		for _, v := range versions.Versions {
			if v.ID != versionID {
				continue
			}
			b, ok := boundsOfGeometry(v.Geometry)
			return b, ok, nil
		}
	}
	return PlanBounds{}, false, nil
}

// boundsOfGeometry reads the seat coordinates out of a plan's geometry JSON.
func boundsOfGeometry(raw json.RawMessage) (PlanBounds, bool) {
	var g struct {
		Sections []struct {
			Rows []struct {
				Seats []struct {
					X float64 `json:"x"`
					Y float64 `json:"y"`
				} `json:"seats"`
			} `json:"rows"`
		} `json:"sections"`
	}
	if err := json.Unmarshal(raw, &g); err != nil {
		return PlanBounds{}, false
	}
	var b PlanBounds
	for _, s := range g.Sections {
		for _, r := range s.Rows {
			for _, st := range r.Seats {
				if b.Seats == 0 {
					b.MinX, b.MaxX, b.MinY, b.MaxY = st.X, st.X, st.Y, st.Y
				} else {
					b.MinX, b.MaxX = min(b.MinX, st.X), max(b.MaxX, st.X)
					b.MinY, b.MaxY = min(b.MinY, st.Y), max(b.MaxY, st.Y)
				}
				b.Seats++
			}
		}
	}
	return b, b.Seats > 0
}

// VenuePlans lists the seating plans a new date at the venue can be sold by:
// the organization's own active plans, each at its current version, with the
// categories that have places. It is the PlanIO of the "+ Event" wizard.
func (c *ArenaClient) VenuePlans(ctx context.Context, jwt string, orgID uuid.UUID, venueID string) ([]PlanRef, error) {
	var plans struct {
		Plans []openapi.SeatingPlan `json:"seating_plans"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/venues/"+url.PathEscape(venueID)+"/seating-plans", jwt, nil, &plans); err != nil {
		return nil, err
	}
	var out []PlanRef
	for _, p := range plans.Plans {
		if p.OwnerOrgId != orgID || string(p.Status) != "active" || p.CurrentVersionId == nil {
			continue
		}
		var versions struct {
			Versions []openapi.SeatingPlanVersion `json:"seating_plan_versions"`
		}
		if err := c.do(ctx, http.MethodGet, "/v1/seating-plans/"+p.Id.String()+"/versions", jwt, nil, &versions); err != nil {
			return nil, err
		}
		for _, v := range versions.Versions {
			if v.Id != *p.CurrentVersionId {
				continue
			}
			if ref, ok := planRefOf(p, v); ok {
				out = append(out, ref)
			}
		}
	}
	return out, nil
}

// planRefOf reads the categories with places out of a version's geometry:
// seated ones count their seats, a standing zone its capacity. The plan's
// display names win over the geometry's, as they do at the import.
func planRefOf(p openapi.SeatingPlan, v openapi.SeatingPlanVersion) (PlanRef, bool) {
	raw, err := json.Marshal(v.Geometry)
	if err != nil {
		return PlanRef{}, false
	}
	var g seating.Geometry
	if err := json.Unmarshal(raw, &g); err != nil {
		return PlanRef{}, false
	}
	seats := map[int]int{}
	for _, sec := range g.Sections {
		for _, row := range sec.Rows {
			for _, s := range row.Seats {
				seats[s.CategoryIndex]++
			}
		}
	}
	ref := PlanRef{VersionID: v.Id.String(), Name: p.Name}
	for _, cat := range g.Categories {
		places := seats[cat.Index]
		if cat.IsGA() {
			places = cat.Capacity
		}
		if places <= 0 {
			continue
		}
		name := cat.Name
		if n := p.CategoryNameOverrides[strconv.Itoa(cat.Index)]; strings.TrimSpace(n) != "" {
			name = n
		}
		ref.Categories = append(ref.Categories, PlanCategory{Index: cat.Index, Name: name, Places: places, GA: cat.IsGA()})
		ref.Seats += places
	}
	return ref, len(ref.Categories) > 0
}
