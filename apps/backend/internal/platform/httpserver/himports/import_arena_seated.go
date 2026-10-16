// import_arena_seated.go — the seated branch of the arena event bundle
// (task S1b, plan docs/plans/event_center_unified_plan_2026-10-14_ru.md §8.3,
// event-bundle spec 08_architecture/19 §8.1).
//
// A bundle with actionEvent.arenaSeatingPlanVersionId creates its session from
// a seating plan version of the organization: every place of the plan is
// written on the session (one 'seat' row per geometry seat, the GA zones'
// places under 'ga|c<index>' keys) through seatmint.Materialize — the same
// step the seating bind and the Bil24-format import run — and the session is
// bound to the version (admission_mode assigned_seats, hybrid or
// general_admission, from the geometry).
//
// The starting layout is the plan's own categories (spec 26 §3.2): every
// categoryList entry prices exactly one plan category, and every plan
// category that owns places is priced by exactly one entry. An entry names
// its category by planCategoryIndex, or, without one, by the category's name
// (case- and space-insensitive; the plan's display-name override counts
// too). The matching is all-or-nothing: an entry without a pair, two entries
// for one category, or a category nobody prices is a 422 with the field's
// path, and nothing is written.
//
// On a session that ALREADY exists the bundle never re-binds the plan and
// never touches a place: arenaSeatingPlanVersionId must then be absent or the
// version the session already has (409 import.seating_plan_version_conflict
// otherwise). Its categories are matched like any other edit (arenaTierId,
// categoryPriceId, name) and change price, sale window and schedule; the
// quota rules of syncImportedCategoryQuotas apply to the GA parts only — a
// seated category's places are the plan's seats.
package himports

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/bil24compat"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/seating"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/gaquota"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/seatmint"
)

// seatingVersionField is the JSON pointer of actionEvent.arenaSeatingPlanVersionId.
const seatingVersionField = "/actionEvent/arenaSeatingPlanVersionId"

// arenaSeatedPlan is the seating plan version a new session is created from,
// with the categoryList matched onto its categories.
type arenaSeatedPlan struct {
	versionID uuid.UUID
	version   gen.SeatingPlanVersionRow
	// venueID is the venue the plan is drawn for.
	venueID  uuid.UUID
	geometry seating.Geometry
	// names are the plan's display-name overrides by category index.
	names map[int]string
	mode  string
	// pairs[i] is the plan category index categoryList[i] prices; filled by
	// matchPlan.
	pairs []int
}

// lookupArenaSeatingPlan resolves actionEvent.arenaSeatingPlanVersionId,
// reading only; nil without one. A version of another organization's plan
// reads exactly like an unknown one.
func lookupArenaSeatingPlan(ctx context.Context, q *gen.Queries, plan importPlan) (*arenaSeatedPlan, error) {
	raw := trimSpace(plan.Request.ActionEvent.ArenaSeatingPlanVersionID)
	if raw == "" {
		return nil, nil
	}
	versionID, err := uuid.Parse(raw)
	if err != nil {
		return nil, failImportAt(http.StatusUnprocessableEntity, "import.invalid_seating_plan_version", seatingVersionField,
			"actionEvent.arenaSeatingPlanVersionId "+raw+" is not a UUID")
	}
	invalid := failImportAt(http.StatusUnprocessableEntity, "import.invalid_seating_plan_version", seatingVersionField,
		"actionEvent.arenaSeatingPlanVersionId "+raw+" is not a seating plan version of this organization")
	version, err := q.GetSeatingPlanVersionByID(ctx, versionID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, invalid
	case err != nil:
		return nil, fmt.Errorf("read seating plan version: %w", err)
	}
	parent, err := q.GetSeatingPlanByID(ctx, version.SeatingPlanID)
	switch {
	case errors.Is(err, pgx.ErrNoRows), err == nil && parent.OwnerOrgID != plan.OrgID:
		return nil, invalid
	case err != nil:
		return nil, fmt.Errorf("read seating plan: %w", err)
	}
	var geometry seating.Geometry
	if err := json.Unmarshal(version.Geometry, &geometry); err != nil {
		return nil, fmt.Errorf("decode seating plan geometry: %w", err)
	}
	geometry = seating.Canonicalize(geometry)
	return &arenaSeatedPlan{
		versionID: versionID,
		version:   version,
		venueID:   parent.VenueID,
		geometry:  geometry,
		names:     planNameOverrides(parent.CategoryNameOverrides),
		mode:      seatmint.AdmissionMode(geometry),
	}, nil
}

