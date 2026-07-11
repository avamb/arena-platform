// types.go carries the pure-data helpers for hseating: response shapes,
// the row-to-response mappers, strict body decoders, and the field
// validators shared by POST / PATCH / fork / version-create.
package hseating

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/seating"
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
// CurrentVersionNumber mirrors CurrentVersionID as a 1-based positional
// version number (null until the first version exists) so clients can
// address the current version through GET
// /v1/seating-plans/{id}/versions/{n} without probing.
type SeatingPlanResponse struct {
	ID                   string  `json:"id"`
	VenueID              string  `json:"venue_id"`
	OwnerOrgID           string  `json:"owner_org_id"`
	Name                 string  `json:"name"`
	PlanType             string  `json:"plan_type"`
	Visibility           string  `json:"visibility"`
	Status               string  `json:"status"`
	SourceSeatingPlanID  *string `json:"source_seating_plan_id"`
	CurrentVersionID     *string `json:"current_version_id"`
	CurrentVersionNumber *int32  `json:"current_version_number"`
	CreatedAt            string  `json:"created_at"`
	UpdatedAt            string  `json:"updated_at"`
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
	if p.CurrentVersionNumber != nil {
		n := *p.CurrentVersionNumber
		out.CurrentVersionNumber = &n
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

// ─────────────────────────────────────────────────────────────────────────────
// Shared geometry helpers (public schema + BSS layout.svg endpoints)
// ─────────────────────────────────────────────────────────────────────────────

// seatKeyIndex walks the geometry once and returns a seat_key → Seat
// lookup. Seats whose Key is empty (hand-authored geometry payloads that
// predate the seat.key serializer) fall back to the canonical
// "<section>|<row>|<number>" derivation via seating.SeatKey.
func seatKeyIndex(g seating.Geometry) map[string]seating.Seat {
	out := make(map[string]seating.Seat, g.SeatCount())
	for _, sec := range g.Sections {
		for _, row := range sec.Rows {
			for _, seat := range row.Seats {
				key := seat.Key
				if key == "" {
					key = seating.SeatKey(sec.Key, row.Key, seat.Number)
				}
				out[key] = seat
			}
		}
	}
	return out
}

// resolveCategoryTiers projects session_seats.tier_id back onto geometry
// category buckets: the first seat (in ListSessionSeats seat_key order)
// whose tier_id resolves to a live ticket_tiers row anchors its category.
// First-seat-wins keeps the resolution stable across renames; seats whose
// tier_id does not match any row in tiers (e.g. a tier soft-deleted after
// bind) are skipped so a later seat can still anchor the category.
func resolveCategoryTiers(
	g seating.Geometry,
	seats []gen.SessionSeatRow,
	tiers []gen.TicketTierRow,
	seatIdx map[string]seating.Seat,
) map[int]gen.TicketTierRow {
	tierByID := make(map[string]gen.TicketTierRow, len(tiers))
	for _, t := range tiers {
		tierByID[t.ID.String()] = t
	}
	out := make(map[int]gen.TicketTierRow, len(g.Categories))
	for _, s := range seats {
		if s.TierID == nil {
			continue
		}
		seat, ok := seatIdx[s.SeatKey]
		if !ok {
			continue
		}
		if _, already := out[seat.CategoryIndex]; already {
			continue
		}
		if tier, ok := tierByID[s.TierID.String()]; ok {
			out[seat.CategoryIndex] = tier
		}
	}
	return out
}
