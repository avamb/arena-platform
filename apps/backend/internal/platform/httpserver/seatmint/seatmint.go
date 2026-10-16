// Package seatmint writes the places of a seating plan version onto a
// session: one session_seats row per geometry seat (kind 'seat') and one
// ga_unit row per General Admission place of a geometry GA zone, keyed
// 'ga|c<category index>|<n>'.
//
// It is the ONE copy of that step, shared by the three callers that bind a
// plan version to a session — the seating bind endpoint (hseating/bind.go),
// the seated half of the Bil24-format import (himports/seating.go) and the
// seated branch of the arena event bundle (himports/import_arena_seated.go)
// — so the three cannot drift apart on seat keys, system seat ids or the GA
// place keys a later quota edit must never collide with.
//
// Everything runs on the caller's transaction (q is bound to it). The caller
// owns the sessions row lock (lock order: sessions FIRST) and ends with
// gaquota.Recompute; nothing here recomputes the session capacity.
package seatmint

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/seating"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/gaquota"
)

// Admission modes a bound plan puts a session in (sessions.admission_mode).
const (
	ModeAssignedSeats    = "assigned_seats"
	ModeHybrid           = "hybrid"
	ModeGeneralAdmission = "general_admission"
)

// Policy says what happens to a geometry category that has no tier.
type Policy int

const (
	// Strict refuses it: a seat or a GA zone without a tier, or a GA zone
	// whose capacity is out of range, is an error (ErrUnmappedCategory /
	// ErrZoneCapacity). The seating bind runs strict — its category map is
	// complete by construction, so a gap is a bug.
	Strict Policy = iota
	// Lenient writes a seat without a tier with a NULL tier_id (priceless,
	// reported through Result.UnmappedSeats) and skips a GA zone without a
	// tier or with an out-of-range capacity. The imports run lenient.
	Lenient
)

// ErrUnmappedCategory is returned under Strict when a seat or a GA zone
// references a category the tier map does not carry.
var ErrUnmappedCategory = errors.New("seatmint: geometry category has no tier")

// ErrZoneCapacity is returned under Strict when a GA zone's capacity is not a
// positive int32.
var ErrZoneCapacity = errors.New("seatmint: GA zone capacity out of range")

// Result is what Materialize wrote.
type Result struct {
	// Seats is the number of kind='seat' rows inserted.
	Seats int
	// GAPlaces is the number of kind='ga_unit' rows inserted.
	GAPlaces int
	// UnmappedSeats reports (Lenient only) that at least one seat was written
	// without a tier.
	UnmappedSeats bool
}