// planNameOverrides decodes seating_plans.category_name_overrides ("1" →
// name). A malformed value is no override at all.
func planNameOverrides(raw json.RawMessage) map[int]string {
	out := map[int]string{}
	if len(raw) == 0 {
		return out
	}
	var byKey map[string]string
	if err := json.Unmarshal(raw, &byKey); err != nil {
		return out
	}
	for k, v := range byKey {
		if idx, err := strconv.Atoi(k); err == nil && trimSpace(v) != "" {
			out[idx] = v
		}
	}
	return out
}

// checkVenue refuses a plan drawn for another venue than the session's.
func (sp *arenaSeatedPlan) checkVenue(venueID uuid.UUID) error {
	if sp.venueID == venueID {
		return nil
	}
	return failImportAt(http.StatusUnprocessableEntity, "import.seating_plan_venue_mismatch", seatingVersionField,
		"the seating plan version is drawn for another venue than the session's; pick that venue with venue.arenaVenueId")
}

// matchPlan matches the categoryList onto the plan's categories and returns
// every problem found, in categoryList order (a dry run reports them all, an
// import the first). On success sp.pairs is filled.
func (sp *arenaSeatedPlan) matchPlan(list []bil24compat.ImportSessionCategory) []error {
	pairs, errs := planCategoryPairs(list, sp.geometry, sp.names)
	if len(errs) == 0 {
		sp.pairs = pairs
	}
	return errs
}

// planCategoryPairs is the matching rule itself — a pure function, unit
// tested without a database. pairs[i] is the plan category index entry i
// prices.
func planCategoryPairs(list []bil24compat.ImportSessionCategory, g seating.Geometry, names map[int]string) ([]int, []error) {
	placed := seatmint.PlacedCategories(g)
	byName := map[string][]int{}
	addName := func(name string, idx int) {
		key := normalizeTierName(name)
		if key == "" {
			return
		}
		for _, have := range byName[key] {
			if have == idx {
				return
			}
		}
		byName[key] = append(byName[key], idx)
	}
	labels := map[int]string{}
	for _, c := range g.Categories {
		if !placed[c.Index] {
			continue
		}
		addName(c.Name, c.Index)
		labels[c.Index] = c.Name
		if n, ok := names[c.Index]; ok {
			addName(n, c.Index)
			labels[c.Index] = n
		}
	}

	var errs []error
	pairs := make([]int, len(list))
	usedBy := map[int]int{}
	for i, c := range list {
		field := fmt.Sprintf("/categoryList/%d/categoryPriceName", i)
		idx := 0
		if c.PlanCategoryIndex != nil {
			field = fmt.Sprintf("/categoryList/%d/planCategoryIndex", i)
			idx = *c.PlanCategoryIndex
			if !placed[idx] {
				errs = append(errs, failImportAt(http.StatusUnprocessableEntity, "import.seating_category_unmatched", field,
					fmt.Sprintf("categoryList[%d].planCategoryIndex %d is not a category of the seating plan that has places", i, idx)))
				continue
			}
		} else {
			name := trimSpace(c.CategoryPriceName)
			candidates := byName[normalizeTierName(name)]
			switch {
			case name == "":
				errs = append(errs, failImportAt(http.StatusUnprocessableEntity, "import.seating_category_unmatched", field,
					fmt.Sprintf("categoryList[%d] names no seating plan category: send planCategoryIndex or the name of a plan category", i)))
				continue
			case len(candidates) == 0:
				errs = append(errs, failImportAt(http.StatusUnprocessableEntity, "import.seating_category_unmatched", field,
					fmt.Sprintf("categoryList[%d] %q is not the name of a seating plan category: send planCategoryIndex or the plan category's name", i, name)))
				continue
			case len(candidates) > 1:
				errs = append(errs, failImportAt(http.StatusUnprocessableEntity, "import.seating_category_unmatched", field,
					fmt.Sprintf("categoryList[%d] %q names several seating plan categories: send planCategoryIndex", i, name)))
				continue
			}
			idx = candidates[0]
		}
		if j, taken := usedBy[idx]; taken {
			errs = append(errs, failImportAt(http.StatusUnprocessableEntity, "import.seating_category_duplicate", field,
				fmt.Sprintf("categoryList[%d] prices seating plan category %d, which categoryList[%d] already prices", i, idx, j)))
			continue
		}
		usedBy[idx] = i
		pairs[i] = idx
	}

	var missing []int
	for idx := range placed {
		if _, ok := usedBy[idx]; !ok {
			missing = append(missing, idx)
		}
	}
	if len(missing) > 0 && len(errs) == 0 {
		sort.Ints(missing)
		parts := make([]string, 0, len(missing))
		for _, idx := range missing {
			parts = append(parts, fmt.Sprintf("%d (%s)", idx, labels[idx]))
		}
		errs = append(errs, failImportAt(http.StatusUnprocessableEntity, "import.seating_category_missing", "/categoryList",
			"every seating plan category with places needs a price in categoryList; missing: "+strings.Join(parts, ", ")))
	}
	return pairs, errs
}

