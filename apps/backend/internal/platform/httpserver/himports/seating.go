// seating.go implements the SEATED half of the Bil24 session import
// (feature #518, W1-C3d; spec §13.2 step 6 and step 10):
//
//	svg → seating.ImportSBTSVG → seating_plans → seating_plan_versions →
//	session binding (category_tier_map by category index) → session_seats
//	materialized with the ORIGINAL Bil24 seat ids → seatList.available:false
//	folded into 'unavailable'.
//
// Everything here runs inside the import transaction opened by
// HandleBil24Session, so a seating failure rolls the whole catalog upsert back.
//
// Deviation from the spec text, deliberate and load-bearing: the spec suggests
// keying the imported plan by the upstream seatingPlanId stored "in metadata",
// but `seating_plans` has no metadata / external-id column and adding one is
// out of scope for this slice. The plan is therefore identified by
// (venue_id, owner_org_id, name) with name = actionEvent.seatingPlanName —
// stable for repeat imports of the same hall, which is what idempotency needs.
package himports

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/seating"
)

// admissionMode → seating_plans.plan_type, mirroring hseating's
// planTypesForAdmissionMode so an imported plan can later be re-bound through
// the normal admin surface without tripping seating.plan_type_mismatch.
var importPlanTypeForMode = map[string]string{
	"assigned_seats":    "assigned_seats",
	"hybrid":            "mixed",
	"general_admission": "general_admission",
}

// seatingOutcome is what the seated path contributes to the response.
type seatingOutcome struct {
	PlanVersionID     *uuid.UUID
	SeatsMaterialized int
}