// Materialize writes the places of geometry g onto sessionID.
//
// tiers maps a geometry category index to the ticket tier that sells it.
// mintGA is false when the GA zones must not be minted: an assigned_seats
// session, or a rebind of the same version whose categories already own their
// places (plan 08_architecture/23 decision 8).
//
// Seats keep their stable geometry seat_key. When the geometry carries
// upstream seat ids (Seat.ExternalID — the Bil24 seatId), they are written
// verbatim as system_seat_id with source 'bil24' and the arena sequence is
// pushed past the largest one first, so a later plain materialization can
// never mint a colliding id; otherwise the sequence mints them ('arena').
//
// Every GA place minted here belongs to its zone's tier, and the zone's
// quantity is recorded with gaquota.AdoptPlanCategory, which also reserves
// the category's own per-session number so a later quota edit continues
// under 'ga|t<unit_seq>' instead of competing for a 'ga|c' key.
func Materialize(
	ctx context.Context,
	q *gen.Queries,
	sessionID uuid.UUID,
	g seating.Geometry,
	tiers map[int]uuid.UUID,
	mintGA bool,
	policy Policy,
) (Result, error) {
	var out Result

	n := g.SeatCount()
	seatKeys := make([]string, 0, n)
	sectorNames := make([]string, 0, n)
	rowNames := make([]string, 0, n)
	seatNumbers := make([]string, 0, n)
	tierIDs := make([]*string, 0, n)
	systemSeatIDs := make([]*int64, 0, n)
	var maxExternalID int64
	for _, section := range g.Sections {
		for _, row := range section.Rows {
			for _, seat := range row.Seats {
				tierID, ok := tiers[seat.CategoryIndex]
				switch {
				case ok:
					s := tierID.String()
					tierIDs = append(tierIDs, &s)
				case policy == Strict:
					return Result{}, fmt.Errorf("%w: seat %q references category %d",
						ErrUnmappedCategory, seat.Key, seat.CategoryIndex)
				default:
					// A category drawn in the plan but not priced is not fatal
					// to an import: the seat exists and can be priced later.
					tierIDs = append(tierIDs, nil)
					out.UnmappedSeats = true
				}
				seatKeys = append(seatKeys, seat.Key)
				sectorNames = append(sectorNames, section.Name)
				rowNames = append(rowNames, row.Name)
				seatNumbers = append(seatNumbers, seat.Number)
				if seat.ExternalID > 0 {
					ext := seat.ExternalID
					systemSeatIDs = append(systemSeatIDs, &ext)
					if ext > maxExternalID {
						maxExternalID = ext
					}
				} else {
					systemSeatIDs = append(systemSeatIDs, nil)
				}
			}
		}
	}

	source := gen.SeatIDSourceArena
	if maxExternalID > 0 {
		source = gen.SeatIDSourceBil24
		if err := q.AdvanceSessionSeatSystemIDSeq(ctx, maxExternalID); err != nil {
			return Result{}, fmt.Errorf("seatmint: advance seat id sequence: %w", err)
		}
	}
	if len(seatKeys) > 0 {
		inserted, err := q.InsertSessionSeats(ctx, sessionID, seatKeys, sectorNames, rowNames,
			seatNumbers, tierIDs, systemSeatIDs, source)
		if err != nil {
			return Result{}, fmt.Errorf("seatmint: insert session seats: %w", err)
		}
		out.Seats = int(inserted)
	}

	if !mintGA {
		return out, nil
	}
	for _, zone := range g.GACategories() {
		tierID, ok := tiers[zone.Index]
		if !ok {
			if policy == Strict {
				return Result{}, fmt.Errorf("%w: GA zone %d", ErrUnmappedCategory, zone.Index)
			}
			// Since migration 0101 a GA place belongs to a category; a
			// NULL-tier batch would be inventory nothing can sell.
			continue
		}
		if zone.Capacity <= 0 || zone.Capacity > math.MaxInt32 {
			if policy == Strict {
				return Result{}, fmt.Errorf("%w: GA zone %d capacity %d", ErrZoneCapacity, zone.Index, zone.Capacity)
			}
			continue
		}
		quantity := int32(zone.Capacity) //nolint:gosec // bound-checked above
		inserted, err := q.InsertGAUnits(ctx, sessionID, ZoneKeyPrefix(zone.Index), 0, &tierID, quantity)
		if err != nil {
			return Result{}, fmt.Errorf("seatmint: insert GA places of zone %d: %w", zone.Index, err)
		}
		out.GAPlaces += int(inserted)
		if err := gaquota.AdoptPlanCategory(ctx, q, sessionID, tierID, quantity); err != nil {
			return Result{}, fmt.Errorf("seatmint: record GA zone %d quantity: %w", zone.Index, err)
		}
	}
	return out, nil
}

// ZoneKeyPrefix is the seat_key prefix of a geometry GA zone's places:
// 'ga|c3' → 'ga|c3|000001', …
func ZoneKeyPrefix(categoryIndex int) string {
	return "ga|c" + strconv.Itoa(categoryIndex)
}

// AdmissionMode is the mode a session bound to g runs in: hybrid when the
// plan has both seats and GA zones, assigned_seats with seats only,
// general_admission with GA zones only.
func AdmissionMode(g seating.Geometry) string {
	seats := g.SeatCount() > 0
	zones := len(g.GACategories()) > 0
	switch {
	case seats && zones:
		return ModeHybrid
	case seats:
		return ModeAssignedSeats
	default:
		return ModeGeneralAdmission
	}
}

// PlanCapacity is the capacity a session bound to version v under mode
// declares at bind time (AB-40 B6). It is only the starting value: the
// caller recomputes the real one from the places with gaquota.Recompute.
func PlanCapacity(mode string, v gen.SeatingPlanVersionRow) int32 {
	switch mode {
	case ModeHybrid:
		return v.CapacitySeated + v.CapacityStanding
	case ModeGeneralAdmission:
		return v.CapacityStanding
	default:
		return v.CapacitySeated
	}
}

// PlacedCategories is the set of geometry categories that own places: every
// category at least one seat references, plus every GA zone. A seated legend
// entry no seat uses owns nothing and is not in the set.
func PlacedCategories(g seating.Geometry) map[int]bool {
	out := make(map[int]bool, len(g.Categories))
	for _, section := range g.Sections {
		for _, row := range section.Rows {
			for _, seat := range row.Seats {
				out[seat.CategoryIndex] = true
			}
		}
	}
	for _, c := range g.Categories {
		if c.IsGA() {
			out[c.Index] = true
		}
	}
	return out
}