// seatedEntries reports which categoryList entries price a SEATED plan
// category (their places are the plan's seats, so their tier carries no
// quantity of its own).
func (sp *arenaSeatedPlan) seatedEntries() map[int]bool {
	if sp == nil {
		return nil
	}
	ga := map[int]bool{}
	for _, c := range sp.geometry.GACategories() {
		ga[c.Index] = true
	}
	out := make(map[int]bool, len(sp.pairs))
	for i, idx := range sp.pairs {
		out[i] = !ga[idx]
	}
	return out
}

// resolveArenaSeating decides the seated branch before anything is written.
// For a session that already exists it only checks that the bundle does not
// ask for another plan; for a new one it resolves the version, checks its
// venue and matches the categories. nil means "no plan to bind".
func resolveArenaSeating(ctx context.Context, q *gen.Queries, plan importPlan, m arenaMatch, venueID uuid.UUID) (*arenaSeatedPlan, error) {
	if m.SessionID != uuid.Nil {
		return nil, checkArenaSeatingKept(ctx, q, plan, m)
	}
	sp, err := lookupArenaSeatingPlan(ctx, q, plan)
	if err != nil || sp == nil {
		return nil, err
	}
	if err := sp.checkVenue(venueID); err != nil {
		return nil, err
	}
	if errs := sp.matchPlan(plan.Request.CategoryList); len(errs) > 0 {
		return nil, errs[0]
	}
	return sp, nil
}

// checkArenaSeatingKept refuses a bundle that names a seating plan version
// other than the one the existing session is bound to (or names one for a
// session that has none). Absent keeps whatever the session has.
func checkArenaSeatingKept(ctx context.Context, q *gen.Queries, plan importPlan, m arenaMatch) error {
	raw := trimSpace(plan.Request.ActionEvent.ArenaSeatingPlanVersionID)
	if raw == "" || m.SessionID == uuid.Nil {
		return nil
	}
	versionID, err := uuid.Parse(raw)
	if err != nil {
		return failImportAt(http.StatusUnprocessableEntity, "import.invalid_seating_plan_version", seatingVersionField,
			"actionEvent.arenaSeatingPlanVersionId "+raw+" is not a UUID")
	}
	binding, err := q.GetSessionSeatingBinding(ctx, m.SessionID, m.Ctx.EventID)
	if err != nil {
		return fmt.Errorf("read session seating binding: %w", err)
	}
	if binding.SeatingPlanVersionID != nil && *binding.SeatingPlanVersionID == versionID {
		return nil
	}
	return failImportAt(http.StatusConflict, "import.seating_plan_version_conflict", seatingVersionField,
		"the session is bound to another seating plan version (or to none); an event bundle never re-binds "+
			"a plan or touches places: omit actionEvent.arenaSeatingPlanVersionId to keep the session's plan")
}

