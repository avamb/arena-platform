// types.go carries the pure-data helpers for hseating: response shapes,
// the row-to-response mappers, strict body decoders, and the field
// validators shared by POST / PATCH / fork / version-create.
package hseating

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// ─────────────────────────────────────────────────────────────────────────────
// Enum guards
// ─────────────────────────────────────────────────────────────────────────────

// Enum tables mirror the CHECK constraints on seating_plans (§5.1 of the
// seating backlog). Kept package-local so the HTTP layer rejects invalid
// values with a 400 envelope rather than surfacing a raw postgres error.

var validPlanTypes = map[string]bool{
	"assigned_seats":    true,
	"general_admission": true,
	"tables":            true,
	"mixed":             true,
}

var validVisibilities = map[string]bool{
	"private":           true,
	"shared_read":       true,
	"public_template":   true,
	"operator_verified": true,
}

var validStatuses = map[string]bool{
	"draft":    true,
	"active":   true,
	"archived": true,
}

// ─────────────────────────────────────────────────────────────────────────────
// Response shapes
// ─────────────────────────────────────────────────────────────────────────────

// SeatingPlanResponse is the JSON representation of a seating_plans row.
type SeatingPlanResponse struct {
	ID                  string  `json:"id"`
	VenueID             string  `json:"venue_id"`
	OwnerOrgID          string  `json:"owner_org_id"`
	Name                string  `json:"name"`
	PlanType            string  `json:"plan_type"`
	Visibility          string  `json:"visibility"`
	Status              string  `json:"status"`
	SourceSeatingPlanID *string `json:"source_seating_plan_id"`
	CurrentVersionID    *string `json:"current_version_id"`
	CreatedAt           string  `json:"created_at"`
	UpdatedAt           string  `json:"updated_at"`
}

// SeatingPlanFromRow renders a seating_plans row into the response shape.
func SeatingPlanFromRow(p gen.SeatingPlanRow) SeatingPlanResponse {
	out := SeatingPlanResponse{
		ID:         p.ID.String(),
		VenueID:    p.VenueID.String(),
		OwnerOrgID: p.OwnerOrgID.String(),
		Name:       p.Name,
		PlanType:   p.PlanType,
		Visibility: p.Visibility,
		Status:     p.Status,
		CreatedAt:  p.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:  p.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if p.SourceSeatingPlanID != nil {
		s := p.SourceSeatingPlanID.String()
		out.SourceSeatingPlanID = &s
	}
	if p.CurrentVersionID != nil {
		s := p.CurrentVersionID.String()
		out.CurrentVersionID = &s
	}
	return out
}

// SeatingPlanVersionResponse is the JSON representation of a
// seating_plan_versions row.
type SeatingPlanVersionResponse struct {
	ID               string          `json:"id"`
	SeatingPlanID    string          `json:"seating_plan_id"`
	VersionNumber    int32           `json:"version_number"`
	Geometry         json.RawMessage `json:"geometry"`
	GeometryChecksum string          `json:"geometry_checksum"`
	SvgAssetMediaID  *string         `json:"svg_asset_media_id"`
	CapacitySeated   int32           `json:"capacity_seated"`
	CapacityStanding int32           `json:"capacity_standing"`
	LockedAt         *string         `json:"locked_at"`
	CreatedAt        string          `json:"created_at"`
}

// SeatingPlanVersionFromRow renders a seating_plan_versions row into the
// response shape.
func SeatingPlanVersionFromRow(v gen.SeatingPlanVersionRow) SeatingPlanVersionResponse {
	out := SeatingPlanVersionResponse{
		ID:               v.ID.String(),
		SeatingPlanID:    v.SeatingPlanID.String(),
		VersionNumber:    v.VersionNumber,
		Geometry:         v.Geometry,
		GeometryChecksum: v.GeometryChecksum,
		CapacitySeated:   v.CapacitySeated,
		CapacityStanding: v.CapacityStanding,
		CreatedAt:        v.CreatedAt.UTC().Format(time.RFC3339),
	}
	if v.SvgAssetMediaID != nil {
		s := v.SvgAssetMediaID.String()
		out.SvgAssetMediaID = &s
	}
	if v.LockedAt != nil {
		s := v.LockedAt.UTC().Format(time.RFC3339)
		out.LockedAt = &s
	}
	return out
}

// ─────────────────────────────────────────────────────────────────────────────
// Body decoding helpers
// ─────────────────────────────────────────────────────────────────────────────

// planCreateFields lists every property accepted by CreateSeatingPlanRequest.
var planCreateFields = map[string]bool{
	"owner_org_id": true,
	"name":         true,
	"plan_type":    true,
	"visibility":   true,
	"status":       true,
}

// planPatchFields lists every property accepted by UpdateSeatingPlanRequest.
var planPatchFields = map[string]bool{
	"name":       true,
	"visibility": true,
	"status":     true,
}

// planForkFields lists every property accepted by ForkSeatingPlanRequest.
var planForkFields = map[string]bool{
	"owner_org_id": true,
	"venue_id":     true,
	"name":         true,
}

// versionCreateFields lists every property accepted by
// CreateSeatingPlanVersionRequest.
var versionCreateFields = map[string]bool{
	"svg":                true,
	"geometry":           true,
	"svg_asset_media_id": true,
	"capacity_standing":  true,
}

// decodeBody parses body into a key→raw-value map after verifying it is a
// JSON object containing only keys allowed by allowed. On failure the
// (code, message) pair describes the 400 error envelope to write.
func decodeBody(body []byte, allowed map[string]bool) (fields map[string]json.RawMessage, code, message string) {
	if err := json.Unmarshal(body, &fields); err != nil {
		return nil, "seating_plan.invalid_json", "request body is not a valid JSON object"
	}
	for key := range fields {
		if !allowed[key] {
			return nil, "seating_plan.unknown_field", "unknown field " + quoteKey(key)
		}
	}
	return fields, "", ""
}

func quoteKey(s string) string { return `"` + s + `"` }

// stringField extracts fields[key] as a trimmed string. Returns
// (value, present, ok): present is false when the key is absent; value is
// nil when the key is present but null or empty (the "clear" signal for
// PATCH); ok is false when the value is not a string or null.
func stringField(fields map[string]json.RawMessage, key string) (value *string, present, ok bool) {
	raw, exists := fields[key]
	if !exists {
		return nil, false, true
	}
	if string(raw) == "null" {
		return nil, true, true
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, true, false
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, true, true
	}
	return &s, true, true
}

// intField extracts fields[key] as an int32. Same present/ok contract as
// stringField; null is rejected.
func intField(fields map[string]json.RawMessage, key string) (value int32, present, ok bool) {
	raw, exists := fields[key]
	if !exists {
		return 0, false, true
	}
	var n int32
	if err := json.Unmarshal(raw, &n); err != nil {
		return 0, true, false
	}
	return n, true, true
}