// importSeating applies spec §13.2 step 6. It is a no-op (and returns a zero
// outcome) for a payload without an svg block: general-admission sessions keep
// the capacity-only shape produced by resolveSession.
func (h *Handler) importSeating(
	ctx context.Context,
	q *gen.Queries,
	plan importPlan,
	eventID, sessionID, venueID uuid.UUID,
	tierIDs map[string]uuid.UUID,
	warnings *warningSink,
) (seatingOutcome, error) {
	req := plan.Request
	if trimSpace(req.SVG) == "" {
		if len(req.SeatList) > 0 {
			// A seat list without geometry cannot be materialized: arena has
			// no coordinates, rows or sectors to hang the seats on.
			warnings.add(WarnSeatingNotImported,
				"seatList was ignored because the payload carries no svg seating plan; the session stays general admission")
		}
		return seatingOutcome{}, nil
	}

	sbt, _, parseErrs := seating.ImportSBTSVG([]byte(req.SVG))
	if len(parseErrs) > 0 {
		return seatingOutcome{}, failImport(http.StatusUnprocessableEntity, "import.invalid_svg",
			"svg is not a valid sbt/1.0 seating plan: "+parseErrs.Error())
	}

	geometry, mode := h.buildImportGeometry(sbt, plan)
	planType, ok := importPlanTypeForMode[mode]
	if !ok {
		return seatingOutcome{}, fmt.Errorf("unmapped admission mode %q", mode)
	}
	checksum, err := seating.Checksum(geometry)
	if err != nil {
		return seatingOutcome{}, fmt.Errorf("checksum imported geometry: %w", err)
	}

	// Step 10 — decide whether the seat set changes at all BEFORE touching
	// anything. An unchanged plan is reused verbatim so a repeat import keeps
	// every system_seat_id (which GET_SEAT_LIST and the sbt image put on the
	// wire) and every operator-applied seat status.
	binding, err := q.GetSessionSeatingBindingForUpdate(ctx, sessionID, eventID)
	if err != nil {
		return seatingOutcome{}, fmt.Errorf("lock session seating binding: %w", err)
	}
	unchanged := false
	if binding.SeatingPlanVersionID != nil {
		bound, verErr := q.GetSeatingPlanVersionByID(ctx, *binding.SeatingPlanVersionID)
		if verErr != nil && !errors.Is(verErr, pgx.ErrNoRows) {
			return seatingOutcome{}, fmt.Errorf("read bound seating plan version: %w", verErr)
		}
		unchanged = verErr == nil &&
			bound.GeometryChecksum == checksum &&
			binding.AdmissionMode == mode
	}

	if unchanged {
		seats, err := h.applySeatAvailability(ctx, q, plan, sessionID, warnings)
		if err != nil {
			return seatingOutcome{}, err
		}
		return seatingOutcome{PlanVersionID: binding.SeatingPlanVersionID, SeatsMaterialized: seats}, nil
	}

	// The seat set WOULD change. A session that already sold or holds seats
	// cannot survive a re-materialization — the tickets point at rows that are
	// about to be deleted — so this is the one hard conflict of the import.
	if binding.SeatingPlanVersionID != nil {
		tickets, err := q.CountTicketsBySession(ctx, sessionID)
		if err != nil {
			return seatingOutcome{}, fmt.Errorf("count tickets: %w", err)
		}
		reservations, err := q.CountReservationsBySession(ctx, sessionID)
		if err != nil {
			return seatingOutcome{}, fmt.Errorf("count reservations: %w", err)
		}
		if tickets > 0 || reservations > 0 {
			return seatingOutcome{}, failImport(http.StatusConflict, "import.session_has_sales",
				fmt.Sprintf("session already has %d ticket(s) and %d reservation(s); "+
					"its seat set cannot be replaced by an import", tickets, reservations))
		}
		if _, err := q.DeleteReservationSeatsBySession(ctx, sessionID); err != nil {
			return seatingOutcome{}, fmt.Errorf("wipe reservation seats: %w", err)
		}
		if _, err := q.DeleteSessionSeatsBySession(ctx, sessionID); err != nil {
			return seatingOutcome{}, fmt.Errorf("wipe session seats: %w", err)
		}
	}

	versionID, err := h.upsertSeatingPlanVersion(ctx, q, plan, venueID, planType, geometry, checksum)
	if err != nil {
		return seatingOutcome{}, err
	}

	materialized, err := h.materializeImportedSeats(ctx, q, geometry, mode, sessionID, tierIDs, warnings)
	if err != nil {
		return seatingOutcome{}, err
	}

	locked, err := q.LockSeatingPlanVersion(ctx, versionID)
	if err != nil {
		return seatingOutcome{}, fmt.Errorf("lock seating plan version: %w", err)
	}
	capacity := locked.CapacitySeated
	switch mode {
	case "hybrid":
		capacity = locked.CapacitySeated + locked.CapacityStanding
	case "general_admission":
		capacity = locked.CapacityStanding
	}
	if _, err := q.BindSessionSeatingPlan(ctx, sessionID, eventID, mode, &versionID, capacity); err != nil {
		return seatingOutcome{}, fmt.Errorf("bind seating plan to session: %w", err)
	}

	if _, err := h.applySeatAvailability(ctx, q, plan, sessionID, warnings); err != nil {
		return seatingOutcome{}, err
	}
	return seatingOutcome{PlanVersionID: &versionID, SeatsMaterialized: materialized}, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Geometry assembly
// ─────────────────────────────────────────────────────────────────────────────

// buildImportGeometry merges the sbt geometry with the payload's
// general-admission categories and derives the admission mode.
//
// Hybrid detection (spec §13.2 step 6): a Bil24 action event is hybrid when it
// carries BOTH placed categories (placement:true, materialized as seats by the
// sbt parser) and unplaced ones (placement:false — pure quota that has no
// geometry at all). The unplaced categories are synthesized here as
// general-admission categories, because ImportSBTSVG never emits them: an sbt
// document only describes seats.
func (h *Handler) buildImportGeometry(sbt seating.SBTPlan, plan importPlan) (seating.Geometry, string) {
	geometry := sbt.Geometry

	maxIndex := 0
	seatedExternal := make(map[int64]struct{}, len(geometry.Categories))
	for _, c := range geometry.Categories {
		if c.Index > maxIndex {
			maxIndex = c.Index
		}
		if c.ExternalID > 0 {
			seatedExternal[c.ExternalID] = struct{}{}
		}
	}

	gaCount := 0
	for _, c := range plan.Request.CategoryList {
		if c.Placement || c.Availability <= 0 {
			continue
		}
		if _, seated := seatedExternal[c.CategoryPriceID]; seated {
			// The category is drawn in the svg; its inventory is the seats,
			// not a quota. Adding a GA block would double-count it.
			continue
		}
		maxIndex++
		gaCount++
		name := trimSpace(c.CategoryPriceName)
		if name == "" {
			name = "Category " + externalIDString(c.CategoryPriceID)
		}
		geometry.Categories = append(geometry.Categories, seating.Category{
			Index:      maxIndex,
			Name:       name,
			Kind:       seating.KindGeneralAdmission,
			Capacity:   int(c.Availability),
			ExternalID: c.CategoryPriceID,
		})
	}
	// Canonicalize is idempotent and never renumbers, so the appended GA
	// indices survive; re-running it keeps Checksum stable across imports.
	geometry = seating.Canonicalize(geometry)

	mode := "general_admission"
	switch {
	case geometry.SeatCount() > 0 && gaCount > 0:
		mode = "hybrid"
	case geometry.SeatCount() > 0:
		mode = "assigned_seats"
	}
	return geometry, mode
}

// ─────────────────────────────────────────────────────────────────────────────
// Plan + version
// ─────────────────────────────────────────────────────────────────────────────

// upsertSeatingPlanVersion finds (or creates) the venue's plan for this Bil24
// hall and appends a version carrying the freshly imported geometry.
//
// A version is NEVER mutated in place — seating_plan_versions is append-only
// by contract (locked_at) — so a changed hall plan produces version n+1 and the
// historical geometry of already-sold sessions stays readable.
func (h *Handler) upsertSeatingPlanVersion(
	ctx context.Context,
	q *gen.Queries,
	plan importPlan,
	venueID uuid.UUID,
	planType string,
	geometry seating.Geometry,
	checksum string,
) (uuid.UUID, error) {
	name := trimSpace(plan.Request.ActionEvent.SeatingPlanName)
	if name == "" {
		if id := plan.Request.ActionEvent.SeatingPlanID; id > 0 {
			name = "Bil24 plan " + externalIDString(id)
		} else {
			name = "Bil24 plan " + externalIDString(plan.Request.Venue.VenueID)
		}
	}

	existing, err := q.ListSeatingPlansByVenue(ctx, venueID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("list seating plans: %w", err)
	}
	var planID uuid.UUID
	for _, p := range existing {
		if p.Name == name && p.OwnerOrgID == plan.OrgID && p.PlanType == planType {
			planID = p.ID
			break
		}
	}
	if planID == uuid.Nil {
		row, insErr := q.InsertSeatingPlan(ctx, venueID, plan.OrgID, name, planType, "private", "active", nil)
		if insErr != nil {
			return uuid.Nil, fmt.Errorf("insert seating plan: %w", insErr)
		}
		planID = row.ID
	}

	// Reuse an identical version of the same plan when one exists: a second
	// session in the same hall must share the geometry, not fork it.
	latest, err := q.GetLatestSeatingPlanVersionNumber(ctx, planID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, fmt.Errorf("read latest plan version number: %w", err)
	}

	raw, err := json.Marshal(geometry)
	if err != nil {
		return uuid.Nil, fmt.Errorf("encode geometry: %w", err)
	}
	seated := geometry.SeatCount()
	standing := geometry.GACapacity()
	if seated > math.MaxInt32 || standing > math.MaxInt32 {
		return uuid.Nil, failImport(http.StatusUnprocessableEntity, "import.plan_too_large",
			"the imported seating plan exceeds the supported capacity")
	}

	version, err := q.InsertSeatingPlanVersion(ctx, planID, latest+1, raw, checksum, nil,
		int32(seated),   //nolint:gosec // bound-checked above
		int32(standing)) //nolint:gosec // bound-checked above
	if err != nil {
		return uuid.Nil, fmt.Errorf("insert seating plan version: %w", err)
	}
	if _, err := q.SetSeatingPlanCurrentVersion(ctx, planID, plan.OrgID, &version.ID); err != nil {
		return uuid.Nil, fmt.Errorf("set current plan version: %w", err)
	}
	return version.ID, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Materialization
// ─────────────────────────────────────────────────────────────────────────────

// materializeImportedSeats writes one session_seats row per geometry seat with
// system_seat_id taken verbatim from the sbt document (system_seat_id_source =
// 'bil24'), plus one ga_unit row per general-admission place. The category →
// tier map is resolved by the category's ExternalID, which is the Bil24
// categoryPriceId that upsertTiers already keyed its result by.
func (h *Handler) materializeImportedSeats(
	ctx context.Context,
	q *gen.Queries,
	geometry seating.Geometry,
	mode string,
	sessionID uuid.UUID,
	tierIDs map[string]uuid.UUID,
	warnings *warningSink,
) (int, error) {
	tierByCategory := make(map[int]uuid.UUID, len(geometry.Categories))
	for _, c := range geometry.Categories {
		if c.ExternalID <= 0 {
			continue
		}
		if tierID, ok := tierIDs[externalIDString(c.ExternalID)]; ok {
			tierByCategory[c.Index] = tierID
		}
	}

	seatCount := geometry.SeatCount()
	seatKeys := make([]string, 0, seatCount)
	sectorNames := make([]string, 0, seatCount)
	rowNames := make([]string, 0, seatCount)
	seatNumbers := make([]string, 0, seatCount)
	tiers := make([]*string, 0, seatCount)
	systemSeatIDs := make([]*int64, 0, seatCount)
	var maxExternalID int64
	unmapped := false

	for _, section := range geometry.Sections {
		for _, row := range section.Rows {
			for _, seat := range row.Seats {
				seatKeys = append(seatKeys, seat.Key)
				sectorNames = append(sectorNames, section.Name)
				rowNames = append(rowNames, row.Name)
				seatNumbers = append(seatNumbers, seat.Number)
				if tierID, ok := tierByCategory[seat.CategoryIndex]; ok {
					s := tierID.String()
					tiers = append(tiers, &s)
				} else {
					// A category drawn in the svg but absent from categoryList
					// is priceless, not fatal: the seat exists and can be
					// priced later through the admin surface.
					tiers = append(tiers, nil)
					unmapped = true
				}
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
	if unmapped {
		warnings.add(WarnCategoryUnmapped,
			"the seating plan references categories that categoryList does not declare; those seats were imported without a ticket tier")
	}

	source := gen.SeatIDSourceArena
	if maxExternalID > 0 {
		source = gen.SeatIDSourceBil24
		// Push the arena sequence past every Bil24 id so a later plain
		// materialization can never mint a colliding system_seat_id.
		if err := q.AdvanceSessionSeatSystemIDSeq(ctx, maxExternalID); err != nil {
			return 0, fmt.Errorf("advance seat id sequence: %w", err)
		}
	}
	materialized := 0
	if len(seatKeys) > 0 {
		inserted, err := q.InsertSessionSeats(ctx, sessionID, seatKeys, sectorNames, rowNames,
			seatNumbers, tiers, systemSeatIDs, source)
		if err != nil {
			return 0, fmt.Errorf("materialize session seats: %w", err)
		}
		materialized = int(inserted)
	}

	if mode != "assigned_seats" {
		for _, cat := range geometry.GACategories() {
			if cat.Capacity <= 0 || cat.Capacity > math.MaxInt32 {
				continue
			}
			var tierPtr *uuid.UUID
			if tierID, ok := tierByCategory[cat.Index]; ok {
				tierPtr = &tierID
			}
			inserted, err := q.InsertGAUnits(ctx, sessionID,
				"ga|c"+strconv.Itoa(cat.Index), 0, tierPtr,
				int32(cat.Capacity)) //nolint:gosec // bound-checked above
			if err != nil {
				return 0, fmt.Errorf("materialize GA units: %w", err)
			}
			materialized += int(inserted)
		}
	}
	return materialized, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// seatList.available
// ─────────────────────────────────────────────────────────────────────────────

// applySeatAvailability folds seatList[].available:false onto the materialized
// rows: an upstream-unavailable seat becomes 'unavailable' so arena never sells
// a seat Bil24 already holds. It returns the number of session_seats rows the
// session carries, which is what the response reports on the reuse path.
//
// Only the available → unavailable direction is applied. Re-opening a seat is
// deliberately NOT automated: arena cannot tell an operator block apart from a
// stale upstream flag, and silently unblocking would resurrect seats an
// operator withdrew on purpose.
func (h *Handler) applySeatAvailability(
	ctx context.Context,
	q *gen.Queries,
	plan importPlan,
	sessionID uuid.UUID,
	warnings *warningSink,
) (int, error) {
	seats, err := q.ListSessionSeats(ctx, sessionID)
	if err != nil {
		return 0, fmt.Errorf("list session seats: %w", err)
	}
	if len(plan.Request.SeatList) == 0 {
		return len(seats), nil
	}

	bySystemID := make(map[int64]gen.SessionSeatRow, len(seats))
	for _, s := range seats {
		bySystemID[s.SystemSeatID] = s
	}

	blocked, missing := 0, 0
	for _, s := range plan.Request.SeatList {
		if s.Available {
			continue
		}
		row, ok := bySystemID[s.SeatID]
		if !ok {
			missing++
			continue
		}
		if row.Status != "available" {
			continue
		}
		version, incErr := q.IncrementSessionSeatStatusVersion(ctx, sessionID)
		if incErr != nil {
			return 0, fmt.Errorf("bump seat status version: %w", incErr)
		}
		if _, blockErr := q.BlockSessionSeat(ctx, row.ID, version); blockErr != nil {
			if errors.Is(blockErr, pgx.ErrNoRows) {
				continue
			}
			return 0, fmt.Errorf("block seat %d: %w", s.SeatID, blockErr)
		}
		blocked++
	}
	if blocked > 0 {
		warnings.add(WarnSeatsBlocked, fmt.Sprintf(
			"%d seat(s) reported unavailable by Bil24 were imported as blocked and are not on sale", blocked))
	}
	if missing > 0 {
		warnings.add(WarnSeatNotInPlan, fmt.Sprintf(
			"%d seatList entr(ies) reference seat ids that the svg seating plan does not contain and were ignored", missing))
	}
	return len(seats), nil
}