// materializeArenaSeating writes the plan's places on the NEW session and
// binds it to the version. Every place belongs to the tier of its plan
// category; the GA zones' quantities are recorded on their tiers.
func materializeArenaSeating(
	ctx context.Context,
	q *gen.Queries,
	eventID, sessionID uuid.UUID,
	sp *arenaSeatedPlan,
	ordered []uuid.UUID,
) error {
	// Lock order: the sessions row first (AGENTS.md). The session is new and
	// invisible to anyone else, but the rule costs nothing to keep.
	if _, err := q.GetSessionSeatingBindingForUpdate(ctx, sessionID, eventID); err != nil {
		return fmt.Errorf("lock new session: %w", err)
	}
	tiers := make(map[int]uuid.UUID, len(sp.pairs))
	for i, idx := range sp.pairs {
		tiers[idx] = ordered[i]
	}
	// Lenient: a GA zone drawn with no capacity gets no places here; its
	// category is then given the bundle's availability like any GA one.
	if _, err := seatmint.Materialize(ctx, q, sessionID, sp.geometry, tiers,
		sp.mode != seatmint.ModeAssignedSeats, seatmint.Lenient); err != nil {
		return err
	}
	locked, err := q.LockSeatingPlanVersion(ctx, sp.versionID)
	if err != nil {
		return fmt.Errorf("lock seating plan version: %w", err)
	}
	versionID := sp.versionID
	if _, err := q.BindSessionSeatingPlan(ctx, sessionID, eventID, sp.mode, &versionID,
		seatmint.PlanCapacity(sp.mode, locked)); err != nil {
		return fmt.Errorf("bind seating plan to session: %w", err)
	}
	if err := gaquota.Recompute(ctx, q, sessionID); err != nil {
		return fmt.Errorf("recompute session capacity: %w", err)
	}
	return nil
}

// refuseSeatedChainLinks refuses a nextCategoryIndex link that starts or
// ends at a seated category: a chain hands free GA places over
// (gaquota.HandOver), and a seated category's places are plan seats.
func refuseSeatedChainLinks(ctx context.Context, q *gen.Queries, plan importPlan, sessionID uuid.UUID, ordered []uuid.UUID) error {
	list := plan.Request.CategoryList
	for i, c := range list {
		if c.NextCategoryIndex == nil {
			continue
		}
		j := *c.NextCategoryIndex
		if j < 0 || j >= len(list) || i >= len(ordered) || j >= len(ordered) {
			continue
		}
		for _, k := range []int{i, j} {
			kind, err := gaquota.CategoryKind(ctx, q, sessionID, ordered[k])
			switch {
			case errors.Is(err, gaquota.ErrCategoryNotFound):
				continue
			case err != nil:
				return fmt.Errorf("classify chained category: %w", err)
			}
			if kind == gaquota.KindSeated {
				return failImportAt(http.StatusUnprocessableEntity, "import.seated_category_chain",
					fmt.Sprintf("/categoryList/%d/nextCategoryIndex", i),
					fmt.Sprintf("categoryList[%d] links a seated category; only general-admission categories "+
						"can hand their places on to a next category", i))
			}
		}
	}
	return nil
}

// arenaSeatingOutcome is what the response reports about the session's plan:
// the bound version and the places the session carries, or nothing for a
// general-admission session.
func arenaSeatingOutcome(ctx context.Context, q *gen.Queries, eventID, sessionID uuid.UUID) (seatingOutcome, error) {
	binding, err := q.GetSessionSeatingBinding(ctx, sessionID, eventID)
	if err != nil {
		return seatingOutcome{}, fmt.Errorf("read session seating binding: %w", err)
	}
	if binding.SeatingPlanVersionID == nil {
		return seatingOutcome{}, nil
	}
	places, err := q.CountSessionPlaces(ctx, sessionID)
	if err != nil {
		return seatingOutcome{}, fmt.Errorf("count session places: %w", err)
	}
	return seatingOutcome{PlanVersionID: binding.SeatingPlanVersionID, SeatsMaterialized: int(places)}, nil
}
